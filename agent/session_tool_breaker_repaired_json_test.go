package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/llm"
)

// A successful syntax repair is not committed when the repaired arguments still
// fail the tool schema. It must nevertheless supply the private breaker identity:
// otherwise every distinct malformed document collapses to the raw invalid-JSON
// marker and the third unrelated schema failure is falsely parked.
func TestSession_RepairedJSONSchemaFailuresKeepDistinctSemanticIdentity(t *testing.T) {
	const name = "repaired_json_schema_failure"
	sess := newSession(t, withoutGitSnapshot())
	sess.stateDir = t.TempDir()
	eventsCh := drainSessionBreakerEvents(sess)
	dispatches := 0
	sess.RegisterTool(name, "exercise repaired JSON identity before schema rejection", map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"target": map[string]any{"type": "string"},
			"count":  map[string]any{"type": "integer"},
		},
		"required": []any{"target", "count"},
	}, func(context.Context, any) (any, error) {
		dispatches++
		return "unexpected dispatch", nil
	})

	raw := []json.RawMessage{
		json.RawMessage(`{"target":"alpha\uX"}`),
		json.RawMessage(`{"target":"beta\uX"}`),
		json.RawMessage(`{"target":"gamma\uX"}`),
	}
	for i, arguments := range raw {
		if json.Valid(arguments) {
			t.Fatalf("fixture %d is valid JSON: %q", i+1, arguments)
		}
		repaired, changes := repair.RepairJSON(arguments)
		if !json.Valid(repaired) || len(changes) == 0 {
			t.Fatalf("fixture %d is not deterministically repairable: repaired=%q changes=%+v", i+1, repaired, changes)
		}
		var args map[string]any
		if err := json.Unmarshal(repaired, &args); err != nil || args["target"] != []string{"alpha�X", "beta�X", "gamma�X"}[i] {
			t.Fatalf("fixture %d repaired semantics = %#v, err=%v", i+1, args, err)
		}
	}

	results := make([]tool.ExecResult, 0, len(raw))
	for i, arguments := range raw {
		results = append(results, sess.execTool(context.Background(), llm.ToolCallData{
			ID:        fmt.Sprintf("repaired-json-%d", i+1),
			Name:      name,
			Arguments: arguments,
		}, ""))
	}
	sess.Close()
	emitted := <-eventsCh

	if dispatches != 0 {
		t.Fatalf("schema-rejected calls dispatched executor %d times", dispatches)
	}
	if len(emitted.ends) != len(raw) {
		t.Fatalf("TOOL_CALL_END count = %d, want %d", len(emitted.ends), len(raw))
	}
	if len(emitted.repaired) != 0 {
		t.Fatalf("uncommitted JSON repairs emitted telemetry: %+v", emitted.repaired)
	}

	exact, semantic := map[string]bool{}, map[string]bool{}
	for i, res := range results {
		if !res.IsError || !res.PrevalOnly {
			t.Fatalf("call %d = %#v, want prevalidation schema failure", i+1, res)
		}
		if strings.Contains(res.Output, "semantic failure loop") {
			t.Errorf("distinct repaired call %d was falsely parked: %q", i+1, res.Output)
		}
		if !strings.Contains(res.FullOutput, "arguments did not match the schema") || !strings.Contains(res.FullOutput, "count (integer)") {
			t.Errorf("call %d lost schema error: %q", i+1, res.FullOutput)
		}
		if res.BreakerExactSignature == "" || res.BreakerSemanticSignature == "" || len(res.BreakerExactSignature) > 96 || len(res.BreakerSemanticSignature) > 96 {
			t.Fatalf("call %d has missing or unbounded breaker identity: %#v", i+1, res)
		}
		exact[res.BreakerExactSignature] = true
		semantic[res.BreakerSemanticSignature] = true

		end := emitted.ends[i]
		if !end.PrevalOnly || end.ArgumentsJSON != string(raw[i]) {
			t.Fatalf("event %d did not retain the rejected raw arguments: %+v", i+1, end)
		}
		if end.BreakerExactSignature != res.BreakerExactSignature || end.BreakerSemanticSignature != res.BreakerSemanticSignature {
			t.Fatalf("event %d breaker identities differ from result: event=%+v result=%#v", i+1, end, res)
		}
		for label, value := range map[string]string{"exact": res.BreakerExactSignature, "semantic": res.BreakerSemanticSignature} {
			if bytes.Contains([]byte(value), raw[i]) || strings.Contains(value, []string{"alpha", "beta", "gamma"}[i]) {
				t.Fatalf("call %d %s identity leaked arguments: %q", i+1, label, value)
			}
		}
	}
	if len(exact) != len(raw) || len(semantic) != len(raw) {
		t.Fatalf("distinct repaired calls produced exact/semantic identities %d/%d, want %d/%d", len(exact), len(semantic), len(raw), len(raw))
	}
}
