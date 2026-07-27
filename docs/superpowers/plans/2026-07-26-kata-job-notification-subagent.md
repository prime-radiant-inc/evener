# Job Notification Subagent Link Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make completed job-notification cards link valid child transcript references through the existing open-beside action, while presenting parsed notification data as readable full-width transcript content.

**Architecture:** Preserve the existing structured notification parser and card boundary. Extend the parser with validated semantic fields from the XML-like attributes, including `transcript_ref`; centralize the existing transcript-pane opener and quiet open-beside button so delegate rows and notification cards use one action path; pass no new navigation state through rail or steering routing. The card will render one compact header, semantic metadata, normal-text output/excerpt, and exactly one full-width raw-payload disclosure.

**Tech Stack:** React 19, TypeScript, Vitest, Testing Library, CSS Modules, Zustand workspace store, existing `Button`, `OpenBesideIcon`, `paneActions.openBeside`, and transcript pane registration.

## Global Constraints

- Keep default tests deterministic and offline; exercise the real workspace store and pane action, not a mocked internal navigation implementation.
- Valid references follow the appwire ref grammar: one qualified `source:thread` pair using `[A-Za-z0-9._~-]+`, with no `..` in the thread part.
- `remote:<thread>` remains supported because the existing transcript pane accepts qualified refs from any registered source; malformed, missing, or empty references render no action.
- Reuse the existing transcript pane (`type: "transcript"`) and `openBeside` action so parent/main-pane placement, mobile fallback, and deduplication remain inherited behavior.
- Render literal raw payload only in one secondary disclosure; do not add nested disclosures or make XML/preformatted content the primary message.
- Keep the raw disclosure a normal block/full-width row; do not touch rail code or steering routing/navigation paths.
- Use sentence-case accessible labels and the existing quiet button/icon grammar.

## File Map

- Modify `cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts`: retain semantic job attributes, validate `transcript_ref`, and expose parsed status/output fields.
- Modify `cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts`: prove local, remote, malformed, missing, and semantic parsing contracts.
- Create `cmd/serf-hub/frontend/src/panes/session/transcript/openTranscript.tsx`: own the shared transcript-pane opener and the exact quiet open action used by delegate rows and notification cards.
- Modify `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx`: replace its private opener/button markup with the shared action without changing delegate behavior.
- Modify `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx`: retain a real behavior assertion that the shared action opens the transcript pane with the expected child identity.
- Modify `cmd/serf-hub/frontend/src/panes/session/transcript/messages/NotificationCard.tsx`: render semantic fields, conditionally expose the shared open-subagent action, and keep one raw disclosure.
- Modify `cmd/serf-hub/frontend/src/panes/session/transcript/messages/notificationcard.module.css`: make raw content a block/full-width row and style compact metadata/action content with existing tokens.
- Modify `cmd/serf-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx`: cover real action/navigation identity, fallback behavior, semantic rendering, raw-row structure, and accessibility/disclosure contracts.

### Task 1: Extend the parser with a safe child reference and semantic fields

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts`

**Interfaces:**
- Produces `ParsedNotification.transcriptRef?: string`, plus parsed `jobId`, `jobType`, `status`, `reason`, `outputBytes`, and `exitCode` fields for the card.
- Produces only validated qualified refs; empty/malformed refs become `undefined`.

- [ ] **Step 1: Write the failing parser tests.**

  Add focused tests that assert a completed delegate block with `transcript_ref="local:child"` returns the exact ref, `status="completed"`, `jobType="delegate"`, and numeric `outputBytes`; a qualified `remote:child` ref is retained; missing and malformed refs are `undefined`; and the useful semantic fields do not require reading `rawText`.

- [ ] **Step 2: Run the parser tests and verify the expected red failure.**

  Run:

  ```bash
  cd cmd/serf-hub/frontend && npm exec vitest -- run src/panes/session/transcript/messages/steeringClassify.test.ts
  ```

  Expected: the new assertions fail because the current parser drops attributes and `ParsedNotification` has no semantic reference/field properties. If dependencies are absent, install/use the repository’s documented frontend dependency path before continuing and record the exact command/result.

- [ ] **Step 3: Implement the smallest parser change.**

  Add a ref validator matching `appwire/refs.go`, parse optional integer attributes only when they are finite non-negative integers, and return the semantic values alongside the existing derived title/tone/secondary/message/excerpt fields. Do not change block splitting, observer parsing, or steering routing.

- [ ] **Step 4: Run the focused parser tests and verify green.**

  Run the same Vitest command and confirm zero failures, then run the existing parser test file once more after any refactor.

- [ ] **Step 5: Commit the parser slice.**

  ```bash
  git add cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts
  git commit -m "feat(web): retain job notification metadata"
  ```

### Task 2: Centralize the existing open-subagent action

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/transcript/openTranscript.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx`

**Interfaces:**
- `openTranscript(ref: string, parentRef?: string): void` calls `paneActions.openBeside` with `{ type: "transcript", params: { ref, parentRef? } }`.
- `OpenTranscriptButton` accepts `{ ref, parentRef?, label?: string }`, uses the existing quiet `Button` and `OpenBesideIcon`, stops row propagation, and exposes the supplied accessible label.

- [ ] **Step 1: Add the shared-action behavior test first.**

  Add a test through the existing delegate row that clicks its action and asserts the real `workspaceStore` contains one transcript pane with the exact child ref and the action's established placement/dedup behavior. Keep the test at the workspace boundary; do not assert a rendered command string or implementation source.

- [ ] **Step 2: Run the action test and observe red.**

  Run:

  ```bash
  cd cmd/serf-hub/frontend && npm exec vitest -- run src/panes/session/transcript/tools/subagentModule.test.tsx
  ```

  Expected: the new shared-module seam does not exist yet, so the test either fails to compile or fails to observe the intended shared component/action.

- [ ] **Step 3: Extract the existing opener and button without changing its contract.**

  Move the current `openTranscript` body and its quiet `Button`/`OpenBesideIcon` markup into the shared module. Keep delegate rows’ visible text and accessible label as `Open transcript`; allow the notification card to supply `Open subagent`. Preserve `parentRef` omission when absent.

- [ ] **Step 4: Run the delegate action tests and verify green.**

  Run the focused test file and confirm the existing and new workspace behavior passes with the same transcript pane identity and placement.

- [ ] **Step 5: Commit the shared action slice.**

  ```bash
  git add cmd/serf-hub/frontend/src/panes/session/transcript/openTranscript.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx
  git commit -m "refactor(web): share transcript opening action"
  ```

### Task 3: Render the notification card as semantic, accessible transcript UI

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/NotificationCard.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/notificationcard.module.css`

**Interfaces:**
- `NotificationCard` consumes the expanded `ParsedNotification` and uses `OpenTranscriptButton` only when `transcriptRef` is valid.
- The card exposes stable `data-testid` hooks for its root, semantic fields, raw disclosure, and raw body so tests verify structure rather than CSS source or giant serialized markup.

- [ ] **Step 1: Add the failing component tests.**

  Add tests that:

  - render a valid local ref, click `Open subagent`, and assert the real workspace store opens a transcript pane for exactly `local:child` beside an already-open main pane;
  - render a qualified remote ref and assert the exact remote identity is preserved;
  - render missing and malformed refs and assert there is no open-subagent button;
  - assert status, job type, output byte count, reason/exit details, and excerpt appear as ordinary text while raw XML appears only in the raw body;
  - assert the action has an accessible button name, the raw disclosure has a keyboard-native summary, there is exactly one `details` for the card, and the raw disclosure is a direct full-width card row rather than a nested/side column.

- [ ] **Step 2: Run the component tests and observe red.**

  Run:

  ```bash
  cd cmd/serf-hub/frontend && npm exec vitest -- run src/panes/session/transcript/messages/NotificationCard.test.tsx
  ```

  Expected: the current card has no action, no semantic fields, and two disclosures for long excerpts; the new assertions fail for those missing contracts.

- [ ] **Step 3: Implement the minimal card and CSS changes.**

  Add one compact metadata row using normal text, show the parsed status in the header/metadata without a large transcript link, and render the excerpt/message as text/markdown. Use the shared button for valid refs. Remove the long-excerpt disclosure so the card owns exactly one disclosure, and make the raw `details` a normal block whose `<pre>` is a block with wrapping and `min-width: 0` rather than a flex sibling of its summary.

- [ ] **Step 4: Run focused component/parser/action tests and verify green.**

  Run:

  ```bash
  cd cmd/serf-hub/frontend && npm exec vitest -- run src/panes/session/transcript/messages/NotificationCard.test.tsx src/panes/session/transcript/messages/steeringClassify.test.ts src/panes/session/transcript/tools/subagentModule.test.tsx
  ```

  Confirm all assertions pass and no existing notification/delegate behavior regresses.

- [ ] **Step 5: Commit the card slice.**

  ```bash
  git add cmd/serf-hub/frontend/src/panes/session/transcript/messages/NotificationCard.tsx cmd/serf-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx cmd/serf-hub/frontend/src/panes/session/transcript/messages/notificationcard.module.css
  git commit -m "fix(web): link job notifications to subagents"
  ```

### Task 4: Run the complete verification gates

**Files:**
- Modify: none unless a verification command identifies a defect in the files above.

- [ ] **Step 1: Re-read the plan and inspect the diff.**

  Confirm the diff does not touch rail files, steering routing paths, or unrelated transcript components, and that there is exactly one raw disclosure in the card.

- [ ] **Step 2: Run frontend focused and full tests.**

  Run the focused Vitest command from Task 3, then the frontend full test command from `package.json`/repository docs.

- [ ] **Step 3: Run typecheck, lint, and build.**

  Run the repository’s frontend `tsc --noEmit`, Biome/lint, and production build commands, recording exit codes and failure counts.

- [ ] **Step 4: Run layoutguard and overflowguard.**

  Run `npm run layoutguard` and `npm run overflowguard`; if either identifies a real browser layout regression, fix it with a new failing contract test before rerunning the guard.

- [ ] **Step 5: Run the repository-level required checks.**

  Run the project’s standard full frontend/repository checks that are available without provider credentials, keeping live/provider tests opt-in.

- [ ] **Step 6: Commit any verification-only fix as a logical slice and verify clean state.**

  Run `git status --short --branch`, `git log -1 --oneline`, and inspect `git diff HEAD` before reporting the final HEAD and evidence.
