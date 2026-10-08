package agent

import (
	"context"
	"maps"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// communicateEndReasonProperty returns the end_reason property the session
// advertises on its communicate tool, or nil when it advertises none.
func communicateEndReasonProperty(t *testing.T, defs []llm.ToolDefinition) map[string]any {
	t.Helper()
	for _, def := range defs {
		if def.Name != "communicate" {
			continue
		}
		props, _ := def.Parameters["properties"].(map[string]any)
		prop, _ := props["end_reason"].(map[string]any)
		return prop
	}
	t.Fatal("communicate is not advertised")
	return nil
}

// A root session's communicate offers end_reason; a delegate's keeps the
// tool unchanged, since a delegate's resting state never asks for a person.
func TestCommunicateEndReasonIsOfferedOnlyToRootSessions(t *testing.T) {
	t.Parallel()
	root := newAskTestSession(t, SessionConfig{})
	prop := communicateEndReasonProperty(t, root.ToolDefinitions())
	if prop == nil {
		t.Fatal("a root session's communicate has no end_reason")
	}
	if want := []string{"done", "needs_response", "waiting_on_work"}; !reflect.DeepEqual(prop["enum"], want) {
		t.Fatalf("end_reason enum = %#v, want %#v", prop["enum"], want)
	}

	cfg := SessionConfig{}
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	delegate := newAskTestSession(t, cfg)
	if prop := communicateEndReasonProperty(t, delegate.ToolDefinitions()); prop != nil {
		t.Fatalf("a delegate's communicate offers end_reason: %#v", prop)
	}
	if prop := communicateEndReasonProperty(t, root.profile.ToolDefinitions()); prop != nil {
		t.Fatal("offering end_reason to a root changed the profile's communicate definition")
	}
}

// The end reason a call carries is delivered with its message: the stated one
// on a root's ending call, done when it states none, and nothing on a call
// that keeps the turn going or on a delegate's call.
func TestCommunicateDeliversItsEndReason(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		root    bool
		args    map[string]any
		want    string
		wantErr bool
	}{
		{name: "root states needs_response", root: true, args: map[string]any{"end_turn": true, "end_reason": "needs_response"}, want: "needs_response"},
		{name: "root states waiting_on_work", root: true, args: map[string]any{"end_turn": true, "end_reason": "waiting_on_work"}, want: "waiting_on_work"},
		{name: "root states done", root: true, args: map[string]any{"end_turn": true, "end_reason": "done"}, want: "done"},
		{name: "root states none", root: true, args: map[string]any{"end_turn": true}, want: "done"},
		{name: "root keeps the turn going", root: true, args: map[string]any{"end_turn": false, "end_reason": "needs_response"}, want: ""},
		{name: "root states an unknown reason", root: true, args: map[string]any{"end_turn": true, "end_reason": "later"}, wantErr: true},
		{name: "delegate ends its turn", root: false, args: map[string]any{"end_turn": true}, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var delivered []events.CommunicateData
			deps := &toolDeps{
				emit: func(events.EventKind, events.EventData) {},
				deliverCommunicate: func(data events.CommunicateData) error {
					delivered = append(delivered, data)
					return nil
				},
				abort:                  func(context.Context) error { return nil },
				drainSteering:          func() []steeringMessage { return nil },
				prependSteering:        func([]steeringMessage) {},
				resultToolName:         func() string { return "communicate" },
				setCommunicateTerminal: func(context.Context, string, string, string, any) bool { return true },
				offersEndReason:        tc.root,
			}
			reg := tool.NewRegistry()
			registerCommunicateTool(reg, deps)
			args := map[string]any{"message": "report"}
			maps.Copy(args, tc.args)
			res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", args))
			if tc.wantErr {
				if !res.IsError || len(delivered) != 0 {
					t.Fatalf("result = %+v, delivered = %+v; want a refusal that delivers nothing", res, delivered)
				}
				return
			}
			if res.IsError {
				t.Fatalf("exec: %s", res.Output)
			}
			if len(delivered) != 1 || delivered[0].EndReason != tc.want {
				t.Fatalf("delivered = %+v, want one message with end reason %q", delivered, tc.want)
			}
		})
	}
}

// The transcript keeps the end reason with the message it ended the turn on.
func TestCommunicateRecordsItsEndReason(t *testing.T) {
	s, adapter := newExecutionSession(t)
	adapter.script(respond(toolCallResponse(communicateCallArgs("comm-1", map[string]any{"message": "pick one", "end_reason": "needs_response"}))))
	if _, err := s.ProcessInput(context.Background(), "talk", nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	communicates := entriesOfKind(transcriptTurnsOf(t, s), schema.TurnCommunicate)
	if len(communicates) != 1 {
		t.Fatalf("%d communicate entries, want 1", len(communicates))
	}
	if got := communicates[0].Communicate.EndReason; got != "needs_response" {
		t.Fatalf("recorded end reason = %q, want needs_response", got)
	}
}
