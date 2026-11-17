// Per-delta derivation cost for the live transcript: a streamed delta must
// reuse every unchanged turn's projection, rows and anchor entries instead of
// re-deriving the whole transcript. The assertions count ELEMENTS (projected
// turns, rows, anchor entries) built per frame, measured by object identity,
// because that is the work the pane pays per streamed delta - not wall-clock
// time, which is not a gate-safe oracle.
import type { AnyNotification, ThreadModel, TranscriptProjection } from "@evener/appwire-client";
import {
  applyNotification,
  createTranscriptProjector,
  hydrateThread,
  makeTranscriptDisplayConfig,
  projectThread,
} from "@evener/appwire-client";
import { describe, expect, test } from "vitest";

const CONFIG = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
const LIVE_TURN_ID = "turn-live";

function wireMessage(turnId: string, index: number, entry: number) {
  return {
    id: `item-${turnId}-${index}`,
    turnId,
    type: index % 3 === 2 ? "commandExecution" : "agentMessage",
    text: `body ${turnId} ${index}`,
    description: index % 3 === 2 ? `did thing ${turnId} ${index}` : undefined,
    status: "completed",
    position: { entry, item: index },
  };
}

function hydratedTranscript(turnCount: number): ThreadModel {
  const turns = Array.from({ length: turnCount }, (_, t) => ({
    id: `turn-${t}`,
    status: "completed",
    version: 1,
    itemsView: "full",
    items: Array.from({ length: 3 }, (_, i) => wireMessage(`turn-${t}`, i, t)),
  }));
  return hydrateThread(
    {
      thread: {
        id: "thr_incremental",
        sessionId: "sess_incremental",
        preview: "incremental",
        ephemeral: false,
        modelProvider: "test",
        createdAt: 1,
        updatedAt: 1,
        status: { type: "active" },
        cwd: "/tmp/project",
        cliVersion: "1.0.0",
        source: "evener",
        evener: { ref: "ref_incremental", capabilities: {}, queue: { revision: 0 } },
        turns,
      },
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc", length: turnCount },
    } as never,
    "ref_incremental",
    1000,
  );
}

function openStream(model: ThreadModel): ThreadModel {
  return applyNotification(
    model,
    {
      method: "overlay/upserted",
      params: {
        threadId: "thr_incremental",
        ref: "ref_incremental",
        item: {
          key: "stream:r1/0:agentMessage",
          kind: "stream",
          turnId: LIVE_TURN_ID,
          roundId: "r1",
          streamId: "r1/0",
          item: {
            type: "agentMessage",
            id: "stream:r1/0:agentMessage",
            turnId: LIVE_TURN_ID,
            roundId: "r1",
            status: "inProgress",
          },
        },
      },
    } as AnyNotification,
    1001,
  );
}

function delta(model: ThreadModel, text: string, now: number, field = "text", key = "stream:r1/0:agentMessage") {
  return applyNotification(
    model,
    {
      method: "overlay/delta",
      params: { threadId: "thr_incremental", ref: "ref_incremental", key, field, delta: text },
    } as AnyNotification,
    now,
  );
}

function rebuilt<T>(previous: readonly T[], next: readonly T[]): number {
  const held = new Set(previous);
  return next.filter((entry) => !held.has(entry)).length;
}

describe("per-delta projection work", () => {
  test("a streamed delta rebuilds only the streaming turn's projection", () => {
    const projector = createTranscriptProjector();
    let model = openStream(hydratedTranscript(40));
    projector.project(model, CONFIG);
    const projection = projector.project(model, CONFIG);
    let previous: TranscriptProjection = projection;
    const turnCount = projection.turns.length;
    expect(turnCount).toBe(41);

    for (let step = 0; step < 5; step += 1) {
      model = delta(model, `chunk${step} `, 1002 + step);
      const next = projector.project(model, CONFIG);
      // The projection keeps every unchanged turn's ProjectedTurn object.
      expect(rebuilt(previous.turns, next.turns)).toBe(1);
      // and the anchor list keeps every anchor entry for those turns.
      expect(rebuilt(previous.anchors, next.anchors)).toBeLessThanOrEqual(1);
      // The eligible-disclosure inventory is spliced, not rebuilt.
      expect(next.eligibleDisclosureIds).toEqual(previous.eligibleDisclosureIds);
      previous = next;
    }
  });

  test("the incremental projection equals a full projection of the same model, frame by frame", () => {
    const projector = createTranscriptProjector();
    let model = openStream(hydratedTranscript(12));
    let now = 1002;
    projector.project(model, CONFIG);
    for (let step = 0; step < 4; step += 1) {
      now += 1;
      model = delta(model, `token${step} `, now);
      expect(projector.project(model, CONFIG)).toEqual(projectThread(model, CONFIG));
    }
  });

  test("an out-of-order item insert falls back to a full projection with the right rank order", () => {
    const projector = createTranscriptProjector();
    let model = openStream(hydratedTranscript(5));
    projector.project(model, CONFIG);
    model = delta(model, "x", 1002);
    projector.project(model, CONFIG);
    // An older entry arriving late: its position sorts before everything held,
    // so every later item's source index shifts.
    model = applyNotification(
      model,
      {
        method: "history/updated",
        params: {
          threadId: "thr_incremental",
          ref: "ref_incremental",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc", length: 6 },
          turns: [{ id: "turn-inserted", status: "completed", version: 1, itemsView: "" }],
          items: [
            {
              type: "agentMessage",
              id: "item-inserted",
              turnId: "turn-inserted",
              text: "older",
              status: "completed",
              position: { entry: -1, item: 0 },
            },
          ],
        },
      } as AnyNotification,
      1003,
    );
    const incremental = projector.project(model, CONFIG);
    expect(incremental).toEqual(projectThread(model, CONFIG));
    // The inserted turn sorts first, ahead of everything from the hydrate.
    expect(incremental.anchors[0]?.sourceIndex).toBe(0);
    expect(incremental.turns[0]?.id).toBe("turn-inserted");
  });

  test("a tool row that starts empty and fills in through deltas keeps updating", () => {
    const projector = createTranscriptProjector();
    let model = hydratedTranscript(3);
    model = applyNotification(
      model,
      {
        method: "overlay/upserted",
        params: {
          threadId: "thr_incremental",
          ref: "ref_incremental",
          item: {
            key: "preview:call-1",
            kind: "tool",
            turnId: "turn-0",
            historyKey: "call-1",
            item: { type: "commandExecution", id: "call-1", turnId: "turn-0", callId: "call-1", status: "inProgress" },
          },
        },
      } as AnyNotification,
      1002,
    );
    projector.project(model, CONFIG);
    for (const chunk of ["Ran ", "npm ", "test"]) {
      model = delta(model, chunk, 1003, "output", "preview:call-1");
      expect(projector.project(model, CONFIG)).toEqual(projectThread(model, CONFIG));
    }
    const live = projector.project(model, CONFIG);
    const streamed = live.turns.flatMap((turn) => turn.entries).find((entry) => entry.item.callId === "call-1");
    expect(streamed?.item.output).toBe("Ran npm test");
  });

  test("settling a stream keeps the settled turns' projections and still matches a full projection", () => {
    const projector = createTranscriptProjector();
    let model = openStream(hydratedTranscript(6));
    projector.project(model, CONFIG);
    model = delta(model, "done", 1002);
    const streamed = projector.project(model, CONFIG);
    model = applyNotification(
      model,
      {
        method: "overlay/end",
        params: { threadId: "thr_incremental", ref: "ref_incremental", roundId: "r1" },
      } as AnyNotification,
      1003,
    );
    model = applyNotification(
      model,
      {
        method: "history/updated",
        params: {
          threadId: "thr_incremental",
          ref: "ref_incremental",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc", length: 7 },
          turns: [{ id: LIVE_TURN_ID, status: "completed", version: 1, itemsView: "" }],
          items: [
            {
              type: "agentMessage",
              id: "item-live",
              turnId: LIVE_TURN_ID,
              roundId: "r1",
              text: "done",
              status: "completed",
              position: { entry: 7, item: 0 },
            },
          ],
        },
      } as AnyNotification,
      1004,
    );
    const settled = projector.project(model, CONFIG);
    // Settling replaces the streamed item's identity, so the position-sorted
    // document really does change and the full path is right. What the reader
    // sees must not: same projected turns, same text, same order.
    expect(settled).toEqual(projectThread(model, CONFIG));
    expect(settled.turns.map((turn) => turn.id)).toEqual(streamed.turns.map((turn) => turn.id));
    const settledLive = settled.turns.at(-1)?.entries.at(-1);
    expect(settledLive?.item.text).toBe("done");
  });
});
