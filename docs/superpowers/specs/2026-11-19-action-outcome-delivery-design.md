# Action-Outcome Delivery for Session Controls

Date: 2026-11-19
Status: Implemented

## Summary

Every session-controls action answers its initiator with an outcome on the
promise. Before this design, an action's answer could only travel the controls'
published state — `pending`, `lastAction`, `error`, `notice` — and a binding
that had been replaced (the reconnect a model pick or a resume causes retires
the controls that started it) may not publish state. RoboRev's open medium was
the sharpest instance: a cold pick's resume resolved a different identity, the
reconnect replaced the controls, the daemon refused the pick, and the sheet
stayed open on silence — the refusal was swallowed at the catch's currency
gate because the answer had no channel that survived the replacement.

The delivered design:

- one `ActionOutcome` type, resolved on the promise every controls action
  returns;
- the controls' published state remains the **behind-screen observer's**
  channel, written only while the binding is current;
- each destination (the model sheet, the session sheet, the composer) renders
  an action's own refusal from its promise, never from `state.error`;
- the destination fence — the identity comparison the old answer depended on —
  is deleted, along with the ref plumbing whose only consumer it was.

## The outcome type

```text
ActionOutcome
  accepted   notice: string | null
  refused    message: string
  notSent    message: string
```

- **accepted** — the action's own yes, with the notice the screen may show. A
  resolved action is accepted even when its own reconnect replaced these
  controls: the answer is owed to whoever asked, not to whoever is current.
- **refused** — the action reached the wire and was refused: the daemon's
  answer, or a confirmation that could not be read. Derived **purely from the
  operation's catch**; nothing re-classifies a refusal from the state the
  answer left behind. The service already throws for superseded and
  rejected picks, so no guesswork is reintroduced at the controls layer.
- **notSent** — the phone's own refusal before anything left it: a catalog
  that no longer carries the pick, another action holding the session, a
  binding that moved on, or nothing to change. Nothing left the phone, so
  the caller may keep the user's text and offer a retry. A catalog miss and
  a busy collision are both `notSent`.

An **initiator never reads its outcome from `ControlsState.error`**: that
channel is the behind-screen observer's, published only while the binding is
current, so a reconnect that replaces the controls would swallow exactly the
refusals this type exists to deliver. The promise is the only channel an
initiator reads.

## What was deleted

The review that amended this design proved each deletion's only consumer was
the mechanism being removed:

- **`landedRef`** — the design originally kept "the ref the accepted pick
  landed on" flowing out of `changeModel` (conversation.ts), through the
  entry's `#runModelPick`, into the controls' `run()`. Its only consumer was
  the destination fence. `changeModel` returns `Promise<void>` again; the
  entry's pick owner returns `Promise<void>`; the fence is gone.
- **The destination fence** — `isCurrent` is `() => boolean` again. The
  screens pass one fence (the binding's), not the two-scope
  `(scope, ref)` comparator that let a retired binding answer for a
  destination it no longer owned. The changeModel-only special case in
  `run()` is gone with it.
- **`attemptControl`'s sniffing** — sessionRestart subscribed to controls
  state to sniff "sent" and read the fenced error publish to detect failure,
  because `run()` lied on the promise. With outcomes it collapses to: call
  the control, map the outcome (`accepted` → "done", `notSent` → "refused",
  `refused` otherwise → "failed").
- **`unsupported.observedVersion`** — no consumer distinguished values.
- **The wire's `ModelControlDesired.requestSequence`** — clients settle a
  desired record by its `clientMutationId`; the daemon's internal issue
  counter stays, but the published record no longer carries the sequence.
  The daemon's desired-matches comparison keeps the internal sequence and
  drops the redundant mutation-id conjunct.
- **`ThreadModelSetResponse.ModelControlVersion`** — only a peer that speaks
  the model-control protocol answers the method at all, so the field could
  only ever be 1. The snapshot stays required and validated.
- **`projectModelControlSnapshot`'s export** — only its own module calls it.

## Per-kind notices

Each completed action's notice comes from a lookup table on `kind`:

| kind | notice |
|---|---|
| rename | Session renamed. |
| compact | Compaction requested. Progress appears in the conversation. |
| shutdown | none — the session's own toast confirms it (ruling 19) |
| forceStop | Runtime stopped. Saved history is available to resume. |
| resume | Runtime resumed. |
| setReasoningEffort | Reasoning effort updated. |
| setVisionModel | Vision model updated. |
| changeModel | Model updated. |

## The destinations

- **ModelSheet** — `closeOn` reads the outcome off the promise. An accepted
  pick closes the sheet and fires its toast; a **refused** pick's message is
  held in local error state, so the sheet shows it and stays open, revealed
  in the header even after the user scrolled away; a notSent the guards
  already explained closes nothing and says nothing new. The local refusal
  clears on the next accepted pick or effort change. `state.error` remains
  the behind-screen observer channel, read for actions this sheet did not
  initiate.
- **SessionInfoSheet** — the rename check is a disposition check. A refused
  rename's message renders under the actions (announced to VoiceOver like
  every failed action) and the typed name stays in the field.
- **The composer** — `submitComposerCommand`'s `/model` refuses as
  **notSent** (`CommandNotSentError`) when authority disappeared or the
  catalog no longer carries the named model, so the text returns to the
  composer. A dispatched refusal keeps the text as the unconfirmed send it
  became. The screen's action-error render gate moved from binding-gated to
  screen-mounted: a resume that resolved a new identity must not swallow
  the command's answer. The rehydrate after a completed command stays
  binding-gated. A hub-answered `WireError` classifies as `refused`; an
  ordinary error stays `unconfirmed` (outcome unknown, check before
  retrying).

## Consumers named by this design

- `mobile-native/src/sessionControls.ts` — the outcome type, the runner, the
  notice table, the publish-if-current state channel.
- `mobile-native/src/session/sessionRestart.ts` — `attemptControl`'s outcome
  map.
- `mobile-native/src/session/ModelSheet.tsx` — the picker's `closeOn`.
- `mobile-native/src/session/SessionInfoSheet.tsx` — the rename.
- `mobile-native/src/composerCommand.ts` — the `/model` notSent refusals.
- `mobile-native/src/screens.tsx` — the single fence, the compact/shutdown
  consumers, the composer's render gate.
- `mobile-native/src/conversation/services/conversation.ts` and
  `mobile-native/src/session/nativeConversationSessions.ts` — the void
  `changeModel` contract.
- `appwire/types_turn.go`, `appwire-client/typescript/modelControl.ts`,
  `reducer.ts`, `transcriptHistory.ts` — the five protocol cuts, with the
  generated TypeScript and protocol reference regenerated in the same commit
  as the type change.

## Testing strategy

Red proofs, written before the fix and green after it:

- **R1** — a refused pick after an identity-changing resume shows its reason
  in the sheet and stays open (`goBack` never called).
- **R2** — a typed `/model` refused across the same resume surfaces in the
  composer's action-error line.
- **R3** — the accepted-pick guards: across an identity-changing resume the
  sheet closes once, toasts once, and the draft stays untouched on the
  original ref.
- **R4** — a refused rename shows its reason in the session sheet.
- **R5** — a notSent `/model` (catalog miss) restores the composer's text; a
  dispatched refusal does not.

The identity-resume tests that built real `SessionControls` with fake fences
now exercise real delivery: the fences are the single binding fence the screen
installs, and the outcome travels the promise. `fakeControls` mocks are
outcome-shaped. No test asserts a fixture's own constant.

## Notes on the design's own amendments

This design was reviewed with fresh eyes before implementation. The review
amended the original draft: `landedRef` was deleted (its only consumer was the
fence the design deleted), sessionRestart was named as a consumer of the
outcome, the fourth swallowed refusal was identified as `compact` (the draft
misnamed it `close`), the catalog-miss and busy-collision classifications
were fixed as `notSent`, and the five protocol cuts above were verified
behavior-preserving and folded in.
