package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// A resumed session holds its transcript writer open while the bootstrap's
// cold delegate-attention appends open and close their own writers on the same
// file. The session's later appends — durable and buffered — must continue the
// one sequence and land after the cold records, not reuse the sequence it
// counted at open or write over them.
func TestColdAttentionAppendWhileSessionWriterOpenKeepsOneSequence(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const sessionID = "shared-writer"
	path := transcriptPath(stateDir, sessionID)
	seed, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := seed.Append(schema.NewTurn(schema.TurnSteering, llm.User("before resume"))); err != nil {
		t.Fatalf("seed append: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed writer: %v", err)
	}

	sessionWriter, _, err := transcript.OpenWriterForSession(path, sessionID)
	if err != nil {
		t.Fatalf("open session writer: %v", err)
	}
	defer sessionWriter.Close() //nolint:errcheck // assertion fixture

	for _, attentionID := range []string{"shell:one", "shell:two"} {
		appended, err := appendColdDelegateNotificationDurablyWithOpen(path, sessionID, attentionID, "cold "+attentionID, time.Unix(100, 0), transcript.OpenWriterForSession)
		if err != nil || !appended {
			t.Fatalf("cold append %s = (%v, %v), want (true, nil)", attentionID, appended, err)
		}
	}
	if err := sessionWriter.AppendDurable(schema.NewTurn(schema.TurnSteering, llm.User("session durable"))); err != nil {
		t.Fatalf("session AppendDurable: %v", err)
	}
	if err := sessionWriter.Append(schema.NewTurn(schema.TurnSteering, llm.User("session buffered"))); err != nil {
		t.Fatalf("session Append: %v", err)
	}

	entries := readAttentionTranscriptEntries(t, path)
	if len(entries) != 5 {
		t.Fatalf("transcript holds %d entries, want 5", len(entries))
	}
	for i, entry := range entries {
		if entry.Seq != i {
			t.Fatalf("entry %d in file order has seq %d, want %d", i, entry.Seq, i)
		}
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("fold attention: %v", err)
	}
	for _, attentionID := range []string{"shell:one", "shell:two"} {
		if _, ok := fold.content[attentionID]; !ok {
			t.Fatalf("cold attention %s is missing from the transcript", attentionID)
		}
	}
}
