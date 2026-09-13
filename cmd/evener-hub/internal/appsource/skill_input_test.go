package appsource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// skillInputGateRecorder captures what a real test daemon actually received,
// so a gate rejection is distinguished from a forwarding: counts stay zero
// when the source refuses to send the mutation at all.
type skillInputGateRecorder struct {
	mu     sync.Mutex
	reads  int
	starts []appwire.TurnStartParams
	steers []appwire.TurnSteerParams
	queues []appwire.TurnQueueParams
	drains []appwire.TurnDrainAsSteerParams
}

func (r *skillInputGateRecorder) recordRead() {
	r.mu.Lock()
	r.reads++
	r.mu.Unlock()
}

func (r *skillInputGateRecorder) recordStart(params appwire.TurnStartParams) {
	r.mu.Lock()
	r.starts = append(r.starts, params)
	r.mu.Unlock()
}

func (r *skillInputGateRecorder) recordSteer(params appwire.TurnSteerParams) {
	r.mu.Lock()
	r.steers = append(r.steers, params)
	r.mu.Unlock()
}

func (r *skillInputGateRecorder) recordQueue(params appwire.TurnQueueParams) {
	r.mu.Lock()
	r.queues = append(r.queues, params)
	r.mu.Unlock()
}

func (r *skillInputGateRecorder) recordDrain(params appwire.TurnDrainAsSteerParams) {
	r.mu.Lock()
	r.drains = append(r.drains, params)
	r.mu.Unlock()
}

func (r *skillInputGateRecorder) readCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

func (r *skillInputGateRecorder) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

func (r *skillInputGateRecorder) steerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.steers)
}

func (r *skillInputGateRecorder) queueCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.queues)
}

func (r *skillInputGateRecorder) drainCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.drains)
}

func (r *skillInputGateRecorder) mutationCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts) + len(r.steers) + len(r.queues) + len(r.drains)
}

func (r *skillInputGateRecorder) lastStart() appwire.TurnStartParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.starts) == 0 {
		return appwire.TurnStartParams{}
	}
	return r.starts[len(r.starts)-1]
}

func (r *skillInputGateRecorder) lastSteer() appwire.TurnSteerParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.steers) == 0 {
		return appwire.TurnSteerParams{}
	}
	return r.steers[len(r.steers)-1]
}

func (r *skillInputGateRecorder) lastQueue() appwire.TurnQueueParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queues) == 0 {
		return appwire.TurnQueueParams{}
	}
	return r.queues[len(r.queues)-1]
}

func (r *skillInputGateRecorder) lastDrain() appwire.TurnDrainAsSteerParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.drains) == 0 {
		return appwire.TurnDrainAsSteerParams{}
	}
	return r.drains[len(r.drains)-1]
}

// startSkillInputGateDaemon stands up a real AppWire endpoint whose
// thread/read answers with the given capabilities (or readErr), recording
// every input-bearing mutation it receives. The handlers accept anything:
// whether a skill selection reaches them at all is the behavior under test.
func startSkillInputGateDaemon(t *testing.T, caps appwire.ThreadCapabilities, readErr error) (*httptest.Server, *skillInputGateRecorder) {
	t.Helper()
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	rec := &skillInputGateRecorder{}
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		rec.recordRead()
		if readErr != nil {
			return appwire.ThreadReadResponse{}, readErr
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: "local:th_1", Capabilities: caps},
		}}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		rec.recordStart(params)
		return appwire.TurnStartResponse{
			Turn:    appwire.Turn{ID: "turn_1"},
			Receipt: appwire.MutationReceipt{ClientMutationID: params.ClientMutationID, Disposition: appwire.MutationDispositionApplied, ThreadID: "th_1", ProjectionState: appwire.MutationProjectionReflected},
		}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnSteer, func(_ context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
		rec.recordSteer(params)
		return appwire.TurnSteerResponse{Receipt: appwire.MutationReceipt{
			ClientMutationID: params.ClientMutationID, Disposition: appwire.MutationDispositionApplied,
			ThreadID: "th_1", ProjectionState: appwire.MutationProjectionReflected,
		}}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
		rec.recordQueue(params)
		return appwire.TurnQueueResponse{Receipt: appwire.MutationReceipt{
			ClientMutationID: params.ClientMutationID, Disposition: appwire.MutationDispositionApplied,
			ThreadID: "th_1", ProjectionState: appwire.MutationProjectionReflected,
		}}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
		rec.recordDrain(params)
		return appwire.TurnDrainAsSteerResponse{Receipt: appwire.MutationReceipt{
			ClientMutationID: params.ClientMutationID, Disposition: appwire.MutationDispositionApplied,
			ThreadID: "th_1", ProjectionState: appwire.MutationProjectionReflected,
		}}, nil
	})
	srv := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	t.Cleanup(srv.Close)
	return srv, rec
}

func skillInputGateSource(t *testing.T, srv *httptest.Server) *LocalDaemonSource {
	t.Helper()
	return NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + srv.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, srv.Client())
}

// skillSelectionInput is one prose item plus one canonical skill selection,
// the composer's shape for a send with a skill picked.
func skillSelectionInput() []appwire.InputItem {
	return []appwire.InputItem{
		{Type: "text", Text: "REQUEST_712"},
		{Type: "skill", Name: "pkg:probe"},
	}
}

// assertSkillSelectionPreserved pins that a forwarded mutation kept the
// selection canonical: the skill item travels with its name, no synthetic
// slash-prose text item replaces or joins it, and the prose item is intact.
func assertSkillSelectionPreserved(t *testing.T, input []appwire.InputItem) {
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

// skillInputMutations exercises every input-bearing mutation the source
// forwards, so the gate is proven per endpoint rather than for start alone.
type skillInputMutation struct {
	name   string
	invoke func(ctx context.Context, source *LocalDaemonSource) error
	count  func(*skillInputGateRecorder) int
	last   func(*skillInputGateRecorder) []appwire.InputItem
}

func skillInputMutations() []skillInputMutation {
	return []skillInputMutation{
		{
			name: "turn/start",
			invoke: func(ctx context.Context, source *LocalDaemonSource) error {
				_, err := source.StartTurn(ctx, appwire.TurnStartParams{Ref: "local:th_1", ClientMutationID: "mutation", ExpectedInstanceID: "sess_1", Input: skillSelectionInput()})
				return err
			},
			count: (*skillInputGateRecorder).startCount,
			last: func(rec *skillInputGateRecorder) []appwire.InputItem {
				return rec.lastStart().Input
			},
		},
		{
			name: "turn/steer",
			invoke: func(ctx context.Context, source *LocalDaemonSource) error {
				_, err := source.SteerTurn(ctx, appwire.TurnSteerParams{Ref: "local:th_1", ClientMutationID: "mutation", ExpectedInstanceID: "sess_1", Input: skillSelectionInput()})
				return err
			},
			count: (*skillInputGateRecorder).steerCount,
			last: func(rec *skillInputGateRecorder) []appwire.InputItem {
				return rec.lastSteer().Input
			},
		},
		{
			name: "turn/queue",
			invoke: func(ctx context.Context, source *LocalDaemonSource) error {
				_, err := source.QueueTurn(ctx, appwire.TurnQueueParams{Ref: "local:th_1", ClientMutationID: "mutation", ExpectedInstanceID: "sess_1", Input: skillSelectionInput()})
				return err
			},
			count: (*skillInputGateRecorder).queueCount,
			last: func(rec *skillInputGateRecorder) []appwire.InputItem {
				return rec.lastQueue().Input
			},
		},
		{
			name: "turn/drainAsSteer",
			invoke: func(ctx context.Context, source *LocalDaemonSource) error {
				_, err := source.DrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{Ref: "local:th_1", ClientMutationID: "mutation", ExpectedInstanceID: "sess_1", ExpectedQueueRevision: 1, Input: skillSelectionInput()})
				return err
			},
			count: (*skillInputGateRecorder).drainCount,
			last: func(rec *skillInputGateRecorder) []appwire.InputItem {
				return rec.lastDrain().Input
			},
		},
	}
}

// TestSkillInputMutationsAreGatedOnTargetCapability is the source-level
// fail-closed gate: a skill selection may only be forwarded to a daemon that
// advertised ThreadCapabilities.SkillInput, and an absent capability (an older
// daemon, or a synthesized thread) reads exactly like a false one. The
// forwarding assertion proves the gate consulted the target's read rather than
// forwarding blindly.
func TestSkillInputMutationsAreGatedOnTargetCapability(t *testing.T) {
	cases := []struct {
		name      string
		caps      appwire.ThreadCapabilities
		supported bool
	}{
		{name: "capability false", caps: appwire.ThreadCapabilities{SkillInput: false}, supported: false},
		{name: "capability absent (older daemon)", caps: appwire.ThreadCapabilities{}, supported: false},
		{name: "capability true", caps: appwire.ThreadCapabilities{SkillInput: true}, supported: true},
	}
	for _, mutation := range skillInputMutations() {
		for _, tc := range cases {
			t.Run(mutation.name+" "+tc.name, func(t *testing.T) {
				srv, rec := startSkillInputGateDaemon(t, tc.caps, nil)
				source := skillInputGateSource(t, srv)
				err := mutation.invoke(context.Background(), source)
				if tc.supported {
					if err != nil {
						t.Fatalf("mutation with a supporting target rejected: %v", err)
					}
					if got := mutation.count(rec); got != 1 {
						t.Fatalf("forwarded mutations=%d, want 1", got)
					}
					assertSkillSelectionPreserved(t, mutation.last(rec))
					if rec.readCount() == 0 {
						t.Fatal("capability was not read from the target before forwarding")
					}
					return
				}
				if err == nil {
					t.Fatal("skill input forwarded to a target that did not advertise skill input support")
				}
				if got := mutation.count(rec); got != 0 {
					t.Fatalf("unsupported skill input was forwarded: %+v", got)
				}
				if got := rec.mutationCount(); got != 0 {
					t.Fatalf("unsupported skill input reached the target through another mutation: %d", got)
				}
			})
		}
	}
}

// TestSkillInputReadFailureIsARejection pins the fail-closed direction: when
// the same-connection capability read fails, the mutation must not be
// forwarded as if the target had permitted it. The daemon's mutation handlers
// accept anything, so a success would mean the gate treated unreadable
// capability as permission.
func TestSkillInputReadFailureIsARejection(t *testing.T) {
	for _, mutation := range skillInputMutations() {
		t.Run(mutation.name, func(t *testing.T) {
			srv, rec := startSkillInputGateDaemon(t, appwire.ThreadCapabilities{SkillInput: true}, appwire.Unavailable("capability read failed"))
			source := skillInputGateSource(t, srv)
			if err := mutation.invoke(context.Background(), source); err == nil {
				t.Fatal("skill input forwarded although the capability read failed")
			}
			if got := mutation.count(rec); got != 0 {
				t.Fatalf("skill input forwarded after a failed capability read: %d", got)
			}
		})
	}
}

// TestSkillInputUnreachableEndpointCannotAuthorizeForwarding pins that a
// target the source cannot reach never authorizes a skill selection: nothing
// is forwarded and the caller receives the transport failure, not a success.
func TestSkillInputUnreachableEndpointCannotAuthorizeForwarding(t *testing.T) {
	for _, mutation := range skillInputMutations() {
		t.Run(mutation.name, func(t *testing.T) {
			srv, rec := startSkillInputGateDaemon(t, appwire.ThreadCapabilities{SkillInput: true}, nil)
			srv.Close()
			source := skillInputGateSource(t, srv)
			if err := mutation.invoke(context.Background(), source); err == nil {
				t.Fatal("skill input reported success against an unreachable endpoint")
			}
			if got := rec.mutationCount(); got != 0 {
				t.Fatalf("unreachable endpoint received a mutation: %d", got)
			}
		})
	}
}

// TestSkillInputPlainTextSkipsCapabilityRead pins the gate's scope: an input
// with no skill selection forwards without the extra capability read, so
// ordinary sends keep their single round trip.
func TestSkillInputPlainTextSkipsCapabilityRead(t *testing.T) {
	srv, rec := startSkillInputGateDaemon(t, appwire.ThreadCapabilities{SkillInput: false}, nil)
	source := skillInputGateSource(t, srv)
	if _, err := source.StartTurn(context.Background(), appwire.TurnStartParams{
		Ref: "local:th_1", ClientMutationID: "mutation", ExpectedInstanceID: "sess_1",
		Input: []appwire.InputItem{{Type: "text", Text: "plain send"}},
	}); err != nil {
		t.Fatalf("plain text start rejected: %v", err)
	}
	if got := rec.startCount(); got != 1 {
		t.Fatalf("plain text start forwarded=%d, want 1", got)
	}
	if got := rec.readCount(); got != 0 {
		t.Fatalf("plain text start performed %d capability reads, want 0", got)
	}
}
