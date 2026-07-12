package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/plugins"
	"primeradiant.com/serf/internal/selfupdate"
)

type covPluginTicker struct {
	c       chan time.Time
	stopped bool
	calls   int
	cancel  context.CancelFunc
}

func (t *covPluginTicker) Chan() <-chan time.Time {
	t.calls++
	if t.calls == 2 && t.cancel != nil {
		t.cancel()
	}
	return t.c
}
func (t *covPluginTicker) Stop() { t.stopped = true }

func FuzzLaunchModelsPluginsBoundaries(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		ctx := context.Background()

		ctl := newHubLaunchController(t.TempDir())
		badCWD := filepath.Join(t.TempDir(), "missing")
		_, _ = ctl.Resolve(ctx, appwire.LaunchConfigResolveParams{CWD: badCWD})
		_, _ = ctl.GetLayer(ctx, appwire.LaunchConfigGetLayerParams{CWD: badCWD})
		_, _ = ctl.SetLayer(ctx, appwire.LaunchConfigSetLayerParams{CWD: badCWD})
		_, _ = ctl.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: badCWD})
		if cloneBoolMap(nil) != nil {
			t.Fatal("nil map clone must remain nil")
		}
		cwd := t.TempDir()
		override := appwire.LaunchConfigLayer{Model: "p/m"}
		_, _ = ctl.Resolve(ctx, appwire.LaunchConfigResolveParams{CWD: cwd, LaunchOverrides: &override})
		_, _ = ctl.GetLayer(ctx, appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: "session"})
		_, _ = ctl.SetLayer(ctx, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: "session"})
		_, _ = ctl.SetLayer(ctx, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: "global", Config: appwire.LaunchConfigLayer{Env: map[string]string{"OPENAI_API_KEY": "secret"}}})
		_, _ = ctl.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: cwd})

		badRoot := filepath.Join(t.TempDir(), "root-file")
		if err := os.WriteFile(badRoot, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		broken := newHubLaunchController(badRoot)
		_, _ = broken.Resolve(ctx, appwire.LaunchConfigResolveParams{CWD: cwd})
		_, _ = broken.GetLayer(ctx, appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: "global"})
		_, _ = broken.SetLayer(ctx, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: "global"})

		corruptProject := t.TempDir()
		if err := os.MkdirAll(filepath.Join(corruptProject, ".serf"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(corruptProject, ".serf", "launch.local.toml"), []byte("="), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = ctl.GetLayer(ctx, appwire.LaunchConfigGetLayerParams{CWD: corruptProject, Layer: "project"})

		trustRoot, trustCWD := t.TempDir(), t.TempDir()
		if err := os.MkdirAll(filepath.Join(trustCWD, ".serf"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(trustCWD, ".serf", "launch.toml"), []byte("model = \"p/m\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		trustCtl := newHubLaunchController(trustRoot)
		resolved, err := trustCtl.Resolve(ctx, appwire.LaunchConfigResolveParams{CWD: trustCWD})
		if err != nil || resolved.Repo == nil {
			t.Fatalf("resolve repo: %+v %v", resolved, err)
		}
		paths := launchconfig.PathsFor(trustRoot, trustCWD)
		if err := os.MkdirAll(filepath.Dir(paths.Meta), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(paths.Meta, 0o755); err != nil {
			t.Fatal(err)
		}
		_, _ = trustCtl.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: trustCWD, Hash: resolved.Repo.Hash})
		oldResolve, oldSaveMeta := hubLaunchResolve, hubLaunchSaveMeta
		t.Cleanup(func() { hubLaunchResolve, hubLaunchSaveMeta = oldResolve, oldSaveMeta })
		hubLaunchResolve = func(string, string, launchconfig.Layer) (launchconfig.Resolved, error) {
			return launchconfig.Resolved{}, errors.New("resolve")
		}
		_, _ = trustCtl.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: trustCWD})
		hubLaunchResolve = oldResolve
		hubLaunchSaveMeta = func(string, launchconfig.Meta) error { return errors.New("save meta") }
		_, _ = trustCtl.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: trustCWD, Hash: resolved.Repo.Hash})
		hubLaunchSaveMeta = oldSaveMeta
		if err := os.RemoveAll(paths.Meta); err != nil {
			t.Fatal(err)
		}
		if err := launchconfig.SaveMeta(paths.Meta, launchconfig.Meta{Schema: 1, Trust: launchconfig.MetaTrust{Decision: "trusted", Hashes: []string{resolved.Repo.Hash}}}); err != nil {
			t.Fatal(err)
		}
		_, _ = trustCtl.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: trustCWD, Hash: resolved.Repo.Hash})
		resolveCalls := 0
		hubLaunchResolve = func(root, dir string, layer launchconfig.Layer) (launchconfig.Resolved, error) {
			resolveCalls++
			if resolveCalls == 2 {
				return launchconfig.Resolved{}, errors.New("resolve after save")
			}
			return oldResolve(root, dir, layer)
		}
		_, _ = trustCtl.TrustRepo(ctx, appwire.LaunchConfigTrustRepoParams{CWD: trustCWD, Hash: resolved.Repo.Hash})
		hubLaunchResolve = func(string, string, launchconfig.Layer) (launchconfig.Resolved, error) {
			return launchconfig.Resolved{}, errors.New("resolve after layer save")
		}
		_, _ = ctl.SetLayer(ctx, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: "global"})
		hubLaunchResolve = oldResolve

		_ = sanitizeModelDescriptors([]appwire.ModelDescriptor{{Provider: " ", Model: "x"}, {Provider: "p", Model: " "}, {Provider: " p ", Model: " m "}})
		_ = sanitizeModelDiagnostics([]appwire.ModelListDiagnostic{{Message: " "}, {Provider: " p ", Source: " s ", Title: " t ", Message: " m ", Hint: " h "}})
		_ = launchHarnessDescriptors(hubcore.WebConfig{CodexSources: []appsource.CodexSourceConfig{{}, {ID: "serf"}, {ID: "extra"}}, CodexLaunches: []codexlaunch.CodexLaunchConfig{{}, {ID: "extra"}, {ID: "launched"}}})
		_, _ = sourceForModelHarness(ctx, hubcore.WebConfig{}, appsource.NewRegistry(), "missing")
		launcher := codexlaunch.NewCodexLauncher([]codexlaunch.CodexLaunchConfig{{ID: "managed", Binary: filepath.Join(t.TempDir(), "missing")}})
		_, _ = sourceForModelHarness(ctx, hubcore.WebConfig{CodexLauncher: launcher}, appsource.NewRegistry(), "managed")
		errorSources := appsource.NewRegistry()
		errorSources.Add(&scriptedAppSource{id: "remote"})
		_, _ = hubModelListInner(ctx, hubcore.WebConfig{}, errorSources, appwire.ModelListParams{Harness: "remote"})
		_, _ = hubModelListInner(ctx, hubcore.WebConfig{Spawner: &fakeRPCModelContractSpawner{err: errors.New("contract")}}, appsource.NewRegistry(), appwire.ModelListParams{})
		_, _ = serfLaunchModelList(ctx, hubcore.WebConfig{Spawner: &fakeRPCWorkingDirModelContractSpawner{contractForWorkingDir: func(context.Context, string) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{}, errors.New("working-dir")
		}}}, cwd)
		_, _ = serfLaunchModelList(ctx, hubcore.WebConfig{Spawner: &fakeRPCModelContractSpawner{err: errors.New("contract")}}, "")
		_, _ = serfLaunchModelList(ctx, hubcore.WebConfig{Spawner: &fakeRPCSpawner{launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) { return nil, errors.New("models") }}}, "")

		pluginRoot := filepath.Join(t.TempDir(), "plugin-root-file")
		if err := os.WriteFile(pluginRoot, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		pctl := newHubPluginsController(pluginRoot)
		_, _ = pctl.ListMarketplaces()
		_, _ = pctl.ListPlugins()
		ref := appwire.PluginRefParams{Plugin: "missing", Marketplace: "missing"}
		_, _ = pctl.Upgrade(ctx, ref)
		_, _ = pctl.Enable(ref)
		_, _ = pctl.Disable(ref)

		mgr := plugins.NewManager(pluginRoot)
		_, errs := runPluginAutoUpgradeTick(ctx, mgr, os.Stderr)
		if len(errs) == 0 {
			t.Fatal("broken manager must report an error")
		}
		refreshCtl := newHubPluginsController(t.TempDir())
		marketDir := t.TempDir()
		writeTestMarketplace(t, marketDir)
		addTestMarketplace(t, refreshCtl, marketDir)
		if err := os.RemoveAll(marketDir); err != nil {
			t.Fatal(err)
		}
		_, _ = runPluginAutoUpgradeTick(ctx, refreshCtl.mgr, os.Stderr)
		if err := os.WriteFile(filepath.Join(refreshCtl.mgr.Root, "installed_plugins.json"), []byte("{"), 0o600); err == nil {
			_, _ = runPluginAutoUpgradeTick(ctx, refreshCtl.mgr, os.Stderr)
		}
		oldList, oldRefresh, oldUpdate := pluginListMarketplaces, pluginRefreshMarketplace, pluginUpdateAutoUpgrade
		t.Cleanup(func() {
			pluginListMarketplaces, pluginRefreshMarketplace, pluginUpdateAutoUpgrade = oldList, oldRefresh, oldUpdate
		})
		pluginListMarketplaces = func(*plugins.Manager) (map[string]plugins.MarketplaceRef, error) {
			return map[string]plugins.MarketplaceRef{"broken": {}}, nil
		}
		pluginRefreshMarketplace = func(context.Context, *plugins.Manager, string) error { return errors.New("refresh") }
		pluginUpdateAutoUpgrade = func(context.Context, *plugins.Manager) ([]plugins.UpgradedPlugin, error) {
			return nil, errors.New("update")
		}
		_, _ = runPluginAutoUpgradeTick(ctx, plugins.NewManager(t.TempDir()), os.Stderr)
		pluginListMarketplaces, pluginRefreshMarketplace, pluginUpdateAutoUpgrade = oldList, oldRefresh, oldUpdate

		ticker := &covPluginTicker{c: make(chan time.Time, 1)}
		ticker.c <- time.Time{}
		oldTicker := newPluginAutoUpgradeTicker
		newPluginAutoUpgradeTicker = func(time.Duration) pluginAutoUpgradeTicker { return ticker }
		t.Cleanup(func() { newPluginAutoUpgradeTicker = oldTicker })
		daemonCtx, cancel := context.WithCancel(ctx)
		ticker.cancel = cancel
		startPluginAutoUpgradeDaemon(daemonCtx, plugins.NewManager(t.TempDir()), time.Hour, nil)
		if !ticker.stopped {
			t.Fatal("daemon did not stop ticker")
		}
		realTicker := oldTicker(time.Hour)
		_ = realTicker.Chan()
		realTicker.Stop()
		oldTick := pluginAutoUpgradeTick
		pluginAutoUpgradeTick = func(context.Context, *plugins.Manager, io.Writer) ([]plugins.UpgradedPlugin, []string) {
			return []plugins.UpgradedPlugin{{Plugin: "p", Marketplace: "m"}}, nil
		}
		t.Cleanup(func() { pluginAutoUpgradeTick = oldTick })
		notifyCtx, notifyCancel := context.WithCancel(ctx)
		notifyCancel()
		startPluginAutoUpgradeDaemon(notifyCtx, plugins.NewManager(t.TempDir()), time.Hour, appserver.NewServer(appserver.ServerConfig{ServerName: "test"}))

		oldUpgrade := runHubSelfUpgrade
		runHubSelfUpgrade = func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
			return selfupdate.Result{}, errors.New("upgrade")
		}
		_, _ = hubUpgrade(ctx, appwire.UpgradeParams{})
		runHubSelfUpgrade = oldUpgrade
	})
}
