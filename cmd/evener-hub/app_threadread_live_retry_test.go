package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// liveManagerThread reports whether read proves the live original manager for
// instance: the daemon, not the saved transcript, served it with mutation
// authority and published conversation provenance.
func liveManagerThread(read appwire.ThreadReadResponse, instance string) bool {
	e := read.Thread.Evener
	return read.BootGeneration != "" && read.BootGeneration != appwire.DaemonlessBootGeneration &&
		e.InstanceID == instance && e.Manager && e.MutationStateAuthoritative &&
		e.ConversationProvenance != nil && e.ConversationProvenance.Authoritative
}

// liveRetryingThread is the live manager projection a retried read must
// return: the identity and provenance fields a saved transcript cannot carry.
func liveRetryingThread(sessionID string) appwire.Thread {
	return appwire.Thread{
		ID:        sessionID,
		SessionID: sessionID,
		Source:    "local",
		Evener: appwire.EvenerThread{
			Ref:                        "local:" + sessionID,
			InstanceID:                 sessionID,
			Manager:                    true,
			MutationStateAuthoritative: true,
			ConversationProvenance:     &appwire.ConversationProvenanceState{Version: 1, Authoritative: true, InstanceID: sessionID},
		},
	}
}

// recordingRetryWait records the pacing the retry asks for and returns at once.
type recordingRetryWait struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (w *recordingRetryWait) wait(_ context.Context, delay time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.delays = append(w.delays, delay)
	return nil
}

func (w *recordingRetryWait) recorded() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.delays...)
}

// newLiveReadHub serves one scripted live source over a real hub RPC server,
// with the session's saved transcript available for the fallback path. read
// answers the source's live read for the session it learns only here.
func newLiveReadHub(t *testing.T, read func(sessionID string) (appsource.RelayReadResult, error), hooks hubcore.RelayLifecycleHooks) (*appwire.Client, *scriptedRelaySessionLease, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-live-read-retry-0000000001")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	lease := &scriptedRelaySessionLease{
		deliveries: make(chan appsource.RelayDelivery),
		readFunc:   func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) { return read(sessionID) },
	}
	source := &relaySessionTestSource{thread: liveRetryingThread(sessionID), lease: lease}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         past,
		RelayHooks:   hooks,
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	t.Cleanup(hub.Close)
	return dialHubRPCInitialized(t, hub), lease, sessionID
}

// newDaemonLiveReadHub serves a real LocalDaemonSource over an appserver
// daemon whose thread/read answers read(call). withRoster adds a live roster
// entry, so the caller's ownership recovery runs its re-resolve and its own
// live read before the saved transcript.
func newDaemonLiveReadHub(t *testing.T, read func(sessionID string, call int32) (appwire.ThreadReadResponse, error), withRoster bool, hooks hubcore.RelayLifecycleHooks) (*appwire.Client, *atomic.Int32, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-daemon-live-read-0000000001")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	var calls atomic.Int32
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return read(sessionID, calls.Add(1))
	})
	upstream := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(upstream.Close)
	entry := rendezvous.Entry{
		SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
		WorkspaceRef: "local:" + sessionID, Endpoint: upstream.URL, Protocol: appwire.ProtocolVersion,
	}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: past, RelayHooks: hooks}
	if withRoster {
		cfg.Roster = hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: entry, SessionID: sessionID})
		previous := hubRosterRefresh
		hubRosterRefresh = func(context.Context, *hubcore.Roster) error { return nil }
		t.Cleanup(func() { hubRosterRefresh = previous })
	}
	appServer := newHubAppServer(cfg, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	t.Cleanup(hub.Close)
	return dialHubRPCInitialized(t, hub), &calls, sessionID
}

func liveReadParams(sessionID string) appwire.ThreadReadParams {
	return appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"}
}

// TestHubRPCNonSubscribedReadRetriesTransientLiveFailure pins that a client
// read which loses one live read answers with the live manager, not with the
// persisted transcript. The saved projection carries no mutation authority and
// no published provenance, so the voice history baseline read saw a replaced
// manager identity and failed. The counting hook proves the retry, not the
// fixture, served the read.
func TestHubRPCNonSubscribedReadRetriesTransientLiveFailure(t *testing.T) {
	var attempts atomic.Int32
	waits := &recordingRetryWait{}
	var reads atomic.Int32
	client, lease, sessionID := newLiveReadHub(t, func(id string) (appsource.RelayReadResult, error) {
		if reads.Add(1) == 1 {
			return appsource.RelayReadResult{}, appwire.SessionUnavailable("relay connection ended before route publication")
		}
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: liveRetryingThread(id), BootGeneration: "7"},
			Handoff:  &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})},
		}, nil
	}, hubcore.RelayLifecycleHooks{
		BeforeReadThread: func() { attempts.Add(1) },
		RetryWait:        waits.wait,
	})
	response, err := client.ThreadRead(context.Background(), liveReadParams(sessionID))
	if err != nil {
		t.Fatalf("non-subscribed thread/read: %v", err)
	}
	if response.BootGeneration != "7" || !liveManagerThread(response, sessionID) {
		t.Fatalf("read answered with a saved projection instead of the live manager: boot=%q evener=%+v", response.BootGeneration, response.Thread.Evener)
	}
	if got := lease.readCallCount(); got != 2 {
		t.Fatalf("live read calls = %d, want the failed read and its retry", got)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("live read attempts through the relay = %d, want 2", got)
	}
	if got := waits.recorded(); len(got) != 0 {
		t.Fatalf("first retry waited %v, want it immediate", got)
	}
}

// TestHubRPCNonSubscribedReadRetriesSecondAttempt pins the paced attempt: the
// first retry is immediate, and only the second pays liveReadRetryDelay.
func TestHubRPCNonSubscribedReadRetriesSecondAttempt(t *testing.T) {
	waits := &recordingRetryWait{}
	var reads atomic.Int32
	client, lease, sessionID := newLiveReadHub(t, func(id string) (appsource.RelayReadResult, error) {
		if reads.Add(1) < 3 {
			return appsource.RelayReadResult{}, appwire.SessionUnavailable("relay connection ended during thread rejoin")
		}
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: liveRetryingThread(id), BootGeneration: "7"},
			Handoff:  &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})},
		}, nil
	}, hubcore.RelayLifecycleHooks{RetryWait: waits.wait})
	response, err := client.ThreadRead(context.Background(), liveReadParams(sessionID))
	if err != nil {
		t.Fatalf("non-subscribed thread/read: %v", err)
	}
	if !liveManagerThread(response, sessionID) {
		t.Fatalf("third attempt did not serve the live manager: %+v", response.Thread.Evener)
	}
	if got := lease.readCallCount(); got != liveReadAttempts {
		t.Fatalf("live read calls = %d, want %d", got, liveReadAttempts)
	}
	if got := waits.recorded(); len(got) != 1 || got[0] != liveReadRetryDelay {
		t.Fatalf("retry waits = %v, want one %s", got, liveReadRetryDelay)
	}
}

// TestHubRPCNonSubscribedReadRetryExhaustionReturnsPastTranscript pins the
// production budget and sequence: three retried live reads, then the caller's
// ownership recovery performs its own live read, and only then does the saved
// transcript answer.
func TestHubRPCNonSubscribedReadRetryExhaustionReturnsPastTranscript(t *testing.T) {
	var attempts atomic.Int32
	client, calls, sessionID := newDaemonLiveReadHub(t, func(string, int32) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("canonical actor read failed")
	}, true, hubcore.RelayLifecycleHooks{BeforeReadThread: func() { attempts.Add(1) }})
	response, err := client.ThreadRead(context.Background(), liveReadParams(sessionID))
	if err != nil {
		t.Fatalf("non-subscribed thread/read: %v", err)
	}
	if response.Thread.ID != sessionID || response.BootGeneration != appwire.DaemonlessBootGeneration || response.Thread.Evener.MutationStateAuthoritative {
		t.Fatalf("exhausted live read = %+v (boot %q), want the saved transcript", response.Thread.Evener, response.BootGeneration)
	}
	if got := calls.Load(); got != liveReadAttempts+1 {
		t.Fatalf("daemon thread/read calls = %d, want %d retried reads plus the caller's recovery read", got, liveReadAttempts+1)
	}
	if got := attempts.Load(); got != liveReadAttempts+1 {
		t.Fatalf("live read attempts through the relay = %d, want %d", got, liveReadAttempts+1)
	}
}

// TestHubRPCNonSubscribedReadRetriesRealRelayFailure runs the retry over a real
// LocalDaemonSource and RelaySession: the fake daemon fails one read and serves
// the next, and the client read still returns the live manager.
func TestHubRPCNonSubscribedReadRetriesRealRelayFailure(t *testing.T) {
	var attempts atomic.Int32
	client, calls, sessionID := newDaemonLiveReadHub(t, func(id string, call int32) (appwire.ThreadReadResponse, error) {
		if call == 1 {
			return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("relay connection ended before route publication")
		}
		return appwire.ThreadReadResponse{Thread: liveRetryingThread(id), BootGeneration: "7"}, nil
	}, false, hubcore.RelayLifecycleHooks{BeforeReadThread: func() { attempts.Add(1) }})
	response, err := client.ThreadRead(context.Background(), liveReadParams(sessionID))
	if err != nil {
		t.Fatalf("non-subscribed thread/read: %v", err)
	}
	if !liveManagerThread(response, sessionID) {
		t.Fatalf("real relay retry did not serve the live manager: boot=%q evener=%+v", response.BootGeneration, response.Thread.Evener)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("daemon thread/read calls = %d, want a failed read and its retry", got)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("live read attempts through the relay = %d, want 2", got)
	}
}

// TestHubRPCNonSubscribedReadStopsRetryingSlowFailure pins the retry budget: a
// persistent slow failure is read once, because the caller's recovery read must
// still fit inside the client's thread/read deadline before the saved answer.
func TestHubRPCNonSubscribedReadStopsRetryingSlowFailure(t *testing.T) {
	client, lease, sessionID := newLiveReadHub(t, func(string) (appsource.RelayReadResult, error) {
		time.Sleep(liveReadRetryBudget + 50*time.Millisecond)
		return appsource.RelayReadResult{}, appwire.SessionUnavailable("relay session is unavailable")
	}, hubcore.RelayLifecycleHooks{})
	response, err := client.ThreadRead(context.Background(), liveReadParams(sessionID))
	if err != nil {
		t.Fatalf("non-subscribed thread/read: %v", err)
	}
	if response.Thread.Evener.MutationStateAuthoritative {
		t.Fatalf("slow failure did not fall back to the saved transcript: %+v", response.Thread.Evener)
	}
	if got := lease.readCallCount(); got != 1 {
		t.Fatalf("live read calls = %d, want the slow failure read only", got)
	}
}

// TestHubRPCNonSubscribedReadDoesNotRetryTerminalFailure pins the transient
// filter: a terminal refusal is read once and still answers from the saved
// transcript under the documented non-subscribing contract.
func TestHubRPCNonSubscribedReadDoesNotRetryTerminalFailure(t *testing.T) {
	client, lease, sessionID := newLiveReadHub(t, func(string) (appsource.RelayReadResult, error) {
		return appsource.RelayReadResult{}, appwire.InvalidParams("read refused")
	}, hubcore.RelayLifecycleHooks{})
	response, err := client.ThreadRead(context.Background(), liveReadParams(sessionID))
	if err != nil {
		t.Fatalf("non-subscribed thread/read: %v", err)
	}
	if response.Thread.Evener.MutationStateAuthoritative {
		t.Fatalf("terminal failure did not fall back to the saved transcript: %+v", response.Thread.Evener)
	}
	if got := lease.readCallCount(); got != 1 {
		t.Fatalf("live read calls = %d, want the terminal read only", got)
	}
}

// TestHubRPCNonSubscribedReadDoesNotRetryDeadSession pins the dead-session
// short circuit: the caller's recovery refreshes the roster and answers from
// the saved transcript without spending the retry budget on a gone session.
func TestHubRPCNonSubscribedReadDoesNotRetryDeadSession(t *testing.T) {
	client, lease, sessionID := newLiveReadHub(t, func(id string) (appsource.RelayReadResult, error) {
		return appsource.RelayReadResult{}, appwire.SessionUnavailable("thread not found: " + id)
	}, hubcore.RelayLifecycleHooks{})
	response, err := client.ThreadRead(context.Background(), liveReadParams(sessionID))
	if err != nil {
		t.Fatalf("non-subscribed thread/read: %v", err)
	}
	if response.Thread.Evener.MutationStateAuthoritative {
		t.Fatalf("dead session did not fall back to the saved transcript: %+v", response.Thread.Evener)
	}
	if got := lease.readCallCount(); got != 1 {
		t.Fatalf("live read calls = %d, want the dead-session read only", got)
	}
}
