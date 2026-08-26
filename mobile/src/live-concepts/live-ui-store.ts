// Persistence and in-memory UI state for the live concept switcher.
//
// Only the concept ID is persisted to storage. All other UI state (drafts,
// disclosures, question state, scroll anchors) lives in memory for the
// lifetime of the store. resetProfileScope() clears thread-keyed UI when the
// active profile changes, but preserves the persisted concept.

import { create } from "zustand";
import type {
  LiveConceptUiState,
  QuestionDraft,
  ScrollAnchor,
} from "./contract";
import type { ConceptId } from "./model";

export interface ConceptStorage {
  read(): string | null;
  write(conceptId: string): void;
  remove(): void;
}

export interface LiveConceptUiStore extends LiveConceptUiState {
  setConcept(concept: ConceptId): void;
  setWorkOpen(open: boolean): void;
  setComposerMode(mode: "send" | "steer" | "queue"): void;
  toggleTool(key: string): void;
  toggleWork(key: string): void;
  setQuestionDraft(key: string, draft: QuestionDraft): void;
  setFocusedItemKey(key: string | null): void;
  setScrollAnchor(anchorKey: string, anchor: ScrollAnchor): void;
  clearScrollAnchor(anchorKey: string): void;
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

export function createLiveConceptUiStore(storage: ConceptStorage) {
  return create<LiveConceptUiStore>((set) => ({
    concept: loadConcept(storage),
    workOpen: false,
    composerMode: "send",
    expandedToolKeys: new Set<string>(),
    expandedWorkKeys: new Set<string>(),
    questionDrafts: {},
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
    setFocusedItemKey: (focusedItemKey) => set({ focusedItemKey }),
    setScrollAnchor: (anchorKey, anchor) =>
      set((state) => ({
        scrollAnchors: { ...state.scrollAnchors, [anchorKey]: anchor },
      })),
    clearScrollAnchor: (anchorKey) =>
      set((state) => {
        const next = { ...state.scrollAnchors };
        delete next[anchorKey];
        return { scrollAnchors: next };
      }),
    resetProfileScope: () =>
      set({
        workOpen: false,
        composerMode: "send",
        expandedToolKeys: new Set<string>(),
        expandedWorkKeys: new Set<string>(),
        questionDrafts: {},
        focusedItemKey: null,
        scrollAnchors: {},
      }),
  }));
}
