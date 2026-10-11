package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

func TestJobsOutputPageLocalAndRemoteWire(t *testing.T) {
	cfg, sessionID, stateDir := seedPastSessionWithJobs(t, []persistedJobFixture{{id: "job_page", output: "abécd"}})
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{})
	daemon.SetJobOutputFunc(func(jobID string, before *int64, maxBytes int64) (appwire.JobOutputPage, bool, error) {
		return agent.LoadSessionJobOutputPage(stateDir, sessionID, jobID, before, maxBytes)
	})
	upstream := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	t.Cleanup(upstream.Close)
	sources := appsource.NewRegistry()
	sources.Add(appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{
			SourceID: "local", SessionID: sessionID, ThreadID: sessionID,
			Endpoint: upstream.URL, Protocol: appwire.ProtocolVersion,
		}}}
	}, upstream.Client()))
	remote, calls, _ := newPushableScriptedRemoteHub(t, func(method string, raw json.RawMessage) any {
		if method == appwire.MethodInitialize {
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		}
		if method != appwire.MethodEvenerJobsOutput {
			return appwire.EmptyResponse{}
		}
		var params appwire.JobsOutputParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return appwire.InvalidParams(err.Error())
		}
		if params.BeforeBytes != nil {
			if *params.BeforeBytes == 0 {
				return appwire.JobsOutputResponse{Data: appwire.JobOutputPage{TotalBytes: 6, Encoding: "utf8"}}
			}
			return appwire.JobOutputPruned(2, 6)
		}
		return appwire.JobsOutputResponse{Data: appwire.JobOutputPage{
			OffsetBytes: 3, BytesReturned: 3, TotalBytes: 6, RetainedStartBytes: 2, Encoding: "base64", Data: "qWNk",
		}}
	})
	sources.Add(appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return remote, nil }))
	hub := newHubAppServer(cfg, sources)
	wire := httptest.NewServer(http.HandlerFunc(hub.ServeWebSocket))
	t.Cleanup(wire.Close)
	client := dialHubRPC(t, wire)
	t.Cleanup(func() { _ = client.Close() })
	ctx := t.Context()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"local", "remote"} {
		ref := owner + ":" + sessionID
		page, err := client.JobOutput(ctx, appwire.JobsOutputParams{Ref: ref, JobID: "job_page", MaxBytes: 3})
		floor := int64(0)
		if owner == "remote" {
			floor = 2
		}
		want := appwire.JobOutputPage{OffsetBytes: 3, BytesReturned: 3, TotalBytes: 6, RetainedStartBytes: floor, Encoding: "base64", Data: "qWNk"}
		if err != nil || page.Data != want {
			t.Fatalf("%s latest = %+v, %v, want %+v", owner, page, err, want)
		}
		zero := int64(0)
		page, err = client.JobOutput(ctx, appwire.JobsOutputParams{Ref: ref, JobID: "job_page", BeforeBytes: &zero, MaxBytes: 3})
		if err != nil || page.Data != (appwire.JobOutputPage{TotalBytes: 6, Encoding: "utf8"}) {
			t.Fatalf("%s explicit zero = %+v, %v", owner, page, err)
		}
	}
	before := int64(1)
	_, err := client.JobOutput(ctx, appwire.JobsOutputParams{Ref: "remote:" + sessionID, JobID: "job_page", BeforeBytes: &before, MaxBytes: 3})
	var pruning appwire.WireError
	if !errors.As(err, &pruning) || pruning.Code != appwire.CodeUnavailable {
		t.Fatalf("remote pruning = %v", err)
	}
	data, err := json.Marshal(pruning.Data)
	if err != nil || string(data) != `{"evenerErrorInfo":"jobOutputPruned","retainedStartBytes":2,"totalBytes":6}` {
		t.Fatalf("remote pruning data = %s, %v", data, err)
	}
	forwarded := scriptedRemoteHubParams[appwire.JobsOutputParams](t, calls(), appwire.MethodEvenerJobsOutput)
	if len(forwarded) != 3 || forwarded[0].BeforeBytes != nil || forwarded[1].BeforeBytes == nil || *forwarded[1].BeforeBytes != 0 || forwarded[2].BeforeBytes == nil || *forwarded[2].BeforeBytes != 1 {
		t.Fatalf("forwarded selectors = %+v", forwarded)
	}
	for _, params := range forwarded {
		if params.Ref != "local:"+sessionID || params.JobID != "job_page" || params.MaxBytes != 3 {
			t.Fatalf("forwarded owner and limit = %+v", params)
		}
	}
}

func TestJobsOutputPageProtocolV6OwnerPreserved(t *testing.T) {
	worker := exec.Command("cat")
	input, err := worker.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if err := worker.Wait(); err != nil {
			t.Errorf("old owner process was interrupted: %v", err)
		}
	})
	var oldRequests atomic.Int32
	oldPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		oldRequests.Add(1)
		http.Error(w, "old owner must not receive current output requests", http.StatusBadRequest)
	}))
	t.Cleanup(oldPeer.Close)
	oldID := identifier.MustNewSessionID()
	cfg, healthyID, stateDir := seedPastSessionWithJobs(t, []persistedJobFixture{{id: "job_page", output: "healthy"}})
	var healthyReads atomic.Int32
	healthyDaemon := daemonserver.NewServer(daemonserver.ServerConfig{})
	healthyDaemon.SetJobOutputFunc(func(jobID string, before *int64, maxBytes int64) (appwire.JobOutputPage, bool, error) {
		healthyReads.Add(1)
		return agent.LoadSessionJobOutputPage(stateDir, healthyID, jobID, before, maxBytes)
	})
	healthyPeer := httptest.NewServer(http.HandlerFunc(healthyDaemon.AppServer().ServeWebSocket))
	t.Cleanup(healthyPeer.Close)
	local := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{
			{Entry: rendezvous.Entry{
				PID: worker.Process.Pid, SourceID: "local", SessionID: oldID, ThreadID: oldID,
				Endpoint: oldPeer.URL, Protocol: "evener-appwire-v6",
			}, Status: appwire.ThreadStatusRestartRequired},
			{Entry: rendezvous.Entry{
				SourceID: "local", SessionID: healthyID, ThreadID: healthyID,
				Endpoint: healthyPeer.URL, Protocol: appwire.ProtocolVersion,
			}},
		}
	}, oldPeer.Client())
	sources := appsource.NewRegistry()
	sources.Add(local)
	hub := newHubAppServer(cfg, sources)
	wire := httptest.NewServer(http.HandlerFunc(hub.ServeWebSocket))
	t.Cleanup(wire.Close)
	client := dialHubRPC(t, wire)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	_, err = client.JobOutput(t.Context(), appwire.JobsOutputParams{Ref: "local:" + oldID, JobID: "job_page"})
	if err == nil || oldRequests.Load() != 0 {
		t.Fatalf("old owner output = %v, requests %d, want refused before dispatch", err, oldRequests.Load())
	}
	if err := worker.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("old owner is no longer running: %v", err)
	}
	page, err := client.JobOutput(t.Context(), appwire.JobsOutputParams{Ref: "local:" + healthyID, JobID: "job_page"})
	if err != nil || page.Data != (appwire.JobOutputPage{BytesReturned: 7, TotalBytes: 7, Encoding: "utf8", Data: "healthy"}) {
		t.Fatalf("independent live v7 owner = %+v, %v", page, err)
	}
	if healthyReads.Load() != 1 {
		t.Fatalf("compatible owner received %d output reads, want one live dispatch", healthyReads.Load())
	}
}
