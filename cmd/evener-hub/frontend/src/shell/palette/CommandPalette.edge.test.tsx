// Edge cases for CommandPalette.tsx that close remaining uncovered lines:
// - commandErrorMessage with non-Error, non-hub-launch error (line 183)
// - search failure path (fetchSearch rejection, lines 283-285)
// - handleCommandResult: blocked result (388-389), promise blocked (394-395),
//   promise reject (400)
// - activateResult: insession (494-495), live/past newTab (505-506)
// - enterPressed: handoff via Enter (519-524)
// - buildView: recent commands header (735), handoff row (744-746)
// - renderResults: enum loading/error empty states (829-832)

import { act, cleanup, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { ALL_CAPABILITIES as CAPS } from "../../threadTestUtils";
import "../../panes/session";
import "../../panes/sessionPanels";
import type { ItemModel, ThreadModel, TurnModel } from "@evener/appwire-client";
import { resetComposerFocusStoreForTests } from "../../panes/session/composer/composerFocus";
import { resetQuoteInsertStoreForTests, useQuoteInsertRequest } from "../../panes/session/composer/quoteInsert";
import { resetFindRequestsForTests, useFindRequest } from "../../panes/session/find/findRequest";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { useCommandCatalog } from "../../stores/commandCatalog";
import { resetConnectionStoreForTests } from "../../stores/connection";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { resetTranscriptDisplayStoreForTests } from "../../stores/transcriptDisplay";
import { NeedsYouProvider } from "../NeedsYouProvider";
import { captureNewTabs, NEW_TAB_POLICY, openedNewTab } from "../openInNewTab.testSupport";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { CommandPalette, commandErrorMessage } from "./CommandPalette";
import { openPalette, paletteStore } from "./paletteController";
import {
  helpChords,
  paletteRowsOfKind,
  paletteSection,
  renderPalette as render,
  scriptSearch,
  scriptSearchFailure,
} from "./paletteTestUtils";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

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
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    contextAssumed: false,
    compactionThreshold: 0,
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
  return { id, turnId: "t1", type: "agentMessage", text };
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
  resetConnectionStoreForTests();
  useCommandCatalog.setState(useCommandCatalog.getInitialState());
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetQuoteInsertStoreForTests();
  resetComposerFocusStoreForTests();
  resetFindRequestsForTests();
  resetTranscriptDisplayStoreForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/");
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
  // Empty values share one non-empty default, so the error strip never renders blank.
  const fallback = commandErrorMessage(null);
  expect(fallback.trim()).not.toBe("");
  expect(commandErrorMessage(undefined)).toBe(fallback);
  expect(commandErrorMessage("  ")).toBe(fallback);
});

test("commandErrorMessage uses err.message for Error instances", () => {
  expect(commandErrorMessage(new Error("boom"))).toBe("boom");
});

// --- remote search results ---

// Remote search waits out the palette's search debounce. Fake timers own that
// clock, and the stubbed `jest` global lets Testing Library's waitFor polls
// advance it, so the debounce costs a poll rather than real time.
describe("remote search results", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  // --- search failure (lines 283-285) ---

  test("a failed search raises an alert in the results in place of any rows", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    scriptSearchFailure();

    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    await user.type(screen.getByRole("combobox"), "query");

    const results = screen.getByRole("listbox");
    await waitFor(() => expect(within(results).getByRole("alert")).toBeTruthy());
    expect(within(results).queryAllByRole("option")).toHaveLength(0);
  });

  test("a failed search still raises an alert when commands match the query", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    scriptSearchFailure();

    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    await user.type(screen.getByRole("combobox"), "settings");

    const results = screen.getByRole("listbox");
    await waitFor(() => expect(within(results).getByRole("alert")).toBeTruthy());
    expect(within(results).getAllByRole("option").length).toBeGreaterThan(0);
  });

  // --- search result activation: live/past with newTab (lines 505-506) ---

  test("Mod+Enter on a search result opens it in a new tab", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const anchors = captureNewTabs();
    scriptSearch({
      live: [{ id: "live1", ref: "local:live1", title: "live result", project: "p", state: "active", age: "now" }],
      past: [],
    });

    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    await user.type(screen.getByRole("combobox"), "live");

    await waitFor(() => expect(paletteSection("live")).not.toBeNull());
    await user.keyboard("{Meta>}{Enter}{/Meta}");

    // The anchor's rel keeps the opened tab from copying this tab's
    // sessionStorage - the per-client mutation identity lives there, and a
    // shared identity would let both tabs claim each other's durable sends.
    expect(openedNewTab(anchors)).toEqual({
      url: "/s/local%3Alive1",
      target: "_blank",
      rel: NEW_TAB_POLICY,
    });
  });

  // --- search result: past result activation (line 504) ---

  test("clicking a past search result navigates to the session", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    scriptSearch({
      live: [],
      past: [{ id: "past1", ref: "local:past1", title: "old session", project: "p", state: "ended", age: "2h" }],
    });

    render(
      <NeedsYouProvider>
        <CommandPalette />
      </NeedsYouProvider>,
    );
    act(() => openPalette());
    await user.type(screen.getByRole("combobox"), "old");

    await waitFor(() => expect(paletteSection("past")).not.toBeNull());
    const [row] = paletteRowsOfKind("past");
    await user.click(row as HTMLElement);

    expect(decodeURIComponent(window.location.pathname)).toBe("/s/local:past1");
  });
});

// --- search result activation: insession (lines 494-495) ---

test("Shift+Enter on an in-session search result closes the palette without navigating", async () => {
  const user = userEvent.setup();
  const anchors = captureNewTabs();
  scriptSearch({ live: [], past: [] });
  focusSession("ref_a", { turns: [turn([item("i1", "search term found here")])] });

  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "search");

  await waitFor(() => expect(paletteRowsOfKind("insession")).toHaveLength(1));
  await user.keyboard("{ArrowDown}{Shift>}{Enter}{/Shift}");

  expect(screen.queryByRole("dialog")).toBeNull();
  expect(window.location.pathname).toBe("/");
  expect(anchors).toHaveLength(0);
});

test("choosing an in-session result hands its words and its item to the session's Find", async () => {
  const user = userEvent.setup();
  scriptSearch({ live: [], past: [] });
  focusSession("ref_a", { turns: [turn([item("i1", "nothing here"), item("i2", "search term found here")])] });
  const paneId = workspaceStore.getState().focusedPaneId ?? "";
  const request = renderHook(() => useFindRequest("ref_a", paneId));
  expect(request.result.current).toBeUndefined();

  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), " Search  term ");
  await waitFor(() => expect(paletteRowsOfKind("insession")).toHaveLength(1));
  await user.keyboard("{ArrowDown}{Enter}");

  expect(screen.queryByRole("dialog")).toBeNull();
  expect(request.result.current).toMatchObject({ query: "Search  term", itemId: "i2" });
  // Another session's Find is not asked.
  expect(renderHook(() => useFindRequest("ref_b", paneId)).result.current).toBeUndefined();
});

test("choosing an in-session result from a companion pane brings up the conversation to show Find in", async () => {
  const user = userEvent.setup();
  scriptSearch({ live: [], past: [] });
  focusSession("ref_a", { turns: [turn([{ ...item("i1", "search term here"), type: "agentMessage" }])] });
  // Only the tasks pane is open: no conversation is mounted to take the ask.
  workspaceStore.setState({
    panes: [{ id: "tasks", type: "sessionTasks", params: { ref: "ref_a" }, slot: "main" }],
    focusedPaneId: "tasks",
  });
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "search term");
  await waitFor(() => expect(paletteRowsOfKind("insession")).toHaveLength(1));
  await user.keyboard("{ArrowDown}{Enter}");

  const conversation = workspaceStore.getState().panes.find((pane) => pane.type === "session");
  expect(conversation).toBeDefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(conversation?.id);
  const asked = renderHook(() => useFindRequest("ref_a", conversation?.id ?? ""));
  expect(asked.result.current).toMatchObject({ query: "search term", itemId: "i1" });
});

test("an in-session row Find cannot land on is listed and only closes the palette", async () => {
  const user = userEvent.setup();
  scriptSearch({ live: [], past: [] });
  // Find searches messages; a tool's output is listed here but is not a place
  // Find can go.
  const call: ItemModel = { ...item("c1", ""), type: "commandExecution", output: "search term in output" };
  focusSession("ref_a", { turns: [turn([call])] });
  const paneId = workspaceStore.getState().focusedPaneId ?? "";
  const request = renderHook(() => useFindRequest("ref_a", paneId));

  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "search term");
  await waitFor(() => expect(paletteRowsOfKind("insession")).toHaveLength(1));
  await user.keyboard("{ArrowDown}{Enter}");

  expect(screen.queryByRole("dialog")).toBeNull();
  expect(request.result.current).toBeUndefined();
});

test("Enter on a plain query prefers the first session hit over the leading command", async () => {
  const user = userEvent.setup();
  scriptSearch({ live: [], past: [] });
  focusSession("ref_a", { turns: [turn([item("i1", "set term found here")])] });

  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "set");
  await waitFor(() => expect(paletteRowsOfKind("insession")).toHaveLength(1));

  await user.keyboard("{Enter}");
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(window.location.pathname).toBe("/");
});

test("ArrowUp to a leading command runs the highlighted command", async () => {
  const user = userEvent.setup();
  scriptSearch({ live: [], past: [] });
  focusSession("ref_a", { turns: [turn([item("i1", "set term found here")])] });

  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette());
  await user.type(screen.getByRole("combobox"), "set");
  await waitFor(() => expect(paletteRowsOfKind("insession")).toHaveLength(1));

  await user.keyboard("{ArrowUp}{Enter}");
  await waitFor(() => expect(window.location.pathname).toBe("/settings"));
});

// --- enterPressed: handoff via arrow+Enter (lines 519-524) ---

test("arrow-down then Enter on the handoff row hands off to the composer", async () => {
  const user = userEvent.setup();
  focusSession("ref_a");

  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/interrupt"));

  // Arrow down to the handoff row (index 0 is the handoff row)
  await user.keyboard("{ArrowDown}{Enter}");

  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBe("/interrupt ");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("arrow-down onto the first command in command-filter runs it instead of sending raw text", async () => {
  const user = userEvent.setup();
  focusSession("ref_a");
  const send = vi.spyOn(threadsStore.getState(), "send");

  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/e"));
  await user.keyboard("{ArrowDown}{Enter}");

  expect(send).not.toHaveBeenCalled();
  await waitFor(() => {
    const closed = screen.queryByRole("dialog") === null;
    const argsPill = screen.queryByRole("button", { name: "back to command list" });
    expect(closed || argsPill !== null).toBe(true);
  });
});

test("Enter runs a command matched by its full title above a handoff row", async () => {
  const user = userEvent.setup();
  focusSession("ref_a");

  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/go to dashboard"));
  await user.keyboard("{Enter}");

  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  const { result: insert } = renderHook(() => useQuoteInsertRequest("ref_a"));
  expect(insert.current?.text).toBeUndefined();
});

test("Enter on a bare slash does not run the highlighted row", async () => {
  const user = userEvent.setup();
  focusSession("ref_a");

  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/"));
  await user.keyboard("{Enter}");

  expect(screen.getByRole("dialog")).toBeTruthy();
});

// --- clearToSearch and showHelp UI callbacks (lines 248-251) ---

test("/search command clears to search mode", async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/"));

  await user.click(screen.getByRole("option", { name: /Search sessions/ }));

  // Should stay open (stayOpen) and be in search mode
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.getByRole("combobox").getAttribute("placeholder")).toMatch(/search/i);
});

// --- help via /help command (showHelp callback) ---

test("/help command shows the help panel", async () => {
  const user = userEvent.setup();
  render(
    <NeedsYouProvider>
      <CommandPalette />
    </NeedsYouProvider>,
  );
  act(() => openPalette("/"));

  await user.click(screen.getByRole("option", { name: /Show keyboard shortcuts/ }));

  expect(paletteSection("help")).not.toBeNull();
  expect(helpChords().length).toBeGreaterThan(0);
});
