package hub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func TestHubRelayCanceledRecoveryDoesNotSubscribeAgain(t *testing.T) {
	const threadID = "th_stop_no_resubscribe"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := subscribeRelayRecovery(ctx, source, appwire.ThreadReadParams{Ref: "codex:" + threadID})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery subscribe error=%v, want context canceled", err)
	}
	select {
	case <-subscribeCalls:
		t.Fatal("canceled relay supervisor subscribed again")
	default:
	}
}

func TestHubRelayStopDuringInitializationCancelsSharedHandleAndAllowsFreshStart(t *testing.T) {
	const threadID = "th_stop_initializing"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	placeholderPublished := make(chan struct{})
	releaseInitializer := make(chan struct{})
	var placeholderOnce sync.Once
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool { return true }
	cfg.RelayHooks.AfterPlaceholder = func(gotThreadID string) {
		if gotThreadID == threadID {
			placeholderOnce.Do(func() { close(placeholderPublished) })
			<-releaseInitializer
		}
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelay(server, cfg, appsource.NewRegistry())

	startResults := make(chan error, 2)
	go func() {
		startResults <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-placeholderPublished:
	case <-time.After(time.Second):
		t.Fatal("initial relay placeholder was not published")
	}
	waiterJoined := make(chan struct{}, 1)
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { waiterJoined <- struct{}{} }
	defer func() { observeHubRelayWait = previousObserveWait }()
	go func() {
		startResults <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-waiterJoined:
	case <-time.After(time.Second):
		t.Fatal("concurrent initializer did not join shared relay handle")
	}

	relays.stopRelay("codex:" + threadID)
	select {
	case err := <-startResults:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shared stop error=%v, want context canceled", err)
		}
	case <-time.After(time.Second):
		close(releaseInitializer)
		awaitRelaySubscribeCall(t, subscribeCalls)
		initial := make(chan appwire.Notification)
		results <- relaySubscribeResult{notifications: initial}
		t.Fatal("stopRelay did not unblock initialization waiter")
	}
	close(releaseInitializer)
	select {
	case err := <-startResults:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("initial owner stop error=%v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial relay owner did not return shared cancellation")
	}
	select {
	case <-subscribeCalls:
		t.Fatal("canceled placeholder entered initial SubscribeThread")
	default:
	}

	observeHubRelayWait = previousObserveWait
	freshResult := make(chan error, 1)
	go func() {
		freshResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	freshNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: freshNotifications}
	select {
	case err := <-freshResult:
		if err != nil {
			t.Fatalf("fresh start after initialization stop: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh relay did not become ready after initialization stop")
	}
}

func TestHubRelayKeyStopKeepsCanonicalSiblingAndListener(t *testing.T) {
	const (
		rootRef  = "local:stop-root"
		childRef = "local:stop-child"
	)
	lease := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			ref, err := appwire.ParseRef(params.Ref)
			if err != nil {
				return appsource.RelayReadResult{}, err
			}
			return appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID: ref.ThreadID, Source: ref.SourceID,
					Evener: appwire.EvenerThread{Ref: params.Ref},
				}},
				Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
			}, nil
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		lease: lease,
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) {
			return appwire.ParseRef(rootRef)
		},
	}
	relays := newHubRelay(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	read := func(ref string) {
		t.Helper()
		result, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
		if err != nil {
			t.Fatalf("readThread(%q): %v", ref, err)
		}
		result.finish(false)
	}
	read(rootRef)
	read(childRef)
	if got := source.acquireCallCount(); got != 1 {
		t.Fatalf("initial relay acquisitions = %d, want one canonical handle", got)
	}

	relays.stopRelay(childRef)
	if got := lease.closeCallCount(); got != 0 {
		t.Fatalf("lease closes after child-key stop = %d, want 0 while root remains", got)
	}
	read(childRef)
	if got := source.acquireCallCount(); got != 1 {
		t.Fatalf("relay acquisitions after child rebind = %d, want existing canonical handle", got)
	}
	if got := lease.listenCallCount(); got != 1 {
		t.Fatalf("RelaySession Listen calls after child rebind = %d, want one retained listener", got)
	}
	relays.stopRelay(rootRef)
	relays.stopRelay(childRef)
}

func TestHubRelayKeyStopDefersFinalTeardownForInFlightCommand(t *testing.T) {
	const relayKey = "local:stop-in-flight"
	readEntered := make(chan struct{})
	releaseRead := make(chan struct{})
	leaseClosed := make(chan struct{})
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "stop-in-flight", Source: "local",
				Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		},
		readHook: func() {
			close(readEntered)
			<-releaseRead
		},
		deliveries: make(chan appsource.RelayDelivery),
		closeHook:  func() { close(leaseClosed) },
	}
	source := &relaySessionTestSource{lease: lease}
	relays := newHubRelay(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	type readOutcome struct {
		read *hubThreadReadResult
		err  error
	}
	readResult := make(chan readOutcome, 1)
	go func() {
		read, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
		readResult <- readOutcome{read: read, err: err}
	}()
	<-readEntered

	relays.stopRelay(relayKey)
	if got := relays.relayCommandCount(relayKey); got != 1 {
		close(releaseRead)
		t.Fatalf("command owners after deferred relay-key stop = %d, want 1", got)
	}
	if got := lease.closeCallCount(); got != 0 {
		close(releaseRead)
		t.Fatalf("lease closes while stopped key command is in flight = %d, want 0", got)
	}
	select {
	case <-leaseClosed:
		close(releaseRead)
		t.Fatal("final canonical lease closed before the in-flight command released")
	default:
	}

	close(releaseRead)
	result := <-readResult
	if result.err != nil {
		t.Fatalf("readThread: %v", result.err)
	}
	result.read.finish(false)
	select {
	case <-leaseClosed:
	case <-time.After(time.Second):
		t.Fatal("deferred relay-key stop did not close the final lease after command release")
	}
	if got := lease.closeCallCount(); got != 1 {
		t.Fatalf("final lease closes = %d, want 1", got)
	}
}

func TestHubRelayPostStopCommandWaitsForFreshGeneration(t *testing.T) {
	const relayKey = "local:stop-fresh-generation"
	readResult := func() appsource.RelayReadResult {
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "stop-fresh-generation", Source: "local",
				Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		}
	}
	oldReadEntered := make(chan struct{})
	releaseOldRead := make(chan struct{})
	oldLeaseClosed := make(chan struct{})
	var oldReadOnce sync.Once
	oldLease := &scriptedRelaySessionLease{
		readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			oldReadOnce.Do(func() {
				close(oldReadEntered)
				<-releaseOldRead
			})
			return readResult(), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
		closeHook:  func() { close(oldLeaseClosed) },
	}
	freshLease := &scriptedRelaySessionLease{
		readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			return readResult(), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	var acquireMu sync.Mutex
	acquisitions := 0
	source := &relaySessionTestSource{
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			defer acquireMu.Unlock()
			acquisitions++
			if acquisitions == 1 {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelay(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	type readOutcome struct {
		read *hubThreadReadResult
		err  error
	}
	startRead := func() <-chan readOutcome {
		out := make(chan readOutcome, 1)
		go func() {
			read, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- readOutcome{read: read, err: err}
		}()
		return out
	}
	oldResult := startRead()
	<-oldReadEntered
	relays.stopRelay(relayKey)

	postStopDeferred := make(chan struct{})
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { close(postStopDeferred) }
	t.Cleanup(func() { observeHubRelayWait = previousObserveWait })
	postStopResult := startRead()
	select {
	case <-postStopDeferred:
	case <-time.After(time.Second):
		close(releaseOldRead)
		t.Fatal("post-stop command joined the stopped generation instead of waiting")
	}
	if got := relays.relayCommandCount(relayKey); got != 1 {
		close(releaseOldRead)
		t.Fatalf("stopped generation command owners = %d, want only the original command", got)
	}
	if got := oldLease.readCallCount(); got != 1 {
		close(releaseOldRead)
		t.Fatalf("stopped generation Read calls = %d, want 1", got)
	}
	if got := oldLease.closeCallCount(); got != 0 {
		close(releaseOldRead)
		t.Fatalf("old lease closes while original command is in flight = %d, want 0", got)
	}

	close(releaseOldRead)
	old := <-oldResult
	if old.err != nil {
		t.Fatalf("old readThread: %v", old.err)
	}
	old.read.finish(false)
	select {
	case <-oldLeaseClosed:
	case <-time.After(time.Second):
		t.Fatal("stopped generation did not close after its original command released")
	}
	postStop := <-postStopResult
	if postStop.err != nil {
		t.Fatalf("post-stop readThread: %v", postStop.err)
	}
	postStop.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("relay acquisitions after stopped generation retired = %d, want fresh second generation", got)
	}
	if got := oldLease.readCallCount(); got != 1 {
		t.Fatalf("old generation Read calls after fresh read = %d, want 1", got)
	}
	if got := freshLease.readCallCount(); got != 1 || freshLease.listenCallCount() != 1 {
		t.Fatalf("fresh generation calls: Read=%d Listen=%d, want 1/1", got, freshLease.listenCallCount())
	}

	rejoined, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
	if err != nil {
		t.Fatalf("fresh generation rejoin: %v", err)
	}
	rejoined.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("relay acquisitions after fresh generation rejoin = %d, want 2", got)
	}
	if got := freshLease.listenCallCount(); got != 1 {
		t.Fatalf("fresh generation Listen calls after rejoin = %d, want 1", got)
	}
	relays.stopRelay(relayKey)
}

func TestHubRelayStopCoversOverlappingPendingGenerations(t *testing.T) {
	const relayKey = "local:overlapping-pending-stop"
	canonicalA := appwire.Ref{SourceID: "local", ThreadID: "overlapping-canonical-a"}
	canonicalB := appwire.Ref{SourceID: "local", ThreadID: "overlapping-canonical-b"}
	readResult := func() appsource.RelayReadResult {
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "overlapping-pending-stop", Source: "local",
				Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		}
	}
	type heldLease struct {
		lease   *scriptedRelaySessionLease
		entered chan struct{}
		release func()
		closed  chan struct{}
	}
	newHeldLease := func() heldLease {
		entered := make(chan struct{})
		releaseRead := make(chan struct{})
		closed := make(chan struct{})
		var releaseOnce sync.Once
		lease := &scriptedRelaySessionLease{
			readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
				close(entered)
				<-releaseRead
				return readResult(), nil
			},
			deliveries: make(chan appsource.RelayDelivery),
			closeHook:  func() { close(closed) },
		}
		return heldLease{
			lease:   lease,
			entered: entered,
			release: func() { releaseOnce.Do(func() { close(releaseRead) }) },
			closed:  closed,
		}
	}
	heldA := newHeldLease()
	heldB := newHeldLease()
	defer heldA.release()
	defer heldB.release()
	freshLease := &scriptedRelaySessionLease{
		readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			return readResult(), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	var resolveMu sync.Mutex
	resolved := canonicalA
	acquisitionsA := 0
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) {
			resolveMu.Lock()
			defer resolveMu.Unlock()
			return resolved, nil
		},
		acquireRelay: func(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			if ref == canonicalB {
				return routeAwareTestLease(heldB.lease), nil
			}
			acquisitionsA++
			if acquisitionsA == 1 {
				return routeAwareTestLease(heldA.lease), nil
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelay(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	type readOutcome struct {
		read *hubThreadReadResult
		err  error
	}
	startRead := func() <-chan readOutcome {
		out := make(chan readOutcome, 1)
		go func() {
			read, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- readOutcome{read: read, err: err}
		}()
		return out
	}
	readA := startRead()
	<-heldA.entered
	resolveMu.Lock()
	resolved = canonicalB
	resolveMu.Unlock()
	readB := startRead()
	<-heldB.entered
	if got := relays.relayCommandCount(relayKey); got != 2 {
		t.Fatalf("overlapping pending command owners = %d, want 2", got)
	}

	relays.stopRelay(relayKey)
	if got := relays.relayCommandCount(relayKey); got != 2 {
		t.Fatalf("stopped overlapping pending command owners = %d, want 2", got)
	}
	resolveMu.Lock()
	resolved = canonicalA
	resolveMu.Unlock()
	deferred := make(chan struct{}, 3)
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { deferred <- struct{}{} }
	t.Cleanup(func() { observeHubRelayWait = previousObserveWait })
	postStop := startRead()
	select {
	case <-deferred:
	case <-time.After(time.Second):
		t.Fatal("post-stop command did not defer behind the older hidden pending generation")
	}
	if got := heldA.lease.readCallCount(); got != 1 {
		t.Fatalf("older stopped pending Read calls = %d, want 1", got)
	}
	if got := heldB.lease.readCallCount(); got != 1 {
		t.Fatalf("newer stopped pending Read calls = %d, want 1", got)
	}

	heldA.release()
	resultA := <-readA
	if resultA.err != nil {
		t.Fatalf("older pending read: %v", resultA.err)
	}
	if relays.relayPublished(relayKey) {
		t.Fatal("older stopped pending read published downstream ownership")
	}
	resultA.read.finish(false)
	select {
	case <-heldA.closed:
	case <-time.After(time.Second):
		t.Fatal("older stopped pending handle did not close after its own command released")
	}
	select {
	case <-deferred:
	case <-time.After(time.Second):
		t.Fatal("post-stop command did not remain deferred behind the newer pending generation")
	}
	if got := relays.relayCommandCount(relayKey); got != 1 {
		t.Fatalf("pending command owners after older release = %d, want newer owner only", got)
	}
	if got := heldB.lease.closeCallCount(); got != 0 {
		t.Fatalf("newer pending lease closes before its command release = %d, want 0", got)
	}

	heldB.release()
	resultB := <-readB
	if resultB.err != nil {
		t.Fatalf("newer pending read: %v", resultB.err)
	}
	if relays.relayPublished(relayKey) {
		t.Fatal("newer stopped pending read published downstream ownership")
	}
	resultB.read.finish(false)
	select {
	case <-heldB.closed:
	case <-time.After(time.Second):
		t.Fatal("newer stopped pending handle did not close after its own command released")
	}
	fresh := <-postStop
	if fresh.err != nil {
		t.Fatalf("fresh post-stop read: %v", fresh.err)
	}
	if !relays.relayPublished(relayKey) {
		t.Fatal("fresh post-stop generation did not publish downstream ownership")
	}
	fresh.read.finish(false)
	if got := source.acquireCallCount(); got != 3 {
		t.Fatalf("relay acquisitions after overlapping stop = %d, want two old plus one fresh", got)
	}
	if got := freshLease.readCallCount(); got != 1 || freshLease.listenCallCount() != 1 {
		t.Fatalf("fresh generation calls: Read=%d Listen=%d, want 1/1", got, freshLease.listenCallCount())
	}
	if got := heldA.lease.readCallCount(); got != 1 {
		t.Fatalf("older stale generation Read calls = %d, want no post-stop join", got)
	}
	if got := heldB.lease.readCallCount(); got != 1 {
		t.Fatalf("newer stale generation Read calls = %d, want no post-stop join", got)
	}

	rejoined, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
	if err != nil {
		t.Fatalf("fresh generation rejoin: %v", err)
	}
	rejoined.finish(false)
	if got := source.acquireCallCount(); got != 3 {
		t.Fatalf("relay acquisitions after fresh rejoin = %d, want 3", got)
	}
	if got := freshLease.listenCallCount(); got != 1 {
		t.Fatalf("fresh generation listeners after rejoin = %d, want 1", got)
	}
	relays.stopRelay(relayKey)
}

func TestHubRelayCanonicalStopRetiresOnlyNamedHandle(t *testing.T) {
	canonicalA := appwire.Ref{SourceID: "local", ThreadID: "stop-canonical-a"}
	canonicalB := appwire.Ref{SourceID: "local", ThreadID: "stop-canonical-b"}
	newLease := func() *scriptedRelaySessionLease {
		return &scriptedRelaySessionLease{
			readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
				ref, err := appwire.ParseRef(params.Ref)
				if err != nil {
					return appsource.RelayReadResult{}, err
				}
				return appsource.RelayReadResult{
					Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
						ID: ref.ThreadID, Source: ref.SourceID,
						Evener: appwire.EvenerThread{Ref: params.Ref},
					}},
					Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
				}, nil
			},
			deliveries: make(chan appsource.RelayDelivery),
		}
	}
	leaseA := newLease()
	leaseB := newLease()
	source := &relaySessionTestSource{
		resolveRelay: func(params appwire.ThreadReadParams) (appwire.Ref, error) {
			if params.Ref == canonicalB.String() {
				return canonicalB, nil
			}
			return canonicalA, nil
		},
		acquireRelay: func(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			if ref == canonicalB {
				return routeAwareTestLease(leaseB), nil
			}
			return routeAwareTestLease(leaseA), nil
		},
	}
	relays := newHubRelay(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	read := func(ref string) {
		t.Helper()
		result, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
		if err != nil {
			t.Fatalf("readThread(%q): %v", ref, err)
		}
		result.finish(false)
	}
	read(canonicalA.String())
	read(canonicalB.String())

	relays.stopCanonicalRelay(canonicalA)
	if got := leaseA.closeCallCount(); got != 1 {
		t.Fatalf("named canonical lease closes = %d, want 1", got)
	}
	if got := leaseB.closeCallCount(); got != 0 {
		t.Fatalf("unrelated canonical lease closes = %d, want 0", got)
	}
	read(canonicalB.String())
	if got := leaseB.listenCallCount(); got != 1 {
		t.Fatalf("unrelated canonical listener starts = %d, want retained single listener", got)
	}
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("acquisitions after unrelated read = %d, want two original canonical handles", got)
	}
	relays.stopCanonicalRelay(canonicalB)
}

func TestHubRelayCanonicalStopRetainsBusyPublishedHandleUntilFreshGeneration(t *testing.T) {
	const relayKey = "local:canonical-stop-busy"
	canonical := appwire.Ref{SourceID: "local", ThreadID: "canonical-stop-busy"}
	readResult := func() appsource.RelayReadResult {
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "canonical-stop-busy", Source: "local", Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		}
	}
	oldClosed := make(chan struct{})
	oldLease := &scriptedRelaySessionLease{
		readFunc:   func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) { return readResult(), nil },
		deliveries: make(chan appsource.RelayDelivery),
		closeHook:  func() { close(oldClosed) },
	}
	freshLease := &scriptedRelaySessionLease{
		readFunc:   func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) { return readResult(), nil },
		deliveries: make(chan appsource.RelayDelivery),
	}
	var acquireMu sync.Mutex
	acquisitions := 0
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) { return canonical, nil },
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			defer acquireMu.Unlock()
			acquisitions++
			if acquisitions == 1 {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelay(
		appserver.NewServer(appserver.ServerConfig{ServerName: "canonical-stop-busy", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	initial, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	initial.finish(false)

	busyEntered := make(chan struct{})
	releaseBusy := make(chan struct{})
	oldLease.mu.Lock()
	oldLease.readHook = func() {
		close(busyEntered)
		<-releaseBusy
	}
	oldLease.mu.Unlock()
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	startRead := func() <-chan outcome {
		out := make(chan outcome, 1)
		go func() {
			read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- outcome{read: read, err: err}
		}()
		return out
	}
	busyResult := startRead()
	<-busyEntered
	relays.stopCanonicalRelay(canonical)
	if got := oldLease.closeCallCount(); got != 0 {
		close(releaseBusy)
		t.Fatalf("canonical stop closed busy published lease: %d", got)
	}
	if got := relays.relayCommandCount(relayKey); got != 1 {
		close(releaseBusy)
		t.Fatalf("busy published command owners after canonical stop = %d, want 1", got)
	}

	deferred := make(chan struct{})
	var deferredOnce sync.Once
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { deferredOnce.Do(func() { close(deferred) }) }
	t.Cleanup(func() { observeHubRelayWait = previousObserveWait })
	postStopResult := startRead()
	select {
	case <-deferred:
	case <-time.After(time.Second):
		close(releaseBusy)
		t.Fatal("post-canonical-stop command did not defer behind busy handle")
	}
	if got := oldLease.readCallCount(); got != 2 {
		close(releaseBusy)
		t.Fatalf("old generation Read calls after stop = %d, want initial plus busy only", got)
	}
	close(releaseBusy)
	busy := <-busyResult
	if busy.err != nil {
		t.Fatal(busy.err)
	}
	if got := oldLease.closeCallCount(); got != 0 {
		t.Fatalf("canonical stop closed lease before busy handoff released: %d", got)
	}
	busy.read.finish(false)
	select {
	case <-oldClosed:
	case <-time.After(time.Second):
		t.Fatal("busy canonical generation did not close after exact owner release")
	}
	postStop := <-postStopResult
	if postStop.err != nil {
		t.Fatal(postStop.err)
	}
	postStop.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("acquisitions after canonical stop drain = %d, want fresh generation", got)
	}
	if got := oldLease.closeCallCount(); got != 1 {
		t.Fatalf("old canonical lease closes = %d, want once", got)
	}
	if got := freshLease.readCallCount(); got != 1 || freshLease.listenCallCount() != 1 {
		t.Fatalf("fresh canonical calls: Read=%d Listen=%d, want 1/1", got, freshLease.listenCallCount())
	}
}

func TestHubRelayCanonicalStopRetainsOverlappingPendingStates(t *testing.T) {
	const (
		relayA = "local:canonical-stop-pending-a"
		relayB = "local:canonical-stop-pending-b"
	)
	canonical := appwire.Ref{SourceID: "local", ThreadID: "canonical-stop-pending-owner"}
	type gate struct {
		entered chan struct{}
		release chan struct{}
	}
	gates := map[string]gate{
		relayA: {entered: make(chan struct{}), release: make(chan struct{})},
		relayB: {entered: make(chan struct{}), release: make(chan struct{})},
	}
	resultFor := func(ref string) appsource.RelayReadResult {
		parsed, err := appwire.ParseRef(ref)
		if err != nil {
			t.Fatal(err)
		}
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: parsed.ThreadID, Source: parsed.SourceID, Evener: appwire.EvenerThread{Ref: ref},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		}
	}
	oldClosed := make(chan struct{})
	oldLease := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			g := gates[params.Ref]
			close(g.entered)
			<-g.release
			return resultFor(params.Ref), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
		closeHook:  func() { close(oldClosed) },
	}
	freshLease := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			return resultFor(params.Ref), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	var acquisitions int
	var acquireMu sync.Mutex
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) { return canonical, nil },
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			defer acquireMu.Unlock()
			acquisitions++
			if acquisitions == 1 {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelay(
		appserver.NewServer(appserver.ServerConfig{ServerName: "canonical-stop-pending", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	startRead := func(ref string) <-chan outcome {
		out := make(chan outcome, 1)
		go func() {
			read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
			out <- outcome{read: read, err: err}
		}()
		return out
	}
	resultA := startRead(relayA)
	<-gates[relayA].entered
	resultB := startRead(relayB)
	<-gates[relayB].entered
	if got := relays.relayCommandCount(relayA) + relays.relayCommandCount(relayB); got != 2 {
		t.Fatalf("overlapping canonical pending owners = %d, want 2", got)
	}
	relays.stopCanonicalRelay(canonical)
	if got := oldLease.closeCallCount(); got != 0 {
		t.Fatalf("canonical stop closed overlapping pending lease: %d", got)
	}

	deferred := make(chan struct{})
	var deferredOnce sync.Once
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { deferredOnce.Do(func() { close(deferred) }) }
	t.Cleanup(func() { observeHubRelayWait = previousObserveWait })
	postStop := startRead(relayA)
	select {
	case <-deferred:
	case <-time.After(time.Second):
		close(gates[relayA].release)
		close(gates[relayB].release)
		t.Fatal("post-stop command did not defer behind overlapping pending canonical states")
	}

	close(gates[relayA].release)
	first := <-resultA
	if first.err != nil {
		t.Fatal(first.err)
	}
	if relays.relayPublished(relayA) {
		t.Fatal("stopped pending relay A published")
	}
	first.read.finish(false)
	if got := oldLease.closeCallCount(); got != 0 {
		t.Fatalf("canonical lease closed with second pending owner: %d", got)
	}
	close(gates[relayB].release)
	second := <-resultB
	if second.err != nil {
		t.Fatal(second.err)
	}
	if relays.relayPublished(relayB) {
		t.Fatal("stopped pending relay B published")
	}
	second.read.finish(false)
	select {
	case <-oldClosed:
	case <-time.After(time.Second):
		t.Fatal("overlapping pending canonical handle did not close after all exact owners released")
	}
	fresh := <-postStop
	if fresh.err != nil {
		t.Fatal(fresh.err)
	}
	fresh.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("canonical acquisitions after overlapping drain = %d, want fresh second generation", got)
	}
	if got := oldLease.closeCallCount(); got != 1 {
		t.Fatalf("old overlapping lease closes = %d, want once", got)
	}
}

func TestHubRelayCanonicalStopWaitsForInitializingAcquire(t *testing.T) {
	const relayKey = "local:canonical-stop-initializing"
	canonical := appwire.Ref{SourceID: "local", ThreadID: "canonical-stop-initializing"}
	readResult := appsource.RelayReadResult{
		Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: canonical.ThreadID, Source: canonical.SourceID, Evener: appwire.EvenerThread{Ref: relayKey},
		}},
		Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
	}
	firstAcquireEntered := make(chan struct{})
	releaseFirstAcquire := make(chan struct{})
	secondAcquireEntered := make(chan struct{})
	oldLease := &scriptedRelaySessionLease{readResult: readResult, deliveries: make(chan appsource.RelayDelivery)}
	freshLease := &scriptedRelaySessionLease{readResult: readResult, deliveries: make(chan appsource.RelayDelivery)}
	var acquireMu sync.Mutex
	acquisitions := 0
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) { return canonical, nil },
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			acquisitions++
			call := acquisitions
			acquireMu.Unlock()
			if call == 1 {
				close(firstAcquireEntered)
				<-releaseFirstAcquire
				return routeAwareTestLease(oldLease), nil
			}
			close(secondAcquireEntered)
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelay(
		appserver.NewServer(appserver.ServerConfig{ServerName: "canonical-stop-initializing", SourceID: "local"}),
		hubcore.WebConfig{}, appsource.NewRegistry(),
	)
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	start := func() <-chan outcome {
		out := make(chan outcome, 1)
		go func() {
			read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- outcome{read: read, err: err}
		}()
		return out
	}
	initial := start()
	<-firstAcquireEntered
	relays.stopCanonicalRelay(canonical)
	waiting := make(chan struct{})
	var waitingOnce sync.Once
	previousWait := observeHubRelayWait
	observeHubRelayWait = func() { waitingOnce.Do(func() { close(waiting) }) }
	t.Cleanup(func() { observeHubRelayWait = previousWait })
	later := start()
	select {
	case <-waiting:
	case <-secondAcquireEntered:
		close(releaseFirstAcquire)
		t.Fatal("post-stop join acquired a fresh lease before stopped initializing acquire drained")
	case <-time.After(time.Second):
		close(releaseFirstAcquire)
		t.Fatal("post-stop join did not reach initializing handle drain wait")
	}
	select {
	case <-secondAcquireEntered:
		close(releaseFirstAcquire)
		t.Fatal("fresh acquisition started while stopped initializing acquire remained blocked")
	default:
	}
	close(releaseFirstAcquire)
	first := <-initial
	if !errors.Is(first.err, context.Canceled) {
		if first.read != nil {
			first.read.finish(false)
		}
		t.Fatalf("stopped initializing read error = %v, want context.Canceled", first.err)
	}
	if got := oldLease.closeCallCount(); got != 1 {
		t.Fatalf("late old lease closes = %d, want 1", got)
	}
	if got := oldLease.listenCallCount(); got != 0 {
		t.Fatalf("late old lease Listen calls = %d, want 0 after stop", got)
	}
	select {
	case <-secondAcquireEntered:
	case <-time.After(time.Second):
		t.Fatal("fresh acquisition did not start after initializing handle drained")
	}
	next := <-later
	if next.err != nil {
		t.Fatal(next.err)
	}
	next.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("acquisitions after initializing stop = %d, want old plus one fresh", got)
	}
	if got := freshLease.listenCallCount(); got != 1 {
		t.Fatalf("fresh listener starts = %d, want 1", got)
	}
}

func TestHubRelayCanonicalStopInitializingAcquireErrorUnblocksFreshGeneration(t *testing.T) {
	const relayKey = "local:canonical-stop-initializing-error"
	canonical := appwire.Ref{SourceID: "local", ThreadID: "canonical-stop-initializing-error"}
	firstAcquireEntered := make(chan struct{})
	releaseFirstAcquire := make(chan struct{})
	acquireFailure := errors.New("initial relay acquisition failed")
	freshLease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: canonical.ThreadID, Source: canonical.SourceID, Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	var acquireMu sync.Mutex
	acquisitions := 0
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) { return canonical, nil },
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			acquisitions++
			call := acquisitions
			acquireMu.Unlock()
			if call == 1 {
				close(firstAcquireEntered)
				<-releaseFirstAcquire
				return nil, acquireFailure
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelay(
		appserver.NewServer(appserver.ServerConfig{ServerName: "canonical-stop-initializing-error", SourceID: "local"}),
		hubcore.WebConfig{}, appsource.NewRegistry(),
	)
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	start := func() <-chan outcome {
		out := make(chan outcome, 1)
		go func() {
			read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- outcome{read: read, err: err}
		}()
		return out
	}
	initial := start()
	<-firstAcquireEntered
	relays.stopCanonicalRelay(canonical)
	later := start()
	close(releaseFirstAcquire)
	first := <-initial
	if !errors.Is(first.err, acquireFailure) {
		t.Fatalf("initial acquisition error = %v, want %v", first.err, acquireFailure)
	}
	next := <-later
	if next.err != nil {
		t.Fatal(next.err)
	}
	next.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("acquisitions after stopped initialization error = %d, want 2", got)
	}
}

type blockingInitializingListenLease struct {
	*scriptedRelaySessionLease
	listenEntered  chan struct{}
	listenCanceled chan struct{}
	releaseListen  chan struct{}
}

func (l *blockingInitializingListenLease) Listen(ctx context.Context) (<-chan appsource.RelayDelivery, error) {
	close(l.listenEntered)
	<-ctx.Done()
	close(l.listenCanceled)
	<-l.releaseListen
	return nil, ctx.Err()
}

func TestHubRelayCanonicalStopRetainsInitializingHandleUntilListenCancellationReturns(t *testing.T) {
	const relayKey = "local:canonical-stop-initializing-listen"
	canonical := appwire.Ref{SourceID: "local", ThreadID: "canonical-stop-initializing-listen"}
	oldBase := &scriptedRelaySessionLease{deliveries: make(chan appsource.RelayDelivery)}
	oldLease := &blockingInitializingListenLease{
		scriptedRelaySessionLease: oldBase,
		listenEntered:             make(chan struct{}),
		listenCanceled:            make(chan struct{}),
		releaseListen:             make(chan struct{}),
	}
	freshLease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: canonical.ThreadID, Source: canonical.SourceID, Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	var acquireMu sync.Mutex
	acquisitions := 0
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) { return canonical, nil },
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			defer acquireMu.Unlock()
			acquisitions++
			if acquisitions == 1 {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelay(
		appserver.NewServer(appserver.ServerConfig{ServerName: "canonical-stop-initializing-listen", SourceID: "local"}),
		hubcore.WebConfig{}, appsource.NewRegistry(),
	)
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	start := func() <-chan outcome {
		out := make(chan outcome, 1)
		go func() {
			read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- outcome{read: read, err: err}
		}()
		return out
	}
	initial := start()
	<-oldLease.listenEntered
	relays.stopCanonicalRelay(canonical)
	<-oldLease.listenCanceled
	if got := oldLease.closeCallCount(); got != 0 {
		close(oldLease.releaseListen)
		t.Fatalf("initializing lease closed before Listen cancellation returned: %d", got)
	}
	waiting := make(chan struct{})
	var waitOnce sync.Once
	previousWait := observeHubRelayWait
	observeHubRelayWait = func() { waitOnce.Do(func() { close(waiting) }) }
	t.Cleanup(func() { observeHubRelayWait = previousWait })
	later := start()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		close(oldLease.releaseListen)
		t.Fatal("post-stop join did not wait for initializing Listen termination")
	}
	close(oldLease.releaseListen)
	first := <-initial
	if !errors.Is(first.err, context.Canceled) {
		t.Fatalf("stopped initializing Listen error = %v, want context.Canceled", first.err)
	}
	if got := oldLease.closeCallCount(); got != 1 {
		t.Fatalf("stopped initializing Listen lease closes = %d, want 1", got)
	}
	next := <-later
	if next.err != nil {
		t.Fatal(next.err)
	}
	next.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("acquisitions after Listen drain = %d, want old plus one fresh", got)
	}
}

func TestHubRelayInitiatingRequestCancellationStopsInitialSubscribeAndAllowsFreshStart(t *testing.T) {
	const threadID = "th_request_canceled_initializing"
	initialRelease := make(chan struct{})
	var releaseInitial sync.Once
	source := &initialRequestCancelRelaySource{
		thread:             codexRelayThread(threadID),
		initialStarted:     make(chan struct{}),
		initialCanceled:    make(chan struct{}),
		initialRelease:     initialRelease,
		initialReturned:    make(chan struct{}),
		freshStarted:       make(chan struct{}),
		freshNotifications: make(chan appwire.Notification),
	}
	registrations := make(chan struct{}, 2)
	supervisors := make(chan struct{}, 2)
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool {
		registrations <- struct{}{}
		return true
	}
	cfg.RelayHooks.BeforeSupervisor = func(gotThreadID string) {
		if gotThreadID == threadID {
			supervisors <- struct{}{}
		}
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelay(server, cfg, appsource.NewRegistry())
	defer releaseInitial.Do(func() { close(initialRelease) })
	defer relays.stopRelay("codex:" + threadID)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	startResults := make(chan error, 2)
	go func() {
		startResults <- relays.startRelay(requestCtx, source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-source.initialStarted:
	case <-time.After(time.Second):
		t.Fatal("initial SubscribeThread did not start")
	}

	waiterJoined := make(chan struct{}, 1)
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { waiterJoined <- struct{}{} }
	defer func() { observeHubRelayWait = previousObserveWait }()
	go func() {
		startResults <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-waiterJoined:
	case <-time.After(time.Second):
		t.Fatal("concurrent requester did not join the initializing relay")
	}

	cancelRequest()
	select {
	case <-source.initialCanceled:
	case <-time.After(time.Second):
		t.Fatal("initiating request cancellation did not cancel initial SubscribeThread")
	}
	select {
	case err := <-startResults:
		t.Fatalf("relay returned before canceled initial SubscribeThread joined: %v", err)
	default:
	}
	select {
	case <-registrations:
		t.Fatal("canceled initializer registered a downstream subscription")
	default:
	}
	select {
	case <-supervisors:
		t.Fatal("canceled initializer launched a supervisor")
	default:
	}

	observeHubRelayWait = previousObserveWait
	freshResult := make(chan error, 1)
	go func() {
		freshResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-source.freshStarted:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not start after requester-canceled initialization")
	}
	select {
	case <-registrations:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not register downstream")
	}
	select {
	case <-supervisors:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not launch a supervisor")
	}
	select {
	case err := <-freshResult:
		if err != nil {
			t.Fatalf("fresh replacement start: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not become ready")
	}

	releaseInitial.Do(func() { close(initialRelease) })
	select {
	case <-source.initialReturned:
	case <-time.After(time.Second):
		t.Fatal("canceled initial SubscribeThread did not return")
	}
	for range 2 {
		select {
		case err := <-startResults:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("shared initialization result=%v, want context canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("initializer and waiter did not receive shared cancellation")
		}
	}
	select {
	case <-registrations:
		t.Fatal("canceled initializer registered downstream after SubscribeThread returned")
	default:
	}
	select {
	case <-supervisors:
		t.Fatal("canceled initializer launched a supervisor after SubscribeThread returned")
	default:
	}
}

func TestHubRelaySurvivesInitiatingRequestCancellationAfterAttachment(t *testing.T) {
	const threadID = "th_request_canceled_established"
	notifications := make(chan appwire.Notification)
	source := &relayBroadcastSource{
		thread:        codexRelayThread(threadID),
		notifications: notifications,
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool { return true }
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelay(server, cfg, appsource.NewRegistry())
	defer relays.stopRelay("codex:" + threadID)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	if err := relays.startRelay(requestCtx, source, appwire.ThreadReadParams{}, source.thread); err != nil {
		t.Fatalf("start relay: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	cancelRequest()
	select {
	case <-source.canceled:
		t.Fatal("established relay was canceled with its initiating request")
	default:
	}

	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread); err != nil {
		t.Fatalf("join established relay after initiating request cancellation: %v", err)
	}
	select {
	case <-source.subscribed:
		t.Fatal("initiating request cancellation caused a replacement upstream subscription")
	default:
	}
	notifications <- relayDeltaNotification(t, threadID, "survived request cancellation")
}

func TestHubRelayStoppedInitializerRejectsSuccessfulSubscribeAndLeavesReplacement(t *testing.T) {
	const threadID = "th_stale_initializer"
	oldNotifications := make(chan appwire.Notification)
	freshNotifications := make(chan appwire.Notification)
	source := &successfulAfterCancelRelaySource{
		thread:             codexRelayThread(threadID),
		initialStarted:     make(chan struct{}),
		initialCanceled:    make(chan struct{}),
		releaseInitial:     make(chan struct{}),
		initialReturned:    make(chan struct{}),
		oldNotifications:   oldNotifications,
		freshStarted:       make(chan struct{}),
		freshNotifications: freshNotifications,
	}
	registrationCalls := make(chan struct{}, 2)
	supervisorStarts := make(chan struct{}, 2)
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool {
		registrationCalls <- struct{}{}
		return true
	}
	cfg.RelayHooks.BeforeSupervisor = func(gotThreadID string) {
		if gotThreadID == threadID {
			supervisorStarts <- struct{}{}
		}
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelay(server, cfg, appsource.NewRegistry())

	oldResult := make(chan error, 1)
	go func() {
		oldResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-source.initialStarted:
	case <-time.After(time.Second):
		t.Fatal("initial SubscribeThread did not start")
	}
	relays.stopRelay("codex:" + threadID)
	select {
	case <-source.initialCanceled:
	case <-time.After(time.Second):
		t.Fatal("stopped initializer source context was not canceled")
	}

	freshResult := make(chan error, 1)
	go func() {
		freshResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-source.freshStarted:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement SubscribeThread did not start")
	}
	select {
	case <-registrationCalls:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not register downstream")
	}
	select {
	case err := <-freshResult:
		if err != nil {
			t.Fatalf("fresh replacement start: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not become ready")
	}
	select {
	case <-supervisorStarts:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement supervisor did not start")
	}

	close(source.releaseInitial)
	select {
	case <-source.initialReturned:
	case <-time.After(time.Second):
		t.Fatal("stale initial SubscribeThread did not return success")
	}
	select {
	case err := <-oldResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stale initializer error=%v, want shared context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale initializer did not return shared cancellation")
	}
	select {
	case <-registrationCalls:
		t.Fatal("stale initializer registered downstream after losing ownership")
	default:
	}
	select {
	case <-supervisorStarts:
		t.Fatal("stale initializer started a supervisor after losing ownership")
	default:
	}

	freshNotifications <- relayDeltaNotification(t, threadID, "fresh remains live")
}

func TestHubRelayStopImmediatelyAfterReadinessAllowsFreshStart(t *testing.T) {
	const threadID = "th_stop_ready"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	readyReached := make(chan struct{})
	releaseReady := make(chan struct{})
	blockFirstReady := make(chan struct{}, 1)
	blockFirstReady <- struct{}{}
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool { return true }
	cfg.RelayHooks.AfterReady = func(gotThreadID string) {
		if gotThreadID != threadID {
			return
		}
		select {
		case <-blockFirstReady:
			close(readyReached)
			<-releaseReady
		default:
		}
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelay(server, cfg, appsource.NewRegistry())
	initialResult := make(chan error, 1)
	go func() {
		initialResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	initialNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: initialNotifications}
	select {
	case <-readyReached:
	case <-time.After(time.Second):
		t.Fatal("initial relay did not reach readiness boundary")
	}

	relays.stopRelay("codex:" + threadID)
	freshResult := make(chan error, 1)
	go func() {
		freshResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-subscribeCalls:
	case err := <-freshResult:
		close(releaseReady)
		t.Fatalf("fresh start returned without subscribing after ready stop: %v", err)
	case <-time.After(time.Second):
		close(releaseReady)
		t.Fatal("fresh start did not replace ready stopped relay")
	}
	freshNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: freshNotifications}
	select {
	case err := <-freshResult:
		if err != nil {
			t.Fatalf("fresh start after ready stop: %v", err)
		}
	case <-time.After(time.Second):
		close(releaseReady)
		t.Fatal("fresh relay did not become ready after ready stop")
	}
	close(releaseReady)
	select {
	case err := <-initialResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("initial ready relay stop error=%v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial ready start did not return shared cancellation")
	}
}

func TestHubRelayStopBeforeLaunchCommitPreventsSupervisorAndAllowsFreshStart(t *testing.T) {
	const threadID = "th_stop_launch_commit"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	launchBoundary := make(chan struct{})
	releaseLaunch := make(chan struct{})
	launches := make(chan struct{}, 2)
	blockFirst := make(chan struct{}, 1)
	blockFirst <- struct{}{}
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool { return true }
	cfg.RelayHooks.BeforeLaunchCommit = func(gotThreadID string) {
		if gotThreadID != threadID {
			return
		}
		select {
		case <-blockFirst:
			close(launchBoundary)
			<-releaseLaunch
		default:
		}
	}
	cfg.RelayHooks.BeforeSupervisor = func(gotThreadID string) {
		if gotThreadID == threadID {
			launches <- struct{}{}
		}
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelay(server, cfg, appsource.NewRegistry())

	oldResult := make(chan error, 1)
	go func() {
		oldResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	oldNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: oldNotifications}
	select {
	case <-launchBoundary:
	case <-time.After(time.Second):
		t.Fatal("old initializer did not reach post-validation pre-launch boundary")
	}

	relays.stopRelay("codex:" + threadID)
	freshResult := make(chan error, 1)
	go func() {
		freshResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	freshNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: freshNotifications}
	select {
	case err := <-freshResult:
		if err != nil {
			t.Fatalf("fresh relay start: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh relay did not become ready")
	}
	select {
	case <-launches:
	case <-time.After(time.Second):
		t.Fatal("fresh relay supervisor did not launch")
	}

	close(releaseLaunch)
	select {
	case err := <-oldResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("old initializer error=%v, want shared context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("old initializer did not return after losing launch commitment")
	}
	select {
	case <-launches:
		t.Fatal("old initializer launched supervisor after stop won")
	default:
	}

	freshNotifications <- relayDeltaNotification(t, threadID, "fresh survives old launch")
}

func TestHubRelayInitialRegistrationFailureCancelsAndAllowsFreshStart(t *testing.T) {
	const threadID = "th_registration_initial"
	source := &relayBroadcastSource{
		thread:        codexRelayThread(threadID),
		notifications: make(chan appwire.Notification),
		subscribed:    make(chan struct{}, 2),
		canceled:      make(chan struct{}, 2),
	}
	registrationCalls := make(chan bool, 2)
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(_ context.Context, _ string, replace bool) bool {
		registrationCalls <- replace
		return len(registrationCalls) > 1
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelay(server, cfg, appsource.NewRegistry())

	err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("initial registration error=%v, want context canceled", err)
	}
	expectRelaySubscription(t, source.subscribed)
	select {
	case <-source.canceled:
	case <-time.After(time.Second):
		t.Fatal("failed initial registration did not cancel upstream relay")
	}

	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread); err != nil {
		t.Fatalf("fresh relay after initial registration failure: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
}

func TestHubRelayExistingRegistrationFailureDoesNotReportAttachment(t *testing.T) {
	const threadID = "th_registration_existing"
	source := &relayBroadcastSource{
		thread:        codexRelayThread(threadID),
		notifications: make(chan appwire.Notification),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	registrationResults := make(chan bool, 2)
	registrationResults <- true
	registrationResults <- false
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool {
		return <-registrationResults
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelay(server, cfg, appsource.NewRegistry())

	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread); err != nil {
		t.Fatalf("initial startRelay: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread); !errors.Is(err, context.Canceled) {
		t.Fatalf("existing registration error=%v, want context canceled", err)
	}
}

type successfulAfterCancelRelaySource struct {
	relayLifecycleSource
	mu                 sync.Mutex
	calls              int
	initialStarted     chan struct{}
	initialCanceled    chan struct{}
	releaseInitial     chan struct{}
	initialReturned    chan struct{}
	oldNotifications   <-chan appwire.Notification
	freshStarted       chan struct{}
	freshNotifications <-chan appwire.Notification
}

type initialRequestCancelRelaySource struct {
	relayLifecycleSource
	mu                 sync.Mutex
	calls              int
	initialStarted     chan struct{}
	initialCanceled    chan struct{}
	initialRelease     chan struct{}
	initialReturned    chan struct{}
	freshStarted       chan struct{}
	freshNotifications <-chan appwire.Notification
}

func (s *initialRequestCancelRelaySource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		close(s.initialStarted)
		<-ctx.Done()
		close(s.initialCanceled)
		<-s.initialRelease
		close(s.initialReturned)
		return nil, ctx.Err()
	}
	close(s.freshStarted)
	return s.freshNotifications, nil
}

func (s *successfulAfterCancelRelaySource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		close(s.initialStarted)
		go func() {
			<-ctx.Done()
			close(s.initialCanceled)
		}()
		<-s.releaseInitial
		close(s.initialReturned)
		return s.oldNotifications, nil
	}
	close(s.freshStarted)
	return s.freshNotifications, nil
}
