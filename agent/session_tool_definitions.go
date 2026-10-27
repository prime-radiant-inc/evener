package agent

import (
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
)

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
	// No delegate can save memory (filterUnavailableMemoryTools).
	canonical = removeStrings(canonical, memorySaveToolNames)
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
	definition := tool.DefDelegateWithResultTool(s.delegateAgentTypeNames(), sandboxSchema, s.resultToolName())
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

// sessionShellDefinition is the shell schema for this session's live
// environment: mode "detached" is advertised only when the environment reports
// it can disown a process (issue #4422). Both the advertised cache and the
// prompt's profile-definition comparison use it, so they cannot disagree.
// Callers run at construction time or hold s.mu, as rebuildToolDefsCache and
// the prompt render do; it reads s.env directly and must not call currentEnv.
func (s *Session) sessionShellDefinition() llm.ToolDefinition {
	return tool.DefShellWithDetach(detachSupportedBy(s.env))
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
	// Work tools advertise intent as REQUIRED: the wire schema carries the
	// mandate. The registry's own validation schema keeps the property-only
	// form (WithIntentParameter at Register), so a call that omits the
	// rationale still validates at dispatch.
	return tool.WithIntentParameterRequired(td)
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
		// The profile's communicate definition is this session's result tool.
		// Present it under the configured name so the prompt's comparison with
		// the advertised set does not report the canonical name as an
		// unavailable provider tool when the result tool is renamed (issue #4906).
		if defs[i].Name == tool.DefaultResultToolName {
			defs[i].Name = s.resultToolName()
		}
		switch defs[i].Name {
		case "delegate":
			sandboxSchema := s.delegateSandboxSchemaForEnv(s.env)
			sandboxSchema.ModelDescription = s.delegateModelDescription
			defs[i] = tool.DefDelegateWithResultTool(s.delegateAgentTypeNames(), sandboxSchema, s.resultToolName())
		case "shell":
			defs[i] = s.sessionShellDefinition()
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
			if td.Name == "shell" {
				td = s.sessionShellDefinition()
			} else {
				// Registration builds descriptions that name the session's result
				// tool; the profile's copy names the default.
				td.Description = s.reg.Get(td.Name).Definition.Description
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
		if td.Name == "shell" {
			// A profile that omits shell still gets the environment-aware
			// schema here rather than the registry's fully capable copy
			// (issue #4422).
			td = s.sessionShellDefinition()
		}
		defs = append(defs, normalizeRegistryToolDefinition(td))
	}
	// The registry holds what this session can call, by canonical name.
	canMessageDelegates := s.reg.Get("delegate_send") != nil
	for i := range defs {
		if isResultToolDefinition(defs[i].Name, defs[i].Name, s.resultToolName()) {
			defs[i] = tool.WithoutIntentParameter(defs[i])
			if s.hasHumanPartnerToAsk() {
				defs[i] = tool.WithCommunicateEndReason(defs[i])
			}
			if s.reportsToParent() {
				defs[i] = tool.WithCommunicateDelegateUpdates(defs[i])
			}
			if canMessageDelegates {
				defs[i] = tool.WithCommunicateDelegatePointer(defs[i])
			}
		} else {
			// Same wire contract as wireToolDef: intent is required in the
			// advertised schema, optional in the registry's validation schema.
			defs[i] = tool.WithIntentParameterRequired(defs[i])
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
