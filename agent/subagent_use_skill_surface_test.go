package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

// TestTypedSubagentPolicyKeepsUseSkill pins the policy half of the rule that a
// delegate must be able to follow a skill-directed brief. A typed agent's
// tools: frontmatter is an allowlist handed to RestrictKeepingResultTool, so
// any tool it omits is DELETED from the child's registry — which strips
// use_skill from every shipped typed role (subagent, explorer, the
// coordinator-workflow roles) unless the policy re-adds it. Without the tool
// the child cannot activate a skill at all; its only recourse is the untracked
// read_file fallback, which the brief-stated skill step does not describe.
func TestTypedSubagentPolicyKeepsUseSkill(t *testing.T) {
	t.Parallel()
	_, allowed, _ := baseSubagentToolPolicy(&plugin.Agent{Tools: []string{"read_file"}}, false)
	if !slices.Contains(allowed, "use_skill") {
		t.Fatalf("typed agent allow-list = %v, want use_skill in it", allowed)
	}
}

func TestRootSystemPromptRetainsProjectDocsAndAvailableAgentsWithoutSkills(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	initGitRepo(t, dir)
	const docSentinel = "SENTINEL-ROOT-DOC-AGENTS-MD"
	writeTestFile(t, filepath.Join(dir, "AGENTS.md"), docSentinel+"\n")

	s := newSession(t,
		withDir(dir),
		withConfig(SessionConfig{
			MaxSubagentDepth: 2,
			StateDir:         t.TempDir(),
			testOnly: testConfig{
				skipGitSnapshot: true,
			},
		}),
	)
	s.delegationAllowance = 1
	// Zero skills: clear skill entries so data.Skills is empty.
	s.skills = skill.Catalog{}

	data, _ := s.buildPromptData(s.env)
	if data.IsSubagent {
		t.Fatal("expected root session to have IsSubagent=false")
	}
	if len(data.Skills) != 0 {
		t.Fatalf("expected 0 skills, got %d", len(data.Skills))
	}
	if len(data.AvailableAgents) == 0 {
		t.Fatal("expected AvailableAgents to be populated for root with delegationAllowance=1")
	}
	if len(data.ProjectDocs) == 0 {
		t.Fatal("expected ProjectDocs to be populated from AGENTS.md")
	}

	prompt, warning, _ := s.renderSystemPrompt(s.env)
	if warning != "" {
		t.Fatalf("renderSystemPrompt warning: %s", warning)
	}
	if strings.Contains(prompt, "<skill-catalog>") {
		t.Error("prompt unexpectedly contains <skill-catalog> when zero skills exist")
	}
	if !strings.Contains(prompt, "<available_agents>") || !strings.Contains(prompt, "</available_agents>") {
		t.Error("prompt missing <available_agents> when zero skills exist")
	}
	if !strings.Contains(prompt, docSentinel) {
		t.Error("prompt missing ProjectDocs sentinel when zero skills exist")
	}
}

func TestSubagentSystemPromptIncludesSkillCatalogWhenSkillsPresent(t *testing.T) {
	t.Parallel()
	skillsDir := t.TempDir()
	writeSkillDirect(t, skillsDir, "scout-skill", "---\nname: scout-skill\ndescription: Scout the workspace\n---\nBody.\n")

	for _, agentType := range []string{"default", "subagent", "explorer"} {
		t.Run(agentType, func(t *testing.T) {
			t.Parallel()
			agentArg := agentType
			if agentArg == "default" {
				agentArg = ""
			}
			parent := newSession(t, withConfig(SessionConfig{
				MaxSubagentDepth: 3,
				StateDir:         t.TempDir(),
				SkillsDirs:       []string{skillsDir},
				testOnly: testConfig{
					skipGitSnapshot: true,
					sandboxProber:   bwrapCapableProber(t.TempDir()),
				},
			}))
			parent.delegationAllowance = 1

			prepared := prepareStableChild(t, parent, delegateArgs{Task: "task", AgentType: agentArg})
			child := prepared.sub.sess
			defer child.Close()

			data, _ := child.buildPromptData(child.env)
			if !data.IsSubagent {
				t.Fatalf("agent_type=%q: expected IsSubagent=true", agentType)
			}
			if !data.HasUseSkill {
				t.Fatalf("agent_type=%q: expected HasUseSkill=true", agentType)
			}
			if len(data.Skills) == 0 {
				t.Fatalf("agent_type=%q: expected non-empty child.Skills", agentType)
			}
			var foundScout bool
			for _, sk := range data.Skills {
				if sk.CatalogName == "scout-skill" {
					foundScout = true
					break
				}
			}
			if !foundScout {
				t.Fatalf("agent_type=%q: expected scout-skill in child.Skills: %+v", agentType, data.Skills)
			}

			prompt, warning, _ := child.renderSystemPrompt(child.env)
			if warning != "" {
				t.Fatalf("agent_type=%q: renderSystemPrompt warning: %s", agentType, warning)
			}
			if !strings.Contains(prompt, "<skill-catalog>") || !strings.Contains(prompt, "</skill-catalog>") {
				t.Fatalf("agent_type=%q: rendered prompt missing structural <skill-catalog> tags", agentType)
			}

			call := child.reg.ExecuteCall(context.Background(), child.currentEnv(), llm.ToolCallData{
				ID:        "activate-scout",
				Name:      "use_skill",
				Arguments: json.RawMessage(`{"skill_name": "scout-skill"}`),
			})
			if call.IsError {
				t.Fatalf("agent_type=%q: use_skill call failed: %s", agentType, call.Output)
			}
		})
	}
}

// TestSubagentRegistryHasUseSkill is the behavioral half: a prepared child
// session's registry actually serves use_skill, for both the untyped default
// surface and a typed agent whose frontmatter never mentions it (subagent and
// explorer are the shipped built-ins the issue names).
func TestSubagentRegistryHasUseSkill(t *testing.T) {
	t.Parallel()
	for _, agentType := range []string{"", "subagent", "explorer"} {
		s := newSession(t, withConfig(SessionConfig{
			MaxSubagentDepth: 3,
			StateDir:         t.TempDir(),
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
				sandboxProber:       bwrapCapableProber(t.TempDir()),
			},
		}))
		s.delegationAllowance = 1
		prepared := prepareStableChild(t, s, delegateArgs{Task: "task", AgentType: agentType})
		if prepared.sub.sess.reg.Get("use_skill") == nil {
			t.Errorf("agent_type=%q: child registry has no use_skill", agentType)
		}
		prepared.sub.sess.Close()
	}
}
