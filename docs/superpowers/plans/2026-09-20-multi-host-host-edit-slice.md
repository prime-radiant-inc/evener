# Host editing and the host schema surface — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make editing a remote host the supported path: one `evener/host/update` mutation that replaces a live sidecar entry durably and live, a wire shape that carries every mutable `HostConfig` field, and an Add/Edit dialog over all of them.

**Architecture:** Four layers, each with its own identity rule. `hostreg.Registry.Update` replaces an entry in place and stamps a fresh registry-wide generation, which is the identity fence every capture-compare consumer already reads. `sshconn.Manager.UpdateHost` performs the retire-the-channel teardown and the swap in one hold of the per-host gate — validating first, then tearing the channel down, then committing the swap — so live state never straddles two identities and a gate-free row build can never pair the new entry with the retired channel. The hub's `hubHostManager.Update` mirrors `Remove`'s commit/live/finish phases: validate before anything is written, persist durable-first with an in-place replace, swap live with the mutation mutex released, then reconcile the derived state (the attach record always; the source's rows and retention only when `roots` changed) and roll the file back if the live phase failed. The pane grows one Add/Edit dialog over the entry shape the wire now carries.

**Tech Stack:** Go (`appwire`, `internal/hostreg`, `internal/sshconn`, `internal/appsource`, `internal/hubcore`, `cmd/evener-hub`), React + TypeScript + zustand + vitest (frontend settings pane), and the repo's own AppWire generation (`make generate` → `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`).

**Spec:** `docs/superpowers/specs/2026-09-20-multi-host-host-edit-slice.md` (ready it alongside this plan: the plan implements §3–§7 and every criterion in §7 is a step below).

## Global Constraints

Every task's requirements implicitly include this section. Values are copied from the spec; each names where the spec records it.

- **No guards.** `mutationId`, `expectedGeneration`, `expectedIncarnationId` and the receipt store are NOT added (§6.1). Update matches slice 1's add/remove, which shipped without them.
- **No busy refusal.** Update waits for the per-host gate; §4's typed busy refusal arrives with the guards (§6.2).
- **No host-count cap.** The registry, config load, and this slice run no count check; the only 64-source limit is the navigation projection's (§6.5, §3.2). Do not add one.
- **Every update tears the channel down**, whether or not the edit changed a dial-relevant field. Reason: a supervisor's capture is generation-fenced, so a channel kept across an update could not be reconnected by the supervisor that owns it (§3.3, §6.10).
- **`name` is immutable.** It is the update's target only; nothing in the request can rename a host, and the edit dialog offers no name input (§3.1, §4).
- **`keyPath` is a slice-1 extension, not a schema field.** The wire carries it, this slice round-trips it, and the dialog keeps the Key path control; dropping it would zero a stored key path on every edit (§3.1, §6.9).
- **The wire says `address` where the schema and the record say `ssh`.** A refusal's `field` uses the same wire spelling as the dialog's inputs, so the two always agree (§5, §6.12).
- **No build/skew signal on the row.** The row's `hubVersion` is the controller's own build, so a marker built from it would compare a string with itself (§3.5, §6.11). Do not add one.
- **Params are nested `entry`; the update response is `{host: HostRow}`; refusal classes stay `InvalidParams`/`Conflict` as slice 1 shipped them (§6.4, §6.7, §6.8).**
- **Durable-first.** Validation runs before any write; the sidecar write precedes every live change; the commit phase ends before the mutex is released; the gate is released last (§3.4).
- **An edit never dials, deploys, attaches, or starts a session** (§4).
- **Bounded gates, CI is the suite of record.** Per task: `gofmt -l` empty on touched Go files, `go build ./...`, the task's targeted `go test` runs, and for frontend tasks `npx biome check --write` on touched files (run from `cmd/evener-hub/frontend`) plus `make test-web`. Also run `make lint-generated` after any `make generate`. Do NOT run `make test` for the whole repo; CI runs the full suite on the pull request.
- **Never `git add -A`.** Stage named paths. Commit on this lane's branch (`host-edit-slice`), never on `main`.

---

### Task 1: `hostreg.Registry.Update`

**Files:**
- Modify: `cmd/evener-hub/internal/hostreg/hostreg.go` (add `Update` beside `AddWithUpstreams`/`Remove`)
- Test: `cmd/evener-hub/internal/hostreg/update_test.go` (new)

**Interfaces:**
- Consumes: `hostreg.Normalize`, `validateEntry`, `ValidateName`, the `ErrUnknownHost`/`ErrInvalidName`/`ErrReservedName`/`ErrMissingSSH`/`ErrAmbiguousSSHUser`/`ErrEmptyRoot` sentinels, `Registry.edges`, `Registry.gen` — all existing.
- Produces: `func (r *Registry) Update(entry Host) error` — replaces `entry.Name`'s stored entry in place, assigns a fresh generation from the registry-wide counter, preserves the name's recorded upstream edges, runs no cap and no cycle check, and refuses (`ErrUnknownHost`) a name the registry does not hold. Task 2 and Task 5 call it.

- [ ] **Step 1: Write the failing tests**

Create `cmd/evener-hub/internal/hostreg/update_test.go`:

```go
package hostreg

import (
	"errors"
	"slices"
	"testing"
)

// TestRegistryUpdateReplacesInPlaceAndAdvancesGeneration pins the identity
// fence every capture-compare consumer reads: an update replaces the entry in
// place, stamps a strictly greater generation on each successive update, and
// makes SameRegistration refuse a capture taken before it.
func TestRegistryUpdateReplacesInPlaceAndAdvancesGeneration(t *testing.T) {
	r, err := New([]Host{{Name: "m4", SSH: "m4.example", Roots: []string{"/a"}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before, ok := r.Get("m4")
	if !ok {
		t.Fatal("m4 not registered after New")
	}
	if err := r.Update(Host{Name: "m4", SSH: "m4b.example", Roots: []string{"/a", "/b"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	first, ok := r.Get("m4")
	if !ok {
		t.Fatal("m4 gone after Update")
	}
	if first.SSH != "m4b.example" || !slices.Equal(first.Roots, []string{"/a", "/b"}) {
		t.Fatalf("entry = %+v, want the edited values", first)
	}
	if first.Generation <= before.Generation {
		t.Fatalf("generation = %d after updating %d, want a strictly greater one", first.Generation, before.Generation)
	}
	if r.SameRegistration("m4", before) {
		t.Fatal("a capture from before the update still matches; the fence is open")
	}
	if err := r.Update(Host{Name: "m4", SSH: "m4c.example"}); err != nil {
		t.Fatalf("second Update: %v", err)
	}
	second, ok := r.Get("m4")
	if !ok {
		t.Fatal("m4 gone after the second Update")
	}
	if second.Generation <= first.Generation {
		t.Fatalf("second update generation = %d, want > %d", second.Generation, first.Generation)
	}
	if r.SameRegistration("m4", first) {
		t.Fatal("a capture from the first update still matches the second's entry")
	}
	if got := len(r.All()); got != 1 {
		t.Fatalf("All() = %d hosts, want 1: an update replaces, never inserts", got)
	}
}

// TestRegistryUpdateRefusalsLeaveTheRegistryUntouched pins that every refusal
// the add path produces is a refusal here too, with nothing changed.
func TestRegistryUpdateRefusalsLeaveTheRegistryUntouched(t *testing.T) {
	r, err := New([]Host{{Name: "m4", SSH: "m4.example", KeyPath: "/keys/m4"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before, _ := r.Get("m4")
	for _, tc := range []struct {
		name  string
		entry Host
		want  error
	}{
		{"unknown name", Host{Name: "nope", SSH: "n.example"}, ErrUnknownHost},
		{"missing ssh", Host{Name: "m4", SSH: "   "}, ErrMissingSSH},
		{"invalid name", Host{Name: "bad name", SSH: "n.example"}, ErrInvalidName},
		{"reserved name", Host{Name: ReservedName, SSH: "n.example"}, ErrReservedName},
		{"empty root", Host{Name: "m4", SSH: "n.example", Roots: []string{"   "}}, ErrEmptyRoot},
		{"ambiguous user", Host{Name: "m4", SSH: "u@n.example", User: "bob"}, ErrAmbiguousSSHUser},
	} {
		if err := r.Update(tc.entry); !errors.Is(err, tc.want) {
			t.Errorf("Update(%s) = %v, want %v", tc.name, err, tc.want)
		}
	}
	after, ok := r.Get("m4")
	if !ok {
		t.Fatal("a refused update dropped the entry")
	}
	if !after.Equal(before) || after.Generation != before.Generation {
		t.Fatalf("entry after the refusals = %+v, want %+v untouched", after, before)
	}
}

// TestRegistryUpdatePreservesUpstreamEdges pins that an edit keeps what the
// attach handshake learned about the host's position in the graph: the edge a
// cycle would close through survives the update.
func TestRegistryUpdatePreservesUpstreamEdges(t *testing.T) {
	r, err := New([]Host{{Name: "a", SSH: "a.example"}, {Name: "b", SSH: "b.example"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// b is downstream of a, so giving a b as an upstream closes a cycle.
	if err := r.SetUpstreams("b", []string{"a"}); err != nil {
		t.Fatalf("SetUpstreams(b): %v", err)
	}
	if err := r.SetUpstreams("a", []string{"b"}); !errors.Is(err, ErrHostCycle) {
		t.Fatalf("SetUpstreams(a) = %v, want ErrHostCycle", err)
	}
	if err := r.Update(Host{Name: "b", SSH: "b2.example"}); err != nil {
		t.Fatalf("Update(b): %v", err)
	}
	if err := r.SetUpstreams("a", []string{"b"}); !errors.Is(err, ErrHostCycle) {
		t.Fatalf("after the update SetUpstreams(a) = %v, want ErrHostCycle: the update dropped b's upstream edge", err)
	}
}

// TestRegistryUpdateStoresWhatItValidated pins the normalize-then-store
// discipline: a padded entry is stored trimmed, so the value the registry
// validated is the value every consumer reads.
func TestRegistryUpdateStoresWhatItValidated(t *testing.T) {
	r, err := New([]Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Update(Host{
		Name:  "  m4  ",
		SSH:   "  m4b.example  ",
		User:  "  operator  ",
		Roots: []string{"  /a  ", " /b "},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, ok := r.Get("m4")
	if !ok {
		t.Fatal("a padded name did not find its host")
	}
	want := Normalize(Host{Name: "m4", SSH: "m4b.example", User: "operator", Roots: []string{"/a", "/b"}})
	if !got.Equal(want) {
		t.Fatalf("stored entry = %+v, want %+v", got, want)
	}
	if len(got.Roots) > 0 {
		got.Roots[0] = "/mutated"
		again, _ := r.Get("m4")
		if again.Roots[0] != "/a" {
			t.Fatal("Get handed out a slice aliased by registry state")
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hostreg/ -run 'TestRegistryUpdate' -v`
Expected: FAIL — `r.Update undefined (type *Registry has no field or method Update)`.

- [ ] **Step 3: Implement `Update`**

Add to `cmd/evener-hub/internal/hostreg/hostreg.go`, directly after `AddWithUpstreams`:

```go
// Update replaces the entry registered under entry.Name in place and stamps it
// with a fresh generation from the registry-wide counter — the identity fence
// every capture-compare consumer reads (the SSH manager's pre-publish rechecks
// and the hub's row fence), so a capture taken before an update stops matching
// once the update lands.
//
// It normalizes and validates exactly as an add does — Normalize, then the same
// validateEntry — so an update can never store what an add would refuse, and a
// refusal leaves the registry untouched. It runs no host-count cap: the
// registry has none, config load has none, and the slice that owns the cap
// (the design document's [03] follow-up) is where it arrives.
//
// Unlike AddWithUpstreams it never inserts: update targets a live entry, so a
// name the registry does not hold is ErrUnknownHost rather than a create. The
// name's upstream edges are preserved verbatim, which is why the cycle check is
// not rerun — the edges are unchanged, and the graph they describe is the one
// that was already acyclic.
func (r *Registry) Update(entry Host) error {
	entry = Normalize(entry)
	if err := validateEntry(entry); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.hosts[entry.Name]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownHost, entry.Name)
	}
	// The generation is assigned under the lock from the registry-wide counter,
	// exactly as AddWithUpstreams does, so an update is as much a new identity
	// as a remove/re-add: no generation a capture can hold is ever reused.
	entry.Generation = r.gen + 1
	r.gen = entry.Generation
	r.hosts[entry.Name] = entry
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hostreg/ -v`
Expected: PASS (every pre-existing hostreg test included — this task changes no existing behavior).

- [ ] **Step 5: Commit**

```bash
gofmt -l cmd/evener-hub/internal/hostreg/ # expect no output
go build ./...
git add cmd/evener-hub/internal/hostreg/hostreg.go cmd/evener-hub/internal/hostreg/update_test.go
git commit -m "feat(hosts): add Registry.Update, the identity-fenced in-place entry swap"
```

---

### Task 2: `sshconn.Manager.UpdateHost`

**Files:**
- Modify: `cmd/evener-hub/internal/sshconn/manager.go` (add `UpdateHost` beside `AddHost`/`RemoveHost`)
- Test: `cmd/evener-hub/internal/sshconn/update_host_test.go` (new)

**Interfaces:**
- Consumes: `hostreg.Registry.Update` (Task 1), `Manager.hostLock`/`releaseHostLock`, `Manager.teardownHostChannel`, `Manager.reg` — all existing.
- Produces: `func (m *Manager) UpdateHost(entry hostreg.Host, onRetire func(retired hostreg.Host)) error` — one hold of the per-host gate, in order: validate the entry, tear the pre-swap identity's channel down, commit the registry swap; `onRetire`, when non-nil, runs inside that same hold, after the teardown and the swap, receiving the entry the swap retired (its generation included) so callers retire per-identity state by generation; never dials; a nil registry is an error. Task 5's live phase calls it.

- [ ] **Step 1: Write the failing tests**

Create `cmd/evener-hub/internal/sshconn/update_host_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/evener-hub/internal/sshconn/ -run 'TestUpdateHost|TestEnsureAfterUpdateDials' -v`
Expected: FAIL — `m.UpdateHost undefined (type *Manager has no field or method UpdateHost)`.

- [ ] **Step 3: Implement `UpdateHost`**

Add to `cmd/evener-hub/internal/sshconn/manager.go`, directly after `AddHost`:

```go
// UpdateHost replaces name's registry entry with entry and tears the host's
// channel down as one atomic step, under the per-host gate. Inside the one
// hold the order is fixed: validate, then tear the retired channel down, then
// swap the registry, then release.
//
// The teardown comes BEFORE the swap because a gate-free reader must never pair
// the new entry with the retired channel. The row build behind host/list and
// host/status takes no host gate: it snapshots the registry and then resolves
// the channel by name alone (ClientIfAttached/ChannelIfAttached compare no
// registrations). If the swap landed first, such a reader could take the new
// entry inside that window and pair it with the still-mapped channel of the
// identity this call is retiring, record the retired host's handshake and facts
// under the new generation — which is above the generation fence the swap
// itself advances — and render the retired host's server facts for the new
// identity until the next successful attach. Unmapping the channel before the
// new generation becomes visible makes that pairing impossible. The reverse
// window, the old entry observed with no channel, is harmless: it can only make
// a row render offline, and the retire leaves that clean.
//
// Validation comes before the teardown because the caller's contract is that an
// error from this method means nothing live changed. hostreg.ValidateEntry is
// the side-effect-free half of the same Normalize+validateEntry pass
// Registry.Update runs, so checking it here lets the teardown stay
// unconditional — a shape refusal returns before the channel is touched.
//
// Every update tears the channel down, whether or not the edit changed a field
// the dial reads. The reason is the fence, not the dial: a supervisor captures
// the entry it supervises when it starts, and reconnectOnce refuses to publish a
// replacement once SameRegistration no longer matches that capture — so a
// channel kept across an update that advanced the generation could never be
// reconnected by the supervisor that owns it, and the host would go dark on the
// next link drop with nothing to bring it back. Retiring the channel with the
// identity keeps one rule instead of a rebind path.
//
// It never dials, deploys, or attaches. An unknown name is hostreg's own
// ErrUnknownHost, and a nil registry is an error rather than a silent no-op,
// mirroring AddHost and RemoveHost: an update that committed nothing must not
// report success.
//
// onRetire, when non-nil, runs while the per-host gate is still held, in the
// same hold as the swap: after the registry already holds the new entry and the
// retired identity's teardown (its Detached included) has been emitted, and
// immediately before the gate is released. That placement is the contract, not
// an implementation detail. Every lifecycle event is delivered synchronously
// with the gate held, so a concurrent attach can only start for the new
// generation once the gate is free; a caller's per-identity state retired from
// this hook therefore cannot have a new-generation event interleave between the
// swap and the retirement and be erased by it. The hook receives the entry the
// swap actually retired — the pre-swap capture, its Generation included — so a
// caller retires per-identity state by generation rather than by timing, which
// is what makes the retirement immune to a late write from a row captured
// before the swap. The hook must not call back into Manager — the gate is
// non-reentrant — and must not take a lock another goroutine may hold while
// parked on this gate.
func (m *Manager) UpdateHost(entry hostreg.Host, onRetire func(retired hostreg.Host)) error {
	if m.reg == nil {
		return errors.New("sshconn: UpdateHost with no registry")
	}
	// Trimmed for the lock key exactly as AddHost trims, so a padded spelling
	// takes the same host gate as its canonical name.
	name := strings.TrimSpace(entry.Name)
	lock := m.hostLock(name)
	defer m.releaseHostLock(name)
	lock.Lock()
	// The identity this call retires is resolved under the gate, and so is the
	// swap: a remove/re-add that took the name while this call waited is not
	// this call's to tear down, and the teardown below is scoped to the entry
	// the update actually replaced. Update refuses an unknown name, so the
	// capture is present whenever the swap succeeded; hadCaptured keeps
	// teardownHostChannel's own contract explicit.
	before, hadCaptured := m.reg.Get(name)
	if err := hostreg.ValidateEntry(entry); err != nil {
		lock.Unlock()
		return err
	}
	teardownName := name
	if hadCaptured {
		teardownName = before.Name
	}
	ch := m.teardownHostChannel(teardownName, before, hadCaptured)
	if err := m.reg.Update(entry); err != nil {
		lock.Unlock()
		if ch != nil {
			_ = ch.Close()
		}
		return err
	}
	// The caller's retirement runs under the gate, in the same hold as the swap,
	// so no new-identity lifecycle event can interleave before it: an attach
	// cannot acquire the gate until it is released below. It receives the entry
	// the swap replaced, so the caller retires by identity, not by timing.
	if onRetire != nil && hadCaptured {
		onRetire(before)
	}
	lock.Unlock()
	// The reap runs after the lock is released, exactly as DetachHost's and
	// RemoveHost's do: Channel.Close blocks on the ssh child's exit.
	if ch != nil {
		_ = ch.Close()
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/evener-hub/internal/sshconn/ -v`
Expected: PASS, including every pre-existing `RemoveHost`/`DetachHost`/reconnect test — this task adds no fence and changes no shared path.

- [ ] **Step 5: Commit**

```bash
gofmt -l cmd/evener-hub/internal/sshconn/ # expect no output
go build ./...
git add cmd/evener-hub/internal/sshconn/manager.go cmd/evener-hub/internal/sshconn/update_host_test.go
git commit -m "feat(hosts): add Manager.UpdateHost, the gate-held swap-and-retire step"
```

---

### Task 3: The wire reshape (`entry`, the update method, the row's entry fields)

**Files:**
- Modify: `appwire/types.go` (host wire block, lines ~3974–4043)
- Modify: `appwire/protocol.go` (host method table, lines ~220–223)
- Modify: `cmd/evener-hub/app_host_manage.go` (`Add`, and `hostRow`)
- Modify: `cmd/evener-hub/frontend/src/stores/hosts.ts` (`add`)
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx` (the add call site)
- Regenerate (never hand-edit): `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`
- Test: `cmd/evener-hub/app_host_manage_test.go`, `cmd/evener-hub/app_host_manage_round12_test.go`, `cmd/evener-hub/app_host_manage_round15_test.go`, `cmd/evener-hub/app_host_manage_round16_test.go`, `cmd/evener-hub/app_host_manage_wiring_test.go` (every `HostAddParams` literal), `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.test.tsx` (the add payload assertion)

**Interfaces:**
- Consumes: nothing from Tasks 1–2 (this task is wire-only).
- Produces, all in package `appwire`:
  - `type HostEntry struct { Name, Address, User, KeyPath, EvenerPath, ConfigPath, Addr string; Roots []string }` with json tags `name,omitempty`, `address`, `user,omitempty`, `keyPath,omitempty`, `evenerPath,omitempty`, `configPath,omitempty`, `addr,omitempty`, `roots,omitempty`.
  - `type HostAddParams struct { Entry HostEntry \`json:"entry"\` }`
  - `type HostUpdateParams struct { Name string \`json:"name"\`; Entry HostEntry \`json:"entry"\` }`
  - `type HostUpdateResponse struct { Host HostRow \`json:"host"\` }`
  - `MethodEvenerHostUpdate = "evener/host/update"`
  - `HostRow` grows: `User`, `EvenerPath`, `ConfigPath`, `Addr` (all `string`, `omitempty`) and `Roots []string` (`json:"roots,omitempty"`).
  - Frontend: `hostsStore.add(entry: HostEntry)`.
  - Tasks 4, 5, 7 build on all of these.

- [ ] **Step 1: Write the failing test for the add path's new fields**

Add to `cmd/evener-hub/app_host_manage_test.go`:

```go
// TestHostManageAddCarriesEveryEntryField pins the apply half of the wire
// reshape: the add request's entry is stored whole — the seven HostConfig
// fields under the wire's spellings plus the slice-1 key path — and the row
// reports them back, so an edit dialog prefilled from a row sees what the host
// actually is.
func TestHostManageAddCarriesEveryEntryField(t *testing.T) {
	m := testHostManager(nil, nil)
	entry := appwire.HostEntry{
		Name:       "m4",
		Address:    "m4.example",
		User:       "operator",
		KeyPath:    "/keys/m4",
		EvenerPath: "/opt/evener",
		ConfigPath: "/etc/evener/hub.toml",
		Addr:       "127.0.0.1:9180",
		Roots:      []string{"/srv/one"},
	}
	row, err := m.Add(context.Background(), appwire.HostAddParams{Entry: entry})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, tc := range []struct {
		field string
		got   string
		want  string
	}{
		{"address", row.Address, entry.Address},
		{"user", row.User, entry.User},
		{"keyPath", row.KeyPath, entry.KeyPath},
		{"evenerPath", row.EvenerPath, entry.EvenerPath},
		{"configPath", row.ConfigPath, entry.ConfigPath},
		{"addr", row.Addr, entry.Addr},
	} {
		if tc.got != tc.want {
			t.Errorf("row.%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	if !slices.Equal(row.Roots, entry.Roots) {
		t.Errorf("row.Roots = %v, want %v", row.Roots, entry.Roots)
	}
	stored, ok := m.cfg.hosts.Get("m4")
	if !ok {
		t.Fatal("the added host is not in the registry")
	}
	if stored.SSH != entry.Address || stored.User != entry.User || stored.KeyPath != entry.KeyPath ||
		stored.EvenerPath != entry.EvenerPath || stored.ConfigPath != entry.ConfigPath ||
		stored.Addr != entry.Addr || !slices.Equal(stored.Roots, entry.Roots) {
		t.Fatalf("stored entry = %+v, want the request's entry", stored)
	}
}
```

(`slices` is already imported by `app_host_manage_test.go`; if not, add it.)

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/evener-hub/ -run TestHostManageAddCarriesEveryEntryField`
Expected: FAIL to compile — `unknown field Entry in struct literal of type appwire.HostAddParams` and `row.User undefined`.

- [ ] **Step 3: Reshape the wire types**

In `appwire/types.go`, replace the `HostAddParams` block and extend `HostRow`:

```go
// HostEntry is one host's effective configuration as a mutation carries it
// (component 08 slice 2): the six mutable HostConfig fields under slice 1's
// wire spellings — Address is the schema's `ssh`, EvenerPath the schema's
// `evener_path`, ConfigPath the schema's `config_path` — plus KeyPath, the one
// field slice 1 added that hub.toml's schema has no spelling for (the dial needs
// a key path, and hostreg's Host says a file-declared host never sets one).
//
// Name is carried by the ADD entry, which has no other place to put the new
// host's name. The update entry omits it: a rename is not a thing this wire
// offers, and the update's target is HostUpdateParams.Name. Both paths accept
// the same field set, so the dialog's inputs and a refusal's field name are the
// same strings on both sides.
type HostEntry struct {
	Name       string   `json:"name,omitempty"`
	Address    string   `json:"address"`
	User       string   `json:"user,omitempty"`
	KeyPath    string   `json:"keyPath,omitempty"`
	EvenerPath string   `json:"evenerPath,omitempty"`
	ConfigPath string   `json:"configPath,omitempty"`
	Addr       string   `json:"addr,omitempty"`
	Roots      []string `json:"roots,omitempty"`
}

// HostAddParams is the evener/host/add payload (component 08 slice 1, reshaped
// by slice 2): one sidecar host entry. The nested entry is the design record's
// own shape, so the guard fields the pipeline slice adds land on a wire this
// slice already matches instead of reshaping it a second time. The handler
// validates exactly like hub.toml loading and refuses a name hub.toml or the
// live set already holds.
type HostAddParams struct {
	Entry HostEntry `json:"entry"`
}

// HostUpdateParams is the evener/host/update payload (component 08 slice 2): the
// name of the live sidecar entry to edit, and the entry that replaces it. Name
// is the immutable target — it identifies which host to edit, and nothing in the
// request can rename one. hub.toml-declared names are refused (edit the file);
// unknown names are InvalidParams.
type HostUpdateParams struct {
	Name  string    `json:"name"`
	Entry HostEntry `json:"entry"`
}

// HostUpdateResponse is evener/host/update's result: the updated row, which the
// dialog's caller re-reads like every other mutation's result. An edit never
// dials, so the row's live state is whatever the teardown left it.
type HostUpdateResponse struct {
	Host HostRow `json:"host"`
}
```

and extend `HostRow` (keep the existing fields and their order; add the entry's):

```go
type HostRow struct {
	Name          string   `json:"name"`
	Address       string   `json:"address,omitempty"`
	User          string   `json:"user,omitempty"`
	KeyPath       string   `json:"keyPath,omitempty"`
	EvenerPath    string   `json:"evenerPath,omitempty"`
	ConfigPath    string   `json:"configPath,omitempty"`
	Addr          string   `json:"addr,omitempty"`
	Roots         []string `json:"roots,omitempty"`
	Origin        string   `json:"origin"`
	Attached      bool     `json:"attached"`
	ServerName    string   `json:"serverName,omitempty"`
	ServerVersion string   `json:"serverVersion,omitempty"`
	HubVersion    string   `json:"hubVersion,omitempty"`
	OS            string   `json:"os,omitempty"`
	Arch          string   `json:"arch,omitempty"`
	LastAttachErr string   `json:"lastAttachError,omitempty"`
	MidAttach     bool     `json:"midAttach"`
	Removed       bool     `json:"removed"`
}
```

Update `HostRow`'s doc comment: the row carries the effective entry plus the live state it already carries, so a dialog prefills from a row and `list`/`status` stay the one source of truth for what a host currently is; the optionals stay absent when unknown.

In `appwire/types.go`'s method-name block, after `MethodEvenerHostRemove`:

```go
	// MethodEvenerHostUpdate edits one live sidecar host entry in place: every
	// field except the name is mutable, the name is the request's target, and
	// the entry's advancement of the registry generation retires the host's
	// channel. hub.toml-declared names are refused (edit the file). See
	// HostUpdateParams.
	MethodEvenerHostUpdate = "evener/host/update"
```

In `appwire/protocol.go`, after the `MethodEvenerHostRemove` row:

```go
	{MethodEvenerHostUpdate, HostUpdateParams{}, HostUpdateResponse{}, ScopeHub, "Edits one live sidecar host entry in place (every field but the name; the name is the target) and retires the host's channel with the identity it replaced; hub.toml-declared names are refused."},
```

and rewrite the `MethodEvenerHostAdd` row's summary to match the nested shape:

```go
	{MethodEvenerHostAdd, HostAddParams{}, HostRow{}, ScopeHub, "Registers one sidecar host entry (its full entry: name, ssh address, user, key path, and the host's paths and roots); validates like hub.toml loading and refuses a name hub.toml or the live set already holds."},
```

- [ ] **Step 4: Regenerate the client and the protocol doc**

Run: `make generate`
Then: `git status --short` — the regenerated `appwire-client/typescript/types.gen.ts` and `docs/appwire-protocol.md` must show up.
Run: `make lint-generated` — must pass (it fails when a generated output is stale or uncommitted).

- [ ] **Step 5: Update the hub's add path**

In `cmd/evener-hub/app_host_manage.go`, replace `Add`'s first lines (through the `ValidateEntry` call) with:

```go
func (m *hubHostManager) Add(ctx context.Context, params appwire.HostAddParams) (appwire.HostRow, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostRow{}, err
	}
	// The wire's entry is the whole configured host, so the add path stores what
	// the dialog collected instead of the three fields slice 1 carried: the
	// entry is normalized and validated as one record, exactly as hub.toml
	// loading does.
	entry := hostreg.Normalize(hostreg.Host{
		Name:       params.Entry.Name,
		SSH:        params.Entry.Address,
		User:       params.Entry.User,
		KeyPath:    params.Entry.KeyPath,
		EvenerPath: params.Entry.EvenerPath,
		ConfigPath: params.Entry.ConfigPath,
		Addr:       params.Entry.Addr,
		Roots:      params.Entry.Roots,
	})
	// Validate without inserting: hostreg's own entry validation runs the
	// exact add-time checks (name grammar, reserved name, ssh destination,
	// user/ssh agreement, non-empty roots) over this one entry without touching
	// live state, so nothing is exposed before the durable save below.
	if err := hostreg.ValidateEntry(entry); err != nil {
		return appwire.HostRow{}, appwire.InvalidParams(fmt.Sprintf("host %q: %v", entry.Name, err))
	}
```

The rest of `Add` is unchanged: `hostRemovingConflict`/duplicate checks, `state.remove`, `saveSidecar`, `addHostToRegistry`, `sidecar.add`, `registerSource`, `hostRow`. Delete the three now-unused locals (`name`, `address`, `keyPath`) and use `entry.Name` in the conflict/duplicate messages.

In `hostRow`, fill the entry's fields:

```go
	row := appwire.HostRow{
		Name:       host.Name,
		Address:    host.SSH,
		User:       host.User,
		KeyPath:    host.KeyPath,
		EvenerPath: host.EvenerPath,
		ConfigPath: host.ConfigPath,
		Addr:       host.Addr,
		Roots:      slices.Clone(host.Roots),
		Origin:     origin,
	}
```

(`cmd/evener-hub/app_host_manage.go` does not import `slices` yet — add it.)

- [ ] **Step 6: Move every `HostAddParams` literal in the hub package to the nested shape**

The literals are flat (`{Name: x, Address: y}`). With no nested braces inside a prefixed literal, one mechanical pass plus `gofmt` converts almost all of them:

```bash
cd <worktree root>
perl -0pi -e 's/appwire\.HostAddParams\{([^{}]*)\}/appwire.HostAddParams{Entry: appwire.HostEntry{$1}}/gs' \
  cmd/evener-hub/app_host_manage_test.go \
  cmd/evener-hub/app_host_manage_round12_test.go \
  cmd/evener-hub/app_host_manage_round15_test.go \
  cmd/evener-hub/app_host_manage_round16_test.go \
  cmd/evener-hub/app_host_manage_wiring_test.go
gofmt -w cmd/evener-hub/app_host_manage_test.go cmd/evener-hub/app_host_manage_round12_test.go cmd/evener-hub/app_host_manage_round15_test.go cmd/evener-hub/app_host_manage_round16_test.go cmd/evener-hub/app_host_manage_wiring_test.go
```

Two sites are slice literals whose ELEMENT literals carry no `appwire.` prefix, so the pass above cannot reach them and they must be converted by hand: the validation table in `TestHostManageAddValidation` (about line 55) and the host loop in `startParkedRemoval` (about line 1784). Convert each element to the nested shape:

```go
	for _, params := range []appwire.HostAddParams{
		{Entry: appwire.HostEntry{Name: "", Address: "h.example"}},
		{Entry: appwire.HostEntry{Name: "   ", Address: "h.example"}},
		...
	} {
```

and, in `startParkedRemoval`:

```go
	for _, host := range []appwire.HostAddParams{
		{Entry: appwire.HostEntry{Name: "keep", Address: "keep.example"}},
		{Entry: appwire.HostEntry{Name: name, Address: name + ".example"}},
	} {
```

Then let the compiler find every remaining site; an unconverted literal fails as `unknown field Address in struct literal of type appwire.HostAddParams`, so nothing can be silently missed:

```bash
go vet ./cmd/evener-hub/
go test ./cmd/evener-hub/ -run xxxNoSuchTest   # compiles the package's test binary
```

Fix each reported site by hand until both commands are clean.

- [ ] **Step 7: Run the hub package tests**

Run: `go test ./cmd/evener-hub/ -run 'TestHostManage|TestHostSidecar' -v`
Expected: PASS. If a wiring test compares a whole `HostRow` with `reflect.DeepEqual` or `==`, its expectation needs the entry's new fields (`User`, `EvenerPath`, `ConfigPath`, `Addr`, `Roots`); the failure names the assertion. Fix the expectation, not the row.

- [ ] **Step 8: Move the frontend's add call to the nested shape**

In `cmd/evener-hub/frontend/src/stores/hosts.ts`:

```ts
import type { HostEntry, HostRow } from "@evener/appwire-client";
```

```ts
  add: (entry: HostEntry) => Promise<HostRow>;
```

```ts
  add: async (entry) => {
    // The wire carries one entry object (component 08 slice 2's shape, the
    // design record's own), so the store passes what the dialog collected
    // straight through rather than picking three fields out of it.
    const row = await requireClient().request("evener/host/add", { entry });
    // Re-read quietly rather than appending: the server owns ordering and
    // the row's attached state, and the list read is cheap and never dials.
    await reReadAfterMutation();
    return row;
  },
```

In `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx`, the existing three inputs become one entry:

```tsx
      await hostsStore.getState().add({ name: name.trim(), address: address.trim(), keyPath: keyPath.trim() });
```

(This line already has this shape — the store's parameter type changed under it, so no edit is needed here unless the compiler says otherwise. Task 7 replaces this dialog wholesale.)

In `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.test.tsx`, the add-payload assertion becomes:

```ts
  expect(fake.calls.find((c) => c.method === "evener/host/add")?.params).toMatchObject({
    entry: { name: "gamma", address: "g.example", keyPath: "/keys/g" },
  });
```

- [ ] **Step 9: Run the frontend gates**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/stores/hosts.ts src/stores/hosts.test.ts src/panes/settings/sections/hosts.tsx src/panes/settings/sections/hosts.test.tsx
cd <worktree root>
make test-web
```

Expected: PASS.

- [ ] **Step 10: Commit**

```bash
gofmt -l appwire cmd/evener-hub # expect no output
go build ./...
git add appwire/types.go appwire/protocol.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md \
  cmd/evener-hub/app_host_manage.go cmd/evener-hub/app_host_manage_test.go cmd/evener-hub/app_host_manage_round12_test.go \
  cmd/evener-hub/app_host_manage_round15_test.go cmd/evener-hub/app_host_manage_round16_test.go cmd/evener-hub/app_host_manage_wiring_test.go \
  cmd/evener-hub/frontend/src/stores/hosts.ts cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx \
  cmd/evener-hub/frontend/src/panes/settings/sections/hosts.test.tsx
git commit -m "feat(hosts): carry the whole host entry on the wire and add evener/host/update"
```

---

### Task 4: A validation refusal names the input it blames

**Files:**
- Modify: `appwire/errors.go` (the `ErrorInfo` block, plus a constructor and its data type)
- Modify: `cmd/evener-hub/app_host_manage.go` (the add path's validation refusal)
- Modify: `appwire-client/typescript/errors.ts` (the client's reader)
- Test: `appwire/errors_test.go`, `cmd/evener-hub/app_host_manage_test.go`, `appwire-client/typescript/errors.test.ts`
- Test (binding): `appwire-client/typescript/errors.test.ts` reads `appwire/errors.go` and `appwire/types.go` with `?raw`, the way it already binds the other discriminators.

**Interfaces:**
- Consumes: `hostreg.ErrMissingSSH`/`ErrAmbiguousSSHUser`/`ErrEmptyRoot`/`ErrInvalidName`/`ErrReservedName`; `HostEntry` (Task 3).
- Produces:
  - `appwire.ErrorInvalidHostField ErrorInfo = "invalidHostField"`
  - `type HostFieldErrorData struct { ErrorData; Field string \`json:"field,omitempty"\` }`
  - `func InvalidHostField(field, message string) WireError`
  - hub: `func hostEntryField(err error) string` and `func hostValidationRefusal(name string, err error) error` — Task 5's update path calls the latter.
  - TS: `export const ErrorInvalidHostField = "invalidHostField"` and `export function hostFieldError(error: unknown): string | undefined` — Task 7's dialog uses it.

- [ ] **Step 1: Write the failing Go test**

Add to `appwire/errors_test.go`:

```go
// TestInvalidHostFieldCarriesTheBlamedInput pins the shape a dialog reads: the
// standard validation code and prose, with evenerErrorInfo naming the refusal
// kind and data.field naming the input in the wire's own spelling.
func TestInvalidHostFieldCarriesTheBlamedInput(t *testing.T) {
	err := InvalidHostField("address", `host "m4": missing ssh destination`)
	if err.Code != CodeInvalidParams {
		t.Fatalf("Code = %d, want CodeInvalidParams", err.Code)
	}
	data, ok := err.Data.(HostFieldErrorData)
	if !ok {
		t.Fatalf("Data = %T, want HostFieldErrorData", err.Data)
	}
	if data.EvenerErrorInfo != ErrorInvalidHostField || data.Field != "address" {
		t.Fatalf("data = %+v, want the invalidHostField discriminant and field address", data)
	}
	raw, err2 := json.Marshal(err)
	if err2 != nil {
		t.Fatalf("Marshal: %v", err2)
	}
	for _, want := range []string{`"evenerErrorInfo":"invalidHostField"`, `"field":"address"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("wire error = %s, want it to contain %s", raw, want)
		}
	}
	// A refusal that blames the entry as a whole carries no field at all, so a
	// client never looks for an input that does not exist.
	whole := InvalidHostField("", "host cycle")
	rawWhole, err3 := json.Marshal(whole)
	if err3 != nil {
		t.Fatalf("Marshal: %v", err3)
	}
	if strings.Contains(string(rawWhole), `"field"`) {
		t.Fatalf("wire error = %s, want no field for a form-level refusal", rawWhole)
	}
}
```

(`appwire/errors_test.go` already imports `encoding/json` and `strings`; add them if not.)

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./appwire/ -run TestInvalidHostFieldCarriesTheBlamedInput`
Expected: FAIL to compile — `undefined: InvalidHostField`.

- [ ] **Step 3: Add the error data and constructor**

In `appwire/errors.go`, in the `ErrorInfo` const block:

```go
	// ErrorInvalidHostField marks a host mutation's validation refusal, whose
	// data names the input that failed (HostFieldErrorData.Field, spelled the
	// way HostEntry's json tags spell it). It shares CodeInvalidParams with
	// plain validation refusals, so a client that wants to place a message on
	// an input must match this discriminant, never the code.
	ErrorInvalidHostField ErrorInfo = "invalidHostField"
```

and beside `ErrorData`:

```go
// HostFieldErrorData is a host mutation's validation-refusal data (component 08
// slice 2): the standard ErrorData plus the input the refusal blames, spelled
// the way the wire names that input (HostEntry's json tags) so a dialog places
// the message without parsing prose. A refusal that blames the entry as a
// whole — a cycle — leaves Field empty and the client shows it form-level. Same
// composition as LifecycleErrorData: the embedded ErrorData keeps every
// consumer that only reads evenerErrorInfo working.
type HostFieldErrorData struct {
	ErrorData
	Field string `json:"field,omitempty"`
}

// InvalidHostField is the validation refusal for a host mutation that blames one
// input. field is HostEntry's wire spelling of that input; an empty field means
// the refusal blames the entry as a whole. message is the hub's own prose,
// unchanged.
func InvalidHostField(field, message string) WireError {
	return WireError{
		Code:    CodeInvalidParams,
		Message: message,
		Data: HostFieldErrorData{
			ErrorData: ErrorData{EvenerErrorInfo: ErrorInvalidHostField},
			Field:     field,
		},
	}
}
```

- [ ] **Step 4: Run the Go test to verify it passes**

Run: `go test ./appwire/ -run TestInvalidHostField -v`
Expected: PASS.

- [ ] **Step 5: Write the failing mapping test**

Add to `cmd/evener-hub/app_host_manage_test.go`:

```go
// TestHostManageValidationRefusalsNameTheInput pins the mapping: each refusal
// the add path can raise names the input the operator can fix, in the spelling
// the dialog's own inputs use (HostEntry's wire fields) — so a message lands on
// the right control without anyone parsing prose. Add-only fields (name) are
// named too, because the edit dialog has no name input to land on.
func TestHostManageValidationRefusalsNameTheInput(t *testing.T) {
	m := testHostManager(nil, nil)
	for _, tc := range []struct {
		entry appwire.HostEntry
		field string
	}{
		{appwire.HostEntry{Name: "m4", Address: "   "}, "address"},
		{appwire.HostEntry{Name: "m4", Address: "u@h.example", User: "bob"}, "user"},
		{appwire.HostEntry{Name: "m4", Address: "h.example", Roots: []string{"  "}}, "roots"},
		{appwire.HostEntry{Name: "bad name", Address: "h.example"}, "name"},
		{appwire.HostEntry{Name: "local", Address: "h.example"}, "name"},
	} {
		_, err := m.Add(context.Background(), appwire.HostAddParams{Entry: tc.entry})
		if err == nil {
			t.Errorf("Add(%+v) accepted, want a validation refusal", tc.entry)
			continue
		}
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Errorf("Add(%+v) error = %v, want a WireError", tc.entry, err)
			continue
		}
		data, ok := wire.Data.(appwire.HostFieldErrorData)
		if !ok {
			t.Errorf("Add(%+v) data = %T, want HostFieldErrorData", tc.entry, wire.Data)
			continue
		}
		if data.EvenerErrorInfo != appwire.ErrorInvalidHostField || data.Field != tc.field {
			t.Errorf("Add(%+v) blamed %q (%s), want %q", tc.entry, data.Field, data.EvenerErrorInfo, tc.field)
		}
	}
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./cmd/evener-hub/ -run TestHostManageValidationRefusalsNameTheInput`
Expected: FAIL — the add path raises `InvalidParams`, so `wire.Data` is `appwire.ErrorData`, not `HostFieldErrorData`.

- [ ] **Step 7: Map the refusals in the hub**

Add to `cmd/evener-hub/app_host_manage.go`, beside `Add`:

```go
// hostEntryField maps a hostreg validation refusal to the input it blames, in
// the wire spelling the dialog's own inputs use (HostEntry's fields), so a
// message lands on the control the operator can fix. A refusal that blames the
// entry as a whole — a cycle — returns "" and the caller raises it form-level.
func hostEntryField(err error) string {
	switch {
	case errors.Is(err, hostreg.ErrMissingSSH):
		return "address"
	case errors.Is(err, hostreg.ErrAmbiguousSSHUser):
		// The field that made the destination ambiguous: user is set while ssh
		// already carries one, and the message says so.
		return "user"
	case errors.Is(err, hostreg.ErrEmptyRoot):
		return "roots"
	case errors.Is(err, hostreg.ErrInvalidName), errors.Is(err, hostreg.ErrReservedName):
		// Add only: the edit dialog has no name input, so this field is what the
		// add form places.
		return "name"
	default:
		// ErrHostCycle and anything else blame the entry as a whole.
		return ""
	}
}

// hostValidationRefusal turns a hostreg validation refusal into the wire
// refusal: the field-carrying shape when the refusal blames one input, a plain
// InvalidParams when it blames the entry as a whole.
func hostValidationRefusal(name string, err error) error {
	message := fmt.Sprintf("host %q: %v", name, err)
	if field := hostEntryField(err); field != "" {
		return appwire.InvalidHostField(field, message)
	}
	return appwire.InvalidParams(message)
}
```

Then in `Add`, replace the `ValidateEntry` refusal with:

```go
	if err := hostreg.ValidateEntry(entry); err != nil {
		return appwire.HostRow{}, hostValidationRefusal(entry.Name, err)
	}
```

- [ ] **Step 8: Run the hub tests**

Run: `go test ./cmd/evener-hub/ -run 'TestHostManage|TestHostSidecar'`
Expected: PASS, including the pre-existing validation test (it asserts the code, which is unchanged).

- [ ] **Step 9: Write the failing TS test**

Add to `appwire-client/typescript/errors.test.ts` (add `appwireTypesGo from "../../appwire/types.go?raw"` beside the existing `appwireErrorsGo` import, and import `ErrorInvalidHostField`, `hostFieldError`):

```ts
test("hostFieldError reads the blamed input off the hub's own discriminant", () => {
  // The binding: the Go constant's value is what this module matches on.
  expect(appwireErrorsGo).toMatch(new RegExp(`ErrorInvalidHostField\\s+ErrorInfo\\s*=\\s*"${ErrorInvalidHostField}"`));
  const blamed = new WireError("host \"m4\": missing ssh destination", -32602, {
    evenerErrorInfo: ErrorInvalidHostField,
    field: "address",
  });
  expect(hostFieldError(blamed)).toBe("address");
  // A plain validation refusal carries no field, a form-level refusal carries an
  // empty one, and a non-WireError carries nothing at all.
  expect(hostFieldError(new WireError("boom", -32602, { evenerErrorInfo: "invalidParams" }))).toBeUndefined();
  expect(hostFieldError(new WireError("boom", -32602, { evenerErrorInfo: ErrorInvalidHostField }))).toBeUndefined();
  expect(hostFieldError(new Error("boom"))).toBeUndefined();
});

test("the host entry's wire spellings are the ones refusals and inputs share", () => {
  // The dialog's inputs and a refusal's field are the same strings because both
  // are HostEntry's json names; this pins that vocabulary against the Go tags so
  // a rename on one side cannot leave the other looking for a missing input.
  for (const name of ["address", "user", "keyPath", "evenerPath", "configPath", "addr", "roots"]) {
    expect(appwireTypesGo).toContain(`json:"${name}`);
  }
});
```

- [ ] **Step 10: Run it to verify it fails**

Run: `cd appwire-client/typescript && npx vitest run errors.test.ts`
Expected: FAIL — `hostFieldError` is not exported.

- [ ] **Step 11: Add the client reader**

In `appwire-client/typescript/errors.ts`:

```ts
// ErrorInvalidHostField is the hub's discriminator for a host mutation's
// validation refusal, whose data names the input that failed
// (appwire.ErrorInvalidHostField, appwire/errors.go). It shares its code with
// every other validation refusal, so the field is only ever read off this
// discriminant. The binding test (errors.test.ts) reads the Go constant.
export const ErrorInvalidHostField = "invalidHostField";

// hostFieldError returns the input a host-mutation refusal blames, or undefined
// for any other rejection — including a refusal that blames the entry as a
// whole, which carries no field. The returned name is the wire spelling the
// dialog's own inputs use, so a caller maps it onto an input directly.
export function hostFieldError(error: unknown): string | undefined {
  if (!(error instanceof WireError) || error.evenerErrorInfo !== ErrorInvalidHostField) return undefined;
  const data = error.data;
  if (!data || typeof data !== "object") return undefined;
  const field = (data as { field?: unknown }).field;
  return typeof field === "string" && field !== "" ? field : undefined;
}
```

- [ ] **Step 12: Run the client tests and the gate**

Run: `cd appwire-client/typescript && npm run qualification` (the package's own typecheck + tests; `make test-api-package` runs the same)
Expected: PASS.

- [ ] **Step 13: Commit**

```bash
gofmt -l appwire cmd/evener-hub # expect no output
go build ./...
git add appwire/errors.go appwire/errors_test.go cmd/evener-hub/app_host_manage.go cmd/evener-hub/app_host_manage_test.go \
  appwire-client/typescript/errors.ts appwire-client/typescript/errors.test.ts
git commit -m "feat(hosts): name the blamed input on a host validation refusal"
```

---

### Task 5: The hub's update flow

**Files:**
- Modify: `cmd/evener-hub/app_host_manage.go` (`hostManagerConfig.removing` → `mutating`, the mark helpers, the store's in-place replace, `Update`, handler registration)
- Test: `cmd/evener-hub/app_host_manage_update_test.go` (new)

**Interfaces:**
- Consumes: `hostreg.Registry.Update` (Task 1), `sshconn.Manager.UpdateHost` (Task 2), `appwire.HostUpdateParams`/`HostUpdateResponse`/`HostEntry` and `HostRow`'s entry fields (Task 3), `hostValidationRefusal` (Task 4), and the existing `saveSidecar`, `rollbackSidecar`, `sidecarRenameCommitted`, `registerSource`, `hostRow`, `assertWireCode`, `assertSidecarNames`, `served`, `blockingRunner`, `waitSidecarLacks`.
- Produces: `func (m *hubHostManager) Update(ctx context.Context, params appwire.HostUpdateParams) (appwire.HostUpdateResponse, error)`; the generalized in-flight mark (`markMutating`/`unmarkMutating`/`isMutating`, `hostMutationConflict`) consulted by Add, Remove, and Update; `hostSidecarStore.withReplaced`/`hostSidecarStore.replace`; the registered `evener/host/update` handler. Task 6 extends `Update`'s live and finish phases.

- [ ] **Step 1: Write the failing tests**

Create `cmd/evener-hub/app_host_manage_update_test.go`. The fixture:

```go
package hub

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// updateFixture is a hub host manager with one sidecar host ("side") already
// committed over a real sidecar file, plus the config path the tests assert the
// durable state against. hubTOMLHosts, when given, are the entries the
// controller's own hub.toml declared (they cannot be edited here).
type updateFixture struct {
	m          *hubHostManager
	configPath string
	sources    *appsource.Registry
	hosts      *hostreg.Registry
}

func newUpdateFixture(t *testing.T, hubTOMLHosts ...hostreg.Host) *updateFixture {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	hosts, err := hostreg.New(hubTOMLHosts)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, nil, hubcore.WebConfig{}, configPath, hosts, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "side", Address: "side.example"},
	}); err != nil {
		t.Fatalf("Add(side): %v", err)
	}
	return &updateFixture{m: m, configPath: configPath, sources: sources, hosts: hosts}
}

// readSidecarBytes reads the sidecar file's raw bytes, so a test can pin that a
// refusal wrote nothing at all.
func readSidecarBytes(t *testing.T, configPath string) []byte {
	t.Helper()
	data, err := os.ReadFile(sidecarPathFor(configPath))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	return data
}
```

and the tests:

```go
// TestHostManageUpdateRoundTripsEveryMutableField pins criteria 1 and 2: each
// mutable field lands, list/status returns it, a fresh boot over the same config
// sees the edited values as the effective entry, and hub.toml was never written.
func TestHostManageUpdateRoundTripsEveryMutableField(t *testing.T) {
	f := newUpdateFixture(t)
	entry := appwire.HostEntry{
		Address:    "side2.example",
		User:       "operator",
		KeyPath:    "/keys/side",
		EvenerPath: "/opt/evener",
		ConfigPath: "/etc/evener/hub.toml",
		Addr:       "127.0.0.1:9180",
		Roots:      []string{"/srv/one", "/srv/two"},
	}
	resp, err := f.m.Update(context.Background(), appwire.HostUpdateParams{Name: "side", Entry: entry})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if resp.Host.Name != "side" || resp.Host.Address != entry.Address {
		t.Fatalf("response row = %+v, want the edited entry", resp.Host)
	}
	status, err := f.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := status.Host
	if got.Address != entry.Address || got.User != entry.User || got.KeyPath != entry.KeyPath ||
		got.EvenerPath != entry.EvenerPath || got.ConfigPath != entry.ConfigPath || got.Addr != entry.Addr ||
		!slices.Equal(got.Roots, entry.Roots) {
		t.Fatalf("row = %+v, want the edited entry %+v", got, entry)
	}
	// The sidecar is the durable record: a fresh boot over the same config sees
	// the edited values, with no hub.toml write anywhere.
	boot := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, f.configPath, nil, nil)
	loaded, ok := boot.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("the edited host is not in the reloaded sidecar")
	}
	want := hostreg.Host{
		Name: "side", SSH: entry.Address, User: entry.User, KeyPath: entry.KeyPath,
		EvenerPath: entry.EvenerPath, ConfigPath: entry.ConfigPath, Addr: entry.Addr, Roots: entry.Roots,
	}
	if !loaded.Equal(want) {
		t.Fatalf("reloaded entry = %+v, want %+v", loaded, want)
	}
	hubTOML, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatalf("read hub.toml: %v", err)
	}
	if len(hubTOML) != 0 {
		t.Fatalf("hub.toml = %q, want it untouched", hubTOML)
	}
}

// TestHostManageUpdateKeepsTheFileOrder pins criterion 17's second half: an edit
// replaces one entry rather than moving it, so the file keeps the order it had.
func TestHostManageUpdateKeepsTheFileOrder(t *testing.T) {
	f := newUpdateFixture(t)
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "second", Address: "second.example"},
	}); err != nil {
		t.Fatalf("Add(second): %v", err)
	}
	if _, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "side2.example"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	assertSidecarNames(t, f.configPath, "side", "second")
}

// TestHostManageUpdateRefusalsCommitNothing pins criteria 4, 5, and 14's
// durable half: a hub.toml name, an unknown name, and a name with a mutation in
// flight all refuse, and the file's bytes are the ones the fixture left.
func TestHostManageUpdateRefusalsCommitNothing(t *testing.T) {
	f := newUpdateFixture(t, hostreg.Host{Name: "toml", SSH: "toml.example"})
	before := readSidecarBytes(t, f.configPath)

	_, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "toml",
		Entry: appwire.HostEntry{Address: "other.example"},
	})
	if err == nil {
		t.Fatal("Update of a hub.toml name committed, want a refusal")
	}
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if !strings.Contains(err.Error(), "hub.toml") {
		t.Fatalf("hub.toml refusal = %v, want the edit-the-file explanation", err)
	}

	_, err = f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "nope",
		Entry: appwire.HostEntry{Address: "other.example"},
	})
	if err == nil {
		t.Fatal("Update of an unknown name committed, want a refusal")
	}
	assertWireCode(t, err, appwire.CodeInvalidParams)

	// The live entries are the two the fixture started with, untouched...
	live, ok := f.hosts.Get("toml")
	if !ok || live.SSH != "toml.example" {
		t.Fatalf("hub.toml entry after the refusals = %+v, want it untouched", live)
	}
	side, ok := f.hosts.Get("side")
	if !ok || side.SSH != "side.example" {
		t.Fatalf("sidecar entry after the refusals = %+v, want it untouched", side)
	}
	// ...and so are the file's bytes.
	if after := readSidecarBytes(t, f.configPath); !bytes.Equal(before, after) {
		t.Fatalf("sidecar changed across refusals:\nbefore %s\nafter  %s", before, after)
	}
}

// TestHostManageUpdateValidatesBeforeWriting pins criterion 12: a refusal
// commits nothing — not even a normalized copy — and a padded input is stored
// trimmed, so the file and the live set cannot drift.
func TestHostManageUpdateValidatesBeforeWriting(t *testing.T) {
	f := newUpdateFixture(t)
	before := readSidecarBytes(t, f.configPath)
	for _, entry := range []appwire.HostEntry{
		{Address: ""},
		{Address: "   "},
		{Address: "h.example", Roots: []string{"   "}},
		{Address: "u@h.example", User: "bob"},
	} {
		if _, err := f.m.Update(context.Background(), appwire.HostUpdateParams{Name: "side", Entry: entry}); err == nil {
			t.Errorf("Update(%+v) accepted, want a validation refusal", entry)
		}
	}
	if after := readSidecarBytes(t, f.configPath); !bytes.Equal(before, after) {
		t.Fatal("a refused update rewrote the sidecar")
	}
	if live, ok := f.hosts.Get("side"); !ok || live.SSH != "side.example" {
		t.Fatalf("live entry after the refusals = %+v, want it untouched", live)
	}
	// The refusal names the input, so the dialog can place it (criterion 11).
	_, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "u@h.example", User: "bob"},
	})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("refusal = %v, want a WireError", err)
	}
	if data, ok := wire.Data.(appwire.HostFieldErrorData); !ok || data.Field != "user" {
		t.Fatalf("refusal data = %#v, want the blamed input user", wire.Data)
	}
	// A padded input is stored trimmed.
	if _, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "  side3.example  ", User: "  operator  "},
	}); err != nil {
		t.Fatalf("Update with padded values: %v", err)
	}
	live, _ := f.hosts.Get("side")
	if live.SSH != "side3.example" || live.User != "operator" {
		t.Fatalf("stored entry = %+v, want the trimmed values", live)
	}
	reloaded, err := loadHostSidecar(sidecarPathFor(f.configPath))
	if err != nil {
		t.Fatalf("reload sidecar: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].SSH != "side3.example" || reloaded[0].User != "operator" {
		t.Fatalf("reloaded entries = %+v, want the trimmed values on disk", reloaded)
	}
}

// TestHostManageUpdateAdvancesTheRegistryGeneration pins criterion 6 through the
// hub: two successive edits give strictly increasing generations and each stops
// matching a capture taken before it.
func TestHostManageUpdateAdvancesTheRegistryGeneration(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	first, _ := f.hosts.Get("side")
	if _, err := f.m.Update(ctx, appwire.HostUpdateParams{Name: "side", Entry: appwire.HostEntry{Address: "a2.example"}}); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	second, _ := f.hosts.Get("side")
	if second.Generation <= first.Generation {
		t.Fatalf("generation = %d after editing %d, want a strictly greater one", second.Generation, first.Generation)
	}
	if f.hosts.SameRegistration("side", first) {
		t.Fatal("a capture from before the first edit still matches")
	}
	if _, err := f.m.Update(ctx, appwire.HostUpdateParams{Name: "side", Entry: appwire.HostEntry{Address: "a3.example"}}); err != nil {
		t.Fatalf("second Update: %v", err)
	}
	third, _ := f.hosts.Get("side")
	if third.Generation <= second.Generation {
		t.Fatalf("generation = %d after the second edit, want > %d", third.Generation, second.Generation)
	}
	if f.hosts.SameRegistration("side", second) {
		t.Fatal("a capture from the first edit still matches the second's entry")
	}
}

// TestHostManageUpdateRollsBackWhenTheLivePhaseFails pins criterion 16: a failed
// live phase leaves the file, the store row, and the live registry describing the
// old entry, and a retry of the same edit then succeeds.
func TestHostManageUpdateRollsBackWhenTheLivePhaseFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	// A manager whose registry is a DIFFERENT, empty one: UpdateHost refuses
	// hostreg.ErrUnknownHost deterministically — the one seam that fails between
	// the durable save and the live swap.
	otherReg, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(otherReg, sshconn.Options{})
	t.Cleanup(func() { _ = manager.Close() })
	hosts, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), manager, hubcore.WebConfig{}, configPath, hosts, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "side", Address: "side.example"},
	}); err != nil {
		t.Fatalf("Add(side): %v", err)
	}
	before, _ := hosts.Get("side")

	if _, err := m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "edited.example"},
	}); err == nil {
		t.Fatal("Update over a live seam that refuses succeeded, want the failure")
	}

	// The live registry, the store row, and the file all still describe the old
	// entry.
	live, ok := hosts.Get("side")
	if !ok || !live.Equal(before) || live.Generation != before.Generation {
		t.Fatalf("live entry after the failed edit = %+v, want %+v", live, before)
	}
	stored := m.cfg.sidecar.snapshot()
	if len(stored) != 1 || stored[0].SSH != "side.example" {
		t.Fatalf("store rows after the failed edit = %+v, want the old entry", stored)
	}
	onDisk, err := loadHostSidecar(sidecarPathFor(configPath))
	if err != nil {
		t.Fatalf("reload sidecar: %v", err)
	}
	if len(onDisk) != 1 || onDisk[0].SSH != "side.example" {
		t.Fatalf("sidecar after the failed edit = %+v, want the old entry", onDisk)
	}

	// The retry lands, from a fresh boot over the same config with a live seam
	// that works: nothing was half-applied.
	boot := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, nil, nil)
	if _, err := boot.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "edited.example"},
	}); err != nil {
		t.Fatalf("retry after the rollback: %v", err)
	}
	reloaded, err := loadHostSidecar(sidecarPathFor(configPath))
	if err != nil {
		t.Fatalf("reload sidecar after the retry: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].SSH != "edited.example" {
		t.Fatalf("sidecar after the retry = %+v, want the edited entry", reloaded)
	}
}
```

The window tests (criterion 14 and the liveness property) need the update analogue of `startParkedRemoval`; add to the same file:

```go
// updateOutcome carries the parked Update's result back to the test.
type updateOutcome struct {
	resp appwire.HostUpdateResponse
	err  error
}

// parkedUpdate is one update driven into its released live-phase window: an
// Ensure parked inside the manager's first probe holds the per-host gate, so the
// Update that follows parks inside Manager.UpdateHost's gate wait. The update's
// commit has landed by the time the helper returns — the file and the store row
// hold the edited entry, and the mark fences the name — and the window stays open
// until release.
type parkedUpdate struct {
	m          *hubHostManager
	sources    *appsource.Registry
	configPath string
	runner     *blockingRunner
	release    func()
	ensureDone chan error
	updateDone chan updateOutcome
}

func startParkedUpdate(t *testing.T, name string) *parkedUpdate {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	reg, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	runner := &blockingRunner{entered: make(chan struct{}), release: make(chan struct{})}
	manager := sshconn.New(reg, sshconn.Options{Runner: runner})
	t.Cleanup(func() { _ = manager.Close() })
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, manager, hubcore.WebConfig{}, configPath, reg, nil)
	for _, host := range []appwire.HostAddParams{
		{Entry: appwire.HostEntry{Name: "keep", Address: "keep.example"}},
		{Entry: appwire.HostEntry{Name: name, Address: name + ".example"}},
	} {
		if _, err := m.Add(context.Background(), host); err != nil {
			t.Fatalf("Add(%s): %v", host.Entry.Name, err)
		}
	}
	// Park an Ensure for the host inside its first probe: it holds the per-host
	// gate the update's live phase must wait for.
	var parked sync.WaitGroup
	parked.Add(2)
	ensureDone := make(chan error, 1)
	go func() {
		defer parked.Done()
		_, err := manager.Ensure(context.Background(), name)
		ensureDone <- err
	}()
	<-runner.entered
	updateDone := make(chan updateOutcome, 1)
	go func() {
		defer parked.Done()
		resp, err := m.Update(context.Background(), appwire.HostUpdateParams{
			Name:  name,
			Entry: appwire.HostEntry{Address: "edited.example"},
		})
		updateDone <- updateOutcome{resp: resp, err: err}
	}()
	// The commit landed once the file holds the edited address: the save runs
	// under the mutation mutex, ahead of the live phase.
	waitSidecarAddress(t, configPath, name, "edited.example")
	var releaseOnce sync.Once
	pu := &parkedUpdate{
		m:          m,
		sources:    sources,
		configPath: configPath,
		runner:     runner,
		release:    func() { releaseOnce.Do(func() { close(runner.release) }) },
		ensureDone: ensureDone,
		updateDone: updateDone,
	}
	t.Cleanup(func() {
		// Release the parks, then drain both goroutines before the manager's
		// Close and the temp dir removal run.
		pu.release()
		parked.Wait()
	})
	return pu
}

// waitSidecarAddress polls the sidecar file until name's entry carries want as
// its address, with a deadline only a genuine failure to commit can hit.
func waitSidecarAddress(t *testing.T, configPath, name, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := loadHostSidecar(sidecarPathFor(configPath))
		if err == nil {
			for _, e := range entries {
				if e.Name == name && e.SSH == want {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the update of %q never committed: sidecar = %+v (%v)", name, entries, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitUpdateDone collects the parked Update's result once the window closes.
func (pu *parkedUpdate) waitUpdateDone(t *testing.T) updateOutcome {
	t.Helper()
	select {
	case done := <-pu.updateDone:
		return done
	case <-time.After(5 * time.Second):
		t.Fatal("the update never finished after its live phase was released")
		return updateOutcome{}
	}
}

// TestHostManageUpdateWindowFencesTheNameAndKeepsConcurrentCommits pins
// criterion 14 and the round-8 liveness property in one window: while an update
// is parked in its live phase the mutation mutex is free (host/list serves), the
// fenced name refuses add, remove, and a second update with the same conflict a
// removal in flight produces today, and a different name commits.
func TestHostManageUpdateWindowFencesTheNameAndKeepsConcurrentCommits(t *testing.T) {
	pu := startParkedUpdate(t, "side")

	if err := served(t, "host/list", func() error {
		_, err := pu.m.List(context.Background(), appwire.EmptyParams{})
		return err
	}); err != nil {
		t.Fatalf("host/list during the update window: %v", err)
	}
	if !pu.runner.parked.Load() || pu.runner.returned.Load() {
		t.Fatal("the parked live phase finished before host/list served; the test did not hold the window open")
	}

	for _, call := range []struct {
		name string
		run  func() error
	}{
		{"host/add", func() error {
			_, err := pu.m.Add(context.Background(), appwire.HostAddParams{
				Entry: appwire.HostEntry{Name: "side", Address: "resurrect.example"},
			})
			return err
		}},
		{"host/remove", func() error {
			_, err := pu.m.Remove(context.Background(), appwire.HostRemoveParams{Name: "side"})
			return err
		}},
		{"host/update", func() error {
			_, err := pu.m.Update(context.Background(), appwire.HostUpdateParams{
				Name:  "side",
				Entry: appwire.HostEntry{Address: "second.example"},
			})
			return err
		}},
	} {
		if err := served(t, call.name, call.run); err == nil {
			t.Errorf("%s of the mid-update name committed, want the mutation conflict", call.name)
		} else {
			assertWireCode(t, err, appwire.CodeConflict)
		}
	}
	// The refusals committed nothing: the entry the update's own commit wrote is
	// the whole durable state of the window.
	assertSidecarNames(t, pu.configPath, "keep", "side")
	entries, err := loadHostSidecar(sidecarPathFor(pu.configPath))
	if err != nil {
		t.Fatalf("loadHostSidecar: %v", err)
	}
	if len(entries) != 2 || entries[1].SSH != "edited.example" {
		t.Fatalf("sidecar during the window = %+v, want the edited entry", entries)
	}

	// A different name commits through the window: one host's teardown holds up
	// no other host's mutation.
	if err := served(t, "host/add (other)", func() error {
		_, err := pu.m.Add(context.Background(), appwire.HostAddParams{
			Entry: appwire.HostEntry{Name: "other", Address: "other.example"},
		})
		return err
	}); err != nil {
		t.Fatalf("Add(other) during the update window: %v", err)
	}
	assertSidecarNames(t, pu.configPath, "keep", "side", "other")

	// Release: the update completes, the row reports the edited entry, and the
	// fence lifts with it.
	pu.release()
	if err := <-pu.ensureDone; err == nil {
		t.Fatal("the parked Ensure succeeded; the blocking runner must fail the probe")
	}
	done := pu.waitUpdateDone(t)
	if done.err != nil {
		t.Fatalf("Update: %v", done.err)
	}
	if done.resp.Host.Address != "edited.example" {
		t.Fatalf("update row = %+v, want the edited entry", done.resp.Host)
	}
	if _, err := pu.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "after.example"},
	}); err != nil {
		t.Fatalf("Update after the window closed: %v", err)
	}
}

// TestHostManageUpdateLeavesTheRowOffline pins criteria 7/8's row half at the hub
// level: the edit retires the channel, so the row reads offline with Connect
// available, and the configured values are the edited entry's (criterion 1).
// The dial half — that a following attach dials the EDITED entry — is pinned in
// package sshconn, where the attach fixtures live
// (TestEnsureAfterUpdateDialsTheEditedEntry).
func TestHostManageUpdateLeavesTheRowOffline(t *testing.T) {
	pu := startParkedUpdate(t, "side")
	pu.release()
	if err := <-pu.ensureDone; err == nil {
		t.Fatal("the parked Ensure succeeded; the blocking runner must fail the probe")
	}
	if done := pu.waitUpdateDone(t); done.err != nil {
		t.Fatalf("Update: %v", done.err)
	}
	row, err := pu.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if row.Host.Attached {
		t.Fatalf("row = %+v, want it offline: every update retires the channel", row.Host)
	}
	if row.Host.Address != "edited.example" || row.Host.Origin != hostOriginSidecar {
		t.Fatalf("row = %+v, want the edited entry under its sidecar origin", row.Host)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/evener-hub/ -run 'TestHostManageUpdate'`
Expected: FAIL to compile — `m.Update undefined`, `hostreg`/`slices` imports missing in the new file as needed.

- [ ] **Step 3: Generalize the in-flight mark**

In `cmd/evener-hub/app_host_manage.go`, rename the mark and its helpers, and generalize the docs and the conflict message (the rename touches only this file):

```go
	// mutating holds the names with a mutation in flight: add, remove, and
	// update all mark the name in their commit phase, after the durable change
	// landed and the store row moved with it, and clear it in their finish
	// phase. The window a mutation releases the mutex for — a teardown that
	// blocks on the per-host gate a supervisor's reconnect/ensure cycle can hold
	// for minutes — must admit no second mutation of the same name, so every
	// mutation refuses a marked name until its own finish clears the mark.
	// Guarded by mu.
	mutating map[string]struct{}
```

```go
// markMutating records name as having a mutation in flight, in the commit phase
// that already made its durable change. Callers hold mu.
func (m *hubHostManager) markMutating(name string) {
	m.cfg.mutating[name] = struct{}{}
}

// unmarkMutating clears the mark in the finish phase, on the success and the
// failure exit alike: a successful mutation leaves the name mutable again, a
// failed one leaves it fully intact and retryable. Callers hold mu.
func (m *hubHostManager) unmarkMutating(name string) {
	delete(m.cfg.mutating, name)
}

// isMutating reports whether name has a mutation in flight — its durable commit
// landed and its live phase has not finished. Callers hold mu.
func (m *hubHostManager) isMutating(name string) bool {
	_, marked := m.cfg.mutating[name]
	return marked
}

// hostMutationConflict is the typed refusal for a name with a mutation already
// in flight: the conflict code tells the caller the name is transiently held and
// retryable, rather than mislabeling it a duplicate (Add) or file-declared (a
// second Remove). It commits nothing.
func hostMutationConflict(name string) error {
	return appwire.Conflict(fmt.Sprintf("host %q: a mutation is already in progress; retry once it finishes", name))
}
```

Update the call sites: `Add` and `Remove` use `m.isMutating(...)`/`hostMutationConflict(...)`; `Remove` calls `m.markMutating(host.Name)` / `m.unmarkMutating(host.Name)`; `newHubHostManager` initializes `mutating: map[string]struct{}{}`; `rowOrigin` reads `m.isMutating(name)` (keeping its doc, which explains that the mark keeps a mid-mutation host's origin truthful).

- [ ] **Step 4: Add the store's in-place replace**

Beside `without` in the sidecar store:

```go
// replace swaps entry in for the entry already stored under entry.Name, in
// place, so the file keeps the order it had: an edit is a minimal change to it
// rather than a reordering nothing asked for. Callers hold hostManagerConfig.mu.
func (s *hostSidecarStore) replace(entry hostreg.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = replaceEntry(s.entries, entry)
}

// withReplaced returns the entries a durable replace would write: the stored
// entries with entry swapped in for the same-name one, in place. Callers hold
// hostManagerConfig.mu.
func (s *hostSidecarStore) withReplaced(entry hostreg.Host) []hostreg.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	return replaceEntry(append([]hostreg.Host(nil), s.entries...), entry)
}

// replaceEntry swaps entry in for the same-name entry, in place, appending when
// the name is absent — unreachable on the update path, whose commit checks
// liveness first, but keeping this helper total rather than silently dropping an
// edit.
func replaceEntry(entries []hostreg.Host, entry hostreg.Host) []hostreg.Host {
	for i, e := range entries {
		if e.Name == entry.Name {
			entries[i] = entry
			return entries
		}
	}
	return append(entries, entry)
}
```

- [ ] **Step 5: Implement `Update`**

Add to `cmd/evener-hub/app_host_manage.go`, after `Remove`:

```go
// Update applies one edit to a live sidecar host entry: the durable sidecar
// entry, the store row, the live registry entry, and the host's channel. Name is
// immutable — it is the target this call addresses, never a value it changes,
// because it keys source IDs, cached rows, manager state, and the file's own
// entries. hub.toml-declared names are refused (edit the file); unknown names
// are InvalidParams; a mutation already in flight for the name is a Conflict.
//
// The three phases mirror Remove's, which is what keeps the durable-first order
// and the compensation paths in one shape:
//
//   - Commit, under the mutation mutex: refuse an in-flight mutation on the
//     name, require a live sidecar entry, validate the entry BEFORE anything is
//     written — hostreg.ValidateEntry is the same call the add flow runs, so a
//     refusal commits nothing and an entry the registry would reject never
//     reaches the file — persist durable-first with one atomic write that
//     replaces the entry in place, replace the store row in the same critical
//     section, set the mark, release the mutex.
//   - Live, mutex-free: with a manager wired, manager.UpdateHost replaces the
//     registry entry and retires the channel under the per-host gate as one
//     atomic step; without one, read the pre-swap entry, call the registry's own
//     Update, and retire the name's retained attach record by that entry's
//     generation — the generation-scoped clear the manager path's hook performs,
//     so a stale row racing the clear is still fenced. An error means nothing
//     live changed: the registry refuses ahead of its own swap.
//   - Finish, under the mutex: clear the mark, compensate a failed live phase
//     by rolling the sidecar back to the live set as it stands now — not a
//     pre-commit copy, so a concurrent add or removal that committed in this
//     window survives; an absent live entry makes that un-commit a removal, and
//     a vanished entry after a successful live phase retires the name's derived
//     state exactly as Remove does — and build the response row from the entry
//     the registry now holds.
//
// An edit never dials, deploys, or attaches: its live effects are exactly the
// registry replacement and the teardown.
func (m *hubHostManager) Update(ctx context.Context, params appwire.HostUpdateParams) (appwire.HostUpdateResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostUpdateResponse{}, err
	}
	name := strings.TrimSpace(params.Name)
	// params.Entry.Name is deliberately not read: the update's target is
	// params.Name, and not reading the entry's own name is what makes a rename
	// unrepresentable rather than merely refused. Name is immutable — it keys
	// source IDs, cached rows, manager state, and the file's own entries — so the
	// request has nowhere to put a new one.
	entry := hostreg.Normalize(hostreg.Host{
		Name:       name,
		SSH:        params.Entry.Address,
		User:       params.Entry.User,
		KeyPath:    params.Entry.KeyPath,
		EvenerPath: params.Entry.EvenerPath,
		ConfigPath: params.Entry.ConfigPath,
		Addr:       params.Entry.Addr,
		Roots:      params.Entry.Roots,
	})
	// Validate before the write: a refusal here commits nothing, and an entry
	// the registry would reject never reaches the file. The registry re-runs the
	// same validation under its own lock; this check is what keeps the file
	// clean, not a substitute for it.
	if err := hostreg.ValidateEntry(entry); err != nil {
		return appwire.HostUpdateResponse{}, hostValidationRefusal(name, err)
	}

	// Commit phase: the durable state and the in-memory sidecar change together,
	// under the mutation mutex, as one read-modify-write cycle.
	m.cfg.mu.Lock()
	if m.isMutating(name) {
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, hostMutationConflict(name)
	}
	if _, ok := m.cfg.hosts.Get(name); !ok {
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	if !m.cfg.sidecar.isSidecar(name) {
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, appwire.InvalidParams(fmt.Sprintf("host %q is declared in hub.toml; edit the file to change it", name))
	}
	// Persist first: the durable sidecar holds the edited entry before any live
	// state changes, so a save failure leaves the host fully intact and the
	// caller can retry. A failure the rename already committed leaves the file
	// holding the edit while the live set still holds the old entry, so it is
	// compensated back before the refusal returns.
	if err := m.saveSidecar(m.cfg.sidecar.withReplaced(entry)); err != nil {
		if sidecarRenameCommitted(err) {
			err = m.rollbackSidecar(m.cfg.sidecar.snapshot(), err)
		}
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, err
	}
	// The store row moves with the save it belongs to, in the same critical
	// section: a concurrent Add or Remove committing in the window below derives
	// its save from the store, and a row still holding the old entry would
	// re-persist it, undoing the edit on the next start.
	m.cfg.sidecar.replace(entry)
	m.markMutating(name)
	m.cfg.mu.Unlock()

	// Live phase, mutex-free: the manager's swap blocks on the per-host gate a
	// supervisor can hold for a whole reconnect/ensure cycle, so holding the
	// mutation mutex across it would freeze every concurrent host/list,
	// host/status, and host/add. The mark fences the window instead.
	var liveErr error
	if m.cfg.manager != nil {
		if err := m.cfg.manager.UpdateHost(entry, nil); err != nil {
			liveErr = fmt.Errorf("update host %q: %w", name, err)
		}
	} else if err := m.cfg.hosts.Update(entry); err != nil {
		liveErr = err
	}

	// Finish phase: the mutex comes back for the bookkeeping, and the mark clears
	// on both exits.
	m.cfg.mu.Lock()
	m.unmarkMutating(name)
	if liveErr != nil {
		// The live phase refused ahead of changing anything, so the edit
		// un-commits: the store row goes back to the live entry and the file
		// follows it. The rollback saves the live snapshot, not a pre-commit
		// copy: concurrent Adds and Removes may have committed in the window,
		// and their entries must survive.
		//
		// An absent live entry makes the un-commit a removal, exactly as the
		// finish phase's vanished arm below: a directly driven registry can drop
		// the name while this edit runs (the mutation mark only fences this
		// manager's own paths), and restoring the committed row would then write
		// the edit for a name that is not live — an edit a later Add duplicates
		// and the next sidecar load rejects. The committed row is dropped before
		// the snapshot is taken, so the rollback writes the live set without it.
		if live, ok := m.cfg.hosts.Get(name); ok {
			m.cfg.sidecar.replace(live)
		} else {
			m.cfg.sidecar.remove(name)
		}
		err := m.rollbackSidecar(m.cfg.sidecar.snapshot(), liveErr)
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, err
	}
	stored, ok := m.cfg.hosts.Get(name)
	if !ok {
		// A concurrent removal took the name while this edit's teardown ran —
		// impossible for the same name through this manager (the mark fences it),
		// but a directly driven registry could still have dropped it: the entry
		// is gone, so there is no row to render. Remove the committed store row
		// and save that live snapshot before refusing; otherwise a later Add would
		// append beside the stale edit and poison the next sidecar load as a
		// duplicate name.
		refusal := appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
		m.cfg.sidecar.remove(name)
		// The name's derived state goes with it, exactly as Remove's finish
		// phase retires it and in the same order: the source registration, the
		// name-keyed attach record, then the remote-thread cache entry, then the
		// retained last-known-good list. The order is load-bearing for the same
		// reason Remove's comment gives: an in-flight walk must fail its
		// cache-generation sweep or its source-ownership check, so it cannot
		// re-store obsolete rows after the cache drop. The store row is already
		// gone, so nothing renders this name again.
		if m.cfg.sources != nil {
			m.cfg.sources.Remove(name)
		}
		m.cfg.state.remove(name)
		if m.cfg.remoteCache != nil {
			m.cfg.remoteCache.RemoveSource(name)
		}
		if m.cfg.forgetLastGoodThreads != nil {
			m.cfg.forgetLastGoodThreads(name)
		}
		err := m.rollbackSidecar(m.cfg.sidecar.snapshot(), refusal)
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, err
	}
	m.cfg.mu.Unlock()
	// The row's retained-state fold is fenced on the entry's generation, so the
	// reread above is what makes the returned row the identity this call
	// committed.
	return appwire.HostUpdateResponse{Host: m.hostRow(ctx, stored, hostOriginSidecar)}, nil
}
```

- [ ] **Step 6: Register the handler**

In `registerHostManageHandlers`, after the remove handler:

```go
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostUpdate, hostManageHandler(func(ctx context.Context, params appwire.HostUpdateParams) (appwire.HostUpdateResponse, error) {
		resp, err := m.Update(ctx, params)
		// An edit can change the host's roots, which is what a source's identity
		// addresses; both add and remove invalidate the manifest's sources on
		// commit, and an edit that moves a source must converge the same way
		// rather than wait for the next refresh tick.
		if err == nil && navigation != nil {
			navigation.Invalidate(navigationChangeHint{Sources: true})
		}
		return resp, err
	}))
```

Update the function's doc comment from "the four slice-1 host-management handlers" to name add/list/status/remove/update.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./cmd/evener-hub/ -run 'TestHostManage|TestHostSidecar' -v`
Expected: PASS. Then: `go test ./cmd/evener-hub/internal/sshconn/ ./cmd/evener-hub/internal/hostreg/ ./appwire/` — PASS.

- [ ] **Step 8: Commit**

```bash
gofmt -l cmd/evener-hub # expect no output
go build ./...
git add cmd/evener-hub/app_host_manage.go cmd/evener-hub/app_host_manage_update_test.go
git commit -m "feat(hosts): add the hub's evener/host/update flow over commit/live/finish"
```

---

### Task 6: What an edit does to the host's derived state

**Files:**
- Modify: `cmd/evener-hub/app_host_manage.go` (`Update`'s live and finish phases)
- Test: `cmd/evener-hub/app_host_manage_update_test.go`

**Interfaces:**
- Consumes: everything Task 5 produced, plus `hostAttachState.remove`/`recordKnown`/`apply`, `remoteCache.RemoveSource`, `forgetLastGoodThreads`, `sources.Remove`, `registerSource`, `hubcore.RemoteThreadCache.SourceGeneration`.
- Produces: no new exported surface — `Update`'s live phase clears the attach record inside the swap's own gate hold, and its finish phase re-registers the source only when the edit changed the host's roots.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/evener-hub/app_host_manage_update_test.go`:

```go
// TestHostManageUpdateClearsTheAttachRecord pins criterion 8's offline half: an
// edit retires the identity the retained attach facts belong to, so the retiring
// record — its attach error and its last-known facts — does not outlive the entry
// it describes. No teardown clears it for an offline host whose address changed;
// this is the one live effect the edit performs itself.
func TestHostManageUpdateClearsTheAttachRecord(t *testing.T) {
	f := newUpdateFixture(t)
	// Retain state the way the manager's lifecycle events and attached rows do.
	f.m.observeEvent(sshconn.Event{Host: "side", Kind: sshconn.EventFailed, Err: errors.New("dial refused")})
	f.m.cfg.mu.Lock()
	f.m.cfg.state.recordKnown(appwire.HostRow{
		Name: "side", Attached: true, ServerName: "remote-hub", ServerVersion: "0.1.0",
		HubVersion: "9.9.9", OS: "linux", Arch: "arm64",
	}, hostFactsValidity{handshake: true, facts: true})
	f.m.cfg.mu.Unlock()

	before, err := f.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if before.Host.LastAttachErr != "dial refused" || before.Host.HubVersion != "9.9.9" {
		t.Fatalf("row before the edit = %+v, want the retained attach state", before.Host)
	}

	if _, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "side2.example"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, err := f.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status after the edit: %v", err)
	}
	if after.Host.LastAttachErr != "" {
		t.Fatalf("row = %+v, want the retired identity's attach error gone", after.Host)
	}
	if after.Host.MidAttach {
		t.Fatalf("row = %+v, want the retired identity's in-progress state gone", after.Host)
	}
	if after.Host.ServerName != "" || after.Host.ServerVersion != "" || after.Host.HubVersion != "" ||
		after.Host.OS != "" || after.Host.Arch != "" {
		t.Fatalf("row = %+v, want the retired identity's last-known facts gone", after.Host)
	}
	if after.Host.Address != "side2.example" {
		t.Fatalf("row = %+v, want the edited entry", after.Host)
	}
}

// TestHostManageUpdateReregistersTheSourceOnlyWhenRootsChange pins criterion 10's
// both halves: a roots edit drops the host's derived rows and its last-good
// retention and registers the source afresh — the source's identity owns what it
// addresses — while an edit that leaves roots alone keeps the very same source,
// its cache generation, and the retained list, so an address or path edit never
// blanks the host's sessions in the tree.
func TestHostManageUpdateReregistersTheSourceOnlyWhenRootsChange(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	hosts, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	sources := appsource.NewRegistry()
	cache := &hubcore.RemoteThreadCache{}
	m := newHubHostManager(sources, nil, hubcore.WebConfig{RemoteThreadCache: cache}, configPath, hosts, nil)
	var forgotten []string
	m.cfg.forgetLastGoodThreads = func(sourceID string) { forgotten = append(forgotten, sourceID) }
	if _, err := m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "side", Address: "side.example", Roots: []string{"/one"}},
	}); err != nil {
		t.Fatalf("Add(side): %v", err)
	}
	firstSource, ok := sources.Source("side")
	if !ok {
		t.Fatal("the added host has no source")
	}
	firstGen, ok := cache.SourceGeneration("side")
	if !ok {
		t.Fatal("the added host's source has no cache generation")
	}

	// A non-roots edit: the same source, the same generation, nothing forgotten.
	if _, err := m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "side2.example", Roots: []string{"/one"}},
	}); err != nil {
		t.Fatalf("non-roots Update: %v", err)
	}
	if kept, ok := sources.Source("side"); !ok || kept != firstSource {
		t.Fatalf("source after a non-roots edit = %v (present %v), want the same instance", kept, ok)
	}
	if gen, ok := cache.SourceGeneration("side"); !ok || gen != firstGen {
		t.Fatalf("cache generation after a non-roots edit = %d (present %v), want it unchanged at %d", gen, ok, firstGen)
	}
	if len(forgotten) != 0 {
		t.Fatalf("a non-roots edit dropped retained rows for %v, want nothing", forgotten)
	}

	// A roots edit: a fresh registration, the retention dropped first, and the
	// generation the re-registration minted still in place afterwards.
	if _, err := m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "side2.example", Roots: []string{"/two"}},
	}); err != nil {
		t.Fatalf("roots Update: %v", err)
	}
	secondSource, ok := sources.Source("side")
	if !ok {
		t.Fatal("the roots edit left the host with no source")
	}
	if secondSource == firstSource {
		t.Fatal("the roots edit kept the old source, whose identity addresses the old roots")
	}
	secondGen, ok := cache.SourceGeneration("side")
	if !ok {
		t.Fatal("the roots edit left the source unregistered in the cache")
	}
	if secondGen == firstGen {
		t.Fatalf("cache generation = %d, want the re-registration's own", secondGen)
	}
	if !slices.Equal(forgotten, []string{"side"}) {
		t.Fatalf("forgotten = %v, want exactly the roots edit's own drop", forgotten)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/ -run 'TestHostManageUpdateClears|TestHostManageUpdateReregisters' -v`
Expected: FAIL — the record survives the edit, and neither the source nor the cache generation changes on a roots edit (`hostFactsValidity`/`appsource`/`hubcore` imports may need adding to the test file).

- [ ] **Step 3: Retire the attach record inside the swap's gate hold**

In `Update`, hand the retirement to the manager so it runs inside the swap's own gate hold, and retire inline only on the no-manager fallback — with no manager no lifecycle event can exist, but a stale row can still race the clear. The retirement is generation-scoped (`state.retire(name, gen)`): the manager path retires by the generation the swap replaced (the hook's argument), and the no-manager path reads the live entry's generation immediately before its own swap:

```go
	var liveErr error
	if m.cfg.manager != nil {
		// The retiring identity's name-keyed attach record is retired from inside
		// the manager's own gate hold, in the same hold as the swap: UpdateHost
		// runs this hook after it has replaced the registry entry and torn down the
		// retired identity's channel, and still before it releases the gate.
		// The retirement is generation-scoped: the hook receives the entry the swap
		// replaced, and marking its generation fences a row that captured the old
		// entry before the swap but only reaches its state write after the
		// retirement. The hook must not call back into the manager (the gate is
		// non-reentrant) and must not take the mutation mutex (another goroutine
		// may hold it while parked on this gate) — it only touches the record
		// state's own lock.
		if err := m.cfg.manager.UpdateHost(entry, func(retired hostreg.Host) {
			m.cfg.state.retire(name, retired.Generation)
		}); err != nil {
			liveErr = fmt.Errorf("update host %q: %w", name, err)
		}
	} else {
		// No sshconn manager is wired (tests, embedders), so no lifecycle event can
		// exist for the new identity: retiring inline on the successful swap gives
		// the same guarantee the manager path's hook holds, and this also clears an
		// offline host's retained error and facts when an address edit has no
		// channel to tear down. The pre-swap entry's generation is read immediately
		// before the swap, exactly as the manager path fences rows built from the
		// entry its swap replaced.
		prior, _ := m.cfg.hosts.Get(name)
		if err := m.cfg.hosts.Update(entry); err != nil {
			liveErr = err
		} else {
			m.cfg.state.retire(name, prior.Generation)
		}
	}
```

The finish phase below compares the edited roots against the roots this call replaced, so the commit phase's liveness read keeps its result instead of discarding it (the mark fences the name from its commit to its finish, so the capture is stable for the whole call). Change:

```go
	if _, ok := m.cfg.hosts.Get(name); !ok {
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
```

to:

```go
	before, ok := m.cfg.hosts.Get(name)
	if !ok {
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
```

- [ ] **Step 4: Reconcile the source in the finish phase**

In `Update`'s finish phase, after the failure arm and the `stored` reread, before the mutex is released:

```go
	// The source's identity owns its derived rows, so a roots edit changes what
	// the source addresses and everything keyed to the old roots goes with it:
	// the remote-thread cache entry (its per-source generation included), the
		// source itself, and the web server's retained last-known-good list, after
		// which the source is registered afresh under the new roots. Both identities
		// must retire before retention is cleared: an in-flight old-source walk then
		// fails either its cache-generation fence or its source-instance ownership
		// check and cannot re-store obsolete rows after the clear. The cache drop also
		// stays before registerSource, so it cannot delete the generation the fresh
		// registration mints.
	//
	// A non-roots edit changes nothing the source's identity owns: it is the same
	// source, its cache generation still owns its rows, and the retained list is
	// still this host's — an edit of the SSH address or a path must not blank the
	// host's sessions in the tree.
	if !slices.Equal(before.Roots, stored.Roots) {
		if m.cfg.remoteCache != nil {
			m.cfg.remoteCache.RemoveSource(name)
		}
		if m.cfg.sources != nil {
			m.cfg.sources.Remove(name)
		}
			if m.cfg.forgetLastGoodThreads != nil {
				m.cfg.forgetLastGoodThreads(name)
			}
		m.registerSource(stored)
	}
```

`before.Roots` is the commit phase's capture of the entry this call replaced; the mark fences the name for the whole call, so it is still the entry that was there.

Extend `Update`'s doc comment so it stays true: the live-phase bullet names the attach record's clear, the finish-phase bullet names the roots comparison and what it drops and re-registers, and the closing paragraph ("its live effects are exactly the registry replacement and the teardown") now names the derived-state reconciliation too.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/evener-hub/ -run 'TestHostManage|TestHostSidecar' -v`
Expected: PASS, including the pre-existing tree/retention tests (`go test ./cmd/evener-hub/ ./cmd/evener-hub/internal/...` for the packages this touches).

- [ ] **Step 6: Commit**

```bash
gofmt -l cmd/evener-hub # expect no output
go build ./...
git add cmd/evener-hub/app_host_manage.go cmd/evener-hub/app_host_manage_update_test.go
git commit -m "feat(hosts): reconcile a host's derived state across an edit"
```

---

### Task 7: The pane's Add/Edit dialog and the update store call

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/hosts.ts` (+ `hosts.test.ts`)
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx` (+ `hosts.test.tsx`)
- Not modified: `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.module.css` — `.form` already stacks the fields, and the Dialog's own panel scrolls (`widgets/dialog/dialog.module.css`), so eight fields need no new class. This is a deliberate deviation from the spec's §9 file list; call it out in the PR description.

**Interfaces:**
- Consumes: `HostEntry`, `HostRow`, `HostUpdateParams` (Task 3), `hostFieldError` (Task 4), `evener/host/update` (Task 5).
- Produces: `hostsStore.update(params: { name: string; entry: HostEntry }): Promise<HostRow>`; `hostsStore.add(entry: HostEntry): Promise<HostRow>`; an extended `hostRowEqual`; `HostEntryDialog` (local to `hosts.tsx`) and the rows' Edit action.

- [ ] **Step 1: Write the failing store tests**

Add to `cmd/evener-hub/frontend/src/stores/hosts.test.ts`:

```ts
test("update sends the target name and the entry, then re-reads quietly", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
  await hostsStore.getState().fetch();

  fake.on("evener/host/update", () => ({ host: { ...row("alpha"), address: "a2.example" } }));
  fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), address: "a2.example" }] }));
  const updated = await hostsStore.getState().update({
    name: "alpha",
    entry: { address: "a2.example", user: "operator" },
  });

  expect(updated.address).toBe("a2.example");
  expect(fake.calls.find((c) => c.method === "evener/host/update")?.params).toEqual({
    name: "alpha",
    entry: { address: "a2.example", user: "operator" },
  });
  const load = hostsStore.getState().load;
  expect(load.phase).toBe("ready");
  if (load.phase !== "ready") throw new Error("unreachable");
  expect(load.hosts[0]?.address).toBe("a2.example");
});

test("a rejected update reaches the caller and keeps the rows", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
  await hostsStore.getState().fetch();

  fake.on("evener/host/update", () => Promise.reject(new Error("host \"alpha\": missing ssh destination")));
  await expect(
    hostsStore.getState().update({ name: "alpha", entry: { address: "" } }),
  ).rejects.toThrowError(/missing ssh destination/);

  const load = hostsStore.getState().load;
  expect(load.phase).toBe("ready");
  if (load.phase !== "ready") throw new Error("unreachable");
  expect(load.hosts).toEqual([row("alpha")]);
});

test("the publish guard does not swallow an edit that changes an entry field only", async () => {
  // hostRowEqual's field list is what decides whether the quiet re-read's rows
  // replace the rendered ones; a comparator that ignores user/evenerPath/
  // configPath/addr/roots would leave the pane rendering the pre-edit row.
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
  await hostsStore.getState().fetch();

  const changes: Partial<HostRow>[] = [
    { user: "operator" },
    { evenerPath: "/opt/evener" },
    { configPath: "/etc/evener/hub.toml" },
    { addr: "127.0.0.1:9180" },
    { roots: ["/srv/one"] },
  ];
  for (const change of changes) {
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), ...change }] }));
    await hostsStore.getState().refresh();
    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts[0]).toMatchObject(change);
  }
});
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/hosts.test.ts`
Expected: FAIL — `hostsStore.getState().update is not a function`, and the last test's row keeps the pre-edit value for `user`/`evenerPath`/etc. because the comparator ignores those fields.

- [ ] **Step 3: Add `update` and extend the comparator**

In `cmd/evener-hub/frontend/src/stores/hosts.ts`:

```ts
  update: (params: { name: string; entry: HostEntry }) => Promise<HostRow>;
```

```ts
  update: async (params) => {
    // The host being edited is named by the request's own field: name is
    // immutable, so it is the target rather than a value here (spec §3.1).
    const row = await requireClient().request("evener/host/update", {
      name: params.name,
      entry: params.entry,
    });
    await reReadAfterMutation();
    return row;
  },
```

and extend `hostRowEqual` with the entry's fields, keeping the existing ones:

```ts
    a.user === b.user &&
    a.keyPath === b.keyPath &&
    a.evenerPath === b.evenerPath &&
    a.configPath === b.configPath &&
    a.addr === b.addr &&
    sameRoots(a.roots, b.roots) &&
```

with:

```ts
// sameRoots compares two rows' roots as lists, treating an absent value and an
// empty one as the same: the wire omits empty optional arrays, so the two
// spellings describe the same host, and neither may make the comparator report a
// change the publish guard would re-render for.
function sameRoots(a: readonly string[] | undefined, b: readonly string[] | undefined): boolean {
  const left = a ?? [];
  const right = b ?? [];
  return left.length === right.length && left.every((value, i) => value === right[i]);
}
```

- [ ] **Step 4: Run the store tests to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/hosts.test.ts src/stores/hosts.test.tsx 2>/dev/null || npx vitest run src/stores/hosts.test.ts`
Expected: PASS.

- [ ] **Step 5: Write the failing pane tests**

Update `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.test.tsx`: change the existing add-payload expectation to the nested entry, and add:

```tsx
test("the add dialog submits every entry field", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [] }));
  fake.on("evener/host/add", () => row({ name: "gamma", address: "g.example" }));
  render(<HostsSection sectionId="hosts" />);
  await user.click(await screen.findByRole("button", { name: "Add host" }));
  await user.type(screen.getByLabelText("Name"), "gamma");
  await user.type(screen.getByLabelText("SSH address"), "g.example");
  await user.type(screen.getByLabelText("User"), "operator");
  await user.type(screen.getByLabelText("Key path"), "/keys/g");
  await user.type(screen.getByLabelText("Evener path"), "/opt/evener");
  await user.type(screen.getByLabelText("Hub config path"), "/etc/evener/hub.toml");
  await user.type(screen.getByLabelText("Hub address"), "127.0.0.1:9180");
  await user.type(screen.getByLabelText("Roots"), "/srv/one{enter}/srv/two");
  const dialog = screen.getByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Add host" }));
  await waitFor(() => {
    expect(fake.calls.filter((c) => c.method === "evener/host/add")).toHaveLength(1);
  });
  expect(fake.calls.find((c) => c.method === "evener/host/add")?.params).toMatchObject({
    entry: {
      name: "gamma",
      address: "g.example",
      user: "operator",
      keyPath: "/keys/g",
      evenerPath: "/opt/evener",
      configPath: "/etc/evener/hub.toml",
      addr: "127.0.0.1:9180",
      roots: ["/srv/one", "/srv/two"],
    },
  });
});

test("a sidecar row offers Edit, prefills the whole entry, and sends no name input", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "beta", address: "b.example", user: "bob", roots: ["/srv/b"] })],
  }));
  fake.on("evener/host/update", () => ({ host: row({ name: "beta", address: "b2.example" }) }));
  render(<HostsSection sectionId="hosts" />);
  const rowEl = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(rowEl).getByRole("button", { name: "Edit" }));

  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByText("Edit beta")).toBeTruthy();
  // The prefilled entry, and no name input: the name is the dialog's title.
  expect((within(dialog).getByLabelText("SSH address") as HTMLInputElement).value).toBe("b.example");
  expect((within(dialog).getByLabelText("User") as HTMLInputElement).value).toBe("bob");
  expect((within(dialog).getByLabelText("Roots") as HTMLTextAreaElement).value).toBe("/srv/b");
  expect(within(dialog).queryByLabelText("Name")).toBeNull();

  await user.clear(within(dialog).getByLabelText("SSH address"));
  await user.type(within(dialog).getByLabelText("SSH address"), "b2.example");
  await user.click(within(dialog).getByRole("button", { name: "Save" }));
  await waitFor(() => {
    expect(fake.calls.filter((c) => c.method === "evener/host/update")).toHaveLength(1);
  });
  expect(fake.calls.find((c) => c.method === "evener/host/update")?.params).toMatchObject({
    name: "beta",
    entry: { address: "b2.example" },
  });
});

test("a hub.toml row offers no Edit action", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "alpha", address: "a.example", origin: "hub.toml" })] }));
  render(<HostsSection sectionId="hosts" />);
  const rowEl = (await screen.findByText("alpha")).closest("li")!;
  expect(within(rowEl).queryByRole("button", { name: "Edit" })).toBeNull();
});

test("a validation refusal lands on the input the hub blamed", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/update", () => {
    throw new WireError('host "beta": missing ssh destination', -32602, {
      evenerErrorInfo: "invalidHostField",
      field: "address",
    });
  });
  render(<HostsSection sectionId="hosts" />);
  const rowEl = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(rowEl).getByRole("button", { name: "Edit" }));
  const dialog = await screen.findByRole("dialog");
  await user.clear(within(dialog).getByLabelText("SSH address"));
  await user.click(within(dialog).getByRole("button", { name: "Save" }));

  // The message is the address row's own: the refusal named the wire field the
  // input is labelled for, so the operator sees it where the fix belongs.
  expect(await within(dialog).findByText(/missing ssh destination/)).toBeTruthy();
  expect(within(dialog).queryByText(/Something went wrong/)).toBeNull();
});
```

(`WireError` is imported from `@evener/appwire-client` in this file's sibling tests; add it to the existing import of `HostRow`:

```ts
import { type HostRow, WireError } from "@evener/appwire-client";
```

The failing assertions before Step 7 are the point of this step.)

- [ ] **Step 6: Run them to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/settings/sections/hosts.test.tsx`
Expected: FAIL — no Edit button, no such labels, `WireError` unused because nothing places the refusal.

- [ ] **Step 7: Build the dialog and the Edit action**

In `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx`:

imports:

```tsx
import { friendlyErrorMessage, type HostEntry, type HostRow, hostFieldError } from "@evener/appwire-client";
```

```tsx
import { Button, Chip, ConfirmDialog, Dialog, EmptyState, FormRow, Input, Skeleton, Textarea, useToasts } from "../../../widgets";
```

the pane's state and submit handlers (replacing `addOpen`/`name`/`address`/`keyPath`/`addError`/`adding`):

```tsx
  const [dialogMode, setDialogMode] = useState<"add" | "edit" | null>(null);
  const [editRow, setEditRow] = useState<HostRow | null>(null);
```

```tsx
  async function handleAdd(entry: HostEntry): Promise<void> {
    await hostsStore.getState().add(entry);
    toasts.push("success", `Added ${entry.name}`);
  }

  async function handleEdit(row: HostRow, entry: HostEntry): Promise<void> {
    await hostsStore.getState().update({ name: row.name, entry });
    toasts.push("success", `Updated ${row.name}`);
  }
```

and the pane's `Add host` button opens the same dialog in add mode:

```tsx
        <Button
          size="sm"
          onClick={() => {
            setEditRow(null);
            setDialogMode("add");
          }}
        >
          Add host
        </Button>
```

the row's Edit action, beside the existing Remove control (sidecar, non-removed rows only):

```tsx
                  {row.origin === "sidecar" && !row.removed && (
                    <Button
                      size="sm"
                      variant="quiet"
                      onClick={() => {
                        setEditRow(row);
                        setDialogMode("edit");
                      }}
                    >
                      Edit
                    </Button>
                  )}
```

the dialog mount, replacing the current `<Dialog open={addOpen} ...>` block. Delete that whole block — its eight labelled fields, its `addError` paragraph, and its footer — along with the pane state it used (`addOpen`, `name`, `address`, `keyPath`, `addError`, `adding`) and the old `handleAdd` body that read them; `Add host` now opens the dialog through `setDialogMode("add")`:

```tsx
      {dialogMode !== null && (
        <HostEntryDialog
          // The key is what makes an edit dialog start from its row: a dialog
          // opened for a different host (or for Add after an Edit) is a fresh
          // mount with fresh field state, never the previous host's values.
          key={dialogMode === "edit" ? `edit:${editRow?.name ?? ""}` : "add"}
          mode={dialogMode}
          row={editRow ?? undefined}
          onClose={() => {
            setDialogMode(null);
            setEditRow(null);
          }}
          onSubmit={async (entry) => {
            if (dialogMode === "edit" && editRow !== null) await handleEdit(editRow, entry);
            else await handleAdd(entry);
          }}
        />
      )}
```

and the dialog itself, at the end of the file:

```tsx
// rootsFromText parses the roots field: one root per line, trimmed, with blank
// lines dropped — so a stray blank line is not an empty root the hub refuses
// (hostreg's ErrEmptyRoot). The field is a Textarea rather than the settings
// cluster's browse-assisted PathListEditor because these paths live on the
// REMOTE host: a controller-side directory picker would offer the wrong
// machine's directories.
function rootsFromText(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "");
}

function rootsToText(roots: readonly string[] | undefined): string {
  return (roots ?? []).join("\n");
}

// HostEntryDialog is the one Add/Edit dialog (spec §3.5, §13): every mutable
// HostConfig field under the wire's own spelling — the spelling a refusal's
// field uses, so a message lands on the input it names — plus slice 1's Key path
// control. Add carries the name; Edit does not, because a name is immutable and
// the host being edited is named in the title. A refusal the hub blames on an
// input is placed in that row's own error slot; anything else is form-level.
interface HostEntryDialogProps {
  mode: "add" | "edit";
  row?: HostRow;
  onClose: () => void;
  onSubmit: (entry: HostEntry) => Promise<void>;
}

function HostEntryDialog({ mode, row, onClose, onSubmit }: HostEntryDialogProps) {
  const [name, setName] = useState(mode === "add" ? "" : (row?.name ?? ""));
  const [address, setAddress] = useState(row?.address ?? "");
  const [user, setUser] = useState(row?.user ?? "");
  const [keyPath, setKeyPath] = useState(row?.keyPath ?? "");
  const [evenerPath, setEvenerPath] = useState(row?.evenerPath ?? "");
  const [configPath, setConfigPath] = useState(row?.configPath ?? "");
  const [addr, setAddr] = useState(row?.addr ?? "");
  const [roots, setRoots] = useState(rootsToText(row?.roots));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<{ field: string | null; message: string } | null>(null);

  const fieldError = (field: string): string | undefined =>
    error !== null && error.field === field ? error.message : undefined;
  const formError = error !== null && error.field === null ? error.message : null;
  // The address is the one field the hub always requires; a name is required on
  // add only, and the edit dialog has no name input to satisfy.
  const submitDisabled = busy || address.trim() === "" || (mode === "add" && name.trim() === "");

  async function handleSubmit(): Promise<void> {
    setBusy(true);
    setError(null);
    try {
      await onSubmit({
        ...(mode === "add" ? { name: name.trim() } : {}),
        address: address.trim(),
        user: user.trim(),
        keyPath: keyPath.trim(),
        evenerPath: evenerPath.trim(),
        configPath: configPath.trim(),
        addr: addr.trim(),
        roots: rootsFromText(roots),
      });
      onClose();
    } catch (err) {
      // Remove's posture, carried over: the failure is shown, never swallowed.
      // A refusal that names an input goes on that input; everything else is
      // form-level.
      setError({ field: hostFieldError(err) ?? null, message: friendlyErrorMessage(err) });
    } finally {
      setBusy(false);
    }
  }

  const prefix = `hosts-${mode}`;
  return (
    <Dialog
      open
      onClose={() => {
        if (!busy) onClose();
      }}
      title={mode === "add" ? "Add host" : `Edit ${row?.name ?? ""}`.trim()}
      footer={
        <>
          <Button variant="quiet" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={submitDisabled} onClick={() => void handleSubmit()}>
            {busy ? "Saving…" : mode === "add" ? "Add host" : "Save"}
          </Button>
        </>
      }
    >
      <div className={CLASS.form}>
        {mode === "add" && (
          <FormRow
            label="Name"
            htmlFor={`${prefix}-name`}
            help="The source ID used in refs and URLs. A name is fixed once the host exists."
            error={fieldError("name")}
          >
            <Input id={`${prefix}-name`} value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
          </FormRow>
        )}
        <FormRow
          label="SSH address"
          htmlFor={`${prefix}-address`}
          help="SSH destination, e.g. host.example or user@host.example."
          error={fieldError("address")}
        >
          <Input id={`${prefix}-address`} value={address} onChange={(e) => setAddress(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label="User"
          htmlFor={`${prefix}-user`}
          help="Optional SSH user; leave empty when the address already names one."
          error={fieldError("user")}
        >
          <Input id={`${prefix}-user`} value={user} onChange={(e) => setUser(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label="Key path"
          htmlFor={`${prefix}-key`}
          help="Optional SSH private-key path used when dialing this host."
          error={fieldError("keyPath")}
        >
          <Input id={`${prefix}-key`} value={keyPath} onChange={(e) => setKeyPath(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label="Evener path"
          htmlFor={`${prefix}-evener`}
          help="Optional path to the evener binary on the host, when it is not on PATH."
          error={fieldError("evenerPath")}
        >
          <Input id={`${prefix}-evener`} value={evenerPath} onChange={(e) => setEvenerPath(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label="Hub config path"
          htmlFor={`${prefix}-config`}
          help="Optional path to the host's hub.toml, when it is not the default."
          error={fieldError("configPath")}
        >
          <Input id={`${prefix}-config`} value={configPath} onChange={(e) => setConfigPath(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label="Hub address"
          htmlFor={`${prefix}-addr`}
          help="Optional listen address of the host's hub, when it is not the default."
          error={fieldError("addr")}
        >
          <Input id={`${prefix}-addr`} value={addr} onChange={(e) => setAddr(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label="Roots"
          htmlFor={`${prefix}-roots`}
          help="Optional directories on the host to serve. One per line."
          error={fieldError("roots")}
        >
          <Textarea id={`${prefix}-roots`} value={roots} onChange={(e) => setRoots(e.target.value)} rows={3} />
        </FormRow>
        {formError !== null && (
          <p className={CLASS.formError} role="alert">
            {formError}
          </p>
        )}
      </div>
    </Dialog>
  );
}
```

Keep `CLASS.form`/`CLASS.formError`, and drop the now-unused `CLASS.error` entry only if nothing else uses it (check with the compiler).

- [ ] **Step 8: Run the pane and store tests**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/settings/sections/hosts.test.tsx src/stores/hosts.test.ts src/panes/settings/sections.test.ts`
Expected: PASS. `sections.test.ts` is the settings dispatcher's own suite; it must keep passing unchanged.

- [ ] **Step 9: Run the frontend gate**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/stores/hosts.ts src/stores/hosts.test.ts src/panes/settings/sections/hosts.tsx src/panes/settings/sections/hosts.test.tsx
cd <worktree root>
make test-web
```

Expected: PASS (biome, typecheck, vitest).

- [ ] **Step 10: Commit**

```bash
git add cmd/evener-hub/frontend/src/stores/hosts.ts cmd/evener-hub/frontend/src/stores/hosts.test.ts \
  cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx cmd/evener-hub/frontend/src/panes/settings/sections/hosts.test.tsx
git commit -m "feat(web): add the host Add/Edit dialog and the update store call"
```

---

## Final gate (after the last task)

- [ ] `go build ./...` — clean.
- [ ] `gofmt -l appwire cmd/evener-hub | tee /tmp/gofmt.out` — empty.
- [ ] `go test ./appwire/ ./cmd/evener-hub/ ./cmd/evener-hub/internal/hostreg/ ./cmd/evener-hub/internal/sshconn/ ./cmd/evener-hub/internal/appsource/ ./cmd/evener-hub/internal/hubcore/` — PASS.
- [ ] `make lint-generated` — PASS (the regenerated client and protocol doc are committed).
- [ ] `make vet` and `make lint` — run them; CI is the suite of record for the full matrix, but these two are the gates that catch naming, tagged-compile floors, and secret-scan problems early.
- [ ] `make test-web` — PASS.
- [ ] Criterion 15's verification item (no new code): with the built hub, add a host, Connect it, start a session on it, and confirm no discovery call bypasses `evener/host/request` (`rg 'host/request' cmd/evener-hub | rg -v _test` while watching the flow; the proxy is the only path). Record the observation in the PR description.

## Self-review: spec §7 coverage

| §7 criterion | Where it is pinned |
| --- | --- |
| 1 each mutable field round-trips, row prefills, pane repaints | T5 `TestHostManageUpdateRoundTripsEveryMutableField`; T7 store comparator test + pane dialog test |
| 2 sidecar is the durable record, no hub.toml write | T5 `...RoundTripsEveryMutableField` (fresh boot + hub.toml bytes) |
| 3 `name` cannot change, no name input | T5 `...RefusalsCommitNothing` (target only); T7 `no name input` assertion |
| 4 hub.toml name refused with the edit-the-file message | T5 `...RefusalsCommitNothing` |
| 5 unknown name refused, nothing written | T5 `...RefusalsCommitNothing` (tombstone-only is not constructible yet — spec §7.5) |
| 6 every update advances the generation; a capture stops matching | T1; T2; T5 `...AdvancesTheRegistryGeneration` |
| 7 attached host edited: channel drops, row offline, Connect re-attaches the edited entry | T2 `TestUpdateHostDropsTheChannel`, `TestEnsureAfterUpdateDialsTheEditedEntry`; T5 `TestHostManageUpdateLeavesTheRowOffline` (gate contention, no sleeps) |
| 8 every edit drops the channel; stale attach error and facts gone | T2 `TestUpdateHostDropsTheChannel`; T6 `TestHostManageUpdateClearsTheAttachRecord` |
| 9 a parked `Ensure` refuses across the swap; update and remove cannot both commit | T2 `TestUpdateHostRacesEnsureAtGate`; T5 window test (same mark, same gate) |
| 10 roots edit drops rows + retention then re-registers; non-roots leaves both | T6 `TestHostManageUpdateReregistersTheSourceOnlyWhenRootsChange` |
| 11 per-field validation lands on the right field in wire spelling | T4 `TestHostManageValidationRefusalsNameTheInput`; T5 `...ValidatesBeforeWriting`; T7 field-placing test |
| 12 invalid entry never reaches the sidecar; padded input stored trimmed | T5 `TestHostManageUpdateValidatesBeforeWriting` |
| 13 no regression to slice 1 (list/status never dial; Connect/remove unchanged; keyPath round-trips; add tests move with the reshape) | T3 step 7 (the whole hub host-manage suite); T5 rounds the key path; T2 `TestUpdateHostDoesNotDial` |
| 14 a mid-update name refuses add, remove, and a second update with the same Conflict | T5 `TestHostManageUpdateWindowFencesTheNameAndKeepsConcurrentCommits` |
| 15 an edit never dials, deploys, or attaches | T2 `TestUpdateHostDoesNotDial`; T5 `...LeavesTheRowOffline` |
| 16 failed live phase leaves file, store row, and registry on the old entry; retry succeeds | T5 `TestHostManageUpdateRollsBackWhenTheLivePhaseFails` |
| 17 upstream edges preserved; file order unchanged | T1 `TestRegistryUpdatePreservesUpstreamEdges`; T5 `TestHostManageUpdateKeepsTheFileOrder` |

**Deliberate deviations from the spec's §9 file list**, to call out in the PR: `hosts.module.css` is untouched (the dialog reuses `.form`, and the Dialog panel already scrolls), and the update tests live in a new `app_host_manage_update_test.go` rather than inside `app_host_manage_test.go`.
