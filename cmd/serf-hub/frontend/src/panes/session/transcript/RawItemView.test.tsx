import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { RawItemView } from "./RawItemView";
import type { ItemModel, TurnModel } from "../../../protocol/model";

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "somethingUnregistered", text: "", ...overrides };
}

test("shows the item's raw type as a label", () => {
  render(<RawItemView item={item({ type: "systemMessage" })} turn={turn} live={false} />);
  expect(screen.getByText("systemMessage")).toBeTruthy();
});

test("shows settled text when not live", () => {
  render(<RawItemView item={item({ text: "hello world" })} turn={turn} live={false} />);
  expect(screen.getByText("hello world")).toBeTruthy();
});

test("shows settled text when live but there is no pendingText to stream", () => {
  render(<RawItemView item={item({ text: "already flattened" })} turn={turn} live={true} />);
  expect(screen.getByText("already flattened")).toBeTruthy();
});

test("streams via StreamingText when live with pendingText chunks - the joined content renders", () => {
  render(<RawItemView item={item({ pendingText: ["hel", "lo"] })} turn={turn} live={true} />);
  expect(screen.getByTestId("streaming-text")).toBeTruthy();
  expect(screen.getByTestId("streaming-text").textContent).toBe("hello");
});

test("does NOT stream when settled (live=false), even if pendingText is (unusually) still present", () => {
  render(<RawItemView item={item({ text: "final", pendingText: ["stale", "chunks"] })} turn={turn} live={false} />);
  expect(screen.queryByTestId("streaming-text")).toBeNull();
  expect(screen.getByText("final")).toBeTruthy();
});

test("an empty pendingText array is treated the same as no pendingText - falls back to settled text, no StreamingText mounted", () => {
  render(<RawItemView item={item({ text: "", pendingText: [] })} turn={turn} live={true} />);
  expect(screen.queryByTestId("streaming-text")).toBeNull();
});

test("incremental growth: re-rendering with more pendingText chunks grows the streamed content without duplication", () => {
  const { rerender } = render(<RawItemView item={item({ pendingText: ["a"] })} turn={turn} live={true} />);
  expect(screen.getByTestId("streaming-text").textContent).toBe("a");
  rerender(<RawItemView item={item({ pendingText: ["a", "b"] })} turn={turn} live={true} />);
  expect(screen.getByTestId("streaming-text").textContent).toBe("ab");
});

test("tags the root with the item's raw type for styling/testing hooks", () => {
  const { container } = render(<RawItemView item={item({ type: "customType" })} turn={turn} live={false} />);
  expect(container.querySelector('[data-item-type="customType"]')).toBeTruthy();
});
