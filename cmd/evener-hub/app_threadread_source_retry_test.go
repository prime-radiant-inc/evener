package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// sourceRetryHub is one live local daemon behind a registry whose local source
// a test may transiently unregister, plus the seams that observe the read's
// resolution retry and its daemon calls.
type sourceRetryHub struct {
	client    *appwire.Client
	session   string
	sources   *appsource.Registry
	refreshes *atomic.Int32
	calls     *atomic.Int32
}

// newSourceRetryHub builds the hub a resolution retry needs: a scripted live
// daemon for one saved session, a matched roster entry, and a roster-refresh
// seam the test counts. republish makes the seam put the transiently
// unregistered source back: the roster refresh does not register sources
// itself, so it is the concurrent registration that repairs the registry while
// the refresh's window is open. The daemon answers every thread/read with the
// live manager projection, so a live answer and a saved answer differ only in
// authority.
func newSourceRetryHub(t *testing.T, republish bool, configure ...func(*hubcore.WebConfig)) *sourceRetryHub {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-source-retry-0000000001")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	var calls atomic.Int32
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		calls.Add(1)
		return appwire.ThreadReadResponse{Thread: liveRetryingThread(sessionID), BootGeneration: "7"}, nil
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
	cfg := hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         past,
		Roster:       hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: entry, SessionID: sessionID}),
	}
	for _, apply := range configure {
		apply(&cfg)
	}
	var refreshes atomic.Int32
	previous := hubRosterRefresh
	hubRosterRefresh = func(context.Context, *hubcore.Roster) error {
		refreshes.Add(1)
		if republish {
			sources.Add(source)
		}
		return nil
	}
	t.Cleanup(func() { hubRosterRefresh = previous })
	appServer := newHubAppServer(cfg, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	t.Cleanup(hub.Close)
	return &sourceRetryHub{
		client:    dialHubRPCInitialized(t, hub),
		session:   sessionID,
		sources:   sources,
		refreshes: &refreshes,
		calls:     &calls,
	}
}

// TestHubRPCThreadReadRetriesTransientSourceResolution pins the follow-up to
// #5742: a read whose source cannot be resolved yet must refresh the roster and
// resolve again before it answers from the persisted transcript. The saved
// projection carries no mutation authority and no provenance, so clients read
// it as a replaced manager. Here the local source is transiently unregistered
// and a registration lands while the refresh's window is open, so the retried
// resolution serves the live manager.
func TestHubRPCThreadReadRetriesTransientSourceResolution(t *testing.T) {
	fixture := newSourceRetryHub(t, true)
	// The registration lost its race with the read: the source is gone when the
	// read resolves it, and it is registered again during the refresh.
	fixture.sources.Remove("local")

	response, err := fixture.client.ThreadRead(context.Background(), liveReadParams(fixture.session))
	if err != nil {
		t.Fatalf("thread/read: %v", err)
	}
	if response.BootGeneration != "7" || !liveManagerThread(response, fixture.session) {
		t.Fatalf("transient resolution failure answered with the saved projection: boot=%q evener=%+v", response.BootGeneration, response.Thread.Evener)
	}
	if got := fixture.refreshes.Load(); got != 1 {
		t.Fatalf("roster refreshes = %d, want the one retry's refresh", got)
	}
	if got := fixture.calls.Load(); got != 1 {
		t.Fatalf("daemon thread/read calls = %d, want the live read only", got)
	}
}

// TestHubRPCThreadReadKeepsTargetDeletedResolutionTerminal pins the fence: a
// target-deleted refusal on source resolution stays terminal and spends no
// roster refresh. The deletion publishes between the read's entry check and the
// resolution's own check under the ownership alias, exactly the window a retry
// must not widen.
func TestHubRPCThreadReadKeepsTargetDeletedResolutionTerminal(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture := newSourceRetryHub(t, false, func(cfg *hubcore.WebConfig) { cfg.DeletionStore = store })
	original := deletionTargetState
	checks := 0
	deletionTargetState = func(*hubcore.DeletionStore, string, string) (hubcore.DeletionState, bool) {
		checks++
		// The subscription admission and the read's own entry check pass; the
		// deletion publishes before the resolution fences the target under the
		// ownership alias.
		return hubcore.DeletionStateDeleting, checks > 2
	}
	t.Cleanup(func() { deletionTargetState = original })

	_, err = fixture.client.ThreadRead(context.Background(), liveReadParams(fixture.session))
	if !isTargetDeletedError(err) {
		t.Fatalf("deleting resolution error = %T %v, want targetDeleted", err, err)
	}
	// The first check is the subscription admission resolver, the second is the
	// read's own entry fence, and the third is the one
	// sourceForThreadWithDeletionFence runs. Pinning the count keeps this test
	// exercising the resolution branch instead of an earlier check.
	if checks != 3 {
		t.Fatalf("deletion checks = %d, want admission, entry, and the resolution fence", checks)
	}
	if got := fixture.refreshes.Load(); got != 0 {
		t.Fatalf("roster refreshes = %d, want none for a target-deleted fence", got)
	}
	if got := fixture.calls.Load(); got != 0 {
		t.Fatalf("daemon thread/read calls = %d, want no live read", got)
	}
}

// TestHubRPCThreadReadResolutionRetryExhaustionServesSavedHistory pins the
// fallback #5749 keeps: when the refreshed resolution still fails, the read
// answers from the persisted transcript exactly as before, after one refresh
// and without spending a live read.
func TestHubRPCThreadReadResolutionRetryExhaustionServesSavedHistory(t *testing.T) {
	fixture := newSourceRetryHub(t, false)
	fixture.sources.Remove("local")

	response, err := fixture.client.ThreadRead(context.Background(), liveReadParams(fixture.session))
	if err != nil {
		t.Fatalf("thread/read: %v", err)
	}
	if response.Thread.ID != fixture.session || response.BootGeneration != appwire.DaemonlessBootGeneration || response.Thread.Evener.MutationStateAuthoritative {
		t.Fatalf("exhausted resolution = %+v (boot %q), want the saved transcript", response.Thread.Evener, response.BootGeneration)
	}
	if got := fixture.refreshes.Load(); got != 1 {
		t.Fatalf("roster refreshes = %d, want the single retry's refresh", got)
	}
	if got := fixture.calls.Load(); got != 0 {
		t.Fatalf("daemon thread/read calls = %d, want no live read", got)
	}
}

// TestResolveReadSourceKeepsTheFirstErrorWhenTheRetryFails pins the error the
// saved-history fallback reports when there is no past entry: the retry's own
// failure does not replace the first resolution error, and the retry happens
// exactly once.
func TestResolveReadSourceKeepsTheFirstErrorWhenTheRetryFails(t *testing.T) {
	sources := appsource.NewRegistry()
	cfg := hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries()}
	var refreshes atomic.Int32
	previous := hubRosterRefresh
	hubRosterRefresh = func(context.Context, *hubcore.Roster) error {
		refreshes.Add(1)
		return nil
	}
	t.Cleanup(func() { hubRosterRefresh = previous })

	_, err := resolveReadSource(t.Context(), cfg, sources, appwire.ThreadReadParams{Ref: localAppRef(webTestSessionID)})
	if !errors.Is(err, appsource.ErrSourceNotFound) {
		t.Fatalf("resolution error = %v, want the first attempt's source-not-found error", err)
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("roster refreshes = %d, want exactly one", got)
	}
}

// TestThreadReadKeepsPendingAskPageTerminalWithoutARetry pins that a
// pending-ask page is neither retried nor answered from saved history: the page
// names a live ask set that only a resolved source can answer.
func TestThreadReadKeepsPendingAskPageTerminalWithoutARetry(t *testing.T) {
	cfg := hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries()}
	var refreshes atomic.Int32
	previous := hubRosterRefresh
	hubRosterRefresh = func(context.Context, *hubcore.Roster) error {
		refreshes.Add(1)
		return nil
	}
	t.Cleanup(func() { hubRosterRefresh = previous })
	// No local source: the page's resolution fails before any page can be read.
	operations := newProjectReadOperations(t, cfg, nil)
	revision, offset, limit := uint64(0), 0, 1

	response, err := operations.read(context.Background(), appwire.ThreadReadParams{
		Ref:                       localAppRef(webTestSessionID),
		ExpectedAskRevision:       &revision,
		ExpectedAskBootGeneration: "boot-1",
		Offset:                    &offset,
		Limit:                     &limit,
	}, false)
	if !errors.Is(err, appsource.ErrSourceNotFound) {
		t.Fatalf("pending ask page error = %v, want the source-missing refusal", err)
	}
	if response.Thread.ID != "" || response.PendingAskPage != nil {
		t.Fatalf("pending ask page answered without a source: %+v", response)
	}
	if got := refreshes.Load(); got != 0 {
		t.Fatalf("roster refreshes = %d, want none for a pending ask page", got)
	}
}
