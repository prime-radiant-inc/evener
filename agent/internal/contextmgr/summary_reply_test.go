package contextmgr

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/provider"
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
		// The summary's own framing markers are not content.
		"end marker only":    "## Progress\n[END SUMMARY]",
		"framed and empty":   "[CONTEXT SUMMARY]\n## Progress\n[END SUMMARY]",
		"other heading only": "## Notes\nFixed the auth bug.",
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
		// Subheadings, bold labels and lines that start with # inside a
		// section are its content, not the end of it.
		"subheading":         "## Progress\n### Files changed\n- auth.go",
		"bold label":         "## Progress\n**Files Modified**\n- auth.go",
		"issue reference":    "## Progress\n#3978 is fixed.",
		"same-line content":  "## Progress: fixed the auth bug",
		"bold colon content": "**Progress:** fixed the auth bug",
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

// A configured cheap model whose reply is not a summary falls through to the
// session model, like a cheap model that fails outright.
func TestSummarizeWithLLM_RejectedCheapReplyFallsBackToSessionModel(t *testing.T) {
	for name, tc := range map[string]struct {
		sessionReply string
		wantStored   bool
	}{
		"session model summarizes": {"## Progress\nFixed the auth bug.", true},
		"session model narrates":   {"I'll check the task list first.", false},
	} {
		t.Run(name, func(t *testing.T) {
			adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("I'll read the plan, then check current state.")}
				},
				func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(tc.sessionReply)} },
			}}
			client := llm.NewClient()
			client.Register(adapter)
			profile := provider.WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-5-mini")
			cm := NewManager(profile, client, cheapmodel.New(client))
			history := []schema.Turn{
				{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")},
				{Kind: schema.TurnAssistant, Message: llm.Assistant("I'll fix it")},
				{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
			}

			result, err := cm.summarizeWithLLM(context.Background(), history, 1)

			var models []string
			for _, r := range adapter.Requests() {
				models = append(models, r.Model)
			}
			if strings.Join(models, " ") != "gpt-5-mini gpt-5.2" {
				t.Fatalf("summary models = %q, want [gpt-5-mini gpt-5.2]", models)
			}
			if !tc.wantStored {
				if err == nil {
					t.Fatalf("stored %q, want an error", result[0].Message.Text())
				}
				return
			}
			if err != nil {
				t.Fatalf("summarizeWithLLM: %v", err)
			}
			if got := result[0].Message.Text(); !strings.Contains(got, tc.sessionReply) {
				t.Fatalf("summary = %q, want the session model's reply", got)
			}
		})
	}
}

func TestSummaryPromptSectionsComeFromThePrompt(t *testing.T) {
	want := []string{"Conversation Timeline", "Progress", "Key Decisions", "Current State", "Pending Work", "Analytical Findings", "Critical Context"}
	if strings.Join(summarySections, "|") != strings.Join(want, "|") {
		t.Fatalf("summarySections = %q, want %q", summarySections, want)
	}
}
