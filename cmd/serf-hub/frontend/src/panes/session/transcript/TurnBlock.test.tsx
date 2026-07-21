import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { TurnBlock, isItemLive } from "./TurnBlock";
import { registerItemRenderer, type ItemRenderProps } from "./types";
import type { ItemModel, TurnModel } from "../../../protocol/model";

afterEach(cleanup);

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
  const rendered = screen.getAllByTestId("raw-item").map((el) => el.textContent);
  expect(rendered).toEqual([
    expect.stringContaining("first"),
    expect.stringContaining("second"),
    expect.stringContaining("third"),
  ]);
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
