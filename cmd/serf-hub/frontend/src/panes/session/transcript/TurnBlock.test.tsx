import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { memo } from "react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { isItemLive, TurnBlock } from "./TurnBlock";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "./types";

// A tool row's open/closed state lives in the shared disclosureStore keyed by
// item.id, so a row this file opens must not leak into another test's row of the
// same id.
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "somethingUnregistered", text: "", ...overrides };
}

function turn(items: ItemModel[], overrides: Partial<TurnModel> = {}): TurnModel {
  return { id: "turn_1", status: "inProgress", items, ...overrides };
}

test("isItemLive: inProgress is live", () => {
  expect(isItemLive(item({ status: "inProgress" }))).toBe(true);
});

test("isItemLive: completed is not live", () => {
  expect(isItemLive(item({ status: "completed" }))).toBe(false);
});

test("isItemLive: an item with no status at all is not live", () => {
  expect(isItemLive(item({ status: undefined }))).toBe(false);
});

test("renders an empty turn without crashing", () => {
  const { container } = render(<TurnBlock turn={turn([])} />);
  expect(container.querySelector('[data-testid="turn-block"]')).toBeTruthy();
});

test("tags the root with the turn id", () => {
  const { container } = render(<TurnBlock turn={turn([], { id: "turn_42" })} />);
  expect(container.querySelector('[data-turn-id="turn_42"]')).toBeTruthy();
});

test("renders items in order via the item-renderer registry", () => {
  const items = [
    item({ id: "a", type: "userMessage", text: "first" }),
    item({ id: "b", type: "agentMessage", text: "second" }),
    item({ id: "c", type: "userMessage", text: "third" }),
  ];
  render(<TurnBlock turn={turn(items)} />);
  // With the messages renderers registered (TurnBlock imports "./messages"
  // for TurnSeparator, whose module registers them as a side effect), these
  // types no longer fall back to the raw view - so assert ordering on the
  // block's text, renderer-agnostic: the registry must dispatch each item
  // in wire order regardless of which component won the type.
  const text = screen.getByTestId("turn-block").textContent ?? "";
  const positions = ["first", "second", "third"].map((t) => text.indexOf(t));
  expect(positions.every((p) => p >= 0)).toBe(true);
  expect([...positions]).toEqual([...positions].sort((a, b) => a - b));
});

test("dispatches a registered item type to its own renderer instead of the raw fallback", () => {
  function DummyRenderer({ item: i }: ItemRenderProps) {
    return <div data-testid="dummy-rendered">{i.text}</div>;
  }
  registerItemRenderer("tb-dummy-type", DummyRenderer);
  const items = [item({ id: "a", type: "tb-dummy-type", text: "via dummy" })];
  render(<TurnBlock turn={turn(items)} />);
  expect(screen.getByTestId("dummy-rendered").textContent).toBe("via dummy");
  expect(screen.queryByTestId("raw-item")).toBeNull();
});

test("dispatches a commandExecution item to ToolCallItem", () => {
  const items = [item({ id: "a", type: "commandExecution", toolName: "tb-tool-x", output: "tool output" })];
  render(<TurnBlock turn={turn(items)} />);
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();
  // A tool row starts collapsed and now mounts its body only while open, so the
  // output is proof of dispatch only once the row is opened. The dispatch itself
  // is what this test is about; the row above is already evidence of it, and the
  // output confirms the descriptor's body ran rather than an empty shell.
  fireEvent.click(screen.getByTestId("tool-row"));
  expect(screen.getByText("tool output")).toBeTruthy();
});

test("computes live per item from its own status, passed through to the renderer", () => {
  function LiveEcho({ live }: ItemRenderProps) {
    return <span data-testid="live-echo">{String(live)}</span>;
  }
  registerItemRenderer("tb-live-echo", LiveEcho);
  const items = [
    item({ id: "a", type: "tb-live-echo", status: "inProgress" }),
    item({ id: "b", type: "tb-live-echo", status: "completed" }),
  ];
  render(<TurnBlock turn={turn(items)} />);
  const echoes = screen.getAllByTestId("live-echo").map((el) => el.textContent);
  expect(echoes).toEqual(["true", "false"]);
});

test("passes the owning turn through to each item renderer", () => {
  function TurnEcho({ turn: t }: ItemRenderProps) {
    return <span data-testid="turn-echo">{t.id}</span>;
  }
  registerItemRenderer("tb-turn-echo", TurnEcho);
  const items = [item({ id: "a", type: "tb-turn-echo" })];
  render(<TurnBlock turn={turn(items, { id: "turn_owner" })} />);
  expect(screen.getByTestId("turn-echo").textContent).toBe("turn_owner");
});

// The exact mechanism wave-4 T5c wraps most registered item renderers with
// (ToolCallItem, RawItemView, and every registered messages/ renderer except
// SystemNoticeItem - see each's own registerItemRenderer call site): a
// renderer memoized with `memo(Component, ignoringTurn)` must not re-render
// when TurnBlock re-renders with a NEW turn object but the SAME item
// reference and the same live-determining status - exactly what a streaming
// delta targeting a DIFFERENT item in the same turn produces (reducer.ts's
// immutable-update discipline: only the delta's own item gets a new
// reference, every sibling item keeps its exact reference, but the
// enclosing TurnModel is rebuilt every time).
test("a renderer memoized with ignoringTurn does not re-render when only the enclosing turn object changes (same item, same live)", () => {
  let renderCount = 0;
  const MemoEcho = memo(function MemoEcho({ item: i }: ItemRenderProps) {
    renderCount += 1;
    return <span data-testid="memo-echo">{i.text}</span>;
  }, ignoringTurn);
  registerItemRenderer("tb-memo-echo", MemoEcho);

  const sharedItem = item({ id: "a", type: "tb-memo-echo", text: "stable", status: "completed" });
  const { rerender } = render(<TurnBlock turn={turn([sharedItem], { id: "turn_1" })} />);
  expect(renderCount).toBe(1);
  expect(screen.getByTestId("memo-echo").textContent).toBe("stable");

  // A brand-new turn object (different id, different reference) - the SAME
  // item reference and thus the same computed `live` - must not re-invoke
  // MemoEcho's render function.
  rerender(<TurnBlock turn={turn([sharedItem], { id: "turn_2" })} />);
  expect(renderCount).toBe(1);
  expect(screen.getByTestId("memo-echo").textContent).toBe("stable");
});
