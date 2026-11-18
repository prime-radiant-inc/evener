package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm/registry"
)

func newModelControlSession(t *testing.T) *Session {
	t.Helper()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withConfig(SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}),
	)
	sess.SetBootGeneration("7")
	return sess
}

func modelControlRequest(sess *Session, model, mutationID string) ModelControlRequest {
	return ModelControlRequest{
		ModelProvider:          "openai",
		Model:                  model,
		ClientMutationID:       mutationID,
		ExpectedInstanceID:     sess.ID(),
		ExpectedBootGeneration: "7",
	}
}

func requireModelControlEvent(t *testing.T, sess *Session) appwire.ModelControlSnapshot {
	t.Helper()
	for {
		select {
		case event := <-sess.Events():
			if event.Kind != events.EventModelControlChanged {
				continue
			}
			data, ok := event.Data.(events.ModelControlChangedData)
			if !ok {
				t.Fatalf("model-control event data type = %T", event.Data)
			}
			return data.Snapshot
		default:
			t.Fatal("model-control transition emitted no event")
			return appwire.ModelControlSnapshot{}
		}
	}
}

func TestModelControlSnapshotStartsAtRevisionOne(t *testing.T) {
	t.Parallel()
	sess := newModelControlSession(t)

	got := sess.ModelControlSnapshot()
	if got.Authority.InstanceID != sess.ID() || got.Authority.BootGeneration != "7" {
		t.Fatalf("authority = %+v, want session id and boot generation 7", got.Authority)
	}
	if got.Revision != 1 || got.Desired != nil {
		t.Fatalf("initial snapshot = %+v, want revision 1 without desired state", got)
	}
	if got.Active.ModelProvider != "openai" || got.Active.Model != "gpt-5.4" {
		t.Fatalf("active model = %+v, want openai/gpt-5.4", got.Active)
	}
}

func TestModelControlSnapshotSerializesEmptyEffortLevelsAsArray(t *testing.T) {
	t.Parallel()
	active := modelControlActive(newAnthropicProfile("claude-unknown-model"))
	if active.ReasoningEffortLevels == nil {
		t.Fatal("active effort levels = nil, want an empty list")
	}

	encoded, err := json.Marshal(cloneModelControlSnapshot(appwire.ModelControlSnapshot{Active: active}))
	if err != nil {
		t.Fatalf("marshal model-control snapshot: %v", err)
	}
	var decoded appwire.ModelControlSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal model-control snapshot: %v", err)
	}
	if decoded.Active.ReasoningEffortLevels == nil {
		t.Fatal("serialized effort levels decoded as nil, want an empty list")
	}
}

func TestModelControlIdentityRejectionDoesNotConsumeSequence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		mutate   func(*ModelControlRequest)
		wantInfo appwire.ErrorInfo
	}{
		{name: "empty identity", mutate: func(r *ModelControlRequest) {
			r.ClientMutationID = ""
			r.ExpectedInstanceID = ""
			r.ExpectedBootGeneration = ""
		}, wantInfo: appwire.ErrorInvalidParams},
		{name: "mutation only", mutate: func(r *ModelControlRequest) {
			r.ExpectedInstanceID = ""
			r.ExpectedBootGeneration = ""
		}, wantInfo: appwire.ErrorInvalidParams},
		{name: "instance only", mutate: func(r *ModelControlRequest) {
			r.ClientMutationID = ""
			r.ExpectedBootGeneration = ""
		}, wantInfo: appwire.ErrorInvalidParams},
		{name: "generation only", mutate: func(r *ModelControlRequest) {
			r.ClientMutationID = ""
			r.ExpectedInstanceID = ""
		}, wantInfo: appwire.ErrorInvalidParams},
		{name: "mutation and instance", mutate: func(r *ModelControlRequest) {
			r.ExpectedBootGeneration = ""
		}, wantInfo: appwire.ErrorInvalidParams},
		{name: "mutation and generation", mutate: func(r *ModelControlRequest) {
			r.ExpectedInstanceID = ""
		}, wantInfo: appwire.ErrorInvalidParams},
		{name: "instance and generation", mutate: func(r *ModelControlRequest) {
			r.ClientMutationID = ""
		}, wantInfo: appwire.ErrorInvalidParams},
		{name: "stale instance", mutate: func(r *ModelControlRequest) {
			r.ExpectedInstanceID = "retired"
		}, wantInfo: appwire.ErrorModelControlAuthorityStale},
		{name: "stale generation", mutate: func(r *ModelControlRequest) {
			r.ExpectedBootGeneration = "6"
		}, wantInfo: appwire.ErrorModelControlAuthorityStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := newModelControlSession(t)
			before := sess.ModelControlSnapshot()
			for len(sess.Events()) > 0 {
				<-sess.Events()
			}
			request := modelControlRequest(sess, "gpt-4.1-mini", "pick")
			tc.mutate(&request)

			_, err := sess.SetModelControl(request)
			if err == nil {
				t.Fatal("invalid authority = nil error, want rejection")
			}
			wire, ok := errors.AsType[appwire.WireError](err)
			data, hasData := appwire.ErrorDataOf(wire.Data)
			if !ok || !hasData || data.EvenerErrorInfo != tc.wantInfo {
				t.Fatalf("authority error = %T %+v data=%+v, want %q", err, wire, data, tc.wantInfo)
			}
			if sess.modelControlRequestSequence != 0 {
				t.Fatalf("request sequence = %d, want no assignment", sess.modelControlRequestSequence)
			}
			if after := sess.ModelControlSnapshot(); !reflect.DeepEqual(after, before) {
				t.Fatalf("authority refusal changed snapshot: before=%+v after=%+v", before, after)
			}
			select {
			case event := <-sess.Events():
				t.Fatalf("authority refusal emitted %s", event.Kind)
			default:
			}

			response, err := sess.SetModelControl(modelControlRequest(sess, "gpt-4.1-mini", "accepted"))
			if err != nil {
				t.Fatalf("valid model control: %v", err)
			}
			if response.ModelControl == nil || response.ModelControl.Desired == nil {
				t.Fatalf("response = %+v, want desired state", response)
			}
			if got := response.ModelControl.Desired.RequestSequence; got != 1 {
				t.Fatalf("request sequence = %d, want 1 after rejected authority check", got)
			}
		})
	}
}

func TestModelControlImmediateAcceptancePublishesDetachedSnapshot(t *testing.T) {
	t.Parallel()
	sess := newModelControlSession(t)

	response, err := sess.SetModelControl(modelControlRequest(sess, "gpt-4.1-mini", "cm_1"))
	if err != nil {
		t.Fatalf("SetModelControl: %v", err)
	}
	if response.ModelControlVersion != appwire.ModelControlVersion1 || response.Disposition != appwire.ModelControlDispositionAccepted {
		t.Fatalf("response metadata = %+v, want version 1 accepted", response)
	}
	if response.ModelControl == nil || response.ModelControl.Revision != 2 {
		t.Fatalf("response snapshot = %+v, want revision 2", response.ModelControl)
	}
	wantDesired := &appwire.ModelControlDesired{
		RequestSequence: 1, ClientMutationID: "cm_1", ModelProvider: "openai", Model: "gpt-4.1-mini", Lifecycle: appwire.ModelControlLifecycleApplied,
	}
	if !reflect.DeepEqual(response.ModelControl.Desired, wantDesired) {
		t.Fatalf("desired = %+v, want %+v", response.ModelControl.Desired, wantDesired)
	}
	if got := response.ModelControl.Active.Model; got != "gpt-4.1-mini" {
		t.Fatalf("active model = %q, want gpt-4.1-mini", got)
	}
	published := requireModelControlEvent(t, sess)
	if !reflect.DeepEqual(published, *response.ModelControl) {
		t.Fatalf("published snapshot = %+v, response snapshot = %+v", published, *response.ModelControl)
	}

	response.ModelControl.Active.ReasoningEffortLevels = append(response.ModelControl.Active.ReasoningEffortLevels, "poison")
	response.ModelControl.Desired.Model = "poison"
	fresh := sess.ModelControlSnapshot()
	if fresh.Desired.Model != "gpt-4.1-mini" {
		t.Fatalf("mutating response changed session desired state: %+v", fresh.Desired)
	}
	for _, level := range fresh.Active.ReasoningEffortLevels {
		if level == "poison" {
			t.Fatalf("mutating response changed session active levels: %v", fresh.Active.ReasoningEffortLevels)
		}
	}
}

func TestModelControlPendingAppliesAtSafePoint(t *testing.T) {
	t.Parallel()
	sess := newModelControlSession(t)
	sess.mu.Lock()
	sess.goalInTurn = true
	sess.mu.Unlock()

	response, err := sess.SetModelControl(modelControlRequest(sess, "gpt-4.1-mini", "cm_pending"))
	if err != nil {
		t.Fatalf("queue model control: %v", err)
	}
	if response.ModelControl == nil || response.ModelControl.Desired == nil || response.ModelControl.Desired.Lifecycle != appwire.ModelControlLifecyclePending {
		t.Fatalf("queued response = %+v, want pending desired state", response)
	}
	if got := response.ModelControl.Active.Model; got != "gpt-5.4" {
		t.Fatalf("queued active model = %q, want gpt-5.4", got)
	}
	requireModelControlEvent(t, sess)

	sess.mu.Lock()
	sess.goalInTurn = false
	sess.mu.Unlock()
	sess.applyQueuedModelSwitches(false)

	settled := sess.ModelControlSnapshot()
	if settled.Revision != 3 || settled.Desired == nil || settled.Desired.Lifecycle != appwire.ModelControlLifecycleApplied {
		t.Fatalf("settled snapshot = %+v, want revision 3 applied", settled)
	}
	if settled.Active.Model != "gpt-4.1-mini" || settled.Desired.ClientMutationID != "cm_pending" {
		t.Fatalf("settled snapshot = %+v, want exact accepted intent and active model", settled)
	}
	published := requireModelControlEvent(t, sess)
	if !reflect.DeepEqual(published, settled) {
		t.Fatalf("published settled snapshot = %+v, accessor = %+v", published, settled)
	}
}

func TestModelControlDirectRefusalPreservesAcceptedDesired(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(newFakeEnumerableAdapter("openai", "gpt-5.4", "gpt-4.1-mini")),
		withConfig(SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}),
	)
	sess.SetBootGeneration("7")
	if _, err := sess.SetModelControl(modelControlRequest(sess, "gpt-4.1-mini", "cm_good")); err != nil {
		t.Fatalf("accepted switch: %v", err)
	}
	before := sess.ModelControlSnapshot()

	if _, err := sess.SetModelControl(modelControlRequest(sess, "not-a-real-model", "cm_bad")); err == nil {
		t.Fatal("unresolvable model = nil error, want refusal")
	}
	after := sess.ModelControlSnapshot()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("direct refusal changed snapshot: before=%+v after=%+v", before, after)
	}
}

func TestModelControlSameModelPublishesOnlyCanonicalTransition(t *testing.T) {
	t.Parallel()
	sess := newModelControlSession(t)

	if _, err := sess.SetModelControl(modelControlRequest(sess, "gpt-5.4", "cm_same")); err != nil {
		t.Fatalf("same-model control: %v", err)
	}
	canonical := requireModelControlEvent(t, sess)
	if canonical.Revision != 2 || canonical.Desired == nil || canonical.Desired.Lifecycle != appwire.ModelControlLifecycleApplied {
		t.Fatalf("same-model snapshot = %+v, want revision 2 applied", canonical)
	}
	for {
		select {
		case event := <-sess.Events():
			if event.Kind == events.EventModelChanged {
				t.Fatal("same-model acceptance emitted legacy model-changed event")
			}
		default:
			return
		}
	}
}

func TestModelControlIdentityFreeDirectRequestIsRefused(t *testing.T) {
	t.Parallel()
	sess := newModelControlSession(t)
	if _, err := sess.SetModelControl(modelControlRequest(sess, "gpt-4.1-mini", "cm_typed")); err != nil {
		t.Fatalf("typed switch: %v", err)
	}
	before := sess.ModelControlSnapshot()
	if _, err := sess.SetModelControl(ModelControlRequest{ModelProvider: "openai", Model: "gpt-5.4"}); err == nil {
		t.Fatal("identity-free direct request = nil error, want rejection")
	}
	if after := sess.ModelControlSnapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("identity-free request changed snapshot: before=%+v after=%+v", before, after)
	}
	if sess.modelControlRequestSequence != 1 {
		t.Fatalf("request sequence = %d, want the accepted request only", sess.modelControlRequestSequence)
	}
}

// TestModelControlSameModelReselectRepublishesContextWindow covers the case a
// fallback leaves behind: the fallback answered, so the context manager
// budgets against the fallback's window, and reselecting the configured model
// restores the configured window. The switch event is the only thing that
// refreshes the daemon's context facet, so suppressing it because the two
// configured profiles match leaves every read reporting the fallback's window.
func TestModelControlSameModelReselectRepublishesContextWindow(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withConfig(SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}),
	)
	sess.SetBootGeneration("7")
	configuredWindow, _ := budgetingWindow(sess.currentProfile())
	// The fallback that answered: another model, so the context manager now
	// budgets against that model's window instead of the configured one.
	const fallbackWindow = 128_000
	if configuredWindow == fallbackWindow {
		t.Fatalf("fixture: configured window %d equals the fallback window", configuredWindow)
	}
	sess.contextMgr.SetProfile(withContextWindow(NewOpenAIProfile("gpt-4.1-mini"), fallbackWindow))
	if got, _ := sess.ContextStatus(); got.Window != fallbackWindow {
		t.Fatalf("fixture window = %d, want the fallback's %d", got.Window, fallbackWindow)
	}

	if _, err := sess.SetModelControl(modelControlRequest(sess, "gpt-5.4", "cm_reselect")); err != nil {
		t.Fatalf("reselect configured model: %v", err)
	}
	requireModelControlEvent(t, sess)
	var found *events.ModelChangedData
	for {
		select {
		case event := <-sess.Events():
			if data, ok := event.Data.(events.ModelChangedData); ok {
				found = &data
			}
			continue
		default:
		}
		break
	}
	if found == nil {
		t.Fatal("reselecting the configured model emitted no EventModelChanged, so the context facet keeps the fallback's window")
	}
	if found.ContextWindow != configuredWindow {
		t.Fatalf("published context window = %d, want the configured model's %d", found.ContextWindow, configuredWindow)
	}
	if got, _ := sess.ContextStatus(); got.Window != configuredWindow {
		t.Fatalf("context window after reselect = %d, want %d", got.Window, configuredWindow)
	}
}

func TestModelControlShutdownAfterSequenceAssignmentIsNotAccepted(t *testing.T) {
	t.Parallel()
	adapter := &gatedListingAdapter{name: "openai"}
	validationStarted := make(chan struct{})
	releaseValidation := make(chan struct{})
	adapter.models = []func() ([]registry.Model, error){
		func() ([]registry.Model, error) {
			return []registry.Model{{ID: "gpt-5.4"}, {ID: "gpt-4.1-mini"}}, nil
		},
		func() ([]registry.Model, error) {
			close(validationStarted)
			<-releaseValidation
			return []registry.Model{{ID: "gpt-5.4"}, {ID: "gpt-4.1-mini"}}, nil
		},
	}
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(adapter),
		withConfig(SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}),
	)
	sess.SetBootGeneration("7")
	before := sess.ModelControlSnapshot()
	type result struct {
		response appwire.ThreadModelSetResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := sess.SetModelControl(modelControlRequest(sess, "gpt-4.1-mini", "cm_closing"))
		done <- result{response: response, err: err}
	}()
	<-validationStarted
	sess.Close()
	close(releaseValidation)
	got := <-done
	if got.err == nil && got.response.Disposition == appwire.ModelControlDispositionAccepted {
		t.Fatalf("shutdown result = %+v, want error or superseded", got.response)
	}
	if after := sess.ModelControlSnapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("shutdown changed snapshot without accepting a transition: before=%+v after=%+v", before, after)
	}
}

func TestModelControlSlowObsoleteRefusalReturnsSuperseded(t *testing.T) {
	t.Parallel()
	adapter := &gatedListingAdapter{name: "openai"}
	slowValidationStarted := make(chan struct{})
	releaseSlowValidation := make(chan struct{})
	adapter.models = []func() ([]registry.Model, error){
		func() ([]registry.Model, error) {
			return []registry.Model{{ID: "gpt-5.4"}, {ID: "gpt-4.1-mini"}}, nil
		},
		func() ([]registry.Model, error) {
			close(slowValidationStarted)
			<-releaseSlowValidation
			return []registry.Model{{ID: "gpt-5.4"}}, nil
		},
		func() ([]registry.Model, error) {
			return []registry.Model{{ID: "gpt-5.4"}, {ID: "gpt-4.1-mini"}}, nil
		},
	}
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(adapter),
		withConfig(SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}),
	)
	sess.SetBootGeneration("7")

	type result struct {
		response appwire.ThreadModelSetResponse
		err      error
	}
	olderDone := make(chan result, 1)
	go func() {
		response, err := sess.SetModelControl(modelControlRequest(sess, "gpt-obsolete", "cm_old"))
		olderDone <- result{response: response, err: err}
	}()
	<-slowValidationStarted
	newer, err := sess.SetModelControl(modelControlRequest(sess, "gpt-4.1-mini", "cm_new"))
	if err != nil {
		t.Fatalf("newer request: %v", err)
	}
	close(releaseSlowValidation)
	older := <-olderDone
	if older.err != nil {
		t.Fatalf("obsolete refusal returned error instead of superseded: %v", older.err)
	}
	if older.response.Disposition != appwire.ModelControlDispositionSuperseded {
		t.Fatalf("obsolete disposition = %q, want superseded", older.response.Disposition)
	}
	if older.response.ModelControl == nil || newer.ModelControl == nil || !reflect.DeepEqual(*older.response.ModelControl, *newer.ModelControl) {
		t.Fatalf("obsolete response snapshot = %+v, newer snapshot = %+v", older.response.ModelControl, newer.ModelControl)
	}
}

func TestModelControlOldAuthorityCannotApplyAfterSequenceReset(t *testing.T) {
	t.Parallel()
	adapter := &gatedListingAdapter{name: "openai"}
	oldValidationStarted := make(chan struct{})
	releaseOldValidation := make(chan struct{})
	adapter.models = []func() ([]registry.Model, error){
		func() ([]registry.Model, error) {
			return []registry.Model{{ID: "gpt-5.4"}, {ID: "gpt-4.1-mini"}, {ID: "gpt-old"}}, nil
		},
		func() ([]registry.Model, error) {
			close(oldValidationStarted)
			<-releaseOldValidation
			return []registry.Model{{ID: "gpt-5.4"}, {ID: "gpt-4.1-mini"}, {ID: "gpt-old"}}, nil
		},
		func() ([]registry.Model, error) {
			return []registry.Model{{ID: "gpt-5.4"}, {ID: "gpt-4.1-mini"}, {ID: "gpt-old"}}, nil
		},
	}
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(adapter),
		withConfig(SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}),
	)
	sess.SetBootGeneration("7")

	type result struct {
		response appwire.ThreadModelSetResponse
		err      error
	}
	oldDone := make(chan result, 1)
	go func() {
		response, err := sess.SetModelControl(modelControlRequest(sess, "gpt-old", "cm_old_authority"))
		oldDone <- result{response: response, err: err}
	}()
	<-oldValidationStarted

	sess.SetBootGeneration("8")
	newRequest := modelControlRequest(sess, "gpt-4.1-mini", "cm_new_authority")
	newRequest.ExpectedBootGeneration = "8"
	newer, err := sess.SetModelControl(newRequest)
	if err != nil {
		t.Fatalf("new-authority request: %v", err)
	}
	close(releaseOldValidation)
	older := <-oldDone
	if older.err != nil {
		t.Fatalf("old-authority request returned error instead of superseded: %v", older.err)
	}
	if older.response.Disposition != appwire.ModelControlDispositionSuperseded {
		t.Fatalf("old-authority disposition = %q, want superseded", older.response.Disposition)
	}
	if older.response.ModelControl == nil || newer.ModelControl == nil || !reflect.DeepEqual(*older.response.ModelControl, *newer.ModelControl) {
		t.Fatalf("old-authority response snapshot = %+v, new-authority snapshot = %+v", older.response.ModelControl, newer.ModelControl)
	}
	if got := sess.ModelControlSnapshot(); got.Authority.BootGeneration != "8" || got.Active.Model != "gpt-4.1-mini" {
		t.Fatalf("settled snapshot = %+v, want generation 8 on gpt-4.1-mini", got)
	}
}
