import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import "../../panes/sessionPanels";
import { resetComposerFocusStoreForTests, useComposerFocusRequest } from "../../panes/session/composer/composerFocus";
import { resetQuoteInsertStoreForTests, useQuoteInsertRequest } from "../../panes/session/composer/quoteInsert";
import { WireError } from "../../protocol/errors";
import "../../panes/sessionPanels";
import type { ItemModel, ThreadModel, TurnModel } from "../../protocol/model";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { NavigationSessionSummary, SearchResult, ThreadCapabilities } from "../../protocol/types.gen";
import { useCommandCatalog } from "../../stores/commandCatalog";
import { connectionStore } from "../../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { keyID } from "../../stores/navigation/types";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { Toast } from "../../widgets";
import { isPaneOpen, resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { CommandPalette, commandErrorMessage } from "./CommandPalette";
import { openPalette, paletteStore } from "./paletteController";
import { renderPalette as render, scriptSearch } from "./paletteTestUtils";

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
  changeVisionModel: true,
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
    visionModel: "",
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

beforeEach(() => {
  paletteStore.setState({ open: false, query: "", openSeq: 0 });
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  useCommandCatalog.setState({ commands: [], loaded: false });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetQuoteInsertStoreForTests();
  resetComposerFocusStoreForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/");
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

function needsYouRows(): NavigationSessionSummary[] {
  return [
    {
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
  ];
}
function setNeedsYouRows(rows: NavigationSessionSummary[] | null): void {
  const key = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
  navigationStore.setState({
    mode: "v2",
    clientGenerationID: "generation_test",
    resources:
      rows === null
        ? new Map()
        : new Map([
            [
              keyID(key),
              {
                key,
                data: { generation_id: "generation_test", revision: 1, sessions: rows, remaining: 0, truncated: false },
                loadedRevision: 1,
                targetRevision: null,
                forceToken: 0,
                etag: "etag",
                loading: false,
                stale: false,
                error: null,
                generationID: "generation_test",
              },
            ],
          ]),
  });
}

test("the empty-query view lists needs-you sessions (title + 'needs you' hint) when any exist", () => {
  setNeedsYouRows(needsYouRows());
  render(<CommandPalette />);
  act(() => openPalette());

  const option = screen.getByRole("option", { name: /Session A/i });
  expect(within(option).getByText(/needs you/i)).toBeTruthy();
  expect(screen.getByRole("option", { name: /Session B/i })).toBeTruthy();
});

test("Enter on a needs-you row opens that session and closes the palette", async () => {
  const user = userEvent.setup();
  setNeedsYouRows(needsYouRows());
  render(<CommandPalette />);
  act(() => openPalette());

  await user.keyboard("{Enter}");

  expect(window.location.pathname).toBe("/s/local%3Any1");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("the empty-query view keeps its old (empty) behavior when there are no needs-you sessions", () => {
  setNeedsYouRows(null);
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

test("an async catalog refresh rerenders the open session palette - a newly-loaded plugin command now surfaces the handoff row", async () => {
  const fake = new FakeClient();
  fake.on("evener/command/list", async () => {
    await Promise.resolve();
    return { commands: [{ name: "review", pluginName: "p", source: "plugin" }] };
  });
  connectionStore.setState({ client: fake as never });
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }] } });
  render(<CommandPalette />);

  act(() => openPalette("/review"));

  await waitFor(() => expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy());
});

test("the palette scopes catalog commands to active diagnostics without mutating the global catalog", async () => {
  const user = userEvent.setup();
  useCommandCatalog.setState({
    commands: [
      { name: "review", pluginName: "enabled", source: "plugin" },
      { name: "secret", pluginName: "excluded", source: "plugin" },
      { name: "whoami", source: "user" },
    ],
    loaded: true,
  });
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "enabled" }] } });
  render(<CommandPalette />);

  act(() => openPalette("/review"));
  expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy();

  await user.clear(screen.getByRole("combobox"));
  await user.type(screen.getByRole("combobox"), "/secret");
  expect(screen.queryByRole("option", { name: /Continue in the composer/ })).toBeNull();

  await user.clear(screen.getByRole("combobox"));
  await user.type(screen.getByRole("combobox"), "/whoami");
  expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy();

  threadsStore.setState({
    threads: new Map([["ref_a", testModel({ ref: "ref_a" })]]),
  });
  await waitFor(() => expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy());
  await user.clear(screen.getByRole("combobox"));
  await user.type(screen.getByRole("combobox"), "/review");
  expect(screen.queryByRole("option", { name: /Continue in the composer/ })).toBeNull();
  await user.clear(screen.getByRole("combobox"));
  await user.type(screen.getByRole("combobox"), "/whoami");
  expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy();
  expect(useCommandCatalog.getState().commands.map((command) => command.name)).toEqual(["review", "secret", "whoami"]);
});

test("selecting a free-arg APP-GLOBAL command enters args mode with a pill and placeholder, and Esc backs out (does not close)", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/spawn"));
  await user.click(screen.getByRole("option", { name: /Start with prompt/ }));

  // Args-mode pill shows the command, and the input placeholder changes.
  expect(screen.getByText("Start with prompt")).toBeTruthy();
  expect(screen.getByRole("combobox").getAttribute("placeholder")).toBe("prompt to start…");

  await user.keyboard("{Escape}");
  // Backed out to command-filter, dialog still open (Esc did not close it).
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.getByRole("option", { name: /Start with prompt/ })).toBeTruthy();
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

// --- session-scoped commands: delisted, handed off to the composer
// (2026-08-14 decision: "the palette is where you go; the composer is where
// you act on this session") ---
//
// The palette itself is now capability-agnostic for these - it only ever
// detects a NAME prefix match, never touches ThreadCapabilities, and never
// makes a wire call for one. Whether a specific built-in can actually run
// (an ended session, a false capability flag, no active turn) is entirely
// the composer's own concern now - see builtinCommand.test.ts's own coverage
// of that (runBuiltinCommand's unavailableReason / blocked-sentinel cases).

test("a session-scoped built-in (/interrupt) is never listed as a runnable command row - only the ONE handoff row", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  focusSession("ref_a", { status: { type: "idle" }, activeTurnId: undefined });
  render(<CommandPalette />);
  act(() => openPalette("/interrupt"));

  expect(screen.queryByRole("option", { name: /Interrupt agent/ })).toBeNull();
  expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "turn/interrupt")).toBe(false);
});

test("with a focused session, activating the handoff row inserts the typed text into that session's composer and closes", async () => {
  const user = userEvent.setup();
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }] } });
  render(<CommandPalette />);
  act(() => openPalette("/interrupt"));

  await user.click(screen.getByRole("option", { name: /Continue in the composer/ }));

  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/interrupt ");
  expect(insert.current?.placement).toBe("prefix");
  const { result: focus } = renderHook(() => useComposerFocusRequest("ref_a"));
  expect(focus.current).not.toBeUndefined();
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("with NO focused session, the handoff row explains that instead and Enter shows an inline error, not an insert", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/interrupt"));

  const row = screen.getByRole("option", { name: /Focus a session to run this command/ });
  expect(row.getAttribute("aria-disabled")).toBe("true");

  await user.keyboard("{Enter}");

  expect(screen.getByRole("alert").textContent).toBe("Focus a session first to run a session command.");
  expect(screen.getByRole("dialog")).toBeTruthy(); // stays open, same as any other blocked action
});

test("the handoff row appears for /model even on an ended session with false capability flags - the palette does not gate on either", () => {
  focusSession("ref_a", {
    status: { type: "ended" },
    capabilities: { ...CAPS, steer: false, interrupt: false, queue: false },
  });
  render(<CommandPalette />);
  act(() => openPalette("/model"));

  expect(screen.queryByRole("option", { name: /Switch model/ })).toBeNull();
  expect(screen.getByRole("option", { name: /Continue in the composer/ })).toBeTruthy();
});

test("a failed auto-resume is attributed to the session, not blamed on the command", () => {
  // The hub returns the spawner's own raw text under evenerErrorInfo hubLaunch
  // when the resume behind a cold-session mutation fails; unprefixed it reads
  // as the command having failed.
  expect(
    commandErrorMessage(new WireError("fork/exec evener: no such file", -32014, { evenerErrorInfo: "hubLaunch" })),
  ).toBe("couldn't start this session: fork/exec evener: no such file");
  // Every other rejection is passed through untouched.
  expect(commandErrorMessage(new WireError("turn t1 is active", -32013, { evenerErrorInfo: "conflict" }))).toBe(
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

  expect(
    within(helpRowFor("toggle the sidebar — inert while typing in an editable field")).getByText("B"),
  ).toBeTruthy();
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

// SHOULD-FIX (product decision): "/" on an empty composer used to open the
// MODAL palette; it now types a literal slash that opens the composer's own
// INLINE slash-command menu instead (Composer.tsx's own handleKeyDown).
test("/help describes the leading-'/' row as opening the inline slash-command menu, not command mode", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/help"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));

  expect(screen.getByText(/opens the inline slash-command menu/i)).toBeTruthy();
  expect(screen.queryByText(/opens command mode/i)).toBeNull();
});

// 2026-08-14: the legend teaches the new split explicitly - a user hunting
// for /goal or /model in the palette's own "/" filter should learn WHY it
// isn't there, not just find it silently missing.
test("/help teaches the palette/composer split for session commands", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/help"));
  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));

  expect(screen.getByText(/hands off to the composer instead/i)).toBeTruthy();
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

test("search mode renders Live and Past sections from AppWire with highlighting", async () => {
  const user = userEvent.setup();
  scriptSearch({
    live: [
      { id: "local:a", ref: "local:local:a", title: "frobnitz worker", project: "proj", state: "active", age: "now" },
    ],
    past: [{ id: "p1", ref: "local:p1", title: "old frobnitz run", project: "old", state: "ended", age: "2h" }],
  });

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
  scriptSearch({ live: [], past: [] });
  focusSession("ref_a", { turns: [turn([item("i1", "please investigate the frobnitz")])] });

  render(<CommandPalette />);
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "frobnitz");

  await waitFor(() => expect(screen.getByText("In session · 1")).toBeTruthy());
  expect(screen.getByText("turn 1")).toBeTruthy();
});

// FIX 2 (real-user report): the same user also checked every settings
// section for a shortcuts legend and never learned the palette even has a
// "/" command mode - the placeholder is the one thing visible on every
// empty-query open, so it now mentions "?" once, matching the bare-"?"
// affordance below.
test('the search-mode placeholder hints at "?" for keyboard shortcuts', () => {
  render(<CommandPalette />);
  act(() => openPalette());

  expect(screen.getByRole("combobox").getAttribute("placeholder")).toMatch(/\?.*shortcut/i);
});

// FIX 2 (real-user report): a user hunting for the keyboard shortcut legend
// tried typing "?" in the palette (not "/help" or "/?" - a bare "?", the
// conventional "show me help" gesture) and, since "?" alone stays in search
// mode (mode.ts's computeMode only switches on a leading "/"), it searched
// live/past sessions for a literal "?" and found nothing. Typing "?" now
// opens the same help view HELP_ROWS renders for "/help", directly from
// search mode, with no "/" prefix required.
test('typing a bare "?" in search mode opens the keyboard-shortcuts help view', async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette());

  await user.type(screen.getByRole("combobox"), "?");

  expect(screen.getByText("Keyboard shortcuts")).toBeTruthy();
});

test('typing past a bare "?" leaves the help view and resumes filtering, same as after /help', async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "?");
  expect(screen.getByText("Keyboard shortcuts")).toBeTruthy();

  await user.type(screen.getByRole("combobox"), "x");

  expect(screen.queryByText("Keyboard shortcuts")).toBeNull();
});

// --- search-result navigation ---

// Typed against the real SearchResult, not Record<string, unknown>: `ref` is
// required now (see search.ts), and a fixture omitting it would be describing
// a response the hub cannot produce.
async function searchAndClick(user: ReturnType<typeof userEvent.setup>, result: SearchResult, term: string) {
  scriptSearch({ live: [result], past: [] });
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

test("Enter on an exact app-global command name runs it", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => openPalette("/settings"));

  await user.keyboard("{Enter}");

  expect(window.location.pathname).toBe("/settings");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on an exact session-scoped command name (/status) hands off to the composer instead of running it", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }] } });
  render(<CommandPalette />);
  act(() => openPalette("/status"));

  await user.keyboard("{Enter}");

  // /status used to toggle the sessionDetails workspace pane directly - the
  // palette no longer runs ANY session command itself (2026-08-14 decision).
  expect(isPaneOpen(workspaceStore.getState(), "sessionDetails", { ref: "ref_a" })).toBe(false);
  expect(send).not.toHaveBeenCalled();
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/status ");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on a fuzzy near-miss of a session-scoped command name (/stat) still hands off, not a raw send", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }, { name: "q" }] } });
  render(<CommandPalette />);
  act(() => openPalette("/stat"));

  await user.keyboard("{Enter}");

  expect(send).not.toHaveBeenCalled();
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/stat ");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on an unknown slash command sends the raw query - the escape hatch for genuinely unrecognized text", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  focusSession("ref_a");
  render(<CommandPalette />);
  act(() => openPalette("/frobnicate main"));

  await user.keyboard("{Enter}");

  expect(send).toHaveBeenCalledWith("ref_a", "/frobnicate main");
  expect(screen.queryByRole("dialog")).toBeNull();
});

// 2026-08-14: a picked session-scoped command - built-in OR plugin catalog -
// no longer runs from the palette at all (nor, for a plugin command, sends
// its qualified form immediately). Both now resolve to the SAME single
// handoff row, which inserts the RAW TEXT THE USER TYPED (not a resolved
// invocation - sessionScopedHandoffMatch only ever answers "does something
// match", never "which") via the SAME per-ref insert/focus seams
// SelectionQuote's "Quote in reply" already uses (quoteInsert.ts/
// composerFocus.ts). The composer's OWN inline slash menu is what resolves a
// plugin command's qualified "/plugin:name" form, once the user keeps typing
// there (slashCompletion.ts's mergeSlashCommands).

test("selecting a plugin catalog entry's handoff row inserts the raw typed text into the composer instead of sending", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  send.mockClear(); // isolate:false: threadsStore.send may already be spied by an earlier test in this worker
  useCommandCatalog.setState({
    commands: [{ name: "review", pluginName: "p", source: "plugin" }],
    loaded: true,
  });
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }] } });
  render(<CommandPalette />);
  act(() => openPalette("/review"));

  await user.click(screen.getByRole("option", { name: /Continue in the composer/ }));

  expect(send).not.toHaveBeenCalled();
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/review ");
  // SHOULD-FIX: "prefix", not the default "append" - a slash command only
  // parses at the draft's start, so it must land there even ahead of
  // whatever the user already typed, not after it.
  expect(insert.current?.placement).toBe("prefix");
  const { result: focus } = renderHook(() => useComposerFocusRequest("ref_a"));
  expect(focus.current).not.toBeUndefined();
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("Enter on a plugin command with arguments hands off the FULL typed text, args included", async () => {
  const user = userEvent.setup();
  const send = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue();
  send.mockClear(); // isolate:false: threadsStore.send may already be spied by an earlier test in this worker
  useCommandCatalog.setState({
    commands: [{ name: "review", pluginName: "p", source: "plugin" }],
    loaded: true,
  });
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }] } });
  render(<CommandPalette />);
  act(() => openPalette("/review main"));

  await user.keyboard("{Enter}");

  expect(send).not.toHaveBeenCalled();
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/review main ");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("two catalog entries sharing a name still collapse to ONE handoff row, not one per entry", async () => {
  useCommandCatalog.setState({
    commands: [
      { name: "review", pluginName: "p", source: "plugin" },
      { name: "review", pluginName: "q", source: "plugin" },
    ],
    loaded: true,
  });
  focusSession("ref_a", { diagnostics: { plugins: [{ name: "p" }, { name: "q" }] } });
  render(<CommandPalette />);
  act(() => openPalette("/review"));

  expect(screen.getAllByRole("option", { name: /Continue in the composer/ })).toHaveLength(1);
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
