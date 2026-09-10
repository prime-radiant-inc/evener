package agent

import (
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestPersistAndEmitRoundTimingsWriteFailurePublishesNeither(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	for {
		select {
		case <-sess.Events():
		default:
			goto drained
		}
	}
drained:
	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	writer, err := transcript.NewWriterWithFS(fs, "/timings.jsonl", transcript.Header{SessionID: sess.id})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	fs.fail = true
	sess.mu.Lock()
	original := sess.transcript
	sess.transcript = writer
	sess.transcriptReady = true
	before := len(sess.history)
	beforeAppends := len(sess.persistedAppendLog)
	sess.mu.Unlock()
	if original != nil {
		t.Cleanup(func() { _ = original.Close() })
	}

	sess.persistAndEmitRoundTimings(events.RoundTimings{Round: 3, TotalRound: time.Second})
	sess.mu.Lock()
	after, afterAppends := len(sess.history), len(sess.persistedAppendLog)
	sess.mu.Unlock()
	if after != before || afterAppends != beforeAppends {
		t.Fatalf("failed write changed history/appends: (%d, %d), want (%d, %d)", after, afterAppends, before, beforeAppends)
	}
	warnings := 0
	for {
		select {
		case event := <-sess.Events():
			switch event.Kind {
			case events.EventRoundTimings:
				t.Fatal("failed timing write published a timing item without a durable counterpart")
			case events.EventWarning:
				warnings++
			}
		default:
			if warnings != 1 {
				t.Fatalf("failed write warnings = %d, want one", warnings)
			}
			return
		}
	}
}

func TestExpandHistoryDropsPersistedRoundTimings(t *testing.T) {
	turn := schema.NewTurn(schema.TurnRoundTimings, llm.System("Round 0 total=1s"))
	turn.RoundTimings = &schema.RoundTimings{Round: 0, TotalRound: time.Second}
	history := expandHistory([]schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("question")),
		turn,
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer")),
	}, replayScope{})
	if len(history) != 2 || history[0].Text() != "question" || history[1].Text() != "answer" {
		t.Fatalf("provider history = %#v, want user and assistant only", history)
	}
}
