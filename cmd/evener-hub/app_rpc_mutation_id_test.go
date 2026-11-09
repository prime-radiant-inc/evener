package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// Every receipt-bearing session mutation correlates on its clientMutationId, so
// a request without one is refused as invalid params before any source is
// resolved. The control row sends the identical request with an id and requires
// a different outcome, so the refusal is attributable to the missing id.
func TestReceiptMutationsRequireClientMutationID(t *testing.T) {
	server := newHubAppServer(hubcore.WebConfig{}, appsource.NewRegistry())
	ref := "missing:thread"
	cases := []struct {
		method string
		params func(id string) any
	}{
		{appwire.MethodTurnStart, func(id string) any { return appwire.TurnStartParams{Ref: ref, ClientMutationID: id} }},
		{appwire.MethodTurnSteer, func(id string) any { return appwire.TurnSteerParams{Ref: ref, ClientMutationID: id} }},
		{appwire.MethodTurnInterrupt, func(id string) any { return appwire.TurnInterruptParams{Ref: ref, ClientMutationID: id} }},
		{appwire.MethodTurnQueue, func(id string) any { return appwire.TurnQueueParams{Ref: ref, ClientMutationID: id} }},
		{appwire.MethodTurnDrainAsSteer, func(id string) any { return appwire.TurnDrainAsSteerParams{Ref: ref, ClientMutationID: id} }},
		{appwire.MethodTurnPromoteQueuedAsSteer, func(id string) any {
			return appwire.TurnPromoteQueuedAsSteerParams{Ref: ref, ClientMutationID: id, ExpectedEntryID: "entry"}
		}},
		{appwire.MethodTurnCancelQueued, func(id string) any {
			return appwire.TurnCancelQueuedParams{Ref: ref, ClientMutationID: id, ExpectedEntryID: "entry"}
		}},
		{appwire.MethodThreadClear, func(id string) any {
			return appwire.ThreadClearParams{Ref: ref, ClientMutationID: id, ExpectedInstanceID: "instance"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			_, err := exactDispatch(context.Background(), t, server, tc.method, tc.params("  "))
			wire, ok := wireErrorFromError(err)
			if !ok || wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("without an id: err=%v, want an invalid-params wire error", err)
			}
			_, err = exactDispatch(context.Background(), t, server, tc.method, tc.params("mutation-1"))
			if wire, ok := wireErrorFromError(err); ok && wire.Code == appwire.CodeInvalidParams {
				t.Fatalf("with an id: err=%v, want the request to get past the id check", err)
			}
		})
	}
}
