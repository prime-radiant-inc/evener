package hub

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestManagerApprovalClosedUnionAndRealOwner(t *testing.T) {
	for _, raw := range []string{`{}`, `{"approval":null}`, `{"stop":{"ref":"local:worker"}}`, `{"approval":{"ref":"local:worker","escalationId":"esc","approve":true,"actor":{"kind":"human"}}}`} {
		var request appwire.SessionControlRequest
		if err := json.Unmarshal([]byte(raw), &request); err == nil {
			t.Fatalf("unimplemented or forged approval decoded: %s", raw)
		}
	}
	owner := &sessionOperations{cfg: hubcore.WebConfig{HubID: "hub-test"}, sources: appsource.NewRegistry()}
	result, err := owner.Control(context.Background(), appwire.SessionControlRequest{Action: "approval", Approval: &appwire.SandboxEscalationResolveParams{Ref: "local:missing", EscalationID: "esc", Approve: true}})
	if err == nil {
		t.Fatalf("missing target approval falsely succeeded: %+v", result)
	}
}
