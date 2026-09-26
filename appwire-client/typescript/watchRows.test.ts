// @vitest-environment node

import { expect, test } from "vitest";
import type { ItemModel, ThreadModel } from "./model";
import { applyNotification, mergeOlderItemPage } from "./reducer";
import { foldWatchSummaries } from "./watchRows";

function watchItem(id: string, raw: unknown, turnId = "t1", argumentsJSON?: string): ItemModel {
  return {
    id,
    turnId,
    type: "commandExecution",
    toolName: "job_watch",
    raw,
    argumentsJSON,
  } as unknown as ItemModel;
}

test("keeps caller order when snapshots have no transcript positions", () => {
  const watching = watchItem("a", { watch_id: "watch_x", watching: true, source: "job_y" }, "t2");
  const ended = watchItem("b", { watch_id: "watch_x", watching: false, end_reason: "job_completed" }, "t1");

  // Without positions the caller's stable array order remains authoritative.
  expect(foldWatchSummaries([ended, watching]).get("watch_x")?.state).toBe("watching");
  expect(foldWatchSummaries([watching, ended]).get("watch_x")?.state).toBe("ended");
});

test("a positionless snapshot sorts after positioned ones, so it decides the fold", () => {
  const positioned = {
    ...watchItem("a", { watch_id: "watch_x", watching: true, source: "job_old" }, "t1"),
    position: { entry: 1, item: 0 },
  };
  const positionless = watchItem("b", { watch_id: "watch_x", watching: false, end_reason: "job_completed" }, "t2");

  // A snapshot with no transcript place sorts after every positioned one, so it
  // wins the fold's last-wins merge in BOTH caller orders. Comparing equal across
  // that boundary instead left the winner to the caller's array order.
  expect(foldWatchSummaries([positioned, positionless]).get("watch_x")?.state).toBe("ended");
  expect(foldWatchSummaries([positionless, positioned]).get("watch_x")?.state).toBe("ended");
});

test("folds positioned snapshots in transcript order when supplied in reverse", () => {
  const older = {
    ...watchItem("a", {
      watch_id: "watch_x",
      watching: true,
      source: "job_old",
      condition: "output_match: old",
      deliveries: 1,
    }),
    position: { entry: 2, item: 3 },
  };
  const middle = {
    ...watchItem("b", {
      watch_id: "watch_x",
      source: "job_middle",
      condition: "output_match: middle",
      deliveries: 2,
    }),
    position: { entry: 3, item: 0 },
  };
  const newer = {
    ...watchItem("c", {
      watch_id: "watch_x",
      watching: false,
      source: "job_new",
      condition: "output_match: new",
      deliveries: 4,
      end_reason: "job_completed",
    }),
    position: { entry: 3, item: 1 },
  };
  const transcriptOrder = foldWatchSummaries([older, middle, newer]).get("watch_x");

  expect(foldWatchSummaries([newer, middle, older]).get("watch_x")).toEqual(transcriptOrder);
  expect(transcriptOrder).toEqual({
    id: "watch_x",
    state: "ended",
    source: "job_new",
    condition: "output_match: new",
    deliveries: 4,
    endReason: "job_completed",
  });
});

test("folds reducer-prepended history before live-appended turns with last-present-field-wins", () => {
  const turn = (id: string, itemId: string, raw: unknown) => ({
    id,
    status: "inProgress" as const,
    itemsView: "full" as const,
    items: [
      {
        id: itemId,
        turnId: id,
        type: "commandExecution" as const,
        text: "",
        toolName: "job_watch",
        raw,
        status: "completed" as const,
      },
    ],
  });
  let model = { threadId: "thr_t", ref: "ref_t", turns: [] } as unknown as ThreadModel;
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turn: turn("turn_2", "live-create", {
          watch_id: "watch_x",
          watching: true,
          source: "job_live",
          condition: "output_match: live",
        }),
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turn: turn("turn_3", "live-inspect", { watch_id: "watch_x", deliveries: 7 }),
      },
    },
    1002,
  );

  const paged = mergeOlderItemPage(model, {
    data: [
      turn("turn_1", "historical-create", {
        watch_id: "watch_x",
        watching: false,
        source: "job_stale",
        condition: "output_match: stale",
        deliveries: 1,
      }),
    ],
  });
  const view = foldWatchSummaries(paged.turns.flatMap((entry) => entry.items));

  expect(paged.turns.map((entry) => entry.id)).toEqual(["turn_1", "turn_2", "turn_3"]);
  expect(view.get("watch_x")).toEqual({
    id: "watch_x",
    state: "watching",
    source: "job_live",
    condition: "output_match: live",
    deliveries: 7,
  });
});

test("derives the inspect-format condition from a producer-shaped create and its arguments", () => {
  const raw = {
    watch_id: "watch_x",
    source: "job_y",
    watching: true,
    output_match: "READY.*",
    events: ["assistant.tool", "turn.completed"],
    event_filter: { tool_name: "exec_command", status: "error" },
    progress_interval_ms: 1500,
    note: "wake parent",
    send: { to: "caller", message: "done", include_excerpt: true },
    replaced_existing: false,
    fired: false,
    status: "running",
  };
  const view = foldWatchSummaries([watchItem("a", raw, "t1", JSON.stringify({ operation: "create", every: 3 }))]);

  expect(view.get("watch_x")).toEqual({
    id: "watch_x",
    state: "watching",
    source: "job_y",
    condition:
      "output_match: READY.*; progress_interval_ms: 1500; note: wake parent; events: [assistant.tool, turn.completed] every 3 where tool_name=exec_command, status=error",
    note: "wake parent",
  });

  const triggerless = foldWatchSummaries([
    watchItem("b", {
      watch_id: "watch_plain",
      source: "job_z",
      watching: true,
      send: { to: "caller" },
      replaced_existing: false,
      fired: false,
      status: "running",
    }),
  ]);
  expect(triggerless.get("watch_plain")).toEqual({ id: "watch_plain", state: "watching", source: "job_z" });
});

test("bounds a create output_match to the producer's 1024-rune truncated form", () => {
  const pattern = "🙂".repeat(1025);
  const view = foldWatchSummaries([watchItem("a", { watch_id: "watch_x", watching: true, output_match: pattern })]);
  const condition = view.get("watch_x")?.condition;

  expect(condition).toBe(`output_match: ${"🙂".repeat(1012)}\n[truncated]`);
  expect(Array.from(condition?.slice("output_match: ".length) ?? "")).toHaveLength(1024);
});

test.each([
  ["one-shot", { after_seconds: 90 }, "after_seconds: 90"],
  ["repeating", { repeat_seconds: 120 }, "repeat_seconds: 120"],
] as const)("derives the %s timer clause from a producer-shaped create", (_label, timer, expected) => {
  const view = foldWatchSummaries([
    watchItem("a", { watch_id: "watch_x", watching: true, source: "self", note: "timer note", ...timer }),
  ]);
  expect(view.get("watch_x")?.condition).toBe(`${expected}; note: timer note`);
});

test("absent watching is not coerced to false", () => {
  const view = foldWatchSummaries([watchItem("a", { watch_id: "watch_x", deliveries: 3 })]);
  expect(view.get("watch_x")?.state).toBe("pending");
});

test("absent fields never clear present fields", () => {
  const view = foldWatchSummaries([
    watchItem("a", {
      watch_id: "watch_x",
      watching: true,
      source: "job_y",
      condition: "output_match: ready",
      deliveries: 3,
      note: "keep this",
    }),
    watchItem("b", { watch_id: "watch_x", source: "job_z" }),
  ]);

  expect(view.get("watch_x")).toEqual({
    id: "watch_x",
    state: "watching",
    source: "job_z",
    condition: "output_match: ready",
    deliveries: 3,
    note: "keep this",
  });
});

test("a later ended inspect wins over an earlier create", () => {
  const view = foldWatchSummaries([
    watchItem("a", {
      watch_id: "watch_x",
      watching: true,
      source: "job_y",
      condition: "output_match: ready",
    }),
    watchItem("b", { watch_id: "watch_x", watching: false, end_reason: "job_completed" }),
  ]);

  expect(view.get("watch_x")).toEqual({
    id: "watch_x",
    state: "ended",
    source: "job_y",
    condition: "output_match: ready",
    endReason: "job_completed",
  });
});

test.each([
  ["clear", { watch_id: "watch_x", watching: false, replaced_existing: false, fired: false }, "cleared"],
  [
    "terminal catch-up",
    { watch_id: "watch_x", watching: false, terminal_catchup: true, fired: false },
    "terminal-catch-up",
  ],
] as const)("a later %s marker wins over an earlier create", (_label, later, expected) => {
  const view = foldWatchSummaries([
    watchItem(
      "a",
      { watch_id: "watch_x", watching: true, source: "job_y", events: ["assistant.tool"] },
      "t1",
      JSON.stringify({ operation: "create", every: 4 }),
    ),
    watchItem("b", later),
  ]);
  expect(view.get("watch_x")).toMatchObject({
    state: expected,
    condition: "events: [assistant.tool] every 4",
  });
});

test("a snapshot lacking watching does not downgrade an earlier watching state", () => {
  const view = foldWatchSummaries([
    watchItem("a", { watch_id: "watch_x", watching: true }),
    watchItem("b", { watch_id: "watch_x", deliveries: 4 }),
  ]);
  expect(view.get("watch_x")?.state).toBe("watching");
});

test("end_reason on a live list row reads ended", () => {
  const view = foldWatchSummaries([
    watchItem("a", {
      watches: [
        {
          watch_id: "watch_x",
          source: "job_y",
          watching: false,
          end_reason: "cleared",
        },
      ],
    }),
  ]);
  expect(view.get("watch_x")?.state).toBe("ended");
});

test("present zero deliveries replace an earlier count", () => {
  const view = foldWatchSummaries([
    watchItem("a", { watch_id: "watch_x", deliveries: 3 }),
    watchItem("b", { watch_id: "watch_x", deliveries: 0 }),
  ]);
  expect(view.get("watch_x")?.deliveries).toBe(0);
});

test("an explicit inspect miss reads missing", () => {
  const view = foldWatchSummaries([watchItem("a", { watch_id: "watch_x", watching: false })]);
  expect(view.get("watch_x")?.state).toBe("missing");
});
