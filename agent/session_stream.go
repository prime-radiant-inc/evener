package agent

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

type sessionStreamAccumulator interface {
	Process(llm.StreamEvent)
	Response() *llm.Response
	PartialResponse() *llm.Response
}

var newSessionStreamAccumulator = func() sessionStreamAccumulator {
	return llm.NewStreamAccumulator()
}

// sessionCallModelAfterConsumeHook is a test-only seam for the ownership
// boundary between stream consumption and retry bookkeeping.
var sessionCallModelAfterConsumeHook func()

type sessionModelResponse struct {
	conversation              *conversationRequest
	Response                  llm.Response
	StreamedAssistant         bool
	CommunicatePreviewCallIDs []string
}

func sortedPreviewCallIDs(calls map[string]struct{}) []string {
	ids := make([]string, 0, len(calls))
	for id := range calls {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *Session) resetCommunicatePreviews(calls map[string]struct{}) {
	for callID := range calls {
		s.emit(events.CommunicatePreviewResetData{CallID: callID})
		delete(calls, callID)
	}
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
	// content event was ever seen. Content events are text deltas, reasoning
	// deltas, and tool-call argument bytes (delta OR end — some adapters,
	// e.g. google, emit a tool call's complete arguments on end alone, with
	// no preceding delta) — not wall-clock attempt duration, since SSE
	// keep-alive comments reset the read timer and let a stalled attempt run
	// minutes with zero output.
	ContentWindow time.Duration
	// SalvagedBytes counts text + tool-arg bytes only; reasoning is never
	// salvaged even though it moves ContentWindow.
	SalvagedBytes int
}

// modelRetryFailFastAfter is the fail-fast budget callModel gives
// llm.RetryStream: the number of consecutive consume-phase failures that ends
// a retry group early (llm/stream_retry.go's streak rule). Named here because
// the retry chip's denominator drops to it as soon as such a failure lands, so
// the two readers must not drift.
const modelRetryFailFastAfter = 4

// emitModelRetry builds the RetryPolicy.OnRetry hook that reports each retry of
// req on the session event bus. Attempt counts retries (the first retry is 1);
// MaxAttempts is the full budget including the initial try, so a consumer can
// render "attempt 9 of 11" without knowing the policy. A wall-budgeted rate
// limit has no attempt denominator, so both denominator fields are zero.
//
// group is the retry group the in-flight callModel invocation is building —
// not yet appended to the round recorder's Groups slice, so reading it here
// needs no lock: emitModelRetry runs synchronously on the turn goroutine
// (RetryStream's own loop, no goroutines spawned), the same goroutine that
// owns group until callModel returns it to its caller. It drives two honesty
// fields: AttemptCap, the policy's full budget until a consume-phase failure
// (PhaseConsume or PhaseSilentStall — both count toward the streak rule) has
// landed in the group, then the early-stop bound, so the chip never promises
// retries the streak rule won't spend; and GroupElapsedMS, wall-clock time
// since this call to emitModelRetry (made once per retry group), so a client
// can render how long the current model call has been running.
//
// It also surfaces jobPhaseModelRetrying into the parent's job activity
// (Component 3's delegate scope): without this, a delegate child grinding
// through a retry storm reads to the parent as the same opaque
// jobPhaseAwaitingModel it showed before the first attempt ever ran.
func (s *Session) emitModelRetry(policy llm.RetryPolicy, req llm.Request, group *groupRecord) func(error, int, time.Duration) {
	groupStart := time.Now()
	return func(err error, attempt int, delay time.Duration) {
		s.noteParentJobActivity(jobPhaseModelRetrying)
		maxAttempts := max(policy.MaxRetries, 0) + 1
		attemptCap := maxAttempts
		if group != nil && group.hasConsumePhaseFailure() {
			attemptCap = modelRetryFailFastAfter
		}
		if policy.WallBudgetedRateLimit(err) {
			maxAttempts, attemptCap = 0, 0
		}
		data := events.ModelRetryData{
			Attempt:        attempt,
			MaxAttempts:    maxAttempts,
			DelayMS:        delay.Milliseconds(),
			ErrorClass:     llm.Kind(err).String(),
			Model:          req.Model,
			GroupElapsedMS: time.Since(groupStart).Milliseconds(),
			AttemptCap:     attemptCap,
		}
		if err != nil {
			data.Message = err.Error()
		}
		if le, ok := errors.AsType[llm.Error](err); ok {
			data.StatusCode = le.StatusCode()
		}
		s.emit(data)
	}
}

// callModel runs one retry group against req's model and records what every
// attempt did into group, which the caller owns (one group per invocation).
func (s *Session) callModel(ctx context.Context, policy llm.RetryPolicy, profile *provider.Profile, req llm.Request, group *groupRecord) (sessionModelResponse, error) {
	groupStart := s.sclock().Now()
	defer func() { group.Elapsed = s.sclock().Now().Sub(groupStart) }()
	previewCalls := map[string]struct{}{}
	rememberPreviewCalls := func(ids []string) {
		for _, id := range ids {
			if id != "" {
				previewCalls[id] = struct{}{}
			}
		}
	}
	withPreviewCalls := func(resp sessionModelResponse) sessionModelResponse {
		resp.CommunicatePreviewCallIDs = sortedPreviewCallIDs(previewCalls)
		return resp
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			s.resetCommunicatePreviews(previewCalls)
			panic(recovered)
		}
	}()
	group.Model = req.Model
	group.Provider = req.Provider
	// Announce every retry before its backoff sleep. Both paths below share this
	// policy, so a rejection at stream open and a mid-stream truncation report
	// alike. Without it the whole retry chain is silent on the event bus: a
	// rejection streams nothing, so the assistant-text reset (which needs partial
	// output) never fires and a long rate limit is indistinguishable from a hang.
	policy.OnRetry = s.emitModelRetry(policy, req, group)
	// Admission runs here so typed local errors reach the session's warning and
	// bounded-recovery paths before provider attempt handling. llm.Client repeats
	// the check immediately before dispatch by design, protecting both paths from
	// middleware that mutates an already admitted request.
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
			s.emit(events.AssistantTextResetData{})
			s.resetCommunicatePreviews(previewCalls)
		},
		// FailFastAfter enables both llm.RetryStream early-stop rules: the
		// streak rule (modelRetryFailFastAfter consecutive consume-phase
		// failures) and the cap rule (2 consecutive long streams cut
		// mid-flight). Disabled (0) in RetryStream's non-agent callers,
		// which see no behavior change.
		FailFastAfter: modelRetryFailFastAfter,
	}, func(ctx context.Context) (llm.AttemptReport, error) {
		attemptStart := time.Now()
		dispatchReq, _, budgetErr := s.budgetModelDispatchRequestForSession(profile, req)
		if budgetErr != nil {
			group.observe(attemptRecord{Phase: llm.PhaseOpen, Err: budgetErr, Duration: time.Since(attemptStart)}, nil)
			return llm.AttemptReport{Phase: llm.PhaseOpen}, budgetErr
		}
		// Final skill-delivery admission: revalidate the exact outgoing
		// shape against pending obligations and commit before dispatch. A
		// missing protected body fails visibly here — never a truncated
		// delivery and never another compact/reload cycle.
		if deliveryErr := s.finalizeSkillDelivery(ctx, dispatchReq); deliveryErr != nil {
			group.observe(attemptRecord{Phase: llm.PhaseOpen, Err: deliveryErr, Duration: time.Since(attemptStart)}, nil)
			return llm.AttemptReport{Phase: llm.PhaseOpen}, deliveryErr
		}
		conversation := s.freezeConversationRequest()
		st, err := s.client.Stream(ctx, dispatchReq)
		if streamUnavailable(err) || (err == nil && st == nil) {
			// Nothing was attempted against the provider: the call falls
			// through to the non-streaming path below, so no attempt is
			// recorded for it.
			streamUnavailableForProfile = true
			return llm.AttemptReport{}, nil
		}
		if err != nil {
			// Rejected before or at stream open — open-phase by definition;
			// consumeModelStream never runs, so it cannot classify this one.
			group.observe(attemptRecord{Phase: llm.PhaseOpen, Err: err, Duration: time.Since(attemptStart)}, nil)
			return llm.AttemptReport{Phase: llm.PhaseOpen}, err
		}
		var obs attemptObservation
		var consumeErr error
		result, obs, consumeErr = s.consumeModelStream(ctx, req, st)
		result.conversation = conversation
		rememberPreviewCalls(result.CommunicatePreviewCallIDs)
		if sessionCallModelAfterConsumeHook != nil {
			sessionCallModelAfterConsumeHook()
		}
		// Recorded inside the closure, so the group keeps this attempt's
		// partial before OnReset discards it ahead of the next one.
		group.observe(attemptRecord{
			Phase:         obs.Phase,
			Err:           consumeErr,
			Duration:      time.Since(attemptStart),
			ContentWindow: obs.ContentWindow,
			SalvagedBytes: obs.SalvagedBytes,
		}, obs.Partial)
		return llm.AttemptReport{
			// obs.Partial != nil is the accumulator's "something was
			// delivered to the caller" signal — text, communicate
			// preview, tool-call args, or reasoning — and unlike
			// sessionModelResponse.StreamedAssistant it survives the
			// error path (StreamedAssistant stays zeroed on every error
			// return, kata-pinned in the msfz fuzz test).
			PartialOutput: obs.Partial != nil,
			Phase:         obs.Phase,
			ContentWindow: obs.ContentWindow,
			SalvagedBytes: obs.SalvagedBytes,
		}, consumeErr
	})
	if !streamUnavailableForProfile {
		return withPreviewCalls(result), err
	}
	// Streaming unsupported by this provider/runtime — fall through to the
	// non-streaming Complete path.

	var conversation *conversationRequest
	resp, err := llm.Retry(ctx, policy, s.cfg.LLMSleep, nil, func() (llm.Response, error) {
		dispatchReq, _, budgetErr := s.budgetModelDispatchRequestForSession(profile, req)
		if budgetErr != nil {
			return llm.Response{}, budgetErr
		}
		// Same final-admission seam as the streaming path.
		if deliveryErr := s.finalizeSkillDelivery(ctx, dispatchReq); deliveryErr != nil {
			return llm.Response{}, deliveryErr
		}
		conversation = s.freezeConversationRequest()
		return s.client.Complete(ctx, dispatchReq)
	})
	if err != nil {
		return sessionModelResponse{}, err
	}
	return sessionModelResponse{Response: resp, conversation: conversation}, nil
}

func isTurnCancellation(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if isAbortError(err) {
		return true
	}
	// A typed llm error that merely wraps a context sentinel (e.g. a
	// RequestTimeoutError from an adapter-level timeout while this turn's ctx is
	// still alive) is a retryable failure, not the turn being interrupted —
	// only a bare context sentinel counts as a cancellation here.
	if _, ok := errors.AsType[llm.Error](err); ok {
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
	communicatePreviewStarted := map[string]bool{}
	communicatePreviewDecoders := map[string]*communicatePreviewDecoder{}
	var communicatePreviewOrder []string
	streamedAssistant := false
	assistantStarted := false
	finished := false
	previewCallIDs := func() []string {
		return append([]string(nil), communicatePreviewOrder...)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			for _, callID := range previewCallIDs() {
				s.emit(events.CommunicatePreviewResetData{CallID: callID})
			}
			panic(recovered)
		}
	}()

	// firstContent/lastContent bound the content-event window — text, tool-arg
	// (delta or end), and reasoning content only, never wall-clock attempt
	// duration (spec: SSE keep-alives reset the read timer, so a stalled
	// attempt can run minutes with zero output; content-event span is what
	// actually discriminates a cap-shaped cutoff from a stall).
	var firstContent, lastContent time.Time
	contentSeen := false
	contentClock := s.cfg.testOnly.contentWindowClock
	if contentClock == nil {
		contentClock = time.Now
	}
	noteContent := func() {
		now := contentClock()
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
		case errors.Is(err, llm.ErrSSEReadTimeout) || errors.Is(err, llm.ErrResponseIdleTimeout) || time.Since(attemptStart) >= 30*time.Second:
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
		s.emit(events.AssistantTextStartData{Model: req.Model})
		assistantStarted = true
		streamedAssistant = true
	}
	emitAssistantDelta := func(delta string) {
		if delta == "" {
			return
		}
		emitAssistantStart()
		s.emit(events.AssistantTextDeltaData{Delta: delta})
	}
	emitReasoningDelta := func(delta string) {
		if delta == "" {
			return
		}
		s.emit(events.ReasoningSummaryDeltaData{Delta: delta})
	}
	emitCommunicatePreview := func(callID string) {
		args := ""
		if b := toolArgs[callID]; b != nil {
			args = b.String()
		}
		// Decode incrementally: this runs once per streamed argument delta, and
		// re-deriving the whole message each time is quadratic in its length.
		decoder := communicatePreviewDecoders[callID]
		if decoder == nil {
			decoder = &communicatePreviewDecoder{field: "message"}
			communicatePreviewDecoders[callID] = decoder
		}
		message, ok := decoder.message(args)
		if !ok || message == "" {
			return
		}
		prev := communicateText[callID]
		if len(message) <= len(prev) || !strings.HasPrefix(message, prev) {
			return
		}
		communicateText[callID] = message
		if !communicatePreviewStarted[callID] {
			s.emit(events.CommunicatePreviewStartData{CallID: callID, Authorship: s.roundTranscriptAuthorship()})
			communicatePreviewStarted[callID] = true
			communicatePreviewOrder = append(communicatePreviewOrder, callID)
		}
		s.emit(events.CommunicatePreviewDeltaData{CallID: callID, Delta: message[len(prev):]})
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
				// The end event carries the provider's final arguments, which
				// replace the accumulated text and need not be an extension of it
				// (rescueClaudeXMLArgs rewrites them). The incremental decoder's
				// offsets are only valid while the text it read is a prefix of the
				// current text, so drop it and decode the final text from scratch.
				delete(communicatePreviewDecoders, ev.ToolCall.ID)
				noteContent()
			}
			if toolNames[ev.ToolCall.ID] == s.resultToolName() {
				emitCommunicatePreview(ev.ToolCall.ID)
			}
		case llm.StreamEventFinish:
			finished = true
		case llm.StreamEventError:
			if ev.Err != nil {
				return sessionModelResponse{CommunicatePreviewCallIDs: previewCallIDs()}, observe(ev.Err), ev.Err
			}
			err := llm.NewStreamError(req.Provider, "stream error", nil)
			return sessionModelResponse{CommunicatePreviewCallIDs: previewCallIDs()}, observe(err), err
		}
	}

	if !finished {
		if err := ctx.Err(); err != nil {
			return sessionModelResponse{CommunicatePreviewCallIDs: previewCallIDs()}, observe(err), err
		}
		err := llm.NewStreamError(req.Provider, "stream ended without finish event", nil)
		return sessionModelResponse{CommunicatePreviewCallIDs: previewCallIDs()}, observe(err), err
	}
	resp := acc.Response()
	if resp == nil {
		err := llm.NewStreamError(req.Provider, "stream ended without response", nil)
		return sessionModelResponse{CommunicatePreviewCallIDs: previewCallIDs()}, observe(err), err
	}
	if resp.Provider == "" {
		resp.Provider = req.Provider
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return sessionModelResponse{Response: *resp, StreamedAssistant: streamedAssistant, CommunicatePreviewCallIDs: previewCallIDs()}, observe(nil), nil
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

// partialJSONStringField pulls ONE named string field out of possibly-truncated
// JSON object text, for the streaming preview above: the communicate tool's
// message field, rendered live as its arguments arrive.
//
// The scanner it runs on — scanPartialJSONStringBody, plus the all-fields
// generalization partialJSONStringFields that settlement uses — lives in
// agent/salvage.go. Salvage is where the partial-JSON extraction rules are
// stated and tested; this function is the single-field preview caller of them.
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
	value, _, _ := scanPartialJSONStringBody(rest[1:])
	return value, true
}

// communicatePreviewDecoder decodes one call's preview message incrementally.
//
// The live preview re-derived the whole message on every argument delta, which
// is O(deltas x message length) for a single message: a communicate call streams
// a delta per token, and each one re-decoded the entire message so far. This
// decodes only the bytes appended since the last delta.
//
// It is not a second JSON implementation. Every chunk goes through
// scanPartialJSONStringBody, the same scanner partialJSONStringField uses, so an
// escape, a \uXXXX, or a surrogate pair is decoded by exactly the code that
// decodes it on the one-shot path. Chunks are cut only where the scanner can
// finish, which is what keeps a sequence split across two deltas intact: the
// incomplete tail stays buffered and is retried with the next delta.
type communicatePreviewDecoder struct {
	field string

	// bodyStart is the offset in the accumulated argument text where the
	// message's string body begins, valid once found is true. It does not move
	// after that, so the key search runs once per call rather than per delta.
	bodyStart int
	found     bool

	// read is how many bytes of the argument text this decoder has consumed
	// from bodyStart onward.
	read int
	// tail is the consumed-but-undecodable suffix: the start of an escape whose
	// remaining bytes have not arrived.
	tail string
	// closed is true once the value's closing quote was decoded, so later deltas
	// belong to other fields.
	closed bool
	// decoded is the message text decoded so far.
	decoded strings.Builder
}

// message returns the decoded message after consuming raw, which must be the
// complete accumulated argument text for the call.
func (d *communicatePreviewDecoder) message(raw string) (string, bool) {
	if !d.found {
		key := `"` + d.field + `"`
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
		d.found = true
		d.bodyStart = len(raw) - len(rest) + 1
		d.read = 0
	}

	if d.closed {
		// The value already ended. Later deltas belong to other fields, so they
		// must not be decoded into this message.
		return d.decoded.String(), true
	}
	body := raw[d.bodyStart:]
	// d.read counts bytes of body consumed INCLUDING the buffered tail: the tail
	// bytes have been fed to the scanner and must not be fed again.
	if len(body) > d.read {
		// Decode the buffered tail plus the bytes not yet seen, as one string: the
		// split must see the joined text, because an escape can be incomplete only
		// once the new bytes are appended to the tail. Splitting the new bytes alone
		// would call a tail-plus-fragment decodable and drop the escape.
		chunk, rest := splitDecodableEscape(d.tail + body[d.read:])
		value, _, terminated := scanPartialJSONStringBody(chunk)
		d.decoded.WriteString(value)
		d.tail = rest
		// Everything consumed is read; rest is the undecodable suffix and stays
		// pending, but its bytes are already accounted for in read.
		d.read = len(body)
		if terminated {
			d.closed = true
		}
	}
	return d.decoded.String(), true
}

// splitDecodableEscape splits s at the first backslash whose escape cannot be
// decoded from s alone, returning the decodable prefix and the incomplete tail.
// An incomplete tail is the beginning of an escape split across deltas: a lone
// backslash, or a \u with fewer than six bytes, extended to twelve when the
// leading code unit announces a surrogate pair.
func splitDecodableEscape(s string) (chunk, tail string) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			// A complete escape is skipped whole, so an escaped quote is never
			// mistaken for the closing quote and an escape is never handed to the
			// scanner half-fed.
			n, ok := escapeWidth(s[i:])
			if !ok {
				return s[:i], s[i:]
			}
			i += n - 1
		case '"':
			// The value's closing quote ends the body; everything after it
			// belongs to other fields. It stays IN the chunk so the scanner sees
			// the termination and stops, rather than running on into the next
			// field's bytes.
			return s[:i+1], ""
		}
	}
	return s, ""
}

// escapeWidth returns how many bytes one JSON escape at s[0] ('\\') occupies,
// and ok=false when s does not yet hold the whole escape.
//
// Width is measured by the decoders the scanner itself uses, not by a second
// reading of the grammar, so a split can never call an escape complete that the
// scanner then refuses to decode.
func escapeWidth(s string) (int, bool) {
	if len(s) < 2 {
		return 0, false
	}
	if s[1] == 'u' {
		_, tail, ok := unquoteJSONUnicodeEscape(s)
		if !ok {
			return 0, false
		}
		return len(s) - len(tail), true
	}
	if s[1] == '/' {
		// JSON allows a solidus to be escaped, and scanPartialJSONStringBody
		// accepts it, but strconv.UnquoteChar is a Go rune unquoter and rejects
		// it. Without this case the splitter would read \/ as an incomplete
		// escape and buffer it forever, freezing the preview at the bytes before
		// it.
		return 2, true
	}
	_, _, tail, err := strconv.UnquoteChar(s, '"')
	if err != nil {
		return 0, false
	}
	return len(s) - len(tail), true
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
