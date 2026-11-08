package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestConversationProvenance_RestoreContinuesCanonicalLineage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	response := func(req llm.Request) llm.Response {
		call := communicateCall("first-output", "first result")
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
	}
	first := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(dir), withSteps(response))
	if _, err := first.ProcessInput(context.Background(), "restore-original-713", nil); err != nil {
		t.Fatal(err)
	}
	entries := readPersistedTranscriptEntries(t, dir, first.ID())
	var original *schema.ConversationOutputProvenance
	for _, entry := range entries {
		if entry.Turn.Kind == schema.TurnCommunicate {
			original = entry.Turn.ConversationProvenance
		}
	}
	if original == nil {
		t.Fatal("original durable output has no provenance")
	}
	first.Close()
	meta, err := schema.LoadSessionMeta(dir, first.ID())
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		if !conversationRequestContains(req, "restore-original-713") || !conversationRequestContains(req, "restore-new-923") {
			t.Fatal("restore request lost input history")
		}
		call := communicateCall("restored-output", "restored result")
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
	}}})
	restored, err := RestoreSessionFromMetaWithConfig(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.ProcessInput(context.Background(), "restore-new-923", nil); err != nil {
		t.Fatal(err)
	}
	entries = readPersistedTranscriptEntries(t, dir, first.ID())
	found := false
	for _, entry := range entries {
		if p := entry.Turn.ConversationProvenance; entry.Turn.Kind == schema.TurnAssistant && p != nil && p.RequestID != original.RequestID {
			found = true
			if p.RunGeneration <= original.RunGeneration {
				t.Errorf("restore reused run generation: old=%d new=%d", original.RunGeneration, p.RunGeneration)
			}
			node := entry.Turn.ConversationFrontier
			if node == nil || node.BaseID != original.FrontierID || len(node.AddedInputs) != 1 {
				t.Errorf("restore rewrote cumulative frontier instead of extending canonical lineage: %+v", node)
			}
		}
	}
	if !found {
		t.Fatal("restored provider output was not durably committed")
	}
}

func TestConversationProvenance_InvalidRestoreIsUnknown(t *testing.T) {
	for _, node := range []*schema.ConversationFrontier{
		{ID: "f", BaseID: "missing"},
		{ID: "f", BaseID: "f"},
		{ID: "f", RemovedInputs: []string{"absent"}},
		{ID: "f", AddedInputs: []string{"a", "a"}},
	} {
		s := &Session{id: "owner"}
		s.restoreConversation([]transcript.Entry{{Turn: schema.Turn{Kind: schema.TurnAssistant, ConversationFrontier: node, ConversationProvenance: &schema.ConversationOutputProvenance{Version: 1, InstanceID: "owner", RunGeneration: 1, RequestID: "req", FrontierID: "f"}}}})
		if state := s.ConversationProvenance(); state.Authoritative {
			t.Errorf("corrupt canonical lineage restored as authoritative: %+v", node)
		}
	}
}

func TestConversationProvenance_EffectiveFoldCoverage(t *testing.T) {
	valid := schema.Turn{Kind: schema.TurnSummary, ConversationCoverageRef: "summary", ConversationFrontier: &schema.ConversationFrontier{ID: "summary", AddedInputs: []string{"input"}}}
	t.Run("superseded intermediate marker", func(t *testing.T) {
		s := &Session{id: "owner", history: []schema.Turn{valid}}
		s.restoreConversation([]transcript.Entry{{Turn: schema.Turn{Kind: schema.TurnCheckpoint}}, {Turn: valid}})
		s.stageConversationInputs(s.history)
		if state := s.ConversationProvenance(); !state.Authoritative || len(s.conversation.preparedInputs) != 1 {
			t.Fatalf("superseded checkpoint poisoned valid effective summary: %+v", state)
		}
	})
	for _, ref := range []string{"", "missing"} {
		t.Run("effective ref="+ref, func(t *testing.T) {
			s := &Session{id: "owner", history: []schema.Turn{{Kind: schema.TurnSummary, ConversationCoverageRef: ref}}}
			s.restoreConversation([]transcript.Entry{{Turn: valid}})
			s.stageConversationInputs(s.history)
			if state := s.ConversationProvenance(); state.Authoritative {
				t.Fatalf("effective summary with missing coverage synthesized known-empty: %+v", state)
			}
		})
	}
}
