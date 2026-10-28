package hub

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func TestHubRPCModelListUsesEvenerLaunchContractWhenDaemonFails(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{}, appwire.InternalError("provider unavailable")
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 104, "th_1", "th_1")

	spawner := &fakeRPCSpawner{
		launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
			return []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  runDir,
		Roster:  roster,
		Spawner: spawner,
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-5.5" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListFallsBackToLocalDaemonWithoutLaunchContract(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-daemon"}}}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 105, "th_1", "th_1")

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-daemon" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListPrefersEvenerLaunchContract(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-stale"}}}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 105, "th_1", "th_1")

	spawner := &fakeRPCSpawner{
		launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
			return []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-5.5" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

// TestHubRPCModelListCachesLaunchContract: two model/list RPCs (two picker
// opens) must spawn `evener launch-check --models` once, not once per open.
// Before the hub cached the launch contract, every open paid the full live
// provider listing again.
func TestHubRPCModelListCachesLaunchContract(t *testing.T) {
	spawner := &countLaunchContractSpawner{modelsFn: func(int, string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	}}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Spawner: spawner})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	for range 2 {
		resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
		if err != nil {
			t.Fatalf("ModelList: %v", err)
		}
		if len(resp.Data) != 1 || resp.Data[0].Model != "gpt-5.5" {
			t.Fatalf("models=%+v", resp.Data)
		}
	}
	if got := spawner.callCount(); got != 1 {
		t.Fatalf("launch contract called %d times for two model/list RPCs, want 1", got)
	}
}

func TestHubRPCModelListUsesWorkingDirForEvenerLaunchContract(t *testing.T) {
	spawner := &fakeRPCWorkingDirModelContractSpawner{
		fallback: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "stale", Model: "wrong"}},
		},
		contractForWorkingDir: func(_ context.Context, cwd string) (appwire.ModelListResponse, error) {
			if cwd != "/tmp/project-with-oauth" {
				return appwire.ModelListResponse{}, fmt.Errorf("cwd=%q, want /tmp/project-with-oauth", cwd)
			}
			return appwire.ModelListResponse{
				Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-visible-to-agent"}},
			}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Spawner: spawner})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{CWD: "/tmp/project-with-oauth"})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-visible-to-agent" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListDoesNotUseLocalDaemonWhenLaunchContractIsEmpty(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-daemon"}}}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 105, "th_1", "th_1")

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  runDir,
		Roster:  roster,
		Spawner: &fakeRPCModelContractSpawner{contract: appwire.ModelListResponse{}},
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestHubRPCModelListDoesNotUseLocalDaemonWhenLaunchContractHasOnlyDiagnostics(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-daemon"}}}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 105, "th_1", "th_1")

	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("models=%+v", resp.Data)
	}
	if len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Provider != "openai" || !strings.Contains(resp.Diagnostics[0].Message, "403") {
		t.Fatalf("diagnostics=%+v", resp.Diagnostics)
	}
}

func TestHubRPCModelListReportsEvenerLaunchDiagnostics(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-evener")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
	  printf '{"protocol":"evener-appwire-v7","models":[{"provider":"ollama","model":"local"}],"diagnostics":[{"provider":"openai","source":"provider","title":"Provider error","message":"HTTP 403"}]}\n'
  exit 0
fi
exit 2
`
	writeFakeEvener(t, bin, script)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  t.TempDir(),
		Spawner: &HubSpawner{Cfg: DefaultConfig(), EvenerBinary: bin, RunDir: t.TempDir(), HubToken: "generated-token"},
		Past:    hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	var resp struct {
		Data []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"data"`
		Diagnostics []struct {
			Provider string `json:"provider"`
			Source   string `json:"source"`
			Title    string `json:"title"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := client.Request(context.Background(), appwire.MethodModelList, appwire.ModelListParams{}, &resp); err != nil {
		t.Fatalf("model/list: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "ollama" || resp.Data[0].Model != "local" {
		t.Fatalf("models=%+v", resp.Data)
	}
	if len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Provider != "openai" || resp.Diagnostics[0].Source != "provider" || !strings.Contains(resp.Diagnostics[0].Message, "403") {
		t.Fatalf("diagnostics=%+v", resp.Diagnostics)
	}
}

type fakeRPCWorkingDirModelContractSpawner struct {
	fakeRPCSpawner
	fallback              appwire.ModelListResponse
	contractForWorkingDir func(context.Context, string) (appwire.ModelListResponse, error)
}

func (f *fakeRPCWorkingDirModelContractSpawner) ListLaunchModelContract(context.Context) (appwire.ModelListResponse, error) {
	return f.fallback, nil
}

func (f *fakeRPCWorkingDirModelContractSpawner) ListLaunchModelContractForWorkingDir(ctx context.Context, cwd string) (appwire.ModelListResponse, error) {
	if f.contractForWorkingDir == nil {
		return appwire.ModelListResponse{}, nil
	}
	return f.contractForWorkingDir(ctx, cwd)
}
