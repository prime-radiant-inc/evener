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
	"strconv"
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
// completions receives one signal per finished repair — fired from the
// registry's post-recording hook, so a received signal means the outcome is
// recorded and the log lines written, never merely that the recipe returned;
// calls counts recipes the registry started.
func installRepairSeam(t *testing.T) (feed func(func(path string) error), completions chan struct{}, calls *atomic.Int32) {
	t.Helper()
	calls = &atomic.Int32{}
	outcomes := make(chan func(string) error, 16)
	completions = make(chan struct{}, 16)
	old := runTranscriptIndexRepair
	oldRecorded := transcriptIndexRepairRecorded
	runTranscriptIndexRepair = func(path string) error {
		calls.Add(1)
		outcome, ok := <-outcomes
		if !ok {
			return nil // the test finished; let the goroutine retire
		}
		return outcome(path)
	}
	transcriptIndexRepairRecorded = func(string) { completions <- struct{}{} }
	feed = func(fn func(path string) error) { outcomes <- fn }
	t.Cleanup(func() {
		runTranscriptIndexRepair = old
		transcriptIndexRepairRecorded = oldRecorded
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
// requireTranscriptHistoryFailedWithoutIdentity asserts the history-failed
// wire error and its entry-naming message, in either the wire-stamped shape
// or the plain one a raw production call answers before the wire layer stamps
// the daemonless read identity onto it.
func requireTranscriptHistoryFailedWithoutIdentity(t testing.TB, err error, ordinal uint64) {
	t.Helper()
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("err = %T %v, want a WireError", err, err)
	}
	var info appwire.ErrorInfo
	switch data := wire.Data.(type) {
	case appwire.HistoryReadErrorData:
		info = data.EvenerErrorInfo
	case appwire.ErrorData:
		info = data.EvenerErrorInfo
	default:
		t.Fatalf("history-failed data = %#v, want transcriptHistoryFailed in either shape", wire.Data)
		return
	}
	if info != appwire.ErrorTranscriptHistoryFailed {
		t.Fatalf("history-failed data = %#v, want transcriptHistoryFailed", wire.Data)
	}
	// The ordinal is the machine value the message carries; match it without
	// pinning the wording around it (Copy Is Not a Test Oracle).
	i := strings.LastIndex(wire.Message, "entry ")
	if i < 0 {
		t.Fatalf("history-failed message = %q, want one naming an entry ordinal", wire.Message)
	}
	got, err := strconv.ParseUint(wire.Message[i+len("entry "):], 10, 64)
	if err != nil || got != ordinal {
		t.Fatalf("history-failed message = %q, want the refusal's entry %d named", wire.Message, ordinal)
	}
}

// isTranscriptHistoryFailed reports whether err carries the terminal
// history-failed answer, in either wire shape, so a test can pin its absence.
func isTranscriptHistoryFailed(err error) bool {
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		return false
	}
	switch data := wire.Data.(type) {
	case appwire.HistoryReadErrorData:
		return data.EvenerErrorInfo == appwire.ErrorTranscriptHistoryFailed
	case appwire.ErrorData:
		return data.EvenerErrorInfo == appwire.ErrorTranscriptHistoryFailed
	}
	return false
}

func requireTranscriptHistoryFailed(t testing.TB, err error, ordinal uint64) {
	t.Helper()
	requireTranscriptHistoryFailedWithoutIdentity(t, err, ordinal)
	wire, _ := errors.AsType[appwire.WireError](err)
	data, ok := wire.Data.(appwire.HistoryReadErrorData)
	if !ok {
		t.Fatalf("history-failed data = %#v, want the wire-stamped shape a raw call has not yet received", wire.Data)
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
	feed(repairTranscriptIndex)
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
	feed(repairTranscriptIndex)
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
// the covered-prefix check cannot see. The sidecar stays identity-healthy —
// the corruption is the SOLE trigger — so the read's catch-up succeeds, the
// window read re-decodes the corrupted entry and refuses, and only a repair
// that rebuilds from the transcript (whose scan quarantines the corrupted
// entry) heals it. A catch-up-only recipe no-ops here: the corruption stays,
// and the post-repair read refuses again, failing this test.
func TestTheRepairRebuildsRatherThanCatchingUp(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, _ := installRepairSeam(t)
	var big []schema.Turn
	for i := range 6 {
		big = append(big, execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("word ", 200)+strconv.Itoa(i)))))
	}
	cfg, entry, path := seedIndexedPastSession(t, big...)
	corruptTranscriptEntry(t, path)

	// The catch-up over the healthy sidecar succeeds; the window read
	// re-decodes the corrupted entry and refuses.
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)

	feed(repairTranscriptIndex)
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

// TestAHeaderCheckFailureStaysRetriable pins the gate's transient corner: a
// transcript the header check cannot diagnose right now — unreadable
// permissions here — answers an ordinary retriable error, never the terminal
// history-failed, and the read converges once the condition clears.
func TestAHeaderCheckFailureStaysRetriable(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	staleSidecarForTest(t, path)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0o644) }()

	_, err := readStaleSession(t, cfg, entry)
	if err == nil {
		t.Fatal("read of a transcript with unreadable permissions should fail")
	}
	if isTranscriptHistoryFailed(err) {
		t.Fatalf("transient header-check failure answered terminal history-failed: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("repairs = %d, want none armed while the transcript cannot be diagnosed", got)
	}

	// The condition clears: the gate passes, a repair arms, and the read
	// answers the typed retryable rebuilding code. Driven to completion, the
	// read converges.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)
	feed(repairTranscriptIndex)
	<-completions
	if got := calls.Load(); got != 1 {
		t.Fatalf("repairs = %d, want exactly one after the condition clears", got)
	}
	response, err := readStaleSession(t, cfg, entry)
	if err != nil {
		t.Fatalf("read after the repair: %v", err)
	}
	if items := flattenTestItems(response.Thread.Turns); len(items) != 1 || items[0].Text != "question" {
		t.Fatalf("post-repair items = %+v, want the transcript's one", items)
	}
}

// TestARepairThatLosesTheTranscriptToDeletionLeavesNoSidecar pins the race
// the recipe's initial stat cannot close: a deletion can remove the
// transcript and its sidecar directory between the stat and the open, and
// the open's MkdirAll then resurrects the directory. A rebuild that failed
// with the transcript gone must finish deletion's contract instead of
// leaving the resurrected artifacts behind.
func TestARepairThatLosesTheTranscriptToDeletionLeavesNoSidecar(t *testing.T) {
	_, _, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	staleSidecarForTest(t, path)
	dir := transcriptindex.DirFor(path)

	old := transcriptIndexRepairPreOpen
	transcriptIndexRepairPreOpen = func(p string) {
		_ = os.Remove(p)
		_ = os.RemoveAll(transcriptindex.DirFor(p))
	}
	t.Cleanup(func() { transcriptIndexRepairPreOpen = old })

	err := repairTranscriptIndex(path)
	if err == nil {
		t.Fatal("the rebuild should fail with the transcript deleted mid-repair")
	}
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Fatalf("sidecar dir %s survived the failed repair, want deletion's contract intact", dir)
	}
}

// TestAFailedRepairKeepsTheSidecarWhenTheTranscriptSurvives pins the cleanup's
// boundary: it exists to finish deletion, so a rebuild that fails while the
// transcript still exists leaves the sidecar directory exactly as the
// in-place failure path always has.
func TestAFailedRepairKeepsTheSidecarWhenTheTranscriptSurvives(t *testing.T) {
	_, _, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	staleSidecarForTest(t, path)
	dir := transcriptindex.DirFor(path)

	// A transcript the rebuild cannot read still exists — the cleanup must
	// leave the sidecar directory to the in-place failure path.
	old := transcriptIndexRepairPreOpen
	transcriptIndexRepairPreOpen = func(p string) {
		if err := os.Chmod(p, 0o000); err != nil {
			t.Errorf("chmod: %v", err)
		}
	}
	t.Cleanup(func() {
		transcriptIndexRepairPreOpen = old
		_ = os.Chmod(path, 0o644)
	})

	err := repairTranscriptIndex(path)
	if err == nil {
		t.Fatal("the rebuild should fail with an unreadable transcript")
	}
	if _, serr := os.Stat(dir); serr != nil {
		t.Fatalf("sidecar dir %s was removed, want it kept while the transcript exists", dir)
	}
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
	feed(repairTranscriptIndex)
	<-completions
	if got := calls.Load(); got != 5 {
		t.Fatalf("repairs = %d, want 5 (three failures, one held re-arm, one fresh)", got)
	}
	if _, err := readStaleSession(t, cfg, entry); err != nil {
		t.Fatalf("read after the successful re-arm: %v", err)
	}
}

// exhaustTheLaneBudget runs three failing refresher-fed repairs through the
// lane, takes the exhausted path's first unpaced re-arm and fails it too, and
// proves the next lane ask within the interval skips: the path is now paced,
// with the failed re-arm's timestamp to measure from.
func exhaustTheLaneBudget(t *testing.T, feed func(func(path string) error), completions <-chan struct{}, path string) {
	t.Helper()
	boom := errors.New("repair failed on purpose")
	for range 3 {
		transcriptIndexRepairs.askFromRefresher(path)
		feed(func(string) error { return boom })
		<-completions
	}
	// The first re-arm after exhaustion is taken — nothing paces it yet —
	// and fails, recording the moment the interval measures from.
	transcriptIndexRepairs.askFromRefresher(path)
	feed(func(string) error { return boom })
	<-completions
	transcriptIndexRepairs.askFromRefresher(path)
	if transcriptIndexRepairs.inFlight(path) {
		t.Fatal("the lane took an exhausted path within the interval, want a skip")
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

	// Exhaust the path's budget through the refresher's lane; the first
	// exhausted re-arm is taken and fails again.
	exhaustTheLaneBudget(t, feed, completions, path)
	if got := calls.Load(); got != 4 {
		t.Fatalf("repairs = %d, want the first exhausted re-arm taken", got)
	}

	// Within the interval, the lane skips the path without recording
	// anything. askFromRefresher decides take-or-skip synchronously under
	// the registry's mutex, so the entry's in-flight flag is settled the
	// moment the ask returns — no sleep races a late take.
	transcriptIndexRepairs.askFromRefresher(path)
	if transcriptIndexRepairs.inFlight(path) {
		t.Fatal("the lane took an exhausted path within the interval, want a skip")
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("repairs = %d, want the lane to skip an exhausted path within the interval", got)
	}

	// Once the interval has passed since the re-arm, the lane takes it
	// again.
	time.Sleep(60 * time.Millisecond)
	transcriptIndexRepairs.askFromRefresher(path)
	feed(repairTranscriptIndex)
	<-completions
	if got := calls.Load(); got != 5 {
		t.Fatalf("repairs = %d, want the lane to re-arm after the interval", got)
	}
}

// TestTheLanePacesAnExhaustedPathOnlyWhileItsStampStands pins the exhausted
// pacing to the failure budget's own key: the transcript's (size, mtime)
// stamp. A transcript that changed since the failures were counted is no
// longer exhausted, so the lane re-arms it immediately instead of pacing it
// against failures some earlier state of the file accumulated.
func TestTheLanePacesAnExhaustedPathOnlyWhileItsStampStands(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	// The interval is wide enough that the two adjacent asks below cannot
	// straddle its expiry: the skip proves the interval stands when the
	// stamp changes, so the take that follows can only be the stamp's.
	shrinkTranscriptRepairLaneIntervalForTest(t, 500*time.Millisecond)
	_, _, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	staleSidecarForTest(t, path)

	// Exhaust the path's budget through the refresher's lane, take one paced
	// re-arm so the interval stands, and prove the next ask skips.
	exhaustTheLaneBudget(t, feed, completions, path)

	// The transcript moves on — a live daemon appended, or the file was
	// rewritten. The failures were counted against another state of the
	// file, so the budget no longer applies and the lane must re-arm now.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	transcriptIndexRepairs.askFromRefresher(path)
	if !transcriptIndexRepairs.inFlight(path) {
		t.Fatal("the lane paced a path whose transcript changed, want an immediate re-arm")
	}
	feed(repairTranscriptIndex)
	<-completions
	if got := calls.Load(); got != 5 {
		t.Fatalf("repairs = %d, want 5 (three failures, one paced re-arm, one after the stamp change)", got)
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
	failures, err := search.Refresh(ctx, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Fatalf("failures = %+v, want none: the refusal skips the session, it does not fail it", failures)
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
	feed(repairTranscriptIndex)
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
		big = append(big, execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("word ", 200)+strconv.Itoa(i)))))
	}
	cfg, entry, path := seedIndexedPastSession(t, big...)
	staleSidecarForTest(t, path)

	// The real recipe heals the sidecar first: a live writer arrives after a
	// repair, on a sidecar its own catch-up can extend from — not on the
	// stale one, whose catch-up would be a full rebuild racing the repair
	// instead of the stall this test means to observe.
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)
	feed(repairTranscriptIndex)
	<-completions

	// The live writer's own turn, appended raw: extending to it must be a
	// genuine incremental extension, the one the repair's incarnation stands.
	appendTranscriptEntries(t, path,
		execution("turn_m2", schema.NewTurn(schema.TurnUserInput, llm.User("the writer's own message"))))
	before := sidecarIncarnation(t, path)

	// The repair holds the sidecar's lock as its rebuild would.
	release, err := transcriptindex.HoldSidecarLockForTest(transcriptindex.DirFor(path))
	if err != nil {
		t.Fatal(err)
	}

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
	select {
	case err := <-extension:
		t.Fatalf("the writer's extension completed while the repair held the lock: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	release()
	if err := <-extension; err != nil {
		t.Fatalf("the writer's extension after the repair's hold: %v", err)
	}
	if after := sidecarIncarnation(t, path); after != before {
		t.Fatalf("the sidecar's incarnation moved from %q to %q: the writer rebuilt instead of extending", before, after)
	}

	// Records stay consistent: the hub's next read describes the transcript,
	// the repair's three entries plus the extension's fourth.
	response, err := readStaleSession(t, cfg, entry)
	if err != nil {
		t.Fatalf("read after the repair and the extension: %v", err)
	}
	if items := flattenTestItems(response.Thread.Turns); len(items) != 4 {
		t.Fatalf("items after the repair and the extension = %+v, want the transcript's four", items)
	}
}

// sidecarIncarnation reads the sidecar build's incarnation, the identity a
// genuine incremental extension keeps and a rebuild mints anew.
func sidecarIncarnation(t *testing.T, path string) string {
	t.Helper()
	index, err := transcriptindex.OpenRefusing(path, transcriptindex.DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Close() }()
	incarnation, err := index.Incarnation()
	if err != nil {
		t.Fatal(err)
	}
	return incarnation
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
// pacing for the test's duration and restores the production default on
// cleanup, so the shrunk value never leaks into later tests in this package.
func shrinkTranscriptRepairLaneIntervalForTest(t *testing.T, d time.Duration) {
	t.Helper()
	old := transcriptRepairLaneExhaustedInterval
	transcriptRepairLaneExhaustedInterval = d
	t.Cleanup(func() { transcriptRepairLaneExhaustedInterval = old })
}
