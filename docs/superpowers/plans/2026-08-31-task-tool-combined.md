# Combined Task Tool (presence-based dispatch) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace task_list's action-enum branching with presence-based dispatch — optional `add` and `update` arrays, no `action` parameter, every response returns the full task list — and fix the status-optional bug it exposed.

**Architecture:** One tool, one `MutateAndPublish` closure per call. Update entries are validated against pre-existing IDs (before adds apply), adds apply, then updates. Empty status means "no change." The tool name `task_list`, the store, the events payloads, the steering machinery, and the repair branch heuristics all stay; only the wire schema and handler dispatch change.

**Tech Stack:** Go (module `primeradiant.com/evener`), JSON Schema validation via the existing `tool.Registry` prevalidation, no new dependencies.

**Spec:** This document is the spec (design + plan). Companion evidence: the review delivered in this session (issue #626 failure family, the `"<nil>"` status bug, strict-mode force-requiring).

## Global Constraints

- Gates: `make lint`, `make vet`, `make test` must pass. Default tests must be deterministic (no network, no credentials, no ambient state).
- Keep the tool name `task_list` — it is load-bearing: registry output limits (`agent/internal/tool/registry.go:923`), subagent allowlists (`agent/subagents.go:258`), reminders (`agent/task_reminders.go`), profile effort-enum sync (`agent/provider/profile.go:542` — `setEffortLevels` regenerates `DefTaskList` by name), transcript conventions (`agent/transcript/transcript.go:65`), session round tracking (`agent/session.go:630-632`).
- Preserve the events payload shapes: `events.EventTaskUpdated` (`TaskStateData`/`TaskUpdatedData`) and `tool.StateResult.State` (the taskToolState snapshot). The hub UI renders these.
- Preserve all steering behavior: current-task steering on manual start, auto-advance steering when a completion leaves nothing in progress, all-done/delegates steering, reminders (nudge/inactivity/all-done).
- Do not modify the repair machinery's generic branch heuristics in `agent/internal/tool/repair/explain.go` — `manage_worktree` still uses the pattern and old persisted calls still reference action-shaped schemas. Deleting it is a separate follow-up once the corpus ages out.
- Set `Strict: false` explicitly on DefTaskList (six precedents in definitions.go). The OpenAI Responses adapter defaults strict=true when unset and force-requires every nested property — which would force `"status": ""` (enum violation) or `"depends_on": []` (silent dep clearing) on strict-mode models. With Strict:false, empty arrays are simply never-required no-op channels.
- Old persisted action-shaped calls (e.g. `{"action":"view"}`) must fail with a helpful error, not crash.
- reasoning_effort keeps the `"inherit"` sentinel semantics (`normalizeTaskEffort`).
- Staged commits use explicit file paths only — never `git add -A` or `git add .`.

## Design

### New wire schema (DefTaskList)

```jsonc
{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "add": {
      "type": "array",
      "description": "Tasks to add. Omit or [] for none. Each: type, brief description (<10 words), detailed prompt, optional depends_on (IDs of existing tasks), optional reasoning_effort.",
      "items": {
        "type": "object",
        "properties": {
          "type":            { "type": "string", "enum": ["research","implement","verify","fix"], "description": "Task type. Use 'fix' for targeted remediation after a specific failure or review finding." },
          "description":     { "type": "string" },
          "prompt":          { "type": "string" },
          "depends_on":      { "type": "array", "items": { "type": "integer" }, "description": "IDs of existing tasks (or earlier tasks in this same add array, which get sequential IDs) this one depends on. Optional." },
          "reasoning_effort": reasoningSchema
        },
        "required": ["type", "description", "prompt"]
      }
    },
    "update": {
      "type": "array",
      "description": "Changes to existing tasks. Omit or [] for none. Each: id plus optional status, notes, depends_on, or reasoning_effort.",
      "items": {
        "type": "object",
        "properties": {
          "id":              { "type": "integer" },
          "status":          { "type": "string", "enum": ["open","in_progress","done","cancelled"] },
          "notes":           { "type": "string", "description": "Document what you tried and why it failed or succeeded. Appended to the task's notes log." },
          "depends_on":      { "type": "array", "items": { "type": "integer" }, "description": "Set dependencies. [] clears them. Omit to leave unchanged." },
          "reasoning_effort": reasoningSchema
        },
        "required": ["id"]
      }
    }
  }
  // no top-level "required" — a bare {} is a valid view
}
```

`reasoningSchema` construction is unchanged from today (string, description, effortLevels enum + "inherit" sentinel when levels are known). Top-level `required` is deliberately absent: a bare call (or empty arrays) is the view/search operation, answered by the always-returned list. `additionalProperties: false` at top level means an `action` key is rejected at schema validation with an error the repair layer can explain — that is the back-compat failure mode for old-shaped calls.

### Tool description (new)

```
Manage your task list. A bare call (or empty add/update) returns the current list. Use add to append new tasks (type, brief description <10 words, detailed prompt, optional depends_on/reasoning_effort) and update to change existing tasks (id plus optional status, notes, depends_on, reasoning_effort) — both in the same call when useful. When you mark a task done, the next eligible task auto-starts and its prompt is injected. Use depends_on to express ordering and notes to record what happened. Only one task may be in_progress at a time; to start a new one, complete or defer the current one in the same update array.
```

### Handler semantics (agent/session_tools_task.go)

Single `mutateAndPublishTaskStore` closure per call:

1. **Decode** `add` and `update` arrays via Go type assertions into `[]taskpkg.TaskInput` / `[]taskpkg.TaskUpdate`, strictly — wrong types produce clear errors; no `fmt.Sprint` coercion. Missing arrays → nil slices. An `update` item that sets none of status/notes/depends_on/reasoning_effort → error: `update entry for task %d changes nothing; include status, notes, depends_on, or reasoning_effort`. An `add` item missing type/description/prompt fails schema validation (they're required in the item schema).
2. **Validate update IDs against the pre-add store state** (inside the mutation closure, before `store.Append`): any `update` id not in the current store → `unknown task ID %d`, nothing applies. Rationale: the model composed the call before any new IDs existed, so updates can never legitimately target this call's adds. (The store's own `updateLocked` re-validates IDs after the adds apply; the pre-check gives the error before any mutation, keeping the whole call atomic on failure.)
3. **Apply** inside the same closure: `store.Append(adds)` then `store.UpdateWithSnapshot(updates)`. If either errors, the publication is abandoned — one revision, one `EventTaskUpdated`. Add `depends_on` semantics are the store's existing ones, unchanged: an add's deps may reference existing tasks and same-batch adds (the store assigns sequential IDs and validates against existing+pending; the model sees the current list with IDs, so same-batch references remain possible exactly as they are today via `append`). Update `depends_on` may only reference pre-existing tasks (an update entry validated against the pre-add state cannot name this call's adds).
4. **Response output contract** (preserves the existing token-economy decision — session_tools_task.go's "terse acknowledgement" comment): mutations return terse acks + `State`; a bare call (or the no-array view) returns `formatTaskList`. The list is NOT appended to mutation acks — the current-task is announced via steering, exactly as today, and `State` carries the snapshot for the hub UI. Search is free because a bare call is the view.
   - Only adds: `Added N task(s). Progress: d/t tasks complete.` + `State: store.View()`
   - Only updates: `Updated <formatTaskUpdates>. Progress: d/t tasks complete.` + `State: taskToolStateSnapshot(...)` — plus auto-advance/all-done text per current rules
   - Both: `Added N task(s); updated <formatTaskUpdates>. Progress: d/t tasks complete.` + snapshot State
   - Neither (bare view): `formatTaskList(store.View())` + `State: store.View()` — same as today's view action.

### Classification from final state (not raw entries)

Because empty status now means "no change," the handler's classification (`completedAny`, `inProgressUpdates`, `started`, `manuallyStartedID`) must derive statuses from the snapshot (`mutation.After` / final view), not from the raw update entries. An update entry with empty status whose task ends in_progress counts for `inProgressUpdates` bookkeeping; `started[id]` is true only when the pre-state wasn't in_progress (the existing `Started *bool` frontend contract).

### Status-optional fix (agent/task/task_store.go)

`TaskUpdate.Status` semantics change: empty string = leave status unchanged; non-empty must be one of the four statuses. In `updateLocked`:

- Status validation skips empty statuses (non-empty invalid values still error with today's message).
- The single-in_progress projection carries the task's current status forward for empty-status entries (only `u.Status != ""` overwrites `projected[u.ID]`).
- The apply loop assigns status only when non-empty.
- Everything else (unknown-ID rejection, dependency validation against the projected graph, cycle detection, single-in_progress blocker messaging, timestamps) is unchanged.

### Strict mode

DefTaskList sets `Strict: false` explicitly (following `DefFindSessionTranscripts`, `DefDoctorEvener`, `DefCommunicate`, `DefDelegate`, and the read/transcript tools — six precedents in definitions.go). Rationale: the OpenAI Responses adapter defaults `strict = true` when `Strict` is nil (`toResponsesTools`, llm/providers/openai/responses.go:875-886) and `strictifyJSONSchema` force-requires every property including nested ones. Under that normalization, a strict model would be forced to emit `"status": ""` (violates the enum) or `"depends_on": []` (which CLEARS dependencies — a silent data mutation) on every update item. Explicit `Strict: false` opts out; the schema is then sent as-is with optional fields genuinely omittable. Non-strict providers (Anthropic path, openaicompat with `strict:false` in the function object) are unaffected. The reasoning_effort "inherit" sentinel keeps working (enum includes it) — and with `Strict: false` the sentinel becomes belt-and-suspenders rather than load-bearing for task_list, though it must stay for any other strict-mode surface that reuses `normalizeTaskEffort` (e.g. future delegate schemas).

### Backward compatibility

Old persisted/queued action-shaped calls fail schema validation (`additionalProperties: false` rejects `action`). The repair layer's `ExplainSchemaError` reports the unknown property with the correct container path and an example built from the NEW schema — the model sees "unknown argument action" plus the new shape and self-corrects in one retry. Sessions renegotiate the tool contract every request (schemas ride along), so exposure is limited to in-flight sessions across a rollout.

### Known limitation (documented, pinned by test)

In one combined call you can complete a task and add new tasks, but you cannot start a task created by the same call's `add`: the update would have to name an ID the model cannot know before publication. The response returns the list with the new IDs, so a follow-up call to start it is natural — and when a completion leaves nothing in progress, auto-advance usually starts the next eligible task anyway. Rejected updates of this shape get `unknown task ID %d`, the same error as any unknown ID (the model cannot distinguish "not yet created" from "never existed," and shouldn't need to).

### Test migration inventory

Production code changes: `agent/internal/tool/definitions.go` (DefTaskList), `agent/session_tools_task.go` (handler + decode), `agent/task/task_store.go` (status semantics). Everything else is tests:

| File | Change |
|---|---|
| `agent/session_tools_task_test.go` | Rewrite handler tests to new call shape; add combined add+update, empty-array, notes-only tests |
| `agent/task_store_test.go` | Add empty-status tests; existing TaskUpdate-level tests unaffected (they use the store API) |
| `agent/session_task_effort_test.go` | Update call shapes (11 refs) |
| `agent/task_workflow_test.go` | Update call shapes |
| `agent/cov_task_updated_test.go` | Update call shapes (3 refs) |
| `agent/session_tools_dispatch_fuzz_test.go` | Update fixtures (22 refs) |
| `agent/session_tool_repair_task_branch_test.go` | DELETE both issue #626 tests (failure shape unrepresentable); replace with old-shape rejection tests |
| `agent/internal/tool/repair/explain_covtest_test.go` | Update task_list fixtures (params builder reflects new schema) |
| `agent/internal/tool/repair/explain_action_branch_test.go` | Update taskListParams builder; keep generic branch machinery tests (they test the machinery, not task_list) |
| `agent/session_attention_test.go` | Update call shapes (5 refs) |
| `agent/session_dod_definition_test.go` | Update call shapes (5 refs) |
| `agent/steering_tool_gate_test.go` | Update call shapes (2 refs) |
| `agent/session_tool_repair_test.go` | Update call shapes (3 refs) |
| `agent/internal/sessionlog/load_fuzz_test.go`, `cov_s5_sessionlog_test.go` | Update fixture JSON |
| `agent/transcript_render_job_test.go` | Update fixture JSON (3 refs) |
| `agent/session_tools_misc_contract_fuzz_test.go`, `session_tools_aux_exact_fuzz_test.go`, `session_lifecycle_tail_coverage_fuzz_test.go` | Update fixtures |
| `agent/provider/profile_test.go` | Verify effort-enum sync passes (setEffortLevels rebuilds DefTaskList by name) |
| `cmd/evener-tui/internal/toolsummary/tool_summary.go:144-158` | PRODUCTION: rewrite the `task_list` case — no `action`; render from `add`/`update` arrays (both may be present; e.g. `add 2, update 1`); keep `renderTaskAppend`/`renderTaskUpdate` helpers, call both when both arrays present |
| `cmd/evener-tui/internal/toolsummary/*_test.go`, `fuzz_coverage_test.go` | Update fixtures to new call shape |
| `cmd/evener-tui/internal/msgrender/*_test.go` | Verify: `taskListBody` renders tool OUTPUT (not args), so unaffected; check fixtures that pass task_list args to renderers expecting `action` |

Frontend tests use `"ln"` as a renamed generic tool in fixtures — verify none assert `action` specifically for task_list (spot-check; no changes expected).

---

### Task 1: Store — empty status means no change

**Files:**
- Modify: `agent/task/task_store.go` (TaskUpdate doc comment, `updateLocked`)
- Test: `agent/task/task_store_test.go`

**Interfaces:**
- Consumes: none.
- Produces: `TaskUpdate.Status == ""` = no status change; all other validations unchanged. Used by the handler (Task 3).

- [ ] **Step 1: Write failing tests**

```go
// TestTaskUpdate_EmptyStatusMeansNoChange pins the contract: an update
// entry with an empty status leaves the task's status unchanged while still
// applying notes, deps, and effort — the tool schema has always documented
// status as optional, so the store must honor that.
func TestTaskUpdate_EmptyStatusMeansNoChange(t *testing.T) {
	s := NewTaskStore(t.TempDir(), "t")
	added, err := s.Append([]TaskInput{{Description: "d", Prompt: "p"}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Notes: "note one"}}); err != nil {
		t.Fatalf("notes-only update: %v", err)
	}
	got := s.View()
	if got[0].Status != TaskOpen {
		t.Fatalf("status changed by notes-only update: %v", got[0].Status)
	}
	if len(got[0].Notes) != 1 || got[0].Notes[0] != "note one" {
		t.Fatalf("notes not applied: %v", got[0].Notes)
	}

	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskInProgress}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Notes: "note two"}}); err != nil {
		t.Fatalf("notes-only during progress: %v", err)
	}
	got = s.View()
	if got[0].Status != TaskInProgress {
		t.Fatalf("notes-only update clobbered in_progress: %v", got[0].Status)
	}
	if len(got[0].Notes) != 2 {
		t.Fatalf("second note not appended: %v", got[0].Notes)
	}
}

// TestTaskUpdate_EmptyStatusStillValidates pins that empty status does not
// weaken the other validations: unknown IDs, unknown deps, and invalid
// non-empty statuses still fail.
func TestTaskUpdate_EmptyStatusStillValidates(t *testing.T) {
	s := NewTaskStore(t.TempDir(), "t")
	added, err := s.Append([]TaskInput{{Description: "d", Prompt: "p"}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Update([]TaskUpdate{{ID: 999, Notes: "x"}}); err == nil {
		t.Fatal("unknown ID with empty status must still be rejected")
	}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, DependsOn: &[]int{999}}}); err == nil {
		t.Fatal("unknown dep must still be rejected")
	}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Status: "bogus"}}); err == nil {
		t.Fatal("bogus status must still be rejected")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./agent/task/ -run 'TestTaskUpdate_EmptyStatus' -v`
Expected: FAIL — `invalid status "" for task 1`.

- [ ] **Step 3: Implement**

Update the `TaskUpdate` doc comment:

```go
// TaskUpdate is a change for an existing task.
// Status empty means no status change; non-empty must be one of the four.
// DependsOn nil means no change; &[]int{} clears the dependency list.
// ReasoningEffort empty means no change; a non-empty value replaces it.
```

In `updateLocked`, change the status validation to skip empties:

```go
	// Validate status values up front so a bad status doesn't half-apply.
	// An empty status means "no change": the tool schema documents status
	// as optional, so notes/deps/effort-only updates are legal.
	for _, u := range updates {
		if u.Status == "" {
			continue
		}
		switch u.Status {
		case TaskOpen, TaskInProgress, TaskDone, TaskCancelled:
			// valid
		default:
			return fmt.Errorf("invalid status %q for task %d: must be open, in_progress, done, or cancelled", u.Status, u.ID)
		}
	}
```

In the projection loop, only overwrite when non-empty:

```go
	for _, u := range updates {
		if _, exists := projected[u.ID]; !exists {
			return fmt.Errorf("unknown task ID %d", u.ID)
		}
		if u.Status != "" {
			projected[u.ID] = u.Status
		}
	}
```

In the apply loop, guard the assignment:

```go
			if s.tasks[i].ID == u.ID {
				if u.Status != "" {
					s.tasks[i].Status = u.Status
				}
				if u.Notes != "" {
					s.tasks[i].Notes = append(s.tasks[i].Notes, u.Notes)
				}
				// ... unchanged: DependsOn, ReasoningEffort, timestamps
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./agent/task/ -run 'TestTaskUpdate_EmptyStatus' -v`
Expected: PASS

- [ ] **Step 5: Run the store suite and audit the old contract**

Run: `go test ./agent/task/`
Expected: PASS. Then `rg -n 'invalid status' agent/task/` and update any test that relied on empty status being rejected (none expected — existing bogus-status tests use non-empty values).

- [ ] **Step 6: Commit**

```bash
git add agent/task/task_store.go agent/task/task_store_test.go
git commit -m "fix(task): empty status in update means no change

The tool schema has always documented status as optional, but the store
validated every update's status against the four-value enum, rejecting
notes-only updates the schema promised were legal. An empty status now
carries the current status forward; validation, the single-in_progress
projection, and the apply loop all treat empty as no-change. Non-empty
status behavior is unchanged."
```

### Task 2: Schema — presence-based DefTaskList

**Files:**
- Modify: `agent/internal/tool/definitions.go` (DefTaskList, ~line 611)
- Test: `agent/internal/tool/definitions_test.go`

**Interfaces:**
- Consumes: none.
- Produces: new DefTaskList shape (consumed by `profile.setEffortLevels` and the registry).

- [ ] **Step 1: Write the failing schema test**

```go
// TestDefTaskList_PresenceBased pins the combined-tool schema: no action
// property, add/update arrays optional, update items require only id, and
// no top-level required list (a bare call is a view).
func TestDefTaskList_PresenceBased(t *testing.T) {
	def := DefTaskList([]string{"low", "high"})
	params := def.Parameters.(map[string]any)
	props := params["properties"].(map[string]any)
	if _, has := props["action"]; has {
		t.Fatal("schema must not have an action property")
	}
	if def.Strict == nil || *def.Strict {
		t.Fatal("DefTaskList must set Strict: false explicitly (strict-mode normalization would force-requires nested update fields)")
	}
	add, has := props["add"]
	if !has {
		t.Fatal("schema must have an add property")
	}
	addItems := add.(map[string]any)["items"].(map[string]any)
	addReq, ok := addItems["required"].([]string)
	if !ok || len(addReq) != 3 || addReq[0] != "type" || addReq[1] != "description" || addReq[2] != "prompt" {
		t.Fatalf("add item required = %v, want [type description prompt]", addItems["required"])
	}
	update, has := props["update"]
	if !has {
		t.Fatal("schema must have an update property")
	}
	updateItems := update.(map[string]any)["items"].(map[string]any)
	updateReq, ok := updateItems["required"].([]string)
	if !ok || len(updateReq) != 1 || updateReq[0] != "id" {
		t.Fatalf("update item required = %v, want [id]", updateItems["required"])
	}
	if top, has := params["required"]; has {
		t.Fatalf("schema must not force-require add/update at top level: %v", top)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./agent/internal/tool/ -run TestDefTaskList_PresenceBased -v`
Expected: FAIL — "schema must not have an action property".

- [ ] **Step 3: Implement DefTaskList**

Replace DefTaskList's body with the Design-section schema: keep the `reasoningSchema` construction exactly as today (shared by both item types), new tool description text (Design section), top level without `required`. Delete the old `action`/`tasks`/`updates` properties.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./agent/internal/tool/ -run TestDefTaskList_PresenceBased -v`
Expected: PASS

- [ ] **Step 5: Verify effort-enum sync still passes**

Run: `go test ./agent/provider/`
Expected: PASS (`setEffortLevels` rebuilds DefTaskList by name; the new schema keeps reasoning_effort enums in both item types). Update any profile test asserting on old property names if needed.

- [ ] **Step 6: Commit**

```bash
git add agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go
git commit -m "feat(tool): task_list schema uses presence-based dispatch

Replace the action enum and the tasks/updates arrays with optional add
and update arrays: whichever array is present is the operation, both can
appear in one call, and every response returns the full list. A bare
call is the view. additionalProperties:false rejects old action-shaped
calls at validation with an explainable error."
```

### Task 3: Handler — combined add/update in one publication

**Files:**
- Rewrite: `agent/session_tools_task.go` (registerTaskTools, decode helper, classification)
- Test: `agent/session_tools_task_test.go`

**Interfaces:**
- Consumes: Task 1 store semantics (empty status = no change), Task 2 schema.
- Produces: same tool name, same events payloads, same steering calls, plus a `decodeTaskArgs(args map[string]any) (adds []taskpkg.TaskInput, updates []taskpkg.TaskUpdate, err error)` helper.

- [ ] **Step 1: Rewrite the harness invoke helper first** — `newTaskToolHarness.update` hardcodes `{"action":"update","updates":...}` (session_tools_task_test.go:53-62). Replace with `call(t *testing.T, args map[string]any) tool.ExecResult` taking raw args (nil-safe), update `h.update` callers in the same commit, and add a `h.view(t)` convenience for bare calls.

- [ ] **Step 2: Write failing handler tests** (using the new `h.call` helper)

```go
// TestTaskTool_CombinedAddUpdate: both arrays in one call apply atomically
// with one publication revision and one EventTaskUpdated.
func TestTaskTool_CombinedAddUpdate(t *testing.T) {
	h := newTaskToolHarness(t, nil)
	res := h.update(t, map[string]any{
		"add": []any{map[string]any{
			"type": "implement", "description": "first", "prompt": "do one",
		}},
		"update": []any{map[string]any{
			"id": 1, "status": "in_progress",
		}},
	})
	if res.IsError {
		t.Fatalf("combined call: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "Added 1 task(s); updated 1→in_progress") {
		t.Fatalf("output should combine add+update ack: %q", res.FullOutput)
	}
}

// TestTaskTool_UpdateReferencesThisCallAddsRejected: the model cannot know
// IDs this call's add would assign, so updates must validate against the
// pre-add store state.
func TestTaskTool_UpdateReferencesThisCallAddsRejected(t *testing.T) {
	h := newTaskToolHarness(t, nil)
	res := h.update(t, map[string]any{
		"add": []any{map[string]any{
			"type": "implement", "description": "first", "prompt": "do one",
		}},
		"update": []any{map[string]any{
			"id": 5, "status": "in_progress",
		}},
	})
	if !res.IsError {
		t.Fatal("update targeting an ID this call's add would create must be rejected")
	}
	if !strings.Contains(res.FullOutput, "unknown task ID 5") {
		t.Fatalf("error should name the unknown ID: %s", res.FullOutput)
	}
	if len(h.store.View()) != 0 {
		t.Fatal("failed combined call must not apply its adds either (atomicity)")
	}
}

// TestTaskTool_EmptyArraysAreNoOps: strict-mode models force-send both
// arrays; empty ones must be no-ops. With no mutation, the response is
// the view output (the list), same as a bare call.
func TestTaskTool_EmptyArraysAreNoOps(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "d", Prompt: "p"}})
	res := h.update(t, map[string]any{"add": []any{}, "update": []any{}})
	if res.IsError {
		t.Fatalf("empty arrays must be no-ops: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "1. [open]") {
		t.Fatalf("response must still return the list: %q", res.FullOutput)
	}
}

// TestTaskTool_NotesOnlyUpdateWorks: the end-to-end bug fix from the review.
func TestTaskTool_NotesOnlyUpdateWorks(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "d", Prompt: "p"}})
	if err := h.store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	res := h.update(t, map[string]any{
		"update": []any{map[string]any{"id": 1, "notes": "found the root cause"}},
	})
	if res.IsError {
		t.Fatalf("notes-only update must succeed (was: invalid status \"<nil>\"): %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "Updated 1.") {
		t.Fatalf("ack: %q", res.FullOutput)
	}
}

// TestTaskTool_ViewIsBareCall: no arrays = view, returns the list.
func TestTaskTool_ViewIsBareCall(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "d", Prompt: "p"}})
	res := h.update(t, map[string]any{})
	if res.IsError {
		t.Fatalf("bare call must be a view: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "1. [open]") {
		t.Fatalf("bare call must return the list: %q", res.FullOutput)
	}
}

// TestTaskTool_OldActionShapeRejectedHelpfully: back-compat — old
// action-shaped calls fail at validation with an error the repair layer
// explains against the new schema.
func TestTaskTool_OldActionShapeRejectedHelpfully(t *testing.T) {
	h := newTaskToolHarness(t, nil)
	res := h.update(t, map[string]any{"action": "view"})
	if !res.IsError {
		t.Fatal("old action-shaped call must be rejected")
	}
}

// TestTaskTool_NoOpUpdateEntryRejected: an update entry that changes
// nothing is a model mistake, not a no-op.
func TestTaskTool_NoOpUpdateEntryRejected(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "d", Prompt: "p"}})
	res := h.update(t, map[string]any{
		"update": []any{map[string]any{"id": 1}},
	})
	if !res.IsError {
		t.Fatal("empty update entry must be rejected")
	}
// TestTaskTool_AutoAdvanceCanPickSameCallAdd: completing a task in a call
// that also adds an eligible replacement auto-starts the new task in the
// same publication — "when you mark a task done, the next eligible task
// auto-starts" applies to same-call adds too.
func TestTaskTool_AutoAdvanceCanPickSameCallAdd(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "first", Prompt: "p1"}})
	if err := h.store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	res := h.call(t, map[string]any{
		"update": []any{map[string]any{"id": 1, "status": "done"}},
		"add":    []any{map[string]any{"type": "implement", "description": "second", "prompt": "p2"}},
	})
	if res.IsError {
		t.Fatalf("combined completion+add: %s", res.FullOutput)
	}
	view := h.store.View()
	if len(view) != 2 || view[1].Status != taskpkg.TaskInProgress {
		t.Fatalf("auto-advance should start the same-call add: %+v", view)
	}
	if len(h.steers) == 0 {
		t.Fatal("auto-advance steering should fire for the new task")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./agent/ -run 'TestTaskTool_' -v`
Expected: FAIL — handler still requires `action`.

- [ ] **Step 3: Implement the handler**

Rewrite `registerTaskTools`. Structure:

```go
func registerTaskTools(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Definition: tool.DefTaskList(deps.reasoningEffortLevels),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			deps.taskGuard.MarkUsed()
			store := deps.taskGuard.Store()

			adds, updates, err := decodeTaskArgs(args)
			if err != nil {
				return nil, err
			}

			return mutateAndPublishTaskStore(store, func(epoch, revision uint64) (any, error) {
				// Validate update IDs against the PRE-ADD state: the model
				// composed this call before any new IDs existed.
				if err := validateUpdateIDs(store, updates); err != nil {
					return nil, err
				}
				var added []taskpkg.Task
				if len(adds) > 0 {
					added, err = store.Append(adds)
					if err != nil {
						return nil, err
					}
				}
				// ... updates path (UpdateWithSnapshot + classification from
				// mutation.After), steering/auto-advance blocks ported verbatim
				// from the current update branch, response assembly per Design.
			})
		},
	})
}
```

`decodeTaskArgs` decodes strictly with typed assertions (no `fmt.Sprint`): `add` items → `taskpkg.TaskInput` (type, description, prompt, depends_on ints, reasoning_effort via `validateTaskEffort`); `update` items → `taskpkg.TaskUpdate` (id, status string, notes, depends_on `*[]int` present/absent distinction, reasoning_effort). Update entries with no change field error ("changes nothing"). Missing/wrong-typed arrays → nil / clear errors.

Classification port (from the current update branch), with statuses read from `mutation.After` instead of raw entries: `previous` map from `mutation.Before`, `finalStatus` derived from `mutation.After` keyed by ID, `inProgressUpdates`/`started`/`completedAny`/`manuallyStartedID` logic unchanged in structure. Manual-start steering, auto-advance, all-done + delegates steering, single `deps.emit(events.EventTaskUpdated, ...)`, and the `taskToolStateSnapshot` call all port verbatim.

- [ ] **Step 4: Run handler tests**

Run: `go test ./agent/ -run 'TestTaskTool_' -v`
Expected: PASS. Then rewrite the existing per-action handler tests (view/append/update) into presence-based equivalents, keeping every steering/auto-advance/all-done/events/classification assertion.

- [ ] **Step 5: Commit**

```bash
git add agent/session_tools_task.go agent/session_tools_task_test.go
git commit -m "feat(task): combined add/update handler with presence-based dispatch

One MutateAndPublish per call: adds and updates decode into typed inputs
with strict errors (no fmt.Sprint coercion), update IDs validate against
the pre-add store state (the model composed the call before new IDs
existed), adds apply first, then updates. Every response returns the
full list; empty arrays are no-ops. Classification derives statuses from
the mutation snapshot so empty-status entries classify correctly.
Auto-advance, steering, and events payloads are unchanged."
```

### Task 4: Test migration sweep

**Files:** the inventory table above.

- [ ] **Step 1:** For each file in the inventory, convert `{"action":"view"}` → `{}`, `{"action":"append","tasks":X}` → `{"add":X}`, `{"action":"update","updates":Y}` → `{"update":Y}`. Where tests assert old error strings (`unknown task_list action`, `requires a non-empty 'tasks' array`), re-assert on the new ones. File-by-file with rg + edits — no sed scripting over 20 files.
- [ ] **Step 2:** Delete the two issue #626 tests in `agent/session_tool_repair_task_branch_test.go` and replace them with old-shape rejection tests (the model-visible error names `action` as unknown and shows the new example shape).
- [ ] **Step 3:** Run `go test ./agent/...` — fix fallout until green.
- [ ] **Step 4:** Run `go test ./agent/internal/tool/...` — task_list fixtures updated; generic branch-machinery tests stay.
- [ ] **Step 5:** Commit with explicit paths (only files actually changed):

```bash
git add <explicitly-changed-files>
git commit -m "test(task): migrate fixtures to presence-based task_list calls"
```

### Task 5: Full gate

- [ ] **Step 1:** `make lint` — expect naming/generated-output gates to pass.
- [ ] **Step 2:** `make vet`.
- [ ] **Step 3:** `make test` (or at minimum `go test ./agent/... ./agent/internal/...`).
- [ ] **Step 4:** Fix any fallout, commit.

---

## Self-Review

1. **Spec coverage:** schema (Task 2), handler + atomicity (Task 3), status fix (Task 1), back-compat (Task 3 + Task 4 rejection tests), test migration (Task 4), gates (Task 5), repair machinery untouched (constraint honored — only test fixtures change).
2. **Placeholder scan:** clean — every step carries complete code or an exact contract.
3. **Type consistency:** `decodeTaskArgs` signature matches its Task 3 use; `validateUpdateIDs(store, updates)` is defined in Task 3 Step 3's structure and named identically; store changes (Task 1) match what Task 3's classification port relies on (empty status, snapshot-derived statuses).

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-08-31-task-tool-combined.md`. Two execution options:

**1. Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks

**2. Inline Execution** — execute tasks in this session with checkpoints

Which approach?
