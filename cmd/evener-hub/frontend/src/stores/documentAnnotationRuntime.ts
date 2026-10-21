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
  // same document. The dismissal is durable: it reaches the repository even
  // when the operation has no in-flight save left to cancel, fencing the
  // identity and flipping its committed row terminal.
  retireAttempted(operation: PreparedAnnotation): void;
  // Application teardown. Aborts every in-flight save transaction it can still
  // abort, but is NOT a dismissal: it never fences an identity or flips a
  // committed row, so committed work survives pending for whichever runtime
  // retries after teardown.
  dispose(): void;
}

export interface DocumentAnnotationPorts {
  // The session wiring constructs these two as one pair: a repository and the
  // retry runtime that drives its retained work.
  repository: BrowserDocumentAnnotationRepository;
  runtime: DocumentAnnotationRuntime;
}

export function createDocumentAnnotationRuntime(repo: BrowserDocumentAnnotationRepository): DocumentAnnotationRuntime {
  const failed = new Map<string, PreparedAnnotation>();
  // Work whose outcome a storage-success trigger cannot see yet: the running
  // pass, every queued or running direct handoff, and every in-flight save.
  // One trigger slot and one spend rule cover all three, so a save and a
  // handoff can never grow the per-window patches that a pass once needed.
  let pass: Promise<void> | null = null;
  // The idle chain: the current pass plus every follow-up pass its settlement
  // starts. whenIdle waits on this, so a trigger retained during a pass still
  // keeps the caller waiting for the pass that spends it, even though a
  // follow-up is a sibling pass rather than a continuation of this pass's own
  // promise.
  let idle: Promise<void> = Promise.resolve();
  let retryNeeded = false;
  // ONE retained trigger: a storage success (or explicit retry request) that
  // arrived while work was outstanding, coalesced to its first reason. The
  // last unit of outstanding work spends it iff retry-needed is set or that
  // unit's own outcome left work pending; otherwise the trigger named state
  // the settled work already covered and is dropped. Disposal clears it, so a
  // latched trigger never schedules a pass after close.
  let latchedTrigger: AnnotationRetryReason | null = null;
  // A direct handoff records an outcome only when it RETURNS or REJECTS:
  // while its preparation is outstanding the refusal that sets retry-needed
  // has not happened, and no pass exists to keep an observation for. A commit
  // landing then names work it may have made claimable, so the trigger is
  // retained across every outstanding handoff - queued ones included, because
  // a queued handoff runs before any pass's sweep and can refuse the same way.
  let outstandingHandoffs = 0;
  // A save's outcome is equally unrecorded while its transaction is in
  // flight: its failure records retry-needed only when the rejection lands,
  // and a trigger arriving meanwhile is the only observation of the storage
  // commit that failure may have left claimable.
  let outstandingSaves = 0;
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
  // operation share it). retireAttempted cancels it as a DISMISSAL so the
  // repository's active save transaction aborts before commit and the identity
  // is fenced; each save releases it at settlement so no listener or handle
  // outlives the save. dispose cancels as TEARDOWN: abort only, never a fence
  // or a durable flip, so a committed row survives pending.
  const cancellations = new Map<
    string,
    {
      token: AnnotationSaveCancellation;
      cancel: (reason: "dismissal" | "teardown") => void;
      active: number;
    }
  >();
  const createCancellation = (): {
    token: AnnotationSaveCancellation;
    cancel: (reason: "dismissal" | "teardown") => void;
  } => {
    const state: { reason: "dismissal" | "teardown" | null; listeners: Set<() => void> } = {
      reason: null,
      listeners: new Set(),
    };
    const cancel = (reason: "dismissal" | "teardown"): void => {
      if (state.reason !== null) return;
      state.reason = reason;
      const listeners = [...state.listeners];
      state.listeners.clear();
      for (const listener of listeners) listener();
    };
    const token: AnnotationSaveCancellation = {
      get cancelled() {
        return state.reason !== null;
      },
      get reason() {
        return state.reason;
      },
      onCancel: (listener) => {
        if (state.reason !== null) {
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
    if (alive) retryNeeded = true;
  };
  // The pass slot, the outstanding handoffs and the in-flight saves are one
  // set: a trigger can see none of their outcomes until they settle.
  const outstandingWork = (): number => (pass === null ? 0 : 1) + outstandingHandoffs + outstandingSaves;
  const save = async (input: PreparedAnnotation): Promise<SaveReceipt | null> => {
    if (!alive) return null;
    // Retain the immutable operation BEFORE the fallible await; a later caller
    // mutation cannot change what a retry persists.
    const operationKey = id(input);
    const operation = failed.get(operationKey) ?? structuredClone(input);
    failed.set(operationKey, operation);
    let entry = cancellations.get(operationKey);
    if (entry === undefined) {
      const created = createCancellation();
      entry = { token: created.token, cancel: created.cancel, active: 0 };
      cancellations.set(operationKey, entry);
    }
    entry.active += 1;
    outstandingSaves += 1;
    try {
      const receipt = await repo.saveAnnotation(operation, entry.token);
      if (failed.get(operationKey) === operation) failed.delete(operationKey);
      return receipt;
    } catch {
      // A cancelled save is handled work, never retryable: a dismissal already
      // retired its operation from the failed set, and teardown cleared it.
      if (entry.token.cancelled) return null;
      needRetry();
      return null;
    } finally {
      entry.active -= 1;
      if (entry.active === 0 && cancellations.get(operationKey) === entry) cancellations.delete(operationKey);
      // A handled dismissal settles here too: it is still work whose outcome
      // a retained trigger was waiting on.
      outstandingSaves -= 1;
      settleWork(false);
    }
  };
  // The ONE spend rule every kind of outstanding work shares: the pass, a
  // direct handoff and a save all settle here. Only the LAST unit to settle
  // reads the latch, and it spends it on work still pending - retry-needed
  // covers a failed save, a refused handoff, a rejection the retry map cannot
  // show, and a pass outcome that left persisted work; settledWorkPending
  // covers a work unit whose own answer left something claimable. At the
  // moment a pass settles, the pass slot is already clear and every queued
  // handoff is still counted, so a handoff queued mid-pass defers the spend to
  // its own settlement instead of racing this one. Returns the pass chain it
  // started, so a pass settlement can chain a follow-up into whenIdle.
  const spendTrigger = (settledWorkPending: boolean): Promise<void> | undefined => {
    if (outstandingWork() > 0) return undefined;
    const trigger = latchedTrigger;
    if (trigger === null) return undefined;
    if (!alive || (!settledWorkPending && !retryNeeded)) {
      latchedTrigger = null;
      return undefined;
    }
    latchedTrigger = null;
    return startPass(trigger);
  };
  const settleWork = (settledWorkPending: boolean): void => {
    void spendTrigger(settledWorkPending);
  };
  const startPass = (reason: AnnotationRetryReason): Promise<void> => {
    if (!alive) return Promise.resolve();
    // Taking the slot clears the need the pass is about to spend; a need
    // recorded while it runs sets the flag again and line 251 keeps it.
    retryNeeded = false;
    // The pass's persisted sweep drives handoffs at the repository level, so
    // it joins the same chain: in-flight direct handoffs finish first and the
    // sweep never races them for the composer.
    const run = handoffChain.then(async () => {
      for (const operation of [...failed.values()]) {
        if (!alive) return;
        // invalidate may have removed this attempt while an earlier save waited.
        if (failed.get(id(operation)) === operation) await save(operation);
      }
      if (alive)
        try {
          const outcome = await repo.retryPendingWithOutcome(reason, retryCancellation);
          if (alive) retryNeeded = retryNeeded || failed.size > 0 || outcome.pending;
        } catch {
          needRetry();
        }
    });
    pass = run;
    // A trigger retained during this pass is spent when it settles, as a
    // SIBLING pass ordered behind the handoff chain rather than a continuation
    // chained INTO this pass's promise. That keeps "the current pass" one
    // awaitable unit, so a direct handoff queued mid-pass can wait for it
    // without also waiting for a follow-up that is itself ordered behind that
    // handoff - the wait graph that deadlocked when the follow-up was part of
    // the pass.
    const chain = run.then(
      () => settlePass(run),
      () => settlePass(run),
    );
    idle = chain;
    return chain;
  };
  const settlePass = (run: Promise<void>): Promise<void> | undefined => {
    // A follow-up started by an earlier settle owns the slot while it runs.
    if (pass === run) pass = null;
    return spendTrigger(false);
  };
  const retryPending = (reason: AnnotationRetryReason): Promise<void> => {
    if (!alive) return Promise.resolve();
    if (pass) {
      // This trigger reports state the running pass may already have visited:
      // retain it for one coalesced follow-up instead of dropping it.
      latchedTrigger ??= reason;
      return idle;
    }
    return startPass(reason);
  };
  return {
    save,
    retryPending,
    whenIdle: () => idle,
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
      // Wait for the pass that was current when this handoff was called, never
      // for a follow-up that pass may start: the follow-up is ordered behind
      // this handoff in the handoff chain, so waiting for both would close the
      // cycle that stranded a queued handoff behind a follow-up waiting on it.
      const prior = pass ? Promise.all([pass, handoffChain]) : handoffChain;
      const attempt = () =>
        alive
          ? repo.handoff(key, operationId, retryCancellation)
          : Promise.resolve<HandoffResult>({ state: "cancelled" });
      const run = prior.then(attempt, attempt);
      handoffChain = run.then(
        () => undefined,
        () => undefined,
      );
      return run.then(
        (result) => {
          const leftPending = result.state === "pending";
          if (leftPending) needRetry();
          outstandingHandoffs -= 1;
          settleWork(leftPending);
          return result;
        },
        (error) => {
          // A rejected handoff is an unobserved outcome the retry map cannot
          // show: spend a retained trigger on the pass that can record it.
          outstandingHandoffs -= 1;
          settleWork(true);
          throw error;
        },
      );
    },
    storageSucceeded: () => {
      if (!alive) return;
      // A storage commit during a running pass, an outstanding handoff or an
      // in-flight save names work whose outcome this notification cannot see
      // yet: retain one coalesced trigger and let the last unit to settle
      // spend it. The pass is included here on purpose - a commit during a
      // pass may have made a handoff the sweep already skipped claimable, and
      // the pass's own settlement is the spend point.
      if (outstandingWork() > 0) {
        latchedTrigger ??= "storage-recovered";
        return;
      }
      // Nothing is outstanding and a need is already recorded: start the pass
      // now. A trigger with neither names no work and is dropped.
      if (retryNeeded) void startPass("storage-recovered");
    },
    registerRetryNeed: () => {
      needRetry();
    },
    invalidate: (key, retired) => {
      if (retired === null) return;
      const documentKey = annotationDocumentKey(key);
      for (const [operationKey, operation] of failed)
        if (
          annotationDocumentKey(operation.lease.key) === documentKey &&
          operation.lease.namespaceIncarnation === retired.namespaceIncarnation &&
          operation.lease.epoch === retired.epoch
        )
          failed.delete(operationKey);
    },
    retireAttempted: (operation) => {
      failed.delete(id(operation));
      // Reach the operation's persistence whatever its state: abort an active
      // save transaction before commit, then fence and durably flip the row.
      // The cancellation handle is released at save settlement, so a
      // settled-failed save can only be dismissed through the repository.
      cancellations.get(id(operation))?.cancel("dismissal");
      void repo.dismissOperation(operation.lease.key, operation.operationId);
    },
    dispose: () => {
      alive = false;
      failed.clear();
      // Teardown aborts in-flight transactions; it is not a dismissal and
      // never fences or flips a committed row.
      for (const entry of cancellations.values()) entry.cancel("teardown");
      cancellations.clear();
      retryNeeded = false;
      latchedTrigger = null;
    },
  };
}
