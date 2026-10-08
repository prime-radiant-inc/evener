package agent

import (
	"maps"
	"slices"
	"strings"
	"testing"

	taskpkg "primeradiant.com/evener/agent/task"
)

// The task_list placeholder rule lives in one argument normalizer that both
// dispatch and the failure breaker's fingerprint use (#4002), so a call that
// differs only by a placeholder is the same failing call.
func TestTaskTool_PlaceholderFieldsFingerprintAsOmitted(t *testing.T) {
	t.Parallel()
	for name, placeholder := range map[string]map[string]any{
		"notes null":       {"notes": "null"},
		"notes empty":      {"notes": ""},
		"depends_on empty": {"depends_on": []any{}},
		"depends_on null":  {"depends_on": nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newDependentTaskHarness(t)
			withPlaceholder := map[string]any{"id": 99, "status": "done"}
			maps.Copy(withPlaceholder, placeholder)
			omitted := map[string]any{"id": 99, "status": "done"}
			// Two identical failures park the third call; alternating the
			// placeholder with its omitted form must not reset the run.
			for i, entry := range []map[string]any{withPlaceholder, omitted} {
				if res := h.update(t, entry); !res.IsError || strings.HasPrefix(res.FullOutput, "evener did not execute") {
					t.Fatalf("call %d should fail in the store: %s", i+1, res.FullOutput)
				}
			}
			res := h.update(t, withPlaceholder)
			if !strings.HasPrefix(res.FullOutput, "evener did not execute this call") {
				t.Fatalf("third failing call should be parked as a repeat: %s", res.FullOutput)
			}
		})
	}
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
