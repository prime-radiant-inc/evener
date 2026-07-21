import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { TurnBlock } from "../TurnBlock";
import { itemRendererFor } from "../types";
import { ThinkBlock } from "./ThinkBlock";

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "reasoning", text: "", ...overrides };
}

test('self-registers under the wire\'s reasoning item type ("reasoning")', () => {
  expect(itemRendererFor("reasoning")).toBe(ThinkBlock);
});

// --- live: open, StreamingText, "Thinking…" ---------------------------------

test("live shows a Thinking label and streams the reasoning text open (not collapsed)", () => {
  render(
    <ThinkBlock item={item({ reasoningSummaries: [["thinking about ", "the problem"]] })} turn={turn} live={true} />,
  );
  expect(screen.getByText("Thinking…")).toBeTruthy();
  expect(screen.getByTestId("streaming-text").textContent).toBe("thinking about the problem");
  // Open while live: no collapsed <details> present at all.
  expect(document.querySelector("details")).toBeNull();
});

test("live with zero chunks so far still shows the open Thinking label (a reasoning item exists the instant it starts)", () => {
  render(<ThinkBlock item={item({ reasoningSummaries: undefined })} turn={turn} live={true} />);
  expect(screen.getByText("Thinking…")).toBeTruthy();
});

test("live renders one independent StreamingText per summaryIndex, joined per-index (not flattened across indices)", () => {
  render(
    <ThinkBlock
      item={item({
        reasoningSummaries: [
          ["first ", "paragraph"],
          ["second ", "paragraph"],
        ],
      })}
      turn={turn}
      live={true}
    />,
  );
  const streams = screen.getAllByTestId("streaming-text");
  expect(streams).toHaveLength(2);
  expect(streams[0]!.textContent).toBe("first paragraph");
  expect(streams[1]!.textContent).toBe("second paragraph");
});

test("live incremental growth on one summaryIndex does not disturb another index's already-rendered text", () => {
  const { rerender } = render(
    <ThinkBlock item={item({ reasoningSummaries: [["a"], ["x"]] })} turn={turn} live={true} />,
  );
  let streams = screen.getAllByTestId("streaming-text");
  expect(streams.map((s) => s.textContent)).toEqual(["a", "x"]);

  // A delta lands on the EARLIER index after the LATER index already
  // started - exactly the interleaving case a naive flatten would corrupt
  // (see reasoningFormat.ts / ThinkBlock.tsx's own design comments).
  rerender(<ThinkBlock item={item({ reasoningSummaries: [["a", "b"], ["x"]] })} turn={turn} live={true} />);
  streams = screen.getAllByTestId("streaming-text");
  expect(streams.map((s) => s.textContent)).toEqual(["ab", "x"]);
});

test("live skips rendering a paragraph for a zero-chunk summaryIndex (no empty-paragraph gap) until it gets content", () => {
  const { container, rerender } = render(
    <ThinkBlock item={item({ reasoningSummaries: [[], ["second"]] })} turn={turn} live={true} />,
  );
  expect(container.querySelectorAll("p")).toHaveLength(1);
  expect(screen.getByTestId("streaming-text").textContent).toBe("second");

  rerender(<ThinkBlock item={item({ reasoningSummaries: [["first"], ["second"]] })} turn={turn} live={true} />);
  expect(container.querySelectorAll("p")).toHaveLength(2);
  const streams = screen.getAllByTestId("streaming-text");
  expect(streams.map((s) => s.textContent)).toEqual(["first", "second"]);
});

// --- settled: collapse to "Thought" + preview -------------------------------

test("settled collapses to a closed details with a preview of the first paragraph's first line", () => {
  render(
    <ThinkBlock
      item={item({ reasoningSummaries: [["The answer is 42.\nmore reasoning"]] })}
      turn={turn}
      live={false}
    />,
  );
  const details = screen.getByRole("group") as HTMLDetailsElement;
  expect(details.tagName).toBe("DETAILS");
  expect(details.open).toBe(false);
  const summary = details.querySelector("summary");
  expect(summary?.textContent).toBe("Thought · The answer is 42.");
});

test("settled body holds the full reasoning text (not just the preview) once expanded", () => {
  render(<ThinkBlock item={item({ reasoningSummaries: [["full reasoning text here"]] })} turn={turn} live={false} />);
  expect(screen.getByText("full reasoning text here")).toBeTruthy();
});

test("no fabricated duration: without real item.startedAt/completedAt, the label omits a number entirely (never invents one)", () => {
  render(<ThinkBlock item={item({ reasoningSummaries: [["content"]] })} turn={turn} live={false} />);
  const summary = document.querySelector("summary");
  expect(summary?.textContent).toBe("Thought · content");
  expect(summary?.textContent).not.toMatch(/\d/);
});

test("a real startedAt/completedAt pair (future-proofing - not populated by today's wire) produces a real Ns label", () => {
  render(
    <ThinkBlock
      item={item({
        reasoningSummaries: [["content"]],
        startedAt: "2026-01-01T00:00:00.000Z",
        completedAt: "2026-01-01T00:00:04.000Z",
      })}
      turn={turn}
      live={false}
    />,
  );
  expect(document.querySelector("summary")?.textContent).toBe("Thought for 4s · content");
});

test("an observed timing pair (no wire pair) also produces a real Ns label", () => {
  render(
    <ThinkBlock
      item={item({
        reasoningSummaries: [["content"]],
        observedStartedAt: "2026-01-01T00:00:00.000Z",
        observedCompletedAt: "2026-01-01T00:00:03.000Z",
      })}
      turn={turn}
      live={false}
    />,
  );
  expect(document.querySelector("summary")?.textContent).toBe("Thought for 3s · content");
});

test("the wire pair wins over the observed pair when both are present", () => {
  render(
    <ThinkBlock
      item={item({
        reasoningSummaries: [["content"]],
        startedAt: "2026-01-01T00:00:00.000Z",
        completedAt: "2026-01-01T00:00:04.000Z",
        observedStartedAt: "2026-01-01T00:00:00.000Z",
        observedCompletedAt: "2026-01-01T00:00:09.000Z",
      })}
      turn={turn}
      live={false}
    />,
  );
  expect(document.querySelector("summary")?.textContent).toBe("Thought for 4s · content");
});

// --- empty thoughts removed --------------------------------------------------

test("settled with no reasoningSummaries at all renders nothing (empty thought removed)", () => {
  const { container } = render(<ThinkBlock item={item({ reasoningSummaries: undefined })} turn={turn} live={false} />);
  expect(container.firstChild).toBeNull();
});

test("settled with only whitespace-only summary chunks renders nothing", () => {
  const { container } = render(
    <ThinkBlock item={item({ reasoningSummaries: [["   "], ["\n"]] })} turn={turn} live={false} />,
  );
  expect(container.firstChild).toBeNull();
});

// --- the live-to-settled transition: the canonical T1 pattern --------------

test("live streaming settles cleanly into the collapsed disclosure, no leftover StreamingText", () => {
  const liveItem = item({ reasoningSummaries: [["thinking..."]] });
  const { rerender } = render(<ThinkBlock item={liveItem} turn={turn} live={true} />);
  expect(screen.getAllByTestId("streaming-text")).toHaveLength(1);

  const settledItem = { ...liveItem, status: "completed" };
  rerender(<ThinkBlock item={settledItem} turn={{ ...turn, status: "completed" }} live={false} />);

  expect(screen.queryByTestId("streaming-text")).toBeNull();
  expect(screen.queryByText("Thinking…")).toBeNull();
  expect(screen.getByText("Thought · thinking...")).toBeTruthy();
});

test("a reset (a new item id replacing the live one) discards prior streamed text without residue", () => {
  // Mirrors RawItemView.test.tsx's own reset test: the "new item id -> no
  // residue" guarantee comes from TurnBlock's `key={item.id}` forcing a
  // full remount (a fresh StreamingText instance), not from anything
  // ThinkBlock does on its own - so this exercises it through TurnBlock,
  // the same way every renderer in this stream is actually used in
  // practice.
  const itemA = item({ id: "item_A", status: "inProgress", reasoningSummaries: [["chunk-a"]] });
  const turnWithA = { ...turn, items: [itemA] };
  const { rerender } = render(<TurnBlock turn={turnWithA} />);
  expect(screen.getByTestId("streaming-text").textContent).toBe("chunk-a");

  const itemB = item({ id: "item_B", status: "inProgress", reasoningSummaries: [["chunk-b"]] });
  const turnWithB = { ...turn, items: [itemB] };
  rerender(<TurnBlock turn={turnWithB} />);
  expect(screen.queryByText("chunk-a")).toBeNull();
  expect(screen.getByTestId("streaming-text").textContent).toBe("chunk-b");
});

test("both live and settled render inside the same stable wrapper testid", () => {
  const { container, rerender } = render(
    <ThinkBlock item={item({ reasoningSummaries: [["x"]] })} turn={turn} live={true} />,
  );
  expect(container.querySelector('[data-testid="think-block"]')).toBeTruthy();
  rerender(<ThinkBlock item={item({ reasoningSummaries: [["x"]] })} turn={turn} live={false} />);
  expect(container.querySelector('[data-testid="think-block"]')).toBeTruthy();
});
