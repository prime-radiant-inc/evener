import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { TurnBlock } from "../TurnBlock";
import { ignoringTurn, itemRendererFor } from "../types";
import { ThinkBlock } from "./ThinkBlock";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "reasoning", text: "", ...overrides };
}

test('self-registers under the wire\'s reasoning item type ("reasoning")', () => {
  expect(itemRendererFor("reasoning")).toBe(ThinkBlock);
});

test("is memoized ignoring turn identity - a fresh turn object on every streaming delta must not re-render an unrelated settled think block", () => {
  expect(ThinkBlock.$$typeof).toBe(Symbol.for("react.memo"));
  expect((ThinkBlock as unknown as { compare: unknown }).compare).toBe(ignoringTurn);
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

// jsdom does not evaluate CSS cascade, so the live quiet-ink contract is
// checked at the two stylesheet boundary points: StreamingText's fallback
// and ThinkBlock's ancestor override must use the same custom property.
test("ThinkBlock quiets live StreamingText through the shared prose ink hook", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const streamingCss = readFileSync(join(here, "../streamingtext.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const thinkCss = readFileSync(join(here, "thinkblock.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  expect(streamingCss).toContain("color: var(--prose-ink, var(--ink-hi));");
  expect(thinkCss).toContain("--prose-ink: var(--ink-mid);");
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

// --- settled: duration + final context --------------------------------------

test("settled collapses to a closed details with duration and the final nonblank context line", () => {
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
  expect(summary?.textContent).toBe("Thought · more reasoning");
});

test("the collapsed summary keeps the final context even though the expanded body remains complete", () => {
  const { container } = render(
    <ThinkBlock
      item={item({ reasoningSummaries: [["Delegating simple directory inspection task\n\nmore detail"]] })}
      turn={turn}
      live={false}
    />,
  );
  const summary = container.querySelector("summary");
  expect(summary?.textContent).toBe("Thought · more detail");
  expect(screen.getByText(/Delegating simple directory inspection task/)).toBeTruthy();
});

test("the plain context removes common Markdown decoration while the expanded body still parses Markdown", () => {
  const { container } = render(
    <ThinkBlock
      item={item({ reasoningSummaries: [["**Delegating simple directory inspection task**"]] })}
      turn={turn}
      live={false}
    />,
  );
  const summary = container.querySelector("summary");
  expect(summary?.textContent).toBe("Thought · Delegating simple directory inspection task");
  expect(summary?.textContent).not.toMatch(/\*/);
  expect(screen.getByText("Delegating simple directory inspection task").tagName).toBe("STRONG");
});

test("a long final context line is honestly truncated in the collapsed summary", () => {
  const longLine = `final context ${"x".repeat(180)}`;
  render(<ThinkBlock item={item({ reasoningSummaries: [[longLine]] })} turn={turn} live={false} />);
  const summary = document.querySelector("summary")?.textContent ?? "";
  expect(summary.endsWith("…")).toBe(true);
  expect(summary).not.toContain(longLine);
});

test("settled body holds the full reasoning text (not just the preview) once expanded", () => {
  render(<ThinkBlock item={item({ reasoningSummaries: [["full reasoning text here"]] })} turn={turn} live={false} />);
  expect(screen.getByText("full reasoning text here")).toBeTruthy();
});

// --- settled: markdown ------------------------------------------------------
// Agents write their reasoning in markdown, so the settled body parses it the
// same way AgentMessageItem parses a settled agent message.

test("settled renders the reasoning through Markdown - a heading token becomes a real heading, not literal source", () => {
  render(
    <ThinkBlock item={item({ reasoningSummaries: [["## The plan\n\nfigure it out"]] })} turn={turn} live={false} />,
  );
  expect(screen.getByRole("heading", { level: 2, name: "The plan" })).toBeTruthy();
  expect(screen.queryByText("## The plan")).toBeNull();
});

test("settled parses lists, emphasis and code spans in reasoning text", () => {
  const { container } = render(
    <ThinkBlock
      item={item({ reasoningSummaries: [["- check `serve.go`\n- then **commit**"]] })}
      turn={turn}
      live={false}
    />,
  );
  expect(Array.from(container.querySelectorAll("li")).map((li) => li.textContent?.trim())).toEqual([
    "check serve.go",
    "then commit",
  ]);
  expect(container.querySelector("code")?.textContent).toBe("serve.go");
  expect(container.querySelector("strong")?.textContent).toBe("commit");
});

test("settled joins every summaryIndex into ONE markdown document, blank-line separated so each stays its own block", () => {
  const { container } = render(
    <ThinkBlock
      item={item({ reasoningSummaries: [["first thought"], ["# second thought"]] })}
      turn={turn}
      live={false}
    />,
  );
  // Blank-line joined: the second index is parsed as its own block-level
  // token, not swallowed into the first index's paragraph.
  expect(container.querySelector("p")?.textContent).toBe("first thought");
  expect(screen.getByRole("heading", { level: 1, name: "second thought" })).toBeTruthy();
});

test("settled does not re-wrap markdown output in the live path's paragraph class (Markdown owns its own block layout)", () => {
  const { container } = render(
    <ThinkBlock item={item({ reasoningSummaries: [["plain thought"]] })} turn={turn} live={false} />,
  );
  const paragraph = container.querySelector("p");
  expect(paragraph?.textContent).toBe("plain thought");
  expect(paragraph?.className).toBe("");
});

// --- live stays plain text: the deliberate trade-off -------------------------
// A markdown parser needs a whole document; StreamingText's append-only DOM
// contract needs deltas. Live wins by staying unparsed - see ThinkBlock.tsx.

test("live streams markdown source as LITERAL text (no parsing per delta) - the append-only streaming contract wins while in flight", () => {
  const { container } = render(
    <ThinkBlock item={item({ reasoningSummaries: [["## heading and **bold**"]] })} turn={turn} live={true} />,
  );
  expect(screen.getByTestId("streaming-text").textContent).toBe("## heading and **bold**");
  expect(container.querySelector("h2")).toBeNull();
  expect(container.querySelector("strong")).toBeNull();
});

test("markdown in a thought is literal while live and parsed once it settles (same item, same text)", () => {
  const markdown = "## Decision\n\n- ship it";
  const liveItem = item({ reasoningSummaries: [[markdown]] });
  const { container, rerender } = render(<ThinkBlock item={liveItem} turn={turn} live={true} />);
  expect(container.querySelector("h2")).toBeNull();

  rerender(<ThinkBlock item={liveItem} turn={{ ...turn, status: "completed" }} live={false} />);
  expect(screen.getByRole("heading", { level: 2, name: "Decision" })).toBeTruthy();
  expect(container.querySelector("li")?.textContent?.trim()).toBe("ship it");
  expect(screen.queryByTestId("streaming-text")).toBeNull();
});

// yt2q: the settled think block's open/closed state lives in the shared
// disclosureStore keyed by item.id, so expanding it survives the remount that
// would reset a native uncontrolled <details>.
test("an expanded settled think block stays open across an unmount+remount with the same item id (store-backed)", () => {
  const think = item({ id: "item_think_remount", reasoningSummaries: [["deep thoughts here"]] });
  const { unmount } = render(<ThinkBlock item={think} turn={turn} live={false} />);
  const details = screen.getByRole("group") as HTMLDetailsElement;
  expect(details.open).toBe(false);
  fireEvent.click(details.querySelector("summary")!);
  expect((screen.getByRole("group") as HTMLDetailsElement).open).toBe(true);

  unmount();
  render(<ThinkBlock item={think} turn={turn} live={false} />);
  expect((screen.getByRole("group") as HTMLDetailsElement).open).toBe(true);
});

test("the same settled think item id has independent disclosure state in different sessions", () => {
  const shared = item({ id: "same_item", reasoningSummaries: [["deep thoughts here"]] });
  render(
    <>
      <ThinkBlock item={shared} turn={turn} live={false} sessionRef="session_a" />
      <ThinkBlock item={shared} turn={turn} live={false} sessionRef="session_b" />
    </>,
  );

  const blocks = screen.getAllByRole("group") as HTMLDetailsElement[];
  expect(blocks).toHaveLength(2);
  expect(blocks[0]?.open).toBe(false);
  expect(blocks[1]?.open).toBe(false);

  fireEvent.click(blocks[0]!.querySelector("summary")!);
  expect(blocks[0]?.open).toBe(true);
  expect(blocks[1]?.open).toBe(false);
});

test("no fabricated duration: without real item.startedAt/completedAt, the label omits a number entirely (never invents one)", () => {
  render(<ThinkBlock item={item({ reasoningSummaries: [["content"]] })} turn={turn} live={false} />);
  const summary = document.querySelector("summary");
  expect(summary?.textContent).toBe("Thought · content");
  expect(summary?.textContent).not.toMatch(/\d/);
});

test("a replay item's real startedAt/completedAt pair produces a duration label", () => {
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

test("a live-observed timing pair (no wire pair) produces a real duration label once settled", () => {
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

test("a completed sub-second thought reports milliseconds, not a rounded second", () => {
  render(
    <ThinkBlock
      item={item({
        reasoningSummaries: [["content"]],
        startedAt: "2026-01-01T00:00:00.000Z",
        completedAt: "2026-01-01T00:00:00.250Z",
      })}
      turn={turn}
      live={false}
    />,
  );
  expect(document.querySelector("summary")?.textContent).toBe("Thought for 250ms · content");
});

test("an in-progress streaming thought never shows a duration summary", () => {
  render(
    <ThinkBlock
      item={item({
        status: "inProgress",
        reasoningSummaries: [["still thinking"]],
        observedStartedAt: "2026-01-01T00:00:00.000Z",
        observedCompletedAt: undefined,
      })}
      turn={turn}
      live={true}
    />,
  );
  expect(screen.getByText("Thinking…")).toBeTruthy();
  expect(screen.queryByText(/Thought for/)).toBeNull();
  expect(document.querySelector("details")).toBeNull();
});

test("an in-progress item stays live even if a stale caller passes live=false", () => {
  render(
    <ThinkBlock item={item({ status: "inProgress", reasoningSummaries: [["still live"]] })} turn={turn} live={false} />,
  );
  expect(screen.getByText("Thinking…")).toBeTruthy();
  expect(document.querySelector("details")).toBeNull();
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

// A closed <details> hides its non-summary children through the UA's own
// skipped-contents mechanism, which any `display` set on the <details> element
// REPLACES. So the gutter layout must be scoped to [open]: unscoped, a
// collapsed thought leaks a sliver of its body text beside the label. jsdom
// evaluates no cascade, so this asserts the declaration rather than the pixels
// - the same stylesheet-source idiom toolRowGrammar.test.tsx uses, comments
// stripped first so the rule cannot match its own prose.
test("the settled gutter layout is scoped to [open], so a collapsed thought cannot leak its body", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "thinkblock.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    "",
  );
  expect(css).toMatch(/\.details\[open\]\s*\{[^}]*display:\s*flex/);
  expect(css).not.toMatch(/\.details\s*\{[^}]*display:\s*flex/);
});
