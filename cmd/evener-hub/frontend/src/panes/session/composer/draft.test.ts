import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import {
  clearDraft,
  composerDraftStorageKey,
  draftStorageKey,
  readComposerDraft,
  readDraft,
  readDraftRevision,
  writeComposerDraft,
  writeDraft,
} from "./draft";

// See shell/DockHost.test.tsx / shell/rail/Rail.test.tsx's identical
// comment: Node 26 shadows jsdom's real window.localStorage with its own
// (non-functional under vitest) global, so every test file that touches
// localStorage needs this same small in-memory stand-in. Scoped to this
// file only.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

test("readDraft returns empty string when nothing is stored for this ref", () => {
  expect(readDraft("local:01AAA")).toBe("");
});

test("writeDraft then readDraft round-trips the same ref's text", () => {
  writeDraft("local:01AAA", "hello world");
  expect(readDraft("local:01AAA")).toBe("hello world");
});

test("writeDraft with blank content stores nothing (readDraft still empty)", () => {
  writeDraft("local:01AAA", "");
  expect(readDraft("local:01AAA")).toBe("");
  expect(localStorage.getItem(draftStorageKey("local:01AAA"))).toBeNull();
});

test("writeDraft with whitespace-only content stores nothing", () => {
  writeDraft("local:01AAA", "   \n\t  ");
  expect(readDraft("local:01AAA")).toBe("");
  expect(localStorage.getItem(draftStorageKey("local:01AAA"))).toBeNull();
});

test("writing an actual draft after a blank one removes the stale empty state and stores the new text", () => {
  writeDraft("local:01AAA", "first draft");
  writeDraft("local:01AAA", "");
  expect(readDraft("local:01AAA")).toBe("");
});

test("each ref's draft is isolated: writing one ref never touches another's", () => {
  writeDraft("local:01AAA", "draft for A");
  writeDraft("local:01BBB", "draft for B");
  expect(readDraft("local:01AAA")).toBe("draft for A");
  expect(readDraft("local:01BBB")).toBe("draft for B");
});

test("clearDraft removes only the given ref's stored draft", () => {
  writeDraft("local:01AAA", "draft for A");
  writeDraft("local:01BBB", "draft for B");
  clearDraft("local:01AAA");
  expect(readDraft("local:01AAA")).toBe("");
  expect(readDraft("local:01BBB")).toBe("draft for B");
});

test("readDraft degrades to empty string when localStorage throws (private mode / disabled storage)", () => {
  vi.spyOn(localStorage, "getItem").mockImplementation(() => {
    throw new Error("storage disabled");
  });
  expect(readDraft("local:01AAA")).toBe("");
});

test("writeDraft never throws when localStorage throws (quota exceeded etc.)", () => {
  vi.spyOn(localStorage, "setItem").mockImplementation(() => {
    throw new Error("quota exceeded");
  });
  expect(() => writeDraft("local:01AAA", "some text")).not.toThrow();
});

test("clearDraft never throws when localStorage throws", () => {
  vi.spyOn(localStorage, "removeItem").mockImplementation(() => {
    throw new Error("storage disabled");
  });
  expect(() => clearDraft("local:01AAA")).not.toThrow();
});

test("draftStorageKey namespaces by ref under the app's evener.* convention", () => {
  expect(draftStorageKey("local:01AAA")).toBe("evener.composer.draft.v1.local:01AAA");
});

// --- structured composer drafts (v2: {text, skillNames}) -------------------

test("readComposerDraft returns an empty draft when nothing is stored for this ref", () => {
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "", skillNames: [] });
});

test("composerDraftStorageKey namespaces the structured draft under the v2 key convention", () => {
  expect(composerDraftStorageKey("local:01AAA")).toBe("evener.composer.draft.v2.local:01AAA");
});

test("writeComposerDraft round-trips text and canonical skill selections", () => {
  writeComposerDraft("local:01AAA", { text: "hello", skillNames: ["pkg:probe"] });
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "hello", skillNames: ["pkg:probe"] });
});

test("writing a structured draft stores one atomic v2 record and removes any old v1 value", () => {
  localStorage.setItem(draftStorageKey("local:01AAA"), "legacy text");
  writeComposerDraft("local:01AAA", { text: "new text", skillNames: ["pkg:probe"] });
  expect(localStorage.getItem(composerDraftStorageKey("local:01AAA"))).toBe(
    JSON.stringify({ text: "new text", skillNames: ["pkg:probe"] }),
  );
  expect(localStorage.getItem(draftStorageKey("local:01AAA"))).toBeNull();
});

test("an existing plain-text draft reads back as literal text with an empty selection list", () => {
  localStorage.setItem(draftStorageKey("local:01AAA"), "plain words");
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "plain words", skillNames: [] });
});

test("an existing plain-text draft is never JSON-decoded to guess selections", () => {
  localStorage.setItem(draftStorageKey("local:01AAA"), '{"text":"hijacked","skillNames":["evil:skill"]}');
  expect(readComposerDraft("local:01AAA")).toEqual({
    text: '{"text":"hijacked","skillNames":["evil:skill"]}',
    skillNames: [],
  });
});

test("an existing plain-text slash mention never becomes an inferred selection", () => {
  localStorage.setItem(draftStorageKey("local:01AAA"), "/pkg:probe do the thing");
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "/pkg:probe do the thing", skillNames: [] });
});

test("text edits retain the draft's skill selections", () => {
  writeComposerDraft("local:01AAA", { text: "first", skillNames: ["pkg:probe"] });
  writeDraft("local:01AAA", "second");
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "second", skillNames: ["pkg:probe"] });
});

test("removing a selection persists the same text without inserting anything", () => {
  writeComposerDraft("local:01AAA", { text: "hello [image 1]", skillNames: ["pkg:probe", "pkg:other"] });
  writeComposerDraft("local:01AAA", { text: "hello [image 1]", skillNames: ["pkg:probe"] });
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "hello [image 1]", skillNames: ["pkg:probe"] });
});

test("a skill-only draft persists even though its text is blank", () => {
  writeComposerDraft("local:01AAA", { text: "", skillNames: ["pkg:probe"] });
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "", skillNames: ["pkg:probe"] });
  expect(localStorage.getItem(composerDraftStorageKey("local:01AAA"))).not.toBeNull();
});

test("blank text with no selections still stores nothing", () => {
  localStorage.setItem(draftStorageKey("local:01AAA"), "legacy text");
  writeComposerDraft("local:01AAA", { text: "", skillNames: [] });
  expect(localStorage.getItem(composerDraftStorageKey("local:01AAA"))).toBeNull();
  expect(localStorage.getItem(draftStorageKey("local:01AAA"))).toBeNull();
});

test("clearDraft removes both the v2 record and any legacy v1 value", () => {
  writeComposerDraft("local:01AAA", { text: "draft", skillNames: ["pkg:probe"] });
  clearDraft("local:01AAA");
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "", skillNames: [] });
  expect(localStorage.getItem(composerDraftStorageKey("local:01AAA"))).toBeNull();
});

test("the draft revision increments on chip changes even when the text is identical", () => {
  const ref = "local:01AAA";
  const before = readDraftRevision(ref);
  writeComposerDraft(ref, { text: "same", skillNames: [] });
  const afterWrite = readDraftRevision(ref);
  expect(afterWrite).toBeGreaterThan(before);
  writeComposerDraft(ref, { text: "same", skillNames: ["pkg:probe"] });
  const afterAdd = readDraftRevision(ref);
  expect(afterAdd).toBeGreaterThan(afterWrite);
  writeComposerDraft(ref, { text: "same", skillNames: [] });
  expect(readDraftRevision(ref)).toBeGreaterThan(afterAdd);
});

test("readComposerDraft degrades to an empty draft when localStorage throws", () => {
  vi.spyOn(localStorage, "getItem").mockImplementation(() => {
    throw new Error("storage disabled");
  });
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "", skillNames: [] });
});

test("writeComposerDraft never throws when localStorage throws", () => {
  vi.spyOn(localStorage, "setItem").mockImplementation(() => {
    throw new Error("quota exceeded");
  });
  expect(() => writeComposerDraft("local:01AAA", { text: "some text", skillNames: ["pkg:probe"] })).not.toThrow();
});
