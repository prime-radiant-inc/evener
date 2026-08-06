# WS6: Validation Errors That Tell The Truth — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every validation/invariant error a session-facing tool can return
names the actual failing field/invariant, at its real location, plus the
corrective action — not a misleading top-level guess. Also: no tool call can
ship a runaway multi-hundred-KB argument payload through the registry
unnoticed.

**Architecture:** Five independent-ish fixes, grouped into three tasks by
call-site proximity:
1. `offendingField` + `ExplainSchemaError` currently collapse a JSON-Schema
   validation error's *deepest cause* to its last path segment and check
   presence/required-list only at the top level. For nested failures (array
   items, nested objects) this produces nonsense. Fix: carry the full
   instance-location path, walk it against both the schema and the parsed
   args to find the real container, and report presence/required-list
   relative to *that* container.
2. `task_list`'s single-in_progress invariant error is silent about which
   task is blocking. `TaskStore.CurrentInProgress()` already exists and is
   unused. `update_goal`'s no-op silently swallows harness-only goal
   updates. `defUpdateGoal` lives in `agent/session_tools_goal.go` instead of
   with the other tool definitions. `delegate_send`'s description doesn't
   warn that it's not how you deliver your own results.
3. The registry has no cap on tool-argument byte size; a runaway generation
   can execute a multi-hundred-KB call that does nothing useful.

**Tech stack:** Go, `agent` module only. Test conventions per
`docs/testing.md`; jsonschema library is
`github.com/santhosh-tekuri/jsonschema/v5`.

**Context (verified 2026-08-06, file:line on this branch's base):**
- `agent/session_tool_repair.go:98-116` (`offendingField`): walks
  `ve.Causes[0]` repeatedly to the deepest cause, trims `ve.InstanceLocation`,
  and returns only the **last path segment**.
- `agent/internal/tool/repair/explain.go:15-32` (`ExplainSchemaError`):
  treats that last segment as a **top-level** property name — checks
  `args[offendingField]` (top-level parsed args) for presence and lists
  `requiredList(params)` (top-level schema `required`).
- Reproduced directly against `DefTaskList`/`DefAskUser`'s real schemas
  (jsonschema v5.3.1, `ValidationError.InstanceLocation`/`KeywordLocation`):
  - `task_list update` with `updates[0]` missing `status` (`{"id":1,"notes":"x"}`)
    fails on `.../updates/items/required`, `InstanceLocation="/updates/0"`.
    Deepest-cause last segment is `"0"` (an array index, not a field) →
    today's message is `task_list: missing required argument "0".` Required
    line shows top-level `["action"]` — meaningless.
  - `ask_user` with `questions[0].header` too long (`maxLength`) fails on
    `.../questions/items/properties/header/maxLength`,
    `InstanceLocation="/questions/0/header"`. Deepest-cause last segment is
    `"header"` → today's message is
    `ask_user: missing required argument "header".` even though `header` is
    **present** (just too long) and isn't even in the top-level `required`
    list (`["questions"]`) — the "header/questions contradiction".
- `agent/task/task_store.go:428-436` (`CurrentInProgress`), error site
  `agent/task/task_store.go:527-547` (`updateLocked`, the
  `inProgressCount > 1` branch) — currently
  `"only one task may be in_progress at a time; update would result in %d"`.
  `Task.Description` (`task_store.go:57`) is the short human-readable title
  field (there is no separate `Title`).
- `agent/session_tools_goal.go:57-59` (`update_goal`'s `SetTerminal` miss):
  returns `tool.StateResult{Output: "No active goal to update."}, nil` —
  stays non-erroring, just uninformative. `defUpdateGoal` is defined in this
  file (`:15-33`) instead of `agent/internal/tool/definitions.go` with every
  other `Def*` function.
- `agent/internal/tool/definitions.go:159-172` (`DefDelegateSend`).
- `agent/internal/tool/registry.go:480-509` (`Registry.ExecuteCall`): no
  argument-size check before `json.Unmarshal(call.Arguments, &args)`.
  Existing error-result conventions: `unknown tool: <name>`,
  `invalid tool arguments JSON: <err>`, `tool args schema validation failed:
  <err>` — all rendered via `truncateResult(name, callID, msg, true, ...)`.

## Global Constraints

- Multi-module gate: `go build ./... && go test ./...` inside `agent/`
  (this workstream touches only the `agent` module).
- Exact-message assertions for every historical payload shape reproduced
  above — no `strings.Contains` where the plan gives a literal string.
- `offendingField`/`ExplainSchemaError`'s existing flat/top-level behavior
  (single path segment, e.g. `edit_file` missing `old_string`) must stay
  byte-for-byte unchanged — `agent/internal/tool/repair/explain_test.go` and
  the `FuzzRepairDiagnostics` fuzz target in `repair_fuzz_test.go` pin this;
  do not weaken or delete their assertions.
- No drive-by refactors. Smallest reasonable change per fix.
- Never leave scratch/exploration files (e.g. ad hoc `_test.go` files used to
  inspect library behavior) in the tree — delete before committing.

---

### Task 1: Path-aware schema-error explanations (nested validation failures)

**Files:**
- Modify: `agent/session_tool_repair.go` (`offendingField`)
- Modify: `agent/internal/tool/repair/explain.go` (`ExplainSchemaError`)
- Test: `agent/internal/tool/repair/explain_test.go` (extend),
  `agent/session_tool_repair_test.go` (extend or new — check which file
  currently covers `prepareToolCall`'s schema-error path; follow its
  pattern)

**Interfaces:**
- `offendingField(err error) string` — same signature. Change: return the
  deepest cause's full trimmed `InstanceLocation` as a JSON-Pointer-style
  path (`"updates/0"`, `"questions/0/header"`, or `""` when unavailable),
  not just the last segment. A single-segment path (today's only case) must
  produce byte-identical downstream text to today.
- `ExplainSchemaError(toolName string, params, args map[string]any,
  instanceLocation string) string` — same signature (4th param is now a path,
  not a bare field name; existing callers passing a single segment, e.g.
  `"old_string"`, are unaffected). Internally: split `instanceLocation` on
  `/`; walk it against `params` (JSON-Schema) and `args` (parsed instance) in
  lockstep, alternating object-property steps (`properties[seg]`) and
  array-index steps (`items` when the schema is `type: array` and `seg`
  parses as an integer). Track `(containerSchema, containerInstance)` — the
  node one level *above* the final segment — as you go.
    - If the final segment resolves to a declared property of
      `containerSchema` (an object-property step): behave exactly as today,
      scoped to the container instead of the root — present in
      `containerInstance` → "wrong type or value"; absent → "missing
      required argument". Required-list line uses `containerSchema`'s
      `required`, not the top-level's. When the container *is* the root
      (path length 1), omit the "in `<container>`" qualifier entirely —
      this is the byte-for-byte-unchanged case.
    - If the final segment does **not** resolve as a declared property (the
      `updates/0` case — it's an array index with nothing after it, so
      "container" and "the node the error is actually about" are the same
      object): the node itself is missing one or more required properties.
      Diff `containerSchema`'s `required` against `containerInstance`'s
      keys; report the first missing one (schema/required-list order) by
      name, scoped to that container.
    - If any walk step can't be resolved (schema shape doesn't match the
      path — e.g. a fuzzed/malformed path), fall back to today's flat
      top-level behavior using the *whole* original string as a single
      field name. Never panic; never lose the tool name/required-list/
      example scaffolding.
  Format the display path with `[N]` for array indices and `.name` for
  object properties, e.g. `questions/0/header` → `questions[0].header`;
  `updates/0` → `updates[0]`.

- [ ] **Step 1: Write the failing tests** in
  `agent/internal/tool/repair/explain_test.go`, table-driven, using the two
  reproduced payload shapes as literal schema/args/path fixtures (mirror
  `DefTaskList`'s `updates` item schema and `DefAskUser`'s `questions` item
  schema inline — this package must stay dependency-free of `agent/internal/tool`,
  so hand-build the relevant schema fragments, not the full `Def*` calls):
  - `updates[0]` missing `status` → exact want:
    `` `task_list: missing required argument "status" in updates[0].\nRequired arguments in updates[0]: id (integer), status (string).\nExample: {"action": "..."}` `` —
    confirm the exact `Example:` line against `minimalExample` on the
    *top-level* schema (unchanged behavior) before hardcoding it.
  - `questions[0].header` too long (present, invalid) → exact want:
    `` `ask_user: argument "questions[0].header" has the wrong type or value.\nRequired arguments in questions[0]: question (string), options (array).\nExample: {"questions": [...]}` `` —
    same caveat: confirm the real `Example:` line before hardcoding.
  - Regression case: flat top-level missing arg (existing behavior) still
    produces the unqualified `"Required arguments:"` line with no `"in "`
    text.
  - Also add a case in `agent/session_tool_repair_test.go` (or wherever
    `prepareToolCall`'s schema-failure path is currently tested) driving
    the *real* `DefTaskList`/`DefAskUser` definitions end-to-end through
    `prepareToolCall`, asserting `PrevalErr` matches the same literal
    strings — this is the regression guard against future schema edits
    silently drifting the two definitions out of sync with these fixtures.
- [ ] **Step 2: Run the new tests, confirm they fail** with today's
  `missing required argument "0"` / `missing required argument "header"`
  text.
- [ ] **Step 3: Implement** `offendingField` (full path, not last segment)
  and `ExplainSchemaError` (path walk) per the interfaces above.
- [ ] **Step 4: Run `agent/internal/tool/repair` and `agent` package tests;
  all green**, including `TestExplainSchemaError_NamesOffendingField`,
  `TestExplainSchemaError_FallbackWhenUnknownField`,
  `TestExplainSchemaError_DoesNotMutateStringRequired`, and
  `FuzzRepairDiagnostics` (`go test ./internal/tool/repair/... -run Fuzz -fuzz=FuzzRepairDiagnostics -fuzztime=10s` from `agent/`, in addition to the
  normal `go test ./...`).
- [ ] **Step 5: Delete any scratch/exploration files** used while designing
  the path walk (there should be none in the final diff — check `git
  status`).
- [ ] **Step 6: Commit**
  (`fix(tool-repair): schema errors report the real nested field, location, and container requirements`).

### Task 2: task_list blocker naming, update_goal no-op reason, delegate_send description, defUpdateGoal relocation

**Files:**
- Modify: `agent/task/task_store.go` (`updateLocked`'s `inProgressCount > 1`
  branch; factor `CurrentInProgress`'s body into an unlocked helper both it
  and `updateLocked` call — `updateLocked` already holds `s.mu`, so it
  cannot call the locking `CurrentInProgress` method directly)
- Modify: `agent/internal/tool/definitions.go` (`DefTaskList`'s
  `Description`; add `defUpdateGoal` here, renamed if needed to match this
  file's naming, e.g. keep `defUpdateGoal` lowercase since it's still
  package-internal to `agent`... **note:** `agent/internal/tool/definitions.go`
  is package `tool` while `defUpdateGoal` today is package `agent` — moving
  it means either exporting it (`DefUpdateGoal`, matching every other
  function in this file) and updating the one call site in
  `agent/session_tools_goal.go`, or keeping it in `agent` package another
  way. Follow the file's existing convention: every function in
  `definitions.go` is exported (`Def...`); export it and fix the call site.
- Modify: `agent/session_tools_goal.go` (remove `defUpdateGoal`, update the
  call site to `tool.DefUpdateGoal()`, rewrite the no-op message)
- Test: `agent/task/task_store_test.go` (extend), `agent/session_tools_goal_test.go`
  (extend — check exact filename), `agent/internal/tool/definitions_test.go`
  (extend, for the moved definition + delegate_send description)

**Interfaces:**
- `task_store.go`: new unexported `currentInProgressLocked() (Task, bool)`
  containing today's `CurrentInProgress` loop; `CurrentInProgress` becomes a
  thin locking wrapper around it; `updateLocked`'s `inProgressCount > 1`
  branch calls `currentInProgressLocked()` to find the blocker (the task
  that is in_progress in `s.tasks` *before* this batch's projection — i.e.
  the one the batch didn't necessarily touch) and builds:
  `` fmt.Errorf("only one task may be in_progress; %d %q is currently in_progress — complete or defer it in the same updates array.", blocker.ID, blocker.Description) ``
  — confirm exact punctuation/wording against the spec string before
  hardcoding the test assertion. If no single unambiguous blocker exists
  (e.g., the batch itself sets two *new* tasks in_progress with none
  previously in_progress), fall back to a message naming the projected IDs
  directly rather than guessing; write the test for the common case
  (existing in_progress task blocks a new one) first, per TDD, then decide
  if the fallback needs its own test based on what `updateLocked` can
  actually construct.
- `DefTaskList`'s `Description` gains one clause stating the invariant,
  e.g. appended: `" Only one task may be in_progress at a time; to start a
  new one, complete or defer the current one in the same updates array."`
- `update_goal`'s no-op path (`SetTerminal` returns false) message becomes
  exactly: `"No goal is active for this session (none was set at launch);
  nothing recorded — this tool only updates a goal the harness
  registered."` (still `tool.StateResult`, still non-erroring — matches
  today's error-free contract).
- `DefDelegateSend`'s `Description` gains a leading sentence:
  `"Sends a message to a child delegate you created — this is not how you
  deliver your own results; use communicate for that."` followed by the
  existing description text.

- [ ] **Step 1: Write failing tests**, one per fix:
  - `task_store_test.go`: a store with task 1 `in_progress`, task 2 `open`;
    `Update([]TaskUpdate{{ID: 2, Status: TaskInProgress}})` (task 1 left
    untouched) → assert the exact error string naming task 1's ID and
    `Description`.
  - `session_tools_goal_test.go` (or wherever `update_goal`'s tool exec is
    tested): a session with no active goal calling `update_goal` → assert
    `StateResult.Output` equals the exact new sentence.
  - `definitions_test.go`: assert `DefTaskList(nil).Description` contains
    the new invariant clause (exact substring, not full-string — the rest
    of the description is unrelated to this fix); assert
    `DefDelegateSend().Description` starts with the new leading sentence
    (exact string, `strings.HasPrefix`); assert `DefUpdateGoal()` exists in
    package `tool` and returns the same shape as before (name, params
    unchanged — this is a pure relocation, verify no behavior drift by
    diffing against the pre-move definition in the test if easy, otherwise
    just assert the known fields).
- [ ] **Step 2: Run, confirm all four fail** (or don't compile, for the
  `DefUpdateGoal` move — that's expected mid-refactor; get the move done
  first if compilation blocks the other three tests from running).
- [ ] **Step 3: Implement** all four changes.
- [ ] **Step 4: Run `agent` module tests; all green.**
- [ ] **Step 5: Commit**
  (`fix(agent): name the task_list in_progress blocker, explain update_goal no-ops, warn delegate_send is not communicate`).

### Task 3: Registry-level tool-argument byte-size cap

**Files:**
- Modify: `agent/internal/tool/registry.go` (`Registry.ExecuteCall`)
- Test: `agent/internal/tool/registry_test.go` (extend)

**Interfaces:**
- A package-level constant, e.g. `const maxToolArgumentBytes = 256 * 1024`,
  checked at the top of `ExecuteCall` (before `json.Unmarshal`, so a
  pathological payload isn't even parsed): when
  `len(call.Arguments) > maxToolArgumentBytes`, return
  `truncateResult(name, callID, msg, true, defaultToolLimit(name))` with
  `msg` following the file's existing terse convention, e.g.:
  `` fmt.Sprintf("tool arguments too large: %d bytes exceeds the %d byte limit", len(call.Arguments), maxToolArgumentBytes) `` —
  match the exact phrasing style of the neighboring `"unknown tool: "` /
  `"invalid tool arguments JSON: %v"` lines (lowercase, no trailing period,
  per that file's convention — confirm before finalizing the test's exact
  string).
  This check applies uniformly regardless of tool name (registry-level, not
  per-tool) — matches item 5's framing ("Registry-level cap").

- [ ] **Step 1: Write the failing test** in `registry_test.go`, following
  `TestToolRegistry_InvalidArgumentsJSON_IsReturnedToModel`'s shape: a
  registered no-op tool, `ExecuteCall` with `Arguments` one byte over
  256KB (e.g. `json.RawMessage(strings.Repeat("a", 256*1024+1))` wrapped so
  it's still syntactically-irrelevant since the cap fires before parsing)
  → assert `res.IsError` and `res.Output` contains the exact size-limit
  message text (or exact-match if the message has no variable prefix
  ambiguity). Add a boundary test at exactly 256KB → not rejected (falls
  through to the existing "invalid tool arguments JSON" path, which is
  fine — the boundary test only needs to confirm the size gate itself
  didn't fire).
- [ ] **Step 2: Run, confirm it fails** (today: no cap, call proceeds to
  JSON parsing).
- [ ] **Step 3: Implement** the constant and check.
- [ ] **Step 4: Run `agent` module tests; all green.**
- [ ] **Step 5: Commit**
  (`feat(tool-registry): cap tool-argument byte size at 256KB`).

## Acceptance (whole workstream)

- All five spec items land with exact-message tests over the historical
  payload shapes (reproduced task_list/ask_user schema failures; the
  task_list blocker name; the update_goal no-op reason; the delegate_send
  description; the 256KB cap).
- `agent/internal/tool/repair/explain_test.go`'s existing flat-schema
  assertions and `FuzzRepairDiagnostics` still pass unmodified in behavior
  (only new cases added).
- `go build ./... && go test ./...` in `agent/` exits 0.
