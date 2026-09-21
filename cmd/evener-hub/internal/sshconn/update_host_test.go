package sshconn

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// TestUpdateHostDropsTheChannel pins the attached-edit rule: the edit lands in
// the registry, the host's channel goes with the generation it was built from,
// and the announced Attached is paired with exactly one Detached.
func TestUpdateHostDropsTheChannel(t *testing.T) {
	m, events := detachHostFixture(t)
	if !m.Attached("alpha") {
		t.Fatal("Attached before UpdateHost = false, want true")
	}
	before, ok := m.reg.Get("alpha")
	if !ok {
		t.Fatal("alpha not registered before the update")
	}
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}); err != nil {
		t.Fatalf("UpdateHost = %v, want nil", err)
	}
	after, ok := m.reg.Get("alpha")
	if !ok {
		t.Fatal("the update dropped the registry entry")
	}
	if after.SSH != "alpha2.example" {
		t.Fatalf("entry = %+v, want the edited address", after)
	}
	if after.Generation <= before.Generation {
		t.Fatalf("generation = %d, want > %d", after.Generation, before.Generation)
	}
	if m.reg.SameRegistration("alpha", before) {
		t.Fatal("a pre-update capture still matches; the identity fence is open")
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after UpdateHost = true, want false: every update retires the channel")
	}
	if _, ok := m.ClientIfAttached("alpha"); ok {
		t.Fatal("ClientIfAttached after UpdateHost = true, want false")
	}
	waitForEvent(t, events, EventDetached)
	if rest := detachHostKinds(events); detachHostCount(rest, EventDetached) != 0 {
		t.Fatalf("extra Detached events after UpdateHost: %v", rest)
	}
}

// TestEnsureAfterUpdateDialsTheEditedEntry pins the half the attached-edit rule
// leaves the operator: the next attach dials the EDITED entry, never the
// retired one, so a following Connect brings the edited host up.
func TestEnsureAfterUpdateDialsTheEditedEntry(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", KeyPath: "/keys/old"}
	var mu sync.Mutex
	var dialed []string
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
			mu.Lock()
			dialed = append(dialed, strings.Join(argv, " "))
			mu.Unlock()
			return goodStartFn(t)(ctx, argv, stderr)
		},
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example", KeyPath: "/keys/new"}); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure after the update: %v", err)
	}
	if !m.Attached("alpha") {
		t.Fatal("the edited entry is not attached after a fresh Ensure")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 2 {
		t.Fatalf("dials = %v, want the initial attach and the edited entry's own", dialed)
	}
	if !strings.Contains(dialed[1], "alpha2.example") || !strings.Contains(dialed[1], "/keys/new") {
		t.Fatalf("post-update dial = %q, want the edited address and key", dialed[1])
	}
}

// TestUpdateHostDoesNotDial pins that an edit is not an attach: the update
// itself issues no ssh process call.
func TestUpdateHostDoesNotDial(t *testing.T) {
	var mu sync.Mutex
	var dialed []string
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
			mu.Lock()
			dialed = append(dialed, strings.Join(argv, " "))
			mu.Unlock()
			return goodStartFn(t)(ctx, argv, stderr)
		},
	}
	m := newTestManager(t, testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example"}), fr, Options{})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	mu.Lock()
	before := len(dialed)
	mu.Unlock()
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != before {
		t.Fatalf("the update dialed: %v", dialed[before:])
	}
}

// TestUpdateHostUnknownNameIsUnknownHost pins that update targets a live entry:
// an unknown name refuses with hostreg's own sentinel and touches nothing.
func TestUpdateHostUnknownNameIsUnknownHost(t *testing.T) {
	reg := testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example"})
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	events := make(chan Event, 64)
	m := newTestManager(t, reg, fr, Options{OnEvent: func(ev Event) { events <- ev }})
	before, _ := reg.Get("alpha")
	if err := m.UpdateHost(hostreg.Host{Name: "nope", SSH: "n.example"}); !errors.Is(err, hostreg.ErrUnknownHost) {
		t.Fatalf("UpdateHost(unknown) = %v, want ErrUnknownHost", err)
	}
	after, ok := reg.Get("alpha")
	if !ok || !after.Equal(before) || after.Generation != before.Generation {
		t.Fatalf("registry after the refusal = %+v, want alpha untouched", after)
	}
	if kinds := detachHostKinds(events); len(kinds) != 0 {
		t.Fatalf("a refused update emitted %v, want nothing", kinds)
	}
}

// TestUpdateHostNilRegistry mirrors AddHost and RemoveHost: a nil registry is an
// error rather than a silent success.
func TestUpdateHostNilRegistry(t *testing.T) {
	m := New(nil, Options{Runner: &fakeRunner{}})
	t.Cleanup(func() { _ = m.Close() })
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha.example"}); err == nil {
		t.Fatal("UpdateHost with no registry = nil, want an error")
	}
}

// TestUpdateHostRacesEnsureAtGate pins the notification/criterion-9 fence: an
// Ensure parked before the gate with a capture of the pre-edit entry must not
// publish a channel, because the update advanced the generation under the same
// gate. The gateHook parks the Ensure deterministically; the update completes
// in that window.
func TestUpdateHostRacesEnsureAtGate(t *testing.T) {
	reg := testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example"})
	gate := newGateHook()
	gate.arm()
	defer gate.open()
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	events := make(chan Event, 64)
	m := newTestManager(t, reg, fr, Options{
		OnEvent:        func(ev Event) { events <- ev },
		beforeHostGate: func(string) { gate.hook() },
	})
	type ensureResult struct {
		ch  *Channel
		err error
	}
	done := make(chan ensureResult, 1)
	go func() {
		ch, err := m.Ensure(context.Background(), "alpha")
		done <- ensureResult{ch, err}
	}()
	gate.wait(t, "the parked Ensure")
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	gate.open()
	r := <-done
	if r.ch != nil {
		t.Fatal("the parked Ensure published a channel captured from the pre-update entry")
	}
	if !errors.Is(r.err, ErrHostNotFound) {
		t.Fatalf("parked Ensure = %v, want ErrHostNotFound (the captured identity is gone)", r.err)
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after the refused attach = true, want false")
	}
	if kinds := detachHostKinds(events); detachHostCount(kinds, EventAttached) != 0 {
		t.Fatalf("the refused attach announced %v, want no Attached", kinds)
	}
}

// TestUpdateHostClearsPerHostCaches pins that the caches follow the retired
// identity, exactly as a removal's teardown leaves them: the edit's new identity
// starts clean rather than inheriting the old one's deploy marker, resolved
// executable, or pending restart.
func TestUpdateHostClearsPerHostCaches(t *testing.T) {
	m, events := detachHostFixture(t)
	m.markDevDeployed("alpha")
	m.setResolvedTarget("alpha", "/home/dev/.local/bin/evener")
	m.setPendingRestart("alpha", "evener hub --addr 127.0.0.1:9180", hubIdentity{})
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}); err != nil {
		t.Fatalf("UpdateHost = %v, want nil", err)
	}
	waitForEvent(t, events, EventDetached)
	if m.isDevDeployed("alpha") {
		t.Fatal("devDeployed survived UpdateHost")
	}
	if m.resolvedTarget("alpha") != "" {
		t.Fatal("resolvedTarget survived UpdateHost")
	}
	if m.pendingRestart("alpha") != (pendingRestartState{}) {
		t.Fatal("pendingRestart survived UpdateHost")
	}
}
