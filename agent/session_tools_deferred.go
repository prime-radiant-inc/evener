package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

func (s *Session) resultToolOwnsToolSearch() bool {
	return s != nil && s.resultToolName() == "tool_search"
}

func (s *Session) ensureToolSearchRegisteredLocked() bool {
	if s == nil || s.reg == nil || s.resultToolOwnsToolSearch() {
		return false
	}
	if slices.Contains(s.cfg.spawn.deniedToolNames, "tool_search") {
		s.reg.Remove("tool_search")
		return false
	}
	if s.reg.Get("tool_search") != nil {
		return true
	}
	register := s.reg.Register
	if s.cfg.testOnly.registerTool != nil {
		register = func(rt tool.RegisteredTool) error {
			return s.cfg.testOnly.registerTool(s.reg, rt)
		}
	}
	if err := register(tool.RegisteredTool{
		Definition: tool.DefToolSearch(),
		ReadOnly:   true,
		Exec:       s.execToolSearch,
	}); err != nil {
		return false
	}
	if lim, ok := s.cfg.ToolOutputLimits["tool_search"]; ok {
		s.reg.OverrideLimits(map[string]schema.ToolOutputLimit{"tool_search": lim})
	}
	return s.reg.Get("tool_search") != nil
}

// mergeTier1Tools returns saved plus any additional non-empty tool names from
// extra, deduplicated while preserving order.
func mergeTier1Tools(saved, extra []string) []string {
	merged := make([]string, 0, len(saved)+len(extra))
	seen := make(map[string]bool, len(saved)+len(extra))
	for _, list := range [][]string{saved, extra} {
		for _, raw := range list {
			name := strings.TrimSpace(raw)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			merged = append(merged, name)
		}
	}
	return merged
}

func (s *Session) isDeferredTool(canonicalName string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isDeferredToolLocked(canonicalName)
}

// deferredToolDefinitionLocked returns the live provider-facing schema for an
// advertised deferred tool stub. Caller holds s.mu.
func (s *Session) deferredToolDefinitionLocked(name string) (llm.ToolDefinition, bool) {
	if s == nil {
		return llm.ToolDefinition{}, false
	}
	def, ok := s.cachedDeferredFullDefs[name]
	return def, ok
}

// advertisedUpfrontToolNameLocked reports the provider-visible wire name of an
// advertised non-deferred tool in cachedToolDefs. Caller holds s.mu.
func (s *Session) advertisedUpfrontToolNameLocked(canonical string, nameMap map[string]string) (string, bool) {
	if _, deferred := s.cachedDeferredFullDefs[canonical]; deferred {
		return "", false
	}
	for _, td := range s.cachedToolDefs {
		if canonicalToolName(td.Name, nameMap) == canonical {
			return td.Name, true
		}
	}
	return "", false
}

// wireDeferredToolDef renders a deferred tool's full schema in its
// provider-visible wire form with the required intent parameter, matching
// Tier-1 work tools.
func wireDeferredToolDef(td llm.ToolDefinition, nameMap map[string]string) llm.ToolDefinition {
	wired := tool.WithIntentParameterRequired(td)
	wired.Name = providerToolName(wired.Name, nameMap)
	return wired
}

type liveToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

func encodeLiveToolSchema(td llm.ToolDefinition) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(liveToolSchema{Name: td.Name, Description: td.Description, Parameters: td.Parameters}); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

func (s *Session) toolSearchMaxCharsLocked() int {
	if rt := s.reg.Get("tool_search"); rt != nil {
		return rt.Limit.MaxChars
	}
	return s.cfg.ToolOutputLimits["tool_search"].MaxChars
}

func fitToolSearchOutput(matched []llm.ToolDefinition, matchedCanonicals []string, staticSuffix string, maxChars int) (string, []string) {
	items := make([]string, len(matched))
	for i, td := range matched {
		items[i] = encodeLiveToolSchema(td)
	}
	if full := "[" + strings.Join(items, ",") + "]" + staticSuffix; maxChars <= 0 || utf8.RuneCountInString(full) <= maxChars {
		return full, matchedCanonicals
	}
	const omittedPrefix = "\nOmitted due to output limit (look up in a smaller follow-up call): "
	var chosenJSON, includedCanonicals, omittedWire []string
	for i, td := range matched {
		candOmitted := slices.Clone(omittedWire)
		for _, rem := range matched[i+1:] {
			candOmitted = append(candOmitted, rem.Name)
		}
		candOut := "[" + strings.Join(append(slices.Clone(chosenJSON), items[i]), ",") + "]" + staticSuffix
		if len(candOmitted) > 0 {
			candOut += omittedPrefix + strings.Join(candOmitted, ", ")
		}
		if utf8.RuneCountInString(candOut) <= maxChars {
			chosenJSON = append(chosenJSON, items[i])
			includedCanonicals = append(includedCanonicals, matchedCanonicals[i])
		} else {
			omittedWire = append(omittedWire, td.Name)
		}
	}
	if len(chosenJSON) == 0 {
		msg := fmt.Sprintf("Omitted due to output limit (%d chars): %s", maxChars, strings.Join(omittedWire, ", "))
		if staticSuffix != "" && utf8.RuneCountInString(msg+staticSuffix) <= maxChars {
			msg += staticSuffix
		}
		return msg, nil
	}
	return "[" + strings.Join(chosenJSON, ",") + "]" + staticSuffix + omittedPrefix + strings.Join(omittedWire, ", "), includedCanonicals
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
	if len(s.cachedDeferredFullDefs) == 0 {
		return tool.StateResult{Output: "No deferred tools are registered in this session."}, nil
	}

	type deferredEntry struct {
		canonical string
		wire      string
		def       llm.ToolDefinition
	}
	entries := make([]deferredEntry, 0, len(s.cachedDeferredFullDefs))
	for canonical, def := range s.cachedDeferredFullDefs {
		entries = append(entries, deferredEntry{
			canonical: canonical,
			wire:      def.Name,
			def:       def,
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
			addMatch(canonical, def)
			continue
		}
		if visible, ok := s.advertisedUpfrontToolNameLocked(canonical, nameMap); ok {
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
				Output: "Already loaded upfront in full (call directly): " + strings.Join(alreadyTier1, ", "),
			}, nil
		}
		return tool.StateResult{
			Output: fmt.Sprintf("No deferred tools matched %q. Available deferred tools: %s", query, strings.Join(visibleNames, ", ")),
		}, nil
	}

	var suffix strings.Builder
	if len(alreadyTier1) > 0 {
		fmt.Fprintf(&suffix, "\nAlready loaded upfront in full (call directly): %s", strings.Join(alreadyTier1, ", "))
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&suffix, "\nUnknown tool(s): %s (available deferred tools: %s)", strings.Join(unknown, ", "), strings.Join(visibleNames, ", "))
	}
	output, includedCanonicals := fitToolSearchOutput(matched, matchedCanonicals, suffix.String(), s.toolSearchMaxCharsLocked())
	if len(includedCanonicals) > 0 {
		if s.disclosedToolSchemas == nil {
			s.disclosedToolSchemas = make(map[string]bool)
		}
		for _, canonical := range includedCanonicals {
			s.disclosedToolSchemas[canonical] = true
		}
	}
	return tool.StateResult{Output: output}, nil
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
			defs = append(defs, def)
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
		wired := tool.WithIntentParameterRequired(td)
		var req any
		var props map[string]any
		if wired.Parameters != nil {
			req = wired.Parameters["required"]
			if p, ok := wired.Parameters["properties"].(map[string]any); ok && len(p) > 0 {
				props = p
			}
		}
		items = append(items, compactSchema{
			Name:        wired.Name,
			Description: wired.Description,
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

var skillDeferredToolGroups = map[string][]string{
	skill.RunningJobsSkillName:        {"job_watch", "job_status", "job_stop", "job_list"},
	skill.UsingWorktreesSkillName:     {"manage_worktree"},
	skill.ReadingTranscriptsSkillName: {"read_transcript", "find_session_transcripts"},
	"doctoring-evener":                {"doctor_evener"},
	skill.GardeningMemorySkillName:    {"memory_read", "memory_write", "memory_edit", "memory_delete", "memory_search"},
}

func (s *Session) skillDisclosure(skillName string) (string, []string) {
	if s == nil {
		return "", nil
	}
	names := skillDeferredToolGroups[skillName]
	if len(names) == 0 {
		return "", nil
	}
	return s.discloseDeferredTools("", names)
}

func (s *Session) steerReactiveToolDisclosure(toolName string, res tool.ExecResult, outputRef string) {
	if s == nil || s.reg == nil {
		return
	}
	var claimed []string
	var blocks []string
	needJobControl := false
	needReadTranscript := outputRef != ""
	jobLeadNote := "Background job started. Manage or inspect jobs directly:"
	switch toolName {
	case "use_skill":
		if !res.IsError && !res.Rejected && !res.PrevalOnly && len(res.ToolState) > 0 {
			var st skillToolState
			if json.Unmarshal(res.ToolState, &st) == nil && st.Outcome.Identity.Name != "" {
				if note, skillClaimed := s.skillDisclosure(st.Outcome.Identity.Name); note != "" {
					blocks = append(blocks, note)
					claimed = append(claimed, skillClaimed...)
				}
			}
		}
	case "shell":
		if len(res.ToolState) > 0 {
			var st struct {
				JobID    string `json:"job_id"`
				Mode     string `json:"mode"`
				TimedOut bool   `json:"timed_out"`
			}
			if json.Unmarshal(res.ToolState, &st) == nil && st.JobID != "" {
				if st.Mode == string(shellModeBackground) || st.TimedOut {
					needJobControl = true
				}
				if st.TimedOut || st.Mode != string(shellModeBackground) {
					needReadTranscript = true
				}
			}
		}
	case "delegate":
		if !res.IsError && !res.Rejected && !res.PrevalOnly {
			needJobControl = true
			needReadTranscript = true
			jobLeadNote = "Delegate started. Manage or inspect delegates directly:"
		}
	}
	if needJobControl {
		jobNames := []string{"job_watch", "job_status", "job_stop", "job_list"}
		if note, names := s.discloseDeferredTools(jobLeadNote, jobNames); len(names) > 0 {
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
	if needJobControl || toolName == "use_skill" {
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
