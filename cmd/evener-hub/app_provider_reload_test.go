package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/githubcopilot"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

func TestProviderFileRepairsAndInvalidEdits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	invalid := "[providers.work\n"
	valid := "[providers.work]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"fixture\"\n"
	write := func(raw string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(invalid)
	reg := hubcore.NewProviderRegistry(testRegistryLoader(dir, path, nil, map[string]string{}))
	if err := reg.Reload(); err == nil {
		t.Fatal("invalid startup unexpectedly loaded")
	}
	ctl := &hubInstancesController{reg: reg, providersConfigPath: path}
	// The observer remains owned even when its first read sees invalid bytes.
	ctl.refreshProviderFile()
	write(valid)
	if !ctl.refreshProviderFile() {
		t.Fatal("repair was not announced")
	}
	list := ctl.List()
	if list.WritesRefused {
		t.Fatal("repair retained write refusal")
	}
	found := false
	for _, inst := range list.Instances {
		if inst.Name == "work" && inst.BaseURL == "http://127.0.0.1:9/v1" {
			found = true
		}
	}
	if !found {
		t.Fatal("repair listing lost work endpoint")
	}
	before := reg.Get()
	write(invalid)
	if !ctl.refreshProviderFile() {
		t.Fatal("invalid edit was not announced")
	}
	if reg.Get() != before || !ctl.List().WritesRefused {
		t.Fatal("invalid edit discarded active config or permitted writes")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != invalid {
		t.Fatalf("edited bytes = %q, err = %v", raw, err)
	}
	if ctl.refreshProviderFile() {
		t.Fatal("unchanged malformed bytes caused repeated invalidation")
	}
	write(valid)
	if !ctl.refreshProviderFile() || ctl.List().WritesRefused {
		t.Fatal("second repair failed to converge")
	}
	if ctl.refreshProviderFile() {
		t.Fatal("unchanged valid file caused repeated invalidation")
	}
}

func TestProviderFileObserverPublishesToOpenClients(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeProvidersToml(t, dir, "[providers.work\n")
	reg := hubcore.NewProviderRegistry(testRegistryLoader(dir, path, nil, map[string]string{}))
	_ = reg.Reload()
	server, _, _, n, _, _ := newHubAppServerWithNavigationAndTrace(hubcore.WebConfig{Registry: reg, ProvidersConfigPath: path}, appsource.NewRegistry(), nil, nil, nil)
	t.Cleanup(func() { server.Shutdown(context.Background()) })
	if n.refreshProviders == nil {
		t.Fatal("server did not retain the recovery owner")
	}
	broadcaster := newRecordingBroadcaster()
	n.OnCommitted(func(change noticeCommitted) { dispatchNoticeCommitted(broadcaster, change) })
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	observed := make(chan struct{})
	done := make(chan struct{})
	go func() {
		n.runNoticeWatcher(ctx, ticks, func(ctx context.Context) []appwire.HubNotice {
			result := n.watchRead(ctx)
			observed <- struct{}{}
			return result
		})
		close(done)
	}()
	<-observed
	baseline := broadcaster.broadcasts()
	writeProvidersToml(t, dir, "[providers.work]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"fixture\"\n")
	ticks <- time.Time{}
	<-observed
	got := broadcaster.broadcasts()
	if len(got) != len(baseline)+1 || got[len(got)-1].method != "evener/auth/updated" {
		t.Fatalf("repair broadcasts = %+v", got)
	}
	ticks <- time.Time{}
	<-observed
	cancel()
	<-done
	if !reflect.DeepEqual(got, broadcaster.broadcasts()) {
		t.Fatal("unchanged repair announced again")
	}
}

func TestProviderFileRetriesFailedLoadWithoutAnotherEdit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeProvidersToml(t, dir, "[providers.work]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\n")
	load := testRegistryLoader(dir, path, nil, map[string]string{})
	fail := true
	reg := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		if fail {
			return nil, nil, errors.New("fixture unavailable")
		}
		return load(extra...)
	})
	_ = reg.Reload()
	ctl := &hubInstancesController{reg: reg, providersConfigPath: path}
	if ctl.refreshProviderFile() {
		t.Fatal("unchanged failed load announced repeatedly")
	}
	fail = false
	if !ctl.refreshProviderFile() || reg.WritesRefused() {
		t.Fatal("recovery stopped after unchanged bytes failed")
	}
}
func TestProviderFileWatcherDoesNotEchoInstanceWrite(t *testing.T) {
	t.Parallel()
	f := newInstancesFixture(t, nil)
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredsStore: f.store,
	})
	defer hub.Close()
	// The initial observation belongs to startup, before this client connects.
	web.notices.OnCommitted(func(noticeCommitted) {})
	web.notices.watchRead(context.Background())
	client := dialHubRPCInitialized(t, hub)
	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate,
		appwire.InstanceCreateParams{Name: "work", Base: "anthropic", OriginClientId: "tab-a"}, &resp); err != nil {
		t.Fatal(err)
	}
	assertInstanceBroadcastShape(t, appwire.MethodEvenerInstanceCreate, waitForAuthUpdatedRaw(t, client), "tab-a")
	generation := f.ctl.reg.Generation()
	broadcaster := newRecordingBroadcaster()
	web.notices.OnCommitted(func(change noticeCommitted) { dispatchNoticeCommitted(broadcaster, change) })
	web.notices.watchRead(context.Background())
	if f.ctl.reg.Generation() != generation || len(broadcaster.broadcasts()) != 0 {
		t.Fatal("watcher reloaded and announced an already-notified instance write")
	}
	// A later external edit still needs the unowned broadcast.
	writeProvidersToml(t, filepath.Dir(f.tomlPath), "[providers.external]\nbase = \"anthropic\"\n")
	web.notices.watchRead(context.Background())
	got := broadcaster.broadcasts()
	if len(got) != 1 || got[0].method != appwire.NotifyEvenerAuthUpdated {
		t.Fatalf("external edit broadcasts = %+v", got)
	}
}

func TestProviderFileWatcherDoesNotEchoRolledBackWrite(t *testing.T) {
	t.Parallel()
	f := newFlakyReloadFixture(t, "", func(load int) bool { return load == 3 })
	f.ctl.refreshProviderFile() // load 2 establishes the observer baseline
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "anthropic"}); err == nil {
		t.Fatal("create unexpectedly survived the scripted load failure")
	}
	generation := f.ctl.reg.Generation()
	if f.ctl.refreshProviderFile() || f.ctl.reg.Generation() != generation {
		t.Fatal("watcher reloaded or announced a rolled-back instance write")
	}
}

// copilotCLI drives the real Copilot service against a scripted GitHub, the
// same way the CLI signs a machine in or out while the hub runs.
type copilotCLI struct {
	service *githubcopilot.Service
}

func newCopilotCLI(t *testing.T) *copilotCLI {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch r.URL.Path {
		case "/user":
			// The numeric id follows the access token, so a test changes the
			// account by signing in with a different token.
			fmt.Fprintf(w, `{"id":%s,"login":"octocat","email":"octocat@example.com"}`, strings.TrimPrefix(token, "gh-"))
		case "/copilot_internal/v2/token":
			fmt.Fprintf(w, `{"token":"copilot-session-%s","expires_at":%d}`, token, time.Now().Add(24*time.Hour).Unix())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return &copilotCLI{service: githubcopilot.NewService(githubcopilot.Config{
		GitHubBaseURL: server.URL, APIBaseURL: server.URL,
	}, server.Client())}
}

func (c *copilotCLI) login(t *testing.T, stateDir, instance, accessToken string) {
	t.Helper()
	record, err := c.service.PrepareLogin(context.Background(), instance, githubcopilot.TokenSet{AccessToken: accessToken})
	if err != nil {
		t.Fatalf("prepare the %s login: %v", instance, err)
	}
	if _, err := c.service.SavePreparedLogin(stateDir, instance, record); err != nil {
		t.Fatalf("save the %s login: %v", instance, err)
	}
}

func (c *copilotCLI) logout(t *testing.T, stateDir, instance string) {
	t.Helper()
	if _, err := c.service.Logout(stateDir, instance); err != nil {
		t.Fatalf("log %s out: %v", instance, err)
	}
}

func authUpdatedCount(b *recordingBroadcaster) int {
	count := 0
	for _, broadcast := range b.broadcasts() {
		if broadcast.method == appwire.NotifyEvenerAuthUpdated {
			count++
		}
	}
	return count
}

func TestCopilotRecordWatcherAdoptsExternalLoginAndLogout(t *testing.T) {
	t.Parallel()
	f := newInstancesFixture(t, nil)
	cli := newCopilotCLI(t)
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredsStore: f.store,
	})
	defer hub.Close()
	// Startup observations belong to the process, not to this client.
	web.notices.OnCommitted(func(noticeCommitted) {})
	web.notices.watchRead(context.Background())
	broadcaster := newRecordingBroadcaster()
	web.notices.OnCommitted(func(change noticeCommitted) { dispatchNoticeCommitted(broadcaster, change) })

	cli.login(t, f.stateDir, "github-copilot", "gh-1001")
	web.notices.watchRead(context.Background())
	if got := authUpdatedCount(broadcaster); got != 1 {
		t.Fatalf("external login broadcasts = %d, want one", got)
	}
	if !f.ctl.reg.Get().HasInstance("github-copilot") {
		t.Fatal("the external login did not add the Copilot instance")
	}
	if name, _, err := f.ctl.reg.Get().DefaultInstance(); err != nil || name != "github-copilot" {
		t.Fatalf("default = %q err = %v, want the signed-in Copilot account", name, err)
	}
	// A refresh rotates tokens and timestamps, never the account.
	cli.login(t, f.stateDir, "github-copilot", "gh-1001")
	web.notices.watchRead(context.Background())
	if got := authUpdatedCount(broadcaster); got != 1 {
		t.Fatalf("a token refresh announced a change: %d broadcasts", got)
	}
	cli.login(t, f.stateDir, "github-copilot", "gh-2002")
	web.notices.watchRead(context.Background())
	if got := authUpdatedCount(broadcaster); got != 2 {
		t.Fatalf("an account switch broadcasts = %d, want two", got)
	}
	cli.logout(t, f.stateDir, "github-copilot")
	web.notices.watchRead(context.Background())
	if got := authUpdatedCount(broadcaster); got != 3 {
		t.Fatalf("an external logout broadcasts = %d, want three", got)
	}
	if f.ctl.reg.Get().HasInstance("github-copilot") {
		t.Fatal("the external logout left the Copilot instance listed")
	}
}

// TestCopilotRecordWatcherObservesExternalLoginWithoutUserLayer pins the
// observation rule when EVENER_PROVIDERS_CONFIG disables the user layer: there
// is no providers.toml to watch, but the Copilot record still moves under the
// hub, so an external login and logout must reload the registry and announce
// the auth change.
func TestCopilotRecordWatcherObservesExternalLoginWithoutUserLayer(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	store := newTestCredentialsStore(t)
	reg := newTestRegistry(t, stateDir, "", store, nil)
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Registry: reg, HubStateRoot: stateDir, CredsStore: store,
		ProvidersConfigPath: "", NoUserLayer: true,
	})
	defer hub.Close()
	// The record watcher is the one surface this mode keeps: even though the
	// controller is constructed, the instance CRUD/list handlers stay
	// unregistered exactly as they were when the controller did not exist.
	client := dialHubRPCInitialized(t, hub)
	for _, method := range []string{appwire.MethodEvenerInstanceList, appwire.MethodEvenerInstanceCreate} {
		err := client.Request(context.Background(), method, appwire.EmptyParams{}, nil)
		requireWireError(t, err, appwire.CodeMethodNotFound)
	}
	// Startup observations belong to the process, not to this client.
	web.notices.OnCommitted(func(noticeCommitted) {})
	web.notices.watchRead(context.Background())
	broadcaster := newRecordingBroadcaster()
	web.notices.OnCommitted(func(change noticeCommitted) { dispatchNoticeCommitted(broadcaster, change) })

	cli := newCopilotCLI(t)
	cli.login(t, stateDir, "github-copilot", "gh-1001")
	web.notices.watchRead(context.Background())
	if got := authUpdatedCount(broadcaster); got != 1 {
		t.Fatalf("external login broadcasts = %d, want one", got)
	}
	if !reg.Get().HasInstance("github-copilot") {
		t.Fatal("the external login did not add the Copilot instance")
	}
	// Still absent after the reload: observing the record must not expose the
	// handlers this mode never registered.
	err := client.Request(context.Background(), appwire.MethodEvenerInstanceList, appwire.EmptyParams{}, nil)
	requireWireError(t, err, appwire.CodeMethodNotFound)

	cli.logout(t, stateDir, "github-copilot")
	web.notices.watchRead(context.Background())
	if got := authUpdatedCount(broadcaster); got != 2 {
		t.Fatalf("external logout broadcasts = %d, want two", got)
	}
	if reg.Get().HasInstance("github-copilot") {
		t.Fatal("the external logout left the Copilot instance listed")
	}
}

func TestCopilotRecordWatcherInvalidatesOnlyTheSwitchedAccountsLiveRows(t *testing.T) {
	t.Parallel()
	f := newInstancesFixture(t, nil)
	writeProvidersToml(t, filepath.Dir(f.tomlPath), "[providers.work]\nbase = \"github-copilot\"\n")
	cli := newCopilotCLI(t)
	cli.login(t, f.stateDir, "github-copilot", "gh-1001")
	cli.login(t, f.stateDir, "work", "gh-1003")
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reload the seeded registry: %v", err)
	}
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredsStore: f.store,
	})
	defer hub.Close()
	web.notices.OnCommitted(func(noticeCommitted) {})
	web.notices.watchRead(context.Background())
	rows := []registry.Model{{ID: "gpt-5.6-sol"}}
	f.ctl.reg.Get().ApplyLiveListing("github-copilot", registry.LiveListing{Rows: rows, Authoritative: true})
	f.ctl.reg.Get().ApplyLiveListing("work", registry.LiveListing{Rows: rows, Authoritative: true})
	broadcaster := newRecordingBroadcaster()
	web.notices.OnCommitted(func(change noticeCommitted) { dispatchNoticeCommitted(broadcaster, change) })

	cli.login(t, f.stateDir, "github-copilot", "gh-2002")
	web.notices.watchRead(context.Background())
	if got := authUpdatedCount(broadcaster); got != 1 {
		t.Fatalf("account switch broadcasts = %d, want one", got)
	}
	if got := f.ctl.reg.Get().LiveModels("github-copilot"); len(got) != 0 {
		t.Fatalf("the switched account kept %d live rows", len(got))
	}
	if got := f.ctl.reg.Get().LiveModels("work"); len(got) != 1 {
		t.Fatalf("an untouched account lost its live rows: %d remain", len(got))
	}
}

func TestCopilotRecordWatcherReportsCorruptionWithoutDeletingTheRecord(t *testing.T) {
	t.Parallel()
	f := newInstancesFixture(t, nil)
	newCopilotCLI(t).login(t, f.stateDir, "github-copilot", "gh-1001")
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredsStore: f.store,
	})
	defer hub.Close()
	web.notices.OnCommitted(func(noticeCommitted) {})
	web.notices.watchRead(context.Background())
	broadcaster := newRecordingBroadcaster()
	web.notices.OnCommitted(func(change noticeCommitted) { dispatchNoticeCommitted(broadcaster, change) })

	path := githubcopilot.AuthFilePath(f.stateDir, "github-copilot")
	if err := os.WriteFile(path, []byte("{not a record"), 0o600); err != nil {
		t.Fatal(err)
	}
	web.notices.watchRead(context.Background())
	if got := authUpdatedCount(broadcaster); got != 1 {
		t.Fatalf("corruption broadcasts = %d, want one", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the corrupt record was deleted: %v", err)
	}
	inst, ok := f.ctl.reg.Get().Instance("github-copilot")
	if !ok {
		t.Fatal("the corrupt record's instance disappeared instead of reporting a sign-in state")
	}
	if inst.CredentialSource != "none" {
		t.Fatalf("credential source = %q, want the corrupt record to resolve no credential", inst.CredentialSource)
	}
}

// TestCopilotDeviceLoginDoesNotEchoItsOwnRecordWrite runs the hub's real
// device sign-in through the RPC surface: the login owns its broadcast, so the
// sweep that adopts the record on disk must stay quiet about that same write.
// Only an external login is the sweep's to announce.
func TestCopilotDeviceLoginDoesNotEchoItsOwnRecordWrite(t *testing.T) {
	f := newInstancesFixture(t, nil)
	transport := newCopilotDeviceTransport()
	transport.polls = []string{
		`{"error":"authorization_pending"}`,
		`{"access_token":"github-access-token","token_type":"bearer","scope":"read:user"}`,
	}
	var clockMu sync.Mutex
	now := time.Now()
	setNow := func(next time.Time) {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = next
	}
	previous := hubAuthControllerSetup
	hubAuthControllerSetup = func(c *hubAuthController) {
		c.client = &http.Client{Transport: transport}
		c.copilotService = githubcopilot.NewService(c.copilotCfg, c.client)
		c.now = func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		}
	}
	t.Cleanup(func() { hubAuthControllerSetup = previous })
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredsStore: f.store,
	})
	defer hub.Close()
	// Startup observations belong to the process, not to this client.
	web.notices.OnCommitted(func(noticeCommitted) {})
	web.notices.watchRead(context.Background())
	broadcaster := newRecordingBroadcaster()
	web.notices.OnCommitted(func(change noticeCommitted) { dispatchNoticeCommitted(broadcaster, change) })

	client := dialHubRPCInitialized(t, hub)
	var start appwire.AuthDeviceStartResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerAuthDeviceStart,
		appwire.AuthDeviceStartParams{Provider: "github-copilot"}, &start); err != nil {
		t.Fatalf("start the Copilot device flow: %v", err)
	}
	if start.Fallback || start.FlowID == "" {
		t.Fatalf("device start = %+v, want a device flow", start)
	}
	poll := func() appwire.AuthDevicePollResponse {
		t.Helper()
		var resp appwire.AuthDevicePollResponse
		if err := client.Request(context.Background(), appwire.MethodEvenerAuthDevicePoll,
			appwire.AuthDevicePollParams{Provider: "github-copilot", FlowID: start.FlowID}, &resp); err != nil {
			t.Fatalf("poll the Copilot device flow: %v", err)
		}
		return resp
	}
	setNow(now.Add(5 * time.Second))
	if got := poll(); got.State != "pending" {
		t.Fatalf("first poll = %+v, want pending", got)
	}
	setNow(now.Add(5 * time.Second))
	if got := poll(); got.State != "authorized" {
		t.Fatalf("authorized poll = %+v, want authorized", got)
	}
	if _, err := githubcopilot.LoadAuth(f.stateDir, "github-copilot"); err != nil {
		t.Fatalf("the hub login did not write its record: %v", err)
	}

	// The login RPC announced its own write. The sweep must adopt the record
	// as already announced rather than announce it a second time.
	web.notices.watchRead(context.Background())
	if got := authUpdatedCount(broadcaster); got != 0 {
		t.Fatalf("the sweep re-announced the hub's own login: %d broadcasts", got)
	}
}
