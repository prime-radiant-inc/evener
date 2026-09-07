package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

func TestAdmissionStableOwnerSwapCancellation(t *testing.T) {
	probeAdmissionIdentitySwap(t, true, true)
}

func TestAdmissionChangedRootRejected(t *testing.T) {
	probeAdmissionIdentitySwap(t, false, true)
}

func TestAdmissionStableOwnerSwapSucceeds(t *testing.T) { probeAdmissionIdentitySwap(t, true, false) }

func probeAdmissionIdentitySwap(t *testing.T, sameOwner, unsubscribe bool) {
	srv := NewServer(ServerConfig{})
	oldRef, newRef := "local:stable", "local:stable"
	params := appwire.ThreadReadParams{Ref: oldRef, Subscribe: true}
	if !sameOwner {
		oldRef, newRef = "local:old-workspace", "local:new-workspace"
		params.Ref = "" // A supported implicit-root request follows the root.
	}
	prepared, err := PrepareAppIdentityForRef("local", "old", oldRef, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	// Ordered ingress has created its admission before dispatch reaches this
	// wrapper. Everything after the barrier is the real production handler.
	appserver.HandleTyped(srv.AppServer().Router(), appwire.MethodThreadRead, func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return srv.handleAppThreadRead(ctx, p)
	})
	client := dialServerAppWire(t, srv)
	done := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(ctx, params)
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("handler barrier not reached")
	}
	prepared, err = PrepareAppIdentityForRef("local", "new", newRef, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared, nil)
	if unsubscribe {
		if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: newRef}); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	select {
	case err := <-done:
		if unsubscribe || !sameOwner {
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
				t.Errorf("stale read error=%v, want unavailable", err)
			}
		} else if err != nil {
			t.Fatalf("same-owner read rejected: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("read blocked")
	}
	want := 0
	if sameOwner && !unsubscribe {
		want = 1
	}
	if got := srv.AppSubscriberCount("new"); got != want {
		t.Errorf("new-identity registration=%d, want %d", got, want)
	}
	if got := srv.AppSubscriberCount("old"); got != 0 {
		t.Errorf("stale old-identity registration=%d, want 0", got)
	}
}
