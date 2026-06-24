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
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

type ModelAttemptMetadata struct {
	HistoryMode             llm.HistoryMode
	EndpointFamily          string
	EndpointURL             string
	RequestModel            string
	RequestFingerprint      string
	StorageScopeFingerprint string
	ContextMarker           string
	AttemptIndex            int
	AttemptCount            int
	FinalAttemptCount       *int
	PreviousResponseIDHash  string
	ConversationIDHash      string
	ResponseIDHash          string
	StoragePolicyLabel      string
}

func singleAttemptRequestMetadata(req llm.Request) (llm.Request, ModelAttemptMetadata) {
	if req.HistoryMode == "" {
		req.HistoryMode = llm.HistoryModeFullHistory
	}
	finalCount := 1
	meta := ModelAttemptMetadata{
		HistoryMode:       req.HistoryMode,
		RequestModel:      req.Model,
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
	approxTokens := float64(count.Tokens)
	threshold := float64(cw) * 0.8
	if approxTokens <= threshold {
		return false
	}

	pct := int(math.Round((approxTokens / float64(cw)) * 100.0))
	msg := fmt.Sprintf("Context usage at ~%d%% of context window", pct)
	s.emit(events.EventWarning, events.WarningData{
		Message:           msg,
		ApproxTokens:      int(math.Round(approxTokens)),
		ContextWindowSize: cw,
		Percent:           pct,
	})
	return true
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
	tPhaseStart := time.Now()

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
	tPhaseStart = time.Now()

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
	tPhaseStart = time.Now()

	// Reuse historyTurns from context management — no redundant copy.
	history = expandHistory(historyTurns)

	t.HistoryExpand = time.Since(tPhaseStart)

	// --- Phase: ToolDefs --- (toolDefs snapshotted with profile/sys above)
	req = s.buildModelRequest(profile, sys, history, toolDefs, reasoningEffort)
	req = s.applyResponsesContinuationAnchorPlanning(ctx, req, historyTurns)
	return profile, sys, history, req, reasoningEffort
}

func (s *Session) applyResponsesContinuationAnchorPlanning(ctx context.Context, req llm.Request, historyTurns []schema.Turn) llm.Request {
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

	plan, err := s.client.PlanResponsesContinuation(ctx, req)
	if err != nil {
		req.HistoryMode = llm.HistoryModeFullHistory
		return req
	}
	support := llm.ResponsesContinuationSupportFor(registry, plan.EndpointFamily)
	decision := llm.DecideResponsesContinuationForRequest(
		llm.ResponsesContinuationAuto,
		support,
		req,
	)
	if decision.HistoryMode != llm.HistoryModeResponsesDelta {
		req.HistoryMode = llm.HistoryModeFullHistory
		return req
	}
	if plan.CanFallbackToChat && len(req.FullHistoryFallbackMessages) == 0 {
		req.HistoryMode = llm.HistoryModeFullHistory
		return req
	}
	if !plan.ContinuationStorageAllowed {
		req.HistoryMode = llm.HistoryModeFullHistory
		return req
	}

	reservation := reserveResponsesContinuationHistoryBase(historyTurns)
	if !responsesContinuationHistoryBaseStillCurrent(reservation, historyTurns) {
		req.HistoryMode = llm.HistoryModeFullHistory
		return req
	}

	candidate, anchorDecision := selectResponsesContinuationAnchorCandidate(s.cfg, historyTurns)
	if anchorDecision.HistoryMode == llm.HistoryModeResponsesDelta &&
		responsesContinuationCandidateMatchesPlan(candidate, plan) {
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
		return req
	}

	req, _ = llm.ApplyResponsesContinuationStoreOverride(req, plan.StoragePolicyLabel)
	req.HistoryMode = llm.HistoryModeFullHistory
	req.Continuation = &llm.ContinuationMetadata{
		EndpointFamily:          string(plan.EndpointFamily),
		RequestFingerprint:      plan.RequestFingerprint,
		ContextMarker:           responseContextMarkerV1,
		StoragePolicyLabel:      plan.StoragePolicyLabel,
		StorageScopeFingerprint: plan.StorageScopeFingerprint,
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
	if isTurnCancellation(ctx, err) {
		return false, err
	}

	// Content filter recovery: compaction often removes the offending content,
	// allowing the next request to succeed. Try once.
	if llm.Kind(err) == llm.KindContentFilter && !*contentFilterRetried && s.contextMgr != nil {
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
	if llm.Kind(err) == llm.KindContextLength {
		s.emit(events.EventWarning, warningDataFromError("Context length exceeded", err))
	}
	// Spec: non-retryable/unrecoverable errors transition the session to closed.
	var le llm.Error
	if errors.As(err, &le) && !le.Retryable() {
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

// recordResponseUsage accumulates the response usage into the context manager and
// records the exact input token count for pressure calculation. Anthropic makes
// multiple forward passes for server-side web search, reporting combined usage
// (~2x actual); that inflated baseline is skipped so the previous value stays valid.
func (s *Session) recordResponseUsage(resp llm.Response) {
	if s.contextMgr == nil {
		return
	}
	s.contextMgr.AddUsage(resp.Usage)

	hasServerWebSearch := false
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentWebSearch {
			hasServerWebSearch = true
			break
		}
	}
	if !hasServerWebSearch {
		totalInput := resp.Usage.InputTokens
		if resp.Usage.CacheReadTokens != nil {
			totalInput += *resp.Usage.CacheReadTokens
		}
		if resp.Usage.CacheWriteTokens != nil {
			totalInput += *resp.Usage.CacheWriteTokens
		}
		if totalInput > 0 {
			s.mu.Lock()
			hLen := len(s.history)
			s.mu.Unlock()
			s.contextMgr.RecordInputTokens(totalInput, hLen)
		}
	}
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
	if reasoningEffort != "" {
		// Clamp to what the active model supports so loop-detector escalation,
		// the --reasoning-effort flag, and the UI selector never send a level the
		// provider rejects (e.g. "xhigh" to a model that tops out at "high").
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
	callCtx := llm.WithAPILogAttemptContext(ctx, llm.APILogContext{
		SessionID:         s.id,
		Round:             round,
		AttemptIndex:      attempt.AttemptIndex,
		AttemptCount:      attempt.AttemptCount,
		FinalAttemptCount: attempt.FinalAttemptCount,
		HistoryMode:       attempt.HistoryMode,
	})
	modelResp, err := s.callModel(callCtx, policy, profile, req)
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
			fbReq := req
			fbReq.Model = fbProfile.Model()
			fbReq.Provider = fbProfile.ID()
			if origEffort != "" {
				// Clamp to the FALLBACK model's levels. WithModel keeps the primary
				// profile's effort levels for some providers (openai/anthropic), so
				// consult the catalog for the fallback model rather than trusting
				// fbProfile's possibly-stale set. LookupModelInfo canonicalizes the
				// "[1m]" suffix, a provider namespace ("anthropic/…" from
				// openrouter-anthropic), and dated snapshots, so a qualified or
				// dated fallback still resolves real levels.
				fbLevels := fbProfile.ReasoningEffortLevels()
				if cat := llm.EmbeddedModelCatalog(); cat != nil {
					if mi := cat.LookupModelInfo(fbProfile.Model()); mi != nil && len(mi.ReasoningEffortLevels) > 0 {
						fbLevels = mi.ReasoningEffortLevels
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
		return modelResp, req, attempt, err
	}
	return modelResp, req, completeAttemptMetadata(attempt, modelResp.Response), nil
}

// logAPICall records one round's request/response (or error) to the transcript.
func (s *Session) logAPICall(round int, roundStart time.Time, llmLatency time.Duration, sys string, historyLen int, req llm.Request, resp llm.Response, err error, attempt ModelAttemptMetadata) {
	if s.transcript != nil {
		apiCall := transcript.APICall{
			Round:                  round,
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
			var endpoint string
			if resp.Raw != nil {
				if v, ok := resp.Raw["endpoint_url"].(string); ok {
					endpoint = v
				}
			}
			apiCall.Response = &llm.APILogResponse{
				ID:            resp.ID,
				IDHash:        attempt.ResponseIDHash,
				Model:         resp.Model,
				FinishReason:  resp.Finish.Reason,
				TextLength:    len(resp.Text()),
				ToolCallCount: len(resp.ToolCalls()),
				Usage:         resp.Usage,
				EndpointURL:   endpoint,
				Raw:           resp.Raw,
			}
		}
		if werr := s.transcript.AppendAPICall(apiCall); werr != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", werr)})
		}
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
