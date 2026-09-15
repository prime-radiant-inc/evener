//go:build linux || darwin

package hubcore

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// crashedDaemonStateDir is a state directory holding the session API log a
// daemon leaves behind, which the identity verifier opens before it can judge
// the process at the entry's PID.
func crashedDaemonStateDir(t *testing.T, sessionID string) string {
	t.Helper()
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sessions", sessionID+".api.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return stateDir
}

// The identity probe evicts only on positive evidence. Verification that
// cannot vouch either way - here the daemon's API log is missing from the
// state dir, so the inspection errors before it can judge the process; the
// same shape as a pidfd that will not open or a /proc read racing a closing
// descriptor on a busy daemon - is not evidence that the PID belongs to
// someone else, and the liveness-only retention stands.
func TestRosterKeepsRetainedEntryWhenOwnershipCannotBeVerified(t *testing.T) {
	other := exec.Command("sleep", "60")
	if err := other.Start(); err != nil {
		t.Fatalf("start stand-in process: %v", err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: other.Process.Pid, SessionID: "01UNVERIFIED", ThreadID: "01UNVERIFIED", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc", StateDir: t.TempDir(), StartedAt: time.Now().UTC()}
	writeRendezvous(t, dir, entry)
	prober := &flakyProber{sessionID: "01UNVERIFIED"}
	roster := NewRoster(dir, prober)
	roster.Refresh()
	if !roster.HasConfirmedEntry(entry) {
		t.Fatal("confirmed entry did not acquire its route")
	}
	prober.fail = true
	roster.Refresh()
	if !roster.HasConfirmedEntry(entry) {
		t.Fatal("an unverifiable identity evicted a live daemon")
	}
	if live, ok := roster.Find("01UNVERIFIED"); !ok || live.Crashed {
		t.Fatalf("an unverifiable identity reads as crashed: ok=%v entry=%+v", ok, live)
	}
}

// And the positive case through the real verifier: a live process at the
// entry's PID that started after the entry was written is another process
// (a PID the kernel reused), so once the socket stops answering the entry is
// never published again - it reads as crashed rather than parking unconfirmed.
func TestRosterDropsEntryWhenAnotherProcessHoldsThePID(t *testing.T) {
	for _, order := range []struct {
		name   string
		probes []bool
	}{{"socket closed", []bool{false, false}}} {
		t.Run(order.name, func(t *testing.T) {
			startedAt := time.Now().UTC()
			time.Sleep(50 * time.Millisecond) // the reused PID's process starts after the entry
			other := exec.Command("sleep", "60")
			if err := other.Start(); err != nil {
				t.Fatalf("start stand-in process: %v", err)
			}
			t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
			dir := t.TempDir()
			entry := rendezvous.Entry{PID: other.Process.Pid, SessionID: "01OTHER", ThreadID: "01OTHER", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc", StateDir: crashedDaemonStateDir(t, "01OTHER"), StartedAt: startedAt}
			writeRendezvous(t, dir, entry)
			prober := &flakyProber{sessionID: "01OTHER"}
			roster := NewRoster(dir, prober)
			for _, probe := range order.probes {
				prober.fail = !probe
				roster.Refresh()
				if roster.HasConfirmedEntry(entry) {
					t.Fatalf("probe answering=%v: a PID held by another process acquired the daemon's route", probe)
				}
				if live, ok := roster.Find("01OTHER"); !ok || !live.Crashed {
					t.Fatalf("probe answering=%v: should read as crashed, got ok=%v entry=%+v (unconfirmed: %+v)", probe, ok, live, roster.UnconfirmedEntries())
				}
			}
		})
	}
}

// The hub is never a daemon. A stale rendezvous file whose PID the hub itself
// reused answers signal 0 and cannot be bound by the force-stop verifier
// (which refuses its own PID), so it read as unknown and was retained for as
// long as the hub ran. In the roster's reading, the hub's own PID is positive
// evidence.
func TestRosterTreatsItsOwnPIDAsAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: os.Getpid(), SessionID: "01HUB", ThreadID: "01HUB", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc", StateDir: crashedDaemonStateDir(t, "01HUB"), StartedAt: time.Now().UTC()}
	writeRendezvous(t, dir, entry)
	roster := NewRoster(dir, &flakyProber{sessionID: "01HUB", fail: true})
	roster.Refresh()
	if roster.HasConfirmedEntry(entry) {
		t.Fatal("a file naming the hub's own PID was published as a daemon")
	}
	if live, ok := roster.Find("01HUB"); !ok || !live.Crashed {
		t.Fatalf("the file should read as crashed, got ok=%v entry=%+v", ok, live)
	}
}
