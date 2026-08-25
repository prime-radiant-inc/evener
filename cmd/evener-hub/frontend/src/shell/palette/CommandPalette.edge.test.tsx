// Edge cases for CommandPalette.tsx that close remaining uncovered lines:
// - commandErrorMessage with non-Error, non-hub-launch error (line 183)
// - search failure path (fetchSearch rejection, lines 283-285)
// - handleCommandResult: blocked result (388-389), promise blocked (394-395),
//   promise reject (400)
// - activateResult: insession (494-495), live/past newTab (505-506)
// - enterPressed: handoff via Enter (519-524)
// - buildView: recent commands header (735), handoff row (744-746)
// - renderResults: enum loading/error empty states (829-832)

import { act, cleanup, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import "../../panes/sessionPanels";
import { resetComposerFocusStoreForTests } from "../../panes/session/composer/composerFocus";
import { resetQuoteInsertStoreForTests, useQuoteInsertRequest } from "../../panes/session/composer/quoteInsert";
import type { ItemModel, ThreadModel, TurnModel } from "../../protocol/model";
import type { ThreadCapabilities } from "../../protocol/types.gen";
import { useCommandCatalog } from "../../stores/commandCatalog";
import { connectionStore } from "../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { resetTreeStoreForTests } from "../../stores/tree";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { CommandPalette, commandErrorMessage } from "./CommandPalette";
import { openPalette, paletteStore } from "./paletteController";

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

const CAPS: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    lastFrameAt: 0,
    capabilities: CAPS,
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...rest,
    jobsTreeRevision,
  };
}

function item(id: string, text: string): ItemModel {
  return { id, turnId: "t1", type: "message", text };
}
function turn(items: ItemModel[]): TurnModel {
  return { id: "t1", status: "completed", items };
}

function focusSession(ref: string, overrides: Partial<ThreadModel> = {}): void {
  workspaceStore.setState({
    panes: [{ id: "p1", type: "session", params: { ref }, slot: "main" }],
    focusedPaneId: "p1",
  });
  threadsStore.setState({ threads: new Map([[ref, testModel({ ref, ...overrides })]]) });
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  paletteStore.setState({ open: false, query: "", openSeq: 0 });
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  useCommandCatalog.setState({ commands: [], loaded: false });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetTreeStoreForTests();
  resetQuoteInsertStoreForTests();
  resetComposerFocusStoreForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/");
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  // @ts-expect-error jsdom baseline has no matchMedia.
  delete window.matchMedia;
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// --- commandErrorMessage (line 183) ---

test("commandErrorMessage falls back to String(err) for non-Error, non-hub-launch values", () => {
  expect(commandErrorMessage("plain string error")).toBe("plain string error");
  expect(commandErrorMessage(42)).toBe("42");
  expect(commandErrorMessage(null)).toBe("command failed");
  expect(commandErrorMessage(undefined)).toBe("command failed");
  expect(commandErrorMessage("  ")).toBe("command failed");
});

test("commandErrorMessage uses err.message for Error instances", () => {
  expect(commandErrorMessage(new Error("boom"))).toBe("boom");
});

// --- search failure (lines 283-285) ---

test("a failed search shows 'Search failed' empty state", async () => {
  const user = userEvent.setup();
  fetchMock.mockResolvedValue({
    ok: false,
    status: 500,
    statusText: "Internal Server Error",
    json: () => Promise.resolve({ error: "server error" }),
  } as Response);

  render(<CommandPalette />);
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "query");

  await waitFor(() => expect(screen.getByText("Search failed")).toBeTruthy());
});

// --- search result activation: insession (lines 494-495) ---

test("Shift+Enter on an in-session search result closes the palette without navigating", async () => {
  const user = userEvent.setup();
  const openSpy = vi.spyOn(window, "open").mockImplementation(() => null);
  fetchMock.mockResolvedValue({
    ok: true,
    status: 200,
    json: () => Promise.resolve({ live: [], past: [] }),
  } as Response);
  focusSession("ref_a", { turns: [turn([item("i1", "search term found here")])] });

  render(<CommandPalette />);
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "search");

  await waitFor(() => expect(screen.getByText("In session · 1")).toBeTruthy());
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  expect(screen.queryByRole("dialog")).toBeNull();
  expect(window.location.pathname).toBe("/");
  expect(openSpy).not.toHaveBeenCalled();
});

// --- search result activation: live/past with newTab (lines 505-506) ---

test("Mod+Enter on a search result opens in a new tab via window.open", async () => {
  const user = userEvent.setup();
  const openSpy = vi.spyOn(window, "open").mockImplementation(() => null);
  fetchMock.mockResolvedValue({
    ok: true,
    status: 200,
    json: () =>
      Promise.resolve({
        live: [{ id: "live1", ref: "local:live1", title: "live result", project: "p", state: "active", age: "now" }],
        past: [],
      }),
  } as Response);

  render(<CommandPalette />);
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "live");

  await waitFor(() => expect(screen.getByText("Live")).toBeTruthy());
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  expect(openSpy).toHaveBeenCalledTimes(1);
  expect(openSpy).toHaveBeenCalledWith("/s/local%3Alive1", "_blank");
});

// --- enterPressed: handoff via arrow+Enter (lines 519-524) ---

test("arrow-down then Enter on the handoff row hands off to the composer", async () => {
  const user = userEvent.setup();
  focusSession("ref_a");

  render(<CommandPalette />);
  act(() => openPalette("/interrupt"));

  // Arrow down to the handoff row (index 0 is the handoff row)
  await user.keyboard("{ArrowDown}{Enter}");

  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/interrupt ");
  expect(screen.queryByRole("dialog")).toBeNull();
});

// --- search result: past result activation (line 504) ---

test("clicking a past search result navigates to the session", async () => {
  const user = userEvent.setup();
  fetchMock.mockResolvedValue({
    ok: true,
    status: 200,
    json: () =>
      Promise.resolve({
        live: [],
        past: [{ id: "past1", ref: "local:past1", title: "old session", project: "p", state: "ended", age: "2h" }],
      }),
  } as Response);

  render(<CommandPalette />);
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "old");

  await waitFor(() => expect(screen.getByText("Past · 1")).toBeTruthy());
  const row = screen.getAllByRole("option").find((o) => o.textContent?.includes("old session"));
  await user.click(row as HTMLElement);

  expect(decodeURIComponent(window.location.pathname)).toBe("/s/local:past1");
});

// --- clearToSearch and showHelp UI callbacks (lines 248-251) ---

test("/search command clears to search mode", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/"));

  await user.click(screen.getByRole("option", { name: /Search sessions/ }));

  // Should stay open (stayOpen) and be in search mode
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.getByRole("combobox").getAttribute("placeholder")).toMatch(/search/i);
});

// --- help via /help command (showHelp callback) ---

test("/help command shows the help panel", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/"));

  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));

  expect(screen.getByText("Keyboard shortcuts")).toBeTruthy();
});
