package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/toolname"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// ctxKey is a private type for context keys in this package.
type ctxKey string

// ctxToolCallID carries the tool call ID into tool execution closures via context.
const ctxToolCallID ctxKey = "toolCallID"

// ctxToolItemID carries the provider/tool item ID into tool execution closures via context.
const ctxToolItemID ctxKey = "toolItemID"

// ctxCommunicateOutputSchema carries a delegate result schema into spawnAgent
// without changing the subagent tool signature.
const ctxCommunicateOutputSchema ctxKey = "communicateOutputSchema"

// ctxDelegationAllowance carries the granted delegation_allowance into child
// session spawn plumbing. createDelegate validates the grant (strictly less than
// the parent's own allowance) and sets this; prepareSubagentRun copies it onto
// the child's spawnConfig.
const ctxDelegationAllowance ctxKey = "delegationAllowance"

// ctxWatchParent carries the non-transitive parent observation grant into child
// session spawn plumbing. createDelegate sets it from delegate(watch_parent=true);
// prepareSubagentRun copies it onto the child's spawnConfig.
const ctxWatchParent ctxKey = "watchParent"

// ctxParentDelegateID carries the delegate handle that owns a child session
// into spawn plumbing so parent-source watches can route back to that child.
const ctxParentDelegateID ctxKey = "parentDelegateID"

// ctxIsolation carries delegate(isolation:"worktree") into child session spawn
// plumbing without changing prepareSubagentRun's signature (native worktree
// tools spec §9). createDelegate sets it after successfully creating the
// delegate's isolation lane; prepareSubagentRun copies it onto the child's
// spawnConfig, which session_init.go reads to unconditionally deny
// manage_worktree regardless of the agent type's base tool policy.
const ctxIsolation ctxKey = "isolation"

// ctxDelegateSandboxPolicy carries an explicit per-delegate sandbox request (a
// *sandbox.SandboxPolicy already validated against the parent's box by
// createDelegate's no-escalation floor) into child session spawn plumbing.
// prepareSubagentRun re-resolves it against the child's lane + memoized host facts
// and EnableSandbox's the child env with it, overriding whatever policy the
// working-dir re-root inherited from the parent. Absent = inherit the parent's box
// (today's behavior).
const ctxDelegateSandboxPolicy ctxKey = "delegateSandboxPolicy"

const (
	defaultAgentName = "default"
)

var delegationPromptToolNames = []string{
	"delegate",
	"delegate_send",
	"job_status",
	"job_list",
	"job_stop",
	"job_watch",
	"read_transcript",
	"shell",
}

type stableDelegateCreateResult struct {
	DelegateID     string                      `json:"delegate_id"`
	ChildSessionID string                      `json:"child_session_id"`
	Type           string                      `json:"type"`
	Status         string                      `json:"status"`
	AgentType      string                      `json:"agent_type,omitempty"`
	Tools          []string                    `json:"tools,omitempty"`
	Reason         string                      `json:"reason,omitempty"`
	Resumable      *bool                       `json:"resumable,omitempty"`
	TranscriptRef  string                      `json:"transcript_ref"`
	Model          string                      `json:"model,omitempty"`
	Sandbox        *delegateSandboxToolResult  `json:"sandbox,omitempty"`
	Worktree       *delegateWorktreeToolResult `json:"worktree,omitempty"`
	Warnings       []string                    `json:"warnings,omitempty"`
	StartError     string                      `json:"error,omitempty"`
}

func registerStableDelegateTool(reg *tool.Registry, s *Session) error {
	reg.Remove("delegate")
	if err := reg.Register(tool.RegisteredTool{
		Definition: s.delegateToolDefinition(),
		Limit:      schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return stableDelegateCreateTool(ctx, s, args, jobToolResultMaxChars(reg, "delegate"))
		},
	}); err != nil {
		return err
	}
	reg.Remove("delegate_send")
	return reg.Register(tool.RegisteredTool{
		Definition: tool.DefDelegateSend(),
		Limit:      schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return stableDelegateSendTool(ctx, s, args, jobToolResultMaxChars(reg, "delegate_send"))
		},
	})
}

func stableDelegateSendTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (any, error) {
	target := strings.TrimSpace(stringArg(args, "to"))
	message := strings.TrimSpace(stringArg(args, "message"))
	wait := 0
	if n, ok := shellIntArg(args, "max_wait_ms"); ok {
		if n < 0 {
			return nil, errors.New("invalid_request: max_wait_ms must be non-negative")
		}
		wait = n
	}
	requestedWait := wait
	if wait > 0 {
		wait = int(clampJobBlockTimeout(wait).Milliseconds())
	}
	if target == runtimeMessageAliasCaller {
		if wait > 0 {
			return nil, errors.New("invalid_request: max_wait_ms is not supported for the caller route; no message was delivered")
		}
		if s == nil || s.delegateController == nil {
			return nil, errors.New("delegate controller is unavailable")
		}
		actor, err := s.delegateActor(ctx)
		if err != nil {
			return nil, err
		}
		plans, err := s.delegateController.SteerCaller(ctx, actor, message, s.activeCausalProvenance())
		if err != nil {
			return nil, err
		}
		if err := s.executeDelegateMutationPlans(plans); err != nil {
			return nil, err
		}
		result := sendMessageResult{Target: target, Action: "delivered"}
		result.WaitIgnoredReason = liveSteerWaitIgnoredReason(requestedWait, result.Status, result.Action)
		return marshalDelegateSendResult(result, maxChars)
	}
	outcome := (delegateRuntime{owner: s}).send(ctx, target, message, wait)
	outcome.result.WaitIgnoredReason = liveSteerWaitIgnoredReason(requestedWait, outcome.result.Status, outcome.result.Action)
	if outcome.result.Err != nil && outcome.result.DelegateID == "" {
		return nil, outcome.result.Err
	}
	if outcome.commit != nil {
		callID, _ := ctx.Value(ctxToolCallID).(string)
		if callID == "" {
			_, _ = outcome.commit.Complete(false)
			return nil, errors.New("delegate result cannot be committed outside a tool round")
		}
		s.queueDelegateDeliveryCommit(callID, outcome.commit)
	}
	return marshalDelegateSendResult(outcome.result, maxChars)
}

func stableDelegateCreateTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (string, error) {
	if _, exists := args["max_wait_ms"]; exists {
		return "", errors.New("invalid_request: delegate creation does not accept max_wait_ms")
	}
	decoded, err := decodeDelegateArgs(args)
	if err != nil {
		return "", err
	}
	result := s.createDelegate(ctx, decoded)
	if result.Err != nil && result.DelegateID == "" {
		return "", result.Err
	}
	out := stableDelegateCreateResult{
		DelegateID:     result.DelegateID,
		ChildSessionID: result.ChildSessionID,
		Type:           result.Type,
		Status:         string(result.Status),
		AgentType:      result.AgentType,
		Tools:          append([]string(nil), result.Tools...),
		Reason:         result.Reason,
		Resumable:      result.Resumable,
		TranscriptRef:  result.TranscriptRef,
		Model:          result.Model,
		Sandbox:        delegateSandboxToolResultFrom(result.Sandbox),
		Worktree:       delegateWorktreeToolResultFrom(result.Worktree),
		Warnings:       append([]string(nil), result.Warnings...),
	}
	if result.Err != nil {
		out.StartError = result.Err.Error()
	}
	return marshalStableDelegateCreateResult(out, maxChars)
}

func marshalStableDelegateCreateResult(out stableDelegateCreateResult, maxChars int) (string, error) {
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	// Preserve the durable identity and outcome by dropping optional diagnostics
	// from least to most useful before falling back to the bounded core.
	out.StartError = ""
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.Warnings = nil
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.Worktree = nil
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.Model = ""
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.Sandbox = nil
	return marshalBoundedJSON(out, maxChars)
}

// resultToolName returns the effective name for the communicate tool.
func (s *Session) resultToolName() string {
	if s.cfg.ResultToolName != "" {
		return s.cfg.ResultToolName
	}
	return "communicate"
}

// RegisterTool registers a custom tool at runtime. It returns an error when the
// definition cannot be registered and leaves the session's tool caches unchanged.
func (s *Session) RegisterTool(name, description string, params map[string]any, fn func(ctx context.Context, args any) (any, error)) error {
	// s.reg self-synchronizes (see the Session.mu lock discipline comment), so
	// Register itself can happen outside s.mu. But the cache rebuild below
	// writes s.cachedToolDefs/s.cachedSystemPrompt and reads s.env/s.envInfo,
	// all of which mu guards — those must happen under lock, mirroring
	// SetModel's pattern, so they can't race a concurrent env swap or model
	// switch.
	if err := s.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        name,
			Description: description,
			Parameters:  params,
		},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return fn(ctx, args)
		},
	}); err != nil {
		return err
	}
	// Rebuild caches so the new tool appears in tool defs and system prompt.
	s.mu.Lock()
	s.rebuildToolDefsCache()
	promptWarning := s.refreshSystemPromptCache(s.env) // already holding s.mu; currentEnv() would deadlock
	s.mu.Unlock()
	s.reportPromptRenderFailure(promptWarning)
	return nil
}

const (
	// visionReasoningEffort deliberately caps image and document descriptions
	// below the session's reasoning effort. This side-channel does perception-
	// shaped work, where inheriting a top-tier effort adds latency without
	// improving the description contract.
	visionReasoningEffort = "low"
	// visionSideChannelTimeout is an explicit caller-owned ceiling. The adapter
	// timeout remains a defense in depth for provider transports, while this
	// context also cancels deterministic/non-HTTP adapters and all cleanup
	// attached to the side-channel call.
	visionSideChannelTimeout = 2 * time.Minute
)

var errVisionSideChannelTimeout = errors.New("vision side-channel deadline")

func (s *Session) visionSideChannelDuration() time.Duration {
	if timeout := s.cfg.testOnly.visionSideChannelTimeout; timeout > 0 {
		return timeout
	}
	return visionSideChannelTimeout
}

type visionSideChannelOutcome uint8

const (
	visionSideChannelSuccess visionSideChannelOutcome = iota
	visionSideChannelOwnedTimeout
	visionSideChannelParentCanceled
	visionSideChannelProviderFailure
)

type visionSideChannelResult struct {
	description string
	elapsed     time.Duration
	usage       llm.Usage
	outcome     visionSideChannelOutcome
}

// describeImage makes a side-channel API call with no tools to describe an image
// using the model's native vision. Returns the text description, or "" on error.
func (s *Session) describeImage(ctx context.Context, r tool.ExecResult) string {
	return s.describeImageCall(ctx, r).description
}

// visionSideChannelStats is the machine-readable accounting contract carried
// in successful image-description steering. Token fields are present only when
// the provider reported usage; usage_available distinguishes an unavailable
// report from a real report whose counters happen to be zero.
type visionSideChannelStats struct {
	ElapsedMS                int64 `json:"elapsed_ms"`
	UsageAvailable           bool  `json:"usage_available"`
	InputTokens              *int  `json:"input_tokens,omitempty"`
	OutputTokens             *int  `json:"output_tokens,omitempty"`
	ReasoningTokens          *int  `json:"reasoning_tokens,omitempty"`
	ReasoningTokensEstimated *int  `json:"reasoning_tokens_estimated,omitempty"`
	CacheReadTokens          *int  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens         *int  `json:"cache_write_tokens,omitempty"`
	CacheWrite1hTokens       *int  `json:"cache_write_1h_tokens,omitempty"`
}

const (
	visionSideChannelStatsOpen  = "<evener:vision_side_channel_stats>"
	visionSideChannelStatsClose = "</evener:vision_side_channel_stats>"
	visionRequestContract       = "Observe the image faithfully and answer the caller's request. Vision is non-authoritative for exact text or bytes; use OCR or the source when exactness matters."
)

func visionUnavailableSteering(path string) string {
	if path == "" {
		return "Vision is unavailable. Use OCR or inspect the source data, or continue without vision."
	}
	return fmt.Sprintf("Vision is unavailable for %s. Use OCR or inspect the source data, or continue without vision.", strconv.Quote(path))
}

func visionFailureSteering(path string, result visionSideChannelResult) string {
	if result.outcome == visionSideChannelProviderFailure {
		if path == "" {
			return "Vision is unavailable because the vision provider failed. Use OCR or inspect the source data, or continue without vision."
		}
		return fmt.Sprintf("Vision is unavailable for %s because the vision provider failed. Use OCR or inspect the source data, or continue without vision.", strconv.Quote(path))
	}
	return visionUnavailableSteering(path)
}

func formatVisionSideChannelStats(result visionSideChannelResult) string {
	stats := visionSideChannelStats{
		ElapsedMS:      result.elapsed.Milliseconds(),
		UsageAvailable: visionUsageAvailable(result.usage),
	}
	if stats.UsageAvailable {
		stats.InputTokens = new(result.usage.InputTokens)
		stats.OutputTokens = new(result.usage.OutputTokens)
		stats.ReasoningTokens = result.usage.ReasoningTokens
		stats.ReasoningTokensEstimated = result.usage.ReasoningTokensEstimated
		stats.CacheReadTokens = result.usage.CacheReadTokens
		stats.CacheWriteTokens = result.usage.CacheWriteTokens
		stats.CacheWrite1hTokens = result.usage.CacheWrite1hTokens
	}
	payload, err := json.Marshal(stats)
	if err != nil {
		return ""
	}
	return visionSideChannelStatsOpen + string(payload) + visionSideChannelStatsClose
}

func visionUsageAvailable(usage llm.Usage) bool {
	return usage.InputTokens != 0 ||
		usage.OutputTokens != 0 ||
		usage.TotalTokens != 0 ||
		usage.ReasoningTokens != nil ||
		usage.ReasoningTokensEstimated != nil ||
		usage.CacheReadTokens != nil ||
		usage.CacheWriteTokens != nil ||
		usage.CacheWrite1hTokens != nil
}

// visionModelOff is the reserved bare-word setting that disables the vision
// side-channel. Only a slash-free value can be the sentinel: a value with a
// slash always parses as "provider/model", so a provider named "off" stays
// reachable as "off/some-model".
const visionModelOff = "off"

// resolveVisionRoute maps the session's vision_model setting to the route the
// side-channel executes on. "" resolves to the session's active route, "off"
// (case-insensitive) disables the call, "provider/model" pins a provider, and
// a bare model resolves on the active provider at call time — so it follows
// SetModel switches. A malformed "x/" or "/x" value degrades to a bare-model
// lookup on the active provider rather than an unroutable request.
func resolveVisionRoute(profile *provider.Profile, setting string) (providerName, modelID string, off bool) {
	setting = strings.TrimSpace(setting)
	if setting == "" {
		return profile.ID(), profile.Model(), false
	}
	if strings.EqualFold(setting, visionModelOff) {
		return "", "", true
	}
	if prov, model, ok := strings.Cut(setting, "/"); ok && prov != "" && model != "" {
		return prov, model, false
	}
	return profile.ID(), setting, false
}

// visionRouteReasoning gates reasoning_effort for the vision request and names
// the levels the fixed vision cap clamps against: the session route uses the
// profile's own answers (which already carry the registry's facts); any other
// route asks the registry whether the row takes an effort control at all
// (spec §7.4: a reasoning row without an explicit control list is
// effort-capable, a toggle-only row is not). A route that does not resolve
// gets no effort knob rather than one it may reject.
func (s *Session) visionRouteReasoning(profile *provider.Profile, providerName, modelID string) (bool, []string) {
	if providerName == profile.ID() && modelID == profile.Model() {
		return profile.SupportsReasoning(), profile.ReasoningEffortLevels()
	}
	res, err := s.client.Resolve(providerName + "/" + modelID)
	if err != nil {
		return false, nil
	}
	return res.Caps.EffortCapable(), res.Caps.EffortValues
}

func (s *Session) describeImageCall(ctx context.Context, r tool.ExecResult) visionSideChannelResult {
	if len(r.ImageData) == 0 {
		return visionSideChannelResult{outcome: visionSideChannelSuccess}
	}
	// Skip for explorer agents — they're just inventorying files, not analyzing images.
	if s.cfg.AgentName == "explorer" {
		return visionSideChannelResult{outcome: visionSideChannelSuccess}
	}

	// Snapshot the profile under s.mu: the vision side-channel runs during the
	// round, so a concurrent SetModel (which mutates it under s.mu) must not race
	// this read (PRI-1958 A2/A4). The fixed low vision cap below is deliberately
	// independent of the session/task reasoning effort.
	s.mu.Lock()
	profile := s.profile
	visionSetting := s.cfg.VisionModel
	s.mu.Unlock()

	routeProvider, routeModel, visionOff := resolveVisionRoute(profile, visionSetting)
	if visionOff {
		return visionSideChannelResult{outcome: visionSideChannelSuccess}
	}

	// Use the caller's stated intent as the vision prompt. The calling LLM
	// knows what it needs — we just ask the vision model to answer that question
	// under one unconditional observation contract.
	prompt := visionPrompt(r.ImageIntent)

	mt := r.ImageMediaType
	if mt == "" {
		mt = "image/png"
	}

	// Build the content part based on media type — images use ContentImage,
	// documents (PDFs) use ContentDocument so the provider sends them correctly.
	var mediaPart llm.ContentPart
	if strings.HasPrefix(mt, "application/pdf") {
		mediaPart = llm.ContentPart{Kind: llm.ContentDocument, Document: &llm.DocumentData{
			Data:      r.ImageData,
			MediaType: mt,
			FileName:  "document.pdf",
		}}
	} else {
		mediaPart = llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{
			Data:      r.ImageData,
			MediaType: mt,
			Detail:    "original",
		}}
	}

	visionTimeout := s.visionSideChannelDuration()
	visionCtx, cancel := context.WithTimeoutCause(ctx, visionTimeout, errVisionSideChannelTimeout)
	defer cancel()
	req := llm.Request{
		Model:    profile.Model(),
		Provider: profile.ID(),
		Messages: []llm.Message{
			{
				Role: llm.RoleUser,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: prompt},
					mediaPart,
				},
			},
		},
		// No tools — force text-only response.
		AdapterTimeout: &llm.AdapterTimeout{
			Connect:    10 * time.Second,
			Request:    visionTimeout,
			StreamRead: 30 * time.Second,
		},
	}
	// This request is built manually (not via buildModelRequest), so clamp the
	// fixed vision cap to the model's supported levels here too. A model whose
	// cheapest level is above the cap gets that level rather than a value it
	// would reject. Gate on SupportsReasoning so non-reasoning models never get
	// reasoning_effort on the wire.
	if supportsReasoning, levels := s.visionRouteReasoning(profile, routeProvider, routeModel); supportsReasoning {
		effort := llm.ClampReasoningEffort(visionReasoningEffort, levels)
		req.ReasoningEffort = &effort
	}
	s.applyModelRequestMetadata(&req)

	start := s.sclock().Now()
	resp, err := s.cheap.CompleteRouted(visionCtx, profile, routeProvider, routeModel, req)
	elapsed := s.sclock().Now().Sub(start)
	elapsed = max(elapsed, 0)
	if err != nil {
		outcome := visionSideChannelProviderFailure
		cause := context.Cause(visionCtx)
		if errors.Is(cause, errVisionSideChannelTimeout) {
			outcome = visionSideChannelOwnedTimeout
		} else if ctx.Err() != nil {
			// Parent cancellation owns races with the side-channel deadline. This
			// prevents a stale unavailable steering message after a canceled turn.
			outcome = visionSideChannelParentCanceled
		} else if errors.Is(visionCtx.Err(), context.DeadlineExceeded) {
			outcome = visionSideChannelOwnedTimeout
		}
		if outcome != visionSideChannelParentCanceled {
			// Provider errors can contain request URLs, bodies, IDs, or credentials.
			// The user-facing steering below is deliberately sanitized, so keep the
			// warning sanitized too rather than reintroducing the raw adapter error.
			s.emit(events.EventWarning, events.WarningData{Message: "vision side-channel unavailable"})
		}
		return visionSideChannelResult{elapsed: elapsed, outcome: outcome}
	}

	return visionSideChannelResult{
		description: strings.TrimSpace(resp.Message.Text()),
		elapsed:     elapsed,
		usage:       resp.Usage,
		outcome:     visionSideChannelSuccess,
	}
}

func visionPrompt(intent string) string {
	intent = strings.TrimSpace(intent)
	if intent == "" {
		intent = "Describe what you see in this image in thorough detail."
	}
	return intent + "\n\n" + visionRequestContract
}

func (s *Session) canonicalToolName(name string) string {
	return canonicalToolName(name, s.currentProfile().ToolNameMap())
}

// canonicalIncomingToolName reverse-maps a provider-visible alias only when the
// exact incoming bytes already form a readable tool name. Malformed names must
// remain unchanged so trimming during alias resolution cannot grant them the
// behavior or presentation of a registered tool.
func (s *Session) canonicalIncomingToolName(name string) string {
	if !tool.IsReadableToolName(name) {
		return name
	}
	return s.canonicalToolName(name)
}

func (s *Session) canonicalizeToolNames(names []string) []string {
	return canonicalizeToolNames(names, s.currentProfile().ToolNameMap())
}

// providerVisibleToolNames reads s.profile directly: its prompt-composition
// caller chain (buildPromptData -> availableAgentEntries ->
// defaultToolSummaryForAgent) already runs under a held s.mu, and s.mu is not
// reentrant, so taking the lock here would deadlock prompt rendering.
func (s *Session) providerVisibleToolNames(names []string) []string {
	return providerVisibleToolNames(names, s.profile.ToolNameMap())
}

// providerVisibleToolName is providerVisibleToolNames for a single name, for
// prose that must address one tool by the name this model actually has. An
// unknown name passes through, so the caller never renders an empty tool name.
//
// It takes s.mu for the profile read (via currentProfile) rather than going
// through the method above, because its callers are OFF the turn goroutine —
// the job-tree drain — where a model switch could otherwise race the read.
func (s *Session) providerVisibleToolName(name string) string {
	visible := providerVisibleToolNames([]string{name}, s.currentProfile().ToolNameMap())
	if len(visible) == 0 {
		return name
	}
	return visible[0]
}

// canonicalToolName resolves a single tool name to its canonical form: a
// provider-visible name (a value in nameMap) maps back to its canonical key;
// anything else (already canonical, or unknown) passes through trimmed. Empty
// input yields empty.
func canonicalToolName(name string, nameMap map[string]string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	for canonical, provider := range nameMap {
		if provider == name {
			return canonical
		}
	}
	return name
}

// canonicalizeToolNames maps each name to its canonical form via nameMap
// (canonical -> provider-visible), preserving first-seen order while dropping
// empties and duplicates.
func canonicalizeToolNames(names []string, nameMap map[string]string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		canonical := canonicalToolName(name, nameMap)
		if canonical == "" || seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, canonical)
	}
	return out
}

// providerToolName resolves a single canonical name to the provider-visible
// name via nameMap, passing through trimmed when unmapped. Empty input yields
// empty.
func providerToolName(name string, nameMap map[string]string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if provider, ok := nameMap[name]; ok {
		return provider
	}
	return name
}

// providerVisibleToolNames maps each name to its provider-visible form via
// nameMap, dropping empties and duplicates, and returns them sorted.
func providerVisibleToolNames(names []string, nameMap map[string]string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		visible := providerToolName(name, nameMap)
		if visible == "" || seen[visible] {
			continue
		}
		seen[visible] = true
		out = append(out, visible)
	}
	sort.Strings(out)
	return out
}

func (s *Session) execTool(ctx context.Context, call llm.ToolCallData, finishReason string) tool.ExecResult {
	if err := s.abortIfClosing(ctx); err != nil {
		return skippedToolResult(call, err)
	}
	if lease, ok := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease); ok && s.delegateController != nil {
		if err := s.delegateController.BeginTool(lease); err != nil {
			return skippedToolResult(call, err)
		}
	}

	// Self-heal off-distribution tool calls before hooks/dispatch. Snapshot the
	// provider name-map here (execTool runs outside s.mu) — never lock inside
	// providerToolName, which has under-lock callers (SetModel).
	nameMap := s.currentProfile().ToolNameMap()
	visibleNames := providerVisibleToolNames(s.reg.Names(), nameMap)
	requestedVisible := providerToolName(call.Name, nameMap)
	if !tool.IsReadableToolName(call.Name) {
		requestedVisible = tool.DisplayToolName(call.Name)
	}
	// Repair events and hooks below observe the prepared call. Keep the provider
	// bytes separately because, if that registration is replaced afterward, the
	// successor's dispatch pipeline must start from the unprepared input.
	originalArguments := append(json.RawMessage(nil), call.Arguments...)
	registered, lifetime := s.reg.SnapshotPrevalidation(call.Name)
	prep := prepareToolCall(call, registered, visibleNames, requestedVisible, s.resultToolName(), finishReason)
	prep.Lifetime = lifetime
	call = prep.Call
	displayName := tool.DisplayToolName(call.Name)
	prevalidated := true
	if len(prep.Changes) > 0 {
		s.emit(events.EventToolCallRepaired, events.ToolCallRepairedData{
			ToolName: displayName,
			CallID:   call.ID,
			Changes:  changeStrings(prep.Changes),
		})
	}

	// PreToolUse hooks
	if s.hookRunner != nil {
		hi := s.hookInput(plugin.HookPreToolUse)
		hi.ToolName = toolname.EvenerToClaude(displayName)
		hi.ToolUseID = call.ID
		if !prep.RawArgumentsRejected && len(call.Arguments) > 0 {
			_ = json.Unmarshal(call.Arguments, &hi.ToolInput)
		}

		preResult := s.hookRunner.RunPreToolUseMatching(s.apiLogContext(ctx), call.Name, hi)
		for _, m := range preResult.ModelContext {
			s.deliverHookContext(m)
		}
		for _, m := range preResult.UserMessages {
			s.deliverHookUserMessage(m)
		}
		if !prep.RawArgumentsRejected && preResult.Denied {
			denyMsg := "Tool call denied by hook"
			if preResult.DenyMessage != "" {
				denyMsg = preResult.DenyMessage
			}
			return tool.ExecResult{
				ToolName:   displayName,
				CallID:     call.ID,
				Output:     denyMsg,
				FullOutput: denyMsg,
				IsError:    true,
			}
		}
		if !prep.RawArgumentsRejected && len(preResult.UpdatedInput) > 0 {
			if err := applyUpdatedToolInput(&call, preResult.UpdatedInput); err != nil {
				msg := "invalid hook updatedInput: " + err.Error()
				return tool.ExecResult{
					ToolName:   displayName,
					CallID:     call.ID,
					Output:     msg,
					FullOutput: msg,
					IsError:    true,
				}
			}
			// Preparation validated the original arguments. A hook may replace
			// any semantic task field, so dispatch this changed call through the
			// normal validation path, including when the update corrected an
			// initial preparation failure. Unchanged prepared calls still avoid
			// running that validation twice.
			prep.PrevalErr = ""
			prevalidated = false
		}
	}
	s.execToolCheckpoint("after_pre_hook")
	if err := s.abortIfClosing(ctx); err != nil {
		return skippedToolResult(call, err)
	}

	argsJSON, _ := json.Marshal(call.Arguments)
	startData := events.ToolCallStartData{
		ToolName:      displayName,
		CallID:        call.ID,
		ArgumentsJSON: string(argsJSON),
	}
	// Promote intent to the top-level event field for observability.
	var args map[string]any
	if !prep.RawArgumentsRejected && len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &args)
	}
	if d := toolStartDescription(args); d != "" {
		startData.Description = d
	}
	s.execToolCheckpoint("before_side_effects")
	toolEventOpen := false
	closeToolEvent := func() {
		if toolEventOpen {
			s.toolEventsWG.Done()
			toolEventOpen = false
		}
	}
	if err := s.withResponseSideEffects(ctx, func() {
		s.toolEventsWG.Add(1)
		toolEventOpen = true
		s.emit(events.EventToolCallStart, startData)
	}); err != nil {
		return skippedToolResult(call, err)
	}
	s.execToolCheckpoint("after_start")
	defer closeToolEvent()
	emitCanceledEnd := func(err error) {
		res := skippedToolResult(call, err)
		s.responseSideEffectsMu.Lock()
		s.emit(events.EventToolCallEnd, events.ToolCallEndData{
			ToolName:      res.ToolName,
			CallID:        res.CallID,
			ArgumentsJSON: string(call.Arguments),
			Error:         res.FullOutput,
		})
		s.responseSideEffectsMu.Unlock()
		closeToolEvent()
	}

	// Session-level tools (subagents) are registered in the registry with closures.
	ctx = context.WithValue(ctx, ctxToolCallID, call.ID)
	if call.ItemID != "" {
		ctx = context.WithValue(ctx, ctxToolItemID, call.ItemID)
	}
	toolStart := s.sclock().Now()
	if err := s.abortIfClosing(ctx); err != nil {
		emitCanceledEnd(err)
		return skippedToolResult(call, err)
	}
	var res tool.ExecResult
	if prep.PrevalErr != "" {
		res = s.reg.FinalizePrevalidationFailure(ctx, prep.Lifetime, call, prep.SemanticArguments, prep.PrevalErr, prep.Boundary, prep.Err)
	} else {
		if prevalidated {
			res = s.reg.ExecutePreparedCall(ctx, s.currentEnv(), call, prep.Lifetime, prep.PreparedArguments, originalArguments)
		} else {
			res = s.reg.ExecuteCall(ctx, s.currentEnv(), call)
		}
	}
	res.DurationMS = time.Since(toolStart).Milliseconds()
	// M7: on a sandbox denial in an interactive root session, raise a human approval
	// card and block HERE — between dispatch and the TOOL_CALL_END emit, so the tool
	// item stays "in progress" while the human decides. On approve, re-dispatch this
	// one call through a grant-scoped env clone (the granted leaf threaded on ctx);
	// on deny/interrupt/close, the typed denial flows on to the model unchanged. A
	// non-sandbox error, a non-interactive/subagent session, or an ineligible denial
	// all pass through untouched (escalateOnSandboxDenial is a no-op for them).
	res = s.escalateOnSandboxDenial(ctx, call.Name, res, toolCallRerunner{session: s, call: call}.run)
	s.execToolCheckpoint("after_execute")
	if err := s.errIfClosing(); err != nil {
		emitCanceledEnd(err)
		return skippedToolResult(call, err)
	}

	s.responseSideEffectsMu.Lock()
	s.execToolCheckpoint("after_side_effect_lock")
	if err := s.errIfClosing(); err != nil {
		canceled := skippedToolResult(call, err)
		s.emit(events.EventToolCallEnd, events.ToolCallEndData{
			ToolName:      canceled.ToolName,
			CallID:        call.ID,
			ArgumentsJSON: string(call.Arguments),
			Error:         canceled.FullOutput,
		})
		s.responseSideEffectsMu.Unlock()
		closeToolEvent()
		return skippedToolResult(call, err)
	}
	outputRef := s.retainToolArtifact(&res)
	// Emit output deltas (best-effort). Even for non-streaming tools, this gives consumers a uniform
	// incremental event pattern that mirrors provider LLM streaming.
	full := res.FullOutput
	const chunk = 4000
	for i := 0; i < len(full); i += chunk {
		j := min(i+chunk, len(full))
		s.emit(events.EventToolCallOutputDelta, events.ToolCallOutputDeltaData{
			ToolName: res.ToolName,
			CallID:   res.CallID,
			Delta:    full[i:j],
		})
	}

	endData := events.ToolCallEndData{
		ToolName:                 res.ToolName,
		CallID:                   res.CallID,
		ArgumentsJSON:            string(call.Arguments),
		BreakerExactSignature:    res.BreakerExactSignature,
		BreakerSemanticSignature: res.BreakerSemanticSignature,
		BreakerBypassed:          res.BreakerBypassed,
		ToolState:                res.ToolState,
		OutputRef:                outputRef,
	}
	// A tool that returned image bytes says so here, by sha. The bytes
	// themselves ride to the transcript with the tool-result turn and are never
	// on this event -- a dashboard fetches them from whichever server publishes
	// the thread, which is why the descriptor carries no URL. Without this the
	// image is invisible to anyone watching live: it only reappears when the
	// session is read back off disk.
	if img, ok := events.ToolResultOutputImage(res.ToolName, res.ImageData, res.ImageMediaType); ok {
		endData.OutputImages = []events.OutputImage{img}
	}
	if res.IsError {
		endData.Error = res.FullOutput
		endData.PrevalOnly = res.PrevalOnly
	} else {
		endData.Output = res.FullOutput
	}
	s.emit(events.EventToolCallEnd, endData)
	s.responseSideEffectsMu.Unlock()
	closeToolEvent()

	// PostToolUse hooks
	if s.hookRunner != nil {
		hi := s.hookInput(plugin.HookPostToolUse)
		hi.ToolName = toolname.EvenerToClaude(displayName)
		hi.ToolUseID = call.ID
		hi.ToolResult = res.FullOutput   // legacy alias
		hi.ToolResponse = res.FullOutput // official field
		postResult := s.hookRunner.RunPostToolUseMatching(s.apiLogContext(ctx), call.Name, hi)
		for _, m := range postResult.ModelContext {
			s.deliverHookContext(m)
		}
		for _, m := range postResult.UserMessages {
			s.deliverHookUserMessage(m)
		}
	}

	return res
}

func (s *Session) execToolCheckpoint(name string) {
	if checkpoint := s.cfg.testOnly.execToolCheckpoint; checkpoint != nil {
		checkpoint(name)
	}
}

func (s *Session) rerunToolWithGrant(ctx context.Context, call llm.ToolCallData) tool.ExecResult {
	env := s.currentEnv()
	if grantPath, ok := invocationGrant(ctx); ok {
		if g, gok := env.(sandboxGranter); gok {
			env = g.WithSandboxInvocationGrant(grantPath)
		}
	}
	rerunStart := s.sclock().Now()
	// A human just approved this exact call, so the repeated-call breaker must
	// not refuse it — its whole job is to stop the model repeating itself.
	result := s.reg.ExecuteCall(tool.WithBreakerBypass(ctx), env, call)
	result.DurationMS = s.sclock().Now().Sub(rerunStart).Milliseconds()
	return result
}

type toolCallRerunner struct {
	session *Session
	call    llm.ToolCallData
}

func (r toolCallRerunner) run(ctx context.Context) tool.ExecResult {
	return r.session.rerunToolWithGrant(ctx, r.call)
}

// toolStartDescription resolves the tool-call-start Description from a tool call's
// decoded arguments: the "intent" field, else empty. Pure over the args map,
// so the promotion order is fuzzable in isolation.
func toolStartDescription(args map[string]any) string {
	if v, ok := args["intent"].(string); ok && v != "" {
		return v
	}
	return ""
}

func applyUpdatedToolInput(call *llm.ToolCallData, updated map[string]any) error {
	if call == nil || len(updated) == 0 {
		return nil
	}
	args := map[string]any{}
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return err
		}
	}
	maps.Copy(args, updated)
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	call.Arguments = json.RawMessage(b)
	return nil
}

func skippedToolResult(call llm.ToolCallData, err error) tool.ExecResult {
	msg := "tool skipped: session is closing"
	if err != nil && !errors.Is(err, context.Canceled) {
		msg = "tool skipped: " + err.Error()
	}
	return tool.ExecResult{
		ToolName:   tool.DisplayToolName(call.Name),
		CallID:     call.ID,
		Output:     msg,
		FullOutput: msg,
		IsError:    true,
	}
}

func (s *Session) appendCanceledToolResults(calls []llm.ToolCallData, results []tool.ExecResult, err error) {
	if len(calls) == 0 {
		return
	}
	if err == nil {
		err = context.Canceled
	}
	parts := make([]llm.ContentPart, 0, len(calls))
	for i, call := range calls {
		res := tool.ExecResult{}
		if i < len(results) {
			res = results[i]
		}
		if res.CallID == "" {
			msg := "tool canceled: " + err.Error()
			res = tool.ExecResult{
				ToolName:   tool.DisplayToolName(call.Name),
				CallID:     call.ID,
				Output:     msg,
				FullOutput: msg,
				IsError:    true,
			}
		}
		parts = append(parts, llm.ContentPart{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID:     res.CallID,
				Name:           res.ToolName,
				Content:        res.Output,
				IsError:        res.IsError,
				PrevalOnly:     res.PrevalOnly,
				DurationMS:     res.DurationMS,
				ToolState:      res.ToolState,
				ImageData:      res.ImageData,
				ImageMediaType: res.ImageMediaType,
			},
		})
	}
	persistedParts := projectToolResultsForTranscript(calls, results, parts)
	s.appendTurnWithTranscriptMessage(
		schema.TurnToolResults,
		llm.Message{Role: llm.RoleTool, Content: parts},
		llm.Message{Role: llm.RoleTool, Content: persistedParts},
	)
}

func (s *Session) appendToolResults(ctx context.Context, calls []llm.ToolCallData, results []tool.ExecResult, parts []llm.ContentPart) error {
	var persistErr error
	if abortErr := s.withResponseSideEffects(ctx, func() {
		commits := s.takeDelegateDeliveryCommits(calls)
		if len(commits) != 0 {
			if observe := s.cfg.testOnly.delegateDeliveryCommitsTaken; observe != nil {
				observe()
			}
			// This is the last cancellation observation before transcript persistence.
			// Once it passes, the durable append below owns the commit point and must
			// finish rather than roll back a write that may already have started.
			if persistErr = ctx.Err(); persistErr != nil {
				abortDelegateToolCallDeliveryCommits(commits)
				return
			}
		}
		persistedParts := projectToolResultsForTranscript(calls, results, parts)
		live := llm.Message{Role: llm.RoleTool, Content: parts}
		persisted := llm.Message{Role: llm.RoleTool, Content: persistedParts}
		if len(commits) != 0 {
			persistErr = s.appendToolResultsWithDeliveryCommitsDurably(live, persisted, commits)
		} else if hasSuccessfulTerminalJobStatusResult(calls, results) {
			persistErr = s.appendTurnWithDurableTranscriptMessage(schema.TurnToolResults, live, persisted)
		} else {
			s.appendTurnWithTranscriptMessage(schema.TurnToolResults, live, persisted)
		}
		if persistErr != nil {
			return
		}
		persistErr = s.flushPendingDelegateDeliveries()
		if persistErr != nil {
			return
		}
		// Persist the completed tool round so resumed sessions always include
		// tool_result turns for any prior assistant tool calls.
		s.maybeAutoSave()
		s.announceReadableToolResultImages(results)
	}); abortErr != nil {
		// The tool round will not persist, so release any inline delivery receipts
		// it acquired before cancellation and leave their durable heads replayable.
		abortDelegateToolCallDeliveryCommits(s.takeDelegateDeliveryCommits(calls))
		if ctx.Err() != nil && !s.isClosingOrClosed() {
			s.appendCanceledToolResults(calls, results, abortErr)
		}
		return abortErr
	}
	return persistErr
}

func (s *Session) queueDelegateDeliveryCommit(callID string, commit *delegateToolResultCommit) {
	if s == nil || callID == "" || commit == nil {
		return
	}
	s.delegateDeliveryMu.Lock()
	s.mu.Lock()
	closing := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closing {
		s.delegateDeliveryMu.Unlock()
		_, _ = commit.Complete(false)
		return
	}
	if s.delegateDeliveryCommits == nil {
		s.delegateDeliveryCommits = make(map[string][]*delegateToolResultCommit)
	}
	s.delegateDeliveryCommits[callID] = append(s.delegateDeliveryCommits[callID], commit)
	s.delegateDeliveryMu.Unlock()
}

func (s *Session) abortDelegateDeliveryCommits() {
	if s == nil {
		return
	}
	s.delegateDeliveryMu.Lock()
	commits := s.delegateDeliveryCommits
	s.delegateDeliveryCommits = nil
	s.delegateDeliveryMu.Unlock()
	for _, pending := range commits {
		for _, commit := range pending {
			if commit != nil {
				_, _ = commit.Complete(false)
			}
		}
	}
}

type delegateToolCallDeliveryCommit struct {
	toolCallID string
	commit     *delegateToolResultCommit
}

func abortDelegateToolCallDeliveryCommits(commits []delegateToolCallDeliveryCommit) {
	for _, binding := range commits {
		if binding.commit != nil {
			_, _ = binding.commit.Complete(false)
		}
	}
}

func (s *Session) takeDelegateDeliveryCommits(calls []llm.ToolCallData) []delegateToolCallDeliveryCommit {
	s.delegateDeliveryMu.Lock()
	defer s.delegateDeliveryMu.Unlock()
	var commits []delegateToolCallDeliveryCommit
	for _, call := range calls {
		for _, commit := range s.delegateDeliveryCommits[call.ID] {
			commits = append(commits, delegateToolCallDeliveryCommit{toolCallID: call.ID, commit: commit})
		}
		delete(s.delegateDeliveryCommits, call.ID)
	}
	return commits
}

func (s *Session) appendToolResultsWithDeliveryCommitsDurably(live, persisted llm.Message, commits []delegateToolCallDeliveryCommit) error {
	live = providerHistoryMessage(live)
	persisted = providerHistoryMessage(persisted)
	liveTurn := schema.NewTurn(schema.TurnToolResults, live)
	persistedTurn := liveTurn
	persistedTurn.Message = persisted
	for _, binding := range commits {
		if binding.commit != nil {
			persistedTurn.DelegateDeliveryCommits = append(persistedTurn.DelegateDeliveryCommits, schema.DelegateDeliveryCommit{
				ToolCallID: binding.toolCallID,
				DeliveryID: binding.commit.deliveryID,
			})
		}
	}
	if err := s.appendTurnAfterTranscriptWrite(
		persistedTurn,
		func() error { return s.writeTranscriptDurableLocked(persistedTurn) },
		func() { s.history = append(s.history, liveTurn) },
	); err != nil {
		abortDelegateToolCallDeliveryCommits(commits)
		return err
	}
	var completionErrs []error
	requeue := false
	for _, binding := range commits {
		if binding.commit == nil {
			continue
		}
		plans, err := binding.commit.Complete(true)
		if err != nil {
			completionErrs = append(completionErrs, err)
			requeue = true
			continue
		}
		if err := s.executeDelegateMutationPlans(plans); err != nil {
			completionErrs = append(completionErrs, err)
		}
	}
	if requeue {
		s.requeueReplayableDelegateDeliveries(nil)
	}
	return errors.Join(completionErrs...)
}

// announceReadableToolResultImages names the round's tool calls whose result
// image bytes just became fetchable, and is called from inside the same
// side-effect bundle as the write that made them so.
//
// A tool result's bytes reach a reader only through the round's tool-result
// turn, and rounds persist whole: between the TOOL_CALL_END that announces an
// image by sha and the write above, the bytes exist nowhere a reader can look.
// That gap is as long as the round's remaining calls take — microseconds for a
// single-call round, the length of a build for an image read batched with one
// (kata v3dv). The descriptor on TOOL_CALL_END is true when it is emitted; this
// is the separate fact that it can now be acted on.
//
// Two things it deliberately does not announce. A round whose results carry no
// image bytes says nothing at all — this sits on the busiest path in the
// system and must stay silent on it. And a session with no transcript writer
// says nothing either: there is no file for a reader to fetch from, so an
// announcement would be the same unfulfillable promise this exists to remove.
func (s *Session) announceReadableToolResultImages(results []tool.ExecResult) {
	if !s.hasTranscriptWriter() {
		return
	}
	var callIDs []string
	for _, r := range results {
		if _, ok := events.ToolResultOutputImage(r.ToolName, r.ImageData, r.ImageMediaType); ok {
			callIDs = append(callIDs, r.CallID)
		}
	}
	if len(callIDs) == 0 {
		return
	}
	s.emit(events.EventToolResultImagesPersisted, events.ToolResultImagesPersistedData{CallIDs: callIDs})
}

// hasTranscriptWriter reports whether this session has somewhere durable to put
// a turn. Nil is the ordinary answer for a session with no state directory, and
// also for one whose writer could not be opened — transcript.Writer.Append is
// nil-safe, so both report a write as successful and drop it.
func (s *Session) hasTranscriptWriter() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcript != nil
}

// allToolDefinitions returns cached tool definitions.
// The cache is built once at session init via rebuildToolDefsCache.
func (s *Session) allToolDefinitions(_ int) []llm.ToolDefinition {
	return s.cachedToolDefs
}

// ToolDefinitions returns the model-facing tool definitions currently
// advertised by this session.
func (s *Session) ToolDefinitions() []llm.ToolDefinition {
	return append([]llm.ToolDefinition(nil), s.cachedToolDefs...)
}

func (s *Session) defaultToolSummaryForAgent(agent plugin.Agent) string {
	allowance := s.delegationAllowance // read under caller's lock or during single-threaded init

	allTools, allowedTools, deniedTools := baseSubagentToolPolicy(&agent, allowance > 0)
	var canonical []string
	switch {
	case allTools || len(allowedTools) == 0:
		canonical = removeStrings(s.reg.Names(), deniedTools)
	default:
		canonical = append([]string(nil), allowedTools...)
		canonical = appendUniqueStrings(canonical, s.resultToolName())
	}
	// Only strip root-only tools from the summary when there is no grantable
	// allowance. When allowance > 0 a typed agent that lists delegate/job_watch
	// keeps them in its printed summary so the DefaultTools line is truthful.
	if allowance <= 0 {
		canonical = removeRootOnlySubagentTools(canonical)
	}
	// ask_user is never callable by a subagent, including an all-tools role;
	// keep the advertised capability set aligned with the unconditional grant
	// guard rather than the parent's interactive-root registry.
	canonical = removeStrings(canonical, protectedGrantTools())
	return formatToolNamesForPrompt(s.providerVisibleToolNames(canonical))
}

func (s *Session) availableAgentEntries() []agentEntry {
	allowance := s.delegationAllowance // read under caller's lock or during single-threaded init
	if allowance <= 0 || !s.canPromptDelegation() {
		return nil
	}

	names := make([]string, 0, len(s.pluginAgents))
	for name := range s.pluginAgents {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]agentEntry, 0, len(names))
	for _, name := range names {
		agent := s.pluginAgents[name]
		entries = append(entries, agentEntry{
			Name:         name,
			Description:  agent.Description,
			DefaultTools: s.defaultToolSummaryForAgent(agent),
			TaskList:     agentTaskEntries(agent.Tasks),
		})
	}
	return entries
}

func (s *Session) delegateCapabilityRoster() string {
	entries := s.availableAgentEntries()
	if len(entries) == 0 {
		return ""
	}
	capabilities := make([]string, 0, len(entries))
	for _, entry := range entries {
		capabilities = append(capabilities, fmt.Sprintf("%s: %s", entry.Name, entry.DefaultTools))
	}
	return strings.Join(capabilities, "; ")
}

func (s *Session) delegateToolDefinition() llm.ToolDefinition {
	sandboxSchema := s.delegateSandboxSchemaForEnv(s.env)
	sandboxSchema.ModelDescription = s.delegateModelDescription
	definition := tool.DefDelegateWithSandbox(s.delegateAgentTypeNames(), sandboxSchema)
	roster := s.delegateCapabilityRoster()
	if roster == "" {
		return definition
	}
	definition.Description += " Role capabilities are listed in the agent_type schema and available-agents prompt."
	if properties, ok := definition.Parameters["properties"].(map[string]any); ok {
		if agentType, ok := properties["agent_type"].(map[string]any); ok {
			agentType["description"] = "Role for the delegate. Choose from the enum; effective capabilities by role: " + roster + "."
		}
	}
	return definition
}

func (s *Session) delegateAgentTypeNames() []string {
	allowance := s.delegationAllowance // read under caller's lock or during single-threaded init
	if allowance <= 0 || !s.canPromptDelegation() {
		return nil
	}

	names := make([]string, 0, len(s.pluginAgents))
	for name := range s.pluginAgents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// wireToolDef renders a canonical tool definition in its provider-visible wire
// form: it renames the tool to the provider-specific name (nameMap is
// canonical→provider) and adds the shared "intent" parameter to work tools.
// communicate/result tools omit intent because their user-facing message and
// strict output envelope already carry the result intent.
func wireToolDef(td llm.ToolDefinition, nameMap map[string]string, resultToolName string) llm.ToolDefinition {
	canonicalName := td.Name
	if mapped, ok := nameMap[td.Name]; ok {
		td.Name = mapped
	}
	if isResultToolDefinition(canonicalName, td.Name, resultToolName) {
		return tool.WithoutIntentParameter(td)
	}
	return tool.WithIntentParameter(td)
}

func isResultToolDefinition(canonicalName, wireName, resultToolName string) bool {
	if resultToolName == "" {
		resultToolName = "communicate"
	}
	return canonicalName == "communicate" || wireName == resultToolName
}

// profileWireToolDefs returns all of the profile's tool definitions in their
// provider-visible wire form (see wireToolDef), unfiltered by the registry.
func (s *Session) profileWireToolDefs() []llm.ToolDefinition {
	nameMap := s.profile.ToolNameMap()
	defs := s.profile.ToolDefinitions()
	for i := range defs {
		if defs[i].Name == "delegate" {
			sandboxSchema := s.delegateSandboxSchemaForEnv(s.env)
			sandboxSchema.ModelDescription = s.delegateModelDescription
			defs[i] = tool.DefDelegateWithSandbox(s.delegateAgentTypeNames(), sandboxSchema)
		}
		defs[i] = wireToolDef(defs[i], nameMap, s.resultToolName())
	}
	return defs
}

// rebuildToolDefsCache builds the cached tool definition lists from the
// current profile, MCP tools, and registry state. Called once at session init
// and again if tools are added at runtime (e.g. MCP or custom tools).
func (s *Session) rebuildToolDefsCache() {
	registered := s.reg.RegisteredNames()

	// Profile tool definitions are canonical (e.g. "shell"); the registry is also
	// keyed by canonical names. Each registered profile tool is advertised in its
	// provider-visible wire form (provider-specific rename + intent param).
	nameMap := s.profile.ToolNameMap() // canonical → provider, may be nil

	var defs []llm.ToolDefinition
	included := make(map[string]bool)
	for _, td := range s.profile.ToolDefinitions() {
		if registered[td.Name] {
			if td.Name == "delegate" {
				td = s.delegateToolDefinition()
				// When this session can only grant allowance 0 (own allowance 1),
				// delegation_allowance has a single legal value — a no-op knob. Hide it
				// so the model is not offered a parameter it cannot meaningfully set.
				if s.delegationAllowance <= 1 {
					if props, ok := td.Parameters["properties"].(map[string]any); ok {
						delete(props, "delegation_allowance")
					}
				}
			}
			if td.Name == "job_watch" {
				td = tool.DefJobWatch(availableEventKindNames())
			}
			wire := td
			if mapped, ok := nameMap[td.Name]; ok {
				wire.Name = mapped
			}
			defs = append(defs, wire)
			included[td.Name] = true // canonical
			// Also track the provider-mapped name so loop 3 (registry tools)
			// won't add a registry tool whose canonical name collides with
			// another tool's provider-mapped wire name.
			included[wire.Name] = true
		}
	}
	for _, td := range s.mcpTools {
		if registered[td.Name] && !included[td.Name] {
			defs = append(defs, td)
			included[td.Name] = true
		}
	}
	// Include any tools registered directly on the registry (e.g. approve/reject
	// on reviewer sessions) that weren't already covered by profile or MCP.
	for _, td := range s.reg.Definitions() {
		if included[td.Name] {
			continue
		}
		defs = append(defs, normalizeRegistryToolDefinition(td))
	}
	for i := range defs {
		if isResultToolDefinition(defs[i].Name, defs[i].Name, s.resultToolName()) {
			defs[i] = tool.WithoutIntentParameter(defs[i])
		} else {
			defs[i] = tool.WithIntentParameter(defs[i])
		}
	}

	s.cachedToolDefs = defs
}

// normalizeRegistryToolDefinition keeps directly registered tools acceptable
// to providers even when a caller supplied only a partial parameter schema.
func normalizeRegistryToolDefinition(td llm.ToolDefinition) llm.ToolDefinition {
	if td.Parameters != nil && td.Parameters["type"] == nil {
		td.Parameters = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	return td
}

// trackReadFile records that a file has been read in this session.
func (s *Session) trackReadFile(path string) {
	s.readFilesMu.Lock()
	s.readFiles[s.resolveFilePath(path)] = true
	s.readFilesMu.Unlock()
}

// readBeforeWriteWarning returns a warning string if the file exists but hasn't
// been read in this session. Returns "" for new files or previously-read files.
func (s *Session) readBeforeWriteWarning(path string) string {
	abs := s.resolveFilePath(path)
	s.readFilesMu.RLock()
	_, seen := s.readFiles[abs]
	s.readFilesMu.RUnlock()
	if seen {
		return ""
	}
	// New file creation is exempt from the warning.
	if !s.currentEnv().FileExists(path) {
		return ""
	}
	return "[WARNING: Writing to file that has not been read in this session. Consider reading first.]\n"
}

func (s *Session) resolveFilePath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.currentEnv().WorkingDirectory(), p)
}

func (s *Session) getOrCreateTaskStore() *task.TaskStore {
	s.taskStoreOnce.Do(func() {
		if s.cfg.spawn.sharedTaskStore != nil {
			s.taskStore = s.cfg.spawn.sharedTaskStore
			return
		}
		dir := s.stateDir
		if dir == "" {
			dir = s.currentEnv().WorkingDirectory()
		}
		s.taskStore = task.NewTaskStore(dir, s.id)
		s.taskStoreLoadErr = s.taskStore.Load()
	})
	return s.taskStore
}

// taskStoreOwnerSessionID names the session whose durable task store backs this
// session's task view. Shared descendants retain the original owner's identity.
func (s *Session) taskStoreOwnerSessionID() string {
	if owner := s.cfg.spawn.sharedTaskStoreOwnerSessionID; owner != "" {
		return owner
	}
	return s.id
}

// getOrCreateGoalStore returns the session's goal store, initializing it lazily
// on first call. The store has its own mutex and is goroutine-safe.
func (s *Session) getOrCreateGoalStore() *goal.Store {
	s.goalStoreOnce.Do(func() {
		s.goalStore = goal.NewStore()
	})
	return s.goalStore
}

// maybeInjectTaskReminder checks whether a task-related steering message
// should be injected before the next LLM call. Returns the message and its
// events.SteeringKind*, or ("", "") when no reminder fires. The kind is
// returned rather than left for the caller to infer so the label on the
// resulting SteeringInjectedData event is ground truth, not a guess from the
// text this function just built.
func (s *Session) maybeInjectTaskReminder() (string, string) {
	s.mu.Lock()
	totalRounds := s.totalRounds
	lastRound := s.taskToolLastRound
	everUsed := s.taskToolEverUsed
	nudgeFired := s.taskNudgeFired
	s.mu.Unlock()

	roundsSinceUse := totalRounds - lastRound
	canUseTaskList := s.canInstructTool("task_list")

	// Trigger 3: never used task_list, 10+ rounds in. A session without the
	// tool is told nothing: this reminder is nothing but a call suggestion.
	if canUseTaskList && !everUsed && !nudgeFired && totalRounds >= 10 {
		s.mu.Lock()
		s.taskNudgeFired = true
		s.mu.Unlock()
		return taskReminderNudge(), events.SteeringKindTaskNudge
	}

	// Trigger 2: tasks exist, not used in 25+ rounds.
	if everUsed && roundsSinceUse >= 25 {
		store := s.getOrCreateTaskStore()
		if len(store.View()) > 0 {
			s.mu.Lock()
			s.taskToolLastRound = totalRounds
			s.mu.Unlock()
			return taskReminderForInactivity(store, canUseTaskList), events.SteeringKindTaskInactive
		}
	}

	return "", ""
}

// stringArg returns args[key] as a string, or "" when absent or not a string.
func stringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// optionalIntArg extracts an optional integer pointer from tool arguments.
func optionalIntArg(args map[string]any, key string) *int {
	v, ok := args[key]
	if !ok {
		return nil
	}
	if n, ok := v.(float64); ok {
		ni := int(n)
		return &ni
	}
	return nil
}

// TasksWithError returns a snapshot of the session's task list and the error,
// if any, encountered while loading its persisted store. A nil error with an
// empty slice is an authoritative empty store; a non-nil error means the
// aggregate is unavailable.
func (s *Session) TasksWithError() ([]task.Task, error) {
	return s.getOrCreateTaskStore().View(), s.taskStoreLoadErr
}

// Tasks returns a snapshot of the session's task list.
func (s *Session) Tasks() []task.Task {
	tasks, _ := s.TasksWithError()
	return tasks
}
