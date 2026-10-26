package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func TestManagerWatchAnswerContinuationReplay(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	jm := root.jobManager
	const ref, instance, generation = "local:worker", "instance", "7@worker"
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	scope := appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation}
	if err := jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Metadata: scope, Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance, ActiveTurnID: "A"}}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "worker.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	appendEntry := func(e schema.Turn) {
		t.Helper()
		e.Format = schema.TurnFormatIdentity
		if err := w.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []struct {
		id    string
		links []string
	}{{"A", nil}, {"B", []string{"A"}}, {"C", []string{"B"}}} {
		appendEntry(schema.Turn{TurnID: v.id, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User(v.id), AnswerToTurnIDs: v.links})
	}
	done := func(id string) {
		appendEntry(schema.Turn{TurnID: id, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnCommunicate, CausalKnown: true, Communicate: &schema.CommunicateInfo{CallID: id, Message: id, EndTurn: true, EndReason: "done"}})
		appendEntry(schema.Turn{TurnID: id, Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: id, Name: "communicate", Content: `{"accepted":true,"end_turn":true}`}}}}})
	}
	done("C")
	for i := range 96 {
		done(fmt.Sprintf("unrelated-%d", i))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	index, err := transcriptindex.Open(path, filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	lookup := func(_, _, _, ancestor, candidate string) (bool, error) {
		// A real index call also proves no lookup holds jm.mu.
		jm.mu.Lock()
		jm.mu.Unlock()
		matches, _, err := index.AnswerContinuation(context.Background(), ancestor, candidate, nil)
		return matches, err
	}
	window, err := index.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	var winner appwire.SessionWatchUpdate
	for {
		for i := len(window.Candidates) - 1; i >= 0; i-- {
			c := window.Candidates[i]
			if c.Item.Observation == nil || c.Item.Observation.TerminalAccepted == nil {
				continue
			}
			m := *c.Item.Observation
			m.Ref, m.InstanceID, m.GenerationID, m.EntryID = ref, instance, generation, c.Item.TranscriptKey
			update := appwire.SessionWatchUpdate{Event: "communicate", Metadata: m}
			if c.TurnID == "C" {
				winner = update
				continue
			}
			if err := jm.onHubObservationLookup(watch.WatchID, update, false, lookup); err != nil {
				t.Fatal(err)
			}
		}
		if !window.HasOlder {
			break
		}
		window, err = index.Before(window.Candidates[0].Position, 40)
		if err != nil {
			t.Fatal(err)
		}
	}
	jm.mu.Lock()
	_, cfg, present := jm.watchConfigByIDLocked(watch.WatchID)
	retained := 0
	if present {
		retained = len(cfg.hubSeen)
	}
	jm.mu.Unlock()
	if !present || retained != 0 {
		t.Fatalf("unrelated completion retained state: present=%v retained=%d", present, retained)
	}
	if winner.Metadata.TurnID != "C" {
		t.Fatal("missing saved terminal")
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		var ctx context.Context
		var cancel context.CancelFunc
		if cause == context.Canceled {
			ctx, cancel = context.WithCancel(t.Context())
			cancel()
		} else {
			ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		}
		err := jm.onHubObservationLookup(watch.WatchID, winner, true, func(_, _, _, ancestor, candidate string) (bool, error) {
			matches, _, err := index.AnswerContinuation(ctx, ancestor, candidate, nil)
			return matches, err
		})
		cancel()
		if !errors.Is(err, cause) || !managerWatchActive(jm, watch.WatchID) {
			t.Fatalf("lookup error must propagate without completion: want=%v got=%v", cause, err)
		}
	}
	// The answer inputs were never delivered to this watch. The saved lookup
	// establishes both edges even when the terminal arrives before older pages.
	if err := jm.onHubObservationLookup(watch.WatchID, winner, true, lookup); err != nil {
		t.Fatal(err)
	}
	if managerWatchActive(jm, watch.WatchID) {
		t.Fatal("recorded descendant did not complete watch")
	}
	if err := jm.onHubObservationLookup(watch.WatchID, winner, false, lookup); err != nil {
		t.Fatal(err)
	}
	completed, sends := 0, 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchID == watch.WatchID && event.Watch != nil && event.Watch.EndReason == "completed" {
			completed++
		}
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID {
			sends++
		}
	}
	if completed != 1 || sends != 1 {
		t.Fatalf("completed=%d sends=%d", completed, sends)
	}
	t.Run("replacement_during_lookup", func(t *testing.T) {
		old, err := jm.configureHubWatch(ref, instance, generation, "local:manager", nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := jm.onHubObservation(old.WatchID, appwire.SessionWatchUpdate{Metadata: scope, Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance, ActiveTurnID: "A"}}}}); err != nil {
			t.Fatal(err)
		}
		var replacement string
		err = jm.onHubObservationLookup(old.WatchID, winner, true, func(_, _, _, ancestor, candidate string) (bool, error) {
			matches, _, err := index.AnswerContinuation(t.Context(), ancestor, candidate, nil)
			if err != nil {
				return false, err
			}
			next, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"question"}, true)
			if err != nil {
				return false, err
			}
			replacement = next.WatchID
			return matches, err
		})
		if err != nil {
			t.Fatal(err)
		}
		if managerWatchActive(jm, old.WatchID) || replacement == old.WatchID || !managerWatchActive(jm, replacement) {
			t.Fatal("lookup changed replacement lifetime")
		}
		for _, event := range loadJobStoreEvents(t, jm) {
			if event.WatchID == old.WatchID && event.Watch != nil && event.Watch.EndReason == "completed" {
				t.Fatal("replaced watch completed")
			}
		}
	})
}
