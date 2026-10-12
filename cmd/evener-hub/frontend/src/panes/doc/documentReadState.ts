// Retained reader work for the browser document-annotation reader (Task 5).
//
// One viewer-id map retains each open document's editable comment, frozen
// capture, accepted lease, attempted immutable operation, phase and scroll
// position across render-host unsubscribe/remount. Key replacement and explicit
// committed disposal end a reader's subscriptions; the shared repository and
// runtime are never owned by an individual host. Every save runs through the
// real document-annotation runtime and repository; no timers or second recovery
// framework are introduced.
import type {
  AcceptedSourceLease,
  AnnotationCommentWork,
  AnnotationRetryReason,
  AnnotationView,
  DestinationProvenance,
  DocumentKey,
  PreparedAnnotation,
} from "@evener/appwire-client";
import { retainedComposerSourceFor } from "../../shell/paneLifetime";
import { getBrowserDraftRepository } from "../../stores/browserDraftRepository";
import { annotationDocumentKey, createDocumentAnnotationRepository } from "../../stores/documentAnnotationIndexedDB";
import {
  createDocumentAnnotationRuntime,
  type DocumentAnnotationPorts,
  type DocumentAnnotationRuntime,
} from "../../stores/documentAnnotationRuntime";
import { createSecureUUID } from "../../stores/secureUUID";

export interface DocumentReaderWork {
  key: DocumentKey;
  ports: DocumentAnnotationPorts;
  comment: AnnotationCommentWork;
  scrollTop: number;
  attempted: PreparedAnnotation | null;
  attemptedComment: AnnotationCommentWork | null;
  lease: AcceptedSourceLease | null;
  phase: "reading" | "saving" | "unsaved" | "expiry-pending";
}

// The one lazy browser singleton: the repository and runtime every production
// reader shares. `targetFor` resolves ONLY the supplied runtime origin within
// the current browser origin and session through the existing pane-lifetime
// authority; it never creates a lifetime or falls back to focus.
let browserPorts: DocumentAnnotationPorts | undefined;

export function getDocumentAnnotationPorts(): DocumentAnnotationPorts {
  if (browserPorts) return browserPorts;
  let runtime: DocumentAnnotationRuntime | undefined;
  const repository = createDocumentAnnotationRepository({
    drafts: getBrowserDraftRepository(),
    targetFor: (origin, key) =>
      origin.kind === "browser-origin" && key.namespace.kind === "browser" && key.namespace.origin === location.origin
        ? retainedComposerSourceFor(origin.paneId, origin.compositionOwnerId, key.sessionRef)
        : null,
    onStorageSuccess: () => runtime?.storageSucceeded(),
  });
  runtime = createDocumentAnnotationRuntime(repository);
  const ports: DocumentAnnotationPorts = { repository, runtime };
  browserPorts = ports;
  return ports;
}

type ReaderEntry = { work: DocumentReaderWork; listeners: Set<() => void>; unsubscribe: () => void };

// The ONE retained-work map, keyed by viewer id. Hosts subscribe and unsubscribe
// their render listeners; only committed viewer removal or key replacement
// disposes the work.
const readers = new Map<string, ReaderEntry>();

export function publishReader(work: DocumentReaderWork): void {
  for (const entry of readers.values()) if (entry.work === work) for (const listener of entry.listeners) listener();
}

// A late receipt can only retire the exact attempt it saw; if the work slot now
// holds a newer attempt, the old completion is ignored untouched.
function settleSavedAttempt(work: DocumentReaderWork, operation: PreparedAnnotation): void {
  if (work.attempted !== operation) return;
  const unchanged = work.comment === work.attemptedComment;
  work.attempted = null;
  work.attemptedComment = null;
  if (unchanged) work.comment = { note: "", captureFrozen: false };
  work.phase = unchanged ? "reading" : "unsaved";
  publishReader(work);
}

async function refreshSavedAttempt(work: DocumentReaderWork): Promise<void> {
  const operation = work.attempted;
  if (!operation) {
    publishReader(work);
    return;
  }
  let views: AnnotationView[];
  try {
    views = await work.ports.repository.listAnnotations(operation.lease.key);
  } catch {
    return; // storage still failed: retain the attempt, comment and honest status
  }
  if (views.some((view) => view.receipt.operationId === operation.operationId)) settleSavedAttempt(work, operation);
}

export function documentReaderWork(
  viewerId: string,
  key: DocumentKey,
  ports: DocumentAnnotationPorts = getDocumentAnnotationPorts(),
): DocumentReaderWork {
  const existing = readers.get(viewerId);
  if (existing && annotationDocumentKey(existing.work.key) === annotationDocumentKey(key)) return existing.work;
  if (existing) disposeDocumentReaderWork(viewerId);
  const work: DocumentReaderWork = {
    key: structuredClone(key),
    ports,
    scrollTop: 0,
    comment: { note: "", captureFrozen: false },
    attempted: null,
    attemptedComment: null,
    lease: null,
    phase: "reading",
  };
  const entry: ReaderEntry = { work, listeners: new Set(), unsubscribe: () => {} };
  readers.set(viewerId, entry);
  entry.unsubscribe = ports.repository.subscribe(key, () => {
    void refreshSavedAttempt(work);
  });
  if (readers.size === 1) document.addEventListener("visibilitychange", onReaderForeground);
  return work;
}

export function subscribeDocumentReaderWork(viewerId: string, listener: () => void): () => void {
  const entry = readers.get(viewerId);
  if (!entry) throw new Error("reader work not created");
  entry.listeners.add(listener);
  return () => {
    entry.listeners.delete(listener);
  };
}

export function disposeDocumentReaderWork(viewerId: string): void {
  const entry = readers.get(viewerId);
  if (!entry) return;
  entry.unsubscribe();
  readers.delete(viewerId);
  if (readers.size === 0) document.removeEventListener("visibilitychange", onReaderForeground);
}

// Genuine application teardown of the ONE lazy production singleton. It retires
// every retained reader first (their real repository unsubscribes and the shared
// foreground listener is removed), then disposes the runtime and closes the
// annotation repository's own connection/channel. The shared browser draft
// repository is a SEPARATE authority and is left untouched, so durable
// annotations and composer drafts keep loading after teardown. No ports are
// created here when none exist; a later getDocumentAnnotationPorts() supplies
// usable fresh ports. Ordinary document host remount and client replacement are
// NOT teardown and never call this.
export function disposeDocumentAnnotationPorts(): void {
  const ports = browserPorts;
  browserPorts = undefined;
  for (const viewerId of [...readers.keys()]) disposeDocumentReaderWork(viewerId);
  if (!ports) return;
  ports.runtime.dispose();
  ports.repository.close();
}

export async function retryDocumentReaders(reason: AnnotationRetryReason): Promise<void> {
  const runtimes = new Set([...readers.values()].map((entry) => entry.work.ports.runtime));
  await Promise.all([...runtimes].map((runtime) => runtime.retryPending(reason)));
  await Promise.all([...readers.values()].map((entry) => refreshSavedAttempt(entry.work)));
}

function onReaderForeground(): void {
  if (document.visibilityState === "visible") void retryDocumentReaders("foreground");
}

export async function saveReaderAnnotation(
  work: DocumentReaderWork,
  destination: DestinationProvenance,
): Promise<void> {
  if (!work.attempted) {
    // Capture the complete lease/capture/note/destination before any await so a
    // later caller mutation cannot change what is saved. Trim gates Send only;
    // the literal note bytes are preserved.
    const lease = work.lease;
    const capture = work.comment.capture;
    const note = work.comment.note;
    if (!lease || !capture || !note.trim()) return;
    work.attemptedComment = work.comment;
    work.attempted = structuredClone({
      operationId: createSecureUUID(),
      lease,
      capture,
      note,
      destination,
    });
  }
  const operation = work.attempted;
  if (!operation) return;
  const key = operation.lease.key; // captured BEFORE any await
  work.phase = "saving";
  publishReader(work);
  const receipt = await work.ports.runtime.save(operation);
  if (!receipt) {
    if (work.attempted === operation) {
      work.phase = "unsaved";
      publishReader(work);
    }
    return; // retain the exact attempt/comment; no handoff and no second Send
  }
  settleSavedAttempt(work, operation);
  await work.ports.runtime.handoff(key, operation.operationId);
  publishReader(work);
}
