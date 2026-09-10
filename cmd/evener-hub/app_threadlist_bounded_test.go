package hub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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
	current *int32
	maximum *int32
}

func (s *barrierThreadListSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	if s.current != nil {
		current := atomic.AddInt32(s.current, 1)
		defer atomic.AddInt32(s.current, -1)
		for {
			maximum := atomic.LoadInt32(s.maximum)
			if current <= maximum || atomic.CompareAndSwapInt32(s.maximum, maximum, current) {
				break
			}
		}
	}
	s.started <- struct{}{}
	<-s.release
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

type cancelableThreadListSource struct {
	*scriptedAppSource
	started chan<- struct{}
	done    chan<- struct{}
}

type failingThreadListSource struct {
	*scriptedAppSource
	err error
}

func (s *failingThreadListSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{}, s.err
}

func (s *cancelableThreadListSource) ListThreads(ctx context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	s.started <- struct{}{}
	<-ctx.Done()
	s.done <- struct{}{}
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
	entered := make(chan struct{}, 1)
	done := make(chan struct{}, 1)
	sources := appsource.NewRegistry()
	sources.Add(&cancelableThreadListSource{scriptedAppSource: &scriptedAppSource{id: "blocked"}, started: entered, done: done})
	result := make(chan error, 1)
	go func() {
		_, err := hubThreadListWithSourceTimeout(ctx, hubcore.WebConfig{}, sources, appwire.ThreadListParams{}, time.Hour)
		result <- err
	}()
	<-entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("hubThreadList error=%v, want context cancellation", err)
	}
	<-done
}

func TestHubThreadListBoundsConcurrentSourcesAndKeepsOptionalErrors(t *testing.T) {
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var current, maximum int32
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	sources := appsource.NewRegistry()
	for i := range 6 {
		sources.Add(&barrierThreadListSource{scriptedAppSource: &scriptedAppSource{id: fmt.Sprintf("source-%d", i), thread: appwire.Thread{ID: fmt.Sprintf("thread-%d", i)}}, started: started, release: release, current: &current, maximum: &maximum})
	}
	result := make(chan appwire.ThreadListResponse, 1)
	go func() {
		response, _ := hubThreadListWithSourceTimeout(context.Background(), hubcore.WebConfig{}, sources, appwire.ThreadListParams{}, time.Hour)
		result <- response
	}()
	for range threadListSourceWorkers {
		<-started
	}
	releaseOnce.Do(func() { close(release) })
	response := <-result
	if len(response.Data) != 6 {
		t.Fatalf("threads=%d, want 6", len(response.Data))
	}
	if maximum > threadListSourceWorkers {
		t.Fatalf("maximum concurrent sources=%d, want <=%d", maximum, threadListSourceWorkers)
	}
}

func TestHubThreadListZeroSourceTimeoutPreservesOptionalAndExplicitErrors(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&failingThreadListSource{scriptedAppSource: &scriptedAppSource{id: "optional-expired"}, err: context.DeadlineExceeded})
	response, err := hubThreadListWithSourceTimeout(context.Background(), hubcore.WebConfig{}, sources, appwire.ThreadListParams{}, 0)
	if err != nil || len(response.Data) != 0 {
		t.Fatalf("optional expired source response=%+v err=%v, want empty success", response, err)
	}
	params := appwire.ThreadListParams{SourceIDs: []string{"optional-expired"}}
	_, err = hubThreadListWithSourceTimeout(context.Background(), hubcore.WebConfig{}, sources, params, 0)
	if err == nil {
		t.Fatal("explicit expired source unexpectedly succeeded")
	}
}

func TestHubThreadListOptionalAndExplicitSourceErrors(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&failingThreadListSource{scriptedAppSource: &scriptedAppSource{id: "optional"}, err: context.DeadlineExceeded})
	if response, err := hubThreadListWithSourceTimeout(context.Background(), hubcore.WebConfig{}, sources, appwire.ThreadListParams{}, time.Second); err != nil || len(response.Data) != 0 {
		t.Fatalf("optional error response=%+v err=%v", response, err)
	}
	params := appwire.ThreadListParams{SourceIDs: []string{"optional"}}
	if _, err := hubThreadListWithSourceTimeout(context.Background(), hubcore.WebConfig{}, sources, params, time.Second); err == nil {
		t.Fatal("explicit source error was swallowed")
	}
}

func TestPastEntryThreadForListUsesMetadataOnly(t *testing.T) {
	entry := hubcore.PastEntry{StateDir: "/path-that-must-not-be-opened", Meta: schema.SessionMeta{ID: "session-1", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/project"}}}
	thread, err := pastEntryThreadForList(context.Background(), hubcore.WebConfig{}, entry)
	if err != nil {
		t.Fatalf("metadata projection: %v", err)
	}
	if thread.ID != entry.Meta.ID || thread.CWD != entry.Meta.EnvInfo.WorkingDir {
		t.Fatalf("metadata projection=%+v, want session metadata", thread)
	}
}
