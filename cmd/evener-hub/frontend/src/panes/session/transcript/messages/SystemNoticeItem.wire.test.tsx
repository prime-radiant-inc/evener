// System events and failed turns as the daemon actually sends them
// (agent/testdata/systemeventwire), through TurnBlock: the words come from
// @evener/appwire-client's systemEventCopy, which the phone reads too.
import { hydrateThread, type Thread, type ThreadItem, type Turn, type TurnModel } from "@evener/appwire-client";
import {
  type SystemEventWireCase,
  systemEventWireFailedTurn,
  systemEventWireFailedTurnWithOtherError,
  systemEventWireItem,
} from "@evener/appwire-client/testing/systemEventWireFixtures";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { TurnBlock } from "../TurnBlock";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

function turnModel(turn: Turn): TurnModel {
  const thread = {
    id: "thread-1",
    sessionId: "session-1",
    preview: "",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 0,
    updatedAt: 0,
    status: { type: "ready" },
    cwd: "/tmp",
    cliVersion: "1.0.0",
    source: "local",
    turns: [turn],
    evener: { ref: "ref-1", queue: { revision: 0 } },
  } as unknown as Thread;
  const model = hydrateThread({ thread }, "ref-1", 0);
  const hydrated = model.turns[0];
  if (!hydrated) throw new Error("no turn");
  return hydrated;
}

function oneItemTurn(item: ThreadItem): TurnModel {
  return turnModel({ id: "turn_1", itemsView: "full", status: "completed", items: [item] } as unknown as Turn);
}

function lineFor(name: SystemEventWireCase): HTMLElement {
  render(<TurnBlock turn={oneItemTurn(systemEventWireItem(name))} />);
  return screen.getByTestId("system-notice-line");
}

test("names a loaded plugin, or says a plugin loaded, with no counts", () => {
  const named = lineFor("plugin-loaded");
  expect(named.textContent).toContain("superpowers");
  expect(named.textContent).not.toMatch(/\d/);
  cleanup();
  const unnamed = lineFor("plugin-loaded-unnamed");
  expect(unnamed.dataset.fallback).toBeUndefined();
  expect(unnamed.textContent).not.toMatch(/\d/);
});

// The pass's own numbers (412000 -> 38000 tokens, 40 -> 5 turns), in the
// compact token form; a pass with none reads with no numbers at all.
test.each([
  ["context-compaction", /^\D*412K\D+38K\D*$/],
  ["context-compaction-turns", /^\D*40\D+5\D*$/],
  ["context-compaction-bare", /^\D+$/],
] as const)("reads a %s pass by its numbers", (name, numbers) => {
  const line = lineFor(name);
  expect(line.dataset.fallback).toBeUndefined();
  expect(line.textContent).toMatch(numbers);
});

test.each(["compaction-summary", "compaction-checkpoint"] as const)(
  "folds a %s under Context summary, never its first line",
  (name) => {
    render(<TurnBlock turn={oneItemTurn(systemEventWireItem(name))} />);
    const scaffold = screen.getByTestId("system-notice-scaffold");
    expect(scaffold.dataset.kind).toBe("compaction");
    expect(scaffold.querySelector("summary")?.textContent).not.toContain("# Summary");
  },
);

test("a failed turn's own failure row names the event, and its end cap carries the message", () => {
  render(<TurnBlock turn={turnModel(systemEventWireFailedTurn())} />);
  const rows = screen.getAllByTestId("system-notice-failure");
  // The failure item's own description, from the recorded wire.
  expect(rows.map((row) => row.textContent)).toEqual(["Turn failed"]);
  expect(screen.getAllByText(/Provider exploded: 529 overloaded/)).toHaveLength(1);
});

test("an earlier, distinct error in a failed turn says its own message", () => {
  render(<TurnBlock turn={turnModel(systemEventWireFailedTurnWithOtherError())} />);
  const rows = screen.getAllByTestId("system-notice-failure");
  expect(rows.map((row) => row.textContent)).toEqual(["MCP server github disconnected", "Turn failed"]);
});

function itemsTurn(...names: SystemEventWireCase[]): TurnModel {
  return turnModel({
    id: "turn_1",
    itemsView: "full",
    status: "completed",
    items: names.map(systemEventWireItem),
  } as unknown as Turn);
}

// A human's Allow or Deny: "Allowed" or "Denied", the tool's short action,
// the path, and when.
test("an approval decision says what was decided, on what, and when", () => {
  render(<TurnBlock turn={itemsTurn("approval-allowed", "approval-denied")} />);
  const rows = screen.getAllByTestId("system-notice-approval");
  expect(rows.map((row) => row.dataset.approved)).toEqual(["true", "false"]);
  const texts = rows.map((row) => row.querySelector("[data-testid=system-notice-approval-text]")?.textContent ?? "");
  expect(texts[0]).toContain("/Users/j/sites/docs/index.md");
  expect(texts[1]).toContain("/etc/hosts");
  for (const row of rows) expect(row.querySelector("time")).not.toBeNull();
});

// A decision is not lifecycle churn: it never folds into a run of system
// events, so it stays in view between them.
test("an approval decision never folds into a run of system events", () => {
  render(
    <TurnBlock turn={itemsTurn("plugin-loaded", "approval-allowed", "context-compaction", "plugin-loaded-unnamed")} />,
  );
  expect(screen.queryByTestId("system-notice-group")).toBeNull();
  expect(screen.getAllByTestId("system-notice-approval")).toHaveLength(1);
});
