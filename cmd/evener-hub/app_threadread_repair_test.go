package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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
// calls counts recipes the registry started. The outcome receives the refused
// build's name the registration carried, so a test can feed the recipe a
// stub that answers it.
func installRepairSeam(t *testing.T) (feed func(func(path, refused string) error), completions chan struct{}, calls *atomic.Int32) {
	t.Helper()
	calls = &atomic.Int32{}
	outcomes := make(chan func(path, refused string) error, 16)
	completions = make(chan struct{}, 16)
	old := runTranscriptIndexRepair
	oldRecorded := transcriptIndexRepairRecorded
	runTranscriptIndexRepair = func(path, refused string) error {
		calls.Add(1)
		outcome, ok := <-outcomes
		if !ok {
			return nil // the test finished; let the goroutine retire
		}
		return outcome(path, refused)
	}
	transcriptIndexRepairRecorded = func(string) { completions <- struct{}{} }
	feed = func(fn func(path, refused string) error) { outcomes <- fn }
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

// flipSidecarProjection rewrites the sidecar's committed projection meta to
// a foreign identity — the shape a week-old daemon leaves behind when a
// schema-fingerprint change lands. A projection string no binary here
// computes: readMeta rejects it, and the refusing handle refuses the
// rebuild. Returns the committed build's recorded entry count, the value a
// refusal carries and a history-failed answer names. Shared by
// staleSidecarForTest and the rendezvous-starvation storm's re-staler, so
// the make-it-refuse mechanism lives exactly once.
func flipSidecarProjection(path string) (entries uint64, err error) {
	dir := transcriptindex.DirFor(path)
	current, err := os.ReadFile(filepath.Join(dir, "CURRENT"))
	if err != nil {
		return 0, err
	}
	metaPath := filepath.Join(dir, strings.TrimSpace(string(current)), "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return 0, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return 0, err
	}
	if err := json.Unmarshal(fields["entries"], &entries); err != nil {
		return 0, err
	}
	fields["projection"] = json.RawMessage(`"transcript-read-model-v6:0000000000000000000000000000000000000000000000000000000000000ff"`)
	rewritten, err := json.Marshal(fields)
	if err != nil {
		return 0, err
	}
	return entries, os.WriteFile(metaPath, rewritten, 0o600)
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
	entries, err := flipSidecarProjection(path)
	if err != nil {
		t.Fatal(err)
	}
	if entries == 0 {
		t.Fatal("the seeded sidecar recorded no entries")
	}
	return entries - 1
}

// refusedBuildName reads the sidecar's committed build name — the identity a
// refusal of this sidecar carries and the armed repair revalidates its
// rebuild against, the same way the production refusal reads it.
func refusedBuildName(t *testing.T, path string) string {
	t.Helper()
	current, err := os.ReadFile(filepath.Join(transcriptindex.DirFor(path), "CURRENT"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(current))
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

// digitsAtEnd extracts the trailing integer an answer's message carries, so
// tests can pin the number without pinning the sentence around it.
var digitsAtEnd = regexp.MustCompile(`(\d+)\s*$`)

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
	// The trailing number is the machine value the answer carries; match it
	// without pinning any of the words around it (Copy Is Not a Test Oracle).
	tail := digitsAtEnd.FindStringSubmatch(wire.Message)
	if tail == nil {
		t.Fatalf("history-failed message = %q, want one ending in the refusal's entry ordinal", wire.Message)
	}
	if got, err := strconv.ParseUint(tail[1], 10, 64); err != nil || got != ordinal {
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

// The classifier's lock-contention split: a read that met the sidecar's lock
// with no repair registered answers the typed contention answer —
// retryable, nothing registered — so a client with no ordinary-error retry
// (the TUI's paced entry) still converges; the registry's own in-flight
// repair keeps the rebuilding answer, which names the repair the client is
// waiting on.
func TestAnswerPastTranscriptIndexErrorClassifiesLockContention(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, _ := installRepairSeam(t)
	_, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)

	// Nothing registered: ordinary contention answers the typed locked
	// answer, not the bare error.
	got := answerPastTranscriptIndexError(entry, transcriptindex.ErrLockBusy)
	wire, ok := errors.AsType[appwire.WireError](got)
	if !ok {
		t.Fatalf("no-repair contention answered %T %v, want the typed locked answer", got, got)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptIndexLocked {
		t.Fatalf("no-repair contention data = %#v, want the typed locked answer", wire.Data)
	}
	if data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("locked answer disposition = %v, want automatic", data.RetryDisposition)
	}

	// The registry's own repair holds the lock: the rebuilding answer.
	transcriptIndexRepairs.askFromRefresher(path, refusedBuildName(t, path))
	if !transcriptIndexRepairs.inFlight(path) {
		t.Fatal("setup: the refresher's ask armed no repair")
	}
	got = answerPastTranscriptIndexError(entry, transcriptindex.ErrLockBusy)
	wire, ok = errors.AsType[appwire.WireError](got)
	if !ok {
		t.Fatalf("in-flight contention answered %T %v, want the typed rebuilding answer", got, got)
	}
	data, ok = wire.Data.(appwire.ErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptIndexRebuilding {
		t.Fatalf("in-flight contention data = %#v, want the typed rebuilding answer", wire.Data)
	}

	// Let the parked repair retire so the registry is clean for the next test.
	feed(repairTranscriptIndex)
	<-completions
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
	// Exactly two lines for this repair, each naming this transcript — the
	// count is the behavior; the name is data. The calls counter above
	// already pins that exactly one repair ran (Copy Is Not a Test Oracle:
	// no assertion pins the sentences).
	base := filepath.Base(path)
	if len(logs) != 2 {
		t.Fatalf("repair log = %q, want exactly two lines for one repair", logs)
	}
	for _, line := range logs {
		if !strings.Contains(line, base) {
			t.Fatalf("repair log = %q, want every line naming %s", logs, base)
		}
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
	feed(func(p, refused string) error {
		release, err := transcriptindex.HoldSidecarLockForTest(transcriptindex.DirFor(p))
		if err != nil {
			return err
		}
		close(holding)
		<-held
		release()
		return repairTranscriptIndex(p, refused)
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

// A refusal's registration can land long after the refusing read answered —
// the refusal path's own header gate reads the transcript between them, a
// real window under load — and in that window another reader's repair can
// already have committed. The late registration still arms its repair; the
// recipe must adopt the committed outcome instead of rebuilding the healthy
// sidecar again, which would re-pay the full rebuild and rotate an
// incarnation readers hold. The refusal's identity — the build it stood on —
// is the revalidation key (#4497 r9 medium).
func TestADelayedRefusalLandingAfterACompletedRepairDoesNotRebuildAgain(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("first question"))),
	)
	staleSidecarForTest(t, path)
	// The identity both readers' refusals carry: the stale build's name.
	refused := refusedBuildName(t, path)

	// One reader's read registers, and the real recipe repairs: the sidecar
	// is healthy, the registry entry gone.
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)
	feed(repairTranscriptIndex)
	<-completions
	if got := calls.Load(); got != 1 {
		t.Fatalf("repairs = %d, want 1", got)
	}
	committed := refusedBuildName(t, path)
	if committed == refused {
		t.Fatal("the first repair did not commit a fresh build")
	}

	// The delayed registration — the second reader's refusal, answered after
	// the repair finished — arms the path's repair again, carrying the same
	// refused identity.
	if armed, _ := transcriptIndexRepairs.answerRead(path, refused); !armed {
		t.Fatal("the delayed registration did not arm a repair")
	}
	feed(repairTranscriptIndex)
	<-completions
	if got := calls.Load(); got != 2 {
		t.Fatalf("repairs = %d, want the delayed registration to run its repair", got)
	}
	if got := refusedBuildName(t, path); got != committed {
		t.Fatalf("the delayed repair rebuilt the healthy sidecar: CURRENT = %q, want the committed %q", got, committed)
	}

	// The committed sidecar the delayed repair adopted still reads.
	if _, err := readStaleSession(t, cfg, entry); err != nil {
		t.Fatalf("read after the delayed repair: %v", err)
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
	// The quarantine's structure, not its copy: the stub item — identified
	// by its machine ID, the API's own contract value — leads, and the five
	// real entries follow with their seeded payloads intact.
	if len(items) != 6 || items[0].ID != "item_unreadable_0" {
		t.Fatalf("post-repair items = %+v, want the quarantine stub leading five intact items", items)
	}
	for i, want := range []string{"word 1", "word 2", "word 3", "word 4", "word 5"} {
		got := items[i+1]
		if got.ID == "item_unreadable_"+strconv.Itoa(i) || !strings.HasSuffix(got.Text, want) {
			t.Fatalf("post-repair item %d = %+v, want the real entry %q", i+1, got, want)
		}
	}
}

// TestAVanishedBuildAnswersRebuildingAndHeals pins the read side of a
// sidecar whose build files disappeared out from under the CURRENT pointer
// that still names them: the read refuses as rebuild-required — a missing
// build is rebuild-fixable, not an access problem — the hub arms exactly one
// repair, and the rebuilt sidecar reads normally.
func TestAVanishedBuildAnswersRebuildingAndHeals(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	dir := transcriptindex.DirFor(path)
	name, err := os.ReadFile(filepath.Join(dir, "CURRENT"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, strings.TrimSpace(string(name)))); err != nil {
		t.Fatal(err)
	}

	_, err = readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)
	feed(repairTranscriptIndex)
	<-completions
	if got := calls.Load(); got != 1 {
		t.Fatalf("repairs = %d, want exactly one for the vanished build", got)
	}
	response, err := readStaleSession(t, cfg, entry)
	if err != nil {
		t.Fatalf("read after the repair: %v", err)
	}
	if items := flattenTestItems(response.Thread.Turns); len(items) != 1 || items[0].Text != "question" {
		t.Fatalf("post-repair items = %+v, want the transcript's one", items)
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
	if runtime.GOOS == "windows" {
		t.Skip("relies on a Unix file permission to make the header check fail transiently")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the file permission this test relies on")
	}
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

	// The refusal the read answered named the sidecar's live build; the
	// recipe revalidates against it exactly as the registered flow does.
	refused := refusedBuildName(t, path)
	err := repairTranscriptIndex(path, refused)
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
	if runtime.GOOS == "windows" {
		t.Skip("relies on a Unix file permission to force a real rebuild failure")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the file permission this test relies on")
	}
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

	refused := refusedBuildName(t, path)
	err := repairTranscriptIndex(path, refused)
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
	feed(func(p, refused string) error {
		if err := os.Remove(p); err != nil {
			return err
		}
		return repairTranscriptIndex(p, refused)
	})
	<-completions
	requireUnchangedSidecar(t, transcriptindex.DirFor(path), before)
}

func TestTheRepairFailureBudget(t *testing.T) {
	wireRepairsForTest(t)
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
		feed(func(_, _ string) error { return boom })
		<-completions
	}
	// The exhausted budget is pinned by what the registry does below — the
	// history-failed answer and the budget's recovery — not by the log
	// sentence announcing it (Copy Is Not a Test Oracle).

	// The read that re-arms the exhausted path answers history-failed itself,
	// and holds its re-armed repair in flight for a moment.
	holdRelease := make(chan struct{})
	held := make(chan struct{})
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptHistoryFailed(t, err, last)
	feed(func(_, _ string) error {
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

// A repair registered after the project's deletion — the refusal was obtained
// before it, and the delayed registration arrived once forget had already
// retired the entry — fails because the transcript is gone. The failure must
// complete the deletion cleanup instead of retaining a failed entry (and its
// queued asks) for a session that no longer exists (#4343 fast-follow low 1).
func TestADelayedRepairRegistrationAfterDeletionLeavesNoRegistryResidue(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("a"))),
	)
	staleSidecarForTest(t, path)

	// A read obtains the refusal before the deletion, and its repair
	// completes.
	_, err := readStaleSession(t, cfg, entry)
	requireTranscriptIndexRebuilding(t, err)
	feed(func(_, _ string) error { return nil })
	<-completions

	// The deletion: forget retires the entry and the transcript file goes.
	transcriptIndexRepairs.forget(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	// The delayed registration arrives carrying the pre-deletion refusal.
	// The lane takes it, the recipe fails because the transcript is gone,
	// and the failure must retire the resurrected entry and its queued asks.
	transcriptIndexRepairs.askFromRefresher(path, refusedBuildName(t, path))
	feed(func(_, _ string) error { return errors.New("repair failed on purpose") })
	<-completions

	transcriptIndexRepairs.mu.Lock()
	_, hasEntry := transcriptIndexRepairs.entries[path]
	queued := len(transcriptIndexRepairs.laneQueue)
	transcriptIndexRepairs.mu.Unlock()
	if hasEntry {
		t.Fatal("a repair that failed because the transcript is gone retained a registry entry for the deleted session")
	}
	if queued != 0 {
		t.Fatalf("retained asks = %d, want the deleted session's queued asks retired", queued)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("repairs = %d, want 2 (the pre-deletion repair and the delayed registration's)", got)
	}
}

// exhaustTheLaneBudget runs three failing refresher-fed repairs through the
// lane, takes the exhausted path's first unpaced re-arm and fails it too, and
// proves the next lane ask within the interval skips: the path is now paced,
// with the failed re-arm's timestamp to measure from.
func exhaustTheLaneBudget(t *testing.T, feed func(func(path, refused string) error), completions <-chan struct{}, path string) {
	t.Helper()
	boom := errors.New("repair failed on purpose")
	for range 3 {
		transcriptIndexRepairs.askFromRefresher(path, refusedBuildName(t, path))
		feed(func(_, _ string) error { return boom })
		<-completions
	}
	// The first re-arm after exhaustion is taken — nothing paces it yet —
	// and fails, recording the moment the interval measures from.
	transcriptIndexRepairs.askFromRefresher(path, refusedBuildName(t, path))
	feed(func(_, _ string) error { return boom })
	<-completions
	transcriptIndexRepairs.askFromRefresher(path, refusedBuildName(t, path))
	if transcriptIndexRepairs.inFlight(path) {
		t.Fatal("the lane took an exhausted path within the interval, want a skip")
	}
}

func TestTheRefresherLanePacesAnExhaustedPath(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	shrinkTranscriptRepairLaneIntervalForTest(t, 50*time.Millisecond)
	advance := freezeTranscriptRepairLaneClockForTest(t)
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

	// Within the interval — the frozen clock guarantees it against any
	// scheduling pause — the lane skips the path without recording anything.
	// askFromRefresher decides take-or-skip synchronously under the
	// registry's mutex, so the entry's in-flight flag is settled the moment
	// the ask returns.
	transcriptIndexRepairs.askFromRefresher(path, refusedBuildName(t, path))
	if transcriptIndexRepairs.inFlight(path) {
		t.Fatal("the lane took an exhausted path within the interval, want a skip")
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("repairs = %d, want the lane to skip an exhausted path within the interval", got)
	}

	// Once the interval has passed since the re-arm — advanced explicitly,
	// never slept past — the lane takes it again.
	advance(60 * time.Millisecond)
	transcriptIndexRepairs.askFromRefresher(path, refusedBuildName(t, path))
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
	// The frozen clock makes the adjacent asks below exactly within the
	// interval: the skip proves the interval stands when the stamp changes,
	// so the take that follows can only be the stamp's.
	shrinkTranscriptRepairLaneIntervalForTest(t, 500*time.Millisecond)
	_ = freezeTranscriptRepairLaneClockForTest(t)
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
	transcriptIndexRepairs.askFromRefresher(path, refusedBuildName(t, path))
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
	transcriptIndexRepairs.askFromRefresher(pathA, refusedBuildName(t, pathA))
	feed(func(_, _ string) error {
		close(held)
		<-release
		return nil
	})
	<-held

	// The lane is busy: the second path's ask skips.
	transcriptIndexRepairs.askFromRefresher(pathB, refusedBuildName(t, pathB))
	if got := calls.Load(); got != 1 {
		t.Fatalf("repairs = %d, want the lane to run one refresher-fed repair at a time", got)
	}

	// A client read of the second path starts its own repair immediately,
	// independent of the lane.
	_, err := readStaleSession(t, cfgB, entryB)
	requireTranscriptIndexRebuilding(t, err)

	close(release)
	<-completions // the lane's repair retires
	feed(func(_, _ string) error { return nil })
	<-completions // the client read's own repair retires too
	if got := calls.Load(); got != 2 {
		t.Fatalf("repairs = %d, want the client read's own repair beside the lane's", got)
	}
}

// A refresher ask that lands while the lane is busy is retained, not
// dropped: the lane takes the next retained ask as each repair retires, so
// an early session that goes stale every cycle cannot monopolize the single
// lane and leave the sessions behind it unindexed indefinitely (#4497 r10
// medium). Retention is deduplicated — one ask per path — and the newest
// ask's refused identity wins.
func TestARefresherAskWhileTheLaneIsBusyIsRetainedAndServeNext(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	_, _, pathA := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("a"))),
	)
	staleSidecarForTest(t, pathA)
	cfgB, entryB, pathB := seedIndexedPastSession(t,
		execution("turn_m2", schema.NewTurn(schema.TurnUserInput, llm.User("b"))),
	)
	staleSidecarForTest(t, pathB)

	// The lane takes the first ask and holds it: the second path's ask —
	// asked twice, to pin the dedupe — lands while the lane is busy.
	heldA := make(chan struct{})
	releaseA := make(chan struct{})
	transcriptIndexRepairs.askFromRefresher(pathA, refusedBuildName(t, pathA))
	feed(func(_, _ string) error {
		close(heldA)
		<-releaseA
		return nil
	})
	<-heldA
	transcriptIndexRepairs.askFromRefresher(pathB, refusedBuildName(t, pathB))
	transcriptIndexRepairs.askFromRefresher(pathB, refusedBuildName(t, pathB))
	feed(repairTranscriptIndex) // the retained ask's outcome: the real recipe

	close(releaseA)
	<-completions // the lane's held repair retires, taking the retained ask
	waitForRepairCalls(t, calls, 2)
	<-completions // the retained ask's repair retires
	if got := calls.Load(); got != 2 {
		t.Fatalf("repairs = %d, want 2: the twice-asked path served once", got)
	}

	// The retained ask ran the real recipe, so the later session's read
	// lands: the starvation the drop left behind is gone.
	if _, err := readStaleSession(t, cfgB, entryB); err != nil {
		t.Fatalf("read after the retained repair: %v", err)
	}
}

// The registry forgets a deleted session's retained ask with its entry: a
// repair completing after the deletion must not take — and re-register —
// the ask the deletion already retired.
func TestAForgetRetiresTheDeletedSessionsRetainedAsk(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	_, _, pathA := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("a"))),
	)
	staleSidecarForTest(t, pathA)
	_, _, pathB := seedIndexedPastSession(t,
		execution("turn_m2", schema.NewTurn(schema.TurnUserInput, llm.User("b"))),
	)
	staleSidecarForTest(t, pathB)

	heldA := make(chan struct{})
	releaseA := make(chan struct{})
	transcriptIndexRepairs.askFromRefresher(pathA, refusedBuildName(t, pathA))
	feed(func(_, _ string) error {
		close(heldA)
		<-releaseA
		return nil
	})
	<-heldA
	transcriptIndexRepairs.askFromRefresher(pathB, refusedBuildName(t, pathB))
	transcriptIndexRepairs.forget(pathB)

	close(releaseA)
	<-completions
	transcriptIndexRepairs.mu.Lock()
	queued := len(transcriptIndexRepairs.laneQueue)
	transcriptIndexRepairs.mu.Unlock()
	if queued != 0 {
		t.Fatalf("retained asks = %d, want the deleted session's ask retired", queued)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("repairs = %d, want the deleted session's retained ask never taken", got)
	}
}

// The take arms a retained ask under the ask-time contract, not around it:
// a path whose budget exhausted while queued is taken once — the take sets
// the lane re-arm stamp the interval measures from — and the ask within the
// interval afterwards skips, exactly as an ask-time re-arm would.
func TestTheTakeOfARetainedAskHonorsTheExhaustionPacing(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	_, _, pathA := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("a"))),
	)
	staleSidecarForTest(t, pathA)
	cfgB, entryB, pathB := seedIndexedPastSession(t,
		execution("turn_m2", schema.NewTurn(schema.TurnUserInput, llm.User("b"))),
	)
	staleSidecarForTest(t, pathB)

	heldA := make(chan struct{})
	releaseA := make(chan struct{})
	transcriptIndexRepairs.askFromRefresher(pathA, refusedBuildName(t, pathA))
	feed(func(_, _ string) error {
		close(heldA)
		<-releaseA
		return nil
	})
	<-heldA
	// The retained ask sits queued while its path exhausts the repair budget
	// through its own client-read repairs.
	transcriptIndexRepairs.askFromRefresher(pathB, refusedBuildName(t, pathB))
	boom := errors.New("repair failed on purpose")
	for range 3 {
		_, err := readStaleSession(t, cfgB, entryB)
		requireTranscriptIndexRebuilding(t, err)
		feed(func(_, _ string) error { return boom })
		<-completions
	}

	close(releaseA)
	<-completions // the lane's held repair retires, taking the retained ask
	feed(func(_, _ string) error { return boom })
	waitForRepairCalls(t, calls, 5)
	<-completions // the retained ask's lane repair fails within the budget
	if got := calls.Load(); got != 5 {
		t.Fatalf("repairs = %d, want the retained ask taken once (1 lane-held, 3 read-armed, 1 taken)", got)
	}

	// The take stamped the re-arm: the ask within the interval skips.
	transcriptIndexRepairs.askFromRefresher(pathB, refusedBuildName(t, pathB))
	if transcriptIndexRepairs.inFlight(pathB) {
		t.Fatal("the lane took an exhausted path within the interval, want a skip")
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("repairs = %d, want the paced ask to skip", got)
	}
}

// The take retires a retained ask whose path is already in flight: the
// path's own repair — a client read's — is serving it, and re-arming the
// lane would run two repairs for one path at once. The ask is dropped,
// not re-queued; the next cycle re-asks if the path is still stale.
func TestTheTakeRetiresARetainedAskWhosePathIsInFlight(t *testing.T) {
	wireRepairsForTest(t)
	feed, completions, calls := installRepairSeam(t)
	_, _, pathA := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("a"))),
	)
	staleSidecarForTest(t, pathA)
	cfgB, entryB, pathB := seedIndexedPastSession(t,
		execution("turn_m2", schema.NewTurn(schema.TurnUserInput, llm.User("b"))),
	)
	staleSidecarForTest(t, pathB)

	heldA := make(chan struct{})
	releaseA := make(chan struct{})
	transcriptIndexRepairs.askFromRefresher(pathA, refusedBuildName(t, pathA))
	feed(func(_, _ string) error {
		close(heldA)
		<-releaseA
		return nil
	})
	<-heldA
	// Retained while the lane holds A; then the path's own client read
	// arms its repair, serving the path without the lane.
	transcriptIndexRepairs.askFromRefresher(pathB, refusedBuildName(t, pathB))
	_, err := readStaleSession(t, cfgB, entryB)
	requireTranscriptIndexRebuilding(t, err)

	close(releaseA)
	<-completions // A retires; its take finds B in flight and retires the ask
	feed(func(_, _ string) error { return nil })
	<-completions // B's own repair retires
	transcriptIndexRepairs.mu.Lock()
	queued := len(transcriptIndexRepairs.laneQueue)
	transcriptIndexRepairs.mu.Unlock()
	if queued != 0 {
		t.Fatalf("retained asks = %d, want the in-flight path's ask retired un-taken", queued)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("repairs = %d, want 2: the lane must not re-arm a path its own repair is serving", got)
	}
}

// waitForRepairCalls bounds a wait for the recipe counter, so a red run
// fails fast instead of hanging on a completion that never comes.
func waitForRepairCalls(t *testing.T, calls *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("repairs = %d, want %d within the bounded wait", calls.Load(), want)
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
	feed(func(_, refused string) error { return repairTranscriptIndex(path, refused) })
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
	feed(func(_, refused string) error {
		close(held)
		<-release
		return repairTranscriptIndex(path, refused)
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

// freezeTranscriptRepairLaneClockForTest parks the registry's clock and
// returns the only way it advances: the test's explicit calls. Pacing
// boundaries then cannot be crossed by a scheduling pause.
func freezeTranscriptRepairLaneClockForTest(t *testing.T) func(time.Duration) {
	t.Helper()
	now := time.Now()
	old := transcriptIndexRepairNow
	transcriptIndexRepairNow = func() time.Time { return now }
	t.Cleanup(func() { transcriptIndexRepairNow = old })
	return func(advance time.Duration) { now = now.Add(advance) }
}

// TestADirectlyConstructedServerHealsAMissingSidecarOnItsOwn pins the
// construction contract the registry's arming hangs on: a server built the
// way an embedder builds it — NewWebServer and the RPC surface Handler()
// serves, no runMain — arms repairs through its own default lifetime, so a
// valid transcript with no sidecar at all recovers on the server's own
// instead of answering history-failed read after read with no repair
// anywhere.
func TestADirectlyConstructedServerHealsAMissingSidecarOnItsOwn(t *testing.T) {
	// Whatever a previous test's wiring left in the shared registry, this
	// one starts from nothing armed: the server's construction must do the
	// arming, not a test's.
	done, cancel := context.WithCancel(context.Background())
	cancel()
	transcriptIndexRepairs.wire(done)

	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("first question"))),
		execution("turn_m1", schema.NewTurn(schema.TurnAssistant, llm.Assistant("first answer"))),
	)
	// The never-indexed shape: the transcript stands, no sidecar at all.
	if err := os.RemoveAll(transcriptindex.DirFor(path)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// captureRepairLog keeps the real recipe's two advisory lines inside the
	// test instead of on the package's stderr.
	logged := captureRepairLog(t)
	web := NewWebServer(cfg)
	_ = web.Handler()
	// The registry's recorded callback — which fires once the outcome is
	// recorded and the log lines are written — is the first point where the
	// re-read can pass and the log is whole: the recipe publishes CURRENT
	// before it releases its lock and the registry records after, so a
	// pointer-watching test races the repair's tail (roborev PR #4497
	// round-25 medium 2).
	recorded := make(chan struct{}, 1)
	oldRecorded := transcriptIndexRepairRecorded
	transcriptIndexRepairRecorded = func(path string) {
		if oldRecorded != nil {
			oldRecorded(path)
		}
		recorded <- struct{}{}
	}
	t.Cleanup(func() { transcriptIndexRepairRecorded = oldRecorded })
	read := func() (appwire.ThreadReadResponse, error) {
		value, err := web.appRPC.Router().Dispatch(context.Background(), appwire.Request{
			ID: appwire.NewIntID(1), Method: appwire.MethodThreadRead,
			Params: mustPagingJSON(t, appwire.ThreadReadParams{
				Ref: "local:" + entry.Meta.ID, IncludeTurns: true, ItemLimit: 40, RequestGeneration: 1,
			}),
		})
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		response, ok := value.(appwire.ThreadReadResponse)
		if !ok {
			t.Fatalf("read answered %T, want ThreadReadResponse", value)
		}
		return response, nil
	}

	// The first read answers the retryable rebuilding state — a repair is
	// armed behind it, not the terminal history-failed.
	_, err := read()
	requireTranscriptIndexRebuilding(t, err)

	// The armed repair is the real recipe — no seam. The sidecar it commits
	// is the build's proof; the recorded callback orders what follows.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(transcriptindex.DirFor(path), "CURRENT")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the armed repair never built the sidecar")
		}
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case <-recorded:
	case <-time.After(10 * time.Second):
		t.Fatal("the armed repair never recorded its outcome")
	}
	if lines := logged(); len(lines) != 2 {
		t.Fatalf("repair log = %q, want exactly two lines for one repair", lines)
	}

	// Recovery, not a status: the re-read returns the transcript's data.
	response, err := read()
	if err != nil {
		t.Fatalf("read after the repair: %v", err)
	}
	if items := flattenTestItems(response.Thread.Turns); len(items) != 2 || items[0].Text != "first question" || items[1].Text != "first answer" {
		t.Fatalf("post-repair items = %+v, want the transcript's two", flattenTestItems(response.Thread.Turns))
	}
}

// TestDetachedRepairLoggingKeepsTheSinkTheRegistryWasArmedWith pins
// hubLogfFor's rule for the repair registry's own log: the sink resolves
// once, when the registry is armed, so a detached repair — one that can
// outlive the startup and shutdown windows where os.Stderr is redirected —
// never follows a later reassign of the mutable global into an unintended
// or closed sink.
func TestDetachedRepairLoggingKeepsTheSinkTheRegistryWasArmedWith(t *testing.T) {
	old := os.Stderr
	t.Cleanup(func() { os.Stderr = old })

	// The sink the arming must capture: a file the test reads back.
	armed, err := os.CreateTemp(t.TempDir(), "repair-sink-armed-")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = armed
	wireRepairsForTest(t)

	// The late redirect, after the arming — captureHubStderr's own shape.
	late, err := os.CreateTemp(t.TempDir(), "repair-sink-late-")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = late

	feed, completions, _ := installRepairSeam(t)
	path := filepath.Join(t.TempDir(), "late.transcript.jsonl")
	_, _ = transcriptIndexRepairs.answerRead(path, "")
	feed(func(path, refused string) error { return nil })
	<-completions
	os.Stderr = old
	if err := armed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := late.Close(); err != nil {
		t.Fatal(err)
	}

	redirected, err := os.ReadFile(late.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(redirected) != 0 {
		t.Fatalf("the repair log followed the late stderr reassign: %q", redirected)
	}
	logged, err := os.ReadFile(armed.Name())
	if err != nil {
		t.Fatal(err)
	}
	// The lines went to the armed sink instead: exactly two for one repair,
	// each naming this transcript — the count is the behavior, the name is
	// data, and no sentence is pinned.
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	if len(lines) != 2 {
		t.Fatalf("repair log = %q, want exactly two lines for one repair", string(logged))
	}
	base := filepath.Base(path)
	for _, line := range lines {
		if !strings.Contains(line, base) {
			t.Fatalf("repair log = %q, want every line naming %s", string(logged), base)
		}
	}
}
