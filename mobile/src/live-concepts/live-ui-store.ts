// Persistence and in-memory UI state for the live concept switcher.
//
// Only the concept ID is persisted. Production composer/question/disclosure
// state remains shared, while frame-owned presentation memory is scoped by the
// exact nested {concept, threadKey} tuple. resetProfileScope() clears every
// profile/thread-bound value but preserves the persisted concept selection.

import { create } from "zustand";
import type {
  ConversationUiMemory,
  LiveConceptUiState,
  QuestionDraft,
} from "./contract";
import type { ConversationAnchor } from "./conversation/contract";
import type { ConceptId } from "./model";

export interface ConceptStorage {
  read(): string | null;
  write(conceptId: string): void;
  remove(): void;
}

export interface LiveConceptUiStore extends LiveConceptUiState {
  readonly conversationUi: ReadonlyMap<
    ConceptId,
    ReadonlyMap<string, ConversationUiMemory>
  >;
  setConcept(concept: ConceptId): void;
  setWorkOpen(open: boolean): void;
  setComposerMode(mode: "send" | "steer" | "queue"): void;
  toggleTool(key: string): void;
  toggleWork(key: string): void;
  setQuestionDraft(key: string, draft: QuestionDraft): void;
  setConversationAnchor(
    concept: ConceptId,
    threadKey: string,
    anchor: ConversationAnchor,
  ): void;
  setConversationUnseen(
    concept: ConceptId,
    threadKey: string,
    count: number,
  ): void;
  setEvidenceState(
    concept: ConceptId,
    threadKey: string,
    evidenceKey: string | null,
    triggerKey: string | null,
  ): void;
  setConversationFocus(
    concept: ConceptId,
    threadKey: string,
    itemKey: string | null,
  ): void;
  toggleConversationEvidence(
    concept: ConceptId,
    threadKey: string,
    evidenceKey: string,
  ): void;
  resetProfileScope(): void;
}

const VALID_CONCEPTS: ReadonlySet<string> = new Set([
  "stillwater",
  "constellation",
  "field-notes",
]);

function loadConcept(storage: ConceptStorage): ConceptId {
  const raw = storage.read();
  if (raw !== null && VALID_CONCEPTS.has(raw)) {
    return raw as ConceptId;
  }
  return "stillwater";
}

function immutableSet<Value>(values: Iterable<Value> = []): ReadonlySet<Value> {
  const snapshot = new Set(values);
  Object.defineProperties(snapshot, {
    add: {
      value: () => {
        throw new TypeError("Cannot mutate an immutable set");
      },
    },
    delete: {
      value: () => {
        throw new TypeError("Cannot mutate an immutable set");
      },
    },
    clear: {
      value: () => {
        throw new TypeError("Cannot mutate an immutable set");
      },
    },
  });
  return Object.freeze(snapshot);
}

function immutableMap<Key, Value>(
  entries: Iterable<readonly [Key, Value]> = [],
): ReadonlyMap<Key, Value> {
  const snapshot = new Map(entries);
  Object.defineProperties(snapshot, {
    set: {
      value: () => {
        throw new TypeError("Cannot mutate an immutable map");
      },
    },
    delete: {
      value: () => {
        throw new TypeError("Cannot mutate an immutable map");
      },
    },
    clear: {
      value: () => {
        throw new TypeError("Cannot mutate an immutable map");
      },
    },
  });
  return Object.freeze(snapshot);
}

function emptyConversationMemory(): ConversationUiMemory {
  return Object.freeze({
    anchor: null,
    unseen: 0,
    evidenceKey: null,
    evidenceTriggerKey: null,
    focusedItemKey: null,
    expandedEvidenceKeys: immutableSet<string>(),
  });
}

function updateConversationMemory(
  state: Pick<LiveConceptUiStore, "conversationUi">,
  concept: ConceptId,
  threadKey: string,
  update: (memory: ConversationUiMemory) => ConversationUiMemory,
): Pick<LiveConceptUiStore, "conversationUi"> {
  const currentConcept = state.conversationUi.get(concept);
  const currentMemory =
    currentConcept?.get(threadKey) ?? emptyConversationMemory();
  const nextMemory = Object.freeze(update(currentMemory));
  const nextConcept = new Map(currentConcept ?? []);
  nextConcept.set(threadKey, nextMemory);
  const next = new Map(state.conversationUi);
  next.set(concept, immutableMap(nextConcept));
  return { conversationUi: immutableMap(next) };
}

function normalizeUnseen(count: number): number {
  if (!Number.isFinite(count) || count <= 0) return 0;
  return Math.floor(count);
}

export function createLiveConceptUiStore(storage: ConceptStorage) {
  return create<LiveConceptUiStore>((set) => ({
    concept: loadConcept(storage),
    workOpen: false,
    composerMode: "send",
    expandedToolKeys: new Set<string>(),
    expandedWorkKeys: new Set<string>(),
    questionDrafts: {},
    conversationUi: immutableMap(),
    focusedItemKey: null,
    scrollAnchors: {},

    setConcept: (concept) => {
      storage.write(concept);
      set({ concept });
    },
    setWorkOpen: (workOpen) => set({ workOpen }),
    setComposerMode: (composerMode) => set({ composerMode }),
    toggleTool: (key) =>
      set((state) => {
        const next = new Set(state.expandedToolKeys);
        if (next.has(key)) {
          next.delete(key);
        } else {
          next.add(key);
        }
        return { expandedToolKeys: next };
      }),
    toggleWork: (key) =>
      set((state) => {
        const next = new Set(state.expandedWorkKeys);
        if (next.has(key)) {
          next.delete(key);
        } else {
          next.add(key);
        }
        return { expandedWorkKeys: next };
      }),
    setQuestionDraft: (key, draft) =>
      set((state) => ({
        questionDrafts: { ...state.questionDrafts, [key]: draft },
      })),
    setConversationAnchor: (concept, threadKey, anchor) =>
      set((state) =>
        updateConversationMemory(state, concept, threadKey, (memory) => ({
          ...memory,
          anchor: Object.freeze({ ...anchor }),
        })),
      ),
    setConversationUnseen: (concept, threadKey, count) =>
      set((state) =>
        updateConversationMemory(state, concept, threadKey, (memory) => ({
          ...memory,
          unseen: normalizeUnseen(count),
        })),
      ),
    setEvidenceState: (concept, threadKey, evidenceKey, evidenceTriggerKey) =>
      set((state) =>
        updateConversationMemory(state, concept, threadKey, (memory) => ({
          ...memory,
          evidenceKey,
          evidenceTriggerKey: evidenceKey === null ? null : evidenceTriggerKey,
        })),
      ),
    setConversationFocus: (concept, threadKey, focusedItemKey) =>
      set((state) =>
        updateConversationMemory(state, concept, threadKey, (memory) => ({
          ...memory,
          focusedItemKey,
        })),
      ),
    toggleConversationEvidence: (concept, threadKey, evidenceKey) =>
      set((state) =>
        updateConversationMemory(state, concept, threadKey, (memory) => {
          const expandedEvidenceKeys = new Set(memory.expandedEvidenceKeys);
          if (expandedEvidenceKeys.has(evidenceKey)) {
            expandedEvidenceKeys.delete(evidenceKey);
          } else {
            expandedEvidenceKeys.add(evidenceKey);
          }
          return {
            ...memory,
            expandedEvidenceKeys: immutableSet(expandedEvidenceKeys),
          };
        }),
      ),
    resetProfileScope: () =>
      set({
        workOpen: false,
        composerMode: "send",
        expandedToolKeys: new Set<string>(),
        expandedWorkKeys: new Set<string>(),
        questionDrafts: {},
        conversationUi: immutableMap(),
        focusedItemKey: null,
        scrollAnchors: {},
      }),
  }));
}
