package appwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestModelControlRequestShapes(t *testing.T) {
	valid := ThreadModelSetParams{
		Ref:                    "local:th_1",
		ModelProvider:          "work",
		Model:                  "gpt-5.5",
		ClientMutationID:       "cm_1",
		ExpectedInstanceID:     "instance_1",
		ExpectedBootGeneration: "7",
	}
	assertJSONKeys(t, valid,
		"ref", "modelProvider", "model", "clientMutationId", "expectedInstanceId", "expectedBootGeneration")
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ThreadModelSetParams)
	}{
		{"ref", func(params *ThreadModelSetParams) { params.Ref = "" }},
		{"model provider", func(params *ThreadModelSetParams) { params.ModelProvider = "" }},
		{"model", func(params *ThreadModelSetParams) { params.Model = "" }},
		{"mutation id", func(params *ThreadModelSetParams) { params.ClientMutationID = "" }},
		{"instance id", func(params *ThreadModelSetParams) { params.ExpectedInstanceID = "" }},
		{"boot generation", func(params *ThreadModelSetParams) { params.ExpectedBootGeneration = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := valid
			test.mutate(&params)
			if err := params.Validate(); err == nil {
				t.Fatal("incomplete model-set params accepted")
			}
		})
	}
}

func TestModelControlSnapshotCarriersReuseWholeShape(t *testing.T) {
	snapshot := ModelControlSnapshot{
		Authority: ModelControlAuthority{InstanceID: "instance_1", BootGeneration: "7"},
		Revision:  9,
		Active: ModelControlActive{
			ModelProvider:         "work",
			Model:                 "gpt-5.5",
			ReasoningEffortLevels: []string{"low", "high"},
			SupportsReasoning:     true,
		},
		Desired: &ModelControlDesired{
			RequestSequence:  4,
			ClientMutationID: "cm_1",
			ModelProvider:    "anthropic",
			Model:            "claude-opus-4-6",
			Lifecycle:        ModelControlLifecyclePending,
		},
	}
	assertJSONKeys(t, snapshot, "authority", "revision", "active", "desired")
	assertJSONKeys(t, snapshot.Authority, "instanceId", "bootGeneration")
	assertJSONKeys(t, snapshot.Active, "modelProvider", "model", "reasoningEffortLevels", "supportsReasoning")
	assertJSONKeys(t, *snapshot.Desired,
		"requestSequence", "clientMutationId", "modelProvider", "model", "lifecycle")

	response := ThreadModelSetResponse{
		ModelControlVersion: ModelControlVersion1,
		Disposition:         ModelControlDispositionAccepted,
		ModelControl:        &snapshot,
	}
	notification := ThreadModelControlChangedParams{
		ThreadID: "th_1", Ref: "local:th_1", ModelControl: snapshot,
	}
	read := ThreadReadResponse{Thread: Thread{Evener: EvenerThread{
		Capabilities: ThreadCapabilities{ModelControlVersion: ModelControlVersion1},
		ModelControl: &snapshot,
	}}}

	assertJSONKeys(t, response, "modelControlVersion", "disposition", "modelControl")
	assertJSONKeys(t, notification, "threadId", "ref", "modelControl")

	responseJSON := modelControlJSON(t, response, "modelControl")
	notificationJSON := modelControlJSON(t, notification, "modelControl")
	readJSON := modelControlJSON(t, read, "thread", "evener", "modelControl")
	if !bytes.Equal(responseJSON, notificationJSON) || !bytes.Equal(responseJSON, readJSON) {
		t.Fatalf("model control carriers differ: response=%s notification=%s read=%s", responseJSON, notificationJSON, readJSON)
	}

	readRaw, err := json.Marshal(read)
	if err != nil {
		t.Fatalf("marshal thread read: %v", err)
	}
	var capability struct {
		Thread struct {
			Evener struct {
				Capabilities struct {
					ModelControlVersion uint `json:"modelControlVersion"`
				} `json:"capabilities"`
			} `json:"evener"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(readRaw, &capability); err != nil {
		t.Fatalf("decode thread read capability: %v", err)
	}
	if capability.Thread.Evener.Capabilities.ModelControlVersion != ModelControlVersion1 {
		t.Fatalf("model control capability = %d, want %d", capability.Thread.Evener.Capabilities.ModelControlVersion, ModelControlVersion1)
	}

	manager := SessionControlResponse{Model: &response}
	assertJSONKeys(t, manager, "result", "actor", "model")
}

func TestValidateThreadModelSetResponseRequiresCompleteVersionOneSnapshot(t *testing.T) {
	valid := completeModelControlResponse()
	if err := ValidateThreadModelSetResponse(valid); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ThreadModelSetResponse)
	}{
		{"missing version", func(response *ThreadModelSetResponse) { response.ModelControlVersion = 0 }},
		{"unknown version", func(response *ThreadModelSetResponse) { response.ModelControlVersion = 2 }},
		{"missing disposition", func(response *ThreadModelSetResponse) { response.Disposition = "" }},
		{"invalid disposition", func(response *ThreadModelSetResponse) { response.Disposition = "invalid" }},
		{"missing snapshot", func(response *ThreadModelSetResponse) { response.ModelControl = nil }},
		{"missing instance", func(response *ThreadModelSetResponse) { response.ModelControl.Authority.InstanceID = "" }},
		{"missing boot", func(response *ThreadModelSetResponse) { response.ModelControl.Authority.BootGeneration = "" }},
		{"zero revision", func(response *ThreadModelSetResponse) { response.ModelControl.Revision = 0 }},
		{"missing active provider", func(response *ThreadModelSetResponse) { response.ModelControl.Active.ModelProvider = "" }},
		{"missing active model", func(response *ThreadModelSetResponse) { response.ModelControl.Active.Model = "" }},
		{"zero desired sequence", func(response *ThreadModelSetResponse) { response.ModelControl.Desired.RequestSequence = 0 }},
		{"missing desired mutation", func(response *ThreadModelSetResponse) { response.ModelControl.Desired.ClientMutationID = "" }},
		{"missing desired provider", func(response *ThreadModelSetResponse) { response.ModelControl.Desired.ModelProvider = "" }},
		{"missing desired model", func(response *ThreadModelSetResponse) { response.ModelControl.Desired.Model = "" }},
		{"invalid desired lifecycle", func(response *ThreadModelSetResponse) { response.ModelControl.Desired.Lifecycle = "invalid" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := completeModelControlResponse()
			test.mutate(&response)
			if err := ValidateThreadModelSetResponse(response); err == nil {
				t.Fatal("incomplete model-set response accepted")
			}
		})
	}
}

func TestModelControlWireErrorsCarryMachineReadableOutcome(t *testing.T) {
	tests := []struct {
		name        string
		err         WireError
		wantInfo    ErrorInfo
		wantOutcome MutationOutcome
		wantRetry   RetryDisposition
	}{
		{
			name:        "stale authority",
			err:         ModelControlAuthorityStale("cm_1", "stale"),
			wantInfo:    ErrorModelControlAuthorityStale,
			wantOutcome: MutationOutcomeNotAccepted,
			wantRetry:   RetryDispositionNone,
		},
		{
			name:        "unknown after dispatch",
			err:         MutationOutcomeUnknownBlocked("cm_1", "unknown"),
			wantInfo:    ErrorMutationOutcomeUnknown,
			wantOutcome: MutationOutcomeUnknown,
			wantRetry:   RetryDispositionBlocked,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, ok := ErrorDataOf(test.err.Data)
			if !ok {
				t.Fatalf("error data type = %T, want ErrorData", test.err.Data)
			}
			if data.EvenerErrorInfo != test.wantInfo || data.ClientMutationID != "cm_1" || data.MutationOutcome != test.wantOutcome || data.RetryDisposition != test.wantRetry {
				t.Fatalf("error data = %+v", data)
			}
		})
	}
}

func TestClientThreadModelSetRejectsMalformedSuccessAsBlockedUnknown(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	errCh := make(chan error, 1)
	go func() {
		_, err := client.ThreadModelSet(ctx, ThreadModelSetParams{
			Ref:                    "local:th_1",
			ModelProvider:          "work",
			Model:                  "gpt-5.5",
			ClientMutationID:       "cm_1",
			ExpectedInstanceID:     "instance_1",
			ExpectedBootGeneration: "7",
		})
		errCh <- err
	}()

	var request Message
	select {
	case request = <-transport.writes:
	case <-time.After(time.Second):
		t.Fatal("model-set request was not written")
	}
	transport.reads <- ResponseMessage(request.Request.ID, map[string]any{})

	select {
	case err := <-errCh:
		var wire WireError
		if !errors.As(err, &wire) || wire.Code != CodeInternalError {
			t.Fatalf("malformed success error = %T %+v, want internal WireError", err, err)
		}
		data, ok := ErrorDataOf(wire.Data)
		if !ok || data.EvenerErrorInfo != ErrorMutationOutcomeUnknown || data.ClientMutationID != "cm_1" || data.MutationOutcome != MutationOutcomeUnknown || data.RetryDisposition != RetryDispositionBlocked {
			t.Fatalf("malformed success data = %+v, ok = %t", data, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("malformed success did not settle the request")
	}
}

func TestModelControlEnumValues(t *testing.T) {
	for got, want := range map[ModelControlLifecycle]string{
		ModelControlLifecyclePending:  "pending",
		ModelControlLifecycleApplied:  "applied",
		ModelControlLifecycleRejected: "rejected",
	} {
		if string(got) != want {
			t.Errorf("model control lifecycle = %q, want %q", got, want)
		}
	}
	for got, want := range map[ModelControlDisposition]string{
		ModelControlDispositionAccepted:   "accepted",
		ModelControlDispositionSuperseded: "superseded",
	} {
		if string(got) != want {
			t.Errorf("model control disposition = %q, want %q", got, want)
		}
	}
}

func TestModelControlNotificationTargetPreservesSnapshot(t *testing.T) {
	snapshot := ModelControlSnapshot{
		Authority: ModelControlAuthority{InstanceID: "instance_1", BootGeneration: "7"},
		Revision:  3,
		Active:    ModelControlActive{ModelProvider: "work", Model: "gpt-5.5"},
	}
	got := (ThreadModelControlChangedParams{ModelControl: snapshot}).WithNotificationTarget("th_1", "local:th_1")
	targeted, ok := got.(ThreadModelControlChangedParams)
	if !ok {
		t.Fatalf("targeted model-control notification type = %T", got)
	}
	if targeted.ThreadID != "th_1" || targeted.Ref != "local:th_1" || targeted.ModelControl.Revision != snapshot.Revision {
		t.Fatalf("targeted model-control notification = %+v", targeted)
	}
}

func modelControlJSON(t *testing.T, value any, path ...string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	for _, key := range path {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatalf("decode %T at %q: %v", value, key, err)
		}
		var ok bool
		raw, ok = object[key]
		if !ok {
			t.Fatalf("%T has no %q at path %v", value, key, path)
		}
	}
	return raw
}

func completeModelControlResponse() ThreadModelSetResponse {
	return ThreadModelSetResponse{
		ModelControlVersion: ModelControlVersion1,
		Disposition:         ModelControlDispositionAccepted,
		ModelControl: &ModelControlSnapshot{
			Authority: ModelControlAuthority{InstanceID: "instance_1", BootGeneration: "7"},
			Revision:  9,
			Active: ModelControlActive{
				ModelProvider:         "work",
				Model:                 "gpt-5.5",
				ReasoningEffortLevels: []string{"low", "high"},
				SupportsReasoning:     true,
			},
			Desired: &ModelControlDesired{
				RequestSequence:  4,
				ClientMutationID: "cm_1",
				ModelProvider:    "anthropic",
				Model:            "claude-opus-4-6",
				Lifecycle:        ModelControlLifecyclePending,
			},
		},
	}
}
