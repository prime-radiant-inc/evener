package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// ModelAttemptMetadata records continuation, endpoint, and attempt-grouping
// details captured across one model call (including any fallback retries) for
// the attempted or successful semantic assistant turn.
type ModelAttemptMetadata struct {
	HistoryMode    llm.HistoryMode
	EndpointFamily string
	// Protocol is the wire protocol of the attempt that answered, read back
	// from the round's attempt group. It is empty when an override served the
	// call, because an override makes no transport attempt.
	Protocol                string
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

// effectiveReasoningEffort decides the reasoning effort for one round without
// touching the session's configured value: the current in-progress task's
// override wins when set (even when lower — deliberately cheap tasks are a
// feature), except while a loop-detect escalation is active, where the
// higher-ranked of the configured and override efforts wins so the "your
// reasoning effort has been increased" steering never lies.
func effectiveReasoningEffort(cfg, override string, escalated bool) string {
	if override == "" {
		return cfg
	}
	if escalated && llm.ReasoningEffortRank(cfg) > llm.ReasoningEffortRank(override) {
		return cfg
	}
	return override
}

// prepareModelRequestWithError runs the per-round input phases and assembles the
// llm.Request for the round. It snapshots the model inputs (profile, system
// prompt, tool definitions, reasoning effort) under s.mu — keeping the round on
// one consistent model and removing the lock-free read races (PRI-1958 A2/A4) —
// then applies context management and expands history. It records the
// SystemPrompt, ContextMgmt, and HistoryExpand phase timings into t.
//
// It also returns the full-history message list a planned continuation delta was
// cut from. That list is the round's, not the request's: the retry after a
// rejected anchor rebuilds from it, and nothing on the wire ever carries it.
func (s *Session) prepareModelRequestWithError(ctx context.Context, round int, t *events.RoundTimings) (profile *provider.Profile, sys string, history []llm.Message, req llm.Request, fullHistory []llm.Message, reasoningEffort string, err error) {
	if err := s.flushPendingDelegateDeliveries(); err != nil {
		return nil, "", nil, llm.Request{}, nil, "", err
	}
	// --- Phase: SystemPrompt ---
	tPhaseStart := s.sclock().Now()

	effortOverride := ""
	// A resumed session may have a persisted task store without having loaded
	// it in this process yet. The once-guarded accessor also avoids racing a
	// task_list mutation that initializes the store concurrently.
	store := s.getOrCreateTaskStore()
	if current, ok := store.CurrentInProgress(); ok {
		effortOverride = normalizeTaskEffort(strings.TrimSpace(current.ReasoningEffort))
	}
	s.mu.Lock()
	profile = s.profile
	sys = s.cachedSystemPrompt
	toolDefs := s.allToolDefinitions(round)
	// The task override applies to this round only; s.cfg.ReasoningEffort keeps
	// the session's configured effort so it is restored when the task ends.
	reasoningEffort = effectiveReasoningEffort(strings.TrimSpace(s.cfg.ReasoningEffort), effortOverride, s.loopEffortEscalated)
	s.mu.Unlock()
	if s.contextMgr != nil {
		s.contextMgr.SetProfile(profile)
	}

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
	managedLen := preManageLen

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
		managedLen = len(historyTurns)

		s.mu.Lock()
		// Context management works on a snapshot without holding s.mu. Preserve
		// turns accepted while it ran so publishing the managed prefix cannot
		// erase durable steering from the next model request.
		if len(s.history) > preManageLen {
			historyTurns = append(historyTurns, s.history[preManageLen:]...)
		}
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
	} else if shrink := preManageLen - managedLen; shrink > 0 {
		s.turnHistoryBaseline -= shrink
		if s.turnHistoryBaseline < 0 {
			s.turnHistoryBaseline = 0
		}
	}
	inFlightFrom := s.turnHistoryBaseline
	s.mu.Unlock()

	// Reuse historyTurns from context management — no redundant copy.
	scope := replayScope{
		Instance:       profile.ID(),
		Model:          profile.Model(),
		Protocol:       profile.Protocol(),
		InFlightFrom:   inFlightFrom,
		protocolOf:     s.instanceProtocol,
		canonicalModel: func(model string) string { return s.canonicalModelID(profile.ID(), model) },
	}
	if lease, ok := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease); ok && s.delegateController != nil {
		claim, claimErr := s.delegateController.BeginModelRequest(lease)
		if claimErr != nil {
			return nil, "", nil, llm.Request{}, nil, "", claimErr
		}
		snapshot := s.delegateModelHistorySnapshot()
		history, claimErr = s.delegateController.CompleteModelRequest(claim, snapshot, scope)
		if claimErr != nil {
			_ = s.delegateController.AbortModelRequest(claim)
			return nil, "", nil, llm.Request{}, nil, "", claimErr
		}
	} else {
		history = expandHistory(historyTurns, scope)
	}

	t.HistoryExpand = time.Since(tPhaseStart)

	// --- Phase: ToolDefs --- (toolDefs snapshotted with profile/sys above)
	req = s.buildModelRequest(profile, sys, history, toolDefs, reasoningEffort)
	req = s.attachFullHistoryInputEstimate(req, historyTurns, len(sys))
	var budget llm.TokenBudget
	if req, budget, err = budgetModelDispatchRequestWithBudget(profile, req); err != nil {
		return profile, sys, history, req, nil, reasoningEffort, err
	}
	initialBudget := budget
	req, fullHistory = s.applyResponsesContinuationAnchorPlanning(ctx, req, historyTurns, profile.SupportsStreaming())
	if req, budget, err = budgetModelDispatchRequestWithBudget(profile, req); err != nil {
		return profile, sys, history, req, fullHistory, reasoningEffort, err
	}
	// Admission runs before and after continuation planning because the latter
	// can introduce a larger full-history shadow. Report the two-stage result as
	// one reduction from the caller's original request to the final allocation.
	if initialBudget.LimitedOutput {
		budget.RequestedOutput = initialBudget.RequestedOutput
		budget.LimitedOutput = true
	}
	s.warnOutputReduction(profile, budget)
	// Stage the mid-turn attention this round's request presents. The guard
	// inside is the single gate, whichever path built the history; staging
	// follows anchor planning because credit belongs to what the request
	// actually carries.
	s.stageRootDelegateAttentionCoverage(req, historyTurns)
	return profile, sys, history, req, fullHistory, reasoningEffort, nil
}

// applyResponsesContinuationAnchorPlanning returns the request to dispatch and,
// when it planned a continuation delta, the full-history message list the delta
// was cut from, for the retry a rejected anchor forces.
func (s *Session) applyResponsesContinuationAnchorPlanning(ctx context.Context, req llm.Request, historyTurns []schema.Turn, stream bool) (llm.Request, []llm.Message) {
	if llm.ResponsesContinuationMode(strings.TrimSpace(s.cfg.OpenAIResponsesContinuation)) != llm.ResponsesContinuationAuto {
		if req.HistoryMode == "" {
			req.HistoryMode = llm.HistoryModeFullHistory
		}
		return req, nil
	}

	registry := s.responsesContinuationSupportRegistry()
	if !responsesContinuationRegistryHasEnabledSupport(registry) {
		if req.HistoryMode == "" {
			req.HistoryMode = llm.HistoryModeFullHistory
		}
		return req, nil
	}
	req = s.applyResponsesContinuationShadowEstimate(req)
	if req.ContinuationDiagnostic == "continuation_shadow_estimate_unavailable" {
		req.HistoryMode = llm.HistoryModeFullHistory
		req.PreviousResponseID = ""
		req.ConversationID = ""
		req.Continuation = nil
		return responsesContinuationWithInputEstimate(req), nil
	}

	plan, err := s.client.PlanResponsesContinuation(ctx, req)
	if err != nil {
		req.HistoryMode = llm.HistoryModeFullHistory
		return responsesContinuationWithInputEstimate(req), nil
	}
	support := llm.ResponsesContinuationSupportFor(registry, plan.EndpointFamily)
	decision := llm.DecideResponsesContinuationForRequest(
		llm.ResponsesContinuationAuto,
		support,
		req,
	)
	if decision.HistoryMode != llm.HistoryModeResponsesDelta {
		req.HistoryMode = llm.HistoryModeFullHistory
		return responsesContinuationWithInputEstimate(req), nil
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
		return responsesContinuationWithInputEstimate(req), nil
	}
	if s.responsesContinuationDisabledForPlan(req, plan, stream) {
		return responsesContinuationFullHistoryRequestForPlan(req, plan), nil
	}

	reservation := reserveResponsesContinuationHistoryBase(historyTurns)
	historyCurrent := responsesContinuationHistoryBaseStillCurrent(reservation, historyTurns)
	if s.cfg.testOnly.responsesContinuationHistoryCurrentFunc != nil {
		historyCurrent = s.cfg.testOnly.responsesContinuationHistoryCurrentFunc(reservation, historyTurns)
	}
	if !historyCurrent {
		req.HistoryMode = llm.HistoryModeFullHistory
		return responsesContinuationWithInputEstimate(req), nil
	}

	candidate, anchorDecision := selectResponsesContinuationAnchorCandidate(s.cfg, historyTurns)
	if anchorDecision.HistoryMode == llm.HistoryModeResponsesDelta &&
		responsesContinuationCandidateMatchesPlan(candidate, plan) {
		fullHistory := append([]llm.Message(nil), req.Messages...)
		req, _ = llm.ApplyResponsesContinuationStoreOverride(req, plan.StoragePolicyLabel)
		req.HistoryMode = llm.HistoryModeResponsesDelta
		req.PreviousResponseID = strings.TrimSpace(candidate.Turn.ResponseID)
		req.Messages = responsesContinuationDeltaMessages(req.Messages, candidate.Delta)
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
		}
		return responsesContinuationWithInputEstimate(req), fullHistory
	}

	return responsesContinuationFullHistoryRequestForPlan(req, plan), nil
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
	tokens, ok := s.estimateResponsesContinuationShadow(shadowReq)
	if !ok {
		req.HistoryMode = llm.HistoryModeFullHistory
		req.ContinuationDiagnostic = "continuation_shadow_estimate_unavailable"
		return req
	}
	if tokens > req.FullHistoryInputTokensEstimate {
		req.FullHistoryInputTokensEstimate = tokens
	}
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

func responsesContinuationFullHistoryWithInputEstimate(req llm.Request) llm.Request {
	req.FullHistoryInputTokensEstimate = 0
	req = responsesContinuationWithInputEstimate(req)
	req.FullHistoryInputTokensEstimate = req.InputTokensEstimate
	return req
}

// attachFullHistoryInputEstimate carries the context manager's conservative
// estimate into the request before any Responses continuation decision. The
// manager may have an exact provider measurement for the visible conversation;
// retaining the larger of that and the request-local estimate keeps a delta from
// hiding the full history from token admission and pressure accounting.
func (s *Session) attachFullHistoryInputEstimate(req llm.Request, history []schema.Turn, sysPromptChars int) llm.Request {
	if s.contextMgr == nil {
		return req
	}
	// EstimateUsage falls back to a local char/4 estimate when no provider
	// measurement exists. Continuation planning supplies its own deterministic
	// full-history shadow in that case; only carry the manager estimate when it
	// is grounded in an actual provider-reported baseline.
	if s.contextMgr.LastInputTokens() <= 0 {
		return req
	}
	estimate := s.contextMgr.EstimateUsage(history, sysPromptChars).Used
	if estimate > req.FullHistoryInputTokensEstimate {
		req.FullHistoryInputTokensEstimate = estimate
	}
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
// A cancellation propagates verbatim, but not before settling any partial the
// round had already streamed (settleInterruptedRound).
// The terminal path emits the failure, terminates any active goal, and settles the
// open session at idle before returning a "provider error"-wrapped value (so
// callers can distinguish a provider failure from agent quiescence; the original
// error is preserved via errors.Unwrap, kata 3xbh). The outer lifecycle error
// boundary remains an idempotent compatibility tail for this provider-owned path.
func (s *Session) handleModelError(ctx context.Context, err error, req llm.Request, contentFilterRetried *bool, contextWarningEmitted bool) (retry bool, ferr error) {
	dec := classifyModelError(
		isTurnCancellation(ctx, err),
		llm.Kind(err),
		*contentFilterRetried,
		s.contextMgr != nil,
	)

	switch dec.Action {
	case modelErrorCancel:
		// The interrupt ends the turn, but the partial this round already
		// streamed is real work; preserve it with interrupt wording that makes
		// no provider-failure claim. Reaching here means the round produced no
		// answer of its own, so the draft cannot duplicate a delivered response.
		s.settleInterruptedRound()
		return false, err
	case modelErrorContentFilterRetry:
		// Content filter recovery: compaction often removes the offending content,
		// allowing the next request to succeed. Try once.
		*contentFilterRetried = true
		s.emit(events.EventWarning, warningDataFromError("Content filter hit — compacting context and retrying", err))
		s.forceCompactForModelRecovery(ctx)
		return true, nil
	}

	// Settle before the failure marker so the model-visible turns this round
	// salvaged sit above it, in the order a reader (and the next request's
	// history) needs: draft, then the steering that explains it, then the
	// presentational failure.
	s.settleFailedRound(err)

	errData := errorDataFromError(err)
	errData.Cause = providerCauseFromError(err, req.Model)
	s.emitTurnFailure(errData)

	// The lifecycle emits the context-disagreement recovery warning before its
	// bounded compaction. Retain this compatibility warning for any terminal
	// context path that reaches this handler without that lifecycle emission.
	if dec.EmitContextLenWarn && !contextWarningEmitted {
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
			Provider:     resp.Provider,
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
		return sandbox.ProviderWebAllowedUnderNetOff(profile.ProviderID())
	}
	return true
}

// buildModelRequest assembles the llm.Request for one round: it lays out the
// system prompt + history into messages (honoring SystemPromptAsUser), then
// applies tools, provider options, reasoning effort, and model metadata.
func (s *Session) buildModelRequest(profile *provider.Profile, sys string, history []llm.Message, toolDefs []llm.ToolDefinition, reasoningEffort string) llm.Request {
	var messages []llm.Message
	if s.cfg.SystemPromptAsUser {
		// Combine system prompt with the first user-role message into one
		// message, since GPT-5.4 ignores the instructions parameter and
		// follows user messages. That first message is usually the task, but
		// maybeAppendEnvironmentContext's harness-injected ENVIRONMENT turn
		// can land ahead of it — the system prompt then combines with that
		// instead, still leading with instructions in a user-role message.
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
		Model:    profile.Model(),
		Provider: profile.ID(),
		Messages: messages,
		Tools:    toolDefs,
		// Ask for a tool call; never force one. A forcing tool_choice leaves a
		// model that cannot honor it with no legal way to stop, and evener targets
		// arbitrary gateways and models where that capability is unknowable in
		// advance. The result-tool contract is enforced in software instead —
		// decideNoToolCalls steers bare text back, and a delegate that ends
		// without communicating gets communicateNudge.
		ToolChoice: &llm.ToolChoice{Mode: "auto"},
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
	req.ReasoningEffort = resolveRequestEffort(reasoningEffort, profile.SupportsReasoning(), profile.ReasoningEffortLevels(), profile.DefaultReasoningEffort())
	s.applyModelRequestMetadata(&req)
	return req
}

// defaultReasoningEffort is what a reasoning model runs at when nothing
// configured it and no model data states a better default.
const defaultReasoningEffort = "medium"

// resolveRequestEffort is the one rule for the effort a request carries, shared
// by the primary and fallback paths:
//
//   - A model that does not reason (catalog, live /models, or providers.toml
//     reasoning=false) never gets an effort, even if one is configured;
//     ClampReasoningEffort would pass it through an empty level list and the
//     provider would 400.
//   - An explicit off ("none") is carried on every reasoning model, never
//     replaced by a default and never clamped into a tier. Which models can
//     be told off, and how it is spelled, is the adapters' call (spec §8.4):
//     they send it where the row's ladder lists an off level and the dialect
//     has a value for one, and omit the control otherwise. Carrying it is
//     also what keeps it distinguishable from "nothing configured", without
//     which a mandatory-thinking row's builder default reads an off as unset
//     and switches thinking back on.
//   - A configured effort is clamped to the model's levels so loop-detector
//     escalation, the --reasoning-effort flag, and the UI selector never send
//     a tier the model rejects.
//   - Nothing configured: the model's own stated default (adaptive Claude runs
//     at high), else medium, clamped. Leaving the field out lets the provider
//     pick, and a gateway-fronted glm-5.3 spent 25k reasoning tokens on one
//     turn that way; mandatory-thinking models reject a reasoning-less
//     request outright.
func resolveRequestEffort(configured string, supportsReasoning bool, levels []string, modelDefault string) *string {
	if !supportsReasoning {
		return nil
	}
	// Normalize here too: config entry points normalize on the way in, but a
	// value that slipped past them ("None", a stored alias) must still be an
	// off, not an unknown level a provider 400s on.
	effort := llm.NormalizeReasoningEffort(configured)
	if effort == "" {
		effort = modelDefault
	}
	if effort == "" {
		effort = defaultReasoningEffort
	}
	if effort == llm.ReasoningEffortNone {
		// Off, whether the user or the model's data said so, carried as the
		// canonical lowercase "none" whatever the model's ladder holds. The
		// adapters decide the wire: the dialects with a real off value send
		// it for a model whose ladder lists the off level, everything else
		// omits the control. Carrying it rather than returning nil is what
		// keeps a mandatory-thinking row's backstop from reading the off as
		// "nothing configured" and switching thinking back on.
		return &effort
	}
	v := llm.ClampReasoningEffort(effort, levels)
	return &v
}

// callModelWithFallback issues the model call for one round and, on a
// fallback-eligible permanent error, retries each configured fallback model in
// order. It returns the (possibly fallback-updated) request actually used so
// downstream logging reflects the model that answered.
func (s *Session) callModelWithFallback(ctx context.Context, profile *provider.Profile, req llm.Request, fullHistory []llm.Message, requestedEffort string, _ int) (sessionModelResponse, llm.Request, ModelAttemptMetadata, error) {
	previewCalls := map[string]struct{}{}
	defer func() {
		if recovered := recover(); recovered != nil {
			s.resetCommunicatePreviews(previewCalls)
			panic(recovered)
		}
	}()
	rememberPreviews := func(resp sessionModelResponse) {
		for _, callID := range resp.CommunicatePreviewCallIDs {
			previewCalls[callID] = struct{}{}
		}
	}
	withPreviews := func(resp sessionModelResponse) sessionModelResponse {
		resp.CommunicatePreviewCallIDs = sortedPreviewCallIDs(previewCalls)
		return resp
	}
	policy := llm.DefaultRetryPolicy()
	if s.cfg.LLMRetryPolicy != nil {
		policy = *s.cfg.LLMRetryPolicy
	}
	req, attempt := singleAttemptRequestMetadata(req)
	group := llm.NewAPIAttemptGroup(attempt.AttemptGroupID)
	callCtx := llm.WithAPIAttemptGroup(ctx, group)
	// Each callModel invocation is one retry group; the round's recorder keeps
	// them all so settlement can see the largest partial the round produced,
	// whichever group produced it. Groups are appended whole (never held by
	// pointer across an append) so a later group's growth cannot relocate an
	// earlier one under a caller holding it.
	recorder := s.roundSalvageRecorder()
	var primaryRecord groupRecord
	modelResp, err := s.callModel(callCtx, policy, profile, req, &primaryRecord)
	rememberPreviews(modelResp)
	recorder.Groups = append(recorder.Groups, primaryRecord)
	// Context disagreement belongs to the outer lifecycle, which owns the one
	// force-compaction/rebuild retry. Do not let the permanent-error fallback
	// chain route around a request that should be retried against the same model
	// after compaction.
	if err != nil && isProviderContextLengthError(err) {
		group.SettleResult(callCtx, err)
		return withPreviews(modelResp), req, attempt, err
	}
	// len(fullHistory) > 0 keeps the retry's precondition next to the retry:
	// the rebuilt request sends fullHistory, so a delta paired with an empty
	// one would dispatch a message-less round instead of declining.
	if err != nil && len(fullHistory) > 0 && shouldRetryResponsesContinuationAsFullHistory(req, err) {
		s.disableResponsesContinuationForRequest(req, profile.SupportsStreaming())
		retryReq := responsesContinuationFullHistoryFallbackRequest(req, fullHistory)
		var budget llm.TokenBudget
		retryReq, budget, budgetErr := budgetModelDispatchRequestWithBudget(profile, retryReq)
		if budgetErr == nil {
			s.warnOutputReduction(profile, budget)
		}
		// Group-transition reset: the primary group's error usually arrives
		// open-phase (nothing streamed), but an in-band mid-stream
		// "response.failed" can leave real salvage on primaryRecord — this
		// retry is still a new group streaming over whatever the primary
		// already showed. See the matching guard before the fallback loop
		// below for the full rationale.
		if _, from := recorder.BestSalvage(); from != nil {
			s.emit(events.EventAssistantTextReset, events.AssistantTextResetData{})
		}
		s.resetCommunicatePreviews(previewCalls)
		var recoveryRecord groupRecord
		if budgetErr != nil {
			err = budgetErr
		} else {
			modelResp, err = s.callModel(callCtx, policy, profile, retryReq, &recoveryRecord)
		}
		rememberPreviews(modelResp)
		recorder.Groups = append(recorder.Groups, recoveryRecord)
		if err == nil {
			req = retryReq
			attempt.RequestModel = retryReq.Model
			attempt.HistoryMode = llm.HistoryModeFullHistoryFallback
		}
	}
	if err != nil && isProviderContextLengthError(err) {
		group.SettleResult(callCtx, err)
		return withPreviews(modelResp), req, attempt, err
	}
	// Fallback chain: when the primary model returns a Permanent-class
	// provider error (403/404/422/..., including an endpoint that cannot
	// serve the model at all), try each configured fallback in literal order. Stops at the first
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
		// requestedEffort is the snapshot taken under lock in prepareModelRequestWithError,
		// before it was clamped to the primary model. Using the snapshot (rather
		// than re-reading live session config) keeps a concurrent runtime effort
		// change from racing/leaking into this request's fallback, and lets a
		// fallback that supports a higher level than the primary use it.
		origEffort := requestedEffort
		for _, fbModel := range s.cfg.ModelFallbacks {
			// validateModelFallbacks keeps a cross-instance entry whose surface
			// matches the session's (spec §7.5), so this is where the session
			// resolver first runs for such an entry — it is no longer the
			// guaranteed WithModel projection it was when every slashed entry
			// was refused. An entry the resolver cannot answer for right now is
			// skipped so the rest of the chain still gets its turn.
			fbProfile, _, resolveErr := s.resolveProfileForRef(profile, fbModel)
			if resolveErr != nil {
				s.emit(events.EventWarning, warningDataFromError(
					fmt.Sprintf("model_fallbacks entry %q could not be resolved; skipping it", fbModel), resolveErr))
				continue
			}
			fbReq, ok := responsesContinuationModelFallbackRequest(req, fullHistory)
			if !ok {
				break
			}
			fbReq.Model = fbProfile.Model()
			fbReq.Provider = fbProfile.ID()
			// The same rule as the primary path, against the FALLBACK model's
			// own facts: fbProfile is resolved from the fallback reference, so
			// its ladder and stated default are the fallback model's, not the
			// primary's.
			fbReq.ReasoningEffort = resolveRequestEffort(origEffort, fbProfile.SupportsReasoning(), fbProfile.ReasoningEffortLevels(), fbProfile.DefaultReasoningEffort())
			fbReq.WebSearch = s.providerWebSearchEnabled(fbProfile)
			fbReq.ProviderOptions = fbProfile.ProviderOptions()
			s.applyModelRequestMetadata(&fbReq)
			fbReq = responsesContinuationFullHistoryWithInputEstimate(fbReq)
			var budget llm.TokenBudget
			fbReq, budget, budgetErr := budgetModelDispatchRequestWithBudget(fbProfile, fbReq)
			if budgetErr == nil {
				s.warnOutputReduction(fbProfile, budget)
			}
			// Group-transition reset (spec: "Group-transition reset"): OnReset
			// only discards partial output between attempts WITHIN one callModel
			// invocation, so a chain walk away from a group that already
			// delivered partial output leaves that partial rendered above this
			// fallback's output. Recomputed from the recorder rather than a
			// local flag, so a later fallback that also streams and dies gets
			// its own reset before the next one runs.
			if _, from := recorder.BestSalvage(); from != nil {
				s.emit(events.EventAssistantTextReset, events.AssistantTextResetData{})
			}
			s.resetCommunicatePreviews(previewCalls)
			var fallbackRecord groupRecord
			if budgetErr != nil {
				err = budgetErr
			} else {
				modelResp, err = s.callModel(callCtx, policy, fbProfile, fbReq, &fallbackRecord)
			}
			rememberPreviews(modelResp)
			recorder.Groups = append(recorder.Groups, fallbackRecord)
			if err != nil && isProviderContextLengthError(err) {
				req = fbReq
				group.SettleResult(callCtx, err)
				return withPreviews(modelResp), req, attempt, err
			}
			if err == nil {
				// Reflect the model that actually answered in the
				// request used for downstream logging (transcript,
				// EventAssistantTextStart fallback path, etc).
				req = fbReq
				if s.contextMgr != nil {
					s.contextMgr.SetProfile(fbProfile)
				}
				attempt.RequestModel = fbReq.Model
				attempt.HistoryMode = llm.HistoryModeFullHistory
				break
			}
			// An unhealthy verdict indicts the provider's endpoint and
			// transport, which every same-provider fallback entry shares, so no
			// remaining entry can route around it. Stopping here also preserves
			// the verdict as the round's terminal error instead of letting a
			// later entry's failure win.
			if _, ok := errors.AsType[*llm.ProviderUnhealthyError](err); ok {
				break
			}
		}
	}
	if err != nil {
		group.SettleResult(callCtx, err)
		return withPreviews(modelResp), req, attempt, err
	}
	attempt = completeAttemptMetadata(attempt, modelResp.Response)
	attempt.Protocol = group.Protocol()
	group.SettleResult(callCtx, nil)
	return withPreviews(modelResp), req, attempt, nil
}

func shouldRetryResponsesContinuationAsFullHistory(req llm.Request, err error) bool {
	if req.HistoryMode != llm.HistoryModeResponsesDelta {
		return false
	}
	if strings.TrimSpace(req.PreviousResponseID) == "" {
		return false
	}
	// An unhealthy verdict settles the round from any group (spec: component 2),
	// and the recovery re-call is another full retry group against the endpoint
	// RetryStream just indicted. Checked before the llm.Error match because the
	// verdict unwraps to its last attempt error, so an anchor-missing attempt
	// error would otherwise be read straight through the verdict.
	if _, ok := errors.AsType[*llm.ProviderUnhealthyError](err); ok {
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

// responsesContinuationFullHistoryFallbackRequest rebuilds the delta request as
// the full history the round kept for it, for the one retry a rejected anchor
// earns.
func responsesContinuationFullHistoryFallbackRequest(req llm.Request, fullHistory []llm.Message) llm.Request {
	fallbackReq := req
	fallbackReq.HistoryMode = llm.HistoryModeFullHistoryFallback
	fallbackReq.Messages = append([]llm.Message(nil), fullHistory...)
	fallbackReq.MaxTokens = nil
	fallbackReq.InputTokensEstimate = 0
	fallbackReq.PreviousResponseID = ""
	fallbackReq.ConversationID = ""
	fallbackReq.Continuation = nil
	fallbackReq = responsesContinuationFullHistoryWithInputEstimate(fallbackReq)
	return fallbackReq
}

// responsesContinuationModelFallbackRequest un-anchors a request for a
// different model. A continuation delta can be relabeled as full history only
// when the round retained that full history; otherwise it refuses construction.
func responsesContinuationModelFallbackRequest(req llm.Request, fullHistory []llm.Message) (llm.Request, bool) {
	if req.HistoryMode == llm.HistoryModeResponsesDelta && len(fullHistory) == 0 {
		return llm.Request{}, false
	}
	fallbackReq := req
	fallbackReq.HistoryMode = llm.HistoryModeFullHistory
	if len(fullHistory) > 0 {
		fallbackReq.Messages = append([]llm.Message(nil), fullHistory...)
	}
	fallbackReq.MaxTokens = nil
	fallbackReq.InputTokensEstimate = 0
	fallbackReq.PreviousResponseID = ""
	fallbackReq.ConversationID = ""
	fallbackReq.Continuation = nil
	fallbackReq = responsesContinuationFullHistoryWithInputEstimate(fallbackReq)
	return fallbackReq, true
}

func budgetModelDispatchRequest(profile *provider.Profile, req llm.Request) (llm.Request, error) {
	budgeted, _, err := budgetModelDispatchRequestWithBudget(profile, req)
	return budgeted, err
}

func budgetModelDispatchRequestWithBudget(profile *provider.Profile, req llm.Request) (llm.Request, llm.TokenBudget, error) {
	resolved := profile.Resolved()
	if window := profile.ContextWindowSize(); window > 0 {
		resolved.Caps.ContextWindow = new(window)
	}
	return llm.ApplyTokenBudget(req, resolved)
}

func (s *Session) warnOutputReduction(profile *provider.Profile, budget llm.TokenBudget) {
	if s == nil || profile == nil || !budget.LimitedOutput {
		return
	}
	s.emit(events.EventWarning, warningDataFromError(fmt.Sprintf(
		"Output allocation reduced for %s/%s: requested=%d admitted=%d",
		profile.ID(), profile.Model(), budget.RequestedOutput, budget.AdmittedOutput,
	), nil))
}

func isLocalContextBudgetError(err error) bool {
	var budgetErr *llm.ContextBudgetError
	return errors.As(err, &budgetErr)
}

func isLocalContextCompactionError(err error) bool {
	var budgetErr *llm.ContextBudgetError
	if !errors.As(err, &budgetErr) {
		return false
	}
	return budgetErr.Limit == "max_input" || budgetErr.Limit == "context_window"
}

func isProviderContextLengthError(err error) bool {
	return !isLocalContextBudgetError(err) && llm.Kind(err) == llm.KindContextLength
}

// forceCompactForModelRecovery rebuilds the session history from a fresh copy
// after an admission or provider context failure. The next loop iteration must
// run all request phases again; it must never reuse the rejected request.
func (s *Session) forceCompactForModelRecovery(ctx context.Context) {
	if s.contextMgr == nil {
		return
	}
	s.contextMgr.Meta = s.buildCompactionMeta()
	s.mu.Lock()
	history := append([]schema.Turn(nil), s.history...)
	s.mu.Unlock()
	preCompactLen := len(history)
	compactionCtx, emitFn, flush := s.compactionEmitFunc(ctx, &history)
	s.contextMgr.ForceCompact(compactionCtx, &history, "", emitFn)
	flush()
	s.mu.Lock()
	if shrink := preCompactLen - len(history); shrink > 0 {
		s.turnHistoryBaseline -= shrink
		if s.turnHistoryBaseline < 0 {
			s.turnHistoryBaseline = 0
		}
	}
	s.history = history
	s.mu.Unlock()
	s.maybeAutoSave()
}

// replayScope carries the outgoing target identity that decides whether
// provider/model-scoped content — thinking/redacted_thinking and web_search raw
// blocks — from completed prior turns may replay after a mid-session model
// switch (spec N4). A zero replayScope (empty Protocol) disables all
// filtering, so history expansion for a target that keeps its own builder
// guards (openai Responses, openai-chat) or for the Responses-continuation
// delta path is byte-identical to before this rule existed.
type replayScope struct {
	Instance string // outgoing instance (req.Provider)
	Model    string // outgoing requested model (req.Model)
	Protocol string // outgoing wire protocol; empty ⇒ no filtering

	// InFlightFrom is the history index of the first turn belonging to the
	// in-flight turn. Turns at or after it are exempt from filtering: a
	// same-protocol fallback round earlier in the current turn keeps its
	// thinking (N4 exempts in-flight rounds and the fallback path).
	InFlightFrom int

	// protocolOf resolves a stored turn's instance to the protocol it speaks
	// today, for turns written before ResponseProtocol existed; "" means the
	// instance is no longer configured and the turn is not eligible (spec §7.5).
	protocolOf func(instance string) string
	// canonicalModel canonicalizes a model id for the ResponseModel fallback
	// comparison. Nil ⇒ raw (trimmed) string comparison.
	canonicalModel func(string) string
}

// active reports whether the scope enforces the N4 replay-provenance rules. An
// empty protocol means "expand without filtering".
func (rs replayScope) active() bool { return strings.TrimSpace(rs.Protocol) != "" }

// producerProtocol is the wire protocol that produced a stored turn: the one
// recorded on the turn, or — for a turn written before ResponseProtocol
// existed — the protocol its instance speaks today.
func (rs replayScope) producerProtocol(t schema.Turn) string {
	if p := strings.TrimSpace(t.ResponseProtocol); p != "" {
		return p
	}
	if rs.protocolOf == nil {
		return ""
	}
	return rs.protocolOf(t.ResponseProvider)
}

// thinkingReplayEligible reports whether a completed prior turn's
// thinking/redacted_thinking blocks may replay into the outgoing request.
// Empty provenance (legacy transcripts) is always eligible. An anthropic
// target requires an exact (instance, requested model) match — the requested
// model taken from ResponseRequestModel, or canonicalized ResponseModel when
// the request-model field is empty (closes G12). google and openai-responses
// targets require the same instance: google's builder must replay prior
// tool-call thought signatures regardless of model, and openai Responses
// carries an opaque encrypted_content blob that only its issuing deployment
// can decrypt (a cross-deployment replay yields "Encrypted content is not
// supported"). Every other target keeps its own builder guard, so expansion
// never strips thinking for it.
func (rs replayScope) thinkingReplayEligible(t schema.Turn) bool {
	if strings.TrimSpace(t.ResponseProvider) == "" {
		return true
	}
	producer := rs.producerProtocol(t)
	switch rs.Protocol {
	case registry.ProtocolAnthropic:
		return producer == rs.Protocol && rs.Instance == t.ResponseProvider && rs.requestedModelMatches(t)
	case registry.ProtocolGoogle, registry.ProtocolOpenAIResponses:
		return producer == rs.Protocol && rs.Instance == t.ResponseProvider
	default:
		return true
	}
}

// requestedModelMatches compares the outgoing requested model against the
// producing turn's, in requested-model space (ResponseRequestModel), falling
// back to canonicalized ResponseModel when the request-model field is empty.
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
// producing protocol must match the target's — across protocols the raw
// payload is foreign JSON and is dropped (G13).
func (rs replayScope) webSearchReplayEligible(t schema.Turn) bool {
	if strings.TrimSpace(t.ResponseProvider) == "" {
		return true
	}
	return rs.producerProtocol(t) == rs.Protocol
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
		case schema.TurnHookCompleted, schema.TurnAttentionResolution:
			// Presentational telemetry and attention resolution can appear inside
			// a tool round without interrupting it.
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

// instanceProtocol resolves an instance name to the protocol it speaks
// today; "" when it is no longer configured. It resolves the instance rather
// than reading the credentialed instance list so a turn produced by a
// curated implicit provider still reports its protocol (spec §5.2).
func (s *Session) instanceProtocol(name string) string {
	if s.client == nil {
		return ""
	}
	res, err := s.client.Registry().ResolveInstance(name)
	if err != nil {
		return ""
	}
	return res.Protocol
}

// canonicalModelID canonicalizes a model ref through the registry so a
// requested alias and a provider-reported dated snapshot compare equal in the
// ResponseModel provenance fallback. instance names the instance the ref
// belongs to; unknown refs compare by trimmed string.
//
// An alias row folds onto its target, which is what canonicalizes a "[1m]"
// ref: the curated overlay carries claude-sonnet-4-5[1m] as an alias of
// claude-sonnet-4-5, and both address the same deployment. Otherwise the
// matched row's id is the canonical one, which folds a dated snapshot the
// catalog does not carry as its own row onto the base row the registry
// matched it against. A dated snapshot that IS its own row folds onto the
// undated row when the instance serves one — applied after the alias fold, so
// every spelling of one deployment lands on the same id.
func (s *Session) canonicalModelID(instance, model string) string {
	trimmed := strings.TrimSpace(model)
	if s.client == nil {
		return trimmed
	}
	res, err := s.client.Resolve(instance + "/" + trimmed)
	if err != nil {
		return trimmed
	}
	id := strings.TrimSpace(res.Model.AliasOf)
	if id == "" {
		id = res.Model.ID
	}
	if id == "" {
		id = trimmed
	}
	if base := registry.StripDatedSuffix(id); base != id {
		if row, err := s.client.Resolve(instance + "/" + base); err == nil && !row.Synthesized {
			return base
		}
	}
	return id
}
