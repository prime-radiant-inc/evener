package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
)

// allToolDefinitions returns cached tool definitions.
// The cache is built once at session init via rebuildToolDefsCache.
func (s *Session) allToolDefinitions(_ int) []llm.ToolDefinition {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cachedToolDefs
}

// ToolDefinitions returns the model-facing tool definitions currently
// advertised by this session.
func (s *Session) ToolDefinitions() []llm.ToolDefinition {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llm.ToolDefinition(nil), s.cachedToolDefs...)
}

// defaultToolsForAgent is the tool set a role's delegate receives when this
// session starts one with no delegation_allowance and no isolation: the
// ceiling the spawn applies, so the roster cannot advertise a capability the
// grant withholds.
func (s *Session) defaultToolsForAgent(registered map[string]bool, agent plugin.Agent) []string {
	canonical := s.subagentToolNameCeilingFromRegistered(registered, &agent, canGrantDelegation(s.delegationAllowance), "")
	canonical = ensureRecoveryReader(canonical, s.reg)
	return s.providerVisibleToolNames(canonical)
}

// delegatableAgentNames returns the role names this session may offer through
// delegate, sorted, or nil when it may offer none.
//
// It is the ONE answer to "which roles exist for this session": the prompt
// roster, the schema enum, and the capability roster the schema's description
// carries all read it, so they cannot list different roles.
func (s *Session) delegatableAgentNames() []string {
	allowance := s.delegationAllowance // read under caller's lock or during single-threaded init
	if allowance <= 0 || !s.canPromptDelegation() {
		return nil
	}
	return slices.Sorted(maps.Keys(s.pluginAgents))
}

// availableAgentEntries is the <available_agents> input: what each role is for
// and which tasks it starts with.
//
// It deliberately carries NO tool list. That list used to be rendered here as
// "Default tools:", computed at launch and then frozen with the rest of the
// launch prompt (#6959), so a project MCP config edited mid-session left the
// prompt advertising the launch-time capabilities while the delegate schema
// advertised the current ones — two capability lists on one request, one of
// them wrong. The delegate tool's agent_type description is the single source
// for current per-role capabilities precisely because it is rebuilt per
// request; keeping tools out of this data is what makes that source single.
func (s *Session) availableAgentEntries() []agentEntry {
	names := s.delegatableAgentNames()
	entries := make([]agentEntry, 0, len(names))
	for _, name := range names {
		agent := s.pluginAgents[name]
		entries = append(entries, agentEntry{
			Name:        name,
			Description: agent.Description,
			TaskList:    agentTaskEntries(agent.Tasks),
		})
	}
	return entries
}

// delegateCapabilityRoster is the per-role capability list the delegate
// schema's agent_type description carries: the tool set a delegate of each role
// receives with no delegation_allowance.
//
// This is the authoritative list. It is computed from the live registry and the
// current exclusion verdict every time the schema is built, which is every
// request, so it describes what a child spawned now can actually register.
func (s *Session) delegateCapabilityRoster() string {
	names := s.delegatableAgentNames()
	if len(names) == 0 {
		return ""
	}
	var registered map[string]bool
	if s.reg != nil {
		registered = s.reg.RegisteredNames()
	}
	capabilities := make([]string, 0, len(names))
	for _, name := range names {
		tools := formatToolNamesForPrompt(s.defaultToolsForAgent(registered, s.pluginAgents[name]))
		capabilities = append(capabilities, fmt.Sprintf("%s: %s", name, tools))
	}
	return strings.Join(capabilities, "; ")
}

func (s *Session) delegateToolDefinition() llm.ToolDefinition {
	sandboxSchema := s.delegateSandboxSchemaForEnv(s.env)
	sandboxSchema.ModelDescription = s.delegateModelDescription
	definition := tool.DefDelegateWithResultTool(s.delegateAgentTypeNames(), sandboxSchema, s.resultToolName())
	s.applyDelegateCapabilityRoster(&definition)
	return definition
}

// applyDelegateCapabilityRoster writes the current role capabilities into a
// delegate tool definition's agent_type description.
//
// It is the ONE writer of that description, called from both places that build a
// delegate schema: the ordinary definition, and the Manager-composed one the hub
// branch serves. Two writers is how the schema and the prompt roster came to
// disagree — Manager composition saved the description before MCP initialization,
// so serving its copy verbatim advertised the startup capability list on a
// request whose prompt listed the current one. A config edit that makes a
// CLI-only tool reachable refreshed the prompt and left the schema behind, and
// the model saw the tool in the roster and not in the schema that grants it.
//
// It REPLACES the maps it writes rather than mutating them, because the
// definition it is handed can be a copy of a published registration:
// Registry.Get returns the tool struct by value while Parameters is the
// registered map. Writing through to agent_type there would edit the schema
// every other reader sees — including argument repair, on the very request
// this call is preparing — so the write stays on the caller's own definition.
func (s *Session) applyDelegateCapabilityRoster(definition *llm.ToolDefinition) {
	if s == nil || definition == nil {
		return
	}
	roster := s.delegateCapabilityRoster()
	if roster == "" {
		return
	}
	definition.Description += " Current per-role capabilities are listed in the agent_type schema, which is authoritative."
	properties, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		return
	}
	agentType, ok := properties["agent_type"].(map[string]any)
	if !ok {
		return
	}
	updated := maps.Clone(agentType)
	clonedProperties := maps.Clone(properties)
	clonedParameters := maps.Clone(definition.Parameters)
	if updated == nil || clonedProperties == nil || clonedParameters == nil {
		return
	}
	// The capability list is stated here, and ONLY here, because this schema is
	// built fresh on every request. The launch system prompt is immutable
	// (#6959), so a list rendered into it states what a role could call at
	// launch — not now. A session restored from such a prompt carries the older
	// list, which is why the precedence is spelled out rather than implied.
	updated["description"] = "Role for the delegate. Choose from the enum; effective capabilities by role: " + roster +
		". This capability list is authoritative. It supersedes any per-role tool list in an <available_agents> block in your system prompt, including one restored from an earlier launch. " +
		tool.DelegateRoleWriteNote
	clonedProperties["agent_type"] = updated
	clonedParameters["properties"] = clonedProperties
	definition.Parameters = clonedParameters
}

func (s *Session) delegateAgentTypeNames() []string {
	return s.delegatableAgentNames()
}

// withoutDelegateAllowance hides the delegation_allowance property when no legal
// grant lets the child delegate, so the model is not offered a parameter it
// cannot meaningfully set.
//
// It returns a definition whose property map is its OWN. The definition it is
// handed can be a copy of a published registration — Manager composition's is —
// and Registry.Get hands out the tool struct by value while Parameters is the
// registered map. A delete through that copy would edit the schema every other
// reader sees, including argument repair on the request being prepared.
func withoutDelegateAllowance(td llm.ToolDefinition) llm.ToolDefinition {
	properties, ok := td.Parameters["properties"].(map[string]any)
	if !ok {
		return td
	}
	if _, present := properties["delegation_allowance"]; !present {
		return td
	}
	clonedProperties := maps.Clone(properties)
	clonedParameters := maps.Clone(td.Parameters)
	if clonedProperties == nil || clonedParameters == nil {
		return td
	}
	delete(clonedProperties, "delegation_allowance")
	clonedParameters["properties"] = clonedProperties
	td.Parameters = clonedParameters
	return td
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

// sessionReadFileDefinition returns the read_file tool definition tailored to
// this session's active vision regime (Jesse's ruling on #6190).
//
// Regime 1 — Neither images nor PDFs run the side-channel (e.g. visionOff is
// true, or AgentName == "explorer", or VisionModel == "" and the active profile
// on the client's registry sees both "image/png" and "application/pdf" inline):
// vision_prompt is omitted from advertised properties and description.
//
// Regime 2 — Images are native inline, while PDFs use the side-channel (VisionModel
// == "" and active profile sees "image/png" inline but not "application/pdf" inline):
// vision_prompt is kept and scoped to PDFs in description and parameter text.
//
// Regime 3 — Side-channel active for both images and PDFs (VisionModel != "" and
// not off, or active profile does not see "image/png" inline): base DefReadFile().
//
// Callers hold s.mu or run at single-threaded init.
func (s *Session) sessionReadFileDefinition() llm.ToolDefinition {
	if s == nil {
		return tool.DefReadFile()
	}
	if s.cfg.AgentName == "explorer" {
		return tool.DefReadFileNativeVision()
	}
	visionSetting := strings.TrimSpace(s.cfg.VisionModel)
	if isVisionDisabledSetting(visionSetting) {
		return tool.DefReadFileNativeVision()
	}
	if visionSetting != "" {
		return tool.DefReadFile()
	}
	if s.profile == nil || s.client == nil || s.profile.Registry() != s.client.Registry() {
		return tool.DefReadFile()
	}
	seesImages := s.profile.SeesToolResultMedia("image/png")
	seesPDFs := s.profile.SeesToolResultMedia("application/pdf")
	if seesImages && seesPDFs {
		return tool.DefReadFileNativeVision()
	}
	if seesImages && !seesPDFs {
		return tool.DefReadFilePDFVisionOnly()
	}
	return tool.DefReadFile()
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
		case "read_file":
			defs[i] = s.sessionReadFileDefinition()
		case "memory_read":
			defs[i] = tool.MemoryDefinition(s.sessionReadFileDefinition(), "memory_read")
		}
		defs[i] = wireToolDef(defs[i], nameMap, s.resultToolName())
	}
	return defs
}

// rebuildToolDefsCache builds the cached tool definition lists from the
// current profile, MCP tools, and registry state. Called once at session init
// and again if tools are added at runtime (e.g. MCP or custom tools).
var tier2DeferredGroups = map[string]string{
	"job_list":                 "jobs",
	"job_status":               "jobs",
	"job_stop":                 "jobs",
	"job_watch":                "jobs",
	"manage_worktree":          "worktrees",
	"read_transcript":          "transcripts",
	"find_session_transcripts": "transcripts",
	"doctor_evener":            "doctor",
	"compact_context":          "compact",
	"web_fetch":                "secondary",
	"model_list":               "secondary",
	"notes_agent_set":          "secondary",
	"notes_read":               "secondary",
	"urls_add":                 "secondary",
	"urls_remove":              "secondary",
	"memory_read":              "secondary",
	"memory_write":             "secondary",
	"memory_edit":              "secondary",
	"memory_delete":            "secondary",
	"memory_search":            "secondary",
}

func (s *Session) isTier1PromotedLocked(canonicalName string) bool {
	if s == nil || len(s.cfg.Tier1Tools) == 0 {
		return false
	}
	nameMap := s.toolNameMap()
	for _, raw := range s.cfg.Tier1Tools {
		if canonicalToolName(raw, nameMap) == canonicalName {
			return true
		}
	}
	return false
}

// isDeferredToolLocked reports whether canonicalName is deferred to a minimal
// Tier-2 stub in req.Tools (with full schemas discoverable via tool_search and
// JIT disclosures). Manager sessions, parent observers, and configured
// Tier1Tools keep matching tools upfront with full Tier-1 schemas.
func (s *Session) isDeferredToolLocked(canonicalName string) bool {
	if s == nil {
		return false
	}
	group, ok := tier2DeferredGroups[canonicalName]
	if !ok {
		return false
	}
	if s.isTier1PromotedLocked(canonicalName) {
		return false
	}
	if group == "jobs" && (s.hubEnabled || s.cfg.spawn.parentWatchGranted) {
		return false
	}
	if s.hubEnabled && (canonicalName == "read_transcript" || canonicalName == "model_list") {
		return false
	}
	return true
}

func (s *Session) hasDeferredToolsLocked() bool {
	if s == nil || s.reg == nil {
		return false
	}
	for _, name := range s.reg.Names() {
		if s.isDeferredToolLocked(name) {
			return true
		}
	}
	return false
}

// deferredToolStubLocked returns the minimal static open-object stub for a
// deferred Tier-2 tool. All deferred stubs carry an empty description except
// delegate, which gets a compact 1-line delegation cue.
func (s *Session) deferredToolStubLocked(canonicalName string, nameMap map[string]string) llm.ToolDefinition {
	strictFalse := false
	desc := ""
	if canonicalName == "delegate" {
		if s.reg != nil && s.reg.Get("tool_search") != nil {
			desc = `Start a parallel delegate session for an independent task; call tool_search(names=["delegate"]) for full parameters.`
		} else {
			desc = "Start a parallel delegate session for an independent task."
		}
	}
	return llm.ToolDefinition{
		Name:        providerToolName(canonicalName, nameMap),
		Description: desc,
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": true,
		},
	}
}

func (s *Session) rebuildToolDefsCache() {
	if s == nil || s.reg == nil {
		if s != nil {
			s.cachedToolDefs = nil
			s.cachedToolDefsJSONBytes = 0
			s.toolDefsGen++
		}
		return
	}
	s.composeHubRegistry()
	hasDeferred := s.syncToolSearchRegistrationLocked()
	registered := s.reg.RegisteredNames()

	// Profile tool definitions are canonical (e.g. "shell"); the registry is also
	// keyed by canonical names. Each registered profile tool is advertised in its
	// provider-visible wire form (provider-specific rename + intent param).
	var nameMap map[string]string // canonical → provider, may be nil
	var profileDefs []llm.ToolDefinition
	if s.profile != nil {
		nameMap = s.profile.ToolNameMap()
		profileDefs = s.profile.ToolDefinitions()
	}

	var defs []llm.ToolDefinition
	included := make(map[string]bool)
	for _, td := range profileDefs {
		if registered[td.Name] {
			// Manager composition already owns both the ordinary and hub branches.
			// Keep its parameters as well as its description on provider requests.
			// Composition writes the capability roster too, on every rebuild, so
			// there is one writer of this schema and nothing left to re-decorate
			// here.
			if _, composed := s.hubOrdinary[td.Name]; s.hubEnabled && composed {
				td = s.reg.Get(td.Name).Definition
			}
			if td.Name == "delegate" {
				if !s.hubEnabled {
					td = s.delegateToolDefinition()
				}
				// When no legal grant lets the child delegate, delegation_allowance has
				// a single useful value — a no-op knob. Hide it so the model is not
				// offered a parameter it cannot meaningfully set.
				//
				// The delete runs on a CLONE. In Manager mode td is composition's
				// registered definition, whose Parameters map IS the registered one:
				// a delete through it would edit the schema argument repair and every
				// other reader see, on the very request this cache serves.
				if !canGrantDelegation(s.delegationAllowance) {
					td = withoutDelegateAllowance(td)
				}
				if !s.hubEnabled && s.delegationAllowance > 0 && s.reg != nil && s.reg.Get("delegate") != nil {
					regTool := s.reg.Get("delegate")
					regTool.Definition = td
					// The registry executor validates calls against regTool.Schema.
					// Remove the static enum from regTool's parameters so tests and runtime plugins
					// can add agents dynamically to s.pluginAgents without failing schema validation.
					if paramsJSON, err := json.Marshal(td.Parameters); err == nil {
						var regParams map[string]any
						if err := json.Unmarshal(paramsJSON, &regParams); err == nil {
							if props, ok := regParams["properties"].(map[string]any); ok {
								if agentTypeProp, ok := props["agent_type"].(map[string]any); ok {
									delete(agentTypeProp, "enum")
								}
							}
							regTool.Definition.Parameters = regParams
						}
					}
					regTool.Schema = nil
					_ = s.reg.Register(*regTool)
				}
			}
			if s.isDeferredToolLocked(td.Name) {
				stub := s.deferredToolStubLocked(td.Name, nameMap)
				defs = append(defs, stub)
				included[td.Name] = true
				included[stub.Name] = true
				continue
			}
			switch td.Name {
			case "shell":
				td = s.sessionShellDefinition()
			case "read_file":
				td = s.sessionReadFileDefinition()
			case "memory_read":
				td = tool.MemoryDefinition(s.sessionReadFileDefinition(), "memory_read")
			default:
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
		if s.isDeferredToolLocked(td.Name) {
			stub := s.deferredToolStubLocked(td.Name, nameMap)
			defs = append(defs, stub)
			included[td.Name] = true
			included[stub.Name] = true
			continue
		}
		if td.Name == "tool_search" {
			if !hasDeferred {
				continue
			}
		}
		// Never expose both apply_patch and edit_file in the same session; both
		// stay registered in s.reg for mid-session model switching.
		if (td.Name == "apply_patch" && included["edit_file"]) || (td.Name == "edit_file" && included["apply_patch"]) {
			continue
		}
		if td.Name == "update_goal" && !s.hasGoalConfigured() {
			continue
		}
		switch td.Name {
		case "shell":
			// A profile that omits shell still gets the environment-aware
			// schema here rather than the registry's fully capable copy
			// (issue #4422).
			td = s.sessionShellDefinition()
		case "read_file":
			td = s.sessionReadFileDefinition()
		case "memory_read":
			td = tool.MemoryDefinition(s.sessionReadFileDefinition(), "memory_read")
		}
		if s.hubEnabled && (td.Name == "job_list" || td.Name == "job_status") {
			if mapped, ok := nameMap[td.Name]; ok {
				td.Name = mapped
			}
		}
		defs = append(defs, normalizeRegistryToolDefinition(td))
		included[td.Name] = true
	}
	// The registry holds what this session can call, by canonical name.
	canMessageDelegates := s.reg.Get("delegate_send") != nil
	for i := range defs {
		if isResultToolDefinition(defs[i].Name, defs[i].Name, s.resultToolName()) {
			defs[i] = tool.WithoutIntentParameter(defs[i])
			if s.reportsToParent() {
				defs[i] = tool.WithCommunicateDelegateUpdates(defs[i])
			}
			if canMessageDelegates {
				defs[i] = tool.WithCommunicateDelegatePointer(defs[i])
			}
		} else if !s.isDeferredToolLocked(canonicalToolName(defs[i].Name, nameMap)) {
			// Same wire contract as wireToolDef: intent is required in the
			// advertised schema, optional in the registry's validation schema.
			defs[i] = tool.WithIntentParameterRequired(defs[i])
		}
	}

	s.cachedToolDefs = defs
	s.cachedToolDefsJSONBytes = llm.ToolsJSONBytes(defs)
	s.toolDefsGen++
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

func (s *Session) hasGoalConfigured() bool {
	if s == nil {
		return false
	}
	_, ok := s.getOrCreateGoalStore().Snapshot()
	return ok
}
