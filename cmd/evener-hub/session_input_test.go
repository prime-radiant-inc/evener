package hub

import (
	"context"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestManagerInputClosedOperationAndAdmission(t *testing.T) {
	store := hubcore.NewSessionPreferencesStore(filepath.Join(t.TempDir(), "index.db"))
	hubID, err := store.HubID()
	if err != nil {
		t.Fatal(err)
	}
	manager := appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance"}
	owner := &sessionOperations{cfg: hubcore.WebConfig{SessionPreferences: store}, sources: appsource.NewRegistry()}
	input := []appwire.InputItem{{Type: "text", Text: "answer"}}
	start := appwire.SessionOperationRequest{Start: &appwire.TurnStartParams{Ref: "local:worker", ClientMutationID: "one", Input: input}}
	if err := start.Validate(); err != nil {
		t.Fatalf("start is a closed union member: %v", err)
	}
	if _, err := owner.Invoke(context.Background(), manager, start); err == nil {
		t.Fatal("Manager input admitted while disabled")
	}
	if _, err := store.SetManager(manager.Ref, hubID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Invoke(context.Background(), manager, start); err == nil {
		t.Fatal("missing source accepted Manager input")
	}
	if err := (appwire.SessionOperationRequest{Start: start.Start, Queue: &appwire.TurnQueueParams{}}).Validate(); err == nil {
		t.Fatal("multiple input actions accepted")
	}
	if err := (appwire.SessionOperationRequest{Control: &appwire.SessionControlRequest{Approval: &appwire.SandboxEscalationResolveParams{Ref: "local:worker", EscalationID: "esc", Approve: true}}}).Validate(); err != nil {
		t.Fatalf("real approval action missing: %v", err)
	}
}

func TestManagerApprovalClosedAction(t *testing.T) {
	if err := (appwire.SessionControlRequest{}).Validate(); err == nil {
		t.Fatal("empty approval action accepted")
	}
	if err := (appwire.SessionControlRequest{Approval: &appwire.SandboxEscalationResolveParams{Ref: "local:worker", EscalationID: "esc", Approve: true}}).Validate(); err != nil {
		t.Fatal(err)
	}
}
