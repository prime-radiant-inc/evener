package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

type sessionStreamAccumulator interface {
	Process(llm.StreamEvent)
	Response() *llm.Response
	PartialResponse() *llm.Response
}

var newSessionStreamAccumulator = func() sessionStreamAccumulator {
	return llm.NewStreamAccumulator()
}

type sessionModelResponse struct {
	Response          llm.Response
	StreamedAssistant bool
}

// attemptObservation carries one consumeModelStream attempt's phase/stats back
// to callModel's retry closure, feeding llm.RetryStream's early-stop rules.
// Populated on every return path, including errors, so a mid-stream failure
// is classified from what the stream actually did.
type attemptObservation struct {
	// Partial is the accumulator's best-effort snapshot at the point of
	// return — nil only when nothing was accumulated.
	Partial *llm.Response
	// Phase classifies the failure per llm.AttemptPhase; meaningful only when
	// the attempt returned a non-nil error.
	Phase llm.AttemptPhase
	// ContentWindow is first-content-event to last-content-event, zero if no
	// content event was ever seen. Content events are text, tool-call-argument,
	// and reasoning deltas — not wall-clock attempt duration, since SSE
	// keep-alive comments reset the read timer and let a stalled attempt run
	// minutes with zero output.
	ContentWindow time.Duration
	// SalvagedBytes counts text + tool-arg bytes only; reasoning is never
	// salvaged even though it moves ContentWindow.
	SalvagedBytes int
}

// emitModelRetry builds the RetryPolicy.OnRetry hook that reports each retry of
// req on the session event bus. Attempt counts retries (the first retry is 1);
// MaxAttempts is the full budget including the initial try, so a consumer can
// render "attempt 9 of 11" without knowing the policy.
func (s *Session) emitModelRetry(policy llm.RetryPolicy, req llm.Request) func(error, int, time.Duration) {
	return func(err error, attempt int, delay time.Duration) {
		data := events.ModelRetryData{
			Attempt:     attempt,
			MaxAttempts: max(policy.MaxRetries, 0) + 1,
			DelayMS:     delay.Milliseconds(),
			ErrorClass:  llm.Kind(err).String(),
			Model:       req.Model,
		}
		if err != nil {
			data.Message = err.Error()
		}
		var le llm.Error
		if errors.As(err, &le) {
			data.StatusCode = le.StatusCode()
		}
		s.emit(events.EventModelRetry, data)
	}
}

func (s *Session) callModel(ctx context.Context, policy llm.RetryPolicy, profile *provider.Profile, req llm.Request) (sessionModelResponse, error) {
	// Announce every retry before its backoff sleep. Both paths below share this
	// policy, so a rejection at stream open and a mid-stream truncation report
	// alike. Without it the whole retry chain is silent on the event bus: a
	// rejection streams nothing, so the assistant-text reset (which needs partial
	// output) never fires and a long rate limit is indistinguishable from a hang.
	policy.OnRetry = s.emitModelRetry(policy, req)
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
			// FailFastAfter enables both llm.RetryStream early-stop rules: the
			// streak rule (4 consecutive consume-phase failures) and the cap
			// rule (2 consecutive long streams cut mid-flight). Disabled (0) in
			// RetryStream's non-agent callers, which see no behavior change.
			FailFastAfter: 4,
		}, func(ctx context.Context) (llm.AttemptReport, error) {
			st, err := s.client.Stream(ctx, req)
			if streamUnavailable(err) || (err == nil && st == nil) {
				streamUnavailableForProfile = true
				return llm.AttemptReport{}, nil
			}
			if err != nil {
				// Rejected before or at stream open — open-phase by definition;
				// consumeModelStream never runs, so it cannot classify this one.
				return llm.AttemptReport{Phase: llm.PhaseOpen}, err
			}
			var obs attemptObservation
			var consumeErr error
			result, obs, consumeErr = s.consumeModelStream(ctx, req, st)
			return llm.AttemptReport{
				// obs.Partial covers everything consumeModelStream delivered to
				// the caller (assistant text, communicate preview, reasoning) —
				// a strict superset of sessionModelResponse.StreamedAssistant,
				// which only tracks assistant-visible text and stays zeroed on
				// every error return (kata-pinned in the msfz fuzz test).
				PartialOutput: obs.Partial != nil,
				Phase:         obs.Phase,
				ContentWindow: obs.ContentWindow,
				SalvagedBytes: obs.SalvagedBytes,
			}, consumeErr
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

// consumeModelStream drains one model stream. It returns the assembled
// response, an attemptObservation classifying the attempt's phase/stats (on
// every return path, including errors), and the terminal error if any.
func (s *Session) consumeModelStream(ctx context.Context, req llm.Request, st llm.Stream) (sessionModelResponse, attemptObservation, error) {
	defer st.Close() //nolint:errcheck

	attemptStart := time.Now()
	acc := newSessionStreamAccumulator()
	toolArgs := map[string]*strings.Builder{}
	toolNames := map[string]string{}
	communicateText := map[string]string{}
	streamedAssistant := false
	assistantStarted := false
	finished := false

	// firstContent/lastContent bound the content-event window — text, tool-arg,
	// and reasoning deltas only, never wall-clock attempt duration (spec:
	// SSE keep-alives reset the read timer, so a stalled attempt can run
	// minutes with zero output; content-event span is what actually
	// discriminates a cap-shaped cutoff from a stall).
	var firstContent, lastContent time.Time
	contentSeen := false
	noteContent := func() {
		now := time.Now()
		if !contentSeen {
			firstContent = now
			contentSeen = true
		}
		lastContent = now
	}

	// observe builds this attempt's observation as of the current accumulator
	// state. Phase is only classified when err is non-nil — RetryStream never
	// inspects it on success.
	observe := func(err error) attemptObservation {
		partial := acc.PartialResponse()
		obs := attemptObservation{
			Partial:       partial,
			SalvagedBytes: salvagedContentBytes(partial),
		}
		if contentSeen {
			obs.ContentWindow = lastContent.Sub(firstContent)
		}
		if err == nil {
			return obs
		}
		switch {
		case contentSeen:
			obs.Phase = llm.PhaseConsume
		case errors.Is(err, llm.ErrSSEReadTimeout) || time.Since(attemptStart) >= 30*time.Second:
			obs.Phase = llm.PhaseSilentStall
		default:
			obs.Phase = llm.PhaseFastReject
		}
		return obs
	}

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
	emitReasoningDelta := func(delta string) {
		if delta == "" {
			return
		}
		s.emit(events.EventReasoningSummaryDelta, events.ReasoningSummaryDeltaData{Delta: delta})
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
			s.noteParentJobActivity(jobPhaseModelStreaming)
			emitAssistantStart()
		case llm.StreamEventTextDelta:
			s.noteParentJobActivity(jobPhaseModelStreaming)
			if ev.Delta != "" {
				noteContent()
			}
			emitAssistantDelta(ev.Delta)
		case llm.StreamEventReasoningDelta:
			s.noteParentJobActivity(jobPhaseModelStreaming)
			if ev.ReasoningDelta != "" {
				noteContent()
			}
			emitReasoningDelta(ev.ReasoningDelta)
		case llm.StreamEventToolCallStart:
			s.noteParentJobActivity(jobPhaseModelStreaming)
			if ev.ToolCall == nil || ev.ToolCall.ID == "" {
				break
			}
			toolNames[ev.ToolCall.ID] = s.canonicalToolName(ev.ToolCall.Name)
			if _, ok := toolArgs[ev.ToolCall.ID]; !ok {
				toolArgs[ev.ToolCall.ID] = &strings.Builder{}
			}
		case llm.StreamEventToolCallDelta:
			s.noteParentJobActivity(jobPhaseModelStreaming)
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
				noteContent()
			}
			if toolNames[ev.ToolCall.ID] == s.resultToolName() {
				emitCommunicatePreview(ev.ToolCall.ID)
			}
		case llm.StreamEventToolCallEnd:
			s.noteParentJobActivity(jobPhaseModelStreaming)
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
				return sessionModelResponse{}, observe(ev.Err), ev.Err
			}
			err := llm.NewStreamError(req.Provider, "stream error", nil)
			return sessionModelResponse{}, observe(err), err
		}
	}

	if !finished {
		if err := ctx.Err(); err != nil {
			return sessionModelResponse{}, observe(err), err
		}
		err := llm.NewStreamError(req.Provider, "stream ended without finish event", nil)
		return sessionModelResponse{}, observe(err), err
	}
	resp := acc.Response()
	if resp == nil {
		err := llm.NewStreamError(req.Provider, "stream ended without response", nil)
		return sessionModelResponse{}, observe(err), err
	}
	if resp.Provider == "" {
		resp.Provider = req.Provider
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return sessionModelResponse{Response: *resp, StreamedAssistant: streamedAssistant}, observe(nil), nil
}

// salvagedContentBytes counts the salvageable bytes in a response snapshot:
// text plus raw tool-call argument bytes. Reasoning is never salvaged, so it
// contributes nothing even though it moves the content window.
func salvagedContentBytes(r *llm.Response) int {
	if r == nil {
		return 0
	}
	n := len(r.Text())
	for _, tc := range r.ToolCalls() {
		n += len(tc.Arguments)
	}
	return n
}

func partialJSONStringField(raw, field string) (string, bool) {
	key := `"` + field + `"`
	_, rest, ok := strings.Cut(raw, key)
	if !ok {
		return "", false
	}
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
