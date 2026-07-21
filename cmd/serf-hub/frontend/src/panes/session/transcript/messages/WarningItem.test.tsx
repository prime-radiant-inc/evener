import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { WarningItem } from "./WarningItem";
import { itemRendererFor } from "../types";
import type { ItemModel, TurnModel } from "../../../../protocol/model";

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "completed", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "warning", text: "", ...overrides };
}

test('self-registers under the wire\'s warning item type ("warning"), exactly once', () => {
  expect(itemRendererFor("warning")).toBe(WarningItem);
});

test("a full payload (title, message, hint) renders all three", () => {
  render(
    <WarningItem
      item={item({ text: "the sandbox blocked write access", warning: { title: "Sandbox blocked", hint: "retry with --sandbox off" } })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByText("Sandbox blocked")).toBeTruthy();
  expect(screen.getByText("the sandbox blocked write access")).toBeTruthy();
  expect(screen.getByText("retry with --sandbox off")).toBeTruthy();
});

test("message-only (no title, no hint) still renders, with a generic label", () => {
  render(<WarningItem item={item({ text: "something concerning happened" })} turn={turn} live={false} />);
  expect(screen.getByText("something concerning happened")).toBeTruthy();
  expect(screen.getByText("Warning")).toBeTruthy(); // generic fallback label, no title given
  expect(screen.queryByTestId("warning-hint")).toBeNull();
});

test("title-only (no message, no hint) renders the title with no message/hint lines", () => {
  render(<WarningItem item={item({ text: "", warning: { title: "Heads up" } })} turn={turn} live={false} />);
  expect(screen.getByText("Heads up")).toBeTruthy();
  expect(screen.queryByTestId("warning-message")).toBeNull();
  expect(screen.queryByTestId("warning-hint")).toBeNull();
});

test("nothing at all (no title, no text, no hint) renders nothing", () => {
  const { container } = render(<WarningItem item={item({ text: "" })} turn={turn} live={false} />);
  expect(container.firstChild).toBeNull();
});
