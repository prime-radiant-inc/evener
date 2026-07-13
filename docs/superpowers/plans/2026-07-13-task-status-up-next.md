# Current Task “Up Next” Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the collapsed live task card label the current in-progress task as “up next” instead of reporting the number of open tasks as “N up next.”

**Architecture:** Keep task state partitioning and the shared task-row renderer unchanged. Add a label at the live-plan frontier when an in-progress task exists, suppress the open queue’s count in the collapsed summary for that state, and preserve the expanded open-task group and degraded open-only fallback.

**Tech Stack:** Browser JavaScript, DOM APIs, Node.js, jsdom, shell test harness.

## Global Constraints

- Modify only the live task-card rendering and its deterministic jsdom coverage.
- Keep open tasks ordered by task ID in the expanded `Up next · N` group.
- Preserve done and cancelled summary counts.
- Preserve the completed-plan message and open-only legacy/degraded discoverability.
- Reuse the shared task-row renderer.
- Add CSS only if existing task-card typography cannot present the label correctly.

---

### Task 1: Label the current task and remove its queue count

**Files:**
- Modify: `cmd/serf-hub/jstest/test-renderer-plan.js:115-145`
- Modify: `cmd/serf-hub/assets/renderer.js:4072-4104`

**Interfaces:**
- Consumes: `renderer.renderLivePlan(tasks)`, where each task has `id`, `description`, and `status`; `buildTaskRowLine(task)` returns the existing task row.
- Produces: a `.task-card-group.task-card-current-label` element with text `Up next` immediately before `.task-card-active`; collapsed `.task-card-summary-line` omits `<N> up next` when an active task exists.

- [ ] **Step 1: Write the failing current-task assertions**

In the first mixed-state scenario in `cmd/serf-hub/jstest/test-renderer-plan.js`, replace the assertion that expects `3 up next` with assertions that identify the label, its adjacent active task, and the absent aggregate count:

```js
  const currentLabel = card.querySelector(".task-card-current-label");
  if (!currentLabel || currentLabel.textContent.trim() !== "Up next") {
    return { ok: false, detail: "current task should be labeled 'Up next'" };
  }
  const currentRow = currentLabel.nextElementSibling;
  if (!currentRow || !currentRow.classList.contains("task-card-active") ||
      !/Current work/.test(currentRow.textContent)) {
    return { ok: false, detail: "'Up next' label should precede the current task row" };
  }
  if (!summary || !/3 done/.test(summary.textContent) || /\d+ up next/i.test(summary.textContent)) {
    return { ok: false, detail: "summary should retain done count without an aggregate up-next count, got " + (summary && summary.textContent) };
  }
```

Keep the existing `show all` assertion and the later expanded-body assertion for `Up next · 3`.

- [ ] **Step 2: Run the focused test and verify the new assertion fails**

Run:

```bash
cd cmd/serf-hub/jstest
./run-one.sh test-renderer-plan.js
```

If `run-one.sh` is unavailable, run the command documented by `cmd/serf-hub/jstest/README.md` for one test file.

Expected: `test-renderer-plan.js` fails because `.task-card-current-label` does not exist and the summary still contains `3 up next`.

- [ ] **Step 3: Add the current-task label and conditional summary behavior**

In `renderLivePlan`, add the label before the existing active row:

```js
      if (active) {
        const currentLabel = document.createElement("div");
        currentLabel.className = "task-card-group task-card-current-label";
        currentLabel.textContent = "Up next";
        card.appendChild(currentLabel);

        const row = buildTaskRowLine(active);
        row.classList.add("task-card-row", "task-card-active");
        card.appendChild(row);
```

Keep the existing active-note rendering unchanged. Change the collapsed open-task summary condition so it remains available only when no active task exists:

```js
      if (open.length && !active) summaryBits.push(open.length + " up next");
```

Do not change the expanded-body condition or its `Up next · ${open.length}` heading.

- [ ] **Step 4: Run the focused test and verify it passes**

Run:

```bash
cd cmd/serf-hub/jstest
./run-one.sh test-renderer-plan.js
```

Expected: `test-renderer-plan.js` passes. The current task has an `Up next` label, the collapsed summary retains `3 done`, and the expanded body still reports `Up next · 3`.

- [ ] **Step 5: Run the full deterministic hub JavaScript suite**

Run:

```bash
cd cmd/serf-hub/jstest
./run-all.sh
```

Expected: every listed test prints `OK` and the command ends with `jstest: all tests passed`.

- [ ] **Step 6: Review the diff and commit the implementation**

Run:

```bash
git diff --check
git diff -- cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-plan.js
git status --short
```

Expected: no whitespace errors; only the renderer, its plan test, and the already-committed design/plan history belong to this feature.

Commit:

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-plan.js docs/superpowers/plans/2026-07-13-task-status-up-next.md
git commit -m "fix(hub): label current task as up next"
```

### Task 2: Verify and integrate the feature branch

**Files:**
- Verify: `cmd/serf-hub/assets/renderer.js`
- Verify: `cmd/serf-hub/jstest/test-renderer-plan.js`
- Verify: `docs/superpowers/specs/2026-07-13-task-status-up-next-design.md`
- Verify: `docs/superpowers/plans/2026-07-13-task-status-up-next.md`

**Interfaces:**
- Consumes: committed branch `fix-task-status-up-next` based on `main`.
- Produces: updated local `main` containing the reviewed feature commits, with deterministic hub JavaScript tests passing after merge.

- [ ] **Step 1: Request an independent code review**

Ask a reviewer to compare the branch against the approved design. Require checks for:

```text
- Current in-progress task is labeled “Up next”.
- Collapsed card does not report active-plan open tasks as “N up next”.
- Expanded open queue remains “Up next · N”.
- Open-only degraded states remain discoverable.
- Done, cancelled, and all-done behavior remains unchanged.
- Tests prove the requested behavior without weakening prior coverage.
```

Expected: no unresolved correctness, scope, or test-coverage findings.

- [ ] **Step 2: Re-run verification on the feature branch**

Run:

```bash
cd cmd/serf-hub/jstest
./run-one.sh test-renderer-plan.js
./run-all.sh
cd ../../..
git diff --check main...HEAD
git status --short --branch
```

Expected: focused and full suites pass, `git diff --check` is silent, and the worktree is clean.

- [ ] **Step 3: Merge the feature branch into local main**

Exit the managed worktree, confirm the main checkout contains only its pre-existing untracked files, then merge without amending or discarding unrelated work:

```bash
git switch main
git merge --no-ff fix-task-status-up-next
```

Expected: merge succeeds without conflicts. Do not stage, modify, or delete pre-existing untracked files in the main checkout.

- [ ] **Step 4: Verify the merged main branch**

Run from the main checkout:

```bash
cd cmd/serf-hub/jstest
./run-one.sh test-renderer-plan.js
./run-all.sh
cd ../../..
git status --short --branch
git log --oneline -5
```

Expected: focused and full suites pass; `main` contains the feature commits and merge commit; only the main checkout’s pre-existing unrelated untracked files remain.
