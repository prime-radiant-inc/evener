package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// TestSkillInputSupportIsFailClosed pins the pure consumption gate: a skill
// item requires the target to advertise support, while an unsupported target
// still accepts input without skill selections.
func TestSkillInputSupportIsFailClosed(t *testing.T) {
	items := []appwire.InputItem{{Type: "skill", Name: "pkg:probe"}}
	for _, supported := range []bool{false, true} {
		err := appwire.ValidateSkillInputSupport(items, supported)
		if (err == nil) != supported {
			t.Fatalf("supported=%v error=%v", supported, err)
		}
	}
	if err := appwire.ValidateSkillInputSupport([]appwire.InputItem{{Type: "text", Text: "REQUEST_712"}}, false); err != nil {
		t.Fatal(err)
	}
}

// hubSkillInputRecorder captures what the daemon behind the hub actually
// received, so a hub rejection is distinguished from a forwarding.
type hubSkillInputRecorder struct {
	mu     sync.Mutex
	starts []appwire.TurnStartParams
	steers []appwire.TurnSteerParams
	queues []appwire.TurnQueueParams
	drains []appwire.TurnDrainAsSteerParams
}

func (r *hubSkillInputRecorder) recordStart(params appwire.TurnStartParams) {
	r.mu.Lock()
	r.starts = append(r.starts, params)
	r.mu.Unlock()
}

func (r *hubSkillInputRecorder) recordSteer(params appwire.TurnSteerParams) {
	r.mu.Lock()
	r.steers = append(r.steers, params)
	r.mu.Unlock()
}

func (r *hubSkillInputRecorder) recordQueue(params appwire.TurnQueueParams) {
	r.mu.Lock()
	r.queues = append(r.queues, params)
	r.mu.Unlock()
}

func (r *hubSkillInputRecorder) recordDrain(params appwire.TurnDrainAsSteerParams) {
	r.mu.Lock()
	r.drains = append(r.drains, params)
	r.mu.Unlock()
}

func (r *hubSkillInputRecorder) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

func (r *hubSkillInputRecorder) steerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.steers)
}

func (r *hubSkillInputRecorder) queueCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.queues)
}

func (r *hubSkillInputRecorder) drainCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.drains)
}

func (r *hubSkillInputRecorder) lastStart() appwire.TurnStartParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.starts) == 0 {
		return appwire.TurnStartParams{}
	}
	return r.starts[len(r.starts)-1]
}

func (r *hubSkillInputRecorder) lastSteer() appwire.TurnSteerParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.steers) == 0 {
		return appwire.TurnSteerParams{}
	}
	return r.steers[len(r.steers)-1]
}

func (r *hubSkillInputRecorder) lastQueue() appwire.TurnQueueParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queues) == 0 {
		return appwire.TurnQueueParams{}
	}
	return r.queues[len(r.queues)-1]
}

func (r *hubSkillInputRecorder) lastDrain() appwire.TurnDrainAsSteerParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.drains) == 0 {
		return appwire.TurnDrainAsSteerParams{}
	}
	return r.drains[len(r.drains)-1]
}

// startSkillInputHubDaemon stands up a real AppWire daemon endpoint whose
// thread/read advertises the given capabilities (or readErr). Its mutation
// handlers accept anything and record what arrived: whether the hub forwards a
// skill selection at all is the behavior under test.
func startSkillInputHubDaemon(t *testing.T, sessionID string, caps appwire.ThreadCapabilities, readErr error) (*httptest.Server, *hubSkillInputRecorder) {
	t.Helper()
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	rec := &hubSkillInputRecorder{}
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if readErr != nil {
			return appwire.ThreadReadResponse{}, readErr
		}
		ref := params.Ref
		if ref == "" {
			ref = "local:" + sessionID
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:    appwire.EvenerThread{Ref: ref, Capabilities: caps},
		}}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		rec.recordStart(params)
		return appwire.TurnStartResponse{
			Turn:    appwire.Turn{ID: "turn_skill"},
			Receipt: appwire.MutationReceipt{ClientMutationID: params.ClientMutationID, Disposition: appwire.MutationDispositionApplied, ThreadID: sessionID, ProjectionState: appwire.MutationProjectionReflected},
		}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnSteer, func(_ context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
		rec.recordSteer(params)
		return appwire.TurnSteerResponse{Receipt: appwire.MutationReceipt{
			ClientMutationID: params.ClientMutationID, Disposition: appwire.MutationDispositionApplied,
			ThreadID: sessionID, ProjectionState: appwire.MutationProjectionReflected,
		}}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
		rec.recordQueue(params)
		return appwire.TurnQueueResponse{Receipt: appwire.MutationReceipt{
			ClientMutationID: params.ClientMutationID, Disposition: appwire.MutationDispositionApplied,
			ThreadID: sessionID, ProjectionState: appwire.MutationProjectionReflected,
		}}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
		rec.recordDrain(params)
		return appwire.TurnDrainAsSteerResponse{Receipt: appwire.MutationReceipt{
			ClientMutationID: params.ClientMutationID, Disposition: appwire.MutationDispositionApplied,
			ThreadID: sessionID, ProjectionState: appwire.MutationProjectionReflected,
		}}, nil
	})
	srv := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	t.Cleanup(srv.Close)
	return srv, rec
}

func skillInputHubSelection() []appwire.InputItem {
	return []appwire.InputItem{
		{Type: "text", Text: "REQUEST_712"},
		{Type: "skill", Name: "pkg:probe"},
	}
}

// assertSkillInputHubSelectionPreserved pins that the daemon received the
// selection canonically: one skill item with its name, one intact prose item,
// and no synthetic slash-prose item.
func assertSkillInputHubSelectionPreserved(t *testing.T, input []appwire.InputItem) {
	t.Helper()
	var skills, texts int
	for _, item := range input {
		switch item.Type {
		case "skill":
			skills++
			if item.Name != "pkg:probe" {
				t.Errorf("forwarded skill name=%q, want pkg:probe", item.Name)
			}
		case "text":
			texts++
			if item.Text != "REQUEST_712" {
				t.Errorf("forwarded text=%q, want REQUEST_712", item.Text)
			}
		default:
			t.Errorf("forwarded unexpected item type %q", item.Type)
		}
	}
	if skills != 1 || texts != 1 {
		t.Errorf("forwarded items: skills=%d texts=%d, want one of each: %+v", skills, texts, input)
	}
}

// assertSkillInputRejection asserts a deterministic refusal: the caller sees
// an invalidParams wire error, not a transport failure or a success.
func assertSkillInputRejection(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("skill input was accepted")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("rejection %T is not a wire error: %v", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("rejection code=%d, want invalidParams: %v", wire.Code, err)
	}
}

func dialSkillInputHub(t *testing.T, hub *httptest.Server) *appwire.Client {
	t.Helper()
	client := dialHubRPC(t, hub)
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return client
}

// TestSkillInputHubGatesStartSteerQueueDrainOnTargetCapability drives the
// hub's direct input-bearing mutations across a real hub-to-daemon connection.
// A target that does not advertise skill input support must fail closed — the
// rejection reaches the caller and nothing is forwarded to the daemon, so the
// selection is never degraded into slash prose — while a supporting target
// receives the canonical selection verbatim.
func TestSkillInputHubGatesStartSteerQueueDrainOnTargetCapability(t *testing.T) {
	cases := []struct {
		name      string
		caps      appwire.ThreadCapabilities
		supported bool
	}{
		{name: "capability false", caps: appwire.ThreadCapabilities{Send: true, Steer: true, SkillInput: false}, supported: false},
		{name: "capability absent (older daemon)", caps: appwire.ThreadCapabilities{Send: true, Steer: true}, supported: false},
		{name: "capability true", caps: appwire.ThreadCapabilities{Send: true, Steer: true, SkillInput: true}, supported: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionID := hubtest.SessionID(t)
			daemon, rec := startSkillInputHubDaemon(t, sessionID, tc.caps, nil)
			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{
				PID:        401,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemon.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: "/tmp",
				Model:      "gpt-5",
			})
			roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusActive})
			roster.Refresh()
			hub := newHubRPCTestServer(t, hubcore.WebConfig{
				RunDir:      runDir,
				Roster:      roster,
				Past:        hubcore.NewPastIndex(""),
				ResumeLocks: hubcore.NewResumeLocks(),
				Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
					return rendezvous.Entry{}, errors.New("live daemon must not be replaced")
				}},
			})
			defer hub.Close()
			client := dialSkillInputHub(t, hub)
			defer client.Close()
			ref := "local:" + sessionID

			// Ordinary start.
			_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{
				Ref: ref, ClientMutationID: "skill-start", ExpectedInstanceID: sessionID,
				Input: skillInputHubSelection(),
			})
			if !tc.supported {
				assertSkillInputRejection(t, err)
				if got := rec.startCount(); got != 0 {
					t.Fatalf("ordinary start forwarded a skill selection to an unsupported target: %d", got)
				}
			} else {
				if err != nil {
					t.Fatalf("TurnStart with supporting target: %v", err)
				}
				if got := rec.startCount(); got != 1 {
					t.Fatalf("ordinary start forwarded=%d, want 1", got)
				}
				assertSkillInputHubSelectionPreserved(t, rec.lastStart().Input)
			}

			// Steer.
			var steer appwire.TurnSteerResponse
			err = client.Request(context.Background(), appwire.MethodTurnSteer, appwire.TurnSteerParams{
				Ref: ref, ClientMutationID: "skill-steer", ExpectedInstanceID: sessionID,
				Input: skillInputHubSelection(),
			}, &steer)
			if !tc.supported {
				assertSkillInputRejection(t, err)
				if got := rec.steerCount(); got != 0 {
					t.Fatalf("steer forwarded a skill selection to an unsupported target: %d", got)
				}
			} else {
				if err != nil {
					t.Fatalf("TurnSteer with supporting target: %v", err)
				}
				if got := rec.steerCount(); got != 1 {
					t.Fatalf("steer forwarded=%d, want 1", got)
				}
				assertSkillInputHubSelectionPreserved(t, rec.lastSteer().Input)
			}

			// Queue.
			var queue appwire.TurnQueueResponse
			err = client.Request(context.Background(), appwire.MethodTurnQueue, appwire.TurnQueueParams{
				Ref: ref, ClientMutationID: "skill-queue", ExpectedInstanceID: sessionID,
				Input: skillInputHubSelection(),
			}, &queue)
			if !tc.supported {
				assertSkillInputRejection(t, err)
				if got := rec.queueCount(); got != 0 {
					t.Fatalf("queue forwarded a skill selection to an unsupported target: %d", got)
				}
			} else {
				if err != nil {
					t.Fatalf("TurnQueue with supporting target: %v", err)
				}
				if got := rec.queueCount(); got != 1 {
					t.Fatalf("queue forwarded=%d, want 1", got)
				}
				assertSkillInputHubSelectionPreserved(t, rec.lastQueue().Input)
			}

			// Drain-as-steer.
			var drain appwire.TurnDrainAsSteerResponse
			err = client.Request(context.Background(), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{
				Ref: ref, ClientMutationID: "skill-drain", ExpectedInstanceID: sessionID, ExpectedQueueRevision: 1,
				Input: skillInputHubSelection(),
			}, &drain)
			if !tc.supported {
				assertSkillInputRejection(t, err)
				if got := rec.drainCount(); got != 0 {
					t.Fatalf("drain forwarded a skill selection to an unsupported target: %d", got)
				}
			} else {
				if err != nil {
					t.Fatalf("TurnDrainAsSteer with supporting target: %v", err)
				}
				if got := rec.drainCount(); got != 1 {
					t.Fatalf("drain forwarded=%d, want 1", got)
				}
				assertSkillInputHubSelectionPreserved(t, rec.lastDrain().Input)
			}
		})
	}
}

// TestSkillInputHubCapabilityReadFailureIsARejection pins fail-closed on the
// direct handlers: a daemon whose thread/read fails cannot authorize a skill
// selection even though its mutation handlers would accept one.
func TestSkillInputHubCapabilityReadFailureIsARejection(t *testing.T) {
	sessionID := hubtest.SessionID(t)
	daemon, rec := startSkillInputHubDaemon(t, sessionID, appwire.ThreadCapabilities{Send: true, Steer: true, SkillInput: true}, appwire.Unavailable("capability read failed"))
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:        402,
		Protocol:   appwire.ProtocolVersion,
		Endpoint:   "ws" + daemon.URL[len("http"):],
		SourceID:   "local",
		ThreadID:   sessionID,
		SessionID:  sessionID,
		WorkingDir: "/tmp",
		Model:      "gpt-5",
	})
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusActive})
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{}, errors.New("live daemon must not be replaced")
		}},
	})
	defer hub.Close()
	client := dialSkillInputHub(t, hub)
	defer client.Close()
	ref := "local:" + sessionID

	var steer appwire.TurnSteerResponse
	err := client.Request(context.Background(), appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref: ref, ClientMutationID: "skill-steer", ExpectedInstanceID: sessionID,
		Input: skillInputHubSelection(),
	}, &steer)
	if err == nil {
		t.Fatal("skill input forwarded although the capability read failed")
	}
	if got := rec.steerCount(); got != 0 {
		t.Fatalf("steer forwarded after a failed capability read: %d", got)
	}
}

// TestSkillInputHubColdStartGatesInitialInputOnTargetCapability covers
// thread/start's initial input: the freshly spawned session's own read decides
// whether the selection may be delivered, and a spawn whose read failed (a
// synthesized thread with no capabilities) cannot authorize it either.
func TestSkillInputHubColdStartGatesInitialInputOnTargetCapability(t *testing.T) {
	cases := []struct {
		name      string
		caps      appwire.ThreadCapabilities
		supported bool
	}{
		{name: "capability false", caps: appwire.ThreadCapabilities{Send: true, SkillInput: false}, supported: false},
		{name: "capability absent (older daemon)", caps: appwire.ThreadCapabilities{Send: true}, supported: false},
		{name: "capability true", caps: appwire.ThreadCapabilities{Send: true, SkillInput: true}, supported: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionID := hubtest.SessionID(t)
			daemon, rec := startSkillInputHubDaemon(t, sessionID, tc.caps, nil)
			runDir := t.TempDir()
			entry := rendezvous.Entry{
				PID:        403,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemon.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: "/tmp",
				Model:      "gpt-5",
			}
			spawner := &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
				writeRendezvous(t, runDir, entry)
				return entry, nil
			}}
			roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusActive})
			hub := newHubRPCTestServer(t, hubcore.WebConfig{
				RunDir:  runDir,
				Roster:  roster,
				Spawner: spawner,
				Past:    hubcore.NewPastIndex(""),
			})
			defer hub.Close()
			client := dialSkillInputHub(t, hub)
			defer client.Close()

			_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
				Model: "openai/gpt-5",
				CWD:   "/tmp",
				Input: skillInputHubSelection(),
			})
			if !tc.supported {
				assertSkillInputRejection(t, err)
				if got := rec.startCount(); got != 0 {
					t.Fatalf("cold start delivered a skill selection to an unsupported target: %d", got)
				}
			} else {
				if err != nil {
					t.Fatalf("ThreadStart with supporting target: %v", err)
				}
				if got := rec.startCount(); got != 1 {
					t.Fatalf("cold start delivered=%d, want 1", got)
				}
				assertSkillInputHubSelectionPreserved(t, rec.lastStart().Input)
			}
		})
	}
}

// TestSkillInputHubResumedStartRechecksCapability covers the turn/start
// auto-resume path: a selection for a stopped session is rechecked against
// the daemon the resume actually produced, so an older replacement daemon
// still fails closed while a supporting one receives the selection.
func TestSkillInputHubResumedStartRechecksCapability(t *testing.T) {
	cases := []struct {
		name      string
		caps      appwire.ThreadCapabilities
		supported bool
	}{
		{name: "capability false", caps: appwire.ThreadCapabilities{Send: true, SkillInput: false}, supported: false},
		{name: "capability absent (older daemon)", caps: appwire.ThreadCapabilities{Send: true}, supported: false},
		{name: "capability true", caps: appwire.ThreadCapabilities{Send: true, SkillInput: true}, supported: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			workingDir := t.TempDir()
			stateDir := filepath.Join(root, "projects", "project-past-0000000000")
			sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
			past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}

			daemon, rec := startSkillInputHubDaemon(t, sessionID, tc.caps, nil)
			runDir := t.TempDir()
			spawner := &fakeRPCSpawner{resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
				if req.WorkingDir != workingDir {
					t.Fatalf("resume request=%+v", req)
				}
				entry := rendezvous.Entry{
					PID:        404,
					Protocol:   appwire.ProtocolVersion,
					Endpoint:   "ws" + daemon.URL[len("http"):],
					SourceID:   "local",
					ThreadID:   sessionID,
					SessionID:  sessionID,
					WorkingDir: workingDir,
				}
				writeRendezvous(t, runDir, entry)
				return entry, nil
			}}
			roster := hubcore.NewRoster(runDir, nil)
			hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
			defer hub.Close()
			client := dialSkillInputHub(t, hub)
			defer client.Close()

			_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{
				ClientMutationID:   "skill-resumed-start",
				ExpectedInstanceID: sessionID,
				Ref:                "local:" + sessionID,
				Input:              skillInputHubSelection(),
			})
			if !tc.supported {
				assertSkillInputRejection(t, err)
				if got := rec.startCount(); got != 0 {
					t.Fatalf("resumed start delivered a skill selection to an unsupported replacement: %d", got)
				}
			} else {
				if err != nil {
					t.Fatalf("resumed TurnStart with supporting target: %v", err)
				}
				if got := rec.startCount(); got != 1 {
					t.Fatalf("resumed start delivered=%d, want 1", got)
				}
				assertSkillInputHubSelectionPreserved(t, rec.lastStart().Input)
			}
		})
	}
}
