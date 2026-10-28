import { expect, test } from "vitest";
import type { ItemModel } from "./model";
import {
  applyNotification,
  cachedSessionRecord,
  decodeTranscriptItem,
  encodeTranscriptItem,
  hydrateThread,
  threadModelFromCache,
} from "./reducer";
import { mergeTranscriptItemVersioned } from "./transcriptHistory";
import { mergeTurnHistory } from "./turnHistoryMerge";

const author = {
  author: {
    kind: "agent" as const,
    sessionRef: "east:child",
    ownerRef: "east:parent",
    delegateId: "dlg_child",
    delegateName: "Child",
  },
  inherited: true,
};
function hydrated(authorship: unknown = author) {
  return hydrateThread(
    JSON.parse(
      JSON.stringify({
        thread: {
          id: "child",
          sessionId: "child",
          preview: "",
          ephemeral: false,
          modelProvider: "test",
          createdAt: 1,
          updatedAt: 1,
          status: { type: "idle" },
          cwd: "/tmp",
          cliVersion: "test",
          source: "east",
          evener: { ref: "east:child", capabilities: {}, queue: { revision: 0 } },
          turns: [
            {
              id: "turn",
              status: "completed",
              items: [
                {
                  id: "item",
                  type: "agentMessage",
                  turnId: "turn",
                  transcriptKey: "key",
                  position: { entry: 1, item: 0 },
                  version: 1,
                  text: "answer",
                  authorship,
                },
              ],
            },
          ],
        },
        bootGeneration: "boot",
        epoch: 1,
        snapshot: { incarnation: "inc", length: 1 },
      }),
    ),
    "east:child",
    1,
  );
}
function first(model: ReturnType<typeof hydrated>): ItemModel {
  const item = model.turns[0]?.items[0];
  if (!item) throw new Error("missing fixture item");
  return item;
}

test("authorship survives real normalization, history update, overlay and cache codecs", () => {
  let model = hydrated();
  expect(first(model).authorship).toEqual(author);
  const restored = decodeTranscriptItem(JSON.parse(JSON.stringify(encodeTranscriptItem(first(model)))));
  expect(restored?.authorship?.author.sessionRef).toBe("east:child");
  expect(restored?.authorship).toEqual(author);
  const cached = cachedSessionRecord(model, 2);
  expect(cached?.history.turns[0]?.items[0]?.authorship).toEqual(author);
  if (!cached) throw new Error("missing cached session");
  expect(first(threadModelFromCache(JSON.parse(JSON.stringify(cached)), 3)).authorship).toEqual(author);
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "child",
        ref: "east:child",
        bootGeneration: "boot",
        epoch: 1,
        snapshot: { incarnation: "inc", length: 2 },
        items: [
          { id: "item", type: "agentMessage", turnId: "turn", transcriptKey: "key", version: 2, authorship: author },
        ],
      },
    },
    2,
  );
  expect(first(model).authorship).toEqual(author);
  expect(first(model).text).toBe("answer");
  model = applyNotification(
    model,
    {
      method: "overlay/upserted",
      params: {
        threadId: "child",
        ref: "east:child",
        item: {
          key: "stream:r/1:reasoning",
          kind: "stream",
          turnId: "turn",
          roundId: "r",
          streamId: "r/1",
          item: { id: "thought", type: "reasoning", text: "thought", authorship: author },
        },
      },
    },
    3,
  );
  const thought = model.turns.flatMap((turn) => turn.items).find((item) => item.text === "thought");
  expect(thought?.authorship).toEqual(author);
});

test("sparse merges retain attribution but authoritative unknown or absent replacements clear it", () => {
  const held = first(hydrated());
  const sparse: ItemModel = { ...first(hydrated(null)), authorship: undefined };
  const merged = mergeTurnHistory(
    [{ id: "turn", status: "completed", items: [held] }],
    [{ id: "turn", status: "completed", items: [sparse] }],
  );
  expect(merged.turns[0]?.items[0]?.authorship).toEqual(author);
  const unknown = first(hydrated({ author: { kind: "unknown" }, inherited: true }));
  expect(mergeTranscriptItemVersioned(held, 1, unknown, 2).authorship).toEqual({
    author: { kind: "unknown" },
    inherited: true,
  });
  expect(mergeTranscriptItemVersioned(held, 1, sparse, 2).authorship).toBeUndefined();
  expect(mergeTranscriptItemVersioned(held, 2, sparse, 1).authorship).toEqual(author);
});

test("old missing and invalid metadata never borrows thread identity", () => {
  expect(first(hydrated(null)).authorship).toBeUndefined();
  expect(first(hydrated({ author: { kind: "agent", sessionRef: "not a ref" }, inherited: true })).authorship).toEqual({
    author: { kind: "unknown" },
    inherited: true,
  });
  const cached = {
    ...encodeTranscriptItem(first(hydrated())),
    authorship: { author: { kind: "human", sessionRef: "east:child" }, inherited: false },
  };
  expect(decodeTranscriptItem(cached)?.authorship).toEqual({ author: { kind: "unknown" }, inherited: false });
});
