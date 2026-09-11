package server

import (
	"context"
	"encoding/json"
	"errors"
	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// Missing router admission must not let a daemon runtime read invoke a handler
// after its owner has fenced access. This uses the actual registered route.
func TestRetirementAdmissionRefusesRoutedRead(t *testing.T) {
	s := NewServer(ServerConfig{})
	admission, ok := any(s).(interface {
		SetRetirementAdmission(func(context.Context, string) (func(), error))
	})
	if !ok {
		t.Fatal("server has no retirement admission hook")
	}
	refused := errors.New("retirement unavailable")
	var method string
	admission.SetRetirementAdmission(func(_ context.Context, m string) (func(), error) { method = m; return nil, refused })
	_, err := s.AppServer().Router().Dispatch(context.Background(), appwire.Request{Method: appwire.MethodThreadRead})
	if !errors.Is(err, refused) {
		t.Fatalf("routed read = %v, want retirement refusal", err)
	}
	if method != "read" {
		t.Fatalf("admission method = %q", method)
	}
}

func TestRetirementReasoningRPCPropagatesRefusal(t *testing.T) {
	s, root, c := retirementEngineServer(t)
	before := root.Meta()
	if claim, _, err := c.TryClaim(true); err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	// No outer admission is installed: the real callback's engine fence alone
	// must report refusal through the actual route, never EmptyResponse success.
	_, err := s.AppServer().Router().Dispatch(context.Background(), appwire.Request{Method: appwire.MethodThreadReasoningEffortSet, Params: []byte(`{"reasoningEffort":"high"}`)})
	if !errors.Is(err, agent.ErrRetirementUnavailable) {
		t.Fatalf("reasoning RPC = %v, want engine retirement refusal", err)
	}
	if root.Meta().Config.ReasoningEffort != before.Config.ReasoningEffort {
		t.Fatal("refused reasoning changed persisted config")
	}
}

func TestRetirementAdmissionCatalogCoverage(t *testing.T) {
	expected := map[string]string{
		appwire.MethodThreadList: "read", appwire.MethodThreadRead: "read", appwire.MethodThreadUnsubscribe: "read", appwire.MethodThreadTurnsList: "read", appwire.MethodEvenerTasksList: "read", appwire.MethodEvenerJobsList: "read", appwire.MethodEvenerJobsOutput: "read", appwire.MethodModelList: "read",
		appwire.MethodThreadClear: "mutation", appwire.MethodThreadModelSet: "mutation", appwire.MethodEvenerThreadNameSet: "mutation", appwire.MethodThreadReasoningEffortSet: "mutation", appwire.MethodThreadVisionModelSet: "mutation", appwire.MethodThreadCompactStart: "mutation", appwire.MethodTurnStart: "mutation", appwire.MethodTurnSteer: "mutation", appwire.MethodTurnInterrupt: "mutation", appwire.MethodTurnQueue: "mutation", appwire.MethodTurnDrainAsSteer: "mutation", appwire.MethodTurnPromoteQueuedAsSteer: "mutation", appwire.MethodTurnCancelQueued: "mutation", appwire.MethodGoalSet: "mutation", appwire.MethodEvenerSandboxEscalationResolve: "mutation", appwire.MethodThreadShutdown: "control",
	}
	catalog := appwire.CatalogMethodNames(appwire.ScopeDaemon)
	if len(daemonRetirementAccessKinds) != len(catalog) {
		t.Fatalf("access table has %d entries, catalog has %d", len(daemonRetirementAccessKinds), len(catalog))
	}
	for method, kind := range daemonRetirementAccessKinds {
		if expected[method] != kind {
			t.Fatalf("extra or wrong classification %q=%q", method, kind)
		}
	}
	if _, ok := daemonRetirementAccess("unknown/runtime"); ok {
		t.Fatal("unknown method classified")
	}
	if len(catalog) != len(expected) {
		t.Fatalf("classification/catalog count: %d vs %d", len(expected), len(catalog))
	}
	for _, method := range catalog {
		want, ok := expected[method]
		if !ok {
			t.Fatalf("unclassified catalog method %q", method)
		}
		t.Run(method, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			var got string
			refused := errors.New("admission refusal")
			s.SetRetirementAdmission(func(_ context.Context, kind string) (func(), error) { got = kind; return nil, refused })
			_, err := s.AppServer().Router().Dispatch(context.Background(), appwire.Request{Method: method})
			if want == "control" {
				if got != "" {
					t.Fatalf("control counted as activity: %q", got)
				}
				return
			}
			if got != want || !errors.Is(err, refused) {
				t.Fatalf("routed classification = %q %v, want %q refusal", got, err, want)
			}
		})
	}
	for _, method := range appwire.ConnectionMethodNames() {
		s := NewServer(ServerConfig{})
		s.SetRetirementAdmission(func(context.Context, string) (func(), error) {
			t.Errorf("connection %q borrowed runtime", method)
			return func() {}, nil
		})
		s.AppServer().Router().Dispatch(context.Background(), appwire.Request{Method: method})
	}
}

// Standard-library clock adapters implement the public aliases without reaching
// into agent/internal. No timer is armed by these manually claimed fixtures.
type retirementRouteClock struct{}

func (retirementRouteClock) Now() time.Time                         { return time.Now() }
func (retirementRouteClock) Sleep(d time.Duration)                  { time.Sleep(d) }
func (retirementRouteClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (retirementRouteClock) AfterFunc(d time.Duration, f func()) agent.RetirementTimer {
	return retirementRouteTimer{time.AfterFunc(d, f)}
}
func (retirementRouteClock) NewTimer(d time.Duration) agent.RetirementTimer {
	return retirementRouteTimer{time.NewTimer(d)}
}
func (retirementRouteClock) NewTicker(d time.Duration) agent.RetirementTicker {
	return retirementRouteTicker{time.NewTicker(d)}
}

type retirementRouteTimer struct{ *time.Timer }

func (t retirementRouteTimer) C() <-chan time.Time { return t.Timer.C }

type retirementRouteTicker struct{ *time.Ticker }

func (t retirementRouteTicker) C() <-chan time.Time { return t.Ticker.C }

func retirementEngineServer(t *testing.T, adapters ...llm.ProviderAdapter) (*Server, *agent.Session, *agent.RetirementController) {
	t.Helper()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&blockingServerAdapter{name: "openai", started: make(chan struct{}), done: make(chan error, 1)})
	for _, adapter := range adapters {
		client.Register(adapter)
	}
	root, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	c, err := agent.NewRetirementController(0, retirementRouteClock{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", root.ID())
	s.SetReasoningEffortFunc(root.SetReasoningEffort)
	s.SetModelFunc(root.SetModel)
	s.SetVisionModelFunc(root.SetVisionModel)
	s.SetCompactFunc(root.Compact)
	s.SetNameFunc(root.Rename)
	s.SetGoalFunc(func(objective string) (bool, error) {
		if objective == "" {
			return false, root.ClearGoal()
		}
		return root.SetGoal(context.Background(), objective)
	})
	s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Start: root.AcceptClientMutationStart, Queue: root.AcceptClientMutationQueue, Steer: root.AcceptClientMutationSteer, Drain: root.AcceptClientMutationDrainAsSteer, Promote: root.AcceptClientMutationPromoteQueuedAsSteer, Cancel: root.AcceptClientMutationCancelQueued, Interrupt: func(ctx context.Context, p appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
		return root.InterruptClientMutation(ctx, p, nil)
	}})
	return s, root, c
}

func TestRetirementDirectSetterCallbackErrorsReachRoute(t *testing.T) {
	s, root, c := retirementEngineServer(t)
	if err := root.Rename("original-name"); err != nil {
		t.Fatal(err)
	}
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v, %v", state, err)
	}
	defer func() {
		if err := c.Abort(claim, ""); err != nil {
			t.Error(err)
		}
	}()
	// No outer route admission: this proves the actual callback error, not the
	// already-covered router fence, reaches the caller.
	for _, tc := range []struct {
		method string
		params any
	}{
		{appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Name: "refused-name"}},
		{appwire.MethodGoalSet, appwire.GoalSetParams{Objective: ""}},
	} {
		t.Run(tc.method, func(t *testing.T) {
			raw, err := json.Marshal(tc.params)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.AppServer().Router().Dispatch(context.Background(), appwire.Request{Method: tc.method, Params: raw})
			if !errors.Is(err, agent.ErrRetirementUnavailable) {
				t.Fatalf("direct callback lifecycle error lost: %v", err)
			}
		})
	}
	if got := root.Meta().Name; got != "original-name" {
		t.Fatalf("refused route changed name: %q", got)
	}
}

type retirementGoalSettlementAdapter struct {
	step int
}

func (*retirementGoalSettlementAdapter) Name() string { return "openai" }
func (*retirementGoalSettlementAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (a *retirementGoalSettlementAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	var call llm.ToolCallData
	switch a.step {
	case 0:
		call = llm.ToolCallData{ID: "original-goal-completion", Name: "update_goal", Type: "function", Arguments: []byte(`{"status":"complete","intent":"completing seeded goal"}`)}
	case 1:
		call = llm.ToolCallData{ID: "original-goal-report", Name: "communicate", Type: "function", Arguments: []byte(`{"message":"settled","end_turn":true,"output":{"message":"","data":{},"artifacts":[]}}`)}
	default:
		return llm.Response{}, errors.New("unexpected provider call after goal settlement")
	}
	a.step++
	return llm.Response{Provider: "openai", Model: req.Model, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}, nil
}

func TestRetirementRoutedEngineEffectsRefused(t *testing.T) {
	s, root, c := retirementEngineServer(t, &retirementGoalSettlementAdapter{})
	if err := root.Rename("opaque-original"); err != nil {
		t.Fatal(err)
	}
	if _, err := root.SetGoal(context.Background(), "opaque-goal"); err != nil {
		t.Fatal(err)
	}
	if _, err := root.ProcessInput(context.Background(), "settle", nil); err != nil {
		t.Fatal(err)
	}
	if g := root.Meta().Goal; g == nil || g.Objective != "opaque-goal" || g.Status != "complete" {
		t.Fatalf("original goal did not settle: %+v", g)
	}
	before := root.Meta()
	s.SetRetirementAdmission(func(_ context.Context, kind string) (func(), error) {
		if kind == "read" {
			return c.Borrow()
		}
		return c.BeginMutation(root.ID(), "admission")
	})
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	input := []appwire.InputItem{{Type: "text", Text: "opaque-input"}}
	cases := []struct {
		method string
		params any
	}{
		{appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "route-start", ExpectedInstanceID: root.ID(), Input: input}},
		{appwire.MethodTurnQueue, appwire.TurnQueueParams{ClientMutationID: "route-queue", ExpectedInstanceID: root.ID(), Input: input}},
		{appwire.MethodTurnSteer, appwire.TurnSteerParams{ClientMutationID: "route-steer", ExpectedInstanceID: root.ID(), Input: input}},
		{appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{ClientMutationID: "route-drain", ExpectedInstanceID: root.ID(), Input: input}},
		{appwire.MethodTurnPromoteQueuedAsSteer, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "route-promote", ExpectedInstanceID: root.ID()}},
		{appwire.MethodTurnCancelQueued, appwire.TurnCancelQueuedParams{ClientMutationID: "route-cancel", ExpectedInstanceID: root.ID()}},
		{appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{ClientMutationID: "route-interrupt", ExpectedInstanceID: root.ID()}},
		{appwire.MethodThreadReasoningEffortSet, appwire.ThreadReasoningEffortSetParams{ReasoningEffort: "high"}},
		{appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{Model: "gpt-5.2"}},
		{appwire.MethodThreadVisionModelSet, appwire.ThreadVisionModelSetParams{VisionModel: "off"}},
		{appwire.MethodThreadCompactStart, appwire.ThreadCompactStartParams{}},
		{appwire.MethodEvenerThreadNameSet, appwire.ThreadNameSetParams{Name: "opaque-new"}},
		{appwire.MethodGoalSet, appwire.GoalSetParams{Objective: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			raw, err := json.Marshal(tc.params)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.AppServer().Router().Dispatch(context.Background(), appwire.Request{Method: tc.method, Params: raw})
			if !errors.Is(err, agent.ErrRetirementUnavailable) {
				t.Fatalf("real routed effect = %v", err)
			}
		})
	}
	// Meta stamps UpdatedAt from the clock on every read (session_state.go),
	// unlike every other field here. Compare actual effects, not read time.
	after := root.Meta()
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("routed refusal changed engine metadata")
	}
	if root.QueueDepth() != 0 || len(root.SteeringQueueSnapshot()) != 0 {
		t.Fatal("routed refusal changed input")
	}
	// Reads may finish while preparing, but cannot enter after commit.
	if _, err := s.AppServer().Router().Dispatch(context.Background(), appwire.Request{Method: appwire.MethodThreadList}); err != nil {
		t.Fatalf("preparing read: %v", err)
	}
	if err := c.Commit(claim); err != nil {
		t.Fatal(err)
	}
	if err := c.DrainReaders(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppServer().Router().Dispatch(context.Background(), appwire.Request{Method: appwire.MethodThreadList}); !errors.Is(err, agent.ErrRetirementUnavailable) {
		t.Fatalf("committed read: %v", err)
	}
}

func TestRetirementRoutedNestedAdmissionReleases(t *testing.T) {
	s, root, c := retirementEngineServer(t)
	s.SetRetirementAdmission(func(_ context.Context, kind string) (func(), error) {
		if kind == "read" {
			return c.Borrow()
		}
		return c.BeginMutation(root.ID(), "admission")
	})
	out, err := s.AppServer().Router().Dispatch(context.Background(), appwire.Request{Method: appwire.MethodThreadReasoningEffortSet, Params: []byte(`{"reasoningEffort":"high"}`)})
	if err != nil || root.ReasoningEffort() != "high" {
		t.Fatalf("nested admitted setter: %v", err)
	}
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("handler leaked mutation admission: %v", err)
	}
	if err := c.Commit(claim); err != nil {
		t.Fatal(err)
	}
	// Detached response serialization needs no runtime lease after the fence.
	if _, err := json.Marshal(out); err != nil {
		t.Fatal(err)
	}
}
