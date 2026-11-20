package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/testwait"
)

// seedStrandedShellRepairStore seeds the crash window the shell repair exists
// for in the stop target's store: a terminal shell with its watch still active
// and its notification still pending. The next reconcile plans a repair for it.
func seedStrandedShellRepairStore(t *testing.T, path string) {
	t.Helper()
	started := time.Unix(10, 0).UTC()
	ended := time.Unix(20, 0).UTC()
	appendDelegateShellEventsAt(t, path,
		jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			TS:             started,
			JobID:          "job-shell",
			Type:           jobstore.JobShell,
			OwnerSessionID: "child-dlg_target",
			StartedAt:      &started,
		},
		jobstore.Event{
			Kind:    jobstore.EventWatchRegistered,
			TS:      started,
			WatchID: "watch-shell",
			Watch: &jobstore.WatchEvent{
				Generation:       "watch-generation",
				OwnerSessionID:   "child-dlg_target",
				VisibleSessionID: "child-dlg_target",
				Target:           "job-shell",
				SendTo:           "receiver-session",
				ConfigHash:       "hash",
				Condition:        "terminal",
			},
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ended,
			JobID:       "job-shell",
			Status:      jobstore.StatusStopped,
			Reason:      "runtime_lost",
			EndedAt:     &ended,
			TerminalGen: "terminal-prefix",
		},
		jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          ended,
			JobID:       "job-shell",
			TerminalGen: "terminal-prefix",
		},
	)
}

// newSeededStrandedStop builds the stranded-stop fixture and seeds the shell
// repair its drain will plan.
func newSeededStrandedStop(t *testing.T) (*strandedStopFixture, string) {
	t.Helper()
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, inline: true, realRoot: true})
	if f.root == nil {
		t.Fatal("fixture produced no root: the drain publishes through it")
	}
	f.root.events = make(chan events.SessionEvent, 64)
	shellPath := filepath.Join(jobsDir(f.controller.stateDir, "child-dlg_target"), "jobs.jsonl")
	seedStrandedShellRepairStore(t, shellPath)
	return f, shellPath
}

// requireStrandedStopWarningAndRepairShareOneReconcile drives a second,
// identically seeded fixture to the reconcile pass that settles the lost-result
// warning, and asserts that SAME pass planned a shell repair. The ordering
// assertion in the test below proves something about the warning only if the
// repair is still pending when that warning is published; if the repair were
// planned on an earlier pass it would already have appended by then, and the
// test could stay green with the warning published before the repairs ran.
func requireStrandedStopWarningAndRepairShareOneReconcile(t *testing.T) {
	t.Helper()
	f, _ := newSeededStrandedStop(t)

	for pass := range 4 {
		requirements, current := f.controller.stopReconcileRequirements(f.controller.stopForResult(f.stop))
		if !current {
			t.Fatalf("pass %d: the fixture's stop reconcile requirements are not current", pass)
		}
		evidence, err := collectDelegateReconcileEvidence(f.controller.stateDir, requirements)
		if err != nil {
			t.Fatalf("pass %d: collectDelegateReconcileEvidence: %v", pass, err)
		}
		plans, err := f.controller.Reconcile(evidence)
		if err != nil {
			t.Fatalf("pass %d: Reconcile: %v", pass, err)
		}
		if len(plans.warnings) != 0 {
			// This is the pass the ordering test observes. Leave its plans
			// unexecuted: the assertion is about what this pass contains.
			if plans.retirementRelease != nil {
				plans.retirementRelease()
			}
			if len(plans.shellRepairs) == 0 {
				t.Fatalf("reconcile pass %d settled %d warning(s) but planned no shell repair: the "+
					"ordering assertion would run after the repair had already appended, so the "+
					"fixture would not pin #6939", pass, len(plans.warnings))
			}
			return
		}
		// Advance exactly as the ordinary drain does between passes: the shell
		// repairs and attention cleanup first, then the mutation plans with the
		// attention already consumed.
		for _, plan := range plans.shellRepairs {
			if err := executeDelegateShellRepair(plan, f.controller.now()); err != nil {
				t.Fatalf("pass %d: executeDelegateShellRepair: %v", pass, err)
			}
		}
		for _, plan := range plans.attention {
			if err := f.controller.executeDelegateAttentionCleanup(plan); err != nil {
				t.Fatalf("pass %d: executeDelegateAttentionCleanup: %v", pass, err)
			}
		}
		rootPlans := plans
		rootPlans.attention = nil
		if err := f.root.executeDelegateMutationPlans(rootPlans); err != nil {
			t.Fatalf("pass %d: executeDelegateMutationPlans: %v", pass, err)
		}
	}
	t.Fatal("four reconcile passes settled no lost-result warning: the stranded stop fixture no longer " +
		"produces the warning this test needs")
}

// An ordinary stop settles a lost-result warning and a shell repair in the same
// reconcile pass. Publishing the warning before the repair runs delays the
// repair behind the user's Notification hook command and leaves clients on a
// stale stop state (#6939).
//
// The old order is observable from the hook itself: it records the store size it
// sees. Published before the repair, the hook sees the pre-drain size; published
// after the plans run, it sees the repair's appended events. The hook then
// blocks until the test releases it, so the test can also check that the stop
// completes while the hook is still running.
func TestOrdinaryStopPublishesLostResultWarningAfterShellRepairs(t *testing.T) {
	requireStrandedStopWarningAndRepairShareOneReconcile(t)

	f, shellPath := newSeededStrandedStop(t)

	// The hook records the byte size of the shell store at the moment the
	// warning is published. No other writer touches that store during the
	// drain, so a size above the pre-drain size means the repair had already
	// appended its events when the warning went out.
	observationPath := filepath.Join(t.TempDir(), "observed-shell-store-size")
	startedPath := filepath.Join(t.TempDir(), "hook-started")
	proceedPath := filepath.Join(t.TempDir(), "hook-proceed")
	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `size=$(wc -c < ` + shellquote.Literal(shellPath) + `); printf '%s\n' "$size" > ` + shellquote.Literal(observationPath) +
			`; : > ` + shellquote.Literal(startedPath) +
			`; i=0; while [ ! -f ` + shellquote.Literal(proceedPath) + ` ] && [ "$i" -lt 1200 ]; do sleep 0.05; i=$((i+1)); done`,
		// The loop bound outlives testwait's 30s ceiling: a synchronous dispatch
		// then holds the driver past the wait and fails there, where a shorter
		// bound would let a blocked driver look like a slow one.
		Timeout: 120,
	})
	f.root.hookRunner = runner
	f.root.env = execenv.NewLocalExecutionEnvironment(t.TempDir())

	before, err := os.Stat(shellPath)
	if err != nil {
		t.Fatalf("stat seeded shell store: %v", err)
	}

	driver := newDelegateStopDriver()
	driven := make(chan struct{})
	go func() {
		f.controller.runStopReconcileDriver(f.controller.stopForResult(f.stop), driver, f.root)
		close(driven)
	}()
	testwait.Until(t, "the lost-result warning's Notification hook to start", func() bool {
		_, statErr := os.Stat(startedPath)
		return statErr == nil
	})
	// The hook is now running and blocked. The drain must reach its completion
	// without waiting for it: the remaining passes record completion and the
	// driver returns while the user's command is still running (#6939).
	release := func() {
		if writeErr := os.WriteFile(proceedPath, []byte("go\n"), 0o644); writeErr != nil {
			t.Fatalf("release the blocked hook: %v", writeErr)
		}
	}
	testwait.Until(t, "the stop reconcile driver to finish while the Notification hook is blocked", func() bool {
		select {
		case <-driven:
			return true
		default:
			return false
		}
	})
	if driver.err != nil {
		release()
		t.Fatalf("ordinary stop driver: %v", driver.err)
	}
	if !channelClosed(f.stop.done) {
		release()
		t.Fatal("the ordinary stop was not complete while the Notification hook was still running: " +
			"the lost-result warning's hook held the completion clients see behind its command (#6939)")
	}
	release()

	raw, err := os.ReadFile(observationPath)
	if err != nil {
		t.Fatalf("the lost-result warning never dispatched its Notification hook: %v", err)
	}
	observed, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		t.Fatalf("hook observation %q is not a byte count: %v", string(raw), err)
	}
	if observed <= before.Size() {
		t.Fatalf("the Notification hook observed the shell store at %d bytes, the pre-drain size (%d): "+
			"the ordinary stop published the lost-result warning before its shell repairs ran, so the "+
			"hook delayed the repair and the stop state its clients see (#6939)", observed, before.Size())
	}
}

// A shell repair that fails aborts the drain before it can finish, so the
// warning the pass settled must still reach its publication on that error exit:
// the receipt it names is already gone from the store, so no later pass can
// rediscover it (#6939).
//
// The seam is a read-only store: evidence collection opens it O_RDONLY and sees
// the pending repair, while the repair's O_RDWR open fails. Root bypasses the
// permission check, so the guard skips there.
func TestOrdinaryStopPublishesSettledWarningWhenShellRepairFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the read-only store seam relies on permission denial, which root bypasses")
	}
	requireStrandedStopWarningAndRepairShareOneReconcile(t)

	f, shellPath := newSeededStrandedStop(t)

	hookDispatchLog := filepath.Join(t.TempDir(), "hook-dispatches")
	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf 'hit\n' >> ` + shellquote.Literal(hookDispatchLog),
		Timeout: 30,
	})
	f.root.hookRunner = runner
	f.root.env = execenv.NewLocalExecutionEnvironment(t.TempDir())

	if err := os.Chmod(shellPath, 0o444); err != nil {
		t.Fatalf("make the shell store read-only: %v", err)
	}
	err := f.controller.drainStopAbandonable(tripwireCtx(t), f.controller.stopForResult(f.stop), f.root, nil, false)
	if err == nil {
		t.Fatal("the ordinary stop drain succeeded despite the read-only shell store: the fixture did " +
			"not exercise the repair failure this guard exists for")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "open" || pathErr.Path != shellPath {
		t.Fatalf("the drain failed with %v, not an open of the read-only shell store %s: this guard did "+
			"not exercise the repair error exit", err, shellPath)
	}

	f.root.waitStopWarningHooks()
	data, readErr := os.ReadFile(hookDispatchLog)
	if readErr != nil {
		t.Fatalf("the drain returned %v and the settled lost-result warning never dispatched its "+
			"Notification hook: the error exit dropped a warning the pass had already settled", err)
	}
	if got := strings.Count(string(data), "hit"); got != 1 {
		t.Fatalf("the settled lost-result warning dispatched its Notification hook %d time(s) on the "+
			"repair error exit, want exactly 1", got)
	}
}
