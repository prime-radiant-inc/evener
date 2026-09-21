package hub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
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

// TestHostManageUpdateCannotRename pins the no-rename invariant (criterion 3):
// an update's target is HostUpdateParams.Name, the request's one immutable
// field, and the entry's own Name is deliberately never read. So a request that
// addresses "side" while its entry carries Name "other" is an ordinary edit of
// side — nothing is re-keyed — rather than a rename. A later change that started
// reading params.Entry.Name would silently move the host to the entry's name
// and leave the addressed name stale, with every other test still green.
func TestHostManageUpdateCannotRename(t *testing.T) {
	f := newUpdateFixture(t)
	resp, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Name: "other", Address: "edited.example"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// The response row still describes the addressed host, not a renamed one,
	// and the removal/rename discriminator is not set on an ordinary edit.
	if resp.Host.Name != "side" || resp.Host.Address != "edited.example" {
		t.Fatalf("response row = %+v, want side with the edited address", resp.Host)
	}
	if resp.Host.Removed {
		t.Fatalf("update row = %+v, want no removed/rename discriminator", resp.Host)
	}
	// The durable sidecar file still holds side (edited), never other.
	assertSidecarNames(t, f.configPath, "side")
	reloaded, err := loadHostSidecar(sidecarPathFor(f.configPath))
	if err != nil {
		t.Fatalf("reload sidecar: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].Name != "side" || reloaded[0].SSH != "edited.example" {
		t.Fatalf("reloaded sidecar = %+v, want side with the edited address only", reloaded)
	}
	// The in-memory sidecar store still holds side (edited), never other.
	stored := f.m.cfg.sidecar.snapshot()
	if len(stored) != 1 || stored[0].Name != "side" || stored[0].SSH != "edited.example" {
		t.Fatalf("sidecar store = %+v, want side with the edited address only", stored)
	}
	if f.m.cfg.sidecar.isSidecar("other") {
		t.Fatal("a rename put the entry's name into the sidecar store")
	}
	// The live registry still holds side (edited), never other.
	live, ok := f.hosts.Get("side")
	if !ok || live.SSH != "edited.example" {
		t.Fatalf("live registry entry = %+v (present %v), want side with the edited address", live, ok)
	}
	if _, ok := f.hosts.Get("other"); ok {
		t.Fatal("a rename registered the entry's name in the live registry")
	}
	// The registry holds no registration under the entry's name, asked through
	// the registry's own predicate rather than guessed at internally.
	if f.hosts.SameRegistration("other", hostreg.Host{Name: "other"}) {
		t.Fatal("the registry matched a registration under the entry's name")
	}
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

// TestHostManageUpdateRefusalPrecedence pins the commit phase's refusal order
// against an invalid entry: the target's own refusal wins over the entry's shape
// refusal. An empty address is a field refusal on its own, so each case below
// would get that generic refusal if the entry were validated first — the old
// order. The surface specifies the target refusal instead: a hub.toml-declared
// name is the edit-the-file refusal, an unknown name is not found, and a name
// with a mutation in flight is the conflict, whatever the entry carries.
func TestHostManageUpdateRefusalPrecedence(t *testing.T) {
	f := newUpdateFixture(t, hostreg.Host{Name: "toml", SSH: "toml.example"})
	// The invalid entry: an empty address, which ValidateEntry alone refuses as a
	// field error. It must not be what these calls are told.
	invalid := appwire.HostEntry{Address: ""}

	assertTargetRefusal := func(name string, err error, want string) {
		t.Helper()
		if err == nil {
			t.Fatalf("Update(%s, invalid entry) committed, want a refusal", name)
		}
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("refusal for %s = %v, want a WireError", name, err)
		}
		if _, isField := wire.Data.(appwire.HostFieldErrorData); isField {
			t.Fatalf("refusal data for %s = %#v, want the target refusal, not a field blame", name, wire.Data)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal for %s = %v, want it to contain %q", name, err, want)
		}
	}

	// hub.toml-declared name: the edit-the-file refusal, not the field refusal.
	_, err := f.m.Update(context.Background(), appwire.HostUpdateParams{Name: "toml", Entry: invalid})
	assertTargetRefusal("toml", err, "hub.toml")

	// Unknown name: not found, not the field refusal.
	_, err = f.m.Update(context.Background(), appwire.HostUpdateParams{Name: "nope", Entry: invalid})
	assertTargetRefusal("nope", err, "unknown host")

	// A live sidecar name with a mutation in flight: the conflict, not the field
	// refusal. Take the mark the update path takes, in the same commit-phase hold.
	f.m.cfg.mu.Lock()
	f.m.markMutating("side")
	f.m.cfg.mu.Unlock()
	_, err = f.m.Update(context.Background(), appwire.HostUpdateParams{Name: "side", Entry: invalid})
	assertTargetRefusal("side", err, "a mutation is already in progress")
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("in-flight refusal = %v, want the conflict code", err)
	}
	f.m.cfg.mu.Lock()
	f.m.unmarkMutating("side")
	f.m.cfg.mu.Unlock()
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

// TestHostManageUpdateMintsOneGenerationWithNoManager pins the invariant the
// hub-level generation test missed: with no sshconn manager wired, a single edit
// swaps the registry entry exactly once, so the entry advances by exactly one
// generation and the retired mark is the pre-swap generation. A double swap
// mints two generations — and marks the intermediate one, not the identity the
// swap actually replaced — so the exact step and the exact mark are both
// asserted.
func TestHostManageUpdateMintsOneGenerationWithNoManager(t *testing.T) {
	f := newUpdateFixture(t)
	if f.m.cfg.manager != nil {
		t.Fatal("the fixture wired a manager; this test pins the no-manager path")
	}
	before, ok := f.hosts.Get("side")
	if !ok {
		t.Fatal("side not registered before the update")
	}
	if _, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "edited.example"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	after, ok := f.hosts.Get("side")
	if !ok {
		t.Fatal("the update dropped the registry entry")
	}
	if after.Generation != before.Generation+1 {
		t.Fatalf("generation = %d after editing %d, want exactly one advance to %d", after.Generation, before.Generation, before.Generation+1)
	}
	f.m.cfg.state.mu.Lock()
	rec := f.m.cfg.state.records["side"]
	var mark uint64
	if rec != nil {
		mark = rec.retiredThrough
	}
	f.m.cfg.state.mu.Unlock()
	if mark != before.Generation {
		t.Fatalf("retired mark = %d, want the pre-swap generation %d", mark, before.Generation)
	}
}

// TestHostManageUpdateRollsBackWhenTheLivePhaseFails pins criterion 16: a failed
// live phase leaves the file, the store row, and the live registry describing the
// old entry, and a retry of the same edit then succeeds.
func TestHostManageUpdateRollsBackWhenTheLivePhaseFails(t *testing.T) {
	f := newUpdateFixture(t)
	// A manager whose registry is a DIFFERENT, empty one: UpdateHost refuses
	// hostreg.ErrUnknownHost deterministically — the one seam that fails between
	// the durable save and the live swap.
	otherReg, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(otherReg, sshconn.Options{})
	t.Cleanup(func() { _ = manager.Close() })
	// Add with the manager unwired so the entry lands in the live registry the
	// commit reads; wiring the different empty manager only now makes the live
	// phase fail after that durable commit, which is the rollback seam under test.
	f.m.cfg.manager = manager
	before, _ := f.hosts.Get("side")

	if _, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "edited.example"},
	}); err == nil {
		t.Fatal("Update over a live seam that refuses succeeded, want the failure")
	}

	// The live registry, the store row, and the file all still describe the old
	// entry.
	live, ok := f.hosts.Get("side")
	if !ok || !live.Equal(before) || live.Generation != before.Generation {
		t.Fatalf("live entry after the failed edit = %+v, want %+v", live, before)
	}
	stored := f.m.cfg.sidecar.snapshot()
	if len(stored) != 1 || stored[0].SSH != "side.example" {
		t.Fatalf("store rows after the failed edit = %+v, want the old entry", stored)
	}
	onDisk, err := loadHostSidecar(sidecarPathFor(f.configPath))
	if err != nil {
		t.Fatalf("reload sidecar: %v", err)
	}
	if len(onDisk) != 1 || onDisk[0].SSH != "side.example" {
		t.Fatalf("sidecar after the failed edit = %+v, want the old entry", onDisk)
	}

	// The retry lands, from a fresh boot over the same config with a live seam
	// that works: nothing was half-applied.
	boot := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, f.configPath, nil, nil)
	if _, err := boot.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "edited.example"},
	}); err != nil {
		t.Fatalf("retry after the rollback: %v", err)
	}
	reloaded, err := loadHostSidecar(sidecarPathFor(f.configPath))
	if err != nil {
		t.Fatalf("reload sidecar after the retry: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].SSH != "edited.example" {
		t.Fatalf("sidecar after the retry = %+v, want the edited entry", reloaded)
	}
}

// TestHostManageUpdateRollsBackWhenTheLiveEntryVanishes drives the defensive
// finish arm that only a directly driven registry can reach: UpdateHost commits
// its swap, and a directly driven registry then removes that new entry inside the
// same gate hold — after the committed swap and the caller's retirement, and so
// before the hub's gate-free finish-phase reread. The failed hub update must
// remove its committed store row and file entry too, so re-adding the name cannot
// persist a duplicate.
func TestHostManageUpdateRollsBackWhenTheLiveEntryVanishes(t *testing.T) {
	f := newUpdateFixture(t)
	removed := make(chan error, 1)
	manager := sshconn.New(f.hosts, sshconn.Options{
		Runner:  &attachedUpdateRunner{},
		OnEvent: func(ev sshconn.Event) { f.m.observeEvent(ev) },
		// The removal lands inside UpdateHost's gate hold, after the committed swap
		// and the caller's retirement, and therefore before the hub's gate-free
		// finish-phase reread: drive the registry directly, bypassing the hub
		// mutation mark, to make that reread observe the vanished live entry. The
		// seam is the window itself rather than a sleep-biased race, so the
		// interleaving is exact.
		AfterUpdateHostSwap: func(name string) {
			removed <- f.hosts.Remove(name)
		},
	})
	t.Cleanup(func() { _ = manager.Close() })
	f.m.cfg.manager = manager
	if _, err := manager.Ensure(context.Background(), "side"); err != nil {
		t.Fatalf("Ensure before Update: %v", err)
	}

	_, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "edited.example"},
	})
	if err == nil {
		t.Fatal("Update whose live entry vanished succeeded, want the defensive refusal")
	}
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if removeErr := <-removed; removeErr != nil {
		t.Fatalf("direct registry removal: %v", removeErr)
	}
	if _, ok := f.hosts.Get("side"); ok {
		t.Fatal("the directly removed live entry reappeared")
	}
	if stored := f.m.cfg.sidecar.snapshot(); len(stored) != 0 {
		t.Fatalf("store rows after the failed edit = %+v, want the empty live set", stored)
	}
	onDisk, loadErr := loadHostSidecar(sidecarPathFor(f.configPath))
	if loadErr != nil {
		t.Fatalf("reload sidecar after the failed edit: %v", loadErr)
	}
	if len(onDisk) != 0 {
		t.Fatalf("sidecar after the failed edit = %+v, want the empty live set", onDisk)
	}

	if _, addErr := f.m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "side", Address: "fresh.example"},
	}); addErr != nil {
		t.Fatalf("re-add after the failed edit: %v", addErr)
	}
	reloaded, loadErr := loadHostSidecar(sidecarPathFor(f.configPath))
	if loadErr != nil {
		t.Fatalf("reload sidecar after the re-add: %v", loadErr)
	}
	if len(reloaded) != 1 || reloaded[0].Name != "side" || reloaded[0].SSH != "fresh.example" {
		t.Fatalf("sidecar after the re-add = %+v, want one fresh entry", reloaded)
	}
}

// TestHostManageUpdateRollsBackAsARemovalWhenTheLiveEntryVanishes pins the
// live-failure arm's absent-entry case, which only a directly driven registry
// reaches: the edit's commit has landed, its live phase is parked on the gate a
// supervisor holds, and the registry drops the name before UpdateHost runs. The
// live phase then fails with hostreg.ErrUnknownHost *and* the live set has no
// entry to restore, so the un-commit must be a removal — the committed store
// row and the file entry go too. Restoring the edited row instead leaves an edit
// for a name that is not live, which a later Add duplicates and the next sidecar
// load rejects. The name's derived state goes with the row exactly as the
// success-side vanished arm retires it: leaving the source registration, the
// attach record, the cache generation, or the retained list behind keeps facts
// for a name the live registry no longer holds, and a later Add reuses the stale
// source instance.
func TestHostManageUpdateRollsBackAsARemovalWhenTheLiveEntryVanishes(t *testing.T) {
	pu := startParkedUpdate(t, "side")
	// The commit landed (the helper waited on the file) and the live phase is
	// parked on the gate the Ensure holds. Drive the registry directly, bypassing
	// the hub's mark, so UpdateHost's own update observes the vanished entry.
	pu.m.cfg.mu.Lock()
	_, present := pu.m.cfg.hosts.Get("side")
	pu.m.cfg.mu.Unlock()
	if !present {
		t.Fatal("the live entry vanished before the test could drop it")
	}
	// Seed the derived state a lifecycle event and an attached row would leave:
	// a name-keyed attach record, plus the fixture's own cache generation and
	// source registration for the name. The assertions below prove the rollback
	// takes all of them with the dropped row.
	seeded, _ := pu.m.cfg.hosts.Get("side")
	pu.m.cfg.mu.Lock()
	pu.m.cfg.state.recordKnown(appwire.HostRow{
		Name: "side", Attached: true, ServerName: "remote-hub", ServerVersion: "0.1.0",
	}, hostFactsValidity{handshake: true}, seeded.Generation)
	pu.m.cfg.mu.Unlock()
	if _, ok := pu.cache.SourceGeneration("side"); !ok {
		t.Fatal("the fixture host's source has no cache generation to retire")
	}
	if _, ok := pu.sources.Source("side"); !ok {
		t.Fatal("the fixture host has no source registration to retire")
	}
	if err := pu.m.cfg.hosts.Remove("side"); err != nil {
		t.Fatalf("direct registry removal: %v", err)
	}
	pu.release()
	if err := <-pu.ensureDone; err == nil {
		t.Fatal("the parked Ensure succeeded; the blocking runner must fail the probe")
	}
	done := pu.waitUpdateDone(t)
	if done.err == nil {
		t.Fatal("Update whose live entry vanished succeeded, want the un-commit")
	}
	if _, ok := pu.m.cfg.hosts.Get("side"); ok {
		t.Fatal("the directly removed live entry reappeared")
	}
	// The store holds the other names the window committed ("keep"); the vanished
	// name must be gone, not just restored to its edit.
	for _, e := range pu.m.cfg.sidecar.snapshot() {
		if e.Name == "side" {
			t.Fatalf("store still holds %q after the failed edit = %+v", e.Name, pu.m.cfg.sidecar.snapshot())
		}
	}
	onDisk, err := loadHostSidecar(sidecarPathFor(pu.configPath))
	if err != nil {
		t.Fatalf("reload sidecar after the failed edit: %v", err)
	}
	for _, e := range onDisk {
		if e.Name == "side" {
			t.Fatalf("sidecar still holds %q after the failed edit = %+v", e.Name, onDisk)
		}
	}
	// The derived state is gone too, exactly as the success-side vanished arm
	// retires it and in the same order. The cache and the retention hook are the
	// fixture's own seams (startParkedUpdate's RemoteThreadCache and
	// forgottenSources), observed the way the success arm's test observes them.
	if _, ok := pu.sources.Source("side"); ok {
		t.Fatal("the vanished host's source registration survived the failed edit")
	}
	if _, ok := pu.cache.SourceGeneration("side"); ok {
		t.Fatal("the vanished host's remote-thread cache entry survived the failed edit")
	}
	if got := pu.forgotten.snapshot(); !slices.Equal(got, []string{"side"}) {
		t.Fatalf("forgotten = %v, want the vanished host's own last-known-good drop", got)
	}
	pu.m.cfg.state.mu.Lock()
	_, hasRecord := pu.m.cfg.state.records["side"]
	pu.m.cfg.state.mu.Unlock()
	if hasRecord {
		t.Fatal("the vanished host's attach record survived the failed edit")
	}
}

// TestHostManageUpdateVanishedEntryRetiresDerivedState pins the finish-phase
// vanished arm's cleanup: when a directly driven registry drops the name inside
// the same gate hold as a successful live phase — after the committed swap and the
// retirement, before the hub rereads the live entry — the committed store row and
// file entry go, and so does the name's derived state, exactly as Remove retires
// it: the source registration, the name-keyed attach record, the remote-thread
// cache entry (generation included), and the retained last-known-good list.
// Pre-fix the arm dropped only the store row, so the tree kept rendering sessions
// for a name the live registry no longer had.
func TestHostManageUpdateVanishedEntryRetiresDerivedState(t *testing.T) {
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
	// The cache and the retention hook are the fixture's own seams: a real
	// RemoteThreadCache records a generation per source and lets RemoveSource
	// drop it, and forgetLastGoodThreads is the web server's retention seam.
	cache := &hubcore.RemoteThreadCache{}
	var forgotten []string
	m := newHubHostManager(sources, nil, hubcore.WebConfig{RemoteThreadCache: cache}, configPath, hosts, nil)
	m.cfg.forgetLastGoodThreads = func(sourceID string) { forgotten = append(forgotten, sourceID) }
	if _, err := m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "side", Address: "side.example"},
	}); err != nil {
		t.Fatalf("Add(side): %v", err)
	}
	if _, ok := sources.Source("side"); !ok {
		t.Fatal("the added host has no source")
	}
	if _, ok := cache.SourceGeneration("side"); !ok {
		t.Fatal("the added host's source has no cache generation")
	}
	// Retain an attach record the way a lifecycle event and an attached row do.
	seeded, _ := m.cfg.hosts.Get("side")
	m.cfg.mu.Lock()
	m.cfg.state.recordKnown(appwire.HostRow{
		Name: "side", Attached: true, ServerName: "remote-hub", ServerVersion: "0.1.0",
	}, hostFactsValidity{handshake: true}, seeded.Generation)
	m.cfg.mu.Unlock()

	// Drive the finish-phase vanished arm: the live phase commits its swap, then a
	// directly driven registry change removes the new entry inside the same gate
	// hold — after the committed swap and the retirement, before the hub's
	// gate-free finish-phase reread — bypassing the hub mutation mark.
	removed := make(chan error, 1)
	manager := sshconn.New(hosts, sshconn.Options{
		Runner:  &attachedUpdateRunner{},
		OnEvent: func(ev sshconn.Event) { m.observeEvent(ev) },
		AfterUpdateHostSwap: func(name string) {
			removed <- hosts.Remove(name)
		},
	})
	t.Cleanup(func() { _ = manager.Close() })
	m.cfg.manager = manager
	if _, err := manager.Ensure(context.Background(), "side"); err != nil {
		t.Fatalf("Ensure before Update: %v", err)
	}

	_, err = m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "edited.example"},
	})
	if err == nil {
		t.Fatal("Update whose live entry vanished succeeded, want the defensive refusal")
	}
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if removeErr := <-removed; removeErr != nil {
		t.Fatalf("direct registry removal: %v", removeErr)
	}
	if stored := m.cfg.sidecar.snapshot(); len(stored) != 0 {
		t.Fatalf("store rows after the vanished edit = %+v, want the empty live set", stored)
	}
	// The derived state is retired exactly as Remove retires it.
	if _, ok := sources.Source("side"); ok {
		t.Fatal("the vanished host still has a source registration")
	}
	if _, ok := cache.SourceGeneration("side"); ok {
		t.Fatal("the vanished host's remote-thread cache entry survived")
	}
	if !slices.Equal(forgotten, []string{"side"}) {
		t.Fatalf("forgotten = %v, want the vanished host's own last-known-good drop", forgotten)
	}
	m.cfg.state.mu.Lock()
	_, hasRecord := m.cfg.state.records["side"]
	m.cfg.state.mu.Unlock()
	if hasRecord {
		t.Fatal("the vanished host's attach record survived")
	}
}

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
	cache      *hubcore.RemoteThreadCache
	forgotten  *forgottenSources
	configPath string
	runner     *blockingRunner
	release    func()
	ensureDone chan error
	updateDone chan updateOutcome
}

// forgottenSources records the names the retention seam was asked to drop. The
// seam runs on the update's own goroutine while the test reads after
// waitUpdateDone, so the mutex keeps the read race-free.
type forgottenSources struct {
	mu    sync.Mutex
	names []string
}

func (f *forgottenSources) forget(sourceID string) {
	f.mu.Lock()
	f.names = append(f.names, sourceID)
	f.mu.Unlock()
}

func (f *forgottenSources) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.names)
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
	// The cache and the retention hook are the fixture's own derived-state
	// seams: a real RemoteThreadCache mints a generation per source at
	// registration (registerRemoteHubSource) and lets RemoveSource drop it, and
	// forgetLastGoodThreads stands in for the web server's retained
	// last-known-good list. Both are wired here so the removal/rollback arms that
	// retire derived state can be observed from the tests that drive them.
	cache := &hubcore.RemoteThreadCache{}
	forgotten := &forgottenSources{}
	m := newHubHostManager(sources, manager, hubcore.WebConfig{RemoteThreadCache: cache}, configPath, reg, nil)
	m.cfg.forgetLastGoodThreads = forgotten.forget
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
		cache:      cache,
		forgotten:  forgotten,
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

// TestHostManageUpdateClearsFactsRecordedWhileWaitingForTheGate pins the
// swap-before-clear ordering: while UpdateHost waits, a row built from the old
// registration may still honestly retain its attached facts. Once the registry
// swaps, that old registration is fenced out and the final clear must remove the
// facts it recorded during the wait.
func TestHostManageUpdateClearsFactsRecordedWhileWaitingForTheGate(t *testing.T) {
	pu := startParkedUpdate(t, "side")

	// Simulate the fenced fold of a concurrent row built from the still-current
	// old entry. observeEvent and recordKnown are the same seams lifecycle events
	// and attached hostRow calls use to repopulate the name-keyed record; the row
	// carries the still-current old entry's generation, exactly as a row built
	// before the swap would.
	preSwap, _ := pu.m.cfg.hosts.Get("side")
	pu.m.cfg.mu.Lock()
	pu.m.cfg.state.recordKnown(appwire.HostRow{
		Name: "side", Attached: true, ServerName: "old-server", ServerVersion: "0.1.0",
		HubVersion: "9.9.9", OS: "linux", Arch: "arm64",
	}, hostFactsValidity{handshake: true, facts: true}, preSwap.Generation)
	pu.m.cfg.mu.Unlock()
	pu.m.observeEvent(sshconn.Event{Host: "side", Kind: sshconn.EventFailed, Err: errors.New("old attach failed")})

	pu.release()
	if err := <-pu.ensureDone; err == nil {
		t.Fatal("the parked Ensure succeeded; the blocking runner must fail the probe")
	}
	done := pu.waitUpdateDone(t)
	if done.err != nil {
		t.Fatalf("Update: %v", done.err)
	}
	row := done.resp.Host
	if row.Name != "side" || row.Address != "edited.example" || row.Origin != hostOriginSidecar {
		t.Fatalf("finished row = %+v, want the edited configured entry", row)
	}
	if row.ServerName != "" || row.ServerVersion != "" || row.HubVersion != "" || row.OS != "" || row.Arch != "" {
		t.Fatalf("finished row = %+v, want no facts retained from the old identity", row)
	}
	if row.LastAttachErr != "" {
		t.Fatalf("finished row = %+v, want no attach error retained from the old identity", row)
	}
}

// attachedUpdateStdio is one in-memory SSH child stream. Its peer serves the
// AppWire initialize handshake, so Manager.Ensure publishes a real Channel that
// the hub's Update path must retire.
type attachedUpdateStdio struct {
	conn net.Conn
	done chan struct{}
	once sync.Once
}

func (s *attachedUpdateStdio) Stdin() io.WriteCloser { return s.conn }
func (s *attachedUpdateStdio) Stdout() io.ReadCloser { return s.conn }
func (s *attachedUpdateStdio) Kill() error {
	s.once.Do(func() {
		_ = s.conn.Close()
		close(s.done)
	})
	return nil
}
func (s *attachedUpdateStdio) Wait() error {
	<-s.done
	return nil
}

// attachedUpdateRunner answers the normal preflight as an already-healthy dev
// hub, then serves initialize over an in-memory stream. It never starts a real
// process or opens a network listener.
type attachedUpdateRunner struct{}

func (*attachedUpdateRunner) Run(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
	joined := strings.Join(argv, " ")
	switch {
	case strings.HasSuffix(joined, "uname -s"):
		return []byte("Linux\n"), nil
	case strings.HasSuffix(joined, "uname -m"):
		return []byte("x86_64\n"), nil
	case strings.Contains(joined, "XDG_STATE_HOME"):
		return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
	case strings.HasSuffix(joined, "id -u"):
		return []byte("1000\n"), nil
	case strings.Contains(joined, "launch-check"):
		return []byte(`{"protocol":"evener-appwire-v5","version":"dev","launch_flags":["api-log"]}`), nil
	case strings.Contains(joined, "api/health"):
		return []byte(`{"version":"dev","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
	default:
		return nil, errors.New("attachedUpdateRunner: unexpected remote command")
	}
}

func (*attachedUpdateRunner) Start(_ context.Context, _ []string, _ io.Writer) (sshconn.Stdio, error) {
	client, server := net.Pipe()
	stdio := &attachedUpdateStdio{conn: client, done: make(chan struct{})}
	go func() {
		transport := appwire.NewStreamTransport(server)
		defer transport.Close()
		for {
			msg, err := transport.Recv(context.Background())
			if err != nil {
				return
			}
			if msg.Request != nil && msg.Request.Method == appwire.MethodInitialize {
				resp := appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
				if err := transport.Send(context.Background(), appwire.ResponseMessage(msg.Request.ID, resp)); err != nil {
					return
				}
			}
		}
	}()
	return stdio, nil
}

// TestHostManageUpdateDropsAnAttachedChannel proves the manager-backed live
// phase is wired through the hub path: a real attached channel exists before
// Update and is gone when Update returns. The offline-row test above alone
// cannot prove this because its parked Ensure deliberately fails before attach.
func TestHostManageUpdateDropsAnAttachedChannel(t *testing.T) {
	f := newUpdateFixture(t)
	runner := &attachedUpdateRunner{}
	manager := sshconn.New(f.hosts, sshconn.Options{Runner: runner})
	t.Cleanup(func() { _ = manager.Close() })
	f.m.cfg.manager = manager
	f.m.cfg.online = manager.Attached
	f.m.cfg.clientIfAttached = manager.ClientIfAttached

	if _, err := manager.Ensure(context.Background(), "side"); err != nil {
		t.Fatalf("Ensure before Update: %v", err)
	}
	if !manager.Attached("side") {
		t.Fatal("host is offline before Update; the test did not establish the channel")
	}
	if _, ok := manager.ClientIfAttached("side"); !ok {
		t.Fatal("ClientIfAttached before Update = false, want a live channel")
	}

	if _, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name:  "side",
		Entry: appwire.HostEntry{Address: "edited.example"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if manager.Attached("side") {
		t.Fatal("Attached after hub Update = true, want the channel retired")
	}
	if _, ok := manager.ClientIfAttached("side"); ok {
		t.Fatal("ClientIfAttached after hub Update = true, want no live channel")
	}
	row, err := f.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status after Update: %v", err)
	}
	if row.Host.Attached || row.Host.Address != "edited.example" {
		t.Fatalf("row after Update = %+v, want the edited host offline", row.Host)
	}
}

// TestHostManageUpdateClearsTheAttachRecord pins criterion 8's offline half: an
// edit retires the identity the retained attach facts belong to, so the retiring
// record — its attach error and its last-known facts — does not outlive the entry
// it describes. No teardown clears it for an offline host whose address changed;
// this is the one live effect the edit performs itself.
func TestHostManageUpdateClearsTheAttachRecord(t *testing.T) {
	f := newUpdateFixture(t)
	// Retain state the way the manager's lifecycle events and attached rows do.
	// The failure carries the error the row renders; the state event that follows
	// it is what marks the host in-progress without clearing that error, so the
	// record carries both halves the edit must retire. Seeding the failure alone
	// would leave midAttach false, and the post-edit mid-attach assertion below
	// could then never fail.
	f.m.observeEvent(sshconn.Event{Host: "side", Kind: sshconn.EventFailed, Err: errors.New("dial refused")})
	f.m.observeEvent(sshconn.Event{Host: "side", Kind: sshconn.EventState, State: sshconn.StateAttaching})
	live, _ := f.hosts.Get("side")
	f.m.cfg.mu.Lock()
	f.m.cfg.state.recordKnown(appwire.HostRow{
		Name: "side", Attached: true, ServerName: "remote-hub", ServerVersion: "0.1.0",
		HubVersion: "9.9.9", OS: "linux", Arch: "arm64",
	}, hostFactsValidity{handshake: true, facts: true}, live.Generation)
	f.m.cfg.mu.Unlock()

	before, err := f.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !before.Host.MidAttach || before.Host.LastAttachErr != "dial refused" || before.Host.HubVersion != "9.9.9" {
		t.Fatalf("row before the edit = %+v, want the retained attach state: in-progress, its error, and its facts", before.Host)
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

// TestHostManageUpdateHoldsRetainedStateOutOfAnInFlightMutation pins the row
// interleaving the mutation mark exists for: an update has committed its swap
// (the new, higher-generation entry is visible) but has not yet run its
// retirement, with the mutation mutex released between the two. A concurrent,
// gate-free row that snapshots the new entry passes hostEntryCurrent and still
// sees gen > retiredThrough, so it would fold the retired identity's midAttach,
// lastAttachError, and last-known facts unless the name's in-flight mark fences
// it out. hostRow must therefore fold and record nothing while the mark is
// held. The mark's lifetime is what makes this exactly the window: it is taken
// in the update's commit phase and cleared in its finish phase (Update), so it
// spans the commit-to-retire gap and nothing beyond it — which is why clearing
// the mark must fold the retained state back, not mute it forever.
func TestHostManageUpdateHoldsRetainedStateOutOfAnInFlightMutation(t *testing.T) {
	f := newUpdateFixture(t)
	// Retain the identity state an edit must retire, the way the manager's
	// lifecycle events and attached rows do: a failure carrying the error, the
	// state event that marks the host in-progress without clearing it, and the
	// last-known facts of an attached row.
	f.m.observeEvent(sshconn.Event{Host: "side", Kind: sshconn.EventFailed, Err: errors.New("dial refused")})
	f.m.observeEvent(sshconn.Event{Host: "side", Kind: sshconn.EventState, State: sshconn.StateAttaching})
	live, _ := f.hosts.Get("side")
	f.m.cfg.mu.Lock()
	f.m.cfg.state.recordKnown(appwire.HostRow{
		Name: "side", Attached: true, ServerName: "remote-hub", ServerVersion: "0.1.0",
		HubVersion: "9.9.9", OS: "linux", Arch: "arm64",
	}, hostFactsValidity{handshake: true, facts: true}, live.Generation)
	// Take the mark the update path takes, in the same commit-phase hold.
	f.m.markMutating("side")
	f.m.cfg.mu.Unlock()

	held, err := f.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status with the mutation mark held: %v", err)
	}
	if held.Host.MidAttach || held.Host.LastAttachErr != "" || held.Host.ServerName != "" ||
		held.Host.ServerVersion != "" || held.Host.HubVersion != "" || held.Host.OS != "" || held.Host.Arch != "" {
		t.Fatalf("row = %+v, want none of the retained state while the name is marked in flight", held.Host)
	}

	// Clear the mark the way the finish phase does; the retained state must fold
	// back, proving the suppression is the window and not a permanent mute.
	f.m.cfg.mu.Lock()
	f.m.unmarkMutating("side")
	f.m.cfg.mu.Unlock()

	released, err := f.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status after the mark cleared: %v", err)
	}
	if !released.Host.MidAttach || released.Host.LastAttachErr != "dial refused" || released.Host.HubVersion != "9.9.9" {
		t.Fatalf("row = %+v, want the retained attach state folded back once the mark cleared", released.Host)
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

	// A roots edit: a fresh registration, the old identities and retention
	// dropped first, and the generation the re-registration minted still in place
	// afterwards.
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

// TestHostManageRootsEditCannotResurrectOldRetention holds the finish phase in
// the exact window after its retention clear and asks a completed old-source
// walk to store real rows. Both ownership configurations must reject the stale
// store: a cache generation when RemoteThreadCache is wired, and the source
// instance when it is absent. The channels hold the window deterministically;
// no scheduling sleep manufactures the race.
func TestHostManageRootsEditCannotResurrectOldRetention(t *testing.T) {
	for _, tt := range []struct {
		name      string
		withCache bool
	}{
		{name: "cache_wired", withCache: true},
		{name: "cache_absent", withCache: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
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
			var cache *hubcore.RemoteThreadCache
			if tt.withCache {
				cache = &hubcore.RemoteThreadCache{}
			}
			webCfg := hubcore.WebConfig{RemoteThreadCache: cache}
			m := newHubHostManager(sources, nil, webCfg, configPath, hosts, nil)
			if _, err := m.Add(context.Background(), appwire.HostAddParams{
				Entry: appwire.HostEntry{Name: "side", Address: "side.example", Roots: []string{"/old-root"}},
			}); err != nil {
				t.Fatalf("Add(side): %v", err)
			}

			oldSource, ok := sources.Source("side")
			if !ok {
				t.Fatal("the added host has no source")
			}
			var oldGeneration uint64
			capturedGeneration := false
			if cache != nil {
				oldGeneration, capturedGeneration = cache.SourceGeneration("side")
				if !capturedGeneration {
					t.Fatal("the added host's source has no cache generation")
				}
			}
			web := &WebServer{cfg: webCfg, sources: sources}
			oldRows := []appwire.Thread{{ID: "retired-root-session", Source: "side", CWD: "/old-root"}}
			web.storeLastGoodThreadsIfCurrent(oldSource, oldGeneration, capturedGeneration, oldRows)
			if got := web.lastGoodThreadsForSource("side"); len(got) != 1 || got[0].ID != oldRows[0].ID || got[0].CWD != oldRows[0].CWD {
				t.Fatalf("seeded retention = %+v, want the old source's real row %+v", got, oldRows)
			}

			forgotten := make(chan struct{})
			releaseFinish := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(releaseFinish) }) })
			m.cfg.forgetLastGoodThreads = func(sourceID string) {
				web.forgetLastGoodThreads(sourceID)
				close(forgotten)
				<-releaseFinish
			}

			updateDone := make(chan error, 1)
			go func() {
				_, err := m.Update(context.Background(), appwire.HostUpdateParams{
					Name:  "side",
					Entry: appwire.HostEntry{Address: "side.example", Roots: []string{"/new-root"}},
				})
				updateDone <- err
			}()
			<-forgotten
			if got := web.lastGoodThreadsForSource("side"); len(got) != 0 {
				t.Fatalf("retention at the held post-forget window = %+v, want none", got)
			}

			// Complete the old walk while the finish phase is held. The retirement
			// that makes this store stale must already have happened, or this write
			// can recreate rows that no later step clears.
			web.storeLastGoodThreadsIfCurrent(oldSource, oldGeneration, capturedGeneration, oldRows)
			releaseOnce.Do(func() { close(releaseFinish) })
			if err := <-updateDone; err != nil {
				t.Fatalf("Update: %v", err)
			}
			if got := web.lastGoodThreadsForSource("side"); len(got) != 0 {
				t.Fatalf("old-root retention survived the completed roots edit: %+v", got)
			}
		})
	}
}

// serveUpdateInitialize answers the AppWire initialize handshake on a runner's
// in-memory stream, so Manager.Ensure can publish a real channel without a
// process or a listener.
func serveUpdateInitialize(server net.Conn) {
	transport := appwire.NewStreamTransport(server)
	defer transport.Close()
	for {
		msg, err := transport.Recv(context.Background())
		if err != nil {
			return
		}
		if msg.Request != nil && msg.Request.Method == appwire.MethodInitialize {
			resp := appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
			if err := transport.Send(context.Background(), appwire.ResponseMessage(msg.Request.ID, resp)); err != nil {
				return
			}
		}
	}
}

// reapParkingStdio is one in-memory SSH child whose exit (Wait) parks on a test
// channel: Channel.Close kills it and then waits on the child's exit, so the
// reap runs to completion only when the test releases it. Kill signals entered,
// which UpdateHost reaches only after it has released the per-host gate.
type reapParkingStdio struct {
	conn        net.Conn
	done        chan struct{}
	once        sync.Once
	entered     chan struct{}
	waitRelease <-chan struct{}
}

func (s *reapParkingStdio) Stdin() io.WriteCloser { return s.conn }
func (s *reapParkingStdio) Stdout() io.ReadCloser { return s.conn }
func (s *reapParkingStdio) Kill() error {
	s.once.Do(func() {
		close(s.entered)
		_ = s.conn.Close()
		close(s.done)
	})
	return nil
}
func (s *reapParkingStdio) Wait() error {
	<-s.done
	<-s.waitRelease
	return nil
}

// reapParkingRunner drives the update-versus-attach race deterministically. Its
// first Start attaches a real in-memory channel and parks that channel's reap
// on reapRelease, so the update's teardown is observed with the per-host gate
// already released but UpdateHost not yet returned. A later Start parks the
// attach itself (after the manager has emitted StateAttaching) and keeps holding
// the per-host gate, so a new-identity Ensure stays mid-attach.
type reapParkingRunner struct {
	attachedUpdateRunner
	mu          sync.Mutex
	starts      int
	newOnce     sync.Once
	reapEntered chan struct{}
	reapRelease chan struct{}
	newEntered  chan struct{}
	newRelease  chan struct{}
}

func (r *reapParkingRunner) Start(_ context.Context, _ []string, _ io.Writer) (sshconn.Stdio, error) {
	r.mu.Lock()
	r.starts++
	n := r.starts
	r.mu.Unlock()
	if n == 1 {
		client, server := net.Pipe()
		stdio := &reapParkingStdio{
			conn: client, done: make(chan struct{}),
			entered: r.reapEntered, waitRelease: r.reapRelease,
		}
		go serveUpdateInitialize(server)
		return stdio, nil
	}
	r.newOnce.Do(func() { close(r.newEntered) })
	<-r.newRelease
	return nil, errors.New("reapParkingRunner: released")
}

// TestHostManageUpdateRetiresTheAttachRecordInsideTheSwapHold is the RED/GREEN
// test for the update-versus-attach race. The record clear is handed to the
// manager and runs inside the swap's own gate hold, so it happens before a
// new-identity attach can start. Pre-fix, the clear ran after UpdateHost
// returned — a window in which the new registration can already have attached
// and recorded its own mid-attach state — and erased that fresh state. The test
// holds UpdateHost in its reap (the gate provably free), drives a real
// new-identity Ensure into its attach so it records midAttach, then releases the
// reap and asserts the record still carries the new identity's state.
func TestHostManageUpdateRetiresTheAttachRecordInsideTheSwapHold(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	reg, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	runner := &reapParkingRunner{
		reapEntered: make(chan struct{}),
		reapRelease: make(chan struct{}),
		newEntered:  make(chan struct{}),
		newRelease:  make(chan struct{}),
	}
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, nil, hubcore.WebConfig{}, configPath, reg, nil)
	manager := sshconn.New(reg, sshconn.Options{
		Runner:  runner,
		OnEvent: func(ev sshconn.Event) { m.observeEvent(ev) },
	})
	t.Cleanup(func() { _ = manager.Close() })
	// The reap and the parked new-identity attach are released independently;
	// one sync.Once each, so releasing the reap cannot swallow the later release.
	// Registered after the manager's Close cleanup so it runs first (LIFO): Close
	// waits for the parked Ensure, so the parks must be released ahead of it.
	var reapOnce, newOnce sync.Once
	t.Cleanup(func() {
		reapOnce.Do(func() { close(runner.reapRelease) })
		newOnce.Do(func() { close(runner.newRelease) })
	})
	m.cfg.manager = manager

	if _, err := m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "side", Address: "side.example"},
	}); err != nil {
		t.Fatalf("Add(side): %v", err)
	}
	// Attach the original identity: the update must have a channel to reap.
	if _, err := manager.Ensure(context.Background(), "side"); err != nil {
		t.Fatalf("Ensure before Update: %v", err)
	}
	if !manager.Attached("side") {
		t.Fatal("host is offline before Update; the test did not establish the channel")
	}

	updateDone := make(chan updateOutcome, 1)
	go func() {
		resp, err := m.Update(context.Background(), appwire.HostUpdateParams{
			Name:  "side",
			Entry: appwire.HostEntry{Address: "edited.example"},
		})
		updateDone <- updateOutcome{resp: resp, err: err}
	}()

	// The update has swapped the entry, emitted the retired identity's Detached,
	// and released the gate: it is now parked in the reap.
	select {
	case <-runner.reapEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the update never reached its reap; the runner did not park the channel close")
	}

	// With the gate free, attach the NEW registration. The runner parks the
	// attach, so this Ensure stays mid-attach holding the gate and has recorded
	// StateAttaching's midAttach for the name.
	newEnsureDone := make(chan error, 1)
	go func() {
		_, err := manager.Ensure(context.Background(), "side")
		newEnsureDone <- err
	}()
	select {
	case <-runner.newEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the new-identity Ensure never reached its attach; the gate was not free")
	}

	// Release the reap: UpdateHost returns, and its post-return clear (pre-fix)
	// would wipe the just-recorded state.
	reapOnce.Do(func() { close(runner.reapRelease) })
	select {
	case done := <-updateDone:
		if done.err != nil {
			t.Fatalf("Update: %v", done.err)
		}
		if !done.resp.Host.MidAttach {
			t.Fatalf("update row = %+v, want the new identity's in-progress attach retained", done.resp.Host)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the update never returned after its reap was released")
	}
	row, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status after Update: %v", err)
	}
	if !row.Host.MidAttach {
		t.Fatalf("row = %+v, want the new identity's in-progress attach retained after Update", row.Host)
	}
	if row.Host.Address != "edited.example" {
		t.Fatalf("row = %+v, want the edited entry", row.Host)
	}

	newOnce.Do(func() { close(runner.newRelease) })
	if err := <-newEnsureDone; err == nil {
		t.Fatal("the parked new-identity Ensure succeeded; the runner must fail the released attach")
	}
}

// TestHostManageUpdateStaleRowCannotResurrectRetiredState pins the reviewer's
// interleaving exactly: a row built from the PRE-swap entry passes
// hostEntryCurrent before the swap, the swap advances the generation and its
// retire hook clears the record, and only THEN does the stale row reach its
// state writes. With the update parked in its reap, the swap and the hook have
// already run and the gate is free, so the stale write lands after the
// retirement — the window that TestHostManageUpdateClearsFactsRecordedWhile-
// WaitingForTheGate never places the write in (that test records before the
// swap). Pre-fix the retirement was a bare remove, so the stale recordKnown
// recreated the record from the retired identity and the stale apply folded
// its facts back; the generation-scoped retire fences both. The other half is
// asserted too: a row for the NEW generation still records and folds normally,
// so the mark cannot block the live identity.
func TestHostManageUpdateStaleRowCannotResurrectRetiredState(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	reg, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	runner := &reapParkingRunner{
		reapEntered: make(chan struct{}),
		reapRelease: make(chan struct{}),
		newEntered:  make(chan struct{}),
		newRelease:  make(chan struct{}),
	}
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, nil, hubcore.WebConfig{}, configPath, reg, nil)
	manager := sshconn.New(reg, sshconn.Options{
		Runner:  runner,
		OnEvent: func(ev sshconn.Event) { m.observeEvent(ev) },
	})
	t.Cleanup(func() { _ = manager.Close() })
	var reapOnce, newOnce sync.Once
	t.Cleanup(func() {
		reapOnce.Do(func() { close(runner.reapRelease) })
		newOnce.Do(func() { close(runner.newRelease) })
	})
	m.cfg.manager = manager

	if _, err := m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "side", Address: "side.example"},
	}); err != nil {
		t.Fatalf("Add(side): %v", err)
	}
	staleEntry, ok := reg.Get("side")
	if !ok {
		t.Fatal("side not registered before the update")
	}
	if _, err := manager.Ensure(context.Background(), "side"); err != nil {
		t.Fatalf("Ensure before Update: %v", err)
	}

	updateDone := make(chan updateOutcome, 1)
	go func() {
		resp, err := m.Update(context.Background(), appwire.HostUpdateParams{
			Name:  "side",
			Entry: appwire.HostEntry{Address: "edited.example"},
		})
		updateDone <- updateOutcome{resp: resp, err: err}
	}()
	// The update has swapped the entry and run its retire hook, then released the
	// gate and parked in the reap: the retirement is already done, and the gate is
	// free for the stale write to land.
	select {
	case <-runner.reapEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the update never reached its reap; the runner did not park the channel close")
	}

	newEntry, ok := reg.Get("side")
	if !ok {
		t.Fatal("the update dropped the registry entry")
	}
	if newEntry.Generation <= staleEntry.Generation {
		t.Fatalf("new generation = %d, want > the retired %d", newEntry.Generation, staleEntry.Generation)
	}

	// The stale late write: an ATTACHED row captured from the retired entry
	// reaches recordKnown after the retirement. Its generation is at or below the
	// retired mark, so it must write nothing back — pre-fix (a bare remove) it
	// recreated the record from the retired identity, which the edited host's
	// later offline rows then rendered as their own.
	staleAttached := appwire.HostRow{
		Name: "side", Attached: true, ServerName: "retired-hub", ServerVersion: "0.0.1",
		HubVersion: "9.9.9", OS: "linux", Arch: "arm64",
	}
	m.cfg.mu.Lock()
	m.cfg.state.recordKnown(staleAttached, hostFactsValidity{handshake: true, facts: true}, staleEntry.Generation)
	m.cfg.mu.Unlock()
	m.cfg.state.mu.Lock()
	rec := m.cfg.state.records["side"]
	var (
		known   appwire.HostRow
		errText string
		mark    uint64
	)
	if rec != nil {
		known = rec.known
		errText = rec.lastErr
		mark = rec.retiredThrough
	}
	m.cfg.state.mu.Unlock()
	if mark != staleEntry.Generation {
		t.Fatalf("retired mark = %d, want the retired generation %d", mark, staleEntry.Generation)
	}
	if known.ServerName != "" || known.ServerVersion != "" || known.HubVersion != "" || known.OS != "" || known.Arch != "" {
		t.Fatalf("record after the stale write = %+v, want the retired identity's facts gone", known)
	}
	if errText != "" {
		t.Fatalf("record attach error after the stale write = %q, want it empty", errText)
	}

	// An offline row built from the retired generation folds nothing either: the
	// offline row the edited host's later list/status renders must not carry the
	// retired facts.
	staleOffline := appwire.HostRow{Name: "side"}
	m.cfg.mu.Lock()
	m.cfg.state.apply(&staleOffline, staleEntry.Generation)
	m.cfg.mu.Unlock()
	if staleOffline.MidAttach || staleOffline.LastAttachErr != "" {
		t.Fatalf("stale offline row = %+v, want no retired attach state folded in", staleOffline)
	}
	if staleOffline.ServerName != "" || staleOffline.ServerVersion != "" || staleOffline.HubVersion != "" || staleOffline.OS != "" || staleOffline.Arch != "" {
		t.Fatalf("stale offline row = %+v, want no retired facts folded in", staleOffline)
	}

	// The mark must not block the live identity: a row for the new generation
	// still records and folds normally.
	freshAttached := appwire.HostRow{
		Name: "side", Attached: true, ServerName: "new-hub", ServerVersion: "1.0.0",
		HubVersion: "1.2.3", OS: "darwin", Arch: "arm64",
	}
	m.cfg.mu.Lock()
	m.cfg.state.recordKnown(freshAttached, hostFactsValidity{handshake: true, facts: true}, newEntry.Generation)
	freshOffline := appwire.HostRow{Name: "side"}
	m.cfg.state.apply(&freshOffline, newEntry.Generation)
	m.cfg.mu.Unlock()
	if freshOffline.ServerName != "new-hub" || freshOffline.ServerVersion != "1.0.0" ||
		freshOffline.HubVersion != "1.2.3" || freshOffline.OS != "darwin" || freshOffline.Arch != "arm64" {
		t.Fatalf("new-generation row = %+v, want its own facts retained and folded", freshOffline)
	}

	reapOnce.Do(func() { close(runner.reapRelease) })
	select {
	case done := <-updateDone:
		if done.err != nil {
			t.Fatalf("Update: %v", done.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the update never returned after its reap was released")
	}
}

// TestHostManageRemoveCarriesTheEffectiveEntryFields pins the removal response's
// shape: it carries the whole effective entry it removed — User, EvenerPath,
// ConfigPath, Addr, and a cloned Roots — not just the name/address/key subset
// slice 1 needed, so a removal row matches every other row for the same host.
// The Roots clone is asserted directly: mutating the response's slice must not
// reach the registry entry's.
func TestHostManageRemoveCarriesTheEffectiveEntryFields(t *testing.T) {
	f := newUpdateFixture(t)
	entry := appwire.HostEntry{
		Name: "rem", Address: "rem.example", User: "operator", KeyPath: "/keys/rem",
		EvenerPath: "/opt/evener", ConfigPath: "/etc/evener/hub.toml",
		Addr: "127.0.0.1:9180", Roots: []string{"/srv/one", "/srv/two"},
	}
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: entry}); err != nil {
		t.Fatalf("Add(rem): %v", err)
	}
	live, ok := f.hosts.Get("rem")
	if !ok {
		t.Fatal("rem not registered after Add")
	}
	resp, err := f.m.Remove(context.Background(), appwire.HostRemoveParams{Name: "rem"})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got := resp.Host
	if !got.Removed || got.Origin != hostOriginSidecar {
		t.Fatalf("removal row = %+v, want Removed with the sidecar origin", got)
	}
	if got.Name != "rem" || got.Address != entry.Address || got.User != entry.User ||
		got.KeyPath != entry.KeyPath || got.EvenerPath != entry.EvenerPath ||
		got.ConfigPath != entry.ConfigPath || got.Addr != entry.Addr {
		t.Fatalf("removal row = %+v, want the whole effective entry %+v", got, entry)
	}
	if !slices.Equal(got.Roots, entry.Roots) {
		t.Fatalf("removal row Roots = %v, want %v", got.Roots, entry.Roots)
	}
	if len(got.Roots) == 0 {
		t.Fatal("removal row carried no roots; the clone assertion below cannot prove independence")
	}
	got.Roots[0] = "/mutated"
	if live.Roots[0] != "/srv/one" {
		t.Fatalf("mutating the response Roots reached the registry entry: %v", live.Roots)
	}
}
