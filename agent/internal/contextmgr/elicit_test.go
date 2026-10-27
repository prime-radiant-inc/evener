package contextmgr

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestElicitNote_ListsLoadedSkills proves the typed loaded-skill metadata from
// successful session inventory crosses the real provider wire: the elicitation
// request carries each skill's canonical name and description, plus the
// selection-block protocol token, so the model can answer with one
// <skill-reload-selection> block.
func TestElicitNote_ListsLoadedSkills(t *testing.T) {
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			prompt := req.Messages[0].Text()
			if !strings.Contains(prompt, "scope:probe") || !strings.Contains(prompt, "opaque-probe-description") {
				t.Errorf("elicitation prompt missing the loaded skill metadata")
			}
			if !strings.Contains(prompt, "skill-reload-selection") {
				t.Errorf("elicitation prompt missing the selection-block protocol token")
			}
			return llm.Response{Message: llm.Assistant("- keep it")}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	got, err := cm.ElicitNote(context.Background(),
		[]schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("work in progress")}},
		[]schema.SkillInventorySummary{{Name: "scope:probe", Description: "opaque-probe-description", Availability: "ordinary", HasOrdinary: true}},
	)
	if err != nil {
		t.Fatalf("ElicitNote: %v", err)
	}
	if got != "- keep it" {
		t.Fatalf("note = %q, want the trimmed bullet list", got)
	}
}

// TestElicitNote_NoLoadedSkillsOmitsSelectionProtocol: with no successfully
// loaded skills the prompt must not request a structured selection — there is
// nothing to select and no reload any block could authorize.
func TestElicitNote_NoLoadedSkillsOmitsSelectionProtocol(t *testing.T) {
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			if strings.Contains(req.Messages[0].Text(), "skill-reload-selection") {
				t.Errorf("elicitation prompt requested a selection with no loaded skills")
			}
			return llm.Response{Message: llm.Assistant("- keep it")}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	if _, err := cm.ElicitNote(context.Background(), nil, nil); err != nil {
		t.Fatalf("ElicitNote: %v", err)
	}
}
