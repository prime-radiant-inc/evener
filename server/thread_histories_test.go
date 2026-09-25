package server

import (
	"fmt"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appoverlay"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func noopHistoryPublish(appwire.HistoryUpdatedParams) error { return nil }
func noopHistoryResync(uint64)                              {}

// writeDelegateTranscript records texts as user-input turns into a fresh
// transcript file for threadID, without wiring any append hook: the
// resulting threadHistory sees nothing recorded in this process and falls
// back to the file, as a delegate whose entries were recorded before the
// registry learned about it would.
func writeDelegateTranscript(t *testing.T, threadID string, texts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), threadID+".transcript.jsonl")
	writer, err := transcript.NewWriterNoSync(path, transcript.Header{SessionID: threadID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	for _, text := range texts {
		if _, err := writer.Record(schema.NewTurn(schema.TurnUserInput, llm.User(text)), transcript.RecordOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func newTestThreadHistories(t *testing.T) *threadHistories {
	t.Helper()
	r := newThreadHistories(transcriptindex.DefaultCacheCapacity, appoverlay.DefaultBudgetBytes)
	t.Cleanup(r.close)
	return r
}

// Two delegates' histories are projected over the same *transcriptindex.Cache
// the registry holds, not one each; ensure is idempotent per threadID.
func TestThreadHistoriesShareOneCache(t *testing.T) {
	r := newTestThreadHistories(t)
	pathA := writeDelegateTranscript(t, "delegate_a", "hello a")
	pathB := writeDelegateTranscript(t, "delegate_b", "hello b")

	ha := r.ensure("delegate_a", "local:delegate_a", pathA, 0, noopHistoryPublish, noopHistoryResync, nil)
	hb := r.ensure("delegate_b", "local:delegate_b", pathB, 0, noopHistoryPublish, noopHistoryResync, nil)

	if ha.cache != r.cache || hb.cache != r.cache {
		t.Fatalf("delegate histories do not share the registry's cache")
	}
	if again := r.ensure("delegate_a", "local:delegate_a", pathA, 0, noopHistoryPublish, noopHistoryResync, nil); again != ha {
		t.Fatalf("ensure created a second history for an already-registered thread")
	}
	if r.get("delegate_a") != ha || r.get("delegate_b") != hb {
		t.Fatalf("get does not return the registered histories")
	}
}

// A delegate whose entries were all written before the registry attached to
// it (nothing recorded in this process) still projects its whole file on
// read, falling back to the file's size instead of a hook-reported length.
func TestThreadHistoriesReadOfUnhookedDelegateProjectsWholeFile(t *testing.T) {
	r := newTestThreadHistories(t)
	path := writeDelegateTranscript(t, "delegate_x", "first", "second")

	h := r.ensure("delegate_x", "local:delegate_x", path, 0, noopHistoryPublish, noopHistoryResync, nil)
	turns, _, _, _, err := h.latest(h.capture(), "local:delegate_x", 10)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("latest returned %d turns, want both entries the file holds", len(turns))
	}
}

// drop closes the dropped thread's overlay, returning its notices' budget
// bytes and clearing its state, and removes it from the registry. Its
// projection goroutine is stopped and waited for by the time drop returns
// (threadHistory.close's guarantee).
func TestThreadHistoriesDropClosesOverlayAndDeregisters(t *testing.T) {
	r := newTestThreadHistories(t)
	path := writeDelegateTranscript(t, "delegate_y", "only")

	h := r.ensure("delegate_y", "local:delegate_y", path, 0, noopHistoryPublish, noopHistoryResync, nil)
	h.overlay.Event(events.New(events.WarningData{Message: "careful"}))
	if len(h.overlay.Snapshot()) == 0 {
		t.Fatal("overlay has no notice to drop")
	}

	r.drop("delegate_y")

	if got := h.overlay.Snapshot(); len(got) != 0 {
		t.Fatalf("overlay not closed by drop: snapshot = %+v", got)
	}
	if r.get("delegate_y") != nil {
		t.Fatal("dropped history is still registered")
	}
}

// Reading 70 delegates in turn, each over its own transcript file, never
// leaves more than the cache's capacity of open index handles: the shared
// cache evicts the least recently released as later delegates are read.
func TestThreadHistoriesSeventyDelegatesLeaveAtMost64OpenHandles(t *testing.T) {
	r := newTestThreadHistories(t)

	for i := range 70 {
		threadID := fmt.Sprintf("delegate_%02d", i)
		ref := "local:" + threadID
		path := writeDelegateTranscript(t, threadID, "only")
		h := r.ensure(threadID, ref, path, 0, noopHistoryPublish, noopHistoryResync, nil)
		if _, _, _, _, err := h.latest(h.capture(), ref, 10); err != nil {
			t.Fatalf("delegate %d: latest: %v", i, err)
		}
	}

	if open := r.cache.OpenHandles(); open > transcriptindex.DefaultCacheCapacity {
		t.Fatalf("cache holds %d open handles, want at most %d", open, transcriptindex.DefaultCacheCapacity)
	}
}
