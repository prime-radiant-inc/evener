package hubcore

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// Probe-carried capabilities: the probe reads the daemon's own Evener
// capabilities from the same projection cut as the status it already carries,
// and the roster publishes them, so the hub's list rows can advertise the
// daemon's answer instead of a hand approximation — the #1840 one-answer
// rule: the same session must not read differently from ListThreads and from
// ThreadRead.

// probedCapabilities is a set no hub hand literal would produce: an
// under-wired daemon's idle answer with no steer, queue or skill-input
// surface. If the roster loses the probe's set, the all-true fallback
// approximation cannot masquerade as it.
var probedCapabilities = appwire.ThreadCapabilities{
	Send: true, Compact: true, Clear: true, Shutdown: true,
	ChangeModel: true, ChangeVisionModel: true, Rename: true,
	Goal: true, SharedNotes: true,
}

func TestRosterCarriesProbeCapabilities(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1002, Address: "127.0.0.1:50002"})
	r := NewRoster(dir, &runningSubagentProber{result: ProbeResult{
		SessionID:         "01CAPS",
		Status:            "idle",
		Capabilities:      probedCapabilities,
		CapabilitiesKnown: true,
		OK:                true,
	}})
	r.Refresh()

	entries := r.List()
	if len(entries) != 1 {
		t.Fatalf("roster entries = %+v, want the probed session", entries)
	}
	if !entries[0].CapabilitiesKnown {
		t.Fatal("LiveEntry.CapabilitiesKnown = false, want true: the roster dropped the probe's capabilities answer")
	}
	if entries[0].Capabilities != probedCapabilities {
		t.Fatalf("LiveEntry.Capabilities = %+v, want the probe's set %+v", entries[0].Capabilities, probedCapabilities)
	}
}

func TestRosterEntryWithoutProbeCapabilitiesStaysUnknown(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1003, Address: "127.0.0.1:50003"})
	r := NewRoster(dir, fakeProber{sessionID: "01NOCAPS", status: "idle"})
	r.Refresh()

	entries := r.List()
	if len(entries) != 1 {
		t.Fatalf("roster entries = %+v, want the probed session", entries)
	}
	if entries[0].CapabilitiesKnown {
		t.Fatal("LiveEntry.CapabilitiesKnown = true, want false: the probe answered no capabilities")
	}
	if entries[0].Capabilities != (appwire.ThreadCapabilities{}) {
		t.Fatalf("LiveEntry.Capabilities = %+v, want the zero set while unknown", entries[0].Capabilities)
	}
}

// capabilityFlipProber answers probes that differ only in the capability
// set, the way a daemon's answer changes when a bit that folds daemon state
// (Clear and its blocked reason, Send and activity) flips while the status
// string itself holds still.
type capabilityFlipProber struct {
	base    ProbeResult
	flipped bool
}

func (p *capabilityFlipProber) Probe(rendezvous.Entry) ProbeResult {
	if !p.flipped {
		return p.base
	}
	flipped := p.base
	flipped.Capabilities = appwire.ThreadCapabilities{Send: true, SkillInput: true}
	return flipped
}

// TestRosterOnChangeFiresOnCapabilityOnlyChange pins the roster's change
// notification against the probe-carried capabilities: onChange invalidates
// the projections cached off the roster, and a capability-only delta must
// move the fingerprint exactly like a status change would, or those caches
// serve a stale answer until some other field happens to change.
func TestRosterOnChangeFiresOnCapabilityOnlyChange(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1004, Address: "127.0.0.1:50004"})
	prober := &capabilityFlipProber{base: ProbeResult{
		SessionID:         "01CAPS",
		Status:            "idle",
		OK:                true,
		CapabilitiesKnown: true,
		Capabilities:      appwire.ThreadCapabilities{Send: true, SkillInput: true, Queue: true},
	}}
	r := NewRoster(dir, prober)
	fired := 0
	r.SetOnChange(func() { fired++ })
	r.Refresh()
	if fired != 1 {
		t.Fatalf("the initial refresh fired onChange %d times, want 1", fired)
	}
	r.Refresh()
	if fired != 1 {
		t.Fatalf("a no-op refresh fired onChange: %d", fired)
	}
	prober.flipped = true
	r.Refresh()
	if fired != 2 {
		t.Fatal("a capability-only change must fire onChange: projections cached on the roster would otherwise keep the stale answer")
	}
}

// TestRosterReadSpawnedThreadPublishesCapabilities mirrors the status-flags
// variant: the caller's direct read of a freshly spawned daemon is the one
// path into the roster that does not go through the prober, and the
// capabilities beside the read's status must survive it too, or the
// pre-resume list row would fall back to the approximation for a daemon the
// hub just read an exact answer from.
func TestRosterReadSpawnedThreadPublishesCapabilities(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	entry := rendezvous.Entry{
		PID: 1001, SourceID: "local", Protocol: appwire.ProtocolVersion,
		Endpoint: "ws://127.0.0.1:50001/rpc", ThreadID: "01SPAWNED", SessionID: "01SPAWNED",
	}
	if _, err := r.ReadSpawnedThread(t.Context(), entry, func(context.Context) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: "01SPAWNED", SessionID: "01SPAWNED",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener: appwire.EvenerThread{Capabilities: probedCapabilities},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	live, ok := r.Find("01SPAWNED")
	if !ok {
		t.Fatal("the confirmed daemon was not published into the roster")
	}
	if !live.CapabilitiesKnown || live.Capabilities != probedCapabilities {
		t.Fatalf("published entry = %+v, want the capabilities the read carried", live)
	}
}
