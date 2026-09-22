// Package catalog holds in-tree product 2 catalog ids.
//
// A catalog row is not certified until a soak hash exists. Do not advertise
// tokens/sec for any id here.
package catalog

// Llama318BQ4 is the first in-tree catalog id (Llama 3.1 8B Instruct Q4).
// Uncertified until a soak hash exists.
const Llama318BQ4 = "llama-3.1-8b-q4"

// Pack ids the scorecard can time. None are certified here. No tok/s.
const (
	WhisperSmall = "whisper-small"
	EmbedMiniLM  = "embed-minilm"
	Qwen2532BQ4  = "qwen-2.5-32b-q4"
	Llama3170BQ4 = "llama-3.1-70b-q4"
)
