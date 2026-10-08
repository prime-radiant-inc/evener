package contextmgr

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// summarizeReplying runs one summarization whose model replies with reply and
// returns the summarizer's result.
func summarizeReplying(t *testing.T, reply, instructions string) ([]schema.Turn, error) {
	t.Helper()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(reply)} },
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("I'll fix it")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}
	return cm.summarizeWithLLMSteered(context.Background(), history, 1, instructions)
}

// A reply that is not a summary would be stored as the session's only record of
// the folded history, so it fails like a summarizer error and the deterministic
// checkpoint stands instead.
func TestSummarizeWithLLM_RejectsNonSummaryReplies(t *testing.T) {
	for name, reply := range map[string]string{
		"empty":      "",
		"whitespace": " \n\t",
		"narration":  "I'll read the plan to get the exact Task 3 and Task 4 code, then check current state.",
		// A recognized heading with nothing under it carries no state.
		"heading only":      "## Progress",
		"headings only":     "## Progress\n\n## Pending Work\n",
		"heading then none": "**Current State**\n   \n",
		// A section name without heading markup is prose, not a section.
		"bare section name": "Progress\nI'll read the plan next.",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := summarizeReplying(t, reply, "")
			if err == nil {
				t.Fatalf("summarizeWithLLM stored %q as a summary, want an error", result[0].Message.Text())
			}
		})
	}
}

func TestSummarizeWithLLM_StoresReplyWithRequiredSection(t *testing.T) {
	for name, reply := range map[string]string{
		"all sections": "## Progress\nFixed the auth bug.\n\n## Pending Work\nRun the tests.",
		"one section":  "## Current State\nEditing auth.go.",
		"bold heading": "**Progress**\nFixed the auth bug.",
		"lowercase h3": "### pending work\nRun the tests.",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := summarizeReplying(t, reply, "")
			if err != nil {
				t.Fatalf("summarizeWithLLM: %v", err)
			}
			if got := result[0].Message.Text(); !strings.Contains(got, reply) {
				t.Fatalf("summary = %q, want it to hold the reply", got)
			}
		})
	}
}

// Caller instructions replace the required sections, so only an empty reply is
// rejected under them.
func TestSummarizeWithLLM_InstructionsRequireOnlyANonEmptyReply(t *testing.T) {
	if _, err := summarizeReplying(t, "Kept: the auth fix and its failing test.", "keep only the auth work"); err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	if _, err := summarizeReplying(t, "", "keep only the auth work"); err == nil {
		t.Fatal("summarizeWithLLM stored an empty reply under instructions, want an error")
	}
}

func TestSummaryPromptSectionsComeFromThePrompt(t *testing.T) {
	want := []string{"Conversation Timeline", "Progress", "Key Decisions", "Current State", "Pending Work", "Analytical Findings", "Critical Context"}
	if strings.Join(summarySections, "|") != strings.Join(want, "|") {
		t.Fatalf("summarySections = %q, want %q", summarySections, want)
	}
}
