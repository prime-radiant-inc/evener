package agent

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// This catches publication-time sampling: an input admitted during Alpha's
// provider request must not be credited to Alpha's durable communicate output.
func TestConversationProvenance_RequestFrontierExcludesLaterAdmission(t *testing.T) {
	t.Run("accepted", func(t *testing.T) { runConversationRequestRace(t, false) })
	t.Run("incorporated-before-old-response", func(t *testing.T) { runConversationRequestRace(t, true) })
}

func runConversationRequestRace(t *testing.T, incorporate bool) {
	t.Parallel()
	const alpha = "opaque-input-alpha-719"
	const beta = "opaque-input-beta-831"
	stateDir := t.TempDir()
	var sess *Session
	requests := 0
	sess = newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(stateDir), withSteps(
		func(req llm.Request) llm.Response {
			requests++
			if !conversationRequestContains(req, alpha) || conversationRequestContains(req, beta) {
				t.Fatal("first provider request did not contain only the original input")
			}
			if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
				ClientMutationID: "beta-mutation", Input: []appwire.InputItem{{Type: "text", Text: beta}},
			}); err != nil {
				t.Fatalf("admit correction during first provider request: %v", err)
			}
			if pending, ok := projectedClientMutation(sess, "beta-mutation"); !ok || pending.ExecutionState != "accepted" {
				t.Fatalf("correction was not actually admitted: %#v, %v", pending, ok)
			}
			if incorporate {
				messages := sess.drainSteering()
				if len(messages) != 1 {
					t.Fatalf("real steering queue=%+v", messages)
				}
				sess.consumeSteeringMessage(messages[0])
				found := false
				for _, entry := range readPersistedTranscriptEntries(t, stateDir, sess.ID()) {
					if entry.Turn.Kind == schema.TurnSteering && entry.Turn.ClientMutationID == "beta-mutation" {
						found = true
					}
				}
				if !found {
					t.Fatal("correction was not durably incorporated before old provider response")
				}
			}
			call := communicateCallArgs("alpha-output", map[string]any{"message": "opaque-output-alpha-719", "end_turn": false})
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
		},
		func(req llm.Request) llm.Response {
			requests++
			if !conversationRequestContains(req, beta) {
				t.Fatal("second provider request did not consume the correction")
			}
			if _, ok := projectedClientMutation(sess, "beta-mutation"); ok {
				t.Fatal("correction remained pending after real steering append")
			}
			call := communicateCall("beta-output", "opaque-output-beta-831")
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
		},
	))
	if _, err := sess.ProcessInput(context.Background(), alpha, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("provider requests = %d, want 2", requests)
	}
	entries := readPersistedTranscriptEntries(t, stateDir, sess.ID())
	outputs := 0
	frontiers := map[string]map[string]bool{"": {}}
	var alphaID string
	var alphaRequest string
	for _, entry := range entries {
		if entry.Turn.Kind == schema.TurnUserInput {
			if len(entry.Turn.ConversationInputIDs) != 1 {
				t.Fatal("direct input lacks exactly one stable identity")
			}
			alphaID = entry.Turn.ConversationInputIDs[0]
		}
		if node := entry.Turn.ConversationFrontier; node != nil {
			base, ok := frontiers[node.BaseID]
			if !ok {
				t.Fatal("frontier is not backward-resolvable")
			}
			set := maps.Clone(base)
			for _, id := range node.AddedInputs {
				set[id] = true
			}
			for _, id := range node.RemovedInputs {
				delete(set, id)
			}
			frontiers[node.ID] = set
		}
		if entry.Turn.Kind != schema.TurnCommunicate {
			continue
		}
		outputs++
		raw, err := json.Marshal(entry.Turn)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields["conversation_provenance"]) == 0 {
			t.Errorf("durably committed output %q has no originating request provenance after correction admission", entry.Turn.Communicate.CallID)
		}
		p := entry.Turn.ConversationProvenance
		if p == nil {
			continue
		}
		if p.Version != 1 || p.InstanceID != sess.ID() || p.RunGeneration == 0 || p.RequestID == "" {
			t.Fatalf("incomplete output identity: %+v", p)
		}
		coverage, ok := frontiers[p.FrontierID]
		if !ok || !coverage[alphaID] {
			t.Fatalf("output lost its original input: %+v", coverage)
		}
		if entry.Turn.Communicate.CallID == "alpha-output" {
			alphaRequest = p.RequestID
			if len(coverage) != 1 || coverage["client:beta-mutation"] {
				t.Fatalf("older output credited newer input: %+v", coverage)
			}
		} else if len(coverage) != 2 || !coverage["client:beta-mutation"] || p.RequestID == alphaRequest {
			t.Fatalf("new output does not identify its distinct actual request: %+v, %+v", p, coverage)
		}
	}
	if outputs != 2 {
		t.Fatalf("durable communicate outputs = %d, want 2", outputs)
	}
}

func conversationRequestContains(req llm.Request, sentinel string) bool {
	for _, message := range req.Messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), sentinel) {
			return true
		}
	}
	return false
}

type conversationRetryAdapter struct {
	*fakeAdapter
	complete func(llm.Request) (llm.Response, error)
}

func (a *conversationRetryAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	return a.complete(req)
}

func TestConversationProvenance_RetryUsesActualRequest(t *testing.T) {
	t.Parallel()
	cfg := minimalTestSessionConfig(t)
	cfg.LLMRetryPolicy = &llm.RetryPolicy{MaxRetries: 1}
	cfg.LLMSleep = func(context.Context, time.Duration) error { return nil }
	s := newSession(t, withConfig(cfg), withStateDir(t.TempDir()))
	calls := 0
	s.client.Register(&conversationRetryAdapter{fakeAdapter: &fakeAdapter{name: "openai"}, complete: func(req llm.Request) (llm.Response, error) {
		calls++
		if !conversationRequestContains(req, "retry-original") || conversationRequestContains(req, "retry-newer") {
			t.Fatal("retry unexpectedly rebuilt its actual request")
		}
		if calls == 1 {
			if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "retry-newer", Input: []appwire.InputItem{{Type: "text", Text: "retry-newer"}}}); err != nil {
				t.Fatal(err)
			}
			messages := s.drainSteering()
			if len(messages) != 1 {
				t.Fatal("correction not admitted before retry")
			}
			s.consumeSteeringMessage(messages[0])
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 503, "retry", nil, nil)
		}
		call := communicateCall("retried-output", "retried result")
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}, nil
	}})
	if _, err := s.ProcessInput(context.Background(), "retry-original", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("actual provider attempts=%d, want 2", calls)
	}
	state := s.ConversationProvenance()
	outputs := 0
	for _, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
		if entry.Turn.Kind != schema.TurnCommunicate {
			continue
		}
		outputs++
		p := entry.Turn.ConversationProvenance
		if p == nil {
			t.Fatal("retry output missing provenance")
		}
		ids, ok := appwire.ResolveConversationFrontier(state, p.FrontierID)
		if !ok || len(ids) != 1 || slices.Contains(ids, "client:retry-newer") {
			t.Fatalf("retry credited newer incorporated input absent from its actual request: %v", ids)
		}
	}
	if outputs != 1 {
		t.Fatalf("durable retried outputs=%d, want 1", outputs)
	}
}
