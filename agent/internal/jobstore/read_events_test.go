package jobstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadEvents_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{
		{Kind: EventWatchRegistered, WatchID: "w1", Watch: &WatchEvent{Generation: "g1", Target: "job:x"}},
		{Kind: EventWatchSendPending, WatchID: "w1", WatchSend: &WatchSendState{Key: WatchSendKey{WatchID: "w1"}, DeliveryID: "d1", UpdateSeq: 1}},
		{Kind: EventWatchSendDelivered, WatchID: "w1", WatchSend: &WatchSendState{Key: WatchSendKey{WatchID: "w1"}, DeliveryID: "d1", UpdateSeq: 1}},
	}
	for _, e := range want {
		if err := st.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := ReadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("ReadEvents got %d events, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Kind != want[i].Kind {
			t.Errorf("event %d kind = %q, want %q", i, got[i].Kind, want[i].Kind)
		}
	}
	// The real folds run over the read-back events.
	if w := FoldWatches(got); w["w1"] == nil || w["w1"].Generation != "g1" {
		t.Errorf("FoldWatches over ReadEvents lost the watch registration")
	}
}

func TestReadEvents_Missing(t *testing.T) {
	got, err := ReadEvents(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != nil {
		t.Errorf("missing file should yield nil events, got %d", len(got))
	}
}

func TestReadEvents_TolerateTrailingPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	content := `{"kind":"watch_registered","seq":1,"job_id":"","watch_id":"w1","watch":{"generation":"g1"}}` + "\n" +
		`{"kind":"watch_send_pending","seq":2,` // truncated trailing line, no newline
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("trailing partial should be tolerated: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 (partial trailing skipped)", len(got))
	}
}

func TestReadEvents_ErrorsOnMidFileCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	content := `{"kind":"watch_registered","seq":1}` + "\n" + `NOT JSON` + "\n" + `{"kind":"watch_cleared","seq":3}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvents(path); err == nil {
		t.Fatal("a malformed non-trailing line should error")
	}
}
