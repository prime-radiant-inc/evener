import type { ThreadModel } from "@evener/appwire-client";

// The recovery fence. A session carrying a restart-blocking
// obligation (a Stop in flight, or a snapshot the daemon reports as
// restartRequired/resumeRequired) admits no session action at all while the
// obligation stands: the hub's recovery admission refuses turn/steer,
// turn/queue and every other fenced mutation for exactly that window
// (cmd/evener-hub's sessionActionRecoveryError reads the resume locks, never
// the projected status), with turn/start carved out only for the merely-
// resumable shape isResumeOnly names, so even a still-ACTIVE snapshot is
// fenced while
// a Stop drains - a live read relays the daemon's active status with
// resumeRequired overlaid beside it (applyThreadResumeRequirement), and the
// store arms the obligation on that very hydration. An offered press in that
// window could only mint durable intent that parks until the explicit Resume
// action clears the fence. The store's own mutation admission enforces it
// (enqueueMutationIntent) and liveControls re-exports it for the surfaces.
// Every ref shape carries the fence: a remote session's hub overlays
// resumeRequired on the read it proxies back, so the same obligation reaches
// this predicate.
export function isRecoveryFenced(restartObligated: boolean): boolean {
  return restartObligated;
}

// A LOCAL session the hub admits a folded send for. The authority is the hub's
// OWN answer, not a shape this client infers: ResumeOnlyFoldable (whom
// applyThreadResumeRequirement stamps) is true exactly when a turn/start on the
// current connection would be admitted under the resume-only carve-out
// (cmd/evener-hub's sessionAdmitsResumeRequired, read without a request: the
// requirement is set, no Stop is draining, the exit is confirmed, and no
// connection fence applies). The client no longer reads resumeRequired plus a
// cleared send capability as this shape - that overlay (applyThreadResumeRequirement)
// sets both for a Stop drain, an unconfirmed force-stop exit, the
// connection-recovery fence, and an incompatible-protocol daemon too, four
// shapes whose turn/start the hub still refuses, so inferring from them offered
// Send where the hub would refuse it.
//
// Deliberately narrow - it is NOT the whole recovery fence. Every clause below
// excludes a shape the hub would refuse or one whose send would starve:
//
//   - the hub bit must be true; an older or non-local snapshot that never
//     carried it is not this shape.
//   - no status clause: the hub's admission is status-independent
//     (sessionActionRecoveryError / resumeOnlyFoldable read no status), so a
//     live snapshot the hub legitimately stamped foldable - a daemon another
//     controller started under a confirmed force-stop obligation - is admitted
//     and folds. A status clause here would hide the folded send from a
//     snapshot the hub would happily admit, silently falling back to the old
//     explicit-Resume step the fold exists to remove. The stale bit a shut-down
//     snapshot leaves behind is defended against where it is written, not here:
//     the off-shut-down transition clears it (settleResumeOnlyOffShutdown) and
//     a connection-generation change invalidates it
//     (invalidateHeldHistoriesForReconnect), so a bit whose shut-down snapshot
//     has ended cannot mis-route a send.
//   - signals.uncertainMessages must be false: while delivery-uncertain rows
//     exist the hub's explicit Resume still runs reconciliation, so the send is
//     not the whole story and the Resume affordance stays.
//   - signals.stopInFlight must be false: a snapshot taken before this page's
//     Stop was answered can still carry the bit the hub stamped before the
//     Stop's drain began, so the local in-flight Stop keeps the fence.
//   - signals.queuedNonSend must be false: a queued non-turn/start durable row
//     (still "submitting", attempted or not) parks at the target's FIFO ahead
//     of a turn/start, so folding a send behind it would silently never
//     deliver it.
//
// A Stop in flight / active drain, a restartRequired daemon that needs the
// older daemon stopped first, and a stale-connection snapshot all leave the hub
// bit false; all keep the fence.
export interface ResumeOnlySignals {
  // The store's delivery-uncertain rows (blockedUnknown).
  uncertainMessages?: boolean;
  // A Force stop this page started is still draining (stoppingRefs).
  stopInFlight?: boolean;
  // A queued non-turn/start durable row (still "submitting", attempted or not)
  // ahead of the send (the pending-turns projection's hasQueuedNonSend).
  queuedNonSend?: boolean;
  // The store's mutation-recency reading (mutationAuthorityRefs), read only by
  // the Send-resumes face below: a local snapshot whose parent still owns its
  // uncertain rows (Session.tsx's recoveryOwnerRef) is not Send-driven.
  mutationStateAuthoritative?: boolean;
}

export function isResumeOnly(model: Pick<ThreadModel, "resumeOnlyFoldable">, signals: ResumeOnlySignals = {}): boolean {
  return (
    model.resumeOnlyFoldable === true &&
    signals.uncertainMessages !== true &&
    signals.stopInFlight !== true &&
    signals.queuedNonSend !== true
  );
}

// A snapshot the pane shows as retained by its owning session: its uncertain
// rows are the owner's to reconcile, and the pane offers the owner's link and a
// Refresh - never a resume, because the hub refuses to resume a session another
// session still owns. Any parent ref counts, local or remote. The ONE
// definition, consumed by the Send-resumes face here, the session notice, and
// SessionChrome's force-stop eligibility.
export function ownerRetainedRef(
  model: Pick<ThreadModel, "status" | "parentRef">,
  mutationStateAuthoritative: boolean,
): string | undefined {
  return !mutationStateAuthoritative &&
    model.status.type !== "notLoaded" &&
    model.status.type !== "restartRequired" &&
    model.parentRef !== undefined &&
    model.parentRef !== ""
    ? model.parentRef
    : undefined;
}

// The recovery fence's Send-driven face: any session carrying a
// restart-blocking obligation. Send is the single recovery action; the store's
// driver resumes before the parked send dispatches. Two shapes keep their own
// controls because Send cannot complete them:
//   - a restartRequired daemon needs its older process stopped first, which
//     Send cannot do;
//   - an owner-retained snapshot's rows belong to its owner.
// Everything else - every ref shape, delivery-uncertain rows, a queued
// non-send row, an unconfirmed exit, the connection fence, and a draining Stop
// (the driver waits the drain out) - offers Send.
export function isSendResumes(
  model: Pick<ThreadModel, "resumeOnlyFoldable" | "status" | "parentRef">,
  restartObligated: boolean,
  signals: ResumeOnlySignals = {},
): boolean {
  if (!restartObligated) return false;
  if (model.status.type === "restartRequired") return false;
  if (ownerRetainedRef(model, signals.mutationStateAuthoritative === true) !== undefined) return false;
  return !isResumeOnly(model, signals);
}
