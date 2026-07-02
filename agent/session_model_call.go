package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// ModelAttemptMetadata records continuation, endpoint, and attempt-grouping
// details captured across one model call (including any fallback retries) for
// transcript and API-log reporting.
type ModelAttemptMetadata struct {
	HistoryMode             llm.HistoryMode
	EndpointFamily          string
	EndpointURL             string
	RequestModel            string
	RequestFingerprint      string
	StorageScopeFingerprint string
	ContextMarker           string
	AttemptGroupID          string
	AttemptIndex            int
	AttemptCount            int
	FinalAttemptCount       *int
	PreviousResponseIDHash  string
	ConversationIDHash      string
	ResponseIDHash          string
	StoragePolicyLabel      string
	AdapterAttempts         []llm.AdapterAttemptRecord
}

func singleAttemptRequestMetadata(req llm.Request) (llm.Request, ModelAttemptMetadata) {
	if req.HistoryMode == "" {
		req.HistoryMode = llm.HistoryModeFullHistory
	}
	finalCount := 1
	meta := ModelAttemptMetadata{
		HistoryMode:       req.HistoryMode,
		RequestModel:      req.Model,
		AttemptGroupID:    newAttemptGroupID(),
		AttemptIndex:      1,
		AttemptCount:      1,
		FinalAttemptCount: &finalCount,
	}
	if req.Continuation != nil {
		meta.EndpointFamily = req.Continuation.EndpointFamily
		meta.RequestFingerprint = req.Continuation.RequestFingerprint
		meta.StorageScopeFingerprint = req.Continuation.StorageScopeFingerprint
		meta.ContextMarker = req.Continuation.ContextMarker
		meta.PreviousResponseIDHash = req.Continuation.PreviousResponseIDHash
		meta.ConversationIDHash = req.Continuation.ConversationIDHash
		meta.StoragePolicyLabel = req.Continuation.StoragePolicyLabel
	}
	return req, meta
}

func newAttemptGroupID() string {
	return "ag_" + ulid.Make().String()
}

type modelAttemptRecorder struct {
	mu      sync.Mutex
	groupID string
	records []llm.AdapterAttemptRecord
}

func newModelAttemptRecorder(groupID string) *modelAttemptRecorder {
	return &modelAttemptRecorder{groupID: groupID}
}

func (r *modelAttemptRecorder) record(ctx context.Context, rec llm.AdapterAttemptRecord) llm.AdapterAttemptRecord {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.HistoryMode == "" {
		rec.HistoryMode = rec.Request.HistoryMode
	}
	if rec.Request.HistoryMode == "" {
		rec.Request.HistoryMode = rec.HistoryMode
	}
	rec.AttemptGroupID = r.groupID
	rec.AttemptIndex = len(r.records) + 1
	if rec.Terminal {
		finalCount := rec.AttemptIndex
		rec.AttemptCount = finalCount
		rec.FinalAttemptCount = &finalCount
	}
	r.records = append(r.records, rec)
	return rec
}

func (r *modelAttemptRecorder) attempts() []llm.AdapterAttemptRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]llm.AdapterAttemptRecord, len(r.records))
	copy(out, r.records)
	return out
}

func completeAttemptMetadata(meta ModelAttemptMetadata, resp llm.Response) ModelAttemptMetadata {
	if resp.Raw != nil {
		if endpoint, _ := resp.Raw["endpoint_url"].(string); endpoint != "" {
			meta.EndpointURL = endpoint
		}
		if idHash, _ := resp.Raw["id_hash"].(string); idHash != "" {
			meta.ResponseIDHash = idHash
		}
	}
	return meta
}

func (s *Session) maybeWarnContextUsage(profile *provider.Profile, req llm.Request) bool {
	if s == nil || profile == nil {
		return false
	}
	cw := profile.ContextWindowSize()
	if cw <= 0 {
		return false
	}

	count := llm.EstimateInputTokens(req)
	warn, approxTokens, pct := contextUsageWarning(cw, count.Tokens)
	if !warn {
		return false
	}

	msg := fmt.Sprintf("Context usage at ~%d%% of context window", pct)
	s.emit(events.EventWarning, events.WarningData{
		Message:           msg,
		ApproxTokens:      approxTokens,
		ContextWindowSize: cw,
		Percent:           pct,
	})
	return true
}

// contextUsageWarning decides whether the input tokens for a request have crossed
// the 80%-of-context-window warning threshold. It is the pure decision core lifted
// out of maybeWarnContextUsage: given the model's context window and the estimated
// input token count, it reports whether to warn, the rounded token count, and the
// percentage of the window used. A non-positive context window never warns.
func contextUsageWarning(contextWindow int, estimatedTokens int) (warn bool, approxTokens int, percent int) {
	if contextWindow <= 0 {
		return false, 0, 0
	}
	approx := float64(estimatedTokens)
	threshold := float64(contextWindow) * 0.8
	if approx <= threshold {
		return false, 0, 0
	}
	pct := int(math.Round((approx / float64(contextWindow)) * 100.0))
	return true, int(math.Round(approx)), pct
}

// prepareModelRequest runs the per-round input phases and assembles the llm.Request
// for the round. It snapshots the model inputs (profile, system prompt, tool
// definitions, reasoning effort) under s.mu — keeping the round on one consistent
// model and removing the lock-free read races (PRI-1958 A2/A4) — then applies
// context management and expands history. It records the SystemPrompt, ContextMgmt,
// and HistoryExpand phase timings into t. It never returns an error: the input
// phases only emit warnings.
func (s *Session) prepareModelRequest(ctx context.Context, round int, t *events.RoundTimings) (profile *provider.Profile, sys string, history []llm.Message, req llm.Request, reasoningEffort string) {
	// --- Phase: SystemPrompt ---
	tPhaseStart := s.sclock().Now()

	effortOverride := ""
	if s.taskStore != nil {
		if current, ok := s.taskStore.CurrentInProgress(); ok {
			effortOverride = strings.TrimSpace(current.ReasoningEffort)
		}
	}
	s.mu.Lock()
	profile = s.profile
	sys = s.cachedSystemPrompt
	toolDefs := s.allToolDefinitions(round)
	if effortOverride != "" {
		s.cfg.ReasoningEffort = effortOverride
	}
	reasoningEffort = strings.TrimSpace(s.cfg.ReasoningEffort)
	s.mu.Unlock()

	t.SystemPrompt = time.Since(tPhaseStart)

	// --- Phase: ContextMgmt ---
	tPhaseStart = s.sclock().Now()

	// Copy history once for both context management and message expansion.
	s.mu.Lock()
	historyTurns := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()
	if repaired, repairs := repairOrphanedToolResults(historyTurns); repairs > 0 {
		s.mu.Lock()
		s.history = repaired
		s.mu.Unlock()
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("Recovered %d interrupted tool call(s) before model request", repairs)})
		s.maybeAutoSave()
		s.retryPendingCallerWatchSendsAfterRepair(ctx)
		s.mu.Lock()
		historyTurns = append([]schema.Turn{}, s.history...)
		s.mu.Unlock()
	}

	// Apply context management before each LLM request.
	if s.strategy != nil {
		// Populate compaction metadata so checkpoint/summarize have session context.
		s.contextMgr.Meta = s.buildCompactionMeta()

		// Variant B (forced note): if a compaction is imminent, elicit + pin a
		// must-keep note from the model BEFORE the fold, so erosion-prone facts are
		// re-stamped verbatim rather than decaying through successive summaries.
		s.maybeElicitNoteBeforeCompaction(ctx, historyTurns, len(sys))

		emitFn, flushCompactionHooks := s.compactionEmitFunc(ctx, &historyTurns)
		if err := s.strategy.ManageContext(ctx, &historyTurns, len(sys), emitFn); err != nil {
			s.emit(events.EventWarning, warningDataFromError("context strategy error: "+err.Error(), err))
		}
		flushCompactionHooks()

		s.mu.Lock()
		s.history = historyTurns
		s.mu.Unlock()
	}

	t.ContextMgmt = time.Since(tPhaseStart)

	// --- Phase: HistoryExpand ---
	tPhaseStart = s.sclock().Now()

	// Reuse historyTurns from context management — no redundant copy.
	history = expandHistory(historyTurns)

	t.HistoryExpand = time.Since(tPhaseStart)

	// --- Phase: ToolDefs --- (toolDefs snapshotted with profile/sys above)
	req = s.buildModelRequest(profile, sys, history, toolDefs, reasoningEffort)
	req = s.applyResponsesContinuationAnchorPlanning(ctx, req, historyTurns, profile.SupportsStreaming())
	return profile, sys, history, req, reasoningEffort
}

func (s *Session) applyResponsesContinuationAnchorPlanning(ctx context.Context, req llm.Request, historyTurns []schema.Turn, stream bool) llm.Request {
	if llm.ResponsesContinuationMode(strings.TrimSpace(s.cfg.OpenAIResponsesContinuation)) != llm.ResponsesContinuationAuto {
		if req.HistoryMode == "" {
			req.HistoryMode = llm.HistoryModeFullHistory
		}
		return req
	}

	registry := s.responsesContinuationSupportRegistry()
	if !responsesContinuationRegistryHasEnabledSupport(registry) {
		if req.HistoryMode == "" {
			req.HistoryMode = llm.HistoryModeFullHistory
		}
		return req
	}
	req = s.applyResponsesContinuationShadowEstimate(req)
	if req.ContinuationDiagnostic == "continuation_shadow_estimate_unavailable" {
		req.HistoryMode = llm.HistoryModeFullHistory
		req.PreviousResponseID = ""
		req.ConversationID = ""
		req.Continuation = nil
		req.FullHistoryFallbackMessages = nil
		return responsesContinuationWithInputEstimate(req)
	}

	plan, err := s.client.PlanResponsesContinuation(ctx, req)
	if err != nil {
		req.HistoryMode = llm.HistoryModeFullHistory
		return responsesContinuationWithInputEstimate(req)
	}
	support := llm.ResponsesContinuationSupportFor(registry, plan.EndpointFamily)
	decision := llm.DecideResponsesContinuationForRequest(
		llm.ResponsesContinuationAuto,
		support,
		req,
	)
	if decision.HistoryMode != llm.HistoryModeResponsesDelta {
		req.HistoryMode = llm.HistoryModeFullHistory
		return responsesContinuationWithInputEstimate(req)
	}
	if !plan.ContinuationStorageAllowed &&
		support.StorageShapeProven &&
		plan.StoragePolicyLabel == llm.ResponsesStoragePolicyPublicOpenAINoStore {
		storedReq, _ := llm.ApplyResponsesContinuationStoreOverride(req, llm.ResponsesStoragePolicyPublicOpenAIStore)
		storedPlan, err := s.client.PlanResponsesContinuation(ctx, storedReq)
		if err == nil && storedPlan.ContinuationStorageAllowed {
			req = storedReq
			plan = storedPlan
		}
	}
	if !plan.ContinuationStorageAllowed {
		req.HistoryMode = llm.HistoryModeFullHistory
		return responsesContinuationWithInputEstimate(req)
	}
	if s.responsesContinuationDisabledForPlan(req, plan, stream) {
		return responsesContinuationFullHistoryRequestForPlan(req, plan)
	}

	reservation := reserveResponsesContinuationHistoryBase(historyTurns)
	if !responsesContinuationHistoryBaseStillCurrent(reservation, historyTurns) {
		req.HistoryMode = llm.HistoryModeFullHistory
		return responsesContinuationWithInputEstimate(req)
	}

	candidate, anchorDecision := selectResponsesContinuationAnchorCandidate(s.cfg, historyTurns)
	if anchorDecision.HistoryMode == llm.HistoryModeResponsesDelta &&
		responsesContinuationCandidateMatchesPlan(candidate, plan) {
		fullHistoryFallbackMessages := append([]llm.Message(nil), req.Messages...)
		req, _ = llm.ApplyResponsesContinuationStoreOverride(req, plan.StoragePolicyLabel)
		req.HistoryMode = llm.HistoryModeResponsesDelta
		req.PreviousResponseID = strings.TrimSpace(candidate.Turn.ResponseID)
		req.Messages = responsesContinuationDeltaMessages(req.Messages, candidate.Delta)
		if plan.CanFallbackToChat {
			req.FullHistoryFallbackMessages = fullHistoryFallbackMessages
		}
		req.Continuation = &llm.ContinuationMetadata{
			PreviousResponseIDHash:  candidate.Turn.ResponseIDHash,
			AnchorTurnIndex:         candidate.TurnIndex,
			DeltaTurnCount:          len(candidate.Delta),
			DeltaTurnKinds:          responsesContinuationTurnKinds(candidate.Delta),
			EndpointFamily:          string(plan.EndpointFamily),
			RequestFingerprint:      plan.RequestFingerprint,
			ContextMarker:           responseContextMarkerV1,
			StoragePolicyLabel:      plan.StoragePolicyLabel,
			StorageScopeFingerprint: plan.StorageScopeFingerprint,
			ChatFallbackHistoryLen:  len(req.FullHistoryFallbackMessages),
		}
		return responsesContinuationWithInputEstimate(req)
	}

	return responsesContinuationFullHistoryRequestForPlan(req, plan)
}

func responsesContinuationFullHistoryRequestForPlan(req llm.Request, plan llm.ResponsesContinuationPlan) llm.Request {
	req, _ = llm.ApplyResponsesContinuationStoreOverride(req, plan.StoragePolicyLabel)
	req.HistoryMode = llm.HistoryModeFullHistory
	req.Continuation = &llm.ContinuationMetadata{
		EndpointFamily:          string(plan.EndpointFamily),
		RequestFingerprint:      plan.RequestFingerprint,
		ContextMarker:           responseContextMarkerV1,
		StoragePolicyLabel:      plan.StoragePolicyLabel,
		StorageScopeFingerprint: plan.StorageScopeFingerprint,
	}
	return responsesContinuationWithInputEstimate(req)
}

func (s *Session) applyResponsesContinuationShadowEstimate(req llm.Request) llm.Request {
	shadowReq := req
	shadowReq.HistoryMode = llm.HistoryModeFullHistory
	shadowReq.PreviousResponseID = ""
	shadowReq.ConversationID = ""
	shadowReq.Continuation = nil
	shadowReq.FullHistoryFallbackMessages = nil
	tokens, ok := s.estimateResponsesContinuationShadow(shadowReq)
	if !ok {
		req.HistoryMode = llm.HistoryModeFullHistory
		req.ContinuationDiagnostic = "continuation_shadow_estimate_unavailable"
		return req
	}
	req.FullHistoryInputTokensEstimate = tokens
	return responsesContinuationWithInputEstimate(req)
}

func (s *Session) estimateResponsesContinuationShadow(req llm.Request) (int, bool) {
	if s.cfg.testOnly.responsesContinuationShadowEstimateFunc != nil {
		return s.cfg.testOnly.responsesContinuationShadowEstimateFunc(req)
	}
	count := llm.EstimateInputTokens(req)
	return count.Tokens, count.Tokens > 0
}

func responsesContinuationWithInputEstimate(req llm.Request) llm.Request {
	req.InputTokensEstimate = llm.EstimateInputTokens(req).Tokens
	return req
}

func responsesContinuationCandidateMatchesPlan(candidate responsesContinuationAnchorCandidate, plan llm.ResponsesContinuationPlan) bool {
	return strings.TrimSpace(candidate.Turn.ResponseID) != "" &&
		candidate.Turn.ResponseRequestFingerprint == plan.RequestFingerprint &&
		candidate.Turn.ResponseStorageScopeFingerprint == plan.StorageScopeFingerprint &&
		candidate.Turn.ResponseContextMarker == responseContextMarkerV1
}

func responsesContinuationDeltaMessages(base []llm.Message, deltaTurns []schema.Turn) []llm.Message {
	messages := make([]llm.Message, 0, len(base)+len(deltaTurns))
	for _, msg := range base {
		if msg.Role != llm.RoleSystem && msg.Role != llm.RoleDeveloper {
			break
		}
		messages = append(messages, msg)
	}
	messages = append(messages, expandHistory(deltaTurns)...)
	return messages
}

func responsesContinuationTurnKinds(turns []schema.Turn) []string {
	kinds := make([]string, 0, len(turns))
	for _, turn := range turns {
		kinds = append(kinds, string(turn.Kind))
	}
	return kinds
}

func (s *Session) responsesContinuationSupportRegistry() map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport {
	if s.cfg.testOnly.responsesContinuationSupportRegistry != nil {
		return s.cfg.testOnly.responsesContinuationSupportRegistry
	}
	return llm.DefaultResponsesContinuationSupportRegistry()
}

type responsesContinuationDisabledKey struct {
	Provider                string
	Model                   string
	EndpointFamily          string
	StorageScopeFingerprint string
	StoragePolicyLabel      string
	Stream                  bool
}

func responsesContinuationDisabledKeyForPlan(req llm.Request, plan llm.ResponsesContinuationPlan, stream bool) responsesContinuationDisabledKey {
	return responsesContinuationDisabledKey{
		Provider:                strings.TrimSpace(req.Provider),
		Model:                   strings.TrimSpace(req.Model),
		EndpointFamily:          strings.TrimSpace(string(plan.EndpointFamily)),
		StorageScopeFingerprint: strings.TrimSpace(plan.StorageScopeFingerprint),
		StoragePolicyLabel:      strings.TrimSpace(plan.StoragePolicyLabel),
		Stream:                  stream,
	}
}

func responsesContinuationDisabledKeyForMetadata(req llm.Request, meta *llm.ContinuationMetadata, stream bool) responsesContinuationDisabledKey {
	if meta == nil {
		return responsesContinuationDisabledKey{}
	}
	return responsesContinuationDisabledKey{
		Provider:                strings.TrimSpace(req.Provider),
		Model:                   strings.TrimSpace(req.Model),
		EndpointFamily:          strings.TrimSpace(meta.EndpointFamily),
		StorageScopeFingerprint: strings.TrimSpace(meta.StorageScopeFingerprint),
		StoragePolicyLabel:      strings.TrimSpace(meta.StoragePolicyLabel),
		Stream:                  stream,
	}
}

func (s *Session) responsesContinuationDisabledForPlan(req llm.Request, plan llm.ResponsesContinuationPlan, stream bool) bool {
	key := responsesContinuationDisabledKeyForPlan(req, plan, stream)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.responsesContinuationDisabled[key]
}

func (s *Session) disableResponsesContinuationForRequest(req llm.Request, stream bool) {
	key := responsesContinuationDisabledKeyForMetadata(req, req.Continuation, stream)
	if key.Provider == "" || key.Model == "" || key.EndpointFamily == "" ||
		key.StorageScopeFingerprint == "" || key.StoragePolicyLabel == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.responsesContinuationDisabled == nil {
		s.responsesContinuationDisabled = map[responsesContinuationDisabledKey]bool{}
	}
	s.responsesContinuationDisabled[key] = true
}

func responsesContinuationRegistryHasEnabledSupport(registry map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport) bool {
	for _, support := range registry {
		if support.Enabled {
			return true
		}
	}
	return false
}

// handleModelError reacts to a failed model call. It returns retry=true when the
// turn should retry the round — content-filter recovery, which compacts away the
// offending content (the next request often succeeds) and records that it has
// tried once via *contentFilterRetried. Otherwise it emits the terminal error,
// emits a context-overflow warning when applicable, closes the session on a
// non-retryable llm.Error, leaves the session out of "processing", and returns
// retry=false with the error the turn should fail with — a "provider error"-wrapped
// value (so callers can distinguish a provider failure from agent quiescence; the
// original error is preserved via errors.Unwrap, kata 3xbh) or the raw cancellation.
func (s *Session) handleModelError(ctx context.Context, err error, req llm.Request, contentFilterRetried *bool) (retry bool, ferr error) {
	var le llm.Error
	llmErrNonRetryable := errors.As(err, &le) && !le.Retryable()
	dec := classifyModelError(
		isTurnCancellation(ctx, err),
		llm.Kind(err),
		*contentFilterRetried,
		s.contextMgr != nil,
		llmErrNonRetryable,
	)

	switch dec.Action {
	case modelErrorCancel:
		return false, err
	case modelErrorContentFilterRetry:
		// Content filter recovery: compaction often removes the offending content,
		// allowing the next request to succeed. Try once.
		*contentFilterRetried = true
		s.emit(events.EventWarning, warningDataFromError("Content filter hit — compacting context and retrying", err))
		s.mu.Lock()
		histCopy := append([]schema.Turn{}, s.history...)
		s.mu.Unlock()
		emitFn, flushCompactionHooks := s.compactionEmitFunc(ctx, &histCopy)
		s.contextMgr.ForceCompact(ctx, &histCopy, "", emitFn)
		flushCompactionHooks()
		s.mu.Lock()
		s.history = histCopy
		s.mu.Unlock()
		return true, nil
	}

	errData := errorDataFromError(err)
	errData.Cause = providerCauseFromError(err, req.Model)
	s.emit(events.EventError, errData)

	// Spec: context overflow should emit a warning (no automatic compaction).
	if dec.EmitContextLenWarn {
		s.emit(events.EventWarning, warningDataFromError("Context length exceeded", err))
	}
	// Spec: non-retryable/unrecoverable errors transition the session to closed.
	if dec.CloseSession {
		// Emit the goal's terminal report BEFORE Close() shuts the events
		// channel — a later emit after Close is a silent no-op, so the
		// "told why it stopped" promise would be lost otherwise (spec §2/C11).
		s.terminateGoalOnError(ctx, err)
		s.finishActiveProvenance()
		s.Close()
	}
	// Recoverable LLM errors (retry policy exhausted, stream-ended, timeouts,
	// etc.) bail out of the run loop without compacting or closing — but we still
	// need to leave the session out of "processing" so it doesn't sit active
	// forever from the daemon's /status endpoint (which would disable steer/send
	// with no recovery path short of restarting the daemon, kata r6y9). meta.json
	// flush happens via the deferred flush at the top of processOneInput (kata ztne).
	s.finishProcessingAtBoundary(ctx, SessionIdle)
	return false, fmt.Errorf("provider error: %w", err)
}

// modelErrorAction names the branch handleModelError takes on a failed model call.
type modelErrorAction int

const (
	// modelErrorCancel: the failure is a turn cancellation; propagate it verbatim.
	modelErrorCancel modelErrorAction = iota
	// modelErrorContentFilterRetry: recover from a content filter by compacting
	// away the offending content and retrying the round once.
	modelErrorContentFilterRetry
	// modelErrorTerminal: emit the terminal error and end the round.
	modelErrorTerminal
)

// modelErrorDecision is the outcome of classifyModelError.
type modelErrorDecision struct {
	Action             modelErrorAction
	EmitContextLenWarn bool
	CloseSession       bool
}

// classifyModelError is the pure decision core lifted out of handleModelError. It
// classifies a failed model call into cancel / content-filter-retry / terminal and,
// for the terminal case, whether to warn about context overflow and whether to close
// the session. haveContextMgr gates content-filter recovery (which needs the context
// manager to compact); llmErrNonRetryable reports whether the error is a
// non-retryable llm.Error (closing the session). All side effects stay in the caller.
func classifyModelError(isCancellation bool, kind llm.ErrorKind, contentFilterAlreadyRetried bool, haveContextMgr bool, llmErrNonRetryable bool) modelErrorDecision {
	if isCancellation {
		return modelErrorDecision{Action: modelErrorCancel}
	}
	if kind == llm.KindContentFilter && !contentFilterAlreadyRetried && haveContextMgr {
		return modelErrorDecision{Action: modelErrorContentFilterRetry}
	}
	return modelErrorDecision{
		Action:             modelErrorTerminal,
		EmitContextLenWarn: kind == llm.KindContextLength,
		CloseSession:       llmErrNonRetryable,
	}
}

// recordResponseUsage accumulates the response usage into the context manager and
// records the input token count for pressure calculation. For continuation delta
// requests, pressure uses the larger full-history shadow estimate so local
// compaction decisions still reflect the visible conversation. Anthropic makes
// multiple forward passes for server-side web search, reporting combined usage
// (~2x actual); that inflated baseline is skipped so the previous value stays valid.
func (s *Session) recordResponseUsage(resp llm.Response, req llm.Request) {
	if s.contextMgr == nil {
		return
	}
	s.contextMgr.AddUsage(resp.Usage)

	tokens, record := effectiveRecordedInputTokens(
		resp.Usage,
		req.FullHistoryInputTokensEstimate,
		responseHasServerWebSearch(resp.Message.Content),
	)
	if record {
		s.mu.Lock()
		hLen := len(s.history)
		s.mu.Unlock()
		s.contextMgr.RecordInputTokens(tokens, hLen)
	}
}

// responseHasServerWebSearch reports whether a response's content includes a
// server-side web-search part, whose combined (~2x) usage baseline is skipped for
// pressure calculation.
func responseHasServerWebSearch(content []llm.ContentPart) bool {
	for _, p := range content {
		if p.Kind == llm.ContentWebSearch {
			return true
		}
	}
	return false
}

// effectiveRecordedInputTokens is the pure decision core lifted out of
// recordResponseUsage: it computes the input token count to record for pressure
// (real input + cache reads/writes, floored by the full-history shadow estimate for
// continuation delta requests) and whether to record it at all. A response carrying
// server-side web search is never recorded (its inflated baseline would corrupt the
// previous value), and a non-positive count is not recorded.
func effectiveRecordedInputTokens(usage llm.Usage, fullHistoryEstimate int, hasServerWebSearch bool) (tokens int, record bool) {
	if hasServerWebSearch {
		return 0, false
	}
	totalInput := usage.InputTokens
	if usage.CacheReadTokens != nil {
		totalInput += *usage.CacheReadTokens
	}
	if usage.CacheWriteTokens != nil {
		totalInput += *usage.CacheWriteTokens
	}
	if fullHistoryEstimate > totalInput {
		totalInput = fullHistoryEstimate
	}
	return totalInput, totalInput > 0
}

// emitAssistantResponse emits the assistant text lifecycle events for a round (start
// /delta/end, skipping start/delta when the assistant already streamed) and appends
// the assistant turn to history unless the response was empty without phase metadata.
// It runs under withResponseSideEffects and returns its abort error.
func (s *Session) emitAssistantResponse(ctx context.Context, resp llm.Response, modelResp sessionModelResponse, txt string, skipHistory bool, finalAttempt ModelAttemptMetadata) error {
	return s.withResponseSideEffects(ctx, func() {
		if !modelResp.StreamedAssistant {
			s.emit(events.EventAssistantTextStart, events.AssistantTextStartData{
				Model: resp.Model,
			})
		}
		if !skipHistory {
			s.appendAssistantTurn(resp, finalAttempt)
		}
		if !modelResp.StreamedAssistant && strings.TrimSpace(txt) != "" {
			s.emit(events.EventAssistantTextDelta, events.AssistantTextDeltaData{Delta: txt})
		}
		textEndData := events.AssistantTextEndData{
			Text:         txt,
			Usage:        resp.Usage,
			FinishReason: resp.Finish.Reason,
			Model:        resp.Model,
		}
		if reasoning := resp.ReasoningText(); reasoning != "" {
			textEndData.Reasoning = reasoning
		}
		s.emit(events.EventAssistantTextEnd, textEndData)
		s.mu.Lock()
		s.modelResponses++
		s.mu.Unlock()
	})
}

// buildModelRequest assembles the llm.Request for one round: it lays out the
// system prompt + history into messages (honoring SystemPromptAsUser), then
// applies tools, provider options, reasoning effort, and model metadata.
func (s *Session) buildModelRequest(profile *provider.Profile, sys string, history []llm.Message, toolDefs []llm.ToolDefinition, reasoningEffort string) llm.Request {
	var messages []llm.Message
	if s.cfg.SystemPromptAsUser {
		// Combine system prompt with the first user message into one
		// message so instructions are adjacent to the task. GPT-5.4
		// ignores the instructions parameter and follows user messages.
		if len(history) > 0 && history[0].Role == llm.RoleUser {
			messages = make([]llm.Message, len(history))
			copy(messages, history)
			messages[0] = prependSystemPromptToUserMessage(sys, history[0])
		} else {
			messages = append([]llm.Message{llm.User(sys)}, history...)
		}
	} else {
		messages = append([]llm.Message{llm.System(sys)}, history...)
	}

	req := llm.Request{
		Model:      profile.Model(),
		Provider:   profile.ID(),
		Messages:   messages,
		Tools:      toolDefs,
		ToolChoice: &llm.ToolChoice{Mode: "required"},
		WebSearch:  profile.SupportsWebSearch(),
		AdapterTimeout: &llm.AdapterTimeout{
			Connect:    10 * time.Second,
			Request:    10 * time.Minute,
			StreamRead: 30 * time.Second,
		},
	}
	if opts := profile.ProviderOptions(); opts != nil {
		req.ProviderOptions = opts
	}
	if reasoningEffort != "" && profile.SupportsReasoning() {
		// Clamp to what the active model supports so loop-detector escalation,
		// the --reasoning-effort flag, and the UI selector never send a level the
		// provider rejects (e.g. "xhigh" to a model that tops out at "high").
		// Gated on SupportsReasoning so a model explicitly declared non-reasoning
		// (providers.toml reasoning=false) never gets reasoning_effort on the
		// wire — ClampReasoningEffort passes the value through unchanged when
		// the supported list is empty, which would otherwise leak the session
		// effort straight through and 400.
		v := llm.ClampReasoningEffort(reasoningEffort, profile.ReasoningEffortLevels())
		req.ReasoningEffort = &v
	}
	s.applyModelRequestMetadata(profile, &req)
	return req
}

// callModelWithFallback issues the model call for one round and, on a
// fallback-eligible permanent error, retries each configured fallback model in
// order. It returns the (possibly fallback-updated) request actually used so
// downstream logging reflects the model that answered.
func (s *Session) callModelWithFallback(ctx context.Context, profile *provider.Profile, req llm.Request, requestedEffort string, round int) (sessionModelResponse, llm.Request, ModelAttemptMetadata, error) {
	policy := llm.DefaultRetryPolicy()
	if s.cfg.LLMRetryPolicy != nil {
		policy = *s.cfg.LLMRetryPolicy
	}
	req, attempt := singleAttemptRequestMetadata(req)
	adapterRecorder := newModelAttemptRecorder(attempt.AttemptGroupID)
	callCtx := llm.WithAPILogAttemptContext(ctx, llm.APILogContext{
		SessionID:         s.id,
		Round:             round,
		AttemptGroupID:    attempt.AttemptGroupID,
		AttemptIndex:      attempt.AttemptIndex,
		AttemptCount:      attempt.AttemptCount,
		FinalAttemptCount: attempt.FinalAttemptCount,
		HistoryMode:       attempt.HistoryMode,
		AttemptRecorder:   adapterRecorder.record,
	})
	modelResp, err := s.callModel(callCtx, policy, profile, req)
	if err != nil && shouldRetryResponsesContinuationAsFullHistory(req, err) {
		s.disableResponsesContinuationForRequest(req, profile.SupportsStreaming())
		retryReq := responsesContinuationFullHistoryFallbackRequest(req)
		modelResp, err = s.callModel(callCtx, policy, profile, retryReq)
		if err == nil {
			req = retryReq
			attempt.RequestModel = retryReq.Model
			attempt.HistoryMode = llm.HistoryModeFullHistoryFallback
		}
	}
	// Fallback chain: when the primary model returns a Permanent-class
	// provider error (403/404/422/...) or an endpoint-fallback signal,
	// try each configured fallback in literal order. Stops at the first
	// success; if all fallbacks also fail, the LAST attempt's error is
	// returned to the caller. Retryable errors (429/5xx) burn the
	// existing retry budget on the same model and DO NOT trigger the
	// fallback chain — they are handled by the retry loop inside
	// callModel. Kata cxw8.
	if err != nil && len(s.cfg.ModelFallbacks) > 0 && modelFallbackEligible(err) {
		// requestedEffort is the snapshot taken under lock in prepareModelRequest,
		// before it was clamped to the primary model. Using the snapshot (rather
		// than re-reading live session config) keeps a concurrent runtime effort
		// change from racing/leaking into this request's fallback, and lets a
		// fallback that supports a higher level than the primary use it.
		origEffort := requestedEffort
		for _, fbModel := range s.cfg.ModelFallbacks {
			// validateModelFallbacks already rejected cross-provider fallbacks,
			// so resolveProfileForRef is guaranteed to return the WithModel path
			// here. We call it anyway so the fallback always uses the same
			// resolution logic as SetModel.
			fbProfile, _, _ := s.resolveProfileForRef(profile, fbModel)
			fbReq := responsesContinuationModelFallbackRequest(req)
			fbReq.Model = fbProfile.Model()
			fbReq.Provider = fbProfile.ID()
			if origEffort != "" && fbProfile.SupportsReasoning() {
				// Clamp to the FALLBACK model's levels. WithModel keeps the primary
				// profile's effort levels for some providers (openai/anthropic), so
				// consult the catalog for the fallback model rather than trusting
				// fbProfile's possibly-stale set. LookupModelInfo canonicalizes the
				// "[1m]" suffix, a provider namespace ("anthropic/…" from
				// openrouter-anthropic), and dated snapshots, so a qualified or
				// dated fallback still resolves real levels.
				//
				// Gated on SupportsReasoning so a fallback explicitly declared
				// non-reasoning never gets reasoning_effort on the wire (see the
				// same guard on the primary path above).
				fbLevels := fbProfile.ReasoningEffortLevels()
				// Explicit providers.toml thinking_levels / reasoning config is
				// authoritative, and ollama's local model names never resolve
				// against the upstream catalog — only consult it when the
				// profile's levels were derived (and might be stale
				// primary-model state).
				if fbProfile.CatalogEffortFallbackEligible() {
					if cat := llm.EmbeddedModelCatalog(); cat != nil {
						if mi := cat.LookupModelInfo(fbProfile.Model()); mi != nil && len(mi.ReasoningEffortLevels) > 0 {
							fbLevels = mi.ReasoningEffortLevels
						}
					}
				}
				clamped := llm.ClampReasoningEffort(origEffort, fbLevels)
				fbReq.ReasoningEffort = &clamped
			} else {
				fbReq.ReasoningEffort = nil
			}
			fbReq.WebSearch = fbProfile.SupportsWebSearch()
			fbReq.ProviderOptions = fbProfile.ProviderOptions()
			fbReq.PromptCacheKey = ""
			fbReq.PromptCacheRetention = ""
			s.applyModelRequestMetadata(profile, &fbReq)
			modelResp, err = s.callModel(callCtx, policy, fbProfile, fbReq)
			if err == nil {
				// Reflect the model that actually answered in the
				// request used for downstream logging (transcript,
				// EventAssistantTextStart fallback path, etc).
				req = fbReq
				attempt.RequestModel = fbReq.Model
				attempt.HistoryMode = llm.HistoryModeFullHistory
				break
			}
		}
	}
	if err != nil {
		attempt.AdapterAttempts = adapterRecorder.attempts()
		return modelResp, req, attempt, err
	}
	attempt = completeAttemptMetadata(attempt, modelResp.Response)
	attempt.AdapterAttempts = adapterRecorder.attempts()
	return modelResp, req, attempt, nil
}

func shouldRetryResponsesContinuationAsFullHistory(req llm.Request, err error) bool {
	if req.HistoryMode != llm.HistoryModeResponsesDelta {
		return false
	}
	if strings.TrimSpace(req.PreviousResponseID) == "" {
		return false
	}
	if len(req.FullHistoryFallbackMessages) == 0 {
		return false
	}
	var llmErr llm.Error
	if !errors.As(err, &llmErr) {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(llmErr.ErrorCode()))
	if code == "previous_response_not_found" {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(llmErr.Error()))
	return (strings.Contains(message, "previous_response") && strings.Contains(message, "not found")) ||
		(strings.Contains(message, "previous response") && (strings.Contains(message, "not found") || strings.Contains(message, "expired")))
}

func responsesContinuationFullHistoryFallbackRequest(req llm.Request) llm.Request {
	fallbackReq := req
	fallbackReq.HistoryMode = llm.HistoryModeFullHistoryFallback
	fallbackReq.Messages = append([]llm.Message(nil), req.FullHistoryFallbackMessages...)
	fallbackReq.PreviousResponseID = ""
	fallbackReq.ConversationID = ""
	fallbackReq.Continuation = nil
	fallbackReq.FullHistoryFallbackMessages = nil
	return fallbackReq
}

func responsesContinuationModelFallbackRequest(req llm.Request) llm.Request {
	fallbackReq := req
	if req.HistoryMode == llm.HistoryModeResponsesDelta {
		fallbackReq.HistoryMode = llm.HistoryModeFullHistory
		if len(req.FullHistoryFallbackMessages) > 0 {
			fallbackReq.Messages = append([]llm.Message(nil), req.FullHistoryFallbackMessages...)
		}
		fallbackReq.PreviousResponseID = ""
		fallbackReq.ConversationID = ""
		fallbackReq.Continuation = nil
		fallbackReq.FullHistoryFallbackMessages = nil
	}
	return fallbackReq
}

// logAPICall records one round's request/response (or error) to the transcript.
func (s *Session) logAPICall(round int, roundStart time.Time, llmLatency time.Duration, sys string, historyLen int, req llm.Request, resp llm.Response, err error, attempt ModelAttemptMetadata) {
	if s.transcript != nil {
		if len(attempt.AdapterAttempts) > 0 {
			for _, adapterAttempt := range attempt.AdapterAttempts {
				s.appendAdapterAttemptAPICall(round, roundStart, llmLatency, sys, historyLen, adapterAttempt)
			}
			return
		}
		apiCall := transcript.APICall{
			Round:                  round,
			AttemptGroupID:         attempt.AttemptGroupID,
			AttemptIndex:           attempt.AttemptIndex,
			AttemptCount:           attempt.AttemptCount,
			FinalAttemptCount:      attempt.FinalAttemptCount,
			HistoryMode:            attempt.HistoryMode,
			PreviousResponseIDHash: attempt.PreviousResponseIDHash,
			ConversationIDHash:     attempt.ConversationIDHash,
			Timestamp:              roundStart.UTC().Format(time.RFC3339),
			LatencyMs:              llmLatency.Milliseconds(),
			SystemPrompt:           sys,
			ContextHistoryTurns:    historyLen,
			SystemPromptBytes:      len(sys),
			Request:                llm.BuildAPILogRequest(req),
		}
		if err != nil {
			apiCall.Error = err.Error()
			setAPICallDiagnostic(&apiCall, err)
		} else {
			apiCall.Response = buildTranscriptAPILogResponse(resp, attempt.ResponseIDHash)
		}
		if werr := s.transcript.AppendAPICall(apiCall); werr != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", werr)})
		}
	}
}

func (s *Session) appendAdapterAttemptAPICall(round int, roundStart time.Time, llmLatency time.Duration, sys string, historyLen int, attempt llm.AdapterAttemptRecord) {
	apiCall := transcript.APICall{
		Round:               round,
		AttemptGroupID:      attempt.AttemptGroupID,
		AttemptIndex:        attempt.AttemptIndex,
		AttemptCount:        attempt.AttemptCount,
		FinalAttemptCount:   attempt.FinalAttemptCount,
		HistoryMode:         attempt.HistoryMode,
		Timestamp:           roundStart.UTC().Format(time.RFC3339),
		LatencyMs:           llmLatency.Milliseconds(),
		SystemPrompt:        sys,
		ContextHistoryTurns: historyLen,
		SystemPromptBytes:   len(sys),
		Request:             llm.BuildAPILogRequest(attempt.Request),
	}
	if attempt.Error != nil {
		apiCall.Error = attempt.Error.Error()
		setAPICallDiagnostic(&apiCall, attempt.Error)
	} else if attempt.Response != nil {
		apiCall.Response = buildTranscriptAPILogResponse(*attempt.Response, "")
	}
	if werr := s.transcript.AppendAPICall(apiCall); werr != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", werr)})
	}
}

func buildTranscriptAPILogResponse(resp llm.Response, idHash string) *llm.APILogResponse {
	var endpoint string
	if resp.Raw != nil {
		if v, ok := resp.Raw["endpoint_url"].(string); ok {
			endpoint = v
		}
	}
	if idHash == "" && resp.Raw != nil {
		if v, ok := resp.Raw["id_hash"].(string); ok {
			idHash = v
		}
	}
	return &llm.APILogResponse{
		ID:            resp.ID,
		IDHash:        idHash,
		Model:         resp.Model,
		FinishReason:  resp.Finish.Reason,
		TextLength:    len(resp.Text()),
		ToolCallCount: len(resp.ToolCalls()),
		Usage:         resp.Usage,
		EndpointURL:   endpoint,
		Raw:           resp.Raw,
	}
}

// expandHistory flattens conversation turns into the per-message slice sent to
// the model: steering/checkpoint/summary turns pass through as-is, and
// aggregated tool-result turns are expanded into individual tool-result messages.
func expandHistory(historyTurns []schema.Turn) []llm.Message {
	history := make([]llm.Message, 0, len(historyTurns))
	for _, t := range historyTurns {
		if t.Kind == schema.TurnSteering {
			history = append(history, t.Message)
			continue
		}
		if t.Kind == schema.TurnToolResults {
			// Expand aggregated tool results into individual messages.
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					history = append(history, llm.ToolResultNamed(
						p.ToolResult.ToolCallID,
						p.ToolResult.Name,
						p.ToolResult.Content,
						p.ToolResult.IsError,
					))
				}
			}
			continue
		}
		if t.Kind == schema.TurnCheckpoint || t.Kind == schema.TurnSummary {
			// Compaction turns carry user-role messages; include as-is.
			history = append(history, t.Message)
			continue
		}
		history = append(history, t.Message)
	}
	return history
}
