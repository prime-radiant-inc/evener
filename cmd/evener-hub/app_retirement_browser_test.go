package hub

// app_retirement_browser_test.go proves the user-visible retirement recovery
// contract through a real headless-Chrome guard driving the production
// Session pane.
//
// TestRetirementBrowser is the entry point npm retirementguard calls:
//
//	cd ../../.. && go test ./cmd/evener-hub -run '^TestRetirementBrowser$' -count=1 -args -retirement-browser
//
// It skips in ordinary Go gates unless -retirement-browser is passed. The
// guard script (scripts/retirementguard/run.mjs) runs Chrome against the
// fixture Hub and proves the recovery contract; the Go test's job is to
// start the fixture and wait for the Node process to exit.
//
// The fixture provides:
//   - A real Hub WebSocket endpoint (httptest.Server, /rpc) with a scripted
//     relay source seeded with two completed turns.
//   - A safe-retire HTTP endpoint (/fixture/retire) the browser harness
//     calls to trigger daemon retirement (closes the relay deliveries
//     channel, which causes the relay loop to reconnect and broadcast
//     evener/thread/resync to connected clients).
//   - A replacement relay that answers subsequent thread/read with the same
//     turns but a new instanceId, and pushes turn/started+turn/completed
//     after accepting a turn/start.
//
// Scripted provider/process launcher remain the only substitutes; the Hub's
// own relay machinery and the browser's real AppwireClient are used as-is.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// retirementBrowserFlag gates TestRetirementBrowser. npm retirementguard
// passes -retirement-browser so the test runs inside the guard invocation
// only. Ordinary go test passes do not pass it, so the test is skipped and
// does not launch Chrome or Vite.
var retirementBrowserFlag = flag.Bool("retirement-browser", false, "run isolated retirement browser fixture (used by npm retirementguard)")

// ---- scripted relay source -------------------------------------------------

// retirementBrowserRelaySource implements a two-generation relay source for
// the browser test fixture. First generation: initial thread with two turns,
// idle deliveries channel. Second generation (after retirement): same turns,
// new instanceId, accepts turn/start and pushes turn notifications.
type retirementBrowserRelaySource struct {
	relayLifecycleSource

	mu sync.Mutex

	// Populated on first AcquireRelaySession; closed by retire().
	gen1Deliveries chan appsource.RelayDelivery

	// Becomes non-nil after retire() is called; handed out on second Acquire.
	gen2Deliveries chan appsource.RelayDelivery

	// retired is closed when the /fixture/retire endpoint is triggered.
	retired chan struct{}

	// turnAccepted is closed when turn/start is handled.
	turnAccepted chan struct{}
	// closeOnce ensures turnAccepted is closed exactly once.
	closeOnce sync.Once

	// staleForwarded is closed by the hub fanout's acknowledgement of the stale
	// old-generation frame emitted just before the resync. The browser
	// assertion (the ghost turn must not survive) is only meaningful when this
	// proves the frame really was forwarded, not dropped in the fixture.
	staleForwarded   chan struct{}
	staleForwardOnce sync.Once

	// retryMutationIDs records every turn/start attempt for the retry draft, so
	// the test can prove the client replayed the SAME clientMutationId after a
	// lost reply. retryAccepted closes when the retry is finally accepted.
	retryMu          sync.Mutex
	retryMutationIDs []string
	retryAccepted    chan struct{}
	retryOnce        sync.Once

	// accepted records every turn this source has accepted, so a replacement
	// re-read returns the full transcript rather than dropping accepted turns.
	acceptedMu sync.Mutex
	accepted   []acceptedTurn

	// degraded makes the replacement advertise unavailable queue/steer/settings.
	// Every turn/start text is recorded so the test can prove an unavailable
	// controller never auto-resumed the daemon with the retained draft.
	degradedMu    sync.Mutex
	degraded      bool
	turnStartText []string

	threadID string
	ref      string
}

type acceptedTurn struct {
	id   string
	text string
}

func newRetirementBrowserRelaySource(threadID, ref string) *retirementBrowserRelaySource {
	return &retirementBrowserRelaySource{
		gen1Deliveries: make(chan appsource.RelayDelivery, 8),
		gen2Deliveries: make(chan appsource.RelayDelivery, 8),
		retired:        make(chan struct{}),
		turnAccepted:   make(chan struct{}),
		staleForwarded: make(chan struct{}),
		retryAccepted:  make(chan struct{}),
		threadID:       threadID,
		ref:            ref,
	}
}

func (s *retirementBrowserRelaySource) retire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.retired:
		// already retired
	default:
		close(s.retired)
		// A frame from the generation that is being retired, racing the
		// replacement: it reaches the client, and the authoritative replacement
		// snapshot that follows must supersede it rather than let it overwrite
		// the replacement. staleForwarded proves the hub really forwarded it.
		staleTurn := appwire.Turn{
			ID:        "turn_old_generation",
			Status:    "completed",
			ItemsView: appwire.TurnItemsViewFragment,
			Items: []appwire.ThreadItem{{
				Type: "userMessage", ID: "turn_old_generation_user", TurnID: "turn_old_generation",
				Text: "STALE OLD GENERATION", Status: "completed",
				TranscriptKey: "turn_old_generation/0", Position: &appwire.ThreadItemPosition{Entry: 99, Item: 0},
			}},
		}
		s.gen1Deliveries <- appsource.RelayDelivery{
			Notification: appwire.Notification{
				Method: appwire.NotifyTurnStarted,
				Params: mustMarshalJSON(map[string]any{"threadId": s.threadID, "ref": s.ref, "turn": staleTurn}),
			},
			Acknowledge: func() { s.staleForwardOnce.Do(func() { close(s.staleForwarded) }) },
			Proceed:     func() {},
		}
		// The source's recovery feed announces the generation change on the LIVE
		// feed before it ends: that resync is what tells the already-connected
		// client to re-read, and the re-read is what asks for the replacement
		// lease. Emitting it on gen1 (buffered) and then closing gen1 exercises
		// exactly that order — the hub forwards the resync to the client's
		// subscription, then retires the dead generation's handle.
		ack := func() {}
		s.gen1Deliveries <- appsource.RelayDelivery{
			Notification: appwire.Notification{
				Method: appwire.NotifyEvenerThreadResync,
				Params: mustMarshalJSON(map[string]any{"threadId": s.threadID, "ref": s.ref}),
			},
			Acknowledge: ack,
			Proceed:     ack,
		}
		close(s.gen1Deliveries)
	}
}

func (s *retirementBrowserRelaySource) ID() string { return "local" }

func (s *retirementBrowserRelaySource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{}, errors.New("ReadThread must not be used for a relay source")
}

func (s *retirementBrowserRelaySource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	return nil, errors.New("SubscribeThread must not be used for a relay source")
}

// retryDraftText and retryDraftTurnID are the second scenario's contract: the
// replacement source drops the FIRST turn/start reply for this draft (a
// transient, retryable failure) and accepts the retry, which must carry the
// SAME clientMutationId. retryMutationIDs records every attempt so the test can
// prove the id was replayed rather than regenerated.
const (
	retryDraftText    = "retirement harness retry draft"
	retryDraftTurnID  = "turn_retirement_retry"
	retainedDraftText = "retirement harness retained draft"
)

// StartTurn accepts a turn/start after retirement and pushes turn/started +
// turn/completed notifications through gen2Deliveries. For retryDraftText the
// first attempt fails retryably with no reply so the client must replay the
// same mutation id against the replacement.
func (s *retirementBrowserRelaySource) StartTurn(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	select {
	case <-s.retired:
		// Replacement source: accept the turn.
	default:
		return appwire.TurnStartResponse{}, appwire.LifecycleUnavailable("retiring")
	}

	text := ""
	if len(params.Input) > 0 {
		text = params.Input[0].Text
	}
	s.degradedMu.Lock()
	s.turnStartText = append(s.turnStartText, text)
	s.degradedMu.Unlock()
	if text == retryDraftText {
		s.retryMu.Lock()
		s.retryMutationIDs = append(s.retryMutationIDs, params.ClientMutationID)
		attempt := len(s.retryMutationIDs)
		s.retryMu.Unlock()
		if attempt == 1 {
			// The reply to this attempt is lost. A resync wakes the client's
			// dispatcher so the replay happens deterministically rather than on
			// a request timeout, exactly as a hub reconnect/resync would.
			s.pushGen2Resync()
			return appwire.TurnStartResponse{}, appwire.Unavailable("simulated lost start reply")
		}
		s.retryOnce.Do(func() { close(s.retryAccepted) })
		newTurn := retirementTurn(retryDraftTurnID, text)
		s.recordAccepted(retryDraftTurnID, text)
		s.emitTurn(newTurn)
		return appwire.TurnStartResponse{Turn: newTurn, Receipt: s.receipt(params, retryDraftTurnID)}, nil
	}

	const newTurnID = "turn_retirement_new"
	newTurn := retirementTurn(newTurnID, text)
	s.recordAccepted(newTurnID, text)
	// Close turnAccepted once so callers can await it.
	s.closeOnce.Do(func() { close(s.turnAccepted) })
	s.emitTurn(newTurn)
	return appwire.TurnStartResponse{Turn: newTurn, Receipt: s.receipt(params, newTurnID)}, nil
}

func retirementTurn(id, text string) appwire.Turn {
	return appwire.Turn{
		ID:        id,
		Status:    "completed",
		ItemsView: "full",
		Items: []appwire.ThreadItem{{
			Type:   "userMessage",
			ID:     id + "_user",
			TurnID: id,
			Text:   text,
			Status: "completed",
		}},
	}
}

func (s *retirementBrowserRelaySource) recordAccepted(id, text string) {
	s.acceptedMu.Lock()
	defer s.acceptedMu.Unlock()
	s.accepted = append(s.accepted, acceptedTurn{id: id, text: text})
}

func (s *retirementBrowserRelaySource) receipt(params appwire.TurnStartParams, turnID string) appwire.MutationReceipt {
	return appwire.MutationReceipt{
		ClientMutationID: params.ClientMutationID,
		Disposition:      appwire.MutationDispositionApplied,
		ThreadID:         s.threadID,
		InstanceID:       "instance_v2",
		TurnID:           turnID,
		ProjectionState:  appwire.MutationProjectionState("reflected"),
	}
}

// emitTurn pushes turn/started + turn/completed through gen2Deliveries so the
// relay broadcasts them to the browser client.
func (s *retirementBrowserRelaySource) emitTurn(newTurn appwire.Turn) {
	params := mustMarshalJSON(map[string]any{"threadId": s.threadID, "ref": s.ref, "turn": newTurn})
	ack := func() {}
	deliver := func(method string) {
		s.mu.Lock()
		ch := s.gen2Deliveries
		s.mu.Unlock()
		select {
		case ch <- appsource.RelayDelivery{Notification: appwire.Notification{Method: method, Params: params}, Acknowledge: ack, Proceed: ack}:
		case <-time.After(5 * time.Second):
		}
	}
	go func() {
		deliver(appwire.NotifyTurnStarted)
		deliver(appwire.NotifyTurnCompleted)
	}()
}

func (s *retirementBrowserRelaySource) pushGen2Resync() {
	ack := func() {}
	s.mu.Lock()
	ch := s.gen2Deliveries
	s.mu.Unlock()
	select {
	case ch <- appsource.RelayDelivery{
		Notification: appwire.Notification{
			Method: appwire.NotifyEvenerThreadResync,
			Params: mustMarshalJSON(map[string]any{"threadId": s.threadID, "ref": s.ref}),
		},
		Acknowledge: ack, Proceed: ack,
	}:
	case <-time.After(5 * time.Second):
	}
}

// degrade makes the replacement advertise unavailable queue/steer/settings and
// publishes the change through the live replacement feed, so the connected
// client re-reads the new capability set without reconnecting.
func (s *retirementBrowserRelaySource) degrade() {
	s.degradedMu.Lock()
	s.degraded = true
	s.degradedMu.Unlock()
	s.pushGen2Resync()
}

func (s *retirementBrowserRelaySource) ResolveRelaySession(params appwire.ThreadReadParams) (appwire.Ref, error) {
	return appwire.ParseRef(params.Ref)
}

func (s *retirementBrowserRelaySource) AcquireRelaySession(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
	s.mu.Lock()
	isRetired := func() bool {
		select {
		case <-s.retired:
			return true
		default:
			return false
		}
	}()
	s.mu.Unlock()

	var instanceID string
	var deliveries chan appsource.RelayDelivery
	if !isRetired {
		instanceID = "instance_v1"
		deliveries = s.gen1Deliveries
	} else {
		instanceID = "instance_v2"
		deliveries = s.gen2Deliveries
	}

	lease := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			// Build the snapshot at read time: a re-read after an accepted turn
			// must include it.
			return appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: s.buildThread(instanceID)},
				// The atomic relay read requires a live continuation: without a
				// Handoff the hub returns "atomic thread read returned no live
				// continuation" and the client's hydration never publishes.
				Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
			}, nil
		},
		deliveries: deliveries,
	}
	return routeAwareTestLease(lease), nil
}

// buildThread constructs the fixture thread with two completed turns and the
// given instanceId.
func (s *retirementBrowserRelaySource) buildThread(instanceID string) appwire.Thread {
	capabilities := appwire.ThreadCapabilities{
		Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true,
		ForkFromTurn: true, Shutdown: true, ChangeModel: true, ChangeVisionModel: true,
		Queue: true, Goal: true, Rename: true,
	}
	s.degradedMu.Lock()
	degraded := s.degraded
	s.degradedMu.Unlock()
	if degraded {
		capabilities.Steer = false
		capabilities.Queue = false
		capabilities.Goal = false
		capabilities.ChangeModel = false
		capabilities.ChangeVisionModel = false
	}
	return appwire.Thread{
		ID:            s.threadID,
		SessionID:     s.threadID,
		Name:          "retirement browser fixture",
		Preview:       "retirement test thread",
		ModelProvider: "anthropic/claude-sonnet-4-5",
		CreatedAt:     1000,
		UpdatedAt:     1000,
		Status:        appwire.ThreadStatus{Type: "idle"},
		CWD:           "/tmp/retirement-browser-test",
		CLIVersion:    "1.0.0",
		Source:        "evener",
		Evener: appwire.EvenerThread{
			Ref:                        s.ref,
			InstanceID:                 instanceID,
			Capabilities:               capabilities,
			Queue:                      appwire.QueueState{Revision: 0},
			MutationStateAuthoritative: true,
		},
		Turns: s.threadTurns(),
	}
}

// threadTurns returns the fixture transcript: the two persisted turns plus every
// turn this source has accepted since, so a re-read after a turn/start returns
// the accepted turn rather than silently dropping it. Turns are item-mode
// fragments because the client's default read is item-mode.
func (s *retirementBrowserRelaySource) threadTurns() []appwire.Turn {
	fragment := func(id string, entry int, itemType, text string) appwire.Turn {
		return appwire.Turn{
			ID:        id,
			Status:    "completed",
			ItemsView: appwire.TurnItemsViewFragment,
			Items: []appwire.ThreadItem{{
				Type: itemType, ID: id + "_item", TurnID: id, Text: text, Status: "completed",
				TranscriptKey: id + "/0", Position: &appwire.ThreadItemPosition{Entry: uint64(entry), Item: 0},
			}},
		}
	}
	turns := []appwire.Turn{
		fragment("turn_fixture_1", 0, "userMessage", "First turn"),
		fragment("turn_fixture_2", 1, "agentMessage", "Second turn reply"),
	}
	s.acceptedMu.Lock()
	defer s.acceptedMu.Unlock()
	for index, accepted := range s.accepted {
		turns = append(turns, fragment(accepted.id, 2+index, "userMessage", accepted.text))
	}
	return turns
}

func mustMarshalJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustMarshalJSON: %v", err))
	}
	return b
}

// ---- TestRetirementBrowser -------------------------------------------------

func TestRetirementBrowser(t *testing.T) {
	if !*retirementBrowserFlag {
		t.Skip("run with -retirement-browser to enable; npm retirementguard passes this flag automatically")
	}

	const (
		threadID = "sess_retirement_browser"
		ref      = "local:" + threadID
	)

	source := newRetirementBrowserRelaySource(threadID, ref)

	sources := appsource.NewRegistry()
	sources.Add(source)

	// Build the Hub appserver. Use a minimal WebConfig — no AuthToken means
	// the auth guard is disabled (same as every other test in this package).
	//
	// The real browser SPA validates the initialize response, which requires a
	// well-formed navigation capability (a non-empty generationId). A bare
	// newHubAppServer config advertises NavigationCapability{Version:1} with an
	// empty generation, so build a real generation here — the same helper other
	// hub tests use — and hand it to the navigation-aware constructor.
	navigationSource := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	navigation := newTestNavigationService(t, navigationSource)
	if _, err := navigation.readV2(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}, nil); err != nil {
		t.Fatalf("build fixture navigation generation: %v", err)
	}
	appServer := newHubAppServerWithNavigation(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources, navigation, nil)

	// Mux: /rpc → Hub WebSocket, /fixture/retire → retirement trigger.
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", appServer.ServeWebSocket)
	mux.HandleFunc("/fixture/retire", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// CORS: the browser harness connects from a different origin
		// (the Vite dev server port). Test-only; no production path uses this.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		source.retire()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	// Handle CORS preflight for the retire endpoint.
	mux.HandleFunc("/fixture/retire/options", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
	})
	// /fixture/degrade makes the replacement advertise unavailable
	// queue/steer/settings for the retained-input, no-auto-resume assertion.
	mux.HandleFunc("/fixture/degrade", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		source.degrade()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	hub := httptest.NewServer(mux)
	t.Cleanup(hub.Close)

	// Build connection endpoints. The browser harness connects to the guard's
	// own Vite origin and lets Vite's /rpc proxy forward the WebSocket to the
	// fixture Hub: the Hub's coder/websocket Accept enforces same-origin, and a
	// direct ws://127.0.0.1:<hub>/rpc upgrade from the Vite-served page carries
	// a different Origin host and is refused. Proxying also exercises the same
	// /rpc route a real dev SPA uses.
	hubWS := hub.URL + "/rpc"
	retireURL := hub.URL + "/fixture/retire"
	degradeURL := hub.URL + "/fixture/degrade"

	// Artifact directory: written by the Node guard; survives a passing run
	// because it is outside the scratch test-web-browser.sh deletes.
	// A caller may name a durable directory (RETIREMENT_BROWSER_ARTIFACT_DIR) so
	// the evidence outlives the test's own temp root.
	artifactDir := os.Getenv("RETIREMENT_BROWSER_ARTIFACT_DIR")
	if artifactDir == "" {
		artifactDir = filepath.Join(t.TempDir(), "retirementguard-artifacts")
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}

	// Spawn the retirementguard Node runner from cmd/evener-hub (the working
	// directory the plan specifies). The runner starts Vite + Chrome,
	// connects to this Hub fixture, and writes artifacts to artifactDir.
	//
	// This is NOT a recursive go test invocation: the runner is a Node script
	// that talks to THIS fixture directly; it does not run another go test.
	cmd := exec.CommandContext(context.Background(), "node", "frontend/scripts/retirementguard/run.mjs")
	cmd.Env = append(os.Environ(),
		"RETIREMENT_HUB_URL="+hubWS,
		"RETIREMENT_RETIRE_URL="+retireURL,
		"RETIREMENT_DEGRADE_URL="+degradeURL,
		"RETIREMENT_REF="+ref,
		"RETIREMENT_ARTIFACT_DIR="+artifactDir,
		// The guard's Vite dev server proxies /rpc to this fixture Hub.
		"EVENER_HUB_ADDR="+hub.URL,
	)
	// Run from cmd/evener-hub so relative paths in the runner (e.g. "frontend/…")
	// resolve correctly. The CWD of the current test binary is already
	// cmd/evener-hub (Go test binary CWD = package directory).
	cmd.Dir = "."
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	t.Logf("retirementguard artifacts will be written to %s", artifactDir)
	t.Logf("fixture Hub: %s (rpc url) / %s (retire)", hubWS, retireURL)

	if err := cmd.Run(); err != nil {
		// Print artifact directory for post-mortem even on failure.
		t.Logf("retirementguard artifacts: %s", artifactDir)
		t.Fatalf("retirementguard exited non-zero: %v", err)
	}

	// Verify the artifact directory contains the expected machine-readable result.
	resultPath := filepath.Join(artifactDir, "result.json")
	resultBytes, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("result.json not found at %s: %v", resultPath, err)
	}
	var result map[string]any
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("result.json malformed: %v", err)
	}
	if assertions, ok := result["assertions"].(string); !ok || assertions != "pass" {
		t.Fatalf("retirementguard result.json assertions=%q, want \"pass\"; content: %s", result["assertions"], resultBytes)
	}

	// The late-old-generation-frame assertion is only meaningful if the stale
	// frame was really forwarded to the browser's subscription. The hub fanout
	// acknowledges each forwarded delivery; wait for that acknowledgement.
	select {
	case <-source.staleForwarded:
	case <-time.After(2 * time.Second):
		t.Fatal("stale old-generation frame was never acknowledged as forwarded; the late-frame assertion would be vacuous")
	}

	// The lost-reply scenario: the first turn/start for the retry draft failed
	// retryably, and the client must have replayed the SAME mutation id exactly
	// once (a regenerated id would be a second, distinct user turn).
	source.retryMu.Lock()
	retryIDs := append([]string(nil), source.retryMutationIDs...)
	source.retryMu.Unlock()
	if len(retryIDs) != 2 || retryIDs[0] == "" || retryIDs[0] != retryIDs[1] {
		t.Fatalf("lost start reply retry attempts = %v, want the same non-empty clientMutationId twice", retryIDs)
	}
	select {
	case <-source.retryAccepted:
	default:
		t.Fatal("the replayed turn/start was never accepted")
	}

	// With queue/steer/settings unavailable, the retained unsent draft must
	// never be auto-submitted: no turn/start may carry it.
	source.degradedMu.Lock()
	startTexts := append([]string(nil), source.turnStartText...)
	source.degradedMu.Unlock()
	for _, text := range startTexts {
		if text == retainedDraftText {
			t.Fatalf("queue/steer/settings unavailability auto-resumed the daemon: turn/start carried %q", text)
		}
	}

	// Verify three screenshots exist.
	for _, name := range []string{"01-before-retirement.png", "02-after-retirement.png", "03-after-send.png"} {
		p := filepath.Join(artifactDir, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected screenshot %s: %v", p, err)
		}
	}

	t.Logf("retirementguard ok; artifacts at %s", artifactDir)
}
