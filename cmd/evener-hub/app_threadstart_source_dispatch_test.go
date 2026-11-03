package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

type captureStartSource struct {
	relayBroadcastSource
	started []appwire.ThreadStartParams
}

func (s *captureStartSource) StartThread(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	s.started = append(s.started, params)
	return appwire.ThreadStartResponse{Thread: s.thread}, nil
}

// A create that names a host dispatches through that source's own StartThread
// with the launch parameters intact; the local default never resolves a remote
// source, so it reaches the local spawner path instead.
func TestHubThreadStartDispatchesANamedSourceCreate(t *testing.T) {
	t.Parallel()
	base := relayBroadcastSource{
		id:     "host-b",
		thread: appwire.Thread{ID: "created", Source: "host-b", Evener: appwire.EvenerThread{Ref: "host-b:created", InstanceID: "created-instance"}},
	}
	source := &captureStartSource{relayBroadcastSource: base}
	sources := appsource.NewRegistry()
	sources.Add(source)
	response, err := hubThreadStart(context.Background(), hubcore.WebConfig{}, sources, appwire.ThreadStartParams{
		Harness: "evener", Source: "host-b", CWD: "/remote/work", Model: "openai/gpt-5.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(source.started) != 1 || source.started[0].CWD != "/remote/work" || source.started[0].Model != "openai/gpt-5.2" {
		t.Fatalf("remote dispatch params = %+v", source.started)
	}
	if response.Thread.Evener.Ref != "host-b:created" {
		t.Fatalf("created ref = %q, want the source's own identity", response.Thread.Evener.Ref)
	}

	// The local default (an absent source) never reaches the remote source: it
	// stops at the local spawner, which this config does not have.
	_, err = hubThreadStart(context.Background(), hubcore.WebConfig{}, sources, appwire.ThreadStartParams{
		Harness: "evener", CWD: "/local/work", Model: "openai/gpt-5.2",
	})
	if !errors.Is(err, errSpawnerNotConfigured) {
		t.Fatalf("local default reached the remote source: err=%v calls=%d", err, len(source.started))
	}
}
