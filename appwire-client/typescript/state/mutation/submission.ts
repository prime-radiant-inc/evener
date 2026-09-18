import type { PendingTurnsStore } from "./pendingTurns";
import type { MutationProjectionFence } from "./projection";
import type { MutationAttachmentRef } from "./records";

// What one submission needs tracked and settled: which ref it targets, the
// draft snapshot to compare against once it commits, and the caller's own
// failure handler. `draftRevisionAtStart` is read by the host through its
// own draft port before calling in, so no draft port is duplicated here.
export interface MutationSubmissionOptions {
  ref: string;
  draftRevisionAtStart: number;
  text: string;
  skillNames: readonly string[];
  onFailure: (error: unknown) => void;
}

// What `store.settleSubmittedDraft` decided, handed back to a caller that
// wants to notify its own listeners once a submission commits - the web's
// composer draft UI and recovery tray, for instance, neither of which this
// module names.
export interface MutationSubmissionCommitted {
  clearedDraft: boolean;
  draftUnchanged: boolean;
}

// Runs one submission through the store's begin/end lifecycle and whichever
// draft-settling rule the given store implements ("clear only if unchanged"
// is the web's own rule; native's is the inverse, and this function makes no
// choice between them - it defers entirely to `store.settleSubmittedDraft`).
//
// `track` registers the whole call, from before `perform` starts, as
// outstanding - not just the refresh it ends with. A caller that instead
// tracked only the tail left a window where a settle round found nothing
// outstanding while a send was still in flight (kata 3p22); tracking from
// here makes one zero round genuine proof, for any host.
//
// `refresh` runs in the `finally` unconditionally: a submission's durable
// outcome is already known by then, so nothing here can delay or change it.
// `onCommitted` and the epoch-guarded `endSubmission` release, by contrast,
// run only if the fence's epoch has not moved since this call started -
// submission ownership outlives a mounted composer, so a retired mount must
// not clear a newer draft written after a tab switch, nor release a guard a
// fresher call already owns.
export function submitWithPendingTracking<A extends MutationAttachmentRef = MutationAttachmentRef>(
  store: PendingTurnsStore<A>,
  fence: MutationProjectionFence<A>,
  track: <T>(work: Promise<T>) => Promise<T>,
  opts: MutationSubmissionOptions,
  perform: () => Promise<void>,
  refresh: (ref?: string) => void,
  onCommitted?: (committed: MutationSubmissionCommitted) => void,
): Promise<void> {
  if (!store.beginSubmission(opts.ref)) {
    return Promise.reject(new Error("A message submission is already pending for this task"));
  }
  const epoch = fence.epoch();
  return track(
    (async () => {
      try {
        try {
          await perform();
        } catch (error) {
          opts.onFailure(error);
          throw error;
        }
        if (epoch === fence.epoch()) {
          const settled = store.settleSubmittedDraft(opts.ref, {
            draftRevisionAtStart: opts.draftRevisionAtStart,
            text: opts.text,
            skillNames: opts.skillNames,
          });
          onCommitted?.({ clearedDraft: settled.cleared, draftUnchanged: settled.draftUnchanged });
        }
      } finally {
        if (epoch === fence.epoch()) {
          store.endSubmission(opts.ref);
        }
        refresh(opts.ref);
      }
    })(),
  );
}
