package sshconn

import (
	"context"
	"errors"
	"io"
	"slices"
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
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}, nil); err != nil {
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
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example", KeyPath: "/keys/new"}, nil); err != nil {
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
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}, nil); err != nil {
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
	if err := m.UpdateHost(hostreg.Host{Name: "nope", SSH: "n.example"}, nil); !errors.Is(err, hostreg.ErrUnknownHost) {
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
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha.example"}, nil); err == nil {
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
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}, nil); err != nil {
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
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}, nil); err != nil {
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

// TestUpdateHostRetireHookRunsAfterTheSwap pins the hook's position in the gate
// hold: when it runs, the registry already holds the new entry, so a caller's
// per-identity retirement observes the swap it belongs to.
func TestUpdateHostRetireHookRunsAfterTheSwap(t *testing.T) {
	reg := testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example"})
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	var (
		called  bool
		saw     hostreg.Host
		present bool
	)
	m := newTestManager(t, reg, fr, Options{})
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}, func(hostreg.Host) {
		called = true
		saw, present = reg.Get("alpha")
	}); err != nil {
		t.Fatalf("UpdateHost = %v, want nil", err)
	}
	if !called {
		t.Fatal("the retire hook did not run on a successful update")
	}
	if !present || saw.SSH != "alpha2.example" {
		t.Fatalf("inside the hook the registry = %+v (present %v), want the new entry", saw, present)
	}
}

// TestUpdateHostRetireHookReceivesTheRetiredEntry pins the hook's payload
// contract: the hook is handed the entry the swap actually replaced — the
// pre-swap capture, with the pre-swap generation and the pre-edit fields — so a
// caller can retire the right identity's state by generation rather than guess
// it by timing. The ordering half is pinned here too: the retired identity's
// Detached is emitted before the hook, both inside the same gate hold.
func TestUpdateHostRetireHookReceivesTheRetiredEntry(t *testing.T) {
	reg := testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example", KeyPath: "/keys/old"})
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	before, ok := reg.Get("alpha")
	if !ok {
		t.Fatal("alpha not registered before the update")
	}
	var (
		mu      sync.Mutex
		order   []string
		retired hostreg.Host
	)
	m := newTestManager(t, reg, fr, Options{OnEvent: func(ev Event) {
		if ev.Kind == EventDetached {
			mu.Lock()
			order = append(order, "detached")
			mu.Unlock()
		}
	}})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example", KeyPath: "/keys/new"}, func(entry hostreg.Host) {
		mu.Lock()
		retired = entry
		order = append(order, "retire")
		mu.Unlock()
	}); err != nil {
		t.Fatalf("UpdateHost = %v, want nil", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if retired.Generation != before.Generation {
		t.Fatalf("retired generation = %d, want the pre-swap %d", retired.Generation, before.Generation)
	}
	if retired.SSH != "alpha.example" || retired.KeyPath != "/keys/old" {
		t.Fatalf("retired entry = %+v, want the pre-edit fields", retired)
	}
	after, ok := reg.Get("alpha")
	if !ok {
		t.Fatal("the update dropped the registry entry")
	}
	if retired.Equal(after) {
		t.Fatal("the hook received the post-swap entry, not the retired identity")
	}
	if !slices.Equal(order, []string{"detached", "retire"}) {
		t.Fatalf("event/hook order = %v, want [detached retire]", order)
	}
}

// TestUpdateHostRetireHookRunsAfterDetached pins the hook's order against the
// retired identity's own lifecycle event: the teardown emits Detached first, and
// the hook runs after it — both inside the same gate hold, deterministically, so
// no sleeps are needed to observe the order.
func TestUpdateHostRetireHookRunsAfterDetached(t *testing.T) {
	reg := testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example"})
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	var (
		mu    sync.Mutex
		order []string
	)
	m := newTestManager(t, reg, fr, Options{OnEvent: func(ev Event) {
		if ev.Kind == EventDetached {
			mu.Lock()
			order = append(order, "detached")
			mu.Unlock()
		}
	}})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}, func(hostreg.Host) {
		mu.Lock()
		order = append(order, "retire")
		mu.Unlock()
	}); err != nil {
		t.Fatalf("UpdateHost = %v, want nil", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(order, []string{"detached", "retire"}) {
		t.Fatalf("event/hook order = %v, want [detached retire]", order)
	}
}

// TestUpdateHostRetireHookNotRunOnRefusal pins the negative half of the hook
// contract: a refused update never runs it, so a caller cannot retire live
// per-identity state for a swap that did not happen.
func TestUpdateHostRetireHookNotRunOnRefusal(t *testing.T) {
	reg := testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example"})
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, reg, fr, Options{})
	called := 0
	hook := func(hostreg.Host) { called++ }
	// An unknown name: the registry's Update reports ErrUnknownHost before the
	// teardown or the hook.
	if err := m.UpdateHost(hostreg.Host{Name: "nope", SSH: "n.example"}, hook); !errors.Is(err, hostreg.ErrUnknownHost) {
		t.Fatalf("UpdateHost(unknown) = %v, want ErrUnknownHost", err)
	}
	// A known name with an entry the registry's own validation refuses: the swap
	// never happens, so the hook must not either.
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: ""}, hook); err == nil {
		t.Fatal("UpdateHost with an invalid entry = nil, want the registry's validation refusal")
	}
	if called != 0 {
		t.Fatalf("the retire hook ran %d times across refused updates, want 0", called)
	}
}

// TestUpdateHostAppearedEntryIsNotTornDownUnderTheEmptyName pins the teardown's
// target and the hook's negative contract when a directly driven registry
// inserts the name between the pre-swap capture and the swap: the capture is
// absent, the swap still succeeds, and there is no identity this call captured
// to key the teardown to or to hand the hook. The devDeployed sentinel under
// the empty name is state a teardown keyed to "" would sweep — and "" is never
// a host, so its survival proves the empty name was not the target. Pre-fix the
// teardown targeted the capture's empty name and the hook ran with a
// zero-generation entry, which would clear a caller's record without advancing
// its fence.
func TestUpdateHostAppearedEntryIsNotTornDownUnderTheEmptyName(t *testing.T) {
	reg := testRegistry(t)
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, reg, fr, Options{
		beforeUpdateHostSwap: func(name string) {
			// The directly driven insert: the name appears after the capture and
			// before the swap, so hadCaptured is false while Update succeeds.
			if err := reg.Add(hostreg.Host{Name: name, SSH: "late.example"}); err != nil {
				t.Fatalf("direct registry insert: %v", err)
			}
		},
	})
	m.mu.Lock()
	m.devDeployed[""] = true
	m.mu.Unlock()

	called := 0
	if err := m.UpdateHost(hostreg.Host{Name: "ghost", SSH: "ghost.example"}, func(hostreg.Host) {
		called++
	}); err != nil {
		t.Fatalf("UpdateHost = %v, want nil", err)
	}
	if called != 0 {
		t.Fatalf("the retire hook ran %d times with no pre-swap capture, want 0", called)
	}
	m.mu.Lock()
	_, emptySurvived := m.devDeployed[""]
	m.mu.Unlock()
	if !emptySurvived {
		t.Fatal("the teardown ran under the empty name, want it keyed to the gate key")
	}
	if after, ok := reg.Get("ghost"); !ok || after.SSH != "ghost.example" {
		t.Fatalf("registry after the update = %+v, want the swapped entry", after)
	}
}

// TestUpdateHostUnmapsTheChannelBeforeTheSwapIsVisible pins the order the gate
// hold must use: the retired channel is unmapped BEFORE the new generation is
// visible in the registry. A row build — host/list's and host/status's, which
// take no host gate — resolves a name through the registry and then looks the
// channel up by name alone (ClientIfAttached/ChannelIfAttached compare no
// registrations). If the swap landed first, such a reader could snapshot the
// new entry inside this window and pair it with the still-mapped channel of
// the retired identity, recording the retired host's handshake and facts under
// the new generation — above the fence the swap itself advances — so the new
// identity would render the retired host's server facts until its next attach.
// The beforeUpdateHostSwap seam is that window exactly: it runs inside the gate
// hold, after the teardown and before the registry swap.
func TestUpdateHostUnmapsTheChannelBeforeTheSwapIsVisible(t *testing.T) {
	reg := testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example"})
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	before, ok := reg.Get("alpha")
	if !ok {
		t.Fatal("alpha not registered before the update")
	}
	seamRan := false
	var (
		channelLive   bool
		channelLiveCh *Channel
		clientLive    bool
		entryAtSeam   hostreg.Host
		entryPresent  bool
	)
	var m *Manager
	m = newTestManager(t, reg, fr, Options{
		beforeUpdateHostSwap: func(name string) {
			seamRan = true
			// The gate-free reader's two lookups, taken while the gate is held.
			// Recorded rather than failed here: this seam runs inside the gate
			// hold, and a t.Fatalf would Goexit out of UpdateHost without
			// releasing the gate it holds, hanging the cleanup. The assertions
			// read these after UpdateHost returns.
			channelLiveCh, channelLive = m.ChannelIfAttached(name)
			_, clientLive = m.ClientIfAttached(name)
			// The registry must still hold the pre-edit identity: only the
			// teardown has run, not the swap.
			entryAtSeam, entryPresent = reg.Get(name)
		},
	})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !m.Attached("alpha") {
		t.Fatal("the fixture host is not attached")
	}
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: "alpha2.example"}, nil); err != nil {
		t.Fatalf("UpdateHost = %v, want nil", err)
	}
	if !seamRan {
		t.Fatal("beforeUpdateHostSwap did not run; the window assertion proved nothing")
	}
	if channelLive {
		t.Fatalf("ChannelIfAttached in the swap window = live channel %p, want false: the retired channel is still reachable", channelLiveCh)
	}
	if clientLive {
		t.Fatal("ClientIfAttached in the swap window = true, want false: the retired channel is still reachable")
	}
	if !entryPresent || !entryAtSeam.Equal(before) || entryAtSeam.Generation != before.Generation {
		t.Fatalf("registry in the swap window = %+v (present %v), want the pre-edit entry %+v", entryAtSeam, entryPresent, before)
	}
	after, ok := reg.Get("alpha")
	if !ok || after.SSH != "alpha2.example" {
		t.Fatalf("registry after the update = %+v, want the swapped entry", after)
	}
	if m.Attached("alpha") {
		t.Fatal("Attached after UpdateHost = true, want false")
	}
}

// TestUpdateHostRefusedEntryLeavesTheLiveChannelAttached pins the validate-first
// guard the teardown-before-swap order needs: a refused update must change
// nothing live, so the entry is validated before the channel is touched. With
// the teardown moved ahead of the swap, a validation that ran after it would
// have torn an attached host's channel down and then answered an error, leaving
// the caller told nothing changed while its live channel was gone.
func TestUpdateHostRefusedEntryLeavesTheLiveChannelAttached(t *testing.T) {
	m, events := detachHostFixture(t)
	if !m.Attached("alpha") {
		t.Fatal("Attached before the refused update = false, want true")
	}
	before, ok := m.reg.Get("alpha")
	if !ok {
		t.Fatal("alpha not registered before the refused update")
	}
	if err := m.UpdateHost(hostreg.Host{Name: "alpha", SSH: ""}, nil); !errors.Is(err, hostreg.ErrMissingSSH) {
		t.Fatalf("UpdateHost(invalid entry) = %v, want ErrMissingSSH", err)
	}
	if !m.Attached("alpha") {
		t.Fatal("Attached after the refused update = false, want true: a refusal must change nothing live")
	}
	if _, ok := m.ClientIfAttached("alpha"); !ok {
		t.Fatal("ClientIfAttached after the refused update = false, want the still-live channel")
	}
	if _, ok := m.ChannelIfAttached("alpha"); !ok {
		t.Fatal("ChannelIfAttached after the refused update = false, want the still-live channel")
	}
	after, ok := m.reg.Get("alpha")
	if !ok || !after.Equal(before) || after.Generation != before.Generation {
		t.Fatalf("registry after the refused update = %+v, want the unedited entry", after)
	}
	if kinds := detachHostKinds(events); detachHostCount(kinds, EventDetached) != 0 {
		t.Fatalf("the refused update emitted %v, want no Detached", kinds)
	}
}
