# Inline Task Update Cards Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render each successful inline `task_list` mutation as a progress header plus only the task changes caused by that call.

**Architecture:** Keep the existing `task_list` suppression and pending-call cache at the event boundary. Route successful mutations to the existing per-call `appendTaskUpdateCard` renderer, simplify that renderer to explicit status/addition transitions plus authoritative auto-activation, and remove the persistent living-plan path and its disclosure UI. The task sidebar remains the full-plan view.

**Tech Stack:** Browser JavaScript, JSDOM renderer tests, CSS, Markdown documentation

## Global Constraints

- Work only in the `task-widget-changes` managed worktree.
- Follow test-driven development: add a failing renderer scenario, observe the expected failure, then change production code.
- A `task_list` view call remains silent.
- A failed or malformed mutation must not render a successful update card.
- Each successful append or update creates a card at that tool call's conversation position.
- The inline card shows `Tasks`, settled/total progress, the progress meter, and only this call's established changes.
- Do not show aggregate `N done`, aggregate `N up next`, unchanged neighboring tasks, `show all`, `more`, or any full-plan disclosure.
- Do not change task state, task ordering, sidebar behavior, or the `task_list` tool contract.
- Use the returned authoritative task snapshot for final row data and auto-activation; degraded replay must not invent transitions.
- Run JavaScript renderer tests from `cmd/serf-hub/jstest` with `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules`.

## File Structure

- Modify `cmd/serf-hub/jstest/test-renderer-plan.js`: replace living-plan scenarios with the inline per-mutation DOM contract.
- Modify `cmd/serf-hub/assets/renderer.js`: route successful calls to per-update rendering, select only established changes, and delete the living-plan renderer.
- Modify `cmd/serf-hub/assets/style.css`: retain shared card/header/row styles and remove disclosure-only living-plan styles.
- Modify `docs/web-ui/design-system.md`: describe per-tool-call change cards instead of one persistent plan card.

---

### Task 1: Render one changes-only card per successful mutation

**Files:**
- Modify: `cmd/serf-hub/jstest/test-renderer-plan.js:1-296`
- Modify: `cmd/serf-hub/assets/renderer.js:1193-1209,1235-1255,4038-4331`

**Interfaces:**
- Consumes: `pendingTaskCalls: Map<callID, {args: object, priorIds: Set<number>}>`, `parseToolState(data.tool_state)`, `buildTaskRowLine(task)`, `touchKind(status)`, and the tool result's task array.
- Produces: `appendTaskListSystemLine(args, stateTasks, priorIds)` appends one `.task-card` for a successful non-view mutation; `appendTaskUpdateCard(args, stateTasks, priorIds)` renders its `.task-card-head`, `.task-card-progress`, `.task-card-meter`, and changed `.task-card-row` elements.

- [ ] **Step 1: Replace the living-plan scenarios with failing per-call scenarios**

Keep `newHarness`, `scenario`, the CSS contract, and `PLAN`. Replace `appendTask` with helpers that can separate mutation arguments from authoritative state:

```js
function taskCall(callId, args, stateTasks, error) {
  return [
    ["TOOL_CALL_START", {
      call_id: callId,
      tool_name: "task_list",
      arguments_json: JSON.stringify(args),
    }],
    ["TOOL_CALL_END", {
      call_id: callId,
      tool_name: "task_list",
      output: error ? "failed" : "ok",
      error: error || undefined,
      tool_state: stateTasks === undefined ? undefined : JSON.stringify(stateTasks),
    }],
  ];
}

function cardRows(card) {
  return Array.from(card.querySelectorAll(".task-card-row"));
}
```

Add these scenarios with direct DOM assertions:

1. `append shows only newly added tasks with progress` — start with an append of `PLAN`; assert one card, `3 / 7`, a meter fill width of `43%`, seven rows, and no `.task-card-summary-line`, `.task-card-toggle`, `.task-card-showall`, or text matching `show all|more`.
2. `completion shows completed and automatically activated tasks` — seed task details with an append, then update only task 4 to `done`; return state with task 5 `in_progress`; assert two cards, and assert the second card has `4 / 7` and exactly two rows containing tasks 4 and 5. Assert task 4 is `.done`, task 5 is `.current`, and no unchanged task title occurs in the second card.
3. `explicit activation shows only the activated task` — update task 5 to `in_progress`; assert the mutation card has exactly one `.current` row for task 5.
4. `non-status update shows only the progress header` — update task 4 with `notes` and no `status`; assert a card exists with the correct progress and zero `.task-card-row` elements.
5. `consecutive mutations create cards for their own changes` — perform two updates; assert three cards including the seed append, and assert each update card contains only its own changed descriptions.
6. `degraded replay renders explicit changes without inventing auto-activation` — seed cached descriptions, then send a completion update with no `tool_state`; assert the completion row appears and no other row appears.
7. `view empty append and failed mutation render no card` — send `view`, an empty append with empty state, and an update ending with `error: "write failed"`; assert no `.task-card` exists.

For the completion scenario, use this event shape to prove that auto-activation comes from authoritative state rather than the update arguments:

```js
...taskCall("t2", {
  action: "update",
  updates: [{ id: 4, status: "done", notes: "shipped it" }],
}, PLAN.map(task => {
  if (task.id === 4) return { ...task, status: "done" };
  if (task.id === 5) return { ...task, status: "in_progress" };
  return task;
}))
```

- [ ] **Step 2: Run the focused test and verify the new contract fails**

Run:

```bash
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
```

Expected: FAIL because repeated mutations still reuse one living card, the card still contains aggregate/disclosure UI, and update-only cards are not appended independently.

- [ ] **Step 3: Reject failed task calls at the event boundary**

In `TOOL_CALL_END`, always delete the pending entry, but render only when `data.error` is absent:

```js
const pending = this.pendingTaskCalls && this.pendingTaskCalls.get(data.call_id);
if (pending !== undefined) {
  this.pendingTaskCalls.delete(data.call_id);
  if (!data.error) {
    this.appendTaskListSystemLine(
      pending.args,
      parseToolState(data.tool_state),
      pending.priorIds,
    );
  }
}
```

This keeps failed calls silent and prevents stale pending state.

- [ ] **Step 4: Route mutations to the per-call renderer**

In `appendTaskListSystemLine`, retain the `view` early return, `rememberTask` calls, and `refreshTaskBadgeSoon`. Replace construction of a full fallback plan and `this.renderLivePlan(tasks)` with:

```js
this.appendTaskUpdateCard(args, stateTasks, priorIds);
this.refreshTaskBadgeSoon();
```

Do not refresh or render for `action === "view"`.

- [ ] **Step 5: Simplify `appendTaskUpdateCard` to established changes**

Use `const hasAuthoritativeState = Array.isArray(stateTasks);`. Build and sort `tasks` from non-empty authoritative state, otherwise from `taskDetails`, retaining the existing update-argument fallback when neither source has rows.

Build `touched` with only additions and named status transitions:

```js
const touched = new Map();
if (args.action === "update" && Array.isArray(args.updates)) {
  for (const update of args.updates) {
    if (!update || update.id == null) continue;
    if (!["done", "cancelled", "in_progress"].includes(update.status)) continue;
    touched.set(Number(update.id), {
      kind: touchKind(update.status),
      note: String(update.notes || "").trim(),
    });
  }

  const completed = args.updates.some(update => update && update.status === "done");
  if (completed && hasAuthoritativeState) {
    const active = tasks.find(task => task.status === "in_progress");
    if (active && !touched.has(Number(active.id))) {
      touched.set(Number(active.id), { kind: "started", note: "" });
    }
  }
} else if (args.action === "append") {
  const known = priorIds || new Set();
  for (const task of tasks) {
    if (!known.has(Number(task.id))) {
      touched.set(Number(task.id), { kind: "added", note: "" });
    }
  }
}
```

Return without a card only when no usable tasks exist. A valid non-status update with authoritative tasks must still render its header.

Construct a new card every call. Reuse the existing header elements and add the meter construction currently in `renderLivePlan`:

```js
const meter = document.createElement("div");
meter.className = "task-card-meter";
const fill = document.createElement("div");
fill.className = "task-card-meter-fill";
fill.style.width = (total ? Math.round((settled / total) * 100) : 0) + "%";
meter.appendChild(fill);
head.appendChild(meter);
```

Render rows by iterating sorted `tasks` and skipping IDs absent from `touched`. Add `task-card-row`, `touched`, and the touch kind to each row. Render a mutation note only for a touched row whose explicit update supplied a non-empty note. Do not render neighboring rows, hidden-row classes, summary counts, completion prose, or disclosure buttons.

- [ ] **Step 6: Delete the persistent living-plan implementation**

Delete:

- `this.livePlanCard = null` initialization.
- `renderLivePlan(tasks)`.
- `taskFoldGroup(label, items)`.
- comments that claim one living card is moved through the transcript.

Keep `appendTaskUpdateCard`, `refreshTaskBadgeSoon`, `buildTaskRowLine`, and sidebar task rendering.

- [ ] **Step 7: Run the focused test and verify it passes**

Run:

```bash
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
```

Expected: all scenarios print `PASS` and the process exits 0.

- [ ] **Step 8: Run related renderer tests**

Run:

```bash
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-task-updated-subscription.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-realistic-flow.js
```

Expected: both scripts exit 0. If a test encodes the superseded living-card contract, update only that contract assertion and rerun it; do not weaken unrelated assertions.

- [ ] **Step 9: Commit the behavior change**

```bash
git status --short
git add cmd/serf-hub/jstest/test-renderer-plan.js cmd/serf-hub/assets/renderer.js
git diff --cached --check
git commit -m "fix(hub): show inline task changes"
```

Expected: one commit containing only the renderer behavior and its tests.

---

### Task 2: Remove living-plan presentation and align the design system

**Files:**
- Modify: `cmd/serf-hub/jstest/test-renderer-plan.js:39-69`
- Modify: `cmd/serf-hub/assets/style.css:3101-3203`
- Modify: `docs/web-ui/design-system.md:155`

**Interfaces:**
- Consumes: Task 1's `.task-card`, `.task-card-head`, `.task-card-progress`, `.task-card-meter`, `.task-card-meter-fill`, `.task-card-row`, and `.task-card-note` DOM contract.
- Produces: CSS and design documentation for per-call changes-only cards, with no disclosure-specific selector contract.

- [ ] **Step 1: Add failing CSS contract assertions for removed disclosure UI**

Extend the synchronous CSS checks in `test-renderer-plan.js`:

```js
[
  !/\.task-card-summary-line\s*\{/.test(styleSrc),
  "task cards must not retain aggregate summary styles",
],
[
  !/\.task-card-toggle\s*\{/.test(styleSrc),
  "task cards must not retain full-plan disclosure styles",
],
[
  !/\.task-card-fold(?:\s|\.|\{)/.test(styleSrc),
  "task cards must not retain done-pile fold styles",
],
```

- [ ] **Step 2: Run the focused test and verify the CSS assertions fail**

Run:

```bash
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
```

Expected: FAIL because living-plan summary, toggle, and fold selectors still exist.

- [ ] **Step 3: Remove disclosure-only CSS and rewrite the task-card comment**

Change the block comment above `.task-card` to describe one inline card per successful mutation, a progress header, and changed rows on a neutral left rail.

Delete selectors used only by the removed living-plan UI:

```text
.task-card-active
.task-card-complete
.task-card-summary-line
.task-card-body
.task-card[data-expanded="true"] .task-card-body
.task-card[data-expanded="true"] .task-card-summary-line
.task-card-group
.task-card-fold
.task-card-fold-head
.task-card-fold-head:hover
.task-card-fold-rows
.task-card-fold.open .task-card-fold-rows
.task-card-toggle
.task-card-toggle:hover
```

Retain `.task-card`, header/progress/meter styles, `.task-card-note`, `.task-time`, and shared `.plan-item` status styles. Before deleting, verify each selector has no remaining JavaScript or HTML reference:

```bash
rg -n 'task-card-(active|complete|summary-line|body|group|fold|toggle)' cmd/serf-hub --glob '!assets/style.css'
```

Expected: no matches.

- [ ] **Step 4: Update the design-system task row**

Replace the `Plan / tasks` table row in `docs/web-ui/design-system.md` with this contract:

```markdown
| **Plan / tasks** | one inline change card per successful `task_list` mutation — progress (`31/46` + thin neutral meter) followed by only tasks added or whose status changed in that call | task notes attached to changed rows | the whole plan in the sidebar; no inline full-plan disclosure | a single neutral **left rail, no box** (rail = "status", box = "needs-you"). Each card stays at its tool call's conversation position. |
```

- [ ] **Step 5: Run focused and full JavaScript renderer suites**

Run:

```bash
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh
```

Expected: the focused test and every `test-*.js` script pass with no failures or timeouts.

- [ ] **Step 6: Run repository checks for the touched web assets**

Run from the repository root:

```bash
git diff --check
make lint-docs
```

Expected: both commands exit 0 with no warnings attributable to the change.

- [ ] **Step 7: Commit presentation cleanup and documentation**

```bash
git status --short
git add cmd/serf-hub/jstest/test-renderer-plan.js cmd/serf-hub/assets/style.css docs/web-ui/design-system.md
git diff --cached --check
git commit -m "docs(hub): align task change cards"
```

Expected: one commit containing only CSS contract cleanup, its test assertions, and design-system documentation.

---

### Task 3: Final verification

**Files:**
- Verify only; no planned modifications.

**Interfaces:**
- Consumes: Task 1's renderer behavior and Task 2's presentation/documentation contract.
- Produces: clean test evidence and repository state for review.

- [ ] **Step 1: Run the complete JavaScript renderer suite from a clean shell**

```bash
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh
```

Expected: `jstest: all tests passed`.

- [ ] **Step 2: Run the repository's short deterministic suite**

From the repository root:

```bash
make test-short
```

Expected: exit 0. Investigate and fix any failure; do not dismiss it as unrelated.

- [ ] **Step 3: Inspect the final diff and history**

```bash
git status --short
git diff HEAD~2..HEAD --check
git diff --stat HEAD~2..HEAD
git log -3 --oneline
```

Expected: clean worktree; no whitespace errors; only the planned renderer, focused test, CSS, and design-system files changed after the design commit.

- [ ] **Step 4: Request final code review**

Provide the reviewer the approved spec, this plan, both implementation commit hashes, the full JavaScript test result, and the `make test-short` result. Resolve every concrete finding with a new failing test when behavior changes, then rerun Steps 1-3.
