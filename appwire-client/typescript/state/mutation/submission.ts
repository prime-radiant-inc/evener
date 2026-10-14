import type { PendingTurnsStore, SubmittedDraft } from "./pendingTurns";
import type { MutationProjectionFence } from "./projection";
import type { MutationProjectionWorkTracker } from "./projectionWork";
import type { MutationAttachmentRef } from "./records";

// What one submission needs tracked and settled: which ref it targets, the
// preparation that captures its immutable draft snapshot, the caller's own
// failure handler, and - via `SubmittedDraft` - the snapshot
// `store.settleSubmittedDraft` compares the current draft against.
// `draftRevisionAtStart` arrives through `prepare`'s result, read by the host
// through its own draft port inside that call, so no draft port is duplicated
// here.
export interface MutationSubmissionOptions {
  ref: string;
  // Captures the submission's immutable draft snapshot INSIDE the ownership
  // window, after `beginSubmission` and before `perform`. A host's durable
  // draft capture is part of the submission it feeds - on the web it is a
  // readiness plus IndexedDB roundtrip - so a same-ref press while it is in
  // flight must hear the guard's existing refusal instead of waiting the
  // window out and double-sending once the first submission releases
  // (#4605).
  prepare: () => Promise<SubmittedDraft>;
  onFailure: (error: unknown) => void;
  // A browser host passes this to settle a submission asynchronously against
  // its durable draft row. When present it replaces the synchronous
  // `store.settleSubmittedDraft` expression below: this module awaits it and
  // reports only its result. Native and every synchronous host leave it
  // undefined and keep the existing synchronous branch unchanged.
  settleDraft?: (ref: string, submitted: SubmittedDraft) => Promise<MutationSubmissionCommitted>;
}

// Exactly what `store.settleSubmittedDraft` returns - named here as the
// report `onCommitted` gets, for a caller that wants to notify its own
// listeners once a submission commits (the web's composer draft UI and
// recovery tray, for instance, neither of which this module names).
export type MutationSubmissionCommitted = ReturnType<PendingTurnsStore["settleSubmittedDraft"]>;

// Runs one submission through the store's begin/end lifecycle, the caller's
// preparation, and whichever draft-settling rule the caller supplies ("clear
// only if unchanged" is the web's own rule; native's is the inverse, and this
// function makes no choice between them). `beginSubmission` has already run
// by the time `prepare` starts, so the ownership window spans the whole
// pipeline: prepare, perform, settle. By default settlement defers to
// `store.settleSubmittedDraft`; a browser host instead passes `settleDraft`
// to await its own durable settlement, and the synchronous store expression
// is then not evaluated.
//
// `track` registers the whole call, from before `prepare` starts, as
// outstanding - not just the refresh it ends with. A caller that instead
// tracked only the tail left a window where a settle round found nothing
// outstanding while a send was still in flight (kata 3p22); tracking from
// here makes one zero round genuine proof, for any host.
//
// `refresh` runs in the `finally` unconditionally: a submission's durable
// outcome is already known by then (including a preparation failure's -
// nothing dispatched), so nothing here can delay or change it. A browser
// hook's own observer and refresh faults cannot reject already accepted
// input, so they are isolated there; the default branch keeps its existing
// unguarded error behavior. `onCommitted` and the epoch-guarded
// `endSubmission` release, by contrast, run only if the fence's epoch has not
// moved since this call started - submission ownership outlives a mounted
// composer, so a retired mount must not clear a newer draft written after a
// tab switch, nor release a guard a fresher call already owns.
export function submitWithPendingTracking<A extends MutationAttachmentRef = MutationAttachmentRef>(
  store: PendingTurnsStore<A>,
  fence: MutationProjectionFence<A>,
  track: MutationProjectionWorkTracker["track"],
  opts: MutationSubmissionOptions,
  perform: (submitted: SubmittedDraft) => Promise<void>,
  refresh: (ref?: string) => void,
  onCommitted?: (committed: MutationSubmissionCommitted, submitted: SubmittedDraft) => void,
): Promise<void> {
  if (!store.beginSubmission(opts.ref)) {
    return Promise.reject(new Error("A message submission is already pending for this task"));
  }
  const epoch = fence.epoch();
  return track(
    (async () => {
      try {
        let submitted: SubmittedDraft;
        try {
          submitted = await opts.prepare();
        } catch (error) {
          // A failed preparation reports through the same onFailure the
          // dispatch path uses - the press's failure feedback fires - and the
          // rejection still propagates: nothing dispatched, so the caller's
          // own catch can run its own non-toast bookkeeping.
          opts.onFailure(error);
          throw error;
        }
        try {
          await perform(submitted);
        } catch (error) {
          opts.onFailure(error);
          throw error;
        }
        if (epoch === fence.epoch()) {
          if (opts.settleDraft) {
            let committed: MutationSubmissionCommitted;
            try {
              committed = await opts.settleDraft(opts.ref, submitted);
            } catch {
              // A failed settlement is conservatively "neither cleared nor
              // unchanged"; it never relabels the accepted submission as failed.
              committed = { cleared: false, draftUnchanged: false };
            }
            if (epoch === fence.epoch()) {
              try {
                onCommitted?.(committed, submitted);
              } catch {
                // Accepted input remains accepted even if an observer throws.
              }
            }
          } else {
            onCommitted?.(store.settleSubmittedDraft(opts.ref, submitted), submitted);
          }
        }
      } finally {
        if (epoch === fence.epoch()) {
          store.endSubmission(opts.ref);
        }
        if (opts.settleDraft) {
          try {
            refresh(opts.ref);
          } catch {
            // A browser refresh fault cannot reject already accepted input.
          }
        } else {
          refresh(opts.ref);
        }
      }
    })(),
  );
}

// Binds the four singletons a host has exactly one of (its store, fence,
// work tracker and refresh) once, at module scope - the convention this
// directory otherwise uses for a long-lived host wiring
// (`createPendingTurnsStore`, `createMutationProjectionWorkTracker`,
// `wireMutationCommitFeed`). Only `opts`, `perform` and `onCommitted` differ
// per submission, so the returned function takes exactly those.
export function createSubmissionRunner<A extends MutationAttachmentRef = MutationAttachmentRef>(
  store: PendingTurnsStore<A>,
  fence: MutationProjectionFence<A>,
  track: MutationProjectionWorkTracker["track"],
  refresh: (ref?: string) => void,
): (
  opts: MutationSubmissionOptions,
  perform: (submitted: SubmittedDraft) => Promise<void>,
  onCommitted?: (committed: MutationSubmissionCommitted, submitted: SubmittedDraft) => void,
) => Promise<void> {
  return (opts, perform, onCommitted) =>
    submitWithPendingTracking(store, fence, track, opts, perform, refresh, onCommitted);
}
