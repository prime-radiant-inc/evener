package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func TestHubRelaySharedSessionAliasesDeliverEachNotificationOnce(t *testing.T) {
	const (
		rootRef  = "local:root-thread"
		childRef = "local:child-thread"
	)
	pool := &aliasRelayPool{}
	source := &aliasRelaySource{
		pool: pool,
		canonicalRefs: map[string]appwire.Ref{
			"root-thread":  {SourceID: "local", ThreadID: "root-thread"},
			"child-thread": {SourceID: "local", ThreadID: "root-thread"},
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	longLived := dialHubRPC(t, hub)
	defer longLived.Close()
	if _, err := longLived.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize long-lived client: %v", err)
	}
	for _, ref := range []string{rootRef, childRef} {
		if _, err := longLived.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
			t.Fatalf("subscribe long-lived client to %s: %v", ref, err)
		}
	}
	if got := source.acquireCallCount(); got != 1 {
		t.Fatalf("relay acquisitions = %d, want one canonical session", got)
	}
	if got := pool.listenerCount(); got != 1 {
		t.Fatalf("relay listeners = %d, want one canonical listener", got)
	}

	fresh := dialHubRPC(t, hub)
	defer fresh.Close()
	if _, err := fresh.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize fresh client: %v", err)
	}
	if _, err := fresh.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
		t.Fatalf("subscribe fresh client: %v", err)
	}

	params, err := json.Marshal(appwire.ReasoningSummaryDeltaParams{
		ThreadID:     "root-thread",
		Ref:          rootRef,
		TurnID:       "turn-alias",
		ItemID:       "item-alias",
		SummaryIndex: 0,
		Delta:        "one logical delta",
	})
	if err != nil {
		t.Fatal(err)
	}
	pool.emit(t, appwire.Notification{Method: appwire.NotifyReasoningSummaryDelta, Params: params})
	// Every alias fanout has acknowledged and enqueued the delta before emit
	// returns. This connection-wide marker is therefore an ordered drain barrier.
	appServer.BroadcastAll("test/alias-barrier", map[string]any{})

	if got := aliasDeltaCountUntilBarrier(t, longLived, "one logical delta"); got != 1 {
		t.Fatalf("long-lived root+child client received delta %d times, want once", got)
	}
	if got := aliasDeltaCountUntilBarrier(t, fresh, "one logical delta"); got != 1 {
		t.Fatalf("fresh root-only client received delta %d times, want once", got)
	}

	params, err = json.Marshal(appwire.ReasoningSummaryDeltaParams{
		ThreadID:     "child-thread",
		Ref:          childRef,
		TurnID:       "turn-alias",
		ItemID:       "item-alias-child",
		SummaryIndex: 0,
		Delta:        "one child delta",
	})
	if err != nil {
		t.Fatal(err)
	}
	pool.emit(t, appwire.Notification{Method: appwire.NotifyReasoningSummaryDelta, Params: params})
	appServer.BroadcastAll("test/alias-barrier", map[string]any{})
	if got := aliasDeltaCountUntilBarrier(t, longLived, "one child delta"); got != 1 {
		t.Fatalf("long-lived root+child client received child delta %d times, want once", got)
	}
	if got := aliasDeltaCountUntilBarrier(t, fresh, "one child delta"); got != 0 {
		t.Fatalf("fresh root-only client received child delta %d times, want none", got)
	}
}

func TestHubRelaySharedSessionAliasesEnrichUntargetedOutputImages(t *testing.T) {
	const (
		rootRef  = "local:root-image"
		childRef = "local:child-image"
	)
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	pool := &aliasRelayPool{}
	source := &aliasRelaySource{
		pool: pool,
		cwd:  cwd,
		canonicalRefs: map[string]appwire.Ref{
			"root-image":  {SourceID: "local", ThreadID: "root-image"},
			"child-image": {SourceID: "local", ThreadID: "root-image"},
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize client: %v", err)
	}
	for _, ref := range []string{rootRef, childRef} {
		if _, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
			t.Fatalf("subscribe client to %s: %v", ref, err)
		}
	}

	started := appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{
		"turnId": "turn-image",
		"item": appwire.ThreadItem{
			Type:          "commandExecution",
			ID:            "item-image",
			ToolName:      "write_file",
			CallID:        "call-image",
			ArgumentsJSON: `{"file_path":"plot.png"}`,
			Status:        appwire.TurnStatusInProgress,
		},
	}).Notification
	pool.emit(t, *started)
	completed := appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
		"turnId": "turn-image",
		"item": appwire.ThreadItem{
			Type:     "commandExecution",
			ID:       "item-image",
			ToolName: "write_file",
			CallID:   "call-image",
			Output:   "wrote",
			Status:   appwire.TurnStatusCompleted,
		},
	}).Notification
	pool.emit(t, *completed)
	appServer.BroadcastAll("test/alias-barrier", map[string]any{})

	items := aliasCompletedItemsUntilBarrier(t, client, "item-image")
	if len(items) != 2 {
		t.Fatalf("completed item broadcasts = %d, want one enriched broadcast per alias", len(items))
	}
	for i, item := range items {
		if len(item.OutputImages) != 1 || item.OutputImages[0].Source != "written-file" || item.OutputImages[0].Path != "plot.png" {
			t.Fatalf("completed item %d OutputImages = %+v, want written-file plot.png descriptor", i, item.OutputImages)
		}
	}
}

func TestHubRelayConcurrentAliasesShareCanonicalHandle(t *testing.T) {
	const (
		rootRef  = "local:root-concurrent"
		childRef = "local:child-concurrent"
	)
	acquireEntered := make(chan struct{}, 2)
	releaseAcquire := make(chan struct{})
	childResolved := make(chan struct{})
	var childResolveOnce sync.Once
	pool := &aliasRelayPool{}
	source := &aliasRelaySource{
		pool: pool,
		canonicalRefs: map[string]appwire.Ref{
			"root-concurrent":  {SourceID: "local", ThreadID: "root-concurrent"},
			"child-concurrent": {SourceID: "local", ThreadID: "root-concurrent"},
		},
		resolveHook: func(params appwire.ThreadReadParams) {
			if params.Ref == childRef {
				childResolveOnce.Do(func() { close(childResolved) })
			}
		},
		acquireEntered: acquireEntered,
		acquireGate:    releaseAcquire,
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)

	type readResult struct {
		read *hubThreadReadResult
		err  error
	}
	read := func(ref string) <-chan readResult {
		result := make(chan readResult, 1)
		go func() {
			got, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
			result <- readResult{read: got, err: err}
		}()
		return result
	}

	rootResult := read(rootRef)
	<-acquireEntered
	childResult := read(childRef)
	<-childResolved
	close(releaseAcquire)

	for ref, result := range map[string]<-chan readResult{rootRef: rootResult, childRef: childResult} {
		got := <-result
		if got.err != nil {
			t.Fatalf("readThread(%s): %v", ref, got.err)
		}
		got.read.finish(false)
	}
	if got := source.acquireCallCount(); got != 1 {
		t.Fatalf("relay acquisitions = %d, want one for concurrent aliases", got)
	}
	if got := pool.listenerCount(); got != 1 {
		t.Fatalf("relay listeners = %d, want one for concurrent aliases", got)
	}
}

func TestHubRelayThreadIDReadUsesAuthoritativeResponseRef(t *testing.T) {
	pool := &aliasRelayPool{}
	source := &aliasRelaySource{
		pool:              pool,
		authoritativeRefs: map[string]string{"lookup-thread": "local:workspace-thread"},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize client: %v", err)
	}
	if _, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{ThreadID: "lookup-thread", Subscribe: true}); err != nil {
		t.Fatalf("subscribe by thread id: %v", err)
	}
	params, err := json.Marshal(appwire.ReasoningSummaryDeltaParams{
		ThreadID:     "lookup-thread",
		Ref:          "local:workspace-thread",
		TurnID:       "turn-workspace",
		ItemID:       "item-workspace",
		SummaryIndex: 0,
		Delta:        "authoritative workspace delta",
	})
	if err != nil {
		t.Fatal(err)
	}
	pool.emit(t, appwire.Notification{Method: appwire.NotifyReasoningSummaryDelta, Params: params})
	appServer.BroadcastAll("test/alias-barrier", map[string]any{})
	if got := aliasDeltaCountUntilBarrier(t, client, "authoritative workspace delta"); got != 1 {
		t.Fatalf("thread-id subscriber received authoritative-ref delta %d times, want once", got)
	}
}

func TestRelayNotificationTargetsPreservesRoutingPrecedenceAndUnknownPayloads(t *testing.T) {
	tests := []struct {
		name         string
		params       string
		threadID     string
		ref          string
		wantTargeted bool
	}{
		{name: "matching ref wins over conflicting thread", params: `{"ref":"local:target","threadId":"other"}`, threadID: "target", ref: "local:target", wantTargeted: true},
		{name: "mismatched ref wins over matching thread", params: `{"ref":"local:other","threadId":"target"}`, threadID: "target", ref: "local:target", wantTargeted: false},
		{name: "thread fallback matches", params: `{"threadId":"target"}`, threadID: "target", ref: "local:target", wantTargeted: true},
		{name: "thread fallback rejects mismatch", params: `{"threadId":"other"}`, threadID: "target", ref: "local:target", wantTargeted: false},
		{name: "untargeted payload preserves delivery", params: `{}`, threadID: "target", ref: "local:target", wantTargeted: true},
		{name: "malformed payload preserves delivery", params: `{`, threadID: "target", ref: "local:target", wantTargeted: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := relayNotificationTargets(appwire.Notification{Params: json.RawMessage(tc.params)}, tc.threadID, tc.ref)
			if got != tc.wantTargeted {
				t.Fatalf("relayNotificationTargets() = %v, want %v", got, tc.wantTargeted)
			}
		})
	}
}

type aliasRelaySource struct {
	relayLifecycleSource
	pool              *aliasRelayPool
	cwd               string
	authoritativeRefs map[string]string
	canonicalRefs     map[string]appwire.Ref

	mu             sync.Mutex
	acquireCalls   int
	resolveHook    func(appwire.ThreadReadParams)
	acquireGate    <-chan struct{}
	acquireEntered chan<- struct{}
}

func (*aliasRelaySource) ID() string { return "local" }

func (s *aliasRelaySource) ResolveRelaySession(params appwire.ThreadReadParams) (appwire.Ref, error) {
	if s.resolveHook != nil {
		s.resolveHook(params)
	}
	var ref appwire.Ref
	if params.Ref != "" {
		parsed, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.Ref{}, err
		}
		ref = parsed
	} else {
		if params.ThreadID == "" {
			return appwire.Ref{}, errors.New("missing relay target")
		}
		ref = appwire.Ref{SourceID: s.ID(), ThreadID: params.ThreadID}
	}
	if canonical := s.canonicalRefs[ref.ThreadID]; canonical != (appwire.Ref{}) {
		return canonical, nil
	}
	return ref, nil
}

func (s *aliasRelaySource) AcquireRelaySession(appwire.Ref) (appsource.RelaySessionLease, error) {
	s.mu.Lock()
	s.acquireCalls++
	s.mu.Unlock()
	if s.acquireEntered != nil {
		s.acquireEntered <- struct{}{}
	}
	if s.acquireGate != nil {
		<-s.acquireGate
	}
	return &aliasRelayLease{source: s}, nil
}

func (s *aliasRelaySource) acquireCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquireCalls
}

type aliasRelayLease struct {
	source *aliasRelaySource
}

func (l *aliasRelayLease) Read(_ context.Context, params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
	threadID := params.ThreadID
	authoritativeRef := params.Ref
	if params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appsource.RelayReadResult{}, err
		}
		threadID = ref.ThreadID
	}
	if mapped := l.source.authoritativeRefs[threadID]; mapped != "" {
		authoritativeRef = mapped
	}
	if authoritativeRef == "" {
		authoritativeRef = appwire.Ref{SourceID: "local", ThreadID: threadID}.String()
	}
	return appsource.RelayReadResult{
		Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "local",
			CWD:       l.source.cwd,
			Evener:    appwire.EvenerThread{Ref: authoritativeRef},
		}},
		Handoff: &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})},
	}, nil
}

func (l *aliasRelayLease) Listen(context.Context) (<-chan appsource.RelayDelivery, error) {
	return l.source.pool.listen(), nil
}

func (*aliasRelayLease) Close() {}

type aliasRelayPool struct {
	mu        sync.Mutex
	listeners []chan appsource.RelayDelivery
}

func (p *aliasRelayPool) listen() <-chan appsource.RelayDelivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	listener := make(chan appsource.RelayDelivery)
	p.listeners = append(p.listeners, listener)
	return listener
}

func (p *aliasRelayPool) listenerCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.listeners)
}

func (p *aliasRelayPool) emit(t *testing.T, notification appwire.Notification) {
	t.Helper()
	p.mu.Lock()
	listeners := append([]chan appsource.RelayDelivery(nil), p.listeners...)
	p.mu.Unlock()
	for _, listener := range listeners {
		ack := make(chan struct{})
		var once sync.Once
		delivery := appsource.RelayDelivery{
			Notification: notification,
			Acknowledge:  func() { once.Do(func() { close(ack) }) },
		}
		select {
		case listener <- delivery:
		case <-time.After(2 * time.Second):
			t.Fatal("relay listener did not accept notification")
		}
		select {
		case <-ack:
		case <-time.After(2 * time.Second):
			t.Fatal("relay listener did not acknowledge notification")
		}
	}
}

func aliasDeltaCountUntilBarrier(t *testing.T, client *appwire.Client, delta string) int {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	count := 0
	for {
		select {
		case notification, ok := <-client.Notifications():
			if !ok {
				t.Fatal("client notification stream closed before barrier")
			}
			if notification.Method == "test/alias-barrier" {
				return count
			}
			if notification.Method == appwire.NotifyReasoningSummaryDelta {
				var params appwire.ReasoningSummaryDeltaParams
				if json.Unmarshal(notification.Params, &params) == nil && params.Delta == delta {
					count++
				}
			}
		case <-timer.C:
			t.Fatal("timed out waiting for alias delivery barrier")
		}
	}
}

func aliasCompletedItemsUntilBarrier(t *testing.T, client *appwire.Client, itemID string) []appwire.ThreadItem {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	var items []appwire.ThreadItem
	for {
		select {
		case notification, ok := <-client.Notifications():
			if !ok {
				t.Fatal("client notification stream closed before barrier")
			}
			if notification.Method == "test/alias-barrier" {
				return items
			}
			if notification.Method == appwire.NotifyItemCompleted {
				var params struct {
					Item appwire.ThreadItem `json:"item"`
				}
				if json.Unmarshal(notification.Params, &params) == nil && params.Item.ID == itemID {
					items = append(items, params.Item)
				}
			}
		case <-timer.C:
			t.Fatal("timed out waiting for alias delivery barrier")
		}
	}
}
