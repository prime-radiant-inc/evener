import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import {
  type ComposerDraft,
  carryComposerDraft,
  clearDraft,
  composerDraftStorageKey,
  draftStorageKey,
  readComposerDraft,
  readDraft,
  readDraftRevision,
  writeComposerDraft,
  writeDraft,
} from "./draft";
import { parseSkillDocument, serializeSkillDocument } from "./skillDocument";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
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

// R09: the carry must preserve whichever draft is genuinely newer. The stamps
// are ordered here the way the real mid-resume case orders them (the destination
// was written first); the source copy under the old ref remains either way.
test("carryComposerDraft lets the genuinely newer source draft win", () => {
  const now = vi.spyOn(Date, "now");
  now.mockReturnValue(1_000);
  writeComposerDraft("local:carry-to", { text: "older destination draft", skillNames: [] });
  now.mockReturnValue(2_000);
  writeComposerDraft("local:carry-from", { text: "newer in-flight draft", skillNames: [] });
  carryComposerDraft("local:carry-from", "local:carry-to");
  expect(readComposerDraft("local:carry-to").text).toBe("newer in-flight draft");
  expect(readComposerDraft("local:carry-from").text).toBe("newer in-flight draft");
});

test("carryComposerDraft preserves a newer destination draft", () => {
  const now = vi.spyOn(Date, "now");
  now.mockReturnValue(3_000);
  writeComposerDraft("local:carry-from", { text: "stale in-flight draft", skillNames: [] });
  now.mockReturnValue(4_000);
  writeComposerDraft("local:carry-to", { text: "newer destination draft", skillNames: [] });
  carryComposerDraft("local:carry-from", "local:carry-to");
  expect(readComposerDraft("local:carry-to").text).toBe("newer destination draft");
  expect(readComposerDraft("local:carry-from").text).toBe("stale in-flight draft");
});

test("carryComposerDraft keeps the destination on a tie or when neither record is stamped", () => {
  const now = vi.spyOn(Date, "now");
  now.mockReturnValue(5_000);
  writeComposerDraft("local:tie-from", { text: "source tie", skillNames: [] });
  writeComposerDraft("local:tie-to", { text: "destination tie", skillNames: [] });
  carryComposerDraft("local:tie-from", "local:tie-to");
  expect(readComposerDraft("local:tie-to").text).toBe("destination tie");
  // Two records an older build wrote, no stamp at all: the destination is kept.
  localStorage.setItem(
    composerDraftStorageKey("local:old-from"),
    JSON.stringify({ text: "old source", skillNames: [] }),
  );
  localStorage.setItem(
    composerDraftStorageKey("local:old-to"),
    JSON.stringify({ text: "old destination", skillNames: [] }),
  );
  carryComposerDraft("local:old-from", "local:old-to");
  expect(readComposerDraft("local:old-to").text).toBe("old destination");
});

test("carryComposerDraft fills a blank destination regardless of stamps", () => {
  writeComposerDraft("local:carry-blank-from", { text: "only draft", skillNames: [] });
  carryComposerDraft("local:carry-blank-from", "local:carry-blank-to");
  expect(readComposerDraft("local:carry-blank-to").text).toBe("only draft");
});

test("carryComposerDraft is a no-op for an empty source", () => {
  writeComposerDraft("local:carry-to", { text: "destination draft", skillNames: [] });
  carryComposerDraft("local:carry-empty", "local:carry-to");
  expect(readComposerDraft("local:carry-to").text).toBe("destination draft");
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
  const stored = localStorage.getItem(composerDraftStorageKey("local:01AAA"));
  expect(stored).not.toBeNull();
  // The one atomic record carries the write's wall-clock stamp beside its content.
  expect(JSON.parse(stored ?? "")).toEqual(
    expect.objectContaining({ text: "new text", skillNames: ["pkg:probe"], editedAt: expect.any(Number) }),
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

test("text edits shift explicit atom locations without selecting same-spelling prose", () => {
  const ref = "local:01AAA";
  writeComposerDraft(ref, {
    text: "🙂 /same /same /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 3 },
      { kind: "skill", name: "same", offset: 9 },
    ],
  });

  writeDraft(ref, "prefix 🙂 /same /same /same");

  const stored = readComposerDraft(ref);
  const expected = {
    text: "prefix 🙂 /same /same /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 10 },
      { kind: "skill", name: "same", offset: 16 },
    ],
  };
  expect(stored).toEqual(expected);
  expect(serializeSkillDocument(parseSkillDocument(stored))).toEqual(expected);
});

test.each<{ name: string; text: string; expected: ComposerDraft }>([
  {
    name: "removing an emoji prefix",
    text: "/same then /same quote /same",
    expected: {
      text: "/same then /same quote /same",
      skillNames: ["same"],
      commandNames: ["same"],
      mentions: [
        { kind: "command", name: "same", offset: 0 },
        { kind: "skill", name: "same", offset: 11 },
      ],
    },
  },
  {
    name: "inserting prose between atoms",
    text: "🙂 /same then extra /same quote /same",
    expected: {
      text: "🙂 /same then extra /same quote /same",
      skillNames: ["same"],
      commandNames: ["same"],
      mentions: [
        { kind: "command", name: "same", offset: 3 },
        { kind: "skill", name: "same", offset: 20 },
      ],
    },
  },
  {
    name: "removing the command label",
    text: "🙂 then /same quote /same",
    expected: {
      text: "🙂 then /same quote /same",
      skillNames: ["same"],
      mentions: [{ kind: "skill", name: "same", offset: 8 }],
    },
  },
  {
    name: "removing the skill label",
    text: "🙂 /same then quote /same",
    expected: {
      text: "🙂 /same then quote /same",
      skillNames: [],
      commandNames: ["same"],
      mentions: [{ kind: "command", name: "same", offset: 3 }],
    },
  },
  {
    name: "replacing the text with literal slash prose",
    text: "replacement /same words",
    expected: { text: "replacement /same words", skillNames: [] },
  },
  {
    name: "clearing the text",
    text: "",
    expected: { text: "", skillNames: [] },
  },
  {
    name: "rewriting identical text",
    text: "🙂 /same then /same quote /same",
    expected: {
      text: "🙂 /same then /same quote /same",
      skillNames: ["same"],
      commandNames: ["same"],
      mentions: [
        { kind: "command", name: "same", offset: 3 },
        { kind: "skill", name: "same", offset: 14 },
      ],
    },
  },
])("text edits preserve exact atom intent when $name", ({ text, expected }) => {
  const ref = "local:01AAA";
  const original: ComposerDraft = {
    text: "🙂 /same then /same quote /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 3 },
      { kind: "skill", name: "same", offset: 14 },
    ],
  };
  writeComposerDraft(ref, original);
  writeComposerDraft("local:01BBB", original);
  const revision = readDraftRevision(ref);

  writeDraft(ref, text);

  const stored = readComposerDraft(ref);
  expect(stored).toEqual(expected);
  expect(serializeSkillDocument(parseSkillDocument(stored))).toEqual(expected);
  expect(readDraftRevision(ref)).toBe(revision + 1);
  expect(readComposerDraft("local:01BBB")).toEqual(original);
  expect(localStorage.getItem(draftStorageKey(ref))).toBeNull();
  if (text === "") expect(localStorage.getItem(composerDraftStorageKey(ref))).toBeNull();
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

// The draft is the one selection source the composer reads verbatim, so a
// hand-edited or corrupted v2 record must not hand padded, empty or repeated
// names to the chip list: they render blank/duplicate chips with colliding
// React keys, and the wire contract forbids a non-canonical name.
test("readComposerDraft canonicalizes the stored skill names", () => {
  localStorage.setItem(
    composerDraftStorageKey("local:01AAA"),
    JSON.stringify({ text: "keep me", skillNames: [" pkg:probe ", "", "pkg:probe", "  ", "pkg:other", "pkg:other"] }),
  );
  expect(readComposerDraft("local:01AAA")).toEqual({
    text: "keep me",
    skillNames: ["pkg:probe", "pkg:other"],
  });
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
