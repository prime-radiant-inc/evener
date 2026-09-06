# Native parity audit

Product authority is the current web UI and server contracts. The old mobile UI is not a feature reference. Native layout may differ; product behavior must have a current source. Jesse explicitly requested model and reasoning controls inside the composer.

This audit is incomplete. Existing simulator screenshots and native tests do not establish product parity.

## Questions

Reference: cmd/evener-hub/frontend/src/panes/session/composer/askDock/.

- AskQuestionCard sorts recommended options first and uses one text field for either a note or a Something else answer. It does not expose You decide, Skip, or fallback pills. Corrected in native: recommended-first options, Something else, one shared field, and removal of those obsolete controls.
- askDockStore seeds recommended selections. Corrected in native: seed untouched answers without replacing edits or deliberately cleared choices.
- AskDock presents one question at a time, maintains the active question, and advances through questions. Corrected in native: a question tab strip and one visible question, with forward/wrap progression and single-choice auto-advance. Active-tab restoration across a full remount remains open.
- Web permits sending an untouched single question; a multi-question batch requires answers. Corrected in native: permit an untouched single question and require non-null resolutions in multi-question batches, including the web empty free-answer behavior.
- reconcileBatches freezes sending batches and reconciles newly arriving questions into an open batch. Native now calls the current web reconciler and tracks submitting/accepted batches by question identity. Automated tests verify frozen membership, late-arrival separation, exclusion of accepted questions, stale/repeated submission rejection, and failed-submit retention. Native race testing and independent interaction with a second batch during an in-flight send remain open.
- Web suppresses the ordinary composer while an ask is pending. Corrected: native hides ordinary input and send/steer/queue actions while questions are pending, and rechecks the live pending set at dispatch. The ordinary draft is retained.
- Corrected: native answer serialization imports the current web askCompose.ts directly. The previous mobile formatter did not encode headers containing closing brackets or newlines, allowing answer framing to break.

## Sandbox approvals

Reference: cmd/evener-hub/frontend/src/panes/session/transcript/tools/sandboxEscalation.tsx and agent/sandbox/escalation.go.

- Allow/Deny is an existing one-action sandbox escalation operation.
- Corrected: native warning says a command may have run before the sandbox blocked it. The server boolean indicates uncertainty, not proof that execution occurred.
- Corrected: native explicitly attributes the approval request to Evener, not the agent.
- Native displays command and output fields supplied by the server. These are additional presentation details, not additional operations.

## Remaining audit

Compare composer model/reasoning capabilities and defaults, queue operations, session management, navigation, transcript projection, and lookbook claims with current web and server counterparts. Reuse of old mobile code does not prove equivalence. Revalidate both native platforms after correcting the question interaction and batch lifecycle.

## First correction verification

Native TypeScript check passed. All 102 native tests across 18 files passed. The header-framing regression failed before the formatter import changed. No fresh native build or simulator verification is claimed for this patch.

## Question interaction correction verification

The three new behavioral tests failed before implementation. Native TypeScript and all 105 tests pass; targeted Biome passes. An iOS Metro production export succeeds, including the direct current-web formatter import. This proves bundling, not a native build or manual interaction. Manual iOS/Android verification of the corrected controls is still required. Batch reconciliation, active-tab restoration across remount, and ordinary composer suppression are still open.

## iOS native manual correction check

Built and installed a Release app containing the revised question controls and ordinary composer suppression. Created a fresh session from the native UI against the isolated scripted provider with marker NATIVE-ASK-HARNESS current web parity controls. The actual harness emitted ask_user (call_fakellm_3). Verified ordinary Message/Send absent while pending, Focused recommended option checked, Something else changing the same field from note to answer, and a written answer submitted once. The provider log recorded that exact free answer; the sheet closed and ordinary Message returned after the pending question resolved. This was an actual harness question, not injected AppWire question data.

The check found stale transcript guidance saying reply below. That copy is corrected in source after the tested build; the copy-only correction has not been rebuilt. Multi-question navigation, race/batch semantics, and Android validation remain outstanding.

![iOS real harness answer and restored composer](assets/parity-question-ios-answer.png)

## Batch ownership correction

QuestionBatches wraps the current web reconcileBatches function; the conversation store subscription feeds current question snapshots into it. Submission rechecks the batch, freezes it when the durable dispatch starts, and settles only that batch on accepted send. The ordinary draft delivery checkpoint remains in use, including uncertain-delivery blocking. This replaces the single answered-definition marker. The sheet still displays the first batch, and a newly arriving batch is presented after that batch settles; simultaneous independent batch interaction remains incomplete.

Native TypeScript, targeted formatting, and 107 tests across 19 files pass. The two ownership tests were added before the implementation. These tests exercise the actual shared reconciler, not a copied algorithm. Previously recorded manual iOS evidence predates this ownership change; it is not manual race evidence.
