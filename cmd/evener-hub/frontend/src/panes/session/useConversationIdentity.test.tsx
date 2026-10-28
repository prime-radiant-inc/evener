import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { readComposerDraft, readRecoverySelection, writeComposerDraft, writeRecoverySelection } from "./composer/draft";
import { createComposerSourceState } from "./composer/sourceState";
import { useConversationIdentity } from "./useConversationIdentity";

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  resetThreadsStoreForTests();
});
afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  vi.restoreAllMocks();
});

function publish(from: string, to: string) {
  act(() => threadsStore.setState({ resumedIdentities: new Map([[from, to]]) }));
}

test("carries structured draft before accepted replacement and acknowledges", () => {
  const value = {
    text: "/alpha /build source",
    skillNames: ["alpha"],
    commandNames: ["build"],
    mentions: [
      { kind: "skill" as const, name: "alpha", offset: 0 },
      { kind: "command" as const, name: "build", offset: 7 },
    ],
  };
  writeComposerDraft("A", value);
  const replace = vi.fn(() => {
    expect(readComposerDraft("resumed")).toEqual(value);
    return true;
  });
  renderHook(() => useConversationIdentity("A", { replace, hasOtherReaders: () => false }));
  publish("A", "resumed");
  expect(replace).toHaveBeenCalledWith("resumed");
  expect(threadsStore.getState().resumedIdentities.has("A")).toBe(false);
  expect(readComposerDraft("A")).toEqual(value);
});

test("rejected replacement retries and retained readers keep acknowledgement pending", () => {
  const replace = vi.fn(() => false);
  const { rerender } = renderHook(
    ({ other }) => useConversationIdentity("A", { replace, hasOtherReaders: () => other }),
    { initialProps: { other: true } },
  );
  publish("A", "resumed");
  expect(replace).toHaveBeenCalledTimes(1);
  expect(threadsStore.getState().resumedIdentities.get("A")).toBe("resumed");
  replace.mockReturnValue(true);
  rerender({ other: true });
  expect(replace).toHaveBeenCalledTimes(2);
  expect(threadsStore.getState().resumedIdentities.get("A")).toBe("resumed");
  rerender({ other: false });
  expect(threadsStore.getState().resumedIdentities.has("A")).toBe(false);
});

test("newer destination wins, old copy stays and staged images do not follow identity", () => {
  vi.spyOn(Date, "now").mockReturnValue(100);
  writeComposerDraft("A", { text: "older /alpha", skillNames: ["alpha"] });
  const source = createComposerSourceState("A", "old-owner");
  source.attachments.setState({
    items: [{ marker: 1, name: "staged.png", mediaType: "image/png", data: "AQID", pending: false }],
  });
  vi.mocked(Date.now).mockReturnValue(200);
  const newer = {
    text: "/build newer",
    skillNames: [],
    commandNames: ["build"],
    mentions: [{ kind: "command" as const, name: "build", offset: 0 }],
  };
  writeComposerDraft("resumed", newer);
  const replace = vi.fn(() => true);
  renderHook(() => useConversationIdentity("A", { replace, hasOtherReaders: () => false }));
  publish("A", "resumed");
  expect(replace).toHaveBeenCalledOnce();
  expect(readComposerDraft("resumed")).toEqual(newer);
  expect(readComposerDraft("A").text).toBe("older /alpha");
  source.dispose();
  const destination = createComposerSourceState("resumed", "new-owner");
  expect(destination.attachments.getState().items).toEqual([]);
  destination.dispose();
});

test("retired A cannot acknowledge or replace B when resume arrives late", () => {
  const replace = vi.fn(() => true);
  const view = renderHook(({ ref }) => useConversationIdentity(ref, { replace, hasOtherReaders: () => false }), {
    initialProps: { ref: "A" },
  });
  writeComposerDraft("B", { text: "B untouched", skillNames: [] });
  view.rerender({ ref: "B" });
  publish("A", "A-resumed");
  expect(replace).not.toHaveBeenCalled();
  expect(readComposerDraft("B").text).toBe("B untouched");
  expect(threadsStore.getState().resumedIdentities.get("A")).toBe("A-resumed");
});

test("a resume carries the conversation's stored pick to the new identity", () => {
  writeRecoverySelection("A", "picked-row");
  const replace = vi.fn(() => true);
  renderHook(() => useConversationIdentity("A", { replace, hasOtherReaders: () => false }));
  publish("A", "resumed");
  expect(readRecoverySelection("resumed")?.clientMutationId).toBe("picked-row");
  // The carry copies: the superseded identity keeps its pick for readers
  // that still hold it.
  expect(readRecoverySelection("A")?.clientMutationId).toBe("picked-row");
});
