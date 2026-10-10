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
  invalidate(key: DocumentKey): void;
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
  let retryNeeded = false;
  let pendingDuringPass = false;
  let alive = true;
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
  const retryPending = (reason: AnnotationRetryReason): Promise<void> => {
    if (!alive) return Promise.resolve();
    if (pass) return pass;
    pendingDuringPass = false; // before assigning pass, never erase observations made while it runs
    pass = Promise.resolve()
      .then(async () => {
        for (const operation of [...failed.values()]) {
          if (!alive) return;
          // invalidate may have removed this attempt while an earlier save waited.
          if (failed.get(id(operation)) === operation) await save(operation);
        }
        if (alive)
          try {
            const outcome = await repo.retryPendingWithOutcome(reason);
            if (alive) retryNeeded = failed.size > 0 || outcome.pending || pendingDuringPass;
          } catch {
            needRetry();
          }
      })
      .finally(() => {
        pass = null;
      });
    return pass;
  };
  return {
    save,
    retryPending,
    whenIdle: () => pass ?? Promise.resolve(),
    handoff: async (key, operationId) => {
      const result = await repo.handoff(key, operationId);
      if (result.state === "pending") needRetry();
      return result;
    },
    storageSucceeded: () => {
      if (alive && retryNeeded && !pass) void retryPending("storage-recovered");
    },
    invalidate: (key) => {
      for (const [operationKey, operation] of failed)
        if (annotationDocumentKey(operation.lease.key) === annotationDocumentKey(key)) failed.delete(operationKey);
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
    },
  };
}
