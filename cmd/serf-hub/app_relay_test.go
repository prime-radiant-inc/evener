package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appserver"
)

func TestHubAtomicRejoinUsesRelaySessionRead(t *testing.T) {
	thread := appwire.Thread{
		ID:        "thread-atomic",
		SessionID: "thread-atomic",
		Source:    "local",
		Serf:      appwire.SerfThread{Ref: "local:thread-atomic"},
	}
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
	}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		relayLifecycleSource: relayLifecycleSource{thread: thread},
		lease:                lease,
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
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       thread.Serf.Ref,
		Subscribe: true,
	})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if response.Thread.ID != thread.ID {
		t.Fatalf("thread ID = %q, want %q", response.Thread.ID, thread.ID)
	}
	if got := source.legacyReadCallCount(); got != 0 {
		t.Fatalf("legacy ReadThread calls = %d, want no second upstream connection", got)
	}
	if got := lease.readCallCount(); got != 1 {
		t.Fatalf("RelaySession Read calls = %d, want 1", got)
	}
	if got := lease.listenCallCount(); got != 1 {
		t.Fatalf("RelaySession Listen calls = %d, want 1 Hub downstream owner", got)
	}
	select {
	case <-handoff.committed:
	default:
		t.Fatal("relay handoff was not committed after the thread/read response entered the downstream queue")
	}
	select {
	case <-handoff.aborted:
		t.Fatal("committed relay handoff was also aborted")
	default:
	}
}

func TestHubAtomicRejoinFansOutAndAcknowledgesAfterResponse(t *testing.T) {
	thread := appwire.Thread{
		ID:        "thread-delivery",
		SessionID: "thread-delivery",
		Source:    "local",
		Serf:      appwire.SerfThread{Ref: "local:thread-delivery"},
	}
	deliveries := make(chan appsource.RelayDelivery, 1)
	acknowledged := make(chan struct{})
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
		onCommit: func() {
			deliveries <- appsource.RelayDelivery{
				Notification: appwire.Notification{
					Method: appwire.NotifyAgentMessageDelta,
					Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
						ThreadID: thread.ID,
						Ref:      thread.Serf.Ref,
						TurnID:   "turn-delivery",
						ItemID:   "item-delivery",
						Delta:    "after snapshot",
					}),
				},
				Acknowledge: func() { close(acknowledged) },
			}
		},
	}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: deliveries,
	}
	source := &relaySessionTestSource{
		relayLifecycleSource: relayLifecycleSource{thread: thread},
		lease:                lease,
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
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       thread.Serf.Ref,
		Subscribe: true,
	}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if got := appServer.SubscriberCount("local:" + thread.ID); got != 1 {
		t.Fatalf("qualified relay subscriber count = %d, want 1", got)
	}
	notification := <-client.Notifications()
	if notification.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("notification method = %q, want %q", notification.Method, appwire.NotifyAgentMessageDelta)
	}
	<-acknowledged
}

func TestHubAtomicRejoinWritesResponseBeforePostCutNotification(t *testing.T) {
	thread := appwire.Thread{
		ID:        "thread-wire-order",
		SessionID: "thread-wire-order",
		Source:    "local",
		Serf:      appwire.SerfThread{Ref: "local:thread-wire-order"},
	}
	deliveries := make(chan appsource.RelayDelivery, 1)
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
		onCommit: func() {
			deliveries <- appsource.RelayDelivery{
				Notification: appwire.Notification{
					Method: appwire.NotifyAgentMessageDelta,
					Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
						ThreadID: thread.ID,
						Ref:      thread.Serf.Ref,
						TurnID:   "turn-wire-order",
						ItemID:   "item-wire-order",
						Delta:    "after snapshot",
					}),
				},
				Acknowledge: func() {},
			}
		},
	}
	source := &relaySessionTestSource{
		relayLifecycleSource: relayLifecycleSource{thread: thread},
		lease: &scriptedRelaySessionLease{
			readResult: appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: thread},
				Handoff:  handoff,
			},
			deliveries: deliveries,
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

	transport, err := appwire.DialWebSocket(context.Background(), "ws"+hub.URL[len("http"):]+"/rpc", hub.Client())
	if err != nil {
		t.Fatalf("dial hub rpc: %v", err)
	}
	client := appwire.NewClient(transport)
	type observedFrame struct {
		message appwire.Message
		err     error
	}
	frames := make(chan observedFrame, 3)
	client.SetOrderedFrameHandler(func(message appwire.Message, err error) {
		frames <- observedFrame{message: message, err: err}
	})
	client.Start(context.Background())
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if frame := <-frames; frame.err != nil || frame.message.Response == nil {
		t.Fatalf("initialize frame = %+v, want response", frame)
	}

	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       thread.Serf.Ref,
		Subscribe: true,
	}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	response := <-frames
	if response.err != nil || response.message.Response == nil {
		t.Fatalf("first hydration frame = %+v, want matching response", response)
	}
	notification := <-frames
	if notification.err != nil || notification.message.Notification == nil {
		t.Fatalf("second hydration frame = %+v, want post-cut notification", notification)
	}
	if notification.message.Notification.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("post-cut method = %q", notification.message.Notification.Method)
	}
}

func TestHubAtomicRejoinRejectsSnapshotWithoutLiveHandoff(t *testing.T) {
	thread := appwire.Thread{
		ID:     "thread-no-handoff",
		Source: "local",
		Serf:   appwire.SerfThread{Ref: "local:thread-no-handoff"},
	}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		relayLifecycleSource: relayLifecycleSource{thread: thread},
		lease:                lease,
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)

	_, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{
		Ref:       thread.Serf.Ref,
		Subscribe: true,
	})
	if err == nil {
		t.Fatal("atomic ThreadRead accepted a snapshot without a live subscribed continuation")
	}
}

func TestHubAtomicRejoinDoesNotConfirmSnapshotWhenLiveHandoffCannotCommit(t *testing.T) {
	thread := appwire.Thread{
		ID:        "thread-stale-handoff",
		SessionID: "thread-stale-handoff",
		Source:    "local",
		Serf:      appwire.SerfThread{Ref: "local:thread-stale-handoff"},
	}
	handoff := &recordingRelayHandoff{
		committed:    make(chan struct{}),
		aborted:      make(chan struct{}),
		refuseCommit: true,
	}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		relayLifecycleSource: relayLifecycleSource{thread: thread},
		lease:                lease,
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
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       thread.Serf.Ref,
		Subscribe: true,
	}); err == nil {
		t.Fatal("thread/read confirmed a snapshot after its live handoff refused to commit")
	}
	if got := appServer.SubscriberCount("local:" + thread.ID); got != 0 {
		t.Fatalf("subscriber count after failed handoff = %d, want capture withdrawn", got)
	}
}

func TestHubTurnStartPreparesCanonicalRelaySession(t *testing.T) {
	thread := appwire.Thread{
		ID:     "thread-turn-start",
		Source: "local",
		Serf:   appwire.SerfThread{Ref: "local:thread-turn-start"},
	}
	handoff := &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		relayLifecycleSource: relayLifecycleSource{thread: thread},
		lease:                lease,
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)

	if _, err := relays.startTurn(context.Background(), source, appwire.TurnStartParams{
		Ref:              thread.Serf.Ref,
		ThreadID:         thread.ID,
		ClientMutationID: "mutation-turn-start",
		Input:            []appwire.InputItem{{Type: "text", Text: "continue"}},
	}); err != nil {
		t.Fatalf("startTurn: %v", err)
	}
	if source.legacyReadCallCount() != 0 || source.legacySubscribeCallCount() != 0 {
		t.Fatalf(
			"legacy LocalDaemon relay calls: read=%d subscribe=%d, want neither",
			source.legacyReadCallCount(),
			source.legacySubscribeCallCount(),
		)
	}
	if lease.readCallCount() != 1 || lease.listenCallCount() != 1 {
		t.Fatalf("RelaySession calls: Read=%d Listen=%d, want one canonical feed", lease.readCallCount(), lease.listenCallCount())
	}
	select {
	case <-handoff.committed:
	default:
		t.Fatal("turn/start relay preparation did not commit its live continuation")
	}
}

func TestHubLifecycleRelaySetupUsesCanonicalRelaySession(t *testing.T) {
	thread := appwire.Thread{
		ID:     "thread-lifecycle",
		Source: "local",
		Serf:   appwire.SerfThread{Ref: "local:thread-lifecycle"},
	}
	handoff := &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		relayLifecycleSource: relayLifecycleSource{thread: thread},
		lease:                lease,
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		sources,
	)

	if err := relays.startRelayForThread(context.Background(), thread); err != nil {
		t.Fatalf("startRelayForThread: %v", err)
	}
	if source.legacyReadCallCount() != 0 || source.legacySubscribeCallCount() != 0 {
		t.Fatalf(
			"legacy LocalDaemon relay calls: read=%d subscribe=%d, want neither",
			source.legacyReadCallCount(),
			source.legacySubscribeCallCount(),
		)
	}
	if lease.readCallCount() != 1 || lease.listenCallCount() != 1 {
		t.Fatalf("RelaySession calls: Read=%d Listen=%d, want one canonical feed", lease.readCallCount(), lease.listenCallCount())
	}
}

func TestHubRelayIdleRetirementYieldsToConcurrentActorCommand(t *testing.T) {
	previousInterval := hubRelayIdleInterval
	hubRelayIdleInterval = time.Millisecond
	t.Cleanup(func() {
		hubRelayIdleInterval = previousInterval
	})

	thread := appwire.Thread{
		ID:     "thread-idle-command",
		Source: "local",
		Serf:   appwire.SerfThread{Ref: "local:thread-idle-command"},
	}
	handoff := &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		relayLifecycleSource: relayLifecycleSource{thread: thread},
		lease:                lease,
	}
	idleEntered := make(chan struct{})
	releaseIdle := make(chan struct{})
	var idleOnce sync.Once
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{RelayHooks: hubcore.RelayLifecycleHooks{
			IdleExit: func(string) {
				idleOnce.Do(func() {
					close(idleEntered)
					<-releaseIdle
				})
			},
		}},
		appsource.NewRegistry(),
	)
	params := appwire.ThreadReadParams{Ref: thread.Serf.Ref, Subscribe: true}
	first, err := relays.readThread(context.Background(), source, params)
	if err != nil {
		t.Fatalf("first readThread: %v", err)
	}
	first.finish(true)
	<-idleEntered

	second, err := relays.readThread(context.Background(), source, params)
	if err != nil {
		close(releaseIdle)
		t.Fatalf("second readThread: %v", err)
	}
	if got := lease.listenCallCount(); got != 1 {
		close(releaseIdle)
		t.Fatalf("RelaySession Listen calls = %d, want the in-flight command to retain the existing Hub owner", got)
	}
	second.finish(true)
	close(releaseIdle)
}

func TestHubRelayBlockedActorDoesNotBlockUnrelatedThread(t *testing.T) {
	blockedThread := appwire.Thread{
		ID:     "thread-blocked",
		Source: "local-a",
		Serf:   appwire.SerfThread{Ref: "local-a:thread-blocked"},
	}
	fastThread := appwire.Thread{
		ID:     "thread-fast",
		Source: "local-b",
		Serf:   appwire.SerfThread{Ref: "local-b:thread-fast"},
	}
	blockedReadEntered := make(chan struct{})
	releaseBlockedRead := make(chan struct{})
	blockedLease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: blockedThread},
			Handoff:  &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})},
		},
		readHook: func() {
			close(blockedReadEntered)
			<-releaseBlockedRead
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	fastLease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: fastThread},
			Handoff:  &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})},
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	blockedSource := &relaySessionTestSource{
		relayLifecycleSource: relayLifecycleSource{thread: blockedThread},
		id:                   "local-a",
		lease:                blockedLease,
	}
	fastSource := &relaySessionTestSource{
		relayLifecycleSource: relayLifecycleSource{thread: fastThread},
		id:                   "local-b",
		lease:                fastLease,
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)

	blockedResult := make(chan error, 1)
	go func() {
		read, err := relays.readThread(context.Background(), blockedSource, appwire.ThreadReadParams{
			Ref:       blockedThread.Serf.Ref,
			Subscribe: true,
		})
		if err == nil {
			read.finish(true)
		}
		blockedResult <- err
	}()
	<-blockedReadEntered

	fastRead, err := relays.readThread(context.Background(), fastSource, appwire.ThreadReadParams{
		Ref:       fastThread.Serf.Ref,
		Subscribe: true,
	})
	if err != nil {
		close(releaseBlockedRead)
		t.Fatalf("unrelated readThread: %v", err)
	}
	fastRead.finish(true)
	close(releaseBlockedRead)
	if err := <-blockedResult; err != nil {
		t.Fatalf("blocked readThread: %v", err)
	}
}

func TestHubAtomicRelayReadLetsDeletionWinAndAbortsHandoff(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	handoff := &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})}
	resumeLocks := hubcore.NewResumeLocks()
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        threadID,
				SessionID: threadID,
				Source:    "local",
				Serf:      appwire.SerfThread{Ref: ref},
			}},
			Handoff: handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
		readHook: func() {
			targetLock := resumeLocks.For(threadID)
			if !targetLock.TryLock() {
				t.Fatal("atomic relay held deletion ownership across upstream read")
			}
			defer targetLock.Unlock()
			if _, err := store.Begin("project-atomic-read-0123456789", []hubcore.DeletionTarget{{
				Ref:      ref,
				ThreadID: threadID,
			}}); err != nil {
				t.Fatal(err)
			}
		},
	}
	source := &relaySessionTestSource{
		relayLifecycleSource: relayLifecycleSource{thread: lease.readResult.Response.Thread},
		lease:                lease,
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{DeletionStore: store, ResumeLocks: resumeLocks},
		appsource.NewRegistry(),
	)

	if _, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{
		Ref:       ref,
		Subscribe: true,
	}); !isTargetDeletedError(err) {
		t.Fatalf("atomic read error = %T %v, want targetDeleted", err, err)
	}
	select {
	case <-handoff.aborted:
	default:
		t.Fatal("deletion-winning atomic read did not abort its live handoff")
	}
	select {
	case <-handoff.committed:
		t.Fatal("deletion-winning atomic read committed its handoff")
	default:
	}
}

type relaySessionTestSource struct {
	relayLifecycleSource
	id    string
	lease *scriptedRelaySessionLease

	mu              sync.Mutex
	acquireCalls    int
	legacyReadCalls int
	legacySubCalls  int
	startTurnCalls  int
}

func (s *relaySessionTestSource) ID() string {
	if s.id != "" {
		return s.id
	}
	return "local"
}

func (s *relaySessionTestSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	s.mu.Lock()
	s.legacyReadCalls++
	s.mu.Unlock()
	return appwire.ThreadReadResponse{}, errors.New("legacy ReadThread must not be used for an atomic LocalDaemon rejoin")
}

func (s *relaySessionTestSource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.legacySubCalls++
	s.mu.Unlock()
	return nil, errors.New("legacy SubscribeThread must not be used for a LocalDaemon relay")
}

func (s *relaySessionTestSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.mu.Lock()
	s.startTurnCalls++
	s.mu.Unlock()
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn-started"}}, nil
}

func (s *relaySessionTestSource) AcquireRelaySession(appwire.ThreadReadParams) (appsource.RelaySessionLease, error) {
	s.mu.Lock()
	s.acquireCalls++
	s.mu.Unlock()
	return s.lease, nil
}

func (s *relaySessionTestSource) legacyReadCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.legacyReadCalls
}

func (s *relaySessionTestSource) legacySubscribeCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.legacySubCalls
}

type scriptedRelaySessionLease struct {
	mu sync.Mutex

	readResult  appsource.RelayReadResult
	readErr     error
	readHook    func()
	deliveries  chan appsource.RelayDelivery
	listenErr   error
	readCalls   int
	listenCalls int
	closeCalls  int
}

func (l *scriptedRelaySessionLease) Read(context.Context, appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
	l.mu.Lock()
	l.readCalls++
	result, err, hook := l.readResult, l.readErr, l.readHook
	l.mu.Unlock()
	if hook != nil {
		hook()
	}
	return result, err
}

func (l *scriptedRelaySessionLease) Listen(context.Context) (<-chan appsource.RelayDelivery, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.listenCalls++
	return l.deliveries, l.listenErr
}

func (l *scriptedRelaySessionLease) Close() {
	l.mu.Lock()
	l.closeCalls++
	l.mu.Unlock()
}

func (l *scriptedRelaySessionLease) readCallCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.readCalls
}

func (l *scriptedRelaySessionLease) listenCallCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.listenCalls
}

type recordingRelayHandoff struct {
	once         sync.Once
	committed    chan struct{}
	aborted      chan struct{}
	onCommit     func()
	refuseCommit bool
}

func (h *recordingRelayHandoff) Commit() bool {
	won := false
	h.once.Do(func() {
		won = !h.refuseCommit
		close(h.committed)
		if h.onCommit != nil {
			h.onCommit()
		}
	})
	return won
}

func (h *recordingRelayHandoff) Abort() bool {
	won := false
	h.once.Do(func() {
		won = true
		close(h.aborted)
	})
	return won
}
