package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubRPCTranscriptDisplayGetReturnsCanonicalShippedDefaults(t *testing.T) {
	hub := newHubRPCTestServer(t, hubcore.WebConfig{HubStateRoot: t.TempDir()})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	init, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !init.Features.TranscriptDisplaySettings {
		t.Fatalf("transcript display capability is false: %+v", init.Features)
	}

	var got appwire.TranscriptDisplayDefaults
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("GET: %v", err)
	}
	if want := appwire.TranscriptDisplayShippedDefaults(); !equalTranscriptDisplayDefaults(got, want) {
		t.Fatalf("GET defaults = %#v, want %#v", got, want)
	}
}

func TestHubRPCTranscriptDisplayPatchBroadcastsCanonicalValue(t *testing.T) {
	store, err := hubcore.NewTranscriptDisplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{TranscriptDisplayStore: store})
	defer hub.Close()

	clientA := dialHubRPC(t, hub)
	defer clientA.Close()
	clientB := dialHubRPC(t, hub)
	defer clientB.Close()
	for _, client := range []*appwire.Client{clientA, clientB} {
		if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
	}

	config := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
	config.Content.Level = appwire.TranscriptLevelActivity
	var result appwire.TranscriptDisplayDefaultsPatchResponse
	if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayPatch,
		appwire.TranscriptDisplayDefaultsPatchParams{
			Layout:           appwire.TranscriptViewportDesktop,
			ExpectedRevision: 0,
			Config:           config,
		}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Layout != appwire.TranscriptViewportDesktop || result.Revision != 1 || result.Config != config {
		t.Fatalf("PATCH result = %#v, want desktop revision 1 and canonical config", result)
	}
	for _, client := range []*appwire.Client{clientA, clientB} {
		notification := receiveTranscriptDisplayChanged(t, client)
		if notification.Layout != result.Layout || notification.Revision != result.Revision || notification.Config != result.Config {
			t.Fatalf("notification = %#v, result = %#v", notification, result)
		}
	}
}

func TestHubRPCTranscriptDisplayPatchRawUnknownFieldRejectedWithoutNotification(t *testing.T) {
	hub := newHubRPCTestServer(t, hubcore.WebConfig{HubStateRoot: t.TempDir()})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}

	config := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
	raw, err := json.Marshal(map[string]any{
		"layout":           appwire.TranscriptViewportDesktop,
		"expectedRevision": 0,
		"config":           config,
		"unknown":          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result appwire.TranscriptDisplayDefaultsPatchResponse
	err = client.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayPatch, json.RawMessage(raw), &result)
	assertTranscriptDisplayWireCode(t, err, appwire.CodeInvalidParams)
	assertNoTranscriptDisplayNotification(t, client)
}

func TestHubRPCTranscriptDisplayPatchFailuresDoNotNotify(t *testing.T) {
	root := t.TempDir()
	store, err := hubcore.NewTranscriptDisplayStore(root)
	if err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{TranscriptDisplayStore: store})
	defer hub.Close()
	clientA := dialHubRPC(t, hub)
	defer clientA.Close()
	clientB := dialHubRPC(t, hub)
	defer clientB.Close()
	for _, client := range []*appwire.Client{clientA, clientB} {
		if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
	}

	defaults := appwire.TranscriptDisplayShippedDefaults()
	var noOp appwire.TranscriptDisplayDefaultsPatchResponse
	if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayPatch,
		appwire.TranscriptDisplayDefaultsPatchParams{
			Layout:           appwire.TranscriptViewportDesktop,
			ExpectedRevision: 0,
			Config:           defaults.Desktop.Config,
		}, &noOp); err != nil {
		t.Fatalf("no-op PATCH: %v", err)
	}
	if noOp.Revision != 0 {
		t.Fatalf("no-op revision = %d, want 0", noOp.Revision)
	}
	assertNoTranscriptDisplayNotification(t, clientA)
	assertNoTranscriptDisplayNotification(t, clientB)

	invalidConfig := defaults.Desktop.Config
	invalidConfig.Version = 99
	invalidRaw, err := json.Marshal(map[string]any{
		"layout":           appwire.TranscriptViewportDesktop,
		"expectedRevision": 0,
		"config":           invalidConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	var invalidResult appwire.TranscriptDisplayDefaultsPatchResponse
	err = clientA.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayPatch, json.RawMessage(invalidRaw), &invalidResult)
	assertTranscriptDisplayWireCode(t, err, appwire.CodeInvalidParams)
	if !containsTranscriptDisplayError(err, "unsupported transcript display config version") {
		t.Fatalf("semantic validation error = %v, want unsupported-version diagnostic", err)
	}
	assertNoTranscriptDisplayNotification(t, clientA)
	assertNoTranscriptDisplayNotification(t, clientB)

	changed := defaults.Desktop.Config
	changed.Content.Level = appwire.TranscriptLevelActivity
	var first appwire.TranscriptDisplayDefaultsPatchResponse
	if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayPatch,
		appwire.TranscriptDisplayDefaultsPatchParams{
			Layout:           appwire.TranscriptViewportDesktop,
			ExpectedRevision: 0,
			Config:           changed,
		}, &first); err != nil {
		t.Fatalf("first PATCH: %v", err)
	}
	_ = receiveTranscriptDisplayChanged(t, clientA)
	_ = receiveTranscriptDisplayChanged(t, clientB)

	var staleResult appwire.TranscriptDisplayDefaultsPatchResponse
	err = clientA.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayPatch,
		appwire.TranscriptDisplayDefaultsPatchParams{
			Layout:           appwire.TranscriptViewportDesktop,
			ExpectedRevision: 0,
			Config:           defaults.Desktop.Config,
		}, &staleResult)
	var conflict appwire.WireError
	if !errors.As(err, &conflict) || conflict.Code != appwire.CodeConflict {
		t.Fatalf("stale PATCH error = %T %v, want conflict", err, err)
	}
	var conflictData appwire.TranscriptDisplayConflictData
	conflictJSON, err := json.Marshal(conflict.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(conflictJSON, &conflictData); err != nil {
		t.Fatalf("decode conflict data: %v", err)
	}
	if conflictData.EvenerErrorInfo != appwire.ErrorConflict || conflictData.Layout != appwire.TranscriptViewportDesktop || conflictData.Current.Revision != 1 || conflictData.Current.Config != changed {
		t.Fatalf("conflict data = %#v, want current desktop revision 1", conflictData)
	}
	assertNoTranscriptDisplayNotification(t, clientA)
	assertNoTranscriptDisplayNotification(t, clientB)

	if err := os.RemoveAll(filepath.Join(root, "transcript-display")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "transcript-display"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedConfig := changed
	failedConfig.Content.Level = appwire.TranscriptLevelFull
	var failedResult appwire.TranscriptDisplayDefaultsPatchResponse
	err = clientA.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayPatch,
		appwire.TranscriptDisplayDefaultsPatchParams{
			Layout:           appwire.TranscriptViewportDesktop,
			ExpectedRevision: 1,
			Config:           failedConfig,
		}, &failedResult)
	assertTranscriptDisplayWireCode(t, err, appwire.CodeInternalError)
	assertNoTranscriptDisplayNotification(t, clientA)
	assertNoTranscriptDisplayNotification(t, clientB)
}

func TestHubRPCTranscriptDisplayPatchPersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{HubStateRoot: root})
	client := dialHubRPC(t, hub)
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	defaults := appwire.TranscriptDisplayShippedDefaults()
	desktop := defaults.Desktop.Config
	desktop.Content.Level = appwire.TranscriptLevelFull
	var desktopResult appwire.TranscriptDisplayDefaultsPatchResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayPatch,
		appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportDesktop, Config: desktop}, &desktopResult); err != nil {
		t.Fatalf("desktop PATCH: %v", err)
	}
	_ = receiveTranscriptDisplayChanged(t, client)
	mobile := defaults.Mobile.Config
	mobile.Content.Level = appwire.TranscriptLevelChat
	var mobileResult appwire.TranscriptDisplayDefaultsPatchResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayPatch,
		appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportMobile, Config: mobile}, &mobileResult); err != nil {
		t.Fatalf("mobile PATCH: %v", err)
	}
	_ = receiveTranscriptDisplayChanged(t, client)
	client.Close()
	hub.Close()

	hub = newHubRPCTestServer(t, hubcore.WebConfig{HubStateRoot: root})
	defer hub.Close()
	client = dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	var got appwire.TranscriptDisplayDefaults
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("GET after restart: %v", err)
	}
	if got.Desktop.Revision != 1 || got.Desktop.Config != desktop || got.Mobile.Revision != 1 || got.Mobile.Config != mobile {
		t.Fatalf("GET after restart = %#v, want independent desktop/mobile revisions and configs", got)
	}
}

func TestHubRPCTranscriptDisplayMalformedStateUsesFallbackAndRemainsReadOnly(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "transcript-display", "state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	malformed := []byte(`{"version":1,"desktop":`)
	if err := os.WriteFile(statePath, malformed, 0o600); err != nil {
		t.Fatal(err)
	}

	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{HubStateRoot: root})
	defer hub.Close()
	if web.cfg.TranscriptDisplayStore == nil || web.transcriptDisplayStoreErr == nil {
		t.Fatalf("malformed startup store=%p loadErr=%v, want non-nil fallback and diagnostic", web.cfg.TranscriptDisplayStore, web.transcriptDisplayStoreErr)
	}
	clientA := dialHubRPC(t, hub)
	defer clientA.Close()
	clientB := dialHubRPC(t, hub)
	defer clientB.Close()
	for _, client := range []*appwire.Client{clientA, clientB} {
		if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
	}
	var got appwire.TranscriptDisplayDefaults
	if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("GET malformed state: %v", err)
	}
	if want := appwire.TranscriptDisplayShippedDefaults(); !equalTranscriptDisplayDefaults(got, want) {
		t.Fatalf("GET malformed state = %#v, want fallback %#v", got, want)
	}
	config := got.Desktop.Config
	config.Content.Level = appwire.TranscriptLevelActivity
	var result appwire.TranscriptDisplayDefaultsPatchResponse
	err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayPatch,
		appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportDesktop, Config: config}, &result)
	assertTranscriptDisplayWireCode(t, err, appwire.CodeInternalError)
	if !containsTranscriptDisplayError(err, "decode transcript display state") {
		t.Fatalf("malformed-state PATCH error = %v, want load diagnostic", err)
	}
	assertNoTranscriptDisplayNotification(t, clientA)
	assertNoTranscriptDisplayNotification(t, clientB)
	unchanged, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unchanged, malformed) {
		t.Fatalf("malformed state was overwritten: %q", unchanged)
	}
}

func receiveTranscriptDisplayChanged(t *testing.T, client *appwire.Client) appwire.TranscriptDisplayChangedParams {
	t.Helper()
	select {
	case notification := <-client.Notifications():
		if notification.Method != appwire.NotifyEvenerSettingsTranscriptDisplayChanged {
			t.Fatalf("notification method = %q, want %q", notification.Method, appwire.NotifyEvenerSettingsTranscriptDisplayChanged)
		}
		var params appwire.TranscriptDisplayChangedParams
		if err := json.Unmarshal(notification.Params, &params); err != nil {
			t.Fatalf("decode notification: %v", err)
		}
		return params
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for transcript display notification")
		return appwire.TranscriptDisplayChangedParams{}
	}
}

func assertNoTranscriptDisplayNotification(t *testing.T, client *appwire.Client) {
	t.Helper()
	select {
	case notification := <-client.Notifications():
		t.Fatalf("unexpected notification %q", notification.Method)
	default:
	}
}

func assertTranscriptDisplayWireCode(t *testing.T, err error, code int) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != code {
		t.Fatalf("error = %T %v, want wire code %d", err, err, code)
	}
}

func containsTranscriptDisplayError(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want)
}

func equalTranscriptDisplayDefaults(a, b appwire.TranscriptDisplayDefaults) bool {
	return a.Desktop == b.Desktop && a.Mobile == b.Mobile
}
