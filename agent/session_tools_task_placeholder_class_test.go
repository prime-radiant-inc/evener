package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"testing"

	taskpkg "primeradiant.com/evener/agent/task"
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

// failingUpdateBase is an update entry that fails in the store (unknown task)
// and does not use field, so the placeholder is the only difference.
func failingUpdateBase(field string) map[string]any {
	if field == "notes" {
		return map[string]any{"id": 99, "status": "done"}
	}
	return map[string]any{"id": 99, "notes": "real note"}
}

func updateCallJSON(t *testing.T, entry map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"update": []any{entry}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func TestTaskTool_EveryUpdatePlaceholderFingerprintsAsOmitted(t *testing.T) {
	t.Parallel()
	for _, tc := range updatePlaceholderCases {
		t.Run(fmt.Sprintf("%s=%#v", tc.field, tc.value), func(t *testing.T) {
			t.Parallel()
			h := newDependentTaskHarness(t)
			omitted := failingUpdateBase(tc.field)
			with := maps.Clone(omitted)
			with[tc.field] = tc.value
			if !thirdCallParked(t, h.reg, "task_list", updateCallJSON(t, with), updateCallJSON(t, omitted), "unknown task ID 99") {
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

// Values that change something keep their own fingerprint and apply:
// reasoning_effort "none" and "null" both turn thinking off.
func TestTaskTool_MeaningfulEffortIsNotAPlaceholder(t *testing.T) {
	t.Parallel()
	for _, effort := range []string{"none", "null", "high"} {
		t.Run(effort, func(t *testing.T) {
			t.Parallel()
			h := newDependentTaskHarness(t)
			omitted := failingUpdateBase("reasoning_effort")
			with := maps.Clone(omitted)
			with["reasoning_effort"] = effort
			if thirdCallParked(t, h.reg, "task_list", updateCallJSON(t, with), updateCallJSON(t, omitted), "unknown task ID 99") {
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
