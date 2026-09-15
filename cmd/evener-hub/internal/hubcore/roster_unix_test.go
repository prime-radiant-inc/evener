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
// cannot run at all - here the verifier refuses to bind this process's own PID,
// the same shape as a pidfd that will not open or a /proc read racing a closing
// descriptor on a busy daemon - is not evidence that the PID belongs to someone
// else, and the liveness-only retention stands (review round 6 on #1325).
func TestRosterKeepsRetainedEntryWhenOwnershipCannotBeVerified(t *testing.T) {
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: os.Getpid(), SessionID: "01UNVERIFIED", ThreadID: "01UNVERIFIED", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc", StateDir: crashedDaemonStateDir(t, "01UNVERIFIED"), StartedAt: time.Now().UTC()}
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
// entry's PID that is not an `evener serve` is another process, so the entry
// takes the crashed path and loses its route.
func TestRosterDropsRetainedEntryWhenAnotherProcessHoldsThePID(t *testing.T) {
	other := exec.Command("sleep", "60")
	if err := other.Start(); err != nil {
		t.Fatalf("start stand-in process: %v", err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: other.Process.Pid, SessionID: "01OTHER", ThreadID: "01OTHER", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc", StateDir: crashedDaemonStateDir(t, "01OTHER"), StartedAt: time.Now().UTC()}
	writeRendezvous(t, dir, entry)
	prober := &flakyProber{sessionID: "01OTHER"}
	roster := NewRoster(dir, prober)
	roster.Refresh()
	if !roster.HasConfirmedEntry(entry) {
		t.Fatal("confirmed entry did not acquire its route")
	}
	prober.fail = true
	roster.Refresh()
	if roster.HasConfirmedEntry(entry) {
		t.Fatal("a PID held by another process kept the daemon's route")
	}
	if live, ok := roster.Find("01OTHER"); !ok || !live.Crashed {
		t.Fatalf("the daemon behind a PID held by another process should read as crashed: ok=%v entry=%+v", ok, live)
	}
}
