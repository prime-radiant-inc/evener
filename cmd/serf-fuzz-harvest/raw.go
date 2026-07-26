package main

import (
	"bytes"
	"os"
	"strings"

	"primeradiant.com/serf/llm/apilog"
)

const harvestAPILogMaxLineBytes = 128 << 20

// harvestSSE walks canonical per-session API logs and emits each SSE response
// body as seeds for FuzzParseSSE (provider-agnostic) plus the matching provider
// metamorphic decoder. Provider rejections with plain JSON bodies are emitted
// once rather than split into SSE windows.
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
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		decoder := apilog.NewDecoder(f, harvestAPILogMaxLineBytes)
		for {
			record, err := decoder.Next()
			if err != nil {
				break
			}
			attempt, ok := record.(apilog.APIAttemptRecord)
			if !ok || attempt.Response == nil {
				continue
			}
			body, err := apilog.DecodeBody(attempt.Response.Body)
			if err != nil || len(body) == 0 {
				continue
			}
			st.scanned++
			dirs := append([]string{r.dir(dirParseSSE)}, providerTargetDirsForAttempt(r, attempt.Request.EndpointFamily, attempt.ProviderInstance, body)...)
			if !looksLikeSSE(body) {
				if attempt.Outcome != apilog.AttemptProviderReject {
					continue
				}
				out, ok := r.scrub(st, san, body, false)
				if ok {
					r.emitBytesTo(st, out, dirs...)
				}
				continue
			}
			for _, win := range sseSeedWindows(body, r.emit.maxSeedBytes) {
				out, ok := r.scrub(st, san, win, true)
				if !ok {
					continue
				}
				r.emitBytesTo(st, out, dirs...)
			}
		}
		_ = f.Close()
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
	for line := range bytes.SplitSeq(body, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("data:")) || bytes.HasPrefix(line, []byte("event:")) {
			return true
		}
	}
	return false
}

// providerTargetDirs maps an older provider-only record by body shape first.
func providerTargetDirs(r *runner, provider string, body []byte) []string {
	return providerTargetDirsForAttempt(r, "", provider, body)
}

// providerTargetDirsForAttempt maps a response to its metamorphic decoder corpora. The
// canonical endpoint family wins over the configured provider-instance name,
// which may be an arbitrary alias. Older records without a family use the wire
// shape; Chat Completions fans out because OpenAI and OpenAI-compatible streams
// share that shape. Canonical provider names are a final fallback for bodies
// such as provider errors that do not identify their protocol.
func providerTargetDirsForAttempt(r *runner, endpointFamily, provider string, body []byte) []string {
	if dirs := endpointFamilyTargetDirs(r, endpointFamily); len(dirs) > 0 {
		return dirs
	}
	if dirs := bodyShapeTargetDirs(r, body); len(dirs) > 0 {
		return dirs
	}

	switch provider {
	case "anthropic":
		return []string{r.dir(dirAnthropicStream)}
	case "google":
		return []string{r.dir(dirGeminiStream)}
	case "openai-compatible":
		return []string{r.dir(dirOpenAICompatStream)}
	case "openai":
		return []string{r.dir(dirOpenAIResponses), r.dir(dirOpenAIChatComplete)}
	}
	return nil
}

func endpointFamilyTargetDirs(r *runner, endpointFamily string) []string {
	switch strings.TrimSpace(endpointFamily) {
	case "anthropic_messages":
		return []string{r.dir(dirAnthropicStream)}
	case "google_stream_generate_content":
		return []string{r.dir(dirGeminiStream)}
	case "openai_public", "openai_codex":
		return []string{r.dir(dirOpenAIResponses)}
	case "openai_chat_completions":
		return []string{r.dir(dirOpenAIChatComplete)}
	case "openai_compatible_chat_completions":
		return []string{r.dir(dirOpenAICompatStream)}
	default:
		return nil
	}
}

func bodyShapeTargetDirs(r *runner, body []byte) []string {
	switch {
	case isResponsesStream(body):
		return []string{r.dir(dirOpenAIResponses)}
	case isAnthropicMessagesStream(body):
		return []string{r.dir(dirAnthropicStream)}
	case isGeminiStream(body):
		return []string{r.dir(dirGeminiStream)}
	case isChatCompletionsStream(body):
		return []string{r.dir(dirOpenAIChatComplete), r.dir(dirOpenAICompatStream)}
	default:
		return nil
	}
}

// isResponsesStream detects the OpenAI Responses API event shape.
func isResponsesStream(body []byte) bool {
	s := string(body)
	return strings.Contains(s, `"type":"response.`) || strings.Contains(s, `"type": "response.`)
}

// isAnthropicMessagesStream detects the Anthropic Messages event vocabulary.
func isAnthropicMessagesStream(body []byte) bool {
	s := string(body)
	return strings.Contains(s, "event: message_start") ||
		strings.Contains(s, `"type":"message_start"`) || strings.Contains(s, `"type": "message_start"`) ||
		strings.Contains(s, `"type":"content_block_`) || strings.Contains(s, `"type": "content_block_`) ||
		strings.Contains(s, `"type":"message_delta"`) || strings.Contains(s, `"type": "message_delta"`)
}

// isGeminiStream detects the streamGenerateContent response vocabulary.
func isGeminiStream(body []byte) bool {
	s := string(body)
	return strings.Contains(s, `"candidates"`) || strings.Contains(s, `"usageMetadata"`) || strings.Contains(s, `"promptFeedback"`)
}

// isChatCompletionsStream detects the OpenAI Chat Completions chunk shape.
func isChatCompletionsStream(body []byte) bool {
	s := string(body)
	return strings.Contains(s, "chat.completion") || strings.Contains(s, `"choices"`)
}
