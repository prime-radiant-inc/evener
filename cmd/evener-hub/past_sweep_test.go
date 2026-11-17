package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/llm"
)

// sweepFixture is a projects root holding one indexed session, plus the two
// indexes the sweeper drives.
type sweepFixture struct {
	stateDir  string
	sessionID string
	past      *hubcore.PastIndex
	search    *hubcore.MessageSearch
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	sessionID := hubtest.SessionID(t)
	now := time.Now().UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
		Name: "Sweep fixture", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID, CreatedAt: now.Add(-time.Hour), ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("the first searchable line"))); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	indexPastTranscriptForTest(t, stateDir, sessionID)

	past := closeOnCleanup(t, hubcore.NewPastIndex(filepath.Join(projectsRoot, "*")))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	search, err := hubcore.OpenMessageSearch(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = search.Close() })
	return &sweepFixture{stateDir: stateDir, sessionID: sessionID, past: past, search: search}
}

// appendUserTurn adds one user message to the transcript, the way a live
// daemon appends as a turn runs.
func (f *sweepFixture) appendUserTurn(t *testing.T, text string) {
	t.Helper()
	path := filepath.Join(f.stateDir, "sessions", f.sessionID+".transcript.jsonl")
	w, _, err := transcript.OpenWriterForSession(path, f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User(text))); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// renameSession rewrites the meta with a new name, the way a client rename
// does.
func (f *sweepFixture) renameSession(t *testing.T, name string) {
	t.Helper()
	now := time.Now().UTC()
	if err := schema.SaveSessionMeta(f.stateDir, schema.SessionMeta{
		ID: f.sessionID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		Name: name, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	}); err != nil {
		t.Fatal(err)
	}
}

// messageSearchable reports whether text is findable through the search index.
func (f *sweepFixture) messageSearchable(t *testing.T, text string) bool {
	t.Helper()
	matches, err := f.search.Match(context.Background(), text, 5)
	if err != nil {
		t.Fatal(err)
	}
	return len(matches[f.sessionID].Hits) > 0
}

// TestSweepPicksUpATranscriptAppendOnTheNextTick pins the visibility promise a
// subscribed client relies on: a message appended after the index was built is
// searchable after the next sweep.
func TestSweepPicksUpATranscriptAppendOnTheNextTick(t *testing.T) {
	f := newSweepFixture(t)
	sweep := newHubPastSweep(f.past, f.search)
	ctx := context.Background()

	sweep.sweep(ctx)
	if !f.messageSearchable(t, "first") {
		t.Fatal("the seeded message is not searchable after a sweep")
	}
	if f.messageSearchable(t, "second") {
		t.Fatal("a message not yet appended is already searchable")
	}

	f.appendUserTurn(t, "the second searchable line")
	sweep.sweep(ctx)
	if !f.messageSearchable(t, "second") {
		t.Fatal("an appended message is not searchable after the next sweep")
	}
}

// TestSweepSkipsFullPassWhenNothingChanged is the change the issue asks for: an
// idle sweep must not redo the full scan and refresh. It counts the work, not
// the time.
func TestSweepSkipsFullPassWhenNothingChanged(t *testing.T) {
	f := newSweepFixture(t)
	sweep := newHubPastSweep(f.past, f.search)
	ctx := context.Background()

	sweep.sweep(ctx)
	before := sweep.fullPassCount()
	sweep.sweep(ctx)
	sweep.sweep(ctx)
	if got := sweep.fullPassCount() - before; got != 0 {
		t.Fatalf("idle sweeps did full work %d times, want 0", got)
	}

	// A rename still has to be seen by the very next sweep, so the gate cannot
	// be a one-way latch.
	f.renameSession(t, "Renamed by the client")
	sweep.sweep(ctx)
	if got := sweep.fullPassCount() - before; got != 1 {
		t.Fatalf("a changed tree did %d full passes, want exactly 1", got)
	}
	entry, ok := f.past.Lookup(f.sessionID)
	if !ok || entry.Meta.Name != "Renamed by the client" {
		t.Fatalf("the rename is not in the index: name=%q found=%v", entry.Meta.Name, ok)
	}
}

// TestSweepRetriesAfterAFailedRefresh pins that a pass whose message-search
// refresh could not read every transcript forces the next tick past the gate.
// MessageSearch.Refresh retries a failed transcript only on its next call, so
// gating that call away would strand the retry until unrelated disk activity
// moved the digest.
func TestSweepRetriesAfterAFailedRefresh(t *testing.T) {
	f := newSweepFixture(t)
	sweep := newHubPastSweep(f.past, f.search)
	ctx := context.Background()

	// An unreadable transcript makes Refresh report a failure without changing
	// any file's stamp, so the digest alone cannot tell the next tick to retry.
	// A directory at the transcript path fails the read the same way a
	// permission error would.
	transcriptPath := filepath.Join(f.stateDir, "sessions", f.sessionID+".transcript.jsonl")
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(transcriptPath, 0o755); err != nil {
		t.Fatal(err)
	}

	sweep.sweep(ctx)
	if !sweep.retry {
		t.Fatal("a pass whose refresh reported a failure did not ask for a retry")
	}
	before := sweep.fullPassCount()
	sweep.sweep(ctx)
	if got := sweep.fullPassCount() - before; got != 1 {
		t.Fatalf("the tick after a failed refresh did %d full passes, want 1", got)
	}
}
