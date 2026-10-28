package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// TestLaunchInstanceExists_AcceptsAProviderTheContractDidNotEnumerate pins
// the registry-only rule (spec §11.3): a launch model contract that never
// listed an instance does not make that instance unlaunchable, as long as the
// registry has it. A name the registry does not have is still refused.
func TestLaunchInstanceExists_AcceptsAProviderTheContractDidNotEnumerate(t *testing.T) {
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, "[providers.work]\nbase = \"anthropic\"\napi_key = \"sk-inline\"\n")
	cfg := hubcore.WebConfig{Registry: newTestRegistry(t, t.TempDir(), tomlPath, nil, nil)}

	if !launchInstanceExists(cfg, "work") {
		t.Error("an instance the registry holds is launchable even when the contract omits it")
	}
	if !launchInstanceExists(cfg, "WORK") {
		t.Error("the instance name is matched case-insensitively, as the launch ref is")
	}
	if launchInstanceExists(cfg, "nowhere") {
		t.Error("a name the registry does not have must not be launchable")
	}
	if launchInstanceExists(hubcore.WebConfig{}, "work") {
		t.Error("with no registry there is nothing to accept on")
	}
}

func TestHubRPCInstanceListRoutesToController(t *testing.T) {
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, "[providers.my-openai]\nbase = \"openai\"\napi_key = \"sk-inline\"\n")
	credsStore := newTestCredentialsStore(t)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, credsStore, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceList, appwire.EmptyParams{}, &resp); err != nil {
		t.Fatalf("evener/instance/list: %v", err)
	}
	found := false
	for _, inst := range resp.Instances {
		if inst.Name == "my-openai" {
			found = true
		}
	}
	if !found {
		t.Fatalf("instances=%+v, want the authored my-openai entry", resp.Instances)
	}
	if len(resp.AvailableProviders) == 0 {
		t.Error("AvailableProviders must be non-empty in list response")
	}
	hasOpenAI := false
	for _, p := range resp.AvailableProviders {
		if p.ID == "openai" {
			hasOpenAI = true
		}
	}
	if !hasOpenAI {
		t.Errorf("AvailableProviders=%+v missing the openai registry id", resp.AvailableProviders)
	}
}

// TestHubRPCInstanceCreateBroadcastsAuthUpdated proves the "multiple browsers
// stay in sync" founding requirement for provider-instance CRUD: a successful
// evener/instance/create must broadcast evener/auth/updated so every other
// connected client refetches its now-stale instance list. The notification is
// reused rather than a new one minted for instances: notifications.js already
// treats evener/auth/updated as payload-agnostic ("something about credentials
// or instances changed, refetch"), reloading both the instances panel and the
// providers settings tab on receipt.
func TestHubRPCInstanceCreateBroadcastsAuthUpdated(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, credsStore, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate, appwire.InstanceCreateParams{Base: "anthropic", Name: "mywork"}, &resp); err != nil {
		t.Fatalf("evener/instance/create: %v", err)
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("method=%q, want %q", got.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for evener/auth/updated broadcast after instance create")
	}
}

// TestHubRPCInstanceCreateAnswersWithItsOwnState: a create's response must be
// the listing the create itself produced, captured under its write lock, not a
// later List() a concurrent edit can slip into. The two destinations differ
// only in their query string, so the sanitized display URL is identical and
// the immutable fingerprint is the only thing that tells them apart - exactly
// the case a client comparing display URLs misreads as its own creation.
func TestHubRPCInstanceCreateAnswersWithItsOwnState(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)

	// Pause the create inside its held write lock, so a concurrent edit is
	// guaranteed to be waiting for that lock when the create releases it. Go's
	// RWMutex admits a waiting writer before a new reader, which puts the edit
	// between the create's write and the listing its handler answers with.
	armed := atomic.Bool{}
	loadEntered := make(chan struct{})
	releaseLoad := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLoad) }) }
	baseLoader := testRegistryLoader(dir, tomlPath, credsStore, nil)
	holder := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		if armed.CompareAndSwap(true, false) {
			close(loadEntered)
			<-releaseLoad
		}
		return baseLoader(extra...)
	})
	if err := holder.Reload(); err != nil {
		t.Fatalf("registry: %v", err)
	}
	// The observer reports the controller the server builds, so the test can
	// arm that controller's List gate rather than a process-wide one.
	var ctl *hubInstancesController
	previousObserver := observeInstancesController
	observeInstancesController = func(observed *hubInstancesController) { ctl = observed }
	t.Cleanup(func() { observeInstancesController = previousObserver })
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            holder,
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	defer hub.Close()
	if ctl == nil {
		t.Fatal("the app server built no instances controller")
	}
	client := dialHubRPC(t, hub)
	defer client.Close()
	// A second connection: AppWire requests on one client are serialized, and
	// the create is still in flight while the edit must reach the hub.
	editClient := dialHubRPC(t, hub)
	defer editClient.Close()
	mustInitializeHubClient(t, client)
	if _, err := editClient.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize(edit client): %v", err)
	}

	const (
		authoredURL = "http://127.0.0.1:9/v1?tenant=authored"
		racedURL    = "http://127.0.0.1:9/v1?tenant=raced"
	)
	type outcome struct {
		resp appwire.InstanceListResponse
		err  error
	}
	createDone := make(chan outcome, 1)
	armed.Store(true)
	go func() {
		var resp appwire.InstanceListResponse
		err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate, appwire.InstanceCreateParams{
			Name: "work", Base: "anthropic", BaseURL: authoredURL,
		}, &resp)
		createDone <- outcome{resp: resp, err: err}
	}()
	select {
	case <-loadEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("Create never reached its reload, so the loader was not paused")
	}

	// Hold any post-mutation List() until the racing edit has finished, so the
	// listing a regressed handler reads is the edit's state. The correct
	// handler returns its captured listing and never reaches this seam.
	editFinished := make(chan struct{})
	ctl.beforeList = func() { <-editFinished }
	editDone := make(chan outcome, 1)
	go func() {
		defer close(editFinished)
		var resp appwire.InstanceListResponse
		err := editClient.Request(context.Background(), appwire.MethodEvenerInstanceEdit, appwire.InstanceEditParams{
			Name: "work", BaseURL: racedURL,
		}, &resp)
		editDone <- outcome{resp: resp, err: err}
	}()
	release()

	created := <-createDone
	if created.err != nil {
		t.Fatalf("create: %v", created.err)
	}
	edited := <-editDone
	if edited.err != nil {
		t.Fatalf("edit: %v", edited.err)
	}
	createdRow := entry(t, created.resp, "work")
	editedRow := entry(t, edited.resp, "work")
	if createdRow.BaseURL != editedRow.BaseURL {
		t.Fatalf("fixture drift: query-only destinations should show the same sanitized URL, got %q and %q", createdRow.BaseURL, editedRow.BaseURL)
	}
	if createdRow.EndpointFingerprint == "" || editedRow.EndpointFingerprint == "" {
		t.Fatalf("fixture drift: both destinations need fingerprints, got %q and %q", createdRow.EndpointFingerprint, editedRow.EndpointFingerprint)
	}
	if createdRow.EndpointFingerprint == editedRow.EndpointFingerprint {
		t.Fatalf("create answered with the edit's destination %q: the response was not the listing captured under the create's write lock", createdRow.EndpointFingerprint)
	}
}

// TestHubRPCInstanceBroadcastEchoesOriginClientId is the instance-side
// counterpart of TestAuthApiKeySetBroadcastEchoesOriginClientId: a mutation
// from a client that names itself must broadcast an evener/auth/updated
// carrying that same id, so the originator recognizes its own echo by id
// instead of treating its own mutation as another client's change and
// refetching. Every registered instance mutation is covered, each against its
// own hub so one case's write cannot perturb the next.
func TestHubRPCInstanceBroadcastEchoesOriginClientId(t *testing.T) {
	cases := []struct {
		name   string
		method string
		params func(origin string) any
	}{
		{"create", appwire.MethodEvenerInstanceCreate, func(origin string) any {
			return appwire.InstanceCreateParams{Base: "anthropic", Name: "mywork", OriginClientId: origin}
		}},
		{"edit", appwire.MethodEvenerInstanceEdit, func(origin string) any {
			return appwire.InstanceEditParams{Name: "base", BaseURL: "https://example.test", OriginClientId: origin}
		}},
		{"remove", appwire.MethodEvenerInstanceRemove, func(origin string) any {
			return appwire.InstanceRemoveParams{Name: "base", OriginClientId: origin}
		}},
		{"setDefault", appwire.MethodEvenerInstanceSetDefault, func(origin string) any {
			return appwire.InstanceSetDefaultParams{Name: "base", OriginClientId: origin}
		}},
		{"setModelDisabled", appwire.MethodEvenerInstanceSetModelDisabled, func(origin string) any {
			return appwire.InstanceSetModelDisabledParams{Name: "base", Model: "claude-opus-4-6", Disabled: true, OriginClientId: origin}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newAuthOriginTestClient(t)

			var resp appwire.InstanceListResponse
			if err := client.Request(context.Background(), tc.method, tc.params("tab-a"), &resp); err != nil {
				t.Fatalf("%s: %v", tc.method, err)
			}

			assertInstanceBroadcastShape(t, tc.method, waitForAuthUpdatedRaw(t, client), "tab-a")
		})
	}
}

// assertInstanceBroadcastShape requires an instance mutation's broadcast to echo
// wantOrigin and to name no provider or active source. The emptiness is part of
// the contract, not incidental: the SDK's own-echo correlation keys on the
// absent provider to tell an instance echo from an auth one, so a stray
// provider would reroute the notification into its auth fallback.
func assertInstanceBroadcastShape(t *testing.T, method string, raw json.RawMessage, wantOrigin string) {
	t.Helper()
	// Key ABSENCE, not an empty value: a payload carrying `"provider":""` decodes
	// to the same empty string as an omitted key, but the SDK routes an instance
	// echo by `provider === undefined`, so an explicit empty provider would send
	// it looking for a "" marker and read the client's own mutation as foreign.
	// Only the raw bytes can pin that.
	if bytes.Contains(raw, []byte("provider")) || bytes.Contains(raw, []byte("activeSource")) {
		t.Errorf("%s params=%s, want neither the provider nor the activeSource key: an instance broadcast names no auth source",
			method, raw)
	}
	var params appwire.EvenerAuthUpdatedParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("%s: decode params %s: %v", method, raw, err)
	}
	if params.OriginClientId != wantOrigin {
		t.Errorf("%s params=%s: originClientId=%q, want %q", method, raw, params.OriginClientId, wantOrigin)
	}
}

// TestHubRPCInstanceRefreshModelsBroadcastEchoesOriginClientId covers the sixth
// mutation handler, whose success needs a live /models endpoint: its broadcast
// must echo the caller's id like the other five.
func TestHubRPCInstanceRefreshModelsBroadcastEchoesOriginClientId(t *testing.T) {
	tomlPath := refreshGateway(t, `{"data":[{"id":"gpt-live"}]}`)
	dir := filepath.Dir(tomlPath)
	credsStore := newTestCredentialsStore(t)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, credsStore, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { client.Close() })

	mustInitializeHubClient(t, client)

	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceRefreshModels,
		appwire.InstanceRefreshModelsParams{Name: "gw", OriginClientId: "tab-a"}, &resp); err != nil {
		t.Fatalf("evener/instance/refreshModels: %v", err)
	}

	assertInstanceBroadcastShape(t, appwire.NotifyEvenerAuthUpdated, waitForAuthUpdatedRaw(t, client), "tab-a")
}

// TestHubRPCInstanceCreateBroadcastWithoutOriginClientIdHasNone is the control
// for TestHubRPCInstanceBroadcastEchoesOriginClientId: the identical
// create with no id must broadcast an empty one, so the id the first test
// observes is the caller's value rather than one the hub supplies on its own.
func TestHubRPCInstanceCreateBroadcastWithoutOriginClientIdHasNone(t *testing.T) {
	client := newAuthOriginTestClient(t)

	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate,
		appwire.InstanceCreateParams{Base: "anthropic", Name: "mywork"}, &resp); err != nil {
		t.Fatalf("evener/instance/create: %v", err)
	}

	assertInstanceBroadcastShape(t, appwire.NotifyEvenerAuthUpdated, waitForAuthUpdatedRaw(t, client), "")
}

// TestHubRPCInstanceEditBroadcastsAuthUpdated is the evener/instance/edit sibling
// of TestHubRPCInstanceCreateBroadcastsAuthUpdated; see its doc comment for why
// evener/auth/updated is the right (reused) notification.
func TestHubRPCInstanceEditBroadcastsAuthUpdated(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, credsStore, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceEdit, appwire.InstanceEditParams{Name: "base", BaseURL: "https://example.test"}, &resp); err != nil {
		t.Fatalf("evener/instance/edit: %v", err)
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("method=%q, want %q", got.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for evener/auth/updated broadcast after instance edit")
	}
}

// A rename whose credential move fails still reached providers.toml and the
// registry, so every other client's instance list is stale by exactly as much
// as it would be on success: the broadcast has to fire even though the call
// comes back an error naming what was left behind. The failure is injected in
// the credentials store's own directory, which is a different directory from
// providers.toml's, so the whole move is the real one and the config write still
// lands.
func TestHubRPCInstanceEditRenameBroadcastsWhenTheCredentialMoveFails(t *testing.T) {
	requireWritableDirRefusal(t)
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)
	if err := credsStore.Set("base", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	breakCredentialWrites(t, credsStore.Path())
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, credsStore, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	var resp appwire.InstanceListResponse
	err := client.Request(context.Background(), appwire.MethodEvenerInstanceEdit, appwire.InstanceEditParams{Name: "base", NewName: "personal", OriginClientId: "tab-a"}, &resp)
	if err == nil {
		t.Fatal("evener/instance/edit = nil, want the leftover credential reported")
	}
	// The refused write left the key behind under the old name.
	if v, _ := credsStore.Get("base"); v != "sk-stored" {
		t.Fatalf("stored key under the old name = %q, want the leftover the failed move kept", v)
	}
	// The discriminator the web sheet keys on: the message alone is identical
	// whether or not the error is wrapped in the evenerErrorInfo payload, so
	// decode the wire data the client reads and pin it here.
	wire := requireWireError(t, err, appwire.CodeInternalError)
	dataJSON, merr := json.Marshal(wire.Data)
	if merr != nil {
		t.Fatal(merr)
	}
	var data appwire.ErrorData
	if err := json.Unmarshal(dataJSON, &data); err != nil {
		t.Fatalf("decode rename-persisted error data: %v", err)
	}
	if data.EvenerErrorInfo != appwire.ErrorInstanceRenamePersisted {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorInstanceRenamePersisted)
	}
	// The file is the new name either way, which is what the other clients
	// are now out of date against.
	if _, ok := readConfigProviders(t, tomlPath)["personal"]; !ok {
		t.Fatal("the rename did not reach providers.toml")
	}

	// A rename that persisted before it failed announces as loudly as a clean
	// one - the error reply is not the only signal every other client's list is
	// stale - but it names no origin: the caller's mutation errored, so its echo
	// must not be consumable as that client's own success.
	assertInstanceBroadcastShape(t, "rename whose credential move failed", waitForAuthUpdatedRaw(t, client), "")
}

// The sibling case: the credential move succeeded and the reload that follows
// it failed, so the rename is on disk in full — providers.toml and
// credentials.toml both carry the new name — and only the hub's view of it is
// behind. Every other client is stale by exactly as much as after a clean
// rename, so the broadcast has to fire here too.
func TestHubRPCInstanceEditRenameBroadcastsWhenTheFinalReloadFails(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)
	if err := credsStore.Set("base", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// The stored key under the new name is what marks the move as done, so
	// this refuses the reload that runs after it and no earlier one: every
	// load before the move — including the one right after providers.toml is
	// written — still finds the key under the old name.
	load := testRegistryLoader(t.TempDir(), tomlPath, credsStore, nil)
	reg := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		if v, _ := credsStore.Get("personal"); v != "" {
			return nil, nil, errors.New("registry refused the reload after the credential move")
		}
		return load(extra...)
	})
	if err := reg.Reload(); err != nil {
		t.Fatalf("registry: %v", err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            reg,
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	var resp appwire.InstanceListResponse
	err := client.Request(context.Background(), appwire.MethodEvenerInstanceEdit, appwire.InstanceEditParams{Name: "base", NewName: "personal"}, &resp)
	if err == nil || !strings.Contains(err.Error(), "registry refused the reload after the credential move") {
		t.Fatalf("evener/instance/edit = %v, want the failed reload reported", err)
	}
	// Same discriminator as the credential-move sibling, on the reload-failed
	// half: the rename stood, so the client is told so through the wire data,
	// not inferred from a message it shares with a plain failure.
	wire := requireWireError(t, err, appwire.CodeInternalError)
	dataJSON, merr := json.Marshal(wire.Data)
	if merr != nil {
		t.Fatal(merr)
	}
	var data appwire.ErrorData
	if err := json.Unmarshal(dataJSON, &data); err != nil {
		t.Fatalf("decode rename-persisted error data: %v", err)
	}
	if data.EvenerErrorInfo != appwire.ErrorInstanceRenamePersisted {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorInstanceRenamePersisted)
	}
	if _, ok := readConfigProviders(t, tomlPath)["personal"]; !ok {
		t.Fatal("the rename did not reach providers.toml")
	}
	if v, _ := credsStore.Get("personal"); v != "sk-stored" {
		t.Fatalf("the credential move did not run: personal = %q", v)
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("method=%q, want %q", got.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for evener/auth/updated after a rename whose final reload failed")
	}
}

// The third rename half: writeAndReload's own reload fails and the rollback
// write that follows it fails too, so providers.toml carries the new name with
// no rollback to undo it. The rename is as persisted as the two cases above and
// the client must be steered to the new name, not told a plain failed save -
// the same ErrorInstanceRenamePersisted discriminator. The seam is
// newInstanceRollbackFixture: setting failReload makes the next registry reload
// fail and blocks the providers.toml temp path, which is exactly the double
// failure writeAndReload reports as write-applied.
func TestHubRPCInstanceEditRenamePersistsWhenTheInitialReloadAndRollbackFail(t *testing.T) {
	f := newInstanceRollbackFixture(t)
	client := dialHubRPC(t, f.hub)
	defer client.Close()
	mustInitializeHubClient(t, client)
	f.failReload.Store(true)

	var resp appwire.InstanceListResponse
	err := client.Request(context.Background(), appwire.MethodEvenerInstanceEdit,
		appwire.InstanceEditParams{Name: "base", NewName: "personal"}, &resp)
	if err == nil {
		t.Fatal("evener/instance/edit = nil, want the reload-and-rollback double failure")
	}
	// The rename stood in providers.toml, so the client gets the discriminator
	// rather than a plain failed save it would present as nothing happened.
	wire := requireWireError(t, err, appwire.CodeInternalError)
	dataJSON, merr := json.Marshal(wire.Data)
	if merr != nil {
		t.Fatal(merr)
	}
	var data appwire.ErrorData
	if err := json.Unmarshal(dataJSON, &data); err != nil {
		t.Fatalf("decode rename-persisted error data: %v", err)
	}
	if data.EvenerErrorInfo != appwire.ErrorInstanceRenamePersisted {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorInstanceRenamePersisted)
	}
	// The file is the new name: the rollback never landed, which is what makes
	// this the persisted-rename case and not the ordinary refusal.
	if _, ok := readConfigProviders(t, f.tomlPath)["personal"]; !ok {
		t.Fatal("the rename did not reach providers.toml")
	}

	waitForAuthUpdatedBroadcast(t, client, "a rename whose initial reload and rollback both failed")
}

// TestHubRPCInstanceRemoveBroadcastsAuthUpdated is the evener/instance/remove
// sibling of TestHubRPCInstanceCreateBroadcastsAuthUpdated; see its doc
// comment for why evener/auth/updated is the right (reused) notification.
func TestHubRPCInstanceRemoveBroadcastsAuthUpdated(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, credsStore, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceRemove, appwire.InstanceRemoveParams{Name: "base"}, &resp); err != nil {
		t.Fatalf("evener/instance/remove: %v", err)
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("method=%q, want %q", got.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for evener/auth/updated broadcast after instance remove")
	}
}

// TestHubRPCInstanceSetDefaultBroadcastsAuthUpdated is the
// evener/instance/setDefault sibling of TestHubRPCInstanceCreateBroadcastsAuthUpdated;
// see its doc comment for why evener/auth/updated is the right (reused)
// notification.
func TestHubRPCInstanceSetDefaultBroadcastsAuthUpdated(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, credsStore, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceSetDefault, appwire.InstanceSetDefaultParams{Name: "base"}, &resp); err != nil {
		t.Fatalf("evener/instance/setDefault: %v", err)
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("method=%q, want %q", got.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for evener/auth/updated broadcast after instance setDefault")
	}
}
