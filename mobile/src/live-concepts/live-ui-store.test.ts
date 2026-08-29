// TDD tests for persisted concept selection and profile-scoped UI memory.

import { describe, expect, it } from "vitest";
import type { ConversationAnchor, QuestionDraft } from "./contract";
import { type ConceptStorage, createLiveConceptUiStore } from "./live-ui-store";
import type { ConceptId } from "./model";

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

const sampleAnchor: ConversationAnchor = {
  threadKey: "thread-a",
  itemKey: "item-a",
  offsetPx: 19,
  following: false,
};

function memoryFor(
  store: ReturnType<typeof createLiveConceptUiStore>,
  concept: ConceptId,
  threadKey: string,
) {
  return store.getState().conversationUi.get(concept)?.get(threadKey);
}

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

describe("live concept UI store — exact nested conversation memory", () => {
  it("stores stable anchors and frame state under concept then exact thread key", () => {
    const store = createLiveConceptUiStore(createMemoryStorage());

    store
      .getState()
      .setConversationAnchor(
        "stillwater",
        "thread:with:delimiter",
        sampleAnchor,
      );
    store
      .getState()
      .setConversationUnseen("stillwater", "thread:with:delimiter", 7);
    store
      .getState()
      .setEvidenceState(
        "stillwater",
        "thread:with:delimiter",
        "evidence-key",
        "trigger-key",
      );
    store
      .getState()
      .setConversationFocus(
        "stillwater",
        "thread:with:delimiter",
        "focused-key",
      );
    store.getState().setConversationAnchor("constellation", "thread", {
      ...sampleAnchor,
      threadKey: "thread",
      itemKey: "constellation-item",
      offsetPx: 31,
      following: true,
    });

    expect(memoryFor(store, "stillwater", "thread:with:delimiter")).toEqual({
      anchor: sampleAnchor,
      unseen: 7,
      evidenceKey: "evidence-key",
      evidenceTriggerKey: "trigger-key",
      focusedItemKey: "focused-key",
      expandedEvidenceKeys: new Set(),
    });
    expect(memoryFor(store, "constellation", "thread")?.anchor).toMatchObject({
      itemKey: "constellation-item",
      offsetPx: 31,
      following: true,
    });
    expect(store.getState().conversationUi).toBeInstanceOf(Map);
    expect(store.getState().conversationUi.get("stillwater")).toBeInstanceOf(
      Map,
    );
  });

  it("publishes immutable snapshots rather than mutating prior maps or memories", () => {
    const store = createLiveConceptUiStore(createMemoryStorage());
    store
      .getState()
      .setConversationAnchor("stillwater", "thread-a", sampleAnchor);
    const firstState = store.getState();
    const firstOuter = firstState.conversationUi;
    const firstInner = firstOuter.get("stillwater");
    const firstMemory = firstInner?.get("thread-a");

    store
      .getState()
      .setConversationUnseen("stillwater", "thread-a", Number.NaN);
    const secondState = store.getState();

    expect(secondState.conversationUi).not.toBe(firstOuter);
    expect(secondState.conversationUi.get("stillwater")).not.toBe(firstInner);
    expect(
      secondState.conversationUi.get("stillwater")?.get("thread-a"),
    ).not.toBe(firstMemory);
    expect(firstMemory?.unseen).toBe(0);
    expect(
      secondState.conversationUi.get("stillwater")?.get("thread-a")?.unseen,
    ).toBe(0);
    expect(() =>
      (secondState.conversationUi as Map<unknown, unknown>).set("x", new Map()),
    ).toThrow("Cannot mutate an immutable map");
    expect(() =>
      (
        secondState.conversationUi.get("stillwater") as Map<unknown, unknown>
      ).clear(),
    ).toThrow("Cannot mutate an immutable map");
    const expandedEvidenceKeys = secondState.conversationUi
      .get("stillwater")
      ?.get("thread-a")?.expandedEvidenceKeys;
    if (expandedEvidenceKeys === undefined) {
      throw new Error("missing immutable conversation memory");
    }
    expect(() => (expandedEvidenceKeys as Set<string>).add("escape")).toThrow(
      "Cannot mutate an immutable set",
    );
    const anchor = secondState.conversationUi
      .get("stillwater")
      ?.get("thread-a")?.anchor;
    if (anchor === null || anchor === undefined) {
      throw new Error("missing immutable conversation anchor");
    }
    expect(() => {
      (anchor as { offsetPx: number }).offsetPx = 99;
    }).toThrow();
  });

  it("keeps each concept anchor and uses no delimiter-composed keys", () => {
    const store = createLiveConceptUiStore(createMemoryStorage());
    const concepts: readonly ConceptId[] = [
      "stillwater",
      "constellation",
      "field-notes",
    ];
    concepts.forEach((concept, index) => {
      store.getState().setConversationAnchor(concept, "shared-thread", {
        ...sampleAnchor,
        itemKey: `${concept}-item`,
        offsetPx: index + 1,
      });
    });

    expect([...store.getState().conversationUi.keys()]).toEqual(concepts);
    for (const concept of concepts) {
      expect(memoryFor(store, concept, "shared-thread")?.anchor?.itemKey).toBe(
        `${concept}-item`,
      );
    }
  });

  it("round-trips non-empty evidence disclosures per concept and thread", () => {
    const store = createLiveConceptUiStore(createMemoryStorage("stillwater"));

    store
      .getState()
      .toggleConversationEvidence("stillwater", "shared-thread", "evidence-a");
    store
      .getState()
      .toggleConversationEvidence("stillwater", "shared-thread", "evidence-b");
    const stillwaterSnapshot = memoryFor(store, "stillwater", "shared-thread");
    expect(stillwaterSnapshot?.expandedEvidenceKeys).toEqual(
      new Set(["evidence-a", "evidence-b"]),
    );

    store.getState().setConcept("constellation");
    store
      .getState()
      .toggleConversationEvidence(
        "constellation",
        "shared-thread",
        "evidence-c",
      );
    expect(
      memoryFor(store, "constellation", "shared-thread")?.expandedEvidenceKeys,
    ).toEqual(new Set(["evidence-c"]));
    expect(memoryFor(store, "stillwater", "shared-thread")).toBe(
      stillwaterSnapshot,
    );

    store.getState().setConcept("stillwater");
    store
      .getState()
      .toggleConversationEvidence("stillwater", "shared-thread", "evidence-a");
    expect(
      memoryFor(store, "stillwater", "shared-thread")?.expandedEvidenceKeys,
    ).toEqual(new Set(["evidence-b"]));

    store.getState().resetProfileScope();
    expect(store.getState().concept).toBe("stillwater");
    expect(store.getState().conversationUi).toEqual(new Map());
  });
});

describe("live concept UI store — shared production UI", () => {
  it("switches concept without clearing composer, drafts, or disclosures", () => {
    const store = createLiveConceptUiStore(createMemoryStorage("stillwater"));

    store.getState().setQuestionDraft("q1", sampleDraft);
    store.getState().toggleTool("tool-1");
    store.getState().toggleWork("work-1");
    store.getState().setWorkOpen(true);
    store.getState().setComposerMode("steer");
    store
      .getState()
      .setConversationAnchor("stillwater", "thread-a", sampleAnchor);

    store.getState().setConcept("constellation");

    expect(store.getState().concept).toBe("constellation");
    expect(store.getState().questionDrafts).toEqual({ q1: sampleDraft });
    expect(store.getState().expandedToolKeys).toEqual(new Set(["tool-1"]));
    expect(store.getState().expandedWorkKeys).toEqual(new Set(["work-1"]));
    expect(store.getState().workOpen).toBe(true);
    expect(store.getState().composerMode).toBe("steer");
    expect(memoryFor(store, "stillwater", "thread-a")?.anchor).toEqual(
      sampleAnchor,
    );
    expect(memoryFor(store, "stillwater", "thread-a")?.anchor).not.toBe(
      sampleAnchor,
    );
  });
});

describe("live concept UI store — profile reset", () => {
  it("clears every thread/concept field while retaining shared concept selection", () => {
    const storage = createMemoryStorage("constellation");
    const store = createLiveConceptUiStore(storage);

    store.getState().setQuestionDraft("q1", sampleDraft);
    store.getState().toggleTool("tool-1");
    store.getState().toggleWork("work-1");
    store.getState().setWorkOpen(true);
    store.getState().setComposerMode("steer");
    store
      .getState()
      .setConversationAnchor("constellation", "thread-a", sampleAnchor);
    store.getState().setConversationUnseen("constellation", "thread-a", 4);
    store
      .getState()
      .setEvidenceState("constellation", "thread-a", "evidence", "trigger");
    store
      .getState()
      .setConversationFocus("constellation", "thread-a", "focused");

    store.getState().resetProfileScope();

    expect(store.getState().concept).toBe("constellation");
    expect(store.getState().questionDrafts).toEqual({});
    expect(store.getState().expandedToolKeys).toEqual(new Set());
    expect(store.getState().expandedWorkKeys).toEqual(new Set());
    expect(store.getState().workOpen).toBe(false);
    expect(store.getState().composerMode).toBe("send");
    expect(store.getState().conversationUi).toEqual(new Map());
  });

  it("does not write persisted concept during profile reset", () => {
    const { storage, writeCount } = createCountingStorage("field-notes");
    const store = createLiveConceptUiStore(storage);
    expect(writeCount()).toBe(0);

    store.getState().resetProfileScope();

    expect(writeCount()).toBe(0);
    expect(storage.read()).toBe("field-notes");
    expect(store.getState().concept).toBe("field-notes");
  });
});
