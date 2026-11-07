package server

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// The existing raw-record hook parks a real assistant append before Session
// adopts its frontier. Reads must not expose that entry with an older graph,
// even when another index handle has already consumed the raw bytes.
func TestThreadReadProvenanceAtRawRecordBoundary(t *testing.T) {
	for _, catchUp := range []bool{false, true} {
		name := "direct"
		if catchUp {
			name = "shared-index-caught-up"
		}
		t.Run(name, func(t *testing.T) {
			const alpha, beta, askID = "raw-alpha-719", "raw-beta-831", "raw-beta-ask"
			alphaArgs, err := json.Marshal(map[string]any{"message": alpha, "end_turn": true, "output": map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}}})
			if err != nil {
				t.Fatal(err)
			}
			askArgs := json.RawMessage(`{"questions":[{"header":"raw-831","question":"raw-question-831","options":[{"label":"raw-a","detail":"raw-detail-a"},{"label":"raw-b","detail":"raw-detail-b"}]}]}`)
			var requests atomic.Int32
			observed := make(chan bool, 2)
			client := llm.NewClient()
			client.Register(&readFrontierProvider{complete: func(req llm.Request) llm.Response {
				n := requests.Add(1)
				var input strings.Builder
				for _, message := range req.Messages {
					if message.Role == llm.RoleUser {
						input.WriteString(message.Text())
					}
				}
				observed <- strings.Contains(input.String(), alpha) && strings.Contains(input.String(), beta) == (n == 2)
				call := llm.ToolCallData{ID: alpha, Name: "communicate", Arguments: alphaArgs, Type: "function"}
				if n == 2 {
					call = llm.ToolCallData{ID: askID, Name: "ask_user", Arguments: askArgs, Type: "function"}
				}
				return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
			}})
			dir := t.TempDir()
			sess, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sess.Close)
			if err := sess.Rename("raw-frontier-fixture"); err != nil {
				t.Fatal(err)
			}
			srv := NewServer(ServerConfig{})
			t.Cleanup(srv.Close)
			wireTranscriptHistory(t, sess, srv)
			source := &conversationEnvelopeSource{sample: sess.ConversationProvenance}
			srv.SetThreadEnvelopeSource(source)
			read := func() appwire.ThreadReadResponse {
				t.Helper()
				response, err := srv.handleAppThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + sess.ID(), IncludeTurns: true, Subscribe: true})
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			if _, err := sess.ProcessInput(t.Context(), alpha, nil); err != nil {
				t.Fatal(err)
			}
			if !<-observed {
				t.Fatal("first actual request has incorrect input coverage")
			}
			srv.RefreshThreadEnvelope()
			read() // Warm the real index before parking an append under its lock.

			parked := make(chan transcript.Record, 1)
			release := make(chan struct{})
			unpark := sync.OnceFunc(func() { close(release) })
			threadID := sess.ID()
			sess.SetTranscriptRecordedFunc(func(rec transcript.Record) {
				srv.recordTranscriptEntry(threadID, rec)
				if rec.Turn.Kind == schema.TurnAssistant && rec.Turn.ConversationFrontier != nil {
					parked <- rec
					<-release
				}
			})
			finished := make(chan struct{})
			var processErr error
			go func() {
				defer close(finished)
				_, processErr = sess.ProcessInput(t.Context(), beta, nil)
			}()
			t.Cleanup(func() {
				unpark()
				<-finished
			})
			var raw transcript.Record
			select {
			case raw = <-parked:
			case <-finished:
				t.Fatalf("second request did not reach raw assistant record: %v", processErr)
			}
			if requests.Load() != 2 || !<-observed || !raw.Recorded || raw.Turn.ConversationProvenance == nil {
				t.Fatal("raw-record scenario did not consume Beta through the actual provider request")
			}
			// Refresh outside the read while the raw hook is parked. Even this
			// latest owner sample cannot contain the not-yet-adopted frontier.
			srv.RefreshThreadEnvelope()
			owner := sess.ConversationProvenance()
			for _, node := range owner.Frontiers {
				if node.ID == raw.Turn.ConversationFrontier.ID {
					t.Fatal("raw-record hook did not park before Session frontier adoption")
				}
			}
			t.Logf("Beta actual request recorded before Session adoption: request=%s frontier=%s", raw.Turn.ConversationProvenance.RequestID, raw.Turn.ConversationFrontier.ID)
			if catchUp {
				h := srv.appHistories.get(threadID)
				idx, err := h.cache.Acquire(h.path)
				if err != nil {
					t.Fatal(err)
				}
				err = idx.CatchUpTo(raw.Offset + raw.Length)
				h.cache.Release(idx)
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := source.calls.Load()
			during := read()
			if source.calls.Load() != calls {
				t.Error("read sampled the live provenance owner")
			}
			assertReadFrontiersResolve(t, during)
			if during.Snapshot == nil || during.Snapshot.Length > raw.Offset {
				t.Errorf("read overtook the producer publication boundary: snapshot=%+v rawOffset=%d", during.Snapshot, raw.Offset)
			}

			unpark()
			<-finished
			if processErr != nil {
				t.Fatal(processErr)
			}
			srv.RefreshThreadEnvelope()
			after := read()
			assertReadFrontiersResolve(t, after)
			found := 0
			for _, turn := range after.Thread.Turns {
				for _, item := range turn.Items {
					if item.CallID == askID {
						found++
						if item.ConversationProvenance == nil || item.ConversationProvenance.RequestID != raw.Turn.ConversationProvenance.RequestID || item.ConversationProvenance.FrontierID != raw.Turn.ConversationFrontier.ID {
							t.Errorf("adopted ask lost its exact originating request: %+v", item.ConversationProvenance)
						}
					}
				}
			}
			if found != 1 {
				t.Errorf("adopted Beta ask appears %d times, want exactly once", found)
			}
		})
	}
}

func assertReadFrontiersResolve(t *testing.T, read appwire.ThreadReadResponse) {
	t.Helper()
	state := read.Thread.Evener.ConversationProvenance
	if state == nil || !state.Authoritative {
		t.Fatal("read lost authoritative producer provenance")
	}
	nodes := make(map[string]appwire.ConversationFrontier, len(state.Frontiers))
	for _, node := range state.Frontiers {
		nodes[node.ID] = node
	}
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if item.ConversationProvenance == nil {
				continue
			}
			seen := make(map[string]bool)
			for id := item.ConversationProvenance.FrontierID; id != ""; {
				node, ok := nodes[id]
				if !ok || seen[id] {
					t.Errorf("canonical item %s has an unresolvable actual frontier %s", item.ID, id)
					break
				}
				seen[id] = true
				id = node.BaseID
			}
		}
	}
}
