# Single-Composer Mutation Recovery Design

**Date:** 2026-07-30
**Status:** Approved

## Problem

The WebUI currently clears the composer after a mutation is committed to the
browser outbox. If the server later guarantees that the mutation was not
accepted, the browser moves the payload into a durable recovery record and
renders a second editable textarea under a `Recovery drafts` heading.

That second editor creates a separate draft model beside the real composer.
Users have to decide which editor is current, recovery-only actions duplicate
normal composer actions, and failed submissions no longer follow the same
interaction model as ordinary drafts and queued messages.

## Goals

- Present exactly one editable composer for a session.
- Return an authoritatively rejected submission to that composer.
- Keep later submitted work ordered in the ordinary queue presentation.
- Preserve text and attachment blobs across reloads, crashes, and tab handoff.
- Preserve the existing one-winner resend transaction across multiple tabs.
- Keep unknown outcomes non-sendable until authority resolves them.

## Non-goals

- Removing the durable mutation outbox or its IndexedDB recovery store.
- Treating an unknown mutation outcome as a definite rejection.
- Changing Hub or daemon mutation outcome semantics.
- Reordering later mutations ahead of an unresolved earlier mutation.
- Adding compatibility aliases or migrating older mutation schemas.
- Redesigning the daemon-owned queue protocol or its row actions.

## Design

### One editor

Remove the `RecoveryTray` and its separate recovery textarea. The normal
Composer textarea is the only editable message surface.

When the oldest durable recovery record for a session has
`recoveryKind: "rejected"` and the Composer has no unsent local draft, Composer
loads that record into its normal textarea. The record remains the durable
owner while it is being edited; textarea changes update the same recovery
record instead of creating an independent copy.

Sending a recovered message uses the existing conditional IndexedDB resend
transaction. That transaction consumes the recovery record, allocates the next
intent sequence and mutation identity, creates one outbox record, and preserves
attachment blobs. Concurrent tabs may display the same recovered input, but
only one resend transaction can win.

### Occupied composer and additional rejections

An existing unsent composer draft is never replaced. If a rejection arrives
while the normal composer is occupied, the rejected record appears in the
existing QueueStrip in intent order. Additional rejected records wait there as
well.

Editing one of those rows moves that record into the normal composer when the
user chooses the existing Edit action. It follows the QueueStrip's current
restore behavior: any unsent composer text stays first, then the rejected text
is appended after a blank line. The combined text and attachments become the
durable recovery record's payload, and the independent local draft stops
owning that merged content. If the current draft is submitted or cleared
without editing the recovery row, the oldest rejected record becomes the
composer's active draft as soon as the composer is empty.

Later ordinary submissions remain in the existing daemon or optimistic queue
presentation; they are not merged unless the user explicitly chooses Edit.

No automatic action submits an unsent local draft merely to make room for a
rejected record.

### Attachments

A recovered record presents its attachments through the normal composer's
attachment area. The original durable blobs remain authoritative until resend.
Removing an attachment while editing updates the recovery record. Resend
re-mints presentation identities as it does today and preserves the remaining
blobs.

### Outcome distinctions

`mutationOutcome: "notAccepted"` is authoritative: the mutation did not apply,
so its input is safe to make editable and sendable again.

`blockedUnknown` is not a failed draft. The server may already have accepted
it. It remains a pending QueueStrip entry labeled `Delivery uncertain` with an
explicit Retry action. It is never copied into the sendable composer, because
that would allow a second mutation to duplicate an already-accepted action.
Later intent sequences remain blocked behind it.

A `targetDeleted` record remains durable and appears as a non-sendable queue
entry because its destination no longer exists. Its payload may be copied, but
the WebUI does not imply that retrying against the deleted target is possible.

## Data flow

1. Composer submission commits an outbox record and clears only the unchanged
   submitted composer snapshot, as it does today.
2. The dispatcher receives an authoritative outcome.
3. Applied or replayed outcomes settle normally.
4. A definite rejection atomically transfers the outbox record to the recovery
   store and notifies the session projection.
5. Composer displays the oldest rejected record in its normal textarea when
   that textarea is available; otherwise QueueStrip displays it in order.
6. Edits remain durable in the recovery record.
7. Send conditionally transfers that record back to the outbox under a new
   mutation identity.

The recovery store remains an internal crash-safety mechanism. It is no longer
a separate user-facing draft system.

## Error handling

- A local outbox commit failure leaves the existing composer content untouched.
- A failed recovery-record edit leaves the durable prior value intact and
  reports the storage failure without clearing the composer.
- A lost or unknown network response stays pending and cannot become sendable
  based on elapsed time.
- A losing cross-tab resend refreshes the projection and does not send a second
  mutation.
- Removing the final text and attachment from an active recovered draft
  discards that recovery record through an explicit durable operation; blank
  recovery records do not reappear after reload.

## Testing

Use the real Composer, QueueStrip, mutation dispatcher, threads store, and
fake IndexedDB with a fake AppWire client.

Add deterministic regressions that prove:

- a `notAccepted` response restores the rejected text into the sole Composer
  textarea and no `Recovery drafts` region or recovered-message textarea
  exists;
- an occupied composer is not overwritten and the rejected record appears in
  QueueStrip in intent order;
- editing a rejected QueueStrip row moves it into the normal composer;
- later ordinary queued messages remain ordinary queue rows;
- recovered text edits and attachment removal remain durable across remount;
- resend preserves remaining attachment blobs and only one concurrent tab
  wins;
- `blockedUnknown` appears as `Delivery uncertain` with Retry and never becomes
  sendable composer text;
- a deleted-target record remains durable and non-sendable; and
- blanking an attachment-free recovered draft removes it durably.

Every changed behavior is developed test-first. The focused Composer, queue,
dispatcher, and mutation-outbox suites run before the full frontend test,
typecheck, lint, and build gates.

## Acceptance criteria

- A session renders only one editable message composer.
- A definitely rejected submission is editable in that composer.
- Existing unsent text is never overwritten by asynchronous recovery.
- Later submitted or rejected work remains visibly ordered in QueueStrip.
- Reload, browser crash, and concurrent tabs cannot lose or duplicate recovered
  text or attachments.
- Unknown outcomes cannot be resent as new composer input.
- The WebUI contains no `Recovery drafts`, `Recovered message`, or
  `Send recovered draft` UI.
