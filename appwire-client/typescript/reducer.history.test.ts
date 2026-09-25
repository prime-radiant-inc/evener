// @vitest-environment node
// The shared reducer's versioned history, live overlay and open-turn display
// rule (docs/superpowers/specs/2026-09-25-transcript-read-model-design.md:
// "Recorded length", "Reads", "Live history notifications", "The live
// overlay", "Turn status"). One test per client rule, each on minimal wire
// fixtures.

import { describe, expect, test } from "vitest";
import { displayTurnStatus } from "./itemFailure";
import type { ThreadModel } from "./model";
import {
  applyHistoryReadFailure,
  applyNotification,
  applyReadResponse,
  hydrateThread,
  issueLatestWindowRead,
  mergeOlderItemPage,
} from "./reducer";
import type {
  AnyNotification,
  HistoryChanges,
  OverlayItem,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  Turn,
} from "./types.gen";

const REF = "ref_h";
const THREAD_ID = "thr_h";
const NOW = 5000;

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

function thread(turns: Turn[], activeTurnId?: string): Thread {
  return {
    id: THREAD_ID,
    sessionId: "sess_h",
    preview: "",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: activeTurnId ? { type: "active" } : { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    turns,
    evener: {
      ref: REF,
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      ...(activeTurnId ? { activeTurnId } : {}),
    },
  };
}

function itemKey(turnId: string, ordinal: number, part = 0): string {
  return `apptranscript-item-v2:${turnId}:${ordinal}:${part}`;
}

// A projected history item: identity, position and version all follow from
// the entry ordinal it was projected from.
function item(turnId: string, ordinal: number, overrides: Partial<ThreadItem> = {}): ThreadItem {
  const key = itemKey(turnId, ordinal, overrides.position?.item ?? 0);
  return {
    type: "agentMessage",
    id: key,
    transcriptKey: key,
    turnId,
    position: { entry: ordinal + 1, item: 0 },
    version: ordinal + 1,
    text: `entry ${ordinal}`,
    ...overrides,
  };
}

function turn(id: string, version: number, items: ThreadItem[] = [], status = "completed"): Turn {
  return { id, itemsView: "full", status, version, items };
}

interface Snapshot {
  bootGeneration?: string;
  epoch?: number;
  incarnation?: string;
  length?: number;
}

function read(
  turns: Turn[],
  options: Snapshot & {
    requestGeneration?: number;
    overlay?: OverlayItem[];
    authoritative?: boolean;
    changes?: HistoryChanges;
    olderCursor?: string;
    activeTurnId?: string;
  } = {},
): ThreadReadResponse {
  return {
    thread: thread(turns, options.activeTurnId),
    requestGeneration: options.requestGeneration ?? 1,
    bootGeneration: options.bootGeneration ?? "1",
    epoch: options.epoch ?? 0,
    snapshot: { incarnation: options.incarnation ?? "inc_a", length: options.length ?? 100 },
    overlay: options.overlay ?? [],
    ...(options.authoritative === undefined ? {} : { authoritative: options.authoritative }),
    ...(options.changes ? { changes: options.changes } : {}),
    ...(options.olderCursor ? { olderCursor: options.olderCursor } : {}),
  };
}

function page(turns: Turn[], options: Snapshot & { authoritative?: boolean } = {}): ThreadTurnsListResponse {
  return {
    data: turns,
    bootGeneration: options.bootGeneration ?? "1",
    epoch: options.epoch ?? 0,
    snapshot: { incarnation: options.incarnation ?? "inc_a", length: options.length ?? 100 },
    ...(options.authoritative === undefined ? {} : { authoritative: options.authoritative }),
  };
}

function updated(items: ThreadItem[], turns: Turn[] = [], options: Snapshot = {}): AnyNotification {
  return {
    method: "history/updated",
    params: {
      threadId: THREAD_ID,
      ref: REF,
      bootGeneration: options.bootGeneration ?? "1",
      epoch: options.epoch ?? 0,
      snapshot: { incarnation: options.incarnation ?? "inc_a", length: options.length ?? 200 },
      turns: turns.map(({ items: _items, ...rest }) => rest),
      items,
    },
  };
}

function upserted(overlayItem: OverlayItem): AnyNotification {
  return { method: "overlay/upserted", params: { threadId: THREAD_ID, ref: REF, item: overlayItem } };
}

function delta(key: string, text: string, field = "text"): AnyNotification {
  return { method: "overlay/delta", params: { threadId: THREAD_ID, ref: REF, key, field, delta: text } };
}

function streamOverlay(turnId: string, roundId: string, attempt: number, text: string): OverlayItem {
  const streamId = `${roundId}/${attempt}`;
  const key = `stream:${streamId}:agentMessage`;
  return {
    key,
    kind: "stream",
    turnId,
    roundId,
    streamId,
    item: { type: "agentMessage", id: key, turnId, roundId, text, status: "inProgress" },
  };
}

function toolOverlay(turnId: string, roundId: string, historyKey: string, output: string): OverlayItem {
  const key = `tool:${historyKey}`;
  return {
    key,
    kind: "tool",
    turnId,
    roundId,
    callId: "call_1",
    historyKey,
    item: { type: "commandExecution", id: key, turnId, roundId, callId: "call_1", output, status: "inProgress" },
  };
}

function noticeOverlay(n: number, anchorEntry: number, turnId: string): OverlayItem {
  const key = `notice:${n}`;
  return {
    key,
    kind: "notice",
    turnId,
    anchor: { entry: anchorEntry, item: 1 << 30, sub: n },
    item: { type: "systemMessage", id: key, turnId, text: `notice ${n}`, status: "completed" },
  };
}

// What a reader sees: each displayed turn and the ids of its items in order.
function shown(model: ThreadModel): Array<[string, string[]]> {
  return model.turns.map((t) => [t.id, t.items.map((i) => i.id)]);
}

function textOf(model: ThreadModel, id: string): string | undefined {
  for (const t of model.turns) {
    const found = t.items.find((i) => i.id === id);
    if (found) return found.text;
  }
  return undefined;
}

function hydrate(turns: Turn[], options: Parameters<typeof read>[1] = {}): ThreadModel {
  return hydrateThread(read(turns, options), REF, NOW);
}

// Issues the next latest-window read the way the store does, returning the
// model that recorded it and the generation the request carries.
function issue(model: ThreadModel): [ThreadModel, number] {
  const issued = issueLatestWindowRead(model);
  return [issued.model, issued.requestGeneration];
}

describe("merging history by version", () => {
  test("a lower version is ignored and an equal one is a no-op", () => {
    const model = hydrate([turn("t1", 3, [item("t1", 2, { text: "current" })])]);
    const lower = applyNotification(model, updated([item("t1", 2, { text: "stale", version: 2 })]), NOW);
    expect(textOf(lower, itemKey("t1", 2))).toBe("current");
    const equal = applyNotification(model, updated([item("t1", 2, { text: "same version" })]), NOW);
    expect(equal.turns).toBe(model.turns);
    const higher = applyNotification(model, updated([item("t1", 2, { text: "newer", version: 9 })]), NOW);
    expect(textOf(higher, itemKey("t1", 2))).toBe("newer");
  });

  test("an update never removes an item", () => {
    const model = hydrate([turn("t1", 2, [item("t1", 0), item("t1", 1)])]);
    const next = applyNotification(model, updated([item("t1", 5)], [turn("t1", 6)]), NOW);
    expect(shown(next)).toEqual([["t1", [itemKey("t1", 0), itemKey("t1", 1), itemKey("t1", 5)]]]);
  });

  test("turns merge by version too", () => {
    const model = hydrate([turn("t1", 4, [item("t1", 0)], "inProgress")]);
    const older = applyNotification(model, updated([], [turn("t1", 3, [], "failed")]), NOW);
    expect(older.turns[0]?.status).toBe("inProgress");
    const newer = applyNotification(model, updated([], [turn("t1", 5, [], "completed")]), NOW);
    expect(newer.turns[0]?.status).toBe("completed");
    expect(newer.turns[0]?.version).toBe(5);
  });

  test("items display by position and turns by their first item's position", () => {
    const model = hydrate([turn("t2", 11, [item("t2", 10)])]);
    const next = applyNotification(
      model,
      updated([item("t2", 12), item("t1", 3), item("t2", 11, { position: { entry: 12, item: 1 } })], [turn("t1", 4)]),
      NOW,
    );
    expect(shown(next)).toEqual([
      ["t1", [itemKey("t1", 3)]],
      ["t2", [itemKey("t2", 10), itemKey("t2", 11, 1), itemKey("t2", 12)]],
    ]);
  });

  test("an older page merges by version and never replaces newer held items", () => {
    const model = hydrate([turn("t2", 11, [item("t2", 10, { text: "live" })])]);
    const next = mergeOlderItemPage(
      model,
      page([turn("t1", 4, [item("t1", 3)]), turn("t2", 11, [item("t2", 10, { text: "old", version: 10 })])]),
    );
    expect(shown(next)).toEqual([
      ["t1", [itemKey("t1", 3)]],
      ["t2", [itemKey("t2", 10)]],
    ]);
    expect(textOf(next, itemKey("t2", 10))).toBe("live");
  });
});

describe("the generation state machine", () => {
  test("an older-epoch update is discarded", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)])], { epoch: 3 });
    const next = applyNotification(model, updated([item("t1", 4)], [], { epoch: 2 }), NOW);
    expect(shown(next)).toEqual(shown(model));
    expect(next.history?.invalidatedAtGeneration).toBeUndefined();
  });

  test("an update from a lower boot generation is ignored", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)])], { bootGeneration: "4" });
    const next = applyNotification(model, updated([item("t1", 4)], [], { bootGeneration: "3" }), NOW);
    expect(shown(next)).toEqual(shown(model));
    expect(next.history?.invalidatedAtGeneration).toBeUndefined();
  });

  test("a higher boot generation invalidates, drops updates, and the next latest window replaces", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)])], { bootGeneration: "1" });
    const invalid = applyNotification(model, updated([item("t1", 4)], [], { bootGeneration: "2" }), NOW);
    expect(invalid.history?.invalidatedAtGeneration).toBe(1);
    expect(shown(invalid)).toEqual(shown(model));
    const dropped = applyNotification(invalid, updated([item("t1", 5)], [], { bootGeneration: "2" }), NOW);
    expect(shown(dropped)).toEqual(shown(model));
    const overlayDropped = applyNotification(dropped, upserted(streamOverlay("t1", "r9", 0, "hi")), NOW);
    expect(overlayDropped.overlay).toEqual({});

    const [issued, generation] = issue(overlayDropped);
    const replaced = applyReadResponse(
      issued,
      read([turn("t9", 3, [item("t9", 2)])], { bootGeneration: "2", requestGeneration: generation }),
      NOW,
    );
    expect(shown(replaced)).toEqual([["t9", [itemKey("t9", 2)]]]);
    expect(replaced.history?.invalidatedAtGeneration).toBeUndefined();
    expect(replaced.history?.bootGeneration).toBe("2");
  });

  test("a latest window from another generation token replaces whole history", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)]), turn("t2", 3, [item("t2", 2)])], {
      bootGeneration: "daemonless",
    });
    const [issued, generation] = issue(model);
    const next = applyReadResponse(
      issued,
      read([turn("t2", 3, [item("t2", 2)])], { bootGeneration: "7", requestGeneration: generation }),
      NOW,
    );
    expect(shown(next)).toEqual([["t2", [itemKey("t2", 2)]]]);
  });

  test("a resync push invalidates; the recovering read is dropped and the next one replaces", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)])], { epoch: 0 });
    const [reading, recovering] = issue(model);
    const invalid = applyNotification(
      reading,
      { method: "evener/thread/resync", params: { threadId: THREAD_ID, ref: REF, bootGeneration: "1", epoch: 1 } },
      NOW,
    );
    expect(invalid.history?.invalidatedAtGeneration).toBe(recovering);
    // The read's cut was taken before the resync: it carries the older epoch.
    const stale = applyReadResponse(
      invalid,
      read([turn("t1", 5, [item("t1", 0), item("t1", 4)])], { epoch: 0, requestGeneration: recovering }),
      NOW,
    );
    expect(stale).toBe(invalid);
    const [reissued, fresh] = issue(stale);
    const replaced = applyReadResponse(
      reissued,
      read([turn("t1", 2, [item("t1", 1)])], { epoch: 1, requestGeneration: fresh }),
      NOW,
    );
    expect(shown(replaced)).toEqual([["t1", [itemKey("t1", 1)]]]);
    expect(replaced.history?.epoch).toBe(1);
  });

  test("a read with a newer epoch replaces instead of merging", () => {
    const model = hydrate([turn("t1", 2, [item("t1", 0), item("t1", 1)])], { epoch: 0 });
    const [issued, generation] = issue(model);
    const next = applyReadResponse(
      issued,
      read([turn("t1", 3, [item("t1", 2)])], { epoch: 1, requestGeneration: generation }),
      NOW,
    );
    expect(shown(next)).toEqual([["t1", [itemKey("t1", 2)]]]);
  });

  test("an update from another incarnation invalidates the thread", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)])]);
    const next = applyNotification(model, updated([item("t1", 4)], [], { incarnation: "inc_b" }), NOW);
    expect(next.history?.invalidatedAtGeneration).toBe(1);
    expect(shown(next)).toEqual(shown(model));
  });
});

describe("request generations and incarnations", () => {
  test("request generations never reset, even when a read replaces history", () => {
    const model = hydrate([], { requestGeneration: 1 });
    const [first, one] = issue(model);
    const replaced = applyReadResponse(first, read([], { bootGeneration: "2", requestGeneration: one }), NOW);
    const [, two] = issue(replaced);
    expect([one, two]).toEqual([2, 3]);
  });

  test("a stale-generation response is discarded after a newer one applied", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)])], { incarnation: "inc_a" });
    const [abandoned, older] = issue(model);
    const [issued, newer] = issue(abandoned);
    const applied = applyReadResponse(
      issued,
      read([turn("t5", 6, [item("t5", 5)])], { incarnation: "inc_b", requestGeneration: newer }),
      NOW,
    );
    expect(applied.history?.appliedGeneration).toBe(newer);
    // The slow response from the old incarnation must not overwrite the newer one.
    const late = applyReadResponse(
      applied,
      read([turn("t1", 1, [item("t1", 0)])], { incarnation: "inc_a", requestGeneration: older }),
      NOW,
    );
    expect(late).toBe(applied);
    expect(shown(late)).toEqual([["t5", [itemKey("t5", 5)]]]);
  });

  test("a latest window from a new incarnation replaces whole history", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)]), turn("t2", 3, [item("t2", 2)])]);
    const [issued, generation] = issue(model);
    const next = applyReadResponse(
      issued,
      read([turn("t2", 3, [item("t2", 2, { text: "rebuilt" })])], {
        incarnation: "inc_b",
        requestGeneration: generation,
      }),
      NOW,
    );
    expect(shown(next)).toEqual([["t2", [itemKey("t2", 2)]]]);
    expect(textOf(next, itemKey("t2", 2))).toBe("rebuilt");
    expect(next.history?.incarnation).toBe("inc_b");
  });

  test("a same-incarnation response with a shorter length is discarded", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)])], { length: 500 });
    const [issued, generation] = issue(model);
    const next = applyReadResponse(
      issued,
      read([turn("t1", 3, [item("t1", 0), item("t1", 2)])], { length: 400, requestGeneration: generation }),
      NOW,
    );
    expect(next).toBe(issued);
  });

  test("a live latest window merges by version and keeps older pages", () => {
    const model = mergeOlderItemPage(
      hydrate([turn("t2", 3, [item("t2", 2, { text: "held" })])], { olderCursor: "c1" }),
      page([turn("t1", 1, [item("t1", 0)])]),
    );
    const [issued, generation] = issue(model);
    const next = applyReadResponse(
      issued,
      read([turn("t2", 4, [item("t2", 2, { text: "held", version: 2 }), item("t2", 3)])], {
        length: 150,
        requestGeneration: generation,
      }),
      NOW,
    );
    expect(shown(next)).toEqual([
      ["t1", [itemKey("t1", 0)]],
      ["t2", [itemKey("t2", 2), itemKey("t2", 3)]],
    ]);
    expect(next.history?.length).toBe(150);
  });

  test("backfill pages accumulate in any order and drop when their snapshot is older", () => {
    const model = hydrate([turn("t3", 5, [item("t3", 4)])], { length: 500 });
    const newerPage = mergeOlderItemPage(model, page([turn("t2", 3, [item("t2", 2)])], { length: 600 }));
    const olderPage = mergeOlderItemPage(newerPage, page([turn("t1", 1, [item("t1", 0)])], { length: 500 }));
    expect(shown(olderPage).map(([id]) => id)).toEqual(["t1", "t2", "t3"]);
    const shorter = mergeOlderItemPage(olderPage, page([turn("t0", 1, [item("t0", 0)])], { length: 400 }));
    expect(shorter.turns).toBe(olderPage.turns);
  });

  test("pages of a newly seen incarnation wait for its latest window; others are discarded", () => {
    const model = hydrate([turn("t3", 5, [item("t3", 4)])], { incarnation: "inc_a" });
    const invalid = applyNotification(model, updated([item("t3", 6)], [], { incarnation: "inc_b" }), NOW);
    const first = mergeOlderItemPage(invalid, page([turn("t1", 1, [item("t1", 0)])], { incarnation: "inc_b" }));
    const deferred = mergeOlderItemPage(first, page([turn("t2", 3, [item("t2", 2)])], { incarnation: "inc_b" }));
    const discarded = mergeOlderItemPage(deferred, page([turn("t0", 1, [item("t0", 0)])], { incarnation: "inc_c" }));
    expect(shown(discarded)).toEqual(shown(model));
    const [issued, generation] = issue(discarded);
    const replaced = applyReadResponse(
      issued,
      read([turn("t3", 7, [item("t3", 4), item("t3", 6)])], { incarnation: "inc_b", requestGeneration: generation }),
      NOW,
    );
    expect(shown(replaced)).toEqual([
      ["t1", [itemKey("t1", 0)]],
      ["t2", [itemKey("t2", 2)]],
      ["t3", [itemKey("t3", 4), itemKey("t3", 6)]],
    ]);
  });
});

describe("authoritative (daemonless) reads", () => {
  const daemonless = { bootGeneration: "daemonless", authoritative: true } as const;

  test("a latest window drops held items past it and keeps older pages", () => {
    const model = mergeOlderItemPage(
      hydrate([turn("t2", 4, [item("t2", 2), item("t2", 3)]), turn("t3", 5, [item("t3", 4)])], daemonless),
      page([turn("t1", 1, [item("t1", 0)])], daemonless),
    );
    const [issued, generation] = issue(model);
    // The file was rewritten past entry 2 within the same snapshot lineage.
    const next = applyReadResponse(
      issued,
      read([turn("t2", 3, [item("t2", 2, { text: "authoritative" })])], {
        ...daemonless,
        length: 120,
        requestGeneration: generation,
      }),
      NOW,
    );
    expect(shown(next)).toEqual([
      ["t1", [itemKey("t1", 0)]],
      ["t2", [itemKey("t2", 2)]],
    ]);
    expect(textOf(next, itemKey("t2", 2))).toBe("authoritative");
  });

  test("a page replaces the items in its own position range", () => {
    const model = hydrate([turn("t1", 3, [item("t1", 0), item("t1", 1), item("t1", 2)])], daemonless);
    const next = mergeOlderItemPage(
      model,
      page([turn("t1", 3, [item("t1", 0, { text: "page" }), item("t1", 2, { text: "page" })])], daemonless),
    );
    expect(shown(next)).toEqual([["t1", [itemKey("t1", 0), itemKey("t1", 2)]]]);
    expect(textOf(next, itemKey("t1", 0))).toBe("page");
  });

  test("changes refresh a held tool item outside the window", () => {
    const tool = item("t1", 0, { type: "commandExecution", callId: "call_1", status: "inProgress", text: undefined });
    const model = mergeOlderItemPage(
      hydrate([turn("t2", 6, [item("t2", 5)])], daemonless),
      page([turn("t1", 1, [tool])], daemonless),
    );
    const [issued, generation] = issue(model);
    const next = applyReadResponse(
      issued,
      read([turn("t2", 6, [item("t2", 5)])], {
        ...daemonless,
        requestGeneration: generation,
        changes: {
          items: [{ ...tool, status: "completed", output: "done", version: 4, completedAtEntry: 4 }],
        },
      }),
      NOW,
    );
    const refreshed = next.turns.find((t) => t.id === "t1")?.items[0];
    expect(refreshed?.status).toBe("completed");
    expect(refreshed?.output).toBe("done");
  });
});

describe("the live overlay", () => {
  test("a stream takes deltas, a reset drops its attempt, and the next attempt streams anew", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)], "inProgress")], { activeTurnId: "t1" });
    const first = streamOverlay("t1", "r1", 0, "Hel");
    let next = applyNotification(model, upserted(first), NOW);
    next = applyNotification(next, delta(first.key, "lo"), NOW);
    expect(textOf(next, first.key)).toBe("Hello");
    next = applyNotification(
      next,
      { method: "overlay/reset", params: { threadId: THREAD_ID, ref: REF, streamId: "r1/0" } },
      NOW,
    );
    expect(shown(next)).toEqual([["t1", [itemKey("t1", 0)]]]);
    const second = streamOverlay("t1", "r1", 1, "Hi");
    next = applyNotification(next, upserted(second), NOW);
    next = applyNotification(next, delta(first.key, " stale"), NOW);
    expect(shown(next)).toEqual([["t1", [itemKey("t1", 0), second.key]]]);
    expect(textOf(next, second.key)).toBe("Hi");
  });

  test("a stream is covered once history holds its round, and later deltas are ignored", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)], "inProgress")], { activeTurnId: "t1" });
    const stream = streamOverlay("t1", "r1", 0, "Hello");
    let next = applyNotification(model, upserted(stream), NOW);
    next = applyNotification(next, updated([item("t1", 1, { text: "Hello there", roundId: "r1" })]), NOW);
    expect(shown(next)).toEqual([["t1", [itemKey("t1", 0), itemKey("t1", 1)]]]);
    expect(next.overlay).toEqual({});
    const late = applyNotification(next, delta(stream.key, " late"), NOW);
    expect(shown(late)).toEqual(shown(next));
    const reupserted = applyNotification(next, upserted(stream), NOW);
    expect(reupserted.overlay).toEqual({});
  });

  test("a preview survives the ASSISTANT item and goes with the COMMUNICATE item", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)], "inProgress")], { activeTurnId: "t1" });
    const preview: OverlayItem = {
      key: "preview:call_c",
      kind: "preview",
      turnId: "t1",
      roundId: "r1",
      streamId: "r1/0",
      callId: "call_c",
      item: { type: "agentMessage", id: "preview:call_c", turnId: "t1", callId: "call_c", text: "Dear user" },
    };
    let next = applyNotification(model, upserted(preview), NOW);
    next = applyNotification(next, updated([item("t1", 1, { type: "reasoning", roundId: "r1" })]), NOW);
    expect(shown(next)).toEqual([["t1", [itemKey("t1", 0), itemKey("t1", 1), preview.key]]]);
    next = applyNotification(next, updated([item("t1", 2, { callId: "call_c", text: "Dear user, done" })]), NOW);
    expect(shown(next)).toEqual([["t1", [itemKey("t1", 0), itemKey("t1", 1), itemKey("t1", 2)]]]);
  });

  test("a tool overlay is laid over its in-progress history item and dropped once it completes", () => {
    const historyKey = itemKey("t1", 1);
    const tool = item("t1", 1, { type: "commandExecution", callId: "call_1", status: "inProgress", text: undefined });
    const model = hydrate([turn("t1", 2, [item("t1", 0), tool], "inProgress")], { activeTurnId: "t1" });
    const overlayItem = toolOverlay("t1", "r1", historyKey, "line 1\n");
    let next = applyNotification(model, upserted(overlayItem), NOW);
    next = applyNotification(next, delta(overlayItem.key, "line 2\n", "output"), NOW);
    const laid = next.turns[0]?.items[1];
    expect(laid?.id).toBe(historyKey);
    expect(laid?.version).toBe(2);
    expect(laid?.output).toBe("line 1\nline 2\n");
    expect(laid?.status).toBe("inProgress");

    next = applyNotification(
      next,
      updated([{ ...tool, status: "completed", output: "recorded", version: 4, completedAtEntry: 4 }]),
      NOW,
    );
    expect(next.overlay).toEqual({});
    expect(next.turns[0]?.items[1]?.output).toBe("recorded");
    expect(next.turns[0]?.items[1]?.overlayKey).toBeUndefined();
  });

  test("overlay/end drops the round's streams, previews and tools but keeps notices", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)], "inProgress")], { activeTurnId: "t1" });
    let next = applyNotification(model, upserted(streamOverlay("t1", "r1", 0, "partial")), NOW);
    next = applyNotification(next, upserted(toolOverlay("t1", "r1", "tool-without-history", "")), NOW);
    next = applyNotification(next, upserted(noticeOverlay(0, 1, "t1")), NOW);
    next = applyNotification(
      next,
      { method: "overlay/end", params: { threadId: THREAD_ID, ref: REF, roundId: "r1" } },
      NOW,
    );
    expect(Object.keys(next.overlay ?? {})).toEqual(["notice:0"]);
    expect(shown(next)).toEqual([["t1", [itemKey("t1", 0), "notice:0"]]]);
  });

  test("a read replaces the overlay with its own", () => {
    const model = applyNotification(
      hydrate([turn("t1", 1, [item("t1", 0)], "inProgress")], { activeTurnId: "t1" }),
      upserted(streamOverlay("t1", "r1", 0, "old")),
      NOW,
    );
    const [issued, generation] = issue(model);
    const fresh = streamOverlay("t1", "r2", 0, "new");
    const next = applyReadResponse(
      issued,
      read([turn("t1", 1, [item("t1", 0)], "inProgress")], {
        requestGeneration: generation,
        overlay: [fresh],
        activeTurnId: "t1",
      }),
      NOW,
    );
    expect(Object.keys(next.overlay ?? {})).toEqual([fresh.key]);
    expect(shown(next)).toEqual([["t1", [itemKey("t1", 0), fresh.key]]]);
  });

  test("a stream for a turn history does not hold yet gets a placeholder turn", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)])]);
    expect(shown(model)).toEqual([["t1", [itemKey("t1", 0)]]]);
    const stream = streamOverlay("t2", "r1", 0, "thinking");
    const next = applyNotification(model, upserted(stream), NOW);
    expect(shown(next)).toEqual([
      ["t1", [itemKey("t1", 0)]],
      ["t2", [stream.key]],
    ]);
  });

  test("notices display at their anchors, ordered among the items of two turns by sub", () => {
    const model = hydrate([
      turn("t1", 2, [item("t1", 0), item("t1", 1)]),
      turn("t2", 4, [item("t2", 2), item("t2", 3)], "inProgress"),
    ]);
    let next = model;
    // Two notices after entry 1 (t1's last), one after entry 2 (t2's first).
    for (const notice of [noticeOverlay(2, 3, "t2"), noticeOverlay(1, 2, "t1"), noticeOverlay(0, 2, "t1")]) {
      next = applyNotification(next, upserted(notice), NOW);
    }
    expect(shown(next)).toEqual([
      ["t1", [itemKey("t1", 0), itemKey("t1", 1), "notice:0", "notice:1"]],
      ["t2", [itemKey("t2", 2), "notice:2", itemKey("t2", 3)]],
    ]);
  });

  test("an overlay delta leaves every other turn's reference untouched", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)]), turn("t2", 3, [item("t2", 2)], "inProgress")], {
      activeTurnId: "t2",
    });
    const stream = streamOverlay("t2", "r1", 0, "a");
    const streaming = applyNotification(model, upserted(stream), NOW);
    const next = applyNotification(streaming, delta(stream.key, "b"), NOW);
    expect(next.turns[0]).toBe(model.turns[0]);
    expect(next.turns[1]).not.toBe(streaming.turns[1]);
    expect(textOf(next, stream.key)).toBe("ab");
  });
});

describe("failed history", () => {
  test("a failed read keeps history, stops merges, keeps the overlay live, and a later read recovers", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)], "inProgress")], { activeTurnId: "t1" });
    const failed = applyHistoryReadFailure(model, "thread history failed at entry 7");
    expect(failed.history?.failed).toBe("thread history failed at entry 7");
    expect(shown(failed)).toEqual(shown(model));
    const unmerged = applyNotification(failed, updated([item("t1", 3)]), NOW);
    expect(shown(unmerged)).toEqual(shown(model));
    const stream = streamOverlay("t1", "r1", 0, "still live");
    const live = applyNotification(unmerged, upserted(stream), NOW);
    expect(shown(live)).toEqual([["t1", [itemKey("t1", 0), stream.key]]]);

    const [issued, generation] = issue(live);
    const recovered = applyReadResponse(
      issued,
      read([turn("t1", 4, [item("t1", 0), item("t1", 3)])], { epoch: 1, requestGeneration: generation }),
      NOW,
    );
    expect(recovered.history?.failed).toBeUndefined();
    expect(shown(recovered)).toEqual([["t1", [itemKey("t1", 0), itemKey("t1", 3)]]]);
  });
});

describe("running state and the open-turn display rule", () => {
  test("an open turn shows as running only while it is the running turn", () => {
    const model = hydrate(
      [turn("t1", 1, [item("t1", 0)], "inProgress"), turn("t2", 3, [item("t2", 2)], "inProgress")],
      {
        activeTurnId: "t2",
      },
    );
    expect(model.runningTurnId).toBe("t2");
    const [t1, t2] = model.turns;
    if (!t1 || !t2) throw new Error("expected two turns");
    expect(displayTurnStatus(t1, model)).toBe("interrupted");
    expect(displayTurnStatus(t2, model)).toBe("inProgress");
    const idle = applyNotification(
      model,
      { method: "thread/status/changed", params: { threadId: THREAD_ID, ref: REF, status: { type: "idle" } } },
      NOW,
    );
    expect(idle.runningTurnId).toBeUndefined();
    expect(displayTurnStatus(t2, idle)).toBe("interrupted");
    expect(displayTurnStatus({ id: "t0", status: "completed" }, idle)).toBe("completed");
    const running = applyNotification(
      idle,
      {
        method: "thread/status/changed",
        params: { threadId: THREAD_ID, ref: REF, status: { type: "active" }, activeTurnId: "t1" },
      },
      NOW,
    );
    expect(displayTurnStatus(t1, running)).toBe("inProgress");
  });

  test("steering items come from history with their server identity", () => {
    const model = hydrate([turn("t1", 1, [item("t1", 0)], "inProgress")], { activeTurnId: "t1" });
    const steer = item("t1", 1, {
      type: "steering",
      source: "user",
      text: "also check the tests",
      clientMutationId: "m1",
    });
    const next = applyNotification(model, updated([steer]), NOW);
    const shownSteer = next.turns[0]?.items[1];
    expect(shownSteer?.id).toBe(itemKey("t1", 1));
    expect(shownSteer?.clientMutationId).toBe("m1");
    expect(next.turns[0]?.items.map((i) => i.id).filter((id) => id.includes("live"))).toEqual([]);
  });

  test("a recorded model output clears a pending model retry", () => {
    const model = applyNotification(
      hydrate([turn("t1", 1, [item("t1", 0)], "inProgress")], { activeTurnId: "t1" }),
      {
        method: "evener/thread/modelRetry",
        params: {
          threadId: THREAD_ID,
          ref: REF,
          attempt: 1,
          maxAttempts: 3,
          delayMs: 1000,
          groupElapsedMs: 10,
          attemptCap: 3,
        },
      } as AnyNotification,
      NOW,
    );
    expect(model.modelRetry).toBeDefined();
    const next = applyNotification(model, updated([item("t1", 1, { roundId: "r1" })]), NOW);
    expect(next.modelRetry).toBeUndefined();
  });
});
