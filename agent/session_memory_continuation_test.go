package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The provider is scripted only at the LLM boundary. Index reads, memory turns,
// anchor selection and request construction all run through the real session.
func TestMemoryContextContinuationRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		boundary  schema.TurnKind
		newAnchor bool
		wantDelta bool
	}{
		{name: "active_anchor", wantDelta: true},
		{name: "checkpoint_without_new_anchor", boundary: schema.TurnCheckpoint},
		{name: "summary_without_new_anchor", boundary: schema.TurnSummary},
		{name: "checkpoint_with_new_anchor", boundary: schema.TurnCheckpoint, newAnchor: true, wantDelta: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, dir := t.TempDir(), t.TempDir()
			for relative, body := range map[string]string{
				"memory/personal/MEMORY.md":                 "opaque-memory-personal-3730",
				"memory/projects/fixture-project/MEMORY.md": "opaque-memory-project-3730",
			} {
				path := filepath.Join(root, relative)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			adapter := &agenttest.FakeAdapter{
				Provider: "openai",
				PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
					return phase4DIContinuationPlan(req), nil
				},
				Steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return agenttest.FinalResponse("done") }},
			}
			client := llm.NewClient()
			client.Register(adapter)
			sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.4")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
				StateDir:                    dir,
				MemoryStateRoot:             root,
				MemoryProjectID:             "fixture-project",
				OpenAIResponsesContinuation: "auto",
				// The boundary waits for index reads only within a budget on the
				// session clock; a read that misses it is projected unavailable.
				// A fake clock never advances, so a slow read under -race still
				// lands in the request.
				clock: agenttest.NewFakeClock(),
				testOnly: testConfig{responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
					llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer sess.Close()
			drainSessionEvents(sess)
			anchor := phase9MatchingAnchor("resp_memory_3730")
			anchor.Timestamp = time.Now()
			sess.history = append(sess.history, schema.NewTurn(schema.TurnUserInput, llm.User("opaque-pre-anchor-3730")), anchor)
			if tc.boundary != "" {
				sess.history = append(sess.history, schema.NewTurn(tc.boundary, llm.User("opaque-context-boundary-3730")))
			}
			if tc.newAnchor {
				sess.history = append(sess.history, anchor)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process provider, only fires on a genuine hang.
			defer cancel()
			if _, err := sess.ProcessInput(ctx, "opaque-current-input-3730", nil); err != nil {
				t.Fatal(err)
			}
			requests := adapter.Requests()
			if len(requests) != 1 {
				t.Fatalf("requests=%d, want 1", len(requests))
			}
			req := requests[0]
			if tc.wantDelta {
				if req.HistoryMode != llm.HistoryModeResponsesDelta || req.PreviousResponseID != "resp_memory_3730" {
					t.Fatalf("mode=%s previous_response_id=%q, want memory delta", req.HistoryMode, req.PreviousResponseID)
				}
				for _, msg := range req.Messages {
					if strings.Contains(msg.Text(), "opaque-pre-anchor-3730") || strings.Contains(msg.Text(), "opaque-context-boundary-3730") {
						t.Fatal("delta replayed pre-anchor history")
					}
				}
			} else {
				if req.HistoryMode != llm.HistoryModeFullHistory || req.PreviousResponseID != "" {
					t.Fatalf("mode=%s previous_response_id=%q, want full history without new anchor", req.HistoryMode, req.PreviousResponseID)
				}
				if !requestMessagesContainText(req.Messages, "opaque-context-boundary-3730") {
					t.Fatal("full history lost its context boundary")
				}
			}
			for name, sentinel := range map[string]string{"memory_personal": "opaque-memory-personal-3730", "memory_project": "opaque-memory-project-3730"} {
				count := 0
				for _, msg := range req.Messages {
					if msg.Name == name && strings.Contains(msg.Text(), sentinel) {
						if msg.Role != llm.RoleUser {
							t.Fatalf("memory role=%s, want user", msg.Role)
						}
						count++
					}
				}
				if count != 1 {
					t.Fatalf("request carries %d %s projections, want 1", count, name)
				}
			}
			if !requestMessagesContainText(req.Messages, "opaque-current-input-3730") {
				t.Fatal("request lost current input")
			}
		})
	}
}
