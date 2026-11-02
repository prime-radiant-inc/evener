# Client-state send/queue gate implementation plan

Plan for phase 3 of [the client-state cleanup design](../specs/2026-10-24-client-state-cleanup-design.md),
send/queue slice. Branch `client-state-cleanup-sendgate`, based on `origin/main`
`3b249841a6`. Status: proposed, no production code changed.

The Retry eligibility gate (`appwire-client/typescript/retryEligibility.ts`,
landed in #4961) is the precedent: one pure decision, a reason for every refusal,
and client-specific facts supplied as inputs. This plan applies that shape to
Send/Queue.

## 1. What is actually duplicated

The shared table is not duplicated. `deriveSendQueueAvailability`
(`appwire-client/typescript/sendQueueAvailability.ts:92`) is one implementation
with one owner of every tier: restart, ended/closed, active, this client's
pending send, plain send. Both clients call it. The duplication is the
composition around it.

Both clients independently do all of the following:

- derive the tier-6 input with `ownPendingSend(pendingMutations)`
  (`state/mutation/pendingEntries.ts:138`);
- call the table and map its two booleans to an action with Queue first
  (native inline at `mobile-native/src/session/sendAction.ts:47-58`; the
  browser through `appwire-client/typescript/submitRouting.ts:18-23`);
- special-case `restartRequired` and `SHUT_DOWN_STATUSES` and then add a
  "first message resumes" substitution
  (`sendAction.ts:33-44,53-57,80-89`;
  `cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx:799-816`);
- require the `send` capability for that shut-down substitution
  (`sendAction.ts:57,82`; `Composer.tsx:811-812`, via
  `controlsFor(target).send`, which for a shut-down status is exactly
  `capabilities.send`);
- put a client-specific recovery fence above the table and decide what a
  pending send does inside that fence
  (`sendAction.ts:33-44,68-89`; `Composer.tsx:762-798`).

The table's tier rules stay untouched. The proposal is one shared composition
that owns ordering, shut-down conversion, capability application, and the
action/reason result; each client keeps its own recovery and connectivity facts
and passes them in.

### Current call sites

| Call site | What it does | This slice |
|---|---|---|
| `appwire-client/typescript/sendQueueAvailability.ts:92` | The table. One owner. | Keep unchanged. |
| `mobile-native/src/session/sendAction.ts:16` | Native action: resume fence, offline, shut-down resume, capability, table. | Migrate to the gate (Stage 1). |
| `Composer.tsx:762` (`availabilityFor`) | Browser availability: recovery fence, table, shut-down send substitution. | Migrate to the gate (Stage 2). |
| `Composer.tsx:1380` (`decideSubmitRoute`) | Queue-first mapping of the two booleans at submit. | Replace with the gate's action after Stage 2; keep the function exported until the SDK question in §8 is answered. |
| `mobile/src/state/conversation.ts:549` (`requireQueue`) | Queue precondition only; no fence, offline, or shut-down composition. | Out of scope. It stays a direct table consumer. |
| `appwire-client/typescript/scripts/qualify-package.mjs:149,156` | Package smoke calls for the table and `decideSubmitRoute`. | Extend for the gate; keep the existing calls. |

### What the two clients currently answer differently

These are not accidentally identical. They are the states the migration must
keep client-specific (or change only in a separate, product-reviewed PR).

| Status class | send cap | pending send | Browser | Native | Why |
|---|---|---|---|---|---|
| idle, awaiting, warning, systemError, unknown/future | false | false | send | none | Browser's table tier 5 ignores `capabilities.send`; native's `sendAction` requires it (`sendAction.ts:57`). |
| notLoaded | false | false | send | none | Same tier-5 answer; native's shut-down resume is gated on `send` (`sendAction.ts:82`). |
| notLoaded | true | false | send (`turn/start`) | resume (explicit resume first) | Browser sends and lets turn/start resume the thread; native parks a durable row then settles the resume (`screens.tsx:2648-2697`). |
| ended, closed | true | false | send (`turn/start`) | resume (explicit resume first) | Browser's shut-down substitution sends; native's `resumeOrNone` returns resume. |
| any status | either | true | queue / none | queue / none | Matches. Tier 6 and the queue veto already agree. |
| active | either | either | queue / none | queue / none | Matches. |

Pending-send rows agree everywhere. The 18 concrete differences produced by the
matrix fall into the status groups above: browser sends where native refuses a
missing send capability, and browser sends where native explicitly resumes. The
plan deliberately does not unify them in this slice.

## 2. States that must stay client-specific

### Native

- **Resume fence.** `resumeRequired` is native's recovery signal. The action is
  `resume` for the first message in any non-`restartRequired` status, `queue`
  when this phone already has a pending send and the harness can queue, and
  `none` otherwise (`sendAction.ts:33-44`). Native does not read
  `resumeOnlyFoldable`; do not make it infer the browser's folded-resume shape.
  The wire fence clears `send` as it sets `resumeRequired`, so the resume action
  must not be gated on `send` (`sendAction.ts:68-71`; test
  `sendAction.test.ts:72-89`).
- **Offline.** `connected === false` changes the route: a shut-down session's
  first message resumes when `send` is advertised, every other message queues
  when a queue seam exists, and otherwise there is no action
  (`sendAction.ts:80-89`; tests `sendAction.test.ts:111-151`). The browser has no
  equivalent state.
- **Restart fence.** `restartRequired` is `none` before any resume or offline
  branch (`sendAction.ts:34,69`; tests `sendAction.test.ts:49,84,120`).
- **Outer guards.** `liveSendKind` in `screens.tsx:2581-2597` additionally
  requires a live service, readiness, no unconfirmed send, no live
  `pendingMutation`, and no pending question. Those are native composition
  facts, not gate inputs. `deliver` in `screens.tsx:2648-2697` owns what
  `resume` means durably; the gate only names the action.
- **Labels.** `composerPlaceholder` and `sendLabel`
  (`sendAction.ts:91-106`) stay native-only. They describe the offline and
  question states, which the gate does not see.

### Browser

- **Recovery fence.** `recoveryFence`
  (`cmd/evener-hub/frontend/src/stores/liveControls.ts:85-111`) is the one
  reading of the hub's `resumeOnlyFoldable` bit, the restart-blocking
  obligation, a Stop this page started, delivery-uncertain rows, a queued
  non-send (held-steer family) row, the mutation-authority mark, and
  owner-retained snapshots. It yields four faces:
  - `sendResumes`: Send is offered and drives the store's resume, even when a
    send is already parked in front of it (`Composer.tsx:782-787`).
  - `resumeOnly`: the hub admits a folded `turn/start`; Send is offered only
    when no pending send exists (`Composer.tsx:789-793`).
  - `stillFenced`: Send and Queue are both refused (`Composer.tsx:788`).
  - `none`: the table applies.
  This pointer/obligation/signal composition stays in `liveControls.ts`. The
  gate receives only the resulting face.
- **Held steer.** The `hasQueuedNonSend` signal
  (`pendingTurnsStore.ts:114-127`) is a non-`turn/start` `submitting` row. It
  narrows the folded-resume face; it is not a pending send and must not enter
  the table's tier 6. `HeldSteerStack` and the queue strip keep their own
  rendering and ordering; the gate does not see them. `ownPendingSend` remains
  send-family only (`pendingEntries.ts:138-140`).
- **Shut-down send.** The browser sends `turn/start` for a shut-down session
  that advertises `send`, including a cold `notLoaded` session, rather than
  minting a separate resume action (`Composer.tsx:799-816`). This is the
  browser's recovery machinery, not a universal rule.
- **Follow-up card and controls.** `fencedLocal`, `sendDriven`,
  `showFollowUpCard`, `canCompose`, the button's disabled expression, the
  tooltip, and the submit toast (`Composer.tsx:818-856,1404-1406,1824-1835`)
  stay in the component. The gate's reasons are diagnostic; they do not replace
  existing user-facing copy in this slice.
- **No connectivity input.** The browser composer has no connection-state
  input; its durable outbox and dispatch path handle offline. `availabilityFor`
  therefore has no connected branch. The browser adapter passes
  `connected: true` to the gate and says why.
- **Press-time re-derivation.** The render and the press both call
  `availabilityFor`, with the press reading the live store
  (`Composer.tsx:1376-1403`). The gate is pure; the caller keeps owning which
  snapshot it passes.

## 3. Proposed shared gate API

New module `appwire-client/typescript/sendQueueGate.ts`, exported from
`appwire-client/typescript/index.ts`. It imports `deriveSendQueueAvailability`
and `SHUT_DOWN_STATUSES`; it does not import a store, a React component, or a
client platform module.

```ts
export type SendQueueAction = "send" | "queue" | "resume" | "none";

export type SendQueueReason =
  // allowed
  | "plain-send"      // table tier 5: open status, no pending send
  | "shut-down-send"  // browser: first message to a shut-down session
  | "resume-folded"   // browser: hub-admitted folded resume
  | "recovery-send"   // browser: Send drives this client's resume
  | "queue"
  | "resume"          // native: explicit resume before the send
  // refused
  | "restart-required"
  | "recovery-fenced"
  | "pending-send-in-flight"
  | "send-unavailable"
  | "queue-unavailable"
  | "offline-no-queue"
  | "no-action";

export type SendQueueDecision =
  | { readonly action: "send"; readonly reason: "plain-send" | "shut-down-send" | "resume-folded" | "recovery-send" }
  | { readonly action: "queue"; readonly reason: "queue" }
  | { readonly action: "resume"; readonly reason: "resume" }
  | { readonly action: "none"; readonly reason: Exclude<SendQueueReason, "plain-send" | "shut-down-send" | "resume-folded" | "recovery-send" | "queue" | "resume"> };

/** The recovery face a client already computed. The gate never computes a
 *  fence, reads a store, or infers a resume from a status. */
export type SendQueueRecovery =
  | { readonly kind: "none" }
  | { readonly kind: "folded-resume" }   // browser resumeOnly
  | { readonly kind: "send-resumes" }    // browser sendResumes
  | { readonly kind: "explicit-resume" } // native resumeRequired
  | { readonly kind: "blocked" };        // browser stillFenced

/** Two honest client policy facts, not client identity. */
export interface SendQueuePolicy {
  /** Whether a table plain-send requires capabilities.send. Native true,
   *  browser false (the browser table's tier 5 ignores it). Recovery and
   *  shut-down branches keep their own capability rules. */
  readonly sendRequiresCapability: boolean;
  /** What a shut-down session's first message is called. Browser "send"
   *  (turn/start folds the resume); native "resume" (explicit resume first). */
  readonly shutDownAction: "send" | "resume";
}

export interface SendQueueGateInput {
  readonly statusType: string;
  readonly capabilities: Pick<ThreadCapabilities, "send" | "queue">;
  /** ownPendingSend over this client's reconciled pending entries. Never the
   *  daemon's session-wide pendingMutations projection. */
  readonly hasPendingSend: boolean;
  /** Native: the live connection. Browser: always true; the browser has no
   *  connectivity branch. */
  readonly connected: boolean;
  readonly recovery: SendQueueRecovery;
  readonly policy: SendQueuePolicy;
}

export const BROWSER_SEND_QUEUE_POLICY: SendQueuePolicy = {
  sendRequiresCapability: false,
  shutDownAction: "send",
};
export const NATIVE_SEND_QUEUE_POLICY: SendQueuePolicy = {
  sendRequiresCapability: true,
  shutDownAction: "resume",
};

export function sendQueueGate(input: SendQueueGateInput): SendQueueDecision;
```

### Precedence contract

The first matching row wins. This ordering is the API contract, not an
implementation detail; test it row by row.

1. `statusType === "restartRequired"` -> `none("restart-required")`.
2. `recovery.kind === "blocked"` -> `none("recovery-fenced")`.
3. `recovery.kind === "send-resumes"` -> `send("recovery-send")`.
4. `recovery.kind === "folded-resume"` -> `hasPendingSend ? none("pending-send-in-flight") : send("resume-folded")`.
5. `recovery.kind === "explicit-resume"` -> if `hasPendingSend`, ask the table
   with `hasPendingSend: true`: `queue` when it can queue, otherwise
   `none("queue-unavailable")`; otherwise `resume("resume")`.
6. `!connected` -> if the status is shut down and there is no pending send,
   `capabilities.send ? resume("resume") : none("send-unavailable")`;
   otherwise ask the table with `hasPendingSend: true`: `queue` when it can
   queue, otherwise `none("offline-no-queue")`.
7. Online: ask the table with the real `hasPendingSend`. If it can queue ->
   `queue("queue")`. If the status is shut down and `capabilities.send` is true
   -> `policy.shutDownAction === "resume" ? resume("resume") : send("shut-down-send")`.
   If the table can send and (`!policy.sendRequiresCapability || capabilities.send`)
   -> `send("plain-send")`. If the table can send but a required send
   capability is false -> `none("send-unavailable")`. Otherwise
   `none("no-action")`.

Restart is first because every reachable current path already answers none for
it: native checks it before resume and offline, and the browser's restart
obligation makes `recoveryFence` return `blocked`; the browser's table also
answers both-false. Two synthetic recovery faces would have differed if a
caller fabricated them: `folded-resume` (old browser offered Send when there
was no pending send; with one it already answered none) and `send-resumes`
(old browser offered Send). Neither is reachable: the hub never stamps
`ResumeOnlyFoldable` for a restart-required daemon
(`cmd/evener-hub/app_turn_start_resume_only_test.go:223`), and `isSendResumes`
explicitly excludes `restartRequired` (`threads.ts:4419`). The gate answers
none for both. The state table marks this as the only intentional answer
change.

### Why these inputs, and not more

| Fact | Owner before the gate | Why it stays an input |
|---|---|---|
| `statusType`, `capabilities.send`, `capabilities.queue` | Wire model | The table's own inputs. |
| `hasPendingSend` | `ownPendingSend` (`pendingEntries.ts:138`) | The caller's durable/optimistic records; the table cannot verify it. |
| `connected` | Native runtime | Native has an offline route; the browser does not. |
| `recovery.kind` | Native `resumeRequired`; browser `recoveryFence` | The fence's obligations, signals, and hub bit are client facts. The gate must not re-derive them from a status or a pending list. |
| `policy.sendRequiresCapability` | Native `sendAction`; browser table tier 5 | A real, tested behavior difference. Making it explicit prevents a silent behavior change in either client. |
| `policy.shutDownAction` | Native `resumeOrNone`; browser shut-down substitution | `send` and `resume` are different wire paths and different durable recovery records. |
| `hasContent`, attachments, skill/command selections, question pending, service readiness, `activeTurnId`, queue depth, `pendingMutations` wholesale, held-steer entries, parent refs, mutation-authority marks | Components/stores | Not part of the Send/Queue action decision. Content stays a caller check; readiness stays each client's submission guard. |

`recovery.kind` is the only new client-to-shared seam. It is deliberately
coarse: the gate knows four non-none recovery behaviors, not the browser's
seven signals or native's runtime lifecycle.

## 4. Staged migration

The design spec's open question asks which client first. Decision: native first.
`sendAction` is one pure function, its entire behavior is already pinned by
`sendAction.test.ts`, and no React, store, or UI code is in the diff. Browser
adoption is the larger change because `availabilityFor` is embedded in a
component and sits beside the recovery card. The first PR should be reviewable
on its own and leave the browser untouched.

### Stage 1 (PR 1): shared gate plus native adoption

Add the gate and migrate one client. No browser file changes.

- Add `appwire-client/typescript/sendQueueGate.ts` and export
  `sendQueueGate`, its input/output types, and the two policy constants from
  `index.ts`.
- Add `appwire-client/typescript/sendQueueGate.test.ts` with the §5 base and
  recovery matrices as literal expected rows. Also add a transition table that
  pins the precedence contract: restart, blocked, send-resumes, folded-resume
  with/without pending, explicit-resume with/without pending and queue seam,
  offline shut-down first message, offline queue, online queue, shut-down send
  versus resume, plain send with/without capability.
- Rewrite `mobile-native/src/session/sendAction.ts` to one adapter:
  `recovery = conversation.resumeRequired ? { kind: "explicit-resume" } : { kind: "none" }`;
  `hasPendingSend = ownPendingSend(pendingMutations)`; call the gate with
  `NATIVE_SEND_QUEUE_POLICY` and return `.action`. Keep `SendAction`,
  `SendSource`, `composerPlaceholder`, and `sendLabel` exactly as they are.
  Delete the now-unreachable `resumeOrNone` and `offlineAction` helpers.
- Keep `sendAction.test.ts` unchanged. It is the acceptance oracle: every
  expected action must still pass. Do not rewrite its expectations to match a
  new implementation. Add a small test that the adapter's recovery mapping is
  `explicit-resume` for `resumeRequired` and `none` otherwise.
- Extend `scripts/qualify-package.mjs` only if the package root surface
  requires a smoke call; keep the existing table and `decideSubmitRoute` smoke
  calls.

What must not change: every `sendAction` answer in `sendAction.test.ts`
(native online, native offline, resume fence, restart, pending send, capability
veto); `sendLabel` and `composerPlaceholder`; `readSendAction`
(`sessionMessage.ts:148-177`); `screens.tsx` guards and `deliver`; the browser.
No wire route changes. No user-facing string changes.

Acceptance: the native `sendAction` suite passes unchanged; the new gate matrix
passes; `mobile-native` typecheck passes; the shared package builds; no browser
file is modified.

### Stage 2 (PR 2): browser adoption

Add the browser adapter and replace `availabilityFor`'s body. No native file
changes.

- Add a pure browser adapter, preferably in
  `cmd/evener-hub/frontend/src/stores/liveControls.ts` beside `recoveryFence`
  (or a small `composer/browserSendQueue.ts` if importing the gate into
  `liveControls.ts` would widen that module's dependencies):
  `browserSendRecovery(model, restartObligated, signals)` maps the
  `RecoveryFenceReading` to the gate's union in the same order as
  `availabilityFor`: `sendResumes -> send-resumes`, `stillFenced -> blocked`,
  `resumeOnly -> folded-resume`, otherwise `none`. It must not read pending rows
  itself; the caller still passes `hasPendingSend`.
- Add `sendQueueDecisionFor(target, pendingSend, restartObligated, signals)` in
  `Composer.tsx` (or beside it) that calls the gate with
  `statusType`, `capabilities`, `hasPendingSend`, `connected: true`,
  `recovery: browserSendRecovery(...)`, and `BROWSER_SEND_QUEUE_POLICY`, and
  returns the decision. Make `availabilityFor` a thin mapping of that decision
  to `{ canSend: action === "send", canQueue: action === "queue" }`. Keep
  `availabilityFor`'s signature and call sites, including the live press-time
  call at `Composer.tsx:1380-1403`, so the render/press re-derivation behavior
  is unchanged. The browser policy never yields `resume`; make that impossible
  by construction and assert it in tests.
- Leave `recoveryFence`, `pressLocalRecoveryFenced`, `sendDrivenModel`,
  `resumeOnlyModel`, the held-steer projection, the follow-up card, and all
  tooltip/toast code untouched. Do not move `hasQueuedNonSend` into the gate.
- Add `liveControls.test.ts` cases for the adapter's four faces and the
  restart/restartRequired precedence. Keep every existing `Composer.test.tsx`
  recovery test unchanged; add a targeted test only if a row is not already
  covered.
- Keep `decideSubmitRoute` in place for this PR. The press still calls it with
  the booleans; replacing it is Stage 3.

What must not change: every existing `availabilityFor` answer and every
Composer test; the `sendResumes` face offering Send with a parked send; the
folded face refusing a second send; the blocked face refusing both; the
shut-down substitution for `notLoaded`, `ended`, and `closed`; the follow-up
card's visibility and the Send button's disabled expression; the press-time
live reads; held-steer rendering; the browser toast copy; native.

Acceptance: all Composer, liveControls, threads session-action, pending-turns,
and held-steer suites pass unchanged; the new adapter tests pass; frontend
typecheck and lint pass.

### Stage 3 (PR 3): remove the duplicated plumbing, then prove the routes

Small cleanup plus the real-hub check. No behavior change.

- Use `sendQueueDecisionFor` at the browser press: when content exists, the
  route is its action (`send`/`queue`, or a refusal for `none`), using the same
  live facts the render used. Replace `decideSubmitRoute` at
  `Composer.tsx:1380` with that mapping. Keep `decideSubmitRoute` exported
  unless the package maintainers agree to a deprecation/removal; it is public
  SDK surface today.
- Remove the direct `deriveSendQueueAvailability` imports from `Composer.tsx`
  and `sendAction.ts`; the table remains imported by the gate,
  `mobile/src/state/conversation.ts:requireQueue`, tests, and qualification.
- Optionally use the decision's reason in diagnostics or tooltips only if the
  existing copy and tests stay unchanged. Do not replace the browser's
  "Send is not available for this session" string or native's labels in this
  slice.
- Run the §6 real-hub journey set twice in a row, per the design spec's exit
  criterion. If any journey shows a route change, fix the mapping and add a
  case; do not change the table.
- Update `docs/daily-work.md` and the client-state row in
  `docs/product/subsystems.md` only with behavior verified by these runs.

What must not change: the gate's decisions for any row in §5; the table; Retry
eligibility; `sessionControls`; held-steer; the outbox/recovery records; the
native lifecycle. No wire route changes.

Acceptance: the full affected journeys pass twice; no test needs a transport
frame to observe durable state; the shared package gate test and both client
suites pass.

### Stage 4 (not in this slice): alignment decisions

The 18 existing browser/native differences in §1 are real product decisions,
not migration mechanics. If the team later decides that a plain send must
require `capabilities.send` everywhere, or that native should fold the resume
like the browser, that is a separate PR with its own behavior-change tests.
Do not fold it into Stage 1, 2, or 3.

## 5. State table: old answers versus proposed answers

### Method and coverage

The table was generated by a scratch oracle that reimplements the landed table
and both current compositions, then compares them with the proposed gate. It
checked 304 reachable browser combinations and 320 reachable native
combinations; zero differed. The oracle is not committed; the plan records its
result, and the Stage 1/2 tests must re-prove it from literal expected rows, not
by copying the gate's own logic.

Status classes collapse statuses that no branch distinguishes:

- `idle, awaiting, warning, systemError`: the table's
  `LIVE_SNAPSHOT_STATUSES` (`sendQueueAvailability.ts:90`).
- `ended, closed`: the table's terminal branch
  (`sendQueueAvailability.ts:111-113`).
- `notLoaded`: shut down, but tier 5 canSend is true before the shut-down
  substitution; it is its own class.
- `active`: its own branch.
- `restartRequired`: answered none before every recovery face.
- `unknown/future`: any status not in the known sets; tier 5, or tier 6 with
  the not-live snapshot rule.

`*` means the answer is invariant across that dimension, not that the
combination is untested. Browser `connected=false` is not a browser state: the
composer has no connectivity input, so the browser rows are online only. Native
tests both values. `resume` is `none` in the base matrix; the recovery matrix
covers native `resumeRequired` (`explicit-resume`) and the browser faces.

### Base matrix (no recovery; browser online; native online and offline)

| status class | send cap | queue cap | pending send | browser old | browser new | native old online | native new online | native old offline | native new offline | diff |
|---|---|---|---|---|---|---|---|---|---|---|
| idle, awaiting, warning, systemError | true | true | true | queue | queue | queue | queue | queue | queue | none |
| idle, awaiting, warning, systemError | true | true | false | send | send | send | send | queue | queue | none |
| idle, awaiting, warning, systemError | true | false | true | none | none | none | none | none | none | none |
| idle, awaiting, warning, systemError | true | false | false | send | send | send | send | none | none | none |
| idle, awaiting, warning, systemError | false | true | true | queue | queue | queue | queue | queue | queue | none |
| idle, awaiting, warning, systemError | false | true | false | send | send | none | none | queue | queue | none |
| idle, awaiting, warning, systemError | false | false | true | none | none | none | none | none | none | none |
| idle, awaiting, warning, systemError | false | false | false | send | send | none | none | none | none | none |
| active | true | true | true | queue | queue | queue | queue | queue | queue | none |
| active | true | true | false | queue | queue | queue | queue | queue | queue | none |
| active | true | false | true | none | none | none | none | none | none | none |
| active | true | false | false | none | none | none | none | none | none | none |
| active | false | true | true | queue | queue | queue | queue | queue | queue | none |
| active | false | true | false | queue | queue | queue | queue | queue | queue | none |
| active | false | false | true | none | none | none | none | none | none | none |
| active | false | false | false | none | none | none | none | none | none | none |
| notLoaded | true | true | true | queue | queue | queue | queue | queue | queue | none |
| notLoaded | true | true | false | send | send | resume | resume | resume | resume | none |
| notLoaded | true | false | true | queue | queue | queue | queue | queue | queue | none |
| notLoaded | true | false | false | send | send | resume | resume | resume | resume | none |
| notLoaded | false | true | true | queue | queue | queue | queue | queue | queue | none |
| notLoaded | false | true | false | send | send | none | none | none | none | none |
| notLoaded | false | false | true | queue | queue | queue | queue | queue | queue | none |
| notLoaded | false | false | false | send | send | none | none | none | none | none |
| ended, closed | true | true | true | queue | queue | queue | queue | queue | queue | none |
| ended, closed | true | true | false | send | send | resume | resume | resume | resume | none |
| ended, closed | true | false | true | queue | queue | queue | queue | queue | queue | none |
| ended, closed | true | false | false | send | send | resume | resume | resume | resume | none |
| ended, closed | false | true | true | queue | queue | queue | queue | queue | queue | none |
| ended, closed | false | true | false | none | none | none | none | none | none | none |
| ended, closed | false | false | true | queue | queue | queue | queue | queue | queue | none |
| ended, closed | false | false | false | none | none | none | none | none | none | none |
| restartRequired | true | true | true | none | none | none | none | none | none | none |
| restartRequired | true | true | false | none | none | none | none | none | none | none |
| restartRequired | true | false | true | none | none | none | none | none | none | none |
| restartRequired | true | false | false | none | none | none | none | none | none | none |
| restartRequired | false | true | true | none | none | none | none | none | none | none |
| restartRequired | false | true | false | none | none | none | none | none | none | none |
| restartRequired | false | false | true | none | none | none | none | none | none | none |
| restartRequired | false | false | false | none | none | none | none | none | none | none |
| unknown/future | true | true | true | queue | queue | queue | queue | queue | queue | none |
| unknown/future | true | true | false | send | send | send | send | queue | queue | none |
| unknown/future | true | false | true | queue | queue | queue | queue | queue | queue | none |
| unknown/future | true | false | false | send | send | send | send | queue | queue | none |
| unknown/future | false | true | true | queue | queue | queue | queue | queue | queue | none |
| unknown/future | false | true | false | send | send | none | none | queue | queue | none |
| unknown/future | false | false | true | queue | queue | queue | queue | queue | queue | none |
| unknown/future | false | false | false | send | send | none | none | queue | queue | none |

Notes on the non-obvious rows:

- `notLoaded` and `ended/closed` with a pending send answer `queue` even when
  `queue=false`: the table's tier 6 does not apply the live-snapshot queue veto
  to terminal or not-live statuses (`sendQueueAvailability.ts:111-113,160-163`).
  Both clients already do this. Keep it.
- Native offline `unknown` with `queue=false` answers `queue` for the same
  reason: `offlineAction` asks the table with `hasPendingSend: true`, and an
  unknown status is not a live snapshot.
- Browser `notLoaded` with `send=false` still sends because table tier 5
  returns `canSend: true` and the browser's policy does not require the
  capability for a plain send. Native refuses. This is one of the 18 existing
  differences, not a gate change.

### Recovery matrix (old versus new)

Browser faces are invariant across status for every reachable non-restart
status; native explicit-resume is invariant except when a pending send has to
consult the table and the queue bit is false. `*` covers the invariant
dimension.

| recovery face/kind | client | status class | send cap | queue cap | pending send | connected | old | new | diff |
|---|---|---|---|---|---|---|---|---|---|
| blocked | browser | any non-restart | true | true | true | * | none | none | none |
| blocked | browser | any non-restart | true | false | true | * | none | none | none |
| blocked | browser | any non-restart | false | true | true | * | none | none | none |
| blocked | browser | any non-restart | false | false | true | * | none | none | none |
| blocked | browser | any non-restart | true | true | false | * | none | none | none |
| blocked | browser | any non-restart | true | false | false | * | none | none | none |
| blocked | browser | any non-restart | false | true | false | * | none | none | none |
| blocked | browser | any non-restart | false | false | false | * | none | none | none |
| send-resumes | browser | any non-restart | true | true | true | * | send | send | none |
| send-resumes | browser | any non-restart | true | false | true | * | send | send | none |
| send-resumes | browser | any non-restart | false | true | true | * | send | send | none |
| send-resumes | browser | any non-restart | false | false | true | * | send | send | none |
| send-resumes | browser | any non-restart | true | true | false | * | send | send | none |
| send-resumes | browser | any non-restart | true | false | false | * | send | send | none |
| send-resumes | browser | any non-restart | false | true | false | * | send | send | none |
| send-resumes | browser | any non-restart | false | false | false | * | send | send | none |
| folded-resume | browser | any non-restart | true | true | true | * | none | none | none |
| folded-resume | browser | any non-restart | true | false | true | * | none | none | none |
| folded-resume | browser | any non-restart | false | true | true | * | none | none | none |
| folded-resume | browser | any non-restart | false | false | true | * | none | none | none |
| folded-resume | browser | any non-restart | true | true | false | * | send | send | none |
| folded-resume | browser | any non-restart | true | false | false | * | send | send | none |
| folded-resume | browser | any non-restart | false | true | false | * | send | send | none |
| folded-resume | browser | any non-restart | false | false | false | * | send | send | none |
| explicit-resume | native | any non-restart | true | true | true | true | queue | queue | none |
| explicit-resume | native | any non-restart | true | true | true | false | queue | queue | none |
| explicit-resume | native | idle/awaiting/warning/systemError/active | true | false | true | true | none | none | none |
| explicit-resume | native | idle/awaiting/warning/systemError/active | true | false | true | false | none | none | none |
| explicit-resume | native | notLoaded/ended/closed/unknown | true | false | true | true | queue | queue | none |
| explicit-resume | native | notLoaded/ended/closed/unknown | true | false | true | false | queue | queue | none |
| explicit-resume | native | any non-restart | false | true | true | true | queue | queue | none |
| explicit-resume | native | any non-restart | false | true | true | false | queue | queue | none |
| explicit-resume | native | idle/awaiting/warning/systemError/active | false | false | true | true | none | none | none |
| explicit-resume | native | idle/awaiting/warning/systemError/active | false | false | true | false | none | none | none |
| explicit-resume | native | notLoaded/ended/closed/unknown | false | false | true | true | queue | queue | none |
| explicit-resume | native | notLoaded/ended/closed/unknown | false | false | true | false | queue | queue | none |
| explicit-resume | native | any non-restart | true | true | false | true | resume | resume | none |
| explicit-resume | native | any non-restart | true | true | false | false | resume | resume | none |
| explicit-resume | native | any non-restart | true | false | false | true | resume | resume | none |
| explicit-resume | native | any non-restart | true | false | false | false | resume | resume | none |
| explicit-resume | native | any non-restart | false | true | false | true | resume | resume | none |
| explicit-resume | native | any non-restart | false | true | false | false | resume | resume | none |
| explicit-resume | native | any non-restart | false | false | false | true | resume | resume | none |
| explicit-resume | native | any non-restart | false | false | false | false | resume | resume | none |

For every reachable recovery face, `restartRequired` is `none` in both old and
new. The synthetic `restartRequired + folded-resume` and
`restartRequired + send-resumes` faces are the two unreachable exceptions
described in §3; the gate answers `none` for both. The base matrix's
`restartRequired` rows therefore cover the connected and resume dimensions:
the gate's first check applies before `recovery`.

### Differences

- Reachable rows: none. Every row above has `diff = none`.
- Intentional answer change: `restartRequired + folded-resume` with no pending
  send -> old browser `send`, new gate `none`. The hub never stamps
  `resumeOnlyFoldable` for a restart-required daemon
  (`app_turn_start_resume_only_test.go:223`), so no real snapshot reaches it.
  The `restartRequired + send-resumes` pair would also have been `send` in the
  old browser, and is excluded by `isSendResumes` (`threads.ts:4419`). If a
  synthetic test asserts the old answer, it is asserting an unreachable state.
  The plan's gate test should pin the safe `none` answer and cite this.
- Existing browser-versus-native differences are listed in §1 and are not
  changes made by this plan. They stay inputs (`policy.sendRequiresCapability`
  and `policy.shutDownAction`).

## 6. Test plan

### Shared package

`appwire-client/typescript/sendQueueGate.test.ts` (new):

- Literal-row tables for §5's base and recovery matrices. Do not generate the
  expected values from `sendQueueGate` itself.
- Precedence transitions: restart before recovery; blocked before
  send-resumes; send-resumes before folded; folded before explicit; explicit
  before offline; offline before online.
- Both policy constants, including the shut-down send/resume difference.
- The 18 existing browser/native differences as explicit parity rows, so a
  future "cleanup" cannot silently unify them.
- The unreachable `restartRequired + folded-resume` and
  `restartRequired + send-resumes` pairs, asserting `none` and documenting why.

`appwire-client/typescript/sendQueueAvailability.test.ts` and
`submitRouting.test.ts`: unchanged. They remain the table's own oracle.

### Native

- `mobile-native/src/session/sendAction.test.ts`: unchanged expected actions.
  This is the Stage 1 acceptance gate.
- `mobile-native/src/session/sessionMessage.test.ts`: queue-behind-own-send,
  accepted send, and daemon-listed pending send.
- `mobile-native/src/session/errorAction.test.ts`: Retry's pending rule must
  stay independent of the send gate.
- `mobile-native/src/session/nativeConversationSessions.test.tsx` and
  `localQueuedGhosts.test.tsx` where they exercise pending rows and resume.
- `mobile-native/src/screens.tsx` outer guards are not unit-tested as a gate
  here; run the screen tests that cover composer readiness if touched. Do not
  move those guards.

### Browser

- `cmd/evener-hub/frontend/src/stores/liveControls.test.ts`: the four recovery
  faces, the hub-bit authority, and the signal narrowing (`uncertainMessages`,
  `queuedNonSend`, `stopInFlight`).
- `Composer.test.tsx`: the existing rows for active queue, cold auto-resume,
  closed second message, merely-resumable fold, Send-resumes, blockedUnknown
  routing, pending-send refusal, fenced idle/active/closed/stopped, and the
  follow-up card. These are the Stage 2 acceptance gate.
- `Composer.liveCapabilities.test.tsx`: status/capability transitions and the
  closed-frame capability stamp.
- `Composer.queuedMessages.test.tsx`, `HeldSteerStack.test.tsx`,
  `HeldSteerAnnouncements.test.tsx`, `Session.test.tsx` where held-steer or
  recovery rendering is asserted.
- `composer/queue/pendingTurnsStore.test.ts` and `.edge.test.ts`: tier-6
  provenance, `hasQueuedNonSend`, and the submission guard.

### Commands

Run from the repository root. The exact commands are:

- Shared gate through the frontend vitest gate (the frontend config includes
  `appwire-client/typescript/**/*.test.ts`):
  ```sh
  (cd cmd/evener-hub/frontend && npm test -- \
    ../../../appwire-client/typescript/sendQueueGate.test.ts \
    ../../../appwire-client/typescript/sendQueueAvailability.test.ts \
    ../../../appwire-client/typescript/submitRouting.test.ts)
  ```
- Browser suites:
  ```sh
  (cd cmd/evener-hub/frontend && npm test -- \
    src/panes/session/composer/Composer.test.tsx \
    src/panes/session/composer/Composer.liveCapabilities.test.tsx \
    src/stores/liveControls.test.ts \
    src/stores/threads.sessionActions.test.ts)
  ```
- Native suites:
  ```sh
  (cd mobile-native && sh ../scripts/native/run-tests.sh \
    src/session/sendAction.test.ts \
    src/session/sessionMessage.test.ts \
    src/session/errorAction.test.ts)
  ```
- Typecheck and lint:
  ```sh
  (cd cmd/evener-hub/frontend && npm run typecheck)
  (cd mobile-native && npm run check)
  (cd appwire-client/typescript && npm run build)
  (cd cmd/evener-hub/frontend && npm run lint)
  (cd mobile-native && npm run lint)
  ```

The `npm test` wrapper applies the repository's load-aware worker budget; do not
replace it with a bare `vitest run` in CI.

### Real-hub journey

The gate is pure, so the real-hub proof is that each action it can produce picks
a route the real hub and daemon accept. Use the existing live-stack e2e set;
add no new general browser harness (the only browser guard is the
retirement-specific one, `app_retirement_browser_test.go`, and it is not a
general composer harness).

| Gate action | Real-hub journey |
|---|---|
| `queue` behind a running turn | `TestE2E_TurnControlReachesTheSession` (`e2e_turn_control_test.go:43`) |
| `queue` after a Stop race | `TestE2E_ASendThatRacedAStopStillRuns` (`e2e_control_without_turn_ids_test.go:131`) |
| `queue` on a finished session with a pending send (tier 6) | `TestE2E_QueuePromptToARetiredSession` (`e2e_queue_prompt_exited_test.go:27`) |
| `send`/resume a retired session | `TestE2E_SendPromptToARetiredSession` (`e2e_resume_prompt_test.go:31`) |
| `resume` after the daemon died mid-turn | `TestE2E_SendPromptAfterDaemonDiedMidTurn` (`e2e_resume_prompt_midturn_test.go:31`) |

Command:

```sh
go test ./cmd/evener-hub -run 'TestE2E_(TurnControlReachesTheSession|ASendThatRacedAStopStillRuns|QueuePromptToARetiredSession|SendPromptToARetiredSession|SendPromptAfterDaemonDiedMidTurn)$' -count=2 -timeout 20m
```

These tests are live-stack e2e: they build binaries, bind loopback, spawn a hub
and daemon, and use the scripted `fakellm` provider. They skip under
`testing.Short()` or where loopback bind/process inspect is unavailable. Run
them on a host that allows both; a skip is not a pass. `-count=2` reruns the set
twice in a row. If Stage 2 or 3 changes a wire route, add one e2e case for that
route rather than relying on an old test that no longer names it.

Run each affected journey twice in a row, per the design spec's exit criterion.
Evidence should name the exact commands and the model-boundary assertions, not
just receipt dispositions.

## 7. Non-goals

- Rewriting or re-tiering `deriveSendQueueAvailability`. The table stays the
  owner of its tiers, including the tier-6/live-snapshot queue veto.
- Unifying browser and native recovery. Native's explicit resume/offline/fence
  behavior and the browser's `sendResumes`/folded/blocked faces remain
  client-specific.
- Changing Retry eligibility, `errorAction`, `canonicalFailureAction`, or
  `ownPendingTurnInput`.
- Changing `sessionControls`, Stop/Steer/drain/promote rules, or the
  `SHUT_DOWN_STATUSES` vocabulary.
- Changing the outbox, recovery records, dispatcher, draft persistence,
  attachments, skill/command selection checks, or the ask dock.
- Changing held-steer rendering or QueueStrip ordering.
- Changing `mobile/src/state/conversation.ts:requireQueue`; it is a
  queue-only precondition, not a duplicate composition.
- Removing `decideSubmitRoute` from the public SDK in this slice.
- Aligning the 18 existing browser/native differences.
- Adding a new global state library, cross-device locking, or a new real-browser
  harness.
- Changing user-facing labels or error copy as part of the gate refactor.

## 8. Open questions

1. Should a plain open-status send require `capabilities.send` in the browser
   as it does in native? Today the table tier 5 ignores it; native refuses.
   Preserving both via `policy.sendRequiresCapability` is safe but keeps the
   difference. If the product answer is "require it everywhere", that is a
   separate behavior-changing PR with new tests for stale/withheld snapshots.
2. Should native use the hub's `resumeOnlyFoldable` bit and fold the resume
   like the browser, instead of explicit `resume`? Native's runtime settle path
   is why it currently does not. Folding could remove a round trip but changes
   recovery ownership; keep explicit for this slice.
3. Should a fenced native session with a pending send offer another
   `turn/start` like the browser's `sendResumes` face, or keep queueing behind
   the first parked send? Native queues today to avoid two parked turn/starts
   racing after recovery (`sendAction.ts:26-32`). Keep.
4. Can `decideSubmitRoute` be deprecated and removed from the SDK now that the
   gate owns the action, or does it have external consumers? The plan keeps it
   exported through Stage 3.
5. Should `sendQueueGate` expose a narrow `queueAllowed` helper for
   `mobile/src/state/conversation.ts:requireQueue`, or should that call site
   stay a direct table consumer? Out of scope here.
6. Should the gate's reasons become user-facing tooltips/toasts? They are
   additive and currently diagnostic only. Changing copy needs its own review.
7. Should a future real-browser guard exercise the composer against a real hub?
   There is no general harness today; the existing Go e2e set is the real-hub
   proof for this slice.

## 9. Decision log

- **Keep the table.** The inventory's "native duplicates the table" claim is
  wrong; both clients call it. The redundancy is composition.
- **Native first.** Stage 1 is one pure function plus the shared gate; the
  browser, with its recovery card and fence, is Stage 2.
- **Recovery is an input, not a re-derivation.** The gate receives four
  non-none recovery behaviors. It never reads `resumeRequired`,
  `resumeOnlyFoldable`, a restart obligation, a pending row, or a held-steer
  row.
- **Two policy facts are explicit.** `sendRequiresCapability` and
  `shutDownAction` preserve the 18 existing differences instead of silently
  normalizing them.
- **Reasons are additive.** Existing UI copy and labels do not change in this
  slice.
- **One unreachable answer changes.** `restartRequired + folded-resume` becomes
  `none` instead of the old browser's `send`; the hub cannot produce that pair.
