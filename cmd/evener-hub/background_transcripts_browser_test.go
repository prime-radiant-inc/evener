//go:build browserguard

// TestBackgroundTranscriptsBrowser is the whole-journey proof for background
// Live transcripts: the REAL hub web app (real AppWire, real stores, native
// IndexedDB) driven in real Chrome in Vite development mode through a REAL hub
// (httptest around web.Handler() with the real roster prober and past index)
// against TWO REAL `evener serve` daemons (cmd/evener's compiled browserguard
// helper binary), with the only scripted piece at the external LLM provider
// adapter.
//
// It reuses the skill guard's fixture (skillGuardSetup / skillGuardStartHelper /
// skillGuardStopHelper) and its private temporary directory; the Node driver
// (frontend/scripts/backgroundtranscriptguard/run.mjs) owns the browser gestures
// and records milestones, and writes the fixture's background-phase commands
// through the same control-file format the skill guard uses. This test asserts
// what the daemons ACTUALLY received (their provider request logs, phase
// acknowledgments and durable transcripts) and what the real hub AppWire trace
// recorded, never a coordinator flag.
//
// The build tag keeps Chrome out of the default suites; the browser gate
// (cmd/evener-dev/webbrowser.go) registers this test so `make test-web-browser`
// runs it.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// Fixture sentinels scripted across the provider boundary. They are opaque
// data, never prose: this Go owner keeps its own copies, independent of the
// driver (frontend/scripts/backgroundtranscriptguard/run.mjs).
const (
	backgroundBeforeEviction = "BACKGROUND_BEFORE_EVICTION"
	backgroundAfterEviction  = "BACKGROUND_AFTER_EVICTION"
	backgroundToolOutput     = "BACKGROUND_TOOL_OUTPUT"
	backgroundToolDone       = "BACKGROUND_TOOL_DONE"
)

const (
	// backgroundDriverTimeout caps how long the driver may run on its own when
	// the test carries no deadline. A hung driver is a failure, never a hang of
	// the gate.
	backgroundDriverTimeout = 10 * time.Minute

	// backgroundDriverForceKillDelay is the driver's forced-stop delay: after
	// the cancellation signal the process is killed outright if it has not
	// exited within this delay. It is the reserve the owning await has to hold
	// to cancel and fully join a driver whose budget ran out.
	backgroundDriverForceKillDelay = 15 * time.Second

	// backgroundDriverJoinReserve is what the owning test holds back to cancel
	// and reap a driver whose forced stop takes backgroundDriverForceKillDelay:
	// the forced stop itself plus a margin to observe the process exit.
	backgroundDriverJoinReserve = backgroundDriverForceKillDelay + 2*time.Second

	// backgroundHelperStopWait is the per-helper shutdown wait: a helper that
	// ignores its shutdown command is killed after this wait. It mirrors the
	// wait in skillGuardHelper.stop (skill_composer_browser_test.go), whose
	// shared implementation is frozen here, so the reserve is stated beside the
	// driver that must leave room for it.
	backgroundHelperStopWait = 20 * time.Second

	// backgroundHelperCleanupReserve is the owning test's reserve for stopping
	// the two background fixture helpers after the driver: two helper
	// shutdowns, each bounded by backgroundHelperStopWait.
	backgroundHelperCleanupReserve = 2 * backgroundHelperStopWait
)

// backgroundHolderView is the reader's decoded view of the driver's
// history-holder milestone. The struct tags are the reader's contract; the
// driver actually emits itemsBefore/itemsAfter (run.mjs's history-holder
// milestone). TestBackgroundTranscriptHolderMilestoneShape pins that mismatch.
type backgroundHolderView struct {
	ItemsBefore    int  `json:"itemsBefore"`
	ItemsAfter     int  `json:"itemsAfter"`
	ProtectedModel bool `json:"protectedModel"`
}

// backgroundHolderMilestone decodes the history-holder milestone into the
// reader's view, extracted from TestBackgroundTranscriptsBrowser so the shape
// can be pinned without a browser.
func backgroundHolderMilestone(t *testing.T, milestones []skillGuardMilestone) (backgroundHolderView, bool) {
	t.Helper()
	var view backgroundHolderView
	if !skillGuardMilestoneDetail(t, milestones, "history-holder", &view) {
		return backgroundHolderView{}, false
	}
	return view, true
}

// TestBackgroundTranscriptHolderMilestoneShape pins the reader against the
// driver's ACTUAL emitted holder detail. The driver emits itemsBefore/itemsAfter
// (frontend/scripts/backgroundtranscriptguard/run.mjs, history-holder
// milestone); the reader must read those keys. It currently binds
// loadedRowsBefore/loadedRowsAfter, so the driver's counts decode to zero and
// the production pressure comparison is 0 vs 0.
func TestBackgroundTranscriptHolderMilestoneShape(t *testing.T) {
	detail := json.RawMessage(`{"itemsBefore":5,"itemsAfter":5,"cursorBefore":"c1","cursorAfter":"c1","anchorBefore":10,"anchorAfter":10,"protectedModel":true}`)
	milestones := []skillGuardMilestone{{Milestone: "history-holder", Detail: detail}}
	holder, ok := backgroundHolderMilestone(t, milestones)
	if !ok {
		t.Fatal("no history-holder milestone")
	}
	if holder.ItemsBefore != 5 || holder.ItemsAfter != 5 {
		t.Errorf(
			"history-holder reader decoded itemsBefore/itemsAfter = %d/%d, want 5/5: the driver emits itemsBefore/itemsAfter but the reader binds %q/%q",
			holder.ItemsBefore, holder.ItemsAfter, "loadedRowsBefore", "loadedRowsAfter",
		)
	}
}

func TestBackgroundTranscriptsBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("browser guard requires a real Chrome and real daemons")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("node is required for the background transcript browser guard: %v", err)
	}
	// The embedded production frontend must exist: the hub serves it for the
	// auth handshake, and the guard's own nested HTML is served by Vite. A
	// missing dist is a build-prerequisite failure, not a pass.
	if _, err := fs.ReadFile(distFS(), "index.html"); err != nil {
		t.Fatalf("the production frontend is not built: run `make build-web` first: %v", err)
	}

	fixture := skillGuardSetup(t)

	// Arm the explicit background fixture mode before the helpers start: the
	// control tail reads from byte zero, so each daemon takes the
	// channel-backed streaming path instead of Complete. The skill guard never
	// writes this command, so its behavior is unchanged.
	for i := 0; i < 2; i++ {
		role := "sustained"
		if i == 1 {
			// Helper beta owns the unopened target: its scripted turn is small
			// before the release and carries the real tool call after it.
			role = "target"
		}
		for _, command := range []map[string]string{
			{"command": "background-mode"},
			{"command": "background-role", "phase": role},
		} {
			if err := appendFileLine(fixture.control[i], command); err != nil {
				t.Fatalf("arm background fixture for helper %d: %v", i, err)
			}
		}
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
	past := hubcore.NewPastIndex(filepath.Join(fixture.stateRoot, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatalf("past index rebuild: %v", err)
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		t.Fatalf("mint auth token: %v", err)
	}
	token := hex.EncodeToString(tokenBytes)
	hubStateRoot := filepath.Join(fixture.root, "hub-state")
	if err := os.MkdirAll(hubStateRoot, 0o700); err != nil {
		t.Fatalf("hub state root: %v", err)
	}
	appwireTrace, err := appserver.NewWebSocketTrace(filepath.Join(fixture.artifact, "appwire-trace.jsonl"))
	if err != nil {
		t.Fatalf("appwire trace: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := appwireTrace.Close(); closeErr != nil {
			t.Errorf("close appwire trace: %v", closeErr)
		}
	})
	web := newWebServer(hubcore.WebConfig{
		AuthToken:     token,
		HubStateRoot:  hubStateRoot,
		RunDir:        fixture.runDir,
		Past:          past,
		Roster:        roster,
		PluginDirs:    []string{filepath.Join(fixture.root, "controller-plugin")},
		StateDir:      fixture.stateRoot,
		PastIndexPath: filepath.Join(hubStateRoot, "index.db"),
	}, appwireTrace)
	past.SetOnChange(func() { web.navigation.Invalidate(navigationChangeHint{}) })
	roster.SetOnChange(func() { web.navigation.Invalidate(navigationChangeHint{}) })
	hub := httptest.NewServer(web.Handler())
	defer hub.Close()
	authURL := hubedge.AuthURLFor(hub.URL, token)

	milestones := filepath.Join(fixture.artifact, "milestones.jsonl")
	if err := os.WriteFile(milestones, nil, 0o600); err != nil {
		t.Fatalf("create milestone file: %v", err)
	}

	// Admit the real driver against the OWNING deadline before it starts: the
	// budget is the remaining test/package deadline minus the join and
	// fixture-cleanup reserves. An admitted run registers its finished channel
	// (so cleanup awaits it) and a driver cancelled at its budget is reaped
	// before helper shutdown begins; a refused run starts no process and
	// registers nothing. No product/browser deadline is widened.
	lt := backgroundDriverLifetime{
		Configured: backgroundDriverTimeout,
		Join:       backgroundDriverJoinReserve,
		Cleanup:    backgroundHelperCleanupReserve,
	}
	deadline, hasDeadline := t.Deadline()
	if err := runOwnedBackgroundDriver(t, deadline, hasDeadline, lt, backgroundDriverLaunchFor(t, fixture, hub.URL, authURL, milestones, entries)); err != nil {
		t.Fatalf("%v\nfull artifacts (logs, screenshots, dumps): %s", err, fixture.artifact)
	}

	for i := range entries {
		skillGuardStopHelper(t, i)
	}

	backgroundAssert(t, fixture, milestones)
}

func TestBackgroundTranscriptsEntrySliceBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("browser guard requires a real Chrome and real daemons")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("node is required for the production-entry browser guard: %v", err)
	}
	if _, err := fs.ReadFile(distFS(), "index.html"); err != nil {
		t.Fatalf("the production frontend is not built: run `make build-web` first: %v", err)
	}
	fixture := skillGuardSetup(t)
	for i, role := range []string{"sustained", "target"} {
		for _, command := range []map[string]string{{"command": "background-mode"}, {"command": "background-role", "phase": role}} {
			if err := appendFileLine(fixture.control[i], command); err != nil {
				t.Fatalf("arm production-entry fixture helper %d: %v", i, err)
			}
		}
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
	past := hubcore.NewPastIndex(filepath.Join(fixture.stateRoot, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatalf("past index rebuild: %v", err)
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		t.Fatalf("mint auth token: %v", err)
	}
	hubStateRoot := filepath.Join(fixture.root, "hub-state")
	if err := os.MkdirAll(hubStateRoot, 0o700); err != nil {
		t.Fatalf("hub state root: %v", err)
	}
	appwireTrace, err := appserver.NewWebSocketTrace(filepath.Join(fixture.artifact, "appwire-trace.jsonl"))
	if err != nil {
		t.Fatalf("appwire trace: %v", err)
	}
	t.Cleanup(func() {
		if err := appwireTrace.Close(); err != nil {
			t.Errorf("close appwire trace: %v", err)
		}
	})
	web := newWebServer(hubcore.WebConfig{AuthToken: hex.EncodeToString(tokenBytes), HubStateRoot: hubStateRoot, RunDir: fixture.runDir, Past: past, Roster: roster, PluginDirs: []string{filepath.Join(fixture.root, "controller-plugin")}, StateDir: fixture.stateRoot, PastIndexPath: filepath.Join(hubStateRoot, "index.db")}, appwireTrace)
	past.SetOnChange(func() { web.navigation.Invalidate(navigationChangeHint{}) })
	roster.SetOnChange(func() { web.navigation.Invalidate(navigationChangeHint{}) })
	hub := httptest.NewServer(web.Handler())
	defer hub.Close()
	milestones := filepath.Join(fixture.artifact, "milestones.jsonl")
	if err := os.WriteFile(milestones, nil, 0o600); err != nil {
		t.Fatalf("create milestone file: %v", err)
	}
	lt := backgroundDriverLifetime{Configured: backgroundDriverTimeout, Join: backgroundDriverJoinReserve, Cleanup: backgroundHelperCleanupReserve}
	deadline, hasDeadline := t.Deadline()
	launch := backgroundDriverLaunchForMode(t, fixture, hub.URL, hubedge.AuthURLFor(hub.URL, hex.EncodeToString(tokenBytes)), milestones, entries, true)
	driverErr := runOwnedBackgroundDriver(t, deadline, hasDeadline, lt, launch)
	for i := range entries {
		skillGuardStopHelper(t, i)
	}
	assertBackgroundEntrySlice(t, fixture, milestones)
	if driverErr != nil {
		t.Errorf("%v\nfull artifacts (logs, screenshots, dumps): %s", driverErr, fixture.artifact)
	}
}

// runBackgroundDriver launches the Node driver. --hub-url is the fixture hub's
// base URL; the driver sets EVENER_HUB_ADDR from it before starting Vite so the
// dev server's proxy forwards /rpc and the hub HTTP routes. --control-path-a/-b
// are the two helpers' control files, so a phase release reaches the daemon
// that owns the driven session.
func runBackgroundDriver(ctx context.Context, t *testing.T, fixture *skillGuardFixture, hubURL, authURL, milestones string, entries [2]rendezvous.Entry) error {
	return runBackgroundDriverMode(ctx, t, fixture, hubURL, authURL, milestones, entries, false)
}

func runBackgroundDriverMode(ctx context.Context, t *testing.T, fixture *skillGuardFixture, hubURL, authURL, milestones string, entries [2]rendezvous.Entry, entrySlice bool) error {
	t.Helper()
	frontend := filepath.Join(skillGuardRepoRoot(t), "cmd", "evener-hub", "frontend")
	driverLogPath := filepath.Join(fixture.artifact, "driver.log")
	driverLog, err := os.OpenFile(driverLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open driver log: %w", err)
	}
	defer driverLog.Close()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("scripts", "backgroundtranscriptguard", "run.mjs"),
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
	if entrySlice {
		cmd.Args = append(cmd.Args, "--entry-production", "true")
	}
	configureBackgroundDriverCommand(cmd)
	cmd.Dir = frontend
	cmd.Stdout = driverLog
	cmd.Stderr = driverLog
	cmd.Env = append([]string{"NODE_DISABLE_COMPILE_CACHE=1"}, os.Environ()...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("node scripts/backgroundtranscriptguard/run.mjs exited %w (driver log: %s)", err, driverLogPath)
	}
	return nil
}

// configureBackgroundDriverCommand applies the driver's cancellation contract:
// the SIGTERM delivered when the context is cancelled, then a forced kill after
// backgroundDriverForceKillDelay. The lifetime regression drives this same
// setup, so the test and the real driver cannot drift on either signal.
func configureBackgroundDriverCommand(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = backgroundDriverForceKillDelay
}

// backgroundDriverLifetime is the reserve set the owning await holds back from
// the test deadline before it starts waiting for the driver.
type backgroundDriverLifetime struct {
	// Configured caps the driver's own run time when no test deadline exists.
	Configured time.Duration
	// Join is held back to cancel the driver and reap it after its forced stop.
	Join time.Duration
	// Cleanup is held back for the fixture helpers stopped after the driver.
	Cleanup time.Duration
}

// backgroundDriverBudget returns how long the driver may run before the owning
// test has to cancel it. It is the smaller of the configured cap and whatever
// the test deadline leaves after the join and cleanup reserves. ok is false
// when nothing positive is left, so the caller fails before a final, doomed
// wait.
func backgroundDriverBudget(deadline time.Time, hasDeadline bool, lt backgroundDriverLifetime) (time.Duration, bool) {
	budget := lt.Configured
	if hasDeadline {
		if remaining := time.Until(deadline) - lt.Join - lt.Cleanup; remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		return 0, false
	}
	return budget, true
}

// awaitBackgroundDriver waits up to budget for the driver to finish. When the
// budget runs out it cancels the driver. timedOut reports the timeout and
// joined reports whether the process was observed to exit, so the caller never
// treats a timeout as success.
func awaitBackgroundDriver(t *testing.T, budget, joinReserve time.Duration, cancel context.CancelFunc, done <-chan error, finished <-chan struct{}) (exitErr error, timedOut, joined bool) {
	select {
	case exitErr = <-done:
		return exitErr, false, true
	case <-time.After(budget):
	}
	cancel()
	joined = skillGuardAwaitDriver(t, finished, joinReserve)
	if joined {
		exitErr = <-done
	}
	return exitErr, true, joined
}

// backgroundDriverLaunch starts one driver run and returns its completion and
// finished channels. It receives the run context the owning function owns.
type backgroundDriverLaunch func(ctx context.Context) (done <-chan error, finished <-chan struct{})

// runOwnedBackgroundDriver owns one driver run's lifetime. It admits the run
// against the owning deadline BEFORE starting anything, so a run with no budget
// left never starts a process and never registers one. When admitted it creates
// the run context (cancelled on every exit, so none leaks), starts the driver
// through launch, and cancels, joins, and collects the exit error of a driver
// whose budget ran out.
func runOwnedBackgroundDriver(t *testing.T, deadline time.Time, hasDeadline bool, lt backgroundDriverLifetime, launch backgroundDriverLaunch) error {
	t.Helper()
	budget, admitted := backgroundDriverBudget(deadline, hasDeadline, lt)
	if !admitted {
		return fmt.Errorf("no driver budget left before the test deadline after the %s join and %s fixture-cleanup reserves", lt.Join, lt.Cleanup)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done, finished := launch(ctx)
	exitErr, timedOut, joined := awaitBackgroundDriver(t, budget, lt.Join, cancel, done, finished)
	if timedOut {
		if !joined {
			return fmt.Errorf("driver did not stop after cancel and a %s join", lt.Join)
		}
		return fmt.Errorf("driver did not exit within %s: %w", budget, exitErr)
	}
	return exitErr
}

// backgroundDriverLaunchFor builds the real driver launch. The finished channel
// is created and registered on the fixture INSIDE the returned closure, so a run
// refused before launch registers nothing and the fixture's cleanup never waits
// on a driver that never started.
func backgroundDriverLaunchFor(t *testing.T, fixture *skillGuardFixture, hubURL, authURL, milestones string, entries [2]rendezvous.Entry) backgroundDriverLaunch {
	return backgroundDriverLaunchForMode(t, fixture, hubURL, authURL, milestones, entries, false)
}

func backgroundDriverLaunchForMode(t *testing.T, fixture *skillGuardFixture, hubURL, authURL, milestones string, entries [2]rendezvous.Entry, entrySlice bool) backgroundDriverLaunch {
	t.Helper()
	return func(ctx context.Context) (<-chan error, <-chan struct{}) {
		finished := make(chan struct{})
		fixture.driverFinished = finished
		done := make(chan error, 1)
		go func() {
			defer close(finished)
			done <- runBackgroundDriverMode(ctx, t, fixture, hubURL, authURL, milestones, entries, entrySlice)
		}()
		return done, finished
	}
}

// ---- post-run assertions ----

func backgroundAssert(t *testing.T, fixture *skillGuardFixture, milestonesPath string) {
	t.Helper()
	// Build isolation: the dev entry and its nested HTML live outside the
	// production build inputs, so the embedded dist must carry neither.
	if err := fs.WalkDir(distFS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.Contains(path, "backgroundtranscriptguard") {
			return fmt.Errorf("the production build carries the guard's nested HTML: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		data, readErr := fs.ReadFile(distFS(), path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), "backgroundTranscriptEvidence") {
			return fmt.Errorf("the production build carries the dev evidence global: %s", path)
		}
		return nil
	}); err != nil {
		t.Errorf("build isolation: %v", err)
	}

	milestones, err := backgroundReadMilestones(milestonesPath)
	if err != nil {
		t.Fatalf("read milestones: %v", err)
	}
	// Every ordered journey phase must be present: the driver names each one
	// and skips nothing silently.
	for _, name := range []string{
		"installer-before-init", "hub-connected", "sessions-visible",
		"target-turn-started", "target-background-before",
		"after-eviction-released", "target-background-after", "target-evicted", "target-checkpoint", "target-checkpoint-content",
		"pane-opened", "pane-rendered", "turn-finished", "exact-once",
		"history-pages", "disk-handoff", "reopen-without-read", "further-paging",
		"history-holder",
		"ws-broken", "gap-turn-completed", "reconnected-latest-window", "paged-back-sentinel",
		"narrow-viewport", "sustained-workload", "batching",
		"done",
	} {
		if !backgroundMilestonePresent(milestones, name) {
			t.Errorf("browser report is missing the %q phase — the guard silently skipped it", name)
		}
	}

	// The real hub AppWire trace must show the store's requests, not just a
	// coordinator's flag.
	trace, err := os.ReadFile(filepath.Join(fixture.artifact, "appwire-trace.jsonl"))
	if err != nil {
		t.Fatalf("read appwire trace: %v", err)
	}
	if len(strings.TrimSpace(string(trace))) == 0 {
		t.Error("the real hub AppWire trace is empty: the browser never exchanged frames through the hub")
	}
	frames, err := decodeTraceFrames(trace)
	if err != nil {
		t.Fatalf("decode appwire trace: %v", err)
	}
	var target struct {
		Ref string `json:"ref"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "target-turn-started", &target) || target.Ref == "" {
		t.Fatal("no target-turn-started milestone carrying the target ref")
	}
	// The target's subscription must be REAL, carried either by a
	// thread/subscribe or by a thread/read with subscribe:true. A raw substring
	// of "thread/subscribe" is not evidence of a subscription.
	targetEv, err := targetTraceEvidenceFor(frames, target.Ref)
	if err != nil {
		t.Fatalf("target trace evidence: %v", err)
	}
	if !targetEv.Subscribed {
		t.Error("the hub trace carries no real target subscription (neither thread/subscribe nor a subscribe:true thread/read)")
	}
	if targetEv.InitialOlderCursor == "" {
		t.Error("the target's initial read carried no olderCursor: it was not a truncated real history window")
	}
	if targetEv.PageCount < 2 {
		t.Errorf("the target received %d older thread/turns/list page(s); at least two are required", targetEv.PageCount)
	}

	// The driver's page evidence must be real, not milestone names alone: a
	// truncated initial window, at least two real older pages, a later-page
	// sentinel rendered while a cursor remains, and real further paging after
	// reopen and after the reconnect replacement.
	var historyPages struct {
		Turns              int    `json:"turns"`
		InitialOlderCursor string `json:"initialOlderCursor"`
		Pages              int    `json:"pages"`
		RemainingCursor    string `json:"remainingCursor"`
		PagesComplete      bool   `json:"pagesComplete"`
		LaterSentinel      string `json:"laterSentinel"`
		LaterRendered      bool   `json:"laterRendered"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "history-pages", &historyPages) {
		t.Fatal("no history-pages milestone")
	}
	if historyPages.InitialOlderCursor == "" {
		t.Error("history-pages: the initial read carried no olderCursor")
	}
	if historyPages.Pages < 2 {
		t.Errorf("history-pages: only %d older page(s) were loaded; at least two are required", historyPages.Pages)
	}
	if !historyPages.LaterRendered {
		t.Errorf("history-pages: the later-page sentinel %q did not render", historyPages.LaterSentinel)
	}
	// A nonempty cursor is required only while the produced history is not yet
	// fully loaded; the entry route may complete it within the two pages, and the
	// further-paging step tolerates that complete case.
	if historyPages.RemainingCursor == "" && !historyPages.PagesComplete {
		t.Error("history-pages: no nonempty cursor remained for the post-reopen further-paging phase, and the produced history was not already complete")
	}
	var furtherPaging struct {
		Pages           int  `json:"pages"`
		AlreadyComplete bool `json:"alreadyComplete"`
		RequiredPage    bool `json:"requiredPage"`
		HistoryOrdered  bool `json:"historyOrdered"`
		HistoryCount    int  `json:"historyCount"`
		ExpectedCount   int  `json:"expectedCount"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "further-paging", &furtherPaging) {
		t.Fatal("no further-paging milestone")
	}
	// The accepted reopen contract restores the loaded window locally with no
	// read and no page, so a reopened pane that already holds the whole produced
	// history has nothing older to fetch. The R77 supersession for this legacy
	// observation (which always demanded a page) requires a page only when the
	// local reopen did NOT already restore the full history, and always requires
	// the history to be complete in order.
	if furtherPaging.RequiredPage && furtherPaging.Pages < 1 {
		t.Error("further-paging: the reopened history was not already complete, but paging issued no older target page")
	}
	if !furtherPaging.HistoryOrdered {
		t.Errorf(
			"further-paging: the reopened history is not the produced sequence in order: loaded=%d expected=%d",
			furtherPaging.HistoryCount,
			furtherPaging.ExpectedCount,
		)
	}
	var pagedBack struct {
		Sentinel string `json:"sentinel"`
		Pages    int    `json:"pages"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "paged-back-sentinel", &pagedBack) {
		t.Fatal("no paged-back-sentinel milestone")
	}
	if pagedBack.Pages < 1 {
		t.Error("paged-back-sentinel: reconnect replacement then paging back issued no older target page")
	}

	// The tool OUTPUT, not the command argument, must be the produced literal.
	var rendered struct {
		SawOutputRegion    bool `json:"sawOutputRegion"`
		SawToolOutputField bool `json:"sawToolOutputField"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "pane-rendered", &rendered) {
		t.Fatal("no pane-rendered milestone")
	}
	if !rendered.SawOutputRegion {
		t.Error("the shell OUTPUT region never rendered the produced tool output")
	}
	if !rendered.SawToolOutputField {
		t.Error("the target model had no tool item whose output carried the produced literal")
	}

	// exactly-once and the running state come from the complete target content.
	var exactOnce struct {
		AfterOnce int `json:"afterOnce"`
		ToolOnce  int `json:"toolOnce"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "exact-once", &exactOnce) {
		t.Fatal("no exact-once milestone")
	}
	if exactOnce.AfterOnce != 1 || exactOnce.ToolOnce != 1 {
		t.Errorf("exact-once from complete target content: after=%d tool=%d, want 1/1", exactOnce.AfterOnce, exactOnce.ToolOnce)
	}
	var finished struct {
		RunningObserved bool `json:"runningObserved"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "turn-finished", &finished) || !finished.RunningObserved {
		t.Error("the target's running state was not observed before completion")
	}

	// Reconnect: a NEW target authoritative read and its applied replacement.
	var reconnect struct {
		NewReads int  `json:"newReads"`
		HasGap   bool `json:"hasGap"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "reconnected-latest-window", &reconnect) {
		t.Fatal("no reconnected-latest-window milestone")
	}
	if reconnect.NewReads < 1 {
		t.Error("reconnect produced no new target authoritative read")
	}
	if !reconnect.HasGap {
		t.Error("the reconnect replacement window does not contain the gap turn")
	}

	var furtherDup struct {
		NoDuplicates bool `json:"noDuplicates"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "further-paging", &furtherDup) || !furtherDup.NoDuplicates {
		t.Error("further paging introduced duplicate items")
	}
	var backDup struct {
		NoDuplicates bool `json:"noDuplicates"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "paged-back-sentinel", &backDup) || !backDup.NoDuplicates {
		t.Error("paging back introduced duplicate items")
	}

	// The scripted stream reached the daemons' provider boundary, and the
	// before/after text and the real shell tool output are on the durable
	// transcripts this Go owner reads (not on the driver's word).
	transcripts, transcriptErr := backgroundTranscriptsFor(t, fixture.stateRoot, strings.TrimPrefix(target.Ref, "local:"))
	if transcriptErr != nil {
		t.Fatalf("durable target transcript: %v", transcriptErr)
	}
	for _, want := range []string{backgroundBeforeEviction, backgroundAfterEviction, backgroundToolOutput} {
		if !strings.Contains(transcripts, want) {
			t.Errorf("no durable transcript contains %q: the background turn did not persist it", want)
		}
	}

	// The phase acknowledgments are recorded by the helpers independently of the
	// driver's milestones.
	acks := backgroundReadLog(t, fixture.requestLog[0]) + backgroundReadLog(t, fixture.requestLog[1])
	for _, phase := range []string{"after-eviction", "finish"} {
		if !strings.Contains(acks, `"phase":"`+phase+`"`) {
			t.Errorf("no helper acknowledged phase %q", phase)
		}
	}

	// The driver's own observed evidence: the target's background read produced
	// the before text while the pane was unopened, and the target checkpoint was
	// committed in real IndexedDB. Literal content is required in both: an
	// advanced sequence or a bare `current:true` cannot certify either.
	var backgroundBefore struct {
		ObservedBefore   bool     `json:"observedBefore"`
		ContentHasBefore bool     `json:"contentHasBefore"`
		ResidentRefs     []string `json:"residentRefs"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "target-background-before", &backgroundBefore) {
		t.Fatal("no target-background-before milestone")
	}
	if !backgroundBefore.ContentHasBefore {
		t.Error("the target's applied content did not carry the literal before-eviction text; an advanced sequence is not content")
	}
	t.Logf("target before text present (resident refs: %v)", backgroundBefore.ResidentRefs)

	// The accepted entry contract keeps live output out of storage until its turn
	// completes, and the ordinary route's background-only store model is a PRUNED
	// projection (recorded bodies are identity-only; only the live overlay carries
	// text). So neither the pre-open model nor native storage carries the literal
	// pre-completion text. The legacy pre-open current-record checkpoint is
	// superseded; the applied after text is asserted through the pane (pane-rendered
	// and exact-once) once the pane opens.
	var backgroundAfter struct {
		ObservedAfter    bool `json:"observedAfter"`
		RunningAdvanced  bool `json:"runningAdvanced"`
		SupersededLegacy bool `json:"supersededLegacyCheckpoint"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "target-background-after", &backgroundAfter) {
		t.Fatal("no target-background-after milestone")
	}
	if !backgroundAfter.ObservedAfter || !backgroundAfter.RunningAdvanced {
		t.Error("the target's background read did not advance to the after-eviction phase")
	}
	if !backgroundAfter.SupersededLegacy {
		t.Error("the target-background-after milestone did not name the superseded legacy current-record checkpoint")
	}

	// The ordinary entry route has no per-ref resident map, so the legacy
	// absence/charge observation is superseded. Its bounded resource is the
	// shared write admission, which covers every entry repository writing through
	// the real cache; under sustained pressure its retained bytes must stay
	// within the injected 32 KiB pending budget.
	var evicted struct {
		RetainedBytes     int64 `json:"retainedBytes"`
		PendingLimitBytes int64 `json:"pendingLimitBytes"`
		PressureBounded   bool  `json:"pressureBounded"`
		SupersededLegacy  bool  `json:"supersededLegacyResident"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "target-evicted", &evicted) {
		t.Fatal("no target-evicted milestone")
	}
	if evicted.PendingLimitBytes != 32*1024 {
		t.Errorf("target-evicted pending limit = %d, want the injected 32768 (the dev entry's install)", evicted.PendingLimitBytes)
	}
	if !evicted.PressureBounded || evicted.RetainedBytes > evicted.PendingLimitBytes {
		t.Errorf(
			"the shared entry admission was not bounded under pressure: retained=%d limit=%d",
			evicted.RetainedBytes, evicted.PendingLimitBytes,
		)
	}
	if !evicted.SupersededLegacy {
		t.Error("the target-evicted milestone did not name the superseded legacy resident instrument")
	}

	// The pre-release gate proves the target's recorded history is durably
	// checkpointed in NATIVE entry storage BEFORE after-eviction is released. The
	// accepted entry contract keeps live output out of storage until its turn
	// completes, so the pre-release durable content is the recorded history, not
	// the live before overlay; the legacy current-record before-text checkpoint is
	// superseded.
	var checkpointContent struct {
		EntryPresent    bool   `json:"entryCheckpointPresent"`
		EntryRows       int    `json:"entryRows"`
		EntryHistoryKey string `json:"entryHistoryKey"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "target-checkpoint-content", &checkpointContent) {
		t.Fatal("no target-checkpoint-content milestone")
	}
	if !checkpointContent.EntryPresent || checkpointContent.EntryRows == 0 || checkpointContent.EntryHistoryKey == "" {
		t.Errorf(
			"the target's native entry history checkpoint was not committed before the after-eviction release: %+v",
			checkpointContent,
		)
	}

	// Zero activation reads: between the driver's recorded trace position (just
	// before it opened the target through the rail) and pane readiness, the
	// real hub must NOT have served a thread/read for the target. The retained
	// background snapshot — not a fresh activation read — must satisfy pane
	// readiness. Read from the real AppWire trace, not the driver's word.
	//
	// Ruling 85 supersession: where the opening cut's durable readiness is known
	// broken (a refused durable write set recoveryNeeded), the entry route issues
	// an honest RECOVERY read — a NON-subscribing thread/read carrying a held
	// snapshot — and the render stays local. The zero-read promise itself still
	// holds and is proved by the entry-slice journey, which runs without a
	// refusal. A read that is a foreground subscription read, or is not a
	// recovery read, still fails here.
	var activation struct {
		TraceBefore        int64  `json:"traceBefore"`
		TraceAfter         int64  `json:"traceAfter"`
		Ref                string `json:"ref"`
		RetainedBeforeOpen bool   `json:"retainedBeforeOpen"`
		SubscribeReads     int    `json:"subscribeReads"`
		RecoveryReads      int    `json:"recoveryReads"`
		PlainReads         int    `json:"plainReads"`
		PreOpenRecovery    struct {
			RecoveryNeeded bool `json:"recoveryNeeded"`
		} `json:"preOpenRecovery"`
		PostOpenRoute struct {
			LastLocalOpenStatus string `json:"lastLocalOpenStatus"`
		} `json:"postOpenRoute"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "pane-activation-reads", &activation) {
		t.Fatal("no pane-activation-reads milestone")
	}
	if activation.Ref == "" {
		t.Fatal("the activation-read milestone names no target ref, so unrelated background reads cannot be excluded")
	}
	if activation.TraceAfter < activation.TraceBefore {
		t.Fatalf("pane-activation-reads offsets are inverted: %d..%d", activation.TraceBefore, activation.TraceAfter)
	}
	traceBytes, err := os.ReadFile(filepath.Join(fixture.artifact, "appwire-trace.jsonl"))
	if err != nil {
		t.Fatalf("read appwire trace for activation window: %v", err)
	}
	if activation.TraceAfter > int64(len(traceBytes)) {
		t.Fatalf("trace window %d..%d exceeds the trace's %d bytes", activation.TraceBefore, activation.TraceAfter, len(traceBytes))
	}
	window := string(traceBytes[activation.TraceBefore:activation.TraceAfter])
	reads, readErr := countTargetThreadReads(window, activation.Ref)
	if readErr != nil {
		t.Fatalf("activation trace window is not cleanly decodable: %v", readErr)
	}
	// Ruling 85: the read the entry route issues in this window is a
	// NON-subscribing thread/read carrying a held snapshot (the app's own
	// held-snapshot refresh), not a foreground subscription read. The zero-read
	// promise still holds and is proved by the entry-slice journey; here any
	// read must be that held-snapshot read, so a future regression that made the
	// pane open a foreground subscription read still fails.
	heldSnapshotOnly := reads > 0 &&
		activation.SubscribeReads == 0 &&
		activation.PlainReads == 0 &&
		activation.RecoveryReads == reads
	t.Logf("pane-activation-reads: reads=%d subscribe=%d recovery=%d plain=%d recoveryNeeded=%v localOpenStatus=%q",
		reads, activation.SubscribeReads, activation.RecoveryReads, activation.PlainReads,
		activation.PreOpenRecovery.RecoveryNeeded, activation.PostOpenRoute.LastLocalOpenStatus)
	if reads != 0 && !heldSnapshotOnly {
		t.Errorf(
			"opening the target pane issued %d thread/read activation read(s) for %s (subscribe=%d recovery=%d plain=%d, recoveryNeeded=%v); the retained background snapshot must satisfy pane readiness, and any read in a broken-readiness cut must be the non-subscribing held-snapshot recovery read",
			reads, activation.Ref, activation.SubscribeReads, activation.RecoveryReads, activation.PlainReads, activation.PreOpenRecovery.RecoveryNeeded,
		)
	} else if heldSnapshotOnly {
		t.Logf("pane activation issued %d held-snapshot refresh read(s) for %s (non-subscribing); render stayed local", reads, activation.Ref)
	}

	// An open history holder keeps its loaded pages and its model under
	// pressure; the driver's observation is that the pane's model stayed
	// resident while a sustained second session filled the budget.
	holder, holderOK := backgroundHolderMilestone(t, milestones)
	if !holderOK {
		t.Fatal("no history-holder milestone")
	}
	if !holder.ProtectedModel {
		t.Error("the open history holder's model was removed under pressure")
	}
	if holder.ItemsAfter < holder.ItemsBefore {
		t.Errorf("the open history holder lost loaded rows under pressure: %d -> %d", holder.ItemsBefore, holder.ItemsAfter)
	}

	// Reopen without a read: the retained later-page sentinel and cursor must
	// satisfy the reopened pane with no fresh thread/read in that window.
	var reopen struct {
		TraceBefore         int64  `json:"traceBefore"`
		TraceAfter          int64  `json:"traceAfter"`
		Sentinel            string `json:"sentinel"`
		Cursor              string `json:"cursor"`
		RestoredCursor      string `json:"restoredCursor"`
		KeptItems           bool   `json:"keptItems"`
		RestoredHasSentinel bool   `json:"restoredHasSentinel"`
		Reads               int    `json:"reads"`
		Pages               int    `json:"pages"`
		Ref                 string `json:"ref"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "reopen-without-read", &reopen) {
		t.Fatal("no reopen-without-read milestone")
	}
	if reopen.Sentinel == "" {
		t.Error("the reopened later-page sentinel is empty; the visible later page was not kept")
	}
	if reopen.TraceAfter < reopen.TraceBefore || reopen.TraceAfter > int64(len(traceBytes)) {
		t.Fatalf("reopen trace window %d..%d is invalid (trace is %d bytes)", reopen.TraceBefore, reopen.TraceAfter, len(traceBytes))
	}
	if reopen.Ref == "" {
		t.Fatal("the reopen milestone names no target ref, so unrelated background reads cannot be excluded")
	}
	// Local restoration must be checked BEFORE any fetch-capable gesture: reject
	// BOTH a target thread/read and a target thread/turns/list in the window, and
	// require the retained cursor and loaded identities to be preserved.
	reopenWindow := string(traceBytes[reopen.TraceBefore:reopen.TraceAfter])
	reopenReads, reopenErr := countTargetThreadReads(reopenWindow, reopen.Ref)
	if reopenErr != nil {
		t.Fatalf("reopen trace window is not cleanly decodable: %v", reopenErr)
	}
	reopenPages, pageErr := countTargetPageRequestsInWindow(reopenWindow, reopen.Ref)
	if pageErr != nil {
		t.Fatalf("reopen trace window pages are not cleanly decodable: %v", pageErr)
	}
	if reopenReads != 0 || reopenPages != 0 {
		t.Errorf("reopening the target fetched instead of restoring locally: thread/read=%d thread/turns/list=%d", reopenReads, reopenPages)
	}
	if !reopen.KeptItems {
		t.Error("reopen lost retained loaded history identities")
	}
	if !reopen.RestoredHasSentinel {
		t.Errorf("the retained later-page sentinel %q is missing from the restored model", reopen.Sentinel)
	}
	if reopen.RestoredCursor != reopen.Cursor {
		t.Errorf("reopen changed the cursor: retained %q vs restored %q", reopen.Cursor, reopen.RestoredCursor)
	}

	// The configured limits are the ones in effect. The ordinary entry route
	// bounds retained bodies in the shared write admission; a bare
	// `retainedBytes <= 32 KiB` alone passes when nothing is retained, so this
	// also requires a produced payload sized ABOVE the injected 32 KiB budget and
	// BELOW the 4 MiB default: only the injected limit can explain refusing work
	// a default would have kept. The hydration limit is the live residency
	// counter that the ordinary route's background reads still pass through.
	var sustained struct {
		PeakRetainedBytes          int64  `json:"peakRetainedBytes"`
		PeakNetworkHydrations      int64  `json:"peakNetworkHydrations"`
		PeakProducedBytes          int64  `json:"peakProducedBytes"`
		PeakProducedBytesLiveModel int64  `json:"peakProducedBytesLiveModel"`
		ProducedBasis              string `json:"producedBasis"`
		PendingLimitBytes          int64  `json:"pendingLimitBytes"`
		DefaultPendingLimitBytes   int64  `json:"defaultPendingLimitBytes"`
		HydrationLimit             int64  `json:"hydrationLimit"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "sustained-workload", &sustained) {
		t.Fatal("no sustained-workload milestone")
	}
	// Ruling 82: the produced payload is measured from the durable entry rows,
	// because the ordinary entry route keeps a background-only ref's live store
	// model as a deliberately pruned projection. Assert the basis explicitly so a
	// later reader can never mistake the live-model figure for the measured one.
	if sustained.ProducedBasis != "durable-entry-rows" {
		t.Errorf("sustained produced basis = %q, want durable-entry-rows (Ruling 82)", sustained.ProducedBasis)
	}
	if sustained.PendingLimitBytes != 32*1024 {
		t.Errorf("sustained pending limit = %d, want the injected 32768 (the dev entry's install)", sustained.PendingLimitBytes)
	}
	if sustained.DefaultPendingLimitBytes != 4*1024*1024 {
		t.Errorf("sustained default pending limit = %d, want 4194304", sustained.DefaultPendingLimitBytes)
	}
	if sustained.HydrationLimit != 2 {
		t.Errorf("sustained hydration limit = %d, want the spec's 2", sustained.HydrationLimit)
	}
	if sustained.PeakRetainedBytes > sustained.PendingLimitBytes {
		t.Errorf(
			"sustained retained peak %d exceeds the injected %d-byte shared admission budget",
			sustained.PeakRetainedBytes, sustained.PendingLimitBytes,
		)
	}
	if sustained.PeakNetworkHydrations > sustained.HydrationLimit {
		t.Errorf("sustained hydration peak %d exceeds the spec's %d concurrent background hydrations", sustained.PeakNetworkHydrations, sustained.HydrationLimit)
	}
	if sustained.PeakProducedBytes <= sustained.PendingLimitBytes {
		t.Errorf("sustained produced payload peak %d does not exceed the injected %d-byte shared budget, so pressure under it is not proven", sustained.PeakProducedBytes, sustained.PendingLimitBytes)
	}
	if sustained.PeakProducedBytes >= sustained.DefaultPendingLimitBytes {
		t.Errorf("sustained produced payload peak %d is not below the %d-byte default", sustained.PeakProducedBytes, sustained.DefaultPendingLimitBytes)
	}
	t.Logf("sustained retained peak %d bytes (injected %d), produced %d durable-entry-rows (live-model %d)",
		sustained.PeakRetainedBytes, sustained.PendingLimitBytes, sustained.PeakProducedBytes, sustained.PeakProducedBytesLiveModel)

	// Deterministic batching: the held-work phase was acknowledged, and the
	// committed writes advanced only after that acknowledgment.
	var batching struct {
		AckObserved       bool `json:"ackObserved"`
		TargetBackground  bool `json:"targetBackground"`
		WorkloadCommitted bool `json:"workloadCommitted"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "batching", &batching) {
		t.Fatal("no batching milestone")
	}
	if !batching.AckObserved {
		t.Error("the held-work phase was never acknowledged by the daemon after the fresh log offset")
	}
	if !batching.TargetBackground {
		t.Error("the batching pressure target was not established as a background holder")
	}
	if !batching.WorkloadCommitted {
		t.Error("the target's native checkpoint never contained the produced sustained workload after the release")
	}
}

func assertBackgroundEntrySlice(t *testing.T, fixture *skillGuardFixture, milestonesPath string) {
	t.Helper()
	milestones, err := backgroundReadMilestones(milestonesPath)
	if err != nil {
		t.Fatalf("read production-entry milestones: %v", err)
	}
	for _, name := range []string{"entry-baseline", "entry-opened", "entry-completed", "done"} {
		if !backgroundMilestonePresent(milestones, name) {
			t.Errorf("production-entry browser report is missing %q", name)
		}
	}
	var baseline struct {
		Ref               string `json:"ref"`
		EntryRows         int    `json:"entryRows"`
		LegacyRecordCount int    `json:"legacyRecordCount"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "entry-baseline", &baseline) {
		t.Fatal("entry baseline milestone missing")
	}
	if baseline.Ref == "" || baseline.EntryRows == 0 {
		t.Errorf("no native entry baseline for selected ref: %+v", baseline)
	}
	if baseline.LegacyRecordCount != 0 {
		t.Errorf("selected ref already has %d legacy cache records", baseline.LegacyRecordCount)
	}
	var opened struct {
		Ref                 string `json:"ref"`
		TraceBefore         int64  `json:"traceBefore"`
		TraceAfter          int64  `json:"traceAfter"`
		ActivationReads     int    `json:"activationReads"`
		ActivationPages     int    `json:"activationPages"`
		Running             bool   `json:"running"`
		LiveBeforePersisted bool   `json:"liveBeforePersisted"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "entry-opened", &opened) {
		t.Fatal("entry opened milestone missing")
	}
	trace, err := os.ReadFile(filepath.Join(fixture.artifact, "appwire-trace.jsonl"))
	if err != nil {
		t.Fatalf("read real AppWire trace: %v", err)
	}
	if opened.TraceBefore <= 0 || opened.TraceAfter <= opened.TraceBefore || opened.TraceAfter > int64(len(trace)) {
		t.Fatalf("invalid trace offsets in entry-opened milestone: %+v (trace bytes %d)", opened, len(trace))
	}
	window := string(trace[opened.TraceBefore:opened.TraceAfter])
	reads, err := countTargetThreadReads(window, opened.Ref)
	if err != nil {
		t.Fatalf("count activation thread/read frames: %v", err)
	}
	pages, err := countTargetPageRequestsInWindow(window, opened.Ref)
	if err != nil {
		t.Fatalf("count activation page frames: %v", err)
	}
	if reads != 0 || pages != 0 {
		t.Errorf("real AppWire trace shows activation reads/pages: %d/%d", reads, pages)
	}
	if opened.ActivationReads != 0 || opened.ActivationPages != 0 {
		t.Errorf("driver observed activation reads/pages: %d/%d", opened.ActivationReads, opened.ActivationPages)
	}
	if !opened.Running {
		t.Error("selected session was not streaming when opened")
	}
	if opened.LiveBeforePersisted {
		t.Error("live-only stream output was persisted before the turn completed")
	}
	frames, err := decodeTraceFrames(trace)
	if err != nil {
		t.Fatalf("decode real AppWire trace: %v", err)
	}
	target, err := targetTraceEvidenceFor(frames, opened.Ref)
	if err != nil {
		t.Fatalf("target AppWire evidence: %v", err)
	}
	if target.ReadResponses == 0 || !target.Subscribed {
		t.Errorf("trace has no initial subscribed background read for target: %+v", target)
	}
	var completed struct {
		DOMCounts               map[string]int `json:"domCounts"`
		RenderedOutputRegions   int            `json:"renderedOutputRegions"`
		ShellRows               int            `json:"shellRows"`
		RenderedOutputText      string         `json:"renderedOutputText"`
		RenderedToolItemCount   int            `json:"renderedToolItemCount"`
		RendererToNativeMatches bool           `json:"rendererToNativeMatches"`
		ProducerItemsCovered    bool           `json:"producerItemsCovered"`
		NativeEntryRows         int            `json:"nativeEntryRows"`
		NativeTurnRows          int            `json:"nativeTurnRows"`
		NativeCoverageRows      int            `json:"nativeCoverageRows"`
		NativeLegacyRows        int            `json:"nativeLegacyRows"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "entry-completed", &completed) {
		t.Fatal("entry completed milestone missing")
	}
	if completed.DOMCounts["before"] != 1 || completed.DOMCounts["after"] != 1 || completed.DOMCounts["tool"] != 1 ||
		completed.RenderedOutputRegions != 1 || completed.ShellRows != 1 || completed.RenderedToolItemCount != 1 {
		t.Errorf("renderer did not show each produced item exactly once in its real tool output region: %+v", completed)
	}
	if !completed.RendererToNativeMatches || !completed.ProducerItemsCovered {
		t.Errorf("renderer and native item/coverage records disagree: %+v", completed)
	}
	if completed.NativeEntryRows == 0 || completed.NativeTurnRows == 0 || completed.NativeCoverageRows == 0 {
		t.Errorf("native entry item, turn, or coverage store is empty: %+v", completed)
	}
	if completed.NativeLegacyRows != 0 {
		t.Errorf("selected session has %d legacy rows", completed.NativeLegacyRows)
	}
	transcript, err := backgroundTranscriptsFor(t, fixture.stateRoot, strings.TrimPrefix(opened.Ref, "local:"))
	if err != nil {
		t.Fatalf("read independent durable producer transcript: %v", err)
	}
	producerPath := filepath.Join(fixture.artifact, "target-producer-transcript.jsonl")
	if err := os.WriteFile(producerPath, []byte(transcript), 0o600); err != nil {
		t.Fatalf("retain raw producer transcript: %v", err)
	}
	producer, err := parseBackgroundEntryProducer(transcript)
	if err != nil {
		t.Fatalf("parse independent durable producer transcript: %v", err)
	}
	var audit struct {
		RenderedSentinelRows []struct {
			ID     string `json:"id"`
			TurnID string `json:"turnId"`
			Text   string `json:"text"`
		} `json:"renderedSentinelRows"`
		RecordedRendererItems []struct {
			ID            string `json:"id"`
			TurnID        string `json:"turnId"`
			Type          string `json:"type"`
			Text          string `json:"text"`
			Output        string `json:"output"`
			Arguments     string `json:"arguments"`
			Status        string `json:"status"`
			CallID        string `json:"callId"`
			TranscriptKey string `json:"transcriptKey"`
			Version       int    `json:"version"`
			Position      struct {
				Entry int `json:"entry"`
				Item  int `json:"item"`
				Sub   int `json:"sub"`
			} `json:"position"`
		} `json:"recordedRendererItems"`
		NativeItems []struct {
			TranscriptKey string `json:"transcriptKey"`
			Item          struct {
				ID        string `json:"id"`
				TurnID    string `json:"turnId"`
				Type      string `json:"type"`
				Text      string `json:"text"`
				Output    string `json:"output"`
				Arguments string `json:"argumentsJSON"`
				Status    string `json:"status"`
				CallID    string `json:"callId"`
				Version   int    `json:"version"`
				Position  struct {
					Entry int `json:"entry"`
					Item  int `json:"item"`
					Sub   int `json:"sub"`
				} `json:"position"`
			} `json:"item"`
			Position struct {
				Entry int `json:"entry"`
				Item  int `json:"item"`
				Sub   int `json:"sub"`
			} `json:"position"`
		} `json:"nativeItems"`
		NativeTurns []struct {
			TurnID string `json:"turnId"`
		} `json:"nativeTurns"`
		NativeCoverage []struct {
			RangeID string `json:"rangeId"`
			Kind    string `json:"kind"`
			Start   *struct {
				Entry int `json:"entry"`
				Item  int `json:"item"`
			} `json:"start"`
			End *struct {
				Entry int `json:"entry"`
				Item  int `json:"item"`
			} `json:"end"`
			Boundary string `json:"boundary"`
		} `json:"nativeCoverage"`
		RenderedOutputRegion struct {
			OutputRegions int      `json:"outputRegions"`
			ShellRows     int      `json:"shellRows"`
			Outputs       []string `json:"outputs"`
		} `json:"renderedOutputRegion"`
	}
	auditData, err := os.ReadFile(filepath.Join(fixture.artifact, "entry-audit.json"))
	if err != nil {
		t.Fatalf("read raw browser audit: %v", err)
	}
	if err := json.Unmarshal(auditData, &audit); err != nil {
		t.Fatalf("decode raw browser audit: %v", err)
	}
	if producer.assistantTurnID == "" || producer.toolCallID == "" || producer.toolResultCallID != producer.toolCallID {
		t.Fatalf("durable producer call identity is incomplete or mismatched: %+v", producer)
	}
	if !strings.Contains(completed.RenderedOutputText, producer.toolResultOutput) ||
		strings.Count(completed.RenderedOutputText, backgroundToolOutput) != 1 {
		t.Errorf("rendered output region does not contain the durable producer output exactly once: output=%q producer=%q", completed.RenderedOutputText, producer.toolResultOutput)
	}
	if audit.RenderedOutputRegion.OutputRegions != 1 || audit.RenderedOutputRegion.ShellRows != 1 ||
		len(audit.RenderedOutputRegion.Outputs) != 1 || audit.RenderedOutputRegion.Outputs[0] != completed.RenderedOutputText {
		t.Errorf("retained rendered output region differs from milestone: %+v", audit.RenderedOutputRegion)
	}
	producerIdentityMatched := false
	for _, item := range audit.RecordedRendererItems {
		if item.TurnID == producer.assistantTurnID && item.TranscriptKey != "" &&
			strings.Contains(item.Text, producer.assistantText) {
			producerIdentityMatched = true
		}
	}
	producerOutputMatched := false
	for _, item := range audit.RecordedRendererItems {
		if item.CallID == producer.toolCallID && item.TurnID == producer.assistantTurnID &&
			strings.Contains(item.Output, backgroundToolOutput) {
			producerOutputMatched = true
		}
	}
	if !producerIdentityMatched || !producerOutputMatched {
		t.Errorf("renderer item identity/content disagrees with durable producer turn/call: identity=%v output=%v producer=%+v", producerIdentityMatched, producerOutputMatched, producer)
	}
	for _, rendered := range audit.RecordedRendererItems {
		matches := 0
		for _, native := range audit.NativeItems {
			if native.TranscriptKey != rendered.TranscriptKey || native.Item.ID != rendered.ID || native.Item.TurnID != rendered.TurnID {
				continue
			}
			matches++
			if native.Item.Type != rendered.Type || native.Item.Text != rendered.Text || native.Item.Output != rendered.Output ||
				native.Item.Arguments != rendered.Arguments || native.Item.Status != rendered.Status ||
				native.Item.CallID != rendered.CallID || native.Item.Version != rendered.Version ||
				native.Position != rendered.Position || native.Item.Position != rendered.Position {
				t.Errorf("native item content or position differs from renderer item %q: renderer=%+v native=%+v", rendered.ID, rendered, native)
			}
			isProducerItem := rendered.TurnID == producer.assistantTurnID &&
				(strings.Contains(rendered.Text, backgroundBeforeEviction) || strings.Contains(rendered.Text, backgroundAfterEviction) ||
					(rendered.CallID == producer.toolCallID && strings.Contains(rendered.Output, backgroundToolOutput)))
			if isProducerItem {
				positionCovered := false
				for _, coverage := range audit.NativeCoverage {
					if coverage.Start == nil || coverage.End == nil {
						continue
					}
					atOrAfterStart := native.Position.Entry > coverage.Start.Entry ||
						(native.Position.Entry == coverage.Start.Entry && native.Position.Item >= coverage.Start.Item)
					atOrBeforeEnd := native.Position.Entry < coverage.End.Entry ||
						(native.Position.Entry == coverage.End.Entry && native.Position.Item <= coverage.End.Item)
					if atOrAfterStart && atOrBeforeEnd {
						positionCovered = true
					}
				}
				if !positionCovered {
					t.Errorf("durable producer item %q at %+v is outside native coverage boundaries", rendered.ID, native.Position)
				}
			}
		}
		if matches != 1 {
			t.Errorf("renderer item %q matched %d native item rows, want exactly one", rendered.ID, matches)
		}
	}
	for _, sentinel := range []string{backgroundBeforeEviction, backgroundAfterEviction} {
		var matches int
		for _, row := range audit.RenderedSentinelRows {
			if strings.Contains(row.Text, sentinel) {
				matches++
				if row.ID == "" || row.TurnID != producer.assistantTurnID {
					t.Errorf("rendered row for %q does not identify durable producer turn %q: %+v", sentinel, producer.assistantTurnID, row)
				}
			}
		}
		if matches != 1 {
			t.Errorf("rendered DOM contains %d rows for %q, want exactly one: %+v", matches, sentinel, audit.RenderedSentinelRows)
		}
	}
	nativeIdentityMatched := false
	for _, item := range audit.NativeItems {
		if item.Item.TurnID == producer.assistantTurnID &&
			(strings.Contains(item.Item.Text, producer.assistantText) ||
				(item.Item.CallID == producer.toolCallID && strings.Contains(item.Item.Output, backgroundToolOutput))) {
			nativeIdentityMatched = true
		}
	}
	nativeTurnMatched := false
	for _, turn := range audit.NativeTurns {
		if turn.TurnID == producer.assistantTurnID {
			nativeTurnMatched = true
		}
	}
	if !nativeIdentityMatched || !nativeTurnMatched {
		t.Errorf("native item/turn rows do not carry durable producer identity: item=%v turn=%v producer=%+v", nativeIdentityMatched, nativeTurnMatched, producer)
	}
	if len(audit.NativeCoverage) == 0 {
		t.Error("raw browser audit contains no native coverage boundaries")
	}
	for _, coverage := range audit.NativeCoverage {
		if coverage.RangeID == "" || coverage.Boundary == "" || coverage.Start == nil || coverage.End == nil {
			t.Errorf("raw browser audit contains an incomplete coverage boundary: %+v", coverage)
		}
	}
	t.Logf("production-entry artifacts: %s (milestones, appwire-trace.jsonl, driver.log, screenshots and dumps)", fixture.artifact)
}

type backgroundEntryProducer struct {
	assistantTurnID  string
	assistantText    string
	toolCallID       string
	toolCallName     string
	toolResultCallID string
	toolResultOutput string
}

func TestBackgroundEntryProducerReadsNestedTurnIdentity(t *testing.T) {
	transcript := strings.Join([]string{
		`{"kind":"entry","seq":203,"turn":{"kind":"ASSISTANT","turn_id":"turn_m41","message":{"content":[{"kind":"text","text":"BACKGROUND_BEFORE_EVICTIONBACKGROUND_AFTER_EVICTION"},{"kind":"tool_call","tool_call":{"id":"background_tool_call","name":"shell"}}]}}}`,
		`{"kind":"entry","seq":204,"turn":{"kind":"TOOL_RESULTS","turn_id":"turn_m41","message":{"content":[{"kind":"tool_result","tool_result":{"tool_call_id":"background_tool_call","content":"BACKGROUND_TOOL_OUTPUT\n[exit 0]"}}]}}}`,
	}, "\n")
	got, err := parseBackgroundEntryProducer(transcript)
	if err != nil {
		t.Fatal(err)
	}
	if got.assistantTurnID != "turn_m41" || got.toolCallID != "background_tool_call" || got.toolResultCallID != got.toolCallID {
		t.Fatalf("producer identities = %+v", got)
	}
}

func parseBackgroundEntryProducer(transcript string) (backgroundEntryProducer, error) {
	type part struct {
		Kind     string `json:"kind"`
		Text     string `json:"text"`
		ToolCall struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"tool_call"`
		ToolResult struct {
			ToolCallID string `json:"tool_call_id"`
			Content    string `json:"content"`
		} `json:"tool_result"`
	}
	type record struct {
		Kind   string `json:"kind"`
		TurnID string `json:"turn_id"`
		Turn   struct {
			Kind    string `json:"kind"`
			TurnID  string `json:"turn_id"`
			Message struct {
				Content []part `json:"content"`
			} `json:"message"`
		} `json:"turn"`
	}
	var got backgroundEntryProducer
	for lineNumber, line := range strings.Split(transcript, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row record
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return backgroundEntryProducer{}, fmt.Errorf("line %d: %w", lineNumber+1, err)
		}
		if row.Kind != "entry" {
			continue
		}
		turnID := row.TurnID
		if turnID == "" {
			turnID = row.Turn.TurnID
		}
		switch row.Turn.Kind {
		case "ASSISTANT":
			for _, content := range row.Turn.Message.Content {
				if content.Kind == "text" && strings.Contains(content.Text, backgroundBeforeEviction) && strings.Contains(content.Text, backgroundAfterEviction) {
					got.assistantTurnID = turnID
					got.assistantText = content.Text
				}
				if content.Kind == "tool_call" && content.ToolCall.Name == "shell" {
					got.toolCallID = content.ToolCall.ID
					got.toolCallName = content.ToolCall.Name
				}
			}
		case "TOOL_RESULTS":
			for _, content := range row.Turn.Message.Content {
				if content.Kind == "tool_result" && content.ToolResult.ToolCallID == got.toolCallID {
					got.toolResultCallID = content.ToolResult.ToolCallID
					got.toolResultOutput = content.ToolResult.Content
				}
			}
		}
	}
	if got.assistantTurnID == "" || got.toolCallID == "" || got.toolCallName != "shell" || got.toolResultCallID == "" ||
		strings.Count(got.assistantText, backgroundBeforeEviction) != 1 ||
		strings.Count(got.assistantText, backgroundAfterEviction) != 1 ||
		strings.Count(got.toolResultOutput, backgroundToolOutput) != 1 {
		return backgroundEntryProducer{}, fmt.Errorf("durable transcript does not contain one complete expected provider turn: %+v", got)
	}
	return got, nil
}

// countTargetThreadReads decodes the real hub AppWire trace window and counts
// thread/read frames for one ref. The trace records RAW WebSocket frames, so a
// plain substring count would also charge unrelated background progress (other
// refs' reads) to the target; only a decoded frame whose params.ref is the
// target ref counts as a target activation read.
// TestBackgroundTranscriptTargetTraceEvidence is a focused parser test for the
// trace decoder: it pairs responses with requests by id, selects only the named
// session, reads olderCursor from thread/read and nextCursor from
// thread/turns/list, sees the subscribe carry on a subscribed read, and rejects
// malformed data instead of reading it as zero.
func TestBackgroundTranscriptTargetTraceEvidence(t *testing.T) {
	const target = "local:tgt"
	lines := []string{
		`{"event":"frame","direction":"browser_to_hub","frame":"{\"id\":1,\"method\":\"thread/read\",\"params\":{\"ref\":\"local:tgt\",\"includeTurns\":true,\"subscribe\":true,\"itemLimit\":40}}"}`,
		`{"event":"frame","direction":"hub_to_browser","frame":"{\"id\":1,\"result\":{\"olderCursor\":\"cursor-1\"}}"}`,
		`{"event":"frame","direction":"browser_to_hub","frame":"{\"id\":2,\"method\":\"thread/read\",\"params\":{\"ref\":\"local:other\",\"includeTurns\":true,\"subscribe\":true}}"}`,
		`{"event":"frame","direction":"hub_to_browser","frame":"{\"id\":2,\"result\":{\"olderCursor\":\"other-cursor\"}}"}`,
		`{"event":"frame","direction":"browser_to_hub","frame":"{\"id\":3,\"method\":\"thread/turns/list\",\"params\":{\"ref\":\"local:tgt\",\"cursor\":\"cursor-1\"}}"}`,
		`{"event":"frame","direction":"hub_to_browser","frame":"{\"id\":3,\"result\":{\"data\":[],\"nextCursor\":\"cursor-2\"}}"}`,
		`{"event":"frame","direction":"browser_to_hub","frame":"{\"id\":4,\"method\":\"thread/turns/list\",\"params\":{\"ref\":\"local:tgt\",\"cursor\":\"cursor-2\"}}"}`,
		`{"event":"frame","direction":"hub_to_browser","frame":"{\"id\":4,\"result\":{\"data\":[],\"nextCursor\":\"cursor-3\"}}"}`,
	}
	frames, err := decodeTraceFrames([]byte(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatalf("decodeTraceFrames: %v", err)
	}
	ev, err := targetTraceEvidenceFor(frames, target)
	if err != nil {
		t.Fatalf("targetTraceEvidenceFor: %v", err)
	}
	if !ev.Subscribed {
		t.Error("subscribed = false, want the target's subscribe:true read")
	}
	if ev.InitialOlderCursor != "cursor-1" {
		t.Errorf("initialOlderCursor = %q, want cursor-1", ev.InitialOlderCursor)
	}
	if ev.PageCount != 2 {
		t.Errorf("pageCount = %d, want 2", ev.PageCount)
	}
	if ev.LastNextCursor != "cursor-3" {
		t.Errorf("lastNextCursor = %q, want cursor-3", ev.LastNextCursor)
	}
	if ev.ReadResponses != 1 {
		t.Errorf("readResponses = %d, want 1 (the other session's read must be excluded)", ev.ReadResponses)
	}
	if _, err := decodeTraceFrames([]byte("{not-json}")); err == nil {
		t.Error("decodeTraceFrames accepted malformed data instead of failing")
	}
}

// TestBackgroundTranscriptTargetTraceEvidenceReusedIDsAcrossConnections pins
// that a response is correlated to the request on its OWN connection. The real
// trace already reuses request IDs across the authentication/reconnect boundary
// (P/evidence/appwire-trace.jsonl:2,6), so a decoder keyed by ID alone
// misattributes a later request's ref/cursor to an earlier response.
func TestBackgroundTranscriptTargetTraceEvidenceReusedIDsAcrossConnections(t *testing.T) {
	lines := []string{
		`{"event":"frame","connection":"conn-1","direction":"browser_to_hub","frame":"{\"id\":1,\"method\":\"thread/read\",\"params\":{\"ref\":\"local:A\",\"includeTurns\":true,\"subscribe\":true,\"itemLimit\":40}}"}`,
		`{"event":"frame","connection":"conn-1","direction":"hub_to_browser","frame":"{\"id\":1,\"result\":{\"olderCursor\":\"a1\"}}"}`,
		`{"event":"frame","connection":"conn-1","direction":"browser_to_hub","frame":"{\"id\":2,\"method\":\"thread/turns/list\",\"params\":{\"ref\":\"local:A\",\"cursor\":\"a1\"}}"}`,
		`{"event":"frame","connection":"conn-1","direction":"hub_to_browser","frame":"{\"id\":2,\"result\":{\"data\":[],\"nextCursor\":\"a2\"}}"}`,
		`{"event":"frame","connection":"conn-2","direction":"browser_to_hub","frame":"{\"id\":1,\"method\":\"thread/read\",\"params\":{\"ref\":\"local:B\",\"includeTurns\":true,\"subscribe\":true,\"itemLimit\":40}}"}`,
		`{"event":"frame","connection":"conn-2","direction":"hub_to_browser","frame":"{\"id\":1,\"result\":{\"olderCursor\":\"b1\"}}"}`,
		`{"event":"frame","connection":"conn-2","direction":"browser_to_hub","frame":"{\"id\":2,\"method\":\"thread/turns/list\",\"params\":{\"ref\":\"local:B\",\"cursor\":\"b1\"}}"}`,
		`{"event":"frame","connection":"conn-2","direction":"hub_to_browser","frame":"{\"id\":2,\"result\":{\"data\":[],\"nextCursor\":\"b2\"}}"}`,
	}
	frames, err := decodeTraceFrames([]byte(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatalf("decodeTraceFrames: %v", err)
	}
	a, err := targetTraceEvidenceFor(frames, "local:A")
	if err != nil {
		t.Fatalf("A evidence: %v", err)
	}
	b, err := targetTraceEvidenceFor(frames, "local:B")
	if err != nil {
		t.Fatalf("B evidence: %v", err)
	}
	if a.InitialOlderCursor != "a1" || a.PageCount != 1 || a.LastNextCursor != "a2" {
		t.Errorf("conn-1 (local:A) evidence = %+v, want olderCursor a1, 1 page, nextCursor a2", a)
	}
	if b.InitialOlderCursor != "b1" || b.PageCount != 1 || b.LastNextCursor != "b2" {
		t.Errorf("conn-2 (local:B) evidence = %+v, want olderCursor b1, 1 page, nextCursor b2", b)
	}
}

// TestBackgroundTranscriptDurableTranscriptPath pins that the durable-transcript
// reader follows the fixture's real layout: the daemons persist to
// stateRoot/projects/<project>/sessions/<id>.transcript.jsonl
// (skill_composer_browser_test.go:534-543). A reader that omits the "projects"
// segment silently finds nothing and fails every content assertion regardless of
// browser success.
func TestBackgroundTranscriptDurableTranscriptPath(t *testing.T) {
	root := t.TempDir()
	write := func(project, sessionID, content string) {
		t.Helper()
		dir := filepath.Join(root, "projects", project, "sessions")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir fixture layout: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, sessionID+".transcript.jsonl"), []byte(content+"\n"), 0o600); err != nil {
			t.Fatalf("write fixture transcript: %v", err)
		}
	}
	write("alpha-0123456789", "01TARGETaaaaaaaaaaaaaaa", "BACKGROUND_TARGET_DURABLE")
	write("alpha-0123456789", "01OTHERaaaaaaaaaaaaaaaa", "BACKGROUND_OTHER_DURABLE")
	got, err := backgroundTranscriptsFor(t, root, "01TARGETaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("target transcript read: %v", err)
	}
	if !strings.Contains(got, "BACKGROUND_TARGET_DURABLE") {
		t.Errorf("target transcript content missing: %q", got)
	}
	if strings.Contains(got, "BACKGROUND_OTHER_DURABLE") {
		t.Errorf("reader combined another session's transcript: %q", got)
	}
	if _, err := backgroundTranscriptsFor(t, root, "01MISSINGaaaaaaaaaaaaaa"); err == nil {
		t.Error("a missing target transcript did not fail")
	}
}

// traceFrame is one decoded hub AppWire frame. Connection is the hub-side
// connection string (canonical websocket_trace.go), so requests and responses
// pair within one connection and a reused request id on a later connection
// cannot be misattributed.
type traceFrame struct {
	Connection string
	Direction  string
	ID         string
	Method     string
	Params     json.RawMessage
	Result     json.RawMessage
}

// decodeTraceFrames parses the hub AppWire trace JSONL into frames. Each line's
// `frame` is parsed as JSON; malformed records are an error, never a silent
// zero. It never scans escaped raw text for a value.
func decodeTraceFrames(data []byte) ([]traceFrame, error) {
	type envelope struct {
		Event      string  `json:"event"`
		Connection string  `json:"connection"`
		Direction  string  `json:"direction"`
		Frame      *string `json:"frame"`
	}
	var frames []traceFrame
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var env envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			return nil, fmt.Errorf("malformed trace record %q: %w", line, err)
		}
		if env.Event != "frame" || env.Frame == nil {
			continue
		}
		var fr struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(*env.Frame), &fr); err != nil {
			return nil, fmt.Errorf("malformed trace frame %q: %w", *env.Frame, err)
		}
		frames = append(frames, traceFrame{
			Connection: env.Connection,
			Direction:  env.Direction,
			ID:         string(fr.ID),
			Method:     fr.Method,
			Params:     fr.Params,
			Result:     fr.Result,
		})
	}
	return frames, nil
}

// targetTraceEvidence is the target session's real paging/subscription evidence.
type targetTraceEvidence struct {
	Subscribed         bool
	InitialOlderCursor string
	ReadResponses      int
	PageCount          int
	LastNextCursor     string
}

// targetTraceEvidenceFor pairs each response with the most recent UNMATCHED
// request on the same connection and id, requiring opposite directions, so a
// reused id (sequentially within a connection, or across the reconnect
// boundary) pairs with the right request. It returns only evidence for ref.
func targetTraceEvidenceFor(frames []traceFrame, ref string) (targetTraceEvidence, error) {
	var ev targetTraceEvidence
	pending := map[string]map[string]traceFrame{}
	threadID := strings.TrimPrefix(ref, "local:")
	for _, f := range frames {
		if f.Method != "" && f.ID != "" && len(f.Result) == 0 {
			if pending[f.Connection] == nil {
				pending[f.Connection] = map[string]traceFrame{}
			}
			pending[f.Connection][f.ID] = f
			continue
		}
		if len(f.Result) == 0 {
			continue
		}
		byID := pending[f.Connection]
		req, ok := byID[f.ID]
		if !ok {
			continue
		}
		delete(byID, f.ID)
		if req.Direction == f.Direction {
			// Not a request/response pair (e.g. a notification echoed twice).
			continue
		}
		var params struct {
			Ref       string `json:"ref"`
			ThreadID  string `json:"threadId"`
			Subscribe bool   `json:"subscribe"`
			Replace   bool   `json:"replaceSubscription"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return ev, fmt.Errorf("malformed trace params: %w", err)
			}
		}
		if params.Ref != ref && params.ThreadID != threadID {
			continue
		}
		switch req.Method {
		case "thread/read":
			ev.ReadResponses++
			if params.Subscribe || params.Replace {
				ev.Subscribed = true
			}
			var result struct {
				OlderCursor *string `json:"olderCursor"`
			}
			if err := json.Unmarshal(f.Result, &result); err != nil {
				return ev, fmt.Errorf("malformed thread/read result: %w", err)
			}
			if result.OlderCursor != nil && ev.InitialOlderCursor == "" {
				ev.InitialOlderCursor = *result.OlderCursor
			}
		case "thread/turns/list":
			var result struct {
				NextCursor *string `json:"nextCursor"`
			}
			if err := json.Unmarshal(f.Result, &result); err != nil {
				return ev, fmt.Errorf("malformed thread/turns/list result: %w", err)
			}
			ev.PageCount++
			if result.NextCursor != nil {
				ev.LastNextCursor = *result.NextCursor
			} else {
				ev.LastNextCursor = ""
			}
		case "thread/subscribe":
			ev.Subscribed = true
		}
	}
	return ev, nil
}

// countTargetPageRequestsInWindow counts the target's thread/turns/list REQUEST
// frames in a raw trace window, so a local-restoration interval can reject
// older-page fetches as well as thread/read activations.
func countTargetPageRequestsInWindow(window, targetRef string) (int, error) {
	frames, err := decodeTraceFrames([]byte(window))
	if err != nil {
		return 0, err
	}
	threadID := strings.TrimPrefix(targetRef, "local:")
	count := 0
	for _, f := range frames {
		if f.Method != "thread/turns/list" || len(f.Params) == 0 {
			continue
		}
		var params struct {
			Ref      string `json:"ref"`
			ThreadID string `json:"threadId"`
		}
		if err := json.Unmarshal(f.Params, &params); err != nil {
			return 0, fmt.Errorf("malformed thread/turns/list params: %w", err)
		}
		if params.Ref == targetRef || params.ThreadID == threadID {
			count++
		}
	}
	return count, nil
}

func countTargetThreadReads(window, targetRef string) (int, error) {
	type frameEnvelope struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	type readParams struct {
		Ref string `json:"ref"`
	}
	count := 0
	for _, line := range strings.Split(window, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record struct {
			Event string  `json:"event"`
			Frame *string `json:"frame"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return 0, fmt.Errorf("malformed trace record %q: %w", line, err)
		}
		if record.Event != "frame" || record.Frame == nil {
			continue
		}
		var frame frameEnvelope
		if err := json.Unmarshal([]byte(*record.Frame), &frame); err != nil {
			return 0, fmt.Errorf("malformed trace frame %q: %w", *record.Frame, err)
		}
		if frame.Method != "thread/read" {
			continue
		}
		var params readParams
		if err := json.Unmarshal(frame.Params, &params); err != nil {
			return 0, fmt.Errorf("malformed thread/read params %q: %w", string(frame.Params), err)
		}
		if params.Ref == targetRef {
			count++
		}
	}
	return count, nil
}

func backgroundReadMilestones(path string) ([]skillGuardMilestone, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []skillGuardMilestone
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m skillGuardMilestone
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func backgroundMilestonePresent(milestones []skillGuardMilestone, name string) bool {
	for _, m := range milestones {
		if m.Milestone == name {
			return true
		}
	}
	return false
}

func backgroundReadLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		t.Fatalf("read log %s: %v", path, err)
	}
	return string(data)
}

// backgroundTranscriptsFor reads exactly the named target session's durable
// transcript under the fixture's real layout:
// stateRoot/projects/<project>/sessions/<sessionID>.transcript.jsonl
// (skill_composer_browser_test.go:534-543). A missing or unreadable expected
// file is an error, never empty content, and no other session's transcript is
// combined in.
func backgroundTranscriptsFor(t *testing.T, stateRoot, sessionID string) (string, error) {
	t.Helper()
	if sessionID == "" {
		return "", errors.New("no target session id for the durable transcript read")
	}
	projectsRoot := filepath.Join(stateRoot, "projects")
	entries, err := os.ReadDir(projectsRoot)
	if err != nil {
		return "", fmt.Errorf("read projects root %s: %w", projectsRoot, err)
	}
	var b strings.Builder
	found := false
	for _, project := range entries {
		if !project.IsDir() {
			continue
		}
		path := filepath.Join(projectsRoot, project.Name(), "sessions", sessionID+".transcript.jsonl")
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("read target transcript %s: %w", path, err)
		}
		found = true
		b.Write(data)
	}
	if !found {
		return "", fmt.Errorf("target transcript %s.transcript.jsonl not found under %s", sessionID, projectsRoot)
	}
	return b.String(), nil
}
