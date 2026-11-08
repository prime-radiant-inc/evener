package appsource

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func setLocalDaemonFleetField(t *testing.T, entry *LocalDaemonEntry, name string, value any) {
	t.Helper()
	field := reflect.ValueOf(entry).Elem().FieldByName(name)
	if !field.IsValid() {
		t.Fatalf("LocalDaemonEntry has no %s field", name)
	}
	field.Set(reflect.ValueOf(value))
}

func TestLocalDaemonSourceListCarriesFreshActiveTurn(t *testing.T) {
	entry := LocalDaemonEntry{
		Entry: rendezvous.Entry{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1/active",
			ThreadID:  "th_active",
			SessionID: "sess_active",
		},
		Status: appwire.ThreadStatusActive,
	}
	setLocalDaemonFleetField(t, &entry, "ActiveTurnID", "turn-running")
	setLocalDaemonFleetField(t, &entry, "ActiveTurnStartedAt", int64(1_790_000_000_123))
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{entry} }, nil)

	response, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 {
		t.Fatalf("threads = %+v", response.Data)
	}
	got := response.Data[0].Evener
	if got.ActiveTurnID != "turn-running" || got.ActiveTurnStartedAt != 1_790_000_000_123 {
		t.Fatalf("active turn = %q at %d, want the fresh probe facts", got.ActiveTurnID, got.ActiveTurnStartedAt)
	}
}
