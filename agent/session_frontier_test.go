package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

func TestExecutionFrontierAdvancesAtFirstRecordedEntry(t *testing.T) {
	t.Parallel()
	s, _ := newExecutionSession(t)
	s.beginExecution("turn_m101")
	if got := s.LatestExecutionTurnID(); got != "" {
		t.Fatalf("unrecorded execution frontier = %q", got)
	}
	rec, err := s.attachedTranscript().Record(schema.Turn{Kind: schema.TurnSteering}, transcript.RecordOptions{Place: transcript.PlaceAsync})
	if err != nil || !rec.Recorded || rec.Turn.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("first async record = %+v, %v", rec, err)
	}
	if got := s.LatestExecutionTurnID(); got != rec.Turn.TurnID {
		t.Fatalf("recorded frontier = %q, want committed execution %q", got, rec.Turn.TurnID)
	}
}

func TestExecutionFirstRecordSignalOncePerExecution(t *testing.T) {
	t.Parallel()
	for _, door := range []transcript.Door{transcript.DoorBuffered, transcript.DoorDurable, transcript.DoorSynced} {
		t.Run([]string{"buffered", "durable", "synced"}[door], func(t *testing.T) {
			s, _ := newExecutionSession(t)
			s.beginExecution("turn_m102")
			w := s.attachedTranscript()
			for i := range 2 {
				rec, err := w.Record(schema.Turn{Kind: schema.TurnCheckpoint}, transcript.RecordOptions{Door: door, Place: transcript.PlaceSession})
				if err != nil || !rec.Recorded {
					t.Fatalf("record %d = %+v, %v", i, rec, err)
				}
			}
			s.completeExecution(schema.TurnCompleted)
			s.Close()
			var signals int
			for ev := range s.Events() {
				if ev.Kind == events.EventExecutionFirstRecorded {
					signals++
				}
			}
			if signals != 1 {
				t.Fatalf("first-record signals = %d, want 1", signals)
			}
			if got := s.LatestExecutionTurnID(); got != "turn_m102" {
				t.Fatalf("completed execution frontier = %q", got)
			}
		})
	}
}

func TestExecutionFrontierPreservesInternalListenerOnHookReplacement(t *testing.T) {
	t.Parallel()
	s, _ := newExecutionSession(t)
	var oldCalls, newCalls int
	s.SetTranscriptRecordedFunc(func(transcript.Record) { oldCalls++ })
	s.SetTranscriptRecordedFunc(func(transcript.Record) { newCalls++ })
	s.beginExecution("turn_m103")
	if rec, err := s.attachedTranscript().Record(schema.Turn{Kind: schema.TurnSteering}, transcript.RecordOptions{Place: transcript.PlaceAsync}); err != nil || !rec.Recorded {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	if oldCalls != 0 || newCalls != 1 {
		t.Fatalf("replaced hook calls = %d, current calls = %d", oldCalls, newCalls)
	}
	s.SetTranscriptRecordedFunc(nil)
	s.completeExecution(schema.TurnCompleted)
	s.beginExecution("turn_m104")
	if rec, err := s.attachedTranscript().Record(schema.Turn{Kind: schema.TurnSteering}, transcript.RecordOptions{Place: transcript.PlaceAsync}); err != nil || !rec.Recorded {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	if got := s.LatestExecutionTurnID(); got != "turn_m104" || newCalls != 1 {
		t.Fatalf("frontier after removing external hook = %q, calls = %d", got, newCalls)
	}
}

func TestExecutionFrontierIgnoresPreservedFirstEntryCopies(t *testing.T) {
	t.Parallel()
	s, _ := newExecutionSession(t)
	s.beginExecution("turn_m105")
	w := s.attachedTranscript()
	if rec, err := w.Record(schema.Turn{Kind: schema.TurnCheckpoint}, transcript.RecordOptions{Place: transcript.PlaceSession}); err != nil || !rec.Recorded {
		t.Fatalf("first record = %+v, %v", rec, err)
	}
	ordinal := uint64(1)
	preserved := schema.Turn{Kind: schema.TurnSteering, TurnID: "turn_m99", TurnKind: schema.TurnSpanExecution, OriginalOrdinal: &ordinal}
	if rec, err := w.Record(preserved, transcript.RecordOptions{Place: transcript.PlaceVerbatim}); err != nil || !rec.Recorded {
		t.Fatalf("preserved record = %+v, %v", rec, err)
	}
	if got := s.LatestExecutionTurnID(); got != "turn_m105" {
		t.Fatalf("frontier after preserved copy = %q", got)
	}
}

func TestExecutionFrontierSeedsIdleRestore(t *testing.T) {
	t.Parallel()
	s, _ := newExecutionSession(t)
	if _, err := s.ProcessInput(context.Background(), "opaque-input", nil); err != nil {
		t.Fatal(err)
	}
	var want string
	for _, turn := range transcriptTurnsOf(t, s) {
		if turn.OriginalOrdinal == nil && turn.TurnKind == schema.TurnSpanExecution {
			want = turn.TurnID
		}
	}
	if want == "" {
		t.Fatal("fixture recorded no execution")
	}
	meta := s.Meta()
	s.Close()
	restored, err := RestoreSessionFromMetaWithConfig(s.client, s.profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{
		StateDir: s.stateDir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	if got := restored.LatestExecutionTurnID(); got != want {
		t.Errorf("idle restored frontier = %q, want %q", got, want)
	}
	restored.Close()
	for ev := range restored.Events() {
		if ev.Kind != events.EventSessionStart {
			continue
		}
		start := ev.Data.(events.SessionStartData)
		if start.LatestExecutionSequence == 0 || (events.ExecutionFrontier{TurnID: start.LatestExecutionTurnID, Sequence: start.LatestExecutionSequence}) != restored.LatestExecutionFrontier() {
			t.Fatalf("restored start lost atomic frontier pair: start=%+v pair=%+v", start, restored.LatestExecutionFrontier())
		}
		encoded, err := json.Marshal(ev.Data)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Frontier string `json:"latest_execution_turn_id"`
		}
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Frontier != want {
			t.Errorf("restored start frontier = %q, want %q", payload.Frontier, want)
		}
		return
	}
	t.Fatal("restored session emitted no start seed")
}

func TestExecutionFrontierSeedUsesNewestOriginalExecution(t *testing.T) {
	t.Parallel()
	ordinal := uint64(1)
	entries := []transcript.Entry{
		{Turn: schema.Turn{TurnID: "turn_m1", TurnKind: schema.TurnSpanExecution}},
		{Turn: schema.Turn{TurnID: "turn_m2", TurnKind: schema.TurnSpanExecution}},
		{Turn: schema.Turn{TurnID: "later-prelude", TurnKind: schema.TurnSpanPrelude}},
		{Turn: schema.Turn{TurnID: "turn_m1", TurnKind: schema.TurnSpanExecution, OriginalOrdinal: &ordinal}},
		{Turn: schema.Turn{TurnID: "later-gap", TurnKind: schema.TurnSpanGap}},
	}
	for _, tc := range []struct {
		name, committed string
		pending         []string
		want            string
	}{
		{name: "empty", want: "turn_m2"},
		{name: "commit-wins", committed: "turn_m3", want: "turn_m3"},
		{name: "queued-commit-wins", pending: []string{"turn_m3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{executionFrontier: events.ExecutionFrontier{TurnID: tc.committed}, frontierPending: tc.pending}
			s.seedExecutionFrontier(entries)
			if got := s.LatestExecutionTurnID(); got != tc.want {
				t.Fatalf("seed frontier = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExecutionFrontierPairSeedAndCommitOrder(t *testing.T) {
	t.Parallel()
	s, _ := newExecutionSession(t)
	seed := []transcript.Entry{{Turn: schema.Turn{TurnID: "turn_m501", TurnKind: schema.TurnSpanExecution}}}
	s.seedExecutionFrontier(seed)
	want := events.ExecutionFrontier{TurnID: "turn_m501", Sequence: 1}
	if got := s.LatestExecutionFrontier(); got != want {
		t.Fatalf("seed pair=%+v want=%+v", got, want)
	}
	s.seedExecutionFrontier([]transcript.Entry{{Turn: schema.Turn{TurnID: "stale-seed", TurnKind: schema.TurnSpanExecution}}})
	fs := installFrontierTestWriter(t, s)
	s.beginExecution("turn_m502")
	fs.mu.Lock()
	fs.failSyncs = 1 // Successful rollback, so no whole line is recorded.
	fs.mu.Unlock()
	w := s.attachedTranscript()
	rec, err := w.Record(schema.Turn{Kind: schema.TurnSteering}, transcript.RecordOptions{Door: transcript.DoorSynced, Place: transcript.PlaceAsync})
	if rec.Recorded || !errors.Is(err, errFrontierFileFault) {
		t.Fatalf("failed record did not exercise rollback: rec=%+v err=%v", rec, err)
	}
	if got := s.LatestExecutionFrontier(); got != want {
		t.Fatalf("seed retry, unrecorded execution or rollback advanced pair: got=%+v want=%+v", got, want)
	}
	rec, err = w.Record(schema.Turn{Kind: schema.TurnSteering}, transcript.RecordOptions{Place: transcript.PlaceAsync})
	if err != nil || !rec.Recorded || rec.Turn.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("retry first record=%+v err=%v", rec, err)
	}
	want = events.ExecutionFrontier{TurnID: "turn_m502", Sequence: 2}
	if got := s.LatestExecutionFrontier(); got != want {
		t.Fatalf("commit pair=%+v want=%+v", got, want)
	}
	ordinal := uint64(1)
	for _, turn := range []schema.Turn{
		{Kind: schema.TurnSteering},
		{Kind: schema.TurnSteering, TurnID: "copied-old", TurnKind: schema.TurnSpanExecution, OriginalOrdinal: &ordinal},
		{Kind: schema.TurnSteering, TurnID: "gap", TurnKind: schema.TurnSpanGap},
	} {
		if rec, err := w.Record(turn, transcript.RecordOptions{Place: transcript.PlaceVerbatim}); err != nil || !rec.Recorded {
			t.Fatalf("non-first record=%+v err=%v", rec, err)
		}
	}
	s.completeExecution(schema.TurnCompleted)
	s.beginExecution("turn_m503")
	s.seedExecutionFrontier(seed)
	if got := s.LatestExecutionFrontier(); got != want {
		t.Fatalf("copy, gap, completion, unrecorded start or stale seed changed pair: got=%+v want=%+v", got, want)
	}
}

func TestExecutionFrontierPairConcurrentCommits(t *testing.T) {
	t.Parallel()
	s, _ := newExecutionSession(t)
	w := s.attachedTranscript()
	for round := 1; round <= 4; round++ {
		id := fmt.Sprintf("turn_m%d", 510+round)
		s.beginExecution(id)
		start := make(chan struct{})
		errors := make(chan error, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				<-start
				rec, err := w.Record(schema.Turn{Kind: schema.TurnSteering}, transcript.RecordOptions{Place: transcript.PlaceAsync})
				if err != nil || !rec.Recorded {
					errors <- fmt.Errorf("record=%+v err=%w", rec, err)
					return
				}
				want := events.ExecutionFrontier{TurnID: id, Sequence: uint64(round)}
				if got := s.LatestExecutionFrontier(); got != want {
					errors <- fmt.Errorf("atomic pair=%+v want=%+v", got, want)
				}
			})
		}
		close(start)
		wg.Wait()
		close(errors)
		for err := range errors {
			t.Error(err)
		}
		s.completeExecution(schema.TurnCompleted)
	}
}

func TestExecutionFrontierSequenceNeverEntersEventJSON(t *testing.T) {
	t.Parallel()
	for _, sequence := range []uint64{0, 42} {
		start := events.SessionStartData{LatestExecutionTurnID: "turn_m520", LatestExecutionSequence: sequence}
		encoded, err := json.Marshal(start)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 3 || string(fields["latest_execution_turn_id"]) != `"turn_m520"` || string(fields["profile"]) != `""` || string(fields["model"]) != `""` {
			t.Fatalf("sequence=%d changed SessionStart JSON keys: %s", sequence, encoded)
		}
		pair, err := json.Marshal(events.ExecutionFrontier{TurnID: "turn_m520", Sequence: sequence})
		if err != nil || string(pair) != `{"turn_id":"turn_m520"}` {
			t.Fatalf("sequence=%d escaped pair: %s err=%v", sequence, pair, err)
		}
	}
}
