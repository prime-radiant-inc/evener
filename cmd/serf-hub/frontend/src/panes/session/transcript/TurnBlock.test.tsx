import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { memo } from "react";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { isItemLive, TurnBlock } from "./TurnBlock";
import { type ItemRenderProps, ignoringTurn, itemRendererFor, registerItemRenderer } from "./types";
import "./tools";

// See TurnSeparator.test.tsx's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global, so
// every test file that touches localStorage needs this same small in-memory
// stand-in. TurnBlock reads the transcript visibility prefs.
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

// A tool row's open/closed state lives in the shared disclosureStore keyed by
// item.id, so a row this file opens must not leak into another test's row of the
// same id.
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "somethingUnregistered", text: "", ...overrides };
}

function turn(items: ItemModel[], overrides: Partial<TurnModel> = {}): TurnModel {
  return { id: "turn_1", status: "inProgress", items, ...overrides };
}

test("isItemLive: inProgress is live", () => {
  expect(isItemLive(item({ status: "inProgress" }))).toBe(true);
});

test("isItemLive: completed is not live", () => {
  expect(isItemLive(item({ status: "completed" }))).toBe(false);
});

test("isItemLive: an item with no status at all is not live", () => {
  expect(isItemLive(item({ status: undefined }))).toBe(false);
});

test("renders an empty turn without crashing", () => {
  const { container } = render(<TurnBlock turn={turn([])} />);
  expect(container.querySelector('[data-testid="turn-block"]')).toBeTruthy();
});

test("tags the root with the turn id", () => {
  const { container } = render(<TurnBlock turn={turn([], { id: "turn_42" })} />);
  expect(container.querySelector('[data-turn-id="turn_42"]')).toBeTruthy();
});

test("the turn root remains a centered, shrinkable reading column", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "turnblock.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toMatch(
    /\.turn\s*\{[\s\S]*width:\s*100%;[\s\S]*max-width:\s*var\(--session-measure\);[\s\S]*margin-inline:\s*auto;/,
  );
});

test("showSeenDivider defaults to false: no divider marker rendered", () => {
  render(<TurnBlock turn={turn([])} />);
  expect(screen.queryByTestId("seen-divider")).toBeNull();
});

test("showSeenDivider renders the divider marker above this turn's content", () => {
  render(<TurnBlock turn={turn([], { id: "turn_42" })} showSeenDivider />);
  expect(screen.getByTestId("seen-divider")).toBeTruthy();
});

test("renders items in order via the item-renderer registry", () => {
  const items = [
    item({ id: "a", type: "userMessage", text: "first" }),
    item({ id: "b", type: "agentMessage", text: "second" }),
    item({ id: "c", type: "userMessage", text: "third" }),
  ];
  render(<TurnBlock turn={turn(items)} />);
  // With the messages renderers registered (TurnBlock imports "./messages"
  // for TurnSeparator, whose module registers them as a side effect), these
  // types no longer fall back to the raw view - so assert ordering on the
  // block's text, renderer-agnostic: the registry must dispatch each item
  // in wire order regardless of which component won the type.
  const text = screen.getByTestId("turn-block").textContent ?? "";
  const positions = ["first", "second", "third"].map((t) => text.indexOf(t));
  expect(positions.every((p) => p >= 0)).toBe(true);
  expect([...positions]).toEqual([...positions].sort((a, b) => a - b));
});

test("dispatches a registered item type to its own renderer instead of the raw fallback", () => {
  function DummyRenderer({ item: i }: ItemRenderProps) {
    return <div data-testid="dummy-rendered">{i.text}</div>;
  }
  registerItemRenderer("tb-dummy-type", DummyRenderer);
  const items = [item({ id: "a", type: "tb-dummy-type", text: "via dummy" })];
  render(<TurnBlock turn={turn(items)} />);
  expect(screen.getByTestId("dummy-rendered").textContent).toBe("via dummy");
  expect(screen.queryByTestId("raw-item")).toBeNull();
});

test("dispatches a commandExecution item to ToolCallItem", () => {
  const items = [item({ id: "a", type: "commandExecution", toolName: "tb-tool-x", output: "tool output" })];
  render(<TurnBlock turn={turn(items)} />);
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();
  // A tool row starts collapsed and now mounts its body only while open, so the
  // output is proof of dispatch only once the row is opened. The dispatch itself
  // is what this test is about; the row above is already evidence of it, and the
  // output confirms the descriptor's body ran rather than an empty shell.
  fireEvent.click(screen.getByTestId("tool-row"));
  expect(screen.getByText("tool output")).toBeTruthy();
});

test("groups a settled non-final tool run behind its highest-consequence summary and keeps one row per call", () => {
  const items = [
    item({
      id: "read-a",
      type: "commandExecution",
      toolName: "read_file",
      argumentsJSON: JSON.stringify({ file_path: "src/cache.go" }),
      status: "completed",
    }),
    item({
      id: "write",
      type: "commandExecution",
      toolName: "write_file",
      argumentsJSON: JSON.stringify({ file_path: "src/cache.go" }),
      status: "completed",
    }),
    item({
      id: "read-b",
      type: "commandExecution",
      toolName: "read_file",
      argumentsJSON: JSON.stringify({ file_path: "src/cache.go" }),
      status: "completed",
    }),
    item({ id: "reply", type: "agentMessage", text: "tests green" }),
  ];
  render(<TurnBlock turn={turn(items)} />);

  const cluster = screen.getByTestId("tool-call-cluster") as HTMLDetailsElement;
  expect(cluster.open).toBe(false);
  expect(screen.getAllByTestId("tool-call-cluster")).toHaveLength(1);
  expect(screen.getAllByTestId("tool-row")).toHaveLength(1);
  expect(cluster.textContent).toContain("3 steps");
  expect(cluster.textContent).toContain("Wrote src/cache.go");
  expect(screen.queryAllByTestId("tool-call-item")).toHaveLength(0);

  fireEvent.click(cluster.querySelector("summary")!);
  expect(cluster.open).toBe(true);
  expect(screen.getByTestId("tool-call-cluster-body")).toBeTruthy();
  expect(screen.getAllByTestId("tool-call-item")).toHaveLength(3);
});

test("a cluster closes when the same virtualized turn and item ids switch sessions", () => {
  const sessionAItems = [
    item({
      id: "shared-a",
      type: "commandExecution",
      toolName: "tb-session-tool",
      argumentsJSON: JSON.stringify({ file_path: "session-a.txt" }),
      output: "session A content",
      status: "completed",
    }),
    item({
      id: "shared-b",
      type: "commandExecution",
      toolName: "tb-session-tool",
      argumentsJSON: JSON.stringify({ file_path: "session-a.txt" }),
      output: "session A content",
      status: "completed",
    }),
    item({
      id: "shared-c",
      type: "commandExecution",
      toolName: "tb-session-tool",
      argumentsJSON: JSON.stringify({ file_path: "session-a.txt" }),
      output: "session A content",
      status: "completed",
    }),
    item({ id: "shared-reply", type: "agentMessage", text: "session A reply" }),
  ];
  const sessionBItems = sessionAItems.map((entry) =>
    entry.type === "commandExecution"
      ? { ...entry, output: "session B content" }
      : { ...entry, text: "session B reply" },
  );
  const sharedTurn = (items: ItemModel[]) => turn(items, { id: "shared-turn" });

  const { rerender } = render(<TurnBlock turn={sharedTurn(sessionAItems)} sessionRef="session_a" />);
  const cluster = screen.getByTestId("tool-call-cluster") as HTMLDetailsElement;
  fireEvent.click(cluster.querySelector("summary")!);
  expect(cluster.open).toBe(true);
  expect(screen.getByTestId("tool-call-cluster-body")).toBeTruthy();
  expect(screen.getAllByTestId("tool-call-item")).toHaveLength(3);

  rerender(<TurnBlock turn={sharedTurn(sessionBItems)} sessionRef="session_b" />);

  const switchedCluster = screen.getByTestId("tool-call-cluster") as HTMLDetailsElement;
  expect(switchedCluster.open).toBe(false);
  expect(screen.queryByTestId("tool-call-cluster-body")).toBeNull();
  expect(screen.queryAllByTestId("tool-call-item")).toHaveLength(0);
});

test("suppressed task_list views do not create an empty cluster", () => {
  const items = [
    item({ id: "view-a", type: "commandExecution", toolName: "task_list", argumentsJSON: '{"action":"view"}' }),
    item({ id: "view-b", type: "commandExecution", toolName: "task_list", argumentsJSON: '{"action":"view"}' }),
    item({ id: "view-c", type: "commandExecution", toolName: "task_list", argumentsJSON: '{"action":"view"}' }),
    item({ id: "view-reply", type: "agentMessage", text: "done" }),
  ];

  render(<TurnBlock turn={turn(items)} />);

  expect(screen.queryByTestId("tool-call-cluster")).toBeNull();
  expect(screen.queryByTestId("tool-call-cluster-body")).toBeNull();
  expect(screen.queryAllByTestId("tool-call-item")).toHaveLength(0);
});

test("computes live per item from its own status, passed through to the renderer", () => {
  function LiveEcho({ live }: ItemRenderProps) {
    return <span data-testid="live-echo">{String(live)}</span>;
  }
  registerItemRenderer("tb-live-echo", LiveEcho);
  const items = [
    item({ id: "a", type: "tb-live-echo", status: "inProgress" }),
    item({ id: "b", type: "tb-live-echo", status: "completed" }),
  ];
  render(<TurnBlock turn={turn(items)} />);
  const echoes = screen.getAllByTestId("live-echo").map((el) => el.textContent);
  expect(echoes).toEqual(["true", "false"]);
});

test("passes the owning turn through to each item renderer", () => {
  function TurnEcho({ turn: t }: ItemRenderProps) {
    return <span data-testid="turn-echo">{t.id}</span>;
  }
  registerItemRenderer("tb-turn-echo", TurnEcho);
  const items = [item({ id: "a", type: "tb-turn-echo" })];
  render(<TurnBlock turn={turn(items, { id: "turn_owner" })} />);
  expect(screen.getByTestId("turn-echo").textContent).toBe("turn_owner");
});

test("passes opensExchange and agentLabel through ItemRenderProps", () => {
  const seen: Array<{ opensExchange?: boolean; agentLabel?: string }> = [];
  const originalAgentMessageRenderer = itemRendererFor("agentMessage");
  try {
    registerItemRenderer("agentMessage", (props) => {
      seen.push({ opensExchange: props.opensExchange, agentLabel: props.agentLabel });
      return null;
    });
    const agentItem = { id: "a1", type: "agentMessage", text: "hi", status: "completed" };
    render(<TurnBlock turn={turn([agentItem])} exchangeOpeners={new Set(["a1"])} agentLabel="k3" />);
    expect(seen).toEqual([{ opensExchange: true, agentLabel: "k3" }]);
  } finally {
    registerItemRenderer("agentMessage", originalAgentMessageRenderer);
  }
});

// The exact mechanism wave-4 T5c wraps most registered item renderers with
// (ToolCallItem, RawItemView, and every registered messages/ renderer except
// SystemNoticeItem - see each's own registerItemRenderer call site): a
// renderer memoized with `memo(Component, ignoringTurn)` must not re-render
// when TurnBlock re-renders with a NEW turn object but the SAME item
// reference and the same live-determining status - exactly what a streaming
// delta targeting a DIFFERENT item in the same turn produces (reducer.ts's
// immutable-update discipline: only the delta's own item gets a new
// reference, every sibling item keeps its exact reference, but the
// enclosing TurnModel is rebuilt every time).
test("a renderer memoized with ignoringTurn does not re-render when only the enclosing turn object changes (same item, same live)", () => {
  let renderCount = 0;
  const MemoEcho = memo(function MemoEcho({ item: i }: ItemRenderProps) {
    renderCount += 1;
    return <span data-testid="memo-echo">{i.text}</span>;
  }, ignoringTurn);
  registerItemRenderer("tb-memo-echo", MemoEcho);

  const sharedItem = item({ id: "a", type: "tb-memo-echo", text: "stable", status: "completed" });
  const { rerender } = render(<TurnBlock turn={turn([sharedItem], { id: "turn_1" })} />);
  expect(renderCount).toBe(1);
  expect(screen.getByTestId("memo-echo").textContent).toBe("stable");

  // A brand-new turn object (different id, different reference) - the SAME
  // item reference and thus the same computed `live` - must not re-invoke
  // MemoEcho's render function.
  rerender(<TurnBlock turn={turn([sharedItem], { id: "turn_2" })} />);
  expect(renderCount).toBe(1);
  expect(screen.getByTestId("memo-echo").textContent).toBe("stable");
});

// --- Settings -> Transcript visibility toggles ------------------------------
// TurnBlock applies them to the turn the renderers receive, so a hidden item
// is gone before SystemNoticeItem computes its consecutive-run grouping.

function systemItem(id: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId: "turn_1", type: "systemMessage", text: `notice ${id}`, ...overrides };
}

function hookItem(id: string, exitCode: number): ItemModel {
  return systemItem(id, { eventKind: "hook_completed", text: `hook ${id} exit ${exitCode}`, exitCode });
}

test("with both hook toggles off (the default), a hook exit line is not rendered", () => {
  render(<TurnBlock turn={turn([hookItem("h", 0), hookItem("i", 1)])} />);
  expect(screen.queryByText(/hook h exit 0/)).toBeNull();
  expect(screen.queryByText(/hook i exit 1/)).toBeNull();
});

test("hookExitsAll renders hook exits of every code; hookExitsNormal renders only exit 0", () => {
  const items = [hookItem("clean", 0), hookItem("failed", 1)];
  const { rerender } = render(<TurnBlock turn={turn(items)} />);

  act(() => prefsStore.getState().setTranscriptStatus("hookExitsAll", true));
  rerender(<TurnBlock turn={turn(items)} />);
  expect(screen.getByText(/hook clean exit 0/)).toBeTruthy();
  expect(screen.getByText(/hook failed exit 1/)).toBeTruthy();

  act(() => {
    prefsStore.getState().setTranscriptStatus("hookExitsAll", false);
    prefsStore.getState().setTranscriptStatus("hookExitsNormal", true);
  });
  rerender(<TurnBlock turn={turn(items)} />);
  expect(screen.getByText(/hook clean exit 0/)).toBeTruthy();
  expect(screen.queryByText(/hook failed exit 1/)).toBeNull();
});

test("flipping a toggle re-renders the transcript live, without a new turn object", () => {
  const items = [hookItem("h", 0)];
  const sameTurn = turn(items);
  render(<TurnBlock turn={sameTurn} />);
  expect(screen.queryByText(/hook h exit 0/)).toBeNull();

  act(() => prefsStore.getState().setTranscriptStatus("hookExitsAll", true));
  expect(screen.getByText(/hook h exit 0/)).toBeTruthy();
});

// The reason filtering happens in TurnBlock rather than inside each renderer:
// SystemNoticeItem groups a run of 3+ ADJACENT systemMessage items into one
// disclosure whose summary counts them. Hiding an item any later would leave
// the survivors wrongly grouped and the count overstated.
test("a hidden item is excluded from system-run grouping, not merely from the output", () => {
  const items = [systemItem("a"), hookItem("h", 1), systemItem("c")];
  render(<TurnBlock turn={turn(items)} />);

  // Three adjacent system items minus the hidden hook leaves two - below the
  // grouping threshold, so each must stand alone and no group may appear.
  expect(screen.queryByTestId("system-notice-group")).toBeNull();
  expect(screen.getByText("notice a")).toBeTruthy();
  expect(screen.getByText("notice c")).toBeTruthy();
});

test("the same three items DO group once the hidden one is shown again", () => {
  const items = [systemItem("a"), hookItem("h", 1), systemItem("c")];
  act(() => prefsStore.getState().setTranscriptStatus("hookExitsAll", true));
  render(<TurnBlock turn={turn(items)} />);

  const group = screen.getByTestId("system-notice-group");
  expect(group.textContent).toContain("3 system events");
});

// Sets the pref both ways explicitly rather than leaning on its default: this
// asserts what the toggle DOES, and must keep passing whichever way the default
// happens to point.
test("promptLoaded off hides the system-prompt scaffold disclosure; on shows it", () => {
  const items = [systemItem("p", { eventKind: "system_prompt", text: "You are a helpful assistant." })];
  act(() => prefsStore.getState().setTranscriptStatus("promptLoaded", false));
  const { rerender } = render(<TurnBlock turn={turn(items)} />);
  expect(screen.queryByTestId("system-notice-scaffold")).toBeNull();

  act(() => prefsStore.getState().setTranscriptStatus("promptLoaded", true));
  rerender(<TurnBlock turn={turn(items)} />);
  expect(screen.getByTestId("system-notice-scaffold").textContent).toContain("System prompt");
});

test("items no toggle governs are untouched with every toggle off", () => {
  const items = [
    item({ id: "u", type: "userMessage", text: "hello" }),
    systemItem("s", { eventKind: "skill_activated", text: "Activated skill: x" }),
  ];
  render(<TurnBlock turn={turn(items)} />);
  const text = screen.getByTestId("turn-block").textContent ?? "";
  expect(text).toContain("hello");
  expect(text).toContain("Activated skill: x");
});
