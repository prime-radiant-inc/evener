package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/internal/toolname"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

// ctxKey is a private type for context keys in this package.
type ctxKey string

// ctxToolCallID carries the tool call ID into tool execution closures via context.
const ctxToolCallID ctxKey = "toolCallID"

// ctxToolItemID carries the provider/tool item ID into tool execution closures via context.
const ctxToolItemID ctxKey = "toolItemID"

// ctxParentJobID carries the delegate job ID into child session spawn plumbing.
const ctxParentJobID ctxKey = "parentJobID"

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

// resultToolName returns the effective name for the communicate tool.
func (s *Session) resultToolName() string {
	if s.cfg.ResultToolName != "" {
		return s.cfg.ResultToolName
	}
	return "communicate"
}

// RegisterTool registers a custom tool at runtime.
func (s *Session) RegisterTool(name, description string, params map[string]any, fn func(ctx context.Context, args any) (any, error)) {
	_ = s.reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{
			Definition: llm.ToolDefinition{
				Name:        name,
				Description: description,
				Parameters:  params,
			},
		},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return fn(ctx, args)
		},
	})
	// Rebuild caches so the new tool appears in tool defs and system prompt.
	s.rebuildToolDefsCache()
	s.refreshSystemPromptCache()
}

// describeImage makes a side-channel API call with no tools to describe an image
// using the model's native vision. Returns the text description, or "" on error.
// The call includes context from the current task so the description is relevant.
func (s *Session) describeImage(ctx context.Context, r tool.ExecResult) string {
	if len(r.ImageData) == 0 {
		return ""
	}
	// Skip for explorer agents — they're just inventorying files, not analyzing images.
	if s.cfg.AgentName == "explorer" {
		return ""
	}

	// Use the caller's stated purpose as the vision prompt. The calling LLM
	// knows what it needs — we just ask the vision model to answer that question.
	purpose := strings.TrimSpace(r.ImagePurpose)
	if purpose == "" {
		purpose = "Describe what you see in this image in thorough detail."
	}

	var prompt strings.Builder
	prompt.WriteString(purpose)
	prompt.WriteString("\n\nBe thorough — the reader cannot see the image and will rely entirely on your description.")

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

	// Snapshot the model inputs under s.mu: the vision side-channel runs during
	// the round, so a concurrent SetModel/SetReasoningEffort (which mutate these
	// under s.mu) must not race these reads (PRI-1958 A2/A4).
	effortOverride := ""
	if s.taskStore != nil {
		if current, ok := s.taskStore.CurrentInProgress(); ok && current.ReasoningEffort != "" {
			effortOverride = current.ReasoningEffort
		}
	}
	s.mu.Lock()
	profile := s.profile
	effort := strings.TrimSpace(s.cfg.ReasoningEffort)
	s.mu.Unlock()
	if effortOverride != "" {
		effort = effortOverride
	}
	req := llm.Request{
		Model:    profile.Model(),
		Provider: profile.ID(),
		Messages: []llm.Message{
			{
				Role: llm.RoleUser,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: prompt.String()},
					mediaPart,
				},
			},
		},
		// No tools — force text-only response.
		AdapterTimeout: &llm.AdapterTimeout{
			Connect:    10 * time.Second,
			Request:    2 * time.Minute,
			StreamRead: 30 * time.Second,
		},
	}
	// Vision descriptions need sufficient reasoning to be accurate.
	// Floor at "high" regardless of the current task's effort level. Use the
	// shared rank so "max" (and any future top-tier name) isn't downgraded.
	if llm.ReasoningEffortRank(effort) < llm.ReasoningEffortRank("high") {
		effort = "high"
	}
	// This request is built manually (not via buildModelRequest), so clamp the
	// effort to the model's supported levels here too — otherwise a top-tier
	// alias like "max"/"xhigh" can reach a model that doesn't accept it.
	effort = llm.ClampReasoningEffort(effort, profile.ReasoningEffortLevels())
	req.ReasoningEffort = &effort
	s.applyModelRequestMetadata(profile, &req)

	resp, err := s.client.Complete(ctx, req)
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("vision side-channel failed: %v", err)})
		return ""
	}

	return strings.TrimSpace(resp.Message.Text())
}

func (s *Session) canonicalToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	for canonical, provider := range s.currentProfile().ToolNameMap() {
		if provider == name {
			return canonical
		}
	}
	return name
}

func (s *Session) canonicalizeToolNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		canonical := s.canonicalToolName(name)
		if canonical == "" || seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, canonical)
	}
	return out
}

func (s *Session) providerToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if provider, ok := s.profile.ToolNameMap()[name]; ok {
		return provider
	}
	return name
}

func (s *Session) providerVisibleToolNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		visible := s.providerToolName(name)
		if visible == "" || seen[visible] {
			continue
		}
		seen[visible] = true
		out = append(out, visible)
	}
	sort.Strings(out)
	return out
}

func (s *Session) execTool(ctx context.Context, call llm.ToolCallData) tool.ExecResult {
	if err := s.abortIfClosing(ctx); err != nil {
		return skippedToolResult(call, err)
	}
	// PreToolUse hooks
	if s.hookRunner != nil {
		hi := s.hookInput(plugin.HookPreToolUse)
		hi.ToolName = toolname.SerfToClaude(call.Name)
		hi.ToolUseID = call.ID
		if len(call.Arguments) > 0 {
			_ = json.Unmarshal(call.Arguments, &hi.ToolInput)
		}

		preResult := s.hookRunner.RunPreToolUse(ctx, hi)
		for _, m := range preResult.ModelContext {
			s.deliverHookContext(m)
		}
		for _, m := range preResult.UserMessages {
			s.deliverHookUserMessage(m)
		}
		if preResult.Denied {
			denyMsg := "Tool call denied by hook"
			if preResult.DenyMessage != "" {
				denyMsg = preResult.DenyMessage
			}
			return tool.ExecResult{
				ToolName:   call.Name,
				CallID:     call.ID,
				Output:     denyMsg,
				FullOutput: denyMsg,
				IsError:    true,
			}
		}
		if len(preResult.UpdatedInput) > 0 {
			if err := applyUpdatedToolInput(&call, preResult.UpdatedInput); err != nil {
				msg := "invalid hook updatedInput: " + err.Error()
				return tool.ExecResult{
					ToolName:   call.Name,
					CallID:     call.ID,
					Output:     msg,
					FullOutput: msg,
					IsError:    true,
				}
			}
		}
	}
	if err := s.abortIfClosing(ctx); err != nil {
		return skippedToolResult(call, err)
	}

	argsJSON, _ := json.Marshal(call.Arguments)
	startData := events.ToolCallStartData{
		ToolName:      call.Name,
		CallID:        call.ID,
		ArgumentsJSON: string(argsJSON),
	}
	// Promote purpose to the top-level event field for observability.
	var args map[string]any
	if len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &args)
	}
	if purpose, ok := args["purpose"].(string); ok && purpose != "" {
		startData.Description = purpose
	} else if desc, ok := args["description"].(string); ok && desc != "" {
		// Backward compatibility for older shell calls and transcripts.
		startData.Description = desc
	}
	startEmitted := false
	toolEventOpen := false
	closeToolEvent := func() {
		if toolEventOpen {
			s.toolEventsWG.Done()
			toolEventOpen = false
		}
	}
	if err := s.withResponseSideEffects(ctx, func() {
		s.toolEventsWG.Add(1)
		startEmitted = true
		toolEventOpen = true
		s.emit(events.EventToolCallStart, startData)
	}); err != nil {
		return skippedToolResult(call, err)
	}
	defer closeToolEvent()
	emitCanceledEnd := func(err error) {
		if !startEmitted {
			return
		}
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
		startEmitted = false
	}

	// Session-level tools (subagents) are registered in the registry with closures.
	ctx = context.WithValue(ctx, ctxToolCallID, call.ID)
	if call.ItemID != "" {
		ctx = context.WithValue(ctx, ctxToolItemID, call.ItemID)
	}
	toolStart := time.Now()
	if err := s.abortIfClosing(ctx); err != nil {
		emitCanceledEnd(err)
		return skippedToolResult(call, err)
	}
	res := s.reg.ExecuteCall(ctx, s.env, call)
	res.DurationMS = time.Since(toolStart).Milliseconds()
	if err := s.errIfClosing(); err != nil {
		emitCanceledEnd(err)
		return skippedToolResult(call, err)
	}

	s.responseSideEffectsMu.Lock()
	if err := s.errIfClosing(); err != nil {
		s.emit(events.EventToolCallEnd, events.ToolCallEndData{
			ToolName:      call.Name,
			CallID:        call.ID,
			ArgumentsJSON: string(call.Arguments),
			Error:         skippedToolResult(call, err).FullOutput,
		})
		s.responseSideEffectsMu.Unlock()
		closeToolEvent()
		return skippedToolResult(call, err)
	}
	// Emit output deltas (best-effort). Even for non-streaming tools, this gives consumers a uniform
	// incremental event pattern that mirrors provider LLM streaming.
	full := res.FullOutput
	const chunk = 4000
	for i := 0; i < len(full); i += chunk {
		j := i + chunk
		if j > len(full) {
			j = len(full)
		}
		s.emit(events.EventToolCallOutputDelta, events.ToolCallOutputDeltaData{
			ToolName: res.ToolName,
			CallID:   res.CallID,
			Delta:    full[i:j],
		})
	}

	endData := events.ToolCallEndData{
		ToolName:      res.ToolName,
		CallID:        res.CallID,
		ArgumentsJSON: string(call.Arguments),
		ToolState:     res.ToolState,
	}
	if res.IsError {
		endData.Error = res.FullOutput
	} else {
		endData.Output = res.FullOutput
	}
	s.emit(events.EventToolCallEnd, endData)
	s.responseSideEffectsMu.Unlock()
	closeToolEvent()
	startEmitted = false

	// PostToolUse hooks
	if s.hookRunner != nil {
		hi := s.hookInput(plugin.HookPostToolUse)
		hi.ToolName = toolname.SerfToClaude(call.Name)
		hi.ToolUseID = call.ID
		hi.ToolResult = res.FullOutput   // legacy alias
		hi.ToolResponse = res.FullOutput // official field
		postResult := s.hookRunner.RunPostToolUse(ctx, hi)
		for _, m := range postResult.ModelContext {
			s.deliverHookContext(m)
		}
		for _, m := range postResult.UserMessages {
			s.deliverHookUserMessage(m)
		}
	}

	return res
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
	for k, v := range updated {
		args[k] = v
	}
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
				DurationMS:     res.DurationMS,
				ToolState:      res.ToolState,
				ImageData:      res.ImageData,
				ImageMediaType: res.ImageMediaType,
			},
		})
	}
	s.appendTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: parts})
}

func (s *Session) appendToolResults(ctx context.Context, calls []llm.ToolCallData, results []tool.ExecResult, parts []llm.ContentPart) error {
	if abortErr := s.withResponseSideEffects(ctx, func() {
		s.appendTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: parts})
		// Persist the completed tool round so resumed sessions always include
		// tool_result turns for any prior assistant tool calls.
		s.maybeAutoSave()
	}); abortErr != nil {
		if ctx.Err() != nil && !s.isClosingOrClosed() {
			s.appendCanceledToolResults(calls, results, abortErr)
		}
		return abortErr
	}
	return nil
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
// canonical→provider) and adds the shared "purpose" parameter to work tools.
// communicate/result tools omit purpose because their user-facing message and
// strict output envelope already carry the result intent.
func wireToolDef(td llm.ToolDefinition, nameMap map[string]string, resultToolName string) llm.ToolDefinition {
	canonicalName := td.Name
	if mapped, ok := nameMap[td.Name]; ok {
		td.Name = mapped
	}
	if isResultToolDefinition(canonicalName, td.Name, resultToolName) {
		return tool.WithoutPurposeParameter(td)
	}
	return tool.WithPurposeParameter(td)
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
	// provider-visible wire form (provider-specific rename + purpose param).
	nameMap := s.profile.ToolNameMap() // canonical → provider, may be nil

	var defs []llm.ToolDefinition
	included := make(map[string]bool)
	for _, td := range s.profile.ToolDefinitions() {
		if registered[td.Name] {
			if td.Name == "delegate" {
				td = tool.DefDelegate(s.delegateAgentTypeNames())
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
			wire := wireToolDef(td, nameMap, s.resultToolName())
			defs = append(defs, wire)
			included[td.Name] = true // canonical
			// Also track the provider-mapped name so loop 3 (registry tools)
			// won't add a registry tool whose canonical name matches the
			// provider name (e.g. OpenAI maps glob→list_dir; the registry
			// also has a separate list_dir tool that must be excluded).
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
		// Normalize empty parameters to a valid object schema so the LLM
		// client doesn't reject the tool definition.
		if td.Parameters != nil && td.Parameters["type"] == nil {
			td.Parameters = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		defs = append(defs, td)
	}
	for i := range defs {
		if isResultToolDefinition(defs[i].Name, defs[i].Name, s.resultToolName()) {
			defs[i] = tool.WithoutPurposeParameter(defs[i])
		} else {
			defs[i] = tool.WithPurposeParameter(defs[i])
		}
	}

	s.cachedToolDefs = defs
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
	if !s.env.FileExists(path) {
		return ""
	}
	return "[WARNING: Writing to file that has not been read in this session. Consider reading first.]\n"
}

func (s *Session) resolveFilePath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.env.WorkingDirectory(), p)
}

func (s *Session) getOrCreateTaskStore() *task.TaskStore {
	s.taskStoreOnce.Do(func() {
		if s.cfg.spawn.sharedTaskStore != nil {
			s.taskStore = s.cfg.spawn.sharedTaskStore
			return
		}
		dir := s.stateDir
		if dir == "" {
			dir = s.env.WorkingDirectory()
		}
		s.taskStore = task.NewTaskStore(dir, s.id)
		_ = s.taskStore.Load()
	})
	return s.taskStore
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
// should be injected before the next LLM call. Returns the message or "".
func (s *Session) maybeInjectTaskReminder() string {
	s.mu.Lock()
	totalRounds := s.totalRounds
	lastRound := s.taskToolLastRound
	everUsed := s.taskToolEverUsed
	nudgeFired := s.taskNudgeFired
	s.mu.Unlock()

	roundsSinceUse := totalRounds - lastRound

	// Trigger 3: never used task_list, 10+ rounds in.
	if !everUsed && !nudgeFired && totalRounds >= 10 {
		s.mu.Lock()
		s.taskNudgeFired = true
		s.mu.Unlock()
		return taskReminderNudge()
	}

	// Trigger 2: tasks exist, not used in 25+ rounds.
	if everUsed && roundsSinceUse >= 25 {
		store := s.getOrCreateTaskStore()
		if len(store.View()) > 0 {
			s.mu.Lock()
			s.taskToolLastRound = totalRounds
			s.mu.Unlock()
			return taskReminderForInactivity(store)
		}
	}

	return ""
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

// optionalPositiveIntArg is like optionalIntArg but returns nil when the value
// is absent OR <= 0. Use this for parameters where zero or negative means "no
// selection" rather than "select item 0".
func optionalPositiveIntArg(args map[string]any, key string) *int {
	n := optionalIntArg(args, key)
	if n == nil || *n <= 0 {
		return nil
	}
	return n
}

// Tasks returns a snapshot of the session's task list.
func (s *Session) Tasks() []task.Task {
	return s.getOrCreateTaskStore().View()
}
