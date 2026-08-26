// TDD tests for the live concept UI store.
//
// Covers valid persisted concept, malformed value fallback to Stillwater,
// switch without clearing draft/disclosures/question state, profile reset
// clearing thread-keyed UI, and scroll anchors keyed by concept/surface/thread.

import { describe, expect, it } from "vitest";
import type { QuestionDraft, ScrollAnchor } from "./contract";
import { type ConceptStorage, createLiveConceptUiStore } from "./live-ui-store";

function createMemoryStorage(initial: string | null = null): ConceptStorage {
  let value = initial;
  return {
    read: () => value,
    write: (id: string) => {
      value = id;
    },
    remove: () => {
      value = null;
    },
  };
}

// Storage wrapper that counts write calls so tests can prove zero writes.
function createCountingStorage(initial: string | null = null) {
  let value = initial;
  let writes = 0;
  const storage: ConceptStorage = {
    read: () => value,
    write: (id: string) => {
      value = id;
      writes++;
    },
    remove: () => {
      value = null;
    },
  };
  return { storage, writeCount: () => writes };
}

const sampleDraft: QuestionDraft = {
  selectedOptionKeys: ["opt-a"],
  note: "draft note",
  resolution: "answer",
};

const sampleAnchor: ScrollAnchor = { itemKey: "item-1", offset: 120 };

describe("live concept UI store — persistence", () => {
  it("loads a valid persisted concept from storage", () => {
    const storage = createMemoryStorage("constellation");
    const store = createLiveConceptUiStore(storage);
    expect(store.getState().concept).toBe("constellation");
  });

  it("falls back to stillwater for a malformed persisted value", () => {
    const storage = createMemoryStorage("not-a-real-concept");
    const store = createLiveConceptUiStore(storage);
    expect(store.getState().concept).toBe("stillwater");
  });

  it("falls back to stillwater when storage is empty", () => {
    const storage = createMemoryStorage(null);
    const store = createLiveConceptUiStore(storage);
    expect(store.getState().concept).toBe("stillwater");
  });

  it("persists the concept to storage on switch", () => {
    const storage = createMemoryStorage("stillwater");
    const store = createLiveConceptUiStore(storage);
    store.getState().setConcept("field-notes");
    expect(storage.read()).toBe("field-notes");
    expect(store.getState().concept).toBe("field-notes");
  });
});

describe("live concept UI store — switch preserves in-memory state", () => {
  it("switches concept without clearing draft/disclosures/question state", () => {
    const storage = createMemoryStorage("stillwater");
    const store = createLiveConceptUiStore(storage);

    store.getState().setQuestionDraft("q1", sampleDraft);
    store.getState().toggleTool("tool-1");
    store.getState().toggleWork("work-1");
    store.getState().setFocusedItemKey("item-1");
    store.getState().setWorkOpen(true);
    store.getState().setComposerMode("steer");
    store.getState().setScrollAnchor("stillwater:sessions:t1", sampleAnchor);

    store.getState().setConcept("constellation");

    expect(store.getState().concept).toBe("constellation");
    expect(store.getState().questionDrafts).toEqual({ q1: sampleDraft });
    expect(store.getState().expandedToolKeys).toEqual(new Set(["tool-1"]));
    expect(store.getState().expandedWorkKeys).toEqual(new Set(["work-1"]));
    expect(store.getState().focusedItemKey).toBe("item-1");
    expect(store.getState().workOpen).toBe(true);
    expect(store.getState().composerMode).toBe("steer");
    expect(store.getState().scrollAnchors).toEqual({
      "stillwater:sessions:t1": sampleAnchor,
    });
  });
});

describe("live concept UI store — profile reset", () => {
  it("resetProfileScope clears thread-keyed UI but keeps concept", () => {
    const storage = createMemoryStorage("constellation");
    const store = createLiveConceptUiStore(storage);

    store.getState().setQuestionDraft("q1", sampleDraft);
    store.getState().toggleTool("tool-1");
    store.getState().toggleWork("work-1");
    store.getState().setFocusedItemKey("item-1");
    store.getState().setWorkOpen(true);
    store.getState().setComposerMode("steer");
    store
      .getState()
      .setScrollAnchor("constellation:conversation:t1", sampleAnchor);

    store.getState().resetProfileScope();

    expect(store.getState().concept).toBe("constellation");
    expect(store.getState().questionDrafts).toEqual({});
    expect(store.getState().expandedToolKeys).toEqual(new Set());
    expect(store.getState().expandedWorkKeys).toEqual(new Set());
    expect(store.getState().focusedItemKey).toBeNull();
    expect(store.getState().workOpen).toBe(false);
    expect(store.getState().composerMode).toBe("send");
    expect(store.getState().scrollAnchors).toEqual({});
  });

  it("resetProfileScope does not write to storage", () => {
    const { storage, writeCount } = createCountingStorage("field-notes");
    const store = createLiveConceptUiStore(storage);

    // One write from the initial load should not have occurred — load only
    // reads. Confirm baseline is zero.
    expect(writeCount()).toBe(0);

    store.getState().resetProfileScope();

    // Prove zero writes during reset and persisted concept unchanged.
    expect(writeCount()).toBe(0);
    expect(storage.read()).toBe("field-notes");
    expect(store.getState().concept).toBe("field-notes");
  });
});

describe("live concept UI store — scroll anchors", () => {
  it("stores scroll anchors keyed by concept/surface/thread", () => {
    const storage = createMemoryStorage(null);
    const store = createLiveConceptUiStore(storage);

    const anchor1: ScrollAnchor = { itemKey: "item-a", offset: 0 };
    const anchor2: ScrollAnchor = { itemKey: "item-b", offset: 50 };

    store.getState().setScrollAnchor("stillwater:sessions:thread-a", anchor1);
    store
      .getState()
      .setScrollAnchor("constellation:conversation:thread-b", anchor2);

    expect(store.getState().scrollAnchors).toEqual({
      "stillwater:sessions:thread-a": anchor1,
      "constellation:conversation:thread-b": anchor2,
    });
  });

  it("preserves scroll anchors across concept switches", () => {
    const storage = createMemoryStorage("stillwater");
    const store = createLiveConceptUiStore(storage);

    store
      .getState()
      .setScrollAnchor("stillwater:sessions:thread-a", sampleAnchor);

    store.getState().setConcept("constellation");

    expect(store.getState().scrollAnchors).toEqual({
      "stillwater:sessions:thread-a": sampleAnchor,
    });
  });

  it("clears an individual scroll anchor", () => {
    const storage = createMemoryStorage(null);
    const store = createLiveConceptUiStore(storage);

    store
      .getState()
      .setScrollAnchor("stillwater:sessions:thread-a", sampleAnchor);
    store.getState().clearScrollAnchor("stillwater:sessions:thread-a");

    expect(store.getState().scrollAnchors).toEqual({});
  });
});
