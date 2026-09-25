package agent

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// recordedLog collects the records a recorded-entry hook sees. Its mutex is a
// leaf: the hook runs under the transcript's append lock.
type recordedLog struct {
	mu      sync.Mutex
	records []transcript.Record
}

func (l *recordedLog) record(rec transcript.Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, rec)
}

func (l *recordedLog) snapshot() []transcript.Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]transcript.Record(nil), l.records...)
}

// requireRecordsCoverEntries checks that records are exactly the file's
// entries from ordinal first on, in ordinal order, and that the last one ends
// at the session's recorded length, which is the file's size.
func requireRecordsCoverEntries(t *testing.T, s *Session, records []transcript.Record, first int) {
	t.Helper()
	entries := transcriptEntries(t, s)
	if len(records) != len(entries)-first {
		t.Fatalf("hook saw %d records, the file has %d entries after the first %d", len(records), len(entries)-first, first)
	}
	for i, rec := range records {
		entry := entries[first+i]
		if !rec.Recorded || rec.Ordinal != uint64(first+i) || rec.Seq != entry.Seq || rec.Turn.Kind != entry.Turn.Kind {
			t.Fatalf("record %d = ordinal %d seq %d %s, want ordinal %d seq %d %s", i, rec.Ordinal, rec.Seq, rec.Turn.Kind, first+i, entry.Seq, entry.Turn.Kind)
		}
	}
	last := records[len(records)-1]
	info, err := os.Stat(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if end := last.Offset + last.Length; end != s.TranscriptRecordedLength() || end != info.Size() {
		t.Fatalf("last record ends at %d; recorded length %d, file size %d", end, s.TranscriptRecordedLength(), info.Size())
	}
}

// The recorded func sees every entry the session records, in ordinal order,
// across a writer attention recovery reopened.
func TestRecordedHookSeesEveryEntryAcrossAttentionRecovery(t *testing.T) {
	s, adapter := newExecutionSession(t)
	var log recordedLog
	first := len(transcriptEntries(t, s))
	s.SetTranscriptRecordedFunc(log.record)
	args, _ := json.Marshal(map[string]any{"file_path": "missing.txt"})
	adapter.script(respond(toolCallResponse(llm.ToolCallData{ID: "read-1", Name: "read_file", Arguments: args, Type: "function"})))
	if _, err := s.ProcessInput(context.Background(), "a tool round", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.appendDelegateNotificationDurably("attention-probe", "a delegate reports first"); err != nil {
		t.Fatalf("attention write: %v", err)
	}
	original := s.attachedTranscript()
	if err := s.stabilizeAttentionForStop("attention-probe"); err != nil {
		t.Fatalf("stabilize attention: %v", err)
	}
	if s.attachedTranscript() == original {
		t.Fatal("attention recovery did not reopen the writer")
	}
	if _, err := s.appendDelegateNotificationDurably("attention-after-reopen", "a delegate reports"); err != nil {
		t.Fatalf("attention write after reopen: %v", err)
	}
	if _, err := s.ProcessInput(context.Background(), "after recovery", nil); err != nil {
		t.Fatal(err)
	}
	records := log.snapshot()
	requireRecordsCoverEntries(t, s, records, first)
	sawAttention := false
	for _, rec := range records {
		sawAttention = sawAttention || rec.Turn.AttentionID == "attention-after-reopen"
	}
	if !sawAttention {
		t.Fatal("the hook did not see the attention write after the reopen")
	}
}

// A func set before the transcript attaches is installed at attach.
func TestRecordedHookSetBeforeAttachIsInstalledAtAttach(t *testing.T) {
	stateDir := t.TempDir()
	const sessionID = "recorded-before-attach"
	writer, err := transcript.NewWriter(transcriptPath(stateDir, sessionID), transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{id: sessionID, stateDir: stateDir}
	var log recordedLog
	s.SetTranscriptRecordedFunc(log.record)
	if got := s.TranscriptRecordedLength(); got != 0 {
		t.Fatalf("recorded length with no transcript = %d, want 0", got)
	}
	s.attachTranscript(writer)
	t.Cleanup(func() { _ = s.closeAttachedTranscript() })
	if err := s.writeTranscript(schema.NewTurn(schema.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatal(err)
	}
	records := log.snapshot()
	if len(records) != 1 || records[0].Turn.Kind != schema.TurnUserInput {
		t.Fatalf("hook saw %d records, want the USER_INPUT", len(records))
	}
	if end := records[0].Offset + records[0].Length; end != s.TranscriptRecordedLength() {
		t.Fatalf("record ends at %d, recorded length %d", end, s.TranscriptRecordedLength())
	}
}

// A delegate child's entries reach the root's descendant recorded func under
// the child's session id.
func TestRecordedHookDescendantEntriesCarryTheChildSessionID(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	var mu sync.Mutex
	bySession := map[string][]transcript.Record{}
	completed := make(chan string, 16)
	root.SetDescendantRecordedFunc(func(sessionID string, rec transcript.Record) {
		mu.Lock()
		bySession[sessionID] = append(bySession[sessionID], rec)
		mu.Unlock()
		if rec.Turn.Kind == schema.TurnCompletion {
			completed <- sessionID
		}
	})
	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "report the recorded entries",
		DelegationAllowance: new(0),
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	children := root.subagents.sessions()
	if len(children) != 1 {
		t.Fatalf("tracked child count = %d, want 1", len(children))
	}
	child := children[0]
	select {
	case sessionID := <-completed:
		if sessionID != child.ID() {
			t.Fatalf("a completion arrived for session %q, want the child %q", sessionID, child.ID())
		}
	// TRIPWIRE: scripted in-process adapter, no real I/O; the child's turn
	// normally completes in well under a second. 30s only fires on a hang.
	case <-time.After(30 * time.Second):
		t.Fatal("the child's completion never reached the descendant recorded func")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := bySession[root.ID()]; ok {
		t.Fatal("the root's own entries reached the descendant recorded func")
	}
	records := bySession[child.ID()]
	if len(records) == 0 {
		t.Fatal("no child records")
	}
	for i := 1; i < len(records); i++ {
		if records[i].Ordinal != records[i-1].Ordinal+1 {
			t.Fatalf("child records out of order: ordinal %d after %d", records[i].Ordinal, records[i-1].Ordinal)
		}
	}
}
