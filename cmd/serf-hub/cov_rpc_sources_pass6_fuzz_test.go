//go:build serffuzz

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appserver"
)

type noReadRelaySource struct{ *scriptedAppSource }

func (*noReadRelaySource) RelayOnThreadRead() bool { return false }

func dispatchRPCPass6(t *testing.T, server *appserver.Server, method string, params any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = server.Router().Dispatch(context.Background(), appwire.Request{
		ID: appwire.NewIntID(1), Method: method, Params: raw,
	})
}

func writeRPCPass6Plugin(t *testing.T, root, name, command string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte(`{"name":"`+name+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "---\ndescription: pass six\nargument-hint: '[value]'\n---\nbody"
	if err := os.WriteFile(filepath.Join(root, "commands", command+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// FuzzRPCSourcesPass6 covers hub construction, source selection, ancillary RPC
// registration, and plugin command discovery using only temporary files and
// scripted external boundaries.
func FuzzRPCSourcesPass6(f *testing.F) {
	for seed := uint8(0); seed < 3; seed++ {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint8) {
		root := t.TempDir()
		t.Setenv("HOME", root)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

		thread := appwire.Thread{
			ID: "thread", SessionID: "session", Source: "remote",
			Serf: appwire.SerfThread{Ref: "remote:thread", Capabilities: appwire.ThreadCapabilities{Send: true}},
		}
		remote := &scriptedAppSource{id: "remote", thread: thread}
		registry := appsource.NewRegistry()
		registry.Add(remote)

		launcher := codexlaunch.NewCodexLauncher([]codexlaunch.CodexLaunchConfig{{ID: "managed"}})
		launcher.Sources["managed"] = remote
		cfg := hubcore.WebConfig{
			HubStateRoot: root, RunDir: filepath.Join(root, "run"),
			PluginRoot:          filepath.Join(root, "plugins"),
			ProvidersConfigPath: filepath.Join(root, "providers.toml"),
			CodexLauncher:       launcher,
		}

		switch seed % 3 {
		case 0:
			configured := newHubSourceRegistry(hubcore.WebConfig{
				RunDir:       cfg.RunDir,
				CodexSources: []appsource.CodexSourceConfig{{ID: "codex", Endpoint: "http://127.0.0.1:1"}},
			})
			if local, ok := configured.Source("local"); ok {
				_, _ = local.ListThreads(context.Background(), appwire.ThreadListParams{})
			}
			_, _ = sourceForThread(configured, "", "thread")
			_, _ = sourceForThread(appsource.NewRegistry(), "", "")
			_, _ = sourceForThread(registry, "remote:thread", "")
			_, _ = sourceForThread(registry, "bad ref", "")
			_, _ = sourceForThreadWithManagedLaunch(context.Background(), cfg, registry, "managed:thread", "thread")
			_, _ = managedLaunchSourceIDForRef(cfg, "managed:thread")
			_, _ = managedLaunchSourceIDForRef(cfg, "local:thread")
			_, _ = managedLaunchSourceIDForRef(cfg, "bad ref")
			_ = hubKnowsRef(cfg, "managed:thread")
			_ = relayOnThreadRead(&noReadRelaySource{remote})

		case 1:
			server := newHubAppServer(cfg, registry)
			for _, call := range []struct {
				method string
				params any
			}{
				{appwire.MethodSerfAuthStatus, appwire.AuthStatusParams{}},
				{appwire.MethodSerfAuthList, appwire.EmptyParams{}},
				{appwire.MethodSerfAuthLogout, appwire.AuthLogoutParams{}},
				{appwire.MethodSerfAuthApiKeySet, appwire.AuthApiKeySetParams{}},
				{appwire.MethodSerfInstanceList, appwire.EmptyParams{}},
				{appwire.MethodSerfInstanceCreate, appwire.InstanceCreateParams{}},
				{appwire.MethodSerfInstanceEdit, appwire.InstanceEditParams{}},
				{appwire.MethodSerfInstanceRemove, appwire.InstanceRemoveParams{}},
				{appwire.MethodSerfInstanceSetDefault, appwire.InstanceSetDefaultParams{}},
				{appwire.MethodSerfMarketplaceList, appwire.EmptyParams{}},
				{appwire.MethodSerfMarketplaceAdd, appwire.MarketplaceAddParams{}},
				{appwire.MethodSerfMarketplaceRemove, appwire.MarketplaceNameParams{}},
				{appwire.MethodSerfMarketplaceRefresh, appwire.MarketplaceNameParams{}},
				{appwire.MethodSerfMarketplaceBrowse, appwire.MarketplaceBrowseParams{}},
				{appwire.MethodSerfPluginList, appwire.EmptyParams{}},
				{appwire.MethodSerfPluginInstall, appwire.PluginRefParams{}},
				{appwire.MethodSerfPluginUpgrade, appwire.PluginRefParams{}},
				{appwire.MethodSerfPluginRemove, appwire.PluginRefParams{}},
				{appwire.MethodSerfPluginEnable, appwire.PluginRefParams{}},
				{appwire.MethodSerfPluginDisable, appwire.PluginRefParams{}},
				{appwire.MethodSerfPluginSetAutoUpgrade, appwire.PluginSetAutoUpgradeParams{}},
				{appwire.MethodSerfTasksList, appwire.TaskListParams{Ref: "remote:thread"}},
				{appwire.MethodSerfHarnessesList, appwire.HarnessListParams{}},
				{appwire.MethodSerfCommandList, appwire.EmptyParams{}},
			} {
				dispatchRPCPass6(t, server, call.method, call.params)
			}
			notifyMarketplaceUpdated(server)
			notifyPluginUpdated(server)
			notifyAuthUpdated(server, "provider", "source")
			notifyLaunchUpdated(server, root, "user")

		case 2:
			a := filepath.Join(root, "plugin-a")
			b := filepath.Join(root, "plugin-b")
			writeRPCPass6Plugin(t, a, "zeta", "same")
			writeRPCPass6Plugin(t, b, "alpha", "same")
			marketplace := filepath.Join(root, "marketplace")
			writeTestMarketplace(t, marketplace)
			server := newHubAppServer(hubcore.WebConfig{
				HubStateRoot: root, PluginRoot: cfg.PluginRoot, PluginDirs: []string{a, b},
			}, registry)
			dispatchRPCPass6(t, server, appwire.MethodSerfCommandList, appwire.EmptyParams{})
			dispatchRPCPass6(t, server, appwire.MethodSerfLaunchResolve, appwire.LaunchConfigResolveParams{})
			dispatchRPCPass6(t, server, appwire.MethodSerfLaunchSchema, appwire.EmptyParams{})
			dispatchRPCPass6(t, server, appwire.MethodSerfLaunchGetLayer, appwire.LaunchConfigGetLayerParams{})
			dispatchRPCPass6(t, server, appwire.MethodSerfLaunchSetLayer, appwire.LaunchConfigSetLayerParams{})
			dispatchRPCPass6(t, server, appwire.MethodSerfMarketplaceAdd, appwire.MarketplaceAddParams{
				Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: marketplace},
			})
			dispatchRPCPass6(t, server, appwire.MethodSerfMarketplaceRefresh, appwire.MarketplaceNameParams{Name: "acme"})
			dispatchRPCPass6(t, server, appwire.MethodSerfMarketplaceBrowse, appwire.MarketplaceBrowseParams{Name: "acme"})
			ref := appwire.PluginRefParams{Plugin: "widget", Marketplace: "acme"}
			dispatchRPCPass6(t, server, appwire.MethodSerfPluginInstall, ref)
			dispatchRPCPass6(t, server, appwire.MethodSerfPluginUpgrade, ref)
			dispatchRPCPass6(t, server, appwire.MethodSerfPluginDisable, ref)
			dispatchRPCPass6(t, server, appwire.MethodSerfPluginEnable, ref)
			dispatchRPCPass6(t, server, appwire.MethodSerfPluginSetAutoUpgrade, appwire.PluginSetAutoUpgradeParams{
				Plugin: "widget", Marketplace: "acme", AutoUpgrade: true,
			})
			dispatchRPCPass6(t, server, appwire.MethodSerfPluginRemove, ref)
			dispatchRPCPass6(t, server, appwire.MethodSerfMarketplaceRemove, appwire.MarketplaceNameParams{Name: "acme"})
			_, _ = hubCommandList(hubcore.WebConfig{PluginRoot: cfg.PluginRoot})
		}
	})
}
