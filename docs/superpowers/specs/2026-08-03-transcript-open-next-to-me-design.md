# Open activity transcripts next to the current pane

Date: 2026-08-03
Ticket: kata `vvpw`

## Goal

When the web UI displays a structured activity row for a job, delegate, session, or equivalent item with a transcript reference, provide an **open transcript beside** action. On desktop, the action opens that transcript in the pane to the right without replacing the current pane.

This ticket covers activity rows only. It does not change raw JSON or log output.

## Existing architecture

The frontend already provides the required pane and placement seams:

- `openTranscript(ref, parentRef?)` opens the read-only `transcript` pane.
- `openTranscript` uses `paneActions.openBeside`.
- The workspace places additional panes in the desktop secondary group, which Dockview renders to the right of the main group.
- Workspace deduplication focuses an existing pane with matching type and parameters instead of opening a duplicate.
- The transcript pane hydrates through the existing threads store and renders the existing read-only transcript surface.
- On mobile, `StackHost` presents the focused pane as the current full-screen stack behavior.

The feature will reuse these paths. It will not add a route, pane type, transcript store, or alternate hydration flow.

## Interaction

Activity rows render a shared open-transcript icon button when their structured model contains a valid, non-empty `transcriptRef`.

- Accessible name: `Open transcript beside`.
- The icon is a button, not a row-wide link, so its purpose is explicit and keyboard accessible.
- Clicking the button stops propagation before calling `openTranscript`, preventing row selection, disclosure toggling, or other row activation from occurring as a side effect.
- The action preserves any existing parent/session context supplied by the row. Existing back-to-parent behavior therefore remains available for child transcripts.
- Repeated activation of the same reference focuses the canonical existing transcript pane rather than creating another pane.
- Rows without a usable transcript reference render no open action.

The implementation should use the existing open-beside icon treatment and button primitives rather than introduce a second visual language.

## Data flow

1. The activity tree reducer/model supplies a row's existing `transcriptRef`.
2. The row passes that value to the shared action component.
3. The action validates that the value is a non-empty string.
4. On activation, the action calls `openTranscript(transcriptRef, parentRef?)`.
5. The workspace either focuses the matching transcript pane or opens it in the desktop secondary group.
6. The transcript pane loads through the existing connection and thread-store lifecycle.

No activity row needs to know how pane placement, deduplication, or transcript loading works.

## Failure and edge behavior

- Missing, blank, or malformed references produce no action.
- The action does not fetch or validate transcript contents before opening. Loading and unavailable-content states remain the transcript pane's responsibility.
- If loading fails, the existing transcript pane error/loading behavior is used; the activity row does not show a second error surface.
- A missing parent reference does not prevent opening a valid transcript. It only omits the back-to-parent affordance.
- Mobile keeps the current StackHost fallback for now. The action may focus the transcript as a full-screen stack item, but this ticket does not choose between modal, stack, split, or other mobile treatments.

## Scope boundaries

In scope:

- Activity-tree job, delegate, session, and equivalent structured rows with transcript references.
- A shared accessible icon action.
- Desktop right-pane placement through `openTranscript` and `openBeside`.
- Deterministic frontend unit tests.

Out of scope:

- Raw job JSON, raw logs, or tool-output formatting.
- A new transcript pane or data path.
- Mobile UX redesign. A follow-up UX decision is recorded in kata `vvpw`.
- General workspace layout changes.
- New deep-link or URL behavior for transcript panes.

## Testing

Add deterministic frontend tests that prove:

- The action appears for a valid transcript reference.
- The action is omitted for missing, blank, or invalid references.
- The action exposes the agreed accessible name and icon affordance.
- Clicking the action stops row-event propagation and calls the existing transcript opener with the expected reference and parent context.
- Repeated opens reuse/focus the existing transcript pane rather than duplicating it.
- Representative job, delegate, and session activity rows wire their references correctly.

Run the relevant Vitest tests, frontend typecheck, lint, and web build. Run browser layout guards if the row markup or styling changes geometry.
