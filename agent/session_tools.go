package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/toolname"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
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

// delegateReportWholeMaxChars is the delivery budget for a delegate_send reply.
// It applies to the complete formatted reply; a larger reply keeps the
// registry's TruncTail cut and, when retention succeeds, its retained-artifact
// reference. See
// docs/superpowers/specs/2026-10-24-issue-4088-delegate-report-truncation.md
// for the measurement and the per-reply and earlier-results scope.
const delegateReportWholeMaxChars = 50_000

// resultToolName returns the effective name for the communicate tool.
func (s *Session) resultToolName() string {
	if s.cfg.ResultToolName != "" {
		return s.cfg.ResultToolName
	}
	return tool.DefaultResultToolName
}

// RegisterTool registers a custom tool at runtime.
func (s *Session) RegisterTool(name, description string, params map[string]any, fn func(ctx context.Context, args any) (any, error)) {
	// s.reg self-synchronizes (see the Session.mu lock discipline comment), so
	// Register itself can happen outside s.mu. But the cache rebuild below
	// writes s.cachedToolDefs/s.cachedSystemPrompt and reads s.env/s.envInfo,
	// all of which mu guards — those must happen under lock, mirroring
	// SetModel's pattern, so they can't race a concurrent env swap or model
	// switch.
	_ = s.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        name,
			Description: description,
			Parameters:  params,
		},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return fn(ctx, args)
		},
	})
	// Rebuild caches so the new tool appears in tool defs and system prompt.
	s.mu.Lock()
	s.rebuildToolDefsCache()
	promptWarning := s.refreshSystemPromptCache(s.env) // already holding s.mu; currentEnv() would deadlock
	s.mu.Unlock()
	s.reportPromptRenderFailure(promptWarning)
}

func (s *Session) canonicalToolName(name string) string {
	return canonicalToolName(name, s.currentProfile().ToolNameMap())
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
	// Snapshot the model's original argument bytes before prepareToolCall may
	// replace them with the repaired form. The live tool-call events carry
	// these bytes so they agree with the reload path, which uses
	// SentArguments() (RawArguments when the original was invalid JSON, else
	// the recorded Arguments). For VALID JSON the reload path's recorded
	// Arguments are the transcript-persisted canonical form (compacted and
	// HTML-escaped by encoding/json), so canonicalize valid bytes the same
	// way here; preserve the exact original bytes only for INVALID JSON (the
	// malformed cases where reload surfaces RawArguments verbatim).
	originalArgs := call.Arguments
	argsJSON := string(originalArgs)
	if json.Valid(originalArgs) {
		if canonical, err := json.Marshal(originalArgs); err == nil {
			argsJSON = string(canonical)
		}
	}
	prep := prepareToolCall(call, s.shellToolForDiagnostics(s.reg.Get(call.Name)), visibleNames, requestedVisible, s.resultToolName(), finishReason)
	call = prep.Call
	prevalidated := true
	if len(prep.Changes) > 0 {
		repaired := events.ToolCallRepairedData{
			ToolName: call.Name,
			CallID:   call.ID,
			Changes:  changeStrings(prep.Changes),
		}
		s.recordNotice(schema.NoticeInfo{Kind: schema.NoticeToolRepair, ToolRepair: &schema.ToolRepairNotice{ToolName: repaired.ToolName, CallID: repaired.CallID, Changes: repaired.Changes}})
		s.emit(repaired)
	}

	if early, changed := s.runPreToolHooks(ctx, &call, prep); early != nil {
		return *early
	} else if changed {
		// Preparation validated the original arguments. A hook may replace
		// any semantic task field, so dispatch this changed call through the
		// normal PreValidate path while unchanged prepared calls still avoid
		// running that hook twice.
		prevalidated = false
	}
	s.execToolCheckpoint("after_pre_hook")
	if err := s.abortIfClosing(ctx); err != nil {
		return skippedToolResult(call, err)
	}

	startData := events.ToolCallStartData{
		ToolName:      call.Name,
		CallID:        call.ID,
		ArgumentsJSON: argsJSON,
	}
	// Promote intent to the top-level event field for observability.
	var args map[string]any
	if !prep.RawArgumentsRejected && len(originalArgs) > 0 && json.Valid(originalArgs) {
		_ = json.Unmarshal(originalArgs, &args)
	}
	// The parent-report line for this call, excerpted once below.
	intentLine := ""
	if d := toolStartDescription(args); d != "" {
		startData.Description = d
		// The excerpt decides what the row may state: a whitespace-only
		// intent excerpts to the empty line, and an empty report is the
		// execution-start clear's word, so a call stating nothing real
		// reports nothing and the previous line stands.
		intentLine = appwire.Excerpt(d, appwire.MaxIntentRunes)
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
		s.emit(startData)
		// The report rides the start's own serialized, cancellation-checked
		// block: parallel read-only calls admit starts in lock order, so the
		// newest admitted start's line is the one that stands, and a call
		// cancelled before admission reports nothing at all.
		if intentLine != "" {
			s.noteParentJobIntent(intentLine)
		}
	}); err != nil {
		return skippedToolResult(call, err)
	}
	s.execToolCheckpoint("after_start")
	defer closeToolEvent()
	emitCanceledEnd := func(err error) {
		res := skippedToolResult(call, err)
		s.responseSideEffectsMu.Lock()
		s.emit(events.ToolCallEndData{
			ToolName:      res.ToolName,
			CallID:        res.CallID,
			ArgumentsJSON: argsJSON,
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
		res = tool.ExecResult{
			ToolName:   call.Name,
			CallID:     call.ID,
			Output:     prep.PrevalErr,
			FullOutput: prep.PrevalErr,
			IsError:    true,
			PrevalOnly: true,
			Err:        prep.Err,
		}
	} else {
		if prevalidated {
			res = s.reg.ExecutePreparedCall(ctx, s.currentEnv(), call)
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
		s.emit(events.ToolCallEndData{
			ToolName:      call.Name,
			CallID:        call.ID,
			ArgumentsJSON: argsJSON,
			Error:         skippedToolResult(call, err).FullOutput,
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
		s.emit(events.ToolCallOutputDeltaData{
			ToolName: res.ToolName,
			CallID:   res.CallID,
			Delta:    full[i:j],
		})
	}

	endData := events.ToolCallEndData{
		ToolName:      res.ToolName,
		CallID:        res.CallID,
		ArgumentsJSON: argsJSON,
		ToolState:     res.ToolState,
		OutputRef:     outputRef,
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
	s.emit(endData)
	s.responseSideEffectsMu.Unlock()
	closeToolEvent()

	if early := s.runPostToolHooks(ctx, call, res); early != nil {
		return *early
	}

	return res
}

// errorResult is a tool result that reports msg as the call's failed output.
func errorResult(call llm.ToolCallData, msg string) tool.ExecResult {
	return tool.ExecResult{
		ToolName:   call.Name,
		CallID:     call.ID,
		Output:     msg,
		FullOutput: msg,
		IsError:    true,
	}
}

// runPreToolHooks runs the PreToolUse hooks for call. A non-nil early result
// ends the call without dispatch (a hook denial, a failed context delivery or
// an unusable updatedInput). changed reports that a hook replaced call's
// arguments, so the prepared call's validation no longer applies.
func (s *Session) runPreToolHooks(ctx context.Context, call *llm.ToolCallData, prep prepareResult) (early *tool.ExecResult, changed bool) {
	if s.hookRunner == nil {
		return nil, false
	}
	hi := s.hookInput(plugin.HookPreToolUse)
	hi.ToolName = toolname.EvenerToClaude(call.Name)
	hi.ToolUseID = call.ID
	if !prep.RawArgumentsRejected && len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &hi.ToolInput)
	}

	preResult := s.hookRunner.RunPreToolUse(s.apiLogContext(ctx), hi)
	for _, m := range preResult.ModelContext {
		if err := s.deliverHookContext(m); err != nil {
			res := errorResult(*call, err.Error())
			return &res, false
		}
	}
	for _, m := range preResult.UserMessages {
		s.deliverHookUserMessage(m)
	}
	if !prep.RawArgumentsRejected && preResult.Denied {
		denyMsg := "Tool call denied by hook"
		if preResult.DenyMessage != "" {
			denyMsg = preResult.DenyMessage
		}
		res := errorResult(*call, denyMsg)
		return &res, false
	}
	if !prep.RawArgumentsRejected && len(preResult.UpdatedInput) > 0 {
		if err := applyUpdatedToolInput(call, preResult.UpdatedInput); err != nil {
			res := errorResult(*call, "invalid hook updatedInput: "+err.Error())
			return &res, false
		}
		return nil, true
	}
	return nil, false
}

// runPostToolHooks runs the PostToolUse hooks for a dispatched call. A non-nil
// early result replaces res because a hook's context could not be delivered.
func (s *Session) runPostToolHooks(ctx context.Context, call llm.ToolCallData, res tool.ExecResult) *tool.ExecResult {
	if s.hookRunner == nil {
		return nil
	}
	hi := s.hookInput(plugin.HookPostToolUse)
	hi.ToolName = toolname.EvenerToClaude(call.Name)
	hi.ToolUseID = call.ID
	hi.ToolResult = res.FullOutput   // legacy alias
	hi.ToolResponse = res.FullOutput // official field
	postResult := s.hookRunner.RunPostToolUse(s.apiLogContext(ctx), hi)
	for _, m := range postResult.ModelContext {
		if err := s.deliverHookContext(m); err != nil {
			// Keep the tool state: a turn-ending communicate's records the
			// end reason restore reads (endedOnNeedsResponse).
			failed := errorResult(call, err.Error())
			failed.ToolState = res.ToolState
			return &failed
		}
	}
	for _, m := range postResult.UserMessages {
		s.deliverHookUserMessage(m)
	}
	return nil
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
		ToolName:   call.Name,
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
				ToolName:   call.Name,
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
	// Computed before the durable append: the round's use_skill executions carry
	// their pending identity in the tool-state side channel (or a typed error),
	// and it attaches to the recorded turn's SkillTurnState first.
	skillState := s.skillTurnStateForToolRound(results)
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
		// Obligations must be durable BEFORE the carrier turn that announces them
		// is recorded — the same order admitSkillActivationBatch holds for the
		// selection route. A crash or failed save in between must never leave a
		// durable carrier whose obligation the snapshot lost, because a later fold
		// would then drop the body with nothing left to reload it. The reverse
		// window is the safe one: an obligation whose carrier never landed
		// re-delivers from its recorded source at the next dispatch seam.
		if persistErr = s.persistSkillToolObligations(skillState); persistErr != nil {
			// The round will not persist, so release the inline delivery receipts
			// it acquired before the failure and leave their durable heads
			// replayable.
			abortDelegateToolCallDeliveryCommits(commits)
			return
		}
		persistedParts := projectToolResultsForTranscript(calls, results, parts)
		live := llm.Message{Role: llm.RoleTool, Content: parts}
		persisted := llm.Message{Role: llm.RoleTool, Content: persistedParts}
		if len(commits) != 0 {
			persistErr = s.appendToolResultsWithDeliveryCommitsDurably(live, persisted, commits, skillState)
		} else if hasTerminalJobResult(calls, results) {
			persistErr = s.appendToolResultsDurably(live, persisted, skillState)
		} else if skillState != nil {
			liveTurn := schema.NewTurn(schema.TurnToolResults, live)
			liveTurn.SkillState = skillState
			persistedTurn := schema.NewTurn(schema.TurnToolResults, persisted)
			persistedTurn.SkillState = skillState
			// The obligation is already durable above, so a failed carrier write
			// returns rather than reporting a recorded round.
			persistErr = s.recordSkillCarrierDurably(liveTurn, persistedTurn)
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

// appendToolResultsDurably mirrors appendTurnWithDurableTranscriptMessage
// (session.go) with the round's typed skill state attached to both copies.
func (s *Session) appendToolResultsDurably(live, persisted llm.Message, skillState *schema.SkillTurnState) error {
	liveTurn := schema.NewTurn(schema.TurnToolResults, live)
	liveTurn.SkillState = skillState.Clone()
	persistedTurn := schema.NewTurn(schema.TurnToolResults, persisted)
	persistedTurn.SkillState = skillState.Clone()
	err := s.appendTurnAfterTranscriptWrite(
		persistedTurn,
		func() error { return s.writeTranscriptDurableLocked(persistedTurn) },
		func() { s.history = append(s.history, liveTurn) },
	)
	if err != nil {
		s.emit(events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	return err
}

// appendToolResultsWithDeliveryCommitsDurably persists the round's tool
// results durably with their delegate delivery commits and, when the round
// included skill tool calls, the typed skill state on both turn copies.
func (s *Session) appendToolResultsWithDeliveryCommitsDurably(live, persisted llm.Message, commits []delegateToolCallDeliveryCommit, skillState *schema.SkillTurnState) error {
	liveTurn := schema.NewTurn(schema.TurnToolResults, live)
	liveTurn.SkillState = skillState.Clone()
	persistedTurn := liveTurn
	persistedTurn.Message = persisted
	persistedTurn.SkillState = skillState.Clone()
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
		func() error { return s.writeTranscriptSyncedLocked(persistedTurn) },
		func() { s.history = append(s.history, liveTurn) },
	); err != nil {
		abortDelegateToolCallDeliveryCommits(commits)
		return err
	}
	var completionErrs []error
	requeue := false
	// commits come grouped by tool call; each call's run completes in order.
	var run []*delegateToolResultCommit
	for i, binding := range commits {
		if binding.commit != nil {
			run = append(run, binding.commit)
		}
		if i+1 < len(commits) && commits[i+1].toolCallID == binding.toolCallID {
			continue
		}
		completed, err := completeDelegateDeliveryCommits(run)
		run = nil
		if err != nil {
			completionErrs = append(completionErrs, err)
			requeue = true
		}
		for _, plans := range completed {
			if err := s.executeDelegateMutationPlans(plans); err != nil {
				completionErrs = append(completionErrs, err)
			}
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
	s.emit(events.ToolResultImagesPersistedData{CallIDs: callIDs})
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

// trackReadFile records that a file has been read in this session.
func (s *Session) trackReadFile(path string) {
	s.readFilesMu.Lock()
	s.readFiles[s.resolveFilePath(path)] = true
	s.readFilesMu.Unlock()
}

// readBeforeWriteWarning returns a warning string if the file exists but hasn't
// been read in this session. Returns "" for new files or previously-read files.
func (s *Session) readBeforeWriteWarning(path string) string {
	return s.readBeforeWriteWarningIn(s.currentEnv(), path)
}

func (s *Session) readBeforeWriteWarningIn(env execenv.ExecutionEnvironment, path string) string {
	abs := s.resolveFilePath(path)
	s.readFilesMu.RLock()
	_, seen := s.readFiles[abs]
	s.readFilesMu.RUnlock()
	if seen {
		return ""
	}
	// New file creation is exempt from the warning.
	if !env.FileExists(path) {
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
