import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { prefsStore, resetPrefsStoreForTests } from "../../../../stores/prefs";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { TurnBlock } from "../TurnBlock";
import { itemRendererFor } from "../types";
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
