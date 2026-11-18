package agent

import (
	"context"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestTheSkillReloadDecidesEligibilityInsideItsCapture pins that the reload's
// rebuild answers eligibility from the capture it took, not from live state read
// after it.
//
// The rebuild reads three things that must describe one moment: the resident
// turns, the reconciled holds that filter them, and whether an original watch
// frame's wake has been admitted. The first two are copied under attentionMu;
// the admission predicate consults the controller's wake and rider sets, which
// change as arms commit. Asking it after the unlock lets a frame arm in that
// window: the rebuild then admits the frame against the new wake state while
// still excluding the report from the old hold snapshot that same arm released.
// The request presents the frame alone and counts the released report as
// presented, which is the order the release exists to prevent.
//
// The seam admits the frame in exactly that window. The assertion is on the
// request: it must not carry a frame whose admission arrived after the capture.
func TestTheSkillReloadDecidesEligibilityInsideItsCapture(t *testing.T) {
	const marker = "the frame admitted inside the reload's capture window"
	profile := namedInstanceProfile("ant", "anthropic", "claude-sonnet-4-5")
	stateDir := realTempDirForTest(t)
	runtime := newSession(t, withAdapter(&fakeAdapter{name: "ant"}), withProfile(profile), withDir(stateDir), withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}))
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	runtime.delegateController = c
	runtime.owningDelegateID = lease.delegateID
	c.mu.Lock()
	c.live[lease.delegateID].runtime = runtime
	c.live[lease.delegateID].binding.runtime = runtime
	c.mu.Unlock()

	missing := filepath.Join(stateDir, "skills", "opaque", "SKILL.md")
	if err := runtime.persistSkillToolObligations(&schema.SkillTurnState{Obligations: []schema.SkillDeliveryObligation{{
		InvocationID: "inv-reload-capture",
		ToolCallID:   "skill-reload-capture",
		Identity: schema.SkillContentIdentity{
			Name:         "opaque",
			DeclaredName: "opaque",
			Source:       missing,
			FileDigest:   "reload-capture-file",
		},
		Route: "model_tool",
	}}}); err != nil {
		t.Fatalf("record the reload obligation: %v", err)
	}

	// The frame is durably present BEFORE the capture, so it is one of the turns
	// the capture reads; nothing has admitted it yet.
	const frameID = "watch:wd_capture:1"
	if _, err := runtime.appendDelegateNotificationDurably(frameID, marker); err != nil {
		t.Fatalf("append the watch frame: %v", err)
	}

	// It is admitted after the capture and before the append decision: the
	// window a live re-read would answer from the far side of.
	admitted := false
	updateSessionTestConfig(runtime, func(cfg *testConfig) {
		cfg.afterDelegateAttentionCapture = func() {
			if admitted {
				return
			}
			admitted = true
			c.mu.Lock()
			state := c.attention[lease.delegateID]
			if state == nil {
				state = &delegateAttentionState{}
				c.attention[lease.delegateID] = state
			}
			if state.wakeIDs == nil {
				state.wakeIDs = make(map[string]struct{})
			}
			state.wakeIDs[frameID] = struct{}{}
			c.mu.Unlock()
		}
	})

	ctx := context.WithValue(context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease), delegateAttentionRunContextKey{}, struct{}{})
	var timings events.RoundTimings
	_, _, history, _, _, _, err := runtime.prepareModelRequestWithError(ctx, 1, &timings)
	if err != nil {
		t.Fatalf("prepare the attention-run request: %v", err)
	}
	if !admitted {
		t.Fatal("premise broken: the frame was never admitted inside the capture window, so this cannot tell a captured answer from a live one")
	}
	if presented := countMessageText(history, marker); presented != 0 {
		t.Fatalf("the request presented %q, which was admitted only after the reload's capture: the capture's holds and turns describe one moment, so eligibility must be answered from that moment rather than re-read against later wake state", frameID)
	}
	// The row stays pending, so this reports withholding rather than a
	// resolution the round performed.
	runtime.attentionMu.Lock()
	pending := delegateAttentionPendingSet(runtime.delegateAttentionPendingIDsLocked())
	_, stillPending := pending[frameID]
	runtime.attentionMu.Unlock()
	if !stillPending {
		t.Fatalf("premise broken: %q is no longer pending after the round, so this cannot tell withholding from resolution", frameID)
	}
}

// TestAFilteredSkillReloadDoesNotSeedAContinuationAnchor pins the rebuild's own
// answer about whether it presents a standard view of the resident history.
//
// The load-bearing case is a frame that lands after the initial projection and
// is withheld by the reload's capture while its wake is still pending. The
// initial projection saw a complete history and reported no drops; the rebuild
// sees the frame and withholds it. Reusing the initial flag lets the filtered
// request seed a continuation anchor. The frame is then admitted before the next
// round and sits before that anchor in resident history, so an otherwise
// eligible delta skips it — a turn the model has never seen, which is the worst
// shape this machinery takes. A request that withholds a row must therefore
// neither use nor seed an anchor.
func TestAFilteredSkillReloadDoesNotSeedAContinuationAnchor(t *testing.T) {
	const marker = "the frame the reload's own capture withheld"
	adapter := &agenttest.FakeAdapter{
		Provider: "openai",
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	profile := NewOpenAIProfile("gpt-5.4")
	stateDir := realTempDirForTest(t)
	runtime, err := NewSession(client, withTestSessionNamer(client, profile), execenv.NewLocalExecutionEnvironment(stateDir), SessionConfig{
		StateDir:                    stateDir,
		OpenAIResponsesContinuation: "auto",
		MaxSubagentDepth:            1,
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() {
		runtime.mu.Lock()
		runtime.delegateController = nil
		runtime.owningDelegateID = ""
		runtime.mu.Unlock()
		drainSessionEvents(runtime)
		runtime.Close()
	})
	drainSessionEvents(runtime)

	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_anchor", "")
	lease := delegateLease{delegateID: "dlg_anchor", generation: 1}
	runtime.delegateController = c
	runtime.owningDelegateID = lease.delegateID
	c.mu.Lock()
	c.live[lease.delegateID].runtime = runtime
	c.live[lease.delegateID].binding.runtime = runtime
	c.mu.Unlock()

	// A stored anchor from an earlier round, so the initial projection is a
	// standard view and reports no drops.
	anchor := responsesContinuationEligibleAssistantTurn("resp_reload_anchor")
	anchor.ResponseIDHash = "cont-handle-v1:response_id:reload_anchor"
	anchor.ResponseRequestFingerprint = "cont-req-v1:reload_anchor"
	anchor.ResponseStorageScopeFingerprint = "cont-scope-v1:reload_anchor"
	runtime.mu.Lock()
	runtime.history = append(runtime.history, anchor)
	runtime.mu.Unlock()

	// The reload obligation makes the delivery path append a typed notification,
	// which is what triggers the rebuild.
	missing := filepath.Join(stateDir, "skills", "opaque", "SKILL.md")
	if err := runtime.persistSkillToolObligations(&schema.SkillTurnState{Obligations: []schema.SkillDeliveryObligation{{
		InvocationID: "inv-anchor-reload",
		ToolCallID:   "skill-anchor-reload",
		Identity: schema.SkillContentIdentity{
			Name:         "opaque",
			DeclaredName: "opaque",
			Source:       missing,
			FileDigest:   "anchor-reload-file",
		},
		Route: "model_tool",
	}}}); err != nil {
		t.Fatalf("record the reload obligation: %v", err)
	}

	// The frame lands after the initial projection: appended from the seam the
	// rebuild's capture follows, so the projection never saw it and the rebuild
	// does.
	const frameID = "watch:wd_anchor:1"
	appended := false
	updateSessionTestConfig(runtime, func(cfg *testConfig) {
		cfg.afterDelegateAttentionProjection = func() {
			if appended {
				return
			}
			appended = true
			if _, err := runtime.appendDelegateNotificationDurably(frameID, marker); err != nil {
				t.Errorf("append the frame: %v", err)
			}
		}
	})

	ctx := context.WithValue(context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease), delegateAttentionRunContextKey{}, struct{}{})
	var timings events.RoundTimings
	_, _, history, req, _, _, err := runtime.prepareModelRequestWithError(ctx, 1, &timings)
	if err != nil {
		t.Fatalf("prepare the attention-run request: %v", err)
	}
	if !appended {
		t.Fatal("premise broken: the frame never landed, so the rebuild never had to decide about it")
	}
	if presented := countMessageText(history, marker); presented != 0 {
		t.Fatalf("the request presented %q, which the reload's own capture withheld: this case does not exercise a filtered rebuild", frameID)
	}
	if req.HistoryMode != llm.HistoryModeFullHistory || req.PreviousResponseID != "" || req.Continuation != nil {
		t.Fatalf("the filtered rebuild dispatched mode=%q previous=%q continuation=%+v, want full history with no anchor: a request that withheld a row must neither continue from a stored anchor nor seed one, or a frame admitted before the next round sits ahead of that anchor and an eligible delta skips a turn the model has never seen", req.HistoryMode, req.PreviousResponseID, req.Continuation)
	}
}
