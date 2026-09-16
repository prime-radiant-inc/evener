package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// threadListBudgetSource is the fan-out's seam for a source that needs a
// deadline of its own; the remote hub source must satisfy it.
var _ threadListBudgetSource = (*appsource.RemoteHubSource)(nil)

// The per-source fan-out budget exists to bound a local daemon's cheap loopback
// list. A remote host's first list instead pays that host's SSH attach, so a
// caller that applies the local budget to it cuts a working attach off, and the
// timeout that results is indistinguishable from a broken host: an unfiltered
// list then drops the whole host and still reports success. Here the attach
// outlasts the local budget and the host's threads must still be listed.
func TestHubRPCThreadListServesRemoteHostWhoseAttachOutlastsLocalBudget(t *testing.T) {
	remote, _ := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadList:
			return appwire.ThreadListResponse{Data: []appwire.Thread{{
				ID:        "t1",
				SessionID: "t1",
				Source:    "local",
				Evener:    appwire.EvenerThread{Ref: "local:t1"},
			}}}
		default:
			return appwire.EmptyResponse{}
		}
	})
	// The connector stands in for sshconn.Ensure: it hands back a client only
	// once the cold attach has finished, and gives up when its context does. The
	// attach here outlasts threadListSourceTimeout, so the local budget alone
	// cannot serve this list.
	source := appsource.NewRemoteHubSource("h1", nil, func(ctx context.Context, _ string) (*appwire.Client, error) {
		select {
		case <-time.After(threadListSourceTimeout + 500*time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return remote, nil
	})

	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	rpc := dialHubRPC(t, srv)
	defer rpc.Close()
	if _, err := rpc.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := rpc.ThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("thread list = %+v, want the cold remote host's row: an unfiltered list dropped a host whose attach outlasted the local budget", resp.Data)
	}
	if resp.Data[0].Source != "h1" || resp.Data[0].ID != "t1" {
		t.Fatalf("row = %+v, want the h1 row", resp.Data[0])
	}
}

// budgetedThreadListSource answers only after delay, and reports its own list
// deadline so the fan-out can budget for it. A source that reports nothing
// keeps the caller's default budget (see threadListTimeoutFor).
type budgetedThreadListSource struct {
	*scriptedAppSource
	budget time.Duration
	delay  time.Duration
}

func (s *budgetedThreadListSource) ThreadListBudget() time.Duration { return s.budget }

func (s *budgetedThreadListSource) ListThreads(ctx context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	select {
	case <-time.After(s.delay):
		return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
	case <-ctx.Done():
		return appwire.ThreadListResponse{}, ctx.Err()
	}
}

// A source that reports its own list budget is not cut off by the local-daemon
// default, and a source that reports none still is: the local daemon's semantics
// are unchanged, because it is the source — not the fan-out — that knows whether
// its list has to attach anything.
func TestHubThreadListUsesSourceReportedBudget(t *testing.T) {
	slow := 250 * time.Millisecond
	const fallback = 25 * time.Millisecond
	sources := appsource.NewRegistry()
	sources.Add(&budgetedThreadListSource{
		scriptedAppSource: &scriptedAppSource{id: "budgeted", thread: appwire.Thread{ID: "budgeted-thread", Source: "budgeted"}},
		budget:            time.Minute,
		delay:             slow,
	})
	sources.Add(&budgetedThreadListSource{
		scriptedAppSource: &scriptedAppSource{id: "unbudgeted", thread: appwire.Thread{ID: "unbudgeted-thread", Source: "unbudgeted"}},
		delay:             slow,
	})

	response, err := hubThreadListWithSourceTimeout(context.Background(), hubcore.WebConfig{}, sources, appwire.ThreadListParams{}, fallback)
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].Source != "budgeted" {
		t.Fatalf("threads = %+v, want only the source that budgeted for its list", response.Data)
	}
}

// The remote list budget is a documented number with two jobs: it must outlast
// the ssh transport's own connect bound (10s), so a host that cannot be reached
// fails on the transport rather than on the list budget, and it must stay well
// under sshconn's preflight and deploy bounds (70s and 10m), which no list may
// block on. This pins the number and that the fan-out honours it for the real
// source.
func TestRemoteHubSourceThreadListBudget(t *testing.T) {
	source := appsource.NewRemoteHubSource("h1", nil, nil)
	budget := source.ThreadListBudget()
	if budget != 15*time.Second {
		t.Fatalf("remote list budget = %s, want the documented 15s", budget)
	}
	if budget <= 10*time.Second {
		t.Fatalf("remote list budget = %s, want more than the ssh transport's 10s connect bound", budget)
	}
	if budget > 30*time.Second {
		t.Fatalf("remote list budget = %s, want a bounded list", budget)
	}
	if got := threadListTimeoutFor(source, threadListSourceTimeout); got != budget {
		t.Fatalf("fan-out budget for a remote source = %s, want the source's %s", got, budget)
	}
}
