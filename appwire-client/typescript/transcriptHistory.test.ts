import { expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "./model";
import { applyNotification } from "./reducer";
import { itemAt, turnAt } from "./testing/modelAccessors";
import {
  applyHistory,
  CAPABILITIES,
  hydrateWithShaImage,
  positionedFragmentTurn,
  SHA_IMAGE,
  testEscalation,
  testHydrate,
  testHydrateV6,
  testThread,
} from "./testing/reducerTestKit";
import {
  applyHistoryReadFailure,
  applyReadModel,
  applyReadResponse,
  frontierFromCursor,
  hydrateThread,
  issueLatestWindowRead,
  mergeOlderItemPage,
  mergeSavedViewPage,
  mergeTranscriptItemVersioned,
  mergeTranscriptTurnVersioned,
  olderFrontierOf,
} from "./transcriptHistory";
import { markItemTextOmitted } from "./transcriptItems";
import type { OverlayItem, ThreadHistoryTurnsListResponse, ThreadReadResponse, Turn } from "./types.gen";

// A v6 read response for the standard test thread. `olderCursor` absent is a
// source read that named no older history; a value is the opaque continuation.
function historyRead(olderCursor?: string): ThreadReadResponse {
  return {
    thread: testThread(),
    bootGeneration: "",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 1 },
    ...(olderCursor === undefined ? {} : { olderCursor }),
  } as ThreadReadResponse;
}

function normalHistoryPage(length: number, data: Turn[]): ThreadHistoryTurnsListResponse {
  return {
    data,
    bootGeneration: "",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length },
    servedFrom: "live-daemon",
    instanceId: "daemon",
    asOf: "2026-11-08T00:00:00Z",
    nextCursor: "older-page",
  };
}

function pageOrderingModel(): ThreadModel {
  return hydrateThread(
    {
      ...historyRead("latest-cursor"),
      thread: testThread({ turns: [positionedFragmentTurn("latest", [["latest", 90]])] }),
      snapshot: { incarnation: "inc-1", length: 100 },
    },
    "ref_t",
    1000,
  );
}

test("reverse-order normal pages retain a newer tool result without advancing latest completeness", () => {
  const toolTurn = (version: number): Turn => ({
    id: "tool-turn",
    status: "completed",
    version,
    itemsView: "fragment",
    items: [
      {
        id: "tool",
        turnId: "tool-turn",
        type: "commandExecution",
        callId: "call",
        status: "completed",
        output: `output-${version}`,
        version,
        position: { entry: 10, item: 0 },
      },
    ],
  });
  const held = mergeOlderItemPage(pageOrderingModel(), normalHistoryPage(120, [toolTurn(120)]));
  expect(held.history?.length).toBe(100);
  expect(held.turns.flatMap((turn) => turn.items).find((item) => item.id === "tool")).toMatchObject({
    version: 120,
    output: "output-120",
  });
  const late = mergeOlderItemPage(held, normalHistoryPage(110, [toolTurn(110)]));
  expect(late.history?.length).toBe(100);
  expect(late.turns.flatMap((turn) => turn.items).find((item) => item.id === "tool")).toMatchObject({
    version: 120,
    output: "output-120",
  });
});

test("an older overlapping page cannot resurrect an omission made by a newer normal page", () => {
  const oldTurn = positionedFragmentTurn("paged", [
    ["left", 10],
    ["omitted", 15],
    ["right", 20],
  ]);
  const newerTurn = positionedFragmentTurn("paged", [
    ["left", 10],
    ["right", 20],
  ]);
  const seeded = mergeOlderItemPage(pageOrderingModel(), normalHistoryPage(110, [oldTurn]));
  expect(seeded.turns.flatMap((turn) => turn.items).map((item) => item.id)).toContain("omitted");
  const replaced = mergeOlderItemPage(seeded, normalHistoryPage(120, [newerTurn]));
  expect(replaced.turns.flatMap((turn) => turn.items).map((item) => item.id)).not.toContain("omitted");
  const late = mergeOlderItemPage(replaced, normalHistoryPage(110, [oldTurn]));
  expect(late.history?.length).toBe(100);
  expect(late.turns.flatMap((turn) => turn.items).map((item) => item.id)).not.toContain("omitted");
});

test("a lower-snapshot disjoint normal page remains usable after a newer page", () => {
  const newer = mergeOlderItemPage(
    pageOrderingModel(),
    normalHistoryPage(120, [positionedFragmentTurn("first-page", [["first", 10]])]),
  );
  const disjoint = mergeOlderItemPage(
    newer,
    normalHistoryPage(110, [positionedFragmentTurn("second-page", [["second", 30]])]),
  );
  expect(disjoint.history?.length).toBe(100);
  expect(disjoint.turns.flatMap((turn) => turn.items).map((item) => item.id)).toEqual(["first", "second", "latest"]);
});

// Item versions are entry ordinals; a response's snapshot length is transcript
// bytes. A bounded read therefore states its own omission boundary with the
// highest entry ordinal it returned, never with that byte count.
function versionedFragmentTurn(entries: number[]): Turn {
  const turn = positionedFragmentTurn(
    "paged",
    entries.map((entry) => [`entry-${entry}`, entry] as const),
  );
  return { ...turn, items: (turn.items ?? []).map((item) => ({ ...item, version: item.position?.entry })) };
}

function boundedTailRead(length: number, entries: number[]) {
  return {
    ...historyRead("read-cursor"),
    thread: testThread({ turns: [versionedFragmentTurn(entries)] }),
    snapshot: { incarnation: "inc-1", length },
    servedFrom: "live-daemon",
    asOf: "2026-11-08T00:00:00Z",
  };
}

test("a bounded read preserves a held item recorded after the entries it returned", () => {
  const held = mergeOlderItemPage(
    pageOrderingModel(),
    normalHistoryPage(100, [versionedFragmentTurn([5, 6, 7, 8, 12])]),
  );
  const next = applyReadResponse(held, boundedTailRead(4_000, [5, 7, 8]), 1001);
  const ids = next.turns.flatMap((turn) => turn.items).map((item) => item.id);
  expect(ids).toContain("entry-12");
  // The control: entry 6 is inside the entries the read covered and was not
  // returned, so it is a genuine omission and must stay removed.
  expect(ids).not.toContain("entry-6");
});

// A saved fallback window speaks for the saved index, not for the live
// continuation. When the held model still names a live cursor, a fallback that
// reaches further back must not replace it with its own exhausted frontier.
test("a disjoint fallback window keeps the held live continuation", () => {
  const live = hydrateThread(
    {
      ...historyRead("before-5"),
      thread: testThread({ turns: [versionedFragmentTurn([5, 6])] }),
      snapshot: { incarnation: "inc-1", length: 100 },
    },
    "ref_t",
    1000,
  );
  expect(live.olderCursor).toBe("before-5");
  const fallback = {
    ...historyRead(undefined),
    thread: testThread({ turns: [versionedFragmentTurn([1])] }),
    snapshot: { incarnation: "inc-1", length: 100 },
    servedFrom: "saved-index",
    fallbackReason: "live-read-failed",
    asOf: "2026-11-08T00:00:00Z",
  };
  const next = applyReadModel(live, hydrateThread(fallback, "ref_t", 1001));
  expect(next.turns.flatMap((turn) => turn.items).map((item) => item.id)).toEqual(["entry-1", "entry-5", "entry-6"]);
  expect(next.olderCursor).toBe("before-5");
  expect(olderFrontierOf(next)).toBe("before-5");
});

for (const facts of ["legacy", "unavailable"] as const) {
  for (const authoritative of [false, true]) {
    test(`${facts} read preserves its existing authoritative policy: ${authoritative}`, () => {
      const turn = (version: number): Turn => {
        const value = positionedFragmentTurn("paged", [["value", 10]], { version });
        return { ...value, items: value.items?.map((item) => ({ ...item, version })) };
      };
      const held = mergeOlderItemPage(pageOrderingModel(), normalHistoryPage(120, [turn(120)]));
      expect(held.turns.flatMap((value) => value.items).find((item) => item.id === "value")?.version).toBe(120);
      const read = {
        ...historyRead("read-cursor"),
        thread: testThread({ turns: [turn(110)] }),
        snapshot: { incarnation: "inc-1", length: 110 },
        authoritative,
        ...(facts === "unavailable" ? { servingFactsUnavailableReason: "method-unavailable" } : {}),
      };
      const next = applyReadResponse(held, read, 1001);
      expect(next.history?.length).toBe(110);
      expect(next.turns.flatMap((value) => value.items).find((item) => item.id === "value")?.version).toBe(
        authoritative ? 110 : 120,
      );
    });
  }
}

// A notification can be projected after the page that omitted one of its
// identities. The newer page's observation still wins: a recorded notification
// cannot put an omitted identity back.
test("a saved page cannot resurrect a newer page omission", () => {
  const replaced = mergeOlderItemPage(
    mergeOlderItemPage(
      pageOrderingModel(),
      normalHistoryPage(110, [
        positionedFragmentTurn("paged", [
          ["left", 10],
          ["omitted", 15],
          ["right", 20],
        ]),
      ]),
    ),
    normalHistoryPage(120, [
      positionedFragmentTurn("paged", [
        ["left", 10],
        ["right", 20],
      ]),
    ]),
  );
  expect(replaced.turns.flatMap((turn) => turn.items).map((item) => item.id)).not.toContain("omitted");
  const saved = mergeSavedViewPage(replaced, {
    bootGeneration: "",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 110 },
    data: [
      positionedFragmentTurn("paged", [
        ["left", 10],
        ["omitted", 15],
        ["right", 20],
      ]),
    ],
    nextCursor: "saved-page",
  } as ThreadHistoryTurnsListResponse);
  expect(saved.turns.flatMap((turn) => turn.items).map((item) => item.id)).not.toContain("omitted");
});

test("a delayed recorded notification cannot resurrect a newer page omission", () => {
  const replaced = mergeOlderItemPage(
    mergeOlderItemPage(
      pageOrderingModel(),
      normalHistoryPage(110, [
        positionedFragmentTurn("paged", [
          ["left", 10],
          ["omitted", 15],
          ["right", 20],
        ]),
      ]),
    ),
    normalHistoryPage(120, [
      positionedFragmentTurn("paged", [
        ["left", 10],
        ["right", 20],
      ]),
    ]),
  );
  expect(replaced.turns.flatMap((turn) => turn.items).map((item) => item.id)).not.toContain("omitted");
  const updated = applyNotification(
    replaced,
    {
      method: "history/updated",
      params: {
        ref: "ref_t",
        threadId: "thr_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 110 },
        items: [
          {
            id: "omitted",
            turnId: "paged",
            type: "agentMessage",
            text: "omitted",
            position: { entry: 15, item: 0 },
            version: 15,
          },
        ],
      },
    },
    1002,
  );
  expect(updated.turns.flatMap((turn) => turn.items).map((item) => item.id)).not.toContain("omitted");
});

// A delayed fallback carries whatever the saved index held when the live read
// failed, including an item a newer live page has since omitted. The newer
// page's observation still wins; the fallback cannot resurrect it.
for (const entryPoint of ["response", "model"] as const) {
  test(`${entryPoint} fallback containing an omitted key cannot resurrect it`, () => {
    const omittedPage = normalHistoryPage(110, [
      positionedFragmentTurn("paged", [
        ["left", 10],
        ["omitted", 15],
        ["right", 20],
      ]),
    ]);
    const replaced = mergeOlderItemPage(
      mergeOlderItemPage(pageOrderingModel(), omittedPage),
      normalHistoryPage(120, [
        positionedFragmentTurn("paged", [
          ["left", 10],
          ["right", 20],
        ]),
      ]),
    );
    expect(replaced.turns.flatMap((turn) => turn.items).map((item) => item.id)).not.toContain("omitted");
    const read = {
      ...historyRead("read-cursor"),
      thread: testThread({
        turns: [
          positionedFragmentTurn("paged", [
            ["left", 10],
            ["omitted", 15],
            ["right", 20],
          ]),
        ],
      }),
      snapshot: { incarnation: "inc-1", length: 110 },
      servedFrom: "saved-index",
      fallbackReason: "live-read-failed",
      asOf: "2026-11-08T00:00:00Z",
    };
    const next =
      entryPoint === "response"
        ? applyReadResponse(replaced, read, 1001)
        : applyReadModel(replaced, hydrateThread(read, "ref_t", 1001));
    expect(next.turns.flatMap((turn) => turn.items).map((item) => item.id)).not.toContain("omitted");
  });
}

for (const entryPoint of ["response", "model"] as const) {
  for (const fallback of [false, true]) {
    test(`${entryPoint} ${fallback ? "fallback" : "normal"} reads retain newer page omissions`, () => {
      const oldPage = normalHistoryPage(110, [
        positionedFragmentTurn("paged", [
          ["left", 10],
          ["omitted", 15],
          ["right", 20],
        ]),
      ]);
      const replaced = mergeOlderItemPage(
        mergeOlderItemPage(pageOrderingModel(), oldPage),
        normalHistoryPage(120, [
          positionedFragmentTurn("paged", [
            ["left", 10],
            ["right", 20],
          ]),
        ]),
      );
      const read = {
        ...historyRead("read-cursor"),
        thread: testThread({ turns: [positionedFragmentTurn("latest", [["latest", 90]])] }),
        snapshot: { incarnation: "inc-1", length: 110 },
        servedFrom: fallback ? "saved-index" : "live-daemon",
        ...(fallback ? { fallbackReason: "live-read-failed" } : {}),
        asOf: "2026-11-08T00:00:00Z",
      };
      const refreshed =
        entryPoint === "response"
          ? applyReadResponse(replaced, read, 1001)
          : applyReadModel(replaced, hydrateThread(read, "ref_t", 1001));
      expect(refreshed.history?.length).toBe(fallback ? 100 : 110);
      const late = mergeOlderItemPage(refreshed, oldPage);
      expect(late.turns.flatMap((turn) => turn.items).map((item) => item.id)).not.toContain("omitted");
    });
  }

  test(`${entryPoint} normal read cannot undo a newer overlapping page observation`, () => {
    const newer = mergeOlderItemPage(
      pageOrderingModel(),
      normalHistoryPage(120, [
        positionedFragmentTurn("paged", [
          ["left", 10],
          ["right", 20],
        ]),
      ]),
    );
    const read = {
      ...historyRead("read-cursor"),
      thread: testThread({
        turns: [
          positionedFragmentTurn("paged", [
            ["left", 10],
            ["omitted", 15],
            ["right", 20],
          ]),
        ],
      }),
      snapshot: { incarnation: "inc-1", length: 110 },
      servedFrom: "live-daemon",
      asOf: "2026-11-08T00:00:00Z",
    };
    const refreshed =
      entryPoint === "response"
        ? applyReadResponse(newer, read, 1001)
        : applyReadModel(newer, hydrateThread(read, "ref_t", 1001));
    expect(refreshed.history?.length).toBe(110);
    expect(refreshed.turns.flatMap((turn) => turn.items).map((item) => item.id)).not.toContain("omitted");
  });
}

for (const recovery of ["invalidated", "failed"] as const) {
  test(`accepted saved fallback recovers ${recovery} history and resumes live notifications`, () => {
    const latest = positionedFragmentTurn("turn_latest", [["held-newer", 9]], { version: 9 });
    const read = {
      ...historyRead("latest-cursor"),
      thread: testThread({ turns: [latest] }),
      snapshot: { incarnation: "inc-1", length: 9 },
      servedFrom: "live-daemon",
      asOf: "2026-11-08T00:00:00Z",
    };
    let held = hydrateThread(read, "ref_t", 1000);
    held = mergeOlderItemPage(held, {
      data: [positionedFragmentTurn("turn_older", [["held-older", 1]])],
      bootGeneration: "",
      epoch: 1,
      snapshot: read.snapshot,
      nextCursor: "held-older-cursor",
    });
    const originalIdentity = held.history;
    const inFlight = issueLatestWindowRead(held);
    held = applyNotification(
      inFlight.model,
      {
        method: "history/updated",
        params: {
          threadId: "thr_t",
          ref: "ref_t",
          bootGeneration: "",
          epoch: 1,
          snapshot: { incarnation: "inc-pending", length: 10 },
          items: [],
        },
      },
      1001,
    );
    expect(held.history).toMatchObject({
      invalidatedAtGeneration: inFlight.requestGeneration,
      awaited: { bootGeneration: "", epoch: 1 },
      pendingIncarnation: "inc-pending",
    });
    if (recovery === "failed") {
      const failing = issueLatestWindowRead(held);
      held = applyHistoryReadFailure(failing.model, "HISTORY_FAILURE_SENTINEL", failing.requestGeneration);
      expect(held.history?.failed).toBeDefined();
    }
    const issued = issueLatestWindowRead(held);
    const fallback = {
      ...historyRead(),
      thread: testThread({ turns: [positionedFragmentTurn("turn_latest", [["saved-only", 8]])] }),
      bootGeneration: "",
      epoch: 0,
      snapshot: { incarnation: "saved-inc", length: 8 },
      servedFrom: "saved-index",
      fallbackReason: "live-read-failed",
      asOf: "2026-11-08T00:00:01Z",
    };
    expect(applyReadResponse(issued.model, { ...fallback, requestGeneration: inFlight.requestGeneration }, 1002)).toBe(
      issued.model,
    );
    let recovered = applyReadResponse(issued.model, { ...fallback, requestGeneration: issued.requestGeneration }, 1003);
    expect(recovered.history?.turns.flatMap((turn) => turn.items.map((item) => item.id))).toEqual([
      "held-older",
      "saved-only",
      "held-newer",
    ]);
    expect(recovered.olderCursor).toBe("held-older-cursor");
    expect(recovered.olderFrontier).toBe("held-older-cursor");
    expect(recovered.history).toMatchObject({
      bootGeneration: originalIdentity?.bootGeneration,
      epoch: originalIdentity?.epoch,
      incarnation: originalIdentity?.incarnation,
      length: originalIdentity?.length,
      appliedGeneration: issued.requestGeneration,
      issuedGeneration: issued.requestGeneration,
    });
    recovered = applyHistory(
      recovered,
      {
        snapshot: { incarnation: "inc-1", length: 10 },
        items: [{ id: "live-recorded", turnId: "turn_latest", type: "agentMessage", version: 10 }],
      },
      1004,
    );
    expect(recovered.history?.turns.flatMap((turn) => turn.items.map((item) => item.id))).toContain("live-recorded");
    const overlay: OverlayItem = {
      key: "stream:recovered",
      kind: "stream",
      turnId: "turn_latest",
      item: { id: "live-overlay", turnId: "turn_latest", type: "agentMessage", text: "OVERLAY_" },
    };
    recovered = applyNotification(
      recovered,
      { method: "overlay/upserted", params: { threadId: "thr_t", ref: "ref_t", item: overlay } },
      1005,
    );
    recovered = applyNotification(
      recovered,
      {
        method: "overlay/delta",
        params: { threadId: "thr_t", ref: "ref_t", key: overlay.key, field: "text", delta: "DELTA" },
      },
      1006,
    );
    expect(recovered.turns.flatMap((turn) => turn.items).find((item) => item.id === "live-overlay")?.text).toBe(
      "OVERLAY_DELTA",
    );
    for (const field of ["invalidatedAtGeneration", "awaited", "pendingIncarnation", "failed"]) {
      expect(recovered.history).not.toHaveProperty(field);
    }
  });
}

test("hydration omits unavailable serving facts from the model shape", () => {
  for (const response of [historyRead(), { thread: testThread() }]) {
    expect(hydrateThread(response, "ref_t", 1000)).not.toHaveProperty("servingFacts");
  }
});

for (const path of ["wire", "hydrated"] as const) {
  test(`${path} refresh clears previous serving facts when the new read has none`, () => {
    const live = { ...historyRead(), servedFrom: "live-daemon", asOf: "2026-11-08T00:00:00Z" };
    const held = hydrateThread(live, "ref_t", 1000);
    expect(held.servingFacts?.servedFrom).toBe("live-daemon");
    const response = { ...historyRead(), epoch: 2, requestGeneration: 1 };
    const refreshed =
      path === "wire"
        ? applyReadResponse(held, response, 1001)
        : applyReadModel(held, hydrateThread(response, "ref_t", 1001));
    expect(refreshed).not.toHaveProperty("servingFacts");
    expect(held.servingFacts?.servedFrom).toBe("live-daemon");
  });
}

test("hydrateThread records a real read's older-frontier provenance (#5585)", () => {
  const cursorless = hydrateThread(historyRead(), "ref_t", 1000);
  expect(cursorless.olderCursor).toBeUndefined();
  // A source read with no cursor names the end of older history; it is not
  // the same state as a cache seed that never asked.
  expect(cursorless.olderFrontier).toBe("none");

  const windowed = hydrateThread(historyRead("cursor-1"), "ref_t", 1000);
  expect(windowed.olderCursor).toBe("cursor-1");
  expect(windowed.olderFrontier).toBe("cursor-1");
});

test("a model that never recorded a frontier is unknown, never source-named none (#5585)", () => {
  expect(olderFrontierOf({})).toBe("unknown");
  expect(olderFrontierOf({ olderFrontier: "none" })).toBe("none");
  expect(frontierFromCursor({})).toBe("none");
  expect(frontierFromCursor({ olderCursor: "c9" })).toBe("c9");
});

test("versioned entry merge rejects older input and preserves richer equal-version text", () => {
  const held: ItemModel = { id: "item-1", turnId: "turn-1", type: "assistant", text: "known text" };
  const stale: ItemModel = { ...held, text: "stale text" };
  const omitted = markItemTextOmitted({ ...held, text: "" });

  expect(mergeTranscriptItemVersioned(held, 4, stale, 3)).toBe(held);
  expect(mergeTranscriptItemVersioned(held, 4, omitted, 4)).toMatchObject({ text: "known text" });
});

test("versioned turn merge rejects older and equal metadata without copying item arrays", () => {
  const item: ItemModel = { id: "item-1", turnId: "turn-1", type: "assistant", text: "body" };
  const held: TurnModel = { id: "turn-1", status: "completed", version: 8, items: [item] };
  expect(mergeTranscriptTurnVersioned(held, { ...held, status: "stale", version: 7, items: [] })).toBe(held);
  expect(mergeTranscriptTurnVersioned(held, { ...held, status: "equal", items: [] })).toBe(held);
  const newer = mergeTranscriptTurnVersioned(held, { ...held, status: "new", version: 9, items: [] });
  expect(newer).toMatchObject({ status: "new", version: 9 });
  expect(newer.items).toBe(held.items);
});

test("hydrateThread carries the snapshot plugin diagnostics into ThreadModel", () => {
  const model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: {},
      diagnostics: {
        plugins: [
          { name: "enabled", skillCount: 1, agentCount: 0, hookCount: 0, mcpCount: 0 },
          { name: "another", skillCount: 0, agentCount: 1, hookCount: 0, mcpCount: 0 },
        ],
      },
    },
  });

  expect(model.diagnostics?.plugins?.map((plugin) => plugin.name)).toEqual(["enabled", "another"]);
});

test("hydrateThread preserves an explicit empty plugin inventory", () => {
  const model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: {},
      diagnostics: { plugins: [] },
    },
  });

  expect(model.diagnostics?.plugins).toEqual([]);
});

test("hydrateThread leaves diagnostics unavailable when the wire omits them", () => {
  expect(testHydrate().diagnostics).toBeUndefined();
});

test("hydrateThread retains and copies the owning command inventory, including authoritative empty", () => {
  const commands = [{ name: "project-only", source: "project", description: "owner" }];
  const model = testHydrate({ evener: { diagnostics: { commands } } });
  expect(model.diagnostics?.commands).toEqual(commands);
  commands[0]!.description = "changed source";
  expect(model.diagnostics?.commands?.[0]?.description).toBe("owner");
  expect(testHydrate({ evener: { diagnostics: { commands: [] } } }).diagnostics?.commands).toEqual([]);
  expect(testHydrate({ evener: { diagnostics: {} } }).diagnostics?.commands).toBeUndefined();
});

test("hydrateThread retains canonical skill descriptors", () => {
  const model = testHydrate({
    evener: {
      diagnostics: {
        skills: [
          {
            name: "plugin:simplify",
            description: "rewrite",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  });
  expect(model.skills).toEqual([
    {
      name: "plugin:simplify",
      description: "rewrite",
      disableModelInvocation: false,
      userInvocable: true,
      available: true,
    },
  ]);
});

test("hydrateThread defaults missing skills and copies wire descriptors", () => {
  expect(testHydrate().skills).toEqual([]);

  const skills = [
    {
      name: "plugin:simplify",
      description: "rewrite",
      disableModelInvocation: false,
      userInvocable: true,
      available: true,
    },
  ];
  const model = testHydrate({ evener: { diagnostics: { skills } } });
  expect(model.skills).not.toBe(skills);
  expect(model.skills?.[0]).not.toBe(skills[0]);
});

test("stable delegate diagnostics preserve lossless fields and omit call-scoped wait reasons", () => {
  const thread = testThread();
  (thread.evener as unknown as Record<string, unknown>).diagnostics = {
    delegates: [
      {
        delegateId: "dlg_lossless",
        ownerSessionId: "sess_t",
        rootSessionId: "sess_t",
        childSessionId: "sess_child",
        transcriptRef: "local:sess_child",
        parentDelegateId: "dlg_parent",
        type: "delegate",
        lifecycle: "idle",
        phase: "idle",
        status: "idle",
        outcome: "exhausted",
        reason: "turn_budget_exhausted",
        terminal: true,
        resumable: false,
        notResumableReason: "turn_budget_exhausted",
        projectionRevision: 9,
        task: "Inspect the repo",
        description: "repository inspection",
        agentType: "explorer",
        requestedModel: "fast",
        resolvedProfileId: "anthropic",
        resolvedModel: "claude-sonnet",
        model: "anthropic/claude-sonnet",
        reasoningEffort: "high",
        originTurnId: "turn_1",
        originToolCallId: "call_1",
        originItemId: "item_1",
        runStartedAt: "2026-08-15T10:00:00Z",
        runEndedAt: "2026-08-15T10:01:00Z",
        latestActivityAt: "2026-08-15T10:00:59Z",
        runningForMs: null,
        quietForMs: 1000,
        durationMs: 60000,
        packetKind: "communicate",
        message: null,
        structuredResult: null,
        structuredResultValid: true,
        structuredResultReason: "explicit null",
        warnings: ["warning one"],
        diagnostics: ["observer armed"],
        exhaustionBudget: "turns",
        exhaustionLimit: 12,
        exhaustionResumable: false,
        delegationAllowance: 2,
        parentWatchGranted: true,
        usage: { inputTokens: 41, outputTokens: 7, cacheReadTokens: 3, totalTokens: 48 },
        worktree: { path: "/tmp/wt", branch: "delegate/dlg_lossless", headSha: "abc", ahead: 2, dirty: true },
        waitIgnoredReason: "must never enter stable state",
      },
    ],
    turnSlots: { inUse: 1, cap: 4, jobs: 2, driveTurns: 1 },
  };

  const model = hydrateThread({ thread }, thread.evener.ref, 1000) as ThreadModel & {
    delegates?: Array<Record<string, unknown>>;
    turnSlots?: Record<string, unknown>;
  };

  expect(model.delegates).toHaveLength(1);
  expect(model.delegates?.[0]).toMatchObject({
    delegateId: "dlg_lossless",
    message: null,
    structuredResult: null,
    structuredResultValid: true,
    structuredResultReason: "explicit null",
    exhaustionBudget: "turns",
    exhaustionLimit: 12,
    exhaustionResumable: false,
    runningForMs: null,
    quietForMs: 1000,
    durationMs: 60000,
    usage: { inputTokens: 41, outputTokens: 7, cacheReadTokens: 3, totalTokens: 48 },
    worktree: { path: "/tmp/wt", branch: "delegate/dlg_lossless", headSha: "abc", ahead: 2, dirty: true },
    warnings: ["warning one"],
    diagnostics: ["observer armed"],
    delegationAllowance: 2,
    parentWatchGranted: true,
  });
  expect(model.delegates?.[0]).not.toHaveProperty("waitIgnoredReason");
  expect(model.turnSlots).toEqual({ inUse: 1, cap: 4, jobs: 2, driveTurns: 1 });
});

// transcriptEntryIndex is the item's 1-based position in the parent
// transcript's entry list (appwire.ThreadItem.TranscriptEntryIndex), and it is
// the ONLY field that names a fork divergence position: thread/fork's
// sourceTurnId is read as that index, while a LIVE turn id is numbered off a
// different counter entirely. The model must therefore carry it through
// verbatim rather than leaving renderers to reach for turnId.
test("wire items carry transcriptEntryIndex into the model - it is what thread/fork's divergence position is read from", () => {
  let model = testHydrateV6();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_2", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
      items: [
        {
          ...{
            type: "userMessage",
            id: "item_user",
            turnId: "turn_2",
            transcriptEntryIndex: 5,
            text: "second task",
            status: "completed",
          },
          turnId: "turn_2",
        },
      ],
    },
    1002,
  );
  expect(itemAt(turnAt(model, 0), 0).transcriptEntryIndex).toBe(5);

  // Absent on the wire stays absent in the model: a missing index means "this
  // entry has no persisted transcript position", never entry 0.
  const hydrated = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
            items: [{ id: "item_a", turnId: "turn_1", type: "userMessage", text: "hello" }],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );
  expect(itemAt(turnAt(hydrated, 0), 0).transcriptEntryIndex).toBeUndefined();
});

test("hydrateThread resolves a sha-bearing image without a stamped url to the serving session's image route", () => {
  const model = hydrateWithShaImage();
  expect(model.imageSessionId).toBe("sess_t");
  expect(itemAt(turnAt(model, 0), 0).images).toEqual([
    { src: `/s/sess_t/images/${SHA_IMAGE}`, name: "photo.png", path: undefined },
  ]);
});

test("hydrateThread trims a whitespace-padded sessionId and falls back to the trimmed thread id", () => {
  // stampThreadImageURLs (output_images.go) trims both (strings.TrimSpace):
  // a padded-but-blank "  " session id must fall back to the thread id, and
  // a clean thread id must survive — neither may escape to a /s/%20... route.
  const blankPadded = testHydrate({ id: "thr_t", sessionId: "   " });
  expect(blankPadded.imageSessionId).toBe("thr_t");
  const padded = testHydrate({ id: "  thr_t  ", sessionId: "  sess_t  " });
  expect(padded.imageSessionId).toBe("sess_t");
});

// A hydrate reads an empty input-images list the same way a live frame does: as
// absence. The wire rarely sends one — `appwire.ThreadItem.Images` is
// `json:",omitempty"` and the producers send nil — but a real fixture does
// (`fixtures/tool-and-jobs.jsonl:4`), and folding it to absent is what keeps an
// older page's images from being erased on merge.
test("an empty input-images list on the wire leaves the item's images unset", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "default",
        items: [
          {
            id: "user-item",
            turnId: "turn_1",
            type: "userMessage",
            text: "look",
            status: "completed",
            images: [],
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).images).toBeUndefined();
});

test("hydrateThread maps a settled item's error onto the model (snapshot path)", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "commandExecution",
            id: "item_tool",
            turnId: "turn_1",
            toolName: "run_tests",
            callId: "call_1",
            error: "exit status 1",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).error).toBe("exit status 1");
});

test("hydrateThread maps a settled item's exitCode onto the model (snapshot path)", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "commandExecution",
            id: "item_tool",
            turnId: "turn_1",
            toolName: "shell",
            callId: "call_1",
            output: "ok",
            exitCode: 0,
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  // A real typed 0 must round-trip as 0, never collapse to undefined — the
  // descriptor distinguishes "ran, exit 0" from "no code (backgrounded)".
  expect(itemAt(turnAt(model, 0), 0).exitCode).toBe(0);
});

// A tool-call's intent crosses the wire as ThreadItem.description (set
// server-side, e.g. delegate's mandate); wireItemToModel historically dropped
// it. The model must carry it so the subagent Activity feed can render each
// child tool-call's intent (§4.2). Both hydrate and live paths fold through
// wireItemToModel, so the snapshot path proves the carry.
test("wireItemToModel carries the wire description (tool-call intent) onto the item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_1_0",
            type: "commandExecution",
            toolName: "delegate",
            callId: "c1",
            description: "audit the reducer",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).description).toBe("audit the reducer");
});

// systemMessage items carry a stable typed discriminator, ThreadItem.eventKind
// (appwire.ThreadItemEventKind*), naming what happened — "system_prompt",
// "compaction", etc. wireItemToModel historically dropped it, forcing the
// transcript renderer to guess scaffold items from their char count. The model
// must carry it so classification is by wire type, not a heuristic.
test("wireItemToModel carries the wire eventKind (scaffold/system discriminator) onto the item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_system",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_system_prompt",
            type: "systemMessage",
            text: "You are Evener.",
            eventKind: "system_prompt",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).eventKind).toBe("system_prompt");
});

// A system item can attach structured detail behind its prose text, e.g. a
// round_timings item's per-phase durations (ThreadItem.raw) or a
// compaction item's before/after counts. wireItemToModel historically dropped
// it, forcing a renderer to re-parse numbers out of human-readable text. The
// model must carry it so a renderer can read the real numbers instead.
test("wireItemToModel carries the wire raw (structured system-item detail) onto the item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_timings",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_round_timings_1",
            type: "systemMessage",
            text: "Round 0 total=1.5s llm=1.2s",
            eventKind: "round_timings",
            raw: { roundTimings: { round: 0, total_round_ns: 1_500_000_000, llm_call_ns: 1_200_000_000 } },
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).raw).toEqual({
    roundTimings: { round: 0, total_round_ns: 1_500_000_000, llm_call_ns: 1_200_000_000 },
  });
});

// A reloaded transcript must label a steer the same way the live one did.
// internal/apptranscript persists ThreadItem.steeringKind alongside the
// steering item (Task 2); wireItemToModel must carry it through on the
// snapshot path exactly as it does for description/eventKind/raw above, or
// the label would work live and vanish on refresh.
test("a reloaded steering item carries steeringKind from the snapshot", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_0",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_steering_0",
            type: "steering",
            text: "done",
            steeringKind: "tasks-done",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).steeringKind).toBe("tasks-done");
});

// On reload, apptranscript.TurnsFromFile mints one wire turn per transcript
// entry, so a tool CALL (assistant entry) and its RESULT (tool-results entry)
// arrive as two items sharing a callId, with different ids, in separate turns.
// The Go contract says "the client merges the two by call id"; the reducer must
// collapse them into the single item the live path already produces, and drop
// the now-empty result turn so its TurnSeparator disappears.
test("reload merges a tool CALL and its RESULT (separate turns, same callId) into one item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_1_0",
            type: "commandExecution",
            toolName: "shell",
            callId: "call_A",
            argumentsJson: JSON.stringify({ command: "make test" }),
            startedAt: 1,
            status: "inProgress",
          },
        ],
      },
      {
        id: "turn_2",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_result_2_0",
            type: "commandExecution",
            toolName: "shell",
            callId: "call_A",
            output: "ok",
            exitCode: 0,
            completedAt: 2,
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 0);

  // Exactly one tool item survives, carrying both halves.
  const items = model.turns.flatMap((t) => t.items).filter((i) => i.callId === "call_A");
  expect(items).toHaveLength(1);
  const merged = items[0];
  if (!merged) throw new Error("expected merged item");
  expect(merged.id).toBe("item_tool_1_0"); // keeps the CALL id
  expect(merged.argumentsJSON).toBe(JSON.stringify({ command: "make test" })); // from the CALL
  expect(merged.output).toBe("ok"); // from the RESULT
  expect(merged.exitCode).toBe(0); // from the RESULT
  expect(merged.status).toBe("completed"); // settled from the RESULT
  expect(merged.startedAt).toBeTruthy(); // carried from the CALL half
  expect(merged.completedAt).toBeTruthy(); // carried from the RESULT half

  // The now-empty result turn is gone, so only one turn (and one separator) remains.
  expect(model.turns).toHaveLength(1);
});

test("reload merges a tool RESULT's raw state into the CALL item (hydration preserves structured raw)", () => {
  const delegateRaw = { id: "dlg_42", type: "delegate", status: "running", task: "do work" };
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_1_0",
            type: "commandExecution",
            toolName: "job_status",
            callId: "call_B",
            argumentsJson: JSON.stringify({ target: "dlg_42" }),
            startedAt: 1,
            status: "inProgress",
          },
        ],
      },
      {
        id: "turn_2",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_result_2_0",
            type: "commandExecution",
            toolName: "job_status",
            callId: "call_B",
            output: JSON.stringify(delegateRaw),
            raw: delegateRaw,
            completedAt: 2,
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 0);
  const items = model.turns.flatMap((t) => t.items).filter((i) => i.callId === "call_B");
  expect(items).toHaveLength(1);
  expect(items[0]?.raw).toEqual(delegateRaw); // raw from the RESULT survives the merge
});

test("hydrateThread carries visionModel and defaults an absent wire value", () => {
  expect(testHydrate().visionModel).toBe("");
  expect(
    testHydrate({
      evener: {
        ref: "ref_t",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        visionModel: "anthropic/claude-haiku-4-5",
      },
    }).visionModel,
  ).toBe("anthropic/claude-haiku-4-5");
});

// S15: the session's sandbox mode and network setting ride the read, for the
// Session sheet's Access section. Absent (an older daemon or hub) stays
// absent: the phone leaves the section out rather than guess.
test("hydrateThread carries the session's access and leaves an absent one out", () => {
  expect(testHydrate().access).toBeUndefined();
  expect(
    testHydrate({
      evener: {
        ref: "ref_t",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        access: { sandbox: "workspace-write", network: false },
      },
    }).access,
  ).toEqual({ sandbox: "workspace-write", network: false });
});

// Observed reasoning timing: the wire never carries reasoning timestamps at
// all (neither the live projector nor the historical reader sets
// ThreadItem.StartedAt/CompletedAt for reasoning items), so the reducer
// stamps its own client-observed arrival times from `now` — see
// ItemModel.observedStartedAt/observedCompletedAt's doc comment in
// model.ts and the reducer's appendReasoningDelta/mergeObservedTiming/
// settleItem comments for the full rationale.

test("hydrated items never carry observed timing fields", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [{ type: "reasoning", id: "item_r", turnId: "turn_1", text: "done thinking", status: "completed" }],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.observedStartedAt).toBeUndefined();
  expect(item.observedCompletedAt).toBeUndefined();
});

// pendingEscalations (M7): appwire/types_thread.go's ThreadEvener.PendingEscalations
// doc comment calls it the "surface-on-entry snapshot ... so a client
// entering / reconnecting to / not-having-seen-live this session surfaces
// the card(s)" and rules it a HUMAN-CLIENT field only, never part of the
// transcript. hydrateThread must therefore carry it verbatim (or default it
// to [], per the Go wire-nullable-array rule: omitempty absent means empty)
// as a THREAD-level ThreadModel field, not a turn item.

test("hydrateThread maps evener.pendingEscalations verbatim into pendingEscalations", () => {
  const escalation = testEscalation();
  const model = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });
  expect(model.pendingEscalations).toEqual([escalation]);
});

// EvenerThread.kind is how a past read names a fork child. The token
// fallback reads it to avoid charging the child's inherited prefix.
test("hydrateThread carries the wire's thread kind", () => {
  expect(testHydrate({ evener: { kind: "fork" } }).kind).toBe("fork");
  expect(testHydrate().kind).toBeUndefined();
});

test("hydrateThread defaults pendingEscalations to an empty array when evener.pendingEscalations is absent", () => {
  const model = testHydrate();
  expect(model.pendingEscalations).toEqual([]);
});

// EvenerThread.Cost is the session-level estimated dollar total (the sibling of
// per-turn Turn.Cost), snapshot-authoritative like usage/workMillis: only a
// wire snapshot (hydrateThread) sets it, and everything else preserves it via
// the reducer's ...model spread. It is null when the daemon omits it (no usage,
// or an uncataloged model) — an honest "unknown" the status row renders as no
// chip, never a misleading ~$0.00.
test("hydrateThread maps evener.cost into the model, null when absent", () => {
  const withCost = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, cost: "~$1.23" },
  });
  expect(withCost.cost).toBe("~$1.23");

  expect(testHydrate().cost).toBeNull();
});

test("hydrateThread keeps absent task aggregate null and distinguishes an authoritative zero", () => {
  expect(testHydrate().tasks).toBeNull();
  expect(
    testHydrate({
      evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, tasks: { total: 0, done: 0 } },
    }).tasks,
  ).toEqual({ total: 0, done: 0 });
});

// Wave 5 T1: ThreadModel gains capabilities/goal/context*/usage/workMillis/
// activeTurnStartedAt/reasoningEffortLevels/supportsReasoning, all hydrated
// from thread.evener (appwire/types_thread.go's EvenerThread). None of
// these except reasoningEffortLevels/supportsReasoning (via
// thread/model/changed, tested above) and reasoningEffort (via
// thread/reasoning-effort/changed, tested above) ever get a live push - see
// EvenerThread's own doc comment ("read on demand ... rather than pushed on
// every event") and appwire/protocol.go's Notifications catalog, which has
// no capabilities/goal/context/usage entry at all.
test("hydrateThread maps capabilities/goal/context*/usage/workMillis/activeTurnStartedAt/reasoningEffortLevels/supportsReasoning verbatim from thread.evener", () => {
  const model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      goal: { status: "active", iterations: 2 },
      contextUsed: 12_000,
      contextWindow: 200_000,
      contextPressure: 0.06,
      contextAssumed: true,
      usage: { inputTokens: 100, outputTokens: 50, cacheReadTokens: 10, totalTokens: 160 },
      workMillis: 45_000,
      activeTurnStartedAt: 1_000,
      reasoningEffortLevels: ["low", "medium", "high"],
      supportsReasoning: true,
    },
  });

  expect(model.capabilities).toEqual(CAPABILITIES);
  expect(model.goal).toEqual({ status: "active", iterations: 2 });
  expect(model.contextUsed).toBe(12_000);
  expect(model.contextWindow).toBe(200_000);
  expect(model.contextPressure).toBe(0.06);
  expect(model.contextAssumed).toBe(true);
  // Absent on the wire — a known window, or an older daemon — reads as a
  // real window, never as an assumption.
  const unassumed = testHydrate({ evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 } } });
  expect(unassumed.contextAssumed).toBe(false);
  expect(model.usage).toEqual({ inputTokens: 100, outputTokens: 50, cacheReadTokens: 10, totalTokens: 160 });
  expect(model.workMillis).toBe(45_000);
  expect(model.activeTurnStartedAt).toBe(new Date(1_000).toISOString());
  expect(model.reasoningEffortLevels).toEqual(["low", "medium", "high"]);
  expect(model.supportsReasoning).toBe(true);
});

test("hydrateThread defaults the wave 5 snapshot-only fields when thread.evener omits them (old daemon / source-backed thread)", () => {
  const model = testHydrate(); // testThread()'s default evener carries none of these

  expect(model.goal).toBeNull();
  expect(model.contextUsed).toBe(0);
  expect(model.contextWindow).toBe(0);
  expect(model.contextPressure).toBe(0);
  expect(model.usage).toBeNull();
  expect(model.workMillis).toBe(0);
  expect(model.activeTurnStartedAt).toBeUndefined();
  expect(model.reasoningEffortLevels).toEqual([]);
  expect(model.supportsReasoning).toBe(false);
});

// Thread.createdAt/updatedAt are top-level wire fields in Unix SECONDS
// (hubcore.UnixSeconds for a past session, entry.StartedAt.Unix() for a live
// one), converted to the model's ISO-string convention like every other
// timestamp on ThreadModel. The session-details panel is what reads them.
test("hydrateThread maps the thread's created/updated wire seconds to ISO instants", () => {
  const model = testHydrate({ createdAt: 1_784_829_766, updatedAt: 1_784_872_877 });

  expect(model.createdAt).toBe(new Date(1_784_829_766_000).toISOString());
  expect(model.updatedAt).toBe(new Date(1_784_872_877_000).toISOString());
});

test("hydrateThread treats Go's zero created/updated stamps as absent rather than 1970", () => {
  const model = testHydrate({ createdAt: 0, updatedAt: 0 });

  expect(model.createdAt).toBeUndefined();
  expect(model.updatedAt).toBeUndefined();
});

test("hydrateThread preserves clientMutationId on authoritative transcript items", () => {
  const model = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
            items: [
              {
                id: "item_a",
                turnId: "turn_1",
                type: "userMessage",
                text: "hello",
                clientMutationId: "mutation-a",
              },
            ],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );

  expect(model.turns[0]?.items[0]).toMatchObject({ clientMutationId: "mutation-a" });
});

test("hydrateThread maps humanNote/agentNote/sessionUrls from thread.evener", () => {
  const model = testHydrate({
    evener: {
      humanNote: "human hello",
      agentNote: "agent hello",
      sessionUrls: [{ id: "u1", url: "https://x.test/y", label: "x", addedBy: "agent", addedAt: 7 }],
    },
  });
  expect(model.humanNote).toBe("human hello");
  expect(model.agentNote).toBe("agent hello");
  expect(model.sessionUrls).toEqual([{ id: "u1", url: "https://x.test/y", label: "x", addedBy: "agent", addedAt: 7 }]);
});

test("hydrateThread defaults notes/urls when thread.evener omits them (old daemon / source-backed thread)", () => {
  const model = testHydrate();
  expect(model.humanNote).toBe("");
  expect(model.agentNote).toBe("");
  expect(model.sessionUrls).toEqual([]);
});

// The notes write gate keys off the recovery fence, which rides the snapshot
// as thread.evener.resumeRequired (absent means not fenced). Without this the
// model cannot tell a fenced live+idle session from an editable one, since the
// SharedNotes capability is deliberately retained for reading.
test("hydrateThread carries the recovery fence from thread.evener.resumeRequired", () => {
  expect(testHydrate().resumeRequired).toBe(false);
  expect(testHydrate({ evener: { resumeRequired: true } }).resumeRequired).toBe(true);
});

// The hub's own admission answer, carried straight from the wire. It is what
// the client now keys Send on, so the model must mirror it (an absent wire
// value means not foldable, never a guess).
test("hydrateThread carries the resume-only foldable bit from thread.evener.resumeOnlyFoldable", () => {
  expect(testHydrate().resumeOnlyFoldable).toBe(false);
  expect(testHydrate({ evener: { resumeOnlyFoldable: true } }).resumeOnlyFoldable).toBe(true);
});

// The artifacts directory is snapshot state: a fresh read that withdraws it
// must clear the model's value, not keep the previous one. Fields merge over
// the existing model, so an omitted key would leave file-link actions aimed at
// a directory the hub no longer advertises.
test("applyReadModel also clears a withdrawn artifactsDir", () => {
  const withRoot = testHydrate({ evener: { artifactsDir: "/state/a/artifacts" } });
  const withdrawn = applyReadModel(withRoot, testHydrate({ evener: {} }));
  expect(withdrawn.artifactsDir).toBeUndefined();
});

test("hydrateThread clears artifactsDir when a snapshot omits it", () => {
  const withRoot = testHydrate({ evener: { artifactsDir: "/state/a/artifacts" } });
  expect(withRoot.artifactsDir).toBe("/state/a/artifacts");
  // A fresh snapshot for the same thread that no longer advertises a root.
  const wire = testThread({ evener: {} });
  const withdrawn = applyReadResponse(
    withRoot,
    {
      thread: wire,
      requestGeneration: 1,
    } as ThreadReadResponse,
    2000,
  );
  expect(withdrawn.artifactsDir).toBeUndefined();
});

// The scratch directory is snapshot state with the same withdraw rule as the
// artifacts directory: it must reach the model from a read, and a fresh read
// that withdraws it must clear it, not keep the previous one.
test("a read carries scratchDir into the model and a withdrawn root clears it", () => {
  expect(testHydrate({ evener: {} }).scratchDir).toBeUndefined();
  const withRoot = testHydrate({ evener: { scratchDir: "/tmp/evener-scratch-a/a" } });
  expect(withRoot.scratchDir).toBe("/tmp/evener-scratch-a/a");
  expect(applyReadModel(withRoot, testHydrate({ evener: {} })).scratchDir).toBeUndefined();
  const wire = testThread({ evener: {} });
  const withdrawn = applyReadResponse(
    withRoot,
    {
      thread: wire,
      requestGeneration: 1,
    } as ThreadReadResponse,
    2000,
  );
  expect(withdrawn.scratchDir).toBeUndefined();
});
