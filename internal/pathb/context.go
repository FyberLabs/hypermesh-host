package pathb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// ContextFit records a single full-window exercise, not sustained throughput,
// GPU residency or a certification verdict. Prompt/output content is discarded.
type ContextFit struct {
	Protocol        string `json:"protocol"`
	ContextTokens   int    `json:"context_tokens"`
	PromptTokens    int    `json:"prompt_tokens"`
	GeneratedTokens int    `json:"generated_tokens"`
	ElapsedMS       int64  `json:"elapsed_ms"`
}

const ContextFitProtocol = "llama-native-context-v1"
const contextDecodeTokens = 16

func (f ContextFit) ValidFor(tokens int) bool {
	return f.Protocol == ContextFitProtocol && tokens >= 512 && tokens <= 131072 &&
		f.ContextTokens == tokens && f.PromptTokens == tokens-contextDecodeTokens &&
		f.GeneratedTokens == contextDecodeTokens && f.ElapsedMS > 0
}

// CheckContextFit uses llama.cpp's native token-array completion so chat
// templates/BOS insertion cannot change the requested prompt length.
// The native response model is a file path, not the OpenAI alias. Model
// identity is bound by the caller's pinned container and verified GGUF hash.
func (r Runner) CheckContextFit(ctx context.Context, tokens int) (*ContextFit, error) {
	fail := func() (*ContextFit, error) { return nil, fmt.Errorf("full-context evidence unavailable or mismatched") }
	if tokens < 512 || tokens > 131072 || r.Model == "" {
		return fail()
	}
	base, err := url.Parse(r.endpoint())
	if err != nil || base.Scheme != "http" || base.User != nil || base.RawQuery != "" ||
		base.Fragment != "" || net.ParseIP(base.Hostname()) == nil {
		return fail()
	}
	ip := net.ParseIP(base.Hostname())
	verifiedBridge := r.VerifiedContainerEndpoint != "" &&
		r.endpoint() == r.VerifiedContainerEndpoint && ip.To4() != nil && ip.IsPrivate()
	if !ip.IsLoopback() && !verifiedBridge {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	client := *r.client()
	client.Timeout = 2 * time.Minute
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request := func(method, path string, body any, out any) error {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		u := *base
		u.Path = path
		u.RawPath = ""
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("context probe transport failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("context probe rejected")
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		if err != nil || len(data) > 4<<20 {
			return fmt.Errorf("context probe response incomplete")
		}
		if json.Unmarshal(data, out) != nil {
			return fmt.Errorf("context probe response invalid")
		}
		return nil
	}
	post := func(path string, body any, out any) error { return request(http.MethodPost, path, body, out) }
	var props struct {
		Defaults struct {
			Context *int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		Slots *int `json:"total_slots"`
	}
	if err := request(http.MethodGet, "/props", nil, &props); err != nil {
		return nil, fmt.Errorf("context metadata unavailable: %w", err)
	}
	if props.Defaults.Context == nil || *props.Defaults.Context != tokens || props.Slots == nil || *props.Slots != 1 {
		return nil, fmt.Errorf("context metadata must report one slot with %d tokens", tokens)
	}
	var seed struct {
		Tokens []int `json:"tokens"`
	}
	if post("/tokenize", map[string]any{"content": "A simple model context check.", "add_special": false, "parse_special": false}, &seed) != nil ||
		len(seed.Tokens) == 0 || len(seed.Tokens) > 64 {
		return fail()
	}
	for _, id := range seed.Tokens {
		if id < 0 || id > 1<<30 {
			return fail()
		}
	}
	prompt := make([]int, tokens-contextDecodeTokens)
	for i := range prompt {
		prompt[i] = seed.Tokens[i%len(seed.Tokens)]
	}
	var response struct {
		Evaluated *int   `json:"tokens_evaluated"`
		Predicted *int   `json:"tokens_predicted"`
		Truncated *bool  `json:"truncated"`
		Stopped   *bool  `json:"stop"`
		StopType  string `json:"stop_type"`
		Settings  struct {
			Context *int `json:"n_ctx"`
		} `json:"generation_settings"`
	}
	started := time.Now()
	err = post("/completion", map[string]any{
		"model": r.Model, "prompt": prompt, "n_predict": contextDecodeTokens,
		"stream": false, "cache_prompt": false, "ignore_eos": true, "temperature": 0,
		"stop": []string{}, "id_slot": 0,
	}, &response)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("context probe deadline exceeded")
	}
	if response.Evaluated == nil ||
		response.Predicted == nil || response.Truncated == nil ||
		response.Stopped == nil || !*response.Stopped || response.StopType != "limit" ||
		(response.Settings.Context != nil && *response.Settings.Context != tokens) {
		return fail()
	}
	// llama.cpp 7fe450e sets truncated at the no-context-shift boundary,
	// even when all requested tokens were evaluated/generated. Accept only
	// exact counts at the independently verified one-slot capacity.
	f := &ContextFit{ContextFitProtocol, tokens, *response.Evaluated, *response.Predicted, time.Since(started).Milliseconds()}
	if !f.ValidFor(tokens) {
		return nil, fmt.Errorf("context token counts differ: prompt=%d generated=%d expected=%d+%d", f.PromptTokens, f.GeneratedTokens, tokens-contextDecodeTokens, contextDecodeTokens)
	}
	return f, nil
}
