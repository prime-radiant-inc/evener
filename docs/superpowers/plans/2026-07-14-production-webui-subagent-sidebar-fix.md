# Production Web UI Subagent Sidebar Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore production Web UI subagent navigation so current subagents remain visible under their direct parent, terminal subagents live in a per-parent inactive disclosure, recursive lineage survives, and clicking a subagent opens or focuses its pane beside its direct parent.

**Architecture:** Carry running in-process delegate IDs from `/status` through the hub roster into tree construction, then build `TreeNode.Children` recursively with effective child state. Project the recursive tree in `sidebar.js` as always-visible current rows plus collapsed inactive groups. Extend `panes.js` with parent-relative insertion and use ancestry stamped during sidebar flattening to open missing ancestor panes and focus existing panes.

**Tech Stack:** Go, vanilla JavaScript, jsdom, HTMX attributes, existing hub tree/roster/pane APIs.

## Global Constraints

- Fix production code; the mockup is design evidence, not the deliverable.
- Preserve direct parentage. Never flatten grandchildren under the main session.
- Current subagents are production states `active`, `awaiting`, `idle`, `warning`, and `notLoaded`; render them directly beneath their parent.
- Inactive subagents are production states `ended`, `closed`, and `errored`; render them only within that parent's initially collapsed `Inactive subagents (N)` disclosure.
- `N` counts terminal direct children only.
- Keep inactive disclosure state independent per parent and persistent across tree resyncs through the existing expanded-state storage.
- A subagent click opens its thread document in a side pane immediately to the right of its direct parent's pane; a direct child of the main session becomes the first side pane.
- If that thread is already open, focus the existing pane and do not duplicate it.
- Opening a nested child first opens any missing ancestor panes in lineage order, subject to the existing three-side-pane cap.
- Preserve ordinary navigation for non-subagent session rows and preserve the existing row-menu `Open beside` action.
- Preserve source-qualified refs and `/thread/<encoded-ref>` thread-document URLs.
- Do not reintroduce a left lineage rail or left-edge active-row accent.
- Tests must be deterministic and require no credentials, provider access, network, sleeps, or ambient developer state.
- Do not add package manifests, lockfiles, or checked-in dependencies. Use external jsdom through `NODE_PATH`.
- Treat commits `e3853f1d3` and `74963ca07` as reference implementations; port only the required behavior and adapt it to current HEAD.

## File Structure

- Modify `cmd/serf-hub/internal/hubcore/prober.go`: decode running delegate transcript refs from detailed status.
- Modify `cmd/serf-hub/internal/hubcore/roster.go`: retain, fingerprint, copy, and query running in-process subagent IDs.
- Modify `cmd/serf-hub/internal/hubcore/tree.go`: apply effective running-child state and recursively build child nodes.
- Modify focused hubcore tests in `prober_test.go`, `roster_test.go`, `tree_test.go`, and `production_subagent_sidebar_regression_test.go`.
- Modify `cmd/serf-hub/assets/sidebar.js`: recursively partition children, render inactive disclosures, stamp ancestry, and open child panes.
- Modify `cmd/serf-hub/assets/style.css`: style nested rows and inactive disclosures without a left lineage rail.
- Modify `cmd/serf-hub/assets/panes.js`: insert a pane after a specified parent pane while preserving focus-existing and the pane cap.
- Modify `cmd/serf-hub/jstest/test-sidebar-children.js`: cover current/inactive recursive projection and click behavior.
- Modify `cmd/serf-hub/jstest/test-panes.js`: cover parent-relative insertion and duplicate focus.

---

### Task 1: Retain running in-process subagent status

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/prober.go`
- Modify: `cmd/serf-hub/internal/hubcore/prober_test.go`
- Modify: `cmd/serf-hub/internal/hubcore/roster.go`
- Modify: `cmd/serf-hub/internal/hubcore/roster_test.go`
- Modify: `cmd/serf-hub/internal/hubcore/coverage_edges_test.go`
- Modify only as required by changed helpers: `cmd/serf-hub/cov_session_residue_pass5_fuzz_test.go`, `cmd/serf-hub/internal/hubcore/scenarios_fuzz_test.go`, `cmd/serf-hub/testmain_test.go`, `cmd/serf-hub/web_test.go`

**Interfaces:**
- Produce `LiveEntry.RunningSubagentIDs []string`.
- Produce `ProbeResult{Status string, PendingApproval bool, PendingAsk bool, RunningSubagentIDs []string}`.
- Produce `(*Roster).IsSubagentActive(sessionID string) bool`.
- Preserve deep-copy behavior in `Roster.List()` and include running IDs in roster change fingerprints.

- [ ] **Step 1: Add focused RED tests**

Add tests that feed a detailed status response containing running `delegate` jobs with local transcript refs, reject completed/non-delegate/malformed refs, prove `Roster.List()` returns a defensive copy, prove fingerprint changes when only running IDs change, and prove `IsSubagentActive` searches all live owners.

Use fixture JSON shaped like:

```json
{
  "session_id": "parent",
  "state": "idle",
  "detailed": {
    "jobs": [
      {"type":"delegate","status":"running","transcript_ref":"local:child-a"},
      {"type":"delegate","status":"completed","transcript_ref":"local:child-b"},
      {"type":"shell","status":"running","transcript_ref":"local:not-a-child"}
    ]
  }
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
go test ./cmd/serf-hub/internal/hubcore -run 'Test(Probe|Roster).*Subagent|TestRoster.*Fingerprint' -count=1 -v
```

Expected: compile or assertion failures because `ProbeResult`, `LiveEntry`, and `Roster` do not retain running child IDs.

- [ ] **Step 3: Port the minimal status-retention behavior**

Use `e3853f1d3` as evidence. Decode only running delegate rows with valid `local:<session-id>` transcript refs. Sort/deduplicate IDs before storing them. Copy slices at roster boundaries. Do not change unrelated probe semantics.

- [ ] **Step 4: Run focused and package tests**

```bash
go test ./cmd/serf-hub/internal/hubcore -run 'Test(Probe|Roster).*Subagent|TestRoster.*Fingerprint' -count=1 -v
go test ./cmd/serf-hub/internal/hubcore -count=1
```

Expected: PASS with no warnings.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore cmd/serf-hub/cov_session_residue_pass5_fuzz_test.go cmd/serf-hub/testmain_test.go cmd/serf-hub/web_test.go
git commit -m "fix(hub): retain running subagent roster state"
```

---

### Task 2: Build recursive subagent trees with effective state

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Modify: `cmd/serf-hub/internal/hubcore/tree_test.go`
- Modify: `cmd/serf-hub/internal/hubcore/production_subagent_sidebar_regression_test.go`
- Modify only if current signatures require it: `cmd/serf-hub/internal/hubcore/scenarios_fuzz_test.go`
- Modify only for active past-child projection: `cmd/serf-hub/app_threadlist.go`, `cmd/serf-hub/app_threadread.go`, `cmd/serf-hub/app_transcripts.go` and their focused tests

**Interfaces:**
- Consume `LiveEntry.RunningSubagentIDs` from Task 1.
- Produce recursive `TreeNode.Children` for subagents and forks.
- A child named in any live owner's `RunningSubagentIDs` receives effective state `active` even without its own rendezvous entry.
- Descendant attention contributes to ancestor/project rollup and default expansion without changing the idle parent's own state.

- [ ] **Step 1: Strengthen the existing RED tests**

Keep `TestBuildTree_PreservesRecursiveSubagentParentage` and replace its reflection bridge with direct `RunningSubagentIDs` construction now that Task 1 defines the field. Add assertions that:

```go
parent.State == "idle"
child.State == "active"
project.RollupState == "active"
project.Expanded == true
grandchild remains nested under child
```

Add a cycle/orphan guard test: malformed lineage must terminate and must not duplicate a node.

- [ ] **Step 2: Verify RED**

```bash
go test ./cmd/serf-hub/internal/hubcore -run 'TestBuildTree_(PreservesRecursiveSubagentParentage|ProjectsRunningInProcessSubagent|GuardsMalformedSubagentLineage)' -count=1 -v
```

Expected: recursive-parentage and/or effective-state assertions fail against the non-recursive builder.

- [ ] **Step 3: Implement one recursive child builder**

Build a `childrenByParent` index once. Use one recursive helper that:

1. marks the current ID in a path-local visited set;
2. sorts direct subagents, then forks, with existing ordering and caps;
3. derives each child through the same node-state/title/ref path as a top-level session;
4. applies effective `active` state when the child's ID appears in the running-child index;
5. recursively assigns `Children`; and
6. skips cycles without hoisting descendants.

Compute rollup/default expansion from the completed subtree. Keep only top-level sessions in project `Current`/`Recent` and attention tiers.

- [ ] **Step 4: Run focused and package tests**

```bash
go test ./cmd/serf-hub/internal/hubcore -run 'TestBuildTree_' -count=1 -v
go test ./cmd/serf-hub/internal/hubcore -count=1
go test ./cmd/serf-hub -run 'Test(Subagent|Thread(Read|List)|APITree)' -count=1
```

Expected: PASS and the two committed production RED tests turn GREEN.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/*tree*test.go cmd/serf-hub/app_threadlist.go cmd/serf-hub/app_threadread.go cmd/serf-hub/app_transcripts.go cmd/serf-hub/*thread*test.go cmd/serf-hub/app_rpc_test.go cmd/serf-hub/cov_threadread_images_fuzz_test.go
git commit -m "fix(hub): preserve recursive active subagent trees"
```

---

### Task 3: Render current and inactive subagent groups in the production sidebar

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js`
- Modify: `cmd/serf-hub/assets/style.css`
- Modify: `cmd/serf-hub/jstest/test-sidebar-children.js`

**Interfaces:**
- Consume recursive API nodes with `kind`, `state`, `ref`, and `children`.
- Produce `isInactiveSubagent(node)`, recursive `pushChildren(out, parent, ancestry)`, and per-parent inactive expansion keys `inactive:<row_id>`.
- Stamp child descriptors with `__child`, `__parentNode`, and `__ancestry` during flattening; do not mutate server-owned node identity fields.
- Current states: `active`, `awaiting`, `idle`, `warning`, `notLoaded`.
- Inactive states: `ended`, `closed`, `errored`.

- [ ] **Step 1: Write production JS RED tests**

Extend `test-sidebar-children.js` using the real `sidebar.js` with a fixture:

```text
main
├─ running child (active)
│  ├─ idle grandchild
│  └─ ended grandchild
├─ idle retained child
├─ errored child
└─ ended child
```

Assert before implementation:

- running and idle direct children render automatically;
- main has `Inactive subagents (2)` collapsed;
- current grandchild renders under its direct parent;
- the child's own inactive disclosure controls only its ended grandchild;
- expanding main inactive does not flatten grandchildren;
- resync preserves expanded inactive state and keyed row identity;
- no vertical lineage rail or left-edge active stripe appears in CSS.

- [ ] **Step 2: Run and verify RED**

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-children.js
```

If that external path is absent, point `NODE_PATH` at an already-installed external jsdom. Expected: current children remain hidden behind the old generic disclosure and recursive assertions fail.

- [ ] **Step 3: Implement the recursive projection**

Replace the generic all-child toggle with:

- direct emission of current children;
- one `Inactive subagents (N)` button for inactive direct children;
- recursive emission under every child;
- expansion keyed by `inactive:<parent-row-id>` in the existing expanded/collapsed storage;
- proper `aria-expanded`, `aria-controls`, and count text;
- normal status icon/text retained for inactive rows.

Use indentation and spacing only. Do not add a lineage border or left-edge selected stripe.

- [ ] **Step 4: Run focused sidebar tests**

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-children.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-aria.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-survivors.js
```

Expected: PASS with pristine output.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-sidebar-children.js
git commit -m "fix(web): separate current and inactive subagents"
```

---

### Task 4: Open subagents beside their direct parent panes

**Files:**
- Modify: `cmd/serf-hub/assets/panes.js`
- Modify: `cmd/serf-hub/assets/sidebar.js`
- Modify: `cmd/serf-hub/jstest/test-panes.js`
- Modify: `cmd/serf-hub/jstest/test-sidebar-children.js`

**Interfaces:**
- Produce `SerfPanes.openAfter(href, title, afterHref)`.
- `afterHref == null` inserts the pane first in `#side-panes`, immediately after the main workspace.
- A non-null `afterHref` inserts after that normalized parent pane.
- Existing hrefs focus and return their pane without reordering or duplication.
- Sidebar activation uses the stamped `__ancestry` to open missing ancestors in order and then the selected child.

- [ ] **Step 1: Write pane and sidebar RED tests**

In `test-panes.js`, assert:

```javascript
P.openAfter("/thread/child", "child", null);
P.openAfter("/thread/sibling", "sibling", null);
// sibling is first side pane; child shifts right
P.openAfter("/thread/grandchild", "grandchild", "/thread/child");
// grandchild is immediately after child
P.openAfter("/thread/grandchild", "grandchild", "/thread/child");
// still exactly one grandchild and its iframe receives focus
```

Assert `MAX_SIDE_PANES` remains enforced and URL/localStorage ordering matches DOM ordering.

In `test-sidebar-children.js`, stub `SerfPanes.openAfter`, click a current child and nested grandchild, and assert ancestry calls use encoded source-qualified refs in parent-to-child order. Assert ordinary main-session rows retain HTMX navigation.

- [ ] **Step 2: Run and verify RED**

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-panes.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-children.js
```

Expected: `openAfter` is missing and child row clicks navigate rather than opening panes.

- [ ] **Step 3: Implement parent-relative insertion**

Refactor pane creation into one internal function used by `open` and `openAfter`. Normalize all href comparisons. For new panes:

- focus existing before checking the cap;
- enforce the existing cap;
- insert before the parent's next sibling, or prepend when `afterHref` is null;
- preserve loading/error UI, persistence, URL ordering, minimum width, and restore behavior.

In the sidebar, intercept activation only for rows stamped `__child` when `window.SerfPanes` exists. Prevent ordinary link navigation, open missing ancestors with `openAfter`, then open/focus the target. Keep the row menu unchanged.

- [ ] **Step 4: Run focused JS tests**

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-panes.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-panes-url.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-panes-error.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-children.js
```

Expected: PASS with no warnings.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/panes.js cmd/serf-hub/assets/sidebar.js cmd/serf-hub/jstest/test-panes.js cmd/serf-hub/jstest/test-sidebar-children.js
git commit -m "fix(web): open subagents beside parent panes"
```

---

### Task 5: Verify the production behavior end to end

**Files:**
- Modify tests only if verification exposes a real uncovered production defect.

**Interfaces:**
- Consume the completed production behavior from Tasks 1-4.
- Produce clean focused Go/JS/browser evidence and a clean worktree.

- [ ] **Step 1: Run production backend tests**

```bash
go test ./cmd/serf-hub/internal/hubcore -count=1
go test ./cmd/serf-hub -run 'Test(Subagent|Thread(Read|List)|APITree|Web_.*Subagent)' -count=1
```

Expected: PASS.

- [ ] **Step 2: Run production frontend tests**

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-children.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-aria.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-sidebar-survivors.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-panes.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-panes-url.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-panes-error.js
```

Expected: PASS with pristine output.

- [ ] **Step 3: Run the real Web UI browser scenario**

Build and launch the hub against deterministic fixture/session data, or use the existing scripted Web UI test harness. Verify:

1. a running in-process child appears directly under its main session;
2. an idle retained child remains current;
3. ended/errored children appear only under `Inactive subagents (N)`;
4. nested children remain under their direct parent;
5. clicking a child creates the first side pane;
6. clicking a grandchild inserts it after its parent pane;
7. clicking an open child focuses it without duplication; and
8. the three-pane cap remains honest.

Capture DOM assertions or screenshots as evidence; do not rely only on visual inspection.

- [ ] **Step 4: Run repository checks**

```bash
git diff --check
git status --short --branch
git diff --name-only $(git merge-base main HEAD)..HEAD
```

Expected: no whitespace errors, no scratch files, and production changes limited to the planned hub files plus approved design/mockup collateral and tests.

- [ ] **Step 5: Commit any test-only verification fix**

Only if Step 3 exposed a production defect and a new RED test was added first:

```bash
git add <focused production files and tests>
git commit -m "fix(web): complete subagent sidebar integration"
```
