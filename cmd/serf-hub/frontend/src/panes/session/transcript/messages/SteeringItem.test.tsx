import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { itemRendererFor } from "../types";
import { SteeringItem } from "./SteeringItem";

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "completed", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "steering", text: "", ...overrides };
}

test('self-registers under the wire\'s steering item type ("steering")', () => {
  expect(itemRendererFor("steering")).toBe(SteeringItem);
});

// --- source: "user" -> a normal user message, never the divider ------------
// Parity issue #24 (internal/appprojector/appwire_projection.go:588,
// internal/apptranscript/apptranscript.go:225-228): steering the human
// typed themselves is indistinguishable from a normal prompt.

test('source "user" renders as a normal user message bubble (the "You" tag), not the divider', () => {
  render(<SteeringItem item={item({ text: "focus on the tests", source: "user" })} turn={turn} live={false} />);
  expect(screen.getByTestId("user-message-item")).toBeTruthy();
  expect(screen.getByText("You")).toBeTruthy();
  expect(screen.getByText("focus on the tests")).toBeTruthy();
  expect(screen.queryByTestId("steering-item")).toBeNull();
});

test('source "user" steering with images renders the same gallery thumbnails a real user message would', () => {
  render(<SteeringItem item={item({ text: "look", source: "user", images: ["a", "b"] })} turn={turn} live={false} />);
  expect(screen.getAllByTestId("image-gallery-thumb")).toHaveLength(2);
});

// --- daemon-sourced (no source, or any non-"user" source) -> quiet divider --

test("no source at all renders the collapsible steering divider, not a user bubble", () => {
  render(<SteeringItem item={item({ text: "<SYSTEM-REMINDER>nudge</SYSTEM-REMINDER>" })} turn={turn} live={false} />);
  expect(screen.getByTestId("steering-item")).toBeTruthy();
  expect(screen.queryByTestId("user-message-item")).toBeNull();
});

test("the divider is collapsed by default and expands to show the verbatim text", () => {
  render(<SteeringItem item={item({ text: "the raw steering body" })} turn={turn} live={false} />);
  const details = screen.getByTestId("steering-item") as HTMLDetailsElement;
  expect(details.open).toBe(false);
  expect(details.textContent).toContain("the raw steering body");
});

test("the divider's summary uses sentence case, not shouting chrome", () => {
  render(<SteeringItem item={item({ text: "nudge text" })} turn={turn} live={false} />);
  const summary = screen.getByTestId("steering-item").querySelector("summary");
  expect(summary?.textContent).toBe("Steering injected");
});

test("blank text with no source and no images renders nothing distinguishable", () => {
  const { container } = render(<SteeringItem item={item({ text: "" })} turn={turn} live={false} />);
  expect(container.firstChild).toBeNull();
});

test('a non-"user", non-empty source (defensive - only "user" is special-cased) still gets the divider treatment', () => {
  render(<SteeringItem item={item({ text: "daemon nudge", source: "system" })} turn={turn} live={false} />);
  expect(screen.getByTestId("steering-item")).toBeTruthy();
});

test("a daemon-sourced steering item with BOTH real text and images renders the text and drops the image count (documented limitation, matches legacy §8 - pinned so a future change is deliberate)", () => {
  render(
    <SteeringItem item={item({ text: "daemon nudge with a picture", images: ["a", "b"] })} turn={turn} live={false} />,
  );
  const el = screen.getByTestId("steering-item");
  expect(el.textContent).toContain("daemon nudge with a picture");
  expect(screen.queryByTestId("user-message-image-placeholder")).toBeNull();
  expect(el.textContent).not.toMatch(/\[\d+ images?\]/);
});
