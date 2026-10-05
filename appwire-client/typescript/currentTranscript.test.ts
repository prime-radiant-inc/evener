import { describe, expect, it } from "vitest";
import {
  applyNotification,
  currentTranscriptState,
  decodeCurrentTranscriptState,
  hydrateThread,
  itemTextPresence,
  markItemTextOmitted,
  threadModelFromCurrentState,
} from "./reducer";
import { currentRead, textDelta } from "./testing/currentTranscript";
import { subagentCallItems } from "./testing/subagentWireFixtures";
import { toolWireItems } from "./testing/toolWireFixtures";
import type { EvenerDelegateInfo, ThreadItem } from "./types.gen";

function readWithItems(ref: string, items: ThreadItem[]) {
  const read = currentRead(ref);
  return {
    ...read,
    thread: {
      ...read.thread,
      turns: [{ id: "turn_1", status: "completed", itemsView: "full" as const, items }],
    },
  };
}

describe("current transcript checkpoints", () => {
  it("preserves a live stream so the restored reducer can continue its fold", () => {
    const original = hydrateThread(currentRead("local:one"), "local:one", 10);
    const encoded = currentTranscriptState(original);
    expect(encoded).toBeDefined();
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded, {
      capabilities: original.capabilities,
      capabilitySource: original.capabilitySource,
      instanceId: original.instanceId,
      resumeRequired: original.resumeRequired,
      resumeOnlyFoldable: original.resumeOnlyFoldable,
    });
    const next = textDelta("local:one", "-beta");

    expect(applyNotification(restored, next, 11).turns).toEqual(applyNotification(original, next, 11).turns);
    expect(restored.turns[0]?.status).toBe("inProgress");
    expect(restored.turns[0]?.items[0]?.text).toBe("alpha");
    expect(restored.olderCursor).toBe("older-40");
    expect(threadModelFromCurrentState(decoded).capabilities.send).toBe(false);
    expect(encoded).not.toHaveProperty("turns");
    expect(encoded?.fields).not.toHaveProperty("pendingMutations");
  });

  it("retains producer-backed tool and delegate history, omitted text and structured payloads", () => {
    for (const [ref, items] of [
      ["local:tools", toolWireItems()],
      ["local:delegates", subagentCallItems()],
    ] as const) {
      const original = hydrateThread(readWithItems(ref, items), ref, 10);
      const itemWithText = original.history?.turns.flatMap((turn) => turn.items)[0];
      if (!itemWithText) throw new Error("producer fixture had no hydrated history item");
      markItemTextOmitted(itemWithText);
      const encoded = currentTranscriptState(original);
      if (!encoded) throw new Error("versioned fixture history did not encode");
      const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
      if (!decoded) throw new Error("producer-backed current state did not decode");
      const restored = threadModelFromCurrentState(decoded);

      expect(restored.history?.turns).toEqual(original.history?.turns);
      const restoredItem = restored.history?.turns
        .flatMap((turn) => turn.items)
        .find((item) => item.id === itemWithText.id);
      expect(restoredItem && itemTextPresence(restoredItem)).toBe("omitted");
      expect(restored.olderCursor).toBe("older-40");
      expect(restored.overlay?.["stream:s1:agentMessage"]?.item.text).toBe("alpha");
    }
  });

  it("rejects unknown versions, malformed positions and mismatched overlay keys", () => {
    const encoded = currentTranscriptState(hydrateThread(currentRead("local:invalid"), "local:invalid", 10));
    if (!encoded) throw new Error("versioned fixture history did not encode");

    expect(decodeCurrentTranscriptState({ ...encoded, version: 2 })).toBeUndefined();
    expect(
      decodeCurrentTranscriptState({ ...encoded, overlay: { wrong: encoded.overlay["stream:s1:agentMessage"] } }),
    ).toBeUndefined();
    expect(
      decodeCurrentTranscriptState({
        ...encoded,
        history: {
          ...encoded.history,
          turns: [
            {
              id: "turn_1",
              status: "completed",
              items: [{ id: "a", turnId: "turn_1", type: "text", text: "x", position: { entry: -1, item: 0 } }],
            },
          ],
        },
      }),
    ).toBeUndefined();
  });

  it("does not leak unknown fields from decoded state into the model", () => {
    const encoded = currentTranscriptState(hydrateThread(currentRead("local:unknown"), "local:unknown", 10));
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState({
      ...encoded,
      fields: { ...encoded.fields, unexpected: "not model state" },
    });
    if (!decoded) throw new Error("valid state with an unknown field did not decode");
    const restored = threadModelFromCurrentState(decoded);

    expect(restored).not.toHaveProperty("unexpected");
  });

  it("keeps omitted text distinct from explicit empty text", () => {
    const read = readWithItems("local:text", [
      { id: "omitted", turnId: "turn_1", type: "agentMessage" },
      { id: "empty", turnId: "turn_1", type: "agentMessage", text: "" },
    ]);
    const original = hydrateThread(read, "local:text", 10);
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const items = restored.history?.turns.flatMap((turn) => turn.items) ?? [];

    expect(items.find((item) => item.id === "omitted" && itemTextPresence(item) === "omitted")?.text).toBe("");
    expect(items.find((item) => item.id === "empty" && itemTextPresence(item) === "provided")?.text).toBe("");
  });

  it("keeps image routes bound to the serving session through JSON", () => {
    const image = {
      id: "image",
      turnId: "turn_1",
      type: "userMessage",
      text: "image",
      images: [{ type: "image", metadata: { sha: "a".repeat(64) } }],
    };
    const original = hydrateThread(readWithItems("local:image", [image]), "local:image", 10);
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded);

    expect(restored.history?.turns[0]?.items[0]?.images?.[0]?.src).toBe(
      `/s/sess_local%3Aimage/images/${"a".repeat(64)}`,
    );
  });

  it("keeps reasoning chunks and delegate projection revisions through JSON", () => {
    const original = hydrateThread(
      readWithItems("local:reasoning", [{ id: "reason", turnId: "turn_1", type: "reasoning", text: "first chunk" }]),
      "local:reasoning",
      10,
    );
    original.delegates = [
      {
        runGeneration: 1,
        delegateId: "dlg_fixture",
        ownerSessionId: "session-root",
        rootSessionId: "session-root",
        childSessionId: "session-child",
        transcriptRef: "local:child",
        type: "delegate",
        lifecycle: "ended",
        phase: "ended",
        status: "completed",
        resumable: true,
        needsAttention: false,
        projectionRevision: 2,
      } satisfies EvenerDelegateInfo,
    ];
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const item = restored.history?.turns[0]?.items[0];

    expect(item?.text).toBe("first chunk");
    expect(item?.reasoningSummaries).toEqual([["first chunk"]]);
    expect(restored.delegates?.map((delegate) => delegate.projectionRevision)).toEqual(
      original.delegates?.map((delegate) => delegate.projectionRevision),
    );
  });

  it("retains invalidated history state and deferred pages without restoring local authority", () => {
    const original = hydrateThread(currentRead("local:deferred"), "local:deferred", 10);
    if (!original.history) throw new Error("versioned fixture history did not hydrate");
    original.history = {
      ...original.history,
      invalidatedAtGeneration: 2,
      awaited: { bootGeneration: "1", epoch: 2 },
      pendingIncarnation: "inc-2",
      failed: "read failed",
      deferredPages: [{ data: [], nextCursor: "older-20" }],
    };
    original.pendingMutations = [
      { clientMutationId: "local-only", method: "turn/start", executionState: "queued", projectionState: "pending" },
    ];
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded);

    expect(restored.history).toMatchObject({
      invalidatedAtGeneration: 2,
      awaited: { bootGeneration: "1", epoch: 2 },
      pendingIncarnation: "inc-2",
      failed: "read failed",
      deferredPages: [{ data: [], nextCursor: "older-20" }],
    });
    expect(restored.pendingMutations).toBeUndefined();
    expect(restored.capabilities.send).toBe(false);
  });

  it("retires a checkpointed overlay when the same history update reaches both reducers", () => {
    const original = hydrateThread(currentRead("local:retire"), "local:retire", 10);
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded, { capabilities: original.capabilities });
    const update = {
      method: "history/updated" as const,
      params: {
        threadId: "thr_local:retire",
        ref: "local:retire",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 41 },
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "",
          },
        ],
        items: [
          {
            id: "a1",
            turnId: "turn_1",
            type: "agentMessage",
            text: "alpha",
            roundId: "r1",
            version: 41,
            position: { entry: 39, item: 0 },
          },
        ],
      },
    };

    const continued = applyNotification(original, update, 11);
    const resumed = applyNotification(restored, update, 11);
    expect(resumed.turns).toEqual(continued.turns);
    expect(resumed.overlay).toEqual({});
    expect(resumed.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual(["alpha"]);
  });
});
