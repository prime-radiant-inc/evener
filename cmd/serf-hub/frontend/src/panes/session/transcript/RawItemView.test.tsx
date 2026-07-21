import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { RawItemView } from "./RawItemView";
import { TurnBlock } from "./TurnBlock";
import { ignoringTurn } from "./types";

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "somethingUnregistered", text: "", ...overrides };
}

test("is memoized ignoring turn identity - a fresh turn object on every streaming delta must not re-render an unrelated settled fallback row", () => {
  expect(RawItemView.$$typeof).toBe(Symbol.for("react.memo"));
  expect((RawItemView as unknown as { compare: unknown }).compare).toBe(ignoringTurn);
});

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

test("live streaming settles on the same instance without residue", () => {
  const liveItem = item({ pendingText: ["hel", "lo"] });
  const { rerender } = render(<RawItemView item={liveItem} turn={turn} live={true} />);
  expect(screen.getByTestId("streaming-text")).toBeTruthy();
  expect(screen.getByTestId("streaming-text").textContent).toBe("hello");

  const settledItem = { ...liveItem, text: "hello", pendingText: undefined };
  rerender(<RawItemView item={settledItem} turn={turn} live={false} />);

  expect(screen.queryByTestId("streaming-text")).toBeNull();
  const settledTexts = screen.queryAllByText("hello");
  expect(settledTexts.length).toBe(1);
});

test("a reset discards the live item's DOM entirely", () => {
  const itemA = item({ id: "item_A", status: "inProgress", pendingText: ["chunk", "A"] });
  const turnWithA = { ...turn, items: [itemA] };
  const { rerender } = render(<TurnBlock turn={turnWithA} />);
  expect(screen.getByTestId("streaming-text").textContent).toBe("chunkA");

  const itemB = item({ id: "item_B", status: "inProgress", pendingText: ["chunk", "B"] });
  const turnWithB = { ...turn, items: [itemB] };
  rerender(<TurnBlock turn={turnWithB} />);

  expect(screen.queryByText("chunkA")).toBeNull();
  expect(screen.getByTestId("streaming-text").textContent).toBe("chunkB");
});
