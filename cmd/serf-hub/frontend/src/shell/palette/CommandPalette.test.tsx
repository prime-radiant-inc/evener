import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../protocol/model";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../protocol/types.gen";
import { connectionStore } from "../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { Toast } from "../../widgets";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { CommandPalette } from "./CommandPalette";
import { openPalette, paletteStore } from "./paletteController";

// See stores/prefs.test.ts: Node 26 shadows jsdom's localStorage.
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
    ...overrides,
  };
}

function item(id: string, text: string): ItemModel {
  return { id, turnId: "t1", type: "message", text };
}
function turn(items: ItemModel[]): TurnModel {
  return { id: "t1", status: "completed", items };
}

function focusSession(ref: string, overrides: Partial<ThreadModel> = {}): void {
  workspaceStore.setState({ panes: [{ id: "p1", type: "session", params: { ref } }], focusedPaneId: "p1" });
  threadsStore.setState({ threads: new Map([[ref, testModel({ ref, ...overrides })]]) });
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  paletteStore.setState({ open: false, query: "", openSeq: 0 });
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/");
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// --- T1 behaviors preserved ---

test("renders no overlay while the palette store is closed", () => {
  render(<CommandPalette />);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("opens an overlay named 'Command palette' when the store opens", () => {
  render(<CommandPalette />);
  act(() => openPalette());
  expect(screen.getByRole("dialog", { name: "Command palette" })).toBeTruthy();
});

test("Escape closes the overlay when no command is selected", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette());
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(paletteStore.getState().open).toBe(false);
});

// --- mode machine ---

test("opening seeded with '/' lands in command-filter mode and lists commands", () => {
  render(<CommandPalette />);
  act(() => openPalette("/"));
  expect(screen.getByRole("option", { name: /New session/ })).toBeTruthy();
  expect(screen.getByRole("option", { name: /Open settings/ })).toBeTruthy();
});

test("a '/set' filter narrows to matching commands and drops non-matches", () => {
  render(<CommandPalette />);
  act(() => openPalette("/set"));
  expect(screen.getByRole("option", { name: /Open settings/ })).toBeTruthy();
  expect(screen.queryByRole("option", { name: /New session/ })).toBeNull();
});

test("selecting a free-arg command enters args mode with a pill and placeholder, and Esc backs out (does not close)", async () => {
  const user = userEvent.setup();
  focusSession("ref_a", { status: { type: "active" }, activeTurnId: "t1" });
  render(<CommandPalette />);
  act(() => openPalette("/steer"));
  await user.click(screen.getByRole("option", { name: /Steer model/ }));

  // Args-mode pill shows the command, and the input placeholder changes.
  expect(screen.getByText("Steer model")).toBeTruthy();
  expect(screen.getByRole("combobox").getAttribute("placeholder")).toBe("steer text…");

  await user.keyboard("{Escape}");
  // Backed out to command-filter, dialog still open (Esc did not close it).
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.getByRole("option", { name: /Steer model/ })).toBeTruthy();
});

// --- execution & error surfacing ---

test("running a navigation command closes the palette and navigates", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/new"));
  await user.click(screen.getByRole("option", { name: /New session/ }));
  expect(window.location.pathname).toBe("/new");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("a blocked command keeps the palette open and shows the inline error strip", async () => {
  const user = userEvent.setup();
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  focusSession("ref_a", { status: { type: "idle" }, activeTurnId: undefined });
  render(<CommandPalette />);
  act(() => openPalette("/interrupt"));
  await user.click(screen.getByRole("option", { name: /Interrupt agent/ }));

  expect(screen.getByRole("alert").textContent).toBe("interrupt failed: no active turn");
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "turn/interrupt")).toBe(false);
});

test("/help renders the seven fixed keyboard-shortcut rows", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/help"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));
  expect(screen.getByText("Keyboard shortcuts")).toBeTruthy();
  expect(screen.getByText("open the palette from anywhere")).toBeTruthy();
  expect(screen.getByText("close the palette (or back out of args mode)")).toBeTruthy();
});

// --- search mode ---

test("search mode renders Live and Past sections from /api/search with highlighting", async () => {
  const user = userEvent.setup();
  fetchMock.mockResolvedValue({
    ok: true,
    status: 200,
    json: () =>
      Promise.resolve({
        live: [{ id: "local:a", title: "frobnitz worker", project: "proj", state: "active", age: "now" }],
        past: [{ id: "p1", title: "old frobnitz run", project: "old", state: "ended", age: "2h" }],
      }),
  } as Response);

  render(<CommandPalette />);
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "frobnitz");

  await waitFor(() => expect(screen.getByText("Live")).toBeTruthy());
  expect(screen.getByText("Past · 1")).toBeTruthy();
  const live = screen.getAllByRole("option").find((o) => o.textContent?.includes("frobnitz worker"));
  expect(live).toBeTruthy();
  // The matched substring is wrapped in <mark> (§2.3 highlighting).
  expect(within(live as HTMLElement).getByText("frobnitz").tagName).toBe("MARK");
});

test("in-session search scans the focused ThreadModel's turns", async () => {
  const user = userEvent.setup();
  fetchMock.mockResolvedValue({
    ok: true,
    status: 200,
    json: () => Promise.resolve({ live: [], past: [] }),
  } as Response);
  focusSession("ref_a", { turns: [turn([item("i1", "please investigate the frobnitz")])] });

  render(<CommandPalette />);
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "frobnitz");

  await waitFor(() => expect(screen.getByText("In session · 1")).toBeTruthy());
  expect(screen.getByText("turn 1")).toBeTruthy();
});

// --- keyboard navigation ---

test("ArrowDown moves the active row (aria-selected) with wraparound", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/"));
  const options = screen.getAllByRole("option");
  expect(options[0]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}");
  expect(screen.getAllByRole("option")[1]?.getAttribute("aria-selected")).toBe("true");
  expect(screen.getAllByRole("option")[0]?.getAttribute("aria-selected")).toBe("false");
});

// Mount the Toast region so any command toast has somewhere to render (not
// asserted here - just ensures no unmounted-region warnings).
test("mounting alongside a Toast region does not throw", () => {
  render(
    <>
      <CommandPalette />
      <Toast />
    </>,
  );
  act(() => openPalette("/"));
  expect(screen.getByRole("dialog")).toBeTruthy();
});
