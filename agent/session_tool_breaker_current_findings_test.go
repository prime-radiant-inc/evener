package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

type sessionBreakerEventSet struct {
	ends     []events.ToolCallEndData
	repaired []events.ToolCallRepairedData
}

func drainSessionBreakerEvents(sess *Session) <-chan sessionBreakerEventSet {
	out := make(chan sessionBreakerEventSet, 1)
	go func() {
		var got sessionBreakerEventSet
		for ev := range sess.Events() {
			switch data := ev.Data.(type) {
			case events.ToolCallEndData:
				got.ends = append(got.ends, data)
			case events.ToolCallRepairedData:
				got.repaired = append(got.repaired, data)
			}
		}
		out <- got
	}()
	return out
}

func sessionUTF8CollisionCalls(t *testing.T, name string) ([2]llm.ToolCallData, llm.ToolCallData) {
	t.Helper()
	makeInvalid := func(id string, invalid byte) llm.ToolCallData {
		raw := append([]byte(`{"value":"`), invalid)
		raw = append(raw, []byte(`"}`)...)
		return llm.ToolCallData{ID: id, Name: name, Arguments: raw}
	}
	invalid := [2]llm.ToolCallData{makeInvalid("session-invalid-ff", 0xff), makeInvalid("session-invalid-fe", 0xfe)}
	valid := llm.ToolCallData{ID: "session-valid-replacement", Name: name, Arguments: json.RawMessage(`{"value":"�"}`)}
	for _, call := range invalid {
		if utf8.Valid(call.Arguments) {
			t.Fatalf("invalid fixture %q is valid UTF-8: %q", call.ID, call.Arguments)
		}
		if got := bytes.ToValidUTF8(call.Arguments, []byte("\ufffd")); !bytes.Equal(got, valid.Arguments) {
			t.Fatalf("lossy decode for %q = %q, want %q", call.ID, got, valid.Arguments)
		}
	}
	return invalid, valid
}

func registerSessionUTF8CollisionTool(t *testing.T, sess *Session, name string, calls *int) {
	t.Helper()
	if err := sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        name,
			Description: "exercise raw UTF-8 validation before custom tool dispatch",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"value": map[string]any{"type": "string"},
				},
				"required": []any{"value"},
			},
		},
		OmitIntent: true,
		Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			(*calls)++
			return args["value"], nil
		},
	}); err != nil {
		t.Fatalf("register session UTF-8 collision tool: %v", err)
	}
}

func TestSession_InvalidUTF8CannotPoisonLossyEquivalentValidCall(t *testing.T) {
	const name = "session_utf8_collision"
	invalid, valid := sessionUTF8CollisionCalls(t, name)

	runInvalid := func(t *testing.T, callsToRun int, runValid bool) ([]tool.ExecResult, int) {
		t.Helper()
		sess := newSession(t, withoutGitSnapshot())
		sess.stateDir = t.TempDir()
		repairedCh := drainRepairedEvents(sess)
		dispatches := 0
		registerSessionUTF8CollisionTool(t, sess, name, &dispatches)
		results := make([]tool.ExecResult, 0, callsToRun+1)
		for i := range callsToRun {
			res := sess.execTool(context.Background(), invalid[i], "")
			results = append(results, res)
			if !res.IsError || !res.PrevalOnly || !strings.Contains(res.FullOutput, "not valid UTF-8") {
				t.Fatalf("invalid session call %d = %#v, want raw prevalidation failure", i+1, res)
			}
			if strings.Contains(res.Output, "semantic failure loop") {
				t.Fatalf("invalid session call %d parked early: %#v", i+1, res)
			}
			if dispatches != 0 {
				t.Fatalf("invalid session call %d dispatched executor; calls=%d", i+1, dispatches)
			}
		}
		if runValid {
			res := sess.execTool(context.Background(), valid, "")
			results = append(results, res)
			if res.IsError || res.FullOutput != "�" || dispatches != 1 {
				t.Fatalf("valid U+FFFD session call was poisoned: calls=%d result=%#v", dispatches, res)
			}
		}
		sess.Close()
		if repaired := <-repairedCh; len(repaired) != 0 {
			t.Fatalf("raw-validation calls emitted repair telemetry: %+v", repaired)
		}
		return results, dispatches
	}

	results, _ := runInvalid(t, 2, true)
	if results[0].BreakerExactSignature == results[1].BreakerExactSignature {
		t.Fatalf("distinct invalid raw bytes shared exact identity %q", results[0].BreakerExactSignature)
	}
	if results[0].BreakerSemanticSignature != results[1].BreakerSemanticSignature {
		t.Fatalf("invalid UTF-8 marker was not stable in one session: %q != %q", results[0].BreakerSemanticSignature, results[1].BreakerSemanticSignature)
	}
	if results[2].BreakerSemanticSignature == results[0].BreakerSemanticSignature {
		t.Fatalf("valid U+FFFD session call shared invalid-byte semantic identity %q", results[2].BreakerSemanticSignature)
	}
	for i, res := range results {
		if res.BreakerExactSignature == "" || res.BreakerSemanticSignature == "" || len(res.BreakerExactSignature) > 96 || len(res.BreakerSemanticSignature) > 96 {
			t.Fatalf("session result %d has missing or unbounded identities: %#v", i+1, res)
		}
	}

	other, _ := runInvalid(t, 1, false)
	if other[0].BreakerExactSignature == results[0].BreakerExactSignature || other[0].BreakerSemanticSignature == results[0].BreakerSemanticSignature {
		t.Fatalf("breaker identities are not session keyed: first=%#v other=%#v", results[0], other[0])
	}
}

func TestSession_InvalidToolNamesEmitPrivateBoundedBreakerIdentity(t *testing.T) {
	secret := "SESSION_RAW_INVALID_NAME_FRAGMENT"
	invalidA := strings.Repeat(secret, 200) + "/one"
	invalidB := strings.Repeat(secret, 200) + "/two"
	args := json.RawMessage(`{"value":"same"}`)

	run := func(t *testing.T, names []string) ([]tool.ExecResult, sessionBreakerEventSet) {
		t.Helper()
		sess := newSession(t, withoutGitSnapshot())
		sess.stateDir = t.TempDir()
		eventsCh := drainSessionBreakerEvents(sess)
		results := make([]tool.ExecResult, 0, len(names))
		for i, name := range names {
			results = append(results, sess.execTool(context.Background(), llm.ToolCallData{
				ID:        "invalid-session-name-" + string(rune('1'+i)),
				Name:      name,
				Arguments: args,
			}, ""))
		}
		sess.Close()
		return results, <-eventsCh
	}

	results, emitted := run(t, []string{invalidA, invalidA, invalidB, invalidA})
	if strings.Contains(results[0].Output, "did not execute") || strings.Contains(results[1].Output, "did not execute") {
		t.Fatalf("same invalid name parked before its third call: first=%q second=%q", results[0].Output, results[1].Output)
	}
	if strings.Contains(results[2].Output, "You just ran") || strings.Contains(results[2].Output, "did not execute") {
		t.Fatalf("different invalid name inherited breaker history: %#v", results[2])
	}
	if !strings.Contains(results[3].Output, "did not execute") {
		t.Fatalf("third same-name call was not parked: %#v", results[3])
	}
	if len(emitted.ends) != len(results) {
		t.Fatalf("TOOL_CALL_END count = %d, want %d", len(emitted.ends), len(results))
	}
	if len(emitted.repaired) != 0 {
		t.Fatalf("invalid names unexpectedly emitted repair telemetry: %+v", emitted.repaired)
	}
	for i, end := range emitted.ends {
		for _, field := range []struct {
			label string
			value string
		}{
			{"exact", end.BreakerExactSignature},
			{"semantic", end.BreakerSemanticSignature},
		} {
			if field.value == "" || len(field.value) > 98 {
				t.Fatalf("event %d %s identity is missing or unbounded: len=%d value=%q", i+1, field.label, len(field.value), field.value)
			}
			if strings.Contains(field.value, secret) || strings.Contains(field.value, "/one") || strings.Contains(field.value, "/two") {
				t.Fatalf("event %d %s identity leaks invalid name: %q", i+1, field.label, field.value)
			}
		}
	}
	if results[0].BreakerExactSignature != results[1].BreakerExactSignature || results[0].BreakerExactSignature != results[3].BreakerExactSignature || results[0].BreakerSemanticSignature != results[1].BreakerSemanticSignature || results[0].BreakerSemanticSignature != results[3].BreakerSemanticSignature {
		t.Fatalf("same invalid name did not group in one session: %#v", results)
	}
	if results[0].BreakerExactSignature == results[2].BreakerExactSignature || results[0].BreakerSemanticSignature == results[2].BreakerSemanticSignature {
		t.Fatalf("distinct invalid names shared identity: first=%#v other=%#v", results[0], results[2])
	}

	other, otherEvents := run(t, []string{invalidA})
	if other[0].BreakerExactSignature == results[0].BreakerExactSignature || other[0].BreakerSemanticSignature == results[0].BreakerSemanticSignature {
		t.Fatalf("invalid-name breaker tokens are not session keyed: first=%#v other=%#v", results[0], other[0])
	}
	if len(otherEvents.ends) != 1 || otherEvents.ends[0].BreakerExactSignature != other[0].BreakerExactSignature || otherEvents.ends[0].BreakerSemanticSignature != other[0].BreakerSemanticSignature {
		t.Fatalf("other session event did not carry its private breaker identities: results=%#v events=%+v", other, otherEvents.ends)
	}
}

func TestSession_InvalidUTF8PrevalidationEventsUseEncodingBoundary(t *testing.T) {
	const name = "session_utf8_boundary"
	sess := newSession(t, withoutGitSnapshot())
	sess.stateDir = t.TempDir()
	eventsCh := drainSessionBreakerEvents(sess)
	dispatches := 0
	registerSessionUTF8CollisionTool(t, sess, name, &dispatches)

	results := make([]tool.ExecResult, 0, 3)
	for i, invalid := range []byte{0xff, 0xfe, 0xfd} {
		raw := append([]byte(`{"value":"`), invalid)
		raw = append(raw, []byte(`"}`)...)
		results = append(results, sess.execTool(context.Background(), llm.ToolCallData{
			ID:        "session-utf8-boundary-" + string(rune('1'+i)),
			Name:      name,
			Arguments: raw,
		}, ""))
	}
	sess.Close()
	emitted := <-eventsCh

	if dispatches != 0 {
		t.Fatalf("invalid UTF-8 prevalidation calls dispatched executor %d times", dispatches)
	}
	if !strings.Contains(results[1].Output, "normalized failure boundary (arguments_encoding)") {
		t.Fatalf("second invalid UTF-8 failure has wrong session guidance: %q", results[1].Output)
	}
	if !strings.Contains(results[2].Output, "semantic failure loop") || !strings.Contains(results[2].Output, "normalized boundary arguments_encoding") {
		t.Fatalf("third invalid UTF-8 failure did not park at encoding boundary: %q", results[2].Output)
	}
	if len(emitted.ends) != len(results) {
		t.Fatalf("invalid UTF-8 TOOL_CALL_END count = %d, want %d", len(emitted.ends), len(results))
	}
	if len(emitted.repaired) != 0 {
		t.Fatalf("raw invalid UTF-8 unexpectedly emitted repair events: %+v", emitted.repaired)
	}
	for i, end := range emitted.ends {
		if !end.PrevalOnly {
			t.Fatalf("event %d lost its prevalidation-only marker: %+v", i+1, end)
		}
		if i < 2 && !strings.Contains(end.Error, "not valid UTF-8") {
			t.Fatalf("event %d lost its raw validation error: %+v", i+1, end)
		}
		if i == 2 && (!strings.Contains(end.Error, "semantic failure loop") || !strings.Contains(end.Error, "normalized boundary arguments_encoding")) {
			t.Fatalf("parked event lost its argument-encoding guidance: %+v", end)
		}
		if strings.Contains(end.Error, "schema_validation") {
			t.Fatalf("event %d mislabeled invalid UTF-8 as schema validation: %+v", i+1, end)
		}
		if end.BreakerExactSignature == "" || end.BreakerSemanticSignature == "" || len(end.BreakerExactSignature) > 98 || len(end.BreakerSemanticSignature) > 98 {
			t.Fatalf("event %d has missing or unbounded breaker identity: %+v", i+1, end)
		}
	}
}

func TestSession_NormalizedAskFailuresShareSemanticIdentityAndPark(t *testing.T) {
	sess := newSession(t, withoutGitSnapshot())
	sess.stateDir = t.TempDir()
	repairedCh := drainRepairedEvents(sess)

	raw := []json.RawMessage{
		json.RawMessage(`{"question":"Which?","options":[{"label":"Only","detail":"one"}]}`),
		json.RawMessage(`{"questions":[{"options":[{"detail":"one","label":"Only"}],"question":"Which?"}]}`),
		json.RawMessage(`{ "options" : [ { "detail" : "one", "label" : "Only" } ], "multi_select" : false, "question" : "Which?" }`),
	}
	registered := sess.reg.Get("ask_user")
	if registered == nil {
		t.Fatal("interactive root session did not register ask_user")
	}
	var normalizedIdentity string
	rawSet := map[string]bool{}
	for i, arguments := range raw {
		call := llm.ToolCallData{ID: "ask-prepare-" + string(rune('1'+i)), Name: "ask_user", Arguments: arguments}
		prep := prepareToolCall(call, registered, sess.reg.Names(), "ask_user", sess.resultToolName(), "")
		if prep.PrevalErr == "" || prep.Boundary != "schema_validation" {
			t.Fatalf("prepare normalized form %d = %+v, want later schema failure", i+1, prep)
		}
		if len(prep.Changes) != 0 || !bytes.Equal(prep.Call.Arguments, arguments) {
			t.Fatalf("normalized form %d committed failed repair: changes=%+v raw=%q want=%q", i+1, prep.Changes, prep.Call.Arguments, arguments)
		}
		if len(prep.SemanticArguments) == 0 {
			t.Fatalf("normalized form %d did not retain private semantic arguments", i+1)
		}
		if i == 0 {
			normalizedIdentity = string(prep.SemanticArguments)
		} else if string(prep.SemanticArguments) != normalizedIdentity {
			t.Fatalf("normalized form %d semantic bytes = %q, want %q", i+1, prep.SemanticArguments, normalizedIdentity)
		}
		rawSet[string(prep.Call.Arguments)] = true
	}
	if len(rawSet) != len(raw) {
		t.Fatalf("test raw forms are not distinct: %d identities for %d calls", len(rawSet), len(raw))
	}

	results := make([]tool.ExecResult, 0, len(raw))
	for i, arguments := range raw {
		res := sess.execTool(context.Background(), llm.ToolCallData{
			ID:        "ask-session-" + string(rune('1'+i)),
			Name:      "ask_user",
			Arguments: arguments,
		}, "")
		results = append(results, res)
		if !res.IsError || !res.PrevalOnly {
			t.Fatalf("normalized schema failure %d = %#v, want prevalidation error", i+1, res)
		}
		if i < 2 && strings.Contains(res.Output, "semantic failure loop") {
			t.Fatalf("normalized schema failure %d parked early: %#v", i+1, res)
		}
		if sess.askPendingCount() != 0 {
			t.Fatalf("normalized schema failure %d dispatched ask_user", i+1)
		}
	}
	if !strings.Contains(results[2].Output, "semantic failure loop") {
		t.Fatalf("third normalized-equivalent ask_user failure was not parked: %#v", results[2])
	}

	exact := map[string]bool{}
	for i, res := range results {
		exact[res.BreakerExactSignature] = true
		if res.BreakerSemanticSignature != results[0].BreakerSemanticSignature {
			t.Fatalf("normalized schema failure %d semantic identity = %q, want %q", i+1, res.BreakerSemanticSignature, results[0].BreakerSemanticSignature)
		}
		if res.BreakerExactSignature == "" || res.BreakerSemanticSignature == "" || len(res.BreakerExactSignature) > 96 || len(res.BreakerSemanticSignature) > 96 {
			t.Fatalf("normalized schema failure %d has missing or unbounded identities: %#v", i+1, res)
		}
	}
	if len(exact) != len(raw) {
		t.Fatalf("distinct raw ask_user bytes collapsed to %d exact identities, want %d", len(exact), len(raw))
	}

	sess.Close()
	if repaired := <-repairedCh; len(repaired) != 0 {
		t.Fatalf("uncommitted ask_user normalization/schema failures emitted repair telemetry: %+v", repaired)
	}
}
