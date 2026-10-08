package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/bundled"
	"primeradiant.com/evener/llm"
)

func renderResourceCapsJSON(cpus float64, memoryMB int64) string {
	if cpus <= 0 || math.IsNaN(cpus) || math.IsInf(cpus, 0) {
		cpus = 0
	}
	if memoryMB < 0 {
		memoryMB = 0
	}
	if cpus == 0 && memoryMB == 0 {
		return ""
	}
	caps := schema.ResourceCaps{
		CPUs:     cpus,
		MemoryMB: memoryMB,
	}
	payload, err := json.Marshal(caps)
	if err != nil {
		return ""
	}
	return string(payload)
}

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

// buildPromptData assembles a promptData from session state for template
// rendering, plus the PROMPT_LOADED sources for the inputs it read: the role
// and each append file. env is the ALREADY-RESOLVED execution environment
// (passed by renderSystemPrompt, which runs under a held s.mu): it must not be
// re-fetched via s.currentEnv(), which would re-lock the non-reentrant s.mu
// and deadlock.
func (s *Session) buildPromptData(env execenv.ExecutionEnvironment) (promptData, []promptSource) {
	agentName := s.cfg.AgentName
	if agentName == "" {
		agentName = defaultAgentName
	}
	var resourceCapsJSON string
	if resources := s.envInfo.Resources; resources != nil {
		resourceCapsJSON = renderResourceCapsJSON(resources.CPUs, resources.MemoryMB)
	}

	read := s.memoryContextEnabled()
	saves := s.memorySaveInstructionsEnabled()
	data := promptData{
		NonInteractive:           s.cfg.noOneToAsk(),
		BaseInstructionsOverride: strings.TrimSpace(s.systemPromptOverride),
		IsSubagent:               s.depth > 0,
		Surface:                  s.profile.Surface(),
		TurnEndsProcess:          s.cfg.TurnEndsProcess,
		MemoryRead:               read,
		MemorySearch:             read && s.canInstructTool("memory_search"),
		MemorySaves:              saves,
		ProjectMemory:            read && s.cfg.MemoryProjectID != "",
		WorkingDir:               s.envInfo.WorkingDir,
		IsGitRepo:                s.envInfo.IsGitRepo,
		GitBranch:                s.envInfo.GitBranch,
		Platform:                 s.envInfo.Platform,
		OSVersion:                s.envInfo.OSVersion,
		Today:                    s.envInfo.Today,
		Model:                    s.profile.Model(),
		KnowledgeCutoff:          s.envInfo.KnowledgeCutoff,
		ResourceCapsJSON:         resourceCapsJSON,
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
	var sources []promptSource
	var roleSource *promptSource
	data.Role, roleSource = resolveRolePrompt(s.cfg.spawn.rolePromptOverride, agentName, bundled.Agents())
	if roleSource != nil {
		sources = append(sources, *roleSource)
	}

	// Skills
	for _, descriptor := range s.skills.ModelEntries() {
		skillName, sm := descriptor.CatalogName, descriptor.Meta
		data.Skills = append(data.Skills, skillEntry{
			Name: sm.Name, CatalogName: skillName, Description: sm.Description,
			Dir: sm.Dir, SkillFile: sm.SkillFile,
		})
	}

	// Profile tools (provider-visible wire form, matching what the API receives)
	profileDefs := s.profileWireToolDefs()
	// Use the same provider-visible tool definitions that are sent to the model.
	// Prompting with canonical names while the API receives mapped names such as
	// exec_command/grep_files/find_files is contradictory and confuses tool use.
	actualDefs := append([]llm.ToolDefinition(nil), s.cachedToolDefs...)
	data.HasUseSkill = toolNameSetFromDefinitions(actualDefs)["use_skill"]
	data.CallableToolNames = toolNamesFromDefinitions(actualDefs)
	data.UnavailableProfileToolNames = unavailableToolNames(profileDefs, actualDefs)

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
	data.HasEndReason = s.hasHumanPartnerToAsk()

	// Available subagent types
	data.AvailableAgents = s.availableAgentEntries()

	// CLI appends: each file is read once, and that read supplies both the
	// prompt text and its PROMPT_LOADED source. An unreadable file is skipped.
	for _, p := range s.cfg.SystemPromptAppend {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		data.CLIAppends = append(data.CLIAppends, string(b))
		sources = append(sources, promptSource{Label: "append:" + p, Size: len(strings.TrimRight(string(b), "\n"))})
	}

	return data, sources
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

// sandboxPromptLine renders the environment-section sandbox line for an env whose
// file tools are confined, so the model knows the immutable box it runs under —
// its mode and network when an OS sandbox enforces it, and which half is enforced
// when only the file tools do (see sandboxPromptBoundary). When a scratch
// directory has been provisioned, its path is appended (kata g8q6): a spawned
// shell command learns the scratch dir through $TMPDIR/$EVENER_SCRATCH_DIR, but
// the model's own file tools (write_file, read_file, …) never see process
// environment variables, so without this line a model has no way to discover the
// one directory its file tools can actually write to outside the worktree — it was
// observed guessing a literal "/tmp/...", which every sandboxed mode denies.
// Empty for an env whose file tools are unconfined, so the line is omitted
// entirely (byte-identical prompt to today). Takes the resolved env directly —
// the prompt-render path holds s.mu, so it must not re-fetch via s.currentEnv().
func sandboxPromptLine(env execenv.ExecutionEnvironment) string {
	le, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok || le.Sandbox == nil || !le.Sandbox.FileToolConfined() {
		return ""
	}
	line := sandboxPromptBoundary(le.Sandbox)
	if scratch := le.SessionScratchDir(); scratch != "" {
		line += ". Scratch directory (read-write even in this sandbox; also $" +
			envvars.TmpDir.Name + " / $" + envvars.EVENERScratchDir.Name + " for shell commands): " + scratch
		if le.Sandbox.Mode == sandbox.ModeReadOnly || le.Sandbox.WriteBlocked {
			if le.Sandbox.Enforced() {
				line += ". Read-only delegates may write only inside this scratch directory; all other writes are denied"
			} else {
				line += ". Your file tools may write only inside this scratch directory; all other file-tool writes are denied"
			}
		}
		line += ". It is deleted when your root session is archived; return what your parent needs in your result."
	}
	return line
}

// sandboxPromptBoundary states what the box actually holds. An enforced policy
// names its mode and network decision. A write-blocked policy with no OS sandbox
// — a read-only delegate on a host with no backend — must not name its mode:
// "off" would describe the ABSENT kernel box rather than the boundary the model
// actually runs under, and reporting it as an ordinary read-only sandbox would
// overstate. It says which half is enforced and which half is on the model's
// honour, because a degradation nobody discloses is how a delegate deleted its
// parent's deliverable.
func sandboxPromptBoundary(rp *sandbox.ResolvedPolicy) string {
	if !rp.Enforced() {
		boundary, _ := degradedReadOnlyBoundaryFor(rp.Mode, rp.WriteBlocked)
		return "read-only for your file tools — fixed for this session. This host has no sandbox backend, so the boundary is ENFORCED for your file tools (every file-tool write outside your scratch directory is denied) and ADVISORY for your shell: " + boundary.shellDisclosure("your shell") + ". Do not write outside the scratch directory. Your file tools will not traverse a symlinked directory either; if one is refused, name the real path instead of retrying"
	}
	netStr := "on"
	if !rp.Network {
		netStr = "off"
	}
	return fmt.Sprintf("%s (network %s) — fixed for this session", rp.Mode, netStr)
}

// renderSystemPrompt renders the system prompt from the embedded template. It
// returns the prompt and, when the render failed, the diagnostic its caller
// must report; see refreshSystemPromptCache for why that is returned rather
// than emitted here, and for the env-locking contract.
func (s *Session) renderSystemPrompt(env execenv.ExecutionEnvironment) (string, string) {
	data, inputSources := s.buildPromptData(env)
	result, err := executeSystemPromptTemplate(data)
	if err != nil {
		// The template parsed at package initialization, so an execution error
		// is a bug. Report it and hand the model a minimal prompt that says so.
		return fmt.Sprintf("Template rendering failed: %v. Please report this bug.", err),
			fmt.Sprintf("template render failed: %v", err)
	}
	sources := make([]promptSource, 0, len(inputSources)+2)
	if data.BaseInstructionsOverride != "" {
		sources = append(sources, promptSource{Label: "cli:" + s.cfg.SystemPromptFile, Size: len(data.BaseInstructionsOverride)})
	}
	sources = append(sources, promptSource{Label: systemPromptTemplateLabel, Size: len(result)})
	sources = append(sources, inputSources...)
	s.promptSourceLog = sources
	return result, ""
}
