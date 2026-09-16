package hub

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// countingRemoteSource is a remote-listed source that records how many
// ListThreads calls it served, so a test can prove the lazy fan-outs skip an
// unattached host without calling into it.
type countingRemoteSource struct {
	*scriptedAppSource
	calls atomic.Int64
}

func (s *countingRemoteSource) ListThreads(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	s.calls.Add(1)
	return s.scriptedAppSource.ListThreads(ctx, params)
}

// A non-explicit (empty SourceIDs) fleet-wide thread/list must run only against
// already-attached sources: an unattached remote host is skipped without a call
// and the dialing connector is never reached (component 05, §"The same gate
// applies to the primary thread/list fan-out"; component 06 acceptance
// criterion 9, "no other path attaches a host implicitly").
func TestHubThreadListNonExplicitFanOutSkipsUnattachedRemoteHost(t *testing.T) {
	var dials atomic.Int64
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			dials.Add(1)
			return &appwire.Client{}, nil
		},
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) { return nil, false },
	}
	sources := appsource.NewRegistry()
	source := &countingRemoteSource{scriptedAppSource: &scriptedAppSource{id: "alpha", thread: appwire.Thread{ID: "alpha-thread", Source: "alpha"}}}
	sources.Add(source)

	resp, err := hubThreadListWithSourceTimeout(context.Background(), cfg, sources, appwire.ThreadListParams{}, time.Second)
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	if got := source.calls.Load(); got != 0 {
		t.Fatalf("unattached remote source was listed %d times; want 0", got)
	}
	if got := dials.Load(); got != 0 {
		t.Fatalf("the empty-filter fan-out dialed %d times; want 0", got)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("empty-filter list returned %d rows; want none", len(resp.Data))
	}
}

// An explicitly named host in SourceIDs is a deliberate, host-targeted request
// and is one of the intended attach triggers: the fan-out attaches it before
// calling the source, whose own resolver is attached-only. It is the opposite of
// the implicit empty-filter fan-out.
func TestHubThreadListExplicitSourceIDsAttachesTheHost(t *testing.T) {
	var dials atomic.Int64
	var attached atomic.Bool
	live := &appwire.Client{}
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			dials.Add(1)
			attached.Store(true)
			return live, nil
		},
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) {
			if attached.Load() {
				return live, true
			}
			return nil, false
		},
	}
	sources := appsource.NewRegistry()
	source := &countingRemoteSource{scriptedAppSource: &scriptedAppSource{id: "alpha", thread: appwire.Thread{ID: "alpha-thread", Source: "alpha"}}}
	sources.Add(source)

	resp, err := hubThreadListWithSourceTimeout(context.Background(), cfg, sources, appwire.ThreadListParams{SourceIDs: []string{"alpha"}}, time.Second)
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("explicit host attach dials = %d, want 1", got)
	}
	if got := source.calls.Load(); got != 1 {
		t.Fatalf("explicit host source listed %d times, want 1", got)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "alpha-thread" {
		t.Fatalf("explicit list = %+v, want the host's thread", resp.Data)
	}
}

// The background snapshot must not force attachment either: refreshRemoteThreadSnapshot
// skips an unattached source without calling into it, so the 30s ticker cannot
// dial every configured host (component 06, §"What already exists").
func TestRefreshRemoteThreadSnapshotSkipsUnattachedSourceWithoutACall(t *testing.T) {
	cfg := hubcore.WebConfig{
		RemoteHosts:                []hostreg.Host{{Name: "alpha"}},
		RemoteHostClient:           func(context.Context, string) (*appwire.Client, error) { return &appwire.Client{}, nil },
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) { return nil, false },
	}
	web := NewWebServer(cfg)
	sources := appsource.NewRegistry()
	source := &countingRemoteSource{scriptedAppSource: &scriptedAppSource{id: "alpha", thread: appwire.Thread{ID: "alpha-thread", Source: "alpha"}}}
	sources.Add(source)
	web.sources = sources

	fetch := web.refreshRemoteThreadSnapshot(context.Background())
	if got := source.calls.Load(); got != 0 {
		t.Fatalf("unattached remote source was listed %d times by the snapshot; want 0", got)
	}
	if len(fetch.threads) != 0 {
		t.Fatalf("snapshot returned %d remote threads; want none", len(fetch.threads))
	}
	if fetch.complete {
		t.Fatal("snapshot reported complete with an unattached source skipped")
	}
}

// newHubSourceRegistry must install the attached-only lookups from WebConfig, so
// a direct read against an unattached host is a typed unavailable error and
// never a dial (component 06 acceptance criterion 13).
func TestNewHubSourceRegistryDirectReadAgainstUnattachedHostIsUnavailable(t *testing.T) {
	var dials atomic.Int64
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			dials.Add(1)
			return &appwire.Client{}, nil
		},
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) { return nil, false },
	}
	registry := newHubSourceRegistry(cfg)
	source, ok := registry.Source("alpha")
	if !ok {
		t.Fatal("registry has no source for alpha")
	}
	_, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "alpha:t1"})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("ReadThread error %T=%v, want WireError", err, err)
	}
	data, _ := wire.Data.(appwire.ErrorData)
	if wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("ReadThread wire=%+v, want session unavailable", wire)
	}
	if got := dials.Load(); got != 0 {
		t.Fatalf("direct read against an unattached host dialed %d times; want 0", got)
	}
}
