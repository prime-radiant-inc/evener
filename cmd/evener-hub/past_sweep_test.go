package hub

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// tick is one tick's work in the order a tick does it: the rebuild when the
// digest moved, then the refresh. The two run on their own loops in production;
// a test that is about what one tick reaches calls this instead of driving the
// loops.
func (s *hubPastSweep) tick(ctx context.Context) {
	s.sweepRebuild(ctx)
	s.sweepRefresh(ctx)
}

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

	sweep.tick(ctx)
	if !f.messageSearchable(t, "first") {
		t.Fatal("the seeded message is not searchable after a sweep")
	}
	if f.messageSearchable(t, "second") {
		t.Fatal("a message not yet appended is already searchable")
	}

	f.appendUserTurn(t, "the second searchable line")
	sweep.tick(ctx)
	if !f.messageSearchable(t, "second") {
		t.Fatal("an appended message is not searchable after the next sweep")
	}
}

// TestSweepReadsASessionARepairedSidecarMadeReadable is the case a digest gate
// over the refresh would strand. A transcript whose sidecar this projection has
// not built is refused with ErrRebuildRequired: Refresh records no stamp and
// reports no failure, and the repair lane that clears the refusal writes only
// the excluded .index-<projection> directory. Nothing the tree digest folds
// moves, so the sweep must reach that session through the refresh it runs
// unconditionally, not through a changed digest.
func TestSweepReadsASessionARepairedSidecarMadeReadable(t *testing.T) {
	f := newSweepFixture(t)
	// Take the sidecar away so the first sweep meets the refusing path, the
	// state every session not yet read under this projection is in.
	transcriptPath := filepath.Join(f.stateDir, "sessions", f.sessionID+".transcript.jsonl")
	if err := os.RemoveAll(transcriptindex.DirFor(transcriptPath)); err != nil {
		t.Fatal(err)
	}
	sweep := newHubPastSweep(f.past, f.search)
	ctx := context.Background()

	sweep.tick(ctx)
	if f.messageSearchable(t, "first") {
		t.Fatal("a session with no sidecar is already searchable; the refusing path was not exercised")
	}

	// The repair lane's effect, and the only write between the two sweeps.
	indexPastTranscriptForTest(t, f.stateDir, f.sessionID)

	sweep.tick(ctx)
	if !f.messageSearchable(t, "first") {
		t.Fatal("a repaired session was not read on the next sweep; running the refresh only behind the digest strands it")
	}
}

// TestSweepSkipsFullPassWhenNothingChanged is the change the issue asks for: an
// idle sweep must not redo the past index's full scan. (The refresh still runs
// every tick, by design.) It counts the work, not the time.
func TestSweepSkipsFullPassWhenNothingChanged(t *testing.T) {
	f := newSweepFixture(t)
	sweep := newHubPastSweep(f.past, f.search)
	ctx := context.Background()

	sweep.tick(ctx)
	before := sweep.rebuildCount()
	sweep.tick(ctx)
	sweep.tick(ctx)
	if got := sweep.rebuildCount() - before; got != 0 {
		t.Fatalf("idle sweeps did full work %d times, want 0", got)
	}

	// A rename still has to be seen by the very next sweep, so the gate cannot
	// be a one-way latch.
	f.renameSession(t, "Renamed by the client")
	sweep.tick(ctx)
	if got := sweep.rebuildCount() - before; got != 1 {
		t.Fatalf("a changed tree did %d full passes, want exactly 1", got)
	}
	entry, ok := f.past.Lookup(f.sessionID)
	if !ok || entry.Meta.Name != "Renamed by the client" {
		t.Fatalf("the rename is not in the index: name=%q found=%v", entry.Meta.Name, ok)
	}
}

// TestSweepRebuildDoesNotWaitBehindASlowRefresh is #6652: one goroutine running
// the rebuild and then the refresh let a refresh pass hold the past index, so a
// hub that dropped its search.db stopped seeing new sessions for as long as the
// fill took. The refresh here is held open and one tick then has to reach the
// rebuild anyway.
func TestSweepRebuildDoesNotWaitBehindASlowRefresh(t *testing.T) {
	f := newSweepFixture(t)
	sweep := newHubPastSweep(f.past, f.search)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	sweep.refresh = func(context.Context) {
		once.Do(func() { close(entered) })
		<-release
	}

	ticks := make(chan time.Time, 1)
	oldTicker := hubTicker
	hubTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	t.Cleanup(func() { hubTicker = oldTicker })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sweep.run(ctx, time.Hour)
	}()
	t.Cleanup(func() {
		cancel()
		close(release)
		<-done
	})

	<-entered
	// The refresh's first pass must not start before the rebuild that gives it
	// a session list: an empty list reads as "every indexed session is gone"
	// and Refresh forgets all of them.
	if sweep.rebuildCount() == 0 {
		t.Fatal("the first refresh pass ran before any rebuild; an empty list forgets every indexed session")
	}

	f.renameSession(t, "Renamed while the refresh was stuck")
	before := sweep.rebuildCount()
	ticks <- time.Now()

	deadline := time.Now().Add(10 * time.Second)
	for sweep.rebuildCount() == before {
		if time.Now().After(deadline) {
			t.Fatal("a tick's rebuild never ran while a refresh pass was held open; the refresh is blocking the past index")
		}
		time.Sleep(5 * time.Millisecond)
	}
	entry, ok := f.past.Lookup(f.sessionID)
	if !ok || entry.Meta.Name != "Renamed while the refresh was stuck" {
		t.Fatalf("the rename is not in the index: name=%q found=%v", entry.Meta.Name, ok)
	}
}
