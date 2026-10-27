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
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

func TestE2E_ManagerReadinessOwnHubFirstInputSSH(t *testing.T) {
	if os.Getenv("EVENER_SSH_E2E") != "1" || os.Getenv("EVENER_SSH_E2E_DEPLOY") != "1" {
		t.Skip("requires both SSH E2E opt-ins; uses only a disposable localhost SSH fixture")
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
	a, aHuman, _, hostDir, bAddr, _ := startManagerAcceptanceSSHFixture(t, provider, ctx)

	bToken, err := os.ReadFile(hostDir + "/state/auth-token")
	if err != nil {
		t.Fatal(err)
	}
	b := hubStack{addr: bAddr, token: strings.TrimSpace(string(bToken)), workDir: hostDir + "/workspace", model: "fake/" + fakellm.ModelID}
	bHuman := b.dialRPC(ctx, t)
	// Reverse-attach B to the already-running A through B's actual host registry.
	// This wrapper pins A's private HOME/XDG roots; explicit hub config keeps
	// bootstrap and authentication on A's fixture-owned state root.
	aStateRoot := filepath.Join(a.home, "state", "evener")
	aAuthToken := filepath.Join(aStateRoot, "auth-token")
	if _, err := os.Stat(aAuthToken); err != nil {
		t.Fatalf("A fixture auth token at %s: %v", aAuthToken, err)
	}
	aHostConfig := filepath.Join(hostDir, "a-hub.toml")
	if err := os.WriteFile(aHostConfig, []byte(fmt.Sprintf("addr = %q\nhub_state_root = %q\nrun_dir = %q\nstate_glob = %q\npast_index_db = %q\nplugin_auto_upgrade = false\n", a.addr, aStateRoot, filepath.Join(aStateRoot, "run"), filepath.Join(aStateRoot, "projects", "*"), filepath.Join(aStateRoot, "index.db"))), 0o600); err != nil {
		t.Fatal(err)
	}
	aWrapper := filepath.Join(hostDir, "bin", "evener-a")
	if err := os.WriteFile(aWrapper, []byte(fmt.Sprintf("#!/bin/sh\nexport HOME=%s XDG_CONFIG_HOME=%s XDG_STATE_HOME=%s XDG_CACHE_HOME=%s\nexec %s \"$@\"\n", shellquote.RemoteWord(a.home), shellquote.RemoteWord(filepath.Join(a.home, "config")), shellquote.RemoteWord(filepath.Join(a.home, "state")), shellquote.RemoteWord(filepath.Join(a.home, "cache")), shellquote.RemoteWord(filepath.Join(a.binDir, "evener")))), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := hostAddCommittedRow(ctx, bHuman, appwire.HostAddParams{Entry: appwire.HostEntry{
		Name: "readiness-a", Address: "localhost", EvenerPath: aWrapper,
		ConfigPath: aHostConfig, Addr: a.addr,
	}}); err != nil {
		t.Fatalf("B add A through host registry: %v", err)
	}
	awaitHostAttachedWithin(ctx, t, bHuman, "readiness-a", hostDeployAttachTimeout)

	localTarget, err := clientRequest[appwire.ThreadStartResponse](ctx, bHuman, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: b.workDir, Model: b.model})
	if err != nil {
		t.Fatal(err)
	}
	localRef := localTarget.Thread.Evener.Ref
	t.Cleanup(func() {
		shutdown, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := clientRequest[appwire.EmptyResponse](shutdown, bHuman, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: localRef}); err != nil {
			t.Errorf("B-local target shutdown: %v", err)
		}
	})
	remoteTarget, err := clientRequest[appwire.ThreadStartResponse](ctx, aHuman, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: a.workDir, Model: a.model})
	if err != nil {
		t.Fatal(err)
	}
	remoteParsed, err := appwire.ParseRef(remoteTarget.Thread.Evener.Ref)
	if err != nil {
		t.Fatal(err)
	}
	remoteRef := (appwire.Ref{SourceID: "readiness-a", ThreadID: remoteParsed.ThreadID}).String()
	t.Cleanup(func() {
		shutdown, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := clientRequest[appwire.EmptyResponse](shutdown, aHuman, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: remoteTarget.Thread.Evener.Ref}); err != nil {
			t.Errorf("A target shutdown: %v", err)
		}
	})

	const opening = "READINESS-OWN-HUB-B-FIRST-INPUT"
	manager, err := clientRequest[appwire.ThreadStartResponse](ctx, bHuman, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: b.workDir, Model: b.model, Manager: true,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
		Input:           []appwire.InputItem{{Type: "text", Text: opening}},
	})
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
	call, err := provider.Next(ctx.Done())
	if err != nil || !call.Contains(opening) || !managerRequestHasTool(call, "job_list") {
		t.Fatalf("B Manager first provider request: call=%v err=%v", call, err)
	}
	call.RespondToolCall("job_list", map[string]any{"hub": map[string]any{"mode": "discovery"}})
	call, err = provider.Next(ctx.Done())
	if err != nil || !call.Contains(opening) {
		t.Fatalf("B discovery provider request: call=%v err=%v", call, err)
	}
	var discovery appwire.SessionOperationResponse
	if !managerToolResult(call, &discovery) || discovery.Discover == nil {
		t.Fatalf("B discovery: result=%+v refusal=%s", discovery, managerToolRefusal(call))
	}
	foundLocal, foundRemote := false, false
	for _, row := range discovery.Discover.Threads.Data {
		foundLocal = foundLocal || row.Evener.Ref == localRef
		foundRemote = foundRemote || row.Evener.Ref == remoteRef
	}
	if !foundLocal || !foundRemote {
		t.Fatalf("B discovery did not include both intended targets: local=%t remote=%t threads=%+v", foundLocal, foundRemote, discovery.Discover.Threads.Data)
	}
	for _, target := range []string{localRef, remoteRef} {
		call.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": target}})
		call, err = provider.Next(ctx.Done())
		if err != nil || !call.Contains(opening) {
			t.Fatalf("B target read provider request %s: call=%v err=%v", target, call, err)
		}
		var read appwire.SessionOperationResponse
		if !managerToolResult(call, &read) || read.Read == nil || read.Read.Thread.Evener.Ref != target || read.Actor.Kind != "manager" || read.Actor.ManagerRef != managerRef {
			t.Fatalf("B Manager read target %s: result=%+v refusal=%s", target, read, managerToolRefusal(call))
		}
	}
	call.RespondToolCall("communicate", communicateArgs("B targets read"))
	turns, err := readTranscriptTurns(ctx, bHuman, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"job_list", "job_status"} {
		if !acceptanceHasTool(turns, tool) {
			t.Fatalf("saved B Manager history lacks %s: %v", tool, managerItemSummary(turns))
		}
	}
}

func TestE2E_ManagerReadinessOwnHubFirstInput(t *testing.T) {
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	stack := startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	human := stack.dialRPC(ctx, t)
	const firstInput = "READINESS-OWN-HUB-FIRST-INPUT"
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, human, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model, Manager: true,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
		Input:           []appwire.InputItem{{Type: "text", Text: firstInput}},
	})
	if err != nil {
		t.Fatal(err)
	}
	managerRef := started.Thread.Evener.Ref
	t.Cleanup(func() {
		shutdown, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := clientRequest[appwire.EmptyResponse](shutdown, human, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: managerRef}); err != nil {
			t.Errorf("Manager shutdown: %v", err)
		}
	})
	call, err := provider.Next(ctx.Done())
	if err != nil || !call.Contains(firstInput) || !managerRequestHasTool(call, "job_list") {
		t.Fatalf("first Manager provider request: call=%v err=%v", call, err)
	}
	call.RespondToolCall("job_list", map[string]any{"hub": map[string]any{"mode": "discovery"}})
	call, err = provider.Next(ctx.Done())
	if err != nil || !call.Contains(firstInput) {
		t.Fatalf("discovery result provider request: call=%v err=%v", call, err)
	}
	var discovery appwire.SessionOperationResponse
	if !managerToolResult(call, &discovery) || discovery.Discover == nil {
		t.Fatalf("Manager discovery through its own hub: result=%+v refusal=%s", discovery, managerToolRefusal(call))
	}
	foundSelf := false
	for _, row := range discovery.Discover.Threads.Data {
		if row.Evener.Ref == managerRef {
			foundSelf = true
		}
	}
	if !foundSelf {
		t.Fatalf("B-hub discovery omitted the Manager hosted on B: %+v", discovery.Discover)
	}
	call.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": managerRef}})
	call, err = provider.Next(ctx.Done())
	if err != nil || !call.Contains(firstInput) {
		t.Fatalf("read result provider request: call=%v err=%v", call, err)
	}
	var read appwire.SessionOperationResponse
	if !managerToolResult(call, &read) || read.Read == nil || read.Read.Thread.Evener.Ref != managerRef || read.Actor.Kind != "manager" || read.Actor.ManagerRef != managerRef {
		t.Fatalf("Manager read through B's hub: result=%+v refusal=%s", read, managerToolRefusal(call))
	}
	call.RespondToolCall("communicate", communicateArgs("own hub read"))
	turns, err := readTranscriptTurns(ctx, human, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	if !acceptanceHasTool(turns, "job_list") || !acceptanceHasTool(turns, "job_status") {
		t.Fatalf("first-turn Manager operations missing from saved history: %v", managerItemSummary(turns))
	}
}

func managerRequestHasTool(call *fakellm.Call, wanted string) bool {
	tools, _ := call.Body["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		function, _ := tool["function"].(map[string]any)
		if function["name"] == wanted {
			return true
		}
	}
	return false
}
