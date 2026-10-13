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
  isSendResumesLocal,
  type ResumeOnlySignals,
  sendDrivenLocalModel,
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
  if (control === "send" && sendDrivenLocalModel(ref)) return undefined;
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
export { isLocalRecoveryFenced, isResumeOnlyLocal, isSendResumesLocal };

// The recovery fence as one reading, so its three consumers cannot drift:
//   sendResumes - a fenced local session whose Send drives the resume: Send is
//                 offered and the store's driver resumes before dispatch.
//   resumeOnly  - the hub admits a folded turn/start (isResumeOnlyLocal).
//   sendDriven  - the union of the two above: the shapes whose only recovery
//                 action is a Send, so the pane/composer sites can ask the one
//                 question instead of recombining the pair.
//   stillFenced - the fence still blocks Send/Queue for any other shape.
//   fencedLocal - a stopped local snapshot still fenced, whose follow-up card
//                 keeps the control row reachable for the retained draft.
// `signals` is the store's delivery-uncertain / in-flight-Stop reading (see
// isResumeOnlyLocal): a caller that can see it passes it so a resumable session
// whose reconciliation is pending keeps the fence and the Resume affordance.
export interface RecoveryFenceReading {
  sendResumes: boolean;
  resumeOnly: boolean;
  sendDriven: boolean;
  stillFenced: boolean;
  fencedLocal: boolean;
}

export function recoveryFence(
  ref: string,
  model: Pick<ThreadModel, "resumeOnlyFoldable" | "status" | "parentRef">,
  restartObligated: boolean,
  signals: ResumeOnlySignals = {},
): RecoveryFenceReading {
  const resumeOnly = isResumeOnlyLocal(ref, model, signals);
  const sendResumes = isSendResumesLocal(ref, model, restartObligated, signals);
  // A Stop this page started is its OWN fence while it drains, independent of
  // the restart obligation: a stale thread refresh can clear
  // restartBlockingObligations while the forceStop RPC is still in flight, and
  // the hub holds Stopping > 0 (refusing even turn/start) for that whole
  // window. So stillFenced reads stopInFlight as its own clause rather than
  // only as a blocker of the resume-only carve-out (which isResumeOnlyLocal
  // already applies, making the two mutually exclusive).
  // The Send-resumes face is deliberately NOT stillFenced: it blocks Queue but
  // offers Send, which the store's driver backs. stillFenced stays "the fence
  // blocks Send/Queue".
  const stillFenced =
    isLocalRecoveryFenced(ref, restartObligated || signals.stopInFlight === true) && !resumeOnly && !sendResumes;
  return {
    sendResumes,
    resumeOnly,
    sendDriven: resumeOnly || sendResumes,
    stillFenced,
    fencedLocal: stillFenced && model.status.type === "notLoaded",
  };
}

// The same fence as a press reads it: the store's recovery state as it holds it
// NOW, not as the subscribing render saw it (this module's own render-vs-press
// rule - a Stop can arm the fence between the render that offered a control
// and the press that follows). The fence it reads is TWO clauses, both off one
// store snapshot: the restart obligation (restartBlockingObligations), and a
// Stop this page started that is still draining (stoppingRefs) as its OWN
// fence - a stale thread refresh can clear the obligation while the local
// forceStop RPC is still in flight, and the hub holds Stopping > 0 (refusing
// every verb this helper guards) for that whole window. It is deliberately
// method-agnostic: none of its callers presses the send (the composer's submit
// runs its own live read, decideSubmitRoute over availabilityFor, which is
// where the resume-only carve-out belongs), and every verb it does guard -
// steer, the queue-strip actions, the recovery-fenced built-ins - is one the
// hub refuses for the obligation's or the drain's whole window, turn/start's
// carve-out notwithstanding. turn/interrupt is NOT among them: the composer's
// Stop press reads pressRefusal(ref, "stop") directly, so the interrupt that
// ends a drain never passes through this fence.
export function pressLocalRecoveryFenced(ref: string): boolean {
  const state = threadsStore.getState();
  return isLocalRecoveryFenced(ref, state.restartBlockingObligations.has(ref) || state.stoppingRefs.has(ref));
}
