package agent

import (
	"strings"
	"testing"
)

func TestToolsSectionPreservesNamedDeliverySurfaces(t *testing.T) {
	t.Parallel()
	section := postureSectionResolver("coordinator").Section("tools", promptData{
		Provider: "openai",
		Agent:    "coordinator",
	})
	for _, want := range []string{
		"inspect and experiment on a copy",
		"delivered artifact or process",
		"paths the requester named",
		"standard observable identity",
		"process name, ports",
		"Disclose deviations",
	} {
		if !containsPromptClause(section, want) {
			t.Fatalf("tools section missing %q: %s", want, section)
		}
	}

	inspect := strings.Index(section, "Inspect relevant files before modifying them")
	preserve := strings.Index(section, "inspect and experiment on a copy")
	if inspect < 0 || preserve < 0 || inspect >= preserve {
		t.Fatalf("preserve-inputs rule out of place: want inspect(%d) < preserve(%d)", inspect, preserve)
	}
}

func TestVerificationSectionPreservesEveryAssertionPremise(t *testing.T) {
	t.Parallel()
	section := postureSectionResolver("coordinator").Section("verification", promptData{
		Provider: "openai",
		Agent:    "coordinator",
	})
	for _, want := range []string{
		"Never delete or weaken a failing assertion",
		"pairing",
		"indexing",
		"tolerance",
		"reference",
		"independent of your implementation",
		"reuse it for every property the requirement names",
	} {
		if !containsPromptClause(section, want) {
			t.Fatalf("verification section missing %q: %s", want, section)
		}
	}

	assertion := strings.Index(section, "Never delete or weaken a failing assertion")
	smoke := strings.Index(section, "Before you change production behavior")
	if assertion < 0 || smoke < 0 || assertion >= smoke {
		t.Fatalf("assertion-preservation rule out of place: want assertion(%d) < smoke(%d)", assertion, smoke)
	}
}

func TestWorkflowSectionBoundsRepairByFinding(t *testing.T) {
	t.Parallel()
	section := postureSectionResolver("coordinator").Section("workflow", promptData{
		Provider: "openai",
		Agent:    "coordinator",
	})
	for _, want := range []string{
		"repair is bounded by what the finding faults",
		"resolving an ambiguity in a subset",
		"not license to delete the superset",
	} {
		if !containsPromptClause(section, want) {
			t.Fatalf("workflow section missing %q: %s", want, section)
		}
	}
}

func TestAssembledPromptCarriesPromptClusterRules(t *testing.T) {
	t.Parallel()
	resolver := postureSectionResolver("coordinator")
	result, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", promptData{
		Provider:           "openai",
		Agent:              "coordinator",
		RolePromptOverride: mustWorkflowAgent(t, "coordinator").SystemPrompt,
		WorkingDir:         "/tmp/test",
		ResultToolName:     "communicate",
		IsGitRepo:          true,
		GitBranch:          "main",
	})
	if err != nil {
		t.Fatalf("render system prompt: %v", err)
	}
	for _, want := range []string{
		"inspect and experiment on a copy",
		"Never delete or weaken a failing assertion",
		"repair is bounded by what the finding faults",
	} {
		if !containsPromptClause(result, want) {
			t.Fatalf("assembled system prompt missing %q", want)
		}
	}
}

func containsPromptClause(prompt, clause string) bool {
	return strings.Contains(strings.Join(strings.Fields(prompt), " "), strings.Join(strings.Fields(clause), " "))
}

func TestAssembledPromptUsesNoRestrictedCampaignVocabulary(t *testing.T) {
	t.Parallel()
	resolver := postureSectionResolver("coordinator")
	result, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", promptData{
		Provider:           "openai",
		Agent:              "coordinator",
		RolePromptOverride: mustWorkflowAgent(t, "coordinator").SystemPrompt,
		WorkingDir:         "/tmp/test",
		ResultToolName:     "communicate",
	})
	if err != nil {
		t.Fatalf("render system prompt: %v", err)
	}
	lower := strings.ToLower(result)
	for _, banned := range []string{"grade", "scor", "benchmark", "evaluator", "evaluat"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("assembled system prompt contains restricted vocabulary %q", banned)
		}
	}
}
