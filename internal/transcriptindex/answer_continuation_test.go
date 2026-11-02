package transcriptindex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestAnswerContinuationRecordedEdges(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		links         map[string][]string
		candidate     string
		want, invalid bool
	}{
		{"two answers", map[string][]string{"B": {"A"}, "C": {"B"}}, "C", true, false},
		{"unrelated", map[string][]string{"B": {"A"}, "C": {"B"}}, "X", false, false},
		{"absent", nil, "C", false, false},
		{"self", map[string][]string{"C": {"C"}}, "C", false, true},
		{"forward", map[string][]string{"B": {"C"}}, "B", false, true},
		{"cycle", map[string][]string{"B": {"C"}, "C": {"B"}}, "C", false, true},
		{"invalid branch", map[string][]string{"C": {"A", "C"}}, "C", false, true},
		{"missing link target", map[string][]string{"C": {"missing"}}, "C", false, true},
		{"missing candidate", nil, "missing", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
			w, err := transcript.NewWriter(path, transcript.Header{SessionID: "session"})
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"A", "B", "X", "C"} {
				if err := w.Append(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: id, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User(id), AnswerToTurnIDs: tc.links[id]}); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			x := openIndex(t, path, t.TempDir())
			// Interface keeps the baseline test executable before the API exists.
			query, ok := any(x).(interface {
				AnswerContinuation(context.Context, string, string, *appwire.SnapshotIdentity) (bool, appwire.SnapshotIdentity, error)
			})
			if !ok {
				t.Fatal("saved history cannot answer continuation membership")
			}
			match, cut, err := query.AnswerContinuation(context.Background(), "A", tc.candidate, nil)
			if tc.invalid {
				if err == nil {
					t.Fatalf("invalid evidence returned %v", match)
				}
				return
			}
			if err != nil || match != tc.want || cut.Incarnation == "" || cut.Length == 0 {
				t.Fatalf("match=%v cut=%+v err=%v", match, cut, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, _, err := query.AnswerContinuation(ctx, "A", tc.candidate, &cut); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled lookup: %v", err)
			}
			bad := cut
			bad.Incarnation = "replaced"
			if _, _, err := query.AnswerContinuation(context.Background(), "A", tc.candidate, &bad); err == nil {
				t.Fatal("foreign identity returned verdict")
			}
			bad = cut
			bad.Length++
			if _, _, err := query.AnswerContinuation(context.Background(), "A", tc.candidate, &bad); err == nil {
				t.Fatal("future cut returned verdict")
			}
			// Replacement invalidates a held cut rather than answering false.
			if err := os.WriteFile(path, []byte("unreadable history\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := query.AnswerContinuation(context.Background(), "A", tc.candidate, &cut); err == nil {
				t.Fatal("replaced history returned verdict")
			}
		})
	}
}

// Quarantine is a visible history item, not evidence that known recorded
// executions lost their explicit links. Only evidence used by this query is
// required to decode.
func TestAnswerContinuationUnrelatedQuarantine(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("not a recorded entry\n")
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range []schema.Turn{
		{Format: schema.TurnFormatIdentity, TurnID: "A", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("question")},
		{Format: schema.TurnFormatIdentity, TurnID: "B", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("answer"), AnswerToTurnIDs: []string{"A"}},
	} {
		data, err := json.Marshal(transcript.Entry{Kind: "entry", Seq: i + 2, MachineryFlagged: true, Turn: entry})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	x := openIndex(t, path, t.TempDir())
	matches, _, err := x.AnswerContinuation(t.Context(), "A", "B", nil)
	if err != nil || !matches {
		t.Fatalf("known chain beside quarantine: matches=%t error=%v", matches, err)
	}
}

func TestAnswerContinuationContendedIndexDoesNotWait(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"mutex", "file"} {
		t.Run(kind, func(t *testing.T) {
			fx := newFormatFixture(t, "open and reclaimed")
			x := openIndex(t, writeFixture(t, fx), t.TempDir())
			if kind == "mutex" {
				x.mu.Lock()
				defer x.mu.Unlock()
			} else {
				other, err := os.OpenFile(x.lock.Name(), os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				if err := lockFile(other, true); err != nil {
					t.Fatal(err)
				}
				defer unlockFile(other)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, _, err := x.AnswerContinuation(ctx, "turn_m15", "turn_m16", nil); done <- err }()
			// A contended query must report busy or cancellation, not wait for an
			// unrelated owner. The cleanup unlocks the index even on the baseline red.
			select {
			case err := <-done:
				if !errors.Is(err, ErrLockBusy) && !errors.Is(err, context.Canceled) {
					t.Fatalf("contention: %v", err)
				}
			case <-time.After(time.Second):
				cancel()
				t.Fatal("lookup waited on the busy index")
			}
		})
	}
}
