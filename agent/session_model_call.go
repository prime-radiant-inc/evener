package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

// ModelAttemptMetadata records continuation, endpoint, and attempt-grouping
// details captured across one model call (including any fallback retries) for
// the successful semantic assistant turn.
type ModelAttemptMetadata struct {
	HistoryMode             llm.HistoryMode
	EndpointFamily          string
	EndpointURL             string
	RequestModel            string
	RequestFingerprint      string
	StorageScopeFingerprint string
	ContextMarker           string
	AttemptGroupID          string
	PreviousResponseIDHash  string
	ConversationIDHash      string
	ResponseIDHash          string
	StoragePolicyLabel      string
}

func singleAttemptRequestMetadata(req llm.Request) (llm.Request, ModelAttemptMetadata) {
	if req.HistoryMode == "" {
		req.HistoryMode = llm.HistoryModeFullHistory
	}
	meta := ModelAttemptMetadata{
		HistoryMode:    req.HistoryMode,
		RequestModel:   req.Model,
		AttemptGroupID: newAttemptGroupID(),
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
	return identifier.MustNewAgentCallID()
}

func completeAttemptMetadata(meta ModelAttemptMetadata, resp llm.Response) ModelAttemptMetadata {
	if resp.Raw != nil {
		if endpoint, _ := resp.Raw["endpoint_url"].(string); endpoint != "" {
			meta.EndpointURL = llm.SanitizeEndpointURL(endpoint)
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
	if s.cfg.testOnly.modelCallContextWindowFunc != nil {
		cw = s.cfg.testOnly.modelCallContextWindowFunc(profile)
	}
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

	preManageLen := len(historyTurns)

	// Apply context management before each LLM request.
	if s.strategy != nil {
		// Populate compaction metadata so checkpoint/summarize have session context.
		s.contextMgr.Meta = s.buildCompactionMeta()

		// Variant B (forced note): if a compaction is imminent, elicit + pin a
		// must-keep note from the model BEFORE the fold, so erosion-prone facts are
		// re-stamped verbatim rather than decaying through successive summaries.
		s.maybeElicitNoteBeforeCompaction(ctx, historyTurns, len(sys))

		compactionCtx, emitFn, flushCompactionHooks := s.compactionEmitFunc(ctx, &historyTurns)
		if err := s.strategy.ManageContext(compactionCtx, &historyTurns, len(sys), emitFn); err != nil {
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

	// Establish the in-flight-turn boundary (spec N4 exemption): capture it at
	// round 0, then track any mid-turn compaction that folds prior turns so the
	// boundary keeps pointing at the current turn's first appended turn.
	s.mu.Lock()
	if round == 0 {
		s.turnHistoryBaseline = len(historyTurns)
	} else if shrink := preManageLen - len(historyTurns); shrink > 0 {
		s.turnHistoryBaseline -= shrink
		if s.turnHistoryBaseline < 0 {
			s.turnHistoryBaseline = 0
		}
	}
	inFlightFrom := s.turnHistoryBaseline
	s.mu.Unlock()

	// Reuse historyTurns from context management — no redundant copy.
	history = expandHistory(historyTurns, replayScope{
		Provider:       profile.ID(),
		Model:          profile.Model(),
		BehaviorTag:    profile.BehaviorTag(),
		InFlightFrom:   inFlightFrom,
		behaviorTagOf:  s.client.BehaviorTagOf,
		canonicalModel: canonicalModelID,
	})

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
	historyCurrent := responsesContinuationHistoryBaseStillCurrent(reservation, historyTurns)
	if s.cfg.testOnly.responsesContinuationHistoryCurrentFunc != nil {
		historyCurrent = s.cfg.testOnly.responsesContinuationHistoryCurrentFunc(reservation, historyTurns)
	}
	if !historyCurrent {
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
	// Responses-continuation delta path (openai target): the Responses builder
	// keeps its own reasoning guard, so expansion filters nothing here.
	messages = append(messages, expandHistory(deltaTurns, replayScope{})...)
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
// tried once via *contentFilterRetried. Provider retryability governs request
// retries elsewhere; every remaining provider failure is terminal for this turn.
// The terminal path emits the failure, terminates any active goal, and settles the
// open session at idle before returning a "provider error"-wrapped value (so
// callers can distinguish a provider failure from agent quiescence; the original
// error is preserved via errors.Unwrap, kata 3xbh). The outer lifecycle error
// boundary remains an idempotent compatibility tail for this provider-owned path.
func (s *Session) handleModelError(ctx context.Context, err error, req llm.Request, contentFilterRetried *bool) (retry bool, ferr error) {
	dec := classifyModelError(
		isTurnCancellation(ctx, err),
		llm.Kind(err),
		*contentFilterRetried,
		s.contextMgr != nil,
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
		compactionCtx, emitFn, flushCompactionHooks := s.compactionEmitFunc(ctx, &histCopy)
		s.contextMgr.ForceCompact(compactionCtx, &histCopy, "", emitFn)
		flushCompactionHooks()
		s.mu.Lock()
		s.history = histCopy
		s.mu.Unlock()
		return true, nil
	}

	errData := errorDataFromError(err)
	errData.Cause = providerCauseFromError(err, req.Model)
	s.emitTurnFailure(errData)

	// Spec: context overflow should emit a warning (no automatic compaction).
	if dec.EmitContextLenWarn {
		s.emit(events.EventWarning, warningDataFromError("Context length exceeded", err))
	}
	s.terminateGoalOnError(ctx, err)
	s.finishProcessingAtBoundary(ctx, SessionIdle)
	return false, fmt.Errorf("provider error: %w", err)
}

// isProviderTerminalError identifies the wrapper returned by handleModelError
// (and any underlying provider error it carries) so the outer drain loop does
// not repeat goal termination or the idle-boundary provenance transition.
func isProviderTerminalError(err error) bool {
	var providerErr llm.Error
	return strings.HasPrefix(err.Error(), "provider error: ") && errors.As(err, &providerErr)
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
}

// classifyModelError is the pure decision core lifted out of handleModelError. It
// classifies a failed model call into cancel / content-filter-retry / terminal and,
// for the terminal case, whether to warn about context overflow. haveContextMgr
// gates content-filter recovery (which needs the context manager to compact). All
// side effects stay in the caller.
func classifyModelError(isCancellation bool, kind llm.ErrorKind, contentFilterAlreadyRetried bool, haveContextMgr bool) modelErrorDecision {
	if isCancellation {
		return modelErrorDecision{Action: modelErrorCancel}
	}
	if kind == llm.KindContentFilter && !contentFilterAlreadyRetried && haveContextMgr {
		return modelErrorDecision{Action: modelErrorContentFilterRetry}
	}
	return modelErrorDecision{
		Action:             modelErrorTerminal,
		EmitContextLenWarn: kind == llm.KindContextLength,
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
	var persistErr error
	if err := s.withResponseSideEffects(ctx, func() {
		if !modelResp.StreamedAssistant {
			s.emit(events.EventAssistantTextStart, events.AssistantTextStartData{
				Model: resp.Model,
			})
		}
		if !skipHistory {
			persistErr = s.appendAssistantTurn(resp, finalAttempt)
			if persistErr != nil {
				return
			}
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
	}); err != nil {
		return err
	}
	return persistErr
}

// providerWebSearchEnabled reports whether provider-native web search — the
// server-side web egress the provider runs for the model — may be requested for
// this profile. It starts from the profile's own capability and, when the session
// is sandboxed with network egress off, additionally requires that the provider's
// web egress is allowed under net=off. That decision comes from the
// provider-capability registry, which fails closed for unknown providers, so
// net=off can never be silently false through provider-native web (a path the user
// cannot inspect). A non-sandboxed session (wrapper nil) passes the profile
// capability through unchanged — byte-identical to before the flag exists.
func (s *Session) providerWebSearchEnabled(profile *provider.Profile) bool {
	if !profile.SupportsWebSearch() {
		return false
	}
	if w := s.sandboxWrapper(); w != nil && !w.Policy().Network {
		return sandbox.ProviderWebAllowedUnderNetOff(profile.BehaviorTag())
	}
	return true
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
		WebSearch:  s.providerWebSearchEnabled(profile),
		AdapterTimeout: &llm.AdapterTimeout{
			Connect:    10 * time.Second,
			Request:    10 * time.Minute,
			StreamRead: 30 * time.Second,
		},
	}
	if opts := profile.ProviderOptions(); opts != nil {
		req.ProviderOptions = opts
	}
	// Request the model's full output budget. Adapters default liberally too
	// (defense in depth), but filling it here makes the cap uniform across
	// providers and immune to any one adapter's default.
	if mt := profile.MaxOutputTokens(); mt > 0 {
		req.MaxTokens = &mt
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
	group := llm.NewAPIAttemptGroup(attempt.AttemptGroupID)
	callCtx := llm.WithAPIAttemptGroup(ctx, group)
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
	//
	// The one exception, and it is not a softening of that rule: a server-set
	// Retry-After longer than the backoff cap, which the retry loop refuses to
	// wait out and returns immediately with zero retries spent (kata r128).
	// Nothing burned a budget there, so "handled by the retry loop" is false for
	// that error alone. See modelFallbackEligible.
	if err != nil && len(s.cfg.ModelFallbacks) > 0 && modelFallbackEligible(err, policy) {
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
			fbReq.WebSearch = s.providerWebSearchEnabled(fbProfile)
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
		group.SettleResult(callCtx, err)
		return modelResp, req, attempt, err
	}
	attempt = completeAttemptMetadata(attempt, modelResp.Response)
	group.SettleResult(callCtx, nil)
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

// replayScope carries the outgoing target identity that decides whether
// provider/model-scoped content — thinking/redacted_thinking and web_search raw
// blocks — from completed prior turns may replay after a mid-session model
// switch (spec N4). A zero replayScope (empty BehaviorTag) disables all
// filtering, so history expansion for a target that keeps its own builder
// guards (openai Responses, openai-compat) or for the Responses-continuation
// delta path is byte-identical to before this rule existed.
type replayScope struct {
	Provider    string // outgoing instance id (req.Provider)
	Model       string // outgoing requested model (req.Model)
	BehaviorTag string // outgoing behavior tag; empty ⇒ no filtering

	// InFlightFrom is the history index of the first turn belonging to the
	// in-flight turn. Turns at or after it are exempt from filtering: a
	// same-behavior-tag fallback round earlier in the current turn keeps its
	// thinking (N4 exempts in-flight rounds and the fallback path).
	InFlightFrom int

	// behaviorTagOf resolves a stored turn's ResponseProvider (instance id) to
	// its behavior tag for the web_search family check. Nil ⇒ the producing
	// family is treated as unknown and web_search is compared same-provider.
	behaviorTagOf func(string) string
	// canonicalModel canonicalizes a model id for the ResponseModel fallback
	// comparison. Nil ⇒ raw (trimmed) string comparison.
	canonicalModel func(string) string
}

// active reports whether the scope enforces the N4 replay-provenance rules. An
// empty behavior tag means "expand without filtering".
func (rs replayScope) active() bool { return strings.TrimSpace(rs.BehaviorTag) != "" }

// builderFamily maps a behavior tag to the wire-format family whose request
// builder serves it. web_search raw blocks are foreign JSON across families, and
// the thinking rule is scoped per family (exact-model for anthropic, same-
// provider for google). An unrecognized tag maps to itself so an unknown
// provider never silently shares a family with a known one.
//
// Sibling tags collapse to one family because they emit the *same* raw block
// shape on the wire: kimi-anthropic/openrouter-anthropic/minimax all speak the
// anthropic wire format, so an anthropic-produced web_search raw block is
// byte-compatible when replayed into any of them (and vice versa). Grouping
// them here is what lets those cross-tag hops replay web_search verbatim
// instead of dropping it as foreign JSON.
func builderFamily(tag string) string {
	switch strings.TrimSpace(tag) {
	case "anthropic", "kimi-anthropic", "openrouter-anthropic", "minimax":
		return "anthropic"
	case "google":
		return "google"
	case "openai":
		return "openai"
	case "openai-compatible", "kimi", "glm", "zai", "deepseek", "together", "ollama", "openrouter":
		return "compat"
	default:
		return strings.TrimSpace(tag)
	}
}

// thinkingReplayEligible reports whether a completed prior turn's
// thinking/redacted_thinking blocks may replay into the outgoing request.
// Empty provenance (legacy transcripts) is always eligible. anthropic-family
// targets require an exact (instance id, requested model) match — the requested
// model taken from ResponseRequestModel, or catalog-canonicalized ResponseModel
// when the request-model field is empty (closes G12). google targets require
// only the same instance id (its builder must replay prior tool-call thought
// signatures regardless of model). Every other target keeps its own builder
// guard, so expansion never strips thinking for it.
func (rs replayScope) thinkingReplayEligible(t schema.Turn) bool {
	if strings.TrimSpace(t.ResponseProvider) == "" {
		return true
	}
	switch builderFamily(rs.BehaviorTag) {
	case "anthropic":
		return rs.Provider == t.ResponseProvider && rs.requestedModelMatches(t)
	case "google":
		return rs.Provider == t.ResponseProvider
	default:
		return true
	}
}

// requestedModelMatches compares the outgoing requested model against the
// producing turn's, in requested-model space (ResponseRequestModel), falling
// back to catalog-canonicalized ResponseModel when the request-model field is
// empty.
func (rs replayScope) requestedModelMatches(t schema.Turn) bool {
	if rm := strings.TrimSpace(t.ResponseRequestModel); rm != "" {
		return strings.TrimSpace(rs.Model) == rm
	}
	return rs.canonicalize(rs.Model) == rs.canonicalize(t.ResponseModel)
}

func (rs replayScope) canonicalize(model string) string {
	if rs.canonicalModel != nil {
		return rs.canonicalModel(model)
	}
	return strings.TrimSpace(model)
}

// webSearchReplayEligible reports whether a completed prior turn's web_search
// raw blocks may replay verbatim. Empty provenance is eligible; otherwise the
// producing behavior-tag family must match the target family (anthropic ↔
// anthropic, openai ↔ openai) — cross-family the raw payload is foreign JSON and
// is dropped (G13).
func (rs replayScope) webSearchReplayEligible(t schema.Turn) bool {
	if strings.TrimSpace(t.ResponseProvider) == "" {
		return true
	}
	var producerTag string
	if rs.behaviorTagOf != nil {
		producerTag = rs.behaviorTagOf(t.ResponseProvider)
	} else if rs.Provider == t.ResponseProvider {
		producerTag = rs.BehaviorTag
	} else {
		producerTag = t.ResponseProvider
	}
	return builderFamily(producerTag) == builderFamily(rs.BehaviorTag)
}

// projectTurnMessage returns t.Message with provider/model-scoped content
// filtered per the N4 rules for the scope. In-flight turns and inactive scopes
// pass through unchanged (identical to legacy expansion). tool_call/tool_result
// and text/image content is never touched.
func (rs replayScope) projectTurnMessage(t schema.Turn, inFlight bool) llm.Message {
	if !rs.active() || inFlight {
		return t.Message
	}
	keepThinking := rs.thinkingReplayEligible(t)
	keepWebSearch := rs.webSearchReplayEligible(t)
	if keepThinking && keepWebSearch {
		return t.Message
	}
	filtered := make([]llm.ContentPart, 0, len(t.Message.Content))
	for _, p := range t.Message.Content {
		switch p.Kind {
		case llm.ContentThinking, llm.ContentRedThinking:
			if !keepThinking {
				continue
			}
		case llm.ContentWebSearch:
			if !keepWebSearch {
				continue
			}
		}
		filtered = append(filtered, p)
	}
	msg := t.Message
	msg.Content = filtered
	return msg
}

// expandHistory flattens conversation turns into the per-message slice sent to
// the model: steering/checkpoint/summary turns pass through as-is, aggregated
// tool-result turns are expanded into individual tool-result messages, and
// TurnModelSwitch markers are dropped (presentational only, never sent).
// Steering recorded while a tool is running remains chronological in semantic
// history, but is deferred here until all results for that assistant turn have
// been projected. Providers require tool results to immediately follow the
// assistant message that requested them.
// scope enforces the N4 cross-model replay-provenance rules: after a switch,
// thinking and web_search blocks a target cannot accept are stripped from the
// outgoing request while staying untouched in the stored transcript.
func expandHistory(historyTurns []schema.Turn, scope replayScope) []llm.Message {
	history := make([]llm.Message, 0, len(historyTurns))
	pendingToolCalls := map[string]struct{}{}
	var deferredSteering []llm.Message

	flushDeferredSteering := func() {
		history = append(history, deferredSteering...)
		deferredSteering = nil
	}
	startToolRound := func(message llm.Message) {
		clear(pendingToolCalls)
		for _, call := range assistantToolCalls(message) {
			pendingToolCalls[call.ID] = struct{}{}
		}
	}
	resolveToolResults := func(message llm.Message) {
		for _, part := range message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil {
				delete(pendingToolCalls, part.ToolResult.ToolCallID)
			}
		}
		if len(pendingToolCalls) == 0 {
			flushDeferredSteering()
		}
	}
	endToolRound := func() {
		flushDeferredSteering()
		clear(pendingToolCalls)
	}

	for i, t := range historyTurns {
		inFlight := scope.active() && i >= scope.InFlightFrom
		switch t.Kind {
		case schema.TurnSteering:
			if len(pendingToolCalls) > 0 {
				deferredSteering = append(deferredSteering, t.Message)
			} else {
				history = append(history, t.Message)
			}
		case schema.TurnEnvironment:
			// Environment context only ever lands at a turn boundary, so no
			// mid-tool-round deferral: pass the message straight through.
			history = append(history, t.Message)
		case schema.TurnToolResults:
			// Expand aggregated tool results into individual messages.
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					history = append(history, llm.Message{
						Role:       llm.RoleTool,
						ToolCallID: p.ToolResult.ToolCallID,
						Content: []llm.ContentPart{{
							Kind: llm.ContentToolResult,
							ToolResult: &llm.ToolResultData{
								ToolCallID:     p.ToolResult.ToolCallID,
								Name:           p.ToolResult.Name,
								Content:        p.ToolResult.Content,
								IsError:        p.ToolResult.IsError,
								PrevalOnly:     p.ToolResult.PrevalOnly,
								DurationMS:     p.ToolResult.DurationMS,
								ToolState:      p.ToolResult.ToolState,
								ImageData:      p.ToolResult.ImageData,
								ImageMediaType: p.ToolResult.ImageMediaType,
							},
						}},
					})
				}
			}
			resolveToolResults(t.Message)
		case schema.TurnTool:
			history = append(history, scope.projectTurnMessage(t, inFlight))
			resolveToolResults(t.Message)
		case schema.TurnAssistant:
			endToolRound()
			message := scope.projectTurnMessage(t, inFlight)
			history = append(history, message)
			startToolRound(message)
		case schema.TurnCheckpoint, schema.TurnSummary:
			// Compaction turns carry user-role messages; include as-is.
			endToolRound()
			history = append(history, t.Message)
		case schema.TurnHookCompleted:
			// Presentational telemetry can appear inside a tool round.
		case schema.TurnModelSwitch, schema.TurnFailure:
			// Persisted switch/failure markers are presentational only and are
			// never sent to the model.
			endToolRound()
		default:
			endToolRound()
			history = append(history, scope.projectTurnMessage(t, inFlight))
		}
	}
	flushDeferredSteering()
	return history
}

// canonicalModelID canonicalizes a model ref through the embedded catalog so a
// requested alias and a provider-reported dated snapshot of the same model
// compare equal in the ResponseModel provenance fallback. Unknown refs compare
// by trimmed string.
func canonicalModelID(model string) string {
	trimmed := strings.TrimSpace(model)
	if cat := llm.EmbeddedModelCatalog(); cat != nil {
		if mi := cat.LookupModelInfo(trimmed); mi != nil && mi.ID != "" {
			return mi.ID
		}
	}
	return trimmed
}
