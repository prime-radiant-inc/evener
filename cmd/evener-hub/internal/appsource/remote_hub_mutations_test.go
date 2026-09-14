package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

const (
	testControllerRef = "host:S"
	testRemoteRef     = "local:S"
	testThreadID      = "S"
)

// wireEnvelope decodes the ref/threadId/clientMutationId fields common to the
// params forwarded to the remote hub.
type wireEnvelope struct {
	Ref              string `json:"ref"`
	ThreadID         string `json:"threadId"`
	ClientMutationID string `json:"clientMutationId"`
}

func decodeWireEnvelope(t *testing.T, raw json.RawMessage) wireEnvelope {
	t.Helper()
	var env wireEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode forwarded params %s: %v", raw, err)
	}
	return env
}

// TestRemoteHubMutationWireMethodsAndRefs asserts, for every 05c method, the
// exact wire method it sends, that a controller ref arrives as local:, and that
// clientMutationId is forwarded unchanged on the mutation-envelope methods.
func TestRemoteHubMutationWireMethodsAndRefs(t *testing.T) {
	type methodCase struct {
		name       string
		wantMethod string
		ref        bool
		threadID   bool
		cmid       string
		invoke     func(context.Context, *RemoteHubSource) error
	}

	cases := []methodCase{
		// Mutation envelope (ClientMutationID present).
		{"StartTurn", appwire.MethodTurnStart, true, true, "cmid-StartTurn", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.StartTurn(ctx, appwire.TurnStartParams{Ref: testControllerRef, ThreadID: testThreadID, ClientMutationID: "cmid-StartTurn"})
			return err
		}},
		{"SteerTurn", appwire.MethodTurnSteer, true, true, "cmid-SteerTurn", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.SteerTurn(ctx, appwire.TurnSteerParams{Ref: testControllerRef, ThreadID: testThreadID, ClientMutationID: "cmid-SteerTurn"})
			return err
		}},
		{"InterruptTurn", appwire.MethodTurnInterrupt, true, true, "cmid-InterruptTurn", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.InterruptTurn(ctx, appwire.TurnInterruptParams{Ref: testControllerRef, ThreadID: testThreadID, ClientMutationID: "cmid-InterruptTurn"})
			return err
		}},
		{"QueueTurn", appwire.MethodTurnQueue, true, false, "cmid-QueueTurn", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.QueueTurn(ctx, appwire.TurnQueueParams{Ref: testControllerRef, ClientMutationID: "cmid-QueueTurn"})
			return err
		}},
		{"DrainAsSteer", appwire.MethodTurnDrainAsSteer, true, false, "cmid-DrainAsSteer", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.DrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{Ref: testControllerRef, ClientMutationID: "cmid-DrainAsSteer"})
			return err
		}},
		{"PromoteQueuedAsSteer", appwire.MethodTurnPromoteQueuedAsSteer, true, false, "cmid-PromoteQueuedAsSteer", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.PromoteQueuedAsSteer(ctx, appwire.TurnPromoteQueuedAsSteerParams{Ref: testControllerRef, ClientMutationID: "cmid-PromoteQueuedAsSteer"})
			return err
		}},
		{"CancelQueued", appwire.MethodTurnCancelQueued, true, false, "cmid-CancelQueued", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.CancelQueued(ctx, appwire.TurnCancelQueuedParams{Ref: testControllerRef, ClientMutationID: "cmid-CancelQueued"})
			return err
		}},
		{"NotesHumanSet", appwire.MethodNotesHumanSet, true, false, "cmid-NotesHumanSet", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.NotesHumanSet(ctx, appwire.NotesHumanSetParams{Ref: testControllerRef, ClientMutationID: "cmid-NotesHumanSet"})
			return err
		}},
		{"UrlsRemove", appwire.MethodUrlsRemove, true, false, "cmid-UrlsRemove", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.UrlsRemove(ctx, appwire.UrlsRemoveParams{Ref: testControllerRef, ClientMutationID: "cmid-UrlsRemove"})
			return err
		}},
		{"ClearThread", appwire.MethodThreadClear, true, false, "cmid-ClearThread", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.ClearThread(ctx, appwire.ThreadClearParams{Ref: testControllerRef, ClientMutationID: "cmid-ClearThread"})
			return err
		}},

		// Thread/turn lifecycle without a ClientMutationID.
		{"StartThread", appwire.MethodThreadStart, false, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.StartThread(ctx, appwire.ThreadStartParams{CWD: "/work"})
			return err
		}},
		{"ResumeThread", appwire.MethodThreadResume, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.ResumeThread(ctx, appwire.ThreadResumeParams{Ref: testControllerRef})
			return err
		}},
		{"ForkThread", appwire.MethodThreadFork, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.ForkThread(ctx, appwire.ThreadForkParams{Ref: testControllerRef})
			return err
		}},
		{"CompactThread", appwire.MethodThreadCompactStart, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			return s.CompactThread(ctx, appwire.ThreadCompactStartParams{Ref: testControllerRef})
		}},
		{"ShutdownThread", appwire.MethodThreadShutdown, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			return s.ShutdownThread(ctx, appwire.ThreadShutdownParams{Ref: testControllerRef})
		}},
		{"SetThreadModel", appwire.MethodThreadModelSet, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			return s.SetThreadModel(ctx, appwire.ThreadModelSetParams{Ref: testControllerRef})
		}},
		{"SetThreadReasoningEffort", appwire.MethodThreadReasoningEffortSet, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			return s.SetThreadReasoningEffort(ctx, appwire.ThreadReasoningEffortSetParams{Ref: testControllerRef})
		}},
		{"SetThreadVisionModel", appwire.MethodThreadVisionModelSet, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			return s.SetThreadVisionModel(ctx, appwire.ThreadVisionModelSetParams{Ref: testControllerRef})
		}},
		{"SetThreadName", appwire.MethodEvenerThreadNameSet, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			return s.SetThreadName(ctx, appwire.ThreadNameSetParams{Ref: testControllerRef, Name: "n"})
		}},
		{"GoalSet", appwire.MethodGoalSet, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.GoalSet(ctx, appwire.GoalSetParams{Ref: testControllerRef})
			return err
		}},
		{"ResolveSandboxEscalation", appwire.MethodEvenerSandboxEscalationResolve, true, true, "", func(ctx context.Context, s *RemoteHubSource) error {
			return s.ResolveSandboxEscalation(ctx, appwire.SandboxEscalationResolveParams{Ref: testControllerRef, ThreadID: testThreadID})
		}},

		// Read-only forwards.
		{"ListTasks", appwire.MethodEvenerTasksList, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.ListTasks(ctx, appwire.TaskListParams{Ref: testControllerRef})
			return err
		}},
		{"ListJobs", appwire.MethodEvenerJobsList, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.ListJobs(ctx, appwire.JobsListParams{Ref: testControllerRef})
			return err
		}},
		{"JobOutput", appwire.MethodEvenerJobsOutput, true, false, "", func(ctx context.Context, s *RemoteHubSource) error {
			_, err := s.JobOutput(ctx, appwire.JobsOutputParams{Ref: testControllerRef, JobID: "j1"})
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, calls := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
				return scriptedReply{result: map[string]any{}}
			})
			if err := tc.invoke(t.Context(), source); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			env := decodeWireEnvelope(t, lastMethodCall(t, calls(), tc.wantMethod))
			if tc.ref && env.Ref != testRemoteRef {
				t.Errorf("%s ref = %q, want %q", tc.name, env.Ref, testRemoteRef)
			}
			if tc.threadID && env.ThreadID != testThreadID {
				t.Errorf("%s threadId = %q, want %q", tc.name, env.ThreadID, testThreadID)
			}
			if tc.cmid != "" && env.ClientMutationID != tc.cmid {
				t.Errorf("%s clientMutationId = %q, want %q", tc.name, env.ClientMutationID, tc.cmid)
			}
		})
	}
}

func localThreadFixture() appwire.Thread {
	return appwire.Thread{
		ID:     "S",
		Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:S", ParentRef: "local:P", InstanceID: "xyz"},
	}
}

// TestRemoteHubMutationOutboundThreadTranslation asserts that thread-creating
// and clearing responses have their Thread (and ClearThread's Ref) moved from
// the remote "local:" namespace into the controller's "host:" namespace.
func TestRemoteHubMutationOutboundThreadTranslation(t *testing.T) {
	cases := []struct {
		name   string
		invoke func(context.Context, *RemoteHubSource) (appwire.Thread, string, error)
	}{
		{"StartThread", func(ctx context.Context, s *RemoteHubSource) (appwire.Thread, string, error) {
			resp, err := s.StartThread(ctx, appwire.ThreadStartParams{CWD: "/work"})
			return resp.Thread, "", err
		}},
		{"ResumeThread", func(ctx context.Context, s *RemoteHubSource) (appwire.Thread, string, error) {
			resp, err := s.ResumeThread(ctx, appwire.ThreadResumeParams{Ref: testControllerRef})
			return resp.Thread, "", err
		}},
		{"ForkThread", func(ctx context.Context, s *RemoteHubSource) (appwire.Thread, string, error) {
			resp, err := s.ForkThread(ctx, appwire.ThreadForkParams{Ref: testControllerRef})
			return resp.Thread, "", err
		}},
		{"ClearThread", func(ctx context.Context, s *RemoteHubSource) (appwire.Thread, string, error) {
			resp, err := s.ClearThread(ctx, appwire.ThreadClearParams{Ref: testControllerRef, ClientMutationID: "cmid-clear"})
			return resp.Thread, resp.Ref, err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
				switch method {
				case appwire.MethodThreadStart:
					return scriptedReply{result: appwire.ThreadStartResponse{Thread: localThreadFixture()}}
				case appwire.MethodThreadResume:
					return scriptedReply{result: appwire.ThreadResumeResponse{Thread: localThreadFixture()}}
				case appwire.MethodThreadFork:
					return scriptedReply{result: appwire.ThreadForkResponse{Thread: localThreadFixture()}}
				case appwire.MethodThreadClear:
					return scriptedReply{result: appwire.ThreadClearResponse{Thread: localThreadFixture(), Ref: testRemoteRef}}
				default:
					t.Errorf("unexpected method %q", method)
					return scriptedReply{result: map[string]any{}}
				}
			})
			thread, ref, err := tc.invoke(t.Context(), source)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if thread.Source != "host" {
				t.Errorf("Thread.Source = %q, want host", thread.Source)
			}
			if thread.Evener.Ref != testControllerRef {
				t.Errorf("Thread.Evener.Ref = %q, want %q", thread.Evener.Ref, testControllerRef)
			}
			if thread.Evener.ParentRef != "host:P" {
				t.Errorf("Thread.Evener.ParentRef = %q, want host:P", thread.Evener.ParentRef)
			}
			if thread.Evener.InstanceID != "xyz" {
				t.Errorf("Thread.Evener.InstanceID = %q, want xyz", thread.Evener.InstanceID)
			}
			if tc.name == "ClearThread" && ref != testControllerRef {
				t.Errorf("ClearThread Ref = %q, want %q", ref, testControllerRef)
			}
		})
	}
}

// TestRemoteHubMutationOutcomeUnknownOnResponseLoss asserts a lost mutation
// response becomes ErrorMutationOutcomeUnknown while the same loss on a
// read-only call stays SessionUnavailable.
func TestRemoteHubMutationOutcomeUnknownOnResponseLoss(t *testing.T) {
	t.Run("mutation", func(t *testing.T) {
		source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
			return scriptedReply{closeConn: true}
		})
		_, err := source.StartTurn(t.Context(), appwire.TurnStartParams{Ref: testControllerRef, ClientMutationID: "cmid-loss"})
		if err == nil {
			t.Fatal("StartTurn succeeded after the remote closed the pipe")
		}
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("error = %T %v, want appwire.WireError", err, err)
		}
		if wire.Code != appwire.CodeInternalError {
			t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeInternalError)
		}
		data, ok := wire.Data.(appwire.ErrorData)
		if !ok {
			t.Fatalf("Data = %T %v, want appwire.ErrorData", wire.Data, wire.Data)
		}
		if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown {
			t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorMutationOutcomeUnknown)
		}
		if data.ClientMutationID != "cmid-loss" {
			t.Fatalf("clientMutationId = %q, want cmid-loss", data.ClientMutationID)
		}
		if data.MutationOutcome != appwire.MutationOutcomeUnknown {
			t.Fatalf("mutationOutcome = %q, want %q", data.MutationOutcome, appwire.MutationOutcomeUnknown)
		}
		if data.RetryDisposition != appwire.RetryDispositionAutomatic {
			t.Fatalf("retryDisposition = %q, want %q", data.RetryDisposition, appwire.RetryDispositionAutomatic)
		}
	})

	t.Run("non-mutation", func(t *testing.T) {
		source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
			return scriptedReply{closeConn: true}
		})
		_, err := source.ListModels(t.Context(), appwire.ModelListParams{})
		if err == nil {
			t.Fatal("ListModels succeeded after the remote closed the pipe")
		}
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("error = %T %v, want appwire.WireError", err, err)
		}
		if wire.Code != appwire.CodeUnavailable {
			t.Fatalf("code = %d, want %d (read loss must stay SessionUnavailable)", wire.Code, appwire.CodeUnavailable)
		}
		data, ok := wire.Data.(appwire.ErrorData)
		if !ok {
			t.Fatalf("Data = %T %v, want appwire.ErrorData", wire.Data, wire.Data)
		}
		if data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
			t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorSessionUnavailable)
		}
	})
}

// TestRemoteHubMutationPreservesSemanticWireError asserts a semantic wire error
// from the responder passes through both call and mutationCall unchanged.
func TestRemoteHubMutationPreservesSemanticWireError(t *testing.T) {
	t.Run("call", func(t *testing.T) {
		semantic := appwire.InvalidParams("boom")
		source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
			return scriptedReply{wireErr: &semantic}
		})
		_, err := source.ListModels(t.Context(), appwire.ModelListParams{})
		assertSemanticBoom(t, err)
	})

	t.Run("mutation", func(t *testing.T) {
		semantic := appwire.InvalidParams("boom")
		source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
			return scriptedReply{wireErr: &semantic}
		})
		_, err := source.StartTurn(t.Context(), appwire.TurnStartParams{Ref: testControllerRef, ClientMutationID: "cmid-sem"})
		assertSemanticBoom(t, err)
	})
}

func assertSemanticBoom(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("call succeeded despite a semantic error")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("code = %d, want %d (semantic error remapped)", wire.Code, appwire.CodeInvalidParams)
	}
	if !strings.Contains(wire.Message, "boom") {
		t.Fatalf("message = %q, want it to preserve %q", wire.Message, "boom")
	}
}

// TestRemoteHubMutationForeignRefRefusedWithoutCall asserts a mutation whose
// ref names another source is refused locally before any wire call.
func TestRemoteHubMutationForeignRefRefusedWithoutCall(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: map[string]any{}}
	})
	_, err := source.StartTurn(t.Context(), appwire.TurnStartParams{Ref: "other:X", ClientMutationID: "cmid-foreign"})
	if err == nil {
		t.Fatal("StartTurn accepted a foreign ref")
	}
	if !strings.Contains(err.Error(), "source not found: other") {
		t.Fatalf("error = %v, want it to contain %q", err, "source not found: other")
	}
	for _, call := range calls() {
		if call.method == appwire.MethodTurnStart {
			t.Fatalf("foreign ref was forwarded: %+v", calls())
		}
	}
}
