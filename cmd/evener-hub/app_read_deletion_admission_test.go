package hub

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// A missing source cannot resolve subscription admission, and unrelated live
// ownership can prevent a refresh from proving absence. Neither uncertainty
// may hide an authoritative durable deletion. Exercise real RPC serialization
// and the on-disk store, not a handler-only call that skips admission.
func TestHubRPCReadDeletionPrecedesUncertainOwnership(t *testing.T) {
	previousRefresh := hubRosterRefresh
	hubRosterRefresh = func(context.Context, *hubcore.Roster) error {
		return errors.New("unrelated daemon discovery is incomplete")
	}
	t.Cleanup(func() { hubRosterRefresh = previousRefresh })
	for _, subscribe := range []bool{false, true} {
		for _, state := range []hubcore.DeletionState{hubcore.DeletionStateDeleting, hubcore.DeletionStateDeleted} {
			t.Run(fmt.Sprintf("subscribe=%t/%s", subscribe, state), func(t *testing.T) {
				id := hubtest.SessionID(t)
				store, err := hubcore.NewDeletionStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				cfg := hubcore.WebConfig{
					StateDir: t.TempDir(), DeletionStore: store,
					Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{
						PID: 1234, SessionID: hubtest.SessionID(t), Status: appwire.ThreadStatusActive,
					}),
				}
				hub := newHubRPCTestServer(t, cfg)
				defer hub.Close()
				client := dialHubRPC(t, hub)
				defer client.Close()
				if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
					t.Fatal(err)
				}
				params := appwire.ThreadReadParams{Ref: "local:" + id, IncludeTurns: true, Subscribe: subscribe}
				var response appwire.ThreadReadResponse
				err = client.Request(t.Context(), appwire.MethodThreadRead, params, &response)
				var wire appwire.WireError
				if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable || isTargetDeletedError(err) {
					t.Fatalf("non-deleted uncertain target = %v, want non-terminal unavailable", err)
				}
				// Publish after startup so recovery cannot consume the test record.
				markStoppedTargetDeleted(t, cfg, id, state)
				err = client.Request(t.Context(), appwire.MethodThreadRead, params, &response)
				assertStoppedTargetDeletedError(t, err)
			})
		}
	}
}
