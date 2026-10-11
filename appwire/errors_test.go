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
		{"TranscriptHistoryFailed", TranscriptHistoryFailed(7), -32603, ErrorTranscriptHistoryFailed, "thread history failed at entry 7"},
		{"UpgradeRequired", UpgradeRequired("evener-appwire-v5", "evener-appwire-v6"), -32600, ErrorUpgradeRequired, "protocol version \"evener-appwire-v5\" is older than this server's \"evener-appwire-v6\": upgrade required"},
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

// A refusal marked not accepted keeps its code, message and category, and says
// the request wasn't carried out and isn't to be retried.
func TestNotAcceptedMarksARefusalKeepingWhatItSaid(t *testing.T) {
	marked := InvalidParams("cwd is not a directory").NotAccepted("mutation-1")
	data, ok := marked.Data.(ErrorData)
	if !ok {
		t.Fatalf("data = %#v, want ErrorData", marked.Data)
	}
	if marked.Code != CodeInvalidParams || marked.Message != "cwd is not a directory" || data.EvenerErrorInfo != ErrorInvalidParams {
		t.Fatalf("marked = %+v, want the refusal's own code, message and category", marked)
	}
	want := ErrorData{
		EvenerErrorInfo:  ErrorInvalidParams,
		ClientMutationID: "mutation-1",
		MutationOutcome:  MutationOutcomeNotAccepted,
		RetryDisposition: RetryDispositionNone,
	}
	if data != want {
		t.Fatalf("data = %+v, want %+v", data, want)
	}
	// Data of another shape gives way to the standard data, so the outcome is
	// always readable.
	other := WireError{Code: CodeInternalError, Message: "boom", Data: map[string]string{"x": "y"}}.NotAccepted("")
	if data, _ := other.Data.(ErrorData); data.MutationOutcome != MutationOutcomeNotAccepted {
		t.Fatalf("data = %#v, want the not-accepted outcome", other.Data)
	}
}

// A refusal whose data wraps the standard data keeps the wrapper: its
// category and its own fields stay, with the outcome marked on the data it
// embeds.
func TestNotAcceptedKeepsWrappedData(t *testing.T) {
	marked := InvalidHostField("hostname", "hostname is taken").NotAccepted("mutation-2")
	data, ok := marked.Data.(HostFieldErrorData)
	if !ok {
		t.Fatalf("data = %#v, want HostFieldErrorData kept", marked.Data)
	}
	want := HostFieldErrorData{
		EvenerErrorInfo:  ErrorInvalidHostField,
		ClientMutationID: "mutation-2",
		MutationOutcome:  MutationOutcomeNotAccepted,
		RetryDisposition: RetryDispositionNone,
		Field:            "hostname",
	}
	if data != want {
		t.Fatalf("data = %+v, want %+v", data, want)
	}
	// On the wire the fields sit side by side, as a client reads them.
	raw, err := json.Marshal(marked.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"evenerErrorInfo":"invalidHostField"`, `"field":"hostname"`, `"mutationOutcome":"notAccepted"`} {
		if !bytes.Contains(raw, []byte(part)) {
			t.Fatalf("wire data %s lacks %s", raw, part)
		}
	}
	// The error it was marked from is left as it was.
	original := InvalidHostField("hostname", "hostname is taken")
	_ = original.NotAccepted("x")
	if data := original.Data.(HostFieldErrorData); data.MutationOutcome != "" {
		t.Fatalf("original data = %+v, want it unmarked", data)
	}

	lifecycle := WireError{Code: CodeUnavailable, Message: "stopping", Data: LifecycleErrorData{
		ErrorData:       ErrorData{EvenerErrorInfo: ErrorSessionUnavailable},
		LifecycleReason: "stopping",
		Retryable:       true,
	}}.NotAccepted("")
	if data, ok := lifecycle.Data.(LifecycleErrorData); !ok || data.LifecycleReason != "stopping" ||
		data.EvenerErrorInfo != ErrorSessionUnavailable || data.MutationOutcome != MutationOutcomeNotAccepted {
		t.Fatalf("data = %#v, want the lifecycle data kept and marked", lifecycle.Data)
	}
}

// The standard data is read from plain or wrapped error data alike, and from
// nothing else.
func TestErrorDataOfReadsPlainAndWrappedData(t *testing.T) {
	if data, ok := ErrorDataOf(InvalidParams("bad").Data); !ok || data.EvenerErrorInfo != ErrorInvalidParams {
		t.Fatalf("plain = %+v, %v; want its ErrorData", data, ok)
	}
	if data, ok := ErrorDataOf(InvalidHostField("hostname", "taken").Data); !ok || data.EvenerErrorInfo != ErrorInvalidHostField {
		t.Fatalf("wrapped = %+v, %v; want the ErrorData it embeds", data, ok)
	}
	for _, other := range []any{nil, map[string]any{"evenerErrorInfo": "x"}, struct{ Name string }{"x"}} {
		if data, ok := ErrorDataOf(other); ok {
			t.Fatalf("ErrorDataOf(%#v) = %+v, want none", other, data)
		}
	}
}
