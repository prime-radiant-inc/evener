# Failed-turn Retry eligibility

## Intent and decision record

This follows the continuation contract in
[the Retry design](2026-10-20-retry-continue-design.md). Jesse decided that
Retry belongs only on the latest failed turn and that repeated presses must
keep one pending continuation, with the button inactive while pending.
He requested implementation of the resulting [pending-submission issue](https://github.com/prime-radiant-inc/evener/issues/4792)
and [latest-turn issue](https://github.com/prime-radiant-inc/evener/issues/4793).

## Scope and non-goals

Apply the two rules to browser and native failed-turn Retry. Preserve native
Resume and Sign in precedence and existing send eligibility. Keep sending the
shared `continue` message through the ordinary mutation and outbox paths.

Do not change queued-message recovery, canceled-message recovery, provider
retries, server admission, wire protocols, or ordinary composer Send semantics.
The Background Jobs cleanup issue and broader outbox-settlement audit remain
separate. No deployment or hub restart is part of this change.

Approved scope: repeated-press protection is per client and session,
including multiple mounted views and reloads of that client. Simultaneous Retry
presses from separate browser tabs or devices are not globally deduplicated.
That stronger guarantee would require a separate server-admission design.

## Behavior

1. Only the actual latest turn's own failure offers Retry. An older failure
   remains readable but has no Retry action, whether a newer turn is active,
   successful, or failed. Include a running turn that has produced no display
   items yet. A newer failure can offer its own Retry.
2. Eligibility follows the current conversation, not the last visible or loaded
   historical row. Refresh, reconnect, and older-page loading preserve the rule.
   The action rechecks eligibility at activation so a stale callback cannot
   submit after the conversation advances. If the loaded window cannot establish
   the live tail, automatically refresh that state before allowing Retry. A
   different running identity, or active status without a running identity, does
   not establish that the displayed failure is the current retryable turn.
3. Once Retry submission begins, further activation cannot create another
   pending continuation for that client and session. Keep an otherwise eligible
   Retry visible but disabled while pending, including the interval before the
   next render. Ordinary composer Send and Queue retain their existing behavior.
4. Keep that protection throughout an unresolved acceptance, lost-reply
   reconciliation, and remount/reconnect. Missing replies never authorize a
   fresh continuation. Reuse the existing durable mutation identity and recovery
   ownership; a component-local busy flag alone cannot be the recovery owner.
   Cover durable enqueue, the interval before its pending projection refreshes,
   wire dispatch, and accepted-but-unreflected delivery, whether the continuation
   was sent directly or queued by the existing routing.
   Pending state that has not loaded is unknown, not empty: automatically load it
   and keep Retry inactive until its owner can establish availability. Another
   unresolved send or queue submission from this client also prevents Retry from
   acting on a stale failure; its payload need not be a continuation. This rule
   concerns unresolved submission, not a change to queued-message recovery.
5. Acceptance permits ordinary conversation progress but does not by itself
   re-enable Retry on the older failure. A definitive refusal retains the
   existing usable recovery path. Once delivery is settled and the current
   conversation is known, an otherwise eligible latest failure can be acted on
   again. Existing explicit recovery for a failed local-storage write remains
   unchanged; normal durable lost-reply recovery must complete automatically.
6. Preserve the composer text, structured selections, and staged image
   attachments on acceptance, refusal, and reconciliation. Do not consume a
   draft or replay the failed prompt. Existing attachment-lifetime limitations
   remain unchanged; this change does not add durable image storage.
7. Native's existing render-time latest-display-turn check and draft submission
   guard are starting points, not proof of the complete rule. Preserve Resume
   and Sign in precedence while extending Retry's identity and pending checks.

## Ownership and implementation constraints

The existing conversation/thread store supplies turn identity and admission.
The existing mutation and outbox owners supply unresolved delivery and recovery
state. Renderers project these owners and existing widgets express availability.
Do not invent a parallel persistent retry queue or expand protocol contracts.
Shared rules belong in the existing shared client when both clients need them;
client-specific wiring stays in its client. Reuse the existing pending-send
projection rather than identifying Retry by its message text or adding a second
durable marker. Avoid new design-system widgets or widget props. Internal
transcript component props may carry source-turn identity and disabled state.

Update `docs/daily-work.md` and the relevant browser/native rows in
`docs/product/subsystems.md` with verified current behavior.

## Whole-journey proof

Use deterministic provider/network boundaries and real Evener code below them.
For each affected surface:

- Start with a failed latest turn and a nonempty draft with an attachment.
  Hold Retry submission, activate again before and after rendering, and assert
  one submitted continuation, one mutation identity, and an inactive action.
- Lose the acceptance reply, reconnect/remount while unresolved, and attempt
  Retry again. Assert no new submission identity, one accepted continuation,
  durable pending settlement, preserved draft, completed recovered output, and
  restored useful operation. Cover remount both during local enqueue and during
  wire dispatch, including a binding replacement before enqueue returns.
- Delay the first durable pending-state read after remount. Assert Retry stays
  inactive, then automatically reflects loaded pending or empty state. Also hold
  the new turn's events after acceptance to cover accepted-but-unreflected state.
- Refuse submission, then exercise the existing explicit recovery and prove
  that useful operation resumes without duplicating the original input.
- Advance the conversation after a failure to a running, successful, and failed
  newer turn. Assert older Retry actions disappear and stale activation does
  nothing. The running case must first have zero display items. A new latest
  failure remains retryable when otherwise eligible.
- Load older history and refresh/reconnect. Assert historical rows never gain
  Retry just because they are the last visible row.

Write regression tests first and observe their behavioral failures. Preserve
existing continuation, image-only, missing-earlier-input, and draft tests.
Assertions must inspect disabled state, mutation identity/counts, submitted
payloads, durable pending state, and transcript outcomes, not prose fragments.
Run targeted suites, both client typechecks, and touched-file formatting.
Extend the existing real-hub Retry guard to supply this journey evidence; its
current single-press scenarios alone do not prove these rules. CI owns full gates.

## Delivery

Use independent specification and implementation PAR, then simplify and
shepherd one combined PR for the two eligibility rules. Both rules touch the
same actions and must be verified together. Merge only under repository policy,
verify the merged content, and remove only owned worktrees and scratch.
