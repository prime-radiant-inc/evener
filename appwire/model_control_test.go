package appwire

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestModelControlRequestShapes(t *testing.T) {
	legacy := ThreadModelSetParams{
		Ref:           "local:th_1",
		ModelProvider: "work",
		Model:         "gpt-5.5",
	}
	assertJSONKeys(t, legacy, "ref", "modelProvider", "model")

	capable := legacy
	capable.ClientMutationID = "cm_1"
	capable.ExpectedInstanceID = "instance_1"
	capable.ExpectedBootGeneration = "7"
	assertJSONKeys(t, capable,
		"ref", "modelProvider", "model", "clientMutationId", "expectedInstanceId", "expectedBootGeneration")
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

func TestModelControlLegacyResponseDecodesUnsupported(t *testing.T) {
	var response ThreadModelSetResponse
	if err := json.Unmarshal([]byte(`{}`), &response); err != nil {
		t.Fatalf("decode legacy model-set response: %v", err)
	}
	if response.ModelControlVersion != 0 || response.ModelControl != nil || response.Disposition != "" {
		t.Fatalf("legacy model-set response decoded as capable: %+v", response)
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
