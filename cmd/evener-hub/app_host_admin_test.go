package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// hostAdminCall is one request the scripted remote hub received.
type hostAdminCall struct {
	method string
	params json.RawMessage
}

// hostAdminReply is the scripted remote hub's canned answer for one request.
type hostAdminReply struct {
	result  any
	wireErr *appwire.WireError
}

// recordingBroadcaster captures the controller's browser fan-out instead of
// standing up real connections.
type recordingBroadcaster struct {
	mu   sync.Mutex
	sent []recordedBroadcast
	ch   chan recordedBroadcast
}

type recordedBroadcast struct {
	method string
	params any
}

func newRecordingBroadcaster() *recordingBroadcaster {
	return &recordingBroadcaster{ch: make(chan recordedBroadcast, 16)}
}

func (r *recordingBroadcaster) BroadcastAll(method string, params any) {
	record := recordedBroadcast{method: method, params: params}
	r.mu.Lock()
	r.sent = append(r.sent, record)
	r.mu.Unlock()
	r.ch <- record
}

func (r *recordingBroadcaster) broadcasts() []recordedBroadcast {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedBroadcast, len(r.sent))
	copy(out, r.sent)
	return out
}

// newScriptedRemoteClient builds an initialized AppWire client backed by an
// in-memory stream pair whose peer answers canned responses, records every
// request, and can push notifications on demand. No SSH, no network, no host —
// the component-05 test harness's shape, local to this package.
func newScriptedRemoteClient(
	t *testing.T,
	handle func(method string, params json.RawMessage) hostAdminReply,
) (*appwire.Client, func() []hostAdminCall, func(method string, params any)) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)

	var mu sync.Mutex
	var calls []hostAdminCall

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := server.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			mu.Lock()
			calls = append(calls, hostAdminCall{method: msg.Request.Method, params: msg.Request.Params})
			mu.Unlock()
			if msg.Request.Method == appwire.MethodInitialize {
				data, _ := json.Marshal(appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"})
				if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
					return
				}
				continue
			}
			reply := handle(msg.Request.Method, msg.Request.Params)
			if reply.wireErr != nil {
				if err := server.Send(ctx, appwire.ErrorMessage(msg.Request.ID, *reply.wireErr)); err != nil {
					return
				}
				continue
			}
			data, err := json.Marshal(reply.result)
			if err != nil {
				return
			}
			if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
				return
			}
		}
	}()

	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		cancel()
		t.Fatalf("initialize scripted remote: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		<-done
	})

	emit := func(method string, params any) {
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer sendCancel()
		if err := server.Send(sendCtx, appwire.NotificationMessage(method, params)); err != nil {
			t.Errorf("emit %s: %v", method, err)
		}
	}

	return client, func() []hostAdminCall {
		mu.Lock()
		defer mu.Unlock()
		out := make([]hostAdminCall, len(calls))
		copy(out, calls)
		return out
	}, emit
}

// scriptedHostAdmin wires a hubHostAdminController to a scripted remote host.
// online controls the component-05 attachment signal.
func scriptedHostAdmin(
	t *testing.T,
	online bool,
	handle func(method string, params json.RawMessage) hostAdminReply,
) (*hubHostAdminController, *recordingBroadcaster, func() []hostAdminCall) {
	t.Helper()
	client, calls, _ := newScriptedRemoteClient(t, handle)
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostOnline(func() bool { return online })
	sources := appsource.NewRegistry()
	sources.Add(source)
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	recorder := newRecordingBroadcaster()
	return newHubHostAdminController(recorder, hosts, sources), recorder, calls
}

func okReply() hostAdminReply {
	return hostAdminReply{result: map[string]any{"ok": true}}
}

func TestHostAdminRequestForwardsAllowedMethodAndReturnsResult(t *testing.T) {
	controller, recorder, calls := scriptedHostAdmin(t, true, func(method string, _ json.RawMessage) hostAdminReply {
		if method != appwire.MethodEvenerInstanceList {
			t.Errorf("remote method = %q, want %q", method, appwire.MethodEvenerInstanceList)
		}
		return hostAdminReply{result: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{{Name: "openai"}}}}
	})

	out, err := controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   "m4",
		Method: appwire.MethodEvenerInstanceList,
		Params: json.RawMessage(`{"limit":3}`),
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	var got appwire.InstanceListResponse
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode result %s: %v", out, err)
	}
	if len(got.Instances) != 1 || got.Instances[0].Name != "openai" {
		t.Fatalf("result = %+v, want the remote's instance list", got.Instances)
	}
	forwarded := calls()
	if len(forwarded) != 2 {
		t.Fatalf("remote calls = %+v, want initialize + the one forwarded request", forwarded)
	}
	if forwarded[1].method != appwire.MethodEvenerInstanceList {
		t.Fatalf("forwarded method = %q", forwarded[1].method)
	}
	if string(forwarded[1].params) != `{"limit":3}` {
		t.Fatalf("forwarded params = %s, want the caller's params unchanged", forwarded[1].params)
	}
	if broadcasts := recorder.broadcasts(); len(broadcasts) != 0 {
		t.Fatalf("a request broadcast %+v; the fan-out emits only notifications", broadcasts)
	}
}

func TestHostAdminRequestReturnsRemoteWireErrorVerbatim(t *testing.T) {
	controller, _, _ := scriptedHostAdmin(t, true, func(string, json.RawMessage) hostAdminReply {
		refusal := appwire.InvalidParams("launch layer env var looks like a credential")
		return hostAdminReply{wireErr: &refusal}
	})

	_, err := controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   "m4",
		Method: appwire.MethodEvenerLaunchSetLayer,
	})
	if err == nil {
		t.Fatal("Request succeeded despite the remote's refusal")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("code = %d, want %d (the host's refusal was not laundered)", wire.Code, appwire.CodeInvalidParams)
	}
	if !strings.Contains(wire.Message, "looks like a credential") {
		t.Fatalf("message = %q, want the remote's own text", wire.Message)
	}
}

func TestHostAdminRequestUnknownHostRefusedWithoutForwarding(t *testing.T) {
	controller, recorder, calls := scriptedHostAdmin(t, true, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})

	_, err := controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   "nope",
		Method: appwire.MethodEvenerInstanceList,
	})
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if got := calls(); len(got) != 1 {
		t.Fatalf("remote calls = %+v, want only initialize", got)
	}
	if broadcasts := recorder.broadcasts(); len(broadcasts) != 0 {
		t.Fatalf("refused request broadcast %+v", broadcasts)
	}
}

func TestHostAdminRequestReservedLocalHostRefused(t *testing.T) {
	controller, _, calls := scriptedHostAdmin(t, true, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})

	// "local" is reserved (hostreg.ReservedName), so it is never a registered
	// remote host and never falls back to local execution.
	_, err := controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   hostreg.ReservedName,
		Method: appwire.MethodEvenerInstanceList,
	})
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if got := calls(); len(got) != 1 {
		t.Fatalf("remote calls = %+v, want only initialize", got)
	}
}

func TestHostAdminRequestUnattachedHostRefusedWithoutForwarding(t *testing.T) {
	controller, _, calls := scriptedHostAdmin(t, false, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})

	_, err := controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   "m4",
		Method: appwire.MethodEvenerInstanceList,
	})
	assertWireCode(t, err, appwire.CodeUnavailable)
	if got := calls(); len(got) != 1 {
		t.Fatalf("remote calls = %+v, want only initialize (never a local fallback)", got)
	}
}

func TestHostAdminRequestHostWithNoSourceRefused(t *testing.T) {
	// A host the config validated but component 04/05 never registered a source
	// for is as unavailable as one whose channel is down.
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	controller := newHubHostAdminController(newRecordingBroadcaster(), hosts, appsource.NewRegistry())

	_, err = controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   "m4",
		Method: appwire.MethodEvenerInstanceList,
	})
	assertWireCode(t, err, appwire.CodeUnavailable)
}

func TestHostAdminControllerToleratesNilSourceRegistry(t *testing.T) {
	// newHubAppServerWithNavigation can be called with no source registry at
	// all (some embedders and tests do), which must refuse rather than panic.
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	controller := newHubHostAdminController(newRecordingBroadcaster(), hosts, nil)
	controller.start(context.Background())
	_, err = controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   "m4",
		Method: appwire.MethodEvenerInstanceList,
	})
	assertWireCode(t, err, appwire.CodeUnavailable)
}

// TestHostAdminAllowListMatchesCatalog pins the proxy's security boundary:
// every hub-scoped method in the catalog is recorded here as allowed or denied,
// and that decision is enforced. Adding a catalog method without adding a row
// fails the coverage check below, so a future addition forces a deliberate
// allow/deny decision instead of silently inheriting a prefix rule.
func TestHostAdminAllowListMatchesCatalog(t *testing.T) {
	policy := map[string]bool{
		"evener/archive/set":                      false,
		"evener/auth/apiKey/clear":                true,
		"evener/auth/apiKey/set":                  true,
		"evener/auth/credentialJson/set":          true,
		"evener/auth/device/poll":                 true,
		"evener/auth/device/start":                true,
		"evener/auth/list":                        true,
		"evener/auth/login/complete":              true,
		"evener/auth/login/start":                 true,
		"evener/auth/logout":                      true,
		"evener/auth/status":                      true,
		"evener/auth/test":                        true,
		"evener/command/list":                     false,
		"evener/dirs/create":                      false,
		"evener/favorite/set":                     false,
		"evener/git/head":                         false,
		"evener/harnesses/list":                   false,
		"evener/host/request":                     false,
		"evener/instance/create":                  true,
		"evener/instance/edit":                    true,
		"evener/instance/list":                    true,
		"evener/instance/remove":                  true,
		"evener/instance/setDefault":              true,
		"evener/jobs/list":                        false,
		"evener/jobs/output":                      false,
		"evener/launch/getLayer":                  true,
		"evener/launch/resolve":                   true,
		"evener/launch/schema":                    true,
		"evener/launch/setLayer":                  true,
		"evener/launch/trustRepo":                 true,
		"evener/marketplace/add":                  true,
		"evener/marketplace/browse":               true,
		"evener/marketplace/edit":                 true,
		"evener/marketplace/list":                 true,
		"evener/marketplace/refresh":              true,
		"evener/marketplace/remove":               true,
		"evener/mobile/pairing":                   false,
		"evener/navigation/read":                  false,
		"evener/path/validate":                    false,
		"evener/paths/complete":                   false,
		"evener/pin-section/delete":               false,
		"evener/pin-section/rename":               false,
		"evener/plugin/checkNow":                  true,
		"evener/plugin/disable":                   true,
		"evener/plugin/enable":                    true,
		"evener/plugin/install":                   true,
		"evener/plugin/list":                      true,
		"evener/plugin/preview":                   true,
		"evener/plugin/remove":                    true,
		"evener/plugin/setAutoUpgrade":            true,
		"evener/plugin/upgrade":                   true,
		"evener/project/delete":                   false,
		"evener/projects/recent":                  false,
		"evener/sandbox/escalation/resolve":       false,
		"evener/search":                           false,
		"evener/session-pin/assign":               false,
		"evener/session-pin/unpin":                false,
		"evener/session/delete":                   false,
		"evener/settings/agentsDoc/get":           true,
		"evener/settings/agentsDoc/set":           true,
		"evener/settings/keybindings/get":         false,
		"evener/settings/keybindings/patch":       false,
		"evener/settings/overview":                false,
		"evener/settings/transcriptDisplay/get":   false,
		"evener/settings/transcriptDisplay/patch": false,
		"evener/spawn/slashCatalog":               false,
		"evener/subagentPreview":                  false,
		"evener/tasks/list":                       false,
		"evener/thread/forceStop":                 false,
		"evener/thread/name/set":                  false,
		"evener/thread/transcripts/list":          false,
		"evener/update/apply":                     false,
		"evener/update/check":                     false,
		"evener/upgrade":                          false,
		"goal/set":                                false,
		"model/list":                              false,
		"notes/human/set":                         false,
		"thread/clear":                            false,
		"thread/compact/start":                    false,
		"thread/fork":                             false,
		"thread/list":                             false,
		"thread/model/set":                        false,
		"thread/read":                             false,
		"thread/reasoning-effort/set":             false,
		"thread/resume":                           false,
		"thread/shutdown":                         false,
		"thread/start":                            false,
		"thread/turns/list":                       false,
		"thread/unsubscribe":                      false,
		"thread/vision-model/set":                 false,
		"turn/cancelQueued":                       false,
		"turn/drainAsSteer":                       false,
		"turn/interrupt":                          false,
		"turn/promoteQueuedAsSteer":               false,
		"turn/queue":                              false,
		"turn/start":                              false,
		"turn/steer":                              false,
		"urls/remove":                             false,
	}

	catalog := appwire.CatalogMethodNames(appwire.ScopeHub)
	for _, name := range catalog {
		if _, ok := policy[name]; !ok {
			t.Errorf("catalog method %q has no allow/deny decision in this table; add one deliberately", name)
		}
	}
	for name := range policy {
		if !slices.Contains(catalog, name) {
			t.Errorf("policy names %q, which is not a ScopeHub catalog method", name)
		}
	}

	// The allow-list itself must contain exactly the methods this table allows,
	// and every allow-listed name must be a real catalog method.
	allowed := make([]string, 0, len(remoteHostAdminMethods))
	for name := range remoteHostAdminMethods {
		allowed = append(allowed, name)
	}
	sort.Strings(allowed)
	for name := range policy {
		if policy[name] {
			if _, ok := remoteHostAdminMethods[name]; !ok {
				t.Errorf("policy allows %q but the proxy's allow-list does not name it", name)
			}
		} else if _, ok := remoteHostAdminMethods[name]; ok {
			t.Errorf("policy denies %q but the proxy's allow-list names it", name)
		}
	}
	for _, name := range allowed {
		if !slices.Contains(catalog, name) {
			t.Errorf("allow-list names %q, which is not a ScopeHub catalog method", name)
		}
	}

	controller, _, calls := scriptedHostAdmin(t, true, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	for _, name := range allowed {
		before := len(calls())
		_, err := controller.Request(context.Background(), appwire.HostRequestParams{Host: "m4", Method: name})
		if err != nil {
			t.Errorf("allowed method %q was refused: %v", name, err)
			continue
		}
		if after := len(calls()); after != before+1 {
			t.Errorf("allowed method %q was not forwarded (calls %d -> %d)", name, before, after)
		}
	}

	// Every denied method is InvalidParams and reaches no remote call — the
	// fail-closed property, including for a method the catalog gains later.
	for _, name := range catalog {
		if policy[name] {
			continue
		}
		before := len(calls())
		_, err := controller.Request(context.Background(), appwire.HostRequestParams{Host: "m4", Method: name})
		assertWireCode(t, err, appwire.CodeInvalidParams)
		if after := len(calls()); after != before {
			t.Errorf("denied method %q was forwarded (%d remote calls)", name, after-before)
		}
	}
}

// TestHostAdminAllowListNamesEverySettingsPaneMethod makes the spec's explicit
// requirement local to the test rather than implied by the table above.
func TestHostAdminAllowListNamesEverySettingsPaneMethod(t *testing.T) {
	for _, name := range []string{
		appwire.MethodEvenerPluginDisable,
		appwire.MethodEvenerPluginSetAutoUpgrade,
		appwire.MethodEvenerSettingsAgentsDocGet,
		appwire.MethodEvenerSettingsAgentsDocSet,
		appwire.MethodEvenerInstanceList,
		appwire.MethodEvenerInstanceCreate,
		appwire.MethodEvenerInstanceEdit,
		appwire.MethodEvenerInstanceRemove,
		appwire.MethodEvenerInstanceSetDefault,
		appwire.MethodEvenerLaunchResolve,
		appwire.MethodEvenerLaunchSchema,
		appwire.MethodEvenerLaunchGetLayer,
		appwire.MethodEvenerLaunchSetLayer,
		appwire.MethodEvenerLaunchTrustRepo,
		appwire.MethodEvenerMarketplaceList,
		appwire.MethodEvenerMarketplaceAdd,
		appwire.MethodEvenerMarketplaceRemove,
		appwire.MethodEvenerMarketplaceRefresh,
		appwire.MethodEvenerMarketplaceEdit,
		appwire.MethodEvenerMarketplaceBrowse,
		appwire.MethodEvenerPluginList,
		appwire.MethodEvenerPluginInstall,
		appwire.MethodEvenerPluginUpgrade,
		appwire.MethodEvenerPluginRemove,
		appwire.MethodEvenerPluginEnable,
		appwire.MethodEvenerPluginPreview,
		appwire.MethodEvenerPluginCheckNow,
		appwire.MethodEvenerAuthStatus,
		appwire.MethodEvenerAuthTest,
		appwire.MethodEvenerAuthList,
		appwire.MethodEvenerAuthLoginStart,
		appwire.MethodEvenerAuthLoginComplete,
		appwire.MethodEvenerAuthLogout,
		appwire.MethodEvenerAuthApiKeySet,
		appwire.MethodEvenerAuthApiKeyClear,
		appwire.MethodEvenerAuthCredentialJsonSet,
		appwire.MethodEvenerAuthDeviceStart,
		appwire.MethodEvenerAuthDevicePoll,
	} {
		if _, ok := remoteHostAdminMethods[name]; !ok {
			t.Errorf("settings-pane method %q is not in the proxy allow-list", name)
		}
	}
}

func TestHostAdminFanOutReEmitsOnlyConfigNotifications(t *testing.T) {
	client, _, emit := newScriptedRemoteClient(t, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	sources := appsource.NewRegistry()
	sources.Add(source)
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	recorder := newRecordingBroadcaster()
	controller := newHubHostAdminController(recorder, hosts, sources)

	controller.start(t.Context())

	// A notification the fan-out does not own must be dropped; emitting it
	// first, on the same ordered stream, proves the one broadcast below is the
	// config notification and not the thread one.
	emit(appwire.NotifyThreadStatusChanged, map[string]any{"threadId": "t1"})
	emit(appwire.NotifyEvenerAuthUpdated, appwire.EvenerAuthUpdatedParams{Provider: "openai", ActiveSource: "store"})

	select {
	case record := <-recorder.ch:
		if record.method != appwire.NotifyEvenerHostNotification {
			t.Fatalf("broadcast method = %q, want %q", record.method, appwire.NotifyEvenerHostNotification)
		}
		envelope, ok := record.params.(appwire.HostNotificationParams)
		if !ok {
			t.Fatalf("broadcast params = %T, want appwire.HostNotificationParams", record.params)
		}
		if envelope.Host != "m4" {
			t.Errorf("envelope host = %q, want m4", envelope.Host)
		}
		if envelope.Method != appwire.NotifyEvenerAuthUpdated {
			t.Errorf("envelope method = %q, want %q", envelope.Method, appwire.NotifyEvenerAuthUpdated)
		}
		var payload appwire.EvenerAuthUpdatedParams
		if err := json.Unmarshal(envelope.Params, &payload); err != nil {
			t.Fatalf("decode envelope params %s: %v", envelope.Params, err)
		}
		if payload.Provider != "openai" || payload.ActiveSource != "store" {
			t.Errorf("envelope params = %+v, want the remote's payload unchanged", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the host-tagged fan-out")
	}

	// Exactly one broadcast: the thread-status notification was not wrapped.
	select {
	case extra := <-recorder.ch:
		t.Fatalf("fan-out emitted a second broadcast %+v; only config notifications are wrapped", extra)
	case <-time.After(100 * time.Millisecond):
	}
	if got := recorder.broadcasts(); len(got) != 1 {
		t.Fatalf("broadcasts = %+v, want exactly one", got)
	}
}

func assertWireCode(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a wire error with code %d, got nil", want)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != want {
		t.Fatalf("wire code = %d (%s), want %d", wire.Code, wire.Message, want)
	}
}
