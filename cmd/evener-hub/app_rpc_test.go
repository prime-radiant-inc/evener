package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/internal/selfupdate"
	"primeradiant.com/evener/rendezvous"
)

func TestHubInitializeReportsBuildVersion(t *testing.T) {
	server := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	message := server.NewConnection("test").HandleMessage(t.Context(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if message.Response == nil || message.Error != nil {
		t.Fatalf("initialize = %#v", message)
	}
	raw, err := json.Marshal(message.Response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	var serverInfo map[string]string
	if err := json.Unmarshal(response["serverInfo"], &serverInfo); err != nil {
		t.Fatal(err)
	}
	if got := serverInfo["buildVersion"]; got == "" || got != buildinfo.Version() {
		t.Fatalf("live build = %q, want %q", got, buildinfo.Version())
	}
	if got := serverInfo["version"]; got != Version {
		t.Fatalf("package version = %q, want %q", got, Version)
	}
}

func TestHubRPCPluginPreviewRoute(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	registerPluginHandlers(server, newHubPluginsController(t.TempDir(), t.TempDir()))
	out, err := server.Router().Dispatch(context.Background(), appwire.Request{
		Method: appwire.MethodEvenerPluginPreview,
		Params: json.RawMessage(`{"cwd":"/tmp"}`),
	})
	if err != nil {
		t.Fatalf("preview route: %v", err)
	}
	if _, ok := out.(appwire.PluginPreviewResponse); !ok {
		t.Fatalf("preview route response = %T, want PluginPreviewResponse", out)
	}
}

func TestHubRPCSteersSurvivingDaemonAfterHubRestart(t *testing.T) {
	for _, cleared := range []bool{false, true} {
		t.Run(fmt.Sprint("cleared=", cleared), func(t *testing.T) { testHubSteersSurvivingDaemon(t, cleared) })
	}
}

func testHubSteersSurvivingDaemon(t *testing.T, cleared bool) {
	const (
		daemonProtocol = appwire.ProtocolVersion
		threadID       = "th_compatible"
		mutationID     = "mutation-compatible-steer"
	)

	steered := make(chan appwire.TurnSteerParams, 1)
	runDir := t.TempDir()
	daemonHTTP := startAppwireTestDaemonWithProtocol(t, runDir, threadID, daemonProtocol, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodTurnSteer, func(_ context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			steered <- params
			return appwire.TurnSteerResponse{Receipt: appwire.MutationReceipt{
				ClientMutationID: params.ClientMutationID,
				Disposition:      appwire.MutationDispositionApplied,
				ThreadID:         threadID,
				ProjectionState:  appwire.MutationProjectionReflected,
			}}, nil
		})
	})
	defer daemonHTTP.Close()

	resumeID := threadID
	if cleared {
		resumeID = "workspace_before_clear"
		entries, err := rendezvous.List(runDir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("rendezvous entries=%+v error=%v", entries, err)
		}
		entry := entries[0]
		entry.WorkspaceRef = "local:" + resumeID
		writeRendezvous(t, runDir, entry)
	}

	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: threadID, status: appwire.ThreadStatusActive})
	roster.Refresh()

	resumeCalls := 0
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      roster,
		Past:        hubcore.NewPastIndex(""),
		ResumeLocks: hubcore.NewResumeLocks(),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			resumeCalls++
			return rendezvous.Entry{}, errors.New("surviving daemon must be reused")
		}},
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resumed, err := appwiretest.ThreadResume(t.Context(), client, appwire.ThreadResumeParams{Ref: "local:" + resumeID})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if resumed.Thread.ID != threadID || resumed.Thread.Evener.Ref != "local:"+threadID || resumeCalls != 0 {
		t.Fatalf("resume = %+v, replacement calls = %d", resumed.Thread, resumeCalls)
	}

	var response appwire.TurnSteerResponse
	err = client.Request(t.Context(), appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:                "local:" + threadID,
		ThreadID:           threadID,
		ClientMutationID:   mutationID,
		ExpectedInstanceID: threadID,
		Input:              []appwire.InputItem{{Type: "text", Text: "survived the hub restart"}},
	}, &response)
	if err != nil {
		t.Fatalf("TurnSteer: %v", err)
	}
	if response.Receipt.ClientMutationID != mutationID || response.Receipt.ThreadID != threadID {
		t.Fatalf("receipt = %+v", response.Receipt)
	}
	params := <-steered
	if params.ClientMutationID != mutationID || inputTextForTest(params.Input) != "survived the hub restart" {
		t.Fatalf("daemon steer params = %+v", params)
	}
}

func TestHubRPCAdvertisesTurnListsWithHandlers(t *testing.T) {
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	init, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// thread/turns/list is implemented (lazy transcript loading), so the hub
	// must advertise the capability.
	if !init.Features.ThreadTurnsList {
		t.Fatalf("ThreadTurnsList not advertised despite Hub handlers: %+v", init.Features)
	}
}

func TestHubRPCUpgradeRunsSelfUpdater(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	var got selfupdate.Options
	previous := runHubSelfUpgrade
	runHubSelfUpgrade = func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		got = opts
		return selfupdate.Result{
			Release:        "snapshot",
			Channel:        "snapshot",
			Archive:        "evener_linux_amd64.tar.gz",
			ShareBinDir:    "/tmp/share/evener/bin",
			BinDir:         "/tmp/bin",
			RestartMessage: "Restart evener to use the upgraded binary.",
		}, nil
	}
	t.Cleanup(func() { runHubSelfUpgrade = previous })

	server := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	params, err := json.Marshal(appwire.UpgradeParams{Requested: "snapshot"})
	if err != nil {
		t.Fatalf("MarshalParams: %v", err)
	}
	raw, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerUpgrade,
		Params: params,
	})
	if err != nil {
		t.Fatalf("Dispatch upgrade: %v", err)
	}
	resp, ok := raw.(appwire.UpgradeResponse)
	if !ok {
		t.Fatalf("response type=%T", raw)
	}
	if resp.Channel != "snapshot" || resp.Archive != "evener_linux_amd64.tar.gz" {
		t.Fatalf("response=%+v", resp)
	}
	if got.Requested != "snapshot" {
		t.Fatalf("Requested=%q, want snapshot", got.Requested)
	}
	if got.CurrentChannel == "" {
		t.Fatal("CurrentChannel is empty")
	}
}

func TestHubRPCThreadActionsRouteToDaemon(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	compactCalled := false
	shutdownCalled := false
	modelCalled := ""
	goalObjective := ""
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("read ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener: appwire.EvenerThread{
				Ref: "local:th_1",
				Capabilities: appwire.ThreadCapabilities{
					Send:         true,
					Steer:        true,
					Interrupt:    true,
					Compact:      true,
					Clear:        true,
					ForkFromTurn: true,
					Shutdown:     true,
					ChangeModel:  true,
					Goal:         true,
				},
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadCompactStart, func(_ context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("compact ref=%q", params.Ref)
		}
		compactCalled = true
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("model ref=%q", params.Ref)
		}
		modelCalled = params.ModelProvider + "/" + params.Model
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadShutdown, func(_ context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("shutdown ref=%q", params.Ref)
		}
		shutdownCalled = true
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodGoalSet, func(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("goal ref=%q", params.Ref)
		}
		goalObjective = params.Objective
		return appwire.GoalSetResponse{Started: true}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 104, "th_1", "sess_1")

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if err := client.ThreadCompactStart(context.Background(), appwire.ThreadCompactStartParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadCompactStart: %v", err)
	}
	if !compactCalled {
		t.Fatal("compact was not routed")
	}
	if err := client.ThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		Ref:           "local:th_1",
		ModelProvider: "openai",
		Model:         "gpt-5",
	}); err != nil {
		t.Fatalf("ThreadModelSet: %v", err)
	}
	if modelCalled != "openai/gpt-5" {
		t.Fatalf("modelCalled=%q", modelCalled)
	}
	goalResp, err := client.GoalSet(context.Background(), appwire.GoalSetParams{
		Ref:       "local:th_1",
		Objective: "improve coverage",
	})
	if err != nil {
		t.Fatalf("GoalSet: %v", err)
	}
	if goalObjective != "improve coverage" {
		t.Fatalf("goalObjective=%q", goalObjective)
	}
	if !goalResp.Started {
		t.Fatal("GoalSet response Started not propagated")
	}
	if err := client.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadShutdown: %v", err)
	}
	if !shutdownCalled {
		t.Fatal("shutdown was not routed")
	}
}

func TestHubRPCTurnMutationsForwardWithoutDynamicCapabilityGates(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener: appwire.EvenerThread{
				Ref:          params.Ref,
				Capabilities: appwire.ThreadCapabilities{},
			},
		}}, nil
	})
	called := make(map[string]int)
	receipt := func(method, mutationID string) appwire.MutationReceipt {
		called[method]++
		return appwire.MutationReceipt{
			ClientMutationID: mutationID,
			Disposition:      appwire.MutationDispositionReplayed,
			ThreadID:         "th_1",
			TurnID:           "turn_1",
			QueueEntryIDs:    []string{"queue_1"},
			ProjectionState:  appwire.MutationProjectionReflected,
		}
	}
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}, Receipt: receipt(appwire.MethodTurnStart, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnSteer, func(_ context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
		return appwire.TurnSteerResponse{Receipt: receipt(appwire.MethodTurnSteer, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnInterrupt, func(_ context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
		return appwire.TurnInterruptResponse{Receipt: receipt(appwire.MethodTurnInterrupt, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
		if inputTextForTest(params.Input) == "reject" {
			return appwire.TurnQueueResponse{}, appwire.WireError{
				Code:    appwire.CodeConflict,
				Message: "turn changed",
				Data: appwire.ErrorData{
					EvenerErrorInfo:  appwire.ErrorConflict,
					ClientMutationID: params.ClientMutationID,
					MutationOutcome:  appwire.MutationOutcomeNotAccepted,
					RetryDisposition: appwire.RetryDispositionNone,
				},
			}
		}
		return appwire.TurnQueueResponse{Receipt: receipt(appwire.MethodTurnQueue, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
		return appwire.TurnDrainAsSteerResponse{Receipt: receipt(appwire.MethodTurnDrainAsSteer, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnPromoteQueuedAsSteer, func(_ context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
		return appwire.TurnPromoteQueuedAsSteerResponse{Receipt: receipt(appwire.MethodTurnPromoteQueuedAsSteer, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnCancelQueued, func(_ context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
		return appwire.TurnCancelQueuedResponse{RemovedText: "queued", RemovedImages: 1, Receipt: receipt(appwire.MethodTurnCancelQueued, params.ClientMutationID)}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       os.Getpid(),
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	tests := []struct {
		name   string
		method string
		id     string
		params any
		result any
	}{
		{"start after response loss", appwire.MethodTurnStart, "mutation-start", appwire.TurnStartParams{Ref: "local:th_1", ClientMutationID: "mutation-start", ExpectedInstanceID: "sess_1", Input: []appwire.InputItem{{Type: "text", Text: "start"}}}, &appwire.TurnStartResponse{}},
		{"steer", appwire.MethodTurnSteer, "mutation-steer", appwire.TurnSteerParams{Ref: "local:th_1", ClientMutationID: "mutation-steer", ExpectedInstanceID: "sess_1", Input: []appwire.InputItem{{Type: "text", Text: "steer"}}}, &appwire.TurnSteerResponse{}},
		{"interrupt", appwire.MethodTurnInterrupt, "mutation-interrupt", appwire.TurnInterruptParams{Ref: "local:th_1", ClientMutationID: "mutation-interrupt", ExpectedInstanceID: "sess_1"}, &appwire.TurnInterruptResponse{}},
		{"queue", appwire.MethodTurnQueue, "mutation-queue", appwire.TurnQueueParams{Ref: "local:th_1", ClientMutationID: "mutation-queue", ExpectedInstanceID: "sess_1", Input: []appwire.InputItem{{Type: "text", Text: "queue"}}}, &appwire.TurnQueueResponse{}},
		{"drain", appwire.MethodTurnDrainAsSteer, "mutation-drain", appwire.TurnDrainAsSteerParams{Ref: "local:th_1", ClientMutationID: "mutation-drain", ExpectedInstanceID: "sess_1", ExpectedQueueRevision: 1}, &appwire.TurnDrainAsSteerResponse{}},
		{"promote", appwire.MethodTurnPromoteQueuedAsSteer, "mutation-promote", appwire.TurnPromoteQueuedAsSteerParams{Ref: "local:th_1", ClientMutationID: "mutation-promote", ExpectedInstanceID: "sess_1", ExpectedEntryID: "queue_1", Index: 0}, &appwire.TurnPromoteQueuedAsSteerResponse{}},
		{"cancel", appwire.MethodTurnCancelQueued, "mutation-cancel", appwire.TurnCancelQueuedParams{Ref: "local:th_1", ClientMutationID: "mutation-cancel", ExpectedInstanceID: "sess_1", ExpectedEntryID: "queue_1", Index: 0}, &appwire.TurnCancelQueuedResponse{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := client.Request(context.Background(), tc.method, tc.params, tc.result); err != nil {
				t.Fatalf("%s: %v", tc.method, err)
			}
			if called[tc.method] != 1 {
				t.Fatalf("%s daemon calls = %d, want 1", tc.method, called[tc.method])
			}
			var got appwire.MutationReceipt
			switch response := tc.result.(type) {
			case *appwire.TurnStartResponse:
				got = response.Receipt
			case *appwire.TurnSteerResponse:
				got = response.Receipt
			case *appwire.TurnInterruptResponse:
				got = response.Receipt
			case *appwire.TurnQueueResponse:
				got = response.Receipt
			case *appwire.TurnDrainAsSteerResponse:
				got = response.Receipt
			case *appwire.TurnPromoteQueuedAsSteerResponse:
				got = response.Receipt
			case *appwire.TurnCancelQueuedResponse:
				got = response.Receipt
				if response.RemovedText != "queued" || response.RemovedImages != 1 {
					t.Fatalf("cancel response = %+v", response)
				}
			default:
				t.Fatalf("unexpected response type %T", tc.result)
			}
			if got.ClientMutationID != tc.id ||
				got.Disposition != appwire.MutationDispositionReplayed ||
				got.TurnID != "turn_1" ||
				!slices.Equal(got.QueueEntryIDs, []string{"queue_1"}) ||
				got.ProjectionState != appwire.MutationProjectionReflected {
				t.Fatalf("receipt = %+v", got)
			}
		})
	}

	rejected := appwire.TurnQueueParams{
		Ref:                "local:th_1",
		ClientMutationID:   "mutation-reject",
		ExpectedInstanceID: "sess_1",
		Input:              []appwire.InputItem{{Type: "text", Text: "reject"}},
	}
	var response appwire.TurnQueueResponse
	err := client.Request(context.Background(), appwire.MethodTurnQueue, rejected, &response)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("TurnQueue rejection %T=%v, want WireError", err, err)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok ||
		data["clientMutationId"] != rejected.ClientMutationID ||
		data["mutationOutcome"] != string(appwire.MutationOutcomeNotAccepted) ||
		data["retryDisposition"] != string(appwire.RetryDispositionNone) {
		t.Fatalf("wire=%+v data=%T %#v", wire, wire.Data, wire.Data)
	}
}

func TestHubRPCTurnSteerMissingLocalSessionReturnsTerminalRejection(t *testing.T) {
	runDir := t.TempDir()
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPCInitialized(t, hub)

	params := appwire.TurnSteerParams{
		Ref:                "local:missing",
		ClientMutationID:   "mutation-missing-session",
		ExpectedInstanceID: "missing",
		Input:              []appwire.InputItem{{Type: "text", Text: "steer"}},
	}
	var response appwire.TurnSteerResponse
	err := client.Request(context.Background(), appwire.MethodTurnSteer, params, &response)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("TurnSteer error %T=%v, want WireError", err, err)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok ||
		data["evenerErrorInfo"] != string(appwire.ErrorSessionUnavailable) ||
		data["clientMutationId"] != params.ClientMutationID ||
		data["mutationOutcome"] != string(appwire.MutationOutcomeNotAccepted) ||
		data["retryDisposition"] != string(appwire.RetryDispositionNone) {
		t.Fatalf("wire=%+v data=%T %#v", wire, wire.Data, wire.Data)
	}
}

func TestHubRPCThreadCompactStartResumesPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:          params.Ref,
				Capabilities: appwire.ThreadCapabilities{Compact: true},
			},
		}}, nil
	})
	compactCalled := false
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadCompactStart, func(_ context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:"+sessionID {
			t.Fatalf("compact ref=%q", params.Ref)
		}
		compactCalled = true
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	var gotReq hubcore.ResumeRequest
	resumeCalls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			gotReq = req
			resumeCalls++
			entry := rendezvous.Entry{
				PID:        106,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if err := client.ThreadCompactStart(context.Background(), appwire.ThreadCompactStartParams{Ref: "local:" + sessionID}); err != nil {
		t.Fatalf("ThreadCompactStart: %v", err)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}
	if gotReq.SessionID != sessionID || gotReq.StateDir != stateDir || gotReq.WorkingDir != workingDir {
		t.Fatalf("resume request=%+v", gotReq)
	}
	if !compactCalled {
		t.Fatal("compact was not routed after resume")
	}
}

func TestHubRPCThreadModelSetResumesPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:          params.Ref,
				Capabilities: appwire.ThreadCapabilities{ChangeModel: true},
			},
		}}, nil
	})
	modelCalled := ""
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:"+sessionID {
			t.Fatalf("model ref=%q", params.Ref)
		}
		modelCalled = params.ModelProvider + "/" + params.Model
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	resumeCalls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != sessionID || req.StateDir != stateDir || req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			resumeCalls++
			entry := rendezvous.Entry{
				PID:        106,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if err := client.ThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		Ref:           "local:" + sessionID,
		ModelProvider: "openai",
		Model:         "gpt-5.6-sol",
	}); err != nil {
		t.Fatalf("ThreadModelSet: %v", err)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}
	if modelCalled != "openai/gpt-5.6-sol" {
		t.Fatalf("modelCalled=%q", modelCalled)
	}
}

func TestHubRPCThreadVisionModelSetResumesPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:          params.Ref,
				Capabilities: appwire.ThreadCapabilities{ChangeVisionModel: true},
			},
		}}, nil
	})
	visionModelCalled := ""
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadVisionModelSet, func(_ context.Context, params appwire.ThreadVisionModelSetParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:"+sessionID {
			t.Fatalf("vision model ref=%q", params.Ref)
		}
		visionModelCalled = params.VisionModel
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	resumeCalls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != sessionID || req.StateDir != stateDir || req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			resumeCalls++
			entry := rendezvous.Entry{
				PID:        106,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if err := client.ThreadVisionModelSet(context.Background(), appwire.ThreadVisionModelSetParams{
		Ref:         "local:" + sessionID,
		VisionModel: "off",
	}); err != nil {
		t.Fatalf("ThreadVisionModelSet: %v", err)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}
	if visionModelCalled != "off" {
		t.Fatalf("visionModelCalled=%q", visionModelCalled)
	}
}

func TestHubRPCUnsupportedThreadActionReturnsStructuredUnavailable(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	shutdownCalled := false
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("read ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener: appwire.EvenerThread{
				Ref: "local:th_1",
				Capabilities: appwire.ThreadCapabilities{
					Send:    true,
					Compact: true,
				},
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadShutdown, func(_ context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		shutdownCalled = true
		return appwire.EmptyResponse{}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 105, "th_1", "sess_1")

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	err := client.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: "local:th_1"})
	if err == nil {
		t.Fatal("ThreadShutdown succeeded for unsupported action")
	}
	if shutdownCalled {
		t.Fatal("unsupported shutdown reached source")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok || data["evenerErrorInfo"] != string(appwire.ErrorActionUnavailable) {
		t.Fatalf("wire data=%#v", wire.Data)
	}
}

// TestHubRPCGoalSetGatedByCapability pins /par A6: goal/set is pre-flight gated
// like every sibling thread action. A source whose ThreadRead reports caps without
// Goal (e.g. codex) must have goal/set rejected with a structured Unavailable
// BEFORE the call reaches the source's GoalSet.
func TestHubRPCGoalSetGatedByCapability(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	goalReached := false
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener: appwire.EvenerThread{
				Ref:          "local:th_1",
				Capabilities: appwire.ThreadCapabilities{Send: true}, // no Goal
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodGoalSet, func(_ context.Context, _ appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
		goalReached = true
		return appwire.GoalSetResponse{Started: true}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 106, "th_1", "sess_1")

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	_, err := client.GoalSet(context.Background(), appwire.GoalSetParams{Ref: "local:th_1", Objective: "do it"})
	if err == nil {
		t.Fatal("GoalSet succeeded despite missing Goal capability")
	}
	if goalReached {
		t.Fatal("gated goal/set still reached the source")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
}

// TestHubLogfForSinkIsFixedAtConstruction pins that hubLogfFor resolves its
// default sink once, when it builds the logger, rather than reading the
// os.Stderr global on every call. A live server goroutine logging through a
// per-call read races any test that redirects os.Stderr (captureHubStderr);
// the real startup path already sets the sink before any server runs (#2783).
func TestHubLogfForSinkIsFixedAtConstruction(t *testing.T) {
	first, err := os.CreateTemp(t.TempDir(), "hub-logf-first-")
	if err != nil {
		t.Fatalf("os.CreateTemp: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	original := os.Stderr
	t.Cleanup(func() { os.Stderr = original })
	os.Stderr = first
	logf := hubLogfFor(hubcore.WebConfig{})

	second, err := os.CreateTemp(t.TempDir(), "hub-logf-second-")
	if err != nil {
		t.Fatalf("os.CreateTemp: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	// Probe the race a live logging goroutine would hit: log from another
	// goroutine while the test redirects os.Stderr, the shape that filed #2783.
	started := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		close(started)
		for range 1 << 20 {
			select {
			case <-stop:
				return
			default:
			}
			logf("sink probe")
		}
	}()
	<-started
	os.Stderr = second
	close(stop)
	<-done

	// The sink resolved at construction still receives a later line; the
	// redirect now in effect does not, because the logger no longer reads the
	// global per call.
	logf("after swap")
	os.Stderr = original

	if size, err := first.Stat(); err != nil || size.Size() == 0 {
		t.Fatalf("sink set at construction captured nothing (stat err=%v)", err)
	}
	if size, err := second.Stat(); err != nil {
		t.Fatalf("stat later redirect: %v", err)
	} else if size.Size() != 0 {
		t.Fatalf("sink followed os.Stderr after construction: %d bytes went to the later redirect", size.Size())
	}
}

func TestHubRPCPathsCompleteReturnsMatchingDirectories(t *testing.T) {
	root := t.TempDir()
	alpha := filepath.Join(root, "alpha")
	if err := os.Mkdir(alpha, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alpine.txt"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range 35 {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("child-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	newProject := filepath.Join(root, "new-project")
	if err := os.Mkdir(newProject, 0o755); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := appwiretest.PathsComplete(context.Background(), client, appwire.PathsCompleteParams{Prefix: filepath.Join(root, "alph")})
	if err != nil {
		t.Fatalf("PathsComplete: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0] != alpha {
		t.Fatalf("dirs=%+v, want [%s]", resp.Data, alpha)
	}

	all, err := appwiretest.PathsComplete(context.Background(), client, appwire.PathsCompleteParams{Prefix: root + "/"})
	if err != nil {
		t.Fatalf("PathsComplete all children: %v", err)
	}
	if len(all.Data) != 38 {
		t.Fatalf("all dirs=%d, want every one of 38 children", len(all.Data))
	}

	fuzzy, err := appwiretest.PathsComplete(context.Background(), client, appwire.PathsCompleteParams{Prefix: filepath.Join(root, "nwprj")})
	if err != nil {
		t.Fatalf("PathsComplete fuzzy: %v", err)
	}
	if len(fuzzy.Data) != 1 || fuzzy.Data[0] != newProject {
		t.Fatalf("fuzzy dirs=%+v, want [%s]", fuzzy.Data, newProject)
	}
}

// TestHubRPCProjectsRecentReturnsMostRecentDirs covers the session creation
// flows' recent-project source (issue #35): evener/projects/recent serves the
// past index's distinct working dirs, most-recently-used first, defaulting to
// the 15-option cap when the request carries no limit. A managed worktree
// lane (the newest session here) must never surface or consume one of those
// slots: it is session machinery, not a project.
func TestHubRPCProjectsRecentReturnsMostRecentDirs(t *testing.T) {
	// RecentProjectDirs drops dirs that no longer exist on disk (issue #50),
	// so every seeded WorkingDir must be a real directory.
	root := t.TempDir()
	mkdir := func(name string) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
		return dir
	}
	alpha := mkdir("alpha")
	beta := mkdir("beta")
	lane := mkdir("lane") // evener-managed worktree lane

	past := hubcore.NewPastIndex("")
	now := time.Now().UTC()
	metas := []schema.SessionMeta{
		{ID: "02wMz5Txv0ManagedLane1", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: lane}, WorktreePath: lane, WorktreeManaged: true},
		{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: now.Add(-1 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: alpha}},
		{ID: "02wMz5Txv2enqVTitaig6F", UpdatedAt: now.Add(-2 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: beta}},
		{ID: "02wMz5Txv5aIxgf9yVdd0N", UpdatedAt: now.Add(-3 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: alpha}}, // older dup — dropped
	}
	for n := range 20 {
		metas = append(metas, schema.SessionMeta{
			ID:        fmt.Sprintf("02wMz5Txv1C3Hut0M8GC%02d", n),
			UpdatedAt: now.Add(-time.Duration(n+4) * time.Minute),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: mkdir(fmt.Sprintf("proj-%02d", n))},
		})
	}
	past.SeedForTest(metas)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := appwiretest.ProjectsRecent(context.Background(), client, appwire.ProjectsRecentParams{})
	if err != nil {
		t.Fatalf("ProjectsRecent: %v", err)
	}
	if len(resp.Data) != 15 {
		t.Fatalf("recent dirs=%d, want the default 15-option cap", len(resp.Data))
	}
	if resp.Data[0] != alpha || resp.Data[1] != beta {
		t.Fatalf("recent dirs[0:2]=%v, want [%s %s] (most recently used first)", resp.Data[:2], alpha, beta)
	}
	if slices.Contains(resp.Data, lane) {
		t.Fatalf("recent dirs contain managed worktree lane %q", lane)
	}

	limited, err := appwiretest.ProjectsRecent(context.Background(), client, appwire.ProjectsRecentParams{Limit: 2})
	if err != nil {
		t.Fatalf("ProjectsRecent limit=2: %v", err)
	}
	if len(limited.Data) != 2 || limited.Data[0] != alpha || limited.Data[1] != beta {
		t.Fatalf("recent dirs limit=2 = %v, want [%s %s]", limited.Data, alpha, beta)
	}
}

// TestHubRPCProjectsRecentEmptyMarshalsAsEmptyArray pins the WIRE shape of a
// hub with no remembered projects. A nil Data slice marshals as JSON `null`,
// but the wire type declares `data: string[]` (non-nullable in the generated
// TypeScript), so a null reaches the browser as a value the type system
// promised was impossible and the first `.length` on it crashes the pane.
func TestHubRPCProjectsRecentEmptyMarshalsAsEmptyArray(t *testing.T) {
	for name, cfg := range map[string]hubcore.WebConfig{
		"no past index": {},
		"empty past":    {Past: hubcore.NewPastIndex("")},
	} {
		t.Run(name, func(t *testing.T) {
			server := newHubAppServer(cfg, appsource.NewRegistry())
			raw, err := server.Router().Dispatch(context.Background(), appwire.Request{
				ID:     appwire.NewIntID(1),
				Method: appwire.MethodEvenerProjectsRecent,
				Params: json.RawMessage(`{}`),
			})
			if err != nil {
				t.Fatalf("Dispatch projects/recent: %v", err)
			}
			encoded, err := json.Marshal(raw)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(encoded) != `{"data":[]}` {
				t.Fatalf("marshalled empty recents = %s, want {\"data\":[]}", encoded)
			}
		})
	}
}

// TestHubRPCTestServerGivesEachTestItsOwnRoots pins the fixture's isolation
// contract: a caller that leaves HubStateRoot, LaunchConfigRoot or PluginRoot
// unset gets a directory of its own, not the package-wide throwaway root every
// test in this binary shares. Without it two parallel tests both writing
// launch.toml, credentials.toml or the plugin store read each other's writes.
func TestHubRPCTestServerGivesEachTestItsOwnRoots(t *testing.T) {
	t.Parallel()
	first, firstWeb := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{})
	defer first.Close()
	second, secondWeb := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{})
	defer second.Close()

	for _, root := range []struct {
		name   string
		got    func(hubcore.WebConfig) string
		shared string
	}{
		{"HubStateRoot", func(c hubcore.WebConfig) string { return c.HubStateRoot }, mustStateRoot(t)},
		{"LaunchConfigRoot", func(c hubcore.WebConfig) string { return c.LaunchConfigRoot }, cmdutil.DefaultConfigRoot()},
		{"PluginRoot", func(c hubcore.WebConfig) string { return c.PluginRoot }, plugins.DefaultRoot()},
	} {
		a, b := root.got(firstWeb.cfg), root.got(secondWeb.cfg)
		if a == "" || b == "" {
			t.Errorf("%s left empty (%q, %q); it falls back to the shared package root", root.name, a, b)
			continue
		}
		if a == b {
			t.Errorf("%s is %q for both servers; the fixture is not isolating them", root.name, a)
		}
		for _, got := range []string{a, b} {
			if got == root.shared {
				t.Errorf("%s resolved to the package-wide default %q", root.name, root.shared)
			}
		}
	}
}

// TestHubRPCTestServerGivesEachTestItsOwnCredentials pins the credential half
// of the same contract. newHubAuthControllerWithStore ignores its state-root
// argument, so a nil CredsStore lands on the process-wide XDG-derived
// credentials.toml: two parallel tests calling evener/auth/apiKey/set read
// each other's keys, and one calling apiKey/clear wipes the other's.
func TestHubRPCTestServerGivesEachTestItsOwnCredentials(t *testing.T) {
	t.Parallel()
	first, firstWeb := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{})
	defer first.Close()
	second, secondWeb := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{})
	defer second.Close()

	shared := cmdutil.CredentialsPath()
	var paths []string
	for i, cfg := range []hubcore.WebConfig{firstWeb.cfg, secondWeb.cfg} {
		if cfg.CredsStore == nil {
			t.Errorf("server %d has a nil CredsStore; the auth controller falls back to the process-wide store", i)
			continue
		}
		paths = append(paths, cfg.CredsStore.Path())
		if cfg.CredsStore.Path() == shared {
			t.Errorf("server %d keeps credentials at the process-wide default %q", i, shared)
		}
		if cfg.CredentialsPath != cfg.CredsStore.Path() {
			t.Errorf("server %d has CredentialsPath %q but a store at %q; a spawned child resolves keys from a different file than the hub writes",
				i, cfg.CredentialsPath, cfg.CredsStore.Path())
		}
	}
	if len(paths) == 2 && paths[0] == paths[1] {
		t.Errorf("both servers keep credentials at %q; the fixture is not isolating them", paths[0])
	}
}

// TestHubRPCTestServerGivesEachTestItsOwnOAuthState pins that a fixture
// server's auth controller and registry share that server's own state root:
// an OAuth record under one server's root signs that server in and is
// invisible to another, so parallel tests never read or overwrite each
// other's auth/<instance>.json.
func TestHubRPCTestServerGivesEachTestItsOwnOAuthState(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	first, firstWeb := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer first.Close()
	second, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer second.Close()

	if err := authopenai.SaveAuth(firstWeb.cfg.HubStateRoot, "openai-codex", authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Hour),
		TokenType:    "Bearer",
		Scope:        "openid profile email",
		AccessToken:  "stored-access-token",
		RefreshToken: "stored-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
		Email:        "stored@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	codexStatus := func(name string, srv *httptest.Server) appwire.AuthStatusResponse {
		client := dialHubRPC(t, srv)
		defer client.Close()
		if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatalf("%s Initialize: %v", name, err)
		}
		status, err := appwiretest.AuthStatus(context.Background(), client, appwire.AuthStatusParams{Provider: "openai-codex"})
		if err != nil {
			t.Fatalf("%s AuthStatus: %v", name, err)
		}
		return status
	}
	if got := codexStatus("first", first); !got.SignedIn || got.ActiveSource != authopenai.AuthSourceOAuth {
		t.Errorf("first status=%+v, want signed in from the record under its own state root %s", got, firstWeb.cfg.HubStateRoot)
	}
	if got := codexStatus("second", second); got.SignedIn || got.HasStoredOAuth {
		t.Errorf("second status=%+v, want signed out: the first server's record must not be visible to it", got)
	}
}

// TestHubRPCTestServerRegistryReadsItsOwnCredentials pins the other half of
// credential isolation: the store the auth handlers write has to be the store
// the registry resolves from. A default registry loaded through
// cmdutil.LoadRegistry reads the process-wide credentials.toml instead, so a
// key set through evener/auth/apiKey/set lands in the server's own store while
// the reload behind that same call resolves from the shared file: the caller
// is told "none" for the key it just stored, and a parallel test's registry
// answers from whatever the shared file happens to hold.
func TestHubRPCTestServerRegistryReadsItsOwnCredentials(t *testing.T) {
	t.Parallel()
	first := newHubRPCTestServer(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer first.Close()
	second := newHubRPCTestServer(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer second.Close()

	firstClient := dialHubRPC(t, first)
	defer firstClient.Close()
	secondClient := dialHubRPC(t, second)
	defer secondClient.Close()
	for _, client := range []*appwire.Client{firstClient, secondClient} {
		if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
	}

	var stored appwire.AuthStatusResponse
	if err := firstClient.Request(context.Background(), appwire.MethodEvenerAuthApiKeySet,
		appwire.AuthApiKeySetParams{Provider: "anthropic", Value: "sk-first-server"}, &stored); err != nil {
		t.Fatalf("evener/auth/apiKey/set: %v", err)
	}
	if stored.ActiveSource != "store" {
		t.Fatalf("first server resolved %q right after storing a key; its registry is not reading the store its handlers write", stored.ActiveSource)
	}

	var other appwire.AuthStatusResponse
	if err := secondClient.Request(context.Background(), appwire.MethodEvenerAuthStatus,
		appwire.AuthStatusParams{Provider: "anthropic"}, &other); err != nil {
		t.Fatalf("evener/auth/status: %v", err)
	}
	if other.ActiveSource == "store" {
		t.Fatalf("second server resolved a stored key for anthropic; it is reading the first server's credentials")
	}
}

// TestHubRPCTestServerRegistryIgnoresTheProcessCredentialsFile pins the last
// piece of shared state out of the fixture's default registry: loading it must
// not read the process-wide credentials.toml at all. cmdutil.LoadRegistry
// loads that file before any caller option applies, so a file that is
// malformed, has group- or world-readable permissions, or is rewritten by
// another test failed every fixture server, whatever store the test handed it.
func TestHubRPCTestServerRegistryIgnoresTheProcessCredentialsFile(t *testing.T) {
	// t.Setenv, hence no t.Parallel: the process credentials path is
	// process-wide state, and the testing package resumes parallel tests only
	// once the sequential ones have finished.
	broken := filepath.Join(t.TempDir(), "credentials.toml")
	if err := os.WriteFile(broken, []byte("[[[not credentials\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", broken, err)
	}
	t.Setenv(envvars.EVENERCredentialsConfig.Name, broken)
	if _, err := credentials.LoadStore(cmdutil.CredentialsPath()); err == nil {
		t.Fatalf("LoadStore(%s) succeeded; this test needs a process credentials file that cannot be loaded", cmdutil.CredentialsPath())
	}

	srv, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer srv.Close()
	if err := web.cfg.Registry.Reload(); err != nil {
		t.Fatalf("registry reload: %v; the fixture registry is still reading the process credentials file", err)
	}

	client := dialHubRPCInitialized(t, srv)
	var status appwire.AuthStatusResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerAuthStatus,
		appwire.AuthStatusParams{Provider: "anthropic"}, &status); err != nil {
		t.Fatalf("evener/auth/status: %v", err)
	}
	if status.ActiveSource == "store" {
		t.Fatalf("anthropic resolved a stored key; the registry is reading credentials this test never wrote")
	}
}

// TestHubRPCRegistersExpectedHandlerSet locks in the exact set of RPC methods
// the hub app server registers (with a providers config present so the
// instance handlers register too): the router's method set must equal the
// list below, so both a dropped registration and one nobody has named here
// fail. That guards the constructor decomposition (registerThreadHandlers /
// Auth / Instance / Launch / Plugin / Misc / PluginAutoUpgrade) in both
// directions.
//
// Two dispatches over the wire tie that set to what /rpc actually serves: a
// registered read-only method (model/list, answered by a LiveModels stub so
// it never asks a live provider) must not answer methodNotFound, and an
// unregistered name must. Dispatching every named method instead adds no
// reachability — /rpc serves the same appRPC whose router the set check
// inspects — while running for real any handler for which empty params are a
// valid write.
func TestHubRPCRegistersExpectedHandlerSet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, "[providers.my-openai]\nbase = \"openai\"\napi_key = \"sk-inline\"\n")
	// One store for the registry and the auth controller both: a nil CredsStore
	// makes newHubAuthControllerWithStore fall back to the on-disk default
	// store under the ambient HOME/XDG env, which the whole package shares.
	credsStore := newTestCredentialsStore(t)
	var liveModelsCalled atomic.Bool
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, credsStore, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
		// model/list is the one registered method dispatched below. With
		// LiveModels unset, NewWebServer falls back to fetchLiveModels, which
		// loads the default client and asks every discovered provider,
		// including the implicit Ollama endpoint. The stub keeps the probe
		// offline and records that the dispatch reached it.
		LiveModels: func(context.Context) []appwire.ModelDescriptor {
			liveModelsCalled.Store(true)
			return nil
		},
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	expected := []string{
		appwire.MethodThreadList,
		appwire.MethodThreadRead,
		appwire.MethodEvenerSessionImage,
		appwire.MethodEvenerSessionDocument,
		appwire.MethodThreadUnsubscribe,
		appwire.MethodThreadTurnsList,
		appwire.MethodEvenerSubagentPreview,
		appwire.MethodThreadStart,
		appwire.MethodThreadResume,
		appwire.MethodThreadFork,
		appwire.MethodTurnStart,
		appwire.MethodTurnSteer,
		appwire.MethodTurnInterrupt,
		appwire.MethodEvenerSandboxEscalationResolve,
		appwire.MethodEvenerDelegateStop,
		appwire.MethodTurnQueue,
		appwire.MethodTurnDrainAsSteer,
		appwire.MethodTurnPromoteQueuedAsSteer,
		appwire.MethodTurnCancelQueued,
		appwire.MethodThreadClear,
		appwire.MethodThreadCompactStart,
		appwire.MethodThreadShutdown,
		appwire.MethodEvenerThreadForceStop,
		appwire.MethodThreadModelSet,
		appwire.MethodThreadVisionModelSet,
		appwire.MethodEvenerThreadNameSet,
		appwire.MethodThreadReasoningEffortSet,
		appwire.MethodGoalSet,
		appwire.MethodNotesHumanSet,
		appwire.MethodUrlsRemove,
		appwire.MethodEvenerAuthStatus,
		appwire.MethodEvenerAuthTest,
		appwire.MethodEvenerAuthLoginStart,
		appwire.MethodEvenerAuthLoginComplete,
		appwire.MethodEvenerAuthLogout,
		appwire.MethodEvenerAuthList,
		appwire.MethodEvenerAuthApiKeySet,
		appwire.MethodEvenerAuthApiKeyConditionalSet,
		appwire.MethodEvenerAuthApiKeyClear,
		appwire.MethodEvenerAuthCredentialJsonSet,
		appwire.MethodEvenerAuthDeviceStart,
		appwire.MethodEvenerAuthDevicePoll,
		appwire.MethodEvenerNavigationRead,
		appwire.MethodEvenerFavoriteSet,
		appwire.MethodEvenerArchiveSet,
		appwire.MethodEvenerProjectDelete,
		appwire.MethodEvenerSessionDelete,
		appwire.MethodEvenerPinSectionRename,
		appwire.MethodEvenerPinSectionDelete,
		appwire.MethodEvenerSessionPinAssign,
		appwire.MethodEvenerSessionPinUnpin,
		appwire.MethodEvenerSessionSeenSet,
		appwire.MethodEvenerSearch,
		appwire.MethodEvenerArchivedList,
		appwire.MethodEvenerActivityRead,
		appwire.MethodEvenerNoticesList,
		appwire.MethodEvenerInstanceList,
		appwire.MethodEvenerInstanceCreate,
		appwire.MethodEvenerInstanceEdit,
		appwire.MethodEvenerInstanceRemove,
		appwire.MethodEvenerInstanceSetDefault,
		appwire.MethodEvenerInstanceSetModelDisabled,
		appwire.MethodEvenerInstanceRefreshModels,
		appwire.MethodEvenerLaunchResolve,
		appwire.MethodEvenerLaunchSchema,
		appwire.MethodEvenerLaunchGetLayer,
		appwire.MethodEvenerLaunchSetLayer,
		appwire.MethodEvenerLaunchTrustRepo,
		appwire.MethodEvenerUpgrade,
		appwire.MethodEvenerUpdateCheck,
		appwire.MethodEvenerUpdateApply,
		appwire.MethodEvenerHubRestart,
		appwire.MethodModelList,
		appwire.MethodEvenerTasksList,
		appwire.MethodEvenerJobsList,
		appwire.MethodEvenerJobsOutput,
		appwire.MethodEvenerJobsGet,
		appwire.MethodEvenerThreadActivityRead,
		appwire.MethodEvenerThreadDelegatesList,
		appwire.MethodEvenerThreadJobsList,
		appwire.MethodEvenerThreadTranscriptsList,
		appwire.MethodEvenerThreadWatchesList,
		appwire.MethodEvenerPathsComplete,
		appwire.MethodEvenerDirsCreate,
		appwire.MethodEvenerProjectsRecent,
		appwire.MethodEvenerPathValidate,
		appwire.MethodEvenerGitHead,
		appwire.MethodEvenerMobilePairing,
		appwire.MethodEvenerDevicePairingCreate,
		appwire.MethodEvenerDevicePairingRead,
		appwire.MethodEvenerDevicePairingApprove,
		appwire.MethodEvenerDevicePairingCancel,

		appwire.MethodEvenerHarnessesList,
		appwire.MethodEvenerCommandList,
		appwire.MethodEvenerSpawnSlashCatalog,
		appwire.MethodEvenerSettingsOverview,
		appwire.MethodEvenerSettingsTranscriptDisplayGet,
		appwire.MethodEvenerSettingsTranscriptDisplayPatch,
		appwire.MethodEvenerSettingsKeybindingsGet,
		appwire.MethodEvenerSettingsKeybindingsPatch,
		appwire.MethodEvenerSettingsAgentsDocGet,
		appwire.MethodEvenerSettingsAgentsDocSet,
		appwire.MethodEvenerMarketplaceList,
		appwire.MethodEvenerMarketplaceAdd,
		appwire.MethodEvenerMarketplaceRemove,
		appwire.MethodEvenerMarketplaceRefresh,
		appwire.MethodEvenerMarketplaceEdit,
		appwire.MethodEvenerMarketplaceBrowse,
		appwire.MethodEvenerPluginList,
		appwire.MethodEvenerPluginInstall,
		appwire.MethodEvenerPluginUpgrade,
		appwire.MethodEvenerPluginRemove,
		appwire.MethodEvenerPluginEnable,
		appwire.MethodEvenerPluginDisable,
		appwire.MethodEvenerPluginSetAutoUpgrade,
		appwire.MethodEvenerPluginCheckUpdates,
		appwire.MethodEvenerPluginCheckNow,
		appwire.MethodEvenerPluginPreview,
		// Component 07a's remote-admin proxy.
		appwire.MethodEvenerHostRequest,
		// Component 06's explicit attach trigger (component 08's Connect).
		appwire.MethodEvenerHostAttach,
		// Component 08's host registry surface (add/list/status/remove/update):
		// controller-local, never dials.
		appwire.MethodEvenerHostAdd,
		appwire.MethodEvenerHostList,
		appwire.MethodEvenerHostStatus,
		appwire.MethodEvenerHostRemove,
		appwire.MethodEvenerHostUpdate,
		// The deploy pipeline's planning read (08b): a mutation — it mints and
		// persists the confirmation token — controller-local like the rest of
		// the host surface, so a peer hub cannot plan against this hub's hosts.
		appwire.MethodEvenerHostPlan,
		// The deploy pipeline's operation starts (08b §6): controller-local
		// mutations, refused to a remote origin exactly like plan.
		appwire.MethodEvenerHostDeploy,
		appwire.MethodEvenerHostRestart,
		// The deploy pipeline's operations read (08b §8/§10): controller-local
		// like the rest of the host surface — it pages this hub's own operation
		// store — so it is never forwarded to a peer hub.
		appwire.MethodEvenerHostOperations,
		// The deploy pipeline's running probe (08b §10): served by every hub, and
		// the one host method that admits the attached controller session
		// instead of refusing it — a browser-origin or forwarded request is
		// still refused by the handler itself.
		appwire.MethodEvenerHostRunning,
		// Component 07c's credential push: controller-local like the proxy, so a
		// peer hub cannot make this hub push its credentials by forwarding it.
		appwire.MethodEvenerHostPushCredentials,
		// Slice 12's teardown-repair mutations: controller-local like the rest
		// of the registry surface — they act on this hub's own hub.toml records
		// and channels — so a peer hub must not be able to resume or clear this
		// hub's remnants by forwarding them.
		appwire.MethodEvenerHostTeardownRetry,
		appwire.MethodEvenerHostTeardownRecover,
		appwire.MethodEvenerDaemonList,
		appwire.MethodEvenerDaemonRetire,
	}

	// The list is a lock, not a sample: nothing may be registered that it does
	// not name, so a handler arriving without a test naming it fails here too.
	registered := excludeHubMethods(web.appRPC.Router().Methods(), appwire.ConnectionMethodNames())
	if missing, extra := setDiff(expected, registered); len(missing) > 0 || len(extra) > 0 {
		t.Errorf("hub handler set differs from the set this test names:\n  named but NOT registered: %v\n  registered but NOT named: %v", missing, extra)
	}

	// Two dispatches tie the set above to what /rpc actually serves: /rpc
	// serves this same appRPC, so landing on a registered method here is what
	// shows the wire path reaches the router the set check inspected. model/list
	// is the read-only pick — it answers from the offline registry this test
	// configured and writes nothing. We only care about the dispatch outcome,
	// not the response body, so pass a nil out: the handler may succeed or
	// reject the empty params with some other error; what it must never return
	// is methodNotFound.
	registeredErr := client.Request(context.Background(), appwire.MethodModelList, appwire.EmptyParams{}, nil)
	var registeredWire appwire.WireError
	if errors.As(registeredErr, &registeredWire) && registeredWire.Code == appwire.CodeMethodNotFound {
		t.Errorf("method %q is not registered (methodNotFound)", appwire.MethodModelList)
	}
	if !liveModelsCalled.Load() {
		t.Errorf("model/list did not reach the LiveModels stub; the probe is no longer offline")
	}

	// Sanity check: an unregistered method must report methodNotFound, proving
	// the assertion above is meaningful.
	unknownErr := client.Request(context.Background(), "evener/__definitely_not_registered__", appwire.EmptyParams{}, nil)
	var unknownWire appwire.WireError
	if !errors.As(unknownErr, &unknownWire) || unknownWire.Code != appwire.CodeMethodNotFound {
		t.Fatalf("expected methodNotFound for unknown method, got %T: %v", unknownErr, unknownErr)
	}
}

func TestHubRPCSavedDelegateReadDoesNotClaimMutationAuthority(t *testing.T) {
	for _, status := range []string{appwire.ThreadStatusActive, appwire.ThreadStatusIdle} {
		t.Run(status, func(t *testing.T) {
			cfg, childID := runningSubagentProjectionConfigWithState(t, status)
			entries := cfg.Roster.List()
			entries[0].Protocol = appwire.ProtocolVersion
			entries[0].Endpoint = "ws://unused.invalid/appwire"
			cfg.Roster = hubcore.NewRosterWithEntries(entries...)
			sources := appsource.NewRegistry()
			sources.Add(&pastFallbackRelaySource{
				thread:  appwire.Thread{ID: childID, Evener: appwire.EvenerThread{Ref: "local:" + childID}},
				readErr: errors.New("delegate transport failed"),
			})
			app := newHubAppServer(cfg, sources)
			hub := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			response, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + childID})
			if err != nil {
				t.Fatal(err)
			}
			if response.Thread.Status.Type != status || response.Thread.Evener.MutationStateAuthoritative {
				t.Fatalf("saved delegate status=%q mutation authority=%v", response.Thread.Status.Type, response.Thread.Evener.MutationStateAuthoritative)
			}
		})
	}
}

// TestHubRPCNotesHumanSetGatedByCapability pins the shared-notes pre-flight
// gate (goal/set's TestHubRPCGoalSetGatedByCapability precedent): a source
// whose ThreadRead reports caps without SharedNotes must have notes/human/set
// rejected with a structured Unavailable BEFORE the call reaches the source.
func TestHubRPCNotesHumanSetGatedByCapability(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	notesReached := false
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener: appwire.EvenerThread{
				Ref:          "local:th_1",
				Capabilities: appwire.ThreadCapabilities{Send: true}, // no SharedNotes
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodNotesHumanSet, func(_ context.Context, _ appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
		notesReached = true
		return appwire.NotesHumanSetResponse{Note: "x"}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 106, "th_1", "sess_1")

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	_, err := client.NotesHumanSet(context.Background(), appwire.NotesHumanSetParams{Ref: "local:th_1", ClientMutationID: "m1", ExpectedInstanceID: "sess_1", Note: "hi"})
	if err == nil {
		t.Fatal("NotesHumanSet succeeded despite missing SharedNotes capability")
	}
	if notesReached {
		t.Fatal("gated notes/human/set still reached the source")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
}

// TestHubRPCUrlsRemoveGatedByCapability pins the same pre-flight gate for
// urls/remove.
func TestHubRPCUrlsRemoveGatedByCapability(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	removeReached := false
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener: appwire.EvenerThread{
				Ref:          "local:th_1",
				Capabilities: appwire.ThreadCapabilities{Send: true}, // no SharedNotes
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodUrlsRemove, func(_ context.Context, _ appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
		removeReached = true
		return appwire.UrlsRemoveResponse{}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 106, "th_1", "sess_1")

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	_, err := client.UrlsRemove(context.Background(), appwire.UrlsRemoveParams{Ref: "local:th_1", ClientMutationID: "m1", ExpectedInstanceID: "sess_1", ID: "u1"})
	if err == nil {
		t.Fatal("UrlsRemove succeeded despite missing SharedNotes capability")
	}
	if removeReached {
		t.Fatal("gated urls/remove still reached the source")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
}
