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

// --- live: StreamingText, plain text - the fast-path law -------------------

test("live with pendingText chunks streams via StreamingText as plain text (markdown syntax NOT parsed while live)", () => {
  render(<AgentMessageItem item={item({ pendingText: ["**bo", "ld**"] })} turn={turn} live={true} />);
  const el = screen.getByTestId("streaming-text");
  expect(el.textContent).toBe("**bold**");
  expect(el.querySelector("strong")).toBeNull();
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
  expect(screen.getByTestId("streaming-text").textContent).toBe("Hel");
  rerender(<AgentMessageItem item={item({ pendingText: ["Hel", "lo"] })} turn={turn} live={true} />);
  expect(screen.getByTestId("streaming-text").textContent).toBe("Hello");
});

// --- the live-to-settled transition: the canonical T1 pattern --------------
// Mirrors RawItemView.test.tsx's own "live streaming settles on the same
// instance without residue" test - every renderer in this stream follows
// this shape per the wave-4 T2 binding constraints.

test("live streaming settles cleanly: StreamingText unmounts, Markdown takes over, no duplicated content", () => {
  const liveItem = item({ pendingText: ["Hel", "lo"] });
  const { rerender } = render(<AgentMessageItem item={liveItem} turn={turn} live={true} />);
  expect(screen.getByTestId("streaming-text").textContent).toBe("Hello");

  const settledItem = { ...liveItem, text: "Hello", pendingText: undefined };
  rerender(<AgentMessageItem item={settledItem} turn={{ ...turn, status: "completed" }} live={false} />);

  expect(screen.queryByTestId("streaming-text")).toBeNull();
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
  expect(screen.getByTestId("streaming-text").textContent).toBe("Done.");

  const settledItem = { ...liveItem, text: "Done.", pendingText: undefined };
  rerender(<AgentMessageItem item={settledItem} turn={{ ...turn, status: "completed" }} live={false} />);
  expect(screen.queryByTestId("streaming-text")).toBeNull();
  expect(screen.getByText("Done.")).toBeTruthy();
});

test("rapid-settle: settling WITHOUT ever having rendered live (turn/completed arrives with no prior delta frame) renders straight to Markdown", () => {
  // TURN_COMPLETED-with-no-prior-END is a real wire case (parity #7,
  // idempotent-finalize) - the item can go from not-yet-rendered straight
  // to settled with full text, no live frame ever observed by this
  // component instance.
  render(<AgentMessageItem item={item({ text: "straight to settled" })} turn={turn} live={false} />);
  expect(screen.queryByTestId("streaming-text")).toBeNull();
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
  expect(screen.getByTestId("streaming-text")).toBeTruthy();
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
  expect(screen.queryByTestId("streaming-text")).toBeNull();
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

test("the agent message keeps the approved unlabelled hero surface without a card frame", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "agentmessageitem.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toMatch(/\.message\s*\{[\s\S]*padding:\s*var\(--space-2\)\s+0;/);
  expect(css).not.toMatch(/\.message\s*\{[\s\S]*--prose-font-size:/);
  expect(css).not.toMatch(/\.message\s*\{[^}]*background\s*:/);
  expect(css).not.toMatch(/\.message\s*\{[^}]*border\s*:/);
  expect(css).not.toMatch(/\.tag\s*\{/);
});

test("Markdown and StreamingText read --prose-font-size with the identical fallback, so live and settled can never disagree on size", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const markdownCss = readFileSync(join(here, "../../../../widgets/markdown/markdown.module.css"), "utf8");
  const streamingCss = readFileSync(join(here, "../streamingtext.module.css"), "utf8");
  const HOOK = "font-size: var(--prose-font-size, var(--font-size-body));";
  expect(markdownCss).toContain(HOOK);
  expect(streamingCss).toContain(HOOK);
});
