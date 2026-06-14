package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf16"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

type sessionModelResponse struct {
	Response          llm.Response
	StreamedAssistant bool
}

func (s *Session) callModel(ctx context.Context, policy llm.RetryPolicy, profile *provider.Profile, req llm.Request) (sessionModelResponse, error) {
	if profile.SupportsStreaming() {
		// Retry the whole open+consume cycle: a retryable failure can surface
		// at stream open (connect/4xx-5xx) OR mid-stream (truncation, after the
		// HTTP response already returned 200). Wrapping only the open — as the
		// old llm.Retry did here — left mid-stream truncations unretried, so a
		// single transient cutoff failed the entire turn.
		var result sessionModelResponse
		streamUnavailableForProfile := false
		err := llm.RetryStream(ctx, llm.RetryStreamOptions{
			Policy:            policy,
			Sleep:             s.cfg.LLMSleep,
			RetryAfterPartial: true,
			// Before retrying an attempt that already streamed partial output,
			// discard it so the retry's output replaces rather than appends.
			OnReset: func() {
				s.emit(events.EventAssistantTextReset, events.AssistantTextResetData{})
			},
		}, func(ctx context.Context) (bool, error) {
			st, err := s.client.Stream(ctx, req)
			if streamUnavailable(err) || (err == nil && st == nil) {
				streamUnavailableForProfile = true
				return false, nil
			}
			if err != nil {
				return false, err
			}
			var partial bool
			var consumeErr error
			result, partial, consumeErr = s.consumeModelStream(ctx, req, st)
			return partial, consumeErr
		})
		if !streamUnavailableForProfile {
			return result, err
		}
		// Streaming unsupported by this provider/runtime — fall through to the
		// non-streaming Complete path.
	}

	resp, err := llm.Retry(ctx, policy, s.cfg.LLMSleep, nil, func() (llm.Response, error) {
		return s.client.Complete(ctx, req)
	})
	if err != nil {
		return sessionModelResponse{}, err
	}
	return sessionModelResponse{Response: resp}, nil
}

func isTurnCancellation(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	var abort *llm.AbortError
	if errors.As(err, &abort) {
		return true
	}
	// A typed llm error that merely wraps a context sentinel (e.g. a
	// RequestTimeoutError from an adapter-level timeout while this turn's ctx is
	// still alive) is a retryable failure, not the turn being interrupted —
	// only a bare context sentinel counts as a cancellation here.
	var le llm.Error
	if errors.As(err, &le) {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func streamUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errStreamUnavailable) {
		return true
	}
	return errors.Is(err, llm.ErrStreamUnsupported)
}

// consumeModelStream drains one model stream. The bool return reports whether
// any partial output was streamed to the user (an assistant-text or communicate
// preview delta) before it returned — RetryStream uses it to decide whether a
// retry must first reset the partial so the next attempt replaces it.
func (s *Session) consumeModelStream(ctx context.Context, req llm.Request, st llm.Stream) (sessionModelResponse, bool, error) {
	defer st.Close() //nolint:errcheck

	acc := llm.NewStreamAccumulator()
	toolArgs := map[string]*strings.Builder{}
	toolNames := map[string]string{}
	communicateText := map[string]string{}
	streamedAssistant := false
	assistantStarted := false
	finished := false

	emitAssistantStart := func() {
		if assistantStarted {
			return
		}
		s.emit(events.EventAssistantTextStart, events.AssistantTextStartData{Model: req.Model})
		assistantStarted = true
		streamedAssistant = true
	}
	emitAssistantDelta := func(delta string) {
		if delta == "" {
			return
		}
		emitAssistantStart()
		s.emit(events.EventAssistantTextDelta, events.AssistantTextDeltaData{Delta: delta})
	}
	emitCommunicatePreview := func(callID string) {
		args := ""
		if b := toolArgs[callID]; b != nil {
			args = b.String()
		}
		message, ok := partialJSONStringField(args, "message")
		if !ok || message == "" {
			return
		}
		prev := communicateText[callID]
		if len(message) <= len(prev) || !strings.HasPrefix(message, prev) {
			return
		}
		communicateText[callID] = message
		emitAssistantDelta(message[len(prev):])
	}

	for ev := range st.Events() {
		acc.Process(ev)
		switch ev.Type {
		case llm.StreamEventTextStart:
			emitAssistantStart()
		case llm.StreamEventTextDelta:
			emitAssistantDelta(ev.Delta)
		case llm.StreamEventToolCallStart:
			if ev.ToolCall == nil || ev.ToolCall.ID == "" {
				break
			}
			toolNames[ev.ToolCall.ID] = s.canonicalToolName(ev.ToolCall.Name)
			if _, ok := toolArgs[ev.ToolCall.ID]; !ok {
				toolArgs[ev.ToolCall.ID] = &strings.Builder{}
			}
		case llm.StreamEventToolCallDelta:
			if ev.ToolCall == nil || ev.ToolCall.ID == "" {
				break
			}
			if ev.ToolCall.Name != "" {
				toolNames[ev.ToolCall.ID] = s.canonicalToolName(ev.ToolCall.Name)
			}
			if _, ok := toolArgs[ev.ToolCall.ID]; !ok {
				toolArgs[ev.ToolCall.ID] = &strings.Builder{}
			}
			if len(ev.ToolCall.Arguments) > 0 {
				toolArgs[ev.ToolCall.ID].Write(ev.ToolCall.Arguments)
			}
			if toolNames[ev.ToolCall.ID] == s.resultToolName() {
				emitCommunicatePreview(ev.ToolCall.ID)
			}
		case llm.StreamEventToolCallEnd:
			if ev.ToolCall == nil || ev.ToolCall.ID == "" {
				break
			}
			if ev.ToolCall.Name != "" {
				toolNames[ev.ToolCall.ID] = s.canonicalToolName(ev.ToolCall.Name)
			}
			if len(ev.ToolCall.Arguments) > 0 {
				b := &strings.Builder{}
				b.Write(ev.ToolCall.Arguments)
				toolArgs[ev.ToolCall.ID] = b
			}
			if toolNames[ev.ToolCall.ID] == s.resultToolName() {
				emitCommunicatePreview(ev.ToolCall.ID)
			}
		case llm.StreamEventFinish:
			finished = true
		case llm.StreamEventError:
			if ev.Err != nil {
				return sessionModelResponse{}, streamedAssistant, ev.Err
			}
			return sessionModelResponse{}, streamedAssistant, llm.NewStreamError(req.Provider, "stream error", nil)
		}
	}

	if !finished {
		if err := ctx.Err(); err != nil {
			return sessionModelResponse{}, streamedAssistant, err
		}
		return sessionModelResponse{}, streamedAssistant, llm.NewStreamError(req.Provider, "stream ended without finish event", nil)
	}
	resp := acc.Response()
	if resp == nil {
		return sessionModelResponse{}, streamedAssistant, llm.NewStreamError(req.Provider, "stream ended without response", nil)
	}
	if resp.Provider == "" {
		resp.Provider = req.Provider
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return sessionModelResponse{Response: *resp, StreamedAssistant: streamedAssistant}, streamedAssistant, nil
}

func partialJSONStringField(raw, field string) (string, bool) {
	key := `"` + field + `"`
	idx := strings.Index(raw, key)
	if idx < 0 {
		return "", false
	}
	rest := raw[idx+len(key):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return "", false
	}
	rest = strings.TrimLeft(rest[colon+1:], " \t\r\n")
	if !strings.HasPrefix(rest, `"`) {
		return "", false
	}
	rest = rest[1:]

	var b strings.Builder
	for len(rest) > 0 {
		ch := rest[0]
		if ch == '"' {
			return b.String(), true
		}
		if ch == '\\' {
			if len(rest) >= 2 && rest[1] == '/' {
				b.WriteByte('/')
				rest = rest[2:]
				continue
			}
			if strings.HasPrefix(rest, `\u`) {
				r, tail, ok := unquoteJSONUnicodeEscape(rest)
				if !ok {
					return b.String(), true
				}
				b.WriteRune(r)
				rest = tail
				continue
			}
			r, _, tail, err := strconv.UnquoteChar(rest, '"')
			if err != nil {
				return b.String(), true
			}
			b.WriteRune(r)
			rest = tail
			continue
		}
		b.WriteByte(ch)
		rest = rest[1:]
	}
	return b.String(), true
}

func unquoteJSONUnicodeEscape(rest string) (rune, string, bool) {
	if len(rest) < 6 {
		return 0, "", false
	}
	value, err := strconv.ParseUint(rest[2:6], 16, 16)
	if err != nil {
		return 0, "", false
	}
	r := rune(value)
	tail := rest[6:]
	if r >= 0xD800 && r <= 0xDBFF {
		if len(tail) < 6 || !strings.HasPrefix(tail, `\u`) {
			return 0, "", false
		}
		lowValue, err := strconv.ParseUint(tail[2:6], 16, 16)
		if err != nil {
			return 0, "", false
		}
		low := rune(lowValue)
		if low < 0xDC00 || low > 0xDFFF {
			return 0, "", false
		}
		return utf16.DecodeRune(r, low), tail[6:], true
	}
	return r, tail, true
}
