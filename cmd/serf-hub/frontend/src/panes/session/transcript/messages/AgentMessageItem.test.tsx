import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { ignoringTurn, itemRendererFor } from "../types";
import { AgentMessageItem } from "./AgentMessageItem";
import { formatClockTime } from "./format";

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

const STARTED_AT = "2026-07-29T12:41:00Z";
const TIME = formatClockTime(STARTED_AT)!;

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "agentMessage", text: "", ...overrides };
}

test('self-registers under the wire\'s agent-message item type ("agentMessage")', () => {
  expect(itemRendererFor("agentMessage")).toBe(AgentMessageItem);
});

test("is memoized ignoring turn identity - a fresh turn object on every streaming delta must not re-render an unrelated settled agent message", () => {
  expect(AgentMessageItem.$$typeof).toBe(Symbol.for("react.memo"));
  expect((AgentMessageItem as unknown as { compare: unknown }).compare).toBe(ignoringTurn);
});

// --- settled: Markdown ------------------------------------------------------

test("settled renders through Markdown - a markdown token actually gets parsed, not shown as literal source", () => {
  render(<AgentMessageItem item={item({ text: "**bold text**" })} turn={turn} live={false} />);
  const strong = screen.getByText("bold text");
  expect(strong.tagName).toBe("STRONG");
});

test("settled with blank text and no pendingText renders nothing at all", () => {
  const { container } = render(<AgentMessageItem item={item({ text: "" })} turn={turn} live={false} />);
  expect(container.firstChild).toBeNull();
});

// --- live: Markdown with auto-close ------------------------------------------
// Agent prose parses markdown WHILE streaming (Jesse, 2026-08-03), through
// the same Markdown widget as the settled path - with the widget's `live`
// flag, so constructs truncated at the stream tail are closed for the
// preview (widgets/markdown/streaming.ts).

test("live parses markdown WHILE streaming - emphasis renders as emphasis, not literal source", () => {
  const { container } = render(
    <AgentMessageItem item={item({ pendingText: ["**bold", " text**"] })} turn={turn} live={true} />,
  );
  expect(container.querySelector("strong")?.textContent).toBe("bold text");
  expect(screen.queryByTestId("streaming-text")).toBeNull();
});

test("live auto-closes a marker truncated at the stream tail - bold renders before its closer arrives", () => {
  const { container } = render(
    <AgentMessageItem item={item({ pendingText: ["the answer is **bo"] })} turn={turn} live={true} />,
  );
  expect(container.querySelector("strong")?.textContent).toBe("bo");
  expect(container.textContent).not.toContain("**");
});

test("auto-close is preview-only: a genuinely unterminated SETTLED source stays literal", () => {
  const { container } = render(
    <AgentMessageItem item={item({ text: "the answer is **bo" })} turn={turn} live={false} />,
  );
  expect(container.querySelector("strong")).toBeNull();
  expect(container.textContent).toContain("**bo");
});

test("live with no pendingText yet (item just started, zero deltas so far) renders nothing rather than an empty shell", () => {
  const { container } = render(<AgentMessageItem item={item({ pendingText: undefined })} turn={turn} live={true} />);
  expect(container.firstChild).toBeNull();
});

test("live with an empty pendingText array is treated the same as no pendingText", () => {
  const { container } = render(<AgentMessageItem item={item({ pendingText: [] })} turn={turn} live={true} />);
  expect(container.firstChild).toBeNull();
});

test("incremental growth: re-rendering with more pendingText chunks grows the streamed content without duplication", () => {
  const { rerender } = render(<AgentMessageItem item={item({ pendingText: ["Hel"] })} turn={turn} live={true} />);
  expect(screen.getByTestId("agent-message-stream").textContent?.trim()).toBe("Hel");
  rerender(<AgentMessageItem item={item({ pendingText: ["Hel", "lo"] })} turn={turn} live={true} />);
  expect(screen.getByTestId("agent-message-stream").textContent?.trim()).toBe("Hello");
});

// --- the live-to-settled transition: the canonical T1 pattern --------------
// Mirrors RawItemView.test.tsx's own "live streaming settles on the same
// instance without residue" test - every renderer in this stream follows
// this shape per the wave-4 T2 binding constraints.

test("live streaming settles cleanly: the stream wrapper unmounts, settled Markdown takes over, no duplicated content", () => {
  const liveItem = item({ pendingText: ["Hel", "lo"] });
  const { rerender } = render(<AgentMessageItem item={liveItem} turn={turn} live={true} />);
  expect(screen.getByTestId("agent-message-stream").textContent?.trim()).toBe("Hello");

  const settledItem = { ...liveItem, text: "Hello", pendingText: undefined };
  rerender(<AgentMessageItem item={settledItem} turn={{ ...turn, status: "completed" }} live={false} />);

  expect(screen.queryByTestId("agent-message-stream")).toBeNull();
  expect(screen.queryAllByText("Hello").length).toBe(1);
});

// --- the rapid-settle sequence: DOMAIN FINDING from T1's live run ----------
// This harness routes final answers through a one-shot communicate tool, so
// agentMessage streaming windows can be very short - a stream that starts
// and settles within one or two renders must still look right (no residue,
// no flash between the live and settled DOM shapes).

test("rapid-settle: a single-chunk live frame immediately followed by settlement still lands correctly", () => {
  const liveItem = item({ pendingText: ["Done."] });
  const { rerender } = render(<AgentMessageItem item={liveItem} turn={turn} live={true} />);
  expect(screen.getByTestId("agent-message-stream").textContent?.trim()).toBe("Done.");

  const settledItem = { ...liveItem, text: "Done.", pendingText: undefined };
  rerender(<AgentMessageItem item={settledItem} turn={{ ...turn, status: "completed" }} live={false} />);
  expect(screen.queryByTestId("agent-message-stream")).toBeNull();
  expect(screen.getByText("Done.")).toBeTruthy();
});

test("rapid-settle: settling WITHOUT ever having rendered live (turn/completed arrives with no prior delta frame) renders straight to Markdown", () => {
  // TURN_COMPLETED-with-no-prior-END is a real wire case (parity #7,
  // idempotent-finalize) - the item can go from not-yet-rendered straight
  // to settled with full text, no live frame ever observed by this
  // component instance.
  render(<AgentMessageItem item={item({ text: "straight to settled" })} turn={turn} live={false} />);
  expect(screen.queryByTestId("agent-message-stream")).toBeNull();
  expect(screen.getByText("straight to settled")).toBeTruthy();
});

test("both live and settled render inside the same stable wrapper testid", () => {
  const { container, rerender } = render(
    <AgentMessageItem item={item({ pendingText: ["x"] })} turn={turn} live={true} />,
  );
  expect(container.querySelector('[data-testid="agent-message-item"]')).toBeTruthy();
  rerender(<AgentMessageItem item={item({ text: "x", pendingText: undefined })} turn={turn} live={false} />);
  expect(container.querySelector('[data-testid="agent-message-item"]')).toBeTruthy();
});

// --- the speaker header (slack-lean spec, decisions 1-2) -------------------
// Replaces the old caption eyebrow at exactly the same trigger: exchange
// boundaries only (opensExchange), in BOTH the live and settled branches.

test("exchange-opening agent message shows the speaker header: name, meta, and a decorative avatar", () => {
  render(
    <AgentMessageItem
      item={item({ text: "the reply", startedAt: STARTED_AT })}
      turn={turn}
      live={false}
      opensExchange
      agentLabel="k3"
    />,
  );
  const root = screen.getByTestId("agent-message-item");
  expect(root.dataset.opensExchange).toBe("true");
  const header = screen.getByTestId("agent-speaker-header");
  expect(within(header).getByText("Agent")).toBeTruthy();
  expect(header.textContent).toBe(`Agentk3 · ${TIME}`);
  // The avatar is decorative - the header names the speaker in words, so
  // exposing the tile would announce the same fact twice.
  const avatar = within(root).getByTestId("speaker-avatar");
  expect(avatar.getAttribute("aria-hidden")).toBe("true");
});

test("the speaker header renders in the LIVE branch too - the eyebrow appeared in both, and the header keeps that parity", () => {
  render(
    <AgentMessageItem
      item={item({ pendingText: ["streaming"], startedAt: STARTED_AT })}
      turn={turn}
      live={true}
      opensExchange
      agentLabel="k3"
    />,
  );
  const header = screen.getByTestId("agent-speaker-header");
  expect(header.textContent).toBe(`Agentk3 · ${TIME}`);
  expect(screen.getByTestId("agent-message-stream").textContent?.trim()).toBe("streaming");
});

test("the speaker header survives the live-to-settled transition on the same instance", () => {
  const liveItem = item({ pendingText: ["Hi"], startedAt: STARTED_AT });
  const { rerender } = render(
    <AgentMessageItem item={liveItem} turn={turn} live={true} opensExchange agentLabel="k3" />,
  );
  expect(screen.getByTestId("agent-speaker-header")).toBeTruthy();
  rerender(
    <AgentMessageItem
      item={{ ...liveItem, text: "Hi", pendingText: undefined }}
      turn={{ ...turn, status: "completed" }}
      live={false}
      opensExchange
      agentLabel="k3"
    />,
  );
  expect(screen.queryByTestId("agent-message-stream")).toBeNull();
  expect(screen.getByTestId("agent-speaker-header").textContent).toBe(`Agentk3 · ${TIME}`);
});

// Meta composition: "{label} · {time}", each part only when defined - never
// a dangling separator, and no meta element at all when both are absent.

test("meta with label only (no startedAt): just the label, no dangling separator", () => {
  render(<AgentMessageItem item={item({ text: "r" })} turn={turn} live={false} opensExchange agentLabel="k3" />);
  const header = screen.getByTestId("agent-speaker-header");
  expect(header.textContent).toBe("Agentk3");
  expect(header.textContent).not.toContain("·");
});

test("meta with time only (no agentLabel): just the time, no dangling separator", () => {
  render(<AgentMessageItem item={item({ text: "r", startedAt: STARTED_AT })} turn={turn} live={false} opensExchange />);
  const header = screen.getByTestId("agent-speaker-header");
  expect(header.textContent).toBe(`Agent${TIME}`);
  expect(header.textContent).not.toContain("·");
});

test("meta with neither label nor time: no meta element at all, just the name", () => {
  render(<AgentMessageItem item={item({ text: "r" })} turn={turn} live={false} opensExchange />);
  const header = screen.getByTestId("agent-speaker-header");
  expect(header.textContent).toBe("Agent");
  // One child: the name span only - an empty meta span would still carry
  // the flex gap and reserve visual space.
  expect(header.children.length).toBe(1);
});

test("meta with an unparseable startedAt drops the time rather than guessing", () => {
  render(
    <AgentMessageItem
      item={item({ text: "r", startedAt: "not a timestamp" })}
      turn={turn}
      live={false}
      opensExchange
      agentLabel="k3"
    />,
  );
  expect(screen.getByTestId("agent-speaker-header").textContent).toBe("Agentk3");
});

test("continuation fragments render no header, no avatar, no opens-exchange marker", () => {
  render(
    <AgentMessageItem
      item={item({ text: "more work", startedAt: STARTED_AT })}
      turn={turn}
      live={false}
      agentLabel="k3"
    />,
  );
  const root = screen.getByTestId("agent-message-item");
  expect(root.dataset.opensExchange).toBeUndefined();
  expect(screen.queryByTestId("agent-speaker-header")).toBeNull();
  expect(screen.queryByTestId("speaker-avatar")).toBeNull();
  expect(root.textContent).not.toContain("Agent");
});

test("mid-exchange LIVE fragments render no header either - the trigger is opensExchange, not liveness", () => {
  render(<AgentMessageItem item={item({ pendingText: ["work"] })} turn={turn} live={true} agentLabel="k3" />);
  expect(screen.queryByTestId("agent-speaker-header")).toBeNull();
  expect(screen.queryByTestId("speaker-avatar")).toBeNull();
});

// --- agent prose is the transcript's hero (kata 7pa0) -----------------------
// jsdom computes no cascade (it structurally cannot see a token step up the
// ramp), so this reads the three stylesheets' own source instead - the same
// technique markdown.test.tsx/StreamingText.test.tsx already use for the
// --markdown-ink hook and the reduced-motion caret. The point isn't any one
// file in isolation: it's that all three agree, which is exactly what keeps
// a message from visibly resizing the instant it settles.

test("sets --prose-font-size once, on the .message ancestor the live and settled children share", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "agentmessageitem.module.css"), "utf8");
  expect(css).not.toContain("--prose-font-size");
});

test("the agent message keeps .message a bare layout row - the bubble treatment lives on .bubble, not the row", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "agentmessageitem.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toMatch(/\.message\s*\{[\s\S]*padding:\s*var\(--space-1\)\s+0;/);
  expect(css).not.toMatch(/\.message\s*\{[\s\S]*--prose-font-size:/);
  expect(css).not.toMatch(/\.message\s*\{[^}]*background\s*:/);
  expect(css).not.toMatch(/\.message\s*\{[^}]*border\s*:/);
  expect(css).not.toMatch(/\.tag\s*\{/);
});

// --- the chat bubble (2026-07-30-transcript-chat-bubbles-design.md) --------
// Every fragment bubbles: opener tailed toward the avatar, continuations
// fully rounded, the SAME wrapper live and settled.

test("every fragment renders its prose inside the bubble wrapper, live and settled", () => {
  const { rerender } = render(<AgentMessageItem item={item({ pendingText: ["x"] })} turn={turn} live={true} />);
  const liveBubble = screen.getByTestId("agent-bubble");
  expect(liveBubble.contains(screen.getByTestId("agent-message-stream"))).toBe(true);
  rerender(<AgentMessageItem item={item({ text: "x", pendingText: undefined })} turn={turn} live={false} />);
  const settledBubble = screen.getByTestId("agent-bubble");
  expect(settledBubble.textContent).toContain("x");
});

test("an opener bubble sits in the column under the speaker header; a continuation bubbles with no header and no avatar", () => {
  render(
    <AgentMessageItem item={item({ text: "the reply" })} turn={turn} live={false} opensExchange agentLabel="k3" />,
  );
  const root = screen.getByTestId("agent-message-item");
  const header = screen.getByTestId("agent-speaker-header");
  const bubble = screen.getByTestId("agent-bubble");
  expect(header.compareDocumentPosition(bubble) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(root.className).not.toBe(bubble.className);
});

test("a continuation fragment's bubble carries the uniform-radius continuation class", () => {
  render(<AgentMessageItem item={item({ text: "more work" })} turn={turn} live={false} agentLabel="k3" />);
  const bubble = screen.getByTestId("agent-bubble");
  expect(bubble.className).toContain("continuation");
  expect(screen.queryByTestId("agent-speaker-header")).toBeNull();
});

test("the bubble fills are token color-mixes and the continuation radius is uniform", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "agentmessageitem.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const bubble = /\.bubble\s*\{([^}]*)\}/.exec(css);
  expect(bubble).not.toBeNull();
  expect(bubble![1]).toMatch(/background:\s*color-mix\(in oklab, var\(--ink-mid\)/);
  expect(bubble![1]).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
  expect(css).toMatch(/\.continuation\s*\{[^}]*border-radius:\s*var\(--radius-pane\);/);
});

// The live path renders through the SAME Markdown widget as the settled
// path (Jesse, 2026-08-03: streaming messages are markdown-rendered too),
// so size/ink parity no longer needs two stylesheets agreeing on a hook -
// it is the same component. What the live branch adds is the caret: the
// design system's one reserved streaming cue, now attached to the stream
// wrapper instead of StreamingText's .live class. jsdom computes no
// cascade, so the caret contract is checked at the declaration level, the
// same technique StreamingText.test.tsx uses for its own caret.

test("the live stream keeps the blinking caret, as an inline bar at the end of the last markdown block (declaration-level)", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "agentmessageitem.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const caret = /\.stream\s*>\s*div\s*>\s*:last-child::after\s*\{([^}]*)\}/.exec(css);
  expect(caret).not.toBeNull();
  expect(caret![1]).toContain("display: inline-block");
  expect(caret![1]).toContain("background: currentColor");
  expect(caret![1]).toContain("animation: streamingCaretBlink");
});

test("the stream caret's blink sits behind the reduced-motion gate, keeping the static bar (declaration-level)", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "agentmessageitem.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const gate = /@media\s*\(prefers-reduced-motion:\s*reduce\)\s*\{([\s\S]*?)\n\}/.exec(css);
  expect(gate).not.toBeNull();
  expect(gate![1]).toMatch(/\.stream\s*>\s*div\s*>\s*:last-child::after\s*\{[^}]*animation:\s*none/);
});
