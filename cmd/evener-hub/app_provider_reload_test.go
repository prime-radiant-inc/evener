package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
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
	server, _, _, n, _ := newHubAppServerWithNavigationAndTrace(hubcore.WebConfig{Registry: reg, ProvidersConfigPath: path}, appsource.NewRegistry(), nil, nil, nil)
	t.Cleanup(func() { server.Shutdown(context.Background()) })
	if n.refreshProviders == nil {
		t.Fatal("server did not retain the recovery owner")
	}
	broadcaster := newRecordingBroadcaster()
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	observed := make(chan struct{})
	done := make(chan struct{})
	go func() {
		runNoticeWatcher(ctx, ticks, func(ctx context.Context) []appwire.HubNotice {
			result := n.watchRead(ctx, broadcaster)
			observed <- struct{}{}
			return result
		}, broadcaster)
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
	web.notices.watchRead(context.Background(), newRecordingBroadcaster())
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate,
		appwire.InstanceCreateParams{Name: "work", Base: "anthropic", OriginClientId: "tab-a"}, &resp); err != nil {
		t.Fatal(err)
	}
	assertInstanceBroadcastShape(t, appwire.MethodEvenerInstanceCreate, waitForAuthUpdatedRaw(t, client), "tab-a")
	generation := f.ctl.reg.Generation()
	broadcaster := newRecordingBroadcaster()
	web.notices.watchRead(context.Background(), broadcaster)
	if f.ctl.reg.Generation() != generation || len(broadcaster.broadcasts()) != 0 {
		t.Fatal("watcher reloaded and announced an already-notified instance write")
	}
	// A later external edit still needs the unowned broadcast.
	writeProvidersToml(t, filepath.Dir(f.tomlPath), "[providers.external]\nbase = \"anthropic\"\n")
	web.notices.watchRead(context.Background(), broadcaster)
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
