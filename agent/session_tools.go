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

	"primeradiant.com/serf/llm"
)

// ctxKey is a private type for context keys in this package.
type ctxKey string

// ctxToolCallID carries the tool call ID into tool execution closures via context.
const ctxToolCallID ctxKey = "toolCallID"

const (
	defaultAgentName = "default"
)

// resultToolName returns the effective name for the communicate tool.
func (s *Session) resultToolName() string {
	if s.cfg.ResultToolName != "" {
		return s.cfg.ResultToolName
	}
	return "communicate"
}

// RegisterTool registers a custom tool at runtime.
func (s *Session) RegisterTool(name, description string, params map[string]any, fn func(ctx context.Context, args any) (any, error)) {
	_ = s.reg.Register(RegisteredTool{
		Tool: llm.Tool{
			Definition: llm.ToolDefinition{
				Name:        name,
				Description: description,
				Parameters:  params,
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
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
func (s *Session) describeImage(ctx context.Context, r ToolExecResult) string {
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
	// Floor at "high" regardless of the current task's effort level.
	effortRank := map[string]int{"low": 1, "medium": 2, "high": 3, "xhigh": 4}
	if effortRank[effort] < effortRank["high"] {
		effort = "high"
	}
	req.ReasoningEffort = &effort
	s.applyModelRequestMetadata(profile, &req)

	resp, err := s.client.Complete(ctx, req)
	if err != nil {
		s.emit(EventWarning, WarningData{Message: fmt.Sprintf("vision side-channel failed: %v", err)})
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

func (s *Session) execTool(ctx context.Context, call llm.ToolCallData) ToolExecResult {
	if err := s.abortIfClosing(ctx); err != nil {
		return skippedToolResult(call, err)
	}
	// PreToolUse hooks
	if s.hookRunner != nil {
		hi := s.hookInput(HookPreToolUse)
		hi.ToolName = MapSerfToolNameToClaude(call.Name)
		if len(call.Arguments) > 0 {
			_ = json.Unmarshal(call.Arguments, &hi.ToolInput)
		}

		preResult := s.hookRunner.RunPreToolUse(ctx, hi)
		for _, msg := range preResult.SystemMessages {
			s.Steer(msg)
		}
		if preResult.Denied {
			denyMsg := "Tool call denied by hook"
			if preResult.DenyMessage != "" {
				denyMsg = preResult.DenyMessage
			}
			return ToolExecResult{
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
				return ToolExecResult{
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
	startData := ToolCallStartData{
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
		s.emit(EventToolCallStart, startData)
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
		s.emit(EventToolCallEnd, ToolCallEndData{
			ToolName: res.ToolName,
			CallID:   res.CallID,
			Error:    res.FullOutput,
		})
		s.responseSideEffectsMu.Unlock()
		closeToolEvent()
		startEmitted = false
	}

	// Session-level tools (subagents) are registered in the registry with closures.
	ctx = context.WithValue(ctx, ctxToolCallID, call.ID)
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
		s.emit(EventToolCallEnd, ToolCallEndData{
			ToolName: call.Name,
			CallID:   call.ID,
			Error:    skippedToolResult(call, err).FullOutput,
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
		s.emit(EventToolCallOutputDelta, ToolCallOutputDeltaData{
			ToolName: res.ToolName,
			CallID:   res.CallID,
			Delta:    full[i:j],
		})
	}

	endData := ToolCallEndData{
		ToolName:  res.ToolName,
		CallID:    res.CallID,
		ToolState: res.ToolState,
	}
	if res.IsError {
		endData.Error = res.FullOutput
	} else {
		endData.Output = res.FullOutput
	}
	s.emit(EventToolCallEnd, endData)
	s.responseSideEffectsMu.Unlock()
	closeToolEvent()
	startEmitted = false

	// PostToolUse hooks
	if s.hookRunner != nil {
		hi := s.hookInput(HookPostToolUse)
		hi.ToolName = MapSerfToolNameToClaude(call.Name)
		hi.ToolResult = res.FullOutput
		postResult := s.hookRunner.RunPostToolUse(ctx, hi)
		for _, msg := range postResult.SystemMessages {
			s.Steer(msg)
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

func skippedToolResult(call llm.ToolCallData, err error) ToolExecResult {
	msg := "tool skipped: session is closing"
	if err != nil && err != context.Canceled {
		msg = "tool skipped: " + err.Error()
	}
	return ToolExecResult{
		ToolName:   call.Name,
		CallID:     call.ID,
		Output:     msg,
		FullOutput: msg,
		IsError:    true,
	}
}

func (s *Session) appendCanceledToolResults(calls []llm.ToolCallData, results []ToolExecResult, err error) {
	if len(calls) == 0 {
		return
	}
	if err == nil {
		err = context.Canceled
	}
	parts := make([]llm.ContentPart, 0, len(calls))
	for i, call := range calls {
		res := ToolExecResult{}
		if i < len(results) {
			res = results[i]
		}
		if res.CallID == "" {
			msg := "tool canceled: " + err.Error()
			res = ToolExecResult{
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
				ImageData:      res.ImageData,
				ImageMediaType: res.ImageMediaType,
			},
		})
	}
	s.appendTurn(TurnToolResults, llm.Message{Role: llm.RoleTool, Content: parts})
}

func (s *Session) appendToolResults(ctx context.Context, calls []llm.ToolCallData, results []ToolExecResult, parts []llm.ContentPart) error {
	if abortErr := s.withResponseSideEffects(ctx, func() {
		s.appendTurn(TurnToolResults, llm.Message{Role: llm.RoleTool, Content: parts})
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

// customToolDescriptions returns formatted descriptions of tools in the registry
// that were registered after session initialization (not core or MCP tools).
func (s *Session) customToolDescriptions() string {
	// Build MCP tool name set for exclusion (MCP tools have their own section).
	mcpNames := make(map[string]bool, len(s.mcpTools))
	for _, td := range s.mcpTools {
		mcpNames[td.Name] = true
	}

	var b strings.Builder
	for _, td := range s.reg.Definitions() {
		if s.coreToolNames[td.Name] || mcpNames[td.Name] {
			continue
		}
		desc := strings.TrimSpace(td.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString(fmt.Sprintf("- %s: %s\n", td.Name, desc))
	}
	return b.String()
}

// allToolDefinitions returns cached tool definitions.
// The cache is built once at session init via rebuildToolDefsCache.
func (s *Session) allToolDefinitions(_ int) []llm.ToolDefinition {
	return s.cachedToolDefs
}

func (s *Session) defaultToolSummaryForAgent(agent PluginAgent) string {
	allTools, allowedTools, deniedTools := baseSubagentToolPolicy(&agent)
	var canonical []string
	switch {
	case allTools || len(allowedTools) == 0:
		canonical = removeStrings(s.reg.Names(), deniedTools)
	default:
		canonical = append([]string(nil), allowedTools...)
		canonical = appendUniqueStrings(canonical, s.resultToolName())
	}
	canonical = removeRootOnlyAgentManagementTools(canonical)
	return formatToolNamesForPrompt(s.providerVisibleToolNames(canonical))
}

func (s *Session) availableAgentEntries() []AgentEntry {
	names := make([]string, 0, len(s.pluginAgents))
	for name, agent := range s.pluginAgents {
		if agentUsesRootOnlyManagementTools(agent) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]AgentEntry, 0, len(names))
	for _, name := range names {
		agent := s.pluginAgents[name]
		entries = append(entries, AgentEntry{
			Name:         name,
			Description:  agent.Description,
			DefaultTools: s.defaultToolSummaryForAgent(agent),
			TaskList:     agentTaskEntries(agent.Tasks),
		})
	}
	return entries
}

// rebuildToolDefsCache builds the cached tool definition lists from the
// current profile, MCP tools, and registry state. Called once at session init
// and again if tools are added at runtime (e.g. MCP or custom tools).
func (s *Session) rebuildToolDefsCache() {
	registered := s.reg.RegisteredNames()

	// Profile tool definitions use provider-specific names (e.g. "exec_command"
	// for OpenAI). Build a reverse map from provider name → canonical name so
	// we can filter against the registry which uses canonical names.
	nameMap := s.profile.ToolNameMap() // canonical → provider, may be nil
	reverseMap := make(map[string]string, len(nameMap))
	for canonical, provider := range nameMap {
		reverseMap[provider] = canonical
	}

	var defs []llm.ToolDefinition
	included := make(map[string]bool)
	for _, td := range s.profile.ToolDefinitions() {
		canonical := td.Name
		if c, ok := reverseMap[td.Name]; ok {
			canonical = c
		}
		if registered[canonical] {
			defs = append(defs, td)
			included[canonical] = true
			// Also track the provider-mapped name so loop 3 (registry tools)
			// won't add a registry tool whose canonical name matches the
			// provider name (e.g. OpenAI maps glob→list_dir; the registry
			// also has a separate list_dir tool that must be excluded).
			included[td.Name] = true
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
		defs[i] = withPurposeParameter(defs[i])
	}

	s.cachedToolDefs = defs
}

// Tool registration.

// toolDeps is the dependency surface the core tool handler closures need from
// their owning session. The registerXxxTools helpers capture a *toolDeps
// instead of a *Session, which cuts the tools⇄session back-cycle: the handler
// closures no longer reference the concrete *Session type. Every member
// forwards to an existing *Session method or field, preserving all locking and
// ordering; toolDeps adds no behavior of its own.
//
// Subagent spawn/wait/send/close are deliberately NOT here — that is a separate
// seam. registerSubagentTools still captures *Session directly.
type toolDeps struct {
	// emit publishes a session event (best-effort, same as Session.emit).
	emit func(kind EventKind, data any)

	// steering queue access for the communicate handler.
	steer           func(msg string)
	drainSteering   func() []steeringMessage
	prependSteering func(entries []steeringMessage)

	// abort returns a non-nil error when the session is closing (= Session.abortIfClosing).
	abort func(ctx context.Context) error

	// resultToolName is the effective name of the communicate tool.
	resultToolName func() string

	// cmdTimeouts is a live getter for the default and max shell command
	// timeouts. It reads cfg on every call so SetTimeout mutations are visible;
	// the values are NOT snapshotted at registration time.
	cmdTimeouts func() (def, max int)

	// readGuard exposes the read-before-write guardrail without leaking the raw
	// readFiles map or its mutex.
	readGuard readGuard

	// taskGuard exposes task-store access and the task reminder bookkeeping,
	// all guarded by the session's own mutex.
	taskGuard taskGuard

	// web exposes the web tools with the profile and client hidden behind them.
	web webDeps

	// setCommunicateResult records the communicate tool's result on the session
	// (fields stay Session-owned; this is the only writer reachable from the handler).
	setCommunicateResult func(awaitReply bool, message, reply, output string)

	// skill looks up a discovered skill by name.
	skill func(name string) (SkillMeta, bool)

	// reasoningEffortLevels is captured once for the task_list tool definition.
	reasoningEffortLevels []string

	// webSearchEnabled is the resolved decision (BehaviorTag == "google") for
	// whether the function-tool web_search should be registered.
	webSearchEnabled bool
}

// readGuard wraps the read-before-write guardrail. It forwards to the
// Session-owned readFiles map + mutex via TrackRead/ReadBeforeWriteWarning so
// the handlers never touch the raw map.
type readGuard struct {
	trackRead              func(path string)
	readBeforeWriteWarning func(path string) string
}

func (g readGuard) TrackRead(path string) { g.trackRead(path) }

func (g readGuard) ReadBeforeWriteWarning(path string) string {
	return g.readBeforeWriteWarning(path)
}

// taskGuard is a thin facade over Session-owned task state. It uses the same
// s.mu as the rest of the session — it does NOT introduce a second mutex.
type taskGuard struct {
	getOrCreateTaskStore func() *TaskStore
	markUsed             func()
	setReasoningEffort   func(effort string)
}

func (g taskGuard) Store() *TaskStore { return g.getOrCreateTaskStore() }

// MarkUsed records that the task_list tool was invoked this round (updates the
// reminder counters under s.mu).
func (g taskGuard) MarkUsed() { g.markUsed() }

func (g taskGuard) SetReasoningEffort(effort string) { g.setReasoningEffort(effort) }

// webDeps holds the bound web tool functions. The profile and client stay
// hidden inside the closures captured here.
type webDeps struct {
	fetch  func(ctx context.Context, rawURL, question string) (any, error)
	search func(ctx context.Context, query string) (any, error)
}

// newToolDeps builds the tool dependency surface from a session. Every member
// is a forwarder to an existing method or field, so behavior and locking are
// unchanged. Built once in registerCoreTools.
func newToolDeps(s *Session) *toolDeps {
	return &toolDeps{
		emit:            s.emit,
		steer:           s.Steer,
		drainSteering:   s.drainSteering,
		prependSteering: s.prependSteering,
		abort:           s.abortIfClosing,
		resultToolName:  s.resultToolName,
		cmdTimeouts: func() (int, int) {
			return s.cfg.DefaultCommandTimeoutMS, s.cfg.MaxCommandTimeoutMS
		},
		readGuard: readGuard{
			trackRead:              s.trackReadFile,
			readBeforeWriteWarning: s.readBeforeWriteWarning,
		},
		taskGuard: taskGuard{
			getOrCreateTaskStore: s.getOrCreateTaskStore,
			markUsed: func() {
				s.mu.Lock()
				s.taskToolEverUsed = true
				s.taskToolLastRound = s.totalRounds
				s.mu.Unlock()
			},
			setReasoningEffort: s.SetReasoningEffort,
		},
		web: webDeps{
			fetch:  s.webFetch,
			search: s.webSearch,
		},
		setCommunicateResult: func(awaitReply bool, message, reply, output string) {
			s.mu.Lock()
			s.communicated = true
			s.communicateAwaitReply = awaitReply
			s.communicateText = message
			s.communicateReply = reply
			s.communicateOutput = output
			s.mu.Unlock()
		},
		skill: func(name string) (SkillMeta, bool) {
			meta, ok := s.skills[name]
			return meta, ok
		},
		reasoningEffortLevels: s.profile.ReasoningEffortLevels(),
		webSearchEnabled:      s.profile.BehaviorTag() == "google",
	}
}

func registerCoreTools(reg *ToolRegistry, s *Session) error {
	deps := newToolDeps(s)
	if err := registerFileTools(reg, deps); err != nil {
		return err
	}
	if err := registerShellTools(reg, deps); err != nil {
		return err
	}
	registerSubagentTools(reg, s)
	registerTaskTools(reg, deps)
	registerWebTools(reg, deps)
	registerCommunicateTool(reg, deps)
	registerSkillTool(reg, deps)
	return nil
}

func registerFileTools(reg *ToolRegistry, deps *toolDeps) error {
	// read_file
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defReadFile(), ReadOnly: true},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			offset := optionalIntArg(args, "offset")
			limit := optionalIntArg(args, "limit")
			purpose, _ := args["purpose"].(string)
			result, err := env.ReadFile(path, offset, limit)
			if err == nil {
				deps.readGuard.TrackRead(path)
				// If the file is an image or document (PDF), return an
				// ImageResult so the vision side-channel can process it.
				if img := parseImageResult(path, result); img != nil {
					img.Purpose = purpose
					return *img, nil
				}
				if doc := parseDocumentResult(path, result); doc != nil {
					doc.Purpose = purpose
					return *doc, nil
				}
			}
			return result, err
		},
	}); err != nil {
		return err
	}

	// write_file
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defWriteFile()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			warn := deps.readGuard.ReadBeforeWriteWarning(path)
			result, err := env.WriteFile(path, fmt.Sprint(args["content"]))
			if err == nil && warn != "" {
				return warn + fmt.Sprint(result), nil
			}
			return result, err
		},
	}); err != nil {
		return err
	}

	// edit_file
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defEditFile()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			replaceAll := false
			if v, ok := args["replace_all"].(bool); ok {
				replaceAll = v
			}
			warn := deps.readGuard.ReadBeforeWriteWarning(path)
			result, err := env.EditFile(path, fmt.Sprint(args["old_string"]), fmt.Sprint(args["new_string"]), replaceAll)
			if err == nil && warn != "" {
				return warn + fmt.Sprint(result), nil
			}
			return result, err
		},
	})

	return nil
}

func registerShellTools(reg *ToolRegistry, deps *toolDeps) error {
	// shell
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defShell()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			cmd := fmt.Sprint(args["command"])
			defTimeout, maxTimeout := deps.cmdTimeouts()
			timeout := defTimeout
			if v, ok := args["timeout_ms"].(float64); ok && int(v) > 0 {
				timeout = int(v)
			}
			if maxTimeout > 0 && timeout > maxTimeout {
				timeout = maxTimeout
			}
			res, err := env.ExecCommand(ctx, cmd, timeout, "", nil)

			// Return a line-oriented tool output so line truncation works as intended for shell output.
			var b strings.Builder
			if strings.TrimSpace(res.Stdout) != "" {
				b.WriteString(res.Stdout)
				if !strings.HasSuffix(res.Stdout, "\n") {
					b.WriteString("\n")
				}
			}
			if strings.TrimSpace(res.Stderr) != "" {
				b.WriteString(res.Stderr)
				if !strings.HasSuffix(res.Stderr, "\n") {
					b.WriteString("\n")
				}
			}
			if errors.Is(err, context.Canceled) && !res.TimedOut {
				b.WriteString("[ERROR: Command was canceled before completion. Partial output is shown above.]\n")
			} else if res.TimedOut {
				b.WriteString(fmt.Sprintf("[ERROR: Command timed out after %dms. Partial output is shown above.\nYou can retry with a longer timeout by setting the timeout_ms parameter.]\n", timeout))
			}
			b.WriteString(fmt.Sprintf("exit_code=%d duration_ms=%d timed_out=%t\n", res.ExitCode, res.DurationMS, res.TimedOut))
			return b.String(), err
		},
	}); err != nil {
		return err
	}

	// list_dir (Gemini-aligned)
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defListDir(), ReadOnly: true},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["path"])
			depth := 1
			if v, ok := args["depth"].(float64); ok && int(v) > 0 {
				depth = int(v)
			}
			return env.ListDirectory(path, depth)
		},
	})

	// grep
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defGrep(), ReadOnly: true},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := fmt.Sprint(args["pattern"])
			path := fmt.Sprint(args["path"])
			glob := fmt.Sprint(args["glob_filter"])
			ci := false
			if v, ok := args["case_insensitive"].(bool); ok {
				ci = v
			}
			maxRes := 100
			if v, ok := args["max_results"].(float64); ok && int(v) > 0 {
				maxRes = int(v)
			}
			outputMode := ""
			if v, ok := args["output_mode"].(string); ok {
				outputMode = v
			}
			return env.Grep(pat, path, glob, ci, maxRes, outputMode)
		},
	}); err != nil {
		return err
	}

	// glob
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defGlob(), ReadOnly: true},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := fmt.Sprint(args["pattern"])
			path := fmt.Sprint(args["path"])
			matches, err := env.Glob(pat, path)
			if err != nil {
				return "", err
			}
			return strings.Join(matches, "\n"), nil
		},
	}); err != nil {
		return err
	}

	// apply_patch (OpenAI-specific; best-effort implementation lives in this repo)
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defApplyPatch()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			patch := fmt.Sprint(args["patch"])
			return ApplyPatch(env.WorkingDirectory(), patch)
		},
	})

	return nil
}

func registerSubagentTools(reg *ToolRegistry, s *Session) {
	// Subagent tools (best-effort; synchronous completion for v1).
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defSpawnAgent()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			task := fmt.Sprint(args["task"])
			model := ""
			if v, ok := args["model"]; ok && v != nil {
				model = fmt.Sprint(v)
			}
			maxTurns := 0
			if v, ok := args["max_turns"].(float64); ok {
				maxTurns = int(v)
			}
			agentType := ""
			if v, ok := args["agent_type"]; ok && v != nil {
				agentType = fmt.Sprint(v)
			}
			blocking := false
			if v, ok := args["blocking"].(bool); ok {
				blocking = v
			}
			reasoningEffort := ""
			if v, ok := args["reasoning_effort"]; ok && v != nil {
				reasoningEffort = fmt.Sprint(v)
			}
			var grantTools []string
			if rawList, ok := args["grant_tools"].([]any); ok {
				for _, item := range rawList {
					s, ok := item.(string)
					if !ok {
						continue
					}
					grantTools = append(grantTools, s)
				}
			}
			var parentTasks []TaskTemplate
			if rawList, ok := args["task_list"].([]any); ok {
				for _, item := range rawList {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					tt := TaskTemplate{}
					if v, ok := m["title"].(string); ok {
						tt.Title = v
					}
					if v, ok := m["prompt"].(string); ok {
						tt.Prompt = v
					}
					if v, ok := m["reasoning_effort"].(string); ok {
						tt.ReasoningEffort = v
					}
					parentTasks = append(parentTasks, tt)
				}
			}
			result, err := s.spawnAgent(ctx, task, model, "", maxTurns, agentType, reasoningEffort, parentTasks, grantTools)
			if err != nil || !blocking {
				return result, err
			}
			// Blocking mode: extract agent_id and wait for completion.
			var spawnResult map[string]any
			if err := json.Unmarshal([]byte(result.(string)), &spawnResult); err != nil {
				return result, nil
			}
			agentID, _ := spawnResult["agent_id"].(string)
			if agentID == "" {
				return result, nil
			}
			waitResult, waitErr := s.waitAgent(ctx, agentID, 0) // 0 = wait indefinitely
			// Include agent_id in the blocking result so the caller can
			// use resume_agent later if needed (e.g. to iterate with a planner).
			if waitStr, ok := waitResult.(string); ok {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(waitStr), &parsed); err == nil {
					parsed["agent_id"] = agentID
					b, _ := json.Marshal(parsed)
					return string(b), waitErr
				}
			}
			return waitResult, waitErr
		},
	})
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defSendInput()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			agentID := fmt.Sprint(args["agent_id"])
			// Append task_list items before sending input.
			if rawList, ok := args["task_list"].([]any); ok && len(rawList) > 0 {
				var items []TaskInput
				for _, item := range rawList {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					ti := TaskInput{
						Description: fmt.Sprint(m["title"]),
						Prompt:      fmt.Sprint(m["prompt"]),
					}
					if v, ok := m["reasoning_effort"].(string); ok {
						ti.ReasoningEffort = v
					}
					items = append(items, ti)
				}
				if sub := s.subagents.get(agentID); sub != nil {
					store := sub.sess.getOrCreateTaskStore()
					store.Append(items)
				}
			}
			result, err := s.sendInput(ctx, agentID, fmt.Sprint(args["message"]))
			if err != nil {
				return result, err
			}
			blocking, _ := args["blocking"].(bool)
			if !blocking {
				return result, nil
			}
			// Blocking mode: wait for the agent to finish and return its result.
			waitResult, waitErr := s.waitAgent(ctx, agentID, 0)
			if waitStr, ok := waitResult.(string); ok {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(waitStr), &parsed); err == nil {
					parsed["agent_id"] = agentID
					b, _ := json.Marshal(parsed)
					return string(b), waitErr
				}
			}
			return waitResult, waitErr
		},
	})
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defWait()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			timeout := 0
			if v, ok := args["timeout_ms"].(float64); ok && int(v) > 0 {
				timeout = int(v)
			}
			// Clamp to minimum to prevent rapid-retry burn.
			if timeout > 0 && timeout < minWaitTimeoutMS {
				timeout = minWaitTimeoutMS
			}
			return s.waitAgent(ctx, fmt.Sprint(args["agent_id"]), timeout)
		},
	})
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defCloseAgent()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return s.closeAgent(fmt.Sprint(args["agent_id"]))
		},
	})
}

func registerTaskTools(reg *ToolRegistry, deps *toolDeps) {
	// Task management.
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defTaskList(deps.reasoningEffortLevels)},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			deps.taskGuard.MarkUsed()
			store := deps.taskGuard.Store()
			action := fmt.Sprint(args["action"])
			switch action {
			case "view":
				return store.View(), nil
			case "append":
				raw, ok := args["tasks"].([]any)
				if !ok || len(raw) == 0 {
					return nil, fmt.Errorf("append requires a non-empty 'tasks' array")
				}
				var items []TaskInput
				for _, r := range raw {
					m, ok := r.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("each task must be an object with description and prompt")
					}
					var depIDs []int
					if depsRaw, ok := m["depends_on"].([]any); ok {
						for _, d := range depsRaw {
							if v, ok := d.(float64); ok {
								depIDs = append(depIDs, int(v))
							}
						}
					}
					var taskType TaskType
					if t, ok := m["type"].(string); ok {
						taskType = TaskType(t)
					}
					reasoningEffort := ""
					if re, ok := m["reasoning_effort"].(string); ok {
						reasoningEffort = re
					}
					items = append(items, TaskInput{
						Type:            taskType,
						Description:     fmt.Sprint(m["description"]),
						Prompt:          fmt.Sprint(m["prompt"]),
						DependsOn:       depIDs,
						ReasoningEffort: reasoningEffort,
					})
				}
				added, err := store.Append(items)
				if err != nil {
					return nil, err
				}

				// The tool response is a terse acknowledgement. The current
				// task is announced via a separate SYSTEM-REMINDER steering
				// message when the agent actually transitions one to
				// in_progress, either manually or via auto-advance.
				total, done := store.Progress()
				return ToolStateResult{
					Output: fmt.Sprintf("Added %d task(s). Progress: %d/%d tasks complete.", len(added), done, total),
					State:  store.View(),
				}, nil
			case "update":
				raw, ok := args["updates"].([]any)
				if !ok || len(raw) == 0 {
					return nil, fmt.Errorf("update requires a non-empty 'updates' array")
				}
				var updates []TaskUpdate
				for _, r := range raw {
					m, ok := r.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("each update must be an object with id and status")
					}
					id := 0
					if v, ok := m["id"].(float64); ok {
						id = int(v)
					}
					u := TaskUpdate{
						ID:     id,
						Status: TaskStatus(fmt.Sprint(m["status"])),
					}
					if n, ok := m["notes"].(string); ok {
						u.Notes = n
					}
					if depsRaw, ok := m["depends_on"]; ok {
						var depIDs []int
						if arr, ok := depsRaw.([]any); ok {
							for _, d := range arr {
								if v, ok := d.(float64); ok {
									depIDs = append(depIDs, int(v))
								}
							}
						}
						u.DependsOn = &depIDs
					}
					if re, ok := m["reasoning_effort"].(string); ok {
						u.ReasoningEffort = re
					}
					updates = append(updates, u)
				}
				if err := store.Update(updates); err != nil {
					return nil, err
				}

				// Classify the batch so we know whether to auto-advance, fire
				// a manual-start steering, or emit the "all done" steering.
				var completedAny bool
				var manuallyStartedID int
				for _, u := range updates {
					if u.Status == TaskDone || u.Status == TaskCancelled {
						completedAny = true
					}
					if u.Status == TaskInProgress {
						manuallyStartedID = u.ID
					}
				}

				// If the agent explicitly started a task, fire its current-task
				// steering so the SYSTEM-REMINDER for the new task shows up on
				// the next turn.
				if manuallyStartedID != 0 {
					for _, t := range store.View() {
						if t.ID == manuallyStartedID {
							if t.ReasoningEffort != "" {
								deps.taskGuard.SetReasoningEffort(t.ReasoningEffort)
							}
							deps.steer(formatCurrentTaskSteering(t))
							break
						}
					}
				}

				if !completedAny && manuallyStartedID == 0 {
					return ToolStateResult{Output: "Updated.", State: store.View()}, nil
				}

				var msg strings.Builder
				msg.WriteString("Updated. ")

				if completedAny {
					// Auto-advance unless the agent already picked what to do next.
					if manuallyStartedID == 0 {
						eligible := store.NextEligible()
						if len(eligible) > 0 {
							next := eligible[0]
							if err := store.Update([]TaskUpdate{{ID: next.ID, Status: TaskInProgress}}); err == nil {
								if next.ReasoningEffort != "" {
									deps.taskGuard.SetReasoningEffort(next.ReasoningEffort)
								}
								deps.steer(formatCurrentTaskSteering(next))
							}
						} else {
							// No eligible task. If nothing remains open or in_progress,
							// signal the agent that the list is exhausted.
							allDone := true
							for _, t := range store.View() {
								if t.Status == TaskOpen || t.Status == TaskInProgress {
									allDone = false
									break
								}
							}
							if allDone && len(store.View()) > 0 {
								deps.steer(taskReminderAllDone())
								msg.WriteString("All tasks complete. ")
							}
						}
					}
				}

				total, done := store.Progress()
				msg.WriteString(fmt.Sprintf("Progress: %d/%d tasks complete.", done, total))
				return ToolStateResult{Output: msg.String(), State: store.View()}, nil
			default:
				return nil, fmt.Errorf("unknown task_list action %q: use view, append, or update", action)
			}
		},
	})
}

func registerWebTools(reg *ToolRegistry, deps *toolDeps) {
	// Web fetch.
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defWebFetch()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			rawURL := fmt.Sprint(args["url"])
			question := fmt.Sprint(args["question"])
			return deps.web.fetch(ctx, rawURL, question)
		},
	})

	// Web search (Gemini only — see tool_web_search.go for why).
	// OpenAI and Anthropic handle web search natively via req.WebSearch;
	// registering a function tool named "web_search" for those providers
	// causes a duplicate name collision with the adapter-injected server tool.
	if deps.webSearchEnabled {
		_ = reg.Register(RegisteredTool{
			Tool: llm.Tool{Definition: defWebSearch()},
			Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
				query := fmt.Sprint(args["query"])
				return deps.web.search(ctx, query)
			},
		})
	}
}

func registerCommunicateTool(reg *ToolRegistry, deps *toolDeps) {
	// communicate is the only user-facing message channel.
	// Use the profile's definition if available (it may have been modified by
	// WithAllowedDecisions to add extra fields to the output schema).
	// Fall back to the base definition otherwise.
	resultToolDef := defCommunicateNamed(deps.resultToolName())
	if existing := reg.Get(deps.resultToolName()); existing != nil {
		resultToolDef = existing.Definition
	}
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: resultToolDef},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			if err := deps.abort(ctx); err != nil {
				return nil, err
			}
			message := ""
			if v, ok := args["message"]; ok {
				message = strings.TrimSpace(fmt.Sprint(v))
			}
			awaitReply, ok := args["await_reply"].(bool)
			if !ok {
				return nil, fmt.Errorf("communicate requires await_reply")
			}

			originalOutput := normalizeNodeOutput(args["output"])
			if message == "" && strings.TrimSpace(originalOutput.Message) != "" {
				message = strings.TrimSpace(originalOutput.Message)
			}
			if message == "" {
				return nil, fmt.Errorf("communicate requires message or output.message")
			}
			explicitStructuredOutput := hasMeaningfulNodeOutput(originalOutput)
			effectiveOutput := originalOutput
			if strings.TrimSpace(effectiveOutput.Message) == "" {
				effectiveOutput.Message = message
			}
			resultText := message
			structuredText := canonicalNodeOutputText(effectiveOutput)
			if explicitStructuredOutput {
				resultText = structuredText
			}
			if err := deps.abort(ctx); err != nil {
				return nil, err
			}

			deps.emit(EventCommunicate, CommunicateData{
				AwaitReply: awaitReply,
				Message:    message,
			})

			// Drain steering queue into the inbox. The inbox is text-only
			// in the wire shape, so image-bearing entries are also appended
			// as TurnSteering to keep their ContentImage parts available to
			// the next model round.
			drained := deps.drainSteering()
			inbox := make([]string, 0, len(drained))
			var deferred []steeringMessage
			for _, msg := range drained {
				if strings.TrimSpace(msg.Text) != "" {
					inbox = append(inbox, msg.Text)
				}
				if len(msg.Images) > 0 {
					deferred = append(deferred, msg)
				}
			}
			deps.prependSteering(deferred)

			deps.setCommunicateResult(awaitReply, message, resultText, structuredText)

			resp := map[string]any{
				"accepted":    true,
				"await_reply": awaitReply,
				"inbox":       inbox,
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		},
	})
}

func registerSkillTool(reg *ToolRegistry, deps *toolDeps) {
	// use_skill (progressive disclosure of skill instructions).
	// Present for provider profiles that include the use_skill tool definition.
	if reg.Get("use_skill") != nil {
		_ = reg.Register(RegisteredTool{
			Tool: llm.Tool{Definition: defUseSkill()},
			Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
				_ = ctx
				_ = env
				skillName := fmt.Sprint(args["skill_name"])
				meta, ok := deps.skill(skillName)
				if !ok {
					return nil, fmt.Errorf("skill %q not found", skillName)
				}
				deps.emit(EventSkillActivated, SkillActivatedData{Name: skillName})
				body, err := LoadSkillBody(meta)
				if err != nil {
					return nil, fmt.Errorf("loading skill %q: %w", skillName, err)
				}
				return fmt.Sprintf("Skill: %s\nLocation: %s\n\n---\n\n%s", skillName, meta.Dir, body), nil
			},
		})
	}
}

type nodeOutput struct {
	Decision  string         `json:"decision,omitempty"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data"`
	Artifacts []string       `json:"artifacts"`
}

func normalizeNodeOutput(raw any) nodeOutput {
	out := nodeOutput{
		Message:   "",
		Data:      map[string]any{},
		Artifacts: []string{},
	}
	if raw == nil {
		return out
	}
	if typed, ok := raw.(nodeOutput); ok {
		if typed.Data == nil {
			typed.Data = map[string]any{}
		}
		if typed.Artifacts == nil {
			typed.Artifacts = []string{}
		}
		return typed
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return out
	}

	if d, ok := m["decision"].(string); ok {
		out.Decision = d
	}
	if msg, ok := m["message"].(string); ok {
		out.Message = msg
	} else if v, ok := m["message"]; ok && v != nil {
		out.Message = fmt.Sprint(v)
	}
	if data, ok := m["data"].(map[string]any); ok {
		out.Data = data
	}
	if arts, ok := m["artifacts"]; ok {
		switch v := arts.(type) {
		case []string:
			out.Artifacts = append([]string{}, v...)
		case []any:
			out.Artifacts = make([]string, 0, len(v))
			for _, a := range v {
				out.Artifacts = append(out.Artifacts, fmt.Sprint(a))
			}
		}
	}
	return out
}

func hasMeaningfulNodeOutput(out nodeOutput) bool {
	return strings.TrimSpace(out.Decision) != "" ||
		strings.TrimSpace(out.Message) != "" ||
		len(out.Data) > 0 ||
		len(out.Artifacts) > 0
}

func canonicalNodeOutputText(raw any) string {
	out := normalizeNodeOutput(raw)
	b, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
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

func (s *Session) getOrCreateTaskStore() *TaskStore {
	s.taskStoreOnce.Do(func() {
		if s.cfg.SharedTaskStore != nil {
			s.taskStore = s.cfg.SharedTaskStore
			return
		}
		dir := s.stateDir
		if dir == "" {
			dir = s.env.WorkingDirectory()
		}
		s.taskStore = NewTaskStore(dir, s.id)
		_ = s.taskStore.Load()
	})
	return s.taskStore
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

// Tasks returns a snapshot of the session's task list.
func (s *Session) Tasks() []Task {
	return s.getOrCreateTaskStore().View()
}
