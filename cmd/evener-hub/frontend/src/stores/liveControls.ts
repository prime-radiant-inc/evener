// The web's one derivation of what a session may be asked to do, and its
// press-time reading of it.
//
// Every control surface (the composer's Stop/Steer/Send, the queue strip's
// drain and promote, the palette's session commands) derives a session's
// controls from the SDK's sessionControls over the wire's status, the harness's
// capabilities and the queue depth: controlsFor. A render offers a control from
// the model it rendered with; the press that follows can land after a status
// frame that render never saw, so a press handler asks pressRefusal, which
// re-derives the controls from the store at the moment the press lands. The
// palette's runWhenAvailable and native's requireControl are the same rule at
// their own boundaries.

import { NO_ACTIVE_TURN, type SessionControls, sessionControls, type ThreadModel } from "@evener/appwire-client";
import {
  isLocalRecoveryFenced,
  isResumeOnlyLocal,
  type ResumeOnlySignals,
  resumeOnlyLocalModel,
  threadsStore,
} from "./threads";

export type SessionControl = keyof SessionControls["reason"];

export function controlsFor(model: ThreadModel): SessionControls {
  return sessionControls(model.status.type, model.capabilities, model.queue?.depth ?? 0);
}

// The session's model as the store holds it now, not as a component rendered
// it: for the handlers that act on a press.
export function liveThreadModel(ref: string): ThreadModel | undefined {
  return threadsStore.getState().threads.get(ref);
}

// Why a press on `control` is refused against the session's live controls, or
// undefined when it may run. A session the store no longer holds has no turn
// to act on. `send` is the one control the hub's resume-only carve-out admits:
// a merely-resumable local session folds its resume into turn/start, so the
// press-time reading agrees with the offered Send instead of returning
// SEND_UNAVAILABLE for the fenced-but-resumable shape. Every other control and
// session keeps its own reason.
export function pressRefusal(ref: string, control: SessionControl): string | undefined {
  const model = liveThreadModel(ref);
  if (!model) return NO_ACTIVE_TURN;
  // The store-wide predicate, not isResumeOnlyLocal over this model: it reads
  // the delivery-uncertain signal from the pending-turns projection too, so a
  // press that lands with blockedUnknown rows fences exactly as the composer's
  // blockedMutations selector would have.
  if (control === "send" && resumeOnlyLocalModel(ref)) return undefined;
  return controlsFor(model).reason[control];
}

// The local recovery fence's shared predicate lives in threads.ts - beside
// the obligation state it reads, and where the store's own mutation admission
// uses it (threads.ts cannot import this module without an import cycle).
// Re-exported here so the control surfaces keep one import site for the fence
// and its press-time reading; Composer.tsx's availabilityFor and QueueStrip's
// press handlers derive from this one predicate (the composer module cannot
// lend QueueStrip its copy: Composer imports QueueStrip), and each call site
// adds the shape its own surface needs.
export { isLocalRecoveryFenced, isResumeOnlyLocal };

// The recovery fence as one reading, so its three consumers cannot drift:
//   resumeOnly  - the hub admits a folded turn/start (isResumeOnlyLocal).
//   stillFenced - the fence still blocks Send/Queue for any other shape.
//   fencedLocal - a stopped local snapshot still fenced, whose follow-up card
//                 keeps the control row reachable for the retained draft.
// `signals` is the store's delivery-uncertain / in-flight-Stop reading (see
// isResumeOnlyLocal): a caller that can see it passes it so a resumable session
// whose reconciliation is pending keeps the fence and the Resume affordance.
export interface RecoveryFenceReading {
  resumeOnly: boolean;
  stillFenced: boolean;
  fencedLocal: boolean;
}

export function recoveryFence(
  ref: string,
  model: Pick<ThreadModel, "resumeOnlyFoldable" | "status">,
  restartObligated: boolean,
  signals: ResumeOnlySignals = {},
): RecoveryFenceReading {
  const resumeOnly = isResumeOnlyLocal(ref, model, signals);
  const stillFenced = isLocalRecoveryFenced(ref, restartObligated) && !resumeOnly;
  return { resumeOnly, stillFenced, fencedLocal: stillFenced && model.status.type === "notLoaded" };
}

// The same fence as a press reads it: the obligation as the store holds it
// NOW, not as the subscribing render saw it (this module's own render-vs-press
// rule - a Stop can arm the fence between the render that offered a control
// and the press that follows). It is deliberately method-agnostic: none of its
// callers presses the send (the composer's submit runs its own live read,
// decideSubmitRoute over availabilityFor, which is where the resume-only
// carve-out belongs), and every verb it does guard - steer, the queue-strip
// actions, the recovery-fenced built-ins - is one the hub refuses for the
// obligation's whole window, turn/start's carve-out notwithstanding.
export function pressLocalRecoveryFenced(ref: string): boolean {
  return isLocalRecoveryFenced(ref, threadsStore.getState().restartBlockingObligations.has(ref));
}
