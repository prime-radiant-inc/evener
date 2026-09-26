package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
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

// assertBroadcastMethods fails t unless b recorded at least one broadcast
// for each of methods.
func assertBroadcastMethods(t *testing.T, b *recordingBroadcaster, methods ...string) {
	t.Helper()
	got := b.broadcasts()
	found := make(map[string]bool, len(got))
	for _, r := range got {
		found[r.method] = true
	}
	for _, method := range methods {
		if !found[method] {
			t.Fatalf("broadcasts = %+v, want %v", got, methods)
		}
	}
}

// assertOneBroadcast fails t unless b recorded exactly one broadcast, for
// method.
func assertOneBroadcast(t *testing.T, b *recordingBroadcaster, method string) {
	t.Helper()
	got := b.broadcasts()
	if len(got) != 1 || got[0].method != method {
		t.Fatalf("broadcasts = %+v, want exactly one %s", got, method)
	}
}

// newScriptedAdminClient builds an initialized AppWire client backed by an
// in-memory stream pair whose peer answers canned responses, records every
// request, and can push notifications on demand. No SSH, no network, no host —
// the component-05 test harness's shape, local to this package.
func newScriptedAdminClient(
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
	client, calls, _ := newScriptedAdminClient(t, handle)
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

// TestHostAdminRequestNormalizesHostWhitespace pins that a padded host id gets
// the same treatment as the canonical one. The host registry trims, the source
// registry does not, so without one normalization a caller-side spelling like
// " m4 " would pass the registry lookup and then be refused as "not attached"
// instead of being forwarded.
func TestHostAdminRequestNormalizesHostWhitespace(t *testing.T) {
	controller, _, calls := scriptedHostAdmin(t, true, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})

	out, err := controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   " m4 ",
		Method: appwire.MethodEvenerInstanceList,
	})
	if err != nil {
		t.Fatalf("Request with a padded host: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("result = %s, want the remote's own result", out)
	}
	if got := calls(); len(got) != 2 {
		t.Fatalf("remote calls = %+v, want initialize + the forwarded request", got)
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
	//
	// The session image fetch is denied for the same reason as the proxy method:
	// it is the controller's own image-route call, resolving against the
	// recipient's local session state, so a peer hub must not be able to drive
	// it by forwarding the request.
	policy := map[string]bool{
		"evener/archive/set":                false,
		"evener/auth/apiKey/clear":          true,
		"evener/auth/apiKey/conditionalSet": true,
		"evener/auth/apiKey/set":            true,
		"evener/auth/credentialJson/set":    true,
		"evener/auth/device/poll":           true,
		"evener/auth/device/start":          true,
		"evener/auth/list":                  true,
		"evener/auth/login/complete":        true,
		"evener/auth/login/start":           true,
		"evener/auth/logout":                true,
		"evener/auth/status":                true,
		"evener/auth/test":                  true,
		"evener/command/list":               false,
		// The resident-process controls are a LOCAL operator surface: the
		// inventory reads this host's live processes and rendezvous records, and
		// retirement stops a daemon after verifying its kernel-serialized
		// ownership on this host. Neither is designed to be driven through the
		// remote admin proxy, so both are denied deliberately rather than left
		// undecided — an unlisted method is refused with appwire.InvalidParams
		// and never forwarded.
		"evener/daemon/list":    false,
		"evener/daemon/retire":  false,
		"evener/dirs/create":    true, // discovery: create the host directory the spawn form asked for
		"evener/favorite/set":   false,
		"evener/git/head":       true, // discovery: read-only branch metadata for a remote path
		"evener/harnesses/list": true, // discovery: the host's own harnesses
		"evener/host/request":   false,
		// evener/host/attach is controller-LOCAL: it dials a host this
		// controller owns through the Ensure-backed seam. There is no host to
		// forward to until the attach succeeds, so it is never a proxied call,
		// and a peer hub must not be able to make this hub attach a new host by
		// forwarding it.
		"evener/host/attach": false,
		// The host-management methods are controller-LOCAL like
		// attach: they act on this controller's own config and channels
		// (add/list/status/remove/update), so they are never proxied calls. Denied
		// deliberately — see TestHostManageNotForwarded, which pins the same
		// requirement from the management side.
		"evener/host/add":    false,
		"evener/host/list":   false,
		"evener/host/status": false,
		"evener/host/remove": false,
		"evener/host/update": false,
		// The credential push is controller-LOCAL: it reads this controller's
		// own store and dispatches to a host itself, like evener/host/request.
		// It is never a proxied call, so a peer hub cannot make this hub push
		// its local credentials by forwarding the method.
		"evener/host/pushCredentials": false,
		"evener/instance/create":      true,
		"evener/instance/edit":        true,
		"evener/instance/list":        true,
		// Two instance methods landed on the catalog after this list was
		// written (per-model enablement and its refresh). The proxy forwards the
		// five instance handlers the settings panes drive remotely and nothing
		// else, so both are denied deliberately rather than left undecided: an
		// unlisted method is refused with appwire.InvalidParams and never
		// forwarded.
		"evener/instance/refreshModels":           false,
		"evener/instance/remove":                  true,
		"evener/instance/setDefault":              true,
		"evener/instance/setModelDisabled":        false,
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
		"evener/session/image":                    false,
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
		appwire.MethodEvenerAuthApiKeyConditionalSet,
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
	wake := controller.takeAttachWakeOwnership(source.ID())
	done := make(chan struct{})
	go func() {
		defer close(done)
		controller.fanOut(ctx, source, wake)
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
	client, _, emit := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
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
	client, _, emit := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
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
	wake := controller.takeAttachWakeOwnership(source.ID())
	done := make(chan struct{})
	go func() {
		controller.fanOut(ctx, source, wake)
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
	client, _, _ := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
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

// TestHostAdminFanOutStopsBeforeItsTransportCloses pins the two lifecycle
// properties round eight's finding depends on, in the order the hub's own defer
// stack produces: the AppWire server is drained unconditionally (main.go,
// registered after the SSH manager's teardown so it runs first), and only then
// does the SSH manager close the channels the fan-out was reading from.
//
// The first property is what stops the fan-out: a shut-down server cancels its
// lifetime, the fan-out's subscription is released while the transport is still
// open, and the goroutine ends. The second checks that the teardown that follows
// cannot resurrect it — a fan-out that outlived the close would re-dial a dead
// channel on its next retry, which is exactly the leak this binding exists to
// prevent. A second Shutdown is safe: the tracing drain and any embedder that
// already drained the server call it again.
func TestHostAdminFanOutStopsBeforeItsTransportCloses(t *testing.T) {
	client, _, _ := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	var connects atomic.Int64
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		connects.Add(1)
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
	// Released while the transport is still open, and therefore before the close
	// below can race it.
	waitForHostNotificationSubscribers(t, source, 0,
		"the fan-out stayed subscribed across the server's shutdown")

	// The SSH manager's half of the teardown: closing the channel the fan-out
	// was reading from must not bring it back. A leaked fan-out would notice the
	// closed subscription, wait out its backoff, and re-dial through the
	// connector — so the assertion is on the connector count, sampled after a
	// full retry interval.
	_ = client.Close()
	before := connects.Load()
	time.Sleep(2 * hostNotificationRetryBase)
	if got := connects.Load(); got != before {
		t.Fatalf("the fan-out re-dialled a closed channel after shutdown: connector calls = %d, want %d", got, before)
	}
	waitForHostNotificationSubscribers(t, source, 0,
		"a fan-out re-subscribed to the source after the server was shut down")

	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
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

// TestHostAdminAttachWakesBackoffSleepingFanOut pins the round-seven M1
// finding: an EventAttached must rebind the host-notification broker
// immediately, not after its exponential backoff (up to 30s) expires. A
// fan-out parked in backoff while its host is offline must subscribe — pinned
// by observing the source's host-notification subscriber count, i.e. the
// SubscribeHostNotifications registration that starts the fresh client's
// drain — as soon as the attach event arrives and the host reports online.
func TestHostAdminAttachWakesBackoffSleepingFanOut(t *testing.T) {
	client, _, _ := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	var online atomic.Bool
	source.SetHostOnline(online.Load)
	sources := appsource.NewRegistry()
	sources.Add(source)
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	controller := newHubHostAdminController(newRecordingBroadcaster(), hosts, sources)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := controller.takeAttachWakeOwnership(source.ID())
	done := make(chan struct{})
	go func() {
		defer close(done)
		controller.fanOut(ctx, source, wake)
	}()

	// Let the fan-out enter backoff while the host reports offline: it must
	// not subscribe before the attach.
	time.Sleep(250 * time.Millisecond)
	if got := source.HostNotificationSubscribers(); got != 0 {
		t.Fatalf("offline fan-out subscribed %d times, want 0 before the attach", got)
	}

	// The attach flips the host online and wakes the broker for it.
	online.Store(true)
	controller.hostAttached("m4")
	waitForHostNotificationSubscribers(t, source, 1,
		"the attach event did not wake the backoff-sleeping fan-out")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fanOut did not return after its context was canceled")
	}
}

// TestHostAdminFanOutExitClearSparesReplacementWake pins the round-4 M5
// finding: during remove/re-add churn the cancelled predecessor's deferred
// clearAttachWake deleted whatever wake entry the host had — including the
// channel its REPLACEMENT was parked on — so the replacement's next
// EventAttached parked a wakeup nobody read and the fan-out slept its backoff
// out. Wake channels are owned per fan-out generation now: the exiting loop
// clears the entry only when it is still its own.
func TestHostAdminFanOutExitClearSparesReplacementWake(t *testing.T) {
	client, _, _ := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	var online atomic.Bool
	source.SetHostOnline(online.Load)
	sources := appsource.NewRegistry()
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	controller := newHubHostAdminController(newRecordingBroadcaster(), hosts, sources)

	// Generation one: it parks in backoff while the host reports offline.
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	// The launch registers the generation's wake channel synchronously, the
	// way launchFanOut does for a live launch.
	wake1 := controller.takeAttachWakeOwnership(source.ID())
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		controller.fanOut(ctx1, source, wake1)
	}()
	time.Sleep(250 * time.Millisecond)
	if got := source.HostNotificationSubscribers(); got != 0 {
		t.Fatalf("offline fan-out subscribed %d times, want 0 before the attach", got)
	}

	// The churn's re-add launches the replacement BEFORE the cancelled
	// predecessor exits — the removal's stop and the re-add's launch are two
	// notifications, so the replacement can hold its wake channel while the
	// predecessor is still winding down.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	// The re-add's launch registers the replacement's channel the same way.
	wake2 := controller.takeAttachWakeOwnership(source.ID())
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		controller.fanOut(ctx2, source, wake2)
	}()
	time.Sleep(250 * time.Millisecond)

	// The removal's cancellation ends generation one. Waiting for its exit
	// makes the ordering exact: the deferred wake clear has run before the
	// attach event below fires.
	cancel1()
	select {
	case <-done1:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled fan-out did not exit")
	}

	// The attach flips the host online and wakes the replacement through the
	// wake entry the REPLACEMENT registered. The replacement must subscribe
	// promptly: its first backoff timer is a full hostNotificationRetryBase
	// away, so a missed wakeup is distinguishable from a served one.
	online.Store(true)
	controller.hostAttached("m4")
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if got := source.HostNotificationSubscribers(); got == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the attach event did not wake the replacement fan-out: the predecessor's exit cleared its wake channel")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel2()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("the replacement fan-out did not return after its context was canceled")
	}
}

// TestHostAdminFanOutLateCancelledPredecessorCannotOrphanReplacementWake pins
// the round-6 M1 finding: wake ownership used to be registered asynchronously
// inside the fanOut goroutine, so during remove/re-add churn a predecessor
// whose body first ran AFTER its replacement registered could overwrite the
// replacement's wake channel and then delete the map entry on exit. The
// replacement stayed parked on an orphaned channel, missed the host's next
// attach wakeup, and slept through its full backoff. The predecessor's body is
// started only once the replacement is parked, so the harmful direction is
// deterministic rather than a scheduling hope — the round-4 test's early waits
// always let the predecessor register first and could not see this.
func TestHostAdminFanOutLateCancelledPredecessorCannotOrphanReplacementWake(t *testing.T) {
	client, _, _ := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	var online atomic.Bool
	onlineChecks := make(chan struct{}, 16)
	source.SetHostOnline(func() bool {
		select {
		case onlineChecks <- struct{}{}:
		default:
		}
		return online.Load()
	})
	sources := appsource.NewRegistry()
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	controller := newHubHostAdminController(newRecordingBroadcaster(), hosts, sources)

	// The remove/re-add churn's launch side: generation one's launch registers
	// its wake channel under the host, the removal cancels generation one and
	// drops the host's wake entry, and the re-add's launch registers the
	// replacement's channel.
	wake1 := controller.takeAttachWakeOwnership("m4")
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	cancel1()
	controller.stopFanOut("m4")
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	wake2 := controller.takeAttachWakeOwnership("m4")

	// The replacement runs first and parks in backoff while the host reports
	// offline. Its first Online() check proves it holds the wake entry the
	// re-add registered and is entering the wait, so everything the predecessor
	// does below happens after that registration.
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		controller.fanOut(ctx2, source, wake2)
	}()
	select {
	case <-onlineChecks:
	case <-time.After(5 * time.Second):
		t.Fatal("the replacement fan-out never reached its first Online() check")
	}

	// The cancelled predecessor's body runs only now, after the replacement
	// registered — the late scheduling the pre-fix asynchronous registration
	// could not defend against (that body registered over the replacement's
	// channel and deleted the host's wake entry on the way out). The fixed
	// launch binds each generation's channel before its goroutine exists, so
	// all this body can do is exit: its ownership-checked clear spares the
	// replacement's entry.
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		controller.fanOut(ctx1, source, wake1)
	}()
	select {
	case <-done1:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled predecessor did not exit")
	}

	// The attach flips the host online and wakes the replacement through the
	// wake entry under the host. The replacement's first backoff is a full
	// hostNotificationRetryBase away, so a served wakeup subscribes promptly
	// while a missed one is still sleeping when the deadline passes.
	online.Store(true)
	controller.hostAttached("m4")
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if got := source.HostNotificationSubscribers(); got == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the attach event did not wake the replacement fan-out: the late-scheduled predecessor orphaned its wake channel")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel2()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("the replacement fan-out did not return after its context was canceled")
	}
}

// TestHostAdminFanOutStopSparesWakeRegisteredMidTeardown pins the low-A
// finding: stopFanOut used to drop the host's wake entry after releasing
// fanOutMu, so a same-name re-add whose launch ran while the removal was
// mid-teardown registered the replacement's channel in that window and then
// lost it to the teardown's unconditional delete — the replacement parked on
// an orphaned channel, missed the host's next EventAttached wakeup, and slept
// out its full backoff. The predecessor's cancel handle parks the removal's
// teardown inside stop(), the point after which the old code still had the
// wake delete queued, so the re-add's launch provably runs mid-teardown —
// deterministic, not a scheduling hope. The fixed stopFanOut drops the entry
// inside the same fanOutMu critical section, before any launch can register.
func TestHostAdminFanOutStopSparesWakeRegisteredMidTeardown(t *testing.T) {
	client, _, _ := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	var online atomic.Bool
	onlineChecks := make(chan struct{}, 16)
	source.SetHostOnline(func() bool {
		select {
		case onlineChecks <- struct{}{}:
		default:
		}
		return online.Load()
	})
	sources := appsource.NewRegistry()
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	controller := newHubHostAdminController(newRecordingBroadcaster(), hosts, sources)

	// The removed generation: its launch registered a wake entry under the
	// host, and its fanOuts cancel handle parks the removal's teardown inside
	// stop().
	controller.takeAttachWakeOwnership("m4")
	inTeardown := make(chan struct{}, 1)
	release := make(chan struct{})
	controller.fanOutMu.Lock()
	controller.fanOuts["m4"] = func() {
		inTeardown <- struct{}{}
		<-release
	}
	controller.fanOutMu.Unlock()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		controller.stopFanOut("m4")
	}()
	select {
	case <-inTeardown:
	case <-time.After(5 * time.Second):
		t.Fatal("stopFanOut never reached the removed generation's cancel handle")
	}

	// The re-add's launch runs entirely while the removal is mid-teardown —
	// the interleaving the registry permits and the old unlock-to-delete
	// window admitted. The re-add registers its replacement source first — the
	// registry makes the insert visible before the add callback fires, and the
	// round-14 launch fence keys on that registration — then the launch
	// registers the replacement's wake channel synchronously under fanOutMu and
	// parks the replacement in backoff.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	sources.Add(source)
	controller.launchFanOut(ctx2, source)
	select {
	case <-onlineChecks:
	case <-time.After(5 * time.Second):
		t.Fatal("the replacement fan-out never reached its first Online() check")
	}

	// The removal's teardown completes only now, with the replacement already
	// registered: the old code's wake delete ran here and took the
	// replacement's entry; the fixed critical section dropped the removed
	// generation's entry before the launch could register.
	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopFanOut did not finish after its cancel handle returned")
	}

	// The attach flips the host online and must wake the replacement through
	// the wake entry the re-add registered. The replacement's first backoff
	// is a full hostNotificationRetryBase away, so a served wakeup subscribes
	// promptly while a missed one is still sleeping when the deadline passes.
	online.Store(true)
	controller.hostAttached("m4")
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if got := source.HostNotificationSubscribers(); got == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the attach event did not wake the replacement fan-out: the removal's teardown cleared its wake channel")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The replacement winds down with its context, so the test leaves no
	// goroutine behind.
	cancel2()
	deadline = time.Now().Add(5 * time.Second)
	for {
		if got := source.HostNotificationSubscribers(); got == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the replacement fan-out did not unwind after its context was canceled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestHostAdminFanOutStaleRemovalCallbackSparesReplacement pins the round-14
// finding: the registry delivers its remove callback outside its own lock, so
// a delayed removal can run after a same-name re-add already registered its
// replacement — and a stopFanOut keyed only by the host id would cancel the
// replacement's fan-out and delete its wake channel, so the re-added host's
// notifications went undelivered. The teardown is conditional on the removal
// still being the name's latest registry word now: a registered replacement
// spares the live fan-out and its wake entry. Deterministic, no sleeps: the
// removal callback is delivered after the re-add's registration by direct
// call order, an interleaving the registry's lock-free callback delivery
// permits, and the per-name state is asserted synchronously.
func TestHostAdminFanOutStaleRemovalCallbackSparesReplacement(t *testing.T) {
	client, _, _ := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	newSource := func() (*appsource.RemoteHubSource, *atomic.Bool, chan struct{}) {
		var online atomic.Bool
		onlineChecks := make(chan struct{}, 16)
		source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
			return client, nil
		})
		source.SetHostOnline(func() bool {
			select {
			case onlineChecks <- struct{}{}:
			default:
			}
			return online.Load()
		})
		return source, &online, onlineChecks
	}
	sources := appsource.NewRegistry()
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	controller := newHubHostAdminController(newRecordingBroadcaster(), hosts, sources)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The removed generation: registered and live, the way its add callback
	// left it.
	first, firstOnline, _ := newSource()
	firstOnline.Store(true)
	sources.Add(first)
	controller.launchFanOut(ctx, first) // the add callback's body
	waitHostSubscribers(t, first, 1)

	// The removal commits its registry delete. The registry fires the remove
	// callback after releasing its lock, so the delivery below can run
	// arbitrarily late — this test delivers it after the re-add.
	sources.Remove("m4")

	// The re-add registers the replacement and delivers its add callback,
	// which runs first: the replacement launches (replacing the removed
	// generation's loop) and parks in backoff while its host reports offline.
	second, secondOnline, secondChecks := newSource()
	sources.Add(second)
	controller.launchFanOut(ctx, second) // the re-add's add callback body
	select {
	case <-secondChecks:
	case <-time.After(5 * time.Second):
		t.Fatal("the replacement fan-out never reached its first Online() check")
	}
	waitHostSubscribers(t, first, 0) // the replaced loop stood down

	// The delayed removal callback for the removed generation runs now, with
	// the replacement registered: pre-fix it cancelled the replacement's
	// cancel handle and deleted its wake entry by name alone.
	controller.stopFanOut("m4")

	// The replacement's fan-out entry survived ...
	controller.fanOutMu.Lock()
	_, fanOutEntry := controller.fanOuts["m4"]
	controller.fanOutMu.Unlock()
	if !fanOutEntry {
		t.Fatal("a delayed removal callback for a replaced source cancelled the replacement's fan-out")
	}
	// ... and so did its wake channel.
	controller.attachWakeMu.Lock()
	_, wakeEntry := controller.attachWake["m4"]
	controller.attachWakeMu.Unlock()
	if !wakeEntry {
		t.Fatal("a delayed removal callback for a replaced source cleared the replacement's wake channel")
	}

	// And the replacement is observably alive: flipping online and attaching
	// must wake it through the surviving entry, well inside its first full
	// backoff (hostNotificationRetryBase).
	secondOnline.Store(true)
	controller.hostAttached("m4")
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if got := second.HostNotificationSubscribers(); got == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the attach event did not wake the replacement: the stale removal callback had torn it down")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	deadline = time.Now().Add(5 * time.Second)
	for {
		if got := second.HostNotificationSubscribers(); got == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the replacement fan-out did not unwind after its context was canceled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestHostAdminFanOutStaleAddCallbackLaunchesNothing pins the round-14
// finding's other half: the registry delivers its add callback outside its
// own lock, so a delayed add can run after a same-name remove already
// committed — and a launchFanOut keyed only by the source id would start a
// fan-out for a source the registry no longer holds, one that polls Online()
// until shutdown. The launch is conditional on the callback's source still
// being the registry's current entry now, so the stale add launches
// nothing. Deterministic, no sleeps: the per-name state is asserted
// synchronously after the callback body returns.
func TestHostAdminFanOutStaleAddCallbackLaunchesNothing(t *testing.T) {
	client, _, _ := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	var online atomic.Bool
	online.Store(true) // a pre-fix launch would subscribe immediately
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostOnline(online.Load)
	sources := appsource.NewRegistry()
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	controller := newHubHostAdminController(newRecordingBroadcaster(), hosts, sources)

	// The add commits its registry insert; the removal commits its delete.
	// The registry fires the add callback after releasing its lock, so its
	// body can run arbitrarily late — this test delivers it after the removal.
	sources.Add(source)
	sources.Remove("m4")
	// Nothing the launch could register outlives the callback body this test
	// pins, so the test's own context is the launch parent.
	controller.launchFanOut(t.Context(), source) // the delayed add callback's body

	// Nothing launched: no cancel handle and no wake entry under the name.
	controller.fanOutMu.Lock()
	_, fanOutEntry := controller.fanOuts["m4"]
	controller.fanOutMu.Unlock()
	if fanOutEntry {
		t.Fatal("a delayed add callback registered a fan-out for an already-removed source")
	}
	controller.attachWakeMu.Lock()
	_, wakeEntry := controller.attachWake["m4"]
	controller.attachWakeMu.Unlock()
	if wakeEntry {
		t.Fatal("a delayed add callback registered a wake entry for an already-removed source")
	}

	// And nothing comes up late: the removed source never gains a subscriber
	// it would keep polling Online() for until shutdown.
	time.Sleep(150 * time.Millisecond)
	if got := source.HostNotificationSubscribers(); got != 0 {
		t.Fatalf("a delayed add callback subscribed a removed source %d times", got)
	}
}

// TestHostAdminFanOutNormalAddAttachRemoveStillBehaves pins the round-14
// fence's negative space: the identity conditions must not suppress the
// normal lifecycle. Through the registry's own hooks — add launches, an
// EventAttached wakeup is harmless to a subscribed generation, and the prompt
// removal still tears the fan-out down exactly once: subscribers 0 -> 1 -> 0,
// one delivery per config notification, and no leaked per-name state.
func TestHostAdminFanOutNormalAddAttachRemoveStillBehaves(t *testing.T) {
	client, _, emit := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostOnline(func() bool { return true })
	sources := appsource.NewRegistry()
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	recorder := newRecordingBroadcaster()
	controller := newHubHostAdminController(recorder, hosts, sources)
	controller.start(t.Context())

	// Add: the registry's add callback launches the fan-out, and it runs —
	// subscribed and delivering.
	sources.Add(source)
	waitHostSubscribers(t, source, 1)
	emit(appwire.NotifyEvenerAuthUpdated, map[string]string{"provider": "openai"})
	expectOneHostNotification(t, recorder, "m4", appwire.NotifyEvenerAuthUpdated)

	// Attach: the EventAttached wakeup is harmless to a subscribed
	// generation, and the fan-out keeps delivering.
	controller.hostAttached("m4")
	emit(appwire.NotifyEvenerLaunchUpdated, map[string]string{"layer": "base"})
	expectOneHostNotification(t, recorder, "m4", appwire.NotifyEvenerLaunchUpdated)

	// Remove: the registry's remove callback must still tear the fan-out down
	// — the round-14 fence suppresses only stale deliveries, not the prompt
	// one. Stopped exactly once: subscribers reach zero and stay there, the
	// per-name state is gone, and a later remote notification delivers
	// nothing.
	sources.Remove("m4")
	waitHostSubscribers(t, source, 0)
	controller.fanOutMu.Lock()
	_, fanOutEntry := controller.fanOuts["m4"]
	controller.fanOutMu.Unlock()
	if fanOutEntry {
		t.Fatal("the prompt removal left the fan-out's cancel entry behind")
	}
	controller.attachWakeMu.Lock()
	_, wakeEntry := controller.attachWake["m4"]
	controller.attachWakeMu.Unlock()
	if wakeEntry {
		t.Fatal("the prompt removal left the fan-out's wake entry behind")
	}
	emit(appwire.NotifyEvenerMarketplaceUpdated, map[string]string{"marketplace": "main"})
	time.Sleep(100 * time.Millisecond)
	if got := recorder.broadcasts(); len(got) != 2 {
		t.Fatalf("broadcasts = %+v, want exactly the two pre-removal deliveries", got)
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

	t.Run("checkNow reports unknown outcome", func(t *testing.T) {
		// Round nine: evener/plugin/checkNow is a mutation, not the read its
		// name suggests. It runs one auto-upgrade daemon pass
		// (app_plugin_autoupgrade.go), which can install a new version of an
		// opted-in git-backed plugin on the remote host — the catalog
		// description says "per plugin actually upgraded" (protocol.go) and
		// TestRegisterPluginAutoUpgradeHandlers_CheckNowRunsOneTick shows the
		// upgraded ref coming back. A lost response to that pass cannot be told
		// apart from one where plugins were upgraded and only the answer was
		// lost, so it must report the outcome as unknown rather than a retryable
		// SessionUnavailable.
		controller, _, _ := scriptedHostAdmin(t, true, func(string, json.RawMessage) hostAdminReply {
			return hostAdminReply{closeConn: true}
		})
		_, err := controller.Request(context.Background(), appwire.HostRequestParams{
			Host:   "m4",
			Method: appwire.MethodEvenerPluginCheckNow,
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
	// evener/host/attach is a controller-local mutation, never a forwarded one:
	// it must stay off both the allow-list and the forwarded-mutation set, so the
	// proxy can never forward a dial request to a peer hub.
	if _, ok := remoteHostAdminMethods[appwire.MethodEvenerHostAttach]; ok {
		t.Errorf("%q must not be on the remote-admin allow-list: it is a controller-local method", appwire.MethodEvenerHostAttach)
	}
	if _, ok := remoteHostAdminMutationMethods[appwire.MethodEvenerHostAttach]; ok {
		t.Errorf("%q must not be classified as a forwarded mutation: it is a controller-local method", appwire.MethodEvenerHostAttach)
	}
}

// sharedHostRequestMethodsPath is the checked-in list the web UI's forwarded
// set and this proxy's allow-list are BOTH pinned to, next to this file. Its
// header states the whole contract; the two tests that read it are
// TestHostAdminAllowListCoversSharedForwardedMethods here and
// hostRouting.test.ts's inventory assertion on the frontend side.
const sharedHostRequestMethodsPath = "host_request_methods.txt"

// readSharedHostRequestMethods parses the checked-in list: one method name per
// non-empty line, '#' comments and blank lines ignored, the same shape as the
// fuzz registry.
func readSharedHostRequestMethods(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(sharedHostRequestMethodsPath)
	if err != nil {
		t.Fatalf("read %s: %v (the cross-language pin is only real while both sides can read it)", sharedHostRequestMethodsPath, err)
	}
	var methods []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		methods = append(methods, line)
	}
	return methods
}

// TestHostAdminAllowListCoversSharedForwardedMethods pins the CROSS-LANGUAGE
// half of the proxy's boundary, which neither side's own table can see. The
// frontend's forwarded set (hostRouting.ts's HOST_DEPENDENT_DISCOVERY_METHODS)
// is checked in at host_request_methods.txt and asserted there against the
// shipped set; here every method on that list must be BOTH on this allow-list
// and actually forwarded rather than refused. That second half matters because
// the allow-list's own test answers only to this package's policy table: a
// maintainer who removes a method and flips its row stays green there while the
// browser keeps forwarding a call the proxy answers with InvalidParams.
func TestHostAdminAllowListCoversSharedForwardedMethods(t *testing.T) {
	methods := readSharedHostRequestMethods(t)
	if len(methods) == 0 {
		t.Fatalf("%s names no forwarded methods, which would make this pin vacuous", sharedHostRequestMethodsPath)
	}

	controller, _, calls := scriptedHostAdmin(t, true, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	for _, name := range methods {
		if _, ok := remoteHostAdminMethods[name]; !ok {
			t.Errorf("the web UI forwards %q but the proxy's allow-list does not name it; every remote call for it is refused with InvalidParams", name)
			continue
		}
		before := len(calls())
		if _, err := controller.Request(context.Background(), appwire.HostRequestParams{Host: "m4", Method: name}); err != nil {
			t.Errorf("forwarded method %q was refused by the proxy: %v", name, err)
			continue
		}
		if after := len(calls()); after != before+1 {
			t.Errorf("forwarded method %q was not forwarded (remote calls %d -> %d)", name, before, after)
		}
	}
}

// TestHostAdminFanOutStartsForRuntimeAddedSource pins the dynamic half of the
// round-2 medium: the notification fan-out follows the shared source registry,
// so a host whose source registers after start (the host-management surface's
// runtime add) gains its fan-out immediately — and a source registered under a
// name that already has a loop replaces that loop instead of running beside
// it: the predecessor's subscription retires before the replacement's fan-out
// delivers, so one host has exactly one loop.
func TestHostAdminFanOutStartsForRuntimeAddedSource(t *testing.T) {
	newSource := func() (*appsource.RemoteHubSource, func(method string, params any)) {
		client, _, emit := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
			return okReply()
		})
		source := appsource.NewRemoteHubSource("runtime-side", nil, func(context.Context, string) (*appwire.Client, error) {
			return client, nil
		})
		source.SetHostOnline(func() bool { return true })
		return source, emit
	}
	sources := appsource.NewRegistry()
	hosts, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	recorder := newRecordingBroadcaster()
	controller := newHubHostAdminController(recorder, hosts, sources)
	controller.start(t.Context())

	// The fan-out starts for a source registered after start returned — the
	// runtime-add shape.
	first, emitFirst := newSource()
	sources.Add(first)
	waitHostSubscribers(t, first, 1)
	emitFirst(appwire.NotifyEvenerAuthUpdated, map[string]string{"provider": "openai"})
	expectOneHostNotification(t, recorder, "runtime-side", appwire.NotifyEvenerAuthUpdated)

	// A source registered under the same name replaces the loop: the
	// predecessor's subscription retires and the replacement's fan-out
	// delivers exactly one more notification.
	second, emitSecond := newSource()
	sources.Add(second)
	waitHostSubscribers(t, second, 1)
	waitHostSubscribers(t, first, 0)
	emitSecond(appwire.NotifyEvenerMarketplaceUpdated, map[string]string{"marketplace": "main"})
	expectOneHostNotification(t, recorder, "runtime-side", appwire.NotifyEvenerMarketplaceUpdated)

	if got := recorder.broadcasts(); len(got) != 2 {
		t.Fatalf("broadcasts = %+v, want exactly one per emitted config update", got)
	}
}

// TestHostAdminFanOutStopsOnSourceRemoval pins the round-3 medium: Remove on
// the shared source registry used to delete only the map entry, so the removed
// host's fan-out goroutine kept polling Online() until shutdown and churn
// leaked one goroutine + RemoteHubSource + fanOuts/attachWake slot per name.
// The registry's on-remove notification now drives the controller's teardown:
// subscribers go to zero, fanOuts drops the name, the host's attachWake entry
// is cleared, and a notification the scripted remote still emits reaches no
// one.
func TestHostAdminFanOutStopsOnSourceRemoval(t *testing.T) {
	client, _, emit := newScriptedAdminClient(t, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})
	source := appsource.NewRemoteHubSource("runtime-side", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostOnline(func() bool { return true })
	sources := appsource.NewRegistry()
	hosts, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	recorder := newRecordingBroadcaster()
	controller := newHubHostAdminController(recorder, hosts, sources)
	controller.start(t.Context())

	sources.Add(source)
	waitHostSubscribers(t, source, 1)
	emit(appwire.NotifyEvenerAuthUpdated, map[string]string{"provider": "openai"})
	expectOneHostNotification(t, recorder, "runtime-side", appwire.NotifyEvenerAuthUpdated)

	// Park a pending attach wakeup under the name first, so the removal's
	// attachWake cleanup has an entry to clear.
	controller.hostAttached("runtime-side")
	controller.fanOutMu.Lock()
	_, running := controller.fanOuts["runtime-side"]
	controller.fanOutMu.Unlock()
	if !running {
		t.Fatal("the added source gained no fan-out entry")
	}

	sources.Remove("runtime-side")
	waitHostSubscribers(t, source, 0)
	controller.fanOutMu.Lock()
	_, leakedFanOut := controller.fanOuts["runtime-side"]
	controller.fanOutMu.Unlock()
	if leakedFanOut {
		t.Fatal("fanOuts kept the removed host's cancel entry")
	}
	controller.attachWakeMu.Lock()
	_, leakedWake := controller.attachWake["runtime-side"]
	controller.attachWakeMu.Unlock()
	if leakedWake {
		t.Fatal("attachWake kept the removed host's wakeup channel")
	}
	// The cancelled loop is gone: the remote keeps emitting, but nothing is
	// subscribed to relay it.
	emit(appwire.NotifyEvenerMarketplaceUpdated, map[string]string{"marketplace": "main"})
	time.Sleep(100 * time.Millisecond)
	if got := recorder.broadcasts(); len(got) != 1 {
		t.Fatalf("broadcasts = %+v, want only the pre-removal fan-out delivery", got)
	}
}

// waitHostSubscribers waits until source reports want live host-level
// subscribers: the observable form of "the fan-out is subscribed" and "the
// replaced loop stood down".
func waitHostSubscribers(t *testing.T, source *appsource.RemoteHubSource, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := source.HostNotificationSubscribers(); got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("subscribers = %d, want %d", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// expectOneHostNotification waits until exactly one host-tagged fan-out
// broadcast has landed for (host, method), and settles: a duplicate arriving
// shortly after the first fails the test.
func expectOneHostNotification(t *testing.T, recorder *recordingBroadcaster, host, method string) {
	t.Helper()
	count := func() int {
		found := 0
		for _, r := range recorder.broadcasts() {
			if r.method != appwire.NotifyEvenerHostNotification {
				continue
			}
			envelope, ok := r.params.(appwire.HostNotificationParams)
			if !ok {
				continue
			}
			if envelope.Host == host && envelope.Method == method {
				found++
			}
		}
		return found
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		switch got := count(); {
		case got == 1:
			// Settle: a second delivery from a duplicated loop would land
			// immediately after the first.
			time.Sleep(150 * time.Millisecond)
			if got := count(); got != 1 {
				t.Fatalf("host-tagged %s for %s delivered %d times, want exactly one", method, host, got)
			}
			return
		case got > 1:
			t.Fatalf("host-tagged %s for %s delivered %d times, want exactly one", method, host, got)
		case time.Now().After(deadline):
			t.Fatalf("timed out waiting for the host-tagged %s for %s; broadcasts = %+v", method, host, recorder.broadcasts())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
