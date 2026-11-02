package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestManagerControlSchemaHasEveryBehaviorAction(t *testing.T) {
	want := []string{"approval", "resume", "fork", "clear", "model", "vision_model", "reasoning_effort", "compact", "drain_queue", "promote_queued", "cancel_queued", "rename", "goal", "human_notes", "remove_url", "archive", "delete", "pin", "unpin", "seen"}
	schema := managerControlSchema()
	props := schema["properties"].(map[string]any)
	got := props["action"].(map[string]any)["enum"]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("advertised actions=%v, behavior cases=%v", got, want)
	}
	for _, name := range want {
		t.Run(name, func(t *testing.T) {
			var params any
			for _, action := range managerControlActions {
				if action.name == name {
					params = action.params
					break
				}
			}
			if params == nil {
				t.Fatalf("%s has no typed wire action", name)
			}
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if _, hasRef := fields["ref"]; hasRef || name == "resume" {
				fields["ref"] = "local:worker"
			}
			if name == "archive" {
				fields["kind"] = "session"
			}
			raw, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			request, err := decodeManagerControl(name, raw)
			if err != nil {
				t.Fatalf("schema-advertised action has no typed dispatch: %v", err)
			}
			if request.Action != name {
				t.Fatalf("wrong action: %+v", request)
			}
		})
	}
	for _, raw := range []string{
		`{"action":"approval","approval":{"ref":"local:worker","escalationId":"esc"},"resume":{"ref":"local:worker"}}`,
		`{"action":"approval","resume":{"ref":"local:worker"}}`,
		`{"action":"unknown","approval":{"ref":"local:worker"}}`,
	} {
		var request appwire.SessionControlRequest
		if err := json.Unmarshal([]byte(raw), &request); err == nil {
			t.Fatalf("closed union accepted %s", raw)
		}
	}
	for _, tc := range []struct {
		name    string
		request appwire.SessionControlRequest
	}{
		{"approval", appwire.SessionControlRequest{Action: "approval", Approval: &appwire.SandboxEscalationResolveParams{ManagerProof: &appwire.SessionActorProof{}}}},
		{"drain_queue", appwire.SessionControlRequest{Action: "drain_queue", DrainQueue: &appwire.TurnDrainAsSteerParams{ManagerProof: &appwire.SessionActorProof{}}}},
		{"promote_queued", appwire.SessionControlRequest{Action: "promote_queued", PromoteQueued: &appwire.TurnPromoteQueuedAsSteerParams{ManagerProof: &appwire.SessionActorProof{}}}},
	} {
		if err := (appwire.SessionOperationRequest{Control: &tc.request}).Validate(); err == nil {
			t.Fatalf("external %s operation accepted forged actor proof", tc.name)
		}
		if _, err := decodeManagerControl(tc.name, []byte(`{"ref":"local:worker","managerProof":{"actor":{"kind":"manager"}}}`)); err == nil {
			t.Fatalf("%s tool forged actor proof", tc.name)
		}
	}
	if _, err := decodeManagerControl("approval", []byte(`{"ref":"local:worker","threadId":"other"}`)); err == nil {
		t.Fatal("tool supplied bare thread alias")
	}
	if _, err := decodeManagerControl("archive", []byte(`{"kind":"project","id":"other","archived":true}`)); err == nil {
		t.Fatal("project archive escaped session-only authority")
	}
}

func TestManagerStopModesTypedScopes(t *testing.T) {
	base := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"target": map[string]any{"type": "string"}, "include_children": map[string]any{"type": "boolean"}}, "required": []string{"target"}}
	schema := managerStopSchema(base)
	props := schema["properties"].(map[string]any)
	if _, ordinary := props["target"]; !ordinary {
		t.Fatal("ordinary recursive job_stop target disappeared")
	}
	branches := props["hub"].(map[string]any)["oneOf"].([]any)
	if len(branches) != 6 {
		t.Fatalf("human stop branches=%d, want interrupt, delegate, owned_delegate, job, shutdown, force_stop", len(branches))
	}
	if _, ordinaryRequired := schema["required"]; ordinaryRequired {
		t.Fatal("ordinary target incorrectly required for Manager mode")
	}
	for _, tc := range []struct {
		mode string
		wire appwire.SessionStopRequest
	}{
		{"interrupt", appwire.SessionStopRequest{Mode: "interrupt", Interrupt: &appwire.TurnInterruptParams{Ref: "local:worker", ClientMutationID: "id"}}},
		{"delegate", appwire.SessionStopRequest{Mode: "delegate", Delegate: &appwire.DelegateStopParams{Ref: "local:worker", DelegateID: "dlg_1"}}},
		{"owned_delegate", appwire.SessionStopRequest{Mode: "owned_delegate", OwnedDelegate: &appwire.SessionOwnedDelegateStopParams{Ref: "local:worker", DelegateID: "dlg_1"}}},
		{"job", appwire.SessionStopRequest{Mode: "job", Job: &appwire.SessionJobStopParams{Ref: "local:worker", JobID: "job_1", IncludeChildren: true, MaxWaitMS: 1}}},
		{"shutdown", appwire.SessionStopRequest{Mode: "shutdown", Shutdown: &appwire.ThreadShutdownParams{Ref: "local:worker"}}},
		{"force_stop", appwire.SessionStopRequest{Mode: "force_stop", ForceStop: &appwire.ThreadForceStopParams{Ref: "local:worker"}}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			raw, err := json.Marshal(tc.wire)
			if err != nil {
				t.Fatal(err)
			}
			var decoded appwire.SessionStopRequest
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, tc.wire) {
				t.Fatalf("stop mode changed payload: got %+v want %+v", decoded, tc.wire)
			}
			mismatched := tc.wire
			mismatched.Mode = "other"
			if err := mismatched.Validate(); err == nil {
				t.Fatal("mismatched stop payload accepted")
			}
		})
	}
	if err := (appwire.SessionStopRequest{Mode: "interrupt", Interrupt: &appwire.TurnInterruptParams{}, Delegate: &appwire.DelegateStopParams{}}).Validate(); err == nil {
		t.Fatal("multiple stop payloads accepted")
	}
	for _, request := range []appwire.SessionStopRequest{
		{Mode: "job", Job: &appwire.SessionJobStopParams{JobID: "dlg_wrong"}},
		{Mode: "job", Job: &appwire.SessionJobStopParams{JobID: "job_valid", MaxWaitMS: -1}},
		{Mode: "owned_delegate", OwnedDelegate: &appwire.SessionOwnedDelegateStopParams{DelegateID: "job_wrong"}},
		{Mode: "owned_delegate", OwnedDelegate: &appwire.SessionOwnedDelegateStopParams{DelegateID: "dlg_valid", MaxWaitMS: -1}},
		{Mode: "delegate", Delegate: &appwire.DelegateStopParams{DelegateID: "job_wrong"}},
		{Mode: "delegate", Delegate: &appwire.DelegateStopParams{DelegateID: "dlg_ok"}, Job: &appwire.SessionJobStopParams{JobID: "job_smuggled"}},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("accepted wrong-prefix or smuggled payload: %+v", request)
		}
	}
}
