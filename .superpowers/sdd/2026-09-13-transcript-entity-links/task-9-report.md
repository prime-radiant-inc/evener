# Task 9 report — Structured id fields

## Status

Implementation is complete. The Task 9 and full `tools/` test suites are green, and Biome is clean. `npm run typecheck` remains blocked by one Task 8 parallel-writer error in `messages/agentEntityLinks.test.tsx`; the Task 9 type error found on the first run was fixed, and the required paused retry named only that out-of-scope file.

Base confirmed before edits: `2a7d3325ff217514f9e8819d01deb0d0e8adfde3`.

## Files changed

- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx`
  - `job_list` renders each structured row identity with `<EntityRef id={identity} />`.
  - Existing type/status/phase/description text and separators are preserved.
  - No parallel open path was added; a resolved job identity receives the single standard `EntityRef` open control.
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx`
  - Adds real entity-view fixtures and acceptance coverage for two `job_list` rows.
  - Adds the `job_status`/`DelegateStatusBody` acceptance assertion because the preflight established there is no dedicated `delegateStatus.test.tsx` and this file already owns the structured `job_status` cases.
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobWatch.tsx`
  - Both static and expandable list-row identities use `<EntityRef id={row.id} display={clipJobID(row.id)} />`.
  - The existing `rows.map((row) => <WatchListRow key={row.id} ... />)` keeps the full watch id as the React key.
  - Reuses the existing `clipJobID`; no second clipper, navigation path, or lifecycle was introduced.
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobWatch.test.tsx`
  - Adds a resolved long-id watch row case proving the full id resolves to a trigger while the visible text remains clipped.
  - Proves the static watch row has no button and clicking its entity trigger leaves the workspace pane list empty.
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/delegateStatus.tsx`
  - Replaces the header id text with `<EntityRef id={state.id ?? ""} triggerOnly />`.
  - Leaves the footer `OpenTranscriptButton` unchanged as the panel's sole open affordance.
- `cmd/evener-hub/frontend/src/panes/session/transcript/EntityRef.tsx`
  - R11-authorized additive API: optional `triggerOnly?: boolean`.
  - Default/undefined behavior is unchanged. `triggerOnly` retains normal context resolution, focusable hover-card trigger, card, portal, placement, and aria behavior while omitting only `OpenButton`.
- `cmd/evener-hub/frontend/src/panes/session/transcript/EntityRef.test.tsx`
  - Adds focused R11 coverage proving a resolved `triggerOnly` delegate has a focusable trigger, no button, and still shows its card after the existing 300 ms focus delay.
- `.superpowers/sdd/2026-09-13-transcript-entity-links/task-9-report.md`
  - This report.

## R11 plan-gap ruling

The reviewed Task 7 `EntityRef` interface had no trigger-only mode, while the binding spec requires the delegate status header to carry only a trigger and retain its existing footer open control. The caller authorized R11: add optional `triggerOnly`, preserve today's default, test it directly, and make the `EntityRef` change after the unambiguous tools work. That ordering and scope were followed.

## TDD evidence

### RED — structured renderer cases

Command:

```text
npx vitest run src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/jobWatch.test.tsx
```

Result before implementation: exit 1; 2 files failed; 3 new tests failed and 146 existing tests passed. Each new case failed because `[data-testid="entity-trigger"]` did not exist:

- `job_status: delegate ID is a trigger and the footer remains the single Open control`
- `job_list: rows render one entity ref and exactly one open control per identity`
- `list watch IDs are clipped entity triggers with no open control or navigation`

### RED — R11

Command:

```text
npx vitest run src/panes/session/transcript/EntityRef.test.tsx -t triggerOnly
```

Result before the API change: exit 1; the new test received an `Open delegate transcript` button instead of `null`, while the other 15 tests were skipped by the filter.

### GREEN

After implementation and again after Biome formatting:

```text
npx vitest run src/panes/session/transcript/EntityRef.test.tsx src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/jobWatch.test.tsx
```

Result: exit 0; 3 files passed; 165 tests passed.

Required focused command:

```text
npx vitest run src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/jobWatch.test.tsx
```

Result: exit 0; 2 files passed; 149 tests passed.

Required complete tools command (also rerun after formatting):

```text
npx vitest run src/panes/session/transcript/tools/
```

Result: exit 0; 20 files passed; 484 tests passed.

## Acceptance-rule pins

- One entity ref per `job_list` identity: `jobTools.test.tsx` test `job_list: rows render one entity ref and exactly one open control per identity` renders two resolved rows and asserts one `entity-trigger` per row.
- Exactly one job row open control: the same test asserts one `Open job log` button inside each resolved row.
- Watch trigger, clipping, and no button: `jobWatch.test.tsx` test `list watch IDs are clipped entity triggers with no open control or navigation` resolves a full long id, asserts the clipped trigger text, and asserts no button in the static row.
- Watch trigger does not navigate: the same test clicks the exact entity trigger and asserts `workspaceStore.getState().panes` remains empty.
- Delegate status single footer control: `jobTools.test.tsx` test `job_status: delegate ID is a trigger and the footer remains the single Open control` asserts the header trigger text and exactly one `/Open/`-named button in the panel. It lives in `jobTools.test.tsx` because that file already owns all `job_status`/`DelegateStatusBody` acceptance cases and no dedicated test file exists.
- Trigger-only contract itself: `EntityRef.test.tsx` test `triggerOnly keeps the resolved hover-card trigger and omits its OpenButton` pins no button plus retained delayed hover card.

## Formatting and verification

- `npx biome check --write` named all seven touched frontend source/test files: exit 0; checked 7 files and fixed 1.
- `npx biome ci src`: exit 0; checked 1191 files; no fixes applied.
- `git diff --check -- <seven Task 9 frontend paths>`: exit 0.
- `npm run typecheck` first reported one Task 9 fixture omission and one Task 8 parallel-writer omission. After adding Task 9's required `description` and `startedAt` fixture fields, the required retry after a 2-second pause reported only:

```text
src/panes/session/transcript/messages/agentEntityLinks.test.tsx(60,9): error TS2741: Property 'startedAt' is missing ...
```

Per the task's parallel-writer rule, this is outside Task 9 scope and was not edited or reverted. The typecheck gate therefore did not exit zero and is reported as incomplete rather than green.

## Self-review

- No `job_list` row has a parallel second open action: resolved identities expose exactly the one standard `EntityRef` control tested per row.
- Watch identity resolution receives the full `row.id`; only `display` uses the existing `clipJobID`. The full id remains the existing React row key.
- Watch entity views have no open target, so their `EntityRef` renders the focusable trigger/card without an `OpenButton`; the click test confirms no navigation.
- Existing expandable watch-row detail buttons remain unchanged; no watch open control was introduced.
- Delegate header uses R11 `triggerOnly`, so it contributes no open button; the unchanged footer remains the only `/Open/`-named button.
- `triggerOnly` is optional and its conditional changes only button emission, leaving all 15 pre-existing `EntityRef` cases green.
- No protocol shape, store subscription, navigation descriptor, formatter, classifier, or floating-card lifecycle was added.

## Concerns

- Verification concern only: `npm run typecheck` is not green because Task 8's concurrent `messages/agentEntityLinks.test.tsx` fixture is missing required `ActivityJob.startedAt`. All failures in Task 9-owned files were fixed; all Task 9 and `tools/` tests and Biome checks pass.
