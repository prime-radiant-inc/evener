package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

const parkedMarker = "evener did not execute this call"

// thirdCallParked runs a, b, then a again through reg, each expected to fail
// in its executor with wantFailure. The breaker parks the third call only when
// a and b share a failure fingerprint, so the result says whether they do.
func thirdCallParked(t *testing.T, reg *tool.Registry, name, a, b, wantFailure string) bool {
	t.Helper()
	exec := func(args string) tool.ExecResult {
		return reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{ID: "c", Name: name, Arguments: json.RawMessage(args)})
	}
	for i, args := range []string{a, b} {
		res := exec(args)
		if !res.IsError || !strings.Contains(res.FullOutput, wantFailure) {
			t.Fatalf("call %d (%s) should fail with %q: %s", i+1, args, wantFailure, res.FullOutput)
		}
	}
	return strings.HasPrefix(exec(a).FullOutput, parkedMarker)
}

func alwaysFails(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
	return nil, errors.New("boom")
}

// An update entry made only of placeholders changes nothing, so it is skipped
// and the rest of the call still applies.
func TestTaskTool_PlaceholderOnlyEntrySkipped(t *testing.T) {
	t.Parallel()
	h := newDependentTaskHarness(t)
	res := h.call(t, map[string]any{
		"update": []any{
			map[string]any{"id": 2, "depends_on": []any{}},
			map[string]any{"id": 1, "status": "in_progress"},
		},
		"add": []any{map[string]any{"type": "verify", "description": "third", "prompt": "p3"}},
	})
	if res.IsError {
		t.Fatalf("siblings of a placeholder-only entry must apply: %s", res.FullOutput)
	}
	if got := taskByID(t, h.store, 1).Status; got != taskpkg.TaskInProgress {
		t.Fatalf("task 1 status = %q, want in_progress", got)
	}
	if got := taskByID(t, h.store, 2).DependsOn; !slices.Equal(got, []int{1}) {
		t.Fatalf("task 2 depends_on = %v, want [1] kept", got)
	}
	if len(h.store.View()) != 3 {
		t.Fatalf("the add should apply: %+v", h.store.View())
	}
}

// A call whose every entry is placeholders still fails, saying the
// placeholders were ignored and how to clear dependencies.
func TestTaskTool_AllPlaceholderUpdateRejectedWithExplanation(t *testing.T) {
	t.Parallel()
	h := newDependentTaskHarness(t)
	res := h.update(t,
		map[string]any{"id": 2, "depends_on": []any{}},
		map[string]any{"id": 1, "notes": "null"},
	)
	if !res.IsError {
		t.Fatalf("an update of only placeholders must be rejected: %s", res.FullOutput)
	}
	for _, want := range []string{"ignored", "[0]"} {
		if !strings.Contains(res.FullOutput, want) {
			t.Fatalf("error should mention %q: %s", want, res.FullOutput)
		}
	}
	if got := taskByID(t, h.store, 2).DependsOn; !slices.Equal(got, []int{1}) {
		t.Fatalf("rejected call changed depends_on: %v", got)
	}
}

// An update made only of placeholders fails in the tool, so the breaker
// records it under the normalized fingerprint: repeating it with different
// placeholders still parks the third call.
func TestTaskTool_RepeatedPlaceholderOnlyUpdateIsParked(t *testing.T) {
	t.Parallel()
	h := newDependentTaskHarness(t)
	for i, entry := range []map[string]any{
		{"id": 2, "depends_on": []any{}},
		{"id": 2, "depends_on": nil},
	} {
		res := h.update(t, entry)
		if !res.IsError || strings.HasPrefix(res.FullOutput, parkedMarker) || !strings.Contains(res.FullOutput, "[0]") {
			t.Fatalf("call %d should fail in the tool and explain [0]: %s", i+1, res.FullOutput)
		}
	}
	if res := h.update(t, map[string]any{"id": 2, "notes": ""}); !strings.HasPrefix(res.FullOutput, parkedMarker) {
		t.Fatalf("third placeholder-only update should be parked: %s", res.FullOutput)
	}
}

// A present add of the wrong type is not treated as absent: its own type
// error surfaces.
func TestTaskTool_WrongTypedAddSurfacesBesidePlaceholderUpdate(t *testing.T) {
	t.Parallel()
	h := newDependentTaskHarness(t)
	res := h.call(t, map[string]any{
		"add":    "oops",
		"update": []any{map[string]any{"id": 2, "depends_on": []any{}}},
	})
	if !res.IsError || !strings.Contains(res.FullOutput, "/add") {
		t.Fatalf("the add type error should surface: %s", res.FullOutput)
	}
}

// Only placeholders fold. [0] clears, add entries have no placeholder
// contract, and a tool without task_list's normalizer folds nothing.
func TestTaskTool_NonPlaceholderFieldsKeepTheirFingerprint(t *testing.T) {
	t.Parallel()
	h := newDependentTaskHarness(t)
	if thirdCallParked(t, h.reg, "task_list",
		`{"update":[{"id":99,"status":"done","depends_on":[0]}]}`,
		`{"update":[{"id":99,"status":"done"}]}`,
		"unknown task ID 99") {
		t.Error("depends_on: [0] must not fingerprint as omitted")
	}
	if thirdCallParked(t, h.reg, "task_list",
		`{"add":[{"type":"fix","description":"a","prompt":"p","depends_on":[99]},{"type":"fix","description":"b","prompt":"p","depends_on":[]}]}`,
		`{"add":[{"type":"fix","description":"a","prompt":"p","depends_on":[99]},{"type":"fix","description":"b","prompt":"p"}]}`,
		"unknown task 99") {
		t.Error("an add entry's depends_on: [] must keep its own fingerprint")
	}
	other := tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: "other_tool", Description: "test tool", Parameters: map[string]any{"type": "object"}},
		Exec:       alwaysFails,
	}
	if err := h.reg.Register(other); err != nil {
		t.Fatalf("register: %v", err)
	}
	if thirdCallParked(t, h.reg, "other_tool",
		`{"update":[{"id":99,"depends_on":[]}]}`,
		`{"update":[{"id":99}]}`,
		"boom") {
		t.Error("another tool's depends_on: [] must not fold")
	}
}

// A placeholder-only entry is dropped only when its id is a real integer;
// otherwise it stays, so validation rejects the whole call and no sibling
// add commits.
func TestTaskTool_PlaceholderEntryWithBadIDRejectsTheCall(t *testing.T) {
	t.Parallel()
	for name, id := range map[string]any{"string": "bad", "zero": 0, "negative": -1, "beyond int": 1e20, "fraction": 1.5, "max int64": int64(math.MaxInt64)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newDependentTaskHarness(t)
			res := h.call(t, map[string]any{
				"add":    []any{map[string]any{"type": "verify", "description": "third", "prompt": "p3"}},
				"update": []any{map[string]any{"id": id, "notes": ""}},
			})
			if !res.IsError {
				t.Fatalf("an update with id %v must fail the call: %s", id, res.FullOutput)
			}
			if n := len(h.store.View()); n != 2 {
				t.Fatalf("the sibling add committed: %d tasks", n)
			}
		})
	}
}

func TestTaskIDValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   any
		want int
		ok   bool
	}{
		{float64(3), 3, true},
		{int64(3), 3, true},
		{json.Number("3"), 3, true},
		{float64(0), 0, false},
		{float64(-1), 0, false},
		{float64(1.5), 0, false},
		{float64(1e20), 0, false},
		{int64(0), 0, false},
		// One bound on every path: ids below 2^53. JSON 2^53+1 decodes to
		// float64 2^53, so accepting 2^53 would let dispatch take an id the
		// fingerprint's int64 view rejects.
		{float64(1<<53 - 1), 1<<53 - 1, true},
		{int64(1<<53 - 1), 1<<53 - 1, true},
		{json.Number("9007199254740991"), 1<<53 - 1, true},
		{float64(1 << 53), 0, false},
		{int64(1 << 53), 0, false},
		{float64(1<<53 + 2), 0, false},
		{int64(1<<53 + 1), 0, false},
		{int64(math.MaxInt64), 0, false},
		{json.Number("100000000000000000000"), 0, false},
		{json.Number("1.5"), 0, false},
		{"3", 0, false},
		{nil, 0, false},
	} {
		got, ok := taskIDValue(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("taskIDValue(%#v) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// decodeTaskArgs applies the placeholder rule itself, so a direct caller gets
// the same reading as dispatch.
func TestDecodeTaskArgs_NormalizesPlaceholders(t *testing.T) {
	t.Parallel()
	_, updates, err := decodeTaskArgs(map[string]any{"update": []any{
		map[string]any{"id": float64(2), "status": "done", "depends_on": []any{}, "notes": "null"},
	}})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(updates) != 1 || updates[0].DependsOn != nil || updates[0].Notes != "" {
		t.Fatalf("placeholders should be no change, got %+v", updates)
	}
}

// When every update entry is dropped beside real adds, the update key goes
// too, so the call is the add-only call.
func TestNormalizeTaskListArgs_DropsEmptiedUpdateKey(t *testing.T) {
	t.Parallel()
	add := []any{map[string]any{"type": "verify", "description": "third", "prompt": "p3"}}
	normalized, err := normalizeTaskListArgs(map[string]any{
		"add":    add,
		"update": []any{map[string]any{"id": float64(2), "depends_on": []any{}}},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if _, has := normalized["update"]; has {
		t.Fatalf("emptied update should be removed: %#v", normalized)
	}
}

// read_transcript's NormalizeArgs folds exactly the neutral fields it removes
// for job refs; other present values keep their own fingerprint.
func TestReadTranscriptFingerprintFoldsOnlyNeutralJobDefaults(t *testing.T) {
	t.Parallel()
	newReg := func(t *testing.T) *tool.Registry {
		t.Helper()
		reg := tool.NewRegistry()
		readTranscript := readTranscriptTool(&toolDeps{})
		readTranscript.Exec = alwaysFails
		if err := reg.Register(readTranscript); err != nil {
			t.Fatalf("register: %v", err)
		}
		return reg
	}
	const job = `{"transcript_ref":"job:j1"}`
	for _, folded := range []string{
		`{"transcript_ref":"job:j1","range":""}`,
		`{"transcript_ref":"job:j1","format":null}`,
		`{"transcript_ref":"job:j1","context_lines":0}`,
	} {
		if !thirdCallParked(t, newReg(t), "read_transcript", folded, job, "boom") {
			t.Errorf("%s should fingerprint as %s", folded, job)
		}
	}
	for _, distinct := range [][2]string{
		{`{"transcript_ref":"job:j1","offset_bytes":0}`, job},
		{`{"transcript_ref":"current","range":""}`, `{"transcript_ref":"current"}`},
	} {
		if thirdCallParked(t, newReg(t), "read_transcript", distinct[0], distinct[1], "boom") {
			t.Errorf("%s must keep its own fingerprint", distinct[0])
		}
	}
}
