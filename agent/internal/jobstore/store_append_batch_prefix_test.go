package jobstore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStoreAppendBatchDurablePrefixIsAValidLog pins the batch contract after a
// crash: the records are independent newline-framed events, so an interruption
// can leave any prefix of the batch complete. That prefix is a valid log whose
// sequence numbers are already assigned, and the next append continues from it.
func TestStoreAppendBatchDurablePrefixIsAValidLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s := openStore(t, path)
	appendEvent(t, s, Event{Kind: EventJobStarted, JobID: "job_A"})
	batch := []Event{
		{Kind: EventWatchRegistered, WatchID: "watch_A", Watch: &WatchEvent{Generation: "wg_1"}},
		{Kind: EventWatchCleared, WatchID: "watch_A", Watch: &WatchEvent{Generation: "wg_1", EndReason: "cleared"}},
		{Kind: EventJobFinished, JobID: "job_A", Status: StatusCompleted, TerminalGen: "gen_A"},
	}
	if err := s.AppendBatch(batch); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	closeStore(t, s)

	// A crash after the first record of the batch leaves exactly one complete
	// line of the batch on disk.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	newlines := 0
	cut := 0
	for i, b := range data {
		if b == '\n' {
			newlines++
			if newlines == 2 {
				cut = i + 1
				break
			}
		}
	}
	if cut == 0 {
		t.Fatalf("log has fewer than two complete records: %s", data)
	}
	if err := os.WriteFile(path, data[:cut], 0o644); err != nil {
		t.Fatalf("truncate batch prefix: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after batch prefix: %v", err)
	}
	events, err := reopened.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents after batch prefix: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events after batch prefix = %#v, want the seed and the first batch record", events)
	}
	if events[1].Seq != 2 || events[1].Kind != EventWatchRegistered || events[1].WatchID != "watch_A" {
		t.Fatalf("first batch record = %#v", events[1])
	}

	if err := reopened.Append(Event{Kind: EventJobStarted, JobID: "job_B"}); err != nil {
		t.Fatalf("append after batch prefix: %v", err)
	}
	events, err = reopened.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents after append: %v", err)
	}
	if len(events) != 3 || events[2].Seq != 3 || events[2].JobID != "job_B" {
		t.Fatalf("events after append = %#v, want job_B at seq 3", events)
	}
	closeStore(t, reopened)
}
