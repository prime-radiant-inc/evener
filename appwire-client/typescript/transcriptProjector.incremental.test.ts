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
  entryDisplayKey,
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

  test("a rebuilt turn that changes which round key it claims does not leave a later reused turn stale", () => {
    // displayKeyFor hands a round's key to the first projected item that claims
    // it. When an earlier turn starts claiming a key that a later turn used to
    // own, that later turn must lose it - and if the later turn is reused, it
    // keeps the old key. Two rows then share a display key, and a renderer
    // keying by it reconciles the wrong rows.
    const twoTurns = (roundInFirst: boolean): ThreadModel => {
      const items = (turnId: string, entry: number, items: object[]) => ({
        id: turnId,
        status: "completed",
        version: 1,
        itemsView: "",
        items: items.map((item) => ({ turnId, status: "completed", position: { entry, item: 0 }, ...item })),
      });
      return hydrateThread(
        {
          thread: {
            id: "thr_roundkey",
            sessionId: "sess_roundkey",
            preview: "roundkey",
            ephemeral: false,
            modelProvider: "test",
            createdAt: 1,
            updatedAt: 1,
            status: { type: "active" },
            cwd: "/tmp/project",
            cliVersion: "1.0.0",
            source: "evener",
            evener: { ref: "ref_roundkey", capabilities: {}, queue: { revision: 0 } },
            turns: [
              items("turn-a", 1, [
                roundInFirst
                  ? { id: "a-1", type: "agentMessage", roundId: "r1", text: "reply from a" }
                  : { id: "a-1", type: "commandExecution", description: "ran" },
              ]),
              items("turn-b", 2, [{ id: "b-1", type: "agentMessage", roundId: "r1", text: "reply from b" }]),
            ],
          },
          items: [],
        } as never,
        "ref_roundkey",
        1000,
      );
    };

    const first = twoTurns(false);
    const projector = createTranscriptProjector();
    const before = projector.project(first, CONFIG);
    const keys = (projection: TranscriptProjection) =>
      projection.turns.map((turn) => turn.entries.map((entry) => `${entry.id}:${entryDisplayKey(entry)}`).join(","));
    expect(keys(before).at(-1)).toContain("b-1:round:r1:agentMessage");

    // Only turn a changed, so turn b is the object that must be reused.
    const updated = twoTurns(true);
    const firstTurn = first.turns.at(0);
    const secondTurn = first.turns.at(1);
    const updatedTurn = updated.turns.at(0);
    if (firstTurn === undefined || secondTurn === undefined || updatedTurn === undefined) throw new Error("no turns");
    const next: ThreadModel = { ...first, turns: [updatedTurn, secondTurn] };
    const reused = projector.project(next, CONFIG);
    const full = projectThread(next, CONFIG);

    // The whole contract: same display key on every entry as a full projection.
    expect(keys(reused)).toEqual(keys(full));
    // And the round key moved to the earlier turn, as a full projection gives it.
    expect(keys(reused).at(-1)).toContain("b-1:b-1");
  });

  test("an earlier turn whose entry count changes re-projects every later turn at its new index", () => {
    // The start-index check: a turn can keep its (identity, position) rank
    // signature while the number of entries it projects changes. Here turn-a's
    // only item is a hook exit the tools preset hides at exit 0 and shows as a
    // critical row when non-zero. The position-sorted document does not move,
    // so only the running projected index can tell turn-b's anchors to shift -
    // and scroll restoration keys on those anchor indexes.
    const twoTurns = (hookExitCode: number): ThreadModel => {
      const items = (turnId: string, entry: number, items: object[]) => ({
        id: turnId,
        status: "completed",
        version: 1,
        itemsView: "",
        items: items.map((item) => ({ turnId, status: "completed", position: { entry, item: 0 }, ...item })),
      });
      return hydrateThread(
        {
          thread: {
            id: "thr_startindex",
            sessionId: "sess_startindex",
            preview: "startindex",
            ephemeral: false,
            modelProvider: "test",
            createdAt: 1,
            updatedAt: 1,
            status: { type: "active" },
            cwd: "/tmp/project",
            cliVersion: "1.0.0",
            source: "evener",
            evener: { ref: "ref_startindex", capabilities: {}, queue: { revision: 0 } },
            turns: [
              items("turn-a", 1, [
                { id: "a-hook", type: "systemMessage", eventKind: "hook_completed", exitCode: hookExitCode, text: "" },
              ]),
              items("turn-b", 2, [{ id: "b-1", type: "agentMessage", text: "reply from b" }]),
            ],
          },
          items: [],
        } as never,
        "ref_startindex",
        1000,
      );
    };

    const first = twoTurns(0);
    const projector = createTranscriptProjector();
    const before = projector.project(first, CONFIG);
    // The exit-0 hook is hidden at tools, so turn-b's only anchor starts at 0.
    expect(before.anchors.map((anchor) => [anchor.id, anchor.index])).toEqual([["b-1", 0]]);

    // Only turn a changed, and turn b is the same object, so the projector must
    // still re-project turn b: its start index moved from 0 to 1.
    const updated = twoTurns(7);
    const firstTurn = first.turns.at(0);
    const secondTurn = first.turns.at(1);
    const updatedTurn = updated.turns.at(0);
    if (firstTurn === undefined || secondTurn === undefined || updatedTurn === undefined) throw new Error("no turns");
    const next: ThreadModel = { ...first, turns: [updatedTurn, secondTurn] };
    const reused = projector.project(next, CONFIG);
    const full = projectThread(next, CONFIG);

    // The whole contract: the incremental answer is the full answer, anchors included.
    expect(reused).toEqual(full);
    expect(reused.anchors.map((anchor) => [anchor.id, anchor.index])).toEqual([
      ["a-hook", 0],
      ["b-1", 1],
    ]);
  });
});
