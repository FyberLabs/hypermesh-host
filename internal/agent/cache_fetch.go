package agent

import (
	"context"
	"fmt"
	"github.com/FyberLabs/hypermesh-host/internal/pull"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

func (a *Agent) modelFetcher() (pull.Fetcher, error) {
	p, err := a.cachePolicy()
	if err != nil {
		return nil, err
	}
	token := ""
	if p != nil && p.TokenFile != "" {
		f, err := os.OpenFile(p.TokenFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, fmt.Errorf("cannot read private model credential")
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("model credential must be a private regular file")
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Geteuid()) {
			return nil, fmt.Errorf("model credential owner differs")
		}
		raw, err := io.ReadAll(io.LimitReader(f, 513))
		if err != nil || len(raw) > 512 {
			return nil, fmt.Errorf("invalid model credential")
		}
		token = strings.TrimSpace(string(raw))
		if len(token) < 8 || strings.ContainsAny(token, "\r\n\t ") {
			return nil, fmt.Errorf("invalid model credential")
		}
	}
	client := &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		host := strings.ToLower(req.URL.Hostname())
		if len(via) > 8 || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Port() != "" || !(host == "huggingface.co" || host == "hf.co" || strings.HasSuffix(host, ".hf.co") || strings.HasSuffix(host, ".huggingface.co")) {
			return fmt.Errorf("untrusted model redirect")
		}
		req.Header.Del("Authorization")
		if host == "huggingface.co" && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return nil
	}}
	return func(ctx context.Context, address string) (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
		if err != nil {
			return nil, fmt.Errorf("invalid model source")
		}
		if token != "" && req.URL.Hostname() == "huggingface.co" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("model transfer failed")
		}
		if response.StatusCode != 200 {
			response.Body.Close()
			return nil, fmt.Errorf("model transfer denied; check source access")
		}
		return response.Body, nil
	}, nil
}
