# Open activity transcripts next to the current pane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an accessible open-transcript-beside icon to structured activity rows for sessions, delegates, and jobs, with desktop right-pane placement through the existing transcript opener.

**Architecture:** Carry each activity node's transcript reference through the existing jobs activity-tree wire contract. Parse it into the frontend activity model, then render one shared icon action from `ActivityTree` for session, delegate, and shell-job nodes. The action calls `openTranscript(ref, parentRef?)`, preserving existing pane placement, deduplication, hydration, and mobile StackHost fallback.

**Tech Stack:** Go appwire/activity projection, React 19, TypeScript, Vitest, Testing Library, existing workspace/transcript pane APIs.

## Global Constraints

- Scope is structured activity rows only; do not change raw JSON, raw logs, or tool-output formatting.
- Desktop opens the transcript in the pane to the right through `openTranscript` and `openBeside`.
- Reuse the existing transcript pane, workspace deduplication, and threads-store hydration paths.
- Missing, blank, or malformed transcript references render no action.
- The accessible action name is `Open transcript beside`.
- Clicking the action stops row-event propagation before opening the transcript.
- Mobile UX redesign is out of scope; retain the current StackHost fallback and document the follow-up in kata `vvpw`.
- Tests must remain deterministic and must not use provider credentials or network access.

---

### Task 1: Add transcript references to the activity-tree contract

**Files:**
- Modify: `appwire/types.go:1208-1226` (`JobActivityJob`)
- Modify: `agent/jobs_activity.go:973-1011` (`projectActivityJob`)
- Test: `agent/jobs_activity_test.go` (activity projection fixtures/assertions)
- Test: `agent/jobs_activity_past_test.go` (persisted activity projection if its fixture assertions enumerate job fields)

**Interfaces:**
- Produces `JobActivityJob.TranscriptRef string `json:"transcriptRef,omitempty"`` for the frontend activity tree.
- Consumes `jobstore.JobRecord.TranscriptRef` through `projectActivityJob`.

- [ ] **Step 1: Write the failing Go test**

Add a projection assertion using an existing activity-tree fixture whose job record has `TranscriptRef`, or construct a minimal record passed through the existing projection test helper. Assert the returned job has the same `TranscriptRef` and that JSON encoding uses `transcriptRef`.

```go
if got := activity.Entries[0].Job.TranscriptRef; got != "job:job_activity" {
    t.Fatalf("TranscriptRef = %q, want %q", got, "job:job_activity")
}
raw, err := json.Marshal(activity.Entries[0].Job)
if err != nil { t.Fatal(err) }
if !strings.Contains(string(raw), `"transcriptRef":"job:job_activity"`) {
    t.Fatalf("encoded activity job omitted transcriptRef: %s", raw)
}
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run: `go test ./agent -run 'Test(JobActivityTree|LoadSessionJobActivityTree)' -count=1`

Expected: FAIL because `JobActivityJob` does not expose the transcript reference.

- [ ] **Step 3: Implement the minimal wire/projection change**

Add the optional `TranscriptRef` field to `appwire.JobActivityJob`. In `projectActivityJob`, copy `rec.TranscriptRef` into the field. Do not alter the existing activity-tree bounds, traversal, or flat compatibility behavior.

- [ ] **Step 4: Run the focused tests and verify they pass**

Run: `go test ./agent -run 'Test(JobActivityTree|LoadSessionJobActivityTree)' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the contract change**

```bash
git add appwire/types.go agent/jobs_activity.go agent/jobs_activity_test.go agent/jobs_activity_past_test.go
git commit -m "feat: expose transcript refs in activity jobs"
```

---

### Task 2: Parse transcript references in the frontend activity model

**Files:**
- Modify: `cmd/serf-hub/frontend/src/protocol/types.gen.ts` (`JobActivityJob`)
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/activityData.ts:18-36,160-220` (`ActivityJob` and `parseJob`)
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/activityData.test.ts`

**Interfaces:**
- Produces `ActivityJob.transcriptRef?: string` for valid non-empty wire values.
- Consumes the generated wire field `transcriptRef?: string`.

- [ ] **Step 1: Write failing parser tests**

Add tests proving a valid `transcriptRef` survives `parseActivityTree`, while an empty or non-string field is rejected as an incomplete/malformed job under the parser’s existing optional-field conventions.

```ts
const tree = parseActivityTree(treeFixture({ job: { transcriptRef: "job:job_activity" } }));
expect(tree?.root.entries[0]).toMatchObject({
  kind: "shell",
  job: { transcriptRef: "job:job_activity" },
});
```

Use the existing fixture builders in `activityData.test.ts`; keep the tests pure and deterministic.

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/activityData.test.ts`

Expected: FAIL because the generated/model types and parser do not retain `transcriptRef`.

- [ ] **Step 3: Implement the minimal parser change**

Add `transcriptRef?: string` to the generated frontend contract and read it with `readOptionalString(raw, "transcriptRef")`. Reject a present non-string value using the same validation style as `command`, `task`, and `reason`; omit an empty string.

- [ ] **Step 4: Run the focused tests and verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/activityData.test.ts`

Expected: PASS.

- [ ] **Step 5: Commit the parser change**

```bash
git add cmd/serf-hub/frontend/src/protocol/types.gen.ts cmd/serf-hub/frontend/src/panes/session/chrome/activityData.ts cmd/serf-hub/frontend/src/panes/session/chrome/activityData.test.ts
git commit -m "feat: parse activity transcript references"
```

---

### Task 3: Add the shared activity-row open-transcript action

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTranscriptAction.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTranscriptAction.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.tsx:13-21,139-212,350-429`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/activitypanel.module.css` only if the existing row action layout needs a focused style rule

**Interfaces:**
- Produces `ActivityTranscriptAction({ transcriptRef: string | undefined; parentRef?: string })`.
- Consumes `openTranscript(ref, parentRef?)`, `OpenBesideIcon`, and the existing `IconButton`/button primitive.

- [ ] **Step 1: Write failing component tests**

Test the shared action in isolation with a mocked `openTranscript` seam:

```tsx
render(<ActivityTranscriptAction transcriptRef="job:job_activity" parentRef="local:session" />);
const button = screen.getByRole("button", { name: "Open transcript beside" });
await user.click(button);
expect(openTranscript).toHaveBeenCalledWith("job:job_activity", "local:session");
```

Also test that `undefined`, `""`, and whitespace-only refs render no button, and that a parent row click handler is not called when the action is clicked. Assert the existing open-beside glyph is present using its stable testable markup or accessible-hidden SVG contract.

- [ ] **Step 2: Run the focused test and verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/ActivityTranscriptAction.test.tsx`

Expected: FAIL because the shared component does not exist.

- [ ] **Step 3: Implement the shared action**

Trim/validate the reference at render time. Return `null` for an absent or blank reference. Otherwise render a quiet small icon button with:

- `aria-label="Open transcript beside"`;
- the existing `OpenBesideIcon`;
- `onClick` that calls `event.stopPropagation()` before `openTranscript(transcriptRef, parentRef)`;
- no fetch, loading state, or alternate error UI.

Keep the component presentation-only apart from the existing opener call.

- [ ] **Step 4: Wire action refs into activity nodes**

Extend `RenderNode` with `transcriptRef?: string` and `parentRef?: string`. Set them at construction:

- session node: `transcriptRef: session.ref` and no parent;
- delegate node: `transcriptRef: delegate.childRef`, `parentRef: parent session ref`;
- shell job node: `transcriptRef: job.transcriptRef`, `parentRef` equal to the owning session ref available to the builder. Pass the owning session ref into `buildJobNode`; for delegate turns, use the delegate child/session ref that owns those turns.

Render `<ActivityTranscriptAction ... />` in the row’s trailing/meta action area for every node. Ensure it is inside the row but its click stops propagation. Do not make the action a tab stop in the tree’s roving-focus model unless the existing icon-button convention requires it; keyboard users must still be able to reach the button with normal Tab navigation.

- [ ] **Step 5: Run focused component/tree tests and verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/ActivityTranscriptAction.test.tsx src/panes/session/chrome/ActivityTree.test.tsx`

Expected: PASS, including representative session, delegate, and shell-job rows.

- [ ] **Step 6: Commit the UI action**

```bash
git add cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTranscriptAction.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTranscriptAction.test.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.tsx cmd/serf-hub/frontend/src/panes/session/chrome/activitypanel.module.css
git commit -m "feat: add activity transcript open-beside action"
```

---

### Task 4: Verify desktop placement, deduplication, and the complete web contract

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.test.tsx` if an integration assertion needs a stable fixture adjustment
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/openTranscript.test.tsx` only if a missing existing regression test is needed for duplicate focusing
- Modify: `kata` issue `vvpw` through `kata comment` (not a repository file) to record mobile UX follow-up status

**Interfaces:**
- Consumes the completed Go wire field, parsed frontend model, and shared activity-row action.
- Verifies the existing `openTranscript`/workspace behavior rather than replacing it.

- [ ] **Step 1: Add/confirm desktop workspace assertions**

Use the existing workspace/open-transcript tests to assert opening the same `{ type: "transcript", params: { ref } }` twice leaves one transcript pane and focuses it. Assert a transcript opened while a main session is present is assigned to the secondary/right-side slot through the existing `openBeside` behavior. Do not change placement policy.

- [ ] **Step 2: Run focused frontend verification**

Run:

```bash
cd cmd/serf-hub/frontend
npm test -- --runInBand
npm run typecheck
npm run lint
npm run build
```

Expected: all commands pass. If the repository’s Vitest wrapper rejects `--runInBand`, rerun `npm test` without that extra flag; do not suppress failures.

- [ ] **Step 3: Run repository web gates**

Run from the repository root:

```bash
make test-web
make build-web
```

Run `make test-web-browser` if the row markup or CSS changes geometry, or if the web gate requires the browser suite for the changed activity panel.

- [ ] **Step 4: Record the mobile boundary**

Append a concise comment to kata issue `vvpw` stating that desktop activity-row opening is implemented through the existing transcript pane, while mobile retains StackHost behavior and needs a separate UX decision.

- [ ] **Step 5: Inspect final repository state**

Run:

```bash
git status --short --branch
git diff main...HEAD --stat
git log --oneline --decorate -8
```

Expected: only the design spec, implementation files, tests, and intentional commits are present; no generated dependencies, scratch files, or unrelated edits remain.

- [ ] **Step 6: Commit any final test-only adjustment**

If Step 1 required a test-only adjustment, commit it separately:

```bash
git add cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.test.tsx cmd/serf-hub/frontend/src/panes/session/transcript/openTranscript.test.tsx
git commit -m "test: cover activity transcript pane reuse"
```
