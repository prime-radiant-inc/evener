//go:build browserguard

// TestWarmReturnTranscriptBrowser is the real-Chrome acceptance proof for
// #7683: a warm tab reopen of a background-warmed session must not publish the
// background lease's pruned notice-only display ahead of the local restore that
// supplies the recorded bodies.
//
// It drives the REAL hub web app (real AppWire, real stores, native IndexedDB)
// in real Chrome in Vite development mode through a REAL hub (httptest around
// web.Handler() with the real roster prober and past index) against a REAL
// `evener serve` daemon (cmd/evener's compiled browserguard helper binary),
// with the only scripted piece at the external LLM provider adapter.
//
// The warm state is ordinary: the daemon records one turn while no pane is
// open, the app's ordinary background lease hydrates it through the real
// AppWire, commits the recorded bodies to the real entry cache database, and
// releases them from display. The driver then opens the pane and asserts only
// STRUCTURAL COUNTS of the committed models and of the hub's AppWire trace. No
// transcript text, session identifier or screenshot enters a milestone.
//
// This is a disjoint new guard: it neither edits nor runs #7684's
// backgroundtranscriptguard runner, and it registers no shared fixture hunk.
// The build tag keeps Chrome out of the default suites.
package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/rendezvous"
)

func TestWarmReturnTranscriptBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("browser guard requires a real Chrome and real daemons")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("node is required for the warm-return browser guard: %v", err)
	}
	requireFreshDist(t)

	fixture := skillGuardSetup(t)
	// Helper alpha keeps its session empty: it is the control proving the hold
	// does not apply to a session that owes no recorded body. Helper beta's
	// target turn is the recorded session the warm reopen restores.
	for _, command := range []map[string]string{
		{"command": "background-mode"},
		{"command": "background-role", "phase": "target"},
	} {
		if err := appendFileLine(fixture.control[1], command); err != nil {
			t.Fatalf("arm warm-return fixture helper beta: %v", err)
		}
	}
	if err := appendFileLine(fixture.control[0], map[string]string{"command": "background-mode"}); err != nil {
		t.Fatalf("arm warm-return fixture helper alpha: %v", err)
	}
	var entries [2]rendezvous.Entry
	for i := range entries {
		entries[i] = skillGuardStartHelper(t, fixture, i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), backgroundDriverTimeout)
	defer cancel()
	roster := hubcore.NewRoster(fixture.runDir, &hubcore.StatusProber{Timeout: 500 * time.Millisecond})
	if err := roster.RefreshAndWait(ctx); err != nil {
		t.Fatalf("roster refresh: %v", err)
	}
	past := newRebuiltPastIndex(t, filepath.Join(fixture.stateRoot, "projects", "*"))
	web, authToken := startBackgroundGuardHub(t, fixture, past, roster)
	handler := http.NewServeMux()
	handler.Handle("/", web.Handler())
	hub := httptest.NewServer(handler)
	defer hub.Close()
	milestones := filepath.Join(fixture.artifact, "milestones.jsonl")
	if err := os.WriteFile(milestones, nil, 0o600); err != nil {
		t.Fatalf("create milestone file: %v", err)
	}
	lt := backgroundDriverLifetime{Configured: backgroundDriverTimeout, Join: backgroundDriverJoinReserve, Cleanup: backgroundHelperCleanupReserve}
	deadline, hasDeadline := t.Deadline()
	launch := warmReturnDriverLaunch(t, fixture, hub.URL, hubedge.AuthURLFor(hub.URL, authToken), milestones, entries)
	driverErr := runOwnedBackgroundDriver(t, deadline, hasDeadline, lt, launch)
	for i := range entries {
		skillGuardStopHelper(t, i)
	}
	assertWarmReturnTranscript(t, fixture, milestones)
	if driverErr != nil {
		t.Errorf("%v\nfull artifacts (logs, dumps): %s", driverErr, fixture.artifact)
	}
}

// warmReturnDriverLaunch builds the runner launch for the real hub and both
// fixture daemons. The finished channel is registered on the fixture inside the
// closure so a run refused before launch registers nothing.
func warmReturnDriverLaunch(t *testing.T, fixture *skillGuardFixture, hubURL, authURL, milestones string, entries [2]rendezvous.Entry) backgroundDriverLaunch {
	t.Helper()
	return func(ctx context.Context) (<-chan error, <-chan struct{}) {
		finished := make(chan struct{})
		fixture.driverFinished = finished
		done := make(chan error, 1)
		go func() {
			defer close(finished)
			done <- runWarmReturnDriver(ctx, t, fixture, hubURL, authURL, milestones, entries)
		}()
		return done, finished
	}
}

func runWarmReturnDriver(ctx context.Context, t *testing.T, fixture *skillGuardFixture, hubURL, authURL, milestones string, entries [2]rendezvous.Entry) error {
	t.Helper()
	frontend := filepath.Join(skillGuardRepoRoot(t), "cmd", "evener-hub", "frontend")
	driverLogPath := filepath.Join(fixture.artifact, "driver.log")
	driverLog, err := os.OpenFile(driverLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer driverLog.Close()
	cmd := backgroundDriverCommand(ctx, filepath.Join("scripts", "warmreturntranscriptguard", "run.mjs"),
		"--url", authURL,
		"--hub-url", hubURL,
		"--artifact-dir", fixture.artifact,
		"--control-path-a", fixture.control[0],
		"--control-path-b", fixture.control[1],
		"--request-log-a", fixture.requestLog[0],
		"--request-log-b", fixture.requestLog[1],
		"--trace-path", filepath.Join(fixture.artifact, "appwire-trace.jsonl"),
		"--milestone-path", milestones,
		"--session-a", skillGuardSessionRef(entries[0]),
		"--session-b", skillGuardSessionRef(entries[1]),
	)
	cmd.Dir = frontend
	cmd.Stdout = driverLog
	cmd.Stderr = driverLog
	cmd.Env = append([]string{"NODE_DISABLE_COMPILE_CACHE=1"}, os.Environ()...)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// warmReturnStructural is one committed model's shape, decoded from a
// milestone. It counts items and notices, never their text.
type warmReturnStructural struct {
	Ref           string `json:"ref"`
	LocalOpening  bool   `json:"localOpening"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field localOpening.
	Pending       bool   `json:"pending"`
	ModelPresent  bool   `json:"modelPresent"`
	DisplayItems  int    `json:"displayItems"`
	NoticeDisplay int    `json:"noticeDisplay"`
	ContentItems  int    `json:"contentItems"`
	// BodyItems counts displayed text-bearing items that are not overlay
	// notices. Notice text must never stand in for the recorded body.
	BodyItems      int `json:"bodyItems"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field bodyItems.
	OverlayNotices int `json:"overlayNotices"`
	HistoryItems   int `json:"historyItems"`
	Turns          int `json:"turns"`
}

// pruned reports the background lease's released display: the notice overlay
// alone, over a history that still records the bodies as bare identities.
func (s warmReturnStructural) pruned() bool {
	return s.ModelPresent && s.DisplayItems > 0 && !s.Pending &&
		s.DisplayItems == s.NoticeDisplay && s.OverlayNotices > 0 && s.HistoryItems > 0
}

type warmReturnNative struct {
	Rows     int `json:"rows"`
	Turns    int `json:"turns"`
	Coverage int `json:"coverage"`
}

type warmReturnActivation struct {
	Reads          int `json:"reads"`
	Pages          int `json:"pages"`
	SubscribeReads int `json:"subscribeReads"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field subscribeReads.
	RecoveryReads  int `json:"recoveryReads"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field recoveryReads.
	PlainReads     int `json:"plainReads"`     //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field plainReads.
}

type warmReturnOpening struct {
	Parked           int                      `json:"parked"`
	Frames           int                      `json:"frames"`
	PendingFrames    int                      `json:"pendingFrames"`    //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field pendingFrames.
	NoticeOnlyFrames int                      `json:"noticeOnlyFrames"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field noticeOnlyFrames.
	TraceBefore      int64                    `json:"traceBefore"`      //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceBefore.
	PendingShape     []warmReturnPendingShape `json:"pendingShape"`     //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field pendingShape.
}

// warmReturnPendingShape is one pending frame's structural counts, recorded so
// the Go side can re-judge the notice-only shape independently of the driver's
// own predicate.
type warmReturnPendingShape struct {
	ModelPresent   bool `json:"modelPresent"`   //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field modelPresent.
	DisplayItems   int  `json:"displayItems"`   //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field displayItems.
	NoticeDisplay  int  `json:"noticeDisplay"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field noticeDisplay.
	ContentItems   int  `json:"contentItems"`   //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field contentItems.
	BodyItems      int  `json:"bodyItems"`      //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field bodyItems.
	OverlayNotices int  `json:"overlayNotices"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field overlayNotices.
	HistoryItems   int  `json:"historyItems"`   //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field historyItems.
}

// noticeOnly reports the display the bug published ahead of the restore: the
// historical notice over a history that records bodies it can no longer show.
func (s warmReturnPendingShape) noticeOnly() bool {
	return s.ModelPresent && s.DisplayItems > 0 && s.DisplayItems == s.NoticeDisplay &&
		s.BodyItems == 0 && s.OverlayNotices > 0 && s.HistoryItems > 0
}

type warmReturnRestored struct {
	warmReturnStructural
	TraceBefore int64                `json:"traceBefore"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceBefore.
	TraceAfter  int64                `json:"traceAfter"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceAfter.
	Activation  warmReturnActivation `json:"activation"`
}

func assertWarmReturnTranscript(t *testing.T, fixture *skillGuardFixture, milestonesPath string) {
	t.Helper()
	milestones, err := backgroundReadMilestones(milestonesPath)
	if err != nil {
		t.Fatalf("read warm-return milestones: %v", err)
	}
	targetRef := ""

	var native warmReturnNative
	if skillGuardMilestoneDetail(t, milestones, "warm-pruned-native", &native) {
		if native.Rows == 0 {
			t.Error("warm-pruned-native: the background lease committed no durable row")
		}
	}
	var pruned warmReturnStructural
	if !skillGuardMilestoneDetail(t, milestones, "warm-pruned", &pruned) {
		t.Fatal("missing warm-pruned milestone")
	}
	targetRef = pruned.Ref
	if targetRef == "" {
		t.Fatal("warm-pruned: the milestone names no target ref")
	}
	if !pruned.pruned() {
		t.Errorf("warm-pruned: the store did not hold the released notice-only display: %+v", pruned)
	}

	var opening warmReturnOpening
	if !skillGuardMilestoneDetail(t, milestones, "warm-opening", &opening) {
		t.Fatal("missing warm-opening milestone")
	}
	if opening.Parked < 1 {
		t.Error("warm-opening: the local restore's durable window read was never parked")
	}
	if opening.PendingFrames == 0 {
		t.Error("warm-opening: no committed model was observed while the local opening was pending")
	}
	if opening.NoticeOnlyFrames != 0 {
		t.Errorf("warm-opening: %d notice-only model(s) were committed ahead of the local restore", opening.NoticeOnlyFrames)
	}
	for index, shape := range opening.PendingShape {
		if shape.noticeOnly() {
			t.Errorf("warm-opening: pending frame %d published the historical notice ahead of the local restore: %+v", index, shape)
		}
	}

	var restored warmReturnRestored
	if !skillGuardMilestoneDetail(t, milestones, "warm-restored", &restored) {
		t.Fatal("missing warm-restored milestone")
	}
	if !restored.ModelPresent || restored.Pending {
		t.Errorf("warm-restored: the restored model is missing or still pending: %+v", restored)
	}
	// The restored display must carry a text-bearing item that is not an
	// overlay notice; the historical notice's own text cannot stand in for the
	// recorded body the restore owes.
	if restored.BodyItems == 0 {
		t.Errorf("warm-restored: no non-notice recorded body in the restored display: %+v", restored)
	}
	if restored.OverlayNotices == 0 {
		t.Error("warm-restored: the historical notice did not settle with its bodies")
	}
	if restored.DisplayItems < restored.NoticeDisplay {
		t.Errorf("warm-restored: display items %d cannot hold %d notice(s)", restored.DisplayItems, restored.NoticeDisplay)
	}
	// The driver's own counts are re-derived from the hub's real trace so a
	// driver-side bookkeeping slip cannot pass as "no wire read".
	trace, err := os.ReadFile(filepath.Join(fixture.artifact, "appwire-trace.jsonl"))
	if err != nil {
		t.Fatalf("read appwire trace: %v", err)
	}
	window := traceWindowBytes(t, trace, restored.TraceBefore, restored.TraceAfter)
	reads, err := countTargetThreadReads(string(window), targetRef)
	if err != nil {
		t.Fatalf("count restored thread/read frames: %v", err)
	}
	pages, err := countTargetPageRequestsInWindow(string(window), targetRef)
	if err != nil {
		t.Fatalf("count restored page frames: %v", err)
	}
	if reads != 0 {
		t.Errorf("warm-restored: the real trace shows %d target thread/read(s); the local restore needed the wire", reads)
	}
	if pages != 0 {
		t.Errorf("warm-restored: the real trace shows %d target thread/turns/list page(s)", pages)
	}
	if restored.Activation.Reads != reads || restored.Activation.Pages != pages {
		t.Errorf("warm-restored: driver activation counts %+v disagree with the real trace (reads=%d pages=%d)", restored.Activation, reads, pages)
	}
	if restored.Activation.SubscribeReads != 0 || restored.Activation.RecoveryReads != 0 || restored.Activation.PlainReads != 0 {
		t.Errorf("warm-restored: activation reads were split as %+v", restored.Activation)
	}

	var control struct {
		warmReturnStructural
		NativeMetadata string `json:"nativeMetadata"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field nativeMetadata.
		NativeRows     int    `json:"nativeRows"`     //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field nativeRows.
	}
	if !skillGuardMilestoneDetail(t, milestones, "warm-notice-control", &control) {
		t.Fatal("missing warm-notice-control milestone")
	}
	if control.LocalOpening || control.Pending {
		t.Errorf("warm-notice-control: the session's local opening never settled: %+v", control)
	}
	if !control.ModelPresent || control.NoticeDisplay < 1 {
		t.Errorf("warm-notice-control: the session settled without a retained notice: %+v", control)
	}
	// This fixture cannot produce a session with no recorded entry of its own,
	// so the control shows what the hold must leave alone: a retained notice
	// settling beside its session's own recorded entry, never withheld as a
	// pruned historical publication. Its durable state is logged for the
	// record, and the empty, notice-only, cleared, replaced-ref and anonymous
	// source cases are covered at the store layer by
	// threads.entryProduction.test.ts and threads.sessionCache.test.ts.

	var cleared struct {
		warmReturnStructural
		NativeMetadata string `json:"nativeMetadata"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field nativeMetadata.
	}
	if !skillGuardMilestoneDetail(t, milestones, "warm-cleared", &cleared) {
		t.Fatal("missing warm-cleared milestone")
	}
	// A clear keeps readable content in the store model by design; its durable
	// boundary is the entry cache, which must hold none of the released rows.
	if cleared.NativeMetadata != "absent" {
		t.Errorf("warm-cleared: native entry storage kept the released rows (%q)", cleared.NativeMetadata)
	}
	if !backgroundMilestonePresent(milestones, "done") {
		t.Error("the warm-return driver never reached its done milestone")
	}
	// The passing run's own evidence: structural counts only, never refs or text.
	t.Logf(
		"warm return: native={rows:%d turns:%d} pruned={items:%d notices:%d history:%d} "+
			"opening={parked:%d frames:%d pending:%d noticeOnly:%d} "+
			"restored={items:%d bodies:%d notices:%d reads:%d pages:%d} "+
			"control={items:%d notices:%d native:%s rows:%d} clear={metadata:%s}",
		native.Rows, native.Turns,
		pruned.DisplayItems, pruned.OverlayNotices, pruned.HistoryItems,
		opening.Parked, opening.Frames, opening.PendingFrames, opening.NoticeOnlyFrames,
		restored.DisplayItems, restored.BodyItems, restored.OverlayNotices, reads, pages,
		control.DisplayItems, control.OverlayNotices, control.NativeMetadata, control.NativeRows,
		cleared.NativeMetadata,
	)
}
