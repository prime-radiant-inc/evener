import { afterEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";

const cryptoWithRandomUUID = globalThis.crypto;

// The helper captures browserRandomSource at import time, so this test imports
// draft only after removing randomUUID from the browser crypto surface.
test("writeComposerDraft persists distinct revisions when randomUUID is unavailable", async () => {
  installLocalStorage(new MemoryStorage());
  vi.stubGlobal("crypto", {
    getRandomValues: cryptoWithRandomUUID.getRandomValues.bind(cryptoWithRandomUUID),
  });
  vi.resetModules();

  const { composerDraftStorageKey, readComposerDraft, writeComposerDraft } = await import("./draft");
  const first = writeComposerDraft("local:no-random-uuid", {
    text: "first /selected",
    skillNames: ["pkg:selected"],
  });
  const second = writeComposerDraft("local:no-random-uuid", {
    text: "second /selected",
    skillNames: ["pkg:selected"],
  });

  expect(first).toEqual(expect.any(String));
  expect(second).toEqual(expect.any(String));
  expect(second).not.toBe(first);
  expect(JSON.parse(localStorage.getItem(composerDraftStorageKey("local:no-random-uuid")) ?? "null")).toMatchObject({
    text: "second /selected",
    skillNames: ["pkg:selected"],
    revisionId: second,
  });
  expect(readComposerDraft("local:no-random-uuid")).toMatchObject({
    text: "second /selected",
    skillNames: ["pkg:selected"],
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
});
