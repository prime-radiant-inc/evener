import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { clearDraft, draftStorageKey, readDraft, writeDraft } from "./draft";

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

test("draftStorageKey namespaces by ref under the app's serf.* convention", () => {
  expect(draftStorageKey("local:01AAA")).toBe("serf.composer.draft.v1.local:01AAA");
});
