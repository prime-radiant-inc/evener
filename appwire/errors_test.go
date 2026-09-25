package appwire

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestInvalidHostFieldCarriesTheBlamedInput pins the shape a dialog reads: the
// standard validation code and prose, with evenerErrorInfo naming the refusal
// kind and data.field naming the input in the wire's own spelling.
func TestInvalidHostFieldCarriesTheBlamedInput(t *testing.T) {
	err := InvalidHostField("address", `host "m4": missing ssh destination`)
	if err.Code != CodeInvalidParams {
		t.Fatalf("Code = %d, want CodeInvalidParams", err.Code)
	}
	data, ok := err.Data.(HostFieldErrorData)
	if !ok {
		t.Fatalf("Data = %T, want HostFieldErrorData", err.Data)
	}
	if data.EvenerErrorInfo != ErrorInvalidHostField || data.Field != "address" {
		t.Fatalf("data = %+v, want the invalidHostField discriminant and field address", data)
	}
	raw, err2 := json.Marshal(err)
	if err2 != nil {
		t.Fatalf("Marshal: %v", err2)
	}
	for _, want := range []string{`"evenerErrorInfo":"invalidHostField"`, `"field":"address"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("wire error = %s, want it to contain %s", raw, want)
		}
	}
	// A refusal that blames the entry as a whole carries no field at all, so a
	// client never looks for an input that does not exist.
	whole := InvalidHostField("", "host cycle")
	rawWhole, err3 := json.Marshal(whole)
	if err3 != nil {
		t.Fatalf("Marshal: %v", err3)
	}
	if strings.Contains(string(rawWhole), `"field"`) {
		t.Fatalf("wire error = %s, want no field for a form-level refusal", rawWhole)
	}
}

// TestWireErrorConstructors asserts each constructor's code (against the literal
// JSON-RPC number, so a mutated CodeX constant is caught), evenerErrorInfo tag, and
// message. errors.go's constructors had no in-package unit coverage.
func TestWireErrorConstructors(t *testing.T) {
	for _, c := range []struct {
		name     string
		err      WireError
		wantCode int
		wantInfo ErrorInfo
		wantMsg  string
	}{
		{"InvalidParams", InvalidParams("bad p"), -32602, ErrorInvalidParams, "bad p"},
		{"InvalidRequest", InvalidRequest("bad r"), -32600, ErrorInvalidParams, "bad r"},
		{"MethodNotFound", MethodNotFound("foo"), -32601, ErrorMethodNotFound, "method not found: foo"},
		{"InternalError", InternalError("boom"), -32603, ErrorInternal, "boom"},
		{"ResourceNotFound", ResourceNotFound("pin section not found"), -32602, ErrorResourceNotFound, "pin section not found"},
		{"Conflict", Conflict("clash"), -32013, ErrorConflict, "clash"},
		{"Unavailable", Unavailable("down"), -32014, ErrorActionUnavailable, "down"},
		{"SessionUnavailable", SessionUnavailable("no sess"), -32014, ErrorSessionUnavailable, "no sess"},
		{"HubLaunchError", HubLaunchError("launch"), -32014, ErrorHubLaunch, "launch"},
		{"QueuedDrainPartial", QueuedDrainPartial("partial"), -32013, ErrorQueuedDrainPartial, "partial"},
		{"InstanceRenamePersisted", InstanceRenamePersisted("leftover"), -32603, ErrorInstanceRenamePersisted, "leftover"},
		{"InstanceRemoveApplied", InstanceRemoveApplied("leftover"), -32603, ErrorInstanceRemoveApplied, "leftover"},
		{"EndpointConflict", EndpointConflict("moved"), -32013, ErrorEndpointConflict, "moved"},
		{"HistoryFailed", HistoryFailed(7), -32603, ErrorHistoryFailed, "thread history failed at entry 7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.err.Code != c.wantCode {
				t.Errorf("Code = %d, want %d", c.err.Code, c.wantCode)
			}
			if c.err.Message != c.wantMsg {
				t.Errorf("Message = %q, want %q", c.err.Message, c.wantMsg)
			}
			if c.err.Error() != c.wantMsg {
				t.Errorf("Error() = %q, want %q", c.err.Error(), c.wantMsg)
			}
			data, ok := c.err.Data.(ErrorData)
			if !ok {
				t.Fatalf("Data = %T, want ErrorData", c.err.Data)
			}
			if data.EvenerErrorInfo != c.wantInfo {
				t.Errorf("EvenerErrorInfo = %q, want %q", data.EvenerErrorInfo, c.wantInfo)
			}
		})
	}
}

func TestTranscriptItemCursorError(t *testing.T) {
	const opaqueCursor = "opaque-cursor-bytes-MUST-NOT-LEAK"
	err := TranscriptItemCursorStale()
	if err.Code != CodeInvalidParams {
		t.Fatalf("Code = %d, want %d", err.Code, CodeInvalidParams)
	}
	if err.Message != "transcript item cursor is stale; refresh the thread" {
		t.Fatalf("Message = %q", err.Message)
	}
	data, ok := err.Data.(ErrorData)
	if !ok {
		t.Fatalf("Data = %T, want ErrorData", err.Data)
	}
	if data.EvenerErrorInfo != ErrorTranscriptItemCursorStale {
		t.Fatalf("EvenerErrorInfo = %q, want %q", data.EvenerErrorInfo, ErrorTranscriptItemCursorStale)
	}
	if data.RetryDisposition != RetryDispositionAutomatic {
		t.Fatalf("RetryDisposition = %q, want %q", data.RetryDisposition, RetryDispositionAutomatic)
	}
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatalf("marshal WireError: %v", marshalErr)
	}
	if bytes.Contains(encoded, []byte(opaqueCursor)) {
		t.Fatalf("serialized WireError echoes opaque cursor bytes: %s", encoded)
	}
	mutant := err
	mutant.Data = map[string]any{"evenerErrorInfo": ErrorTranscriptItemCursorStale, "cursor": opaqueCursor}
	mutated, marshalErr := json.Marshal(mutant)
	if marshalErr != nil {
		t.Fatalf("marshal mutated WireError: %v", marshalErr)
	}
	if !bytes.Contains(mutated, []byte(opaqueCursor)) {
		t.Fatalf("serialized-byte assertion is not mutation-sensitive: %s", mutated)
	}
}
