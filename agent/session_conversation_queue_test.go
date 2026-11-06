package agent

import (
	"context"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestConversationProvenance_PromotionPreservesOutOfOrderInputIdentity(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	requests := 0
	var sess *Session
	var queueID string
	response := func(id string, terminal bool) llm.Response {
		call := communicateCallArgs(id, map[string]any{"message": "opaque-output-" + id, "end_turn": terminal})
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
	}
	sess = newSession(t, withStateDir(stateDir), withConfig(minimalTestSessionConfig(t)), withSteps(
		func(req llm.Request) llm.Response {
			requests++
			queued, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "original-queue", Input: []appwire.InputItem{{Type: "text", Text: "opaque-queued-239"}}})
			if err != nil {
				t.Fatal(err)
			}
			queueID = queued.Receipt.QueueEntryIDs[0]
			if _, err = sess.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "later-steer", Input: []appwire.InputItem{{Type: "text", Text: "opaque-steered-391"}}}); err != nil {
				t.Fatal(err)
			}
			return response("initial-output", false)
		},
		func(req llm.Request) llm.Response {
			requests++
			if conversationRequestContains(req, "opaque-queued-239") || !conversationRequestContains(req, "opaque-steered-391") {
				t.Fatal("provider did not observe out-of-order consumption")
			}
			if _, err := sess.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "promotion-control", Index: 0, ExpectedEntryID: queueID}); err != nil {
				t.Fatal(err)
			}
			return response("steered-output", false)
		},
		func(req llm.Request) llm.Response {
			requests++
			if !conversationRequestContains(req, "opaque-queued-239") {
				t.Fatal("promoted original input did not reach provider")
			}
			return response("promoted-output", true)
		},
	))
	if _, err := sess.ProcessInput(context.Background(), "opaque-start-251", nil); err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Fatalf("requests=%d", requests)
	}
	entries := readPersistedTranscriptEntries(t, stateDir, sess.ID())
	var promoted bool
	for _, entry := range entries {
		if entry.Turn.Kind == schema.TurnSteering && entry.Turn.ClientMutationID == "promotion-control" {
			promoted = true
			if !slices.Equal(entry.Turn.ConversationInputIDs, []string{"client:original-queue"}) {
				t.Fatalf("promotion replaced original input identity: %v", entry.Turn.ConversationInputIDs)
			}
		}
	}
	if !promoted {
		t.Fatal("promotion did not produce a durable steering entry")
	}
}
