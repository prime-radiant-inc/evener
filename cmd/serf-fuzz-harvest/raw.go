package main

import (
	"bytes"
	"encoding/json"
	"strings"

	"primeradiant.com/serf/llm"
)

// harvestSSE walks api-raw.jsonl files and emits each streaming response body as
// seeds for FuzzParseSSE (provider-agnostic) plus the matching provider
// metamorphic decoder, routed by the recorded provider and body shape.
//
// Real provider streams for an agent turn are large (commonly 150–250 KB), far
// over the seed-size cap, so a whole-stream seed would always be dropped as
// oversized. Instead each stream is split into bounded windows of whole SSE
// events (sseSeedWindows): lean seeds that still carry consecutive events, so
// the multi-event accumulation logic — content blocks, tool-call deltas, finish
// reasons, usage — where the real decoder bugs live is still exercised.
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
			dirs := append([]string{r.dir(dirParseSSE)}, providerTargetDirs(r, entry.Provider, body)...)
			for _, win := range sseSeedWindows(body, r.emit.maxSeedBytes) {
				out, ok := r.scrub(st, san, win, sse)
				if !ok {
					continue
				}
				r.emitBytesTo(st, out, dirs...)
			}
		})
	}
}

// sseSeedWindows splits a recorded SSE stream into windows of whole events, each
// at most maxBytes, so one large real stream yields several lean, realistic
// sub-stream seeds rather than a single oversized blob. Events are the
// blank-line-delimited records of the SSE framing; consecutive events are packed
// greedily, and each window keeps its framing so it re-parses as a stream. A
// single event larger than maxBytes is skipped (the emitter would drop it as
// oversized anyway). A stream already within maxBytes returns as one window.
func sseSeedWindows(body []byte, maxBytes int) [][]byte {
	if maxBytes <= 0 || len(body) <= maxBytes {
		return [][]byte{body}
	}
	var windows [][]byte
	var cur []byte
	for _, ev := range splitSSEEvents(body) {
		if len(ev) > maxBytes {
			continue // an oversized single event can't fit any window
		}
		if len(cur) > 0 && len(cur)+len(ev) > maxBytes {
			windows = append(windows, cur)
			cur = nil
		}
		cur = append(cur, ev...)
	}
	if len(cur) > 0 {
		windows = append(windows, cur)
	}
	return windows
}

// splitSSEEvents splits an SSE body into events on the blank-line separator,
// keeping each event's trailing "\n\n" so the pieces concatenate back to a valid
// stream. The final unterminated remainder (if any) is returned as one event.
func splitSSEEvents(body []byte) [][]byte {
	var events [][]byte
	for len(body) > 0 {
		i := bytes.Index(body, []byte("\n\n"))
		if i < 0 {
			events = append(events, body)
			break
		}
		events = append(events, body[:i+2])
		body = body[i+2:]
	}
	return events
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
