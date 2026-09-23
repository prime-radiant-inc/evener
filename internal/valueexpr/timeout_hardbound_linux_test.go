//go:build linux

package valueexpr

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The command timeout must be a hard bound even when a descendant escapes
// the process group and keeps the captured pipes open: the group SIGKILL
// reaches the direct child, but a setsid'd grandchild survives it holding
// stdout, which would leave Wait — and a whole credential resolve or config
// load — blocked until that grandchild exits. WaitDelay closes the pipes
// once the budget elapses instead. The 5s bound is generous on purpose: the
// test pins that evaluate RETURNS, not the exact moment it returns.
func TestCommandTimeoutHardBoundDespiteEscapedDescendant(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	// The setsid'd descendant escapes the group on purpose (that is the
	// regression), so the test must reap it itself: it holds no live pipe
	// once the drain closed them, and nothing else would ever reach it.
	// The descendant reports its own pid, so the test reaps exactly that
	// process — a pattern kill can name an unrelated one — and can assert
	// the escape actually happened: an earlier revision marked the process
	// with `exec -a`, which dash does not support, so the descendant died
	// at spawn and the test passed without exercising the drain at all.
	dir := t.TempDir()
	marker := filepath.Join(dir, "escapee.pid")
	t.Cleanup(func() {
		if pid, err := os.ReadFile(marker); err == nil {
			exec.Command("kill", "-9", strings.TrimSpace(string(pid))).Run()
		}
	})
	commandTimeout = 500 * time.Millisecond

	start := time.Now()
	_, err := evaluate(`setsid sh -c 'sleep 10 & echo $! > ` + marker + `' & exec sleep 30`)
	if err == nil {
		t.Fatal("evaluate returned nil; want the timeout error")
	}
	if cmdErr, ok := errors.AsType[*CommandError](err); !ok || !cmdErr.Timeout {
		t.Fatalf("err = %v; want the command timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("evaluate blocked %v; the timeout is not a hard bound", elapsed)
	}
	// The escapee held the captured stdout until the drain closed it, and
	// its ten seconds still run: it must exist and have outlived the run's
	// group kill, or the scenario above never materialized.
	pid, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the escaped descendant never reported its pid (%v); the scenario did not run", err)
	}
	escapee := strings.TrimSpace(string(pid))
	if exec.Command("kill", "-0", escapee).Run() != nil {
		t.Fatalf("the escaped descendant (pid %s) did not outlive the run's group kill", escapee)
	}
}

// The wait the caller experiences is bounded by the deadline plus the
// drain grace, the composition the spec documents: the group dies at the
// deadline, and the drain closes the pipes a straggler still holds within
// its own small budget — never a second deadline.
func TestCommandWaitBoundedByDeadlinePlusDrain(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	dir := t.TempDir()
	marker := filepath.Join(dir, "escapee.pid")
	t.Cleanup(func() {
		if pid, err := os.ReadFile(marker); err == nil {
			exec.Command("kill", "-9", strings.TrimSpace(string(pid))).Run()
		}
	})
	commandTimeout = 500 * time.Millisecond
	drainGrace = 200 * time.Millisecond

	start := time.Now()
	_, err := evaluate(`setsid sh -c 'sleep 10 & echo $! > ` + marker + `' & exec sleep 30`)
	if cmdErr, ok := errors.AsType[*CommandError](err); !ok || !cmdErr.Timeout {
		t.Fatalf("err = %v; want the command timeout", err)
	}
	if elapsed := time.Since(start); elapsed > commandTimeout+drainGrace+time.Second {
		t.Fatalf("evaluate blocked %v; the wait must close within the deadline plus the drain grace", elapsed)
	}
}

// WaitDelay also bounds the drain when the shell itself exits cleanly and
// only a descendant holds the captured pipes. On that path no deadline ever
// fires, so Cancel — which os/exec calls only from the Context's watcher —
// never kills the group: the ErrWaitDelay branch must, or every resolve of
// a config whose command leaves a background job running leaks one process
// (and a failed run is uncached, so every retry leaks another).
func TestWaitDelayKillsLingeringGroup(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	// The drain grace must sit strictly inside the run budget, or the
	// context deadline fires first and the run reports the timeout instead.
	commandTimeout = 800 * time.Millisecond
	drainGrace = 200 * time.Millisecond

	script := filepath.Join(t.TempDir(), "linger.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("pkill", "-f", script).Run() })

	_, err := realRunCommand(`"` + script + `" & exit 0`)
	if err == nil {
		t.Fatal("realRunCommand returned nil; want the drain error")
	}
	cmdErr, ok := errors.AsType[*CommandError](err)
	if !ok || cmdErr.Timeout || !strings.Contains(cmdErr.Error(), "WaitDelay") {
		t.Fatalf("err = %v; want the WaitDelay drain error, not the timeout", err)
	}

	// The kill lands with the return; give the SIGKILL a moment to show.
	var lingering string
	for range 20 {
		out, _ := exec.Command("pgrep", "-af", script).Output()
		if len(out) == 0 {
			lingering = ""
			break
		}
		lingering = string(out)
		time.Sleep(100 * time.Millisecond)
	}
	if lingering != "" {
		t.Fatalf("the shell's descendant outlived the run: %s", lingering)
	}
}

// The nonzero-exit sibling: os/exec reports the shell's own exit status in
// preference to the drain's ErrWaitDelay, so a failed run whose descendant
// still holds the captured pipes must kill the group too — the kill cannot
// live on the ErrWaitDelay branch alone.
func TestWaitDelayKillsLingeringGroupOnNonzeroExit(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	commandTimeout = 800 * time.Millisecond
	drainGrace = 200 * time.Millisecond

	script := filepath.Join(t.TempDir(), "linger.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("pkill", "-f", script).Run() })

	_, err := realRunCommand(`"` + script + `" & exit 3`)
	cmdErr, ok := errors.AsType[*CommandError](err)
	if !ok || cmdErr.Status != 3 {
		t.Fatalf("err = %v; want the shell's exit status 3", err)
	}

	var lingering string
	for range 20 {
		out, _ := exec.Command("pgrep", "-af", script).Output()
		if len(out) == 0 {
			lingering = ""
			break
		}
		lingering = string(out)
		time.Sleep(100 * time.Millisecond)
	}
	if lingering != "" {
		t.Fatalf("the shell's descendant outlived the failed run: %s", lingering)
	}
}

// A successful command's group dies with the run too: a mint command is
// not a daemon launcher, and a background job it left behind with its
// pipes closed would otherwise outlive the run on the success path, where
// no failure ever reaches the group.
func TestSuccessfulRunKillsLingeringGroup(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	commandTimeout = 800 * time.Millisecond
	drainGrace = 200 * time.Millisecond

	dir := t.TempDir()
	script := filepath.Join(dir, "linger-detached.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30.5 >/dev/null 2>&1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The shell reports the background job's pid, so the test tracks and
	// reaps exactly the process it spawned — a pattern kill can name an
	// unrelated sleeper.
	marker := filepath.Join(dir, "sleeper.pid")
	t.Cleanup(func() {
		if pid, err := os.ReadFile(marker); err == nil {
			exec.Command("kill", "-9", strings.TrimSpace(string(pid))).Run()
		}
	})

	res, err := evaluate(`"` + script + `" & echo $! > ` + marker + `; echo token`)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Value != "token" {
		t.Fatalf("value = %q; want the echoed token", res.Value)
	}

	pid, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the background job never reported its pid (%v); the scenario did not run", err)
	}
	// The kill lands with the return; give the SIGKILL a moment to show.
	sleeper := strings.TrimSpace(string(pid))
	for range 20 {
		if exec.Command("kill", "-0", sleeper).Run() != nil {
			return // the group kill reaped it
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the successful run's background job (pid %s) outlived it", sleeper)
}
