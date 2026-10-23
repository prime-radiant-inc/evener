package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func delegateDispositionRegistry(t *testing.T, delegate, offersEndReason bool) (*tool.Registry, *[]events.CommunicateData) {
	t.Helper()
	delivered := []events.CommunicateData{}
	record := func(data events.CommunicateData) error {
		delivered = append(delivered, data)
		return nil
	}
	deps := &toolDeps{
		emit:               func(events.EventKind, events.EventData) {},
		deliverCommunicate: record,
		abort:              func(context.Context) error { return nil },
		drainSteering:      func() []steeringMessage { return nil },
		prependSteering:    func([]steeringMessage) {},
		resultToolName:     func() string { return "communicate" },
		setCommunicateTerminal: func(_ context.Context, info schema.CommunicateInfo, _, _ string, _ any) (bool, error) {
			return true, record(events.CommunicateData{CallID: info.CallID, EndTurn: info.EndTurn, Message: info.Message, EndReason: info.EndReason, Blocker: info.Blocker, WaitingOn: info.WaitingOn})
		},
		offersEndReason: offersEndReason,
		delegate:        delegate,
	}
	reg := tool.NewRegistry()
	if err := registerCommunicateTool(reg, deps); err != nil {
		t.Fatalf("register communicate: %v", err)
	}
	return reg, &delivered
}

func assertInvalidParams(t *testing.T, res tool.ExecResult) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("result = %+v, want an invalid-params refusal", res)
	}
	var wire appwire.WireError
	if !errors.As(res.Err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %T %v, want appwire invalid-params", res.Err, res.Err)
	}
}

func TestDelegateCommunicateOffersOnlyDelegateDispositions(t *testing.T) {
	def := tool.WithDelegateCommunicateEndReason(tool.DefCommunicate())
	props, _ := def.Parameters["properties"].(map[string]any)
	reason, _ := props["end_reason"].(map[string]any)
	if reason == nil {
		t.Fatal("delegate communicate has no end_reason")
	}
	if want := []string{"done", "blocked", "waiting_on_work"}; !reflect.DeepEqual(reason["enum"], want) {
		t.Fatalf("delegate end_reason enum = %#v, want %#v", reason["enum"], want)
	}
	for _, name := range []string{"blocker", "waiting_on"} {
		prop, _ := props[name].(map[string]any)
		if prop == nil || prop["type"] != "string" {
			t.Fatalf("delegate communicate %q = %#v, want a string property", name, props[name])
		}
	}
}

func TestDelegateCommunicateRejectsNeedsResponse(t *testing.T) {
	reg, delivered := delegateDispositionRegistry(t, true, false)
	res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", map[string]any{
		"message":    "cannot go on",
		"end_turn":   true,
		"end_reason": "needs_response",
	}))
	if !res.IsError {
		t.Fatalf("result = %+v, want needs_response refused", res)
	}
	if len(*delivered) != 0 {
		t.Fatalf("delivered = %+v, want nothing delivered on a refusal", *delivered)
	}
}

func TestDelegateCommunicateDefaultsTerminalReasonToDone(t *testing.T) {
	reg, delivered := delegateDispositionRegistry(t, true, false)
	res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", map[string]any{
		"message":  "finished",
		"end_turn": true,
	}))
	if res.IsError {
		t.Fatalf("execute: %s", res.Output)
	}
	if len(*delivered) != 1 || (*delivered)[0].EndReason != "done" {
		t.Fatalf("delivered = %+v, want one message with end reason done", *delivered)
	}
}

func TestDelegateCommunicateRequiresBlockerAndWaitTarget(t *testing.T) {
	for _, tc := range []struct {
		name          string
		args          map[string]any
		wantErr       bool
		wantBlocker   string
		wantWaitingOn string
	}{
		{name: "blocked without blocker", args: map[string]any{"end_reason": "blocked"}, wantErr: true},
		{name: "waiting without target", args: map[string]any{"end_reason": "waiting_on_work"}, wantErr: true},
		{name: "blocked with blocker", args: map[string]any{"end_reason": "blocked", "blocker": "repository access"}, wantBlocker: "repository access"},
		{name: "waiting with target", args: map[string]any{"end_reason": "waiting_on_work", "waiting_on": "job-1"}, wantWaitingOn: "job-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, delivered := delegateDispositionRegistry(t, true, false)
			args := map[string]any{"message": "report", "end_turn": true}
			maps.Copy(args, tc.args)
			res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", args))
			if tc.wantErr {
				assertInvalidParams(t, res)
				if len(*delivered) != 0 {
					t.Fatalf("delivered = %+v, want nothing delivered on a refusal", *delivered)
				}
				return
			}
			if res.IsError {
				t.Fatalf("execute: %s", res.Output)
			}
			if len(*delivered) != 1 {
				t.Fatalf("delivered = %+v, want one message", *delivered)
			}
			if got := (*delivered)[0].Blocker; got != tc.wantBlocker {
				t.Fatalf("blocker = %q, want %q", got, tc.wantBlocker)
			}
			if got := (*delivered)[0].WaitingOn; got != tc.wantWaitingOn {
				t.Fatalf("waiting_on = %q, want %q", got, tc.wantWaitingOn)
			}
		})
	}

	t.Run("a root rejects delegate targets", func(t *testing.T) {
		reg, delivered := delegateDispositionRegistry(t, false, true)
		res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", map[string]any{
			"message":  "report",
			"end_turn": true,
			"blocker":  "not a delegate",
		}))
		if !res.IsError {
			t.Fatalf("result = %+v, want a root's blocker refused", res)
		}
		if len(*delivered) != 0 {
			t.Fatalf("delivered = %+v, want nothing delivered on a refusal", *delivered)
		}
	})
}

func TestDelegateBlockedAndWaitingDoNotRequireOutput(t *testing.T) {
	rawCall := func(args map[string]any) llm.ToolCallData {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal call: %v", err)
		}
		return llm.ToolCallData{ID: "comm-1", Name: "communicate", Arguments: raw, Type: "function"}
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "blocked", args: map[string]any{"message": "blocked", "end_turn": true, "end_reason": "blocked", "blocker": "access"}},
		{name: "waiting", args: map[string]any{"message": "waiting", "end_turn": true, "end_reason": "waiting_on_work", "waiting_on": "job-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, delivered := delegateDispositionRegistry(t, true, false)
			res := reg.ExecuteCall(context.Background(), nil, rawCall(tc.args))
			if res.IsError {
				t.Fatalf("%s without output: %s", tc.name, res.Output)
			}
			if len(*delivered) != 1 {
				t.Fatalf("delivered = %+v, want one report", *delivered)
			}
		})
	}

	reg, _ := delegateDispositionRegistry(t, true, false)
	res := reg.ExecuteCall(context.Background(), nil, rawCall(map[string]any{
		"message": "done", "end_turn": true, "end_reason": "done",
	}))
	if !res.IsError {
		t.Fatalf("done without output was accepted: %+v", res)
	}
}

func TestDelegateDispositionWorksWithClosedResultSchema(t *testing.T) {
	closed := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"message": map[string]any{"type": "string"},
			"data":    map[string]any{"type": "object"},
			"artifacts": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
		"required": []string{"message", "data", "artifacts"},
	}
	def := tool.WithDelegateCommunicateEndReason(tool.DefCommunicate())
	props, _ := def.Parameters["properties"].(map[string]any)
	props["output"] = closed
	if got := props["output"]; !reflect.DeepEqual(got, closed) {
		t.Fatalf("output schema = %#v, want it unchanged by the disposition properties", got)
	}

	reg := tool.NewRegistry()
	if err := reg.Register(tool.RegisteredTool{
		Definition: def,
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return "ok", nil
		},
	}); err != nil {
		t.Fatalf("register communicate: %v", err)
	}

	res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", map[string]any{
		"message":    "report",
		"end_turn":   true,
		"end_reason": "waiting_on_work",
		"waiting_on": "job-1",
		"output":     map[string]any{"message": "report"},
	}))
	if res.IsError {
		t.Fatalf("closed schema rejected a disposition: %s", res.Output)
	}

	res = reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-2", map[string]any{
		"message":  "report",
		"end_turn": true,
		"output":   map[string]any{"message": "report", "extra": true},
	}))
	if !res.IsError {
		t.Fatalf("result = %+v, want the closed output schema to reject an extra property", res)
	}
}
