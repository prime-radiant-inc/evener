package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// wireRepairsForTest arms the repair registry with the test's background
// context, the way hub startup wires it with the hub's.
func wireRepairsForTest(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	transcriptIndexRepairs.wire(ctx)
	return ctx
}

// installRepairSeam replaces the repair recipe with a queue the test feeds
// one outcome per repair: a repair with no outcome fed yet holds (in flight,
// doing nothing), and each fed outcome runs to the repair's completion.
// completions receives one signal per finished repair; calls counts recipes
// the registry started.
func installRepairSeam(t *testing.T) (feed func(func(path string) error), completions chan struct{}, calls *atomic.Int32) {
	t.Helper()
	calls = &atomic.Int32{}
	outcomes := make(chan func(string) error, 16)
	completions = make(chan struct{}, 16)
	old := runTranscriptIndexRepair
	runTranscriptIndexRepair = func(path string) error {
		calls.Add(1)
		outcome, ok := <-outcomes
		if !ok {
			return nil // the test finished; let the goroutine retire
		}
		err := outcome(path)
		completions <- struct{}{}
		return err
	}
	feed = func(fn func(path string) error) { outcomes <- fn }
	t.Cleanup(func() {
		runTranscriptIndexRepair = old
		close(outcomes)
	})
	return feed, completions, calls
}

// captureRepairLog replaces the repair registry's log line for the test's
// duration and returns the lines logged so far.
func captureRepairLog(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	old := transcriptIndexRepairLog
	transcriptIndexRepairLog = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { transcriptIndexRepairLog = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

// staleSidecarForTest rewrites the sidecar's committed projection to a foreign
// identity — the shape a week-old daemon leaves behind when a
// schema-fingerprint change lands. Rewritten after a healthy build rather
// than seeded through an open path, which would repair the sidecar. Returns
// the ordinal of the last entry the build recorded, the value a refusal
// carries and a history-failed answer names.
func staleSidecarForTest(t *testing.T, path string) uint64 {
	t.Helper()
	index, err := transcriptindex.Open(path, transcriptindex.DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	dir := transcriptindex.DirFor(path)
	current, err := os.ReadFile(filepath.Join(dir, "CURRENT"))
	if err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(dir, strings.TrimSpace(string(current)), "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	var entries uint64
	if err := json.Unmarshal(fields["entries"], &entries); err != nil {
		t.Fatal(err)
	}
	// A projection string no binary here computes: readMeta rejects it, and
	// the refusing handle refuses the rebuild.
	fields["projection"] = json.RawMessage(`"transcript-read-model-v6:0000000000000000000000000000000000000000000000000000000000000ff"`)
	rewritten, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	if entries == 0 {
		t.Fatal("the seeded sidecar recorded no entries")
	}
	return entries - 1
}

// corruptTranscriptEntry breaks the first entry's JSON in place: the text
// value's opening quote becomes a digit, so the lenient decoder the window
// read re-decodes with rejects the line. Length-preserving, and far outside
// the covered tail the grown-by-appends check sums, so catch-up stays healthy
// while the window read finds the committed index inconsistent.
func corruptTranscriptEntry(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	headerEnd := bytes.IndexByte(data, '\n')
	if headerEnd < 0 || headerEnd+1 >= len(data) {
		t.Fatal("transcript too short to corrupt")
	}
	quote := []byte(`"text":"`)
	at := bytes.Index(data[headerEnd:], quote)
	if at < 0 {
		t.Fatal("the first entry line carries no text value to corrupt")
	}
	data[headerEnd+at+len(quote)-1] = '1'
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// requireTranscriptIndexRebuilding pins the typed wire answer: the rebuilding
// code on the internal-error channel, retryable automatically, carrying the
// daemonless history identity.
func requireTranscriptIndexRebuilding(t testing.TB, err error) {
	t.Helper()
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("err = %T %v, want a WireError", err, err)
	}
	if wire.Code != appwire.CodeInternalError {
		t.Fatalf("rebuilding answer code = %v, want %v", wire.Code, appwire.CodeInternalError)
	}
	data, ok := wire.Data.(appwire.HistoryReadErrorData)
	if !ok {
		t.Fatalf("rebuilding answer data = %#v, want HistoryReadErrorData", wire.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorTranscriptIndexRebuilding {
		t.Fatalf("rebuilding answer info = %q, want %q", data.EvenerErrorInfo, appwire.ErrorTranscriptIndexRebuilding)
	}
	if data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("rebuilding retry disposition = %q, want automatic", data.RetryDisposition)
	}
	if data.BootGeneration != appwire.DaemonlessBootGeneration || data.Epoch == nil || *data.Epoch != 0 {
		t.Fatalf("rebuilding identity = boot %q epoch %v, want the daemonless generation and epoch 0", data.BootGeneration, data.Epoch)
	}
}

// requireTranscriptHistoryFailed pins the history-failed answer, including
// the ordinal its message names.
func requireTranscriptHistoryFailed(t testing.TB, err error, ordinal uint64) {
	t.Helper()
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("err = %T %v, want a WireError", err, err)
	}
	data, ok := wire.Data.(appwire.HistoryReadErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptHistoryFailed {
		t.Fatalf("history-failed data = %#v, want transcriptHistoryFailed", wire.Data)
	}
	if want := fmt.Sprintf("thread history failed at entry %d", ordinal); wire.Message != want {
		t.Fatalf("history-failed message = %q, want %q", wire.Message, want)
	}
	if data.BootGeneration != appwire.DaemonlessBootGeneration || data.Epoch == nil || *data.Epoch != 0 {
		t.Fatalf("history-failed identity = boot %q epoch %v, want the daemonless generation and epoch 0", data.BootGeneration, data.Epoch)
	}
}

// readStaleSession dispatches one daemonless thread/read of the seeded session.
func readStaleSession(t *testing.T, cfg hubcore.WebConfig, entry hubcore.PastEntry) (appwire.ThreadReadResponse, error) {
	t.Helper()
	return dispatchDaemonlessThreadRead(t, cfg, appwire.ThreadReadParams{
		Ref: "local:" + entry.Meta.ID, IncludeTurns: true, ItemLimit: 40, RequestGeneration: 1,
	})
}

func TestAStaleSidecarAnswersRebuildingAndRepairsInBackground(t *testing.T) {
	wireRepairsForTest(t)
	logged := captureRepairLog(t)
	feed, completions, calls := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("first question"))),
		execution("turn_m1", schema.NewTurn(schema.TurnAssistant, llm.Assistant("first answer"))),
	)
	last := staleSidecarForTest(t, path)
	_ = last

	// The first read answers the typed rebuilding error promptly — no inline
	// rebuild ran, or the read would have returned data.
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)

	// Exactly one repair runs; feed it the real recipe.
	feed(func(path string) error { return repairTranscriptIndex(path) })
	<-completions
	if got := calls.Load(); got != 1 {
		t.Fatalf("repairs = %d, want 1", got)
	}
	logs := logged()
	want := []string{
		fmt.Sprintf("transcript index repair started (%s)", filepath.Base(path)),
		fmt.Sprintf("transcript index repair finished (%s)", filepath.Base(path)),
	}
	if len(logs) != 2 || logs[0] != want[0] || logs[1] != want[1] {
		t.Fatalf("repair log = %q, want %q", logs, want)
	}

	// The retry after the repair returns correct data with today's shape.
	response, err := readStaleSession(t, cfg, entry)
	if err != nil {
		t.Fatalf("read after the repair: %v", err)
	}
	if !response.Authoritative || response.BootGeneration != appwire.DaemonlessBootGeneration || response.Snapshot == nil {
		t.Fatalf("post-repair read identity = %+v, want authoritative daemonless with a snapshot", response)
	}
	if items := flattenTestItems(response.Thread.Turns); len(items) != 2 || items[0].Text != "first question" || items[1].Text != "first answer" {
		t.Fatalf("post-repair items = %+v, want the transcript's two", items)
	}

	// Warm cache, stale again: the cached refusing handle refuses the same way.
	staleSidecarForTest(t, path)
	_, err = readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)
	feed(func(path string) error { return repairTranscriptIndex(path) })
	<-completions
	if got := calls.Load(); got != 2 {
		t.Fatalf("repairs = %d, want 2", got)
	}
	if _, err := readStaleSession(t, cfg, entry); err != nil {
		t.Fatalf("read after the second repair: %v", err)
	}
}

func TestConcurrentReadsDuringARepairAllAnswerRebuilding(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	staleSidecarForTest(t, path)

	// The repair holds the sidecar's exclusive lock, as its rebuild does.
	holding := make(chan struct{})
	held := make(chan struct{})
	feed(func(p string) error {
		release, err := transcriptindex.HoldSidecarLockForTest(transcriptindex.DirFor(p))
		if err != nil {
			return err
		}
		close(holding)
		<-held
		release()
		return repairTranscriptIndex(p)
	})
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)
	<-holding

	// Reads racing the repair's lock hold all answer the typed error — the
	// refusal branch and the busy branch's registry lookup alike — and no
	// second repair starts.
	const readers = 5
	errs := make(chan error, readers)
	for range readers {
		go func() {
			_, err := readStaleSession(t, cfg, entry)
			errs <- err
		}()
	}
	for range readers {
		requireTranscriptIndexRebuilding(t, <-errs)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("repairs = %d, want 1 for all concurrent readers", got)
	}

	close(held) // release the lock; the repair's rebuild runs
	<-completions
	if _, err := readStaleSession(t, cfg, entry); err != nil {
		t.Fatalf("read after the repair completed: %v", err)
	}
}

func TestLockContentionWithoutARepairIsAnOrdinaryTransient(t *testing.T) {
	wireRepairsForTest(t)
	_, _, calls := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	// A healthy sidecar with no repair anywhere: a foreign writer holds the
	// lock mid-extension.
	release, err := transcriptindex.HoldSidecarLockForTest(transcriptindex.DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = readStaleSession(t, cfg, entry)
	if err == nil {
		t.Fatal("read under a held lock succeeded")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("err = %T %v, want a WireError", err, err)
	}
	// The answer is an ordinary transient error: neither typed code. The
	// registry was never consulted, so no repair was registered.
	data, ok := wire.Data.(appwire.HistoryReadErrorData)
	if !ok {
		t.Fatalf("contention answer data = %#v, want the history-read identity stamp", wire.Data)
	}
	if data.EvenerErrorInfo == appwire.ErrorTranscriptIndexRebuilding {
		t.Fatal("lock contention with no repair in flight answered the rebuilding code")
	}
	if data.EvenerErrorInfo == appwire.ErrorTranscriptHistoryFailed {
		t.Fatal("lock contention with no repair in flight answered history-failed")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("repairs = %d, want none registered", got)
	}
	release()
	if _, err := readStaleSession(t, cfg, entry); err != nil {
		t.Fatalf("read after the contention cleared: %v", err)
	}
}

// TestTheRepairRebuildsRatherThanCatchingUp pins the repair's recipe on the
// trigger where a catch-up-only repair would no-op forever: a corrupt record
// the covered-prefix check cannot see. The repair must run the exported
// Rebuild, whose scan quarantines the corrupted entry.
func TestTheRepairRebuildsRatherThanCatchingUp(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, _ := installRepairSeam(t)
	var big []schema.Turn
	for i := range 6 {
		big = append(big, execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("word ", 200)+fmt.Sprint(i)))))
	}
	cfg, entry, path := seedIndexedPastSession(t, big...)
	staleSidecarForTest(t, path) // a committed build exists for the corruption to sit inside
	corruptTranscriptEntry(t, path)

	// The window read re-decodes the corrupted entry and refuses.
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)

	feed(func(path string) error { return repairTranscriptIndex(path) })
	<-completions

	// The repair rebuilt: the read succeeds with the corrupted entry's
	// quarantine leading the window.
	response, err := readStaleSession(t, cfg, entry)
	if err != nil {
		t.Fatalf("read after the repair: %v", err)
	}
	items := flattenTestItems(response.Thread.Turns)
	if len(items) != 6 || items[0].ID != "item_unreadable_0" || items[0].Description != "Unreadable transcript entry" {
		t.Fatalf("post-repair items = %+v, want the quarantine leading five intact items", items)
	}
}

func TestALegacyTranscriptAnswersHistoryFailedDirectly(t *testing.T) {
	wireRepairsForTest(t)
	_, _, calls := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotSidecarDir(t, transcriptindex.DirFor(path))
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptHistoryFailed(t, err, 0)
	if got := calls.Load(); got != 0 {
		t.Fatalf("repairs = %d, want none for a legacy transcript", got)
	}
	// Nothing new is written — no fresh build directory, no CURRENT change —
	// for a transcript that can never be indexed. The open's directory and
	// lock file are today's residue.
	requireUnchangedSidecar(t, transcriptindex.DirFor(path), before)
}

// requireUnchangedSidecar asserts the sidecar directory still holds exactly
// the entries snapshot recorded, with every file's bytes unchanged, so a test
// can pin that a read wrote nothing new into it.
func requireUnchangedSidecar(t *testing.T, dir string, before sidecarSnapshot) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Name()] = true
		if !before.names[e.Name()] {
			t.Fatalf("the sidecar gained a new %q", e.Name())
		}
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, before.files[e.Name()]) {
			t.Fatalf("the sidecar's %s changed", e.Name())
		}
	}
	for name := range before.names {
		if !seen[name] {
			t.Fatalf("the sidecar lost %q", name)
		}
	}
}

// sidecarSnapshot records a sidecar directory's shape: every entry's name,
// and the bytes of every file.
type sidecarSnapshot struct {
	names map[string]bool
	files map[string][]byte
}

func snapshotSidecarDir(t *testing.T, dir string) sidecarSnapshot {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := sidecarSnapshot{names: map[string]bool{}, files: map[string][]byte{}}
	for _, e := range entries {
		snapshot.names[e.Name()] = true
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		snapshot.files[e.Name()] = data
	}
	return snapshot
}

func TestAVanishedTranscriptAbortsTheRepairWithoutCreatingAnything(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, _ := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	staleSidecarForTest(t, path)
	before := snapshotSidecarDir(t, transcriptindex.DirFor(path))

	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)

	// The transcript vanishes while the repair is queued: the recipe stats it
	// first and aborts without creating anything.
	feed(func(p string) error {
		if err := os.Remove(p); err != nil {
			return err
		}
		return repairTranscriptIndex(p)
	})
	<-completions
	requireUnchangedSidecar(t, transcriptindex.DirFor(path), before)
}

func TestTheRepairFailureBudget(t *testing.T) {
	wireRepairsForTest(t)
	logged := captureRepairLog(t)
	feed, completions, calls := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	last := staleSidecarForTest(t, path)

	// Three consecutive failures exhaust the budget; each read before
	// exhaustion answers the rebuilding code.
	boom := errors.New("repair failed on purpose")
	for range 3 {
		_, err := readStaleSession(t, cfg, entry)
		requireTranscriptIndexRebuilding(t, err)
		feed(func(string) error { return boom })
		<-completions
	}
	base := filepath.Base(path)
	wantExhausted := fmt.Sprintf("transcript index repair budget exhausted (%s)", base)
	if logs := logged(); !slices.Contains(logs, wantExhausted) {
		t.Fatalf("repair log = %q, want %q among them", logs, wantExhausted)
	}

	// The read that re-arms the exhausted path answers history-failed itself,
	// and holds its re-armed repair in flight for a moment.
	holdRelease := make(chan struct{})
	held := make(chan struct{})
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptHistoryFailed(t, err, last)
	feed(func(string) error {
		close(held)
		<-holdRelease
		return nil
	})
	<-held

	// A concurrent read while the re-armed repair runs answers rebuilding:
	// the status stays truthful while a repair is actually in flight.
	concurrent := make(chan error, 1)
	go func() {
		_, err := readStaleSession(t, cfg, entry)
		concurrent <- err
	}()
	requireTranscriptIndexRebuilding(t, <-concurrent)

	// The re-armed repair succeeds and clears the budget.
	close(holdRelease)
	<-completions
	staleSidecarForTest(t, path)
	_, err = readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)
	feed(func(path string) error { return repairTranscriptIndex(path) })
	<-completions
	if got := calls.Load(); got != 5 {
		t.Fatalf("repairs = %d, want 5 (three failures, one held re-arm, one fresh)", got)
	}
	if _, err := readStaleSession(t, cfg, entry); err != nil {
		t.Fatalf("read after the successful re-arm: %v", err)
	}
}

func TestTheRefresherLanePacesAnExhaustedPath(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	shrinkTranscriptRepairLaneIntervalForTest(t, 50*time.Millisecond)
	_, _, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	staleSidecarForTest(t, path)

	// Exhaust the path's budget through the refresher's lane.
	boom := errors.New("repair failed on purpose")
	for range 3 {
		transcriptIndexRepairs.askFromRefresher(path)
		feed(func(string) error { return boom })
		<-completions
	}

	// The exhausted path's first lane re-arm is taken — there is no previous
	// re-arm to pace against — and fails again.
	transcriptIndexRepairs.askFromRefresher(path)
	feed(func(string) error { return boom })
	<-completions
	if got := calls.Load(); got != 4 {
		t.Fatalf("repairs = %d, want the first exhausted re-arm taken", got)
	}

	// Within the interval, the lane skips the path without recording
	// anything.
	transcriptIndexRepairs.askFromRefresher(path)
	time.Sleep(10 * time.Millisecond)
	if got := calls.Load(); got != 4 {
		t.Fatalf("repairs = %d, want the lane to skip an exhausted path within the interval", got)
	}

	// Once the interval has passed since the re-arm, the lane takes it
	// again.
	time.Sleep(60 * time.Millisecond)
	transcriptIndexRepairs.askFromRefresher(path)
	feed(func(path string) error { return repairTranscriptIndex(path) })
	<-completions
	if got := calls.Load(); got != 5 {
		t.Fatalf("repairs = %d, want the lane to re-arm after the interval", got)
	}
}

func TestARefresherFedRepairHoldsTheLaneToOneAtATime(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	_, _, pathA := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("a"))),
	)
	staleSidecarForTest(t, pathA)
	cfgB, entryB, pathB := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("b"))),
	)
	staleSidecarForTest(t, pathB)

	held := make(chan struct{})
	release := make(chan struct{})
	transcriptIndexRepairs.askFromRefresher(pathA)
	feed(func(string) error {
		close(held)
		<-release
		return nil
	})
	<-held

	// The lane is busy: the second path's ask skips.
	transcriptIndexRepairs.askFromRefresher(pathB)
	if got := calls.Load(); got != 1 {
		t.Fatalf("repairs = %d, want the lane to run one refresher-fed repair at a time", got)
	}

	// A client read of the second path starts its own repair immediately,
	// independent of the lane.
	_, err := readStaleSession(t, cfgB, entryB)
	requireTranscriptIndexRebuilding(t, err)

	close(release)
	<-completions // the lane's repair retires
	feed(func(string) error { return nil })
	<-completions // the client read's own repair retires too
	if got := calls.Load(); got != 2 {
		t.Fatalf("repairs = %d, want the client read's own repair beside the lane's", got)
	}
}

// The wiring hub startup installs (main.go): a refresh whose refusing read
// skips a session registers the shared repair through the wired lane
// callback — the real registry behind the real MessageSearch, not the
// hubcore test's local mirror — and the session's next cycle indexes it
// once the repair commits.
func TestARefresherRefusalRegistersThroughTheWiredLane(t *testing.T) {
	ctx := wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	_, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("settle the lane question"))),
	)
	staleSidecarForTest(t, path)

	search, err := hubcore.OpenMessageSearch(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = search.Close() })
	// The exact wiring hub startup installs (main.go).
	search.SetRepairAsker(transcriptIndexRepairs.askFromRefresher)

	// The lane's repair runs the real recipe when fed, so the refusal's
	// registration is observable as that repair completing.
	feed(func(string) error { return repairTranscriptIndex(path) })
	sessions := []hubcore.MessageSearchSession{{ID: entry.Meta.ID, TranscriptPath: path}}

	// First cycle: the refusing read skips the session — nothing indexed,
	// no failure recorded — and registers exactly one lane repair.
	if _, err := search.Refresh(ctx, sessions); err != nil {
		t.Fatal(err)
	}
	<-completions
	if got := calls.Load(); got != 1 {
		t.Fatalf("repairs = %d, want the refusal to register exactly one lane repair", got)
	}
	if matches, _ := search.Match(ctx, "settle", 3); len(matches) != 0 {
		t.Fatalf("settle matches = %+v, want none while the sidecar is stale", matches)
	}

	// Second cycle: the repaired sidecar indexes the session's messages.
	if _, err := search.Refresh(ctx, sessions); err != nil {
		t.Fatal(err)
	}
	matches, err := search.Match(ctx, "settle", 3)
	if err != nil {
		t.Fatal(err)
	}
	if matches[sessions[0].ID].Count != 1 {
		t.Fatalf("settle matches = %+v, want the repaired session indexed", matches)
	}
}

func TestNoRepairStartsOnceTheBackgroundContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transcriptIndexRepairs.wire(ctx)
	_, _, calls := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	staleSidecarForTest(t, path)

	cancel()
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptHistoryFailed(t, err, staleSidecarOrdinal(t, path))
	if got := calls.Load(); got != 0 {
		t.Fatalf("repairs = %d, want none once the background context is done", got)
	}
}

func TestProjectDeletionDropsTheRepairEntry(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, _ := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	staleSidecarForTest(t, path)

	held := make(chan struct{})
	release := make(chan struct{})
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)
	feed(func(string) error {
		close(held)
		<-release
		return repairTranscriptIndex(path)
	})
	<-held

	// The project's deletion drops the registry entry while the repair runs.
	if err := (&WebServer{}).removeProjectDeletionArtifacts(entry.StateDir, entry.Meta.ID); err != nil {
		t.Fatal(err)
	}
	transcriptIndexRepairs.mu.Lock()
	_, known := transcriptIndexRepairs.entries[path]
	transcriptIndexRepairs.mu.Unlock()
	if known {
		t.Fatal("the deletion left the path's repair entry behind")
	}

	// The in-flight repair finishing after the deletion records nothing: no
	// recreated entry.
	close(release)
	<-completions
	transcriptIndexRepairs.mu.Lock()
	_, known = transcriptIndexRepairs.entries[path]
	transcriptIndexRepairs.mu.Unlock()
	if known {
		t.Fatal("the repair's completion recreated the deleted path's entry")
	}
}

func TestANeverIndexedSessionAnswersRebuildingOnFirstRead(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	// No sidecar at all: the uniform path arms the repair while the first
	// read answers rebuilding.
	if err := os.RemoveAll(transcriptindex.DirFor(path)); err != nil {
		t.Fatal(err)
	}
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)
	feed(func(path string) error { return repairTranscriptIndex(path) })
	<-completions
	if got := calls.Load(); got != 1 {
		t.Fatalf("repairs = %d, want 1", got)
	}
	response, err := readStaleSession(t, cfg, entry)
	if err != nil {
		t.Fatalf("retry after the first repair: %v", err)
	}
	if items := flattenTestItems(response.Thread.Turns); len(items) != 1 || items[0].Text != "question" {
		t.Fatalf("retry items = %+v, want the session's one", items)
	}
}

func TestALiveWriterObservesTheRepairHoldAsAStallAndSurvivesIt(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, _ := installRepairSeam(t)
	var big []schema.Turn
	for i := range 3 {
		big = append(big, execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("word ", 200)+fmt.Sprint(i)))))
	}
	cfg, entry, path := seedIndexedPastSession(t, big...)
	staleSidecarForTest(t, path)

	// The repair holds the sidecar's lock as its rebuild would.
	holding := make(chan struct{})
	held := make(chan struct{})
	feed(func(p string) error {
		release, err := transcriptindex.HoldSidecarLockForTest(transcriptindex.DirFor(p))
		if err != nil {
			return err
		}
		close(holding)
		<-held
		release()
		return repairTranscriptIndex(p)
	})
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)
	<-holding

	// A live writer extends through the same sidecar: its blocking extension
	// stalls on the repair's hold and completes after the release.
	extension := make(chan error, 1)
	go func() {
		writer, err := transcriptindex.Open(path, transcriptindex.DirFor(path))
		if err != nil {
			extension <- err
			return
		}
		defer writer.Close() //nolint:errcheck // test writer
		extension <- writer.CatchUp()
	}()
	time.Sleep(10 * time.Millisecond) // let the writer reach the lock
	close(held)
	<-completions
	if err := <-extension; err != nil {
		t.Fatalf("the writer's extension after the repair's hold: %v", err)
	}

	// Records stay consistent: the hub's next read describes the transcript.
	response, err := readStaleSession(t, cfg, entry)
	if err != nil {
		t.Fatalf("read after the repair and the extension: %v", err)
	}
	if items := flattenTestItems(response.Thread.Turns); len(items) != 3 {
		t.Fatalf("items after the repair and the extension = %+v, want the transcript's three", items)
	}
}

// staleSidecarOrdinal reads the sidecar's last recorded entry ordinal, for
// asserting the history-failed answer's message.
func staleSidecarOrdinal(t *testing.T, path string) uint64 {
	t.Helper()
	dir := transcriptindex.DirFor(path)
	current, err := os.ReadFile(filepath.Join(dir, "CURRENT"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, strings.TrimSpace(string(current)), "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	var entries uint64
	if err := json.Unmarshal(fields["entries"], &entries); err != nil {
		t.Fatal(err)
	}
	if entries == 0 {
		t.Fatal("the sidecar recorded no entries")
	}
	return entries - 1
}

// shrinkTranscriptRepairLaneIntervalForTest shrinks the lane's exhausted-path
// pacing for the test's duration.
func shrinkTranscriptRepairLaneIntervalForTest(t *testing.T, d time.Duration) func() {
	t.Helper()
	old := transcriptRepairLaneExhaustedInterval
	transcriptRepairLaneExhaustedInterval = d
	return func() { transcriptRepairLaneExhaustedInterval = old }
}
