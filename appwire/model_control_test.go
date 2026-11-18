package appwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
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

func TestSessionControlModelParamsExposeOnlyUserIntent(t *testing.T) {
	params := SessionControlModelParams{
		Ref:           "local:th_1",
		ModelProvider: "work",
		Model:         "gpt-5.5",
	}
	assertJSONKeys(t, params, "ref", "modelProvider", "model")

	request := SessionControlRequest{Action: "model", Model: &params}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid Manager model request rejected: %v", err)
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

// An unknown model-control version must not make the thread unreadable. The
// envelope carries the snapshot in typed version-1 fields, so a newer daemon
// whose snapshot members changed shape would otherwise fail the whole decode
// and take the transcript, status and capabilities with it. The version says
// whose shape the snapshot is; a reader that does not know that version can
// still show every other member of the thread and leaves the snapshot out,
// which is what the shared client already does with an unsupported version.
func TestThreadReadKeepsAnUnknownModelControlVersionThreadReadable(t *testing.T) {
	// The version-1 struct reads `revision` as a number. A version-2 payload is
	// free to make it a string, and that conflict is what fails the decode.
	payload := `{"thread":{"id":"t_unknown","evener":{"capabilities":{"modelControlVersion":2},` +
		`"modelControl":{"revision":"7","active":{"target":"openai/gpt-5"}}}}}`

	var response ThreadReadResponse
	if err := decodeResponseResult(MethodThreadRead, json.RawMessage(payload), &response); err != nil {
		t.Fatalf("an unknown model-control version made the thread unreadable: %v", err)
	}
	if response.Thread.ID != "t_unknown" {
		t.Fatalf("thread id = %q, want the read's own thread", response.Thread.ID)
	}
	if got := response.Thread.Evener.Capabilities.ModelControlVersion; got != 2 {
		t.Fatalf("reported model-control version = %d, want the daemon's 2 preserved", got)
	}
	if response.Thread.Evener.ModelControl != nil {
		t.Fatalf("snapshot = %+v, want an unknown version's snapshot left undecoded", response.Thread.Evener.ModelControl)
	}

	// A version-1 payload still has to satisfy the version-1 contract, whatever
	// the thread around it looks like. This one carries every required member,
	// so presence alone accepts it, and its snapshot then fails to decode.
	versionOne := `{"thread":{"id":"t_v1","evener":{"capabilities":{"modelControlVersion":1},` +
		`"modelControl":{"revision":"7","active":{"reasoningEffortLevels":["high"],"supportsReasoning":true}}}}}`
	var malformed ThreadReadResponse
	if err := decodeResponseResult(MethodThreadRead, json.RawMessage(versionOne), &malformed); err == nil {
		t.Fatal("a version-1 read whose snapshot does not decode was accepted")
	}
}

// A newer version may change the shape of a member this build knows, not only
// its scalar types. The presence probe reads the snapshot through version-1
// types, so it has to check the advertised version first: otherwise a shape
// conflict is reported as a malformed read and the thread becomes unreadable,
// which is the outcome the thread's own decode exists to avoid.
func TestThreadReadKeepsANewerVersionWhoseSnapshotShapeConflicts(t *testing.T) {
	// Version 1 decodes `active` as an object. Version 2 restates it as the
	// model's own identifier, so the version-1 shape cannot read it.
	payload := `{"thread":{"id":"t_shape","evener":{"capabilities":{"modelControlVersion":2},` +
		`"modelControl":{"revision":4,"active":"openai/gpt-5"}}}}`

	var response ThreadReadResponse
	if err := decodeResponseResult(MethodThreadRead, json.RawMessage(payload), &response); err != nil {
		t.Fatalf("a newer version's snapshot shape made the thread unreadable: %v", err)
	}
	if response.Thread.ID != "t_shape" {
		t.Fatalf("thread id = %q, want the read's own thread", response.Thread.ID)
	}
	if got := response.Thread.Evener.Capabilities.ModelControlVersion; got != 2 {
		t.Fatalf("reported model-control version = %d, want the daemon's 2 preserved", got)
	}
	if response.Thread.Evener.ModelControl != nil {
		t.Fatalf("snapshot = %+v, want an unknown version's snapshot left undecoded", response.Thread.Evener.ModelControl)
	}
}

// The versioned history read carries the same thread shape and is the route
// clients prefer, so the version-1 contract has to hold there too. Without it a
// daemon's omission decodes as false and can be re-emitted as a supplied value.
func TestHistoryReadRefusesAnIncompleteVersionOneSnapshot(t *testing.T) {
	omitted := `{"thread":{"id":"t_h","evener":{"capabilities":{"modelControlVersion":1},` +
		`"modelControl":{"authority":{"instanceId":"i1","bootGeneration":"b1"},"revision":3,` +
		`"active":{"modelProvider":"openai","model":"gpt-5.4","reasoningEffortLevels":["high"]}}}}}`

	var response ThreadHistoryReadResponse
	err := decodeResponseResult(MethodThreadHistoryRead, json.RawMessage(omitted), &response)
	if err == nil {
		t.Fatal("the history read accepted a version-1 envelope with no supportsReasoning")
	}

	// An explicitly supplied false is the daemon's own statement and survives.
	supplied := `{"thread":{"id":"t_h","evener":{"capabilities":{"modelControlVersion":1},` +
		`"modelControl":{"authority":{"instanceId":"i1","bootGeneration":"b1"},"revision":3,` +
		`"active":{"modelProvider":"openai","model":"gpt-5.4","reasoningEffortLevels":[],"supportsReasoning":false}}}}}`
	var suppliedResponse ThreadHistoryReadResponse
	if err := decodeResponseResult(MethodThreadHistoryRead, json.RawMessage(supplied), &suppliedResponse); err != nil {
		t.Fatalf("an explicit supportsReasoning=false was refused: %v", err)
	}
	snapshot := suppliedResponse.Thread.Evener.ModelControl
	if snapshot == nil || snapshot.Active.SupportsReasoning {
		t.Fatalf("snapshot = %+v, want the daemon's explicit false preserved", snapshot)
	}

	// A history read allocates bound storage for a large frame like any other
	// read, so the refusal has to hold on that path as well: it declines the fast
	// path and leaves the frame for the request's own decode.
	pad := strings.Repeat("x", boundFrameMinSize)
	storage := ThreadHistoryReadResponse{}
	frame := []byte(`{"id":1,"result":` + omitted + `,"pad":"` + pad + `"}`)
	msg, bound, err := decodeFrameBound(frame, func([]byte) any { return &storage })
	if err != nil {
		t.Fatalf("the bound history path reported a receive error for a per-request refusal: %v", err)
	}
	if bound != nil {
		t.Fatalf("bound=%v, want the refused frame to decline the fast path", bound != nil)
	}
	raw, ok := msg.Response.Result.(json.RawMessage)
	if !ok {
		t.Fatalf("Response.Result = %T, want the wire bytes for the request's own decode", msg.Response.Result)
	}
	var delivered ThreadHistoryReadResponse
	if err := decodeResponseResult(MethodThreadHistoryRead, raw, &delivered); err == nil {
		t.Fatal("the bound history path accepted a version-1 envelope with no supportsReasoning")
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
	tests := []struct {
		name   string
		result func(t *testing.T) any
	}{
		{name: "empty", result: func(*testing.T) any { return map[string]any{} }},
		{name: "missing reasoning levels", result: func(t *testing.T) any {
			result := completeModelControlResponseMap(t)
			delete(result["modelControl"].(map[string]any)["active"].(map[string]any), "reasoningEffortLevels")
			return result
		}},
		{name: "missing supports reasoning", result: func(t *testing.T) any {
			result := completeModelControlResponseMap(t)
			delete(result["modelControl"].(map[string]any)["active"].(map[string]any), "supportsReasoning")
			return result
		}},
		{name: "wrong revision type", result: func(t *testing.T) any {
			result := completeModelControlResponseMap(t)
			result["modelControl"].(map[string]any)["revision"] = "nine"
			return result
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := newMemoryTransport()
			client := NewClient(transport)
			ctx := t.Context()
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
			transport.reads <- ResponseMessage(request.Request.ID, test.result(t))

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
		})
	}
}

func TestClientThreadReadRequiresCompleteVersionOneModelControl(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing reasoning levels", mutate: func(result map[string]any) {
			delete(readModelControlActive(result), "reasoningEffortLevels")
		}},
		{name: "missing supports reasoning", mutate: func(result map[string]any) {
			delete(readModelControlActive(result), "supportsReasoning")
		}},
		{name: "wrong supports reasoning type", mutate: func(result map[string]any) {
			readModelControlActive(result)["supportsReasoning"] = "false"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := completeModelControlReadMap(t)
			test.mutate(result)
			if _, err := clientThreadReadResult(t, result); err == nil {
				t.Fatal("incomplete version-one model-control read accepted")
			}
		})
	}
}

func TestClientThreadReadKeepsUnknownModelControlVersionReadable(t *testing.T) {
	result := completeModelControlReadMap(t)
	capabilities := result["thread"].(map[string]any)["evener"].(map[string]any)["capabilities"].(map[string]any)
	capabilities["modelControlVersion"] = float64(2)
	delete(readModelControlActive(result), "reasoningEffortLevels")
	delete(readModelControlActive(result), "supportsReasoning")

	read, err := clientThreadReadResult(t, result)
	if err != nil {
		t.Fatalf("unknown-version read rejected: %v", err)
	}
	if read.Thread.Evener.Capabilities.ModelControlVersion != 2 {
		t.Fatalf("model-control version = %d, want 2", read.Thread.Evener.Capabilities.ModelControlVersion)
	}
}

func clientThreadReadResult(t *testing.T, result any) (ThreadReadResponse, error) {
	t.Helper()
	transport := newMemoryTransport()
	client := NewClient(transport)
	client.Start(t.Context())
	type readResult struct {
		response ThreadReadResponse
		err      error
	}
	done := make(chan readResult, 1)
	go func() {
		response, err := client.ThreadRead(t.Context(), ThreadReadParams{Ref: "local:th_1"})
		done <- readResult{response: response, err: err}
	}()
	request := <-transport.writes
	transport.reads <- ResponseMessage(request.Request.ID, result)
	select {
	case got := <-done:
		return got.response, got.err
	case <-time.After(time.Second):
		t.Fatal("thread read did not settle")
		return ThreadReadResponse{}, nil
	}
}

func completeModelControlReadMap(t *testing.T) map[string]any {
	t.Helper()
	response := completeModelControlResponse()
	read := ThreadReadResponse{Thread: Thread{ID: "th_1", Evener: EvenerThread{
		Ref:          "local:th_1",
		InstanceID:   response.ModelControl.Authority.InstanceID,
		Capabilities: ThreadCapabilities{ModelControlVersion: ModelControlVersion1},
		ModelControl: response.ModelControl,
	}}}
	raw, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func readModelControlActive(result map[string]any) map[string]any {
	return result["thread"].(map[string]any)["evener"].(map[string]any)["modelControl"].(map[string]any)["active"].(map[string]any)
}

func completeModelControlResponseMap(t *testing.T) map[string]any {
	t.Helper()
	raw, err := json.Marshal(completeModelControlResponse())
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
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
