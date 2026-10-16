import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import * as draftAPI from "./draft";
import {
  type ComposerDraft,
  clearRuntimeDraft,
  composerDraftStorageKey,
  decodeSupportedComposerDraft,
  draftStorageKey,
  markDraftEdited,
  publishRuntimeDraft,
  readComposerDraft,
  readDraft,
  readDraftRevision,
} from "./draft";
import { parseSkillDocument, patchSelectionText, serializeSkillDocument } from "./skillDocument";

const owner = "draft-projection-test";
const projectedRefs = new Set<string>();
function project(ref: string, value: ComposerDraft) {
  projectedRefs.add(ref);
  publishRuntimeDraft(ref, value, owner);
}
beforeEach(() => installLocalStorage(new MemoryStorage()));
afterEach(() => {
  for (const ref of projectedRefs) clearRuntimeDraft(ref, owner);
  projectedRefs.clear();
  vi.restoreAllMocks();
});

// These remain supported old-build inputs, not a browser durable writer API.
test("legacy key namespaces remain available for migration", () => {
  expect(draftStorageKey("local:01AAA")).toBe("evener.composer.draft.v1.local:01AAA");
  expect(composerDraftStorageKey("local:01AAA")).toBe("evener.composer.draft.v2.local:01AAA");
});
test("absent supported bytes return an empty structured and text draft", () => {
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "", skillNames: [] });
  expect(readDraft("local:01AAA")).toBe("");
});
test.each(["plain words", '{"text":"hijacked","skillNames":["evil:skill"]}', "/pkg:probe do the thing"])(
  "v1 remains literal text, including %s",
  (text) => {
    localStorage.setItem(draftStorageKey("local:01AAA"), text);
    expect(readComposerDraft("local:01AAA")).toEqual({ text, skillNames: [] });
    expect(readDraft("local:01AAA")).toBe(text);
  },
);
test("decodeSupportedComposerDraft preserves a valid structured draft including explicit empty mentions", () => {
  expect(
    decodeSupportedComposerDraft(
      JSON.stringify({
        text: "hello /same",
        skillNames: [" same ", "same"],
        commandNames: ["command"],
        mentions: [],
      }),
      "ignored legacy",
    ),
  ).toEqual({
    text: "hello /same",
    skillNames: ["same"],
    commandNames: ["command"],
    mentions: [],
  });
});
test.each([
  ["invalid JSON", "not-json", '{"text":"literal /same"}'],
  ["invalid structured shape", '{"text":4,"skillNames":[]}', "/same remains prose"],
  ["absent structured bytes", null, '{"text":"JSON-looking v1"} /slash'],
] as const)("decodeSupportedComposerDraft uses literal v1 bytes for %s", (_name, structured, legacy) => {
  expect(decodeSupportedComposerDraft(structured, legacy)).toEqual({ text: legacy, skillNames: [] });
});
test("valid v2 short-circuits an unreadable v1 key", () => {
  const ref = "local:short-circuit";
  const structuredKey = composerDraftStorageKey(ref);
  const legacyKey = draftStorageKey(ref);
  const accesses: string[] = [];
  vi.spyOn(localStorage, "getItem").mockImplementation((key) => {
    accesses.push(key);
    if (key === structuredKey) return JSON.stringify({ text: "valid v2", skillNames: ["skill"], mentions: [] });
    if (key === legacyKey) throw new Error("legacy key is unreadable");
    return null;
  });
  expect(readComposerDraft(ref)).toEqual({ text: "valid v2", skillNames: ["skill"], mentions: [] });
  expect(accesses).toEqual([structuredKey]);
});
test("supported v2 canonicalizes padded, empty and repeated skill names", () => {
  localStorage.setItem(
    composerDraftStorageKey("local:01AAA"),
    JSON.stringify({
      text: "keep me",
      skillNames: [" pkg:probe ", "", "pkg:probe", "  ", "pkg:other", "pkg:other"],
    }),
  );
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "keep me", skillNames: ["pkg:probe", "pkg:other"] });
});
test("legacy reads degrade to empty when localStorage is disabled", () => {
  vi.spyOn(localStorage, "getItem").mockImplementation(() => {
    throw new Error("storage disabled");
  });
  expect(readComposerDraft("local:01AAA")).toEqual({ text: "", skillNames: [] });
  expect(readDraft("local:01AAA")).toBe("");
});

// The repository owns durability now. Publishing or removing a runtime read
// projection must never remove, rewrite or infer anything from legacy bytes.
test.each([
  { text: "hello", skillNames: ["pkg:probe"] },
  { text: "", skillNames: [] },
  { text: "   \n\t  ", skillNames: [] },
  { text: "", skillNames: ["pkg:probe"] },
  { text: "/same", skillNames: [], mentions: [] },
])("runtime projection preserves the exact full value $text without any storage access", (value) => {
  const ref = "local:01AAA";
  const v1 = "legacy text";
  const v2 = JSON.stringify({ text: "legacy v2", skillNames: [] });
  localStorage.setItem(draftStorageKey(ref), v1);
  localStorage.setItem(composerDraftStorageKey(ref), v2);
  const get = vi.spyOn(localStorage, "getItem");
  const put = vi.spyOn(localStorage, "setItem");
  const remove = vi.spyOn(localStorage, "removeItem");
  project(ref, value);
  expect(readComposerDraft(ref)).toEqual(value);
  expect(readDraft(ref)).toBe(value.text);
  expect(get).not.toHaveBeenCalled();
  expect(put).not.toHaveBeenCalled();
  expect(remove).not.toHaveBeenCalled();
  clearRuntimeDraft(ref, owner);
  expect(put).not.toHaveBeenCalled();
  expect(remove).not.toHaveBeenCalled();
  expect(localStorage.getItem(draftStorageKey(ref))).toBe(v1);
  expect(localStorage.getItem(composerDraftStorageKey(ref))).toBe(v2);
  expect(readComposerDraft(ref)).toEqual({ text: "legacy v2", skillNames: [] });
});
test("runtime publication and cleanup work even when every storage operation throws", () => {
  for (const method of ["getItem", "setItem", "removeItem"] as const) {
    vi.spyOn(localStorage, method).mockImplementation(() => {
      throw new Error("storage disabled");
    });
  }
  const value = { text: "kept runtime text", skillNames: ["pkg:probe"] };
  expect(() => project("local:01AAA", value)).not.toThrow();
  expect(readComposerDraft("local:01AAA")).toEqual(value);
  expect(() => clearRuntimeDraft("local:01AAA", owner)).not.toThrow();
  expect(localStorage.getItem).not.toHaveBeenCalled();
  expect(localStorage.setItem).not.toHaveBeenCalled();
  expect(localStorage.removeItem).not.toHaveBeenCalled();
});
test("refs are isolated and an obsolete publisher cannot remove a replacement projection", () => {
  project("local:01AAA", { text: "draft for A", skillNames: [] });
  project("local:01BBB", { text: "draft for B", skillNames: [] });
  publishRuntimeDraft("local:01AAA", { text: "replacement A", skillNames: [], mentions: [] }, "new-owner");
  try {
    clearRuntimeDraft("local:01AAA", owner);
    expect(readComposerDraft("local:01AAA")).toEqual({ text: "replacement A", skillNames: [], mentions: [] });
    expect(readDraft("local:01BBB")).toBe("draft for B");
  } finally {
    clearRuntimeDraft("local:01AAA", "new-owner");
  }
  expect(readDraft("local:01AAA")).toBe("");
  expect(readDraft("local:01BBB")).toBe("draft for B");
});
test("projected text or selection replacement preserves only its explicit metadata", () => {
  const ref = "local:01AAA";
  project(ref, { text: "first", skillNames: ["pkg:probe", "pkg:other"] });
  project(ref, { ...readComposerDraft(ref), text: "second" });
  expect(readComposerDraft(ref)).toEqual({ text: "second", skillNames: ["pkg:probe", "pkg:other"] });
  project(ref, { text: "second", skillNames: ["pkg:probe"] });
  expect(readComposerDraft(ref)).toEqual({ text: "second", skillNames: ["pkg:probe"] });
  project(ref, { text: "", skillNames: [] });
  expect(readDraft(ref)).toBe("");
});
test("runtime edit revisions advance explicitly on same-text selection changes, never on publication or disposal", () => {
  const ref = "local:01AAA";
  const before = readDraftRevision(ref);
  project(ref, { text: "same", skillNames: [] });
  expect(readDraftRevision(ref)).toBe(before);
  markDraftEdited(ref);
  expect(readDraftRevision(ref)).toBe(before + 1);
  project(ref, { text: "same", skillNames: ["pkg:probe"] });
  markDraftEdited(ref);
  expect(readDraftRevision(ref)).toBe(before + 2);
  project(ref, { text: "same", skillNames: [] });
  markDraftEdited(ref);
  clearRuntimeDraft(ref, owner);
  expect(readDraftRevision(ref)).toBe(before + 3);
});
test("draft.ts exposes no durable writer or clear capability", () => {
  for (const name of ["writeDraft", "writeComposerDraft", "clearDraft", "clearPersistedDraft"]) {
    expect(draftAPI).not.toHaveProperty(name);
  }
});

test("text edits shift explicit atom locations without selecting same-spelling prose", () => {
  const original: ComposerDraft = {
    text: "🙂 /same /same /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 3 },
      { kind: "skill", name: "same", offset: 9 },
    ],
  };
  const expected = {
    text: "prefix 🙂 /same /same /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 10 },
      { kind: "skill", name: "same", offset: 16 },
    ],
  };
  project("local:01AAA", patchSelectionText(original, expected.text));
  expect(readComposerDraft("local:01AAA")).toEqual(expected);
  expect(serializeSkillDocument(parseSkillDocument(readComposerDraft("local:01AAA")))).toEqual(expected);
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
  const original: ComposerDraft = {
    text: "🙂 /same then /same quote /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 3 },
      { kind: "skill", name: "same", offset: 14 },
    ],
  };
  project("local:01AAA", original);
  project("local:01BBB", original);
  project("local:01AAA", patchSelectionText(readComposerDraft("local:01AAA"), text));
  expect(readComposerDraft("local:01AAA")).toEqual(expected);
  expect(serializeSkillDocument(parseSkillDocument(readComposerDraft("local:01AAA")))).toEqual(expected);
  expect(readComposerDraft("local:01BBB")).toEqual(original);
  expect(localStorage.getItem(draftStorageKey("local:01AAA"))).toBeNull();
  expect(localStorage.getItem(composerDraftStorageKey("local:01AAA"))).toBeNull();
});
