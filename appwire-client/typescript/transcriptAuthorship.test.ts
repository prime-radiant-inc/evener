import { expect, test } from "vitest";
import type { ItemModel } from "./model";
import { applyNotification } from "./reducer";
import {
  cachedSessionRecord,
  decodeTranscriptItem,
  encodeTranscriptItem,
  threadModelFromCache,
} from "./transcriptCache";
import { hydrateThread, mergeHistory, mergeTranscriptItemVersioned } from "./transcriptHistory";
import { itemIsIdentityOnly, itemTextPresence, markItemIdentityOnly, markItemTextOmitted } from "./transcriptItems";
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

test("an equal-version refresh fills missing ask evidence without replacing the held row", () => {
  // A client that cached a reply before resolvedAskCalls existed holds it at
  // some version. The daemon then serves that SAME version with the evidence,
  // because the evidence rides the observation rather than the item version.
  // Version precedence would keep the held row forever, so the field would
  // never arrive for anyone with a warm cache.
  const heldText = "the reply the browser already holds";
  const held: ItemModel = { id: "answer", turnId: "turn", type: "userMessage", text: heldText, version: 4 };
  const refreshed: ItemModel = {
    id: "answer",
    turnId: "turn",
    type: "userMessage",
    text: "a different body the refresh also carries",
    version: 4,
    resolvedAskCalls: [{ turnId: "turn-question", callId: "call-1" }],
    resolvedAskLegacyCount: 0,
  };
  const filled = mergeTranscriptItemVersioned(held, 4, refreshed, 4);
  expect(filled.resolvedAskCalls).toEqual([{ turnId: "turn-question", callId: "call-1" }]);
  expect(filled.resolvedAskLegacyCount).toBe(0);
  // The held text wins: filling evidence must not let a same-version refresh
  // replace the body a reader is already looking at.
  expect(filled.text).toBe(heldText);
  // Evidence the held row already has is not replaced by an equal-version
  // refresh, and a refresh carrying none leaves the held row untouched.
  const withEvidence: ItemModel = {
    ...held,
    resolvedAskCalls: [{ turnId: "turn-question", callId: "call-held" }],
  };
  expect(mergeTranscriptItemVersioned(withEvidence, 4, refreshed, 4).resolvedAskCalls).toEqual([
    { turnId: "turn-question", callId: "call-held" },
  ]);
  expect(mergeTranscriptItemVersioned(held, 4, refreshed, 4).text).toBe(heldText);
  // A held row whose body the read omitted carries that fact in a
  // non-enumerable marker. Filling ask evidence must preserve it: without the
  // marker the row reads as an authoritative empty settle, and the local
  // reconciliation that still owes this reply its body will refuse to supply
  // it.
  const omittedBody = markItemTextOmitted({ ...held, text: "" });
  const omittedFilled = mergeTranscriptItemVersioned(omittedBody, 4, refreshed, 4);
  expect(itemTextPresence(omittedFilled)).toBe("omitted");
  expect(omittedFilled.resolvedAskCalls).toEqual([{ turnId: "turn-question", callId: "call-1" }]);
  expect(
    mergeTranscriptItemVersioned(
      held,
      4,
      { ...refreshed, resolvedAskCalls: undefined, resolvedAskLegacyCount: undefined },
      4,
    ),
  ).toBe(held);
  // A live refresh reaches the transcript model through mergeHistory, not the
  // entry-cache merge, so the same rule has to hold there: the evidence
  // arrives, and the held body stands.
  const mergedTurns = mergeHistory([{ id: "turn", status: "completed", items: [held] }], {
    turns: [{ id: "turn", status: "completed", items: [] }],
    items: [refreshed],
  });
  expect(mergedTurns[0]?.items[0]?.resolvedAskCalls).toEqual([{ turnId: "turn-question", callId: "call-1" }]);
  expect(mergedTurns[0]?.items[0]?.text).toBe(heldText);
  // Background pruning can reduce a reply to an identity-only placeholder at
  // the version the daemon then re-serves in full. The refresh carries the
  // body, so it must replace the placeholder rather than fill evidence onto
  // it: an evidence-only fill would leave a reply with no text even though the
  // full text is right there.
  const placeholder = markItemIdentityOnly({ id: "answer", turnId: "turn", type: "userMessage", text: "", version: 4 });
  const upgradedTurns = mergeHistory([{ id: "turn", status: "completed", items: [placeholder] }], {
    turns: [{ id: "turn", status: "completed", items: [] }],
    items: [refreshed],
  });
  expect(upgradedTurns[0]?.items[0]?.text).toBe(refreshed.text);
  expect(upgradedTurns[0]?.items[0]?.resolvedAskCalls).toEqual([{ turnId: "turn-question", callId: "call-1" }]);
  // A placeholder that only gains evidence must stay a placeholder. The
  // enriching spread drops the non-enumerable identity-only marker, and
  // without it a later same-version refresh can no longer take the upgrade
  // path that would restore the body: the fill would destroy the very
  // evidence that fixes the row.
  // Both rows pruned: no upgrade applies, so the fill runs, and the marker
  // has to survive it.
  const evidenceOnly: ItemModel = { id: "answer", turnId: "turn", type: "userMessage", text: "", version: 5 };
  const enrichedPlaceholder = mergeTranscriptItemVersioned(
    markItemIdentityOnly(evidenceOnly),
    5,
    markItemIdentityOnly({ ...refreshed, version: 5, text: "" }),
    5,
  );
  expect(itemIsIdentityOnly(enrichedPlaceholder)).toBe(true);
  expect(enrichedPlaceholder.resolvedAskCalls).toEqual([{ turnId: "turn-question", callId: "call-1" }]);
});

test("a versionless held row still keeps the fields a sparse update omits", () => {
  // A live refresh can reissue an item the model already holds at no recorded
  // version. Version precedence gives the incoming row that slot, but an
  // omission-tolerant merge must still fill what the update did not carry: a
  // sparse tool update that drops output or images must not erase them, and a
  // text-omitting update must not blank the row.
  const held: ItemModel = {
    id: "tool",
    turnId: "turn",
    type: "toolCall",
    text: "the output the reader already holds",
    output: "kept output",
  };
  const sparse: ItemModel = {
    id: "tool",
    turnId: "turn",
    type: "toolCall",
    text: "",
    status: "completed",
  };
  markItemTextOmitted(sparse);
  const merged = mergeHistory([{ id: "turn", status: "completed", items: [held] }], {
    turns: [{ id: "turn", status: "completed", items: [] }],
    items: [sparse],
  });
  expect(merged[0]?.items[0]?.output).toBe(held.output);
  expect(merged[0]?.items[0]?.text).toBe(held.text);
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
