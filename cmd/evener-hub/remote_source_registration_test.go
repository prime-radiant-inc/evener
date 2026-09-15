package hub

import (
	"context"
	"errors"
	"io"
	"os"
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
	registry := newHubSourceRegistry(hubcore.WebConfig{})
	if _, ok := registry.Source("local"); !ok {
		t.Fatal("local source not registered")
	}
	if _, ok := registry.Source("h1"); ok {
		t.Fatal("h1 registered without configured hosts")
	}
}

func TestNewHubSourceRegistryEmptyRefDefaultsLocal(t *testing.T) {
	registry := newHubSourceRegistry(hubcore.WebConfig{
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

// A plain (non-subscribe) thread/read must not start a relay for a remote hub
// source: startRelay calls SubscribeThread unconditionally, which is staged
// until 05b, so the default relay policy would discard every successful read.
func TestRemoteHubSourceDoesNotRelayPlainThreadRead(t *testing.T) {
	source := appsource.NewRemoteHubSource("h1", nil, unusedRemoteHostClient)
	if relayOnThreadRead(source) {
		t.Fatal("RemoteHubSource relays a plain thread read; SubscribeThread is not implemented until 05b")
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
