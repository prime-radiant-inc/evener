package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/promptpath"
	"primeradiant.com/serf/llm"
)

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
func (s *Session) refreshSystemPromptCache() {
	s.cachedSystemPrompt = s.renderSystemPrompt()
}

// buildPromptData assembles a promptData from session state for template rendering.
func (s *Session) buildPromptData() promptData {
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
	// exec_command/grep_files/list_dir is contradictory and confuses tool use.
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

// renderSystemPrompt renders the system prompt using the template resolver.
func (s *Session) renderSystemPrompt() string {
	gitRoot := execenv.GitRootOrEmpty(s.env, s.envInfo.WorkingDir)
	projDir := promptpath.ProjectPromptsDir(gitRoot)
	if s.cfg.NoProjectPrompts {
		projDir = ""
	}

	projSections := ""
	globalSections := ""
	if projDir != "" {
		projSections = filepath.Join(projDir, "sections")
	}
	if gd := promptpath.GlobalPromptsDir(); gd != "" {
		globalSections = filepath.Join(gd, "sections")
	}

	resolver := &sectionResolver{
		provider: s.profile.BehaviorTag(),
		agent:    s.cfg.AgentName,
		agentFS:  embeddedAgents,
		sources: []sectionSource{
			diskSource{dir: projSections},
			diskSource{dir: globalSections},
			embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"},
		},
	}
	if resolver.agent == "" {
		resolver.agent = defaultAgentName
	}

	data := s.buildPromptData()

	templateName := "system"
	if s.depth > 0 {
		templateName = "subagent"
	}

	result, sources, err := resolver.RenderEmbedded(
		embeddedPrompts, "prompts/templates/", templateName, data,
	)
	if err != nil {
		// Template rendering should not fail — embedded templates are compiled into the binary.
		// Log the error and return a minimal prompt rather than silently degrading to legacy.
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("template render failed: %v", err)})
		return fmt.Sprintf("Template rendering failed: %v. Please report this bug.", err)
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
	return result
}
