package hub

import (
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestHubRouterMatchesCatalog keeps appwire.Methods (the source of the
// generated protocol doc) in lockstep with what evener-hub actually registers.
// The hub serves the ScopeHub + ScopeBoth methods. A registry and a
// ProvidersConfigPath are set so the evener/instance/* handlers register (they
// no-op without both).
func TestHubRouterMatchesCatalog(t *testing.T) {
	stateDir := t.TempDir()
	cfg := hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, stateDir, "", nil, nil),
		ProvidersConfigPath: filepath.Join(t.TempDir(), "providers.toml"),
		HubStateRoot:        stateDir,
	}
	web := NewWebServer(cfg)
	got := excludeHubMethods(web.appRPC.Router().Methods(), appwire.ConnectionMethodNames())
	want := appwire.CatalogMethodNames(appwire.ScopeHub)

	miss, extra := setDiff(want, got)
	if len(miss) > 0 || len(extra) > 0 {
		t.Fatalf("hub router vs appwire catalog mismatch:\n  cataloged but NOT registered: %v\n  registered but NOT cataloged: %v\nUpdate appwire/protocol.go (and run `make generate`).", miss, extra)
	}
}

func TestHubInitializeAdvertisesNavigationCapability(t *testing.T) {
	server := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	message := server.NewConnection("test").HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if message.Response == nil {
		t.Fatalf("initialize response = %#v, want success", message)
	}
	response, ok := message.Response.Result.(appwire.InitializeResponse)
	if !ok {
		t.Fatalf("initialize result = %T, want appwire.InitializeResponse", message.Response.Result)
	}
	if response.Navigation == nil {
		t.Fatal("navigation capability is absent")
	}
	if response.Navigation.Version != 1 {
		t.Fatalf("navigation version = %d, want 1", response.Navigation.Version)
	}
}

// TestHubInitializeReportsOwnMachineFacts pins that the hub's handshake names
// its own machine: its system and architecture from the running binary, and
// its project roots from hub.toml's top-level `roots` (WebConfig.MachineRoots),
// so a client showing the hub's own machine beside its SSH hosts has the same
// facts a HostRow carries.
func TestHubInitializeReportsOwnMachineFacts(t *testing.T) {
	server := newHubAppServer(hubcore.WebConfig{
		Past:         hubcore.NewPastIndex(""),
		MachineRoots: []string{"/Users/jesse/git", "/srv/work"},
	}, appsource.NewRegistry())
	message := server.NewConnection("test").HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if message.Response == nil {
		t.Fatalf("initialize response = %#v, want success", message)
	}
	response, ok := message.Response.Result.(appwire.InitializeResponse)
	if !ok {
		t.Fatalf("initialize result = %T, want appwire.InitializeResponse", message.Response.Result)
	}
	if response.ServerInfo.OS != runtime.GOOS || response.ServerInfo.Arch != runtime.GOARCH {
		t.Fatalf("ServerInfo system = %q/%q, want %q/%q", response.ServerInfo.OS, response.ServerInfo.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if got, want := response.ServerInfo.Roots, []string{"/Users/jesse/git", "/srv/work"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ServerInfo roots = %v, want %v", got, want)
	}
}

func excludeHubMethods(names, drop []string) []string {
	skip := map[string]bool{}
	for _, d := range drop {
		skip[d] = true
	}
	var out []string
	for _, n := range names {
		if !skip[n] {
			out = append(out, n)
		}
	}
	return out
}

func setDiff(want, got []string) (missing, extra []string) {
	w := map[string]bool{}
	for _, s := range want {
		w[s] = true
	}
	g := map[string]bool{}
	for _, s := range got {
		g[s] = true
	}
	for s := range w {
		if !g[s] {
			missing = append(missing, s)
		}
	}
	for s := range g {
		if !w[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}
