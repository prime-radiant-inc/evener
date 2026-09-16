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
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
	// closeConn closes the connection instead of answering, simulating a
	// response lost after the remote may or may not have applied the request.
	closeConn bool
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
			if reply.closeConn {
				_ = serverConn.Close()
				return
			}
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
	// policy records the deliberate decision for every ScopeHub catalog method:
	// true = the evener/host/request proxy forwards it; false = it is denied and
	// refused with appwire.InvalidParams without reaching the remote. The denied
	// families are controller-local state or UI (navigation, jobs, tasks,
	// thread/turn, keybindings, overview, transcript display), local-process
	// control (upgrade, update, mobile pairing, sandbox escalation), other
	// mutating local surfaces (archive, pin, favorite, project delete, URLs,
	// search, subagent preview), and the proxy method itself (no chaining).
	// Rows are added one method at a time: a catalog method with no row fails the
	// coverage check below, so a future addition still forces a decision.
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
		"evener/dirs/create":                      true, // discovery: create the host directory the spawn form asked for
		"evener/favorite/set":                     false,
		"evener/git/head":                         true, // discovery: read-only branch metadata for a remote path
		"evener/harnesses/list":                   true, // discovery: the host's own harnesses
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
		"evener/path/validate":                    true, // discovery: validate against the host's filesystem
		"evener/paths/complete":                   true, // discovery: complete against the host's filesystem
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
		"evener/projects/recent":                  true, // discovery: the host's recent project directories
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
		"evener/spawn/slashCatalog":               true, // discovery: the host's pre-session slash catalog
		"evener/subagentPreview":                  false,
		"evener/tasks/list":                       false,
		"evener/thread/forceStop":                 false,
		"evener/thread/name/set":                  false,
		"evener/thread/transcripts/list":          false,
		"evener/update/apply":                     false,
		"evener/update/check":                     false,
		"evener/upgrade":                          false,
		"goal/set":                                false,
		"model/list":                              true, // discovery: the host's own model inventory (ScopeBoth)
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
		// Host-dependent discovery: the remote settings panes' filesystem
		// helpers and the spawn form's discovery calls (specs round seven).
		appwire.MethodEvenerPathsComplete,
		appwire.MethodEvenerPathValidate,
		appwire.MethodEvenerDirsCreate,
		appwire.MethodEvenerProjectsRecent,
		appwire.MethodEvenerHarnessesList,
		appwire.MethodEvenerSpawnSlashCatalog,
		appwire.MethodEvenerGitHead,
		appwire.MethodModelList,
	} {
		if _, ok := remoteHostAdminMethods[name]; !ok {
			t.Errorf("settings-pane method %q is not in the proxy allow-list", name)
		}
	}
}

// TestHostAdminRequestForwardsDiscoveryMethodAndReturnsResult is the round-seven
// regression for the settings panes and spawn form: a newly allowed discovery
// method must reach the selected remote hub and return that hub's own answer.
func TestHostAdminRequestForwardsDiscoveryMethodAndReturnsResult(t *testing.T) {
	controller, _, calls := scriptedHostAdmin(t, true, func(method string, params json.RawMessage) hostAdminReply {
		if method != appwire.MethodEvenerPathsComplete {
			t.Errorf("remote method = %q, want %q", method, appwire.MethodEvenerPathsComplete)
		}
		if string(params) != `{"prefix":"/home/m4/pro"}` {
			t.Errorf("forwarded params = %s, want the caller's params unchanged", params)
		}
		return hostAdminReply{result: appwire.PathsCompleteResponse{
			Data: []string{"/home/m4/project"},
		}}
	})

	out, err := controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   "m4",
		Method: appwire.MethodEvenerPathsComplete,
		Params: json.RawMessage(`{"prefix":"/home/m4/pro"}`),
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	var got appwire.PathsCompleteResponse
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode result %s: %v", out, err)
	}
	if len(got.Data) != 1 || got.Data[0] != "/home/m4/project" {
		t.Fatalf("result = %+v, want the remote's own completion", got.Data)
	}
	forwarded := calls()
	if len(forwarded) != 2 || forwarded[1].method != appwire.MethodEvenerPathsComplete {
		t.Fatalf("forwarded calls = %+v, want initialize + evener/paths/complete", forwarded)
	}
}

// TestHostAdminFanOutLeavesReconnectToTheSupervisorWhileOffline pins the retry
// discipline: the fan-out must not drive the component-04 connector for a host
// with no live channel (that would run an SSH preflight on every retry for a
// terminally failed host), and must pick the host up once it reports online.
func TestHostAdminFanOutLeavesReconnectToTheSupervisorWhileOffline(t *testing.T) {
	var online atomic.Bool
	var dials atomic.Int64
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		dials.Add(1)
		return nil, errors.New("no live channel")
	})
	source.SetHostOnline(online.Load)

	hosts, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	controller := newHubHostAdminController(newRecordingBroadcaster(), hosts, appsource.NewRegistry())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		controller.fanOut(ctx, source)
	}()

	// Offline: the connector must not be invoked at all, however long we wait.
	time.Sleep(250 * time.Millisecond)
	if got := dials.Load(); got != 0 {
		t.Fatalf("connector invoked %d times for an offline host, want 0", got)
	}

	// Online: the fan-out subscribes (and here fails, because the connector has
	// no client), so the connector is at least asked.
	online.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for dials.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("fan-out never attempted a subscription after the host came online")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fanOut did not return after its context was canceled")
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

// TestHostAdminFanOutStopsWhenContextCanceled pins the fan-out's shutdown path:
// canceling its context must unblock the receive loop promptly even while the
// subscription channel is still open, so a process-lifetime ctx or a test
// context tears the goroutine down instead of parking it forever.
func TestHostAdminFanOutStopsWhenContextCanceled(t *testing.T) {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		controller.fanOut(ctx, source)
		close(done)
	}()

	// Prove the fan-out is subscribed and relaying before cancelling, so the
	// assertion below is about unblocking an established subscription.
	emit(appwire.NotifyEvenerAuthUpdated, map[string]string{"provider": "openai"})
	select {
	case <-recorder.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the fan-out to relay a config notification")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fanOut did not return after its context was canceled")
	}
}

// TestHostAdminFanOutStopsWhenServerShutdown pins round eight's lifecycle
// finding end to end: the fan-out's context is the RPC server's own lifetime
// handle, so shutting the server down releases the fan-out's subscription and
// stops its worker. Server recreation is the case that matters — a hub server
// rebuilt in-process over the same source registry must not end up with the
// previous server's fan-out still subscribed beside its own, retaining the old
// server's sources and relaying every host notification twice.
func TestHostAdminFanOutStopsWhenServerShutdown(t *testing.T) {
	client, _, _ := newScriptedRemoteClient(t, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostOnline(func() bool { return true })
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		RemoteHosts:  []hostreg.Host{{Name: "m4", SSH: "m4.example"}},
	}

	server := newHubAppServer(cfg, sources)
	waitForHostNotificationSubscribers(t, source, 1, "the fan-out never subscribed to the remote host")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-server.Lifetime().Done():
	default:
		t.Fatal("the server lifetime handle is still open after Shutdown; a fan-out bound to it has nothing to stop on")
	}
	waitForHostNotificationSubscribers(t, source, 0,
		"the fan-out stayed subscribed after its server was shut down")

	// Recreation: the replacement server must own the only fan-out. With the
	// old worker still alive this count reaches 2, which is exactly the
	// duplicate-notification leak this binding exists to prevent.
	replacement := newHubAppServer(cfg, sources)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = replacement.Shutdown(cleanupCtx)
	})
	waitForHostNotificationSubscribers(t, source, 1,
		"the replacement server's fan-out is not the only host-notification subscriber")
}

// waitForHostNotificationSubscribers waits for the source's host-level
// subscriber count to reach want, failing with why after a bounded wait.
func waitForHostNotificationSubscribers(t *testing.T, source *appsource.RemoteHubSource, want int, why string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := source.HostNotificationSubscribers(); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: host-notification subscribers = %d, want %d",
				why, source.HostNotificationSubscribers(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestHostAdminForbiddenMethodRefusedBeforeAvailabilityCheck pins the ordering
// of the fail-closed checks: a method the proxy may never forward is refused
// with InvalidParams even when the host is offline, without consulting the
// source registry at all.
func TestHostAdminForbiddenMethodRefusedBeforeAvailabilityCheck(t *testing.T) {
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	// No sources at all: if the method check ran after source resolution this
	// would report Unavailable rather than InvalidParams.
	controller := newHubHostAdminController(newRecordingBroadcaster(), hosts, appsource.NewRegistry())

	_, err = controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   "m4",
		Method: "evener/instance/deleteAll",
	})
	assertWireCode(t, err, appwire.CodeInvalidParams)
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

// TestHostAdminRequestMapsLostResponseByMutationSafety pins the round-three
// retry-safety split at the proxy boundary. A lost response for a read-only
// forwarded method is SessionUnavailable, which the browser may retry. The same
// loss for a non-idempotent forwarded method must instead report the outcome as
// unknown — neither claiming the method failed nor inviting a blind retry that
// could create a second instance or a second install.
func TestHostAdminRequestMapsLostResponseByMutationSafety(t *testing.T) {
	t.Run("read stays session-unavailable", func(t *testing.T) {
		controller, _, _ := scriptedHostAdmin(t, true, func(string, json.RawMessage) hostAdminReply {
			return hostAdminReply{closeConn: true}
		})
		_, err := controller.Request(context.Background(), appwire.HostRequestParams{
			Host:   "m4",
			Method: appwire.MethodEvenerInstanceList,
		})
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("error = %T %v, want appwire.WireError", err, err)
		}
		if wire.Code != appwire.CodeUnavailable {
			t.Fatalf("code = %d, want %d (a read loss must stay retryable)", wire.Code, appwire.CodeUnavailable)
		}
		data, ok := wire.Data.(appwire.ErrorData)
		if !ok {
			t.Fatalf("Data = %T %v, want appwire.ErrorData", wire.Data, wire.Data)
		}
		if data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
			t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorSessionUnavailable)
		}
	})

	t.Run("mutation reports unknown outcome", func(t *testing.T) {
		controller, _, _ := scriptedHostAdmin(t, true, func(string, json.RawMessage) hostAdminReply {
			return hostAdminReply{closeConn: true}
		})
		_, err := controller.Request(context.Background(), appwire.HostRequestParams{
			Host:   "m4",
			Method: appwire.MethodEvenerPluginInstall,
			Params: json.RawMessage(`{"name":"p"}`),
		})
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("error = %T %v, want appwire.WireError", err, err)
		}
		if wire.Code == appwire.CodeUnavailable {
			t.Fatalf("code = %d, want an explicit outcome-unknown error, not a retryable SessionUnavailable", wire.Code)
		}
		data, ok := wire.Data.(appwire.ErrorData)
		if !ok {
			t.Fatalf("Data = %T %v, want appwire.ErrorData", wire.Data, wire.Data)
		}
		if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown {
			t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorMutationOutcomeUnknown)
		}
		if data.MutationOutcome != appwire.MutationOutcomeUnknown {
			t.Fatalf("mutationOutcome = %q, want %q", data.MutationOutcome, appwire.MutationOutcomeUnknown)
		}
		if data.RetryDisposition != appwire.RetryDispositionBlocked {
			t.Fatalf("retryDisposition = %q, want %q", data.RetryDisposition, appwire.RetryDispositionBlocked)
		}
	})
}

// TestHostAdminMutationClassificationMatchesAllowList forces a retry-safety
// decision for every allow-listed method, the same way
// TestHostAdminAllowListMatchesCatalog forces an allow/deny decision: each
// allow-listed method must be classified exactly once, as a non-idempotent
// mutation (remoteHostAdminMutationMethods) or as an explicit read (readOnly,
// below). A method in neither, or in both, fails. A future allow-list addition
// therefore cannot silently inherit the read path's retryable error mapping.
func TestHostAdminMutationClassificationMatchesAllowList(t *testing.T) {
	// readOnly names every allow-listed method whose effect is a lookup or a
	// refetch, so an identical retry is safe and SessionUnavailable is the
	// honest error. Everything else allow-listed is a mutation.
	readOnly := map[string]bool{
		appwire.MethodEvenerInstanceList:         true,
		appwire.MethodEvenerLaunchResolve:        true,
		appwire.MethodEvenerLaunchSchema:         true,
		appwire.MethodEvenerLaunchGetLayer:       true,
		appwire.MethodEvenerMarketplaceList:      true,
		appwire.MethodEvenerMarketplaceBrowse:    true,
		appwire.MethodEvenerMarketplaceRefresh:   true,
		appwire.MethodEvenerPluginList:           true,
		appwire.MethodEvenerPluginPreview:        true,
		appwire.MethodEvenerPluginCheckNow:       true,
		appwire.MethodEvenerAuthStatus:           true,
		appwire.MethodEvenerAuthTest:             true,
		appwire.MethodEvenerAuthList:             true,
		appwire.MethodEvenerSettingsAgentsDocGet: true,
		appwire.MethodEvenerPathsComplete:        true,
		appwire.MethodEvenerPathValidate:         true,
		appwire.MethodEvenerProjectsRecent:       true,
		appwire.MethodEvenerHarnessesList:        true,
		appwire.MethodEvenerSpawnSlashCatalog:    true,
		appwire.MethodEvenerGitHead:              true,
		appwire.MethodModelList:                  true,
	}

	for name := range remoteHostAdminMethods {
		_, mutating := remoteHostAdminMutationMethods[name]
		_, read := readOnly[name]
		switch {
		case mutating && read:
			t.Errorf("allow-listed method %q is classified as both a mutation and a read", name)
		case !mutating && !read:
			t.Errorf("allow-listed method %q has no retry-safety classification; add it to remoteHostAdminMutationMethods or to this test's readOnly set", name)
		}
	}
	for name := range remoteHostAdminMutationMethods {
		if _, ok := remoteHostAdminMethods[name]; !ok {
			t.Errorf("mutation set names %q, which is not on the proxy allow-list", name)
		}
	}
	for name := range readOnly {
		if _, ok := remoteHostAdminMethods[name]; !ok {
			t.Errorf("readOnly names %q, which is not on the proxy allow-list", name)
		}
	}
}
