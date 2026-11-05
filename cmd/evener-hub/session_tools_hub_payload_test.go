package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

type controlOwnerCall struct {
	method string
	params any
}

type controlPayloadRecordingSource struct {
	*scriptedAppSource
	calls []controlOwnerCall
}

func (s *controlPayloadRecordingSource) record(method string, params any) {
	s.calls = append(s.calls, controlOwnerCall{method: method, params: params})
}

func (s *controlPayloadRecordingSource) ResolveSandboxEscalation(_ context.Context, p appwire.SandboxEscalationResolveParams) error {
	s.record(appwire.MethodEvenerSandboxEscalationResolve, p)
	return nil
}
func (s *controlPayloadRecordingSource) ResumeThread(_ context.Context, p appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	s.record(appwire.MethodThreadResume, p)
	return appwire.ThreadResumeResponse{Thread: s.thread}, nil
}
func (s *controlPayloadRecordingSource) ClearThread(_ context.Context, p appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	s.record(appwire.MethodThreadClear, p)
	return appwire.ThreadClearResponse{}, nil
}
func (s *controlPayloadRecordingSource) SetThreadModel(_ context.Context, p appwire.ThreadModelSetParams) error {
	s.record(appwire.MethodThreadModelSet, p)
	return nil
}
func (s *controlPayloadRecordingSource) SetThreadVisionModel(_ context.Context, p appwire.ThreadVisionModelSetParams) error {
	s.record(appwire.MethodThreadVisionModelSet, p)
	return nil
}
func (s *controlPayloadRecordingSource) SetThreadReasoningEffort(_ context.Context, p appwire.ThreadReasoningEffortSetParams) error {
	s.record(appwire.MethodThreadReasoningEffortSet, p)
	return nil
}
func (s *controlPayloadRecordingSource) CompactThread(_ context.Context, p appwire.ThreadCompactStartParams) error {
	s.record(appwire.MethodThreadCompactStart, p)
	return nil
}
func (s *controlPayloadRecordingSource) DrainAsSteer(_ context.Context, p appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	s.record(appwire.MethodTurnDrainAsSteer, p)
	return appwire.TurnDrainAsSteerResponse{}, nil
}
func (s *controlPayloadRecordingSource) PromoteQueuedAsSteer(_ context.Context, p appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	s.record(appwire.MethodTurnPromoteQueuedAsSteer, p)
	return appwire.TurnPromoteQueuedAsSteerResponse{}, nil
}
func (s *controlPayloadRecordingSource) CancelQueued(_ context.Context, p appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	s.record(appwire.MethodTurnCancelQueued, p)
	return appwire.TurnCancelQueuedResponse{}, nil
}
func (s *controlPayloadRecordingSource) GoalSet(_ context.Context, p appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	s.record(appwire.MethodGoalSet, p)
	return appwire.GoalSetResponse{}, nil
}
func (s *controlPayloadRecordingSource) NotesHumanSet(_ context.Context, p appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
	s.record(appwire.MethodNotesHumanSet, p)
	return appwire.NotesHumanSetResponse{}, nil
}
func (s *controlPayloadRecordingSource) UrlsRemove(_ context.Context, p appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	s.record(appwire.MethodUrlsRemove, p)
	return appwire.UrlsRemoveResponse{}, nil
}

// These thirteen actions have separate refusal-parity tests. This test reaches
// their actual guarded owner boundary and checks every input field, without
// simulating a successful daemon mutation in thirteen separate fixtures.
func TestManagerHumanControlOwnerPayloadParity(t *testing.T) {
	const ref = "peer:payload-session"
	cases := []struct {
		method  string
		request appwire.SessionControlRequest
	}{
		{appwire.MethodEvenerSandboxEscalationResolve, appwire.SessionControlRequest{Action: "approval", Approval: &appwire.SandboxEscalationResolveParams{Ref: ref, ThreadID: "payload-session", EscalationID: "escalation-7", Approve: true}}},
		{appwire.MethodThreadResume, appwire.SessionControlRequest{Action: "resume", Resume: &appwire.ThreadResumeParams{Ref: ref, Session: "payload-session"}}},
		{appwire.MethodThreadClear, appwire.SessionControlRequest{Action: "clear", Clear: &appwire.ThreadClearParams{Ref: ref, ClientMutationID: "clear-7", ExpectedInstanceID: "instance-7"}}},
		{appwire.MethodThreadModelSet, appwire.SessionControlRequest{Action: "model", Model: &appwire.ThreadModelSetParams{Ref: ref, ModelProvider: "openai", Model: "gpt-5"}}},
		{appwire.MethodThreadVisionModelSet, appwire.SessionControlRequest{Action: "vision_model", VisionModel: &appwire.ThreadVisionModelSetParams{Ref: ref, VisionModel: "openai/gpt-5"}}},
		{appwire.MethodThreadReasoningEffortSet, appwire.SessionControlRequest{Action: "reasoning_effort", ReasoningEffort: &appwire.ThreadReasoningEffortSetParams{Ref: ref, ReasoningEffort: "high"}}},
		{appwire.MethodThreadCompactStart, appwire.SessionControlRequest{Action: "compact", Compact: &appwire.ThreadCompactStartParams{Ref: ref}}},
		{appwire.MethodTurnDrainAsSteer, appwire.SessionControlRequest{Action: "drain_queue", DrainQueue: &appwire.TurnDrainAsSteerParams{Ref: ref, ClientMutationID: "drain-7", ExpectedInstanceID: "instance-7", ExpectedQueueRevision: 19, Input: []appwire.InputItem{{Type: "text", Text: "do not drop this input"}}}}},
		{appwire.MethodTurnPromoteQueuedAsSteer, appwire.SessionControlRequest{Action: "promote_queued", PromoteQueued: &appwire.TurnPromoteQueuedAsSteerParams{Ref: ref, Index: 3, ClientMutationID: "promote-7", ExpectedInstanceID: "instance-7", ExpectedEntryID: "entry-7"}}},
		{appwire.MethodTurnCancelQueued, appwire.SessionControlRequest{Action: "cancel_queued", CancelQueued: &appwire.TurnCancelQueuedParams{Ref: ref, Index: 4, ClientMutationID: "cancel-7", ExpectedInstanceID: "instance-7", ExpectedEntryID: "entry-8"}}},
		{appwire.MethodGoalSet, appwire.SessionControlRequest{Action: "goal", Goal: &appwire.GoalSetParams{Ref: ref, Objective: "complete the payload check"}}},
		{appwire.MethodNotesHumanSet, appwire.SessionControlRequest{Action: "human_notes", HumanNotes: &appwire.NotesHumanSetParams{Ref: ref, ClientMutationID: "notes-7", ExpectedInstanceID: "instance-7", Note: "retain the whole note"}}},
		{appwire.MethodUrlsRemove, appwire.SessionControlRequest{Action: "remove_url", RemoveURL: &appwire.UrlsRemoveParams{Ref: ref, ClientMutationID: "url-7", ExpectedInstanceID: "instance-7", ID: "url-entry-7"}}},
	}
	for _, tc := range cases {
		t.Run(tc.request.Action, func(t *testing.T) {
			source := &controlPayloadRecordingSource{scriptedAppSource: &scriptedAppSource{
				id: "peer",
				thread: appwire.Thread{ID: "payload-session", Evener: appwire.EvenerThread{
					Ref: ref, Capabilities: appwire.ThreadCapabilities{Clear: true, Compact: true, ChangeModel: true, ChangeVisionModel: true, Goal: true, SharedNotes: true},
				}},
			}}
			web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), HubStateRoot: t.TempDir()})
			web.sources.Add(source)
			owner := &sessionOperations{cfg: web.cfg, sources: web.sources, navigation: web.navigation, resolve: web.resolveTopLevelSessionRef}

			// Compare the serialized literal, not a value reconstructed by the
			// dispatch adapter under test. This also checks the union JSON decode.
			rawUnion, err := json.Marshal(appwire.SessionOperationRequest{Control: &tc.request})
			if err != nil {
				t.Fatal(err)
			}
			var encoded struct {
				Control map[string]json.RawMessage `json:"control"`
			}
			if err := json.Unmarshal(rawUnion, &encoded); err != nil {
				t.Fatal(err)
			}
			want := encoded.Control[tc.request.Action]
			if len(want) == 0 {
				t.Fatalf("missing typed payload for %s", tc.request.Action)
			}
			var decoded appwire.SessionOperationRequest
			if err := json.Unmarshal(rawUnion, &decoded); err != nil {
				t.Fatalf("Manager union rejected %s: %v", tc.request.Action, err)
			}
			if _, err := owner.Control(t.Context(), *decoded.Control); err != nil {
				t.Fatalf("Manager owner %s: %v", tc.request.Action, err)
			}
			if _, err := web.appRPC.Router().Dispatch(t.Context(), appwire.Request{ID: appwire.NewIntID(1), Method: tc.method, Params: want}); err != nil {
				t.Fatalf("human RPC owner %s: %v", tc.request.Action, err)
			}
			if len(source.calls) != 2 {
				t.Fatalf("owner received %d calls, want Manager and human: %+v", len(source.calls), source.calls)
			}
			for i, call := range source.calls {
				if call.method != tc.method {
					t.Fatalf("owner call %d method=%q, want %q", i, call.method, tc.method)
				}
				got, err := json.Marshal(call.params)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("owner call %d payload=%s, want exact typed payload %s", i, got, want)
				}
			}
		})
	}
}
