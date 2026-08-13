import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import "../../panes/sessionPanels";
import { resetComposerFocusStoreForTests, useComposerFocusRequest } from "../../panes/session/composer/composerFocus";
import { resetQuoteInsertStoreForTests, useQuoteInsertRequest } from "../../panes/session/composer/quoteInsert";
import { WireError } from "../../protocol/errors";
import "../../panes/sessionPanels";
import type { ItemModel, ThreadModel, TurnModel } from "../../protocol/model";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../protocol/types.gen";
import { useCommandCatalog } from "../../stores/commandCatalog";
import { connectionStore } from "../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { resetTreeStoreForTests, type TreeResponse, treeStore } from "../../stores/tree";
import { Toast } from "../../widgets";
import { isPaneOpen, resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
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
  resetTreeStoreForTests();
  resetQuoteInsertStoreForTests();
  resetComposerFocusStoreForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/");
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  // Keep viewport tests isolated from direct matchMedia assignments.
  // @ts-expect-error jsdom baseline has no matchMedia.
  delete window.matchMedia;
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

// --- empty-query view: needs-you sessions (UX fix) ------------------------

function needsYouTree(): TreeResponse {
  return {
    generated_at: "2026-01-01T00:00:00Z",
    sources: [],
    live: [],
    needs_you: [
      {
        row_id: "r1",
        ref: "local:ny1",
        host_id: "local",
        session_id: "ny1",
        title: "Session A",
        project: "P",
        state: "awaiting",
        kind: "session",
        live: true,
        children: [],
      },
      {
        row_id: "r2",
        ref: "local:ny2",
        host_id: "local",
        session_id: "ny2",
        title: "Session B",
        project: "P",
        state: "awaiting",
        kind: "session",
        live: true,
        children: [],
      },
    ],
    pin_sections: [],
    projects: [],
    archived_projects: [],
    test_runs: [],
    attentionSummary: { needsYou: 2, error: 0, working: 0 },
  };
}

test("the empty-query view lists needs-you sessions (title + 'needs you' hint) when any exist", () => {
  treeStore.setState({ tree: needsYouTree() });
  render(<CommandPalette />);
  act(() => openPalette());

  const option = screen.getByRole("option", { name: /Session A/i });
  expect(within(option).getByText(/needs you/i)).toBeTruthy();
  expect(screen.getByRole("option", { name: /Session B/i })).toBeTruthy();
});

test("Enter on a needs-you row opens that session and closes the palette", async () => {
  const user = userEvent.setup();
  treeStore.setState({ tree: needsYouTree() });
  render(<CommandPalette />);
  act(() => openPalette());

  await user.keyboard("{Enter}");

  expect(window.location.pathname).toBe("/s/local%3Any1");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("the empty-query view keeps its old (empty) behavior when there are no needs-you sessions", () => {
  treeStore.setState({ tree: null });
  render(<CommandPalette />);
  act(() => openPalette());

  expect(screen.queryByRole("option")).toBeNull();
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

// UX fix: HELP_ROWS was stale, hand-rolled, and mono-typeface (the design
// bar bans mono for chrome labels). It's now {keys, desc}[] rendered through
// the shared KeyHint widget (Mod resolves to the reviewing platform's own
// symbol), covering every real chord across the app - not just the
// palette's own - including ones the old table never had at all (Mod+I,
// Mod+J, Mod+', ask dock's Mod+Enter and arrow-key navigation) and
// correcting the composer's Enter/Shift+Enter/Mod+Enter semantics against
// Composer.tsx's actual keydown routing (handleKeyDown) rather than the old
// generic "run the highlighted command" wording.
function helpRowFor(desc: string): HTMLElement {
  const descEl = screen.getByText(desc);
  return descEl.parentElement as HTMLElement;
}

test("/help renders through KeyHint, with the platform's own Mod glyph", async () => {
  const user = userEvent.setup();
  Object.defineProperty(window.navigator, "platform", { value: "Win32", configurable: true });
  render(<CommandPalette />);
  act(() => openPalette("/help"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));

  expect(screen.getByText("Keyboard shortcuts")).toBeTruthy();
  // KeyHint's own bordered form: one <kbd> per key, not a hand-rolled string.
  expect(document.querySelectorAll("kbd").length).toBeGreaterThan(0);
  const paletteRow = helpRowFor("open the command palette");
  expect(within(paletteRow).getAllByText("Ctrl").length).toBeGreaterThan(0);
  expect(within(paletteRow).getByText("K")).toBeTruthy();
});

test("/help lists the UX-fix chords the old table never had", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/help"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));

  expect(within(helpRowFor("toggle the sidebar")).getByText("B")).toBeTruthy();
  expect(within(helpRowFor("focus the composer")).getByText("I")).toBeTruthy();
  expect(within(helpRowFor("go to the next session needing you")).getByText("J")).toBeTruthy();
  expect(within(helpRowFor("quote the selection into the composer")).getByText("'")).toBeTruthy();
  expect(within(helpRowFor("previous ask-dock question")).getByText("←")).toBeTruthy();
  expect(within(helpRowFor("next ask-dock question")).getByText("→")).toBeTruthy();
});

// Composer.tsx's own handleKeyDown: plain Enter (or Mod+Enter, always)
// submits (send, or queue when the agent is mid-turn); Shift+Enter only
// steers when Enter-to-send is OFF (with it on, Shift+Enter is a literal
// newline). The old table's "⇧↵ jump to a turn" and "⌘↵ open in a new tab"
// described the PALETTE's search-result rows, not the composer at all - a
// different (still-real) row covers those, kept separately below.
test("/help describes the composer's real Enter/Shift+Enter/Mod+Enter semantics", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/help"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));

  expect(screen.getByText(/send now.*queue.*mid-turn/i)).toBeTruthy();
  expect(screen.getByText(/steer mid-turn/i)).toBeTruthy();
  expect(screen.getByText(/send or queue now, regardless/i)).toBeTruthy();
});

test("/help documents ask dock's Mod+Enter answer/approve chord (ask dock and sandbox approval)", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/help"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));

  expect(screen.getByText(/answer.*approve/i)).toBeTruthy();
});

test("HELP_ROWS drops the mono-typeface rule (design bar bans mono for chrome labels)", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "commandpalette.module.css"), "utf8");
  const helpKeysRule = /\.helpKeys\s*\{[^}]*\}/.exec(css);
  expect(helpKeysRule).not.toBeNull();
  expect(helpKeysRule?.[0]).not.toMatch(/font-family:\s*var\(--font-mono\)/);
});

test("the help panel is inert: ArrowDown+Enter never fires a hidden registry command (§2.8)", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  // No session focused -> command-filter with the empty "/" filter lists the
  // global commands. Showing help must not leave that list navigable
  // underneath, or ArrowDown+Enter fires a hidden one invisibly (with no
  // client it blocks, surfacing the error strip).
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
  focusSession("ref_a");
  render(<CommandPalette />);
  act(() => openPalette("/status"));

  await user.keyboard("{Enter}");

  // /status toggles the sessionDetails workspace pane directly at every
  // viewport - the chrome trigger button it used to click no longer renders
  // (the unified SessionMenu owns Details/Tasks).
  expect(isPaneOpen(workspaceStore.getState(), "sessionDetails", { ref: "ref_a" })).toBe(true);
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

// UX fix: a picked plugin slash-command used to send() immediately (no
// chance to add arguments). It now inserts the qualified invocation into
// the composer's draft instead - the user finishes typing and sends it
// themselves - via the SAME per-ref insert/focus seams SelectionQuote's
// "Quote in reply" already uses (quoteInsert.ts/composerFocus.ts).

test("selecting a plugin catalog entry inserts its qualified form into the composer instead of sending", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  send.mockClear(); // isolate:false: threadsStore.send may already be spied by an earlier test in this worker
  useCommandCatalog.setState({
    commands: [{ name: "review", pluginName: "p", source: "plugin" }],
    loaded: true,
  });
  focusSession("ref_a");
  render(<CommandPalette />);
  act(() => openPalette("/review"));

  await user.click(screen.getByRole("option", { name: /review \[plugin\]/ }));

  expect(send).not.toHaveBeenCalled();
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/p:review ");
  const { result: focus } = renderHook(() => useComposerFocusRequest("ref_a"));
  expect(focus.current).not.toBeUndefined();
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on a plugin command with arguments preserves its qualified invocation in the inserted text", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  send.mockClear(); // isolate:false: threadsStore.send may already be spied by an earlier test in this worker
  useCommandCatalog.setState({
    commands: [{ name: "review", pluginName: "p", source: "plugin" }],
    loaded: true,
  });
  focusSession("ref_a");
  render(<CommandPalette />);
  act(() => openPalette("/review main"));

  await user.keyboard("{Enter}");

  expect(send).not.toHaveBeenCalled();
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/p:review main ");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Arrow-selected catalog entry activates instead of the first result", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  send.mockClear(); // isolate:false: threadsStore.send may already be spied by an earlier test in this worker
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

  expect(send).not.toHaveBeenCalled();
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/q:review ");
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
