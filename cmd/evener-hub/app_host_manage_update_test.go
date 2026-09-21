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
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, hosts, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "side", Address: "side.example"},
	}); err != nil {
		t.Fatalf("Add(side): %v", err)
	}
	// Add with the manager unwired so the entry lands in the live registry the
	// commit reads; wiring the different empty manager only now makes the live
	// phase fail after that durable commit, which is the rollback seam under test.
	m.cfg.manager = manager
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
