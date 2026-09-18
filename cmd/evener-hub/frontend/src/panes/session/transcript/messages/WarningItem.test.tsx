import type { ItemModel, TurnModel } from "@evener/appwire-client";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { ignoringTurn, itemRendererFor } from "../types";
import { WarningItem } from "./WarningItem";

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "completed", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "warning", text: "", ...overrides };
}

test('self-registers under the wire\'s warning item type ("warning"), exactly once', () => {
  expect(itemRendererFor("warning")).toBe(WarningItem);
});

test("is memoized ignoring turn identity - a fresh turn object on every streaming delta must not re-render an unrelated settled warning row", () => {
  expect(WarningItem.$$typeof).toBe(Symbol.for("react.memo"));
  expect((WarningItem as unknown as { compare: unknown }).compare).toBe(ignoringTurn);
});

test("a full payload (title, message, hint) renders all three", () => {
  render(
    <WarningItem
      item={item({
        text: "the sandbox blocked write access",
        warning: { title: "Sandbox blocked", hint: "retry with --sandbox off" },
      })}
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

test("whitespace-only title/hint counts as absent, the same reading hasWarningText gives item.text's raw-frame fallback", () => {
  // A frame whose title/hint are whitespace-only carries no real message
  // either (the reducer's own hasWarningText check treats them as absent
  // too, so item.text falls back to the raw frame JSON — see reducer.ts's
  // warning fold). Both readings must agree: the blank title/hint never
  // renders alongside the raw-frame text.
  render(
    <WarningItem
      item={item({ text: '{"threadId":"thr_t"}', warning: { title: "   ", hint: "  " } })}
      turn={turn}
      live={false}
    />,
  );
  // Blank strings are not real content: the generic "Warning" label stands
  // in for the blank title, and no hint line renders — matching
  // hasWarningText's trim check instead of WarningItem's own truthiness.
  expect(screen.getByText("Warning")).toBeTruthy();
  expect(screen.getByText('{"threadId":"thr_t"}')).toBeTruthy();
  expect(screen.queryByTestId("warning-hint")).toBeNull();
});

test("an object-valued title/hint (a malformed wire frame bypassing ItemModel's string type) never reaches the DOM as a React child", () => {
  const objectTitle = { nested: "object" } as unknown as string;
  const arrayHint = ["a", "b"] as unknown as string;
  expect(() =>
    render(
      <WarningItem
        item={item({ text: "something happened", warning: { title: objectTitle, hint: arrayHint } })}
        turn={turn}
        live={false}
      />,
    ),
  ).not.toThrow();
  // Falls back to the generic label instead of stringifying/crashing on the
  // non-string value.
  expect(screen.getByText("Warning")).toBeTruthy();
  expect(screen.queryByTestId("warning-hint")).toBeNull();
});
