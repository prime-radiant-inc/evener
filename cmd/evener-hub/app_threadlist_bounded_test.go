package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

type barrierThreadListSource struct {
	*scriptedAppSource
	started chan<- struct{}
	release <-chan struct{}
}

func (s *barrierThreadListSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	s.started <- struct{}{}
	<-s.release
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

type cancelableThreadListSource struct{ *scriptedAppSource }

func (s *cancelableThreadListSource) ListThreads(ctx context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	<-ctx.Done()
	return appwire.ThreadListResponse{}, ctx.Err()
}

func TestHubThreadListQueriesSourcesInParallelAndPreservesOrder(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	sources := appsource.NewRegistry()
	sources.Add(&barrierThreadListSource{scriptedAppSource: &scriptedAppSource{id: "first", thread: appwire.Thread{ID: "first-thread", Source: "first"}}, started: started, release: release})
	sources.Add(&barrierThreadListSource{scriptedAppSource: &scriptedAppSource{id: "second", thread: appwire.Thread{ID: "second-thread", Source: "second"}}, started: started, release: release})

	result := make(chan appwire.ThreadListResponse, 1)
	errors := make(chan error, 1)
	go func() {
		response, err := hubThreadListWithSourceTimeout(context.Background(), hubcore.WebConfig{}, sources, appwire.ThreadListParams{}, time.Hour)
		result <- response
		errors <- err
	}()
	for range 2 {
		<-started
	}
	close(release)
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	response := <-result
	if len(response.Data) != 2 || response.Data[0].ID != "first-thread" || response.Data[1].ID != "second-thread" {
		t.Fatalf("threads=%+v, want source order", response.Data)
	}
}

func TestHubThreadListCancellationReleasesSourceWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sources := appsource.NewRegistry()
	sources.Add(&cancelableThreadListSource{scriptedAppSource: &scriptedAppSource{id: "blocked"}})
	result := make(chan error, 1)
	go func() {
		_, err := hubThreadListWithSourceTimeout(ctx, hubcore.WebConfig{}, sources, appwire.ThreadListParams{}, time.Hour)
		result <- err
	}()
	cancel()
	if err := <-result; err != context.Canceled {
		t.Fatalf("hubThreadList error=%v, want context cancellation", err)
	}
}

func TestPastEntryThreadForListUsesMetadataOnly(t *testing.T) {
	entry := hubcore.PastEntry{StateDir: "/path-that-must-not-be-opened", Meta: schema.SessionMeta{ID: "session-1", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/project"}}}
	thread := pastEntryThreadForList(hubcore.WebConfig{}, entry)
	if thread.ID != entry.Meta.ID || thread.CWD != entry.Meta.EnvInfo.WorkingDir {
		t.Fatalf("metadata projection=%+v, want session metadata", thread)
	}
}
