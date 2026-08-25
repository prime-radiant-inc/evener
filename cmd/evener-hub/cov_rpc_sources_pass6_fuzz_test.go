//go:build evenerfuzz

package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/codexlaunch"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
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
			Evener: appwire.EvenerThread{Ref: "remote:thread", Capabilities: appwire.ThreadCapabilities{Send: true}},
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
				{appwire.MethodEvenerAuthStatus, appwire.AuthStatusParams{}},
				{appwire.MethodEvenerAuthList, appwire.EmptyParams{}},
				{appwire.MethodEvenerAuthLogout, appwire.AuthLogoutParams{}},
				{appwire.MethodEvenerAuthApiKeySet, appwire.AuthApiKeySetParams{}},
				{appwire.MethodEvenerInstanceList, appwire.EmptyParams{}},
				{appwire.MethodEvenerInstanceCreate, appwire.InstanceCreateParams{}},
				{appwire.MethodEvenerInstanceEdit, appwire.InstanceEditParams{}},
				{appwire.MethodEvenerInstanceRemove, appwire.InstanceRemoveParams{}},
				{appwire.MethodEvenerInstanceSetDefault, appwire.InstanceSetDefaultParams{}},
				{appwire.MethodEvenerMarketplaceList, appwire.EmptyParams{}},
				{appwire.MethodEvenerMarketplaceAdd, appwire.MarketplaceAddParams{}},
				{appwire.MethodEvenerMarketplaceRemove, appwire.MarketplaceNameParams{}},
				{appwire.MethodEvenerMarketplaceRefresh, appwire.MarketplaceNameParams{}},
				{appwire.MethodEvenerMarketplaceBrowse, appwire.MarketplaceBrowseParams{}},
				{appwire.MethodEvenerPluginList, appwire.EmptyParams{}},
				{appwire.MethodEvenerPluginInstall, appwire.PluginRefParams{}},
				{appwire.MethodEvenerPluginUpgrade, appwire.PluginRefParams{}},
				{appwire.MethodEvenerPluginRemove, appwire.PluginRefParams{}},
				{appwire.MethodEvenerPluginEnable, appwire.PluginRefParams{}},
				{appwire.MethodEvenerPluginDisable, appwire.PluginRefParams{}},
				{appwire.MethodEvenerPluginSetAutoUpgrade, appwire.PluginSetAutoUpgradeParams{}},
				{appwire.MethodEvenerTasksList, appwire.TaskListParams{Ref: "remote:thread"}},
				{appwire.MethodEvenerHarnessesList, appwire.HarnessListParams{}},
				{appwire.MethodEvenerCommandList, appwire.EmptyParams{}},
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
			dispatchRPCPass6(t, server, appwire.MethodEvenerCommandList, appwire.EmptyParams{})
			dispatchRPCPass6(t, server, appwire.MethodEvenerLaunchResolve, appwire.LaunchConfigResolveParams{})
			dispatchRPCPass6(t, server, appwire.MethodEvenerLaunchSchema, appwire.EmptyParams{})
			dispatchRPCPass6(t, server, appwire.MethodEvenerLaunchGetLayer, appwire.LaunchConfigGetLayerParams{})
			dispatchRPCPass6(t, server, appwire.MethodEvenerLaunchSetLayer, appwire.LaunchConfigSetLayerParams{})
			dispatchRPCPass6(t, server, appwire.MethodEvenerMarketplaceAdd, appwire.MarketplaceAddParams{
				Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: marketplace},
			})
			dispatchRPCPass6(t, server, appwire.MethodEvenerMarketplaceRefresh, appwire.MarketplaceNameParams{Name: "acme"})
			dispatchRPCPass6(t, server, appwire.MethodEvenerMarketplaceBrowse, appwire.MarketplaceBrowseParams{Name: "acme"})
			ref := appwire.PluginRefParams{Plugin: "widget", Marketplace: "acme"}
			dispatchRPCPass6(t, server, appwire.MethodEvenerPluginInstall, ref)
			dispatchRPCPass6(t, server, appwire.MethodEvenerPluginUpgrade, ref)
			dispatchRPCPass6(t, server, appwire.MethodEvenerPluginDisable, ref)
			dispatchRPCPass6(t, server, appwire.MethodEvenerPluginEnable, ref)
			dispatchRPCPass6(t, server, appwire.MethodEvenerPluginSetAutoUpgrade, appwire.PluginSetAutoUpgradeParams{
				Plugin: "widget", Marketplace: "acme", AutoUpgrade: true,
			})
			dispatchRPCPass6(t, server, appwire.MethodEvenerPluginRemove, ref)
			dispatchRPCPass6(t, server, appwire.MethodEvenerMarketplaceRemove, appwire.MarketplaceNameParams{Name: "acme"})
			_, _ = hubCommandList(hubcore.WebConfig{PluginRoot: cfg.PluginRoot})
		}
	})
}
