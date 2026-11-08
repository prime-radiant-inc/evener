package appsource

import (
	"context"
	"reflect"
	"testing"
	"time"

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

func TestLocalDaemonSourceListUsesDiscoveryMomentForQuietTime(t *testing.T) {
	builtAt := time.UnixMilli(1_790_000_010_000)
	entry := LocalDaemonEntry{
		Entry: rendezvous.Entry{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1/quiet",
			ThreadID:  "th_quiet",
			SessionID: "sess_quiet",
		},
		Status: appwire.ThreadStatusActive,
	}
	setLocalDaemonFleetField(t, &entry, "ProbeState", "fresh")
	setLocalDaemonFleetField(t, &entry, "Activity", &appwire.ThreadActivity{
		LastActivityAt: builtAt.Add(-7500 * time.Millisecond).UnixMilli(),
	})
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{entry} }, nil)

	response, err := source.ListThreads(WithThreadListBuiltAt(context.Background(), builtAt), appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	got := response.Data[0].Evener
	if got.QuietForMS == nil || *got.QuietForMS != 7500 {
		t.Fatalf("quiet time = %v, want 7500ms", got.QuietForMS)
	}
	if got.ProbeState != "fresh" {
		t.Fatalf("probe state = %q, want fresh", got.ProbeState)
	}
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

func TestLocalDaemonSourceCrashIsDisplayOnly(t *testing.T) {
	entry := LocalDaemonEntry{
		Entry: rendezvous.Entry{
			Protocol:   appwire.ProtocolVersion,
			Endpoint:   "ws://127.0.0.1/crashed",
			ThreadID:   "sess-crashed",
			SessionID:  "sess-crashed",
			InstanceID: "instance-crashed",
		},
		SessionID:           "sess-crashed",
		Status:              "errored",
		Crashed:             true,
		ProbeState:          "retained",
		ActiveTurnID:        "stale-turn",
		ActiveTurnStartedAt: 1_790_000_000_123,
		Activity:            &appwire.ThreadActivity{LastActivityAt: 1_790_000_000_000},
		Failure:             &appwire.ThreadFailure{},
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{entry} }, nil)

	response, err := source.ListThreads(WithThreadListBuiltAt(context.Background(), time.UnixMilli(1_790_000_010_000)), appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 {
		t.Fatalf("crashed rows = %d, want 1", len(response.Data))
	}
	got := response.Data[0]
	if got.Status.Type != appwire.ThreadStatusSystemError || !got.Evener.Crashed || got.Evener.ProbeState != "retained" {
		t.Fatalf("crashed row = %+v", got)
	}
	if got.Evener.Failure == nil {
		t.Fatal("crashed row dropped durable failure")
	}
	if got.Evener.ActiveTurnID != "" || got.Evener.ActiveTurnStartedAt != 0 || got.Evener.QuietForMS != nil {
		t.Fatalf("crashed row kept volatile facts: %+v", got.Evener)
	}

	if _, err := source.SessionToolsThread(appwire.SessionBinding{Ref: "local:sess-crashed", InstanceID: "instance-crashed"}); err == nil {
		t.Fatal("crashed row resolved a session-tools binding")
	}
	if _, err := source.localEntryForRefMode("local:sess-crashed", "", true); err == nil {
		t.Fatal("crashed row resolved a live route")
	}
	models, err := source.ListModels(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Data) != 0 {
		t.Fatalf("crashed row returned models: %+v", models.Data)
	}
}

func TestRunSessionToolsOmitsCrashedRoutes(t *testing.T) {
	entries := []LocalDaemonEntry{
		{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/live", SessionID: "live", InstanceID: "instance-live"}},
		{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/crashed", SessionID: "crashed", InstanceID: "instance-crashed"}, Crashed: true},
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return entries }, nil)

	wanted := source.sessionToolsWanted()
	if _, ok := wanted["local:crashed"]; ok {
		t.Fatal("RunSessionTools published a crashed route")
	}
	if _, ok := wanted["local:live"]; !ok {
		t.Fatal("RunSessionTools omitted a live route")
	}
}
