package appwire

import "testing"

// TestWireErrorConstructors asserts each constructor's code (against the literal
// JSON-RPC number, so a mutated CodeX constant is caught), serfErrorInfo tag, and
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
		{"Conflict", Conflict("clash"), -32013, ErrorConflict, "clash"},
		{"Unavailable", Unavailable("down"), -32014, ErrorActionUnavailable, "down"},
		{"SessionUnavailable", SessionUnavailable("no sess"), -32014, ErrorSessionUnavailable, "no sess"},
		{"HubLaunchError", HubLaunchError("launch"), -32014, ErrorHubLaunch, "launch"},
		{"QueuedDrainPartial", QueuedDrainPartial("partial"), -32013, ErrorQueuedDrainPartial, "partial"},
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
			if data.SerfErrorInfo != c.wantInfo {
				t.Errorf("SerfErrorInfo = %q, want %q", data.SerfErrorInfo, c.wantInfo)
			}
		})
	}
}
