package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// installAttachedManager bolts a real sshconn.Manager onto a fixture and wires
// the manager-backed seams main.go installs, so a row that adopts the channel
// renders that channel's own handshake and facts rather than a fake's. A real
// manager is the point: only sshconn's channel carries the registration it was
// built from, which is the identity the row lookup must pair with its entry.
func installAttachedManager(t *testing.T, f *updateFixture) *sshconn.Manager {
	t.Helper()
	manager := sshconn.New(f.hosts, sshconn.Options{Runner: &attachedUpdateRunner{}})
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

// rowHasLiveState reports whether row carries any live attached state: the
// attached flag or any field the attached-only lookups fill. An offline row
// must carry none of it, so the offline assertions state that one predicate
// here rather than repeating the field list.
func rowHasLiveState(row appwire.HostRow) bool {
	return row.Attached || row.ServerName != "" || row.ServerVersion != "" ||
		row.HubVersion != "" || row.OS != "" || row.Arch != ""
}
