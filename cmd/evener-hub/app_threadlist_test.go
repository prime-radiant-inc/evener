package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

type blockingThreadListSource struct {
	*scriptedAppSource
	release <-chan struct{}
}

func (s *blockingThreadListSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	<-s.release
	return appwire.ThreadListResponse{}, nil
}

func TestHubThreadListBoundsUnresponsiveSource(t *testing.T) {
	previousTimeout := threadListSourceTimeout
	threadListSourceTimeout = 20 * time.Millisecond
	t.Cleanup(func() { threadListSourceTimeout = previousTimeout })

	release := make(chan struct{})
	source := &blockingThreadListSource{
		scriptedAppSource: &scriptedAppSource{id: "blocked"},
		release:           release,
	}
	registry := appsource.NewRegistry()
	registry.Add(source)
	registry.Add(&scriptedAppSource{
		id: "fast",
		thread: appwire.Thread{
			ID:     "fast-thread",
			Source: "fast",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		},
	})

	started := time.Now()
	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{}, registry, appwire.ThreadListParams{})
	close(release)
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "fast-thread" {
		t.Fatalf("threads=%+v, want the responsive source's partial result", resp.Data)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("thread list took %s despite source timeout", elapsed)
	}
}
