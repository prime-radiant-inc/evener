package rvreg

import (
	"testing"

	"primeradiant.com/serf/rendezvous"
)

func TestRegistrationUpdatesSessionIdentity(t *testing.T) {
	runDir := t.TempDir()
	reg := &Registration{}
	if err := reg.Register(runDir, rendezvous.Entry{
		PID:       4242,
		Protocol:  "serf-appwire-v1",
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "01OLD",
		SessionID: "01OLD",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := reg.UpdateSessionID("01NEW"); err != nil {
		t.Fatalf("UpdateSessionID: %v", err)
	}

	entries, err := rendezvous.List(runDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%+v", entries)
	}
	if entries[0].ThreadID != "01NEW" || entries[0].SessionID != "01NEW" {
		t.Fatalf("entry identity=%+v", entries[0])
	}
}
