package repair

import (
	"strings"
	"testing"
)

// taskListParams mirrors DefTaskList's parameters (this package must stay
// dependency-free of agent/internal/tool, so the fixture is hand-built rather
// than calling the real Def* function). tasks is the append-branch array,
// updates the update-branch array; the real schema selects between them by
// the action value in prose, not by a combinator — the handler enforces it.
// The "For <action>:" description tags are what the branch detection keys on.
// taskListParams is a SYNTHETIC action-enum schema shaped like the pre-rework
// DefTaskList, kept because the branch machinery it exercises (actionTag
// parsing, namedBranch enum membership, action-scoped arrays) still serves
// manage_worktree and any MCP tool carrying a selector; task_list itself is
// presence-based now and cannot produce these failures.
func taskListParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"view", "append", "update"},
			},
			"tasks": map[string]any{
				"type":        "array",
				"description": "For append: tasks to add. Each has a type, brief description (<10 words), a detailed prompt, and optional reasoning_effort.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"type":             map[string]any{"type": "string", "enum": []string{"research", "implement", "verify", "fix"}},
						"description":      map[string]any{"type": "string"},
						"prompt":           map[string]any{"type": "string"},
						"reasoning_effort": map[string]any{"type": "string"},
					},
					"required": []string{"type", "description", "prompt"},
				},
			},
			"updates": map[string]any{
				"type":        "array",
				"description": "For update: list of {id, status} pairs with optional notes.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":     map[string]any{"type": "integer"},
						"status": map[string]any{"type": "string", "enum": []string{"open", "in_progress", "done", "cancelled"}},
						"notes":  map[string]any{"type": "string"},
					},
					"required": []string{"id", "status"},
				},
			},
		},
		"required": []string{"action"},
	}
}

// taskListUpdateArgs mirrors the exact failing call shape from live session
// 034FsgXdyimiBvbubPlB4w (issue #626): action "update" with the update
// entries placed in the append-branch "tasks" array. Schema validation fails
// inside tasks[0] ("missing required type/description/prompt"), and the
// explanation must name the branch that produced the requirement and show the
// shape the caller's action actually uses — not the bare append-branch
// required-list, which reads to an update caller as describing its own call.
func taskListUpdateArgs() map[string]any {
	return map[string]any{
		"action": "update",
		"tasks": []any{map[string]any{
			"id":               float64(1),
			"status":           "in_progress",
			"notes":            "",
			"reasoning_effort": "inherit",
		}},
	}
}

// Issue #626: a conditional sub-schema failure (tasks[0] applies to
// action:"append") must not be presented as a plain missing-argument error.
// The old message — missing required argument "type" in tasks[0] + the
// append-branch required list + a top-level-only Example — sends an update
// caller into an unrecoverable loop: following it (adding type/description/
// prompt to tasks[0]) still fails, this time with the handler's "update
// requires a non-empty 'updates' array", and the two errors alternate without
// either ever stating the actual fix. The golden pins the full machine
// contract: the branch attribution, the update-branch Example, and the
// pointer to the array the caller's action actually takes.
func TestExplainSchemaError_TaskListUpdateCallerGolden(t *testing.T) {
	got := ExplainSchemaError("task_list", taskListParams(), taskListUpdateArgs(), "tasks/0", "")
	want := "task_list: missing required argument \"type\" in tasks[0].\n" +
		"Required arguments in tasks[0] for action \"append\": type (string), description (string), prompt (string).\n" +
		"Example: {\"action\": \"update\", \"updates\": [{\"id\": 0, \"status\": \"...\"}]} " +
		"Your action \"update\" takes \"updates\" (sent: tasks)."
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The reverse misplacement (append caller sending the update array) gets the
// mirror-image guidance: the update-branch requirement is attributed to
// action "update", and the caller is pointed at "tasks".
func TestExplainSchemaError_TaskListAppendCallerGolden(t *testing.T) {
	args := map[string]any{
		"action":  "append",
		"updates": []any{map[string]any{"id": float64(1)}},
	}
	got := ExplainSchemaError("task_list", taskListParams(), args, "updates/0", "")
	want := "task_list: missing required argument \"status\" in updates[0].\n" +
		"Required arguments in updates[0] for action \"update\": id (integer), status (string).\n" +
		"Example: {\"action\": \"append\", \"tasks\": [{\"description\": \"...\", \"prompt\": \"...\", \"type\": \"...\"}]} " +
		"Your action \"append\" takes \"tasks\" (sent: updates)."
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Issue #626 complaint 3 (round 2): a same-branch caller — correct usage,
// append with a task missing prompt — must get the append-shaped Example,
// not the action-only template that gives no usable branch context.
func TestExplainSchemaError_SameBranchCallerGetsBranchExample(t *testing.T) {
	args := map[string]any{
		"action": "append",
		"tasks": []any{map[string]any{
			"type":        "implement",
			"description": "d",
		}},
	}
	got := ExplainSchemaError("task_list", taskListParams(), args, "tasks/0", "")
	want := "task_list: missing required argument \"prompt\" in tasks[0].\n" +
		"Required arguments in tasks[0]: type (string), description (string), prompt (string).\n" +
		"Example: {\"action\": \"append\", \"tasks\": [{\"description\": \"...\", \"prompt\": \"...\", \"type\": \"...\"}]}"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Issue #626 round 2, finding 1: a present field inside the wrong branch's
// array (a type error, not a missing field) must also carry the branch
// attribution — the early return on the present-field path otherwise enters
// the same unrecoverable loop one step later.
func TestExplainSchemaError_PresentFieldInWrongBranchNamesBranch(t *testing.T) {
	args := map[string]any{
		"action": "update",
		"tasks": []any{map[string]any{
			"type":        123,
			"description": "d",
			"prompt":      "p",
		}},
	}
	got := ExplainSchemaError("task_list", taskListParams(), args, "tasks/0/type", "type")
	want := "task_list: argument \"tasks[0].type\" has the wrong type or value.\n" +
		"Example: {\"action\": \"update\", \"updates\": [{\"id\": 0, \"status\": \"...\"}]} " +
		"(this failure is in the array for action \"append\"; your action \"update\" takes \"updates\", not tasks)"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A constraint-keyword message on the present-field path (enum violation)
// inside the wrong branch's array carries the branch tail too.
func TestExplainSchemaError_ConstraintKeywordInWrongBranchGolden(t *testing.T) {
	args := map[string]any{
		"action": "update",
		"tasks": []any{map[string]any{
			"type":        "bogus",
			"description": "d",
			"prompt":      "p",
		}},
	}
	got := ExplainSchemaError("task_list", taskListParams(), args, "tasks/0/type", "enum")
	want := "task_list: argument \"tasks[0].type\" is not one of the allowed values: research, implement, verify, fix. Value is \"bogus\". " +
		"(this failure is in the array for action \"append\"; your action \"update\" takes \"updates\", not tasks)"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A top-level tagged array supplied with the wrong type (a string instead of
// an array) still gets the caller's own branch shape in the Example.
func TestExplainSchemaError_TopLevelTaggedArrayWrongTypeGolden(t *testing.T) {
	args := map[string]any{"action": "update", "tasks": "oops"}
	got := ExplainSchemaError("task_list", taskListParams(), args, "tasks", "type")
	want := "task_list: argument \"tasks\" has the wrong type or value.\n" +
		"Example: {\"action\": \"update\", \"updates\": [{\"id\": 0, \"status\": \"...\"}]}"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Both action-scoped arrays sent: the failure still names the branch it
// came from on both the missing-field and present-field paths (round 3
// alignment — the two paths previously disagreed, with the present-field
// path dropping the attribution entirely when the correct array was also
// sent).
func TestExplainSchemaError_BothArraysSentStillNamesBranch(t *testing.T) {
	both := map[string]any{
		"action":  "update",
		"tasks":   []any{map[string]any{"id": 1, "status": "in_progress"}},
		"updates": []any{map[string]any{"id": 2, "status": "done"}},
	}
	missingPath := ExplainSchemaError("task_list", taskListParams(), both, "tasks/0", "")
	if !strings.Contains(missingPath, `for action "append"`) {
		t.Fatalf("missing-field path dropped branch attribution when both arrays were sent: %q", missingPath)
	}
	// Present-field variant: type error in the wrong branch's array.
	present := map[string]any{
		"action":  "update",
		"tasks":   []any{map[string]any{"type": 123, "description": "d", "prompt": "p"}},
		"updates": []any{map[string]any{"id": 2, "status": "done"}},
	}
	presentPath := ExplainSchemaError("task_list", taskListParams(), present, "tasks/0/type", "type")
	if !strings.Contains(presentPath, `array for action "append"`) {
		t.Fatalf("present-field path dropped branch attribution when both arrays were sent: %q", presentPath)
	}
}

// A prose "For X: ..." description whose X is not a member of the selector's
// enum must not be treated as a branch tag (read_file's "For large files read
// in slices: ..." style on an array-tagged property). The message falls back
// to the generic form with no branch attribution.
func TestExplainSchemaError_ProseTagOutsideEnumIsNotBranch(t *testing.T) {
	params := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"view", "append", "update"}},
			"slices": map[string]any{
				"type":        "array",
				"description": "For large files read in slices: line ranges.",
				"items": map[string]any{
					"type":       "object",
					"properties": map[string]any{"start": map[string]any{"type": "integer"}},
					"required":   []string{"start"},
				},
			},
		},
		"required": []string{"action"},
	}
	args := map[string]any{"action": "view", "slices": []any{map[string]any{}}}
	got := ExplainSchemaError("read_file", params, args, "slices/0", "")
	// The guard this pins: the parsed tag "large files read in slices" is not
	// in the selector's enum, so it must NOT be named as a branch. This
	// assertion fails if the enum-membership check in namedBranch is removed
	// (verified by experiment: disabling the guard produces branch
	// attribution here).
	if strings.Contains(got, "large files read in slices") {
		t.Fatalf("prose tag outside the selector enum was treated as a branch: %q", got)
	}
	if strings.Contains(got, "for action") {
		t.Fatalf("message must not attribute a branch when the tag is not an enum member: %q", got)
	}
	want := "read_file: missing required argument \"start\" in slices[0].\n" +
		"Required arguments in slices[0]: start (integer).\n" +
		"Example: {\"slices\": [{\"start\": 0}]}"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
