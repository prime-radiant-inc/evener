package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

func (s *Session) toolSearchRegisteredTool() tool.RegisteredTool {
	return tool.RegisteredTool{
		Definition: tool.DefToolSearch(),
		ReadOnly:   true,
		Exec:       s.execToolSearch,
	}
}

func (s *Session) registerToolSearch(register func(tool.RegisteredTool) error) error {
	if register == nil {
		return nil
	}
	return register(s.toolSearchRegisteredTool())
}

func (s *Session) syncToolSearchRegistrationLocked() bool {
	if s == nil || s.reg == nil {
		return false
	}
	hasDeferred := s.hasDeferredToolsLocked()
	if !hasDeferred {
		s.reg.Remove("tool_search")
		return false
	}
	if s.reg.Get("tool_search") == nil && !slices.Contains(s.cfg.spawn.deniedToolNames, "tool_search") {
		register := s.reg.Register
		if s.cfg.testOnly.registerTool != nil {
			register = func(rt tool.RegisteredTool) error {
				return s.cfg.testOnly.registerTool(s.reg, rt)
			}
		}
		_ = s.registerToolSearch(register)
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

// wireDeferredToolDef applies provider-visible naming and strips the optional
// shared intent property so returned deferred schemas match wire tool names cleanly.
func wireDeferredToolDef(td llm.ToolDefinition, nameMap map[string]string) llm.ToolDefinition {
	clean := tool.WithoutIntentParameter(td)
	clean.Name = providerToolName(clean.Name, nameMap)
	return clean
}

type liveToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

func encodeLiveToolSchemas(defs []llm.ToolDefinition) string {
	items := make([]liveToolSchema, 0, len(defs))
	for _, td := range defs {
		items = append(items, liveToolSchema{
			Name:        td.Name,
			Description: td.Description,
			Parameters:  td.Parameters,
		})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(items); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

func parseToolSearchNames(args map[string]any) []string {
	raw, ok := args["names"]
	if !ok || raw == nil {
		return nil
	}
	var out []string
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
	case []string:
		for _, s := range v {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

func (s *Session) execToolSearch(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	reqNames := parseToolSearchNames(args)
	query := strings.TrimSpace(stringArg(args, "query"))

	s.mu.Lock()
	defer s.mu.Unlock()

	nameMap := s.toolNameMap()
	deferredCanonicals := s.deferredToolNamesLocked()
	if len(deferredCanonicals) == 0 {
		return tool.StateResult{Output: "No deferred tools are registered in this session."}, nil
	}

	type deferredEntry struct {
		canonical string
		wire      string
		def       llm.ToolDefinition
	}
	entries := make([]deferredEntry, 0, len(deferredCanonicals))
	for _, canonical := range deferredCanonicals {
		def, ok := s.deferredToolDefinitionLocked(canonical)
		if !ok {
			continue
		}
		wireDef := wireDeferredToolDef(def, nameMap)
		entries = append(entries, deferredEntry{
			canonical: canonical,
			wire:      wireDef.Name,
			def:       wireDef,
		})
	}
	slices.SortFunc(entries, func(a, b deferredEntry) int {
		return strings.Compare(a.wire, b.wire)
	})

	// List all deferred tools with 1-line summaries when query is omitted or "all" and no names are given.
	if len(reqNames) == 0 && (query == "" || strings.EqualFold(query, "all")) {
		var b strings.Builder
		b.WriteString("Deferred tools (call directly by name, or pass names=[...] to tool_search for full schemas):\n")
		for _, e := range entries {
			firstLine, _, _ := strings.Cut(strings.TrimSpace(e.def.Description), "\n")
			fmt.Fprintf(&b, "- %s: %s\n", e.wire, firstLine)
		}
		return tool.StateResult{Output: strings.TrimSpace(b.String())}, nil
	}

	var matched []llm.ToolDefinition
	var matchedCanonicals []string
	seenMatched := make(map[string]bool)
	addMatch := func(canonical string, def llm.ToolDefinition) {
		if seenMatched[canonical] {
			return
		}
		seenMatched[canonical] = true
		matched = append(matched, def)
		matchedCanonicals = append(matchedCanonicals, canonical)
	}

	var alreadyTier1 []string
	var unknown []string
	for _, raw := range reqNames {
		canonical := canonicalToolName(raw, nameMap)
		if def, ok := s.deferredToolDefinitionLocked(canonical); ok {
			addMatch(canonical, wireDeferredToolDef(def, nameMap))
			continue
		}
		if s.reg != nil && s.reg.Get(canonical) != nil {
			visible := providerToolName(canonical, nameMap)
			if !slices.Contains(alreadyTier1, visible) {
				alreadyTier1 = append(alreadyTier1, visible)
			}
			continue
		}
		if !slices.Contains(unknown, raw) {
			unknown = append(unknown, raw)
		}
	}

	if query != "" && !strings.EqualFold(query, "all") {
		qLower := strings.ToLower(query)
		tokens := strings.Fields(qLower)
		matchAll := func(haystack string) bool {
			if strings.Contains(haystack, qLower) {
				return true
			}
			if len(tokens) <= 1 {
				return false
			}
			for _, tok := range tokens {
				if !strings.Contains(haystack, tok) {
					return false
				}
			}
			return true
		}
		queryMatched := 0
		for _, e := range entries {
			haystack := strings.ToLower(e.wire + " " + e.canonical + " " + e.def.Description)
			if matchAll(haystack) {
				addMatch(e.canonical, e.def)
				queryMatched++
			}
		}
		if queryMatched == 0 && len(tokens) > 1 {
			for _, e := range entries {
				haystack := strings.ToLower(e.wire + " " + e.canonical + " " + e.def.Description)
				for _, tok := range tokens {
					if strings.Contains(haystack, tok) {
						addMatch(e.canonical, e.def)
						break
					}
				}
			}
		}
	}

	visibleNames := make([]string, len(entries))
	for i, e := range entries {
		visibleNames[i] = e.wire
	}

	if len(matched) == 0 {
		if len(unknown) > 0 {
			return tool.StateResult{
				Output:  fmt.Sprintf("tool_search: unknown tool(s) %s (available deferred tools: %s)", strings.Join(unknown, ", "), strings.Join(visibleNames, ", ")),
				IsError: true,
			}, nil
		}
		if len(alreadyTier1) > 0 {
			return tool.StateResult{
				Output: fmt.Sprintf("Already loaded upfront in full (call directly): %s", strings.Join(alreadyTier1, ", ")),
			}, nil
		}
		return tool.StateResult{
			Output: fmt.Sprintf("No deferred tools matched %q. Available deferred tools: %s", query, strings.Join(visibleNames, ", ")),
		}, nil
	}

	if s.disclosedToolSchemas == nil {
		s.disclosedToolSchemas = make(map[string]bool)
	}
	for _, canonical := range matchedCanonicals {
		s.disclosedToolSchemas[canonical] = true
	}

	var out strings.Builder
	out.WriteString(encodeLiveToolSchemas(matched))
	if len(alreadyTier1) > 0 {
		fmt.Fprintf(&out, "\nAlready loaded upfront in full (call directly): %s", strings.Join(alreadyTier1, ", "))
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&out, "\nUnknown tool(s): %s (available deferred tools: %s)", strings.Join(unknown, ", "), strings.Join(visibleNames, ", "))
	}
	return tool.StateResult{Output: out.String()}, nil
}

func (s *Session) deferredToolDefsForDisclosureWithCanonical(names []string, claim bool) ([]llm.ToolDefinition, []string) {
	if s == nil || s.reg == nil || len(names) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if claim && s.disclosedToolSchemas == nil {
		s.disclosedToolSchemas = make(map[string]bool)
	}
	nameMap := s.toolNameMap()
	var defs []llm.ToolDefinition
	var canonicals []string
	for _, name := range names {
		if s.disclosedToolSchemas[name] {
			continue
		}
		if def, ok := s.deferredToolDefinitionLocked(name); ok {
			if claim {
				s.disclosedToolSchemas[name] = true
			}
			defs = append(defs, wireDeferredToolDef(def, nameMap))
			canonicals = append(canonicals, name)
		}
	}
	return defs, canonicals
}

func (s *Session) deferredToolDefsForDisclosure(names []string, claim bool) []llm.ToolDefinition {
	defs, _ := s.deferredToolDefsForDisclosureWithCanonical(names, claim)
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
	body := "Deferred tool schemas (call these tools directly by name):\n" + strings.TrimSpace(buf.String())
	if leadNote != "" {
		body = leadNote + "\n" + body
	}
	return systemNotification(body)
}

func (s *Session) discloseDeferredTools(leadNote string, names []string) (string, []string) {
	defs, claimed := s.deferredToolDefsForDisclosureWithCanonical(names, true)
	if len(defs) == 0 {
		return "", nil
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
		if note, names := s.discloseDeferredTools("Background job started. Manage or inspect jobs directly:", jobNames); len(names) > 0 {
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
