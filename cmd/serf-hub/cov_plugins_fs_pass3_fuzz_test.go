package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/plugins"
	"primeradiant.com/serf/internal/selfupdate"
	"primeradiant.com/serf/llm/providercfg"
)

type fuzzModelSource struct {
	*scriptedAppSource
	resp appwire.ModelListResponse
	err  error
}

func (s *fuzzModelSource) ListModels(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
	return s.resp, s.err
}

func fuzzWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fuzzMarketplace(t *testing.T, root string) string {
	t.Helper()
	src := filepath.Join(root, "source")
	fuzzWriteFile(t, filepath.Join(src, ".claude-plugin", "marketplace.json"), `{"name":"acme","description":"catalog","owner":{"name":"owner"},"plugins":[{"name":"widget","description":"desc","category":"tools","homepage":"https://invalid","author":{"name":"author"},"source":"./plugins/widget"}]}`)
	fuzzWriteFile(t, filepath.Join(src, "plugins", "widget", ".claude-plugin", "plugin.json"), `{"name":"widget","version":"1.0.0"}`)
	return src
}

func fuzzExerciseLaunch(t *testing.T, root string) {
	t.Helper()
	ctx := context.Background()
	state, cwd := filepath.Join(root, "state"), filepath.Join(root, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	c := newHubLaunchController(state)
	c.now = func() time.Time { return time.Unix(123, 0).UTC() }
	_, _ = c.Schema(ctx, appwire.EmptyParams{})
	_, _ = c.Resolve(ctx, appwire.LaunchConfigResolveParams{CWD: cwd})
	override := appwire.LaunchConfigLayer{Model: "override/model"}
	_, _ = c.Resolve(ctx, appwire.LaunchConfigResolveParams{CWD: cwd, LaunchOverrides: &override})
	_, _ = c.Resolve(ctx, appwire.LaunchConfigResolveParams{CWD: filepath.Join(root, "missing")})
	_, _ = c.GetLayer(ctx, appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: "global"})
	_, _ = c.GetLayer(ctx, appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: "project"})
	_, _ = c.GetLayer(ctx, appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: "repo"})
	_, _ = c.GetLayer(ctx, appwire.LaunchConfigGetLayerParams{CWD: filepath.Join(root, "missing"), Layer: "global"})
	_, _ = c.SetLayer(ctx, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: "global", Config: appwire.LaunchConfigLayer{Model: "openai/gpt"}})
	_, _ = c.SetLayer(ctx, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: "project", Config: appwire.LaunchConfigLayer{Env: map[string]string{"SAFE": "yes"}}})
	_, _ = c.SetLayer(ctx, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: "project", Config: appwire.LaunchConfigLayer{Env: map[string]string{"OPENAI_API_KEY": "secret"}}})
	_, _ = c.SetLayer(ctx, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: "repo"})
	_, _ = c.SetLayer(ctx, appwire.LaunchConfigSetLayerParams{CWD: filepath.Join(root, "missing"), Layer: "global"})
	_, _ = c.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: "none"})
	fuzzWriteFile(t, filepath.Join(cwd, ".serf", "launch.toml"), "model = \"openai/gpt\"\n")
	resolved, _ := c.Resolve(ctx, appwire.LaunchConfigResolveParams{CWD: cwd})
	if resolved.Repo != nil {
		_, _ = c.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: "wrong"})
		_, _ = c.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: resolved.Repo.Hash})
		_, _ = c.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: resolved.Repo.Hash})
	}
	fuzzWriteFile(t, filepath.Join(cwd, ".serf", "launch.local.toml"), "[")
	_, _ = c.GetLayer(ctx, appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: "project"})
	fuzzWriteFile(t, filepath.Join(state, "launch.toml"), "[")
	_, _ = c.Resolve(ctx, appwire.LaunchConfigResolveParams{CWD: cwd})
	blocker := filepath.Join(root, "blocker")
	fuzzWriteFile(t, blocker, "x")
	blocked := newHubLaunchController(blocker)
	_, _ = blocked.SetLayer(ctx, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: "global"})
	_, _ = blocked.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: resolved.Repo.Hash})
	_, _ = c.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: filepath.Join(root, "missing"), Hash: "x"})
}

func fuzzExerciseModels(data []byte) {
	ctx := context.Background()
	name := strings.TrimSpace(string(bytes.Map(func(r rune) rune {
		if r < 32 {
			return -1
		}
		return r
	}, data)))
	if name == "" {
		name = "model"
	}
	models := []appwire.ModelDescriptor{{Provider: " p ", Model: " " + name + " "}, {}, {Provider: "q"}}
	diags := []appwire.ModelListDiagnostic{{Provider: " p ", Source: " s ", Title: " t ", Message: " message ", Hint: " h "}, {Message: " "}}
	_ = cloneBoolMap(nil)
	_ = cloneBoolMap(map[string]bool{"x": true})
	_ = sanitizeModelDescriptors(models)
	_ = sanitizeModelDiagnostics(diags)
	_ = providerHasLaunchDiagnostic(diags, "P")
	_ = providerHasLaunchDiagnostic(nil, "P")
	_ = behaviorTagFromConfig("openrouter-anthropic", nil)
	pcfg := &providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "router", Type: providercfg.Type("openrouter-anthropic")}}}
	_ = behaviorTagFromConfig("router", pcfg)
	_ = behaviorTagFromConfig("missing", pcfg)
	_ = launchProviderAllowsUnreportedModels("openrouter-anthropic", nil)
	_ = launchProviderAllowsUnreportedModels("openai", nil)
	_ = launchHarnessDescriptors(hubcore.WebConfig{})
	_ = launchHarnessDescriptors(hubcore.WebConfig{
		CodexSources:  []appsource.CodexSourceConfig{{}, {ID: "serf"}, {ID: "remote"}},
		CodexLaunches: []codexlaunch.CodexLaunchConfig{{}, {ID: "remote"}, {ID: "other"}},
	})
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{{ID: "a", ProfileID: "p", Model: name}, {ID: "b", ProfileID: "gone", Model: "gone"}})
	_ = attachRecentModels(hubcore.WebConfig{Past: past}, appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "p", Model: name}}})
	_ = attachRecentModels(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appwire.ModelListResponse{})

	registry := appsource.NewRegistry()
	source := &fuzzModelSource{scriptedAppSource: &scriptedAppSource{id: "remote"}, resp: appwire.ModelListResponse{Data: models, Diagnostics: diags}}
	registry.Add(source)
	_, _ = hubModelList(ctx, hubcore.WebConfig{}, registry, appwire.ModelListParams{Harness: "remote"})
	source.err = errors.New("list")
	_, _ = hubModelList(ctx, hubcore.WebConfig{}, registry, appwire.ModelListParams{Harness: "remote"})
	_, _ = hubModelList(ctx, hubcore.WebConfig{}, registry, appwire.ModelListParams{Harness: "missing"})
	_, _ = hubModelList(ctx, hubcore.WebConfig{}, registry, appwire.ModelListParams{})
	local := &fuzzModelSource{scriptedAppSource: &scriptedAppSource{id: "local"}, resp: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "p", Model: name}}}}
	registry.Add(local)
	_, _ = hubModelList(ctx, hubcore.WebConfig{}, registry, appwire.ModelListParams{})
	local.err = errors.New("local")
	_, _ = hubModelList(ctx, hubcore.WebConfig{}, registry, appwire.ModelListParams{})

	sp := &fakeRPCSpawner{launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) { return models, nil }}
	cfg := hubcore.WebConfig{Spawner: sp}
	_, _ = hubModelList(ctx, cfg, registry, appwire.ModelListParams{})
	_ = validateSerfLaunchModel(ctx, cfg, mustModelRef("p/"+name), "")
	_ = validateSerfLaunchModel(ctx, cfg, mustModelRef("p/missing"), "")
	_ = validateSerfLaunchModel(ctx, cfg, mustModelRef("missing/model"), "")
	sp.launchModels = func(context.Context) ([]appwire.ModelDescriptor, error) { return nil, errors.New("models") }
	_ = validateSerfLaunchModel(ctx, cfg, mustModelRef("p/anything"), "")

	contract := &fakeRPCModelContractSpawner{contract: appwire.ModelListResponse{Data: models, Diagnostics: diags}}
	ccfg := hubcore.WebConfig{Spawner: contract}
	_, _ = serfLaunchModelList(ctx, ccfg, "")
	_ = hasSerfLaunchModelLister(ccfg)
	contract.err = errors.New("contract")
	_, _ = serfLaunchModelList(ctx, ccfg, "")
	working := &fakeRPCWorkingDirModelContractSpawner{contractForWorkingDir: func(context.Context, string) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: models, Diagnostics: diags}, nil
	}}
	wcfg := hubcore.WebConfig{Spawner: working}
	_, _ = serfLaunchModelList(ctx, wcfg, "/tmp")
	working.contractForWorkingDir = func(context.Context, string) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{}, errors.New("cwd")
	}
	_, _ = serfLaunchModelList(ctx, wcfg, "/tmp")
	_ = hasSerfLaunchModelLister(hubcore.WebConfig{})
}

func mustModelRef(raw string) (out cmdutil.ModelRef) {
	out, _ = cmdutil.ParseModelRef(raw)
	return out
}

func fuzzExercisePlugins(t *testing.T, root string) {
	t.Helper()
	ctx := context.Background()
	c := newHubPluginsController(filepath.Join(root, "plugins"))
	src := fuzzMarketplace(t, root)
	_ = marketplaceSourceToWire(marketplaceSourceFromWire(appwire.MarketplaceSourceInput{Kind: "directory", Path: src}))
	_, _ = c.ListMarketplaces()
	_, _ = c.AddMarketplace(ctx, appwire.MarketplaceAddParams{Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: src}})
	_, _ = c.AddMarketplace(ctx, appwire.MarketplaceAddParams{Name: "bad", Source: appwire.MarketplaceSourceInput{Kind: "bad"}})
	_, _ = c.Browse(ctx, appwire.MarketplaceBrowseParams{Name: "acme"})
	_, _ = c.Browse(ctx, appwire.MarketplaceBrowseParams{Name: "missing"})
	_, _ = c.RefreshMarketplace(ctx, appwire.MarketplaceNameParams{Name: "acme"})
	_, _ = c.RefreshMarketplace(ctx, appwire.MarketplaceNameParams{Name: "missing"})
	ref := appwire.PluginRefParams{Plugin: "widget", Marketplace: "acme"}
	_, _ = c.ListPlugins()
	_, _ = c.Install(ctx, ref)
	_, _ = c.Install(ctx, appwire.PluginRefParams{Plugin: "missing", Marketplace: "acme"})
	_, _ = c.Disable(ref)
	_, _ = c.Enable(ref)
	_, _ = c.SetAutoUpgrade(appwire.PluginSetAutoUpgradeParams{Plugin: "widget", Marketplace: "acme", AutoUpgrade: true})
	_, _ = c.Upgrade(ctx, ref)
	_, _ = c.Remove(ref)
	_, _ = c.Remove(ref)
	_, _ = c.RemoveMarketplace(appwire.MarketplaceNameParams{Name: "acme"})
	_, _ = c.RemoveMarketplace(appwire.MarketplaceNameParams{Name: "missing"})

	corrupt := newHubPluginsController(filepath.Join(root, "corrupt"))
	fuzzWriteFile(t, filepath.Join(root, "corrupt", "known_marketplaces.json"), "{")
	_, _ = corrupt.ListMarketplaces()
	fuzzWriteFile(t, filepath.Join(root, "corrupt", "installed_plugins.json"), "{")
	_, _ = corrupt.ListPlugins()
	badref := appwire.PluginRefParams{Plugin: "x", Marketplace: "y"}
	_, _ = corrupt.Upgrade(ctx, badref)
	_, _ = corrupt.Enable(badref)
	_, _ = corrupt.Disable(badref)
	_, _ = corrupt.SetAutoUpgrade(appwire.PluginSetAutoUpgradeParams{Plugin: "x", Marketplace: "y"})

	mgr := plugins.NewManager(filepath.Join(root, "auto"))
	_, _ = runPluginAutoUpgradeTick(ctx, mgr, &bytes.Buffer{})
	_, _ = runPluginAutoUpgradeTick(ctx, plugins.NewManager(filepath.Join(root, "forced-list")), &bytes.Buffer{})
	_, _ = runPluginAutoUpgradeTick(ctx, plugins.NewManager(filepath.Join(root, "forced-refresh")), &bytes.Buffer{})
	_, _ = runPluginAutoUpgradeTick(ctx, plugins.NewManager(filepath.Join(root, "forced-update")), &bytes.Buffer{})
	_, _ = runPluginAutoUpgradeTick(ctx, plugins.NewManager(filepath.Join(root, "forced-updated")), &bytes.Buffer{})
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "fuzz"})
	registerPluginAutoUpgradeHandlers(server, plugins.NewManager(filepath.Join(root, "forced-updated")))
	_, _ = server.Router().Dispatch(ctx, appwire.Request{ID: appwire.NewIntID(1), Method: appwire.MethodSerfPluginCheckNow})
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	startPluginAutoUpgradeDaemon(cancelCtx, mgr, time.Hour, server)
}

func fuzzExerciseUpgrade() {
	old := runHubSelfUpgrade
	defer func() { runHubSelfUpgrade = old }()
	runHubSelfUpgrade = func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{Release: "r", Channel: "c", URL: "u", Archive: "a", Prefix: "p", BinDir: "b", ShareBinDir: "s", Installed: []string{"serf"}, RestartMessage: "restart"}, nil
	}
	_, _ = hubUpgrade(context.Background(), appwire.UpgradeParams{Requested: "latest"})
	runHubSelfUpgrade = func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{}, errors.New("upgrade")
	}
	_, _ = hubUpgrade(context.Background(), appwire.UpgradeParams{})
}

func FuzzPluginsFSPass3(f *testing.F) {
	oldList, oldRefresh, oldUpdate := pluginAutoUpgradeListMarketplaces, pluginAutoUpgradeRefreshMarketplace, pluginAutoUpgradeUpdate
	pluginAutoUpgradeListMarketplaces = func(mgr *plugins.Manager) (plugins.Marketplaces, error) {
		switch filepath.Base(mgr.Root) {
		case "forced-list":
			return nil, errors.New("list")
		case "forced-refresh":
			return plugins.Marketplaces{"b": {}, "a": {}}, nil
		default:
			return oldList(mgr)
		}
	}
	pluginAutoUpgradeRefreshMarketplace = func(ctx context.Context, mgr *plugins.Manager, name string) error {
		if filepath.Base(mgr.Root) == "forced-refresh" && name == "a" {
			return errors.New("refresh")
		}
		if filepath.Base(mgr.Root) == "forced-refresh" {
			return nil
		}
		return oldRefresh(ctx, mgr, name)
	}
	pluginAutoUpgradeUpdate = func(ctx context.Context, mgr *plugins.Manager) ([]plugins.UpgradedPlugin, error) {
		switch filepath.Base(mgr.Root) {
		case "forced-update":
			return nil, errors.New("update")
		case "forced-updated":
			return []plugins.UpgradedPlugin{{Plugin: "widget", Marketplace: "acme"}}, nil
		default:
			return oldUpdate(ctx, mgr)
		}
	}
	f.Cleanup(func() {
		pluginAutoUpgradeListMarketplaces = oldList
		pluginAutoUpgradeRefreshMarketplace = oldRefresh
		pluginAutoUpgradeUpdate = oldUpdate
	})
	f.Add([]byte("model"))
	f.Fuzz(func(t *testing.T, data []byte) {
		root := t.TempDir()
		fuzzExerciseLaunch(t, root)
		fuzzExerciseModels(data)
		fuzzExercisePlugins(t, root)
		fuzzExerciseUpgrade()
	})
}
