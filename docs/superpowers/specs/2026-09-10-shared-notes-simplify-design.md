# Shared notes: atomic saves and shared drafts

Approved by Jesse on 2026-09-10 for PR #1070 (`shared-notes`).

## Goal

Keep the shared-notes feature while replacing its competing delivery and editor coordinators. A human-note save durably accepts the canonical note and its notification together. The editor saves 10 seconds after a dirty blur, unless an editor for that session regains focus.

## Human-note transaction

Use the existing client-mutation store and `executeAtomic`. The effect snapshot contains the canonical human note, one typed pending steering entry, and the mutation result/receipt. The request has one client mutation ID throughout dispatch, retry, receipt reconciliation, and delivery. There is no nested steer mutation, adoption link, delivery-pending metadata map, or notes-specific recovery loop.

The reservation snapshot must not change the note or enqueue notification. The effect checks current state, including recovery where prepare does not run. Normalize once on the server using the existing whitespace and Unicode limits. Equal canonical values produce an applied no-op receipt without a new event or steer. Retrying an applied request returns its recorded result without reverting a later note.

An active interrupt fence rejects a changed save before either effect commits. Stop's existing steering-held gate remains authoritative: accepted notification stays parked, and saving does not release Stop. Use ordinary pending-steering reflection, wake, recovery, and consumption. Persist the human-note steering kind at acceptance, rather than annotating a second record afterward.

A pre-commit persistence failure leaves the previous note and queue intact. A lost response or post-rename error may represent a committed save; retry the original payload and ID. “Saved” means durable acceptance, not model consumption. Replaying accepted pending work may wake normal delivery but never enqueue another notification.

## Read authority and preservation

The mutation snapshot owns the human note. Live metadata, model context, notes reads, authoritative thread snapshots, restored sessions, and past-session reads must project that same committed value. Metadata's human-note field is a projection, not a second writer.

Preserve existing stored notes, including an explicitly cleared note. Inspect the existing format before deciding how to seed the new authority. Any necessary compatibility/migration policy requiring approval must be resolved before implementation; do not silently discard data or invent a general compatibility layer.

Agent notes and URLs stay in existing metadata storage. Their persistence lock must cover tentative mutation, durable write, and rollback. Preserve ownership, capability checks, scope enforcement, limits, events, TUI, and past-session display.

Bare filenames retain literal percent sequences. Decode file-URL syntax once at the URL boundary before the existing scope check. HTTP and secure-path behavior remain unchanged.

## Shared editor state

One session-owned draft record serves every mounted editor. It tracks text, edit generation, submitted generation/identity, and error. The existing mutation outbox owns persisted requests, serialization, receipts, and ambiguous retries. Do not add a second FIFO or per-panel save controller.

- Actual edits make a draft dirty. Focus alone never creates a write.
- Dirty blur starts a 10,000 ms timer if no editor for the session remains focused.
- Any editor for the same session gaining focus cancels that timer.
- An unchanged draft has no timer or request.
- When the timer fires, submit the current dirty generation through the existing target-scoped outbox.
- Editing during a pending save preserves the newer generation. A later dirty blur schedules that edit normally.
- Acknowledgment clears only the submitted generation. Older responses and pushes cannot overwrite newer or failed text.
- Incoming authoritative notes replace a clean draft, including while focused. Dirty drafts retain user text.
- Closing a focused panel without a blur retains its draft and does not invent a save. A timer from an actual earlier blur survives panel unmount.
- Failed drafts survive panel close/reopen. Ambiguous requests retain their original identity for retry. Definite rejections remain visible and recoverable.
- Timer execution rechecks session liveness and write capability. It never submits through a stale session/client identity.

Keep the existing panel layout, agent-note rendering, URL list, and removal controls. Display the server's canonical acknowledgment; do not reproduce Go normalization in TypeScript.

## Verification

Use deterministic tests at real store/session boundaries with filesystem, network/RPC, provider, and clock seams only. Read `docs/developing-evener/testing.md` before changing tests.

Backend cases: atomic acceptance; reservation-only crash; pre/post-effect-rename errors; same-ID replay after a newer save; concurrent same-value requests; fence rejection; held steering; restart and cleared-note restoration; live/past/context authority; no duplicate delivery; metadata rollback; literal-percent filenames with an independently constructed file-URL control.

Frontend cases: no request before 10 seconds; submission at 10 seconds; refocus cancellation; two panels sharing one draft/timer; clean focused remote updates; edits/reverts during pending saves; delayed old acknowledgment followed by newer rejection; failed close/reopen; unmount lifecycle; stable retry ID/payload; liveness/capability recheck; outbox receipt/rejoin integration. Use fake timers and real stores with scripted external RPC responses.

Run focused red/green tests for each change, an independent review, `make merge-approval-gate`, `make vet`, and `make test-web-browser` on a Chrome-capable host. Before frontend gates, run Biome on touched files under `src/` only. Report any gate that cannot run as incomplete.

## Delivery

Commit focused changes with named staging. Recheck the remote head before a normal push to `origin/shared-notes`. Preserve concurrent work and unrelated files. Update PR #1070 for review; do not merge into main.
