package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// updatePlaceholderCases lists, per optional update field, values whose
// effect is identical to omitting the field.
var updatePlaceholderCases = []struct {
	field string
	value any
}{
	{"status", ""},
	{"status", nil},
	{"notes", ""},
	{"notes", "null"},
	{"notes", " NULL "},
	{"notes", nil},
	{"depends_on", []any{}},
	{"depends_on", nil},
	{"reasoning_effort", ""},
	{"reasoning_effort", "  "},
	{"reasoning_effort", "inherit"},
	{"reasoning_effort", " Inherit "},
	{"reasoning_effort", nil},
}

// fingerprintsAsOmitted reports whether an update setting field to value
// shares a failure fingerprint with the same update leaving field out. The
// base entry fails in the store (unknown task) and does not use field, so
// value is the only difference.
func fingerprintsAsOmitted(t *testing.T, reg *tool.Registry, field string, value any) bool {
	t.Helper()
	omitted := map[string]any{"id": 99, "notes": "real note"}
	if field == "notes" {
		omitted = map[string]any{"id": 99, "status": "done"}
	}
	with := maps.Clone(omitted)
	with[field] = value
	call := func(entry map[string]any) string { return mustJSON(t, map[string]any{"update": []any{entry}}) }
	return thirdCallParked(t, reg, "task_list", call(with), call(omitted), "unknown task ID 99")
}

func TestTaskTool_EveryUpdatePlaceholderFingerprintsAsOmitted(t *testing.T) {
	t.Parallel()
	for _, tc := range updatePlaceholderCases {
		t.Run(fmt.Sprintf("%s=%#v", tc.field, tc.value), func(t *testing.T) {
			t.Parallel()
			if !fingerprintsAsOmitted(t, newDependentTaskHarness(t).reg, tc.field, tc.value) {
				t.Errorf("%s: %#v should fingerprint as the omitted field", tc.field, tc.value)
			}
		})
	}
}

func TestTaskTool_EveryPlaceholderOnlyEntryLetsSiblingApply(t *testing.T) {
	t.Parallel()
	for _, tc := range updatePlaceholderCases {
		t.Run(fmt.Sprintf("%s=%#v", tc.field, tc.value), func(t *testing.T) {
			t.Parallel()
			h := newDependentTaskHarness(t)
			res := h.update(t,
				map[string]any{"id": 2, tc.field: tc.value},
				map[string]any{"id": 1, "status": "in_progress"},
			)
			if res.IsError {
				t.Fatalf("the sibling update should apply: %s", res.FullOutput)
			}
			if got := taskByID(t, h.store, 1).Status; got != taskpkg.TaskInProgress {
				t.Fatalf("task 1 status = %q, want in_progress", got)
			}
		})
	}
}

// A live session prepares a call (schema gate, argument repair) before
// dispatch. Placeholders the schema rejects, or that enum repair strips to a
// bare id, must still be skipped there, so the real sibling applies.
func TestTaskTool_PreparedPlaceholderOnlyEntriesLetSiblingApply(t *testing.T) {
	t.Parallel()
	for name, placeholders := range map[string][]any{
		"notes null":        {map[string]any{"id": 2, "notes": nil}},
		"status empty":      {map[string]any{"id": 2, "status": ""}},
		"status null":       {map[string]any{"id": 2, "status": nil}},
		"effort null":       {map[string]any{"id": 2, "reasoning_effort": nil}},
		"notes and status":  {map[string]any{"id": 2, "notes": nil, "status": ""}},
		"two placeholders":  {map[string]any{"id": 2, "notes": nil}, map[string]any{"id": 2, "status": ""}},
		"depends_on null":   {map[string]any{"id": 2, "depends_on": nil}},
		"effort whitespace": {map[string]any{"id": 2, "reasoning_effort": "  "}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newDependentTaskHarness(t)
			updates := append(slices.Clone(placeholders), map[string]any{"id": 1, "status": "in_progress"})
			registered := h.reg.Get("task_list")
			prepared := prepareToolCall(llm.ToolCallData{ID: "c", Name: "task_list", Arguments: json.RawMessage(mustJSON(t, map[string]any{"update": updates}))},
				registered, []string{"task_list"}, "task_list", "communicate", "")
			if prepared.PrevalErr != "" {
				t.Fatalf("preparation rejected the call: %s", prepared.PrevalErr)
			}
			res := h.reg.ExecutePreparedCall(context.Background(), nil, prepared.Call)
			if res.IsError {
				t.Fatalf("the sibling update should apply: %s", res.FullOutput)
			}
			if got := taskByID(t, h.store, 1).Status; got != taskpkg.TaskInProgress {
				t.Fatalf("task 1 status = %q, want in_progress", got)
			}
		})
	}
}

// Values that change something keep their own fingerprint and apply:
// reasoning_effort "none" and "null" both turn thinking off.
func TestTaskTool_MeaningfulEffortIsNotAPlaceholder(t *testing.T) {
	t.Parallel()
	for _, effort := range []string{"none", "null", "high"} {
		t.Run(effort, func(t *testing.T) {
			t.Parallel()
			h := newDependentTaskHarness(t)
			if fingerprintsAsOmitted(t, h.reg, "reasoning_effort", effort) {
				t.Errorf("reasoning_effort %q must keep its own fingerprint", effort)
			}
			if res := h.update(t, map[string]any{"id": 1, "reasoning_effort": effort}); res.IsError {
				t.Fatalf("update: %s", res.FullOutput)
			}
			want := effort
			if effort == "null" {
				want = "none"
			}
			if got := taskByID(t, h.store, 1).ReasoningEffort; got != want {
				t.Fatalf("reasoning_effort = %q, want %q", got, want)
			}
		})
	}
}

// The placeholder table covers every optional update field, so a field
// added to the update shape cannot miss it.
func TestUpdatePlaceholdersCoverEveryOptionalField(t *testing.T) {
	t.Parallel()
	optional := slices.DeleteFunc(slices.Clone(taskListUpdateFields), func(field string) bool { return field == "id" })
	slices.Sort(optional)
	if tabled := slices.Sorted(maps.Keys(updatePlaceholders)); !slices.Equal(optional, tabled) {
		t.Fatalf("update fields %v, placeholder table %v", optional, tabled)
	}
}

// Every depends_on element is a task id within the same bound as an update
// id, so a fractional or out-of-range number is rejected, not truncated.
func TestDecodeTaskArgs_DependsOnElementsAreTaskIDs(t *testing.T) {
	t.Parallel()
	for _, bad := range []any{1.5, 1e20, -1.0, float64(1 << 53)} {
		for _, kind := range []string{"add", "update"} {
			t.Run(fmt.Sprintf("%s %v", kind, bad), func(t *testing.T) {
				t.Parallel()
				entry := map[string]any{"type": "fix", "description": "d", "prompt": "p", "depends_on": []any{bad}}
				if kind == "update" {
					entry = map[string]any{"id": float64(2), "depends_on": []any{bad}}
				}
				_, _, err := decodeTaskArgs(map[string]any{kind: []any{entry}})
				if err == nil || !strings.Contains(err.Error(), "depends_on") {
					t.Fatalf("depends_on [%v] should be rejected, got %v", bad, err)
				}
			})
		}
	}
}
