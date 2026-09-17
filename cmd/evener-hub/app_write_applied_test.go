package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm/registry"
)

// writeDirMarketplace plants a directory-source marketplace holding one
// plugin, which AddMarketplace can register without a network or a git binary.
func writeDirMarketplace(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"name":"` + name + `","plugins":[{"name":"demo","source":"./demo","description":"d"}]}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("marketplace.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "demo", ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir demo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo", ".claude-plugin", "plugin.json"), []byte(`{"name":"demo"}`), 0o644); err != nil {
		t.Fatalf("plugin.json: %v", err)
	}
	return dir
}

// breakPluginStoreReads makes every later read of the plugin store fail, by
// leaving its marketplaces file holding something no loader can parse. The
// files it writes are the store's own (plugins.Manager reads them back), so
// the failure this produces is a real listing failure, not a stubbed one.
func breakPluginStoreReads(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "known_marketplaces.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("breaking the marketplaces file: %v", err)
	}
}

// atPluginWriteBetween installs a hook between an applied plugin or
// marketplace write and the listing that answers it, restoring the seam when
// the test ends.
func atPluginWriteBetween(t *testing.T, hook func()) {
	t.Helper()
	original := pluginWriteBetween
	pluginWriteBetween = hook
	t.Cleanup(func() { pluginWriteBetween = original })
}

// A marketplace write that applied and whose listing then failed is still an
// applied write: the store carries the new marketplace, so every other client's
// list is stale and the hub owes them the broadcast. The error goes back to the
// caller as well — the listing really did fail — which is why the write is
// reported as applied rather than reported as an error alone (#1572).
func TestPlugins_MarketplaceAddWhoseListingFailedIsStillApplied(t *testing.T) {
	root := t.TempDir()
	ctl := newHubPluginsController(root)
	src := writeDirMarketplace(t, "demo-market")
	atPluginWriteBetween(t, func() { breakPluginStoreReads(t, root) })

	_, err := ctl.AddMarketplace(context.Background(), appwire.MarketplaceAddParams{
		Name:   "demo-market",
		Source: appwire.MarketplaceSourceInput{Kind: string(plugins.SourceDirectory), Path: src},
	})
	if err == nil {
		t.Fatal("AddMarketplace = nil, want the failed listing reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("AddMarketplace = %v (%T), want an applied write so the handler still broadcasts", err, err)
	}
}

// The plugin half of the marketplace case above: the enable reached the store,
// so the flag every client shows is stale whatever the listing did.
func TestPlugins_EnableWhoseListingFailedIsStillApplied(t *testing.T) {
	root := t.TempDir()
	ctl := newHubPluginsController(root)
	src := writeDirMarketplace(t, "demo-market")
	ctx := context.Background()
	if _, err := ctl.AddMarketplace(ctx, appwire.MarketplaceAddParams{
		Name:   "demo-market",
		Source: appwire.MarketplaceSourceInput{Kind: string(plugins.SourceDirectory), Path: src},
	}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, _, err := ctl.Install(ctx, appwire.PluginRefParams{Plugin: "demo", Marketplace: "demo-market"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	atPluginWriteBetween(t, func() { breakPluginStoreReads(t, root) })

	_, err := ctl.Disable(ctx, appwire.PluginRefParams{Plugin: "demo", Marketplace: "demo-market"})
	if err == nil {
		t.Fatal("Disable = nil, want the failed listing reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("Disable = %v (%T), want an applied write so the handler still broadcasts", err, err)
	}
}

// A write that never applied is not announced: a refusal leaves every client's
// list correct, and broadcasting it would have them all refetch for nothing.
func TestPlugins_ARefusedWriteIsNotApplied(t *testing.T) {
	ctl := newHubPluginsController(t.TempDir())

	_, err := ctl.Remove(context.Background(), appwire.PluginRefParams{Plugin: "absent", Marketplace: "nowhere"})
	if err == nil {
		t.Fatal("Remove(absent) = nil, want a refusal")
	}
	if writeDidApply(err) {
		t.Fatalf("Remove(absent) = %v (%T), want a refusal that is not announced", err, err)
	}
}

// blockProvidersWrites leaves a directory where WriteConfigFile stages its
// bytes, so every later write of providers.toml fails on a real filesystem
// refusal (the technique breakCredentialWrites uses on the credentials file).
func blockProvidersWrites(t *testing.T, tomlPath string) {
	t.Helper()
	// The reload this runs from can be attempted more than once in a call, and
	// the path only has to be occupied, not freshly created.
	if err := os.Mkdir(tomlPath+".tmp", 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		t.Errorf("blocking the providers.toml temp path: %v", err)
	}
}

// instanceRollbackFixture serves an RPC hub whose registry reloads normally
// until failReload is set, and then fails the reload after blocking the
// rollback write that follows it — the double failure #1543 is about, which
// leaves providers.toml carrying a change no rollback could undo.
type instanceRollbackFixture struct {
	hub        *httptest.Server
	tomlPath   string
	failReload *atomic.Bool
}

func newInstanceRollbackFixture(t *testing.T) *instanceRollbackFixture {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)
	failReload := &atomic.Bool{}
	load := testRegistryLoader(t.TempDir(), tomlPath, credsStore, nil)
	reg := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		if failReload.Load() {
			blockProvidersWrites(t, tomlPath)
			return nil, nil, errors.New("the registry refused to load")
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
	t.Cleanup(hub.Close)
	return &instanceRollbackFixture{hub: hub, tomlPath: tomlPath, failReload: failReload}
}

// waitForAuthUpdatedBroadcast fails the test unless evener/auth/updated
// arrives, which is what tells every other client its instance list is stale.
func waitForAuthUpdatedBroadcast(t *testing.T, client *appwire.Client, what string) {
	t.Helper()
	for {
		select {
		case got := <-client.Notifications():
			if got.Method == appwire.NotifyEvenerAuthUpdated {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for evener/auth/updated after %s", what)
		}
	}
}

// A create whose reload failed and whose rollback could not be written leaves
// the new instance in providers.toml: the config changed, so every other
// client's list is stale and the hub owes them evener/auth/updated, even
// though this caller is told the create failed (#1543).
func TestHubRPCInstanceCreateBroadcastsWhenTheRollbackCouldNotBeWritten(t *testing.T) {
	f := newInstanceRollbackFixture(t)
	client := dialHubRPC(t, f.hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	f.failReload.Store(true)

	var resp appwire.InstanceListResponse
	err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate,
		appwire.InstanceCreateParams{Base: "anthropic", Name: "mywork"}, &resp)
	if err == nil {
		t.Fatal("evener/instance/create = nil, want the failed rollback reported")
	}
	if !strings.Contains(err.Error(), "restoring the previous config failed") {
		t.Fatalf("evener/instance/create = %v, want the double failure this test is about", err)
	}
	waitForAuthUpdatedBroadcast(t, client, "a create whose rollback could not be written")
}

// The removal sibling of the create case above: the entry is gone from the
// config and the rollback could not put it back, so the removal stands and
// every other client is holding a list with an instance that no longer exists.
func TestHubRPCInstanceRemoveBroadcastsWhenTheRollbackCouldNotBeWritten(t *testing.T) {
	f := newInstanceRollbackFixture(t)
	client := dialHubRPC(t, f.hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var created appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate,
		appwire.InstanceCreateParams{Base: "anthropic", Name: "doomed"}, &created); err != nil {
		t.Fatalf("evener/instance/create: %v", err)
	}
	waitForAuthUpdatedBroadcast(t, client, "the create this removal undoes")
	f.failReload.Store(true)

	var resp appwire.InstanceListResponse
	err := client.Request(context.Background(), appwire.MethodEvenerInstanceRemove,
		appwire.InstanceRemoveParams{Name: "doomed"}, &resp)
	if err == nil {
		t.Fatal("evener/instance/remove = nil, want the failed rollback reported")
	}
	if !strings.Contains(err.Error(), "the removal stands in the config") {
		t.Fatalf("evener/instance/remove = %v, want the double failure this test is about", err)
	}
	waitForAuthUpdatedBroadcast(t, client, "a removal whose rollback could not be written")
}

// The cleanup's own failure can also fail to restore what it already
// deleted: TestInstances_RemoveRestoresTheStoredKeyWhenTheOAuthRecordCannotBeDeleted
// covers the OAuth delete failing after the stored key is already gone, with
// a clean restore of that key. This is its double-failure sibling - putting
// the key back also fails - so the key stays deleted even though [providers.work]
// never moved, and every other client's credential status for it is stale.
func TestInstances_RemoveMarksAppliedWhenTheDeletedCredentialCannotBeRestored(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	f.ctl.auth.deleteAuth = func(string, string) (bool, error) { return false, errors.New("delete refused") }
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil {
		t.Fatal("Remove = nil, want the cleanup and restore failures reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the stored key stayed deleted", err, err)
	}
}

// TestInstances_RemoveRestoresCredentialsWhenTheConfigWriteFails covers the
// config write failing with a clean credential restore. This is its
// double-failure sibling: the credentials stay deleted even though
// [providers.work] never moved, so every other client's credential status
// for it is stale.
func TestInstances_RemoveMarksAppliedWhenTheConfigWriteFailsAndTheCredentialCannotBeRestored(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	originalDelete := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(dir, name string) (bool, error) {
		if err := os.Remove(f.tomlPath); err != nil {
			t.Errorf("Remove(%s): %v", f.tomlPath, err)
		}
		if err := os.Mkdir(f.tomlPath, 0o700); err != nil {
			t.Errorf("Mkdir(%s): %v", f.tomlPath, err)
		}
		return originalDelete(dir, name)
	}
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil {
		t.Fatal("Remove = nil, want the config write failure reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the credentials stayed deleted", err, err)
	}
}

// TestInstances_RemoveRestoresTheCredentialBeforeTheRollbackReload covers the
// removal's reload failing, the config rollback landing, and the rollback's
// own reload succeeding, with a clean credential restore - the removal never
// applied, so that test wants a plain refusal. This is its double-failure
// sibling: the credential restore itself fails, so the key stays deleted
// even though [providers.work] is back, and every other client's credential
// status for it is stale.
func TestInstances_RemoveMarksAppliedWhenTheRollbackSucceedsButTheCredentialCannotBeRestored(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The layer the removal's reload is made to fail on: an entry that parses
	// but cannot resolve an endpoint (#711), the same technique
	// TestInstances_RemoveRestoresTheCredentialBeforeTheRollbackReload uses.
	brokenPath := filepath.Join(filepath.Dir(f.tomlPath), "broken.toml")
	if err := os.WriteFile(brokenPath, []byte("[providers.standalone]\nprotocol = \"openai-chat\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var loads int
	loadFn := func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		loads++
		store, err := credentials.LoadStore(f.credsPath)
		if err != nil {
			return nil, nil, err
		}
		path := f.tomlPath
		if loads == 2 {
			path = brokenPath
		}
		opts := append(
			testProbeRegistryOptions(f.stateDir, store, func(string) (string, bool) { return "", false }),
			registry.WithConfigPath(path),
		)
		r, err := registry.Load(append(opts, extra...)...)
		return r, store, err
	}
	replacement := hubcore.NewProviderRegistry(loadFn)
	f.ctl.reg = replacement
	f.ctl.auth.reg = replacement
	if err := replacement.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil {
		t.Fatal("Remove = nil, want the reload failure reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the stored key stayed deleted despite the config rollback", err, err)
	}
}

// seedUnfetchedMarketplace plants known_marketplaces.json directly under
// pluginRoot, naming a directory-source marketplace whose InstallLocation is
// still empty - the state a seeded pointer is in before its first
// Browse/Install/Upgrade. AddMarketplace always fetches immediately, so a
// seeded-but-unfetched pointer can only be produced by writing the store's
// own file, the technique TestPlugins_Marketplace_BrowseRefusalsAreWireErrors
// already uses for a different case.
func seedUnfetchedMarketplace(t *testing.T, pluginRoot, name, dir string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{name: map[string]any{
		"source": map[string]any{"source": "directory", "path": dir},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "known_marketplaces.json"), body, 0o644); err != nil {
		t.Fatalf("seeding known_marketplaces.json: %v", err)
	}
}

// countBroadcasts drains every notification a client receives until quiet
// passes with none arriving, so a test can assert exactly how many times a
// method fired instead of racing a single select against the first one.
func countBroadcasts(client *appwire.Client, quiet time.Duration) map[string]int {
	counts := map[string]int{}
	for {
		select {
		case got := <-client.Notifications():
			counts[got.Method]++
		case <-time.After(quiet):
			return counts
		}
	}
}

// Install and Browse's first access to a seeded, unfetched marketplace
// persists a backfill (InstallLocation/LastUpdated) whether or not the call
// itself fails - earlier rounds covered the failure paths (marketplaceChanged
// wrapped in an error), but a SUCCESSFUL Install or Browse must broadcast
// evener/marketplace/updated too, exactly once, or every other client keeps
// an empty install location until something unrelated happens to broadcast.
// Closes #1672.
func TestHubRPCInstallBroadcastsMarketplaceUpdatedOnASuccessfulLazyFetch(t *testing.T) {
	pluginRoot := t.TempDir()
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	seedUnfetchedMarketplace(t, pluginRoot, "acme", dir)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{PluginRoot: pluginRoot})
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var resp appwire.PluginListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerPluginInstall,
		appwire.PluginRefParams{Plugin: "widget", Marketplace: "acme"}, &resp); err != nil {
		t.Fatalf("evener/plugin/install: %v", err)
	}

	counts := countBroadcasts(client, 200*time.Millisecond)
	if counts[appwire.NotifyEvenerMarketplaceUpdated] != 1 {
		t.Fatalf("evener/marketplace/updated fired %d times, want exactly 1 (all broadcasts: %v)", counts[appwire.NotifyEvenerMarketplaceUpdated], counts)
	}
}

func TestHubRPCBrowseBroadcastsMarketplaceUpdatedOnASuccessfulLazyFetch(t *testing.T) {
	pluginRoot := t.TempDir()
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	seedUnfetchedMarketplace(t, pluginRoot, "acme", dir)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{PluginRoot: pluginRoot})
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var resp appwire.MarketplaceBrowseResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerMarketplaceBrowse,
		appwire.MarketplaceBrowseParams{Name: "acme"}, &resp); err != nil {
		t.Fatalf("evener/marketplace/browse: %v", err)
	}

	counts := countBroadcasts(client, 200*time.Millisecond)
	if counts[appwire.NotifyEvenerMarketplaceUpdated] != 1 {
		t.Fatalf("evener/marketplace/updated fired %d times, want exactly 1 (all broadcasts: %v)", counts[appwire.NotifyEvenerMarketplaceUpdated], counts)
	}
}

// seedLegacyNamedMarketplace plants known_marketplaces.json directly under
// pluginRoot, naming a marketplace under a traversing name validNameComponent
// refuses today (the shape an older evener or a hand edit could leave): the
// store lock's own migration (migrateMarketplaceNames, run from lockStore on
// every acquisition - reads included) renames it the next time anything
// takes the lock, the same technique
// TestPlugins_Marketplace_BrowseRefusalsAreWireErrors already uses.
func seedLegacyNamedMarketplace(t *testing.T, pluginRoot string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"../../escape": map[string]any{
		"source": map[string]any{"source": "url", "url": filepath.Join(t.TempDir(), "absent.git")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "known_marketplaces.json"), body, 0o644); err != nil {
		t.Fatalf("seeding known_marketplaces.json: %v", err)
	}
}

// A plain evener/marketplace/list call can trigger lockStore's legacy-name
// migration (every acquisition runs it, reads included) and persist a rename
// - a real change to the marketplace store, the same class of thing round 6
// covers for a lazy fetch. Every other client's marketplace listing is stale
// the moment that rename lands, whether or not the call that triggered it
// was itself a write.
func TestHubRPCMarketplaceListBroadcastsWhenLockStoreMigratesALegacyName(t *testing.T) {
	pluginRoot := t.TempDir()
	seedLegacyNamedMarketplace(t, pluginRoot)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{PluginRoot: pluginRoot})
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.MarketplaceList(context.Background())
	if err != nil {
		t.Fatalf("evener/marketplace/list: %v", err)
	}
	if len(resp.Marketplaces) != 1 || resp.Marketplaces[0].Name != "escape" {
		t.Fatalf("marketplaces = %+v, want the legacy name migrated to escape (confirms the migration actually ran)", resp.Marketplaces)
	}

	counts := countBroadcasts(client, 200*time.Millisecond)
	if counts[appwire.NotifyEvenerMarketplaceUpdated] != 1 {
		t.Fatalf("evener/marketplace/updated fired %d times, want exactly 1 (all broadcasts: %v)", counts[appwire.NotifyEvenerMarketplaceUpdated], counts)
	}
}

// The plugin-list sibling of the marketplace-list case above: evener/plugin/list
// also runs behind the migration barrier (List -> loadMigratedMarketplaces),
// and a migration re-keys the registry as well as the marketplaces file, so a
// plain plugin list can persist a rename too. Both listings are stale the
// moment it lands.
func TestHubRPCPluginListBroadcastsWhenLockStoreMigratesALegacyName(t *testing.T) {
	pluginRoot := t.TempDir()
	seedLegacyNamedMarketplace(t, pluginRoot)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{PluginRoot: pluginRoot})
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if _, err := client.PluginList(context.Background()); err != nil {
		t.Fatalf("evener/plugin/list: %v", err)
	}

	counts := countBroadcasts(client, 200*time.Millisecond)
	if counts[appwire.NotifyEvenerMarketplaceUpdated] != 1 {
		t.Fatalf("evener/marketplace/updated fired %d times, want exactly 1 (all broadcasts: %v)", counts[appwire.NotifyEvenerMarketplaceUpdated], counts)
	}
	if counts[appwire.NotifyEvenerPluginUpdated] != 1 {
		t.Fatalf("evener/plugin/updated fired %d times, want exactly 1 (all broadcasts: %v)", counts[appwire.NotifyEvenerPluginUpdated], counts)
	}
}

// Browse's own migration case: a Browse for the legacy name itself is an
// InvalidParams refusal (the name it asked for is unknown by the time it
// looks it up - the lock's migration renamed it first), but the migration it
// triggered along the way still persisted, so both stores' broadcasts are
// owed even though this particular call failed.
func TestHubRPCBrowseBroadcastsWhenLockStoreMigratesALegacyName(t *testing.T) {
	pluginRoot := t.TempDir()
	seedLegacyNamedMarketplace(t, pluginRoot)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{PluginRoot: pluginRoot})
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	_, err := client.MarketplaceBrowse(context.Background(), appwire.MarketplaceBrowseParams{Name: "../../escape"})
	if err == nil {
		t.Fatal("evener/marketplace/browse = nil, want the now-renamed name reported unknown")
	}

	counts := countBroadcasts(client, 200*time.Millisecond)
	if counts[appwire.NotifyEvenerMarketplaceUpdated] != 1 {
		t.Fatalf("evener/marketplace/updated fired %d times, want exactly 1 (all broadcasts: %v)", counts[appwire.NotifyEvenerMarketplaceUpdated], counts)
	}
	if counts[appwire.NotifyEvenerPluginUpdated] != 1 {
		t.Fatalf("evener/plugin/updated fired %d times, want exactly 1 (all broadcasts: %v)", counts[appwire.NotifyEvenerPluginUpdated], counts)
	}
}
