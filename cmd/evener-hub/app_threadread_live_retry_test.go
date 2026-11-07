package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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

// newLiveReadHub serves one scripted live source over a real hub RPC server,
// with the session's saved transcript available for the fallback path. read
// answers the source's live read for the session it learns only here.
func newLiveReadHub(t *testing.T, read func(sessionID string) (appsource.RelayReadResult, error)) (*appwire.Client, *scriptedRelaySessionLease, string) {
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
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	t.Cleanup(hub.Close)
	return dialHubRPCInitialized(t, hub), lease, sessionID
}

// TestHubRPCNonSubscribedReadRetriesTransientLiveFailure pins that a client
// read which loses one live read answers with the live manager, not with the
// persisted transcript. The saved projection carries no mutation authority and
// no published provenance, so the voice history baseline read saw a replaced
// manager identity and failed. The scripted source answers the retry; the
// relay's own reconnect is pinned by the relay tests.
func TestHubRPCNonSubscribedReadRetriesTransientLiveFailure(t *testing.T) {
	var reads atomic.Int32
	client, lease, sessionID := newLiveReadHub(t, func(id string) (appsource.RelayReadResult, error) {
		if reads.Add(1) == 1 {
			return appsource.RelayReadResult{}, appwire.SessionUnavailable("relay connection ended before route publication")
		}
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: liveRetryingThread(id), BootGeneration: "7"},
			Handoff:  &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})},
		}, nil
	})
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          "local:" + sessionID,
		IncludeTurns: true,
		ItemsView:    "full",
	})
	if err != nil {
		t.Fatalf("non-subscribed thread/read: %v", err)
	}
	if response.BootGeneration != "7" || !liveManagerThread(response, sessionID) {
		t.Fatalf("read answered with a saved projection instead of the live manager: boot=%q evener=%+v", response.BootGeneration, response.Thread.Evener)
	}
	if got := lease.readCallCount(); got != 2 {
		t.Fatalf("live read calls = %d, want the failed read and its retry", got)
	}
}

// TestHubRPCNonSubscribedReadRetryExhaustionReturnsPastTranscript keeps the
// documented contract for a source that never answers: after the bounded live
// attempts, a non-subscribed read still returns the saved transcript.
func TestHubRPCNonSubscribedReadRetryExhaustionReturnsPastTranscript(t *testing.T) {
	client, lease, sessionID := newLiveReadHub(t, func(string) (appsource.RelayReadResult, error) {
		return appsource.RelayReadResult{}, appwire.SessionUnavailable("canonical actor read failed")
	})
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          "local:" + sessionID,
		IncludeTurns: true,
		ItemsView:    "full",
	})
	if err != nil {
		t.Fatalf("non-subscribed thread/read: %v", err)
	}
	if response.Thread.ID != sessionID || response.Thread.Evener.MutationStateAuthoritative {
		t.Fatalf("exhausted live read = %+v, want the saved transcript", response.Thread.Evener)
	}
	if got := lease.readCallCount(); got != liveReadAttempts {
		t.Fatalf("live read calls = %d, want the %d-attempt budget", got, liveReadAttempts)
	}
}
