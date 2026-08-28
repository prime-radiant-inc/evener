package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// FuzzSessionLivePass4 closes the successful remote-source paths left by the
// broad route fuzzers. The source is entirely in-process and never dials a
// daemon or provider.
func FuzzSessionLivePass4(f *testing.F) {
	for op := range uint8(3) {
		f.Add(op, "remote title", int64(1700000000))
	}
	f.Fuzz(func(t *testing.T, op uint8, title string, stamp int64) {
		stamp = 1700000000 + stamp%10000
		thread := appwire.Thread{
			ID: "thread-1", SessionID: "thread-1", Source: "remote", Name: title,
			CWD: "/work/project", ModelProvider: "provider/model", CreatedAt: stamp - 10,
			UpdatedAt: stamp, Status: appwire.ThreadStatus{Type: "active"},
			Turns: []appwire.Turn{{ID: "turn-1", Status: appwire.TurnStatusCompleted}},
			Evener: appwire.EvenerThread{Ref: "remote:thread-1", ActiveTurnID: "turn-2",
				Capabilities: appwire.ThreadCapabilities{Send: true, Steer: true, Interrupt: true, Clear: true, Shutdown: true, Queue: true},
				Usage:        &appwire.EvenerUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}},
		}
		web := NewWebServer(hubcore.WebConfig{})
		registry := appsource.NewRegistry()
		registry.Add(&scriptedAppSource{id: "remote", thread: thread})
		web.sources = registry

		switch op % 3 {
		case 0:
			metas, live, _ := web.navigationTreeInputs(context.Background())
			if len(metas) != 1 || len(live) != 1 {
				t.Fatalf("remote inputs metas=%d live=%d", len(metas), len(live))
			}
			_ = web.apiTreeSources()
		case 1:
			threads := web.refreshRemoteThreads(context.Background())
			if len(threads) != 1 || threads[0].Evener.Ref == "" {
				t.Fatalf("remote threads=%+v", threads)
			}
		case 2:
			source := &stubThreadLister{id: "remote", resp: appwire.ThreadListResponse{Data: []appwire.Thread{thread}}}
			_ = web.listThreadsWithFallback(context.Background(), source)
			source.resp.Data = nil
			_ = web.listThreadsWithFallback(context.Background(), source)
			source.err = context.DeadlineExceeded
			_ = web.listThreadsWithFallback(context.Background(), source)
			_ = time.Unix(stamp, 0)
		}
	})
}
