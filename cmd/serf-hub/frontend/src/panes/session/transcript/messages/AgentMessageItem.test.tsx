import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { ignoringTurn, itemRendererFor } from "../types";
import { AgentMessageItem } from "./AgentMessageItem";

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

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

test("exchange-opening agent message shows a visible eyebrow with the model label", () => {
  render(
    <AgentMessageItem item={item({ text: "the reply" })} turn={turn} live={false} opensExchange agentLabel="k3" />,
  );
  const root = screen.getByTestId("agent-message-item");
  expect(root.dataset.opensExchange).toBe("true");
  expect(root.firstElementChild!.textContent).toBe("Agent · k3");
});

test("continuation fragments render no eyebrow", () => {
  render(<AgentMessageItem item={item({ text: "more work" })} turn={turn} live={false} />);
  const root = screen.getByTestId("agent-message-item");
  expect(root.dataset.opensExchange).toBeUndefined();
  expect(root.textContent).not.toContain("Agent");
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
