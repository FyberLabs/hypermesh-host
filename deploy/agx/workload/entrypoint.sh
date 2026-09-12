#!/bin/sh
# Start the local completions listener. Fail honestly if the GGUF is missing.
set -eu
MODEL=/models/model.gguf
if [ ! -s "$MODEL" ]; then
	echo "No GGUF at $MODEL. Volume-mount the host weights (forgot -v ...:/models/model.gguf:ro). This image does not bake or invent a model." >&2
	exit 1
fi
exec llama-server -m "$MODEL" --host 0.0.0.0 --port 8080 --alias hypermesh-test
