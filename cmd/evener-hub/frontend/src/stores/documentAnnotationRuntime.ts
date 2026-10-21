// Browser document-annotation runtime retry coordinator.
//
// Failed saves are immutable, session-bound runtime work retained here (not a
// second registry of readers or durable owners). Each retry reason coalesces into
// one bounded pass that first re-saves retained failures, then asks the real
// browser repository for its completed outcome. retry-needed is kept until a
// completed pass proves no retained failure, no persisted pending handoff and no
// additional pending observation occurred. The runtime never self-reschedules:
// only a later external trigger starts the next pass. See the Task 3 design's
// bounded retry-health transitions.
import type {
  AnnotationRetryReason,
  DocumentKey,
  HandoffResult,
  PreparedAnnotation,
  SaveReceipt,
} from "@evener/appwire-client";
import {
  type AnnotationRetryCancellation,
  type AnnotationSaveCancellation,
  annotationDocumentKey,
  annotationOperationKey,
  type BrowserDocumentAnnotationRepository,
} from "./documentAnnotationIndexedDB";

export interface DocumentAnnotationRuntime {
  save(operation: PreparedAnnotation): Promise<SaveReceipt | null>;
  handoff(key: DocumentKey, operationId: string): Promise<HandoffResult>;
  retryPending(reason: AnnotationRetryReason): Promise<void>;
  whenIdle(): Promise<void>;
  storageSucceeded(): void;
  // The repository's own retained work (a failed expiry fence) registers its
  // retry need here: no failed save or pending handoff will, so without this
  // the next storage-success notification cannot start the completing pass.
  registerRetryNeed(): void;
  // Retire the failed saves of one retired authority: only operations whose
  // lease names the retired lease's namespace incarnation and epoch leave the
  // retry map, so a reader retiring its own dead authority cannot also drop a
  // failed save another reader of the same document holds against the live
  // replacement epoch.
  invalidate(key: DocumentKey, retired: PreparedAnnotation["lease"] | null): void;
  // Retire exactly ONE retained failed operation, keyed by its existing
  // namespace-qualified operation identity, leaving every other operation in the
  // shared failed-work set untouched. Cancel uses this to dismiss only its own
  // unsaved attempt without discarding a different reader's failed work for the
  // same document.
  retireAttempted(operation: PreparedAnnotation): void;
  dispose(): void;
}

export interface DocumentAnnotationPorts {
  repository: BrowserDocumentAnnotationRepository;
  runtime: DocumentAnnotationRuntime;
}

export function createDocumentAnnotationRuntime(repo: BrowserDocumentAnnotationRepository): DocumentAnnotationRuntime {
  const failed = new Map<string, PreparedAnnotation>();
  let pass: Promise<void> | null = null;
  // The sweep part of the running pass, without its follow-up. A direct handoff
  // queued mid-pass joins the chain the follow-up also waits on, so waiting for
  // the whole pass would leave each waiting for the other: this separates the
  // handoff work it can race from the follow-up that runs behind it.
  let sweep: Promise<void> | null = null;
  let retryNeeded = false;
  let pendingDuringPass = false;
  // A recovery trigger arriving while a pass runs names state that pass may
  // already have visited: the composer its handoff skipped can have become
  // claimable after the sweep moved on. Retain one such trigger and run one
  // coalesced follow-up when the pass settles; a trigger during that follow-up
  // coalesces into at most one more pass the same way. Disposal clears it, so
  // no follow-up is ever scheduled after close.
  let followUpRequested = false;
  let followUpReason: AnnotationRetryReason | null = null;
  // A direct handoff records an outcome only when it RETURNS: while its
  // preparation is outstanding the refusal that sets retryNeeded has not
  // happened, and no pass exists to keep an observation for. A storage commit
  // landing then names work this commit may have made claimable, so dropping
  // the trigger leaves the annotation pending until unrelated activity
  // arrives. Retain one trigger across EVERY outstanding handoff - queued ones
  // included, because a queued handoff runs before any pass's sweep and can
  // refuse the same way - and let the last one to settle spend it: one
  // serialized pass if any of them left work pending, nothing if they all
  // applied. Disposal clears it, so nothing schedules after close.
  let outstandingHandoffs = 0;
  let handoffRecoveryTriggered = false;
  let alive = true;
  // The pass hands off through the repository's own sweep, so disposal must be
  // observable between those handoffs and not only at this pass's outer
  // boundaries: a disposed runtime leaves the sweep's unreached operations
  // pending instead of appending to a retained composer.
  const retryCancellation: AnnotationRetryCancellation = {
    get cancelled() {
      return !alive;
    },
  };
  // Handoffs serialize: the composer source's preparation machinery is
  // single-flight, so two concurrent handoffs to one composer can deadlock
  // one another, and even when they interleave harmlessly the second
  // preparation captures a revision the first is about to replace - its
  // revision conflict then strands the reference behind a storage-success
  // notification that already fired with no retry need to act on. Chaining
  // behind every prior handoff and any running pass gives each handoff a
  // fresh, settled preparation whose append is the only one in flight.
  let handoffChain: Promise<unknown> = Promise.resolve();
  const id = (operation: PreparedAnnotation) =>
    annotationOperationKey(operation.lease.key.namespace, operation.operationId);
  // One operation-scoped cancellation handle per operation, alive only while a
  // save for it is in flight (reference-counted so concurrent saves for the same
  // operation share it). retireAttempted/dispose cancel it so the repository's
  // active save transaction aborts before commit; each save releases it at
  // settlement so no listener or handle outlives the save.
  const cancellations = new Map<string, { token: AnnotationSaveCancellation; cancel: () => void; active: number }>();
  const createCancellation = (): { token: AnnotationSaveCancellation; cancel: () => void } => {
    const state = { cancelled: false, listeners: new Set<() => void>() };
    const cancel = (): void => {
      if (state.cancelled) return;
      state.cancelled = true;
      const listeners = [...state.listeners];
      state.listeners.clear();
      for (const listener of listeners) listener();
    };
    const token: AnnotationSaveCancellation = {
      get cancelled() {
        return state.cancelled;
      },
      onCancel: (listener) => {
        if (state.cancelled) {
          listener();
          return () => {};
        }
        state.listeners.add(listener);
        return () => {
          state.listeners.delete(listener);
        };
      },
    };
    return { token, cancel };
  };
  const needRetry = () => {
    if (alive) {
      retryNeeded = true;
      if (pass) pendingDuringPass = true;
    }
  };
  const save = async (input: PreparedAnnotation): Promise<SaveReceipt | null> => {
    if (!alive) return null;
    // Retain the immutable operation BEFORE the fallible await; a later caller
    // mutation cannot change what a retry persists.
    const operation = failed.get(id(input)) ?? structuredClone(input);
    const operationKey = id(operation);
    failed.set(operationKey, operation);
    let entry = cancellations.get(operationKey);
    if (entry === undefined) {
      const created = createCancellation();
      entry = { token: created.token, cancel: created.cancel, active: 0 };
      cancellations.set(operationKey, entry);
    }
    entry.active += 1;
    try {
      const receipt = await repo.saveAnnotation(operation, entry.token);
      if (failed.get(operationKey) === operation) failed.delete(operationKey);
      return receipt;
    } catch {
      // A cancelled save is a handled dismissal, never retryable work: its
      // operation was already retired from the failed set by retireAttempted.
      if (entry.token.cancelled) return null;
      needRetry();
      return null;
    } finally {
      entry.active -= 1;
      if (entry.active === 0 && cancellations.get(operationKey) === entry) cancellations.delete(operationKey);
    }
  };
  const startPass = (reason: AnnotationRetryReason): Promise<void> => {
    pendingDuringPass = false; // before assigning pass, never erase observations made while it runs
    followUpRequested = false;
    followUpReason = null;
    // The pass's persisted sweep drives handoffs at the repository level, so
    // it joins the same chain: an in-flight direct handoff finishes first and
    // the sweep never races it for the composer.
    const current = handoffChain.then(async () => {
      for (const operation of [...failed.values()]) {
        if (!alive) return;
        // invalidate may have removed this attempt while an earlier save waited.
        if (failed.get(id(operation)) === operation) await save(operation);
      }
      if (alive)
        try {
          const outcome = await repo.retryPendingWithOutcome(reason, retryCancellation);
          if (alive) retryNeeded = failed.size > 0 || outcome.pending || pendingDuringPass;
        } catch {
          needRetry();
        }
    });
    const withFollowUp = current.then(async () => {
      if (!alive || !followUpRequested) return;
      // Read the retained reason before startPass resets the entry point: the
      // follow-up's own trigger handling stays independent of this pass's.
      const nextReason = followUpReason ?? reason;
      await startPass(nextReason);
    });
    pass = withFollowUp;
    sweep = current;
    const settle = () => {
      // A nested follow-up owns the slot while it runs, and a later trigger
      // outside every pass starts its own.
      if (pass === withFollowUp) {
        pass = null;
        sweep = null;
      }
    };
    void withFollowUp.then(settle, settle);
    return withFollowUp;
  };
  const retryPending = (reason: AnnotationRetryReason): Promise<void> => {
    if (!alive) return Promise.resolve();
    if (pass) {
      // This trigger reports state the running pass may already have visited:
      // retain it for one coalesced follow-up instead of dropping it.
      followUpRequested = true;
      followUpReason ??= reason;
      return pass;
    }
    return startPass(reason);
  };
  // Settlement bookkeeping for one direct handoff. Only the last outstanding
  // handoff can spend the retained trigger: a pending one has already called
  // needRetry(), and one settling success never cancels the refusal another
  // queued handoff has still to record. At the moment this schedules, no other
  // handoff is in flight or queued and this handoff's own chain entry has
  // settled, so the pass waits only on settled work.
  const settleHandoff = (leftPending: boolean): void => {
    outstandingHandoffs -= 1;
    if (outstandingHandoffs > 0) return;
    const retained = handoffRecoveryTriggered;
    handoffRecoveryTriggered = false;
    if (!alive || !retained || !leftPending) return;
    void retryPending("storage-recovered");
  };
  return {
    save,
    retryPending,
    whenIdle: () => pass ?? Promise.resolve(),
    handoff: (key, operationId) => {
      // A disposed runtime owns no composer work: a handoff arriving after
      // disposal, or waiting in the serialization chain when disposal lands,
      // must answer cancelled instead of running against a repository whose
      // connection can reopen and appending to a retained composer after
      // application teardown.
      if (!alive) return Promise.resolve<HandoffResult>({ state: "cancelled" });
      // Counted from the call rather than from the moment the chain reaches it:
      // a queued handoff's outcome is just as unrecorded as a running one's.
      outstandingHandoffs += 1;
      const prior = sweep ? Promise.all([sweep, handoffChain]) : handoffChain;
      const run = prior.then(
        () =>
          alive
            ? repo.handoff(key, operationId, retryCancellation)
            : Promise.resolve<HandoffResult>({ state: "cancelled" }),
        () =>
          alive
            ? repo.handoff(key, operationId, retryCancellation)
            : Promise.resolve<HandoffResult>({ state: "cancelled" }),
      );
      handoffChain = run.then(
        () => undefined,
        () => undefined,
      );
      return run.then(
        (result) => {
          const leftPending = result.state === "pending";
          if (leftPending) needRetry();
          settleHandoff(leftPending);
          return result;
        },
        (error) => {
          // A rejected handoff is an unobserved outcome the retry map cannot
          // show: spend a retained trigger on the pass that can record it.
          settleHandoff(true);
          throw error;
        },
      );
    },
    storageSucceeded: () => {
      if (!alive) return;
      if (pass) {
        // A storage commit during a pass is the same retained trigger: it may
        // have made a skipped handoff claimable after the sweep passed it. Do
        // not gate on retryNeeded, which the running pass has not updated yet.
        followUpRequested = true;
        followUpReason ??= "storage-recovered";
        return;
      }
      if (retryNeeded) {
        void retryPending("storage-recovered");
        return;
      }
      // Nothing recorded a need and no pass runs: an outstanding direct handoff
      // is the only work whose outcome this notification cannot see yet.
      if (outstandingHandoffs > 0) handoffRecoveryTriggered = true;
    },
    registerRetryNeed: () => {
      needRetry();
    },
    invalidate: (key, retired) => {
      for (const [operationKey, operation] of failed)
        if (
          retired !== null &&
          annotationDocumentKey(operation.lease.key) === annotationDocumentKey(key) &&
          operation.lease.namespaceIncarnation === retired.namespaceIncarnation &&
          operation.lease.epoch === retired.epoch
        )
          failed.delete(operationKey);
    },
    retireAttempted: (operation) => {
      failed.delete(id(operation));
      // Reach the operation's in-flight persistence: abort its active save
      // transaction before commit. A save that has not yet opened observes the
      // cancellation at its next boundary.
      cancellations.get(id(operation))?.cancel();
    },
    dispose: () => {
      alive = false;
      failed.clear();
      for (const entry of cancellations.values()) entry.cancel();
      cancellations.clear();
      retryNeeded = false;
      pendingDuringPass = false;
      followUpRequested = false;
      followUpReason = null;
      handoffRecoveryTriggered = false;
    },
  };
}
