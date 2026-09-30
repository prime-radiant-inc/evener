package hub

// A picker open past the launch list's TTL is served the cached list at once
// and refreshes it behind the request. When that refresh lands with a list
// that differs, the hub tells every client (Jesse, 2026-09-30), so an open
// picker updates in place instead of showing the old list until it is
// reopened. It reuses evener/auth/updated's no-data form, the broadcast the
// hub already sends when a live model listing changes what clients show.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// staleLaunchWeb is a web server whose unscoped launch list is cached and then
// retired by a registry reload, so the next read is served stale and starts a
// background refresh. models answers each launch check by call number.
func staleLaunchWeb(t *testing.T, models func(call int) appwire.ModelListResponse) (*WebServer, *int, *countLaunchContractSpawner) {
	t.Helper()
	spawner := &countLaunchContractSpawner{modelsFn: func(call int, _ string) appwire.ModelListResponse { return models(call) }}
	reg := newBumpableProviderRegistry(t)
	web := &WebServer{
		cfg:          hubcore.WebConfig{Registry: reg, Spawner: spawner},
		launchModels: &launchModelsCache{entries: map[string]*launchModelsEntry{}, refreshing: map[string]bool{}},
		lifetime:     context.Background(),
	}
	announced := 0
	web.launchModelsChanged = func() { announced++ }
	if _, err := web.fetchLaunchModels(context.Background(), ""); err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	if err := reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return web, &announced, spawner
}

// changingModels answers each launch check with a different list.
func changingModels(call int) appwire.ModelListResponse {
	return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: fmt.Sprintf("gen-%d", call)}}}
}

func TestLaunchRefreshAnnouncesAChangedList(t *testing.T) {
	web, announced, _ := staleLaunchWeb(t, changingModels)
	stale, err := web.fetchLaunchModels(context.Background(), "")
	if err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	if stale.Data[0].Model != "gen-1" {
		t.Fatalf("precondition: served %q, want the stale gen-1", stale.Data[0].Model)
	}
	web.waitLaunchRefreshes()
	if *announced != 1 {
		t.Fatalf("announced %d times after a refresh that changed the list, want once", *announced)
	}
}

func TestLaunchRefreshOfAnUnchangedListStaysSilent(t *testing.T) {
	web, announced, _ := staleLaunchWeb(t, func(int) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "same"}}}
	})
	if _, err := web.fetchLaunchModels(context.Background(), ""); err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	web.waitLaunchRefreshes()
	if *announced != 0 {
		t.Fatalf("announced %d times after a refresh that changed nothing, want none", *announced)
	}
}

// The hub's constructor wires the announcement to the broadcast: a client
// connected to the hub receives evener/auth/updated when a refresh changes
// the launch list.
func TestLaunchRefreshBroadcastsAuthUpdated(t *testing.T) {
	spawner := &countLaunchContractSpawner{modelsFn: func(call int, _ string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: fmt.Sprintf("gen-%d", call)}}}
	}}
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Spawner: spawner})
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { client.Close() })
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var resp appwire.ModelListResponse
	if err := client.Request(context.Background(), appwire.MethodModelList, appwire.ModelListParams{}, &resp); err != nil {
		t.Fatalf("model/list: %v", err)
	}
	if err := web.cfg.Registry.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := client.Request(context.Background(), appwire.MethodModelList, appwire.ModelListParams{}, &resp); err != nil {
		t.Fatalf("model/list: %v", err)
	}
	// The no-data form: no provider and no originating client, so every
	// client reads it as a change it did not make and re-reads.
	got := waitForAuthUpdated(t, client)
	if got.Provider != "" || got.ActiveSource != "" || got.OriginClientId != "" {
		t.Fatalf("auth/updated = %+v, want the no-data form", got)
	}
}

// A refresh that fails says nothing: the picker keeps the list it has.
func TestLaunchRefreshThatFailsStaysSilent(t *testing.T) {
	web, announced, spawner := staleLaunchWeb(t, changingModels)
	spawner.mu.Lock()
	spawner.err = errors.New("launch check failed")
	spawner.mu.Unlock()
	if _, err := web.fetchLaunchModels(context.Background(), ""); err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	web.waitLaunchRefreshes()
	if *announced != 0 {
		t.Fatalf("announced %d times after a failed refresh, want none", *announced)
	}
}

// The comparison is against the list the triggering request was served, not
// whatever the cache holds by the time the refresh lands: an entry evicted
// while its refresh ran still left a picker showing that list, so the
// refreshed list is announced.
func TestLaunchRefreshOfAnEvictedEntryAnnounces(t *testing.T) {
	web, announced, spawner := staleLaunchWeb(t, changingModels)
	release := make(chan struct{})
	spawner.mu.Lock()
	next := spawner.modelsFn
	spawner.modelsFn = func(call int, dir string) appwire.ModelListResponse {
		<-release
		return next(call, dir)
	}
	spawner.mu.Unlock()
	if _, err := web.fetchLaunchModels(context.Background(), ""); err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	// The refresh is waiting on the launch check; evict the entry under it.
	deadline := time.Now().Add(5 * time.Second)
	for spawner.callCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the stale read never started a refresh")
		}
		time.Sleep(time.Millisecond)
	}
	web.launchModels.mu.Lock()
	delete(web.launchModels.entries, "")
	web.launchModels.mu.Unlock()
	close(release)
	web.waitLaunchRefreshes()
	if *announced != 1 {
		t.Fatalf("announced %d times after refreshing an evicted entry, want once", *announced)
	}
}
