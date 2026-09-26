package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
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

// hookedRemoteSource lists its scripted row after running onList, so a test
// can land registry churn at an exact point inside a walk — the mid-walk
// window between the walk's enumeration of one source and its read of the
// next.
type hookedRemoteSource struct {
	*scriptedAppSource
	onList func()
}

func (s *hookedRemoteSource) ListThreads(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	if s.onList != nil {
		s.onList()
	}
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

// The shared host-routing origin guard covers the explicit-SourceIDs
// thread/list attach trigger too: a remote-originated explicit list is refused
// typed before the Ensure-backed dial, so a peer hub cannot make this hub
// attach a configured host (component 07, §"Host-routing origin guard").
func TestHubThreadListExplicitAttachRefusesRemoteOriginatedDial(t *testing.T) {
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
	sources.Add(appsource.NewRemoteHubSource("alpha", nil, cfg.RemoteHostClient))

	ctx := withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge)
	_, err := hubThreadListWithSourceTimeout(ctx, cfg, sources, appwire.ThreadListParams{SourceIDs: []string{"alpha"}}, time.Second)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("remote-originated explicit list error %T=%v, want WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("remote-originated explicit list wire=%+v, want invalid params", wire)
	}
	if got := dials.Load(); got != 0 {
		t.Fatalf("remote-originated explicit list dialed %d times, want 0", got)
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

// A host that detaches between two snapshot ticks must not lose the rows it
// already contributed. The skip branch serves last-known-good instead of an
// empty list — in both fetch.threads (the flattened tree input) and
// fetch.sources[id].Threads (the per-source manifest view) — and reports
// Complete == false for that source so the manifest does not claim an
// authoritative empty list. This pins the carry-forward behavior the round-two
// snapshot gate ships; the reviewer found it untested, not broken.
func TestRefreshRemoteThreadSnapshotKeepsLastKnownGoodWhenHostDetaches(t *testing.T) {
	var attached atomic.Bool
	attached.Store(true)
	live := &appwire.Client{}
	cfg := hubcore.WebConfig{
		RemoteHosts:      []hostreg.Host{{Name: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) { return live, nil },
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) {
			if attached.Load() {
				return live, true
			}
			return nil, false
		},
	}
	web := NewWebServer(cfg)
	sources := appsource.NewRegistry()
	source := &countingRemoteSource{scriptedAppSource: &scriptedAppSource{id: "alpha", thread: appwire.Thread{ID: "alpha-thread", Source: "alpha"}}}
	sources.Add(source)
	web.sources = sources

	first := web.refreshRemoteThreadSnapshot(context.Background())
	if got := source.calls.Load(); got != 1 {
		t.Fatalf("attached source listed %d times; want 1", got)
	}
	if len(first.threads) != 1 || first.threads[0].ID != "alpha-thread" {
		t.Fatalf("first snapshot threads = %+v, want the source's row", first.threads)
	}
	if !first.complete {
		t.Fatal("first snapshot reported incomplete while the host was attached")
	}
	if snap, ok := first.sources["alpha"]; !ok || len(snap.Threads) != 1 || !snap.Complete {
		t.Fatalf("first snapshot source alpha = %+v, ok=%v; want one complete row", snap, ok)
	}

	attached.Store(false)
	second := web.refreshRemoteThreadSnapshot(context.Background())
	if got := source.calls.Load(); got != 1 {
		t.Fatalf("detached source was listed %d times; want no further call", got)
	}
	if len(second.threads) != 1 || second.threads[0].ID != "alpha-thread" {
		t.Fatalf("carried-forward threads = %+v, want the previously listed row", second.threads)
	}
	snap, ok := second.sources["alpha"]
	if !ok {
		t.Fatal("snapshot dropped the detached source entirely")
	}
	if len(snap.Threads) != 1 || snap.Threads[0].ID != "alpha-thread" {
		t.Fatalf("carried-forward source threads = %+v, want the previously listed row", snap.Threads)
	}
	if snap.Complete {
		t.Fatal("detached source reported Complete while serving carried-forward rows")
	}
	if second.complete {
		t.Fatal("snapshot reported complete with a detached source serving carried-forward rows")
	}
}

// TestRefreshRemoteThreadSnapshotDropsRowsWhenChurnLandsAfterRead pins the
// round-10 fix on the real walk path: the remove and re-add fire from the
// second-listed source's list, so they land after the walk has read the
// victim — the rows the walk holds belong to the removed registration, and
// the walk's own read-time capture mismatches the re-added one at the
// publish. A capture taken once before the walk started reading published
// those rows under the re-added identity.
func TestRefreshRemoteThreadSnapshotDropsRowsWhenChurnLandsAfterRead(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})
	sources := appsource.NewRegistry()
	// The registration discipline the host manager follows: generations are
	// assigned before the sources become registry-visible.
	cache.RegisterSource("aaa-victim")
	cache.RegisterSource("zzz-churner")
	// The walk reads its sources in name order, so aaa-victim is read first
	// and the churn the churner's list performs lands strictly after that
	// read — the round-10 window between the read and the publish.
	churned := make(chan struct{})
	sources.Add(&scriptedAppSource{id: "aaa-victim", thread: appwire.Thread{ID: "victim-thread", Source: "aaa-victim"}})
	sources.Add(&hookedRemoteSource{
		scriptedAppSource: &scriptedAppSource{id: "zzz-churner", thread: appwire.Thread{ID: "churner-thread", Source: "zzz-churner"}},
		onList: func() {
			cache.RemoveSource("aaa-victim")
			cache.RegisterSource("aaa-victim")
			close(churned)
		},
	})
	web.sources = sources

	fetch := web.refreshRemoteThreadSnapshot(context.Background())
	select {
	case <-churned:
	default:
		t.Fatal("walk finished without the churner's list running; fixture ordering broken")
	}
	if len(fetch.threads) != 2 {
		t.Fatalf("walk threads = %+v, want both sources' rows read", fetch.threads)
	}
	cache.StoreWalkSnapshot(hubcore.RemoteThreadSnapshot{
		Threads:  fetch.threads,
		Complete: fetch.complete,
		Sources:  fetch.sources,
	}, fetch.sourceGenerations)
	for _, thread := range cache.Snapshot().Threads {
		if thread.Source == "aaa-victim" {
			t.Fatalf("late walk published thread %q under the re-added identity", thread.ID)
		}
	}
	if _, ok := cache.Snapshot().Sources["aaa-victim"]; ok {
		t.Fatal("late walk published the re-added name's per-source snapshot from the removed registration's rows")
	}
	if got := cache.Snapshot().Threads; len(got) != 1 || got[0].ID != "churner-thread" {
		t.Fatalf("threads after the late publish = %+v, want the churner's row alone", got)
	}
}

// TestRefreshRemoteThreadSnapshotPublishesRowsReadAfterMidWalkChurn pins the
// immediacy the read-time capture buys on the real walk path: the remove and
// re-add fire from the first-listed source's list, so they land BEFORE the
// walk reads the churned name — the walk reads the re-added registration's
// rows and captures the re-added generation, and they publish on this tick.
// A capture taken once at walk start would still hold the removed
// registration's generation and drop the rows the re-added registration
// owns.
func TestRefreshRemoteThreadSnapshotPublishesRowsReadAfterMidWalkChurn(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})
	sources := appsource.NewRegistry()
	cache.RegisterSource("aaa-churner")
	cache.RegisterSource("zzz-victim")
	churned := make(chan struct{})
	sources.Add(&hookedRemoteSource{
		scriptedAppSource: &scriptedAppSource{id: "aaa-churner", thread: appwire.Thread{ID: "churner-thread", Source: "aaa-churner"}},
		onList: func() {
			cache.RemoveSource("zzz-victim")
			cache.RegisterSource("zzz-victim")
			close(churned)
		},
	})
	sources.Add(&scriptedAppSource{id: "zzz-victim", thread: appwire.Thread{ID: "victim-thread", Source: "zzz-victim"}})
	web.sources = sources

	fetch := web.refreshRemoteThreadSnapshot(context.Background())
	select {
	case <-churned:
	default:
		t.Fatal("walk finished without the churner's list running; fixture ordering broken")
	}
	// The walk captured the re-added registration's generation when it read
	// the victim — not the removed registration the walk started under.
	current, ok := cache.SourceGeneration("zzz-victim")
	if !ok {
		t.Fatal("re-added source carries no generation")
	}
	if captured, ok := fetch.sourceGenerations["zzz-victim"]; !ok || captured != current {
		t.Fatalf("walk captured zzz-victim at %d (ok=%v), want the read-time generation %d", captured, ok, current)
	}
	cache.StoreWalkSnapshot(hubcore.RemoteThreadSnapshot{
		Threads:  fetch.threads,
		Complete: fetch.complete,
		Sources:  fetch.sources,
	}, fetch.sourceGenerations)
	if got := cache.Snapshot().Threads; len(got) != 2 {
		t.Fatalf("threads after the publish = %+v, want both sources' rows: the rows read under the re-added registration must publish", got)
	}
	if _, ok := cache.Snapshot().Sources["zzz-victim"]; !ok {
		t.Fatal("publish dropped the re-added source's per-source snapshot")
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

// stubAttachedChannel models one installed sshconn.Channel generation: its
// client, preflight, and handshake all come from the same connection.
type stubAttachedChannel struct {
	client    *appwire.Client
	preflight sshconn.Preflight
	handshake appwire.InitializeResponse
}

func (s stubAttachedChannel) Client() *appwire.Client { return s.client }
func (s stubAttachedChannel) Preflight() sshconn.Preflight {
	return s.preflight
}
func (s stubAttachedChannel) Handshake() appwire.InitializeResponse { return s.handshake }

// The capability probe resolves its client once and runs every wire read on it.
// If a supervisor reconnect installs a new channel before the probe asks for
// the preflight facts, answering from the new channel would let the probe cache
// a snapshot assembled from two generations — the hazard the pre-attached-only
// code refused with "connection changed during capability probe". The
// attached-only version must refuse just the same: an installed channel whose
// client is not the probe's is a typed unavailable, never a mixed snapshot.
func TestRemoteHostFactsRefusesAReconnectedChannelGeneration(t *testing.T) {
	probeClient := &appwire.Client{}
	reconnected := stubAttachedChannel{
		client:    &appwire.Client{}, // a different generation than probeClient
		preflight: sshconn.Preflight{Protocol: "v9", OS: "plan9", Arch: "mips"},
		handshake: appwire.InitializeResponse{ProtocolVersion: "v9", SourceID: "local"},
	}

	_, err := remoteHostFactsForChannel(reconnected, "alpha", probeClient)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("facts for a reconnected generation: error %T=%v, want WireError", err, err)
	}
	data, _ := wire.Data.(appwire.ErrorData)
	if wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("facts wire=%+v, want session unavailable", wire)
	}

	if hs, ok := remoteHostHandshakeForChannel(reconnected, probeClient); ok {
		t.Fatalf("handshake for a reconnected generation = %+v, true; want false", hs)
	}
}

// When the installed channel is the probe's own generation, both seams answer
// from it: the facts carry that channel's preflight, and the handshake is the
// one that channel captured.
func TestRemoteHostFactsAndHandshakeUseTheProbesGeneration(t *testing.T) {
	probeClient := &appwire.Client{}
	installed := stubAttachedChannel{
		client:    probeClient,
		preflight: sshconn.Preflight{Protocol: "v4", OS: "linux", Arch: "amd64"},
		handshake: appwire.InitializeResponse{ProtocolVersion: "v4", SourceID: "local"},
	}

	facts, err := remoteHostFactsForChannel(installed, "alpha", probeClient)
	if err != nil {
		t.Fatalf("facts: %v", err)
	}
	if facts.ProtocolVersion != "v4" || facts.OS != "linux" || facts.Arch != "amd64" {
		t.Fatalf("facts = %+v, want the installed channel's preflight", facts)
	}

	hs, ok := remoteHostHandshakeForChannel(installed, probeClient)
	if !ok || hs.ProtocolVersion != "v4" || hs.SourceID != "local" {
		t.Fatalf("handshake = %+v, %v; want the installed channel's handshake", hs, ok)
	}
}

// An explicit host-targeted thread/list attaches the host before calling the
// source, whose own resolver is attached-only. When that attach fails, the
// error must be classified through the source exactly as its own call path
// classifies a connect failure: sshconn's transient attach failure reaches the
// caller as the typed SessionUnavailable the auto-resume/refusal gates match,
// not as the raw transport error no gate can attribute to the host.
func TestHubThreadListExplicitAttachFailureIsSessionUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		attachErr error
	}{
		{
			name:      "ssh start chain",
			attachErr: fmt.Errorf("%w: host %q link dropped before the channel was usable", sshconn.ErrSSHStart, "alpha"),
		},
		{
			name:      "deadline exceeded chain",
			attachErr: fmt.Errorf("attach alpha: %w", context.DeadlineExceeded),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hubcore.WebConfig{
				RemoteHosts: []hostreg.Host{{Name: "alpha"}},
				RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
					return nil, tc.attachErr
				},
				RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) { return nil, false },
			}
			sources := appsource.NewRegistry()
			// A real remote hub source: the classification under test is the
			// mapping its own call path uses.
			sources.Add(appsource.NewRemoteHubSource("alpha", nil, cfg.RemoteHostClient))

			_, err := hubThreadListWithSourceTimeout(context.Background(), cfg, sources, appwire.ThreadListParams{SourceIDs: []string{"alpha"}}, time.Second)
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("explicit attach failure %T=%v, want a typed WireError", err, err)
			}
			data, _ := wire.Data.(appwire.ErrorData)
			if wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
				t.Fatalf("explicit attach failure wire=%+v, want session unavailable", wire)
			}
			if !strings.Contains(wire.Message, "alpha") {
				t.Fatalf("attach failure message %q does not name host alpha", wire.Message)
			}
		})
	}
}

// A caller cancellation racing the facts lookup is the caller's own context
// ending, not host unavailability: RemoteHostFacts must return the ctx error
// raw rather than the typed SessionUnavailable the auto-resume gate acts on.
// The lookup must not even run once the context is done.
func TestRemoteHostFactsReturnsCallerCancellationRaw(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lookedUp := false
	_, err := remoteHostFactsIfAttached(ctx, "alpha", &appwire.Client{}, func(string) (attachedChannelView, bool) {
		lookedUp = true
		return nil, false
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled-context facts error %T=%v, want context.Canceled", err, err)
	}
	if wire, ok := errors.AsType[appwire.WireError](err); ok {
		t.Fatalf("canceled context classified as host unavailability: %+v", wire)
	}
	if lookedUp {
		t.Fatal("the facts lookup ran after the caller canceled")
	}
}
