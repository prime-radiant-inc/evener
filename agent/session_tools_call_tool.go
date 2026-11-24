package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

type callToolCarrierKey struct{}

type callToolResultCarrier struct {
	res *tool.ExecResult
}

func itemIDFromContext(ctx context.Context) string {
	itemID, _ := ctx.Value(ctxToolItemID).(string)
	return itemID
}

func (s *Session) callToolRegisteredTool() tool.RegisteredTool {
	return tool.RegisteredTool{
		Definition: tool.DefCallTool(),
		// Inner tools apply their own output limits at ExecutePreparedCall.
		Limit: schema.ToolOutputLimit{},
		Exec:  s.execCallTool,
	}
}

func (s *Session) registerCallTool(register func(tool.RegisteredTool) error) error {
	if register == nil {
		return nil
	}
	return register(s.callToolRegisteredTool())
}

func (s *Session) syncCallToolRegistrationLocked() bool {
	if s == nil || s.reg == nil {
		return false
	}
	hasDeferred := s.hasDeferredToolsLocked()
	if !hasDeferred {
		s.reg.Remove("call_tool")
		return false
	}
	if s.reg.Get("call_tool") == nil && !slices.Contains(s.cfg.spawn.deniedToolNames, "call_tool") {
		register := s.reg.Register
		if s.cfg.testOnly.registerTool != nil {
			register = func(rt tool.RegisteredTool) error {
				return s.cfg.testOnly.registerTool(s.reg, rt)
			}
		}
		_ = s.registerCallTool(register)
	}
	return true
}

func (s *Session) isDeferredTool(canonicalName string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isDeferredToolLocked(canonicalName)
}

func (s *Session) deferredToolNamesLocked() []string {
	if s == nil || s.reg == nil {
		return nil
	}
	var names []string
	for _, name := range s.reg.Names() {
		if s.isDeferredToolLocked(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func (s *Session) deferredToolNames() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deferredToolNamesLocked()
}

func (s *Session) callToolDefinitionLocked() llm.ToolDefinition {
	def := tool.DefCallTool()
	if names := s.deferredToolNamesLocked(); len(names) > 0 {
		def.Description += " Available here: " + strings.Join(names, ", ") + "."
	}
	return def
}

func isCallToolListQuery(rawName string) bool {
	switch strings.ToLower(strings.TrimSpace(rawName)) {
	case "list", "discover", "help", "?":
		return true
	default:
		return false
	}
}

func (s *Session) deferredToolsSummaryResult() tool.StateResult {
	s.mu.Lock()
	names := s.deferredToolNamesLocked()
	var b strings.Builder
	b.WriteString("Available deferred tools (invoke via call_tool(name=..., arguments=...)):\n")
	for _, name := range names {
		def, ok := s.deferredToolDefinitionLocked(name)
		if !ok {
			continue
		}
		desc, _, _ := strings.Cut(strings.TrimSpace(def.Description), "\n")
		fmt.Fprintf(&b, "- %s: %s\n", name, desc)
	}
	s.mu.Unlock()
	return tool.StateResult{Output: strings.TrimSpace(b.String())}
}

func (s *Session) execCallTool(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	rawName := strings.TrimSpace(stringArg(args, "name"))
	if rawName == "" {
		return nil, fmt.Errorf("call_tool: name is required")
	}
	canonical := s.canonicalToolName(rawName)
	rt := s.reg.Get(canonical)
	if rt == nil {
		if isCallToolListQuery(rawName) {
			return s.deferredToolsSummaryResult(), nil
		}
		if names := s.deferredToolNames(); len(names) > 0 {
			return nil, fmt.Errorf("call_tool: unknown deferred tool %q (available: %s)", rawName, strings.Join(names, ", "))
		}
		return nil, fmt.Errorf("call_tool: unknown deferred tool %q", rawName)
	}
	if !s.isDeferredTool(canonical) {
		visible := s.providerVisibleToolName(canonical)
		return nil, fmt.Errorf("call_tool: %q is directly callable (not deferred); call %s directly", rawName, visible)
	}

	var innerArgs map[string]any
	if rawArgs, ok := args["arguments"]; !ok || rawArgs == nil {
		innerArgs = map[string]any{}
	} else if m, ok := rawArgs.(map[string]any); ok {
		innerArgs = maps.Clone(m)
	} else {
		msg := fmt.Sprintf("call_tool: arguments for %q must be a JSON object", canonical)
		if notice := s.discloseDeferredToolsNotification("", []string{canonical}); notice != "" {
			msg += "\n" + notice
		}
		return tool.StateResult{Output: msg, IsError: true}, nil
	}
	if _, hasIntent := innerArgs["intent"]; !hasIntent {
		if outerIntent := strings.TrimSpace(tool.IntentFromContext(ctx)); outerIntent != "" {
			innerArgs["intent"] = outerIntent
		}
	}
	innerArgsJSON, err := json.Marshal(innerArgs)
	if err != nil {
		return nil, fmt.Errorf("call_tool: marshal arguments for %q: %w", canonical, err)
	}

	innerCall := llm.ToolCallData{
		ID:        callIDFromContext(ctx),
		ItemID:    itemIDFromContext(ctx),
		Name:      canonical,
		Arguments: innerArgsJSON,
		Type:      "function",
	}
	prep := prepareToolCall(innerCall, s.shellToolForDiagnostics(rt), []string{canonical}, canonical, s.resultToolName(), "")
	innerCall = prep.Call
	if len(prep.Changes) > 0 {
		repaired := events.ToolCallRepairedData{
			ToolName: innerCall.Name,
			CallID:   innerCall.ID,
			Changes:  changeStrings(prep.Changes),
		}
		s.recordNotice(schema.NoticeInfo{Kind: schema.NoticeToolRepair, ToolRepair: &schema.ToolRepairNotice{ToolName: repaired.ToolName, CallID: repaired.CallID, Changes: repaired.Changes}})
		s.emit(repaired)
	}

	carrier, _ := ctx.Value(callToolCarrierKey{}).(*callToolResultCarrier)
	setResult := func(r tool.ExecResult) tool.StateResult {
		if carrier != nil {
			copied := r
			carrier.res = &copied
		}
		var state any
		if len(r.ToolState) > 0 {
			state = json.RawMessage(r.ToolState)
		}
		return tool.StateResult{Output: r.Output, IsError: r.IsError, State: state}
	}

	if prep.PrevalErr != "" {
		msg := prep.PrevalErr
		if notice := s.discloseDeferredToolsNotification("", []string{canonical}); notice != "" {
			msg += "\n" + notice
		}
		return setResult(tool.ExecResult{
			ToolName:   canonical,
			CallID:     innerCall.ID,
			Output:     msg,
			FullOutput: msg,
			IsError:    true,
			PrevalOnly: true,
			Err:        prep.Err,
		}), nil
	}

	prevalidated := true
	if early, changed := s.runPreToolHooks(ctx, &innerCall, prep); early != nil {
		return setResult(*early), nil
	} else if changed {
		prevalidated = false
	}

	var res tool.ExecResult
	if prevalidated {
		res = s.reg.ExecutePreparedCall(ctx, env, innerCall)
	} else {
		res = s.reg.ExecuteCall(ctx, env, innerCall)
	}
	if !res.Rejected {
		s.stopHookActive.Store(false)
	}
	res = s.escalateOnSandboxDenial(ctx, innerCall.Name, res, toolCallRerunner{session: s, call: innerCall}.run)
	droppedUnknown := slices.ContainsFunc(prep.Changes, func(c repair.Change) bool { return c.Kind == repair.ChangeDropUnknown })
	if res.Rejected || droppedUnknown {
		if notice := s.discloseDeferredToolsNotification("", []string{canonical}); notice != "" {
			res.Output += "\n" + notice
			res.FullOutput += "\n" + notice
		}
	}
	if early := s.runPostToolHooks(ctx, innerCall, res); early != nil {
		res = *early
	}
	return setResult(res), nil
}

func callToolTargetName(argsJSON json.RawMessage) string {
	var payload struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(argsJSON, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Name)
}

// deferredToolDefinitionLocked returns the live schema for a registered deferred
// tool. Caller holds s.mu.
func (s *Session) deferredToolDefinitionLocked(name string) (llm.ToolDefinition, bool) {
	if s == nil || s.reg == nil {
		return llm.ToolDefinition{}, false
	}
	rt := s.reg.Get(name)
	if rt == nil || !s.isDeferredToolLocked(name) {
		return llm.ToolDefinition{}, false
	}
	if name == "delegate" && !s.hubEnabled {
		td := s.delegateToolDefinition()
		if !canGrantDelegation(s.delegationAllowance) {
			td = withoutDelegateAllowance(td)
		}
		return td, true
	}
	if name == "memory_read" {
		return tool.MemoryDefinition(s.sessionReadFileDefinition(), "memory_read"), true
	}
	return rt.Definition, true
}

func (s *Session) deferredToolDefsForDisclosure(names []string, claim bool) []llm.ToolDefinition {
	if s == nil || s.reg == nil || len(names) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if claim && s.disclosedToolSchemas == nil {
		s.disclosedToolSchemas = make(map[string]bool)
	}
	var defs []llm.ToolDefinition
	for _, name := range names {
		if s.disclosedToolSchemas[name] {
			continue
		}
		if def, ok := s.deferredToolDefinitionLocked(name); ok {
			if claim {
				s.disclosedToolSchemas[name] = true
			}
			defs = append(defs, def)
		}
	}
	return defs
}

func (s *Session) markToolSchemasDisclosed(names []string) {
	if s == nil || len(names) == 0 {
		return
	}
	s.mu.Lock()
	if s.disclosedToolSchemas == nil {
		s.disclosedToolSchemas = make(map[string]bool)
	}
	for _, name := range names {
		s.disclosedToolSchemas[name] = true
	}
	s.mu.Unlock()
}

func formatDeferredToolSchemas(defs []llm.ToolDefinition) string {
	return formatDeferredToolSchemasWithLead("", defs)
}

func formatDeferredToolSchemasWithLead(leadNote string, defs []llm.ToolDefinition) string {
	if len(defs) == 0 {
		return ""
	}
	type compactSchema struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Required    any            `json:"required,omitempty"`
		Properties  map[string]any `json:"properties,omitempty"`
	}
	items := make([]compactSchema, 0, len(defs))
	for _, td := range defs {
		clean := tool.WithoutIntentParameter(td)
		var req any
		var props map[string]any
		if clean.Parameters != nil {
			req = clean.Parameters["required"]
			if p, ok := clean.Parameters["properties"].(map[string]any); ok && len(p) > 0 {
				props = p
			}
		}
		items = append(items, compactSchema{
			Name:        clean.Name,
			Description: clean.Description,
			Required:    req,
			Properties:  props,
		})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(items); err != nil {
		return ""
	}
	body := "Deferred tool schemas (invoke via `call_tool(name=..., arguments=...)`):\n" + strings.TrimSpace(buf.String())
	if leadNote != "" {
		body = leadNote + "\n" + body
	}
	return systemNotification(body)
}

func (s *Session) discloseDeferredTools(leadNote string, names []string) (string, []string) {
	defs := s.deferredToolDefsForDisclosure(names, true)
	if len(defs) == 0 {
		return "", nil
	}
	claimed := make([]string, 0, len(defs))
	for _, d := range defs {
		claimed = append(claimed, d.Name)
	}
	return formatDeferredToolSchemasWithLead(leadNote, defs), claimed
}

func (s *Session) discloseDeferredToolsNotification(leadNote string, names []string) string {
	note, _ := s.discloseDeferredTools(leadNote, names)
	return note
}

func formatAvailableAgentsBlock(entries []agentEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<available_agents>\nAvailable `delegate` roles (`agent_type`); authoritative per-role tool lists live in the `delegate` schema.\n")
	for _, a := range entries {
		fmt.Fprintf(&b, "<agent>\nName: `%s`\nDescription: %s\nDefault task list:\n", a.Name, a.Description)
		if len(a.TaskList) == 0 {
			b.WriteString("- none\n")
		} else {
			for _, task := range a.TaskList {
				fmt.Fprintf(&b, "- `%s`: %s", task.Title, task.Description)
				if task.ReplacedByParentTasks {
					b.WriteString(" Include relevant parent task details in the `delegate` task prompt for this step.")
				}
				b.WriteByte('\n')
			}
		}
		b.WriteString("</agent>\n")
	}
	b.WriteString("</available_agents>")
	return b.String()
}

var skillDeferredToolGroups = map[string][]string{
	skill.RunningJobsSkillName:        {"job_watch", "job_status", "job_stop", "job_list", "delegate", "delegate_send"},
	skill.UsingWorktreesSkillName:     {"manage_worktree"},
	skill.ReadingTranscriptsSkillName: {"read_transcript", "find_session_transcripts"},
	"doctoring-evener":                {"doctor_evener"},
	skill.GardeningMemorySkillName:    {"memory_read", "memory_write", "memory_edit", "memory_delete", "memory_search"},
}

func (s *Session) skillDisclosureSuffix(skillName string) string {
	if s == nil {
		return ""
	}
	var parts []string
	if names := skillDeferredToolGroups[skillName]; len(names) > 0 {
		if note := s.discloseDeferredToolsNotification("", names); note != "" {
			parts = append(parts, note)
		}
	}
	if skillName == skill.RunningJobsSkillName && s.reg != nil && s.reg.Get("delegate") != nil && s.isDeferredTool("delegate") && !s.isSubagentSession() {
		s.mu.Lock()
		already := s.disclosedAvailableAgents
		var entries []agentEntry
		if !already {
			entries = s.availableAgentEntries()
			if len(entries) > 0 {
				s.disclosedAvailableAgents = true
			}
		}
		s.mu.Unlock()
		if block := formatAvailableAgentsBlock(entries); block != "" {
			parts = append(parts, block)
		}
	}
	return strings.Join(parts, "\n")
}

func (s *Session) steerReactiveToolDisclosure(toolName string, res tool.ExecResult, outputRef string) {
	if s == nil || s.reg == nil {
		return
	}
	needJobControl := false
	needReadTranscript := outputRef != ""
	if toolName == "shell" && len(res.ToolState) > 0 {
		var st struct {
			JobID    string `json:"job_id"`
			Mode     string `json:"mode"`
			TimedOut bool   `json:"timed_out"`
		}
		if json.Unmarshal(res.ToolState, &st) == nil && st.JobID != "" {
			if st.Mode == string(shellModeBackground) {
				needJobControl = true
			}
			if st.TimedOut || st.Mode != string(shellModeBackground) {
				needReadTranscript = true
			}
		}
	}
	var claimed []string
	var blocks []string
	if needJobControl {
		jobNames := []string{"job_watch", "job_status", "job_stop", "job_list"}
		if note, names := s.discloseDeferredTools("Background job started. Manage or inspect jobs via `call_tool`:", jobNames); len(names) > 0 {
			claimed = append(claimed, names...)
			if note != "" {
				blocks = append(blocks, note)
			}
		}
	}
	if needReadTranscript {
		if note, names := s.discloseDeferredTools("", []string{"read_transcript"}); len(names) > 0 {
			claimed = append(claimed, names...)
			if note != "" {
				blocks = append(blocks, note)
			}
		}
	}
	if len(blocks) == 0 {
		return
	}
	kind := events.SteeringKindTranscriptPointer
	if needJobControl {
		kind = events.SteeringKindNotification
	}
	if err := s.steerKind(strings.Join(blocks, "\n"), kind); err != nil {
		s.mu.Lock()
		for _, name := range claimed {
			delete(s.disclosedToolSchemas, name)
		}
		s.mu.Unlock()
		s.emitDiagnosticWarning(events.WarningData{Message: fmt.Sprintf("deferred tool schema disclosure failed: %v", err)})
	}
}
