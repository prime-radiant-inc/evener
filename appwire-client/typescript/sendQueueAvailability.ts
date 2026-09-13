// deriveSendQueueAvailability computes whether the composer's Send and
// Queue actions are currently available, ported from the legacy composer's
// send-vs-queue capability precedence (parity-m5-composer.md §A, lines
// 64-71, citing cmd/evener-hub/assets/renderer.js:479-513):
//
//   1. ended/closed              -> send=false, queue=false (tier 6 first)
//   2. [DROPPED - see below]
//   3. active && capabilities.queue === false explicitly -> both false
//   4. active                    -> send=false, queue=true (queue-mode default)
//   6. hasPendingSend            -> send=false, queue=true (see tier 6 below)
//   5. else (idle/awaiting/...)  -> send=true,  queue=false (plain-send default)
//
// Tier 6 is this codebase's own addition and sits BETWEEN 4 and 5 rather than
// at the end of the list, which is why it is numbered out of order: the legacy
// table it extends is quoted verbatim above. It also decides tier 1's row
// before that row's own answer applies - a finished session resumes on the
// first message, so it holds this client's in-flight sends like any other.
//
// The legacy tier 2 ("the source already advertised live send/queue
// capabilities for the CURRENT state, `liveCapabilitiesStatus === state`")
// is intentionally absent, and stays absent now that ThreadModel.capabilities
// IS live: thread/status/changed carries the set that goes with the status it
// announces (kata 06t8), so the two always describe the same moment - and for
// that fresh set, this table already computes exactly what reading it would.
// The hub gates Send on "no turn in flight" and Queue on "a turn in flight"
// (server/appwire_runtime.go's appCapabilities), which is tiers 4 and 5; the
// one thing the status alone cannot say is whether the harness wired a queue
// at all, and that is tier 3. A tier 2 would restate the table, not correct
// it.
//
// Capability booleans are therefore still consulted ONLY in tier 3, which is
// also where the legacy code treated them as authoritative (the "explicitly
// known" queue-cap-false branch).
//
// The active tier checks `statusType === "active"` ALONE - verified directly
// against the cited renderer.js:479-513 (updateThreadState's sendBtn
// branches), which key only on the `state` string and `this.liveSendCap`/
// `this.liveQueueCap`; nowhere in that chain does it read activeTurnId. The
// stronger `state === "active" && !!activeTurnId` formula lives in a
// DIFFERENT, deliberately-shared predicate (thread-state.js:16-18,
// `EvenerThreadState.isBusy`) that gates interrupt/steer/model-switch, not
// send/queue - that file's own header comment lists exactly those three
// call sites and none of them is the composer's send/queue capability
// chain. Folding isBusy's activeTurnId check into THIS gate would also add
// a real race this store must not have: thread/status/changed (which flips
// ThreadModel.status.type to "active") and turn/started (which populates
// ThreadModel.activeTurnId) are two separate notifications, so there is a
// window where status already reads "active" but activeTurnId hasn't
// arrived yet. The verbatim table queues in that window; requiring
// activeTurnId would instead fall through to the plain-send default and
// let a legitimate queue attempt bounce off the daemon as a ConflictError.
// This helper therefore does not take activeTurnId as an input at all.

import type { ThreadCapabilities } from "./types.gen";

export interface SendQueueAvailabilityInput {
  statusType: string;
  capabilities: ThreadCapabilities;
  // Whether THIS client has a turn/start of its own still in flight or
  // accepted-but-not-yet-reflected. What this client did, not a guess about
  // what the daemon has got to yet - see tier 6 in the function below.
  //
  // Contract the caller owns, because nothing here can check it: this must come
  // from the caller's own durable outbox/optimistic records. The daemon's
  // session-wide pendingMutations projection is NOT an acceptable source - it
  // describes every client on the session and only ever refreshes at hydrate,
  // so it is precisely the stale-late-arriving state tier 6 argues it is not.
  hasPendingSend?: boolean;
}

export interface SendQueueAvailability {
  canSend: boolean;
  canQueue: boolean;
}

const BOTH_UNAVAILABLE: SendQueueAvailability = { canSend: false, canQueue: false };
const QUEUE_MODE: SendQueueAvailability = { canSend: false, canQueue: true };
const PLAIN_SEND_MODE: SendQueueAvailability = { canSend: true, canQueue: false };

export function deriveSendQueueAvailability({
  statusType,
  capabilities,
  hasPendingSend,
}: SendQueueAvailabilityInput): SendQueueAvailability {
  if (statusType === "restartRequired") return BOTH_UNAVAILABLE;
  // Tier 6 (below) reaches inside this tier rather than being shadowed by it. A
  // finished session is resumable - turn/start alone carries the hub's
  // auto-resume (app_rpc.go's resumeTurnStartThread) - so a first message wakes
  // a daemon and the seconds that takes are all window: the status still reads
  // terminal while a turn this client submitted is already in flight, and a
  // second message routed by the status alone is another turn/start into the
  // daemon that just started. It bounces the same way, with the same
  // Conflict("turn is already active").
  //
  // With nothing of this client's pending, the answer is unchanged: no turn to
  // send to and none to queue behind. Whether such a session can be written to
  // at all is the SEND CAPABILITY's question, not this table's - the composer
  // reads that separately for its follow-up card.
  if (statusType === "ended" || statusType === "closed") {
    return hasPendingSend ? QUEUE_MODE : BOTH_UNAVAILABLE;
  }

  if (statusType === "active") {
    if (capabilities.queue === false) return BOTH_UNAVAILABLE;
    return QUEUE_MODE;
  }

  // Tier 6: a turn this client already submitted counts as active even before
  // the status says so. Send two messages quickly and the second is composed
  // before any status frame for the first arrives; tier 5 below would route it
  // to turn/start and the daemon would refuse it with
  // Conflict("turn is already active").
  //
  // This is NOT the activeTurnId fold the header rejects. activeTurnId is the
  // daemon's state arriving late; a pending send is this client's own record
  // of what it just did, and cannot be stale relative to itself. That holds
  // only for the input this flag is documented to take - see its contract on
  // SendQueueAvailabilityInput, which no code here can enforce.
  //
  // It is a tier of its own, ABOVE the capability veto rather than inside the
  // active branch, and that placement is the whole point. The capabilities in
  // hand during this window are the IDLE ones, and an idle thread advertises
  // queue:false (server/appwire_runtime.go's appCapabilities gates Queue on
  // `active`, which is `processing || appReservedTurnID != ""`). Letting the
  // veto see them turns this rule into BOTH_UNAVAILABLE and DISABLES the
  // composer in exactly the window it exists to serve - worse than the bounce,
  // which at least left a recovery row the user could resend from. An idle
  // queue:false means "no turn to queue behind", not "this harness has no
  // queue"; nothing in that snapshot distinguishes the two, so this tier does
  // not consult it. A harness with no queue at all answers turn/queue with
  // Unavailable, and the user sees that.
  //
  // The queue lands with no turn id because there is no turn id to send:
  // appwire v3 dropped expectedTurnId from turn/queue outright (appwire/
  // types.go's ProtocolVersion note), so neither handleAppTurnQueue nor the
  // agent's own clientMutationQueue has a turn precondition left to fail.
  // Pinned live by cmd/evener-hub/e2e_control_without_turn_ids_test.go's
  // TestE2E_ASendThatRacedAStopStillRuns, which proves a queue accepted
  // against a session with nothing running reaches a real model request.
  //
  // The terminal branch at the top answers with this same rule, for the same
  // reason - see its own comment for how a session the status calls finished
  // comes to be holding one of this client's sends.
  if (hasPendingSend) return QUEUE_MODE;

  return PLAIN_SEND_MODE;
}
