package appwire

import (
	"encoding/json"
	"testing"
)

func TestManagerCreatePostSpawnFailurePreservesOriginalData(t *testing.T) {
	original := WireError{Code: CodeConflict, Message: "read confirmation failed", Data: struct {
		ErrorData
		Detail string `json:"detail"`
	}{ErrorData: ErrorData{EvenerErrorInfo: ErrorConflict, ClientMutationID: "original-id", Cause: "ownerRead", RetryDisposition: RetryDispositionAutomatic}, Detail: "retained"}}
	partial := original.WithThreadStartPartial("local:created", "instance-1")
	if partial.Code != original.Code || partial.Message != original.Message {
		t.Fatalf("original envelope changed: %+v", partial)
	}
	if _, ok := partial.Data.(ThreadStartPartialErrorData); !ok {
		t.Fatalf("partial data is %T, want typed partial data", partial.Data)
	}
	encoded, err := json.Marshal(partial.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		ThreadStartPartialErrorData
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(encoded, &data); err != nil {
		t.Fatal(err)
	}
	if data.EvenerErrorInfo != ErrorConflict || data.ClientMutationID != "original-id" || data.Cause != "ownerRead" || data.Detail != "retained" || data.RetryDisposition != RetryDispositionBlocked || data.CreatedRef != "local:created" || data.CreatedInstanceID != "instance-1" {
		t.Fatalf("classified partial data: %s", encoded)
	}
	if RetrySafeMutationMethod(MethodThreadStart) {
		t.Fatal("thread/start must not be an automatic retry-safe mutation")
	}
}
