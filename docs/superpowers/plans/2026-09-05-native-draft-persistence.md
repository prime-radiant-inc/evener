# Native draft persistence implementation plan

> Use test-driven and subagent-driven development. Jesse delegated routine decisions; execute without additional approval gates.

**Goal:** Retain exact unfinished composition per hub/session across navigation, backgrounding and app recreation, with separate durable uncertain submissions.

**Architecture:** A SQLite repository stores one row per hub/session. A draft document owns local text independently of the transient conversation store. Screens mirror text into the existing store on submission; shared protocol/mutation handling remains authoritative for acceptance. Small synchronous parameterized writes checkpoint text before sending. Credentials remain in SecureStore.

**Spec:** docs/design/mobile/style-guide.md, composer and continuity contracts.

- [x] Repository with real SQLite tests: exact text, reopen, hub/session isolation, deletion, uncertain text.
- [x] Draft document with snapshots: edit, checkpoint, acceptance, explicit recovery and storage-error handling. Verify checkpoint failure prevents sending and newer drafts survive completion.
- [x] Native adapter and screen binding. Disable mutations until load succeeds; never overwrite an unread record. Preserve unsaved text after write failure and expose retry. Delete hub drafts on explicit hub removal.
- [x] Build both targets and manually verify navigation, hub switch, restart, accepted-send clearing and no automatic replay of uncertain sends.
- [x] Independent review, native tests/typecheck and source-linked runtime evidence.

## Boundaries

No AppWire migration or compatibility layer. Older already-lost drafts cannot be recreated. Draft text stays in the device app sandbox, separate from credentials. Measure typing before claiming fluency. Full app functionality, rich rendering and accessibility follow-ups remain active work.

## Outcome

Implemented in `b4d7060cf`. Both-platform runtime checks, review findings and limits are recorded in [draft persistence evidence](../../design/mobile/draft-persistence-evidence.md). Representative-device typing performance remains an app-wide quality task; no fluency claim is made by this slice.
