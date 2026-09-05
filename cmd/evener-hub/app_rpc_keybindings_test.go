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

func TestHubRPCKeybindingsGetReturnsCanonicalShippedDefaults(t *testing.T) {
	hub := newHubRPCTestServer(t, hubcore.WebConfig{HubStateRoot: t.TempDir()})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	init, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !init.Features.KeybindingsSettings {
		t.Fatalf("keybindings capability is false: %+v", init.Features)
	}

	var got appwire.KeybindingsOverrides
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("GET: %v", err)
	}
	if want := appwire.KeybindingsShippedDefaults(); !equalKeybindingsOverrides(got, want) {
		t.Fatalf("GET defaults = %#v, want %#v", got, want)
	}
}

func TestHubRPCKeybindingsPatchBroadcastsCanonicalValue(t *testing.T) {
	store, err := hubcore.NewKeybindingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{KeybindingsStore: store})
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

	config := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: new("ctrl+n")},
		{Action: "thread.close", Chord: nil},
	}}
	var result appwire.KeybindingsPatchResponse
	if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsPatch,
		appwire.KeybindingsPatchParams{
			ExpectedRevision: 0,
			Config:           config,
		}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Revision != 1 || result.Version != 1 || !equalKeybindingsRules(result.Rules, config.Rules) {
		t.Fatalf("PATCH result = %#v, want revision 1 and canonical rules", result)
	}
	for _, client := range []*appwire.Client{clientA, clientB} {
		notification := receiveKeybindingsChanged(t, client)
		if notification.Revision != result.Revision || notification.Version != result.Version || !equalKeybindingsRules(notification.Rules, result.Rules) {
			t.Fatalf("notification = %#v, result = %#v", notification, result)
		}
	}
}

func TestHubRPCKeybindingsPatchPostRenameErrorStillBroadcasts(t *testing.T) {
	// A durable failure AFTER the rename means the patch applied: the store
	// published the new revision, and the error the patching client gets back
	// must not leave every OTHER client stale. The hub therefore broadcasts
	// the canonical snapshot before surfacing the failure.
	wantFailure := errors.New("after rename failed")
	store, err := hubcore.NewKeybindingsStoreForTest(t.TempDir(), func() error { return wantFailure })
	if err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{KeybindingsStore: store})
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

	config := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: new("ctrl+n")},
	}}
	var result appwire.KeybindingsPatchResponse
	err = clientA.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsPatch,
		appwire.KeybindingsPatchParams{ExpectedRevision: 0, Config: config}, &result)
	if err == nil {
		t.Fatal("PATCH with a post-rename fault returned no error")
	}
	// The error must carry the APPLIED canonical state so the requesting
	// client reconciles from it rather than treating its write as rejected.
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInternalError {
		t.Fatalf("error = %T %v, want wire code %d", err, err, appwire.CodeInternalError)
	}
	dataJSON, merr := json.Marshal(wire.Data)
	if merr != nil {
		t.Fatal(merr)
	}
	var data appwire.KeybindingsPostRenameData
	if err := json.Unmarshal(dataJSON, &data); err != nil {
		t.Fatalf("decode post-rename error data: %v", err)
	}
	if data.EvenerErrorInfo != appwire.ErrorKeybindingsPostRename {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorKeybindingsPostRename)
	}
	if data.Applied.Revision != 1 || !equalKeybindingsRules(data.Applied.Rules, config.Rules) {
		t.Fatalf("applied = %#v, want revision 1 and the canonical rules", data.Applied)
	}
	for _, client := range []*appwire.Client{clientA, clientB} {
		notification := receiveKeybindingsChanged(t, client)
		if notification.Revision != 1 || notification.Version != 1 || !equalKeybindingsRules(notification.Rules, config.Rules) {
			t.Fatalf("notification = %#v, want revision 1 and the canonical rules", notification)
		}
	}
}

func TestHubRPCKeybindingsPatchRawUnknownFieldRejectedWithoutNotification(t *testing.T) {
	hub := newHubRPCTestServer(t, hubcore.WebConfig{HubStateRoot: t.TempDir()})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(map[string]any{
		"expectedRevision": 0,
		"config": map[string]any{
			"version": 1,
			"rules":   []any{},
		},
		"unknown": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result appwire.KeybindingsPatchResponse
	err = client.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsPatch, json.RawMessage(raw), &result)
	assertKeybindingsWireCode(t, err, appwire.CodeInvalidParams)
	assertNoKeybindingsNotification(t, client)
}

func TestHubRPCKeybindingsPatchFailuresDoNotNotify(t *testing.T) {
	root := t.TempDir()
	store, err := hubcore.NewKeybindingsStore(root)
	if err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{KeybindingsStore: store})
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

	defaults := appwire.KeybindingsShippedDefaults()
	var noOp appwire.KeybindingsPatchResponse
	if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsPatch,
		appwire.KeybindingsPatchParams{
			ExpectedRevision: 0,
			Config:           appwire.KeybindingsConfig{Version: defaults.Version, Rules: defaults.Rules},
		}, &noOp); err != nil {
		t.Fatalf("no-op PATCH: %v", err)
	}
	if noOp.Revision != 0 {
		t.Fatalf("no-op revision = %d, want 0", noOp.Revision)
	}
	assertNoKeybindingsNotification(t, clientA)
	assertNoKeybindingsNotification(t, clientB)

	invalidRaw, err := json.Marshal(map[string]any{
		"expectedRevision": 0,
		"config": map[string]any{
			"version": 99,
			"rules":   []any{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var invalidResult appwire.KeybindingsPatchResponse
	err = clientA.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsPatch, json.RawMessage(invalidRaw), &invalidResult)
	assertKeybindingsWireCode(t, err, appwire.CodeInvalidParams)
	if !containsKeybindingsError(err, "unsupported keybindings config version") {
		t.Fatalf("structural validation error = %v, want unsupported-version diagnostic", err)
	}
	assertNoKeybindingsNotification(t, clientA)
	assertNoKeybindingsNotification(t, clientB)

	changed := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: new("ctrl+n")},
	}}
	var first appwire.KeybindingsPatchResponse
	if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsPatch,
		appwire.KeybindingsPatchParams{
			ExpectedRevision: 0,
			Config:           changed,
		}, &first); err != nil {
		t.Fatalf("first PATCH: %v", err)
	}
	_ = receiveKeybindingsChanged(t, clientA)
	_ = receiveKeybindingsChanged(t, clientB)

	var staleResult appwire.KeybindingsPatchResponse
	err = clientA.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsPatch,
		appwire.KeybindingsPatchParams{
			ExpectedRevision: 0,
			Config:           appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{}},
		}, &staleResult)
	var conflict appwire.WireError
	if !errors.As(err, &conflict) || conflict.Code != appwire.CodeConflict {
		t.Fatalf("stale PATCH error = %T %v, want conflict", err, err)
	}
	var conflictData appwire.KeybindingsConflictData
	conflictJSON, err := json.Marshal(conflict.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(conflictJSON, &conflictData); err != nil {
		t.Fatalf("decode conflict data: %v", err)
	}
	if conflictData.EvenerErrorInfo != appwire.ErrorConflict || conflictData.Current.Revision != 1 || !equalKeybindingsRules(conflictData.Current.Rules, changed.Rules) {
		t.Fatalf("conflict data = %#v, want current revision 1 with canonical rules", conflictData)
	}
	assertNoKeybindingsNotification(t, clientA)
	assertNoKeybindingsNotification(t, clientB)

	if err := os.RemoveAll(filepath.Join(root, "keybindings")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keybindings"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedConfig := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: new("ctrl+alt+n")},
	}}
	var failedResult appwire.KeybindingsPatchResponse
	err = clientA.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsPatch,
		appwire.KeybindingsPatchParams{
			ExpectedRevision: 1,
			Config:           failedConfig,
		}, &failedResult)
	assertKeybindingsWireCode(t, err, appwire.CodeInternalError)
	assertNoKeybindingsNotification(t, clientA)
	assertNoKeybindingsNotification(t, clientB)
}

func TestHubRPCKeybindingsPatchPersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{HubStateRoot: root})
	client := dialHubRPC(t, hub)
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	config := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: new("ctrl+n")},
		{Action: "thread.close", Chord: nil},
	}}
	var result appwire.KeybindingsPatchResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsPatch,
		appwire.KeybindingsPatchParams{Config: config}, &result); err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	_ = receiveKeybindingsChanged(t, client)
	client.Close()
	hub.Close()

	hub = newHubRPCTestServer(t, hubcore.WebConfig{HubStateRoot: root})
	defer hub.Close()
	client = dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	var got appwire.KeybindingsOverrides
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("GET after restart: %v", err)
	}
	if got.Revision != 1 || got.Version != 1 || !equalKeybindingsRules(got.Rules, config.Rules) {
		t.Fatalf("GET after restart = %#v, want revision 1 and canonical rules", got)
	}
}

func TestHubRPCKeybindingsMalformedStateUsesFallbackAndRemainsReadOnly(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "keybindings", "state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	malformed := []byte(`{"version":1,"revision":`)
	if err := os.WriteFile(statePath, malformed, 0o600); err != nil {
		t.Fatal(err)
	}

	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{HubStateRoot: root})
	defer hub.Close()
	if web.cfg.KeybindingsStore == nil || web.keybindingsStoreErr == nil {
		t.Fatalf("malformed startup store=%p loadErr=%v, want non-nil fallback and diagnostic", web.cfg.KeybindingsStore, web.keybindingsStoreErr)
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
	var got appwire.KeybindingsOverrides
	if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("GET malformed state: %v", err)
	}
	if want := appwire.KeybindingsShippedDefaults(); !equalKeybindingsOverrides(got, want) {
		t.Fatalf("GET malformed state = %#v, want fallback %#v", got, want)
	}
	var result appwire.KeybindingsPatchResponse
	err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsKeybindingsPatch,
		appwire.KeybindingsPatchParams{Config: appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
			{Action: "thread.new", Chord: new("ctrl+n")},
		}}}, &result)
	assertKeybindingsWireCode(t, err, appwire.CodeInternalError)
	if !containsKeybindingsError(err, "decode keybindings state") {
		t.Fatalf("malformed-state PATCH error = %v, want load diagnostic", err)
	}
	assertNoKeybindingsNotification(t, clientA)
	assertNoKeybindingsNotification(t, clientB)
	unchanged, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unchanged, malformed) {
		t.Fatalf("malformed state was overwritten: %q", unchanged)
	}
}

func receiveKeybindingsChanged(t *testing.T, client *appwire.Client) appwire.KeybindingsChangedParams {
	t.Helper()
	select {
	case notification := <-client.Notifications():
		if notification.Method != appwire.NotifyEvenerSettingsKeybindingsChanged {
			t.Fatalf("notification method = %q, want %q", notification.Method, appwire.NotifyEvenerSettingsKeybindingsChanged)
		}
		var params appwire.KeybindingsChangedParams
		if err := json.Unmarshal(notification.Params, &params); err != nil {
			t.Fatalf("decode notification: %v", err)
		}
		return params
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for keybindings notification")
		return appwire.KeybindingsChangedParams{}
	}
}

func assertNoKeybindingsNotification(t *testing.T, client *appwire.Client) {
	t.Helper()
	select {
	case notification := <-client.Notifications():
		t.Fatalf("unexpected notification %q", notification.Method)
	default:
	}
}

func assertKeybindingsWireCode(t *testing.T, err error, code int) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != code {
		t.Fatalf("error = %T %v, want wire code %d", err, err, code)
	}
}

func containsKeybindingsError(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want)
}

func equalKeybindingsOverrides(a, b appwire.KeybindingsOverrides) bool {
	return a.Version == b.Version && a.Revision == b.Revision && equalKeybindingsRules(a.Rules, b.Rules)
}

func equalKeybindingsRules(a, b []appwire.KeybindingsRule) bool {
	if len(a) != len(b) {
		return false
	}
	if (a == nil) != (b == nil) {
		return false
	}
	for i := range a {
		if a[i].Action != b[i].Action {
			return false
		}
		if (a[i].Chord == nil) != (b[i].Chord == nil) {
			return false
		}
		if a[i].Chord != nil && *a[i].Chord != *b[i].Chord {
			return false
		}
	}
	return true
}
