package hub

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

func TestE2E_ManagerReadinessSavedAttributionAndReplaySSH(t *testing.T) {
	if os.Getenv("EVENER_SSH_E2E") != "1" || os.Getenv("EVENER_SSH_E2E_DEPLOY") != "1" {
		t.Skip("requires both SSH E2E opt-ins; uses only disposable localhost SSH fixtures")
	}
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: localhost SSH, two hubs and daemons")
	}
	probe, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := exec.CommandContext(probe, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "localhost", "true").Run(); err != nil {
		t.Fatalf("required localhost SSH unavailable: %v", err)
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	a, aHuman, b, bHuman, _, bHubID := startManagerReadinessAttachedFixture(ctx, t, provider)
	manager, err := clientRequest[appwire.ThreadStartResponse](ctx, bHuman, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: b.workDir, Model: b.model, Manager: true, LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"}})
	if err != nil {
		t.Fatal(err)
	}
	managerRef := manager.Thread.Evener.Ref
	t.Cleanup(func() {
		shutdown, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := clientRequest[appwire.EmptyResponse](shutdown, bHuman, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: managerRef}); err != nil {
			t.Errorf("B Manager shutdown: %v", err)
		}
	})
	// Calling the existing workflow twice routes both actions through the B
	// Manager's tools. Empty host name selects B-local; the second call crosses
	// B's attached-host registry to A.
	acceptanceRemoteManagerInput(ctx, t, provider, b, b, bHuman, bHuman, managerRef, "", bHubID)
	acceptanceRemoteManagerInput(ctx, t, provider, b, a, bHuman, aHuman, managerRef, "readiness-a", bHubID)
}

// startManagerReadinessAttachedFixture connects the existing two-hub SSH
// fixture in both directions. B's identity comes from its configured store.
func startManagerReadinessAttachedFixture(ctx context.Context, t *testing.T, provider *fakellm.Server) (hubStack, *appwire.Client, hubStack, *appwire.Client, string, string) {
	t.Helper()
	a, aHuman, _, hostDir, bAddr, _ := startManagerAcceptanceSSHFixture(t, provider, ctx)
	tokenBytes, err := os.ReadFile(filepath.Join(hostDir, "state", "auth-token"))
	if err != nil {
		t.Fatal(err)
	}
	b := hubStack{addr: bAddr, token: strings.TrimSpace(string(tokenBytes)), workDir: filepath.Join(hostDir, "workspace"), model: "fake/" + fakellm.ModelID, home: filepath.Join(hostDir, "home")}
	bHuman := b.dialRPC(ctx, t)
	// Add A to B through B's real host registry, with A's private launch roots.
	aState := filepath.Join(a.home, "state", "evener")
	aConfig := filepath.Join(hostDir, "readiness-a.toml")
	if err := os.WriteFile(aConfig, []byte(fmt.Sprintf("addr = %q\nhub_state_root = %q\nrun_dir = %q\nstate_glob = %q\npast_index_db = %q\nplugin_auto_upgrade = false\n", a.addr, aState, filepath.Join(aState, "run"), filepath.Join(aState, "projects", "*"), filepath.Join(aState, "index.db"))), 0600); err != nil {
		t.Fatal(err)
	}
	aWrapper := filepath.Join(hostDir, "bin", "evener-readiness-a")
	if err := os.WriteFile(aWrapper, []byte(fmt.Sprintf("#!/bin/sh\nexport HOME=%s XDG_CONFIG_HOME=%s XDG_STATE_HOME=%s XDG_CACHE_HOME=%s\nexec %s \"$@\"\n", shellquote.RemoteWord(a.home), shellquote.RemoteWord(filepath.Join(a.home, "config")), shellquote.RemoteWord(filepath.Join(a.home, "state")), shellquote.RemoteWord(filepath.Join(a.home, "cache")), shellquote.RemoteWord(filepath.Join(a.binDir, "evener")))), 0700); err != nil {
		t.Fatal(err)
	}
	const hostName = "readiness-a"
	if _, err := hostAddCommittedRow(ctx, bHuman, appwire.HostAddParams{Entry: appwire.HostEntry{Name: hostName, Address: "localhost", EvenerPath: aWrapper, ConfigPath: aConfig, Addr: a.addr}}); err != nil {
		t.Fatal(err)
	}
	awaitHostAttachedWithin(ctx, t, bHuman, hostName, hostDeployAttachTimeout)
	bHubID, err := hubcore.NewHubIdentityStore(filepath.Join(hostDir, "state", "index.db")).HubID()
	if err != nil {
		t.Fatal(err)
	}
	if bHubID == "" {
		t.Fatal("B durable hub identity is empty")
	}
	return a, aHuman, b, bHuman, hostDir, bHubID
}
