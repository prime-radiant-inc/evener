package main

import (
	"encoding/json"
	"strings"

	"primeradiant.com/serf/llm"
)

// harvestSSE walks api-raw.jsonl files and emits each streaming response body as
// a seed for FuzzParseSSE (provider-agnostic) plus the matching provider
// metamorphic decoder, routed by the recorded provider and body shape.
func harvestSSE(r *runner, san *Sanitizer, paths []string) {
	st := r.stat("sse")
	for _, path := range paths {
		_ = forEachJSONLine(path, func(line []byte) { //nolint:errcheck // best-effort per file
			var entry llm.APIRawLogEntry
			if json.Unmarshal(line, &entry) != nil {
				return
			}
			if entry.Mode != "stream" || entry.ResponseBody == "" {
				return
			}
			st.scanned++
			body := []byte(entry.ResponseBody)
			sse := looksLikeSSE(body)
			out, ok := r.scrub(st, san, body, sse)
			if !ok {
				return
			}
			dirs := append([]string{r.dir(dirParseSSE)}, providerTargetDirs(r, entry.Provider, body)...)
			r.emitBytesTo(st, out, dirs...)
		})
	}
}

// looksLikeSSE reports whether a recorded body is an SSE stream (vs a JSON error
// body), so the scrubber picks framing-aware vs plain-JSON scrubbing.
func looksLikeSSE(body []byte) bool {
	s := string(body)
	return strings.Contains(s, "data:") || strings.Contains(s, "event:")
}

// providerTargetDirs maps a recorded provider (and, for OpenAI, the stream shape)
// to its metamorphic decoder's testdata dir. An unrecognized provider seeds only
// the provider-agnostic FuzzParseSSE (handled by the caller).
func providerTargetDirs(r *runner, provider string, body []byte) []string {
	switch provider {
	case "anthropic":
		return []string{r.dir(dirAnthropicStream)}
	case "google":
		return []string{r.dir(dirGeminiStream)}
	case "openai-compatible":
		return []string{r.dir(dirOpenAICompatStream)}
	case "openai":
		switch {
		case isResponsesStream(body):
			return []string{r.dir(dirOpenAIResponses)}
		case isChatCompletionsStream(body):
			return []string{r.dir(dirOpenAIChatComplete)}
		}
	}
	return nil
}

// isResponsesStream detects the OpenAI Responses API event shape.
func isResponsesStream(body []byte) bool {
	return strings.Contains(string(body), `"type":"response.`)
}

// isChatCompletionsStream detects the OpenAI Chat Completions chunk shape.
func isChatCompletionsStream(body []byte) bool {
	s := string(body)
	return strings.Contains(s, "chat.completion") || strings.Contains(s, `"choices"`)
}
