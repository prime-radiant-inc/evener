import { expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "./model";
import { itemAt, turnAt } from "./testing/modelAccessors";
import {
  applyHistory,
  fragmentTurn,
  hydrateWithShaImage,
  positionedFragmentTurn,
  SHA_IMAGE,
  testHydrate,
  testHydrateV6,
  testThread,
} from "./testing/reducerTestKit";
import { hydrateThread, mergeOlderItemPage, prependOlderTurns } from "./transcriptHistory";
import { markItemTextOmitted } from "./transcriptItems";
import { mergeTurnHistory, mergeTurnHistoryWithFolds } from "./turnHistoryMerge";
import type { ThreadItem, ThreadTurnsListResponse, Turn } from "./types.gen";

function toolWireTurn(turnId: string, itemId: string, callId: string, overrides: Partial<ThreadItem> = {}): Turn {
  return {
    id: turnId,
    status: "completed",
    itemsView: "full",
    items: [
      {
        id: itemId,
        turnId,
        type: "commandExecution",
        toolName: "shell",
        callId,
        ...overrides,
      },
    ],
  };
}

function toolModelTurn(turnId: string, item: Partial<ItemModel> & Pick<ItemModel, "id" | "callId">): TurnModel {
  const { id, callId, ...overrides } = item;
  return {
    id: turnId,
    status: "completed",
    items: [
      {
        ...overrides,
        id,
        turnId,
        type: overrides.type ?? "commandExecution",
        text: overrides.text ?? "",
        toolName: overrides.toolName ?? "shell",
        callId,
      },
    ],
  };
}

test("a sha+bytes image with no known session keeps the data-URI (unknown-session fallback)", () => {
  // Blank wire ids name no fetchable route (imageSessionRoute undefined keeps
  // the branch dark), so the same sha+bytes payload that resolves to a route
  // above resolves to the usable bytes here instead of a broken src.
  const model = hydrateWithShaImage({});
  const wireModel = { ...model, imageSessionId: "", threadId: "" };
  const page: ThreadTurnsListResponse = {
    data: [
      {
        id: "turn_0",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "userMessage",
            id: "item_paged",
            turnId: "turn_0",
            text: "older",
            status: "completed",
            images: [
              {
                type: "image",
                name: "photo.png",
                mediaType: "image/png",
                data: "iVBORw0KGgo=",
                metadata: { sha: SHA_IMAGE },
              },
            ],
          },
        ],
      },
    ],
    nextCursor: undefined,
  };
  const paged = mergeOlderItemPage(wireModel, page);
  expect(itemAt(turnAt(paged, 0), 0).images).toEqual([
    { src: "data:image/png;base64,iVBORw0KGgo=", name: "photo.png", path: undefined },
  ]);
});

// Task 1-3 carried a typed kind (events.SteeringKind* on the Go side) onto
// the wire at each injection site, through to EvenerSteeringInjectedParams.kind
// on the live notification. The model must carry it the last hop onto the
// item so the transcript can label a steer from the wire kind instead of
// pattern-matching its prose (a later task's job — this one only carries the
// string).

test("prependOlderTurns keeps order and advances olderCursor", () => {
  const thread = testThread({ turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);

  const resp: ThreadTurnsListResponse = {
    data: [
      { id: "turn_0", status: "completed", itemsView: "full", items: [] },
      { id: "turn_1", status: "completed", itemsView: "full", items: [] },
    ],
    nextCursor: "cursor_0",
  };
  const result = prependOlderTurns(model, resp);

  expect(result.turns.map((t) => t.id)).toEqual(["turn_0", "turn_1", "turn_2"]);
  expect(result.olderCursor).toBe("cursor_0");
});

// ThreadTurnsListResponse.data is typed as a required Turn[], but that's a
// TS-side promise, not a wire guarantee - mirrors the same defense
// hydrateThread/wireToTurnModel already apply to thread.turns/turn.items
// (both `?? []`). A response missing `data` entirely (the Go zero value for
// a nil slice marshals as JSON `null`, not `[]`) must not crash and must
// behave as "an empty page" rather than losing the turns already in model.
test("prependOlderTurns tolerates a wire-nullable data array (treats it as an empty page)", () => {
  const thread = testThread({ turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);

  const resp = { nextCursor: "cursor_0" } as unknown as ThreadTurnsListResponse;
  const result = prependOlderTurns(model, resp);

  expect(result.turns.map((t) => t.id)).toEqual(["turn_2"]);
  expect(result.olderCursor).toBe("cursor_0");
});

test("mergeOlderItemPage keeps a fresh completed call ahead of an older standalone result", () => {
  const model = testHydrate({
    turns: [
      toolWireTurn("fresh-call", "item_tool_1_0", "call_A", {
        output: "fresh output",
        status: "completed",
        completedAt: 20,
      }),
    ],
  });

  const merged = mergeOlderItemPage(model, {
    data: [toolWireTurn("old-result", "item_tool_result_0_0", "call_A", { output: "old output", completedAt: 10 })],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call_A");

  expect(item).toMatchObject({
    id: "item_tool_1_0",
    output: "fresh output",
    status: "completed",
    completedAt: new Date(20).toISOString(),
  });
});

test("mergeOlderItemPage fills absent fresh call fields from an older result without replacing fresh status", () => {
  const model = testHydrate({
    turns: [toolWireTurn("fresh-call", "item_tool_1_0", "call_B", { status: "inProgress", startedAt: 20 })],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("old-result", "item_tool_result_0_0", "call_B", {
        output: "older output",
        status: "completed",
        completedAt: 10,
        exitCode: 3,
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call_B");

  expect(item).toMatchObject({
    output: "older output",
    status: "inProgress",
    completedAt: new Date(10).toISOString(),
    exitCode: 3,
  });
});

test("mergeOlderItemPage lets a fresh result settle an older call", () => {
  const model = testHydrate({
    turns: [
      toolWireTurn("fresh-result", "item_tool_result_2_0", "call_C", {
        output: "fresh result",
        status: "completed",
        completedAt: 20,
      }),
    ],
  });

  const merged = mergeOlderItemPage(model, {
    data: [toolWireTurn("old-call", "item_tool_1_0", "call_C", { status: "inProgress", startedAt: 10 })],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call_C");

  expect(item).toMatchObject({
    id: "item_tool_1_0",
    output: "fresh result",
    status: "completed",
    completedAt: new Date(20).toISOString(),
    startedAt: new Date(10).toISOString(),
  });
});

test("mergeOlderItemPage prefers the fresh result even when an older result is visited later", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("fresh-call", { id: "item_tool_1_0", callId: "call_D", status: "inProgress" }),
    toolModelTurn("fresh-result", {
      id: "item_tool_result_2_0",
      callId: "call_D",
      output: "fresh result",
      status: "completed",
      completedAt: new Date(20).toISOString(),
    }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("old-result", "item_tool_result_3_0", "call_D", {
        output: "old result",
        status: "completed",
        completedAt: 10,
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call_D");

  expect(item).toMatchObject({ output: "fresh result", completedAt: new Date(20).toISOString() });
});

test("mergeOlderItemPage preserves an older empty metadata turn beside an unrelated result", () => {
  const model = testHydrate({
    turns: [toolWireTurn("fresh-result", "item_tool_result_2_0", "unrelated-call", { output: "fresh" })],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      {
        id: "older-empty",
        status: "completed",
        itemsView: "full",
        startedAt: 10,
        completedAt: 20,
        durationMs: 10,
        usage: { inputTokens: 3 },
        cost: "0.03",
        error: { message: "older turn error" },
        items: [],
      },
    ],
  });

  expect(merged.turns.map((turn) => turn.id)).toEqual(["older-empty", "fresh-result"]);
  expect(merged.turns[0]).toMatchObject({
    startedAt: new Date(10).toISOString(),
    completedAt: new Date(20).toISOString(),
    durationMs: 10,
    usage: { inputTokens: 3 },
    cost: "0.03",
    error: { message: "older turn error" },
  });
});

test("mergeOlderItemPage preserves a fresh empty metadata turn beside an unrelated older result", () => {
  const model = testHydrate({
    turns: [
      {
        id: "fresh-empty",
        status: "completed",
        itemsView: "full",
        items: [],
        startedAt: 30,
        completedAt: 40,
        durationMs: 10,
        usage: { outputTokens: 4 },
        cost: "0.04",
        error: { message: "fresh turn error" },
      },
    ],
  });

  const merged = mergeOlderItemPage(model, {
    data: [toolWireTurn("older-result", "item_tool_result_0_0", "unrelated-call", { output: "old" })],
  });

  expect(merged.turns.map((turn) => turn.id)).toEqual(["older-result", "fresh-empty"]);
  expect(merged.turns[1]).toMatchObject({
    startedAt: new Date(30).toISOString(),
    completedAt: new Date(40).toISOString(),
    durationMs: 10,
    usage: { outputTokens: 4 },
    cost: "0.04",
    error: { message: "fresh turn error" },
  });
});

test("mergeOlderItemPage preserves metadata on a consumed result-only turn", () => {
  const model = testHydrate({
    turns: [toolWireTurn("fresh-call", "item_tool_1_0", "call-with-metadata", { status: "inProgress" })],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      {
        id: "older-result",
        status: "completed",
        itemsView: "full",
        startedAt: 50,
        completedAt: 60,
        durationMs: 10,
        usage: { inputTokens: 5 },
        cost: "0.05",
        error: { message: "consumed turn error" },
        items: [
          {
            id: "item_tool_result_0_0",
            turnId: "older-result",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-with-metadata",
            output: "old result",
            status: "completed",
          },
        ],
      },
    ],
  });

  expect(merged.turns.map((turn) => turn.id)).toEqual(["older-result", "fresh-call"]);
  expect(merged.turns[0]).toMatchObject({
    items: [],
    startedAt: new Date(50).toISOString(),
    completedAt: new Date(60).toISOString(),
    durationMs: 10,
    usage: { inputTokens: 5 },
    cost: "0.05",
    error: { message: "consumed turn error" },
  });
  expect(merged.turns[1]?.items[0]).toMatchObject({
    id: "item_tool_1_0",
    callId: "call-with-metadata",
    toolName: "shell",
    output: "old result",
    status: "inProgress",
  });
});

test("mergeOlderItemPage drops a metadata-free consumed result-only turn", () => {
  const model = testHydrate({
    turns: [toolWireTurn("fresh-call", "item_tool_1_0", "call-without-metadata", { status: "inProgress" })],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("older-result", "item_tool_result_0_0", "call-without-metadata", {
        output: "old result",
        status: "completed",
      }),
    ],
  });

  expect(merged.turns.map((turn) => turn.id)).toEqual(["fresh-call"]);
});

// The #2213 final-verdict Medium: contribution tracking attributed text
// suppliers by strict value equality, so an explicitly provided empty text
// and an omitted text — both hydrating to "" — recorded as sharing the text
// supplier. The side that actually supplied text then held every supplier
// only together with the side that supplied none, so itemSideContributes
// denied it the claim and the page's contribution went unrecorded. The
// attribution must follow mergePageItem's own presence rule: itemTextPresence
// for text, property presence for the spread-merged fields.
test("an explicitly provided empty text claims the text supply over an omitted-text item", () => {
  // The page side: a hand-built item carries no presence marker, which the
  // package reads as "provided" — the same semantics an older/mixed producer
  // that spells text: "" gets. The retained side: the sparse wire shape,
  // which hydrates an omitted text to "" under the omitted marker.
  const pageItem: ItemModel = { id: "x1", turnId: "tp", type: "assistant", text: "" };
  const retainedItem: ItemModel = markItemTextOmitted({
    id: "x1",
    turnId: "t1",
    type: "assistant",
    text: "",
  });
  const folds = mergeTurnHistoryWithFolds(
    [{ id: "tp", status: "completed", items: [pageItem] }],
    [{ id: "t1", status: "completed", items: [retainedItem] }],
  );
  const merged = folds.turns.flatMap((turn) => turn.items).find((item) => item.id === "x1");
  expect(merged).toBeDefined();
  // The merge kept the page's text (mergePageItem's presence rule: a provided
  // text wins over an omitted one), so the page is the only side that
  // supplied it — removing the page loses the merged item's text settle.
  const pageInputs = new Set<ItemModel>([pageItem]);
  expect(folds.itemSideContributes(merged!, (input) => pageInputs.has(input))).toBe(true);
});

test("mergeOlderItemPage preserves same-source call/result folding", () => {
  const merged = mergeOlderItemPage(testHydrate(), {
    data: [
      toolWireTurn("old-call", "item_tool_1_0", "call_E", { status: "inProgress", startedAt: 10 }),
      toolWireTurn("old-result", "item_tool_result_2_0", "call_E", {
        output: "same-source result",
        status: "completed",
        completedAt: 20,
      }),
    ],
  });

  const items = merged.turns.flatMap((turn) => turn.items).filter((item) => item.callId === "call_E");
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({
    id: "item_tool_1_0",
    output: "same-source result",
    status: "completed",
    completedAt: new Date(20).toISOString(),
  });
});

test("mergeOlderItemPage keeps the last same-source result in traversal order", () => {
  const merged = mergeOlderItemPage(testHydrate(), {
    data: [
      toolWireTurn("old-call", "item_tool_1_0", "call_F", { status: "inProgress", startedAt: 10 }),
      toolWireTurn("old-result-one", "item_tool_result_2_0", "call_F", {
        output: "first result",
        status: "completed",
        completedAt: 20,
      }),
      toolWireTurn("old-result-two", "item_tool_result_3_0", "call_F", {
        output: "last result",
        status: "completed",
        completedAt: 30,
      }),
    ],
  });

  const items = merged.turns.flatMap((turn) => turn.items).filter((item) => item.callId === "call_F");
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({ output: "last result", completedAt: new Date(30).toISOString() });
});

test("mergeOlderItemPage does not promote fields inherited by a fresh result fragment", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-provenance", {
      id: "item_tool_1_0",
      callId: "call-provenance",
      output: "fresh call output",
      status: "completed",
    }),
    toolModelTurn("turn-provenance", {
      id: "item_tool_result_2_0",
      callId: "call-provenance",
      status: "completed",
    }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("turn-provenance", "item_tool_result_2_0", "call-provenance", {
        output: "older result output",
        status: "completed",
      }),
    ],
  });

  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-provenance");
  expect(item).toMatchObject({ id: "item_tool_1_0", output: "fresh call output" });
});

test("mergeOlderItemPage preserves older fields through a transitive result alias", () => {
  const model = testHydrate();
  const freshCall = toolModelTurn("fresh-turn", {
    id: "item_tool_0_0",
    callId: "call-alias",
    position: { entry: 1, item: 0 },
  });
  const freshResultA = toolModelTurn("fresh-turn", {
    id: "item_tool_result_a",
    callId: "call-alias",
    transcriptKey: "result-key",
    position: { entry: 1, item: 1 },
  });
  const freshResultB = toolModelTurn("fresh-turn", {
    id: "item_tool_result_b",
    callId: "call-alias",
    transcriptKey: "result-key",
    position: { entry: 1, item: 2 },
  });
  model.turns = [
    {
      id: "fresh-turn",
      status: "completed",
      items: [freshCall.items[0]!, freshResultA.items[0]!, freshResultB.items[0]!],
    },
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("older-result", "item_tool_result_a", "call-alias", {
        output: "older output",
        transcriptKey: undefined,
        status: "completed",
        position: { entry: 1, item: 1 },
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-alias");

  expect(item).toMatchObject({ id: "item_tool_0_0", output: "older output" });
});

test("mergeOlderItemPage routes a fresh result with an omitted callId through its resolved item", () => {
  const model = testHydrate({
    turns: [
      toolWireTurn("fresh-call", "item_tool_0_0", "call-routing"),
      toolWireTurn("fresh-result", "item_tool_result_1_0", "call-routing", {
        callId: undefined,
        output: "fresh output",
        status: "completed",
      }),
    ],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("older-result", "item_tool_result_1_0", "call-routing", {
        output: "older output",
        status: "completed",
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-routing");

  expect(merged.turns.flatMap((turn) => turn.items)).toHaveLength(1);
  expect(item).toMatchObject({ id: "item_tool_0_0", output: "fresh output" });
});

test("mergeOlderItemPage routes a fresh call with an omitted callId through its resolved item", () => {
  const model = testHydrate({
    turns: [
      toolWireTurn("fresh-call", "item_tool_0_0", "call-routing", {
        callId: undefined,
        output: "fresh output",
      }),
    ],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("older-call", "item_tool_0_0", "call-routing"),
      toolWireTurn("older-result", "item_tool_result_1_0", "call-routing", {
        output: "older output",
        status: "completed",
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-routing");

  expect(item).toMatchObject({ id: "item_tool_0_0", output: "fresh output" });
});

test("mergeOlderItemPage routes an older fallback with an omitted callId through its resolved item", () => {
  const model = testHydrate();
  const freshCall = toolModelTurn("fresh-turn", { id: "item_tool_0_0", callId: "call-routing" });
  const freshResult = toolModelTurn("fresh-result", { id: "item_tool_result_1_0", callId: "call-routing" });
  model.turns = [freshCall, freshResult];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("older-result", "item_tool_result_1_0", "call-routing", {
        callId: undefined,
        output: "older output",
        status: "completed",
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-routing");

  expect(item).toMatchObject({ id: "item_tool_0_0", output: "older output" });
});

function countIdentityReadsForMerge(count: number, includeTool: boolean): number {
  let reads = 0;
  const items = Array.from({ length: count }, (_, index) => {
    const item: ItemModel = {
      id: `message-${index}`,
      turnId: "fresh",
      type: "agentMessage",
      text: "",
    };
    Object.defineProperty(item, "id", {
      configurable: true,
      enumerable: true,
      get: () => {
        reads += 1;
        return `message-${index}`;
      },
    });
    return item;
  });
  if (includeTool) {
    items.push(
      {
        id: "item_tool_0_0",
        turnId: "fresh",
        type: "commandExecution",
        text: "",
        callId: "cost-call",
      },
      {
        id: "item_tool_result_1_0",
        turnId: "fresh",
        type: "commandExecution",
        text: "",
        callId: "cost-call",
        output: "done",
        status: "completed",
      },
    );
  }
  const model = testHydrate();
  model.turns = [{ id: "fresh", status: "completed", items }];
  mergeOlderItemPage(model, { data: [] });
  return reads;
}

test("mergeOlderItemPage keeps no-tool provenance work linear as history grows", () => {
  const smaller = countIdentityReadsForMerge(8, false);
  const larger = countIdentityReadsForMerge(16, false);
  expect(larger).toBeLessThanOrEqual(smaller * 3 + 16);
});

test("mergeOlderItemPage keeps one-call provenance work linear as history grows", () => {
  const smaller = countIdentityReadsForMerge(20, true);
  const larger = countIdentityReadsForMerge(40, true);
  expect(larger).toBeLessThanOrEqual(smaller * 3 + 40);
});

function countActiveToolCandidateReads(callCount: number): { reads: number; items: ItemModel[] } {
  let reads = 0;
  const charged = (id: string, overrides: Partial<ItemModel>): ItemModel => {
    const item: ItemModel = {
      id,
      turnId: "fresh",
      type: "commandExecution",
      text: "",
      toolName: "shell",
      ...overrides,
    };
    Object.defineProperty(item, "id", {
      configurable: true,
      enumerable: true,
      get: () => {
        reads += 1;
        return id;
      },
    });
    return item;
  };
  const items: ItemModel[] = [];
  for (let index = 0; index < callCount; index += 1) {
    const callId = `cost-active-${index}`;
    // The call carries a provisional output of its own; the result below
    // supersedes it (fresh results precede fresh calls in preferredToolField),
    // so the merged item's output is a real field-precedence assertion, not
    // just a filled-in blank.
    items.push(charged(`item_tool_${index}_0`, { callId, output: `call-output-${index}` }));
    items.push(charged(`item_tool_result_${index}_0`, { callId, output: `output-${index}`, status: "completed" }));
  }
  const model = testHydrate();
  model.turns = [{ id: "fresh", status: "completed", items }];
  const merged = mergeOlderItemPage(model, { data: [] });
  return { reads, items: merged.turns.flatMap((turn) => turn.items) };
}

// The sibling guards above instrument ordinary (agentMessage) item ids, so they
// pin only work done BEFORE the `if (!item.callId) continue` guard: a regression
// that scans the active call/result candidates themselves — the work after that
// guard — never touches an ordinary item's id and would be invisible to them.
// This fixture grows the active candidate set (N then 2N genuine call/result
// pairs, the shape the live path produces) with every call and result id
// instrumented, so a superlinear scan of those candidates shows up as a
// superlinear read count.
//
// Measured on the linear collector: 20 active calls -> 180 reads, 40 -> 360
// (9 reads per call: exactly 2x work for 2x the candidates). The pre-#1982
// quadratic candidate collector (9747f949) measured 1820 -> 6840 on this same
// fixture (3.76x). The 3x + 40 bound below therefore fails the quadratic scan
// by a wide margin while leaving a linear collector ~1.5x of headroom; it is
// deterministic, with no elapsed-time threshold.
test("mergeOlderItemPage keeps growing active tool-call candidates linear, folding each result", () => {
  const smaller = countActiveToolCandidateReads(20);
  const larger = countActiveToolCandidateReads(40);

  // The counter must actually observe candidate identity reads, not zero.
  expect(smaller.reads).toBeGreaterThan(0);

  // The fold is real, so the count above is taken over a merge that did the
  // provenance work: one surviving item per call, each carrying its RESULT's
  // output (precedence over the call's own) and status, and no standalone
  // result left behind.
  expect(larger.items).toHaveLength(40);
  expect(larger.items.some((item) => item.id.startsWith("item_tool_result_"))).toBe(false);
  for (const item of larger.items) {
    expect(item.callId).toBeDefined();
    expect(item.output).toBe(`output-${item.callId?.slice("cost-active-".length)}`);
    expect(item.status).toBe("completed");
  }

  expect(larger.reads).toBeLessThanOrEqual(smaller.reads * 3 + 40);
});

test("mergeOlderItemPage preserves older fallback fields across distinct result fragments", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-fallback", { id: "item_tool_1_0", callId: "call-fallback" }),
    toolModelTurn("turn-fallback", { id: "item_tool_result_2_0", callId: "call-fallback" }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("turn-fallback", "item_tool_result_0_0", "call-fallback", {
        output: "older output",
        error: "older error",
        prevalOnly: false,
        exitCode: 0,
        completedAt: 10,
        status: "completed",
        outputImages: [{ source: "test", url: "older-image" }],
        raw: { source: "older" },
      }),
    ],
  });

  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-fallback");
  expect(item).toMatchObject({
    output: "older output",
    error: "older error",
    prevalOnly: false,
    exitCode: 0,
    completedAt: new Date(10).toISOString(),
    status: "completed",
    outputImages: [{ src: "older-image" }],
    raw: { source: "older" },
  });
});

test("mergeOlderItemPage keeps every defined fresh call field ahead of an older result", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-fields-call", {
      id: "item_tool_1_0",
      callId: "call-fields-call",
      output: "fresh output",
      error: "fresh error",
      prevalOnly: true,
      exitCode: 7,
      completedAt: new Date(20).toISOString(),
      status: "inProgress",
      outputImages: [{ src: "fresh-image" }],
      raw: { source: "fresh" },
    }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("turn-fields-call", "item_tool_result_0_0", "call-fields-call", {
        output: "older output",
        error: "older error",
        prevalOnly: false,
        exitCode: 3,
        completedAt: 10,
        status: "completed",
        outputImages: [{ source: "test", url: "older-image" }],
        raw: { source: "older" },
      }),
    ],
  });

  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-fields-call");
  expect(item).toMatchObject({
    output: "fresh output",
    error: "fresh error",
    prevalOnly: true,
    exitCode: 7,
    completedAt: new Date(20).toISOString(),
    status: "inProgress",
    outputImages: [{ src: "fresh-image" }],
    raw: { source: "fresh" },
  });
});

test("mergeOlderItemPage keeps every defined fresh result field ahead of an older call", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-fields-result", {
      id: "item_tool_result_2_0",
      callId: "call-fields-result",
      output: "fresh output",
      error: "fresh error",
      prevalOnly: true,
      exitCode: 7,
      completedAt: new Date(20).toISOString(),
      status: "completed",
      outputImages: [{ src: "fresh-image" }],
      raw: { source: "fresh" },
    }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("turn-fields-result", "item_tool_1_0", "call-fields-result", {
        output: "older output",
        error: "older error",
        prevalOnly: false,
        exitCode: 3,
        completedAt: 10,
        status: "inProgress",
        outputImages: [{ source: "test", url: "older-image" }],
        raw: { source: "older" },
      }),
    ],
  });

  const item = merged.turns
    .flatMap((turn) => turn.items)
    .find((candidate) => candidate.callId === "call-fields-result");
  expect(item).toMatchObject({
    id: "item_tool_1_0",
    output: "fresh output",
    error: "fresh error",
    prevalOnly: true,
    exitCode: 7,
    completedAt: new Date(20).toISOString(),
    status: "completed",
    outputImages: [{ src: "fresh-image" }],
    raw: { source: "fresh" },
  });
});

test("mergeOlderItemPage combines duplicate fresh result fragments without older fills", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-duplicate-fresh", { id: "item_tool_1_0", callId: "call-duplicate-fresh" }),
    toolModelTurn("turn-duplicate-fresh", {
      id: "item_tool_result_2_0",
      callId: "call-duplicate-fresh",
      output: "fresh output",
    }),
    toolModelTurn("turn-duplicate-fresh", {
      id: "item_tool_result_2_0",
      callId: "call-duplicate-fresh",
      error: "fresh error",
    }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("turn-duplicate-fresh", "item_tool_result_0_0", "call-duplicate-fresh", {
        output: "older output",
        error: "older error",
        raw: { source: "older" },
      }),
    ],
  });

  const item = merged.turns
    .flatMap((turn) => turn.items)
    .find((candidate) => candidate.callId === "call-duplicate-fresh");
  expect(item).toMatchObject({ output: "fresh output", error: "fresh error", raw: { source: "older" } });
});

test("mergeOlderItemPage keeps normalized same-source result traversal for older fragments", () => {
  const merged = mergeOlderItemPage(testHydrate(), {
    data: [
      toolWireTurn("turn-order-old", "item_tool_1_0", "call-order-old", { position: { entry: 1, item: 0 } }),
      toolWireTurn("turn-order-old", "item_tool_result_3_0", "call-order-old", {
        position: { entry: 1, item: 2 },
        output: "later",
      }),
      toolWireTurn("turn-order-old", "item_tool_result_2_0", "call-order-old", {
        position: { entry: 1, item: 1 },
        output: "earlier",
      }),
    ],
  });

  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "later" });
});

test("mergeOlderItemPage keeps normalized fresh-only result traversal on an empty page", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-order-fresh", {
      id: "item_tool_1_0",
      callId: "call-order-fresh",
      position: { entry: 1, item: 0 },
    }),
    toolModelTurn("turn-order-fresh", {
      id: "item_tool_result_3_0",
      callId: "call-order-fresh",
      position: { entry: 1, item: 2 },
      output: "later",
    }),
    toolModelTurn("turn-order-fresh", {
      id: "item_tool_result_2_0",
      callId: "call-order-fresh",
      position: { entry: 1, item: 1 },
      output: "earlier",
    }),
  ];

  const merged = mergeOlderItemPage(model, { data: [] });
  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "later" });
});

test("mergeOlderItemPage keeps normalized fresh traversal when an older bridge joins fragments", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("fresh-call", {
      id: "item_tool_1_0",
      callId: "call-order-bridge",
      position: { entry: 1, item: 0 },
    }),
    toolModelTurn("fresh-late", {
      id: "item_tool_result_3_0",
      callId: "call-order-bridge",
      position: { entry: 1, item: 2 },
      output: "later",
    }),
    toolModelTurn("fresh-early", {
      id: "item_tool_result_2_0",
      callId: "call-order-bridge",
      position: { entry: 1, item: 1 },
      output: "earlier",
    }),
  ];

  const bridge = {
    id: "old-bridge",
    status: "completed" as const,
    itemsView: "full" as const,
    items: [
      {
        id: "item_tool_1_0",
        turnId: "old-bridge",
        type: "commandExecution",
        callId: "call-order-bridge",
        position: { entry: 1, item: 0 },
      },
      {
        id: "item_tool_result_3_0",
        turnId: "old-bridge",
        type: "commandExecution",
        callId: "call-order-bridge",
        position: { entry: 1, item: 2 },
        output: "older late",
      },
      {
        id: "item_tool_result_2_0",
        turnId: "old-bridge",
        type: "commandExecution",
        callId: "call-order-bridge",
        position: { entry: 1, item: 1 },
        output: "older early",
      },
    ],
  };

  const merged = mergeOlderItemPage(model, { data: [bridge] });
  expect(merged.turns.flatMap((turn) => turn.items).find((item) => item.callId === "call-order-bridge")).toMatchObject({
    id: "item_tool_1_0",
    output: "later",
  });
});

test("mergeTurnHistory keeps a fresh completed call ahead of an older partial result", () => {
  const merged = mergeTurnHistory(
    [
      toolModelTurn("turn-public-A", {
        id: "item_tool_result_0_0",
        callId: "call-public-A",
        output: "older output",
        completedAt: new Date(10).toISOString(),
      }),
    ],
    [
      toolModelTurn("turn-public-A", {
        id: "item_tool_1_0",
        callId: "call-public-A",
        output: "fresh output",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items[0]).toMatchObject({
    id: "item_tool_1_0",
    output: "fresh output",
    status: "completed",
    completedAt: new Date(20).toISOString(),
  });
  expect(merged.olderCoverage).toBe(false);
});

test("mergeTurnHistory keeps a fresh result ahead of an older call", () => {
  const merged = mergeTurnHistory(
    [
      toolModelTurn("turn-public-B", {
        id: "item_tool_1_0",
        callId: "call-public-B",
        status: "inProgress",
        startedAt: new Date(10).toISOString(),
      }),
    ],
    [
      toolModelTurn("turn-public-B", {
        id: "item_tool_result_2_0",
        callId: "call-public-B",
        output: "fresh result",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items[0]).toMatchObject({
    id: "item_tool_1_0",
    output: "fresh result",
    status: "completed",
    completedAt: new Date(20).toISOString(),
    startedAt: new Date(10).toISOString(),
  });
  expect(merged.olderCoverage).toBe(true);
});

test("mergeTurnHistory counts an older result field the fresh call does not supply", () => {
  const merged = mergeTurnHistory(
    [
      toolModelTurn("turn-public-C", {
        id: "item_tool_result_0_0",
        callId: "call-public-C",
        output: "older output",
        exitCode: 0,
      }),
    ],
    [
      toolModelTurn("turn-public-C", {
        id: "item_tool_1_0",
        callId: "call-public-C",
        output: "fresh output",
        status: "completed",
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "fresh output", exitCode: 0 });
});

test("mergeTurnHistory does not count a result a fresh call in another turn supersedes", () => {
  const merged = mergeTurnHistory(
    [
      toolModelTurn("turn-older-result", {
        id: "item_tool_result_0_0",
        callId: "call-cross-turn",
        output: "older output",
        completedAt: new Date(10).toISOString(),
      }),
    ],
    [
      toolModelTurn("turn-fresh-call", {
        id: "item_tool_1_0",
        callId: "call-cross-turn",
        output: "fresh output",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "fresh output" });
});

test("mergeTurnHistory counts a consumed shell's preserved usage as coverage", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-older-result",
        status: "completed",
        usage: { inputTokens: 30, outputTokens: 4 },
        items: [
          {
            id: "item_tool_result_0_0",
            turnId: "turn-older-result",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-cross-turn",
            text: "",
            output: "older output",
            status: "completed",
            completedAt: new Date(10).toISOString(),
          },
        ],
      },
    ],
    [
      toolModelTurn("turn-fresh-call", {
        id: "item_tool_1_0",
        callId: "call-cross-turn",
        output: "fresh output",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-older-result", "turn-fresh-call"]);
  expect(merged.turns[0]?.items).toHaveLength(0);
  expect(merged.turns[0]?.usage).toEqual({ inputTokens: 30, outputTokens: 4 });
  expect(merged.turns[1]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "fresh output" });
  expect(merged.turns[1]?.usage).toBeUndefined();
});

test("mergeTurnHistory does not count fallback fields on a fold-discarded matched result", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-matched",
        status: "completed",
        items: [
          {
            id: "item_tool_result_1",
            turnId: "turn-matched",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-matched",
            text: "",
            output: "older output",
            startedAt: "2026-09-19T10:00:00.000Z",
            status: "completed",
            completedAt: new Date(10).toISOString(),
          },
        ],
      },
    ],
    [
      {
        id: "turn-matched",
        status: "completed",
        items: [
          {
            id: "item_tool_1",
            turnId: "turn-matched",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-matched",
            text: "",
            output: "fresh output",
            status: "completed",
            completedAt: new Date(20).toISOString(),
          },
          {
            id: "item_tool_result_1",
            turnId: "turn-matched",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-matched",
            text: "",
            status: "completed",
            completedAt: new Date(20).toISOString(),
          },
        ],
      },
    ],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns).toHaveLength(1);
  const items = merged.turns[0]?.items ?? [];
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({ id: "item_tool_1", output: "fresh output" });
  expect(items[0]?.startedAt).toBeUndefined();
  expect(items.map((item) => item.id)).not.toContain("item_tool_result_1");
});

test("mergeTurnHistory counts surviving result output beside a consumed shell's preserved usage", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-older-result",
        status: "completed",
        usage: { inputTokens: 30, outputTokens: 4 },
        items: [
          {
            id: "item_tool_result_0_0",
            turnId: "turn-older-result",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-cross-turn",
            text: "",
            output: "older output",
            status: "completed",
            completedAt: new Date(10).toISOString(),
          },
        ],
      },
    ],
    [
      toolModelTurn("turn-fresh-call", {
        id: "item_tool_1_0",
        callId: "call-cross-turn",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-older-result", "turn-fresh-call"]);
  expect(merged.turns[0]?.items).toHaveLength(0);
  expect(merged.turns[0]?.usage).toEqual({ inputTokens: 30, outputTokens: 4 });
  expect(merged.turns[1]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "older output" });
  expect(merged.turns[1]?.usage).toBeUndefined();
});

test("mergeTurnHistory counts surviving output despite a folded result's discarded text", () => {
  let model = testHydrateV6({
    turns: [{ id: "turn-live", status: "inProgress", itemsView: "full", items: [] }],
  });
  const startItem = (item: ThreadItem, at: number): ThreadModel =>
    applyHistory(
      model,
      {
        items: [{ ...item, turnId: "turn-live" }],
      },
      at,
    );
  // Wire items without text land with omitted text presence, and the live path
  // appends both without folding the result into its call.
  model = startItem(
    { type: "commandExecution", id: "item_tool_1", turnId: "turn-live", toolName: "shell", callId: "call-text" },
    2000,
  );
  model = startItem(
    {
      type: "commandExecution",
      id: "item_tool_result_1",
      turnId: "turn-live",
      toolName: "shell",
      callId: "call-text",
      status: "completed",
    },
    2100,
  );

  const merged = mergeTurnHistory(
    [
      {
        id: "turn-page",
        status: "completed",
        items: [
          {
            id: "item_tool_result_1",
            turnId: "turn-page",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-text",
            text: "older settled text",
            output: "older output",
            status: "completed",
          },
        ],
      },
    ],
    model.turns,
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns).toHaveLength(1);
  const items = merged.turns[0]?.items ?? [];
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({ id: "item_tool_1", output: "older output" });
  expect(items[0]?.text).toBe("");
});

test("mergeTurnHistory counts an older result whose only call coalesced into a fresh result", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-call",
        status: "completed",
        items: [
          {
            id: "item_tool_1",
            turnId: "turn-call",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-z",
            transcriptKey: "shared-key",
            text: "",
            argumentsJSON: "same args",
          },
        ],
      },
      {
        id: "turn-page",
        status: "completed",
        items: [
          {
            id: "item_tool_result_2",
            turnId: "turn-page",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-z",
            text: "",
            output: "older output",
            status: "completed",
            completedAt: new Date(10).toISOString(),
          },
        ],
      },
    ],
    [
      {
        id: "turn-fresh",
        status: "completed",
        items: [
          {
            id: "item_tool_result_9",
            turnId: "turn-fresh",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-z",
            transcriptKey: "shared-key",
            text: "",
            argumentsJSON: "same args",
            output: "fresh output",
            status: "completed",
            completedAt: new Date(20).toISOString(),
          },
        ],
      },
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-fresh", "turn-page"]);
  expect(merged.turns[1]?.items[0]).toMatchObject({ id: "item_tool_result_2", output: "older output" });
});

test("mergeTurnHistory does not count an older field a fresh alias chain replaces", () => {
  const newer: TurnModel[] = [
    {
      id: "turn-f1",
      status: "completed",
      items: [
        {
          id: "a",
          turnId: "turn-f1",
          type: "agentMessage",
          text: "",
          transcriptKey: "k",
        },
      ],
    },
    {
      id: "turn-f2",
      status: "completed",
      items: [
        {
          id: "b",
          turnId: "turn-f2",
          type: "agentMessage",
          text: "",
          transcriptKey: "k",
          output: "fresh",
        },
      ],
    },
  ];
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-old",
        status: "completed",
        items: [
          {
            id: "a",
            turnId: "turn-old",
            type: "agentMessage",
            text: "",
            output: "old",
          },
        ],
      },
    ],
    newer,
  );

  expect(merged.turns).toBe(newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
});

test("mergeTurnHistory does not count a field a later older fragment clears", () => {
  const newer: TurnModel[] = [
    {
      id: "turn-f",
      status: "completed",
      items: [{ id: "a", turnId: "turn-f", type: "agentMessage", text: "", status: "completed" }],
    },
  ];
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-1",
        status: "completed",
        items: [
          { id: "a", turnId: "turn-1", type: "agentMessage", text: "", status: "completed", transcriptEntryIndex: 5 },
        ],
      },
      {
        id: "turn-2",
        status: "completed",
        items: [
          {
            id: "a",
            turnId: "turn-2",
            type: "agentMessage",
            text: "",
            status: "completed",
            transcriptEntryIndex: undefined,
          },
        ],
      },
    ],
    newer,
  );

  expect(merged.turns).toBe(newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
});

test("mergeTurnHistory counts an older status the rank merge retains", () => {
  const item = { id: "a", turnId: "turn-1", type: "agentMessage", text: "same" };
  const merged = mergeTurnHistory(
    [{ id: "turn-1", status: "completed", items: [{ ...item, status: "completed" }] }],
    [{ id: "turn-1", status: "completed", items: [{ ...item, status: "inProgress" }] }],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "a", status: "completed" });
});

test("mergeTurnHistory counts a turn status the rank merge retains", () => {
  const merged = mergeTurnHistory(
    [{ id: "t", status: "completed", items: [] }],
    [{ id: "t", status: "inProgress", items: [] }],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.id).toBe("t");
  expect(merged.turns[0]?.status).toBe("completed");
});

test("mergeTurnHistory does not count an older call a coalesced result supersedes", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-call",
        status: "completed",
        items: [
          {
            id: "item_tool_1",
            turnId: "turn-call",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-x",
            transcriptKey: "shared-key",
            text: "",
            argumentsJSON: "older args",
          },
        ],
      },
      {
        id: "turn-result",
        status: "completed",
        items: [
          {
            id: "item_tool_result_2",
            turnId: "turn-result",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-x",
            transcriptKey: "shared-key",
            text: "",
            output: "older output",
            status: "completed",
            completedAt: new Date(10).toISOString(),
          },
        ],
      },
    ],
    [
      toolModelTurn("turn-fresh-call", {
        id: "item_tool_9_0",
        callId: "call-x",
        output: "fresh output",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.id).toBe("turn-fresh-call");
  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "item_tool_9_0", output: "fresh output" });
});

test("mergeTurnHistory counts a result the fold keeps when no call item exists", () => {
  const merged = mergeTurnHistory(
    [
      toolModelTurn("turn-shared", {
        id: "item_tool_result_0_0",
        callId: "call-results-only",
        output: "older output",
      }),
    ],
    [
      toolModelTurn("turn-shared", {
        id: "item_tool_result_2_0",
        callId: "call-results-only",
        output: "fresh result",
        status: "completed",
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items).toHaveLength(2);
});

test("mergeTurnHistory keeps older fallback fields and fragments while fresh fields win", () => {
  const older: TurnModel[] = [
    {
      id: "turn-old",
      status: "completed",
      usage: { inputTokens: 500, outputTokens: 20 },
      items: [
        {
          id: "old-only",
          transcriptKey: "old-only",
          turnId: "turn-old",
          type: "agentMessage",
          text: "older-only item",
          position: { entry: 1, item: 0 },
          status: "completed",
        },
        {
          id: "old-shared",
          transcriptKey: "shared-item",
          turnId: "turn-old",
          type: "agentMessage",
          text: "older text",
          position: { entry: 2, item: 0 },
          status: "completed",
        },
      ],
    },
  ];
  const newer: TurnModel[] = [
    {
      id: "turn-fresh",
      status: "completed",
      items: [
        {
          id: "fresh-shared",
          transcriptKey: "shared-item",
          turnId: "turn-fresh",
          type: "agentMessage",
          text: "fresh text",
          position: { entry: 2, item: 0 },
          status: "completed",
        },
        {
          id: "fresh-only",
          transcriptKey: "fresh-only",
          turnId: "turn-fresh",
          type: "agentMessage",
          text: "fresh-only item",
          position: { entry: 3, item: 0 },
          status: "completed",
        },
      ],
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(true);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.id).toBe("turn-fresh");
  expect(merged.turns[0]?.usage).toEqual({
    inputTokens: 500,
    outputTokens: 20,
  });
  expect(merged.turns[0]?.items.map((item) => item.transcriptKey)).toEqual(["old-only", "shared-item", "fresh-only"]);
  expect(merged.turns[0]?.items.find((item) => item.transcriptKey === "shared-item")?.text).toBe("fresh text");
});

test("mergeTurnHistory returns the fresh view when older history contributes nothing", () => {
  const newer: TurnModel[] = [{ id: "turn-1", status: "completed", items: [], usage: { inputTokens: 1 } }];
  const older: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [],
      usage: { inputTokens: 99 },
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns).toBe(newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(false);
});

test("mergeTurnHistory does not fold duplicate fresh fragments when older history contributes nothing", () => {
  const older: TurnModel[] = [{ id: "turn-1", status: "completed", items: [] }];
  const newer: TurnModel[] = [
    { id: "turn-0", status: "completed", items: [] },
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "fresh-a",
          turnId: "turn-1",
          type: "agentMessage",
          text: "a",
          status: "completed",
        },
      ],
    },
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "fresh-b",
          turnId: "turn-1",
          type: "agentMessage",
          text: "b",
          status: "completed",
        },
      ],
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns).toBe(newer);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-0", "turn-1", "turn-1"]);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(false);
});

test("mergeTurnHistory treats matching fresh fragments as one coverage window", () => {
  const older: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      usage: { inputTokens: 1 },
      items: [{ id: "item-a", turnId: "turn-1", type: "agentMessage", text: "a", status: "completed" }],
    },
  ];
  const newer: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [{ id: "item-a", turnId: "turn-1", type: "agentMessage", text: "a", status: "completed" }],
    },
    {
      id: "turn-1",
      status: "completed",
      usage: { inputTokens: 1 },
      items: [{ id: "item-b", turnId: "turn-1", type: "agentMessage", text: "b", status: "completed" }],
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns).toBe(newer);
  expect(merged.turns).toHaveLength(2);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
});

test("mergeTurnHistory checks coverage against every transitively matching fresh fragment", () => {
  const newer: TurnModel[] = [
    {
      id: "turn-a",
      status: "completed",
      items: [{ id: "item-x", turnId: "turn-a", type: "agentMessage", text: "fresh-a", status: "completed" }],
    },
    {
      id: "turn-b",
      status: "completed",
      usage: { inputTokens: 11 },
      items: [{ id: "item-x", turnId: "turn-b", type: "agentMessage", text: "fresh-b", status: "completed" }],
    },
  ];

  const merged = mergeTurnHistory(
    [{ id: "turn-a", status: "completed", usage: { inputTokens: 11 }, items: [] }],
    newer,
  );

  expect(merged.turns).toBe(newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(false);
});

test("mergeTurnHistory treats collectively supplied item fields as covered", () => {
  const newer: TurnModel[] = [
    {
      id: "turn-a",
      status: "completed",
      items: [{ id: "item-x", turnId: "turn-a", type: "agentMessage", text: "fresh-a", status: "completed" }],
    },
    {
      id: "turn-b",
      status: "completed",
      items: [
        {
          id: "item-x",
          turnId: "turn-b",
          type: "agentMessage",
          text: "fresh-b",
          output: "settled output",
          status: "completed",
        },
      ],
    },
  ];

  const merged = mergeTurnHistory(
    [
      {
        id: "turn-a",
        status: "completed",
        items: [
          {
            id: "item-x",
            turnId: "turn-a",
            type: "agentMessage",
            text: "older",
            output: "settled output",
            status: "completed",
          },
        ],
      },
    ],
    newer,
  );

  expect(merged.turns).toBe(newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
});

function orderedHistoryTurn(id: string, entry?: number): TurnModel {
  return {
    id,
    status: "completed",
    items:
      entry === undefined
        ? []
        : [
            {
              id: `item-${id}`,
              turnId: id,
              type: "agentMessage",
              text: id,
              status: "completed",
              position: { entry, item: 0 },
            },
          ],
  };
}

test.each([
  ["unmatched turn between anchors", ["A", "B", "C"], ["A", "C"], ["A", "B", "C"]],
  ["unanchored history and fresh prefix", ["A", "B", "C"], ["D", "A", "C"], ["D", "A", "B", "C"]],
  ["fresh unmatched turn wins the no-position tie", ["A", "B", "C"], ["A", "D", "C"], ["A", "D", "B", "C"]],
  ["unmatched head and tail", ["X", "A", "C", "Z"], ["A", "C"], ["X", "A", "C", "Z"]],
])("mergeTurnHistory weaves %s deterministically", (_name, olderIds, newerIds, expected) => {
  const merged = mergeTurnHistory(
    olderIds.map((id) => orderedHistoryTurn(id)),
    newerIds.map((id) => orderedHistoryTurn(id)),
  );
  expect(merged.turns.map((turn) => turn.id)).toEqual(expected);
  expect(merged.olderCoverage).toBe(true);
});

test("mergeTurnHistory uses canonical item positions to place an older run", () => {
  const merged = mergeTurnHistory(
    [orderedHistoryTurn("A", 1), orderedHistoryTurn("B", 2), orderedHistoryTurn("C", 4)],
    [orderedHistoryTurn("A", 1), orderedHistoryTurn("D", 3), orderedHistoryTurn("C", 4)],
  );

  expect(merged.turns.map((turn) => turn.id)).toEqual(["A", "B", "D", "C"]);
});

test("mergeTurnHistory keeps live-only fields and warning items out of transcript coverage", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-1",
        status: "completed",
        items: [
          {
            id: "reasoning-1",
            turnId: "turn-1",
            type: "reasoning",
            text: "same",
            status: "completed",
            pendingText: ["live delta"],
            reasoningSummaries: [["same"]],
          },
          {
            id: "warning-1",
            turnId: "turn-1",
            type: "warning",
            text: "provider warning",
            status: "completed",
            warning: { source: "provider", title: "Provider", hint: "slow" },
          },
        ],
      },
    ],
    [
      {
        id: "turn-1",
        status: "completed",
        items: [{ id: "reasoning-1", turnId: "turn-1", type: "reasoning", text: "same", status: "completed" }],
      },
    ],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns[0]?.items).toHaveLength(2);
  expect(merged.turns[0]?.items[0]?.pendingText).toEqual(["live delta"]);
  expect(merged.turns[0]?.items[0]?.reasoningSummaries).toEqual([["same"]]);
  expect(merged.turns[0]?.items[1]).toMatchObject({ type: "warning", warning: { title: "Provider" } });

  const warningOnly = mergeTurnHistory(
    [
      {
        id: "turn-warning",
        status: "completed",
        items: [
          {
            id: "warning-only",
            turnId: "turn-warning",
            type: "warning",
            text: "provider warning",
            status: "completed",
            warning: { title: "Provider" },
          },
        ],
      },
    ],
    [{ id: "turn-warning", status: "completed", items: [] }],
  );
  expect(warningOnly.olderCoverage).toBe(false);
  expect(warningOnly.turns[0]?.items).toHaveLength(1);

  const unmatchedWarning = mergeTurnHistory(
    [
      {
        id: "unmatched-warning-turn",
        status: "completed",
        items: [
          {
            id: "unmatched-warning",
            turnId: "unmatched-warning-turn",
            type: "warning",
            text: "provider warning",
            status: "completed",
            warning: { title: "Provider" },
          },
        ],
      },
    ],
    [],
  );
  expect(unmatchedWarning.olderCoverage).toBe(false);
  expect(unmatchedWarning.turns[0]?.items).toHaveLength(1);
});

test("mergeTurnHistory counts canonical fields on an unmatched warning-only turn", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "unmatched-warning-with-usage",
        status: "completed",
        usage: { inputTokens: 11, outputTokens: 4 },
        completedAt: "2026-09-19T12:00:00.000Z",
        items: [
          {
            id: "warning-with-usage",
            turnId: "unmatched-warning-with-usage",
            type: "warning",
            text: "provider warning",
            status: "completed",
            warning: { title: "Provider" },
          },
        ],
      },
    ],
    [],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns[0]?.usage).toEqual({ inputTokens: 11, outputTokens: 4 });
  expect(merged.turns[0]?.completedAt).toBe("2026-09-19T12:00:00.000Z");
  expect(merged.turns[0]?.items).toHaveLength(1);
  expect(merged.turns[0]?.items[0]).toMatchObject({ type: "warning", warning: { title: "Provider" } });
});

test("mergeTurnHistory counts older usage a fresh null does not overwrite", () => {
  const item = { id: "item-1", turnId: "turn-1", type: "agentMessage", text: "shared", status: "completed" };
  const merged = mergeTurnHistory(
    [{ id: "turn-1", status: "completed", usage: { inputTokens: 7, outputTokens: 2 }, items: [item] }],
    [{ id: "turn-1", status: "completed", usage: null, items: [{ ...item }] }],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns[0]?.usage).toEqual({ inputTokens: 7, outputTokens: 2 });
});

test("mergeTurnHistory does not count an older null as persisted coverage", () => {
  const item = { id: "item-1", turnId: "turn-1", type: "agentMessage", text: "shared", status: "completed" };
  const merged = mergeTurnHistory(
    [{ id: "turn-1", status: "completed", usage: null, items: [item] }],
    [{ id: "turn-1", status: "completed", items: [{ ...item }] }],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns[0]?.usage).toBeNull();
});

test("mergeTurnHistory counts older item raw a fresh null does not overwrite", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-1",
        status: "completed",
        items: [
          {
            id: "item-1",
            turnId: "turn-1",
            type: "agentMessage",
            text: "",
            raw: { roundTimings: { totalMs: 5 } },
            status: "completed",
          },
        ],
      },
    ],
    [
      {
        id: "turn-1",
        status: "completed",
        items: [{ id: "item-1", turnId: "turn-1", type: "agentMessage", text: "", raw: null, status: "completed" }],
      },
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns[0]?.items[0]?.raw).toEqual({ roundTimings: { totalMs: 5 } });
});

test("mergeTurnHistory does not count a spread-overwritten transcript index as coverage", () => {
  const item = { id: "item-1", turnId: "turn-1", type: "agentMessage", text: "shared", status: "completed" };
  const merged = mergeTurnHistory(
    [{ id: "turn-1", status: "completed", items: [{ ...item, transcriptEntryIndex: 5 }] }],
    [{ id: "turn-1", status: "completed", items: [{ ...item, transcriptEntryIndex: undefined }] }],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns[0]?.items[0]?.transcriptEntryIndex).toBeUndefined();
});

test("mergeTurnHistory counts older position a fresh undefined does not overwrite", () => {
  const item = { id: "item-1", turnId: "turn-1", type: "agentMessage", text: "shared", status: "completed" };
  const merged = mergeTurnHistory(
    [{ id: "turn-1", status: "completed", items: [{ ...item, position: { entry: 2, item: 0 } }] }],
    [{ id: "turn-1", status: "completed", items: [{ ...item, position: undefined }] }],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns[0]?.items[0]?.position).toEqual({ entry: 2, item: 0 });
});

test("mergeTurnHistory does not duplicate an older turn consumed by shared fresh fragments", () => {
  const sharedItem = {
    id: "shared-item",
    turnId: "turn-a",
    type: "agentMessage",
    text: "shared",
    status: "completed",
  };
  const merged = mergeTurnHistory(
    [{ id: "turn-a", status: "completed", usage: { inputTokens: 10 }, items: [] }],
    [
      { id: "turn-a", status: "completed", items: [sharedItem] },
      { id: "turn-b", status: "completed", items: [{ ...sharedItem, turnId: "turn-b" }] },
    ],
  );

  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.id).toBe("turn-b");
  expect(merged.turns[0]?.usage).toEqual({ inputTokens: 10 });
  expect(merged.turns[0]?.items).toHaveLength(1);
});

function historyTurnWithItems(id: string, itemIds: string[]): TurnModel {
  return {
    id,
    status: "completed",
    items: itemIds.map((itemId) => ({
      id: itemId,
      turnId: id,
      type: "agentMessage",
      text: itemId,
      status: "completed",
    })),
  };
}

test.each([
  ["collapsed anchor alone", ["F"], ["F", "B"]],
  ["fresh prefix", ["X", "F"], ["X", "F", "B"]],
  ["fresh suffix", ["F", "Y"], ["F", "Y", "B"]],
  ["fresh prefix and suffix", ["X", "F", "Y"], ["X", "F", "Y", "B"]],
])("mergeTurnHistory retains a run between collapsed anchors with %s", (_name, freshIds, expectedIds) => {
  const merged = mergeTurnHistory(
    [
      historyTurnWithItems("A", ["item-A"]),
      historyTurnWithItems("B", ["item-B"]),
      historyTurnWithItems("C", ["item-C"]),
    ],
    freshIds.map((id) => historyTurnWithItems(id, id === "F" ? ["item-A", "item-C"] : [])),
  );

  expect(merged.turns.map((turn) => turn.id)).toEqual(expectedIds);
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(true);
  expect(merged.turns.flatMap((turn) => turn.items).filter((item) => item.id === "item-B")).toHaveLength(1);
});

test("mergeTurnHistory retains a warning-only run between collapsed anchors", () => {
  const merged = mergeTurnHistory(
    [
      historyTurnWithItems("A", ["item-A"]),
      {
        id: "B",
        status: "completed",
        items: [{ id: "warning-B", turnId: "B", type: "warning", text: "live warning", status: "completed" }],
      },
      historyTurnWithItems("C", ["item-C"]),
    ],
    [historyTurnWithItems("F", ["item-A", "item-C"])],
  );

  expect(merged.turns.map((turn) => turn.id)).toEqual(["F", "B"]);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
  expect(merged.turns[1]?.items).toMatchObject([{ id: "warning-B", type: "warning" }]);
});

test("mergeTurnHistory retains runs across multiple collapsed anchor groups", () => {
  const merged = mergeTurnHistory(
    [
      historyTurnWithItems("A", ["item-A"]),
      historyTurnWithItems("B", ["item-B"]),
      historyTurnWithItems("C", ["item-C"]),
      historyTurnWithItems("D", ["item-D"]),
      historyTurnWithItems("E", ["item-E"]),
      historyTurnWithItems("F", ["item-F"]),
      historyTurnWithItems("G", ["item-G"]),
    ],
    [
      historyTurnWithItems("X", []),
      historyTurnWithItems("H", ["item-A", "item-C"]),
      historyTurnWithItems("I", ["item-F", "item-G"]),
      historyTurnWithItems("Y", []),
    ],
  );

  expect(merged.turns.map((turn) => turn.id)).toEqual(["X", "H", "B", "D", "E", "I", "Y"]);
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(true);
  expect(
    merged.turns
      .flatMap((turn) => turn.items)
      .map((item) => item.id)
      .sort(),
  ).toEqual(["item-A", "item-B", "item-C", "item-D", "item-E", "item-F", "item-G"].sort());
});

test("mergeTurnHistory preserves fresh ordering when its window extends before retained turns", () => {
  const older: TurnModel[] = [
    { id: "turn-1", status: "completed", items: [], usage: { inputTokens: 1 } },
    { id: "turn-2", status: "completed", items: [], usage: { inputTokens: 2 } },
  ];
  const newer: TurnModel[] = [
    { id: "turn-0", status: "completed", items: [], usage: { inputTokens: 0 } },
    { id: "turn-1", status: "completed", items: [], usage: { inputTokens: 1 } },
    { id: "turn-2", status: "completed", items: [], usage: { inputTokens: 2 } },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns).toBe(newer);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-0", "turn-1", "turn-2"]);
});

test("mergeTurnHistory preserves fresh ordering when matching older fields contribute", () => {
  const older: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      usage: { inputTokens: 500 },
      items: [
        {
          id: "shared-old",
          transcriptKey: "shared",
          turnId: "turn-1",
          type: "agentMessage",
          text: "old",
          position: { entry: 1, item: 0 },
          status: "completed",
        },
      ],
    },
    { id: "turn-2", status: "completed", items: [], usage: { inputTokens: 2 } },
  ];
  const newer: TurnModel[] = [
    { id: "turn-0", status: "completed", items: [], usage: { inputTokens: 0 } },
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "shared-new",
          transcriptKey: "shared",
          turnId: "turn-1",
          type: "agentMessage",
          text: "new",
          position: { entry: 1, item: 0 },
          status: "completed",
        },
      ],
    },
    { id: "turn-2", status: "completed", items: [], usage: { inputTokens: 2 } },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-0", "turn-1", "turn-2"]);
  expect(merged.turns[1]?.usage).toEqual({ inputTokens: 500 });
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(true);
});

test("mergeTurnHistory folds matching fresh fragments once and keeps disjoint items", () => {
  const older: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "old",
          transcriptKey: "old",
          turnId: "turn-1",
          type: "agentMessage",
          text: "old",
          status: "completed",
        },
      ],
    },
  ];
  const newer: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "fresh-a",
          transcriptKey: "fresh-a",
          turnId: "turn-1",
          type: "agentMessage",
          text: "a",
          status: "completed",
        },
      ],
    },
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "fresh-b",
          transcriptKey: "fresh-b",
          turnId: "turn-1",
          type: "agentMessage",
          text: "b",
          status: "completed",
        },
      ],
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items.map((item) => item.transcriptKey)).toEqual(["old", "fresh-a", "fresh-b"]);
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(false);
});

test("mergeTurnHistory keeps unproven disjoint turn fragments separate", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "old-fragment",
        status: "completed",
        items: [
          {
            id: "old-item",
            turnId: "old-fragment",
            type: "agentMessage",
            text: "old",
            status: "completed",
          },
        ],
      },
    ],
    [
      {
        id: "fresh-fragment",
        status: "completed",
        items: [
          {
            id: "fresh-item",
            turnId: "fresh-fragment",
            type: "agentMessage",
            text: "fresh",
            status: "completed",
          },
        ],
      },
    ],
  );

  expect(merged.turns.map((turn) => turn.id)).toEqual(["old-fragment", "fresh-fragment"]);
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(false);
});

test("mergeTurnHistory does not treat retained observation metadata as transcript coverage", () => {
  const older: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "item-1",
          turnId: "turn-1",
          type: "reasoning",
          text: "same",
          status: "completed",
          observedStartedAt: "2026-09-18T00:00:01.000Z",
        },
      ],
    },
  ];
  const newer: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "item-1",
          turnId: "turn-1",
          type: "reasoning",
          text: "same",
          status: "completed",
        },
      ],
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
  expect(merged.turns[0]?.items[0]?.observedStartedAt).toBe("2026-09-18T00:00:01.000Z");
});

test("mergeOlderItemPage coalesces every transitively overlapping fragment", () => {
  const model = testHydrate({
    turns: [
      fragmentTurn("fresh-x", ["x"]),
      fragmentTurn("fresh-w", ["w"]),
      fragmentTurn("fresh-zw", ["z", "w"]),
      fragmentTurn("fresh-yz", ["y", "z"]),
      fragmentTurn("fresh-xy", ["x", "y"]),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      fragmentTurn("old-xp", ["x", "p"]),
      fragmentTurn("old-y", ["y"]),
      fragmentTurn("old-z", ["z"]),
      fragmentTurn("old-w", ["w"]),
    ],
    nextCursor: "cursor_0",
  });

  expect(result.turns).toHaveLength(1);
  expect(result.turns[0]?.items.map((item) => item.id)).toEqual(["x", "p", "y", "z", "w"]);
  expect(new Set(result.turns[0]?.items.map((item) => item.id))).toHaveLength(5);
  expect(result.olderCursor).toBe("cursor_0");
});

test("mergeOlderItemPage coalesces overlapping fresh fragments without an older match", () => {
  const model = testHydrate({
    turns: [
      fragmentTurn("fresh-a", ["x"]),
      fragmentTurn("fresh-b", ["y"]),
      fragmentTurn("fresh-c", ["x"], { usage: { inputTokens: 2 } }),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [fragmentTurn("old-z", ["z"])],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["old-z", "fresh-c", "fresh-b"]);
  expect(result.turns[1]?.items.map((item) => item.id)).toEqual(["x"]);
  expect(result.turns[1]?.usage).toEqual({ inputTokens: 2 });
});

test("mergeOlderItemPage applies fresh fields after coalescing a fragment chain", () => {
  const model = testHydrate({
    turns: [
      fragmentTurn("fresh-x", ["x"], { usage: { inputTokens: 2 } }),
      fragmentTurn("fresh-xy", ["x", "y"], { usage: { inputTokens: 3 } }),
      fragmentTurn("fresh-y", ["y"], { usage: { inputTokens: 4 } }),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [fragmentTurn("old-x", ["x"], { usage: { inputTokens: 1 } }), fragmentTurn("old-y", ["y"])],
  });

  expect(result.turns).toHaveLength(1);
  expect(result.turns[0]).toMatchObject({ id: "fresh-y", usage: { inputTokens: 4 } });
  expect(result.turns[0]?.items.map((item) => item.id).sort()).toEqual(["x", "y"]);
});

test("mergeOlderItemPage places a positioned retained run inside fresh anchors", () => {
  const model = testHydrate({
    turns: [
      positionedFragmentTurn("fresh-a", [["a", 1]]),
      positionedFragmentTurn("fresh-d", [["d", 3]]),
      positionedFragmentTurn("fresh-c", [["c", 4]]),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-a", [["a", 1]]),
      positionedFragmentTurn("old-b", [["b", 2]]),
      positionedFragmentTurn("old-c", [["c", 4]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-a", "old-b", "fresh-d", "fresh-c"]);
});

test("mergeOlderItemPage preserves retained prefix and suffix runs", () => {
  const model = testHydrate({
    turns: [positionedFragmentTurn("fresh-a", [["a", 1]]), positionedFragmentTurn("fresh-c", [["c", 4]])],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-p", [["p", 0]]),
      positionedFragmentTurn("old-a", [["a", 1]]),
      positionedFragmentTurn("old-b", [["b", 2]]),
      positionedFragmentTurn("old-c", [["c", 4]]),
      positionedFragmentTurn("old-s", [["s", 5]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["old-p", "fresh-a", "old-b", "fresh-c", "old-s"]);
});

test("mergeOlderItemPage preserves fresh prefix and suffix around a retained run", () => {
  const model = testHydrate({
    turns: [
      positionedFragmentTurn("fresh-x", [["x", 0]]),
      positionedFragmentTurn("fresh-a", [["a", 1]]),
      positionedFragmentTurn("fresh-c", [["c", 4]]),
      positionedFragmentTurn("fresh-y", [["y", 5]]),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-a", [["a", 1]]),
      positionedFragmentTurn("old-b", [["b", 2]]),
      positionedFragmentTurn("old-c", [["c", 4]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-x", "fresh-a", "old-b", "fresh-c", "fresh-y"]);
});

test("mergeOlderItemPage retains a warning-only run after a collapsed anchor", () => {
  const model = testHydrate({
    turns: [fragmentTurn("fresh-f", ["a", "c"])],
  });
  const warning = {
    id: "old-b",
    status: "completed" as const,
    itemsView: "fragment" as const,
    items: [
      {
        id: "warning-b",
        turnId: "old-b",
        type: "warning" as const,
        text: "live warning",
        status: "completed" as const,
      },
    ],
  };

  const result = mergeOlderItemPage(model, {
    data: [positionedFragmentTurn("old-a", [["a", 1]]), warning, positionedFragmentTurn("old-c", [["c", 3]])],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-f", "old-b"]);
  expect(result.turns.filter((turn) => turn.id === "old-b")).toHaveLength(1);
  expect(result.turns[1]?.items).toMatchObject([{ id: "warning-b", type: "warning" }]);
});

test("mergeOlderItemPage places an unpositioned warning run with its next positioned turn", () => {
  const model = testHydrate({
    turns: [
      positionedFragmentTurn("fresh-a", [["a", 1]]),
      positionedFragmentTurn("fresh-d", [["d", 3]]),
      positionedFragmentTurn("fresh-c", [["c", 4]]),
    ],
  });
  const warning = {
    id: "old-warning",
    status: "completed" as const,
    itemsView: "fragment" as const,
    items: [
      {
        id: "warning-item",
        turnId: "old-warning",
        type: "warning" as const,
        text: "retained warning",
        status: "completed" as const,
      },
    ],
  };

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-a", [["a", 1]]),
      warning,
      positionedFragmentTurn("old-b", [["b", 2]]),
      positionedFragmentTurn("old-c", [["c", 4]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-a", "old-warning", "old-b", "fresh-d", "fresh-c"]);
});

test.each([
  { gap: "prefix", freshEntry: 1 },
  { gap: "prefix", freshEntry: 3 },
  { gap: "interior", freshEntry: 1 },
  { gap: "interior", freshEntry: 3 },
  { gap: "suffix", freshEntry: 1 },
  { gap: "suffix", freshEntry: 3 },
])(
  "mergeOlderItemPage honors positions past a fresh announcement in a $gap gap at $freshEntry",
  ({ gap, freshEntry }) => {
    const before = gap === "prefix" ? [] : [positionedFragmentTurn("fresh-a", [["a", 0]])];
    const after = gap === "suffix" ? [] : [positionedFragmentTurn("fresh-c", [["c", 4]])];
    const model = testHydrate({
      turns: [...before, positionedFragmentTurn("fresh-d", [["d", freshEntry]]), ...after],
    });
    model.turns.splice(before.length, 0, {
      id: "announcement",
      status: "completed",
      items: [{ id: "u", turnId: "announcement", type: "systemMessage", text: "notice", status: "completed" }],
    });
    const freshIds = model.turns.map((turn) => turn.id);
    const result = mergeOlderItemPage(model, {
      data: [...before, positionedFragmentTurn("old-b", [["b", 2]]), ...after],
    });
    const turnIds = result.turns.map((turn) => turn.id);
    const itemIds = result.turns.flatMap((turn) => turn.items.map((item) => item.id));

    expect(turnIds.filter((id) => freshIds.includes(id))).toEqual(freshIds);
    expect(turnIds.filter((id) => !freshIds.includes(id))).toEqual(["old-b"]);
    expect(itemIds.sort()).toEqual([
      ...(gap === "prefix" ? [] : ["a"]),
      "b",
      ...(gap === "suffix" ? [] : ["c"]),
      "d",
      "u",
    ]);
    if (before.length > 0) expect(turnIds.indexOf("fresh-a")).toBeLessThan(turnIds.indexOf("old-b"));
    if (after.length > 0) expect(turnIds.indexOf("old-b")).toBeLessThan(turnIds.indexOf("fresh-c"));
    if (freshEntry < 2) expect(turnIds.indexOf("fresh-d")).toBeLessThan(turnIds.indexOf("old-b"));
    else expect(turnIds.indexOf("old-b")).toBeLessThan(turnIds.indexOf("fresh-d"));
  },
);

test("mergeOlderItemPage keeps retained turns ordered across interleaved collapsed anchors", () => {
  const model = testHydrate({
    turns: [
      positionedFragmentTurn("fresh-ac", [
        ["a", 1],
        ["c", 4],
      ]),
      positionedFragmentTurn("fresh-bd", [
        ["b", 2],
        ["d", 6],
      ]),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-a", [["a", 1]]),
      positionedFragmentTurn("old-b", [["b", 2]]),
      positionedFragmentTurn("old-x", [["x", 3]]),
      positionedFragmentTurn("old-c", [["c", 4]]),
      positionedFragmentTurn("old-y", [["y", 5]]),
      positionedFragmentTurn("old-d", [["d", 6]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-ac", "fresh-bd", "old-x", "old-y"]);
});

test("mergeOlderItemPage uses the earliest compatible successor anchor", () => {
  const model = testHydrate({
    turns: [
      positionedFragmentTurn("fresh-a", [["a", 1]]),
      positionedFragmentTurn("fresh-b", [["b", 4]]),
      positionedFragmentTurn("fresh-c", [["c", 3]]),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-a", [["a", 1]]),
      positionedFragmentTurn("old-x", [["x", 2]]),
      positionedFragmentTurn("old-c", [["c", 3]]),
      positionedFragmentTurn("old-b", [["b", 4]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-a", "old-x", "fresh-b", "fresh-c"]);
});

test("mergeOlderItemPage merges shared turns and transcript items in position order with current precedence", () => {
  const thread = testThread({
    turns: [
      {
        id: "live-turn",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "live-k1",
            transcriptKey: "k1",
            position: { entry: 2, item: 1 },
            turnId: "live-turn",
            type: "agentMessage",
            text: "new text",
            status: "completed",
          },
          {
            id: "live-k2",
            transcriptKey: "k2",
            position: { entry: 3, item: 1 },
            turnId: "live-turn",
            type: "agentMessage",
            text: "current-only",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  const current = model.turns[0];
  if (!current) throw new Error("expected current turn");
  const currentItem = current.items[0];
  if (!currentItem) throw new Error("expected current item");
  currentItem.observedStartedAt = "1970-01-01T00:00:01.001Z";
  currentItem.observedCompletedAt = "1970-01-01T00:00:01.002Z";
  currentItem.reasoningSummaries = [["kept reasoning"]];
  currentItem.outputImages = [{ src: "new-image" }];

  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "historical-turn",
        status: "inProgress",
        itemsView: "full",
        items: [
          {
            id: "old-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "historical-turn",
            type: "agentMessage",
            text: "older-only",
            status: "completed",
          },
          {
            id: "old-k1",
            transcriptKey: "k1",
            position: { entry: 2, item: 1 },
            turnId: "historical-turn",
            type: "agentMessage",
            text: "old duplicate",
            argumentsJson: "old arguments",
            outputImages: [{ url: "old-image", source: "old-source" }],
            status: "inProgress",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });

  expect(result.turns).toHaveLength(1);
  expect(result.turns[0]?.items.map((item) => item.transcriptKey)).toEqual(["k0", "k1", "k2"]);
  expect(result.turns[0]?.items.filter((item) => item.transcriptKey === "k1")).toHaveLength(1);
  expect(result.turns[0]?.items[1]).toMatchObject({
    id: "live-k1",
    text: "new text",
    argumentsJSON: "old arguments",
    observedStartedAt: "1970-01-01T00:00:01.001Z",
    observedCompletedAt: "1970-01-01T00:00:01.002Z",
    reasoningSummaries: [["kept reasoning"]],
    outputImages: [{ src: "new-image" }],
    status: "completed",
  });
  expect(result.olderCursor).toBe("cursor_0");
});

test("mergeOlderItemPage preserves older settled payload and usage when the current same-key fragment omits them", () => {
  const thread = testThread({
    turns: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "current-item",
            transcriptKey: "shared-key",
            position: { entry: 4, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "newer text wins",
            status: "completed",
          },
        ],
      },
    ],
  });

  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        usage: { inputTokens: 11, outputTokens: 7, totalTokens: 18 },
        itemsView: "fragment",
        items: [
          {
            id: "older-item",
            transcriptKey: "shared-key",
            position: { entry: 4, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "older text",
            output: "older output",
            error: "older error",
            exitCode: 3,
            completedAt: 2,
            status: "completed",
          },
        ],
      },
    ],
  });

  expect(result.turns[0]?.usage).toEqual({ inputTokens: 11, outputTokens: 7, totalTokens: 18 });
  expect(result.turns[0]?.items[0]).toMatchObject({
    id: "current-item",
    text: "newer text wins",
    output: "older output",
    error: "older error",
    exitCode: 3,
    completedAt: "1970-01-01T00:00:00.002Z",
    status: "completed",
  });
});

test.each([
  ["omitted text falls back to the older settled text", undefined, "settled text"],
  ["explicit empty text remains empty", "", ""],
  ["explicit nonempty text remains the newer text", "newer text", "newer text"],
])("mergeOlderItemPage preserves same-key text presence semantics: %s", (_case, newerText, expectedText) => {
  const model = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "shared-turn",
            status: "completed",
            itemsView: "fragment",
            items: [
              {
                id: "newer-item",
                transcriptKey: "shared-key",
                position: { entry: 2, item: 0 },
                turnId: "shared-turn",
                type: "agentMessage",
                ...(newerText === undefined ? {} : { text: newerText }),
                status: "completed",
              },
              {
                id: "newer-after",
                transcriptKey: "after-key",
                position: { entry: 2, item: 1 },
                turnId: "shared-turn",
                type: "agentMessage",
                text: "after",
                status: "completed",
              },
            ],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );

  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "settled-item",
            transcriptKey: "shared-key",
            position: { entry: 2, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "settled text",
            output: "settled output",
            status: "completed",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });

  expect(result.turns[0]?.items.map((item) => item.transcriptKey)).toEqual(["shared-key", "after-key"]);
  expect(result.turns[0]?.items[0]).toMatchObject({
    id: "newer-item",
    text: expectedText,
    output: "settled output",
    position: { entry: 2, item: 0 },
  });
  expect(result.olderCursor).toBe("cursor_0");
});

test("mergeOlderItemPage keeps omitted text absent across repeated same-key merges", () => {
  const model = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "shared-turn",
            status: "completed",
            itemsView: "fragment",
            items: [{ id: "newer-item", transcriptKey: "shared-key", turnId: "shared-turn", type: "agentMessage" }],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );
  const page = {
    data: [
      {
        id: "shared-turn",
        status: "completed" as const,
        itemsView: "full" as const,
        items: [
          {
            id: "settled-item",
            transcriptKey: "shared-key",
            turnId: "shared-turn",
            type: "agentMessage" as const,
            text: "settled text",
            status: "completed" as const,
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  };

  const first = mergeOlderItemPage(model, page);
  const second = mergeOlderItemPage(first, page);

  expect(first.turns[0]?.items[0]?.text).toBe("settled text");
  expect(second.turns[0]?.items[0]?.text).toBe("settled text");
  expect(second.turns[0]?.items).toHaveLength(1);
});

test("an even older omitted fragment does not erase an older provided text", () => {
  const model = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "shared-turn",
            status: "completed",
            itemsView: "fragment",
            items: [{ id: "newer-item", transcriptKey: "shared-key", turnId: "shared-turn", type: "agentMessage" }],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );
  const provided = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "provided-item",
            transcriptKey: "shared-key",
            turnId: "shared-turn",
            type: "agentMessage",
            text: "A",
          },
        ],
      },
    ],
  });
  const result = mergeOlderItemPage(provided, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "omitted-item",
            transcriptKey: "shared-key",
            turnId: "shared-turn",
            type: "agentMessage",
          },
        ],
      },
    ],
  });

  expect(result.turns[0]?.items[0]?.text).toBe("A");
});

test("mergeOlderItemPage retains the older status when an equal-rank newer item omits status", () => {
  const thread = testThread({
    turns: [
      {
        id: "shared-turn",
        status: "inProgress",
        itemsView: "fragment",
        items: [
          {
            id: "newer-item",
            transcriptKey: "shared-key",
            position: { entry: 4, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "newer text without status",
          },
        ],
      },
    ],
  });

  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "inProgress",
        itemsView: "fragment",
        items: [
          {
            id: "older-item",
            transcriptKey: "shared-key",
            position: { entry: 4, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "older text",
            status: "inProgress",
          },
        ],
      },
    ],
  });

  expect(result.turns[0]?.items[0]).toMatchObject({ text: "newer text without status", status: "inProgress" });
});

test("mergeOlderItemPage retains older identity when a newer matching item omits it", () => {
  const thread = testThread({
    turns: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "same-id",
            transcriptKey: "",
            turnId: "shared-turn",
            type: "agentMessage",
            text: "newer unkeyed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "same-id",
            transcriptKey: "older-key",
            position: { entry: 1, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "older keyed",
          },
        ],
      },
    ],
  });
  expect(result.turns[0]?.items).toHaveLength(1);
  expect(result.turns[0]?.items[0]).toMatchObject({
    id: "same-id",
    text: "newer unkeyed",
    transcriptKey: "older-key",
    position: { entry: 1, item: 0 },
  });
});

test("mergeOlderItemPage lets newer defined identity replace older identity", () => {
  const thread = testThread({
    turns: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "same-id",
            transcriptKey: "newer-key",
            position: { entry: 2, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "newer keyed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "same-id",
            turnId: "shared-turn",
            type: "agentMessage",
            text: "older unkeyed",
          },
        ],
      },
    ],
  });
  expect(result.turns[0]?.items).toHaveLength(1);
  expect(result.turns[0]?.items[0]).toMatchObject({
    transcriptKey: "newer-key",
    position: { entry: 2, item: 0 },
    text: "newer keyed",
  });
});

// A keyless alias matches a keyed item by id, so a live keyless fragment
// sharing an item id with two historical fragments whose supplied transcript
// keys differ would match BOTH — and the coalescing group merge would bridge
// the two recorded turns into one, combining their items and turn payloads,
// even though the two keyed turns do not match each other. The ambiguous
// keyless alias must not bridge them: the conflicting turns stay separate,
// and the alias folds into the first (earliest) group it matched, keeping
// its fresh text under that group's key.
test("mergeOlderItemPage keeps conflicting transcript keys apart when a keyless alias bridges them", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn-U",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "shared-id",
            turnId: "turn-U",
            type: "agentMessage",
            text: "fresh text",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "turn-A",
        status: "completed",
        usage: { inputTokens: 10 },
        itemsView: "fragment",
        items: [
          {
            id: "shared-id",
            transcriptKey: "key-a",
            turnId: "turn-A",
            type: "agentMessage",
            text: "older A",
            status: "completed",
          },
        ],
      },
      {
        id: "turn-B",
        status: "completed",
        usage: { inputTokens: 20 },
        itemsView: "fragment",
        items: [
          {
            id: "shared-id",
            transcriptKey: "key-b",
            turnId: "turn-B",
            type: "agentMessage",
            text: "older B",
            status: "completed",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });

  // Two turns, one key each: key-a and key-b never share a turn.
  expect(
    result.turns.map((turn) => ({
      usage: turn.usage,
      keys: turn.items.map((item) => item.transcriptKey),
    })),
  ).toEqual([
    { usage: { inputTokens: 10 }, keys: ["key-a"] },
    { usage: { inputTokens: 20 }, keys: ["key-b"] },
  ]);
  // The keyless alias still folds into the turn it matched: its fresh text
  // wins under that turn's key rather than surviving as a third turn.
  expect(result.turns[0]?.items[0]).toMatchObject({ id: "shared-id", text: "fresh text" });
});

// The bridge need not be the group a keyless alias lands in first. Here the
// target turn carries one keyed identity (`w`/`key-w`), a keyless alias also
// shares the id of two OTHER turns that conflict with each other (`z` under
// `key-a` and `key-b`). Neither conflicting turn disagrees with the target, so
// a guard that only compares each candidate with the target absorbs both and
// re-introduces the bridge; the guard must check each candidate against the
// group as it grows, so `key-a` and `key-b` still never share a turn.
test("mergeOlderItemPage keeps two mutually conflicting keys apart when a keyless alias touches a neutral third turn", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn-F",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "w",
            transcriptKey: "key-w",
            turnId: "turn-F",
            type: "agentMessage",
            text: "fresh w",
            status: "completed",
          },
          {
            id: "z",
            turnId: "turn-F",
            type: "agentMessage",
            text: "fresh z",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "turn-T",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "w",
            transcriptKey: "key-w",
            turnId: "turn-T",
            type: "agentMessage",
            text: "older w",
            status: "completed",
          },
        ],
      },
      {
        id: "turn-A",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "z",
            transcriptKey: "key-a",
            turnId: "turn-A",
            type: "agentMessage",
            text: "older A",
            status: "completed",
          },
        ],
      },
      {
        id: "turn-B",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "z",
            transcriptKey: "key-b",
            turnId: "turn-B",
            type: "agentMessage",
            text: "older B",
            status: "completed",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });

  expect(result.turns.map((turn) => turn.items.map((item) => item.transcriptKey))).toEqual([
    ["key-w", "key-a"],
    ["key-b"],
  ]);
});

// The alias can also arrive FIRST. With an older page ordered alias, key-a,
// key-b, the alias forms its own group and each later keyed fragment matches
// ONLY that group (the two keyed fragments do not match each other), so a
// guard applied only to the absorbed candidates never runs. The incoming
// fragment must be checked against the group it is about to join, so the two
// keys still never share a turn.
test("mergeOlderItemPage keeps conflicting keys apart when the keyless alias arrives before them", () => {
  const model = hydrateThread({ thread: testThread({ turns: [] }), olderCursor: "cursor_1" }, "ref_t", 1000);
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "turn-K",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "shared-id",
            turnId: "turn-K",
            type: "agentMessage",
            text: "older keyless",
            status: "completed",
          },
        ],
      },
      {
        id: "turn-A",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "shared-id",
            transcriptKey: "key-a",
            turnId: "turn-A",
            type: "agentMessage",
            text: "older A",
            status: "completed",
          },
        ],
      },
      {
        id: "turn-B",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "shared-id",
            transcriptKey: "key-b",
            turnId: "turn-B",
            type: "agentMessage",
            text: "older B",
            status: "completed",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });

  expect(result.turns.map((turn) => turn.items.map((item) => item.transcriptKey))).toEqual([["key-a"], ["key-b"]]);
});

// A fragment that conflicts with the FIRST matching group may still fold into
// a later compatible one. Refusing outright leaves a shared keyless item
// standing twice — the exact duplication coalescing exists to remove — and
// makes the result depend on group insertion order.
test("mergeOlderItemPage folds an alias into a later compatible turn instead of leaving a duplicate", () => {
  const model = testHydrate({
    turns: [
      {
        id: "turn-F",
        status: "completed",
        itemsView: "fragment",
        items: [
          { id: "a", turnId: "turn-F", type: "agentMessage", text: "fresh a", status: "completed" },
          { id: "b", turnId: "turn-F", type: "agentMessage", text: "fresh b", status: "completed" },
          {
            id: "p",
            transcriptKey: "key-f",
            turnId: "turn-F",
            type: "agentMessage",
            text: "fresh p",
            status: "completed",
          },
        ],
      },
    ],
  });
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "turn-G0",
        status: "completed",
        itemsView: "fragment",
        items: [
          { id: "a", turnId: "turn-G0", type: "agentMessage", text: "older a", status: "completed" },
          {
            id: "p",
            transcriptKey: "key-g",
            turnId: "turn-G0",
            type: "agentMessage",
            text: "older p",
            status: "completed",
          },
        ],
      },
      {
        id: "turn-G1",
        status: "completed",
        itemsView: "fragment",
        items: [{ id: "b", turnId: "turn-G1", type: "agentMessage", text: "older b", status: "completed" }],
      },
    ],
    nextCursor: "cursor_0",
  });

  // F conflicts with G0 (p: key-f vs key-g) but not G1, so it folds into G1:
  // `a` and `b` are not each left standing in their own separate turn.
  expect(result.turns).toHaveLength(2);
  const folded = result.turns.find((turn) => turn.items.some((item) => item.transcriptKey === "key-f"));
  expect(folded?.items.map((item) => item.id).sort()).toEqual(["a", "b", "p"]);
  const kept = result.turns.find((turn) => turn.items.some((item) => item.transcriptKey === "key-g"));
  expect(kept?.items.map((item) => item.id).sort()).toEqual(["a", "p"]);
});

test("mergeOlderItemPage position-orders the final items when pages arrive out of chronology", () => {
  const thread = testThread({
    turns: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "current-earlier",
            transcriptKey: "key-1",
            position: { entry: 1, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "current earlier position",
          },
        ],
      },
    ],
  });

  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "arrived-later",
            transcriptKey: "key-3",
            position: { entry: 3, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "later position arrived on older request",
          },
        ],
      },
    ],
  });

  expect(result.turns[0]?.items.map((item) => item.transcriptKey)).toEqual(["key-1", "key-3"]);
});

test("mergeOlderItemPage preserves unmatched results and folds a result-only turn only after its call arrives", () => {
  const model = testHydrate();
  const resultOnly = {
    id: "result-turn",
    status: "completed",
    itemsView: "full",
    items: [
      {
        id: "item_tool_result_orphan",
        transcriptKey: "result-key",
        position: { entry: 1, item: 1 },
        turnId: "result-turn",
        type: "commandExecution",
        callId: "orphan-call",
        output: "orphan output",
        status: "completed",
      },
    ],
  };
  const visible = mergeOlderItemPage(model, { data: [resultOnly], nextCursor: "cursor_0" });
  expect(visible.turns).toHaveLength(1);
  expect(visible.turns[0]?.items[0]?.output).toBe("orphan output");

  const withCall = mergeOlderItemPage(visible, {
    data: [
      {
        id: "call-turn",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_call_orphan",
            transcriptKey: "call-key",
            position: { entry: 0, item: 1 },
            turnId: "call-turn",
            type: "commandExecution",
            callId: "orphan-call",
            argumentsJson: "{}",
            status: "inProgress",
          },
        ],
      },
    ],
    nextCursor: "cursor_done",
  });
  expect(withCall.turns).toHaveLength(1);
  expect(withCall.turns[0]?.items).toHaveLength(1);
  expect(withCall.turns[0]?.items[0]).toMatchObject({
    id: "item_tool_call_orphan",
    argumentsJSON: "{}",
    output: "orphan output",
    status: "completed",
  });
});

// The hub says "this item's output images are gone" with an explicit [] on the
// item frame (appwire.ThreadItem.OutputImages is omitzero for exactly this). An
// absent field still means "this frame says nothing", where the other side's
// list is the only one anybody has.
test("an explicit empty outputImages list removes the images an older page still carries", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "live-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "commandExecution",
            callId: "call_1",
            outputImages: [],
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).outputImages).toEqual([]);

  const merged = mergeOlderItemPage(model, {
    data: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "old-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "commandExecution",
            callId: "call_1",
            outputImages: [{ url: "stale-image", source: "tool-result" }],
            status: "completed",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });
  expect(itemAt(turnAt(merged, 0), 0).outputImages).toEqual([]);
});

test("an absent outputImages field keeps the images the other page carries", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "live-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "commandExecution",
            callId: "call_1",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).outputImages).toBeUndefined();

  const merged = mergeOlderItemPage(model, {
    data: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "old-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "commandExecution",
            callId: "call_1",
            outputImages: [{ url: "kept-image", source: "tool-result" }],
            status: "completed",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });
  expect(itemAt(turnAt(merged, 0), 0).outputImages).toEqual([{ src: "kept-image", source: "tool-result" }]);
});

// Output images: see appwire.MergeOutputImages; input images: see appwire.MergeInputImages.
test("an empty input images list never erases the images an older page carries", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "live-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "userMessage",
            text: "look at this",
            images: [],
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);

  const merged = mergeOlderItemPage(model, {
    data: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "old-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "userMessage",
            text: "look at this",
            images: [{ type: "image", mediaType: "image/png", data: "aGk=", name: "shot.png" }],
            status: "completed",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });
  const images = itemAt(turnAt(merged, 0), 0).images;
  expect(images).toHaveLength(1);
});

// --- prependOlderTurns edge case (line 307) ---

test("prependOlderTurns keeps an unmatched item in the turn", () => {
  // This exercises the items.push(item) path in mergeToolCallsByCallId
  // when an item from the older turn has no tool call/result pattern.
  // The response uses ThreadTurnsListResponse with .data as wire turns.
  const wireTurn = {
    id: "old_turn",
    status: "completed" as const,
    itemsView: "full",
    items: [{ id: "old_item", type: "message", text: "old message" }],
  };
  const currentTurn: TurnModel = { id: "current", status: "completed", items: [] };
  const model = testHydrate();
  const result = prependOlderTurns({ ...model, turns: [currentTurn] }, { data: [wireTurn], nextCursor: undefined });
  expect(result.turns.map((turn) => turn.id)).toEqual(["old_turn", "current"]);
  expect(result.turns[0]).toMatchObject({
    id: "old_turn",
    status: "completed",
    items: [{ id: "old_item", type: "message", text: "old message" }],
  });
  expect(result.turns[1]).toBe(currentTurn);
});
