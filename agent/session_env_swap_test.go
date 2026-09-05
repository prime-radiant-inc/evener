package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
)

// runGitCmd runs a real git command (via the ambient PATH, not through any
// session execution environment) for test fixture setup.
func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestSwapEnvAndRefresh_UpdatesEnvInfoAndPromptCache proves swapEnvAndRefresh
// (a) installs the new env, (b) recomputes envInfo (working dir, git branch,
// etc.) against the NEW root rather than leaving it frozen at the old one,
// and (c) the eagerly-rendered system prompt cache reflects the new
// directory — the exact staleness spec §7 exists to prevent (before this
// fix the model would believe it's still in the old directory).
func TestSwapEnvAndRefresh_UpdatesEnvInfoAndPromptCache(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	oldWD := sess.currentEnv().WorkingDirectory()

	repoDir := t.TempDir()
	runGitCmd(t, repoDir, "init")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	runGitCmd(t, repoDir, "add", "README.md")
	runGitCmd(t, repoDir, "commit", "-m", "initial commit")
	runGitCmd(t, repoDir, "checkout", "-b", "feature-branch")

	base, ok := sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("session env is not *execenv.LocalExecutionEnvironment: %T", sess.currentEnv())
	}
	next := base.WithWorkingDirectory(repoDir)

	sess.swapEnvAndRefresh(next, nil)

	if got := sess.currentEnv().WorkingDirectory(); got != repoDir {
		t.Fatalf("currentEnv().WorkingDirectory() = %q, want %q", got, repoDir)
	}
	if sess.currentEnv().WorkingDirectory() == oldWD {
		t.Fatalf("currentEnv().WorkingDirectory() still equals the pre-swap root %q", oldWD)
	}

	sess.mu.Lock()
	ei := sess.envInfo
	prompt := sess.cachedSystemPrompt
	sess.mu.Unlock()

	if ei.WorkingDir != repoDir {
		t.Errorf("envInfo.WorkingDir = %q, want %q", ei.WorkingDir, repoDir)
	}
	if !ei.IsGitRepo {
		t.Errorf("envInfo.IsGitRepo = false, want true (swapped into a git repo)")
	}
	if ei.GitBranch != "feature-branch" {
		t.Errorf("envInfo.GitBranch = %q, want %q", ei.GitBranch, "feature-branch")
	}

	if !strings.Contains(prompt, repoDir) {
		t.Errorf("cachedSystemPrompt does not mention the new working dir %q:\n%s", repoDir, prompt)
	}
	if !strings.Contains(prompt, "feature-branch") {
		t.Errorf("cachedSystemPrompt does not mention the new git branch %q:\n%s", "feature-branch", prompt)
	}
}

const gitCountingShimScript = `#!/bin/sh
printf '%s\n' "$*" >> "$SWAP_TEST_LOG"
"$SWAP_TEST_REAL_GIT" "$@"
`

func TestSwapEnvAndRefresh_TestConfigSkipsGitDiscovery(t *testing.T) {
	sess := newSession(t,
		withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true}),
		withoutGitSnapshot(),
	)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not found on PATH: %v", err)
	}

	repoDir := t.TempDir()
	runGitCmd(t, repoDir, "init")

	shimDir := t.TempDir()
	shimPath := filepath.Join(shimDir, "git")
	if err := os.WriteFile(shimPath, []byte(gitCountingShimScript), 0o755); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "git.log")

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+origPath)
	t.Setenv("SWAP_TEST_REAL_GIT", realGit)
	t.Setenv("SWAP_TEST_LOG", logPath)

	base, ok := sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("session env is not *execenv.LocalExecutionEnvironment: %T", sess.currentEnv())
	}
	next := base.WithWorkingDirectory(repoDir)

	sess.swapEnvAndRefresh(next, nil)

	sess.mu.Lock()
	ei := sess.envInfo
	sess.mu.Unlock()

	if ei.WorkingDir != repoDir {
		t.Errorf("envInfo.WorkingDir = %q, want %q", ei.WorkingDir, repoDir)
	}
	if ei.IsGitRepo {
		t.Error("envInfo.IsGitRepo = true, want false when git snapshot is skipped")
	}
	if data, err := os.ReadFile(logPath); err == nil && strings.TrimSpace(string(data)) != "" {
		t.Fatalf("swapEnvAndRefresh called git despite skipGitSnapshot+NoProjectPrompts; commands:\n%s", data)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read git log: %v", err)
	}
}

// gitShimScript is a PATH-shim `git` that, for every invocation, touches a
// marker file and then blocks until the test harness creates a companion
// ack file (bounded so a harness bug can't hang the test forever), before
// delegating to the real git binary. The block turns "the subprocess is
// running" into a rendezvous: the harness knows the subprocess is paused
// right there, so it can sample s.mu's state at that exact instant instead
// of racing a background poller against a fixed-duration sleep.
const gitShimScript = `#!/bin/sh
touch "$SWAP_TEST_MARKER"
i=0
while [ ! -e "$SWAP_TEST_MARKER.ack" ] && [ "$i" -lt 2000 ]; do
    sleep 0.005
    i=$((i + 1))
done
rm -f "$SWAP_TEST_MARKER.ack"
"$SWAP_TEST_REAL_GIT" "$@"
rc=$?
rm -f "$SWAP_TEST_MARKER"
exit $rc
`

// TestSwapEnvAndRefresh_NoGitForkWhileLocked proves the normative two-step
// split from spec §7: the git snapshot (several `git` forks, `status` can
// take seconds on a big repo) must run OUTSIDE s.mu, never inside it —
// holding s.mu across a subprocess would stall every event emit, Meta()
// autosave, and hub poll.
//
// Mechanism: a PATH-shim `git` touches a marker file on every invocation and
// then blocks, mid-fork, until the watcher goroutine acks it. That rendezvous
// lets the watcher TryLock s.mu at the exact instant each subprocess is
// running — a deterministic sample, not a poll racing a fixed-duration sleep
// (a fixed sleep plus a background poller is inherently flaky: under
// scheduler contention, such as -race or a loaded CI runner, the poller can
// go unscheduled long enough to miss the entire window). For each
// marker-present invocation it records whether s.mu was unlocked at that
// instant. Any invocation where it wasn't means s.mu was held while a git
// subprocess forked — exactly the bug this test exists to catch.
func TestSwapEnvAndRefresh_NoGitForkWhileLocked(t *testing.T) {
	// This test asserts s.mu's lock state at fork time, not latency, so its
	// correctness must not depend on machine load. The rendezvous below holds
	// each shimmed git invocation open until the watcher acks it; under heavy
	// scheduler contention (e.g. the -race gate on a loaded CI runner) that
	// ack can occasionally take longer than the production 2s git-exec
	// deadline, which would starve the shim for a reason unrelated to the
	// locking behavior under test. Widen both packages' deadlines for this
	// test only — mirrors execenv's TestResolveMainRepoRoot_SeparateCacheSlots.
	origAgentTimeout := gitExecTimeout
	gitExecTimeout = 30 * time.Second
	t.Cleanup(func() { gitExecTimeout = origAgentTimeout })
	t.Cleanup(execenv.SetGitExecTimeoutForTesting(30 * time.Second))

	sess := newSession(t)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not found on PATH: %v", err)
	}

	repoDir := t.TempDir()
	runGitCmd(t, repoDir, "init")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	runGitCmd(t, repoDir, "add", "README.md")
	runGitCmd(t, repoDir, "commit", "-m", "initial commit")
	runGitCmd(t, repoDir, "remote", "add", "origin", "https://example.invalid/repo.git")

	shimDir := t.TempDir()
	shimPath := filepath.Join(shimDir, "git")
	if err := os.WriteFile(shimPath, []byte(gitShimScript), 0o755); err != nil {
		t.Fatalf("write git shim: %v", err)
	}

	scratchDir := t.TempDir()
	markerPath := filepath.Join(scratchDir, "git-marker")

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+origPath)
	t.Setenv("SWAP_TEST_REAL_GIT", realGit)
	t.Setenv("SWAP_TEST_MARKER", markerPath)

	base, ok := sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("session env is not *execenv.LocalExecutionEnvironment: %T", sess.currentEnv())
	}
	next := base.WithWorkingDirectory(repoDir)

	ackPath := markerPath + ".ack"
	stop := make(chan struct{})
	done := make(chan struct{})
	var totalWindows, badWindows, freeSamples int
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := os.Stat(markerPath); err == nil {
				// Rendezvous: the shim is blocked waiting for ackPath right
				// now, so this TryLock reflects s.mu's state at the exact
				// moment the git subprocess is running, not a racy sample.
				totalWindows++
				if sess.mu.TryLock() {
					sess.mu.Unlock()
					freeSamples++
				} else {
					badWindows++
				}
				if f, err := os.Create(ackPath); err == nil {
					f.Close()
				}
				// Wait for the shim to consume the ack and clear the marker
				// before looking for the next invocation, so the same
				// window is never counted twice.
				for {
					if _, err := os.Stat(markerPath); os.IsNotExist(err) {
						break
					}
					select {
					case <-stop:
						return
					default:
					}
					time.Sleep(time.Millisecond)
				}
				continue
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// The watcher window spans the whole call, including step 2's locked
	// section and its first post-swap prompt render.
	sess.swapEnvAndRefresh(next, nil)
	close(stop)
	<-done

	if totalWindows == 0 {
		t.Fatal("shim observed no git-invocation window (PATH shim likely not intercepting — sanity check failed)")
	}
	if freeSamples == 0 {
		t.Fatalf("watcher never observed s.mu unlocked at all; watcher is not functioning")
	}
	if badWindows > 0 {
		t.Fatalf("s.mu was locked at the moment %d/%d git subprocess invocation(s) ran — the git snapshot (or a git-forking render) ran while s.mu was locked, violating spec §7's outside-s.mu requirement", badWindows, totalWindows)
	}
}

// TestSession_RegisterTool_NoRaceWithConcurrentEnvSwap is the carry-over
// finding from Task 4: RegisterTool's cache rebuild (rebuildToolDefsCache +
// refreshSystemPromptCache) used to run unlocked, racing swapEnvAndRefresh's
// locked cache rebuild on the same s.cachedToolDefs/s.cachedSystemPrompt
// fields. Run under -race, this hammers both concurrently.
func TestSession_RegisterTool_NoRaceWithConcurrentEnvSwap(t *testing.T) {
	if !raceDetectorEnabled {
		t.Skip("race-detector stress test; run with -race")
	}
	t.Parallel()
	sess := newSession(t)

	base, ok := sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("session env is not *execenv.LocalExecutionEnvironment: %T", sess.currentEnv())
	}
	dirA := t.TempDir()
	dirB := t.TempDir()
	envA := base.WithWorkingDirectory(dirA)
	envB := base.WithWorkingDirectory(dirB)

	const swapIterations = 20
	const registerIterations = 20

	var wg sync.WaitGroup

	wg.Go(func() {
		for i := range swapIterations {
			if i%2 == 0 {
				sess.swapEnvAndRefresh(envA, nil)
			} else {
				sess.swapEnvAndRefresh(envB, nil)
			}
		}
	})

	wg.Go(func() {
		for i := range registerIterations {
			name := fmt.Sprintf("race_tool_%d", i)
			sess.RegisterTool(name, "a race-test tool", map[string]any{"type": "object"},
				func(ctx context.Context, args any) (any, error) { return "ok", nil })
		}
	})

	wg.Wait()
}

// closeFenceProbe bounds the window a fence test gives a close to reach
// environment cleanup while it holds admitted environment work. This is an
// absence proof: the join is exactly what keeps close out of cleanup while the
// work is held, so there is no ordering signal to await, only a window to let
// pass. A scripted close reaches cleanup in well under a second when the join
// is missing, so two seconds is a tripwire with margin; it is spent on every
// passing run.
const closeFenceProbe = 2 * time.Second

// A swap that passed its closing check is admitted: the session promised to
// finish it. Its refresh then runs git on the environment it is installing —
// forks on the process table the session's own close reaps — so a close that
// walks past an admitted swap tears the environment down while those commands
// are still starting, on contexts it never gets to cancel.
//
// Close therefore waits for every admitted swap before it cleans the
// environment, and the refresh runs under the session's own context so the
// close's cancel makes that wait short rather than a full close budget.
//
// The hook below holds the swap in exactly the window step 1's git occupies:
// admitted, past the scratch move, with the install still to come.
func TestWorktreeSwap_CloseWaitsForAnAdmittedSwapBeforeEnvironmentCleanup(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()

	closeBegun := make(chan struct{})
	closeDone := make(chan struct{})
	cleanupObserved := make(chan struct{})
	var refreshLiveAfterClose, cleanupDuringRefresh atomic.Bool

	r.s.cfg.testOnly.closeAfterDisposeSweepJoin = func() { close(closeBegun) }
	r.s.cfg.testOnly.envCleanupObserved = func(execenv.ExecutionEnvironment) { close(cleanupObserved) }
	r.s.cfg.testOnly.swapEnvAfterAdopt = func(refreshCtx context.Context) {
		go func() {
			defer close(closeDone)
			r.s.Close()
		}()
		<-closeBegun
		// The close cancels the session context before any teardown, so by here
		// the refresh is already cancelled: its git stops instead of forking
		// onto a process table the close is about to reap.
		if refreshCtx.Err() == nil {
			refreshLiveAfterClose.Store(true)
		}
		select {
		case <-cleanupObserved:
			cleanupDuringRefresh.Store(true)
		case <-time.After(closeFenceProbe):
		}
	}

	_, err := r.create(t, map[string]any{"name": "lane"})
	<-closeDone

	if err == nil {
		t.Error("the enter succeeded while the session closed under it, want a refusal")
	}
	if refreshLiveAfterClose.Load() {
		t.Error("the refresh context was still live after the session began closing: the close cannot stop the git the refresh forks")
	}
	if cleanupDuringRefresh.Load() {
		t.Error("the close cleaned the session's environment while an admitted swap was still in flight")
	}
}

// The fence on admitted environment work is BOUNDED by the close budget on
// purpose. The refresh runs under the session's own context, which the close
// cancels before it reaches the join, so the bound only bites when a process is
// already ignoring cancellation — and an unbounded fence there would turn one
// hung git into a daemon that never shuts down. Giving up is the lesser
// failure, but it must not be a silent one: the close is about to reap the
// process table under whatever is still running, so it says what it walked
// past, by name.
func TestWorktreeSwap_CloseBudgetExpiringOnTheEnvWorkFenceNamesWhatItWalkedPast(t *testing.T) {
	oldBudget := LaneClosePassBudget
	LaneClosePassBudget = 200 * time.Millisecond
	t.Cleanup(func() { LaneClosePassBudget = oldBudget })

	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	lanePath, _ := createLaneExpectations(t, r, "lane")

	warnings := collectWarningsUntilClosed(r.s)

	closeBegun := make(chan struct{})
	closeDone := make(chan struct{})
	r.s.cfg.testOnly.closeAfterDisposeSweepJoin = func() { close(closeBegun) }
	r.s.cfg.testOnly.swapEnvAfterAdopt = func(context.Context) {
		go func() {
			defer close(closeDone)
			r.s.Close()
		}()
		<-closeBegun
		// Stand in for a refresh whose git ignores the cancellation the close
		// already delivered: outlast the whole budget.
		time.Sleep(2 * LaneClosePassBudget)
	}

	_, err := r.create(t, map[string]any{"name": "lane"})
	<-closeDone

	if err == nil {
		t.Error("the enter succeeded while the session closed under it, want a refusal")
	}
	// Close completed rather than hanging behind the held swap: <-closeDone
	// returned above, and the events channel it closes ends the collector.
	msgs := <-warnings
	found := fenceWarnings(msgs)
	if len(found) != 1 {
		t.Fatalf("fence warnings = %q, want exactly one naming the work the close walked past; all warnings were %q", found, msgs)
	}
	fence := found[0]
	if !strings.Contains(fence, lanePath) {
		t.Errorf("fence warning %q does not name the swap still in flight (%s)", fence, lanePath)
	}
}

// collectWarningsUntilClosed drains every EventWarning off sess until its close
// shuts the events channel, and hands the messages back on the returned
// channel. A fence test reads it AFTER the close it is watching has returned:
// ranging over the channel is what makes "closed" the terminator, so unlike
// warningMessages it is safe across a close.
func collectWarningsUntilClosed(sess *Session) <-chan []string {
	out := make(chan []string, 1)
	go func() {
		var msgs []string
		for ev := range sess.Events() {
			if ev.Kind != events.EventWarning {
				continue
			}
			if data, ok := ev.Data.(events.WarningData); ok {
				msgs = append(msgs, data.Message)
			}
		}
		out <- msgs
	}()
	return out
}

// fenceWarnings returns the environment-work fence warnings among msgs.
func fenceWarnings(msgs []string) []string {
	var found []string
	for _, msg := range msgs {
		if strings.Contains(msg, "environment work still in flight") {
			found = append(found, msg)
		}
	}
	return found
}

// A close whose budget is already spent still reaches the environment-work
// fence: the delegate-tree stop cancels the cascade budget outright when its
// own stop timed out, and the close carries on to cleanup. With nothing
// admitted there is nothing the cleanup can run under, so there is nothing to
// say — and a warning naming an empty list is noise on every such close.
func TestWorktreeSwap_ExpiredBudgetWithNothingAdmittedSaysNothing(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	warnings := collectWarningsUntilClosed(r.s)

	// A deadline already in the past: ensureCloseBudget reuses an incoming one
	// rather than minting a fresh deadline, so the fence join meets a budget
	// that expired before the close began.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	r.s.close(ctx, true)

	if got := fenceWarnings(<-warnings); len(got) != 0 {
		t.Errorf("a close with nothing admitted warned about the fence anyway: %q", got)
	}
}
