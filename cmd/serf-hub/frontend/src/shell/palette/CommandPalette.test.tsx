import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../protocol/errors";
import type { ItemModel, ThreadModel, TurnModel } from "../../protocol/model";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../protocol/types.gen";
import { useCommandCatalog } from "../../stores/commandCatalog";
import { connectionStore } from "../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { Toast } from "../../widgets";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { CommandPalette, commandErrorMessage } from "./CommandPalette";
import { openPalette, paletteStore } from "./paletteController";
import type { SearchResult } from "./search";

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

test("an async catalog refresh rerenders the open session palette", async () => {
  const fake = new FakeClient();
  fake.on("serf/command/list", async () => {
    await Promise.resolve();
    return { commands: [{ name: "review", pluginName: "p", source: "plugin" }] };
  });
  connectionStore.setState({ client: fake as never });
  focusSession("ref_a");
  render(<CommandPalette />);

  act(() => openPalette("/review"));

  await waitFor(() => expect(screen.getByRole("option", { name: /review \[plugin\]/ })).toBeTruthy());
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

// --- unavailable commands: present and explained, never hidden (kata zshh) ---

test("an ended session still lists /model, and it runs", async () => {
  const user = userEvent.setup();
  const fake = new FakeClient("ready");
  fake.on("model/list", () => ({ data: [{ provider: "openai", model: "gpt-5.5" }] }));
  fake.on("thread/model/set", () => ({}));
  connectionStore.getState().connect(fake);
  // pastEntryThread's advertisement for a cold exited thread.
  focusSession("ref_a", {
    status: { type: "ended" },
    capabilities: { ...CAPS, steer: false, interrupt: false, queue: false },
  });
  render(<CommandPalette />);
  act(() => openPalette("/model"));

  const row = screen.getByRole("option", { name: /Switch model/ });
  expect(row.getAttribute("aria-disabled")).toBeNull();
  await user.click(row);
  await user.click(await screen.findByRole("option", { name: /gpt-5\.5/ }));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/model/set")).toBe(true));
});

test("a command the wire refuses renders disabled with its reason, and choosing it explains itself", async () => {
  const user = userEvent.setup();
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  focusSession("ref_a", { status: { type: "ended" }, capabilities: { ...CAPS, steer: false } });
  render(<CommandPalette />);
  act(() => openPalette("/steer"));

  // Present, not hidden - a missing row is indistinguishable from one the user
  // misremembered, and it breaks a keyboard user's motor pattern.
  const row = screen.getByRole("option", { name: /Steer model/ });
  expect(row.getAttribute("aria-disabled")).toBe("true");
  expect(row.textContent).toContain("not available right now");

  await user.click(row);
  expect(screen.getByRole("alert").textContent).toBe("/steer is not available right now");
  // Did NOT enter args mode, and made no wire call.
  expect(screen.getByRole("combobox").getAttribute("placeholder")).not.toBe("steer text…");
  expect(fake.calls.some((c) => c.method === "turn/steer")).toBe(false);
});

test("a failed auto-resume is attributed to the session, not blamed on the command", () => {
  // The hub returns the spawner's own raw text under serfErrorInfo hubLaunch
  // when the resume behind a cold-session mutation fails; unprefixed it reads
  // as the command having failed.
  expect(
    commandErrorMessage(new WireError("fork/exec serf: no such file", -32014, { serfErrorInfo: "hubLaunch" })),
  ).toBe("couldn't start this session: fork/exec serf: no such file");
  // Every other rejection is passed through untouched.
  expect(commandErrorMessage(new WireError("turn t1 is active", -32013, { serfErrorInfo: "conflict" }))).toBe(
    "turn t1 is active",
  );
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

test("the help panel is inert: ArrowDown+Enter never fires a hidden registry command (§2.8)", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  // No session focused -> command-filter with the empty "/" filter lists the 8
  // global commands; the last is /upgrade (index 7). Showing help must not
  // leave that list navigable underneath, or ArrowDown+Enter fires /upgrade
  // invisibly (with no client it blocks, surfacing the error strip).
  act(() => openPalette("/"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));
  await user.keyboard("{ArrowDown}{Enter}");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.getByText("Keyboard shortcuts")).toBeTruthy();
});

test("typing after /help leaves the help panel and returns to a real command list (§2.8)", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));
  await user.type(screen.getByRole("combobox"), "settings");
  expect(screen.queryByText("Keyboard shortcuts")).toBeNull();
  expect(screen.getByRole("option", { name: /Open settings/ })).toBeTruthy();
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

// --- search-result navigation ---

// Typed against the real SearchResult, not Record<string, unknown>: `ref` is
// required now (see search.ts), and a fixture omitting it would be describing
// a response the hub cannot produce.
async function searchAndClick(user: ReturnType<typeof userEvent.setup>, result: SearchResult, term: string) {
  fetchMock.mockResolvedValue({
    ok: true,
    status: 200,
    json: () => Promise.resolve({ live: [result], past: [] }),
  } as Response);
  render(<CommandPalette />);
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), term);
  await waitFor(() => expect(screen.getByText("Live")).toBeTruthy());
  const row = screen.getAllByRole("option").find((o) => o.textContent?.includes(term));
  await user.click(row as HTMLElement);
}

// One ref form, and the type is what enforces it: a hit with no ref is not a
// state this code can be in, so there is no runtime fallback to test. The hub
// sets Ref at both construction sites and its field carries no omitempty.
test("a search result is opened by its qualified ref", async () => {
  const user = userEvent.setup();
  await searchAndClick(
    user,
    { id: "bare123", ref: "local:qualified", title: "reffuls", project: "p", state: "active", age: "now" },
    "reffuls",
  );
  expect(decodeURIComponent(window.location.pathname)).toBe("/s/local:qualified");
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

test("Enter on an exact built-in name runs the built-in", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  const trigger = document.createElement("button");
  trigger.setAttribute("data-details-trigger", "true");
  document.body.appendChild(trigger);
  vi.stubGlobal("matchMedia", vi.fn().mockReturnValue({ matches: true }));
  const click = vi.spyOn(trigger, "click");
  focusSession("ref_a");
  render(<CommandPalette />);
  act(() => openPalette("/status"));

  await user.keyboard("{Enter}");

  expect(click).toHaveBeenCalled();
  expect(send).not.toHaveBeenCalled();
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on a fuzzy near-miss falls through to the session", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  focusSession("ref_a");
  render(<CommandPalette />);
  act(() => openPalette("/stat"));

  await user.keyboard("{Enter}");

  expect(send).toHaveBeenCalledWith("ref_a", "/stat");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on an unknown slash command sends the raw query", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  focusSession("ref_a");
  render(<CommandPalette />);
  act(() => openPalette("/review main"));

  await user.keyboard("{Enter}");

  expect(send).toHaveBeenCalledWith("ref_a", "/review main");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("selecting a plugin catalog entry submits the qualified form", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  useCommandCatalog.setState({
    commands: [{ name: "review", pluginName: "p", source: "plugin" }],
    loaded: true,
  });
  focusSession("ref_a");
  render(<CommandPalette />);
  act(() => openPalette("/review"));

  await user.click(screen.getByRole("option", { name: /review \[plugin\]/ }));

  expect(send).toHaveBeenCalledWith("ref_a", "/p:review");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on a plugin command with arguments preserves its qualified invocation", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  useCommandCatalog.setState({
    commands: [{ name: "review", pluginName: "p", source: "plugin" }],
    loaded: true,
  });
  focusSession("ref_a");
  render(<CommandPalette />);
  act(() => openPalette("/review main"));

  await user.keyboard("{Enter}");

  expect(send).toHaveBeenCalledWith("ref_a", "/p:review main");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Arrow-selected catalog entry activates instead of the first result", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  useCommandCatalog.setState({
    commands: [
      { name: "review", pluginName: "p", source: "plugin" },
      { name: "review", pluginName: "q", source: "plugin" },
    ],
    loaded: true,
  });
  focusSession("ref_a");
  render(<CommandPalette />);
  act(() => openPalette("/review"));

  await user.keyboard("{ArrowDown}{Enter}");

  expect(send).toHaveBeenCalledWith("ref_a", "/q:review");
  expect(screen.queryByRole("dialog")).toBeNull();
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
