package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestE2E_ModelSwitchOnColdSessionReachesTheResumedSubscriber pins the
// at-rest pick for the shape the live-stack tests cannot reach from a live
// daemon: a session whose daemon has exited. The hub resumes the session
// behind thread/model/set (app_model.go's setThreadModelWithResume), and the
// pane that subscribed while the session was cold must learn the switch the
// same way - thread/model/changed through the re-established relay - so the
// label converges without a re-read. A frame lost across the resume cut reads
// to the user as the switch silently flipping back to the old model.
func TestE2E_ModelSwitchOnColdSessionReachesTheResumedSubscriber(t *testing.T) {
	stack, provider := startE2EHubStack(t)
	provider.AdvertiseModels(fakellm.ModelID, fakellm.SwitchTargetID)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)

	const openingPrompt = "EVENER-E2E-COLD-SWITCH-OPENING"

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "evener",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: openingPrompt}},
		Model:           stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	ref := started.Thread.Evener.Ref
	if ref == "" {
		t.Fatalf("thread/start returned no evener ref: %+v", started.Thread)
	}
	t.Cleanup(func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelShutdown()
		_, _ = clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref})
	})

	// One real turn, so the session has a settled turn and its meta on disk.
	round1, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the opening model request: %v", err)
	}
	if !round1.Contains(openingPrompt) {
		t.Fatalf("the opening prompt never reached the model")
	}
	round1.RespondToolCall("communicate", communicateArgs("opening turn done"))
	awaitThread(ctx, t, client, ref, "the opening turn to finish", func(thread appwire.Thread) bool {
		return thread.Status.Type != "active"
	})

	// Retire the daemon and wait until the roster no longer lists it: the
	// session below is picked in the cold, exited state, exactly the shape
	// the dashboard shows after a daemon exits.
	retireDaemonUntilAccepted(ctx, t, client, ref)
	goneAt := time.Now()
	for {
		list, err := clientRequest[appwire.DaemonListResponse](ctx, client, appwire.MethodEvenerDaemonList, appwire.DaemonListParams{})
		if err == nil {
			if _, still := residentIdentityForRef(list, ref); !still {
				break
			}
		}
		if time.Since(goneAt) > 30*time.Second {
			t.Fatal("the retired daemon never left the roster")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The pane subscribes while the session is cold, the way the session pane
	// is open on an exited session.
	if _, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
		t.Fatalf("thread/read subscribe on the cold session: %v", err)
	}
	modelChanged := make(chan appwire.ThreadModelChangedParams, 8)
	go func() {
		for notification := range client.Notifications() {
			if notification.Method != appwire.NotifyThreadModelChanged {
				continue
			}
			var params appwire.ThreadModelChangedParams
			if json.Unmarshal(notification.Params, &params) != nil {
				continue
			}
			modelChanged <- params
		}
	}()

	if _, err := clientRequest[appwire.EmptyResponse](ctx, client, appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{
		Ref:           ref,
		ModelProvider: "fake",
		Model:         fakellm.SwitchTargetID,
	}); err != nil {
		t.Fatalf("thread/model/set on the cold session: %v", err)
	}

	select {
	case params := <-modelChanged:
		if params.Model != fakellm.SwitchTargetID || params.ModelProvider != "fake" {
			t.Fatalf("thread/model/changed = provider %q model %q, want fake/%s", params.ModelProvider, params.Model, fakellm.SwitchTargetID)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("the switch's thread/model/changed never reached the subscriber that attached while the session was cold")
	case <-ctx.Done():
		t.Fatalf("waiting for thread/model/changed: %v", ctx.Err())
	}

	read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("thread/read after the cold-session switch: %v", err)
	}
	if read.Thread.ModelProvider != fakellm.SwitchTargetID {
		t.Fatalf("thread/read ModelProvider after the cold-session switch = %q, want %q", read.Thread.ModelProvider, fakellm.SwitchTargetID)
	}
}

// TestE2E_ModelSwitchQueuesMidTurnAndPushesAtRest drives the composer's model
// contract against the real hub and the real daemon it spawns: a model picked
// while a turn is running is accepted and applied at the turn boundary (so the
// message the user was typing runs on the new model, and the running turn
// finishes on the one it started with), and a pick made at rest pushes
// thread/model/changed to the subscribed web client and shows in thread/read.
//
// The old behavior this replaces refused the mid-turn pick with Conflict
// "session is processing" — the exact error a composer user saw — leaving no
// way to choose the next turn's model while the current one ran.
func TestE2E_ModelSwitchQueuesMidTurnAndPushesAtRest(t *testing.T) {
	stack, provider := startE2EHubStack(t)
	provider.AdvertiseModels(fakellm.ModelID, fakellm.SwitchTargetID)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)

	const (
		openingPrompt = "EVENER-E2E-MODEL-SWITCH-OPENING"
		queuedText    = "EVENER-E2E-MODEL-SWITCH-QUEUED"
	)

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "evener",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: openingPrompt}},
		Model:           stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	ref := started.Thread.Evener.Ref
	if ref == "" {
		t.Fatalf("thread/start returned no evener ref: %+v", started.Thread)
	}
	t.Cleanup(func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelShutdown()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Errorf("thread/shutdown left the daemon running: %v", err)
		}
	})

	if _, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
		t.Fatalf("thread/read subscribe: %v", err)
	}
	modelChanged := make(chan appwire.ThreadModelChangedParams, 8)
	go func() {
		for notification := range client.Notifications() {
			if notification.Method != appwire.NotifyThreadModelChanged {
				continue
			}
			var params appwire.ThreadModelChangedParams
			if json.Unmarshal(notification.Params, &params) != nil {
				continue
			}
			modelChanged <- params
		}
	}()
	awaitModelChanged := func(wantModel string) appwire.ThreadModelChangedParams {
		t.Helper()
		for {
			select {
			case params := <-modelChanged:
				if params.Model == wantModel {
					return params
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("no thread/model/changed with model %q reached the hub subscriber", wantModel)
			case <-ctx.Done():
				t.Fatalf("waiting for thread/model/changed with model %q: %v", wantModel, ctx.Err())
			}
		}
	}

	// Round 1 held open: the turn is genuinely in flight while the pick fires.
	round1, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the session's first model request: %v", err)
	}

	// The mid-turn pick. This used to answer Conflict "session is processing"
	// through the hub; it must now be accepted and queued for the boundary.
	if _, err := clientRequest[appwire.EmptyResponse](ctx, client, appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{
		Ref:           ref,
		ModelProvider: "fake",
		Model:         fakellm.SwitchTargetID,
	}); err != nil {
		t.Fatalf("mid-turn thread/model/set: %v", err)
	}

	// The composer's send-while-busy: the typed message queues behind the turn.
	queueReceipt, err := clientRequest[appwire.TurnQueueResponse](ctx, client, appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:                ref,
		ClientMutationID:   newMutationID(t),
		ExpectedInstanceID: localInstanceIDForTestRef(ref),
		Input:              []appwire.InputItem{{Type: "text", Text: queuedText}},
	})
	if err != nil {
		t.Fatalf("turn/queue behind the in-flight turn: %v", err)
	}
	if queueReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/queue disposition = %q, want %q", queueReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}

	// End the first turn. It finishes on the model it started with.
	if got, _ := round1.Body["model"].(string); got != fakellm.ModelID {
		t.Fatalf("first turn ran on model %q, want the launch model %q", got, fakellm.ModelID)
	}
	round1.RespondToolCall("communicate", communicateArgs("first turn done"))

	// The queued message's turn opens with the SWITCHED model in its first
	// request: the boundary applied the pick before this turn began.
	round2, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the queued message's model request: %v", err)
	}
	if got, _ := round2.Body["model"].(string); got != fakellm.SwitchTargetID {
		t.Fatalf("queued turn ran on model %q, want the queued switch %q", got, fakellm.SwitchTargetID)
	}
	if !round2.Contains(queuedText) {
		t.Fatalf("the queued message never reached the session: no turn carried %q", queuedText)
	}

	// The boundary-applied switch reaches the subscribed web client through
	// the hub, so every surface's label converges without a re-read.
	params := awaitModelChanged(fakellm.SwitchTargetID)
	if params.ModelProvider != "fake" {
		t.Fatalf("thread/model/changed ModelProvider = %q, want %q", params.ModelProvider, "fake")
	}

	round2.RespondToolCall("communicate", communicateArgs("second turn done"))

	// AT REST: a further pick applies immediately and pushes the same way.
	if _, err := clientRequest[appwire.EmptyResponse](ctx, client, appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{
		Ref:           ref,
		ModelProvider: "fake",
		Model:         fakellm.ModelID,
	}); err != nil {
		t.Fatalf("at-rest thread/model/set: %v", err)
	}
	if params := awaitModelChanged(fakellm.ModelID); params.ModelProvider != "fake" {
		t.Fatalf("at-rest thread/model/changed ModelProvider = %q, want %q", params.ModelProvider, "fake")
	}
	read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("thread/read after the at-rest switch: %v", err)
	}
	if read.Thread.ModelProvider != fakellm.ModelID {
		t.Fatalf("thread/read ModelProvider after the at-rest switch = %q, want %q", read.Thread.ModelProvider, fakellm.ModelID)
	}
}
