import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import rawStreamingStyles from "../streamingtext.module.css";
import { TurnBlock } from "../TurnBlock";
import { itemRendererFor } from "../types";
import { ThinkBlock } from "./ThinkBlock";
import rawThinkStyles from "./thinkblock.module.css";

const STREAMING_LIVE = requireClass(rawStreamingStyles.live, "streamingtext.module.css", "live");

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

function thinkCss(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "thinkblock.module.css");
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "reasoning", text: "", ...overrides };
}

test('self-registers under the wire\'s reasoning item type ("reasoning")', () => {
  expect(itemRendererFor("reasoning")).toBe(ThinkBlock);
});

test("is memoized: the comparator ignores turn identity but tracks the current-thought flip", () => {
  expect(ThinkBlock.$$typeof).toBe(Symbol.for("react.memo"));
  const compare = (ThinkBlock as unknown as { compare: (a: unknown, b: unknown) => boolean }).compare;
  const think = item({ id: "cmp_think", status: "inProgress", reasoningSummaries: [["x"]] });
  const later: ItemModel = { id: "cmp_later", turnId: "turn_1", type: "agentMessage", text: "", status: "inProgress" };
  const base = { item: think, turn: { ...turn, items: [think] }, live: true };
  // A fresh turn object with the thought still the tail: no re-render - the
  // whole point of ignoring turn identity on unrelated deltas survives.
  expect(compare(base, { ...base, turn: { ...turn, items: [think] } })).toBe(true);
  // A later item lands: the current-thought flip MUST re-render, or the
  // superseded thought would stay open in its draft state forever (the wire
  // never completes a reasoning item mid-turn).
  expect(compare(base, { ...base, turn: { ...turn, items: [think, later] } })).toBe(false);
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

// Jesse's review call: a blinking caret inside a read-only reasoning view
// reads as an edit cursor. Liveness is carried by the "Thinking…" eyebrow
// and the visibly growing text, so ThinkBlock mounts StreamingText with the
// caret class OFF even while the stream is live. (Agent prose keeps the
// caret - it is the design system's reserved streaming cue THERE.)
test("live streaming shows NO blinking caret", () => {
  render(<ThinkBlock item={item({ reasoningSummaries: [["streaming"]] })} turn={turn} live={true} />);
  expect(screen.getByTestId("streaming-text").classList.contains(STREAMING_LIVE)).toBe(false);
});

// The layout contract behind the second review pass: both the live view and
// the expanded settled view are stacked rows (eyebrow/label line, then a
// full-width body), never a label-beside-body column that squeezes the
// thought into a narrow offset strip.
test("live stacks the label above the streaming body - no flex gutter", () => {
  expect(thinkCss()).not.toMatch(/\.live\s*\{[^}]*display:\s*flex/);
});

// --- the kind icon: the thought row leads with the lightbulb ----------------
// Jesse's review call: "thinking should also have an icon" - the same
// leading-icon grammar the tool rows use (widgets/toolicon).

test("the live eyebrow leads with the thought icon, decorative only", () => {
  render(<ThinkBlock item={item({ reasoningSummaries: [["streaming"]] })} turn={turn} live={true} />);
  const icon = screen.getByTestId("think-block-icon");
  expect(icon.getAttribute("aria-hidden")).toBe("true");
  expect(icon.querySelector("svg")).not.toBeNull();
});

test("the settled summary leads with the thought icon, ahead of the label text", () => {
  render(<ThinkBlock item={item({ reasoningSummaries: [["done thinking"]] })} turn={turn} live={false} />);
  const summary = document.querySelector("summary");
  const icon = screen.getByTestId("think-block-icon");
  expect(summary?.contains(icon)).toBe(true);
  expect(summary?.firstElementChild).toBe(icon);
});

test("the thought icon is a centred flex-none glyph (baseline alignment would seat it on its bottom edge)", () => {
  const css = thinkCss();
  const rule = /\.icon\s*\{([^}]*)\}/.exec(css);
  expect(rule).not.toBeNull();
  expect(rule![1]).toContain("flex: none");
  expect(rule![1]).toContain("align-self: center");
});

test("the thought icon rails at 50% opacity in the speaker-avatar column, gutter-pulled only above the breakpoint", () => {
  const css = thinkCss();
  const rule = /\.icon\s*\{([^}]*)\}/.exec(css);
  expect(rule).not.toBeNull();
  expect(rule![1]).toContain("opacity: 0.5");
  expect(rule![1]).toContain("width: var(--speaker-avatar-size)");
  expect(rule![1]).toContain("margin-right: var(--speaker-gap)");
  // The pull is on the CONTAINERS, never on the icon: .summary's overflow:
  // hidden would clip a negatively-margined child away entirely (qrv1).
  expect(rule![1]).not.toContain("margin-left: calc(-1 * var(--speaker-gutter))");
  const mediaRule = /@media \(min-width: 700px\) \{\s*\.label,\s*\.summary\s*\{([^}]*)\}/.exec(css);
  expect(mediaRule).not.toBeNull();
  expect(mediaRule![1]).toContain("margin-left: calc(-1 * var(--speaker-gutter))");
});

test("an expanded thought renders as a full-width row below its summary - no column chrome", () => {
  const css = thinkCss();
  // No [open] flex row squeezing the body beside the label...
  expect(css).not.toMatch(/\.details\[open\]\s*\{[^}]*display:\s*flex/);
  // ...and no left border/inset making the body an offset column.
  const body = /\.body\s*\{([^}]*)\}/.exec(css);
  expect(body).not.toBeNull();
  expect(body![1]).not.toContain("border-left");
  expect(body![1]).not.toContain("padding-left");
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

// --- superseded live thoughts settle --------------------------------------
// The wire never emits item/completed for a reasoning item (TurnBlock's
// isItemLive comment defers the per-type nuance to this renderer): once
// anything later starts in the turn, the thought is no longer the current
// activity, and with the live view now CAPPED, leaving it live would hide
// the head of the thought for the rest of the turn. Tail position - not
// wire status - is what "still thinking" means.

test("an inProgress reasoning item that is no longer the turn's tail renders the settled disclosure, with no invented duration", () => {
  const think = item({
    id: "think_superseded",
    status: "inProgress",
    reasoningSummaries: [["done reasoning\n\nfinal line"]],
  });
  const later: ItemModel = {
    id: "msg_after",
    turnId: "turn_1",
    type: "agentMessage",
    text: "answering",
    status: "inProgress",
  };
  render(<ThinkBlock item={think} turn={{ ...turn, items: [think, later] }} live={true} />);
  const summary = document.querySelector("summary");
  expect(summary?.textContent).toBe("Thought · final line");
  expect(summary?.textContent).not.toMatch(/\d/);
  expect(screen.queryByText("Thinking…")).toBeNull();
});

test("an inProgress reasoning item that IS the turn's tail stays live", () => {
  const think = item({ id: "think_tail", status: "inProgress", reasoningSummaries: [["still going"]] });
  render(<ThinkBlock item={think} turn={{ ...turn, items: [think] }} live={true} />);
  expect(screen.getByText("Thinking…")).toBeTruthy();
  expect(document.querySelector("details")).toBeNull();
});

test("through TurnBlock: a later item landing in the turn collapses the live thought to its disclosure", () => {
  const think = item({ id: "think_flow", status: "inProgress", reasoningSummaries: [["deep thought line"]] });
  const { rerender } = render(<TurnBlock turn={{ ...turn, items: [think] }} />);
  expect(screen.getByText("Thinking…")).toBeTruthy();

  const later: ItemModel = {
    id: "msg_next",
    turnId: "turn_1",
    type: "agentMessage",
    text: "now answering",
    status: "inProgress",
  };
  rerender(<TurnBlock turn={{ ...turn, items: [think, later] }} />);
  expect(screen.queryByText("Thinking…")).toBeNull();
  expect(document.querySelector('[data-testid="think-block"] summary')?.textContent).toBe(
    "Thought · deep thought line",
  );
});

// --- live: the draft treatment (mockup #4) ----------------------------------
// In-flight reasoning reads as a DRAFT - italic while streaming, settling to
// roman - and the whole of it is on screen: the thought stays OPEN and
// unbounded for as long as it runs (Jesse, bh8h), scrolling with the
// transcript like every other growing item.

test("the live body carries the draft treatment - italic in flight, roman once settled (declaration-level)", () => {
  const css = thinkCss();
  const rule = /\.liveBody\s*\{([^}]*)\}/.exec(css);
  expect(rule).not.toBeNull();
  expect(rule![1]).toContain("font-style: italic");
  // Nothing re-italicizes the settled views: summary and body stay roman.
  expect(css).not.toMatch(/\.body\s*\{[^}]*font-style/);
  expect(css).not.toMatch(/\.summary\s*\{[^}]*font-style/);
});

test("a running thought is not height-bounded - the whole block is readable as it streams", () => {
  const css = thinkCss();
  // Every rule the live subtree wears, checked for a cap of any shape: a
  // max-height, a line clamp, or a clipping box to hide an overflow inside.
  const liveRules = [...css.matchAll(/\.(live|liveBody|liveScroll|paragraph)\s*\{([^}]*)\}/g)];
  expect(liveRules.length).toBeGreaterThanOrEqual(3);
  for (const [, name, declarations] of liveRules) {
    expect(`.${name} {${declarations}}`).not.toMatch(/max-height|line-clamp|overflow:\s*hidden/);
  }
  // And no element between the italic wrapper and the paragraphs to clip them.
  render(<ThinkBlock item={item({ reasoningSummaries: [["first\n", "second"]] })} turn={turn} live={true} />);
  const body = screen.getByTestId("think-block-live-body");
  expect(screen.queryByTestId("think-block-live-scroll")).toBeNull();
  expect(body.querySelector("p")?.parentElement).toBe(body);
  expect(body.textContent).toBe("first\nsecond");
});

test("the live wrapper carries its stylesheet class - the italic actually binds to the DOM", () => {
  render(<ThinkBlock item={item({ reasoningSummaries: [["bound"]] })} turn={turn} live={true} />);
  const body = screen.getByTestId("think-block-live-body");
  expect(body.classList.contains(requireClass(rawThinkStyles.liveBody, "thinkblock.module.css", "liveBody"))).toBe(
    true,
  );
});

test("no fade and no clip flag over the live draft - nothing is cut off to mark", () => {
  const css = thinkCss();
  expect(css).not.toMatch(/data-clipped/);
  expect(css).not.toMatch(/\.liveBody(\[[^\]]*\])?::before/);
  render(<ThinkBlock item={item({ reasoningSummaries: [["plain"]] })} turn={turn} live={true} />);
  expect(screen.getByTestId("think-block-live-body").dataset.clipped).toBeUndefined();
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

test("opening the disclosure drops the preview from the summary", () => {
  const think = item({
    id: "think_open_preview",
    reasoningSummaries: [["First line of reasoning\n\nsecond paragraph"]],
  });
  render(<ThinkBlock item={think} turn={turn} live={false} />);
  const summary = screen.getByText(/Thought/);
  const preview = summary.textContent?.split("·")[1]?.trim() ?? "";
  expect(preview).toBeTruthy();
  fireEvent.click(summary);
  expect(summary.textContent).not.toContain(preview);
});

test("the open disclosure keeps the dot-joined duration label - never the old 'for' phrasing", () => {
  render(
    <ThinkBlock
      item={item({
        id: "think_open_duration",
        reasoningSummaries: [["content"]],
        startedAt: "2026-01-01T00:00:00.000Z",
        completedAt: "2026-01-01T00:00:04.000Z",
      })}
      turn={turn}
      live={false}
    />,
  );
  const summary = document.querySelector("summary");
  expect(summary?.textContent).toBe("Thought · 4s · content");
  fireEvent.click(summary!);
  expect(summary?.textContent).toBe("Thought · 4s");
});

// --- the trailing chevron: mockup #4's disclosure affordance ----------------
// The draft restyle's collapsed line is "close to today plus a chevron" - the
// same shared widgets/chevron every disclosure uses, riding at the tail of the
// summary and turning 90° when open (ToolRow's data-open idiom).

test("the settled summary trails with a chevron that tracks the open state", () => {
  render(
    <ThinkBlock
      item={item({ id: "think_chevron", reasoningSummaries: [["deep thought"]] })}
      turn={turn}
      live={false}
    />,
  );
  const summary = document.querySelector("summary");
  const chevron = screen.getByTestId("think-block-chevron");
  expect(chevron.getAttribute("aria-hidden")).toBe("true");
  expect(chevron.querySelector("svg")).not.toBeNull();
  expect(summary?.lastElementChild).toBe(chevron);
  expect(chevron.dataset.open).toBe("false");
  fireEvent.click(summary!);
  expect(chevron.dataset.open).toBe("true");
});

test("the live eyebrow carries no chevron - there is nothing to disclose while streaming", () => {
  render(<ThinkBlock item={item({ reasoningSummaries: [["streaming"]] })} turn={turn} live={true} />);
  expect(screen.queryByTestId("think-block-chevron")).toBeNull();
});

test("the chevron turns via the shared rotate-on-open idiom, scoped to data-open (declaration-level, jsdom runs no cascade)", () => {
  expect(thinkCss()).toMatch(/\.chevron\[data-open="true"\]\s*>\s*svg\s*\{[^}]*transform:\s*rotate\(90deg\)/);
});

test("the summary text ellipsizes in its own span so the trailing chevron is never pushed out of view", () => {
  const rule = /\.summaryText\s*\{([^}]*)\}/.exec(thinkCss());
  expect(rule).not.toBeNull();
  expect(rule![1]).toContain("overflow: hidden");
  expect(rule![1]).toContain("text-overflow: ellipsis");
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
  expect(document.querySelector("summary")?.textContent).toBe("Thought · 4s · content");
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
  expect(document.querySelector("summary")?.textContent).toBe("Thought · 3s · content");
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
  expect(document.querySelector("summary")?.textContent).toBe("Thought · 4s · content");
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
  expect(document.querySelector("summary")?.textContent).toBe("Thought · 250ms · content");
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
  expect(screen.queryByText(/Thought/)).toBeNull();
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
// A `display` value set on a <details> element REPLACES the UA's skipped-
// contents mechanism that hides a collapsed details' body - so any
// display:flex/grid on .details (the old gutter row had one, scoped to
// [open]) is both the column layout the second review pass removed AND a
// potential body-leak-while-collapsed if its scope is ever dropped. The
// invariant is now total: .details never sets display at all, the UA hides
// the collapsed body natively. jsdom evaluates no cascade, so this asserts
// the declaration - comments stripped first so the rule cannot match its
// own prose.
test("a collapsed thought cannot leak its body - .details never overrides the UA's native hiding", () => {
  expect(thinkCss()).not.toMatch(/\.details(\[open\])?\s*\{[^}]*display:/);
});
