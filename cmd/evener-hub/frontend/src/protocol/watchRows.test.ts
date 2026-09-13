import { expect, test } from "vitest";
import type { ItemModel } from "./model";
import { foldWatchSummaries } from "./watchRows";

function watchItem(id: string, raw: unknown, turnId = "t1"): ItemModel {
  return { id, turnId, type: "commandExecution", toolName: "job_watch", raw } as unknown as ItemModel;
}

test("folds snapshots in caller-supplied transcript order", () => {
  const watching = watchItem("a", { watch_id: "watch_x", watching: true, source: "job_y" }, "t2");
  const ended = watchItem("b", { watch_id: "watch_x", watching: false, end_reason: "job_completed" }, "t1");

  // The caller supplies transcript order; the final array snapshot wins.
  expect(foldWatchSummaries([ended, watching]).get("watch_x")?.state).toBe("watching");
  expect(foldWatchSummaries([watching, ended]).get("watch_x")?.state).toBe("ended");
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
    watchItem("a", { watch_id: "watch_x", watching: true, source: "job_y" }),
    watchItem("b", later),
  ]);
  expect(view.get("watch_x")?.state).toBe(expected);
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
