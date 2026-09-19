package hub

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func unusedRemoteHostClient(context.Context, string) (*appwire.Client, error) {
	return nil, errors.New("remote host client is not used in this test")
}

func TestNewHubSourceRegistryRegistersRemoteHosts(t *testing.T) {
	registry := newHubSourceRegistry(hubcore.WebConfig{
		Roster: hubcore.NewRosterWithEntries(),
		RemoteHosts: []hostreg.Host{
			{Name: "h1", SSH: "h1.example"},
			{Name: "h2", SSH: "h2.example"},
		},
		RemoteHostClient: unusedRemoteHostClient,
	})
	for _, id := range []string{"local", "h1", "h2"} {
		if _, ok := registry.Source(id); !ok {
			t.Errorf("source %q not registered", id)
		}
	}
}

func TestNewHubSourceRegistryWithoutHostsOnlyLocal(t *testing.T) {
	registry := newHubSourceRegistry(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries()})
	if _, ok := registry.Source("local"); !ok {
		t.Fatal("local source not registered")
	}
	if _, ok := registry.Source("h1"); ok {
		t.Fatal("h1 registered without configured hosts")
	}
}

// A registry built without a roster has no local source at all: the hub always
// wires one (main.go), and a lookup of a local ref must fail as "source not
// found" rather than find a source that lists nothing. Remote hosts do not
// depend on the local roster, so a configured one is still registered.
func TestNewHubSourceRegistryWithoutARosterRegistersOnlyRemoteHosts(t *testing.T) {
	registry := newHubSourceRegistry(hubcore.WebConfig{
		RemoteHosts:      []hostreg.Host{{Name: "h1", SSH: "h1.example"}},
		RemoteHostClient: unusedRemoteHostClient,
	})
	if _, ok := registry.Source("local"); ok {
		t.Fatal("local source registered without a roster")
	}
	if _, ok := registry.Source("h1"); !ok {
		t.Fatal("h1 not registered although it is configured")
	}
}

func TestNewHubSourceRegistryEmptyRefDefaultsLocal(t *testing.T) {
	registry := newHubSourceRegistry(hubcore.WebConfig{
		Roster:           hubcore.NewRosterWithEntries(),
		RemoteHosts:      []hostreg.Host{{Name: "h1", SSH: "h1.example"}},
		RemoteHostClient: unusedRemoteHostClient,
	})
	source, err := sourceForThread(registry, "", "")
	if err != nil {
		t.Fatalf("sourceForThread: %v", err)
	}
	if source.ID() != "local" {
		t.Fatalf("empty-ref default source = %q, want local", source.ID())
	}
	host, err := sourceForThread(registry, "h1:session", "")
	if err != nil {
		t.Fatalf("sourceForThread(host ref): %v", err)
	}
	if host.ID() != "h1" {
		t.Fatalf("host ref source = %q, want h1", host.ID())
	}
}

func TestNewHubSourceRegistrySkipsHostsWithoutClient(t *testing.T) {
	original := os.Stderr
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		os.Stderr = original
		_ = readEnd.Close()
	})
	os.Stderr = writeEnd
	registry := newHubSourceRegistry(hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{
			{Name: "h1", SSH: "h1.example"},
			{Name: "h2", SSH: "h2.example"},
		},
	})
	_ = writeEnd.Close()
	os.Stderr = original
	data, _ := io.ReadAll(readEnd)

	if _, ok := registry.Source("h1"); ok {
		t.Fatal("h1 registered without a remote host client")
	}
	diagnostic := string(data)
	if !strings.Contains(diagnostic, "h1") || !strings.Contains(diagnostic, "h2") {
		t.Fatalf("diagnostic = %q, want it to name both skipped hosts", diagnostic)
	}
}

// A plain (non-subscribe) thread/read starts a relay for a remote hub source,
// exactly as it does for the local daemon: the hub's default relay policy is
// true and 05b implements SubscribeThread, so the relay's attach is a real
// subscription the source can retire. 05a overrode both answers to false only
// while SubscribeThread was staged, which would have discarded every read.
func TestRemoteHubSourceRelaysPlainThreadRead(t *testing.T) {
	source := appsource.NewRemoteHubSource("h1", nil, unusedRemoteHostClient)
	if !relayOnThreadRead(source) {
		t.Fatal("RemoteHubSource does not relay a plain thread read; SubscribeThread is implemented and owns the subscription")
	}
	if !sourceSupportsThreadRelay(source) {
		t.Fatal("RemoteHubSource reports no relay fan-out; a subscribed read would never reach SubscribeThread")
	}
}

func TestRemoteHubSourceWireMethodsInHubCatalog(t *testing.T) {
	hubMethods := map[string]bool{}
	for _, name := range appwire.CatalogMethodNames(appwire.ScopeHub) {
		hubMethods[name] = true
	}
	for _, method := range []string{
		appwire.MethodThreadList,
		appwire.MethodThreadRead,
		appwire.MethodThreadTurnsList,
		appwire.MethodModelList,
	} {
		if !hubMethods[method] {
			t.Errorf("wire method %q is not in CatalogMethodNames(ScopeHub)", method)
		}
	}
}

// runMain builds one registry entry per validated [[hosts]] entry and hands the
// same slice to the web config as RemoteHosts (one source per host), so this
// mapping is the only place a configured field can be lost before either
// consumer sees it. This asserts every field on purpose, the way config_test.go
// pins the validation round-trip: a field added to HostConfig or hostreg.Host
// but not to the mapping would otherwise reach sshconn zeroed — a non-default
// ConfigPath would make the bridge attach with the wrong hub.toml, and a
// non-default Addr would make the restart/health probes address the default
// listener instead of the configured one.
func TestHostRegistryEntriesCarryEveryHostField(t *testing.T) {
	want := hostreg.Host{
		Name:       "alpha",
		SSH:        "alpha.example",
		User:       "deploy",
		EvenerPath: "/opt/evener/bin/evener",
		ConfigPath: "/etc/evener/alpha-hub.toml",
		Addr:       "127.0.0.1:9280",
		Roots:      []string{"/srv/one", "/srv/two"},
	}
	cfg := Config{Hosts: []HostConfig{{
		Name:       want.Name,
		SSH:        want.SSH,
		User:       want.User,
		EvenerPath: want.EvenerPath,
		ConfigPath: want.ConfigPath,
		Addr:       want.Addr,
		Roots:      want.Roots,
	}}}

	entries := hostRegistryEntries(cfg)
	if len(entries) != 1 || !reflect.DeepEqual(entries[0], want) {
		t.Fatalf("hostRegistryEntries() = %+v, want [%+v]", entries, want)
	}

	// The registry main.go hands to sshconn.New must store the same values.
	registry, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New(hostRegistryEntries(cfg)): %v", err)
	}
	registered, ok := registry.Get("alpha")
	if !ok {
		t.Fatal("alpha missing from the registry built from the config")
	}
	// The one difference from the configured literal is the field the registry
	// owns: Add assigns the entry its per-name generation on insert, so a
	// byte-identical re-add of the name is a different entry (the round-3
	// identity rule). Everything else must round-trip unchanged.
	want.Generation = 1
	if !reflect.DeepEqual(registered, want) {
		t.Fatalf("registry.Get(%q) = %+v, want %+v", "alpha", registered, want)
	}
}
