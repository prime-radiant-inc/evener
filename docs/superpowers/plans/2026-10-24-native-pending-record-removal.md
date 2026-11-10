# Native pendingMutation removal

Status: draft. User approved proceeding on 2026-10-25. This is phase 2b of
`2026-10-24-client-state-cleanup-design.md`.

## Goal

Remove `ConversationMutationState.pendingMutation` as a second in-flight record
without weakening Stop/interrupt, failed-send retention, out-of-order
completion, or the click-to-commit window.

## What exists today

`pendingMutation` is a store record written before a mutation is durable and
retained as `failed` after a terminal failure. Five readers have no durable
replacement:

1. Stop/interrupt click guard (`screens.tsx:2398`, plus the render-local
   `stopBusy`).
2. Failed-error retention (`conversation.ts:2455`, `:3687`, `:3697`).
3. Trailing-reread staleness (`conversation.ts:1317`, `:1346`, driven by
   `mutationOwnerRev` at `:1744`).
4. Durable completion identity (`conversation.ts:3115`, `:3143`, `:3211`,
   `:3231`, `:3291`, `:3656`, `:3666`).
5. Click-to-commit for Send/Queue/answer/Stop (`screens.tsx:851`, `:1891`,
   `:2132`, `:2588`).

The outbox projection covers post-enqueue rows. Draft `unconfirmed` covers the
composer's send acceptance window only. `MutationReceipt.clientMutationId`
already carries the admitted identity.

## Stages

### Stage A: admitted identity

- The native runtime currently returns `undefined` from `submit()` after an
  ordinary enqueue, interrupt, recovery resend, or direct fallback. Stage A
  must add a local admission result that carries the durable row's
  `clientMutationId` through the runtime and submitter, while preserving
  acknowledgement at the enqueue boundary. A service receipt's id is not
  available on the production path.
- Keep a per-attempt token for ownership checks before admission and for
  `MutationNotSentError`; those failures happen before a row exists.
- On a successful admitted submit, record the row identity as the pending
  record's identity (or a store-local `admittedId` beside it).
- Key the completion/out-of-order/not-sent guards to the admitted id.
- Keep `pendingMutation` and all UI behavior.
- Tests: the production runtime admission returns the durable row id; an older
  completion cannot clear a newer mutation; not-sent clears only its own.

### Stage B: click-to-commit marker

- For Send/Queue, use the existing draft `unconfirmed`/`submitting` fact as the
  click-to-commit owner.
- The Stop guard is not Stop-specific: it refuses while ANY send, queue, or
  interrupt is pending. The replacement must carry the same any-mutation fact
  (kind + phase, plus the admitted id when known), not a Stop-only boolean.
  A Stop-only marker would let Stop race an ordinary send.
- Replace the five `pendingMutation?.status` screen reads with those facts.
- Tests: Stop and interrupt during the click window; answer submission;
  repeated Send/Stop; draft preservation.

### Stage C: failed retention

- Move a terminal failure's retained error to `error` plus a minimal
  `failedSubmission` marker (kind, id, draft snapshot).
- Replace `pendingMutation.status === "failed"` reads.
- Tests: failed send retains the error and draft; the next send clears it;
  rehydrate success does not clear a failed mutation's error.

### Stage D: remove the field

- Delete `pendingMutation`, its writes/clears, and the legacy `mutationOwnerRev`
  coupling where the admitted identity or generation already fences.
- Update `screens.tsx`, `conversation.ts`, and tests.
- Run the native suite and the real-hub send/queue journeys twice.

## Non-goals

- No registry API for `ForkScreen`; its one-shot modal service stays.
- No change to the send/queue gate, Retry eligibility, draft persistence, or
  wire routes.
- No removal of persisted-row migration/read-old/write-new windows.

## Open questions

- Is a separate `failedSubmission` marker needed, or can `error` carry the
  failed draft/kind without it?
- Can Stop/interrupt use the same minimal marker as Send/Queue, or do they need
  their own because no draft exists?
- Does the `mutationOwnerRev` trailing-reread fence remain necessary once
  completion uses admitted identities?
