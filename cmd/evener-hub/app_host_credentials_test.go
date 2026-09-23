package hub

// evener/host/pushCredentials (component 07c): the controller copies its local
// provider-instance keys to one named host. The unit is the local
// credentials-store entry; the join onto the remote `Provider` value is
// identity on the instance name, and the write is the host's atomic
// conditional set. These tests drive the pusher against a scripted remote hub —
// no SSH, no network, no host.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
)

// credentialPushHarness wires the pusher to a scripted remote host "m4". dials
// counts how many times the Ensure-backed client seam was reached, so a test
// can assert a refusal happened before any dial.
type credentialPushHarness struct {
	pusher *hubHostCredentialsPusher
	calls  func() []hostAdminCall
	dials  func() int
}

func newCredentialPushHarness(
	t *testing.T,
	creds *credentials.Store,
	online bool,
	handle func(method string, params json.RawMessage) hostAdminReply,
) *credentialPushHarness {
	t.Helper()
	client, calls, _ := newScriptedAdminClient(t, handle)
	var mu sync.Mutex
	dials := 0
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		mu.Lock()
		dials++
		mu.Unlock()
		return client, nil
	})
	source.SetHostOnline(func() bool { return online })
	sources := appsource.NewRegistry()
	sources.Add(source)
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	admin := newHubHostAdminController(newRecordingBroadcaster(), hosts, sources)
	return &credentialPushHarness{
		pusher: &hubHostCredentialsPusher{admin: admin, creds: creds},
		calls:  calls,
		dials: func() int {
			mu.Lock()
			defer mu.Unlock()
			return dials
		},
	}
}

// resultFor returns the one result for instance, or fails.
func resultFor(t *testing.T, resp appwire.HostPushCredentialsResponse, instance string) appwire.HostCredentialPushResult {
	t.Helper()
	var found []appwire.HostCredentialPushResult
	for _, result := range resp.Results {
		if result.Instance == instance {
			found = append(found, result)
		}
	}
	if len(found) != 1 {
		t.Fatalf("results = %+v, want exactly one for %q", resp.Results, instance)
	}
	return found[0]
}

// countedMethod returns how many recorded remote calls used method, and their
// decoded params.
func countedMethod(t *testing.T, calls []hostAdminCall, method string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	for _, call := range calls {
		if call.method == method {
			out = append(out, call.params)
		}
	}
	return out
}

func statusReply(source, revision string) hostAdminReply {
	return hostAdminReply{result: appwire.AuthStatusResponse{
		Provider:       "x",
		Supported:      true,
		ActiveSource:   source,
		ConfigRevision: revision,
	}}
}

// providerOf decodes the Provider out of an auth/status params payload.
func providerOf(t *testing.T, params json.RawMessage) string {
	t.Helper()
	var p appwire.AuthStatusParams
	if err := json.Unmarshal(params, &p); err != nil {
		t.Fatalf("decode auth/status params: %v", err)
	}
	return p.Provider
}

// TestHostPushCredentials_WritesAndReportsPerInstanceActions is the core push:
// each local entry is read, joined on the host by instance name, fenced with the
// source/revision the status read captured, and written by the host's
// conditional set; the report carries the host's own action and the local store
// is never written.
func TestHostPushCredentials_WritesAndReportsPerInstanceActions(t *testing.T) {
	store := newTestCredentialsStore(t)
	if err := store.Set("openai", "sk-openai-local"); err != nil {
		t.Fatalf("seed openai: %v", err)
	}
	if err := store.Set("anthropic", "sk-anthropic-local"); err != nil {
		t.Fatalf("seed anthropic: %v", err)
	}

	var mu sync.Mutex
	setParams := map[string]appwire.ApiKeyConditionalSetParams{}
	h := newCredentialPushHarness(t, store, true, func(method string, params json.RawMessage) hostAdminReply {
		switch method {
		case appwire.MethodEvenerInstanceList:
			if string(params) != `{}` {
				t.Errorf("instance/list params = %s, want the EmptyParams {}", params)
			}
			return hostAdminReply{result: appwire.InstanceListResponse{
				Instances: []appwire.InstanceEntry{{Name: "openai"}, {Name: "anthropic"}},
			}}
		case appwire.MethodEvenerAuthStatus:
			return statusReply("store", "rev-"+providerOf(t, params))
		case appwire.MethodEvenerAuthApiKeyConditionalSet:
			var decoded appwire.ApiKeyConditionalSetParams
			if err := json.Unmarshal(params, &decoded); err != nil {
				t.Fatalf("decode conditionalSet params: %v", err)
			}
			mu.Lock()
			setParams[decoded.Provider] = decoded
			mu.Unlock()
			action := appwire.ApiKeyConditionalSetActionAdded
			if decoded.Provider == "anthropic" {
				action = appwire.ApiKeyConditionalSetActionUpdated
			}
			return hostAdminReply{result: appwire.ApiKeyConditionalSetResponse{Action: action, Status: appwire.AuthStatusResponse{Provider: decoded.Provider, ActiveSource: "store"}}}
		default:
			return hostAdminReply{result: appwire.EmptyResponse{}}
		}
	})

	resp, err := h.pusher.Push(context.Background(), appwire.HostPushCredentialsParams{Host: "m4"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if resp.Host != "m4" {
		t.Fatalf("Host = %q, want m4", resp.Host)
	}
	if got := resultFor(t, resp, "openai"); got.Action != appwire.HostCredentialPushAdded {
		t.Fatalf("openai result = %+v, want added", got)
	}
	if got := resultFor(t, resp, "anthropic"); got.Action != appwire.HostCredentialPushUpdated {
		t.Fatalf("anthropic result = %+v, want updated", got)
	}

	// The wire Provider is the local store key, verbatim, for both provider-keyed
	// methods; and the fence echoes exactly what the status read returned.
	mu.Lock()
	defer mu.Unlock()
	for name, value := range map[string]string{"openai": "sk-openai-local", "anthropic": "sk-anthropic-local"} {
		got, ok := setParams[name]
		if !ok {
			t.Fatalf("no conditionalSet for %q; set params = %+v", name, setParams)
		}
		if got.Value != value {
			t.Fatalf("conditionalSet(%s).Value = %q, want the local store value", name, got.Value)
		}
		if got.ExpectedSource != "store" || got.ExpectedRevision != "rev-"+name {
			t.Fatalf("conditionalSet(%s) fence = (%q,%q), want the status read's (store,rev-%s)", name, got.ExpectedSource, got.ExpectedRevision, name)
		}
	}

	// instance/list is called exactly once; status and conditionalSet once per
	// matched entry.
	if n := len(countedMethod(t, h.calls(), appwire.MethodEvenerInstanceList)); n != 1 {
		t.Fatalf("instance/list calls = %d, want exactly 1", n)
	}
	for _, method := range []string{appwire.MethodEvenerAuthStatus, appwire.MethodEvenerAuthApiKeyConditionalSet} {
		if n := len(countedMethod(t, h.calls(), method)); n != 2 {
			t.Fatalf("%s calls = %d, want one per matched entry", method, n)
		}
	}

	// The local store is untouched: the push never writes the controller's own store.
	for name, value := range map[string]string{"openai": "sk-openai-local", "anthropic": "sk-anthropic-local"} {
		got, ok := store.Get(name)
		if !ok || got != value {
			t.Fatalf("local store %q = %q present=%v, want it unchanged", name, got, ok)
		}
	}
}

// TestHostPushCredentials_OneRefusedEntryDoesNotAbortTheRest is the fence's
// consumer contract: a per-entry failure (a stale-revision refusal from the
// host's conditional set) is reported as "failed" with the reason and the
// remaining entries are still pushed.
func TestHostPushCredentials_OneRefusedEntryDoesNotAbortTheRest(t *testing.T) {
	store := newTestCredentialsStore(t)
	if err := store.Set("a", "sk-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("b", "sk-b"); err != nil {
		t.Fatal(err)
	}

	stale := appwire.Conflict("a changed on the host after this credential was prepared: its configuration revision no longer matches the one this request observed; re-read the instance and start the push again")
	h := newCredentialPushHarness(t, store, true, func(method string, params json.RawMessage) hostAdminReply {
		switch method {
		case appwire.MethodEvenerInstanceList:
			return hostAdminReply{result: appwire.InstanceListResponse{
				Instances: []appwire.InstanceEntry{{Name: "a"}, {Name: "b"}},
			}}
		case appwire.MethodEvenerAuthStatus:
			return statusReply("store", "rev")
		case appwire.MethodEvenerAuthApiKeyConditionalSet:
			var decoded appwire.ApiKeyConditionalSetParams
			if err := json.Unmarshal(params, &decoded); err != nil {
				t.Fatalf("decode conditionalSet params: %v", err)
			}
			if decoded.Provider == "a" {
				wire := stale
				return hostAdminReply{wireErr: &wire}
			}
			return hostAdminReply{result: appwire.ApiKeyConditionalSetResponse{Action: appwire.ApiKeyConditionalSetActionUpdated, Status: appwire.AuthStatusResponse{Provider: "b", ActiveSource: "store"}}}
		default:
			return hostAdminReply{result: appwire.EmptyResponse{}}
		}
	})

	resp, err := h.pusher.Push(context.Background(), appwire.HostPushCredentialsParams{Host: "m4"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	failed := resultFor(t, resp, "a")
	if failed.Action != appwire.HostCredentialPushFailed {
		t.Fatalf("a result = %+v, want failed", failed)
	}
	if !strings.Contains(failed.Reason, "configuration revision no longer matches") {
		t.Fatalf("a reason = %q, want the host's stale-revision text", failed.Reason)
	}
	if got := resultFor(t, resp, "b"); got.Action != appwire.HostCredentialPushUpdated {
		t.Fatalf("b result = %+v, want updated: a refused entry must not abort the rest", got)
	}
	// b was still pushed, and both were attempted.
	if n := len(countedMethod(t, h.calls(), appwire.MethodEvenerAuthApiKeyConditionalSet)); n != 2 {
		t.Fatalf("conditionalSet calls = %d, want one per entry", n)
	}
	// The refused entry's key is never written by the controller's store either.
	if got, ok := store.Get("a"); !ok || got != "sk-a" {
		t.Fatalf("local store a = %q present=%v, want it unchanged", got, ok)
	}
}

// TestHostPushCredentials_ImplicitProviderFallbackJoinsByProviderID proves the
// join matches a curated implicit provider by AvailableProviders[].ID when the
// host has no explicit instance under that name, and that the instance name is
// never passed to instance/list.
func TestHostPushCredentials_ImplicitProviderFallbackJoinsByProviderID(t *testing.T) {
	store := newTestCredentialsStore(t)
	if err := store.Set("openai", "sk-openai-local"); err != nil {
		t.Fatal(err)
	}
	h := newCredentialPushHarness(t, store, true, func(method string, params json.RawMessage) hostAdminReply {
		switch method {
		case appwire.MethodEvenerInstanceList:
			// No explicit instance rows at all; the implicit fallback is the only
			// counterpart.
			return hostAdminReply{result: appwire.InstanceListResponse{
				AvailableProviders: []appwire.ProviderDescriptor{{ID: "openai", Name: "OpenAI", Implicit: true}},
			}}
		case appwire.MethodEvenerAuthStatus:
			return statusReply("none", "rev-openai")
		case appwire.MethodEvenerAuthApiKeyConditionalSet:
			return hostAdminReply{result: appwire.ApiKeyConditionalSetResponse{Action: appwire.ApiKeyConditionalSetActionAdded, Status: appwire.AuthStatusResponse{Provider: "openai", ActiveSource: "store"}}}
		default:
			return hostAdminReply{result: appwire.EmptyResponse{}}
		}
	})

	resp, err := h.pusher.Push(context.Background(), appwire.HostPushCredentialsParams{Host: "m4"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := resultFor(t, resp, "openai"); got.Action != appwire.HostCredentialPushAdded {
		t.Fatalf("openai result = %+v, want added via the implicit-provider fallback", got)
	}
	// The name is a Provider value to the auth methods, never a parameter to
	// instance/list.
	for _, params := range countedMethod(t, h.calls(), appwire.MethodEvenerInstanceList) {
		if string(params) != `{}` {
			t.Fatalf("instance/list params = %s, want {} (no instance parameter)", params)
		}
	}
	statuses := countedMethod(t, h.calls(), appwire.MethodEvenerAuthStatus)
	if len(statuses) != 1 || !strings.Contains(string(statuses[0]), `"provider":"openai"`) {
		t.Fatalf("auth/status params = %v, want provider openai", statuses)
	}
}

// TestHostPushCredentials_NoMatchingInstanceSkips proves a local key with no
// remote counterpart is skipped with the spec's reason and is not pushed under a
// guessed provider.
func TestHostPushCredentials_NoMatchingInstanceSkips(t *testing.T) {
	store := newTestCredentialsStore(t)
	if err := store.Set("orphan", "sk-orphan"); err != nil {
		t.Fatal(err)
	}
	h := newCredentialPushHarness(t, store, true, func(method string, params json.RawMessage) hostAdminReply {
		switch method {
		case appwire.MethodEvenerInstanceList:
			return hostAdminReply{result: appwire.InstanceListResponse{
				Instances:          []appwire.InstanceEntry{{Name: "other"}},
				AvailableProviders: []appwire.ProviderDescriptor{{ID: "another"}},
			}}
		default:
			return hostAdminReply{result: appwire.EmptyResponse{}}
		}
	})

	resp, err := h.pusher.Push(context.Background(), appwire.HostPushCredentialsParams{Host: "m4"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	got := resultFor(t, resp, "orphan")
	if got.Action != appwire.HostCredentialPushSkipped || got.Reason != "no matching instance on the host" {
		t.Fatalf("orphan result = %+v, want a skip with the no-counterpart reason", got)
	}
	if n := len(countedMethod(t, h.calls(), appwire.MethodEvenerAuthApiKeyConditionalSet)); n != 0 {
		t.Fatalf("conditionalSet calls = %d, want none for a key with no counterpart", n)
	}
}

// TestHostPushCredentials_NonImplicitProviderIsNotAMatch pins the implicit half
// of the join: an AvailableProviders entry that is not implicit is an
// authoring/curation target, not an instance the host resolves a key under, so
// a local key naming it is skipped as "no matching instance on the host" and
// never forwarded.
func TestHostPushCredentials_NonImplicitProviderIsNotAMatch(t *testing.T) {
	store := newTestCredentialsStore(t)
	if err := store.Set("openai-compatible", "sk-local"); err != nil {
		t.Fatal(err)
	}
	h := newCredentialPushHarness(t, store, true, func(method string, params json.RawMessage) hostAdminReply {
		switch method {
		case appwire.MethodEvenerInstanceList:
			// The ID is listed as an available provider, but not as an implicit
			// one: the host's own fallback requires Implicit.
			return hostAdminReply{result: appwire.InstanceListResponse{
				AvailableProviders: []appwire.ProviderDescriptor{{ID: "openai-compatible", Name: "OpenAI-compatible", Implicit: false}},
			}}
		default:
			return hostAdminReply{result: appwire.EmptyResponse{}}
		}
	})

	resp, err := h.pusher.Push(context.Background(), appwire.HostPushCredentialsParams{Host: "m4"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	got := resultFor(t, resp, "openai-compatible")
	if got.Action != appwire.HostCredentialPushSkipped || got.Reason != "no matching instance on the host" {
		t.Fatalf("result = %+v, want a skip with the no-counterpart reason", got)
	}
	for _, method := range []string{appwire.MethodEvenerAuthStatus, appwire.MethodEvenerAuthApiKeyConditionalSet} {
		if n := len(countedMethod(t, h.calls(), method)); n != 0 {
			t.Fatalf("%s calls = %d, want none: a non-implicit provider is not a match", method, n)
		}
	}
}

// TestHostPushCredentials_BlankLocalEntryStillGetsARow pins the row contract:
// every entry the store listed gets exactly one result, including one whose
// value is gone by the time the push reads it. The store lists an entry with a
// blank value (Names sees it, Get does not) - the shape a clear landing between
// the listing and the read leaves.
func TestHostPushCredentials_BlankLocalEntryStillGetsARow(t *testing.T) {
	store := newTestCredentialsStore(t)
	if err := store.Set("gone", "  "); err != nil {
		t.Fatalf("seed blank entry: %v", err)
	}
	if names := store.Names(); len(names) != 1 || names[0] != "gone" {
		t.Fatalf("store.Names() = %v, want the blank entry listed", names)
	}
	if _, ok := store.Get("gone"); ok {
		t.Fatal("the blank entry reads back a value; this fixture cannot exercise the vanished path")
	}
	h := newCredentialPushHarness(t, store, true, func(method string, params json.RawMessage) hostAdminReply {
		switch method {
		case appwire.MethodEvenerInstanceList:
			return hostAdminReply{result: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{{Name: "gone"}}}}
		default:
			return hostAdminReply{result: appwire.EmptyResponse{}}
		}
	})

	resp, err := h.pusher.Push(context.Background(), appwire.HostPushCredentialsParams{Host: "m4"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %+v, want one row for the one entry Names() listed", resp.Results)
	}
	got := resultFor(t, resp, "gone")
	if got.Action != appwire.HostCredentialPushSkipped || got.Reason == "" {
		t.Fatalf("result = %+v, want a skip with a reason", got)
	}
	if n := len(countedMethod(t, h.calls(), appwire.MethodEvenerAuthApiKeyConditionalSet)); n != 0 {
		t.Fatalf("conditionalSet calls = %d, want none for an entry with no value", n)
	}
}

// TestHostPushCredentials_RefusesRemoteOriginatedBeforeAnyDial pins the shared
// origin guard: a bridge-originated push is refused typed, reaches no remote
// host, and never dials.
func TestHostPushCredentials_RefusesRemoteOriginatedBeforeAnyDial(t *testing.T) {
	store := newTestCredentialsStore(t)
	if err := store.Set("openai", "sk-openai-local"); err != nil {
		t.Fatal(err)
	}
	h := newCredentialPushHarness(t, store, true, func(string, json.RawMessage) hostAdminReply {
		return hostAdminReply{result: appwire.InstanceListResponse{}}
	})

	_, err := h.pusher.Push(
		withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge),
		appwire.HostPushCredentialsParams{Host: "m4"},
	)
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if !strings.Contains(err.Error(), hostRoutingOriginBridge) {
		t.Fatalf("refusal %q does not name the origin %q", err, hostRoutingOriginBridge)
	}
	if got := h.dials(); got != 0 {
		t.Fatalf("remote-originated push dialed the host %d times, want 0", got)
	}
	if got := h.calls(); len(got) != 1 {
		t.Fatalf("remote calls = %+v, want only the harness's initialize", got)
	}
}

// TestHostPushCredentials_UnknownAndUnattachedHostsRefuseTyped proves each host
// failure has its own typed refusal instead of an empty success.
func TestHostPushCredentials_UnknownAndUnattachedHostsRefuseTyped(t *testing.T) {
	store := newTestCredentialsStore(t)
	if err := store.Set("openai", "sk-openai-local"); err != nil {
		t.Fatal(err)
	}

	// Unknown host.
	h := newCredentialPushHarness(t, store, true, func(string, json.RawMessage) hostAdminReply {
		return hostAdminReply{result: appwire.InstanceListResponse{}}
	})
	_, err := h.pusher.Push(context.Background(), appwire.HostPushCredentialsParams{Host: "nope"})
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if !strings.Contains(err.Error(), "unknown host") {
		t.Fatalf("unknown-host refusal = %q, want it to say unknown host", err)
	}
	if got := h.dials(); got != 0 {
		t.Fatalf("unknown-host push dialed %d times, want 0", got)
	}

	// Known but not attached.
	offline := newCredentialPushHarness(t, store, false, func(string, json.RawMessage) hostAdminReply {
		return hostAdminReply{result: appwire.InstanceListResponse{}}
	})
	_, err = offline.pusher.Push(context.Background(), appwire.HostPushCredentialsParams{Host: "m4"})
	assertWireCode(t, err, appwire.CodeUnavailable)
	if !strings.Contains(err.Error(), "not attached") {
		t.Fatalf("unattached refusal = %q, want it to say not attached", err)
	}
	if got := offline.dials(); got != 0 {
		t.Fatalf("unattached push dialed %d times, want 0", got)
	}
	if got := offline.calls(); len(got) != 1 {
		t.Fatalf("unattached remote calls = %+v, want only the harness's initialize", got)
	}
}

// TestHostPushCredentials_ServedOverHubRPC proves the method is registered and
// reachable over the real hub RPC edge with its declared wire shape.
func TestHostPushCredentials_ServedOverHubRPC(t *testing.T) {
	stateDir := t.TempDir()
	store := newTestCredentialsStore(t)
	if err := store.Set("openai", "sk-openai-local"); err != nil {
		t.Fatal(err)
	}
	reg := newTestRegistry(t, stateDir, "", store, nil)

	client, calls, _ := newScriptedAdminClient(t, func(method string, params json.RawMessage) hostAdminReply {
		switch method {
		case appwire.MethodEvenerInstanceList:
			return hostAdminReply{result: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{{Name: "openai"}}}}
		case appwire.MethodEvenerAuthStatus:
			return statusReply("none", "rev-openai")
		case appwire.MethodEvenerAuthApiKeyConditionalSet:
			return hostAdminReply{result: appwire.ApiKeyConditionalSetResponse{Action: appwire.ApiKeyConditionalSetActionAdded, Status: appwire.AuthStatusResponse{Provider: "openai", ActiveSource: "store"}}}
		default:
			return hostAdminReply{result: appwire.EmptyResponse{}}
		}
	})
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostOnline(func() bool { return true })

	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	srv, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		HubStateRoot:        stateDir,
		RemoteHostRegistry:  hosts,
		CredsStore:          store,
		Registry:            reg,
		ProvidersConfigPath: filepath.Join(t.TempDir(), "providers.toml"),
	})
	defer srv.Close()
	web.sources.Add(source)

	rpc := dialHubRPC(t, srv)
	defer rpc.Close()
	if _, err := rpc.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var resp appwire.HostPushCredentialsResponse
	if err := rpc.Request(context.Background(), appwire.MethodEvenerHostPushCredentials, appwire.HostPushCredentialsParams{Host: "m4"}, &resp); err != nil {
		t.Fatalf("evener/host/pushCredentials: %v", err)
	}
	if resp.Host != "m4" || len(resp.Results) != 1 || resp.Results[0].Instance != "openai" || resp.Results[0].Action != appwire.HostCredentialPushAdded {
		t.Fatalf("response = %+v, want one added result for openai", resp)
	}
	if n := len(countedMethod(t, calls(), appwire.MethodEvenerAuthApiKeyConditionalSet)); n != 1 {
		t.Fatalf("conditionalSet calls = %d, want 1", n)
	}
}

// TestHostPushCredentials_ReportNeverCarriesTheKeyValue pins the invariant the
// push's doc and spec criterion 7 (07-remote-admin.md: "No key value appears in
// the push response, controller logs, or errors") state: no key value reaches
// the response. It drives every non-success path the harness can produce with a
// value in flight - a refused conditional set (the fence), an unreadable status,
// a skipped entry - and marshals the WHOLE response, so a field added later is
// covered by construction rather than by remembering to extend a field list.
func TestHostPushCredentials_ReportNeverCarriesTheKeyValue(t *testing.T) {
	// Distinctive on purpose: a value short or guessable could pass this by
	// coincidence inside some unrelated field.
	const marker = "vbt9Qm2Xr7Lp4Kd8Ns3Zf6Hw1Yc5Jt0Bg7Ru2Ea9Pi4Ol6Dd"

	store := newTestCredentialsStore(t)
	for _, name := range []string{"written", "refused", "unreadable", "orphan"} {
		if err := store.Set(name, marker+"-"+name); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	h := newCredentialPushHarness(t, store, true, func(method string, params json.RawMessage) hostAdminReply {
		switch method {
		case appwire.MethodEvenerInstanceList:
			// "orphan" is deliberately absent: no counterpart on the host.
			return hostAdminReply{result: appwire.InstanceListResponse{
				Instances: []appwire.InstanceEntry{{Name: "written"}, {Name: "refused"}, {Name: "unreadable"}},
			}}
		case appwire.MethodEvenerAuthStatus:
			if providerOf(t, params) == "unreadable" {
				wire := appwire.Unavailable("host could not read the instance status")
				return hostAdminReply{wireErr: &wire}
			}
			return statusReply("store", "rev")
		case appwire.MethodEvenerAuthApiKeyConditionalSet:
			var decoded appwire.ApiKeyConditionalSetParams
			if err := json.Unmarshal(params, &decoded); err != nil {
				t.Fatalf("decode conditionalSet params: %v", err)
			}
			if decoded.Provider == "refused" {
				wire := appwire.Conflict("refused changed on the host after this credential was prepared: its configuration revision no longer matches the one this request observed; re-read the instance and start the push again")
				return hostAdminReply{wireErr: &wire}
			}
			return hostAdminReply{result: appwire.ApiKeyConditionalSetResponse{
				Action: appwire.ApiKeyConditionalSetActionAdded,
				Reason: "added",
				Status: appwire.AuthStatusResponse{Provider: decoded.Provider, ActiveSource: "store"},
			}}
		default:
			return hostAdminReply{result: appwire.EmptyResponse{}}
		}
	})

	resp, err := h.pusher.Push(context.Background(), appwire.HostPushCredentialsParams{Host: "m4"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Every path ran: added, two failures, one skip. A run that silently skipped
	// everything would make the hygiene assertion below vacuous.
	if got := resultFor(t, resp, "written").Action; got != appwire.HostCredentialPushAdded {
		t.Fatalf("written = %q, want added", got)
	}
	if got := resultFor(t, resp, "refused").Action; got != appwire.HostCredentialPushFailed {
		t.Fatalf("refused = %q, want failed", got)
	}
	if got := resultFor(t, resp, "unreadable").Action; got != appwire.HostCredentialPushFailed {
		t.Fatalf("unreadable = %q, want failed", got)
	}
	if got := resultFor(t, resp, "orphan").Action; got != appwire.HostCredentialPushSkipped {
		t.Fatalf("orphan = %q, want skipped", got)
	}

	// Positive control: the marker really did cross the wire, as the conditional
	// set's Value, so its absence from the response is not "nothing was pushed".
	var carried bool
	for _, params := range countedMethod(t, h.calls(), appwire.MethodEvenerAuthApiKeyConditionalSet) {
		if strings.Contains(string(params), marker) {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("the marker never reached the conditional set; the hygiene assertion would be vacuous")
	}

	// The whole report, as a client would receive it.
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(encoded), marker) {
		t.Fatalf("the push response carries a key value:\n%s", encoded)
	}
}

// broadcastInstancesToml declares two key-capable instances the conditional
// set classifies differently: "addable" has no credential (a push is added),
// and "headerful" resolves from authored credential headers (a push is
// skipped).
const broadcastInstancesToml = `[providers.addable]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"

[providers.headerful]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"

[providers.headerful.credential_headers]
Authorization = "Bearer $HDR_KEY"
`

// TestAuthApiKeyConditionalSetBroadcastsOnlyOnALandedWrite pins the broadcast
// branch in the evener/auth/apiKey/conditionalSet handler (app_rpc.go): a
// landed write owes the same evener/auth/updated every other credential write
// broadcasts, and a skipped classification - which wrote nothing - must not
// make clients refetch. The push handler itself registers the pusher directly
// and deliberately broadcasts nothing: it writes the HOST's store, so the
// browser learns of it through the host-notification fan-out, not a
// controller-side auth/updated.
func TestAuthApiKeyConditionalSetBroadcastsOnlyOnALandedWrite(t *testing.T) {
	stateDir := t.TempDir()
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, broadcastInstancesToml)
	store := newTestCredentialsStore(t)
	reg := newTestRegistry(t, stateDir, tomlPath, store, map[string]string{"HDR_KEY": "sk-hdr"})

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            reg,
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        stateDir,
		CredsStore:          store,
	})
	defer hub.Close()
	rpc := dialHubRPC(t, hub)
	defer rpc.Close()
	if _, err := rpc.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// A landed write broadcasts evener/auth/updated naming the instance and the
	// source it now resolves from.
	var added appwire.ApiKeyConditionalSetResponse
	if err := rpc.Request(context.Background(), appwire.MethodEvenerAuthApiKeyConditionalSet,
		appwire.ApiKeyConditionalSetParams{Provider: "addable", Value: "sk-pushed"}, &added); err != nil {
		t.Fatalf("evener/auth/apiKey/conditionalSet: %v", err)
	}
	if added.Action != appwire.ApiKeyConditionalSetActionAdded {
		t.Fatalf("action = %q, want added", added.Action)
	}
	params := waitForAuthUpdated(t, rpc)
	if params.Provider != "addable" || params.ActiveSource != "store" {
		t.Fatalf("broadcast params = %+v, want the pushed instance addable resolving from store", params)
	}

	// A skipped classification writes nothing and broadcasts nothing.
	var skipped appwire.ApiKeyConditionalSetResponse
	if err := rpc.Request(context.Background(), appwire.MethodEvenerAuthApiKeyConditionalSet,
		appwire.ApiKeyConditionalSetParams{Provider: "headerful", Value: "sk-pushed"}, &skipped); err != nil {
		t.Fatalf("evener/auth/apiKey/conditionalSet (skip): %v", err)
	}
	if skipped.Action != appwire.ApiKeyConditionalSetActionSkipped {
		t.Fatalf("action = %q, want skipped", skipped.Action)
	}
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case note, ok := <-rpc.Notifications():
			if !ok {
				t.Fatal("notification channel closed before the negative window ended")
			}
			if note.Method == appwire.NotifyEvenerAuthUpdated {
				t.Fatalf("a skipped conditional set broadcast %s; a skip writes nothing and must not make clients refetch", appwire.NotifyEvenerAuthUpdated)
			}
		case <-deadline:
			return
		}
	}
}
