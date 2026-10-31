//go:build browserguard

// TestBackgroundTranscriptsEntrySliceBrowser is the whole-journey proof for
// background Live transcripts on the production entry route: the REAL hub web
// app (real AppWire, real stores, native IndexedDB) driven in real Chrome in
// Vite development mode through a REAL hub (httptest around web.Handler() with
// the real roster prober and past index) against TWO REAL `evener serve` daemons
// (cmd/evener's compiled browserguard helper binary), with the only scripted
// piece at the external LLM provider adapter.
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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
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
	// backgroundGapTurn is the turn produced on the target's own daemon while the
	// browser socket is down; the reconnect's authoritative window must carry it.
	backgroundGapTurn = "BACKGROUND_GAP_TURN"
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
	gate := newBackgroundTransportGate(web.Handler(), backgroundTransportRecorder(fixture))
	hub := httptest.NewServer(gate)
	defer hub.Close()
	// Drain before hub.Close and before trace/fixture cleanup, including when
	// the Node owner fails. Reuse the existing helper-cleanup reserve.
	drainCtx := ctx
	if deadline, ok := t.Deadline(); ok {
		var drainCancel context.CancelFunc
		drainCtx, drainCancel = context.WithDeadline(ctx, deadline.Add(-backgroundHelperCleanupReserve))
		defer drainCancel()
	}
	drain := func() {
		if err := gate.close(drainCtx, true); err != nil {
			t.Errorf("terminal transport drain: %v", err)
		}
	}
	defer drain()
	milestones := filepath.Join(fixture.artifact, "milestones.jsonl")
	if err := os.WriteFile(milestones, nil, 0o600); err != nil {
		t.Fatalf("create milestone file: %v", err)
	}
	lt := backgroundDriverLifetime{Configured: backgroundDriverTimeout, Join: backgroundDriverJoinReserve, Cleanup: backgroundHelperCleanupReserve}
	deadline, hasDeadline := t.Deadline()
	launch := backgroundDriverLaunchForMode(t, fixture, hub.URL, hubedge.AuthURLFor(hub.URL, hex.EncodeToString(tokenBytes)), milestones, entries, true)
	driverErr := runOwnedBackgroundDriver(t, deadline, hasDeadline, lt, launch)
	drain()
	for i := range entries {
		skillGuardStopHelper(t, i)
	}
	assertBackgroundDisplay(t, milestones)
	assertBackgroundEntrySlice(t, fixture, milestones)
	if driverErr != nil {
		t.Errorf("%v\nfull artifacts (logs, screenshots, dumps): %s", driverErr, fixture.artifact)
	}
}

type backgroundDisplaySnapshot struct {
	Local            map[string]json.RawMessage   `json:"local"`
	Effective        map[string]json.RawMessage   `json:"effective"`
	Publications     []map[string]json.RawMessage `json:"publications"`
	Messages         []json.RawMessage            `json:"messages"`
	Storage          []backgroundDisplayStorage   `json:"storage"`
	Persisted        map[string]*string           `json:"persisted"`
	BroadcastChannel string                       `json:"broadcast_channel"`
	StorageBlocked   bool                         `json:"storage_blocked"`
	Origin           string                       `json:"origin"`
	Href             string                       `json:"href"`
}

type backgroundDisplayStorage struct {
	Key          string  `json:"key"`
	OldValue     *string `json:"old_value"`
	NewValue     *string `json:"new_value"`
	URL          string  `json:"url"`
	Trusted      bool    `json:"trusted"`
	LocalStorage bool    `json:"local_storage"`
	Blocked      bool    `json:"blocked"`
}

// Independent literals pin the whole config, not only a preset or driver flag.
var backgroundDisplayConfigs = [...]json.RawMessage{
	json.RawMessage(`{"version":1,"content":{"kind":"preset","level":"full"},"advanced":{"roundTimings":true,"tokenCounts":false,"estimatedCost":true,"systemEvents":false,"promptEvents":true,"hookExits":"all"}}`),
	json.RawMessage(`{"version":1,"content":{"kind":"preset","level":"tools"},"advanced":{"roundTimings":false,"tokenCounts":true,"estimatedCost":false,"systemEvents":true,"promptEvents":false,"hookExits":"successful"}}`),
	json.RawMessage(`{"version":1,"content":{"kind":"preset","level":"activity"},"advanced":{"roundTimings":false,"tokenCounts":true,"estimatedCost":true,"systemEvents":true,"promptEvents":false,"hookExits":"none"}}`),
	json.RawMessage(`{"version":1,"content":{"kind":"preset","level":"intent"},"advanced":{"roundTimings":true,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":true,"hookExits":"all"}}`),
}

func backgroundDisplayEqual(a, b any) bool {
	normalize := func(value any) any {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		var decoded any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return err
		}
		return decoded
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func assertBackgroundDisplay(t *testing.T, milestonesPath string) {
	t.Helper()
	milestones, err := backgroundReadMilestones(milestonesPath)
	if err != nil {
		t.Fatalf("read display milestones: %v", err)
	}
	var nativeSources [2]string
	var nativeTargets [2]string
	for _, transport := range []string{"broadcast", "storage"} {
		var baseline struct {
			TargetIDs [2]string                    `json:"target_ids"`
			Tabs      [2]backgroundDisplaySnapshot `json:"tabs"`
		}
		if !skillGuardMilestoneDetail(t, milestones, "display-"+transport+"-baseline", &baseline) {
			t.Errorf("missing mandatory display %s baseline", transport)
			continue
		}
		if baseline.TargetIDs[0] == "" || baseline.TargetIDs[1] == "" || baseline.TargetIDs[0] == baseline.TargetIDs[1] || baseline.Tabs[0].Origin == "" || baseline.Tabs[0].Origin != baseline.Tabs[1].Origin {
			t.Errorf("display %s did not use distinct same-origin tabs: %+v", transport, baseline)
		}
		if transport == "broadcast" {
			nativeTargets = baseline.TargetIDs
		} else {
			for _, target := range baseline.TargetIDs {
				if target == nativeTargets[0] || target == nativeTargets[1] {
					t.Error("storage fallback reused a BroadcastChannel document")
				}
			}
		}
		for n, tab := range baseline.Tabs {
			wantChannel := "function"
			if transport == "storage" {
				wantChannel = "undefined"
			}
			if !backgroundDisplayEqual(tab.Local, map[string]json.RawMessage{}) || len(tab.Effective) != 2 || tab.BroadcastChannel != wantChannel || tab.StorageBlocked != (transport == "broadcast") || tab.Href == "" {
				t.Errorf("display %s tab %d invalid empty/transport baseline: %+v", transport, n, tab)
			}
		}
		expected := map[string]json.RawMessage{}
		// This list is deliberately not read from the driver's operation table.
		for _, operation := range []struct {
			name   string
			sender int
			layout string
			config int
		}{
			{"desktop-v1", 0, "desktop", 0}, {"mobile-v1", 1, "mobile", 1},
			{"desktop-v2", 0, "desktop", 2}, {"mobile-v2", 1, "mobile", 3},
			{"clear-desktop", 0, "desktop", -1}, {"clear-mobile", 1, "mobile", -1},
		} {
			name := "display-" + transport + "-" + operation.name
			var record struct {
				Sender int                          `json:"sender"`
				Layout string                       `json:"layout"`
				Before [2]backgroundDisplaySnapshot `json:"before"`
				After  [2]backgroundDisplaySnapshot `json:"after"`
			}
			if !skillGuardMilestoneDetail(t, milestones, name, &record) {
				t.Errorf("missing mandatory display milestone %q", name)
				continue
			}
			if record.Sender != operation.sender || record.Layout != operation.layout {
				t.Errorf("%s wrong direction/layout: %+v", name, record)
			}
			var oldValue, newValue *string
			if old, ok := expected[operation.layout]; ok {
				value := string(old)
				oldValue = &value
			}
			for n, before := range record.Before {
				if !backgroundDisplayEqual(before.Local, expected) || len(before.Publications) != 0 || len(before.Messages) != 0 || len(before.Storage) != 0 {
					t.Errorf("%s tab %d wrong before state or observations not reset: %+v", name, n, before)
				}
			}
			if operation.config < 0 {
				delete(expected, operation.layout)
			} else {
				config := backgroundDisplayConfigs[operation.config]
				expected[operation.layout] = config
				value := string(config)
				newValue = &value
			}
			for n, after := range record.After {
				if !backgroundDisplayEqual(after.Local, expected) || len(after.Publications) != 1 || !backgroundDisplayEqual(after.Publications[0], expected) {
					t.Errorf("%s tab %d wrong full local map or publication history: %+v", name, n, after)
				}
				for _, layout := range []string{"desktop", "mobile"} {
					config, present := expected[layout]
					var persisted *string
					if present {
						value := string(config)
						persisted = &value
					} else {
						config = baseline.Tabs[n].Effective[layout]
					}
					if !backgroundDisplayEqual(after.Effective[layout], config) || !reflect.DeepEqual(after.Persisted[layout], persisted) {
						t.Errorf("%s tab %d wrong %s effective/durable config: %+v", name, n, layout, after)
					}
				}
				if n == operation.sender {
					if len(after.Storage) != 0 {
						t.Errorf("%s sender received its own storage event: %+v", name, after.Storage)
					}
				} else {
					want := []backgroundDisplayStorage{{Key: "evener.prefs.transcriptDisplay." + operation.layout, OldValue: oldValue, NewValue: newValue, URL: baseline.Tabs[operation.sender].Href, Trusted: true, LocalStorage: true, Blocked: transport == "broadcast"}}
					if !reflect.DeepEqual(after.Storage, want) {
						t.Errorf("%s receiver storage events = %+v, want %+v", name, after.Storage, want)
					}
				}
				if transport == "storage" {
					if len(after.Messages) != 0 {
						t.Errorf("%s fallback used BroadcastChannel: %s", name, after.Messages)
					}
					continue
				}
				if len(after.Messages) != 1 {
					t.Errorf("%s tab %d expected one native envelope, got %s", name, n, after.Messages)
					continue
				}
				var envelope map[string]any
				if err := json.Unmarshal(after.Messages[0], &envelope); err != nil {
					t.Errorf("%s malformed envelope: %v", name, err)
					continue
				}
				source, _ := envelope["sourceId"].(string)
				if source == "" || (nativeSources[operation.sender] != "" && source != nativeSources[operation.sender]) {
					t.Errorf("%s unstable or empty source ID: %q", name, source)
				}
				nativeSources[operation.sender] = source
				want := map[string]any{"version": 1, "sourceId": source, "layout": operation.layout, "config": newValue, "fingerprint": newValue}
				if !backgroundDisplayEqual(envelope, want) {
					t.Errorf("%s tab %d wrong exact five-field envelope: %s", name, n, after.Messages[0])
				}
			}
		}
	}
	if nativeSources[0] == "" || nativeSources[1] == "" || nativeSources[0] == nativeSources[1] {
		t.Errorf("display native senders must have distinct nonempty source IDs: %q", nativeSources)
	}
	var fingerprint struct {
		Invalid  json.RawMessage              `json:"invalid"`
		Valid    json.RawMessage              `json:"valid"`
		Before   [2]backgroundDisplaySnapshot `json:"before"`
		Rejected [2]backgroundDisplaySnapshot `json:"rejected"`
		Accepted [2]backgroundDisplaySnapshot `json:"accepted"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "display-fingerprint", &fingerprint) {
		t.Error("missing mandatory display fingerprint rejection and positive control")
		return
	}
	source := "display-guard-fingerprint-control"
	invalid := map[string]any{"version": 1, "sourceId": source, "layout": "desktop", "config": string(backgroundDisplayConfigs[0]), "fingerprint": string(backgroundDisplayConfigs[1])}
	valid := map[string]any{"version": 1, "sourceId": source, "layout": "desktop", "config": string(backgroundDisplayConfigs[0]), "fingerprint": string(backgroundDisplayConfigs[0])}
	if source == nativeSources[0] || source == nativeSources[1] || !backgroundDisplayEqual(fingerprint.Invalid, invalid) || !backgroundDisplayEqual(fingerprint.Valid, valid) {
		t.Errorf("fingerprint control changed more than the fingerprint or used a real sender identity: %+v", fingerprint)
	}
	for n := range 2 {
		before, rejected, accepted := fingerprint.Before[n], fingerprint.Rejected[n], fingerprint.Accepted[n]
		want := map[string]json.RawMessage{"desktop": backgroundDisplayConfigs[0]}
		if !backgroundDisplayEqual(before.Local, map[string]json.RawMessage{}) || len(before.Publications) != 0 || !backgroundDisplayEqual(rejected.Local, before.Local) || len(rejected.Publications) != 0 || !backgroundDisplayEqual(rejected.Messages, []any{invalid}) {
			t.Errorf("fingerprint mismatch was not observed and rejected without transient publication in tab %d: %+v", n, rejected)
		}
		if !backgroundDisplayEqual(accepted.Local, want) || !backgroundDisplayEqual(accepted.Publications, []any{want}) || !backgroundDisplayEqual(accepted.Messages, []any{invalid, valid}) || !backgroundDisplayEqual(accepted.Effective["desktop"], backgroundDisplayConfigs[0]) || !backgroundDisplayEqual(accepted.Effective["mobile"], before.Effective["mobile"]) {
			t.Errorf("corrected fingerprint was not observed and applied exactly once in tab %d: %+v", n, accepted)
		}
		if len(rejected.Storage) != 0 || len(accepted.Storage) != 0 || !reflect.DeepEqual(before.Persisted, rejected.Persisted) || !reflect.DeepEqual(before.Persisted, accepted.Persisted) {
			t.Errorf("fingerprint injection wrote storage in tab %d", n)
		}
	}
}

// runBackgroundDriverMode launches the Node driver. --hub-url is the fixture
// hub's base URL; the driver sets EVENER_HUB_URL from it before starting Vite
// so the dev server's proxy forwards /rpc and the hub HTTP routes.
// --control-path-a/-b are the two helpers' control files, so a phase release
// reaches the daemon that owns the driven session. entrySlice adds
// --entry-production true, selecting the production-entry journey; the legacy
// session-cache journey was retired with the legacy stores (6d-2b) and the
// driver rejects a run without it.
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
func awaitBackgroundDriver(t *testing.T, budget, joinReserve time.Duration, cancel context.CancelFunc, done <-chan error, finished <-chan struct{}) (timedOut, joined bool, exitErr error) {
	select {
	case exitErr = <-done:
		return false, true, exitErr
	case <-time.After(budget):
	}
	cancel()
	joined = skillGuardAwaitDriver(t, finished, joinReserve)
	if joined {
		exitErr = <-done
	}
	return true, joined, exitErr
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
	timedOut, joined, exitErr := awaitBackgroundDriver(t, budget, lt.Join, cancel, done, finished)
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
	return backgroundDriverLaunchForMode(t, fixture, hubURL, authURL, milestones, entries, true)
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

func assertBackgroundEntrySlice(t *testing.T, fixture *skillGuardFixture, milestonesPath string) {
	t.Helper()
	milestones, err := backgroundReadMilestones(milestonesPath)
	if err != nil {
		t.Fatalf("read production-entry milestones: %v", err)
	}
	for _, name := range []string{
		"entry-baseline", "entry-opened", "entry-completed",
		"history-paging", "reopen-without-read", "further-paging",
		"ws-broken", "gap-turn-completed", "reconnected-latest-window", "paged-back-sentinel",
		"narrow-viewport", "two-tab-delete-fence", "two-tab-delete-refusal", "two-tab-clear-fence",
		"responsive-reading", "new-pane-latest",
		"done",
	} {
		if !backgroundMilestonePresent(milestones, name) {
			t.Errorf("production-entry browser report is missing %q", name)
		}
	}
	var baseline struct {
		Ref               string `json:"ref"`
		EntryRows         int    `json:"entryRows"`         //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field entryRows.
		LegacyRecordCount int    `json:"legacyRecordCount"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field legacyRecordCount.
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
		TraceBefore         int64  `json:"traceBefore"`     //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceBefore.
		TraceAfter          int64  `json:"traceAfter"`      //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceAfter.
		ActivationReads     int    `json:"activationReads"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field activationReads.
		ActivationPages     int    `json:"activationPages"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field activationPages.
		Running             bool   `json:"running"`
		LiveBeforePersisted bool   `json:"liveBeforePersisted"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field liveBeforePersisted.
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
		DOMCounts               map[string]int `json:"domCounts"`               //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field domCounts.
		RenderedOutputRegions   int            `json:"renderedOutputRegions"`   //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field renderedOutputRegions.
		ShellRows               int            `json:"shellRows"`               //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field shellRows.
		RenderedOutputText      string         `json:"renderedOutputText"`      //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field renderedOutputText.
		RenderedToolItemCount   int            `json:"renderedToolItemCount"`   //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field renderedToolItemCount.
		RendererToNativeMatches bool           `json:"rendererToNativeMatches"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field rendererToNativeMatches.
		ProducerItemsCovered    bool           `json:"producerItemsCovered"`    //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field producerItemsCovered.
		NativeEntryRows         int            `json:"nativeEntryRows"`         //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field nativeEntryRows.
		NativeTurnRows          int            `json:"nativeTurnRows"`          //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field nativeTurnRows.
		NativeCoverageRows      int            `json:"nativeCoverageRows"`      //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field nativeCoverageRows.
		NativeLegacyRows        int            `json:"nativeLegacyRows"`        //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field nativeLegacyRows.
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
			TurnID string `json:"turnId"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field turnId.
			Text   string `json:"text"`
		} `json:"renderedSentinelRows"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field renderedSentinelRows.
		RecordedRendererItems []struct {
			ID            string `json:"id"`
			TurnID        string `json:"turnId"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field turnId.
			Type          string `json:"type"`
			Text          string `json:"text"`
			Output        string `json:"output"`
			Arguments     string `json:"arguments"`
			Status        string `json:"status"`
			CallID        string `json:"callId"`        //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field callId.
			TranscriptKey string `json:"transcriptKey"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field transcriptKey.
			Version       int    `json:"version"`
			Position      struct {
				Entry int `json:"entry"`
				Item  int `json:"item"`
				Sub   int `json:"sub"`
			} `json:"position"`
		} `json:"recordedRendererItems"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field recordedRendererItems.
		NativeItems []struct {
			TranscriptKey string `json:"transcriptKey"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field transcriptKey.
			Item          struct {
				ID        string `json:"id"`
				TurnID    string `json:"turnId"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field turnId.
				Type      string `json:"type"`
				Text      string `json:"text"`
				Output    string `json:"output"`
				Arguments string `json:"argumentsJSON"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field argumentsJSON.
				Status    string `json:"status"`
				CallID    string `json:"callId"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field callId.
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
		} `json:"nativeItems"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field nativeItems.
		NativeTurns []struct {
			TurnID string `json:"turnId"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field turnId.
		} `json:"nativeTurns"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field nativeTurns.
		NativeCoverage []struct {
			RangeID string `json:"rangeId"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field rangeId.
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
		} `json:"nativeCoverage"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field nativeCoverage.
		RenderedOutputRegion struct {
			OutputRegions int      `json:"outputRegions"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field outputRegions.
			ShellRows     int      `json:"shellRows"`     //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field shellRows.
			Outputs       []string `json:"outputs"`
		} `json:"renderedOutputRegion"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field renderedOutputRegion.
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
	assertBackgroundEntryPagingAndReconnect(t, fixture, opened.Ref, milestones)
	assertBackgroundEntryNarrowViewport(t, milestones)
	assertBackgroundReadingPlacement(t, milestones)
	assertBackgroundTwoTabLive(t, milestones, producer)
	assertBackgroundEntryTwoTabFencing(t, milestones)
	t.Logf("production-entry artifacts: %s (milestones, appwire-trace.jsonl, driver.log, screenshots and dumps)", fixture.artifact)
}

func assertBackgroundTwoTabLive(t *testing.T, milestones []skillGuardMilestone, producer backgroundEntryProducer) {
	t.Helper()
	type renderedRow struct {
		ID     string `json:"id"`
		TurnID string `json:"turnId"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field turnId.
		Text   string `json:"text"`
	}
	var live struct {
		Tabs []struct {
			BeforeRows []renderedRow `json:"beforeRows"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field beforeRows.
			AfterRows  []renderedRow `json:"afterRows"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field afterRows.
			Items      []struct {
				backgroundItemIdentity
				CallID string `json:"callId"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field callId.
			} `json:"items"`
			Output string `json:"output"`
		} `json:"tabs"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "two-tab-live", &live) {
		t.Fatal("two-tab live evidence missing")
	}
	if len(live.Tabs) != 2 {
		t.Fatalf("live browser evidence has %d tabs, want 2", len(live.Tabs))
	}
	for index, tab := range live.Tabs {
		for phase, rows := range [][]renderedRow{tab.BeforeRows, tab.AfterRows} {
			var textBuilder strings.Builder
			for _, row := range rows {
				if row.ID == "" || row.TurnID != producer.assistantTurnID {
					t.Errorf("tab %d phase %d rendered a different producer: %+v", index, phase, row)
				}
				textBuilder.WriteString(row.Text)
			}
			text := textBuilder.String()
			if strings.Count(text, backgroundBeforeEviction) != 1 || strings.Count(text, backgroundAfterEviction) != phase {
				t.Errorf("tab %d phase %d has missing or duplicate live output: %q", index, phase, text)
			}
			if phase == 1 && strings.Index(text, backgroundBeforeEviction) > strings.Index(text, backgroundAfterEviction) {
				t.Errorf("tab %d reversed the producer output", index)
			}
		}
		textMatches, toolMatches := 0, 0
		seen := make(map[string]bool)
		for _, item := range tab.Items {
			if item.ID == nil || *item.ID == "" || seen[*item.ID] {
				t.Errorf("tab %d has a missing or duplicate item identity: %+v", index, item)
			} else {
				seen[*item.ID] = true
			}
			if item.TurnID == nil || *item.TurnID != producer.assistantTurnID {
				t.Errorf("tab %d recorded a foreign turn: %+v", index, item)
			}
			if strings.Contains(item.Text, producer.assistantText) {
				textMatches++
			}
			if item.CallID == producer.toolCallID && item.Output == producer.toolResultOutput {
				toolMatches++
			}
		}
		if textMatches != 1 || toolMatches != 1 || tab.Output != producer.toolResultOutput {
			t.Errorf("tab %d disagrees with durable producer: text=%d tool=%d output=%q", index, textMatches, toolMatches, tab.Output)
		}
	}
	first, second := live.Tabs[0].Items, live.Tabs[1].Items
	if len(first) != len(second) || len(first) == 0 {
		t.Fatalf("two-tab item counts differ or are empty: %d/%d", len(first), len(second))
	}
	for index := range first {
		if !backgroundItemEqual(first[index].backgroundItemIdentity, second[index].backgroundItemIdentity) || first[index].CallID != second[index].CallID {
			t.Errorf("two-tab ordered item identity differs at %d: %+v / %+v", index, first[index], second[index])
		}
	}
}

// assertBackgroundEntryTwoTabFencing reads the two-real-tab Clear/delete fence
// milestones: one tab's delete fences its own deletedRefs, its real sibling
// observes suppression over the native BroadcastChannel without joining those
// deletedRefs, and the durable row is gone from the shared IndexedDB; a fresh
// sibling acquisition does not resurrect it; a sibling Clear suppresses a held
// lease without rebinding its epoch.
func assertBackgroundEntryTwoTabFencing(t *testing.T, milestones []skillGuardMilestone) {
	t.Helper()
	var delFence struct {
		ADeletedRefs       bool `json:"aDeletedRefs"`       //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field aDeletedRefs.
		BSuppressed        bool `json:"bSuppressed"`        //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field bSuppressed.
		BJoinedDeletedRefs bool `json:"bJoinedDeletedRefs"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field bJoinedDeletedRefs.
		DurableBefore      int  `json:"durableBefore"`      //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field durableBefore.
		DurableRemoved     bool `json:"durableRemoved"`     //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field durableRemoved.
	}
	if skillGuardMilestoneDetail(t, milestones, "two-tab-delete-fence", &delFence) {
		if !delFence.ADeletedRefs {
			t.Error("two-tab-delete-fence: the deleting tab did not fence deletedRefs")
		}
		if !delFence.BSuppressed {
			t.Error("two-tab-delete-fence: the sibling tab did not observe suppression over the real BroadcastChannel")
		}
		if delFence.BJoinedDeletedRefs {
			t.Error("two-tab-delete-fence: the sibling tab joined the deleting tab's deletedRefs")
		}
		if delFence.DurableBefore == 0 || !delFence.DurableRemoved {
			t.Errorf("two-tab-delete-fence: the shared IndexedDB record was not removed: %+v", delFence)
		}
	}
	var delRefusal struct {
		RoutePresent  bool `json:"routePresent"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field routePresent.
		RowsAfterOpen int  `json:"rowsAfterOpen"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field rowsAfterOpen.
	}
	if skillGuardMilestoneDetail(t, milestones, "two-tab-delete-refusal", &delRefusal) {
		if delRefusal.RoutePresent {
			t.Error("two-tab-delete-refusal: the sibling's fresh acquisition opened an entry route for a deleted ref")
		}
		if delRefusal.RowsAfterOpen != 0 {
			t.Errorf("two-tab-delete-refusal: a fresh sibling acquisition resurrected %d durable rows", delRefusal.RowsAfterOpen)
		}
	}
	var clearFence struct {
		EpochBefore *int `json:"epochBefore"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field epochBefore.
		EpochAfter  *int `json:"epochAfter"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field epochAfter.
		Suppressed  bool `json:"suppressed"`
	}
	if skillGuardMilestoneDetail(t, milestones, "two-tab-clear-fence", &clearFence) {
		if !clearFence.Suppressed {
			t.Error("two-tab-clear-fence: the holding tab did not observe the sibling clear over the real BroadcastChannel")
		}
		if clearFence.EpochBefore == nil || clearFence.EpochAfter == nil || *clearFence.EpochBefore != *clearFence.EpochAfter {
			t.Errorf("two-tab-clear-fence: the sibling clear rebound the held lease epoch: %+v", clearFence)
		}
	}
}

// assertBackgroundEntryPagingAndReconnect reads the browser journey's paging,
// reopen-without-read, further-paging, real-socket-gap/reconnect and
// paged-back-to-sentinel milestones, and RE-DERIVES each transport claim from
// the raw appwire-trace.jsonl at the milestone's own byte window, the same way
// assertBackgroundEntrySlice re-derives entry-opened. The gap turn is
// correlated to the daemon's own durable target-producer-transcript.jsonl, not
// to the driver's constant, and the reconnect read is correlated to the gap
// turn inside the raw trace.
func assertBackgroundEntryPagingAndReconnect(t *testing.T, fixture *skillGuardFixture, ref string, milestones []skillGuardMilestone) {
	t.Helper()
	assertBackgroundTransportInterval(t, fixture, ref)
	trace, err := os.ReadFile(filepath.Join(fixture.artifact, "appwire-trace.jsonl"))
	if err != nil {
		t.Fatalf("read real AppWire trace for paging/reconnect: %v", err)
	}
	var paging struct {
		TraceBefore        int64  `json:"traceBefore"`        //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceBefore.
		TraceAfter         int64  `json:"traceAfter"`         //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceAfter.
		InitialOlderCursor string `json:"initialOlderCursor"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field initialOlderCursor.
		Pages              int    `json:"pages"`
		RemainingCursor    string `json:"remainingCursor"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field remainingCursor.
		PagesComplete      bool   `json:"pagesComplete"`   //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field pagesComplete.
		LaterSentinel      string `json:"laterSentinel"`   //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field laterSentinel.
		LaterRendered      bool   `json:"laterRendered"`   //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field laterRendered.
	}
	if skillGuardMilestoneDetail(t, milestones, "history-paging", &paging) {
		window := traceWindowBytes(t, trace, paging.TraceBefore, paging.TraceAfter)
		pages, err := countTargetPageRequestsInWindow(string(window), ref)
		if err != nil {
			t.Fatalf("count paging page frames: %v", err)
		}
		if pages < 2 {
			t.Errorf("history-paging: the real trace shows %d target older page frame(s), want at least two", pages)
		}
		if paging.InitialOlderCursor == "" {
			t.Error("history-paging: the initial read was not a truncated window with an older cursor")
		}
		if !paging.LaterRendered {
			t.Errorf("history-paging: the later-page sentinel %q did not render", paging.LaterSentinel)
		}
		if paging.RemainingCursor == "" && !paging.PagesComplete {
			t.Error("history-paging: two older pages exhausted the cursor yet the history is not complete")
		}
	}
	var reopen struct {
		TraceBefore              int64    `json:"traceBefore"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceBefore.
		TraceAfter               int64    `json:"traceAfter"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceAfter.
		Cursor                   string   `json:"cursor"`
		RestoredCursor           string   `json:"restoredCursor"`           //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field restoredCursor.
		CursorRetentionExercised bool     `json:"cursorRetentionExercised"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field cursorRetentionExercised.
		Reads                    int      `json:"reads"`
		Pages                    int      `json:"pages"`
		PageRequestCursors       []string `json:"pageRequestCursors"`   //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field pageRequestCursors.
		OlderDemand              bool     `json:"olderDemand"`          //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field olderDemand.
		RetainedItemsPresent     bool     `json:"retainedItemsPresent"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field retainedItemsPresent.
		RestoredHasSentinel      bool     `json:"restoredHasSentinel"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field restoredHasSentinel.
	}
	if skillGuardMilestoneDetail(t, milestones, "reopen-without-read", &reopen) {
		window := traceWindowBytes(t, trace, reopen.TraceBefore, reopen.TraceAfter)
		reads, err := countTargetThreadReads(string(window), ref)
		if err != nil {
			t.Fatalf("count reopen thread/read frames: %v", err)
		}
		pages, err := countTargetPageRequestsInWindow(string(window), ref)
		if err != nil {
			t.Fatalf("count reopen page frames: %v", err)
		}
		cursors, err := targetPageRequestCursorsInWindow(window, ref)
		if err != nil {
			t.Fatalf("decode reopen page request cursors: %v", err)
		}
		if reads != 0 {
			t.Errorf("reopen-without-read: the real trace shows %d target thread/read(s); the local restore needed the wire", reads)
		}
		if len(cursors) != pages {
			t.Errorf("reopen-without-read: real page frames %d differ from decoded request cursors %d", pages, len(cursors))
		}
		// A reopened pane may issue one older page for genuinely uncached
		// content; that request must carry the retained cursor (content below
		// the loaded window), never re-fetch the window the durable rows held.
		for _, cursor := range cursors {
			if cursor != reopen.Cursor {
				t.Errorf("reopen-without-read: real page request cursor %q is not the retained cursor %q (re-fetched held content)", cursor, reopen.Cursor)
			}
		}
		if !reopen.OlderDemand {
			t.Error("reopen-without-read: the milestone reports a page request that re-fetched held content")
		}
		if !reopen.RetainedItemsPresent || !reopen.RestoredHasSentinel {
			t.Errorf("reopen-without-read: restored model lost the retained window or the later-page sentinel: %+v", reopen)
		}
		// The cursor clause is only meaningful when a cursor existed. Assert it
		// then; otherwise state plainly that it was not exercised.
		if reopen.CursorRetentionExercised {
			if reopen.Cursor != reopen.RestoredCursor {
				t.Errorf("reopen-without-read: cursor changed on local restore: %q -> %q", reopen.Cursor, reopen.RestoredCursor)
			}
		} else {
			t.Logf("reopen-without-read: cursor retention not exercised (the loaded window had no remaining cursor); this proves the local restore issued no thread/read and re-fetched no held content")
		}
	}
	var further struct {
		TraceBefore     int64 `json:"traceBefore"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceBefore.
		TraceAfter      int64 `json:"traceAfter"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceAfter.
		Pages           int   `json:"pages"`
		AlreadyComplete bool  `json:"alreadyComplete"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field alreadyComplete.
		NoDuplicates    bool  `json:"noDuplicates"`    //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field noDuplicates.
		HistoryOrdered  bool  `json:"historyOrdered"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field historyOrdered.
		HistoryCount    int   `json:"historyCount"`    //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field historyCount.
		ExpectedCount   int   `json:"expectedCount"`   //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field expectedCount.
	}
	if skillGuardMilestoneDetail(t, milestones, "further-paging", &further) {
		window := traceWindowBytes(t, trace, further.TraceBefore, further.TraceAfter)
		pages, err := countTargetPageRequestsInWindow(string(window), ref)
		if err != nil {
			t.Fatalf("count further-paging page frames: %v", err)
		}
		if pages < 1 {
			t.Error("further-paging: the real trace shows no target older page frame; the behaviour did not paginate")
		}
		if further.AlreadyComplete {
			t.Error("further-paging: the history was already complete before the further-paging phase; the geometry left nothing to page")
		}
		if !further.NoDuplicates {
			t.Error("further-paging: page load introduced duplicate items")
		}
		if !further.HistoryOrdered || further.HistoryCount != further.ExpectedCount {
			t.Errorf("further-paging: history is not the produced sequence in order: %+v", further)
		}
	}
	var broken struct {
		Disconnected  bool `json:"disconnected"`
		ClosedSockets int  `json:"closedSockets"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field closedSockets.
	}
	if skillGuardMilestoneDetail(t, milestones, "ws-broken", &broken) {
		if !broken.Disconnected {
			t.Errorf("ws-broken: the app never observed the real socket gap (closedSockets=%d)", broken.ClosedSockets)
		}
	}
	var gap struct {
		Sentinel string `json:"sentinel"`
		TurnID   string `json:"turnId"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field turnId.
	}
	if skillGuardMilestoneDetail(t, milestones, "gap-turn-completed", &gap) {
		producerData, err := os.ReadFile(filepath.Join(fixture.artifact, "target-producer-transcript.jsonl"))
		if err != nil {
			t.Fatalf("read durable producer transcript for the gap turn: %v", err)
		}
		durableID, err := backgroundGapTurnID(string(producerData))
		if err != nil {
			t.Errorf("gap-turn-completed: %v", err)
		}
		// The driver's turn id must be the daemon's own durable gap-turn id, not
		// a value compared to the driver's constant.
		if gap.TurnID == "" || durableID != gap.TurnID {
			t.Errorf("gap-turn-completed: driver turn id %q does not match the durable transcript's gap turn %q", gap.TurnID, durableID)
		}
	}
	var reconnect struct {
		TraceBefore int64  `json:"traceBefore"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceBefore.
		TraceAfter  int64  `json:"traceAfter"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field traceAfter.
		NewReads    int    `json:"newReads"`    //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field newReads.
		CursorAfter string `json:"cursorAfter"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field cursorAfter.
		HasGap      bool   `json:"hasGap"`      //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field hasGap.
	}
	if skillGuardMilestoneDetail(t, milestones, "reconnected-latest-window", &reconnect) {
		window := traceWindowBytes(t, trace, reconnect.TraceBefore, reconnect.TraceAfter)
		reads, err := countTargetThreadReads(string(window), ref)
		if err != nil {
			t.Fatalf("count reconnect thread/read frames: %v", err)
		}
		if reads < 1 {
			t.Error("reconnected-latest-window: the real trace shows no target thread/read during the gap/reconnect window")
		}
		// The reconnect's authoritative read must carry the gap turn: correlate
		// it in the raw trace, independently of the driver's hasGap flag.
		gapInRead, err := readWindowContainsText(window, backgroundGapTurn)
		if err != nil {
			t.Fatalf("decode reconnect read window: %v", err)
		}
		if !gapInRead {
			t.Error("reconnected-latest-window: no real target read in the reconnect window carries the gap turn")
		}
		if reconnect.NewReads < 1 {
			t.Error("reconnected-latest-window: reconnect produced no new authoritative read")
		}
		if !reconnect.HasGap {
			t.Error("reconnected-latest-window: the reconnected window does not contain the gap turn")
		}
	}
	var backed struct {
		Pages           int  `json:"pages"`
		NoDuplicates    bool `json:"noDuplicates"`    //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field noDuplicates.
		SentinelPresent bool `json:"sentinelPresent"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field sentinelPresent.
		HistoryOrdered  bool `json:"historyOrdered"`  //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field historyOrdered.
	}
	if skillGuardMilestoneDetail(t, milestones, "paged-back-sentinel", &backed) {
		if backed.Pages < 1 {
			t.Error("paged-back-sentinel: paging back after reconnect issued no older page")
		}
		if !backed.NoDuplicates {
			t.Error("paged-back-sentinel: paging back introduced duplicate items")
		}
		if !backed.SentinelPresent || !backed.HistoryOrdered {
			t.Errorf("paged-back-sentinel: the range did not restore to the sentinel in order: %+v", backed)
		}
	}
	for _, name := range []string{"responsive-reading", "narrow-viewport"} {
		var opening struct {
			TraceBefore int64 `json:"traceBefore"` //nolint:tagliatelle // Matches the JavaScript browser evidence contract.
			TraceAfter  int64 `json:"traceAfter"`  //nolint:tagliatelle // Matches the JavaScript browser evidence contract.
		}
		if skillGuardMilestoneDetail(t, milestones, name, &opening) {
			window := traceWindowBytes(t, trace, opening.TraceBefore, opening.TraceAfter)
			reads, err := countTargetThreadReads(string(window), ref)
			if err != nil {
				t.Fatalf("decode %s read frames: %v", name, err)
			}
			pages, err := countTargetPageRequestsInWindow(string(window), ref)
			if err != nil {
				t.Fatalf("decode %s page frames: %v", name, err)
			}
			if reads != 0 || pages != 0 {
				t.Errorf("%s: healthy local opening issued %d reads and %d older pages", name, reads, pages)
			}
		}
	}
}

// backgroundAssertionSink is the part of *testing.T the entry owner assertions
// need. Parameterizing on it lets a regression test drive an owning assertion
// directly and record its verdicts, so the test fails if the owner stops
// comparing bodies instead of only exercising a helper the owner happens to call.
type backgroundAssertionSink interface {
	Helper()
	Fatalf(format string, args ...any)
	Errorf(format string, args ...any)
	Error(args ...any)
}

// backgroundMilestoneDetail shares milestone lookup and malformed-detail
// handling with the skill guard, reporting through the sink.
func backgroundMilestoneDetail(t backgroundAssertionSink, milestones []skillGuardMilestone, name string, into any) bool {
	t.Helper()
	return skillGuardMilestoneDetail(t, milestones, name, into)
}

// assertBackgroundEntryNarrowViewport reads the 420px repeat-activation
// milestone: the transcript was re-activated through the mobile shell, and the
// driver recorded the real model's item payloads before and after that
// re-activation. The Go owner cannot read the DOM, but it does not trust the
// driver's boolean: it re-derives preservation from the two recorded payloads,
// requiring every before item to survive in order with the same text, output,
// arguments and status, and independently confirms the produced gap content
// survived.
//
// It takes a backgroundAssertionSink rather than *testing.T so an owner-level
// regression test can drive THIS assertion directly and capture its verdicts.
func assertBackgroundEntryNarrowViewport(t backgroundAssertionSink, milestones []skillGuardMilestone) {
	t.Helper()
	var narrow struct {
		Width            int                      `json:"width"`
		BeforeItems      int                      `json:"beforeItems"`      //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field beforeItems.
		AfterItems       int                      `json:"afterItems"`       //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field afterItems.
		ContentPreserved bool                     `json:"contentPreserved"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field contentPreserved.
		SentinelAfter    bool                     `json:"sentinelAfter"`    //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field sentinelAfter.
		BeforePayload    []backgroundItemIdentity `json:"beforePayload"`    //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field beforePayload.
		AfterPayload     []backgroundItemIdentity `json:"afterPayload"`     //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field afterPayload.
	}
	if backgroundMilestoneDetail(t, milestones, "narrow-viewport", &narrow) {
		if narrow.Width != 420 {
			t.Errorf("narrow-viewport: activated at %dpx, want 420px", narrow.Width)
		}
		if narrow.BeforeItems == 0 || narrow.AfterItems == 0 || narrow.BeforeItems != narrow.AfterItems {
			t.Errorf("narrow-viewport: repeat activation changed the loaded item set (%d -> %d)", narrow.BeforeItems, narrow.AfterItems)
		}
		if !narrow.ContentPreserved {
			t.Error("narrow-viewport: repeat activation did not preserve the transcript content")
		}
		// Independently compare the recorded before/after bodies. The driver's
		// contentPreserved boolean is not trusted; a rewrite that kept IDs but
		// emptied a text or tool output must fail here.
		if len(narrow.BeforePayload) == 0 || len(narrow.AfterPayload) == 0 {
			t.Errorf("narrow-viewport: the milestone recorded no before/after bodies to compare (%d -> %d)", len(narrow.BeforePayload), len(narrow.AfterPayload))
		} else if preserved, why := backgroundPayloadPreserved(narrow.BeforePayload, narrow.AfterPayload); !preserved {
			t.Errorf("narrow-viewport: repeat activation did not preserve the transcript bodies: %s", why)
		}
		if !narrow.SentinelAfter {
			t.Error("narrow-viewport: the produced content was absent after the mobile-shell re-activation")
		}
		// Re-derive the sentinel from the recorded bodies, independently of the
		// driver's sentinelAfter boolean.
		if !backgroundPayloadContainsText(narrow.AfterPayload, backgroundGapTurn) {
			t.Errorf("narrow-viewport: no after item carries the produced gap content %q", backgroundGapTurn)
		}
	}
}

// Independent acceptance of raw DOM geometry and workspace record identities.
// The producer marker is fixed here, not taken from a driver's success flag.
type backgroundReading struct {
	Width         int     `json:"width"`
	ScrollTop     float64 `json:"scrollTop"`     //nolint:tagliatelle // Matches the JavaScript browser evidence contract.
	ScrollHeight  float64 `json:"scrollHeight"`  //nolint:tagliatelle // Matches the JavaScript browser evidence contract.
	ClientHeight  float64 `json:"clientHeight"`  //nolint:tagliatelle // Matches the JavaScript browser evidence contract.
	ViewportWidth float64 `json:"viewportWidth"` //nolint:tagliatelle // Matches the JavaScript browser evidence contract.
	Semantic      struct {
		ID     string  `json:"id"`
		Offset float64 `json:"offset"`
		Height float64 `json:"height"`
		Text   string  `json:"text"`
	} `json:"semantic"`
	Anchor struct {
		ItemID string  `json:"itemId"` //nolint:tagliatelle // Matches the JavaScript browser evidence contract.
		Offset float64 `json:"offset"`
	} `json:"anchor"`
	Panes []struct {
		ID       string `json:"id"`
		Identity int    `json:"identity"`
	} `json:"panes"`
	VisibleRows []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"visibleRows"` //nolint:tagliatelle // Matches the JavaScript browser evidence contract.
}

func assertBackgroundReadingPlacement(t backgroundAssertionSink, milestones []skillGuardMilestone) {
	t.Helper()
	var responsive struct {
		Before        backgroundReading        `json:"before"`
		After         backgroundReading        `json:"after"`
		BeforePayload []backgroundItemIdentity `json:"beforePayload"` //nolint:tagliatelle // Matches the JavaScript browser evidence contract.
		AfterPayload  []backgroundItemIdentity `json:"afterPayload"`  //nolint:tagliatelle // Matches the JavaScript browser evidence contract.
	}
	if backgroundMilestoneDetail(t, milestones, "responsive-reading", &responsive) {
		if ok, why := backgroundPayloadPreserved(responsive.BeforePayload, responsive.AfterPayload); !ok {
			t.Errorf("responsive-reading: loaded payload changed: %s", why)
		}
		before, after := responsive.Before, responsive.After
		if before.Width != 1440 || after.Width != 420 || len(before.Panes) != 1 || len(after.Panes) != 1 {
			t.Error("responsive-reading: missing desktop/mobile pane geometry")
		} else if before.Panes[0] != after.Panes[0] || before.Panes[0].Identity == 0 {
			t.Error("responsive-reading: logical pane record was replaced")
		}
		if before.Anchor.ItemID == "" || before.Anchor.ItemID != after.Anchor.ItemID || before.Semantic.ID == "" || before.Semantic.ID != after.Semantic.ID {
			t.Error("responsive-reading: older semantic entry changed")
		}
		// The fixture reads inside a short entry. Its bottom's distance from the
		// viewport top is the visible-tail reading point, measured independently
		// in each DOM rather than supplied by the production placement helper.
		if before.ViewportWidth <= 0 || after.ViewportWidth <= 0 || before.ViewportWidth == after.ViewportWidth ||
			before.Semantic.Height <= 0 || before.Semantic.Height >= before.ClientHeight || before.Semantic.Offset >= 0 ||
			after.Semantic.Height <= 0 || after.Semantic.Offset > 0 || after.Semantic.Height+after.Semantic.Offset <= 0 ||
			math.Abs((before.Semantic.Height+before.Semantic.Offset)-(after.Semantic.Height+after.Semantic.Offset)) > 2 {
			t.Error("responsive-reading: semantic visible-tail reading point changed")
		}
		for _, reading := range []backgroundReading{before, after} {
			if reading.ClientHeight <= 0 || reading.ScrollHeight-reading.ClientHeight-reading.ScrollTop <= reading.ClientHeight {
				t.Error("responsive-reading: reader was not away from latest")
			}
			visible := false
			for _, row := range reading.VisibleRows {
				visible = visible || (row.ID == reading.Anchor.ItemID && strings.Contains(row.Text, "BACKGROUND_HISTORY_27"))
			}
			if !visible {
				t.Error("responsive-reading: older producer payload missing from visible anchored row")
			}
			if !strings.Contains(reading.Semantic.Text, "BACKGROUND_HISTORY_27") {
				t.Error("responsive-reading: older producer payload missing from semantic entry")
			}
		}
	} else {
		t.Error("responsive-reading: missing placement evidence")
	}
	var reopened struct {
		Before      backgroundReading `json:"before"`
		After       backgroundReading `json:"after"`
		ClosedPanes []json.RawMessage `json:"closedPanes"` //nolint:tagliatelle // Matches the JavaScript browser evidence contract.
	}
	if backgroundMilestoneDetail(t, milestones, "new-pane-latest", &reopened) {
		before, after := reopened.Before, reopened.After
		if before.ClientHeight <= 0 || before.ScrollHeight-before.ClientHeight-before.ScrollTop <= before.ClientHeight {
			t.Error("new-pane-latest: pre-close reader was not away from latest")
		}
		olderVisible := false
		for _, row := range before.VisibleRows {
			olderVisible = olderVisible || (row.ID == before.Anchor.ItemID && strings.Contains(row.Text, "BACKGROUND_HISTORY_27"))
		}
		if !olderVisible {
			t.Error("new-pane-latest: pre-close older payload missing from visible anchored row")
		}
		if reopened.ClosedPanes == nil || len(reopened.ClosedPanes) != 0 || len(before.Panes) != 1 || len(after.Panes) != 1 {
			t.Error("new-pane-latest: missing genuine close/new-pane evidence")
		} else if before.Panes[0].Identity == after.Panes[0].Identity || after.Panes[0].Identity == 0 {
			t.Error("new-pane-latest: reused old logical pane record")
		}
		if after.ClientHeight <= 0 || math.Abs(after.ScrollHeight-after.ClientHeight-after.ScrollTop) > 2 {
			t.Error("new-pane-latest: new pane is not at the true bottom")
		}
		visible := false
		for _, row := range after.VisibleRows {
			visible = visible || strings.Contains(row.Text, backgroundGapTurn)
		}
		if !visible {
			t.Error("new-pane-latest: latest producer payload missing from visible rows")
		}
	} else {
		t.Error("new-pane-latest: missing placement evidence")
	}
}

func TestBackgroundReadingPlacementOwnerRejectsDamage(t *testing.T) {
	for _, damage := range []string{"none", "row", "semantic entry", "offset", "fixed offset", "semantic payload", "missing geometry", "owner", "visible payload", "body", "source key", "version", "position", "close omitted", "same owner reopen", "old placement reopen", "latest missing", "missing milestone"} {
		t.Run(damage, func(t *testing.T) {
			var before backgroundReading
			if err := json.Unmarshal([]byte(`{"width":1440,"scrollTop":3000,"scrollHeight":10000,"clientHeight":700,"anchor":{"itemId":"turn-27","offset":16},"panes":[{"id":"pane-1","identity":1}],"visibleRows":[{"id":"turn-27","text":"BACKGROUND_HISTORY_27"}]}`), &before); err != nil {
				t.Fatal(err)
			}
			before.ViewportWidth = 1097
			before.Semantic.ID = "user-27"
			before.Semantic.Offset = -16
			before.Semantic.Height = 109.125
			before.Semantic.Text = "BACKGROUND_HISTORY_27"
			clone := func(value backgroundReading) backgroundReading {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				var result backgroundReading
				if err := json.Unmarshal(data, &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			after := clone(before)
			after.Width = 420
			after.ViewportWidth = 388
			after.Semantic.Offset = -32
			after.Semantic.Height = 125.125
			latest := clone(after)
			latest.Panes[0].Identity = 2
			latest.ScrollTop = 9300
			latest.VisibleRows[0].Text = backgroundGapTurn
			var payload []backgroundItemIdentity
			if err := json.Unmarshal([]byte(`[{"text":"BACKGROUND_HISTORY_27","transcriptKey":"user-27","version":1,"position":{"entry":27,"item":0}}]`), &payload); err != nil {
				t.Fatal(err)
			}
			responsive := map[string]any{"before": before, "after": after, "beforePayload": payload, "afterPayload": payload}
			reopen := map[string]any{"before": clone(after), "after": latest, "closedPanes": []any{}}
			switch damage {
			case "row":
				after.Anchor.ItemID = "wrong"
			case "offset":
				after.Semantic.Offset += 20
			case "semantic entry":
				after.Semantic.ID = "agent-27"
			case "fixed offset":
				after.Semantic.Offset = -16
			case "semantic payload":
				after.Semantic.Text = ""
			case "missing geometry":
				after.Semantic.Height = 0
			case "owner":
				after.Panes[0].Identity = 2
			case "visible payload":
				after.VisibleRows[0].Text = ""
			case "body":
				responsive["afterPayload"] = []backgroundItemIdentity{{Text: ""}}
			case "source key", "version", "position":
				damaged := payload[0]
				if damage == "source key" {
					damaged.TranscriptKey = nil
				}
				if damage == "version" {
					damaged.Version = nil
				}
				if damage == "position" {
					damaged.Position = nil
				}
				responsive["afterPayload"] = []backgroundItemIdentity{damaged}
			case "close omitted":
				delete(reopen, "closedPanes")
			case "same owner reopen":
				latest.Panes[0].Identity = 1
			case "old placement reopen":
				latest.ScrollTop = 3000
			case "latest missing":
				latest.VisibleRows[0].Text = ""
			}
			responsive["after"], reopen["after"] = after, latest
			var milestones []skillGuardMilestone
			for name, detail := range map[string]any{"responsive-reading": responsive, "new-pane-latest": reopen} {
				data, err := json.Marshal(detail)
				if err != nil {
					t.Fatal(err)
				}
				milestones = append(milestones, skillGuardMilestone{Milestone: name, Detail: data})
			}
			if damage == "missing milestone" {
				milestones = nil
			}
			recorder := &backgroundFailureRecorder{}
			assertBackgroundReadingPlacement(recorder, milestones)
			if (len(recorder.failures) == 0) != (damage == "none") {
				t.Fatalf("owner acceptance for %s: %v", damage, recorder.failures)
			}
		})
	}
}

func TestTraceWindowBytesEmpty(t *testing.T) {
	for _, offset := range []int64{0, 2, 4} {
		t.Run(strconv.FormatInt(offset, 10), func(t *testing.T) {
			if got := traceWindowBytes(t, []byte("abc\n"), offset, offset); len(got) != 0 {
				t.Fatalf("empty window returned %q", got)
			}
		})
	}
}

// traceWindowBytes returns the trace slice [before, after) with any record that
// straddles a boundary completed (or trimmed), so a request at the window's end
// is not lost and a partial record is never parsed. It mirrors the byte
// arithmetic the entry-opened assertion relies on.
func traceWindowBytes(t *testing.T, trace []byte, before, after int64) []byte {
	t.Helper()
	if before < 0 || after < before || after > int64(len(trace)) {
		t.Fatalf("invalid trace window %d..%d (trace bytes %d)", before, after, len(trace))
	}
	if before == after {
		return trace[before:after]
	}
	end := after
	if trace[after-1] != '\n' {
		idx := bytes.IndexByte(trace[after:], '\n')
		if idx < 0 {
			t.Fatalf("trace window ends in an incomplete record at %d", after)
		}
		end = after + int64(idx) + 1
	}
	start := before
	if start > 0 && trace[start-1] != '\n' {
		idx := bytes.IndexByte(trace[start:end], '\n')
		if idx < 0 {
			t.Fatalf("trace window starts in an incomplete record at %d", before)
		}
		start = start + int64(idx) + 1
	}
	return trace[start:end]
}

// targetPageRequestCursorsInWindow decodes a raw trace window and returns the
// cursor of every TARGET thread/turns/list request frame, so the Go owner can
// see which content a reopened pane asked for.
func targetPageRequestCursorsInWindow(data []byte, ref string) ([]string, error) {
	frames, err := decodeTraceFrames(data)
	if err != nil {
		return nil, err
	}
	threadID := strings.TrimPrefix(ref, "local:")
	var cursors []string
	for _, f := range frames {
		if f.Method != "thread/turns/list" || len(f.Params) == 0 {
			continue
		}
		var params struct {
			Ref      string `json:"ref"`
			ThreadID string `json:"threadId"` //nolint:tagliatelle // Matches the AppWire JSON field threadId.
			Cursor   string `json:"cursor"`
		}
		if err := json.Unmarshal(f.Params, &params); err != nil {
			return nil, fmt.Errorf("malformed thread/turns/list params: %w", err)
		}
		if params.Ref == ref || params.ThreadID == threadID {
			cursors = append(cursors, params.Cursor)
		}
	}
	return cursors, nil
}

// readWindowContainsText reports whether any thread/read RESULT in the raw trace
// window carries an item whose text contains value. It decodes the frames; it
// never scans escaped raw text.
func readWindowContainsText(data []byte, value string) (bool, error) {
	frames, err := decodeTraceFrames(data)
	if err != nil {
		return false, err
	}
	for _, f := range frames {
		if len(f.Result) == 0 {
			continue
		}
		var result struct {
			Thread struct {
				Turns []struct {
					Items []struct {
						Text string `json:"text"`
					} `json:"items"`
				} `json:"turns"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(f.Result, &result); err != nil {
			continue
		}
		for _, turn := range result.Thread.Turns {
			for _, item := range turn.Items {
				if strings.Contains(item.Text, value) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// backgroundItemIdentity mirrors one entry-driver item identity (run.mjs's
// itemIdentity): turnId/id/type anchor the item, and text/output/arguments/status
// are its body. The driver records these for the narrow-viewport before and after
// models so the Go owner can compare the bodies itself.
type backgroundItemIdentity struct {
	TurnID        *string `json:"turnId"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field turnId.
	ID            *string `json:"id"`
	Type          *string `json:"type"`
	Text          string  `json:"text"`
	Output        string  `json:"output"`
	Arguments     *string `json:"arguments"`
	Status        *string `json:"status"`
	TranscriptKey *string `json:"transcriptKey"` //nolint:tagliatelle // Matches the recorded JavaScript browser evidence field transcriptKey.
	Version       *int    `json:"version"`
	Position      *struct {
		Entry int `json:"entry"`
		Item  int `json:"item"`
		Sub   int `json:"sub"`
	} `json:"position"`
}

func backgroundNullableEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func backgroundItemEqual(a, b backgroundItemIdentity) bool {
	return backgroundNullableEqual(a.TurnID, b.TurnID) &&
		backgroundNullableEqual(a.ID, b.ID) &&
		backgroundNullableEqual(a.Type, b.Type) &&
		a.Text == b.Text &&
		a.Output == b.Output &&
		backgroundNullableEqual(a.Arguments, b.Arguments) &&
		backgroundNullableEqual(a.Status, b.Status) &&
		backgroundNullableEqual(a.TranscriptKey, b.TranscriptKey) &&
		reflect.DeepEqual(a.Version, b.Version) && reflect.DeepEqual(a.Position, b.Position)
}

// backgroundPayloadPreserved RE-DERIVES the driver's narrow-viewport
// preservation claim from the two recorded item payloads, independently of the
// driver's contentPreserved boolean. Every before item must appear in the after
// model, in order, with the same body (text, tool output, arguments, status). The
// after model may carry OLDER content the re-activation fetched; it must not drop
// or rewrite any item that was present before.
func backgroundPayloadPreserved(before, after []backgroundItemIdentity) (bool, string) {
	if len(before) == 0 {
		return false, "no before items were recorded"
	}
	index := 0
	for _, item := range after {
		if index >= len(before) {
			break
		}
		if backgroundItemEqual(before[index], item) {
			index++
		}
	}
	if index != len(before) {
		return false, fmt.Sprintf("only %d of %d before items survived in order with their body", index, len(before))
	}
	return true, ""
}

// backgroundPayloadContainsText reports whether any recorded item's text or tool
// output carries want, so the Go owner can confirm produced content survived
// without trusting the driver's sentinelAfter boolean.
func backgroundPayloadContainsText(payload []backgroundItemIdentity, want string) bool {
	for _, item := range payload {
		if strings.Contains(item.Text, want) || strings.Contains(item.Output, want) {
			return true
		}
	}
	return false
}

// backgroundGapTurnID returns the turn id of the daemon's own durable turn that
// carries backgroundGapTurn, read from the retained producer transcript.
func backgroundGapTurnID(transcript string) (string, error) {
	type content struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	type record struct {
		Kind   string `json:"kind"`
		TurnID string `json:"turn_id"`
		Turn   struct {
			TurnID  string `json:"turn_id"`
			Message struct {
				Content []content `json:"content"`
			} `json:"message"`
		} `json:"turn"`
	}
	for line := range strings.SplitSeq(transcript, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row record
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return "", fmt.Errorf("malformed durable transcript line %q: %w", line, err)
		}
		if row.Kind != "entry" {
			continue
		}
		for _, c := range row.Turn.Message.Content {
			if !strings.Contains(c.Text, backgroundGapTurn) {
				continue
			}
			id := row.TurnID
			if id == "" {
				id = row.Turn.TurnID
			}
			if id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("the durable transcript carries no turn with %s", backgroundGapTurn)
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

func TestBackgroundPayloadPreservedRejectsBodyLoss(t *testing.T) {
	str := func(s string) *string { return &s }
	// The sentinel is item c; the text-loss and output-loss cases damage the
	// OTHER items so the body comparison, not the sentinel, is what fails.
	before := []backgroundItemIdentity{
		{ID: str("a"), Type: str("assistant"), Text: "BACKGROUND_OTHER_TEXT", Status: str("done")},
		{ID: str("b"), Type: str("tool"), Output: "BACKGROUND_TOOL_OUTPUT", Arguments: str(`{"command":"ls"}`), Status: str("done")},
		{ID: str("c"), Type: str("assistant"), Text: "BACKGROUND_GAP_TURN", Status: str("done")},
	}
	// Control: identical payloads are preserved.
	if ok, why := backgroundPayloadPreserved(before, before); !ok {
		t.Fatalf("identical payloads were not preserved: %s", why)
	}
	// Same IDs and order, sentinel intact, but another item's text emptied.
	lostText := []backgroundItemIdentity{before[0], before[1], before[2]}
	lostText[0].Text = ""
	if ok, _ := backgroundPayloadPreserved(before, lostText); ok {
		t.Error("an emptied text with unchanged IDs was accepted as preserved")
	}
	// Same IDs and order, but an item's tool output emptied.
	lostOutput := []backgroundItemIdentity{before[0], before[1], before[2]}
	lostOutput[1].Output = ""
	if ok, _ := backgroundPayloadPreserved(before, lostOutput); ok {
		t.Error("an emptied tool output with unchanged IDs was accepted as preserved")
	}
	// Same IDs and order, but a changed tool argument.
	changedArguments := []backgroundItemIdentity{before[0], before[1], before[2]}
	changedArguments[1].Arguments = str(`{"command":"pwd"}`)
	if ok, _ := backgroundPayloadPreserved(before, changedArguments); ok {
		t.Error("changed arguments with unchanged IDs were accepted as preserved")
	}
	// Same IDs and order, but a changed status.
	changedStatus := []backgroundItemIdentity{before[0], before[1], before[2]}
	changedStatus[2].Status = str("error")
	if ok, _ := backgroundPayloadPreserved(before, changedStatus); ok {
		t.Error("a changed status with unchanged IDs was accepted as preserved")
	}
	// The same items in a different order are not preserved.
	reordered := []backgroundItemIdentity{before[1], before[0], before[2]}
	if ok, _ := backgroundPayloadPreserved(before, reordered); ok {
		t.Error("reordered items were accepted as preserved")
	}
	// Older content fetched by the re-activation is allowed...
	withOlder := append([]backgroundItemIdentity{{ID: str("older"), Type: str("assistant"), Text: "old"}}, before...)
	if ok, why := backgroundPayloadPreserved(before, withOlder); !ok {
		t.Errorf("older prepended content wrongly failed preservation: %s", why)
	}
	// ...but a dropped before item is not.
	if ok, _ := backgroundPayloadPreserved(before, before[1:]); ok {
		t.Error("a dropped before item was accepted as preserved")
	}
}

// backgroundFailureRecorder captures an owner assertion's Error/Errorf/Fatalf
// calls so a regression test can require that it fails on damaged input without
// failing the surrounding test.
type backgroundFailureRecorder struct {
	failures []string
}

func (r *backgroundFailureRecorder) Helper() {}

func (r *backgroundFailureRecorder) Fatalf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *backgroundFailureRecorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *backgroundFailureRecorder) Error(args ...any) {
	r.failures = append(r.failures, fmt.Sprint(args...))
}

// TestBackgroundNarrowViewportOwnerRejectsDamagedBody drives the OWNING
// assertion (assertBackgroundEntryNarrowViewport), not a helper it calls, with a
// milestone whose booleans and counts all pass but whose before/after bodies
// disagree: another item's text is emptied while the sentinel stays intact. The
// owning assertion must fail. A version that trusts the driver's
// contentPreserved boolean and counts instead of comparing bodies would accept
// it, so this test fails when the owner stops comparing bodies.
func TestBackgroundNarrowViewportOwnerRejectsDamagedBody(t *testing.T) {
	str := func(s string) *string { return &s }
	intact := []backgroundItemIdentity{
		{ID: str("a"), Type: str("assistant"), Text: "BACKGROUND_OTHER_TEXT", Status: str("done")},
		{ID: str("b"), Type: str("tool"), Output: "BACKGROUND_TOOL_OUTPUT", Arguments: str(`{"command":"ls"}`), Status: str("done")},
		{ID: str("c"), Type: str("assistant"), Text: backgroundGapTurn, Status: str("done")},
	}
	damaged := []backgroundItemIdentity{intact[0], intact[1], intact[2]}
	damaged[0].Text = ""

	milestone := func(before, after []backgroundItemIdentity) skillGuardMilestone {
		detail, err := json.Marshal(map[string]any{
			"width":            420,
			"beforeItems":      len(before),
			"afterItems":       len(after),
			"contentPreserved": true,
			"sentinelAfter":    true,
			"beforePayload":    before,
			"afterPayload":     after,
		})
		if err != nil {
			t.Fatalf("marshal narrow-viewport milestone: %v", err)
		}
		return skillGuardMilestone{Milestone: "narrow-viewport", Detail: detail}
	}

	// Control: the intact milestone passes the owner assertion.
	control := &backgroundFailureRecorder{}
	assertBackgroundEntryNarrowViewport(control, []skillGuardMilestone{milestone(intact, intact)})
	if len(control.failures) != 0 {
		t.Fatalf("the owner assertion rejected an intact milestone: %v", control.failures)
	}

	// Every boolean and count is green; only the recorded bodies disagree. The
	// owning assertion must still fail.
	recorder := &backgroundFailureRecorder{}
	assertBackgroundEntryNarrowViewport(recorder, []skillGuardMilestone{milestone(intact, damaged)})
	if len(recorder.failures) == 0 {
		t.Fatal("assertBackgroundEntryNarrowViewport accepted a milestone whose recorded bodies disagree")
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
	for line := range strings.SplitSeq(string(data), "\n") {
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
			ThreadID  string `json:"threadId"` //nolint:tagliatelle // Matches the AppWire JSON field threadId.
			Subscribe bool   `json:"subscribe"`
			Replace   bool   `json:"replaceSubscription"` //nolint:tagliatelle // Matches the AppWire JSON field replaceSubscription.
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
				OlderCursor *string `json:"olderCursor"` //nolint:tagliatelle // Matches the AppWire JSON field olderCursor.
			}
			if err := json.Unmarshal(f.Result, &result); err != nil {
				return ev, fmt.Errorf("malformed thread/read result: %w", err)
			}
			if result.OlderCursor != nil && ev.InitialOlderCursor == "" {
				ev.InitialOlderCursor = *result.OlderCursor
			}
		case "thread/turns/list":
			var result struct {
				NextCursor *string `json:"nextCursor"` //nolint:tagliatelle // Matches the AppWire JSON field nextCursor.
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
			ThreadID string `json:"threadId"` //nolint:tagliatelle // Matches the AppWire JSON field threadId.
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
	for line := range strings.SplitSeq(window, "\n") {
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
	for line := range strings.SplitSeq(string(data), "\n") {
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
