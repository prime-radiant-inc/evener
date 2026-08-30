package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestSimulatorFixtureHubContract(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-be-read")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-be-read")
	hub := newSimulatorFixtureHub(t.TempDir())
	wantMethods := []string{
		"initialize", "thread/list", "thread/read", "thread/turns/list",
		"turn/start", "turn/steer", "turn/queue", "turn/interrupt",
		"evener/tasks/list", "evener/jobs/list", "evener/mobile/pairing",
	}
	if got := hub.RegisteredMethods(); !slices.Equal(wantMethods, got) {
		t.Fatalf("registered methods = %q, want %q", got, wantMethods)
	}
	if got := hub.ProviderName(); got != "simulator-scripted" {
		t.Fatalf("provider = %q", got)
	}
	thread39, thread500 := hub.PathologicalThreads()
	if len(thread39.Items) != 39 || len(thread500.Items) != 500 {
		t.Fatalf("item counts = %d,%d", len(thread39.Items), len(thread500.Items))
	}
	if got := len([]byte(thread39.SystemPrelude)); got != 44_700 {
		t.Fatalf("system prelude bytes = %d", got)
	}
	if hub.ReadLiveProviderEnvironment() {
		t.Fatal("fixture Hub read a live provider environment")
	}
}

func TestPairingURLIsConsumedOnce(t *testing.T) {
	hub := newSimulatorFixtureHub(t.TempDir())
	first, err := hub.ConsumePairingURL("http://127.0.0.1:9180")
	if err != nil {
		t.Fatal(err)
	}
	if first == "" {
		t.Fatal("empty pairing URL")
	}
	if _, err := hub.ConsumePairingURL("http://127.0.0.1:9180"); !errors.Is(err, ErrPairingURLConsumed) {
		t.Fatalf("second consume error = %v", err)
	}
}

func TestScriptedMutationReceiptsAndNotifications(t *testing.T) {
	hub := newSimulatorFixtureHub(t.TempDir())
	cases := []struct {
		method string
		params any
	}{
		{appwire.MethodTurnStart, appwire.TurnStartParams{ThreadID: pathological39ID, ClientMutationID: "send-1", Input: []appwire.InputItem{{Type: "text", Text: "send"}}}},
		{appwire.MethodTurnSteer, appwire.TurnSteerParams{ThreadID: pathological39ID, ClientMutationID: "steer-1", Input: []appwire.InputItem{{Type: "text", Text: "steer"}}}},
		{appwire.MethodTurnQueue, appwire.TurnQueueParams{Ref: pathological39Ref, ClientMutationID: "queue-1", Input: []appwire.InputItem{{Type: "text", Text: "queue"}}}},
		{appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{ThreadID: pathological39ID, ClientMutationID: "interrupt-1"}},
	}
	for _, tc := range cases {
		result, err := hub.Dispatch(t.Context(), tc.method, tc.params)
		if err != nil {
			t.Fatalf("%s: %v", tc.method, err)
		}
		receipt := mutationReceipt(t, result)
		if receipt.ClientMutationID == "" || receipt.ThreadID != pathological39ID || receipt.Disposition != appwire.MutationDispositionApplied {
			t.Fatalf("%s receipt = %#v", tc.method, receipt)
		}
	}

	wantNotifications := []string{
		appwire.NotifyTurnStarted,
		appwire.NotifyItemStarted,
		appwire.NotifyAgentMessageDelta,
		appwire.NotifyReasoningSummaryDelta,
		appwire.NotifyToolOutputDelta,
		"item/question/created",
		appwire.NotifyItemCompleted,
		appwire.NotifyTurnCompleted,
	}
	got := hub.NotificationMethods()
	for _, method := range wantNotifications {
		if !slices.Contains(got, method) {
			t.Errorf("notifications %q missing %q", got, method)
		}
	}
}

func TestStaleGenerationRejected(t *testing.T) {
	hub := newSimulatorFixtureHub(t.TempDir())
	if err := hub.AcceptProjection(7, pathological500ID); err != nil {
		t.Fatal(err)
	}
	before := hub.AcceptedGeneration()
	if err := hub.AcceptProjection(6, pathological39ID); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale projection error = %v", err)
	}
	if got := hub.AcceptedGeneration(); got != before {
		t.Fatalf("accepted generation changed from %d to %d", before, got)
	}
}

func TestReadyManifestModeAndShutdownCleanup(t *testing.T) {
	root := t.TempDir()
	hub := newSimulatorFixtureHub(root)
	manifestPath := filepath.Join(root, "fixture-hub-ready.json")
	manifest := SimulatorReadyManifest{
		Status: "ready", Provider: hub.ProviderName(), Pathological39Items: 39,
		Pathological500Items: 500, SystemPreludeUTF8Bytes: 44_700,
	}
	if err := hub.WriteReadyManifest(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("manifest mode = %o", got)
	}
	var decoded SimulatorReadyManifest
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Provider != "simulator-scripted" || decoded.Pathological39Items != 39 || decoded.SystemPreludeUTF8Bytes != 44_700 {
		t.Fatalf("manifest = %#v", decoded)
	}
	if err := hub.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manifestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest after shutdown: %v", err)
	}
}

func mutationReceipt(t *testing.T, value any) appwire.MutationReceipt {
	t.Helper()
	switch response := value.(type) {
	case appwire.TurnStartResponse:
		return response.Receipt
	case appwire.TurnSteerResponse:
		return response.Receipt
	case appwire.TurnQueueResponse:
		return response.Receipt
	case appwire.TurnInterruptResponse:
		return response.Receipt
	default:
		t.Fatalf("unexpected mutation response %T", value)
		return appwire.MutationReceipt{}
	}
}
