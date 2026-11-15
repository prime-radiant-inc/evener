package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
)

// foldCopy returns a shallow copy of turn stamped as a compaction replay copy
// of original ordinal ord.
func foldCopy(turn schema.Turn, ord uint64) schema.Turn {
	turn.OriginalOrdinal = &ord
	return turn
}

// writeFile writes content to path, creating parent dirs. Test-only helper.
func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDelegateEvents(t testing.TB, delegatesPath string, events []delegatestore.Event) {
	t.Helper()
	store, err := delegatestore.Open(delegatesPath)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(existing)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AppendBatch(state, events); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// appendRawLine appends raw bytes directly to an on-disk journal, bypassing
// the canonical writer -- the only way to construct a deliberately corrupt
// or partial fixture. Test-only.
func appendRawLine(t testing.TB, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// legacyFailureJournalEvents builds the jobs-journal fixture the bounded
// legacy-failure fold tests share: the retired delegate activation as the
// journal's oldest record, then padding shell jobs pushing any later corrupt
// line far beyond a shrunk head window.
func legacyFailureJournalEvents(sid string, startedAt time.Time) []jobstore.Event {
	events := []jobstore.Event{{
		// The retired activation format: a delegate-type job, the oldest
		// record in the journal and the one the failures fold exists to find.
		Kind: jobstore.EventJobStarted, TS: startedAt, JobID: "delegate_legacy", Type: jobstore.JobType("delegate"), OwnerSessionID: sid, VisibleToSession: sid, StartedAt: &startedAt,
	}}
	for i := range 12 {
		ts := startedAt.Add(time.Duration(i) * time.Second)
		events = append(events, jobstore.Event{Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("shell_%02d", i), Type: jobstore.JobShell, OwnerSessionID: sid, VisibleToSession: sid, Command: "echo padding", StartedAt: &ts})
	}
	return events
}

// hasFailureCode reports whether failures carries the given diagnostic code.
func hasFailureCode(failures []StateFailure, code string) bool {
	for _, failure := range failures {
		if failure.Code == code {
			return true
		}
	}
	return false
}
