package hub

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// installAttachedManager bolts a real sshconn.Manager onto a fixture with the
// standard attached-update runner, wiring the manager-backed seams main.go
// installs (wireAttachedManager).
func installAttachedManager(t *testing.T, f *updateFixture) *sshconn.Manager {
	t.Helper()
	return wireAttachedManager(t, f, &attachedUpdateRunner{})
}

// wireAttachedManager bolts a real sshconn.Manager onto a fixture and wires the
// manager-backed seams main.go installs, so a row that adopts the channel
// renders that channel's own handshake and facts rather than a fake's. A real
// manager is the point: only sshconn's channel carries the registration it was
// published for, which is the identity the row lookup must pair with its entry.
// runner is the process seam, so a caller can drive the preflight that resolves
// an executable path for a host that configured none.
func wireAttachedManager(t *testing.T, f *updateFixture, runner sshconn.Runner) *sshconn.Manager {
	t.Helper()
	manager := sshconn.New(f.hosts, sshconn.Options{Runner: runner})
	t.Cleanup(func() { _ = manager.Close() })
	f.m.cfg.manager = manager
	f.m.cfg.online = manager.Attached
	f.m.cfg.clientIfAttached = manager.ClientIfAttached
	f.m.cfg.handshake = func(host string, client *appwire.Client) (appwire.InitializeResponse, bool) {
		ch, ok := manager.ChannelIfAttached(host)
		if !ok {
			return appwire.InitializeResponse{}, false
		}
		return remoteHostHandshakeForChannel(ch, client)
	}
	f.m.cfg.facts = func(ctx context.Context, host string, client *appwire.Client) (appsource.HostFacts, error) {
		return remoteHostFactsIfAttached(ctx, host, client, func(h string) (attachedChannelView, bool) {
			ch, ok := manager.ChannelIfAttached(h)
			if !ok {
				return nil, false
			}
			return ch, true
		})
	}
	return manager
}

// TestHostRowPairsTheChannelWithTheEntryItRenders pins the generation fence on
// the row path: the live state under a name belongs to the registration the
// channel was built from, not merely to the name. A row whose entry is not that
// registration — same name, advanced generation (a remove/re-add or an edit),
// or the same generation with changed content — must not adopt the installed
// channel's client, attached flag, or server facts; the channel's own
// registration still renders attached. Deterministic, no sleeps: the channel is
// installed by a real Ensure over an in-memory runner.
func TestHostRowPairsTheChannelWithTheEntryItRenders(t *testing.T) {
	f := newUpdateFixture(t)
	manager := installAttachedManager(t, f)
	live, ok := f.hosts.Get("side")
	if !ok {
		t.Fatal("side is not live")
	}
	if _, err := manager.Ensure(context.Background(), "side"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// The channel's own registration: the row adopts it and renders its facts.
	row := f.m.hostRow(context.Background(), live, hostOriginSidecar)
	if !row.Attached || row.OS != "linux" || row.Arch != "amd64" {
		t.Fatalf("row for the channel's own registration = %+v, want attached with its facts", row)
	}

	// A different registration under the same name: same content, advanced
	// generation — the identity a swap leaves behind while the name is unchanged.
	other := live
	other.Generation = live.Generation + 1
	row = f.m.hostRow(context.Background(), other, hostOriginSidecar)
	if rowHasLiveState(row) {
		t.Fatalf("row for another registration = %+v, want offline with none of the installed channel's state", row)
	}

	// The same generation with changed content — the other half of the
	// registration predicate: content equality could not tell a swapped entry
	// from this row's, so it must not pass on generation alone.
	other = live
	other.SSH = "elsewhere.example"
	row = f.m.hostRow(context.Background(), other, hostOriginSidecar)
	if rowHasLiveState(row) {
		t.Fatalf("row for changed content = %+v, want offline with no facts", row)
	}
}

// TestHostRowAcrossTheUpdateWindowDoesNotAdoptTheNewGenerationsChannel pins the
// motivating window: an edit advances the registry generation and retires the
// channel under it, and the next attach publishes a channel for the new
// identity. A row that snapshotted the pre-swap entry (hostRow runs without the
// mutation mutex, so a parked facts read holds up no commit) can reach its live
// lookups after that attach — it must render offline rather than pair the old
// configuration with the new channel's attached state and server facts, while
// the new identity's own row still adopts its channel.
func TestHostRowAcrossTheUpdateWindowDoesNotAdoptTheNewGenerationsChannel(t *testing.T) {
	f := newUpdateFixture(t)
	manager := installAttachedManager(t, f)
	preSwap, ok := f.hosts.Get("side")
	if !ok {
		t.Fatal("side is not live")
	}

	// The edit the window is about, then the attach it admits: the channel now
	// installed was built from a later registration than preSwap.
	if _, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "edited.example"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := manager.Ensure(context.Background(), "side"); err != nil {
		t.Fatalf("Ensure after the edit: %v", err)
	}
	current, ok := f.hosts.Get("side")
	if !ok {
		t.Fatal("the edited entry is not live")
	}

	// The stale row resolves while the new generation's channel is installed: it
	// must not read that channel's live state.
	stale := f.m.hostRow(context.Background(), preSwap, hostOriginSidecar)
	if rowHasLiveState(stale) {
		t.Fatalf("row built from the pre-swap entry = %+v, want offline with none of the new channel's state", stale)
	}

	// The new identity's own row still adopts its channel: the guard refuses
	// only the mismatched pairing, not the live one.
	fresh := f.m.hostRow(context.Background(), current, hostOriginSidecar)
	if !fresh.Attached || fresh.OS != "linux" || fresh.Arch != "amd64" {
		t.Fatalf("row for the edited identity = %+v, want attached with its channel's facts", fresh)
	}
}

// resolvingUpdateRunner is an attachedUpdateRunner whose host has no
// evener_path and whose binary is absent from the non-interactive PATH but
// present at the installer default ~/.local/bin/evener. Preflight therefore
// resolves that path and records it (probeInstallerDefaultExecutable →
// setResolvedTarget), exactly as it does for the common host that configured no
// evener_path; the Manager folds it onto the copy of the host it dials, never
// onto the registry entry.
type resolvingUpdateRunner struct {
	attachedUpdateRunner
}

func (*resolvingUpdateRunner) Run(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
	joined := strings.Join(argv, " ")
	switch {
	case strings.Contains(joined, "/home/dev/.local/bin/evener launch-check"):
		return []byte(`{"protocol":"evener-appwire-v5","version":"dev","launch_flags":["api-log"]}`), nil
	case strings.Contains(joined, " evener launch-check"):
		// The host's non-interactive PATH does not carry the binary.
		return []byte("sh: 1: evener: not found\n"), evenerMissingExitStatus()
	case strings.Contains(joined, "command -v evener >/dev/null 2>&1"):
		// The dedicated executable probe: the binary is absent from the
		// non-interactive PATH. Only this probe's own 1 is the verified absent
		// answer; the launch-check's 127 and its text are not the recognizer.
		return nil, executableProbeAbsentExitStatus()
	case strings.Contains(joined, "[ -f ") && strings.Contains(joined, ".local/bin/evener"):
		return []byte("/home/dev/.local/bin/evener\n"), nil
	default:
		return (&attachedUpdateRunner{}).Run(context.Background(), argv, nil)
	}
}

// evenerMissingExitStatus builds the real *exec.ExitError a shell reports for a
// command that cannot be found (status 127). The launch-check still fails this
// way on a host with no evener on the PATH, but the missing-executable result is
// recognized from executableProbeAbsentExitStatus below, not from this status.
func evenerMissingExitStatus() error {
	return exec.Command("sh", "-c", "exit 127").Run()
}

// executableProbeAbsentExitStatus builds the real *exec.ExitError the dedicated
// executable probe reports for an absent target (status 1).
func executableProbeAbsentExitStatus() error {
	return exec.Command("sh", "-c", "exit 1").Run()
}

// TestHostRowRendersAttachedWhenTheManagerResolvedTheTarget pins the roborev
// High on PR #2125: a row must pair its entry with the installed channel by the
// registration the registry holds, not by the Manager's dial copy, which
// carries the executable path the Manager resolved for a host that configured
// no evener_path. The registry entry here has an empty EvenerPath while the
// installed channel was dialed with the resolved target ~/.local/bin/evener, so
// the row must still render attached with the channel's live facts rather than
// going offline — the common case, since most hosts configure no evener_path.
// Deterministic, no sleeps: the channel is installed by a real Ensure over an
// in-memory runner whose preflight performs the resolution.
func TestHostRowRendersAttachedWhenTheManagerResolvedTheTarget(t *testing.T) {
	f := newUpdateFixture(t)
	manager := wireAttachedManager(t, f, &resolvingUpdateRunner{})
	live, ok := f.hosts.Get("side")
	if !ok {
		t.Fatal("side is not live")
	}
	if live.EvenerPath != "" {
		t.Fatalf("fixture entry EvenerPath = %q, want empty (the common case)", live.EvenerPath)
	}
	if _, err := manager.Ensure(context.Background(), "side"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Prove the test exercises the defect: the channel really was dialed with
	// the Manager's resolved target, while the registry entry stayed empty.
	ch, ok := manager.ChannelIfAttached("side")
	if !ok {
		t.Fatal("no installed channel")
	}
	if ch.Host().EvenerPath != "/home/dev/.local/bin/evener" {
		t.Fatalf("channel dial host EvenerPath = %q, want the manager's resolved target", ch.Host().EvenerPath)
	}
	stored, ok := f.hosts.Get("side")
	if !ok || stored.EvenerPath != "" {
		t.Fatalf("registry entry after Ensure = %+v, want EvenerPath still empty", stored)
	}

	// The row renders from the registry entry (empty EvenerPath) and must pair
	// it with the installed channel by its registration.
	row := f.m.hostRow(context.Background(), live, hostOriginSidecar)
	if !row.Attached || row.OS != "linux" || row.Arch != "amd64" {
		t.Fatalf("row for the resolved-target host = %+v, want attached with its facts", row)
	}
}

// rowHasLiveState reports whether row carries any live attached state: the
// attached flag or any field the attached-only lookups fill. An offline row
// must carry none of it, so the offline assertions state that one predicate
// here rather than repeating the field list.
func rowHasLiveState(row appwire.HostRow) bool {
	return row.Attached || row.ServerName != "" || row.ServerVersion != "" ||
		row.HubVersion != "" || row.OS != "" || row.Arch != ""
}
