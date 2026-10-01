package runtime

import (
	"errors"
	"strings"
)

const (
	// DefaultLlamaJinja enables jinja chat templates (OpenAI tool calling).
	// The GGUF's embedded Meta Llama 3.1 template supports tools.
	DefaultLlamaJinja = "1"
	// DefaultLlamaCtxSize is the llama-server context length.
	DefaultLlamaCtxSize = "16384"
	// DefaultLlamaTimeout is the llama-server read/write timeout in seconds.
	// The upstream default (600) is too short at about 8.5 tok/s.
	DefaultLlamaTimeout = "1800"
	// ServeAliasEnv is the container env the replacement command reads for
	// llama-server --alias. It is not LLAMA_ARG_ALIAS: the image entrypoint
	// passes --alias on the CLI, and llama.cpp CLI flags beat LLAMA_ARG_*.
	ServeAliasEnv = "HYPERMESH_SERVE_ALIAS"
)

// ErrNoCatalogID means lease_start has no catalog id to use as the
// served model alias. Not hypermesh-test.
var ErrNoCatalogID = errors.New("hypermesh: lease catalog_id is required for llama-server --alias")

// llamaServerCommand replaces the image ENTRYPOINT
// (/usr/local/bin/entrypoint.sh). That script checks /models/model.gguf
// and then execs llama-server with a hardcoded --alias hypermesh-test.
// CLI flags win over LLAMA_ARG_*, so an env alias cannot fix it.
// This command keeps the missing-or-empty GGUF failure and passes
// --alias from ServeAliasEnv (the lease catalog id). Jinja, context
// size, and timeout stay LLAMA_ARG_* env vars: neither the image
// entrypoint nor this command passes those flags. Works with the
// current image and a later image that keeps the same contract
// (llama-server on PATH, weights at /models/model.gguf). No rebuild.
const llamaServerCommand = `set -eu
MODEL=/models/model.gguf
if [ ! -s "$MODEL" ]; then
	echo "No GGUF at $MODEL. Volume-mount the host weights (forgot -v ...:/models/model.gguf:ro). This image does not bake or invent a model." >&2
	exit 1
fi
serve_alias=${HYPERMESH_SERVE_ALIAS:-}
if [ -z "$serve_alias" ]; then
	echo "No catalog model id for llama-server --alias." >&2
	exit 1
fi
exec llama-server -m "$MODEL" --host 0.0.0.0 --port 8080 --alias "$serve_alias"
`

func requireServeAlias(catalogID string) (string, error) {
	id := strings.TrimSpace(catalogID)
	if id == "" || strings.ContainsAny(id, "\r\n\x00") {
		return "", ErrNoCatalogID
	}
	return id, nil
}

func llamaOrDefault(v, def string) string {
	if s := strings.TrimSpace(v); s != "" {
		return s
	}
	return def
}

// llamaServerDockerOpts are docker run options for the lease container's
// llama-server: tool-calling env, and an entrypoint override so --alias
// is the catalog id. The image ref and `-c` command follow these.
func llamaServerDockerOpts(spec Spec) []string {
	alias := strings.TrimSpace(spec.CatalogID)
	args := []string{
		"-e", "LLAMA_ARG_JINJA=" + llamaOrDefault(spec.LlamaJinja, DefaultLlamaJinja),
		"-e", "LLAMA_ARG_CTX_SIZE=" + llamaOrDefault(spec.LlamaCtxSize, DefaultLlamaCtxSize),
		"-e", "LLAMA_ARG_TIMEOUT=" + llamaOrDefault(spec.LlamaTimeout, DefaultLlamaTimeout),
		"-e", ServeAliasEnv + "=" + alias,
		"--entrypoint", "/bin/sh",
	}
	if spec.SingleSlot { args=append(args,"-e","LLAMA_ARG_N_PARALLEL=1","-e","LLAMA_ARG_CONTEXT_SHIFT=0") }
	return args
}
