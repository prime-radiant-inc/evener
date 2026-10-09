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
    failed.set(id(operation), operation);
    try {
      const receipt = await repo.saveAnnotation(operation);
      if (failed.get(id(operation)) === operation) failed.delete(id(operation));
      return receipt;
    } catch {
      needRetry();
      return null;
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
    dispose: () => {
      alive = false;
      failed.clear();
      retryNeeded = false;
      pendingDuringPass = false;
    },
  };
}
