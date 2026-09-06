# Native parity audit

Product authority is the current web UI and server contracts. The old mobile UI is not a feature reference. Native layout may differ; product behavior must have a current source. Jesse explicitly requested model and reasoning controls inside the composer.

This audit is incomplete. Existing simulator screenshots and native tests do not establish product parity.

## Questions

Reference: cmd/evener-hub/frontend/src/panes/session/composer/askDock/.

- AskQuestionCard sorts recommended options first and uses one text field for either a note or a Something else answer. It does not expose You decide, Skip, or fallback pills. Native currently differs on all these points.
- askDockStore seeds recommended selections. Native currently leaves options unselected.
- AskDock presents one question at a time, maintains the active question, and advances through questions. Native currently stacks every question.
- Web permits sending an untouched single question; a multi-question batch requires answers. Native currently requires every question to have an explicit resolution.
- reconcileBatches freezes sending batches and reconciles newly arriving questions into an open batch. Native currently uses one flattened definition signature; late arrivals and answered-elsewhere changes need equivalent ownership semantics.
- Web suppresses the ordinary composer while an ask is pending. Native still allows ordinary composition.
- Corrected: native answer serialization imports the current web askCompose.ts directly. The previous mobile formatter did not encode headers containing closing brackets or newlines, allowing answer framing to break.

## Sandbox approvals

Reference: cmd/evener-hub/frontend/src/panes/session/transcript/tools/sandboxEscalation.tsx and agent/sandbox/escalation.go.

- Allow/Deny is an existing one-action sandbox escalation operation.
- Corrected: native warning says a command may have run before the sandbox blocked it. The server boolean indicates uncertainty, not proof that execution occurred.
- Native should carry the web's explicit attribution that Evener requested the approval, not the agent.
- Native displays command and output fields supplied by the server. These are additional presentation details, not additional operations.

## Remaining audit

Compare composer model/reasoning capabilities and defaults, queue operations, session management, navigation, transcript projection, and lookbook claims with current web and server counterparts. Reuse of old mobile code does not prove equivalence. Revalidate both native platforms after correcting the question interaction and batch lifecycle.

## First correction verification

Native TypeScript check passed. All 102 native tests across 18 files passed. The header-framing regression failed before the formatter import changed. No fresh native build or simulator verification is claimed for this patch.
