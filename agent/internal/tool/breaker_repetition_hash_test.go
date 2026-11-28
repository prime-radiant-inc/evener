package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// registerRepeatingTool registers a tool that always returns body, so the
// breaker sees a byte-identical result on every dispatch.
func registerRepeatingTool(t *testing.T, name, body string) *Registry {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        name,
			Description: "returns a fixed body",
			Parameters:  map[string]any{"type": "object"},
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return body, nil
		},
	}); err != nil {
		t.Fatalf("Register(%s): %v", name, err)
	}
	return reg
}

// TestBreakerRepeatedIdenticalOutputsTripTheRepetitionNudge pins the guarantee
// the maphash fingerprint still has to carry: the N-th byte-identical successful
// result trips the repetition nudge. That nudge is the only thing standing
// between a model looping on one tool call and a runaway turn, so it must survive
// a change of hash function.
func TestBreakerRepeatedIdenticalOutputsTripTheRepetitionNudge(t *testing.T) {
	const body = "the same tool result, byte for byte"
	reg := registerRepeatingTool(t, "repeats", body)
	call := llm.ToolCallData{Name: "repeats", ID: "id", Arguments: json.RawMessage(`{"path":"same"}`)}

	for i := 1; i < breakerThreshold; i++ {
		res := reg.ExecuteCall(context.Background(), nil, call)
		if res.IsError || res.Rejected {
			t.Fatalf("run %d = %+v, want a clean execution", i, res)
		}
		if strings.Contains(res.Output, "repeated") {
			t.Fatalf("run %d carried a repetition nudge before the threshold: %q", i, res.Output)
		}
	}
	res := reg.ExecuteCall(context.Background(), nil, call)
	if res.IsError || res.Rejected {
		t.Fatalf("threshold run = %+v, want a clean execution", res)
	}
	if !strings.Contains(res.Output, repetitionNudgeText(breakerThreshold, DefaultResultToolName)) {
		t.Fatalf("threshold run output = %q, want it to carry the repetition nudge", res.Output)
	}
}

// TestBreakerDistinctOutputsDoNotTripTheRepetitionNudge is the other half: a
// model making progress must not be nudged for repetition. Two distinct bodies
// under the same call signature reset the streak, so no nudge appears however
// many times the call runs.
func TestBreakerDistinctOutputsDoNotTripTheRepetitionNudge(t *testing.T) {
	reg := NewRegistry()
	var body string
	if err := reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        "advances",
			Description: "returns a different body each call",
			Parameters:  map[string]any{"type": "object"},
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return body, nil
		},
	}); err != nil {
		t.Fatalf("Register(advances): %v", err)
	}
	call := llm.ToolCallData{Name: "advances", ID: "id", Arguments: json.RawMessage(`{"path":"same"}`)}
	for i := range 2 * breakerThreshold {
		body = "result number " + strings.Repeat("x", i+1)
		res := reg.ExecuteCall(context.Background(), nil, call)
		if strings.Contains(res.Output, "repeated") {
			t.Fatalf("run %d with a distinct body carried a repetition nudge: %q", i, res.Output)
		}
	}
}

// TestRepetitionHashDistinguishesByteIdenticalBodies is the property the streak
// depends on, asserted directly so a hash change cannot silently make every body
// look equal: identical input hashes identically, one differing byte does not.
func TestRepetitionHashDistinguishesByteIdenticalBodies(t *testing.T) {
	a := hashResultBody("some tool output")
	if a != hashResultBody("some tool output") {
		t.Fatal("identical bodies hashed differently, so repetition would never be detected")
	}
	for _, other := range []string{
		"",
		"some tool outpu",
		"some tool output ",
		"Some tool output",
		"some tool output\n",
	} {
		if hashResultBody(other) == a {
			t.Fatalf("a body differing only as %q hashed the same, so distinct results would read as repetition", other)
		}
	}
}
