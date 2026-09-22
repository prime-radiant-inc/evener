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
	t.Cleanup(func() { exec.Command("pkill", "-f", "hardbound-escape").Run() })
	commandTimeout = 500 * time.Millisecond

	start := time.Now()
	_, err := evaluate(`setsid sh -c "exec -a hardbound-escape sleep 10" & exec sleep 30`)
	if err == nil {
		t.Fatal("evaluate returned nil; want the timeout error")
	}
	if cmdErr, ok := errors.AsType[*CommandError](err); !ok || !cmdErr.Timeout {
		t.Fatalf("err = %v; want the command timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("evaluate blocked %v; the timeout is not a hard bound", elapsed)
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

	script := filepath.Join(t.TempDir(), "linger-detached.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30.5 >/dev/null 2>&1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("pkill", "-f", "sleep 30.5").Run() })

	res, err := evaluate(`"` + script + `" & echo token`)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Value != "token" {
		t.Fatalf("value = %q; want the echoed token", res.Value)
	}

	var lingering string
	for range 20 {
		out, _ := exec.Command("pgrep", "-af", "sleep 30.5").Output()
		if len(out) == 0 {
			lingering = ""
			break
		}
		lingering = string(out)
		time.Sleep(100 * time.Millisecond)
	}
	if lingering != "" {
		t.Fatalf("the successful run's background job outlived it: %s", lingering)
	}
}
