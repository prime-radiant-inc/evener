# Durable start claim recovery audit

## Scope and source

This is a read-only audit of the durable `turn/start` claim-failure path at
source SHA `f06e99bcf09c4ddc52f7773f053da9dc026de324` (`origin/main`). It is a
pre-existing issue and is separate from the #1073 deferred terminal-frame and
identity-replacement changes. No implementation or generated-contract change is
claimed here.

## Observed lifecycle

`AcceptClientMutationStart` first persists an accepted start intent and returns a
successful `turn/start` response whose turn status is `in_progress` and whose
receipt projection is `pending` (`agent/session_client_mutation.go:244-337`).
The RPC handler does not emit a terminal or queue notification at acceptance
(`server/appwire_runtime.go:1468-1503`).

The serve loop then runs `ProcessClientMutationStart`. It invokes its runnable
callback before claiming (`agent/session_client_mutation.go:442-452`), and the
callback marks the turn processing in the server (`cmd/evener/serve.go:1197-1202`).
If the durable claim fails before any transcript or carrier exists, processing
returns without `SessionEnd`, a mutation result, or an error notification. The
unconditional cleanup clears processing and restores the wire state
(`cmd/evener/serve.go:1213-1220`), so the server can become idle while the client
still holds the successful `in_progress` response.

The mutation store distinguishes two persistence outcomes
(`agent/session_client_mutation.go:1254-1275`):

- Before snapshot rename, the accepted state remains accepted. Repeating the
  same mutation ID is idempotent and wakes accepted work
  (`agent/session_client_mutation.go:244-247,327-336`). The serve loop does not
  automatically wake it after the failed claim.
- After snapshot rename, the claimed state is committed even when the write
  returns an error. A claimed start is not runnable by the normal dispatcher,
  and the claim-error caller does not invoke `returnClaimedClientMutationStart`
  (`agent/session_client_mutation.go:726-749,813-835`). The durable mutation is
  normalized back to accepted only by session restore, which also restores its
  reservation (`agent/session_client_mutation_queue.go:1070-1128`), after which
  runner installation wakes it (`agent/session_client_mutation.go:752-760`).

The client projection retains pending accepted or claimed executions for
reconciliation. Accepted input is represented as pending optimistic work, while
a claimed execution remains an authoritative pending entry
(`agent/session_client_mutation_queue.go:35-86`; `cmd/evener-hub/frontend/src/panes/session/composer/queue/pendingReconcile.ts:82-96`).
No internal claim error is converted into a terminal rejection or mutation
outcome.

## Required future regression outcomes

A future fix should preserve the durable accepted intent and avoid a busy retry
loop or fabricated rejection. Deterministic coverage should prove both cases
through the real claim caller:

1. A pre-rename failure leaves the execution accepted with its budget reservation
   and provides a bounded, contract-defined retry/reconciliation trigger.
2. An after-rename failure restores a committed claim to accepted (or otherwise
   proves a finite equivalent recovery), restores its reservation, and makes it
   runnable again without requiring an unrelated daemon restart.
3. A failed claim that produced no carrier does not emit `SessionEnd`, but the
   successful `in_progress` response must not leave the live client permanently
   divergent from the server’s idle state.
4. Repeating the original mutation ID must remain idempotent and must not create
   a second logical turn.

The existing direct store/readback tests do not establish these live caller,
retry-trigger, or subscriber-convergence contracts. This audit records the
pre-existing behavior for a separate follow-up; it does not select or implement
the recovery design.
