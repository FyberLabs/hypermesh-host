package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStartArgsLlamaServerToolCalling(t *testing.T) {
	const catalog = "llama-3.1-8b-q4"
	args := StartArgs(Spec{LeaseID: "lease-9", CatalogID: catalog}, testRef)
	assertLlamaServerArgs(t, args, catalog, "1", "16384", "1800")
	imageBeforeCommand(t, args, testRef)
	assertWorkloadJail(t, args)
}

func TestStartArgsLlamaServerOverrides(t *testing.T) {
	const catalog = "qwen-2.5-32b-q4"
	args := StartArgs(Spec{
		LeaseID:      "lease-9",
		CatalogID:    catalog,
		LlamaJinja:   "1",
		LlamaCtxSize: "32768",
		LlamaTimeout: "900",
	}, testRef)
	assertLlamaServerArgs(t, args, catalog, "1", "32768", "900")
}

func TestStartArgsLlamaServerBlankOverridesUseDefaults(t *testing.T) {
	args := StartArgs(Spec{
		LeaseID:      "lease-9",
		CatalogID:    "llama-3.1-8b-q4",
		LlamaJinja:   "  ",
		LlamaCtxSize: "",
		LlamaTimeout: "\t",
	}, testRef)
	assertLlamaServerArgs(t, args, "llama-3.1-8b-q4", "1", "16384", "1800")
}

func TestStartRequiresCatalogAlias(t *testing.T) {
	n := &NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("docker must not run without a catalog id")
		return nil, nil
	}}
	for _, catalog := range []string{"", "  ", "llama\n3"} {
		_, err := n.Start(context.Background(), Spec{
			LeaseID:     "lease-1",
			CatalogID:   catalog,
			ImageDigest: testRef,
			Runtime:     "nvidia",
		})
		if !errors.Is(err, ErrNoCatalogID) {
			t.Fatalf("catalog %q: got %v", catalog, err)
		}
	}
}

func assertLlamaServerArgs(t *testing.T, args []string, catalog, jinja, ctxSize, timeout string) {
	t.Helper()
	if got := dockerEnvValue(args, "LLAMA_ARG_JINJA"); got != jinja {
		t.Fatalf("LLAMA_ARG_JINJA=%q want %q in %v", got, jinja, args)
	}
	if got := dockerEnvValue(args, "LLAMA_ARG_CTX_SIZE"); got != ctxSize {
		t.Fatalf("LLAMA_ARG_CTX_SIZE=%q want %q in %v", got, ctxSize, args)
	}
	if got := dockerEnvValue(args, "LLAMA_ARG_TIMEOUT"); got != timeout {
		t.Fatalf("LLAMA_ARG_TIMEOUT=%q want %q in %v", got, timeout, args)
	}
	if got := dockerEnvValue(args, "LLAMA_ARG_ALIAS"); got != "" {
		t.Fatalf("LLAMA_ARG_ALIAS cannot override the image --alias, got %q", got)
	}
	if got := dockerEnvValue(args, ServeAliasEnv); got != catalog {
		t.Fatalf("alias env %q want catalog %q in %v", got, catalog, args)
	}
	if flagValue(args, "--entrypoint") != "/bin/sh" {
		t.Fatalf("entrypoint override: %v", args)
	}
	script := args[len(args)-1]
	if script != llamaServerCommand {
		t.Fatalf("llama-server command: %q", script)
	}
	if !strings.Contains(script, `! -s "$MODEL"`) || !strings.Contains(script, "/models/model.gguf") {
		t.Fatalf("command must fail honestly when the GGUF is missing or empty: %s", script)
	}
	if !strings.Contains(script, `exec llama-server`) || !strings.Contains(script, `--alias "$serve_alias"`) {
		t.Fatalf("command must exec llama-server with catalog alias: %s", script)
	}
	if !strings.Contains(script, "${"+ServeAliasEnv+":-}") {
		t.Fatalf("command must read %s for --alias: %s", ServeAliasEnv, script)
	}
	if strings.Contains(script, "hypermesh-test") {
		t.Fatalf("alias must not be hypermesh-test: %s", script)
	}
	if strings.Contains(script, "--jinja") || strings.Contains(script, "--ctx-size") || strings.Contains(script, "--timeout") {
		t.Fatalf("jinja/ctx/timeout stay LLAMA_ARG_* env (CLI would override them): %s", script)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--alias hypermesh-test") || strings.Contains(joined, "--alias=hypermesh-test") {
		t.Fatalf("hardcoded image alias leaked into docker args: %v", args)
	}
}

func dockerEnvValue(args []string, key string) string {
	prefix := key + "="
	for i := 0; i < len(args)-1; i++ {
		if (args[i] == "-e" || args[i] == "--env") && strings.HasPrefix(args[i+1], prefix) {
			return strings.TrimPrefix(args[i+1], prefix)
		}
	}
	return ""
}
