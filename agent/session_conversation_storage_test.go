package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestConversationProvenance_StorageGrowthIsLinear(t *testing.T) {
	t.Parallel()
	var steps []func(llm.Request) llm.Response
	for i := range 64 {
		steps = append(steps, func(llm.Request) llm.Response {
			call := communicateCallArgs(fmt.Sprintf("growth-output-%d", i), map[string]any{"message": "progress", "end_turn": i%2 == 1})
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
		})
	}
	dir := t.TempDir()
	s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(dir), withSteps(steps...))
	var halfBytes int
	for i := range 32 {
		if _, err := s.ProcessInput(context.Background(), fmt.Sprintf("independent input %d", i), nil); err != nil {
			t.Fatal(err)
		}
		if i == 15 {
			raw, err := json.Marshal(s.ConversationProvenance().Frontiers)
			if err != nil {
				t.Fatal(err)
			}
			halfBytes = len(raw)
		}
	}
	state := s.ConversationProvenance()
	full, err := json.Marshal(state.Frontiers)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Authoritative || len(state.Frontiers) != 32 || len(full) > halfBytes*3 {
		t.Fatalf("frontier growth is not linear: nodes=%d half=%d full=%d authority=%v", len(state.Frontiers), halfBytes, len(full), state.Authoritative)
	}
	added := 0
	for _, node := range state.Frontiers {
		added += len(node.AddedInputs)
		if len(node.RemovedInputs) != 0 {
			t.Fatal("unchanged progress removed coverage")
		}
	}
	if added != 32 {
		t.Fatalf("input identities duplicated cumulatively: %d", added)
	}
	entries := readPersistedTranscriptEntries(t, dir, s.ID())
	outputs := 0
	nodes := 0
	for _, entry := range entries {
		if entry.Turn.ConversationFrontier != nil {
			nodes++
		}
		if entry.Turn.Kind == schema.TurnCommunicate {
			outputs++
			if entry.Turn.ConversationFrontier != nil {
				t.Fatal("output duplicated canonical frontier")
			}
		}
	}
	if outputs != 64 || nodes != 32 {
		t.Fatalf("actual durable outputs=%d nodes=%d", outputs, nodes)
	}
	t.Logf("32 inputs, 64 provider outputs, 32 canonical delta nodes, 32 total added identities; graph bytes 16 inputs=%d, 32 inputs=%d", halfBytes, len(full))
}
