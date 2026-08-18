import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { prefsStore, resetPrefsStoreForTests } from "../../../../stores/prefs";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { TurnBlock } from "../TurnBlock";
import { itemRendererFor } from "../types";
import { formatCharCount } from "./format";
import { SystemNoticeItem } from "./SystemNoticeItem";

// See TurnSeparator.test.tsx's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global. These
// tests render through TurnBlock, which reads the transcript visibility prefs.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  localStorage.clear();
  resetPrefsStoreForTests();
});

// The prompt-loaded setting gates whether a system-prompt item reaches a
// renderer at all (transcriptVisibility.ts). These tests are about how such an
// item RENDERS once shown, so they opt it in; visibility itself is covered by
// transcriptVisibility.test.ts and TurnBlock.test.tsx.
function showSystemPrompt() {
  prefsStore.getState().setTranscriptStatus("promptLoaded", true);
}

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

function item(id: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId: "turn_1", type: "systemMessage", text: `notice ${id}`, ...overrides };
}

function turnWith(items: ItemModel[]): TurnModel {
  return { id: "turn_1", status: "completed", items };
}

test('self-registers under the wire\'s system-message item type ("systemMessage")', () => {
  expect(itemRendererFor("systemMessage")).toBe(SystemNoticeItem);
});

// --- below the grouping threshold: each item stands alone -------------------
// Parity: contracts-transcript-scroll-liveness.md #12 ("fewer than 3
// adjacent lifecycle events do not coalesce - each renders as its own
// visible block").

test("a single systemMessage item (run of 1) renders as its own standalone quiet line", () => {
  const a = item("a");
  render(<TurnBlock turn={turnWith([a])} />);
  expect(screen.getByText("notice a")).toBeTruthy();
  expect(screen.queryByTestId("system-notice-group")).toBeNull();
});

test("two consecutive systemMessage items (run of 2) both render standalone, not grouped", () => {
  const a = item("a");
  const b = item("b");
  render(<TurnBlock turn={turnWith([a, b])} />);
  expect(screen.getByText("notice a")).toBeTruthy();
  expect(screen.getByText("notice b")).toBeTruthy();
  expect(screen.queryByTestId("system-notice-group")).toBeNull();
});

// --- 3+ consecutive: one collapsed group ------------------------------------

test("three consecutive systemMessage items group into one collapsed disclosure", () => {
  const items = [item("a"), item("b"), item("c")];
  render(<TurnBlock turn={turnWith(items)} />);
  const group = screen.getByTestId("system-notice-group") as HTMLDetailsElement;
  expect(group.tagName).toBe("DETAILS");
  expect(group.open).toBe(false);
  // Only ONE group renders, not three - the non-first members contribute
  // nothing of their own.
  expect(screen.getAllByTestId("system-notice-group")).toHaveLength(1);
});

test("the same scaffold item id has independent disclosure state in different sessions", () => {
  showSystemPrompt();
  const shared = item("same_item", { eventKind: "system_prompt", text: "You are a helpful assistant." });
  render(
    <>
      <TurnBlock turn={turnWith([shared])} sessionRef="session_a" />
      <TurnBlock turn={turnWith([shared])} sessionRef="session_b" />
    </>,
  );

  const scaffolds = screen.getAllByTestId("system-notice-scaffold") as HTMLDetailsElement[];
  expect(scaffolds).toHaveLength(2);
  expect(scaffolds[0]?.open).toBe(false);
  expect(scaffolds[1]?.open).toBe(false);

  fireEvent.click(scaffolds[0]!.querySelector("summary")!);
  expect(scaffolds[0]?.open).toBe(true);
  expect(scaffolds[1]?.open).toBe(false);
});

test("the group's summary names the count and the first event", () => {
  const items = [item("a", { text: "first thing happened" }), item("b"), item("c")];
  render(<TurnBlock turn={turnWith(items)} />);
  const summary = screen.getByTestId("system-notice-group").querySelector("summary");
  expect(summary?.textContent).toBe("3 system events · first thing happened");
});

// yt2q: the grouped-system-events disclosure's open/closed state lives in the
// shared disclosureStore keyed by the run's first item id, so expanding it
// survives the remount that would reset a native uncontrolled <details>.
test("an expanded system-events group stays open across an unmount+remount (store-backed by the run's first item id)", () => {
  const items = [item("a"), item("b"), item("c")];
  const { unmount } = render(<TurnBlock turn={turnWith(items)} />);
  const group = screen.getByTestId("system-notice-group") as HTMLDetailsElement;
  expect(group.open).toBe(false);
  fireEvent.click(group.querySelector("summary")!);
  expect((screen.getByTestId("system-notice-group") as HTMLDetailsElement).open).toBe(true);

  unmount();
  render(<TurnBlock turn={turnWith(items)} />);
  expect((screen.getByTestId("system-notice-group") as HTMLDetailsElement).open).toBe(true);
});

test("expanding the group reveals every individual line", () => {
  const items = [item("a"), item("b"), item("c"), item("d")];
  render(<TurnBlock turn={turnWith(items)} />);
  expect(screen.getByText("notice a")).toBeTruthy();
  expect(screen.getByText("notice b")).toBeTruthy();
  expect(screen.getByText("notice c")).toBeTruthy();
  expect(screen.getByText("notice d")).toBeTruthy();
});

// --- a non-systemMessage entry between two runs breaks them apart ----------

test("a non-lifecycle entry between two systemMessage items breaks the run into two sub-threshold groups", () => {
  const items = [
    item("a"),
    item("b"),
    { id: "prose", turnId: "turn_1", type: "agentMessage", text: "hi" },
    item("c"),
    item("d"),
  ];
  render(<TurnBlock turn={turnWith(items)} />);
  // Neither side reaches 3, so no group renders at all - all four notices
  // stand alone.
  expect(screen.queryByTestId("system-notice-group")).toBeNull();
  expect(screen.getByText("notice a")).toBeTruthy();
  expect(screen.getByText("notice b")).toBeTruthy();
  expect(screen.getByText("notice c")).toBeTruthy();
  expect(screen.getByText("notice d")).toBeTruthy();
});

// --- blank text fallback -----------------------------------------------------

test("a systemMessage item with blank text falls back to a sentence-case category label, never an invisible row", () => {
  render(<TurnBlock turn={turnWith([item("a", { text: "" })])} />);
  expect(screen.getByTestId("system-notice-line").textContent).toBe("System event");
});

// --- scaffold classification by typed eventKind (kata ckgw) ------------------
// Scaffolding (the system prompt, a compaction summary) is classified by the
// wire's typed ThreadItem.eventKind discriminator, NOT by a text-length
// heuristic. A short-text scaffold item still gets the disclosure; a long
// non-scaffold notice stays a quiet line.

test("a system_prompt eventKind item renders as a collapsed scaffold disclosure, even when its text is short", () => {
  showSystemPrompt();
  render(<TurnBlock turn={turnWith([item("a", { text: "short", eventKind: "system_prompt" })])} />);
  const scaffold = screen.getByTestId("system-notice-scaffold") as HTMLDetailsElement;
  expect(scaffold.tagName).toBe("DETAILS");
  expect(scaffold.open).toBe(false);
  expect(scaffold.querySelector("summary")?.textContent).toBe("System prompt · 5 chars");
});

test("an expanded system prompt keeps its summary and Markdown body in full-width rows", () => {
  showSystemPrompt();
  const text = `## Identity\n\n${"You are a careful assistant. ".repeat(1000)}`;
  render(<TurnBlock turn={turnWith([item("prompt", { text, eventKind: "system_prompt" })])} />);
  const scaffold = screen.getByTestId("system-notice-scaffold") as HTMLDetailsElement;
  const summary = scaffold.querySelector("summary");
  const body = scaffold.querySelector('[data-testid="system-notice-scaffold-body"]');
  expect(summary?.textContent).toBe(`System prompt · ${formatCharCount(text.length)}`);
  expect(scaffold.open).toBe(false);
  expect(scaffold.querySelectorAll("details")).toHaveLength(0);
  expect(scaffold.children).toHaveLength(2);
  expect(scaffold.children[0]).toBe(summary);
  expect(scaffold.children[1]).toBe(body);
  expect(summary?.tagName).toBe("SUMMARY");
  expect(summary?.getAttribute("role")).toBeNull();

  fireEvent.click(summary!);
  expect(scaffold.open).toBe(true);
  expect(body?.querySelector("h2")?.textContent).toBe("Identity");
  expect(body?.textContent).toContain("You are a careful assistant.");
});

test("the system prompt scaffold CSS makes the summary and body block rows", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "systemnoticeitem.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (selector: string): string => {
    const match = css.match(new RegExp(`\\.${selector}\\s*\\{([^}]*)\\}`));
    return match?.[1] ?? "";
  };
  expect(rule("scaffold")).toContain("display: block");
  expect(rule("scaffold")).not.toContain("display: flex");
  expect(rule("scaffoldSummary")).toContain("display: list-item");
  expect(rule("scaffoldSummary")).toContain("min-width: 0");
  expect(rule("scaffoldSummary")).toContain("max-width: 100%");
  expect(rule("scaffoldBody")).toContain("width: 100%");
});

test("a compaction eventKind item renders as a scaffold disclosure", () => {
  render(<TurnBlock turn={turnWith([item("a", { text: "kept context", eventKind: "compaction" })])} />);
  expect(screen.getByTestId("system-notice-scaffold")).toBeTruthy();
  expect(screen.queryByTestId("system-notice-line")).toBeNull();
});

test("a long systemMessage notice with no scaffold eventKind stays a plain quiet line (no char-count heuristic)", () => {
  const wall = "x".repeat(2000);
  render(<TurnBlock turn={turnWith([item("a", { text: wall })])} />);
  expect(screen.getByTestId("system-notice-line")).toBeTruthy();
  expect(screen.queryByTestId("system-notice-scaffold")).toBeNull();
});

// --- a turn failure is marked, not quiet (kata 0wb6) -------------------------
// A reloaded turn failure arrives as a systemMessage item with the typed
// "error" eventKind. Every other system notice is lifecycle churn a reader
// scrolls past; this one is the thing readers hunt for, so it carries the same
// row-level mark a failed tool call does and full-contrast ink.

function failureItem(overrides: Partial<ItemModel> = {}): ItemModel {
  return item("boom", {
    text: "openai error (status=401): incorrect API key",
    description: "Turn failed",
    error: "openai error (status=401): incorrect API key",
    status: "failed",
    eventKind: "error",
    ...overrides,
  });
}

function failedTurn(items: ItemModel[]): TurnModel {
  return { id: "turn_1", status: "failed", items, error: { message: "openai error (status=401)" } };
}

test("a turn failure renders the failure mark, not a quiet lifecycle line", () => {
  render(<TurnBlock turn={turnWith([failureItem()])} />);
  expect(screen.getByTestId("system-notice-failure")).toBeTruthy();
  expect(screen.queryByTestId("system-notice-line")).toBeNull();
  expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
});

test("a turn failure carries the same urgent-anchor marker a failed tool row does", () => {
  render(<TurnBlock turn={turnWith([failureItem()])} />);
  expect(screen.getByTestId("system-notice-failure").getAttribute("data-attention")).toBe("error");
});

test("an ordinary system notice carries no attention marker and no failure mark", () => {
  render(<TurnBlock turn={turnWith([item("a", { text: "model switched to gpt-5.4" })])} />);
  expect(screen.queryByTestId("system-notice-failure")).toBeNull();
  expect(screen.getByTestId("system-notice-line").getAttribute("data-attention")).toBeNull();
});

// The end cap beneath restates the message with its taxonomy chip, hint and
// recovery action. The row says WHAT happened; the cap says the detail. Saying
// the same sentence twice, ten pixels apart, is what the reloaded failure did
// before.
test("with the end cap present the failure row names the event instead of repeating the message", () => {
  render(<TurnBlock turn={failedTurn([failureItem()])} />);
  expect(screen.getByTestId("system-notice-failure").textContent).toBe("Turn failed");
  // The message itself still appears exactly once, in the end cap.
  expect(screen.getAllByText(/openai error \(status=401\)/)).toHaveLength(1);
});

// A client or daemon that carries the item without a turn-level error has no
// cap to lean on, so the row must carry the message itself. A failure is never
// left unstated.
test("with no end cap the failure row carries the message itself", () => {
  render(<TurnBlock turn={turnWith([failureItem()])} />);
  expect(screen.getByTestId("system-notice-failure").textContent).toBe("openai error (status=401): incorrect API key");
});

test("a failure with neither description nor text names the event rather than filing it as a generic notice", () => {
  render(<TurnBlock turn={failedTurn([failureItem({ text: "", description: "" })])} />);
  expect(screen.getByTestId("system-notice-failure").textContent).toBe("Turn failed");
  expect(screen.queryByText("System event")).toBeNull();
});

test("with no end cap and no message, the row falls back to the wire's own description", () => {
  render(<TurnBlock turn={turnWith([failureItem({ text: "", description: "Provider rejected the request" })])} />);
  expect(screen.getByTestId("system-notice-failure").textContent).toBe("Provider rejected the request");
});

test("a failure among lifecycle notices is never folded into their collapsed group", () => {
  const items = [item("a"), item("b"), failureItem(), item("c"), item("d")];
  render(<TurnBlock turn={turnWith(items)} />);
  expect(screen.getByTestId("system-notice-failure")).toBeTruthy();
  // Two runs of two remain either side; neither reaches the grouping threshold,
  // so nothing collapses and no summary can describe the failure away.
  expect(screen.queryByTestId("system-notice-group")).toBeNull();
});

test("a failure adjacent to a group that does collapse is left outside it, and uncounted", () => {
  const items = [item("a"), item("b"), item("c"), failureItem()];
  render(<TurnBlock turn={turnWith(items)} />);
  expect(screen.getByTestId("system-notice-failure")).toBeTruthy();
  const summary = screen.getByTestId("system-notice-group").querySelector("summary");
  expect(summary?.textContent).toBe("3 system events · notice a");
});

// The stable item_system_prompt id remains a narrow fallback for a system
// prompt projected by an older daemon that predates the typed eventKind, so a
// heterogeneous-version relay still collapses it correctly.
test("the item_system_prompt id still classifies as scaffold when the wire carries no eventKind (old-daemon fallback)", () => {
  showSystemPrompt();
  render(<TurnBlock turn={turnWith([item("item_system_prompt", { text: "You are Serf." })])} />);
  expect(screen.getByTestId("system-notice-scaffold")).toBeTruthy();
  expect(screen.getByTestId("system-notice-scaffold").querySelector("summary")?.textContent).toBe(
    "System prompt · 13 chars",
  );
});

// --- round timings redesign (kata 7zkv) ------------------------------------
// A round_timings item's raw dump ("Round 0 total=6.411312958s
// llm=4.935822084s context=8.625µs ...") answered "where did this round go"
// only if a reader did the arithmetic themselves. It renders ONE quiet line
// leading with the dominant phase - no disclosure (Jesse's review call on
// the tiered-density follow-up) - with the full per-phase breakdown on the
// line's hover title, built from item.raw's structured numbers, not by
// re-parsing that prose.

function showRoundTimings() {
  prefsStore.getState().setTranscriptStatus("roundTimings", true);
}

function roundTimingsItem(id: string, fields: Record<string, number> = {}): ItemModel {
  return item(id, {
    eventKind: "round_timings",
    text: "Round 0 total=6.411312958s llm=4.935822084s context=8.625µs tools=1.462408667s prompt=83ns history=12.5µs tool_defs=0s persistence=8.742792ms after_action=500ns overhead=4.317707ms",
    raw: {
      roundTimings: {
        round: 0,
        total_round_ns: 6_411_312_958,
        llm_call_ns: 4_935_822_084,
        context_mgmt_ns: 8_625,
        tool_exec_ns: 1_462_408_667,
        system_prompt_ns: 83,
        history_expand_ns: 12_500,
        tool_defs_ns: 0,
        persistence_ns: 8_742_792,
        after_action_ns: 500,
        loop_overhead_ns: 4_317_707,
        ...fields,
      },
    },
  });
}

test("a round_timings item with structured raw renders ONE quiet line naming the dominant phase - no disclosure, not the raw dump", () => {
  showRoundTimings();
  render(<TurnBlock turn={turnWith([roundTimingsItem("rt")])} />);
  expect(screen.getByTestId("system-notice-line").textContent).toBe("Round 0 · 6.4s — LLM 4.9s (77%)");
  expect(screen.queryByTestId("system-notice-timings")).toBeNull();
  expect(screen.queryByText(/total=6\.411312958s/)).toBeNull();
});

test("the full phase breakdown rides the line's hover title, sub-1ms phases folded into an omitted count", () => {
  showRoundTimings();
  render(<TurnBlock turn={turnWith([roundTimingsItem("rt")])} />);
  const title = screen.getByTestId("system-notice-line").getAttribute("title") ?? "";
  expect(title).toContain("LLM 4.9s (77%)");
  expect(title).toContain("Tools 1.5s (23%)");
  expect(title).toContain("Persistence 9ms (<1%)");
  expect(title).toContain("Overhead 4ms (<1%)");
  // context (8.625µs), prompt (83ns), history (12.5µs), after_action (500ns)
  // all round under 1ms; folded into a count, not shown as false "1ms" rows.
  expect(title).toContain("+ 4 phases under 1ms");
  expect(title).not.toContain("Context");
  expect(title).not.toContain("Prompt");
});

test("a round_timings item renders nothing expandable - no details element, no disclosure state to persist", () => {
  showRoundTimings();
  const { container } = render(<TurnBlock turn={turnWith([roundTimingsItem("rt")])} />);
  expect(container.querySelector("details")).toBeNull();
  expect(container.querySelector("summary")).toBeNull();
});

test("a round_timings item with no raw (older daemon) falls back to the plain prose line", () => {
  showRoundTimings();
  const oldItem = item("rt", { eventKind: "round_timings", text: "Round 0 total=1.5s llm=1.2s" });
  render(<TurnBlock turn={turnWith([oldItem])} />);
  expect(screen.getByTestId("system-notice-line").textContent).toBe("Round 0 total=1.5s llm=1.2s");
  expect(screen.queryByTestId("system-notice-timings")).toBeNull();
});

test("a round_timings item whose raw fails to narrow (malformed) falls back to the plain prose line", () => {
  showRoundTimings();
  const badItem = item("rt", {
    eventKind: "round_timings",
    text: "Round 0 total=1.5s",
    raw: { roundTimings: "garbage" },
  });
  render(<TurnBlock turn={turnWith([badItem])} />);
  expect(screen.getByTestId("system-notice-line").textContent).toBe("Round 0 total=1.5s");
});
