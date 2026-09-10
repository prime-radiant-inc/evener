package server

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Compaction is requested through the real tool and runs before the round's
// timing record. The fake boundary supplies model responses only.
func TestRoundTimingsAfterSelfCompactionRetainLiveReplayIdentity(t *testing.T) {
	dir := t.TempDir()
	adapter := &compactionTimingAdapter{}
	client := llm.NewClient()
	client.Register(adapter)
	profile := provider.WithCheapModel(provider.NewOpenAIProfile("gpt-5.2"), "openai/fixture-summary")
	sess, err := agent.NewSession(client, profile, execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	sess.SetClientMutationStartWakeFunc(func() {})
	done := make(chan struct{}, 1)
	var mu sync.Mutex
	var live *Server
	go sess.ConsumeEventsLossless(func(event events.SessionEvent) {
		mu.Lock()
		current := live
		mu.Unlock()
		if current != nil {
			current.RecordAppEvent(event)
		}
		if event.Kind == events.EventSessionEnd {
			done <- struct{}{}
		}
	}, func() {})
	run := func(id string) string {
		t.Helper()
		accepted, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: id, ExpectedInstanceID: sess.ID(), Input: []appwire.InputItem{{Type: "text", Text: id}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := sess.ProcessClientMutationStart(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		<-done
		return accepted.Turn.ID
	}
	// Enough ordinary persisted records for a real fold with the default
	// preserved tail; the assertions below require a durable compaction marker.
	for i := 0; i < 6; i++ {
		run(fmt.Sprintf("seed-%d", i))
	}
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, sess.ID(), sess.TranscriptPath())
	mu.Lock()
	live = srv
	mu.Unlock()
	owner := run("compact-final-round")
	writer, entries, err := transcript.OpenWriterForSession(sess.TranscriptPath(), sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	marker, timing := -1, -1
	for i, entry := range entries {
		if entry.Turn.Kind == schema.TurnCheckpoint || entry.Turn.Kind == schema.TurnSummary {
			marker = i
		}
		if entry.Turn.Kind == schema.TurnRoundTimings {
			timing = i
		}
	}
	if marker < 0 || timing <= marker {
		t.Fatalf("expected real compaction before final timing, marker=%d timing=%d", marker, timing)
	}
	read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sess.ID(), IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	cold, _, err := appTurnsFromTranscriptFile(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	latest := func(turns []appwire.Turn) replayItemIdentity {
		t.Helper()
		var found []replayItemIdentity
		for _, item := range replayItemIdentities(turns) {
			if item.EventKind == appwire.ThreadItemEventKindRoundTimings {
				found = append(found, item)
			}
		}
		if len(found) == 0 {
			t.Fatal("missing completed round timing")
		}
		return found[len(found)-1]
	}
	want, got := latest(read.Thread.Turns), latest(cold)
	if want.TurnID != owner {
		t.Fatalf("live timing owner=%s, want active turn %s", want.TurnID, owner)
	}
	if !reflect.DeepEqual(got, want) {
		for i, entry := range entries {
			t.Logf("saved %d kind=%s owner=%s stable=%s", i, entry.Turn.Kind, entry.Turn.OwningTurnID, entry.Turn.StableTurnID)
		}
		for _, item := range replayItemIdentities(read.Thread.Turns) {
			if item.Position.Entry >= 7 {
				t.Logf("live %s %s %s %s", item.TurnID, item.Key, item.EventKind, item.Description)
			}
		}
		for _, item := range replayItemIdentities(cold) {
			if item.Position.Entry >= 7 {
				t.Logf("cold %s %s %s %s", item.TurnID, item.Key, item.EventKind, item.Description)
			}
		}
		t.Fatalf("compaction timing replay changed identity:\nlive=%#v\ncold=%#v", want, got)
	}
	initial, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sess.ID(), IncludeTurns: true, ItemLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	paged := initial.Thread.Turns
	cursor := initial.OlderCursor
	for cursor != "" {
		page, err := srv.handleAppThreadTurnsList(context.Background(), appwire.ThreadTurnsListParams{Ref: "local:" + sess.ID(), Cursor: cursor, ItemLimit: 1})
		if err != nil {
			t.Fatal(err)
		}
		paged = append(page.Data, paged...)
		cursor = page.NextCursor
	}
	if got := latest(paged); !reflect.DeepEqual(got, want) {
		t.Fatalf("bounded compaction timing changed identity:\nlive=%#v\npaged=%#v", want, got)
	}
}

type compactionTimingAdapter struct{ calls int }

func (*compactionTimingAdapter) Name() string { return "openai" }
func (a *compactionTimingAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	if req.Model == "fixture-summary" || !requestHasTool(req, "communicate") {
		return llm.Response{Provider: a.Name(), Model: req.Model, Message: llm.Assistant("[CONTEXT SUMMARY]\nSaved fixture work.\n[END SUMMARY]")}, nil
	}
	a.calls++
	response := steeringOwnerCommunicateResponse(true, a.calls)
	response.Provider = a.Name()
	response.Model = req.Model
	if a.calls == 7 {
		args, _ := json.Marshal(map[string]string{"note_to_self": "retained fixture state", "compaction_instructions": "retain the fixture work"})
		compact := llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "compact-fixture", Name: "compact_context", Arguments: args, Type: "function"}}
		response.Message.Content = append([]llm.ContentPart{compact}, response.Message.Content...)
	}
	return response, nil
}
func (*compactionTimingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
