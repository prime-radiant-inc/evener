import type {
  DestinationProvenance,
  DocFileContent,
  DocumentKey,
  ExpiryReceipt,
  PreparedAnnotation,
} from "@evener/appwire-client";

export const LOCAL_COMPOSITION_DATABASE_NAME = "evener-mutation-outbox";
export const LOCAL_COMPOSITION_DATABASE_VERSION = 4;

export const MUTATION_OUTBOX_STORE = "outbox";
export const MUTATION_OPTIMISTIC_STORE = "optimistic";
export const MUTATION_RECOVERY_STORE = "recovery";
export const MUTATION_SEQUENCES_STORE = "sequences";
export const MUTATION_TARGET_SEQUENCE_INDEX = "byTargetSequence";

export const COMPOSER_DRAFTS_STORE = "composerDrafts";
export const DRAFT_TRANSFERS_STORE = "draftTransfers";
export const DRAFT_CONFLICTS_STORE = "draftConflicts";
export const ANNOTATION_NAMESPACES_STORE = "annotationNamespaces";
export const ANNOTATION_HEADS_STORE = "annotationHeads";
export const ANNOTATION_EVIDENCE_STORE = "annotationEvidence";
export const ANNOTATION_PAYLOADS_STORE = "annotationPayloads";
export const ANNOTATION_OPERATIONS_STORE = "annotationOperations";
export const ANNOTATION_SEQUENCES_STORE = "annotationSequences";
export const ANNOTATION_EXPIRY_STORE = "annotationExpiry";

// Row DTOs for the browser annotation stores. Version 4/keyPaths are unchanged;
// only the shapes are named here so the adapter and the draft repository share
// one definition. JSON key encoders are pure and live here so both modules can
// import them without a cycle.
export interface AnnotationHeadRow {
  documentKey: string;
  namespaceIncarnation: string;
  epoch: string;
  headRevision: number;
  readGeneration: number;
  displayedSnapshotId: string;
  evidenceKey: string;
}

export interface AnnotationEvidenceRow {
  evidenceKey: string;
  documentKey: string;
  epoch: string;
  content: DocFileContent;
}

export interface AnnotationPayloadRow {
  payloadKey: string;
  documentKey: string;
  epoch: string;
  operation: PreparedAnnotation;
}

export interface AnnotationSequenceRow {
  documentKey: string;
  lastSequence: number;
}

export type AnnotationOperationRow = {
  operationKey: string;
  namespaceKey: string;
  namespaceIncarnation: string;
  documentKey: string;
  sessionKey: string;
  sessionSequence: number;
  operationId: string;
  epoch: string;
} & (
  | { state: "pending"; destination: DestinationProvenance; payloadKey: string }
  | { state: "applied"; draftRevision: number }
  | { state: "cancelled" }
);

export interface AnnotationExpiryRow {
  expiryKey: string;
  receipt: ExpiryReceipt;
}

export function annotationNamespaceKey(namespace: DocumentKey["namespace"]): string {
  return JSON.stringify([namespace.kind, namespace.kind === "browser" ? namespace.origin : namespace.hubId]);
}

export function annotationOperationKey(namespace: DocumentKey["namespace"], id: string): string {
  return JSON.stringify([namespace.kind, namespace.kind === "browser" ? namespace.origin : namespace.hubId, id]);
}

export function annotationDocumentKey(key: DocumentKey): string {
  return JSON.stringify([
    key.namespace.kind,
    key.namespace.kind === "browser" ? key.namespace.origin : key.namespace.hubId,
    key.sessionRef,
    key.path,
  ]);
}

export function annotationExpiryKey(documentKey: string, epoch: string): string {
  return JSON.stringify([documentKey, epoch]);
}

export function registerLocalCompositionSchema(database: IDBDatabase): void {
  if (!database.objectStoreNames.contains(MUTATION_OUTBOX_STORE)) {
    const outbox = database.createObjectStore(MUTATION_OUTBOX_STORE, { keyPath: "clientMutationId" });
    outbox.createIndex(MUTATION_TARGET_SEQUENCE_INDEX, ["targetRef", "intentSequence"], { unique: true });
  }
  if (!database.objectStoreNames.contains(MUTATION_OPTIMISTIC_STORE)) {
    const optimistic = database.createObjectStore(MUTATION_OPTIMISTIC_STORE, { keyPath: "clientMutationId" });
    optimistic.createIndex(MUTATION_TARGET_SEQUENCE_INDEX, ["targetRef", "intentSequence"], { unique: true });
  }
  if (!database.objectStoreNames.contains(MUTATION_RECOVERY_STORE)) {
    const recovery = database.createObjectStore(MUTATION_RECOVERY_STORE, { keyPath: "clientMutationId" });
    recovery.createIndex(MUTATION_TARGET_SEQUENCE_INDEX, ["targetRef", "intentSequence"]);
  }
  if (!database.objectStoreNames.contains(MUTATION_SEQUENCES_STORE)) {
    database.createObjectStore(MUTATION_SEQUENCES_STORE, { keyPath: "targetRef" });
  }
  if (!database.objectStoreNames.contains(COMPOSER_DRAFTS_STORE)) {
    database.createObjectStore(COMPOSER_DRAFTS_STORE, { keyPath: "serializedKey" });
  }
  if (!database.objectStoreNames.contains(DRAFT_TRANSFERS_STORE)) {
    database.createObjectStore(DRAFT_TRANSFERS_STORE, { keyPath: "serializedKey" });
  }
  if (!database.objectStoreNames.contains(DRAFT_CONFLICTS_STORE)) {
    const conflicts = database.createObjectStore(DRAFT_CONFLICTS_STORE, { keyPath: "conflictId" });
    conflicts.createIndex("byDraftKey", "draftKey");
  }
  if (!database.objectStoreNames.contains(ANNOTATION_NAMESPACES_STORE)) {
    database.createObjectStore(ANNOTATION_NAMESPACES_STORE, { keyPath: "namespaceKey" });
  }
  if (!database.objectStoreNames.contains(ANNOTATION_HEADS_STORE)) {
    database.createObjectStore(ANNOTATION_HEADS_STORE, { keyPath: "documentKey" });
  }
  if (!database.objectStoreNames.contains(ANNOTATION_EVIDENCE_STORE)) {
    database.createObjectStore(ANNOTATION_EVIDENCE_STORE, { keyPath: "evidenceKey" });
  }
  if (!database.objectStoreNames.contains(ANNOTATION_PAYLOADS_STORE)) {
    database.createObjectStore(ANNOTATION_PAYLOADS_STORE, { keyPath: "payloadKey" });
  }
  if (!database.objectStoreNames.contains(ANNOTATION_OPERATIONS_STORE)) {
    database.createObjectStore(ANNOTATION_OPERATIONS_STORE, { keyPath: "operationKey" });
  }
  if (!database.objectStoreNames.contains(ANNOTATION_SEQUENCES_STORE)) {
    database.createObjectStore(ANNOTATION_SEQUENCES_STORE, { keyPath: "documentKey" });
  }
  if (!database.objectStoreNames.contains(ANNOTATION_EXPIRY_STORE)) {
    database.createObjectStore(ANNOTATION_EXPIRY_STORE, { keyPath: "expiryKey" });
  }
}
