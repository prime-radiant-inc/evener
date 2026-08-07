package agent

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/promptpath"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/bundled"
	"primeradiant.com/serf/llm"
)

var renderEmbeddedSystemPrompt = func(resolver *sectionResolver, fs embed.FS, prefix, name string, data promptData) (string, []promptSource, error) {
	return resolver.RenderEmbedded(fs, prefix, name, data)
}

var projectPromptDir = func(env execenv.ExecutionEnvironment, workingDir string) string {
	gitRoot := execenv.GitRootOrEmpty(env, workingDir)
	return promptpath.ProjectPromptsDir(gitRoot)
}

var globalPromptDir = promptpath.GlobalPromptsDir

func prependSystemPromptToUserMessage(systemPrompt string, user llm.Message) llm.Message {
	combined := user
	parts := make([]llm.ContentPart, 0, len(user.Content)+1)
	if strings.TrimSpace(systemPrompt) != "" {
		parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: systemPrompt + "\n\n"})
	}
	parts = append(parts, user.Content...)
	combined.Content = parts
	return combined
}

// rebuildPromptCache caches system prompt components that don't change between
// renders. env is the execution environment to render against: callers that
// already hold s.mu (e.g. SetModel) must pass s.env directly; callers that
// don't must resolve it via currentEnv() first — renderSystemPrompt cannot
// call currentEnv() itself since it is invoked from both locked and unlocked
// contexts and s.mu is not reentrant.
// It RETURNS the diagnostic for a failed render rather than emitting it, and
// that is a lock-safety requirement, not a style choice. Three of its four
// callers run under s.mu, and emit's first act is activeCausalProvenance(),
// which takes s.mu — so emitting from in here self-deadlocks a non-reentrant
// mutex with no concurrency involved at all.
//
// This RELOCATES the rule, it does not retire it. What the callee can no longer
// do, the caller now must: report only after unlocking (reportPromptRenderFailure),
// or buffer instead, which is what initSessionState does because nothing may
// reach the stream before SESSION_START. Each of the three locked call sites
// carries its own regression test, because moving the report back inside any one
// of those critical sections leaves the whole package green otherwise.
//
// The empty string means the render succeeded.
func (s *Session) refreshSystemPromptCache(env execenv.ExecutionEnvironment) string {
	if s.cfg.testOnly.minimalSystemPrompt {
		s.cachedSystemPrompt = "test system prompt"
		s.promptSourceLog = nil
		return ""
	}
	prompt, warning := s.renderSystemPrompt(env)
	s.cachedSystemPrompt = prompt
	return warning
}

// reportPromptRenderFailure emits the diagnostic refreshSystemPromptCache
// returned, if any. Callers MUST call it after releasing s.mu: it goes through
// emit, which takes s.mu to stamp provenance.
//
// It is a named function rather than an inline `if` at each site so the lock
// contract has somewhere to be written down once, and so a reader at a call
// site can see that the emit is deliberately outside the critical section
// above it rather than incidentally after it.
func (s *Session) reportPromptRenderFailure(warning string) {
	if warning == "" {
		return
	}
	s.emit(events.EventWarning, events.WarningData{Message: warning})
}

func promptSectionDirExists(dir string) bool {
	if dir == "" {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// buildPromptData assembles a promptData from session state for template rendering.
// env is the ALREADY-RESOLVED execution environment (passed by renderSystemPrompt,
// which runs under a held s.mu): it must not be re-fetched via s.currentEnv(), which
// would re-lock the non-reentrant s.mu and deadlock.
func (s *Session) buildPromptData(env execenv.ExecutionEnvironment) promptData {
	agentName := s.cfg.AgentName
	if agentName == "" {
		agentName = defaultAgentName
	}

	data := promptData{
		NonInteractive:           s.cfg.NonInteractive,
		Provider:                 s.profile.ID(),
		Agent:                    agentName,
		BaseInstructionsOverride: strings.TrimSpace(s.systemPromptOverride),
		RolePromptOverride:       strings.TrimSpace(s.cfg.spawn.rolePromptOverride),
		WorkingDir:               s.envInfo.WorkingDir,
		IsGitRepo:                s.envInfo.IsGitRepo,
		GitBranch:                s.envInfo.GitBranch,
		Platform:                 s.envInfo.Platform,
		OSVersion:                s.envInfo.OSVersion,
		Today:                    s.envInfo.Today,
		Model:                    s.profile.Model(),
		KnowledgeCutoff:          s.envInfo.KnowledgeCutoff,
		Sandbox:                  sandboxPromptLine(env),
		Capabilities:             capabilityPreambleLines(capabilityFactsFromEnv(env, s.capabilities)),
		GitModifiedFiles:         s.envInfo.GitModifiedFiles,
		GitUntrackedFiles:        s.envInfo.GitUntrackedFiles,
		GitRecentCommitTitles:    s.envInfo.GitRecentCommitTitles,
		WorkspaceTree:            s.envInfo.Workspace.Tree,
		BuildInfo:                s.envInfo.Workspace.BuildInfo,
		ResultToolName:           s.resultToolName(),
		UserInstructionOverride:  strings.TrimSpace(s.cfg.UserInstructionOverride),
		ProjectDocs:              s.projectDocs,
		ActivatedSkillBodies:     append([]string(nil), s.cfg.spawn.activatedSkillBodies...),
	}

	// Skills
	hasUseSkill := false
	for _, td := range s.profile.ToolDefinitions() {
		if td.Name == "use_skill" {
			hasUseSkill = true
			break
		}
	}
	data.HasUseSkill = hasUseSkill
	for skillName, sm := range s.skills {
		data.Skills = append(data.Skills, skillEntry{
			Name: sm.Name, CatalogName: skillName, Description: sm.Description,
			Dir: sm.Dir, SkillFile: sm.SkillFile,
		})
	}

	// Profile tools (provider-visible wire form, matching what the API receives)
	profileDefs := s.profileWireToolDefs()
	data.ProfileTools = toolEntriesFromDefinitions(profileDefs)
	// Use the same provider-visible tool definitions that are sent to the model.
	// Prompting with canonical names while the API receives mapped names such as
	// exec_command/grep_files/find_files is contradictory and confuses tool use.
	actualDefs := append([]llm.ToolDefinition(nil), s.cachedToolDefs...)
	data.CallableToolNames = toolNamesFromDefinitions(actualDefs)
	data.UnavailableProfileToolNames = unavailableToolNames(profileDefs, actualDefs)

	// MCP tools
	data.MCPTools = toolEntriesFromDefinitions(s.mcpTools)

	// Custom tools (not core, not MCP)
	mcpNames := make(map[string]bool, len(s.mcpTools))
	for _, td := range s.mcpTools {
		mcpNames[td.Name] = true
	}
	var customToolDefs []llm.ToolDefinition
	for _, td := range s.reg.Definitions() {
		if s.coreToolNames[td.Name] || mcpNames[td.Name] {
			continue
		}
		customToolDefs = append(customToolDefs, td)
	}
	data.CustomTools = toolEntriesFromDefinitions(customToolDefs)

	// Delegation capability: a grantable allowance (> 0) unlocks the delegation
	// and background-jobs prompt surface only when those tools are callable.
	data.DelegationAllowance = s.delegationAllowance
	data.CanDelegate = s.canPromptDelegation()

	// The section-facing tool-availability set, canonical names straight from
	// the registry (see promptData.HasTool).
	data.CallableTools = s.reg.RegisteredNames()

	// ask_user's registration IS the interactive-root gate itself (spec §7
	// point 1); reading it back from the registry avoids a second predicate
	// that could drift from the real gate.
	data.HasAskUser = s.reg.Get("ask_user") != nil

	// Available subagent types
	data.AvailableAgents = s.availableAgentEntries()

	// CLI appends: read file paths into contents
	for _, p := range s.cfg.SystemPromptAppend {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		data.CLIAppends = append(data.CLIAppends, string(b))
	}

	return data
}

func (s *Session) canPromptDelegation() bool {
	if s.delegationAllowance <= 0 || s.reg == nil {
		return false
	}
	for _, name := range delegationPromptToolNames {
		if s.reg.Get(name) == nil {
			return false
		}
	}
	return true
}

// sandboxPromptLine renders the environment-section sandbox line for a sandboxed
// env ("<mode> (network on|off) — fixed for this session"), so the model knows the
// immutable box it runs under. When a kernel wrapper has provisioned a real
// scratch directory, its path is appended (kata g8q6): a spawned shell command
// learns the scratch dir through $TMPDIR/$SERF_SCRATCH_DIR, but the model's own
// file tools (write_file, read_file, …) never see process environment
// variables, so without this line a model has no way to discover the one
// directory its file tools can actually write to outside the worktree — it was
// observed guessing a literal "/tmp/...", which every sandboxed mode denies.
// Empty for an unsandboxed env so the line is omitted entirely (byte-identical
// prompt to today). Takes the resolved env directly — the prompt-render path
// holds s.mu, so it must not re-fetch via s.currentEnv().
func sandboxPromptLine(env execenv.ExecutionEnvironment) string {
	le, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok || le.Sandbox == nil || !le.Sandbox.Enforced() {
		return ""
	}
	netStr := "on"
	if !le.Sandbox.Network {
		netStr = "off"
	}
	line := fmt.Sprintf("%s (network %s) — fixed for this session", le.Sandbox.Mode, netStr)
	if le.Wrapper != nil {
		if scratch := le.Wrapper.SessionTmp(); scratch != "" {
			line += ". Scratch directory (read-write even in this sandbox; also $" +
				envvars.TmpDir.Name + " / $" + envvars.SERFScratchDir.Name + " for shell commands): " + scratch
			if le.Sandbox.Mode == sandbox.ModeReadOnly {
				line += ". Read-only delegates may write only inside this scratch directory; all other writes are denied."
			}
			line += ". In your final human-readable handoff, report this absolute scratch path and the absolute paths of any artifacts your parent should retain; cleanup is manual."
		}
	}
	return line
}

// renderSystemPrompt renders the system prompt using the template resolver. It
// returns the prompt and, when the render failed, the diagnostic its caller
// must report; see refreshSystemPromptCache for why that is returned rather
// than emitted here, and for the env-locking contract.
func (s *Session) renderSystemPrompt(env execenv.ExecutionEnvironment) (string, string) {
	projDir := ""
	if !s.cfg.NoProjectPrompts {
		projDir = projectPromptDir(env, s.envInfo.WorkingDir)
	}

	projSections := ""
	globalSections := ""
	if projDir != "" {
		projSections = filepath.Join(projDir, "sections")
	}
	if gd := globalPromptDir(); gd != "" {
		globalSections = filepath.Join(gd, "sections")
	}

	sectionSources := make([]sectionSource, 0, 3)
	if promptSectionDirExists(projSections) {
		sectionSources = append(sectionSources, diskSource{dir: projSections})
	}
	if promptSectionDirExists(globalSections) {
		sectionSources = append(sectionSources, diskSource{dir: globalSections})
	}
	sectionSources = append(sectionSources, embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"})

	resolver := &sectionResolver{
		provider: s.profile.BehaviorTag(),
		agent:    s.cfg.AgentName,
		agentFS:  bundled.Agents(),
		sources:  sectionSources,
	}
	if resolver.agent == "" {
		resolver.agent = defaultAgentName
	}

	data := s.buildPromptData(env)

	templateName := "system"
	if s.depth > 0 {
		templateName = "subagent"
	}

	result, sources, err := renderEmbeddedSystemPrompt(resolver,
		embeddedPrompts, "prompts/templates/", templateName, data,
	)
	if err != nil {
		// Template rendering should not fail — embedded templates are compiled into the binary.
		// Report the error and return a minimal prompt rather than silently degrading to legacy.
		return fmt.Sprintf("Template rendering failed: %v. Please report this bug.", err),
			fmt.Sprintf("template render failed: %v", err)
	}
	if trimmed := strings.TrimSpace(s.systemPromptOverride); trimmed != "" {
		sources = append([]promptSource{{
			Label: "cli:" + s.cfg.SystemPromptFile,
			Size:  len(trimmed),
		}}, sources...)
	}
	for _, p := range s.cfg.SystemPromptAppend {
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			continue
		}
		sources = append(sources, promptSource{
			Label: "append:" + p,
			Size:  len(strings.TrimRight(string(b), "\n")),
		})
	}
	s.promptSourceLog = sources
	return result, ""
}
