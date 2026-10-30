package hub

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestDeletionFenceRejectsSourceResolution(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := localAppRef(webTestSessionID)
	if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: webTestSessionID,
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
	}
	sources := newHubSourceRegistry(cfg)

	_, err = sourceForThreadWithDeletionFence(t.Context(), cfg, sources, ref, webTestSessionID)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("deleting source resolution error = %T %v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok ||
		data.MutationOutcome != appwire.MutationOutcomeTargetDeleted ||
		data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("deleting source resolution wire error = %#v", wire)
	}
}

func TestDeletionFenceDoesNotFallBackToPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	project, err := identifier.ResolveProject(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", project.ID)
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	store, err := hubcore.NewDeletionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := localAppRef(sessionID)
	if _, err := store.Begin(project.ID, []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: sessionID,
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
		Past:          past,
	}
	sources := newHubSourceRegistry(cfg)
	server := newHubAppServer(cfg, sources)
	raw, err := json.Marshal(appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}

	_, err = server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodThreadRead,
		Params: raw,
	})
	if !isTargetDeletedError(err) {
		t.Fatalf("deleting past read error = %T %v, want targetDeleted", err, err)
	}
}

func TestDeletionFenceRejectsResumeBeforeSpawner(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	project, err := identifier.ResolveProject(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", project.ID)
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	store, err := hubcore.NewDeletionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := localAppRef(sessionID)
	if _, err := store.Begin(project.ID, []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: sessionID,
	}}); err != nil {
		t.Fatal(err)
	}
	resumeCalls := 0
	spawner := &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
		resumeCalls++
		return rendezvous.Entry{}, errors.New("deleting target reached spawner")
	}}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
		Past:          past,
		Spawner:       spawner,
	}

	sources := newHubSourceRegistry(cfg)
	_, err = hubThreadResume(context.Background(), cfg, sources, appwire.ThreadResumeParams{Ref: ref})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("deleting resume error = %T %v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.MutationOutcome != appwire.MutationOutcomeTargetDeleted {
		t.Fatalf("deleting resume wire error = %#v", wire)
	}
	if resumeCalls != 0 {
		t.Fatalf("deleting resume reached spawner %d times", resumeCalls)
	}
}

func TestDeletionFenceRejectsResumeWithNilSpawner(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(sessionID)
	if _, err := store.Begin("project-resume-nil-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: sessionID,
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
	}

	_, err = hubThreadResume(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadResumeParams{Ref: ref})
	if !isTargetDeletedError(err) {
		t.Fatalf("deleting resume error with nil spawner = %T %v, want targetDeleted", err, err)
	}
}

func TestDeletionFenceRejectsMutationWithTargetDeletedOutcome(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := localAppRef(webTestSessionID)
	if _, err := store.Begin("project-mutate-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: webTestSessionID,
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
	}
	source := &deletionFenceMutationSource{}
	sources := appsource.NewRegistry()
	sources.Add(source)
	server := newHubAppServer(cfg, sources)
	tests := []struct {
		method string
		id     string
		params any
	}{
		{appwire.MethodTurnStart, "mutation-start-after-delete", appwire.TurnStartParams{
			Ref: ref, ClientMutationID: "mutation-start-after-delete",
			Input: []appwire.InputItem{{Type: "text", Text: "start"}},
		}},
		{appwire.MethodTurnSteer, "mutation-steer-after-delete", appwire.TurnSteerParams{
			Ref: ref, ClientMutationID: "mutation-steer-after-delete", Input: []appwire.InputItem{{Type: "text", Text: "steer"}},
		}},
		{appwire.MethodTurnInterrupt, "mutation-interrupt-after-delete", appwire.TurnInterruptParams{
			Ref: ref, ClientMutationID: "mutation-interrupt-after-delete"}},
		{appwire.MethodTurnQueue, "mutation-queue-after-delete", appwire.TurnQueueParams{
			Ref: ref, ClientMutationID: "mutation-queue-after-delete", Input: []appwire.InputItem{{Type: "text", Text: "queue"}},
		}},
		{appwire.MethodTurnDrainAsSteer, "mutation-drain-after-delete", appwire.TurnDrainAsSteerParams{
			Ref: ref, ClientMutationID: "mutation-drain-after-delete",
			ExpectedQueueRevision: 1,
		}},
		{appwire.MethodTurnPromoteQueuedAsSteer, "mutation-promote-after-delete", appwire.TurnPromoteQueuedAsSteerParams{
			Ref: ref, ClientMutationID: "mutation-promote-after-delete",
			ExpectedEntryID: "queue-before-delete",
		}},
		{appwire.MethodTurnCancelQueued, "mutation-cancel-after-delete", appwire.TurnCancelQueuedParams{
			Ref: ref, ClientMutationID: "mutation-cancel-after-delete",
			ExpectedEntryID: "queue-before-delete",
		}},
	}
	for i, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			raw, err := json.Marshal(tc.params)
			if err != nil {
				t.Fatal(err)
			}
			_, err = server.Router().Dispatch(context.Background(), appwire.Request{
				ID:     appwire.NewIntID(int64(i + 1)),
				Method: tc.method,
				Params: raw,
			})
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("deleting mutation error = %T %v, want WireError", err, err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok ||
				data.ClientMutationID != tc.id ||
				data.MutationOutcome != appwire.MutationOutcomeTargetDeleted ||
				data.RetryDisposition != appwire.RetryDispositionNone {
				t.Fatalf("deleting mutation wire error = %#v", wire)
			}
			if source.calls[tc.method] != 0 {
				t.Fatalf("deleting mutation reached %s source %d times", tc.method, source.calls[tc.method])
			}
		})
	}
}

func TestDeletionFenceRejectsRelayBeforeSubscribe(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	threadID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	if _, err := store.Begin("project-relay-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: threadID,
	}}); err != nil {
		t.Fatal(err)
	}
	source := &deletionFenceRelaySource{
		thread: appwire.Thread{
			ID:     threadID,
			Evener: appwire.EvenerThread{Ref: ref},
		},
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelay(server, cfg, appsource.NewRegistry())

	err = relays.startRelay(context.Background(), source, appwire.ThreadReadParams{Ref: ref}, source.thread)
	if !isTargetDeletedError(err) {
		t.Fatalf("deleting relay error = %T %v, want targetDeleted", err, err)
	}
	if source.subscribeCalls != 0 {
		t.Fatalf("deleting relay subscribed %d times", source.subscribeCalls)
	}
}

func TestDeletionFenceCanCommitDuringInitialRelayIOAndBlocksPublication(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	threadID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	resumeLocks := hubcore.NewResumeLocks()
	source := &deletionOwnershipProbeRelaySource{
		thread: appwire.Thread{
			ID:     threadID,
			Evener: appwire.EvenerThread{Ref: ref},
		},
		store:       store,
		resumeLocks: resumeLocks,
		projectID:   "project-relay-initial-0123456789",
		ref:         ref,
		threadID:    threadID,
		probeOnCall: 1,
		probed:      make(chan deletionOwnershipProbeResult, 1),
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   resumeLocks,
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelay(server, cfg, appsource.NewRegistry())

	relayErr := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{Ref: ref}, source.thread)
	result := <-source.probed
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.deletedBeforeSubscribe {
		t.Fatal("relay held deletion ownership across upstream I/O")
	}
	if !isTargetDeletedError(relayErr) {
		t.Fatalf("relay error = %T %v, want targetDeleted after deletion won the I/O race", relayErr, relayErr)
	}
}

func TestDeletionFenceCanCommitDuringRecoveryRelayIOAndStopsPublication(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	threadID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	resumeLocks := hubcore.NewResumeLocks()
	initialNotifications := make(chan appwire.Notification)
	source := &deletionOwnershipProbeRelaySource{
		thread: appwire.Thread{
			ID:     threadID,
			Evener: appwire.EvenerThread{Ref: ref},
		},
		store:                store,
		resumeLocks:          resumeLocks,
		projectID:            "project-relay-recovery-0123456789",
		ref:                  ref,
		threadID:             threadID,
		probeOnCall:          2,
		initialNotifications: initialNotifications,
		probed:               make(chan deletionOwnershipProbeResult, 1),
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   resumeLocks,
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelay(server, cfg, appsource.NewRegistry())
	defer relays.stopRelay("local:" + threadID)

	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{Ref: ref}, source.thread); err != nil {
		t.Fatal(err)
	}
	close(initialNotifications)
	result := <-source.probed
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.deletedBeforeSubscribe {
		t.Fatal("relay held deletion ownership across recovery I/O")
	}
}

func TestDeletionFenceTurnStartDoesNotWaitForRelayWhileOwningTarget(t *testing.T) {
	threadID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	resumeLocks := hubcore.NewResumeLocks()
	placeholderPublished := make(chan struct{})
	releaseInitializer := make(chan struct{})
	source := &inheritedDeletionOwnershipRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: ref},
		},
		notifications: make(chan appwire.Notification),
		subscribed:    make(chan struct{}, 1),
	}
	cfg := hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		ResumeLocks:  resumeLocks,
		RelayHooks: hubcore.RelayLifecycleHooks{
			AfterPlaceholder: func(gotThreadID string) {
				if gotThreadID != threadID {
					return
				}
				close(placeholderPublished)
				<-releaseInitializer
			},
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	var relays *hubRelay
	previousObserveFunctions := observeHubRelay
	observeHubRelay = func(got *hubRelay) {
		relays = got
	}
	t.Cleanup(func() {
		observeHubRelay = previousObserveFunctions
	})
	server := newHubAppServer(cfg, sources)
	observeHubRelay = previousObserveFunctions
	defer relays.stopRelay("local:" + threadID)

	waiterJoined := make(chan struct{}, 1)
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() {
		waiterJoined <- struct{}{}
	}
	t.Cleanup(func() {
		observeHubRelayWait = previousObserveWait
	})

	initializerDone := make(chan error, 1)
	go func() {
		initializerDone <- relays.startRelay(
			context.Background(),
			source,
			appwire.ThreadReadParams{Ref: ref},
			source.thread,
		)
	}()
	<-placeholderPublished

	raw, err := json.Marshal(appwire.TurnStartParams{
		ThreadID:           threadID,
		Ref:                ref,
		ClientMutationID:   "turn-start-relay-ownership",
		ExpectedInstanceID: threadID,
		Input:              []appwire.InputItem{{Type: "text", Text: "continue"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	turnDone := make(chan error, 1)
	go func() {
		_, err := server.Router().Dispatch(context.Background(), appwire.Request{
			ID:     appwire.NewIntID(1),
			Method: appwire.MethodTurnStart,
			Params: raw,
		})
		turnDone <- err
	}()
	<-waiterJoined

	targetLock := resumeLocks.For(threadID)
	targetWasFree := targetLock.TryLock()
	if targetWasFree {
		targetLock.Unlock()
	} else {
		relays.stopRelay("local:" + threadID)
	}
	close(releaseInitializer)
	initializerErr := <-initializerDone
	turnErr := <-turnDone

	if !targetWasFree {
		t.Fatal("turn/start waited for the relay initializer while owning the deletion target")
	}
	if initializerErr != nil {
		t.Fatalf("initialize relay: %v", initializerErr)
	}
	if turnErr != nil {
		t.Fatalf("turn/start: %v", turnErr)
	}
	select {
	case <-source.subscribed:
	default:
		t.Fatal("relay initializer did not subscribe")
	}
}

func TestDeletionFenceRejectsForkBeforeLaunchingChild(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	threadID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	if _, err := store.Begin("project-fork-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: threadID,
	}}); err != nil {
		t.Fatal(err)
	}
	oldAside := hubAsideSession
	t.Cleanup(func() { hubAsideSession = oldAside })
	launchCalls := 0
	hubAsideSession = func(string, string) (string, error) {
		launchCalls++
		return "", errors.New("deleting fork launched child")
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
		StateDir:      t.TempDir(),
	}

	_, err = hubThreadFork(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadForkParams{
		Ref:   ref,
		Aside: true,
	})
	if !isTargetDeletedError(err) {
		t.Fatalf("deleting fork error = %T %v, want targetDeleted", err, err)
	}
	if launchCalls != 0 {
		t.Fatalf("deleting fork launched %d children", launchCalls)
	}
}

type deletionFenceRelaySource struct {
	relayLifecycleSource
	subscribeCalls int
}

func (s *deletionFenceRelaySource) ID() string {
	return "local"
}

func (s *deletionFenceRelaySource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.subscribeCalls++
	return nil, errors.New("deleting relay reached source subscription")
}

type inheritedDeletionOwnershipRelaySource struct {
	relayLifecycleSource
	notifications <-chan appwire.Notification
	subscribed    chan struct{}
}

func (s *inheritedDeletionOwnershipRelaySource) ID() string {
	return "local"
}

func (s *inheritedDeletionOwnershipRelaySource) SubscribeThread(
	context.Context,
	appwire.ThreadReadParams,
) (<-chan appwire.Notification, error) {
	s.subscribed <- struct{}{}
	return s.notifications, nil
}

func (s *inheritedDeletionOwnershipRelaySource) StartTurn(
	context.Context,
	appwire.TurnStartParams,
) (appwire.TurnStartResponse, error) {
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_started"}}, nil
}

type deletionOwnershipProbeResult struct {
	deletedBeforeSubscribe bool
	err                    error
}

type deletionOwnershipProbeRelaySource struct {
	relayLifecycleSource
	store                *hubcore.DeletionStore
	resumeLocks          *hubcore.ResumeLocks
	projectID            string
	ref                  string
	threadID             string
	probeOnCall          int
	initialNotifications <-chan appwire.Notification
	probed               chan deletionOwnershipProbeResult
	mu                   sync.Mutex
	subscribeCalls       int
}

func (s *deletionOwnershipProbeRelaySource) ID() string {
	return "local"
}

func (s *deletionOwnershipProbeRelaySource) SubscribeThread(
	context.Context,
	appwire.ThreadReadParams,
) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.subscribeCalls++
	call := s.subscribeCalls
	s.mu.Unlock()
	if call < s.probeOnCall {
		return s.initialNotifications, nil
	}

	lock := s.resumeLocks.For(s.threadID)
	if !lock.TryLock() {
		s.probed <- deletionOwnershipProbeResult{}
		return nil, errors.New("stop relay ownership probe")
	}
	_, err := s.store.Begin(s.projectID, []hubcore.DeletionTarget{{
		Ref:      s.ref,
		ThreadID: s.threadID,
	}})
	lock.Unlock()
	if err != nil {
		s.probed <- deletionOwnershipProbeResult{err: err}
		return nil, err
	}
	_, deleted := s.store.TargetState(s.ref, s.threadID)
	s.probed <- deletionOwnershipProbeResult{deletedBeforeSubscribe: deleted}
	return nil, errors.New("stop relay ownership probe")
}

type deletionFenceMutationSource struct {
	relayLifecycleSource
	calls map[string]int
}

func (s *deletionFenceMutationSource) ID() string {
	return "local"
}

func (s *deletionFenceMutationSource) QueueTurn(
	context.Context,
	appwire.TurnQueueParams,
) (appwire.TurnQueueResponse, error) {
	s.recordCall(appwire.MethodTurnQueue)
	return appwire.TurnQueueResponse{}, nil
}

func (s *deletionFenceMutationSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.recordCall(appwire.MethodTurnStart)
	return appwire.TurnStartResponse{}, nil
}

func (s *deletionFenceMutationSource) SteerTurn(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	s.recordCall(appwire.MethodTurnSteer)
	return appwire.TurnSteerResponse{}, nil
}

func (s *deletionFenceMutationSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	s.recordCall(appwire.MethodTurnInterrupt)
	return appwire.TurnInterruptResponse{}, nil
}

func (s *deletionFenceMutationSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	s.recordCall(appwire.MethodTurnDrainAsSteer)
	return appwire.TurnDrainAsSteerResponse{}, nil
}

func (s *deletionFenceMutationSource) PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	s.recordCall(appwire.MethodTurnPromoteQueuedAsSteer)
	return appwire.TurnPromoteQueuedAsSteerResponse{}, nil
}

func (s *deletionFenceMutationSource) CancelQueued(context.Context, appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	s.recordCall(appwire.MethodTurnCancelQueued)
	return appwire.TurnCancelQueuedResponse{}, nil
}

func (s *deletionFenceMutationSource) recordCall(method string) {
	if s.calls == nil {
		s.calls = make(map[string]int)
	}
	s.calls[method]++
}
