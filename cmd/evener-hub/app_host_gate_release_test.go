package hub

// Tests for spec 08 §4's "gate released last" rule (deploy pipeline 08b §5's
// mutation rebind ordering): an update/remove holds its per-host reservation
// THROUGH the post-commit rebind/teardown and releases it last, so no gate
// waiter can acquire a half-rebound host. The window these tests pin is the one
// the S10 build released early (the commit landed, the reservation was dropped
// before the manager's teardown) and the mutation mark alone fenced.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// assertGateBusy pins that a plan-class acquisition — any competing holder —
// cannot enter name's gate, and that the refusal names the manager reservation
// the mutation holds (holder manager, with the mutation's activity). It fails
// the test when the acquisition succeeds: that success is the released window
// itself.
func assertGateBusy(t *testing.T, mgr *sshconn.Manager, name, activity string) {
	t.Helper()
	release, err := mgr.TryAcquire(name, hostops.Holder{Kind: hostops.HolderPlan})
	if err == nil {
		release()
		t.Fatalf("a competing acquisition entered host %q's gate while the %s's reservation must be held through the teardown", name, activity)
	}
	var busy *hostops.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("competing acquisition refusal = %v, want *hostops.BusyError", err)
	}
	if busy.Holder.Kind != hostops.HolderManager || busy.Holder.Activity != activity {
		t.Fatalf("busy holder = %+v, want the %s's manager reservation", busy.Holder, activity)
	}
}

// assertGateFree pins the release-last half: once the mutation has completed,
// its reservation is gone and the gate is acquirable again — a reservation that
// outlived the mutation would wedge the host.
func assertGateFree(t *testing.T, mgr *sshconn.Manager, name string) {
	t.Helper()
	release, err := mgr.TryAcquire(name, hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire after %q's mutation completed: %v", name, err)
	}
	release()
}

// TestHostManageRemoveHoldsTheReservationThroughTheTeardown parks a removal in
// its post-commit window — the exact window the S10 build released the
// reservation in — and pins that the reservation still holds the gate: a
// competing acquisition is refused busy naming the removal. It then releases
// the window and pins the removal completes and releases the reservation last.
func TestHostManageRemoveHoldsTheReservationThroughTheTeardown(t *testing.T) {
	pr := startParkedRemoval(t, "side")
	mgr := pr.m.cfg.manager
	if mgr == nil {
		t.Fatal("the removal fixture wired no manager")
	}

	assertGateBusy(t, mgr, "side", "remove")

	pr.release()
	done := pr.waitRemovalDone(t)
	if done.err != nil {
		t.Fatalf("Remove: %v", done.err)
	}
	if !done.resp.Host.Removed {
		t.Fatalf("remove row = %+v, want Removed", done.resp.Host)
	}
	assertGateFree(t, mgr, "side")
}

// TestHostManageUpdateHoldsTheReservationThroughTheRebind parks an update twice:
// first in its post-commit window (the old released window, before the manager
// call), then inside the manager's rebind itself (before the registry swap). A
// competing acquisition must be refused at both points — the reservation spans
// the whole rebind, not merely up to the call — and must succeed once the
// update has completed and released it last.
func TestHostManageUpdateHoldsTheReservationThroughTheRebind(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	reg, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	rebindEntered := make(chan struct{})
	rebindRelease := make(chan struct{})
	var rebindOnce, rebindReleaseOnce sync.Once
	manager := sshconn.New(reg, sshconn.Options{
		// The manager's own post-swap seam: a park here is inside the manager call
		// the update makes, after the teardown and the registry swap have landed
		// and before the call returns.
		AfterUpdateHostSwap: func(name string) {
			if name != "side" {
				return
			}
			rebindOnce.Do(func() { close(rebindEntered) })
			<-rebindRelease
		},
	})
	t.Cleanup(func() { _ = manager.Close() })
	// Registered after the manager's Close cleanup so it runs first (LIFO): the
	// parks must be released before Close waits on anything they hold.
	t.Cleanup(func() { rebindReleaseOnce.Do(func() { close(rebindRelease) }) })

	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, manager, hubcore.WebConfig{}, configPath, reg, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "side", Address: "side.example"},
	}); err != nil {
		t.Fatalf("Add(side): %v", err)
	}

	postCommitEntered := make(chan struct{})
	postCommitRelease := make(chan struct{})
	var postOnce, postReleaseOnce sync.Once
	m.testOnlyParkPostCommit = func(name string) {
		if name != "side" {
			return
		}
		postOnce.Do(func() { close(postCommitEntered) })
		<-postCommitRelease
	}
	t.Cleanup(func() { postReleaseOnce.Do(func() { close(postCommitRelease) }) })

	params := updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"})
	done := make(chan updateOutcome, 1)
	go func() {
		resp, err := m.Update(context.Background(), params)
		done <- updateOutcome{resp: resp, err: err}
	}()

	<-postCommitEntered
	assertGateBusy(t, manager, "side", "update")

	postReleaseOnce.Do(func() { close(postCommitRelease) })
	select {
	case <-rebindEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the update never reached the manager's rebind; the reservation was not passed through")
	}
	assertGateBusy(t, manager, "side", "update")

	rebindReleaseOnce.Do(func() { close(rebindRelease) })
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("Update: %v", out.err)
		}
		if out.resp.Host.Address != "edited.example" {
			t.Fatalf("update row = %+v, want the edited entry", out.resp.Host)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the update never returned after its rebind was released")
	}
	assertGateFree(t, manager, "side")
}
