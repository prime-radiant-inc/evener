package hub

// A picker open past the launch list's TTL is served the cached list at once
// and refreshes it behind the request. When that refresh lands with a list
// that differs, the hub tells every client (Jesse, 2026-09-30), so an open
// picker updates in place instead of showing the old list until it is
// reopened. It reuses evener/auth/updated's no-data form, the broadcast the
// hub already sends when a live model listing changes what clients show.

import (
	"context"
	"fmt"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// staleLaunchWeb is a web server whose unscoped launch list is cached and then
// retired by a registry reload, so the next read is served stale and starts a
// background refresh. models answers each launch check by call number.
func staleLaunchWeb(t *testing.T, models func(call int) appwire.ModelListResponse) (*WebServer, *int) {
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
	return web, &announced
}

func TestLaunchRefreshAnnouncesAChangedList(t *testing.T) {
	web, announced := staleLaunchWeb(t, func(call int) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: fmt.Sprintf("gen-%d", call)}}}
	})
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
	web, announced := staleLaunchWeb(t, func(int) appwire.ModelListResponse {
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
	waitForAuthUpdated(t, client)
}
