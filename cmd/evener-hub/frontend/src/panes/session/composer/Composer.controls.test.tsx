import { act, cleanup, fireEvent, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { appliedReceipt } from "../../../mutationReceiptTestUtils";
import {
  activitySidebarStore,
  resetActivitySidebarStoreForTests,
} from "../../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../../shell/clientContext";
import { paletteStore } from "../../../shell/palette/paletteController";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { activityPanelStore, resetActivityPanelStoreForTests } from "../../../stores/activityPanel";
import { useCommandCatalog } from "../../../stores/commandCatalog";
import { connectionStore } from "../../../stores/connection";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { sessionActivitySnapshot } from "../../../stores/sessionActivity";
import { answerActivityRead } from "../../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { ALL_CAPABILITIES as FULL_CAPABILITIES } from "../../../threadTestUtils";
import { Toast } from "../../../widgets";
import buttonStyles from "../../../widgets/button/button.module.css";
import iconButtonStyles from "../../../widgets/iconbutton/iconbutton.module.css";
import promptCardStyles from "../../../widgets/promptcard/promptcard.module.css";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { hoverForTooltip } from "../../../widgets/tooltip/tooltipTestUtils";
import { settleActivityDiscovery } from "../testing/activityDiscovery";
import { createTestComposerSource } from "../testing/composerSource";
import { editorCursor, replaceEditorText } from "../testing/editor";
import { installMobileViewport } from "../testing/mobileViewport";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { requestComposerFocus, resetComposerFocusStoreForTests } from "./composerFocus";
import {
  Composer,
  currentWorkEvener,
  foldStatusFrame,
  installNarrowComposer,
  mountComposer,
  mountComposerWithHandle,
  PAST_THREAD_CAPABILITIES,
  pendingAskTurns,
  readResponse,
  steerButton,
  stopButton,
  submitButton,
  textarea,
} from "./composerTestUtils";
import { submitTooltipLabel } from "./controlTooltips";
import { draftStorageKey, readComposerDraft, writeComposerDraft } from "./draft";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";
import {
  consumeQuoteInsert,
  requestQuoteInsert,
  resetQuoteInsertStoreForTests,
  useQuoteInsertRequest,
} from "./quoteInsert";

beforeAll(async () => {
  installLocalStorage(new MemoryStorage());
  await import("../index");
});

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  localStorage.clear();
  resetPrefsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivityPanelStoreForTests();
  resetPendingTurnsStoreForTests();
  // askDockStore reconciles reactively off threadsStore (registered once at
  // module load - askDockStore.ts's own header comment), so its byRef map
  // outlives resetThreadsStoreForTests() the same way the toast store
  // outlives RTL's cleanup below: without this, a pending-ask batch minted
  // for "ref_a" in one test would still be sitting there for the next
  // test's own "ref_a" mount.
  resetAskDockStoreForTests();
  resetQuoteInsertStoreForTests();
  resetComposerFocusStoreForTests();
  // useCommandCatalog is module state the same way, and is entirely unused
  // by every OTHER test in this file - only the slash-completion tests
  // below ever populate it - so resetting it here is purely additive
  // isolation, never a behavior change for the rest of the suite.
  useCommandCatalog.setState(useCommandCatalog.getInitialState());
  // The toast store is module state that outlives RTL's own cleanup, so a
  // toast pushed by one test would otherwise still be in the next test's
  // tree and make a getByText for the same message ambiguous.
  resetToastStoreForTests();
  paletteStore.setState({ open: false, query: "" });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  resetActivitySidebarStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivityPanelStoreForTests();
  // A narrow-layout test leaves its stub installed; jsdom has no real
  // ResizeObserver, so the honest baseline for the next test is none at all.
  delete (globalThis as { ResizeObserver?: typeof ResizeObserver }).ResizeObserver;
});

test.each([399, 400])("Stop replaces Send only for an empty draft at width %s", async (width) => {
  installNarrowComposer(width);
  const fake = await mountComposer("ref_a", { status: { type: "active" } });
  const editor = textarea();
  act(() => editor.focus());
  expect(stopButton().disabled).toBe(false);
  expect(screen.queryByTestId("composer-submit")).toBeNull();
  act(() => replaceEditorText(editor, " \n "));
  expect(screen.queryByTestId("composer-submit")).toBeNull();
  expect(stopButton().disabled).toBe(false);
  act(() => replaceEditorText(editor, "draft"));
  const send = submitButton();
  expect(send.disabled).toBe(false);
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  act(() => replaceEditorText(editor, ""));
  expect(stopButton()).not.toBe(send);
  expect(send.isConnected).toBe(false);
  expect(document.activeElement).toBe(editor);
  expect(screen.queryByTestId("composer-submit")).toBeNull();
  act(() => foldStatusFrame(fake, "idle"));
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  expect(submitButton().disabled).toBe(true);
});

test.each([399, 400])("draft Stop remains reachable in the real menu at width %s", async (width) => {
  installNarrowComposer(width);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "active" } });
  act(() => replaceEditorText(textarea(), "keep this draft"));
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  expect(submitButton().disabled).toBe(false);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  const stop = screen.getByRole("menuitem", { name: "Stop" });
  expect(stop.getAttribute("aria-disabled")).not.toBe("true");
  expect(screen.queryByRole("menuitem", { name: "Steer" }) !== null).toBe(width === 399);
  expect(screen.queryByTestId("composer-steer") !== null).toBe(width === 400);
  fake.on("turn/interrupt", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thr_ref_a"),
  }));
  await user.click(stop);
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toHaveLength(1);
  expect(textarea().textContent).toBe("keep this draft");
});

test.each([399, 400])("empty keyboard and form submission never interrupt at width %s", async (width) => {
  installNarrowComposer(width);
  const fake = await mountComposer("ref_a", { status: { type: "active" } });
  act(() => prefsStore.setState({ enterToSend: true }));
  expect(screen.queryByTestId("composer-submit")).toBeNull();
  fireEvent.keyDown(textarea(), { key: "Enter" });
  fireEvent.keyDown(textarea(), { key: "Enter", ctrlKey: true });
  fireEvent.keyDown(textarea(), { key: "Enter", metaKey: true });
  fireEvent.submit(screen.getByTestId("composer-form"));
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => ["turn/interrupt", "turn/start", "turn/queue"].includes(call.method))).toEqual([]);
});

// --- basic surface ---------------------------------------------------------

test.each([399, 400])("Send-to-Stop guards the inline button for exactly 500ms at width %s", async (width) => {
  installNarrowComposer(width);
  const fake = await mountComposer("ref_a", { status: { type: "active" } });
  vi.useFakeTimers();
  expect(stopButton().disabled).toBe(false);
  act(() => replaceEditorText(textarea(), "draft"));
  const send = submitButton();
  act(() => replaceEditorText(textarea(), ""));
  expect(stopButton()).not.toBe(send);
  expect(stopButton().disabled).toBe(true);
  fireEvent.click(stopButton());
  expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toEqual([]);
  fireEvent.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Stop" }).getAttribute("aria-disabled")).not.toBe("true");
  fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
  act(() => vi.advanceTimersByTime(499));
  expect(stopButton().disabled).toBe(true);
  act(() => vi.advanceTimersByTime(1));
  expect(stopButton().disabled).toBe(false);
});

test("a cancelled delay cannot shorten the next Send-to-Stop guard", async () => {
  await mountComposer("ref_a", { status: { type: "active" } });
  vi.useFakeTimers();
  act(() => replaceEditorText(textarea(), "first"));
  act(() => replaceEditorText(textarea(), ""));
  expect(stopButton().disabled).toBe(true);
  act(() => vi.advanceTimersByTime(400));
  act(() => replaceEditorText(textarea(), "second"));
  expect(submitButton().disabled).toBe(false);
  act(() => replaceEditorText(textarea(), ""));
  act(() => vi.advanceTimersByTime(100));
  expect(stopButton().disabled).toBe(true);
  act(() => vi.advanceTimersByTime(399));
  expect(stopButton().disabled).toBe(true);
  act(() => vi.advanceTimersByTime(1));
  expect(stopButton().disabled).toBe(false);
});

test.each([399, 400])("a fast Send cannot become an accidental Stop at width %s", async (width) => {
  installNarrowComposer(width);
  const fake = await mountComposer("ref_a", { status: { type: "active" } });
  for (const method of ["turn/queue", "turn/interrupt"] as const) {
    fake.on(method, (params) => ({
      receipt: appliedReceipt(params.clientMutationId, "thr_ref_a"),
    }));
  }
  act(() => replaceEditorText(textarea(), "send once"));
  const send = submitButton();
  act(() => send.focus());
  // Keep IndexedDB's task scheduling real while controlling the activation clock.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  fireEvent.click(send);
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("");
  expect(document.activeElement).toBe(textarea());
  expect(send.isConnected).toBe(false);
  expect(stopButton().disabled).toBe(true);
  fireEvent.click(stopButton());
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toHaveLength(0);
  act(() => vi.advanceTimersByTime(500));
  expect(stopButton().disabled).toBe(false);
  fireEvent.click(stopButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toHaveLength(1);
});

test("session replacement and unmount cancel the old inline Stop delay", async () => {
  const { fake, rerender, unmount } = await mountComposerWithHandle("ref_a", { status: { type: "active" } });
  fake.on("thread/read", () => readResponse("ref_b", { status: { type: "active" } }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_b");
  });
  const source = createTestComposerSource("ref_b");
  vi.useFakeTimers();
  act(() => replaceEditorText(textarea(), "first"));
  act(() => replaceEditorText(textarea(), ""));
  expect(stopButton().disabled).toBe(true);
  act(() => vi.advanceTimersByTime(400));
  rerender(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref="ref_b" source={source} focused={false} />
    </ClientProvider>,
  );
  // This session opens on Stop, so it does not inherit the old guard.
  expect(stopButton().disabled).toBe(false);
  act(() => replaceEditorText(textarea(), "second"));
  act(() => replaceEditorText(textarea(), ""));
  act(() => vi.advanceTimersByTime(100));
  expect(stopButton().disabled).toBe(true);
  act(() => vi.advanceTimersByTime(400));
  expect(stopButton().disabled).toBe(false);
  act(() => replaceEditorText(textarea(), "third"));
  act(() => replaceEditorText(textarea(), ""));
  unmount();
  act(() => vi.advanceTimersByTime(500));
  expect(screen.queryByTestId("composer-stop")).toBeNull();
});

test("renders a textarea with an accessible name", async () => {
  await mountComposer("ref_a");
  expect(textarea()).toBeTruthy();
  // One editable, one textbox: a wrapper that is itself a textbox would nest
  // the role and hand assistive tech an editable that owns no content.
  // Counted without a name filter on purpose: an unnamed wrapper textbox would
  // hide from an accessible-name query and this guard has to see it.
  expect(screen.getAllByRole("textbox")).toHaveLength(1);
});

test.each([false, true])("the composer omits the recipient and Activity row on %s", async (mobile) => {
  const restore = mobile ? installMobileViewport() : undefined;
  try {
    await mountComposer("ref_a", { name: "Release coordinator" });

    expect(screen.queryByText("To: Release coordinator")).toBeNull();
    expect(screen.queryByRole("button", { name: /^Activity/ })).toBeNull();
    expect(textarea().getAttribute("aria-describedby")).toBeNull();
  } finally {
    restore?.();
  }
});

// --- mount autofocus ---------------------------------------------------------
//
// Loading a session into the browser UI should land keyboard focus in that
// pane's composer, desktop only, and only when the pane itself is the
// workspace's focused one - a session opening in a background tab must never
// yank focus away from what the reader is doing.
test("a focused pane focuses its composer on mount (desktop)", async () => {
  await mountComposer("ref_a", {}, { focused: true });
  await waitFor(() => expect(document.activeElement).toBe(textarea()));
});

test("an unfocused pane never focuses its composer on mount", async () => {
  await mountComposer("ref_a", {}, { focused: false });
  await flushPendingTurnsProjectionForTests();
  expect(document.activeElement).not.toBe(textarea());
});

test("a focused pane never focuses its composer on mount on mobile", async () => {
  const restoreViewport = installMobileViewport();
  try {
    await mountComposer("ref_a", {}, { focused: true });
    await flushPendingTurnsProjectionForTests();
    expect(document.activeElement).not.toBe(textarea());
  } finally {
    restoreViewport();
  }
});

test("the real live Composer mount discovers initial activity without a test-supplied opt-in", async () => {
  const ref = "ref_activity_live";
  const fake = connectReadyFakeClient();
  const activityRefs: unknown[] = [];
  const subtreeRefs: unknown[] = [];
  fake.on("thread/read", () => readResponse(ref));
  fake.on("evener/thread/activity/read", (params) => {
    // Discovery is the session read; the subtree read is the subagent count's own.
    (params.scope === "subtree" ? subtreeRefs : activityRefs).push(params.ref);
    return answerActivityRead(params);
  });
  await threadsStore.getState().ensureThread(ref);
  expect(activityPanelStore.getState().entries.has(ref)).toBe(false);
  expect(sessionActivitySnapshot(fake, ref, "session")?.summary).toBeUndefined();

  render(
    <ClientProvider client={fake}>
      <Composer ref={ref} source={createTestComposerSource(ref)} focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(activityRefs).toEqual([ref]));

  // One shared subagent count read, however many surfaces show it.

  await waitFor(() => expect(subtreeRefs).toEqual([ref]));
  expect(sessionActivitySnapshot(fake, ref, "session")?.summary).not.toBeNull();
  expect(sessionActivitySnapshot(fake, ref, "session")?.summaryState.loading).toBe(false);
});

// Saved sessions discover activity through inline chrome while the editor rests.
test("a saved notLoaded session with sending enabled discovers activity while its card rests", async () => {
  const ref = "ref_activity_saved";
  const fake = connectReadyFakeClient();
  const activityRefs: unknown[] = [];
  const subtreeRefs: unknown[] = [];
  fake.on("thread/read", () =>
    readResponse(ref, {
      status: { type: "notLoaded" },
      evener: { ref, mutationStateAuthoritative: true, capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
    }),
  );
  fake.on("evener/thread/activity/read", (params) => {
    // Discovery is the session read; the subtree read is the subagent count's own.
    (params.scope === "subtree" ? subtreeRefs : activityRefs).push(params.ref);
    return answerActivityRead(params);
  });
  await threadsStore.getState().ensureThread(ref);

  render(
    <ClientProvider client={fake}>
      <Composer ref={ref} source={createTestComposerSource(ref)} focused={false} />
    </ClientProvider>,
  );

  expect(textarea().style.minHeight).toBe("1lh");
  expect(screen.getByTestId("session-chrome-inline")).toBeTruthy();
  await waitFor(() => expect(activityRefs).toEqual([ref]));
  // One shared subagent count read, however many surfaces show it.
  await waitFor(() => expect(subtreeRefs).toEqual([ref]));
  expect(sessionActivitySnapshot(fake, ref, "session")?.summary).not.toBeNull();
  expect(sessionActivitySnapshot(fake, ref, "session")?.summaryState.loading).toBe(false);
});

test.each(["notLoaded", "ended", "closed"] as const)(
  "an empty %s follow-up offers its controls before the editor is engaged",
  async (status) => {
    const user = userEvent.setup();
    await mountComposer("ref_a", {
      status: { type: status },
      evener: { ref: "ref_a", capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
    });
    const editor = textarea();
    expect(editor.style.minHeight).toBe("1lh");
    expect(screen.getByTestId("model-switch-trigger")).toBeTruthy();
    expect(submitButton().disabled).toBe(true);
    await user.click(screen.getByRole("button", { name: "Session actions" }));
    expect(editor.style.minHeight).toBe("3lh");
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Session actions" }));
    expect(editor.textContent).toBe("");
  },
);

test.each(["notLoaded", "ended", "closed"] as const)(
  "an empty %s follow-up keeps its session actions open when the editor loses focus",
  async (status) => {
    const user = userEvent.setup();
    await mountComposer("ref_a", {
      status: { type: status },
      evener: { ref: "ref_a", capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
    });
    await user.click(textarea());
    await user.click(screen.getByRole("button", { name: "Session actions" }));

    expect(screen.queryByRole("menu")).not.toBeNull();
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    expect(screen.getByRole("dialog", { name: "Rename session" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("button", { name: "Session actions" })).toBe(document.activeElement);
    render(<button type="button">Outside composer</button>);
    await user.click(screen.getByRole("button", { name: "Outside composer" }));
    await waitFor(() => expect(textarea().style.minHeight).toBe("1lh"));
    expect(screen.getByTestId("session-chrome-inline")).toBeTruthy();
    expect(textarea().textContent).toBe("");
  },
);

test("an empty exited follow-up can pick its next model before entering the editor", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "notLoaded" },
    evener: { ref: "ref_a", capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
  });
  fake.on("model/list", () => ({ data: [{ provider: "openai", model: "gpt-5.5" }] }));
  fake.on("thread/model/set", () => ({}));
  expect(textarea().style.minHeight).toBe("1lh");
  await user.click(screen.getByTestId("model-switch-trigger"));

  expect(screen.queryByRole("combobox")).not.toBeNull();
  await user.clear(screen.getByRole("combobox"));
  await user.click(await screen.findByRole("option", { name: /gpt-5.5/ }));
  await waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "thread/model/set").map((call) => call.params)).toEqual([
      { ref: "ref_a", modelProvider: "openai", model: "gpt-5.5" },
    ]),
  );
  render(<button type="button">Outside composer</button>);
  await user.click(screen.getByRole("button", { name: "Outside composer" }));
  await waitFor(() => expect(textarea().style.minHeight).toBe("1lh"));
  expect(screen.getByTestId("session-chrome-inline")).toBeTruthy();
});

test("an empty exited follow-up keeps its phone menus usable", async () => {
  const restoreViewport = installMobileViewport();
  try {
    const user = userEvent.setup();
    const fake = await mountComposer("ref_a", {
      status: { type: "notLoaded" },
      evener: { ref: "ref_a", capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
    });
    fake.on("model/list", () => ({ data: [{ provider: "openai", model: "gpt-5.5" }] }));
    fake.on("thread/model/set", () => ({}));
    expect(textarea().style.minHeight).toBe("1lh");
    await user.click(screen.getByRole("button", { name: "Session actions" }));
    expect(screen.queryByRole("menu")).not.toBeNull();
    await user.keyboard("{Escape}");
    await user.click(screen.getByTestId("model-switch-trigger"));
    expect(screen.getByRole("dialog", { name: "Choose model" })).toBeTruthy();
    await user.click(await screen.findByRole("option", { name: /gpt-5.5/ }));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(1));
    expect(document.activeElement).toBe(screen.getByTestId("model-switch-trigger"));
  } finally {
    cleanup();
    restoreViewport();
  }
});

test("an empty exited follow-up supports keyboard focus through its controls and picker", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "notLoaded" },
    evener: { ref: "ref_a", capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
  });
  fake.on("model/list", () => ({ data: [{ provider: "openai", model: "gpt-5.5" }] }));
  await user.click(textarea());
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Attach image" }));
  await user.tab();
  expect(document.activeElement).toBe(screen.getByTestId("model-switch-trigger"));
  await user.keyboard("{Enter}");
  expect(screen.queryByRole("combobox")).not.toBeNull();
  await user.keyboard("{Escape}");
  expect(document.activeElement).toBe(screen.getByTestId("model-switch-trigger"));
  expect(screen.queryByRole("combobox")).toBeNull();
});

test("an empty exited follow-up survives the native blur checkpoint before its next control gains focus", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "notLoaded" },
    evener: { ref: "ref_a", capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
  });
  const editor = textarea();
  await user.click(editor);
  const trigger = screen.getByRole("button", { name: "Session actions" });
  // Chrome runs microtasks after native blur, before the destination focus.
  await act(async () => {
    editor.blur();
    await Promise.resolve();
  });
  expect(trigger.isConnected).toBe(true);
  act(() => trigger.focus());
  await user.click(trigger);
  await act(async () => {
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  });
  expect(screen.queryByRole("menu")).not.toBeNull();
});

test("restores a stored draft into the textarea on mount", async () => {
  localStorage.setItem("evener.composer.draft.v1.ref_a", "unsent thought");
  await mountComposer("ref_a");
  expect(textarea().textContent).toBe("unsent thought");
});

test("typing persists the draft under this ref's storage key", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");
  await user.type(textarea(), "hi");
  expect(readComposerDraft("ref_a")).toEqual({ text: "hi", skillNames: [] });
});

// --- quote-insert (SelectionQuote's "Quote in reply" seam) -----------------
//
// SelectionQuote.tsx is a sibling component under Session.tsx, never a
// child of Composer - it hands quoted markdown to this ref's Composer via
// quoteInsert.ts's requestQuoteInsert/useQuoteInsertRequest pub/sub (that
// file's own header comment), not a prop. These tests exercise that seam
// from the Composer side only: they never render SelectionQuote, just call
// requestQuoteInsert directly, the same way the real bar would.

test("a controlled replacement over a leading chip replaces it rather than merging text", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_replace_with_chip";
  writeComposerDraft(ref, { text: "/cleanup", skillNames: ["cleanup"] });
  await mountComposer(ref, { evener: currentWorkEvener({ goal: true }) });
  const editor = textarea();
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));

  expect(editor.textContent).toBe("/goal Keep the session focused");
  expect(within(editor).queryAllByTestId("composer-skill-chip")).toHaveLength(0);
  expect(readComposerDraft(ref)).toEqual({ text: "/goal Keep the session focused", skillNames: [] });
});

test("a quote inserted before a leading chip leaves the chip whole and adds only its own text", async () => {
  const ref = "ref_inline_prefix_quote";
  writeComposerDraft(ref, { text: "/cleanup", skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);

  act(() => {
    requestQuoteInsert(ref, "/review ", "prefix");
  });

  await waitFor(() => expect(editor.textContent).toBe("/review /cleanup"));
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "/review /cleanup", skillNames: ["cleanup"] });
});

test("a quote-insert request writes the quoted markdown into an empty composer and focuses it", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n");
  });
  const editor = textarea();
  await waitFor(() => expect(editor.textContent).toBe("> quoted line\n\n"));
  expect(document.activeElement).toBe(editor);
});

test("a quote-insert request appends after a blank line, keeping whatever the user already typed", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");
  const editor = textarea();
  await user.type(editor, "mn");
  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n");
  });
  await waitFor(() => expect(editor.textContent).toBe("mn\n\n> quoted line\n\n"));
});

test("a quote-insert request for a DIFFERENT ref never reaches this composer", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestQuoteInsert("ref_other", "> quoted line\n\n");
  });
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("");
});

// The embedded cascade column put a second composer in the world for the
// same ref, so a quote request must name the seat it is quoting into: the
// pane id of the conversation the selection was made in. The routing
// contract lives in quoteInsert's read side - only the addressed seat's
// hook may see the request - because in production both composers' merge
// effects hold the same captured request within one commit and the first
// composer's consume cannot un-capture it. An unaddressed request keeps
// the historic any-composer routing for callers that cannot pin a
// destination.
test("an addressed quote-insert request is visible only to the addressed seat", async () => {
  const ref = "ref_addressed_seats";
  const seatA = renderHook(() => useQuoteInsertRequest(ref, "pane_a"));
  const seatB = renderHook(() => useQuoteInsertRequest(ref, "pane_b"));

  act(() => {
    requestQuoteInsert(ref, "> quoted line\n\n", "append", "pane_a");
  });

  expect(seatA.result.current?.text).toBe("> quoted line\n\n");
  expect(seatB.result.current).toBeUndefined();
  act(() => {
    consumeQuoteInsert(ref);
  });
  expect(seatA.result.current).toBeUndefined();
});

test("an addressed quote-insert request reaches the addressed seat's composer", async () => {
  const ref = "ref_two_seats";
  const fakeA = connectReadyFakeClient();
  fakeA.on("thread/read", () => readResponse(ref));
  const fakeB = connectReadyFakeClient();
  fakeB.on("thread/read", () => readResponse(ref));
  await threadsStore.getState().ensureThread(ref);
  render(
    <ClientProvider client={fakeA}>
      <Toast />
      <Composer ref={ref} paneId="pane_a" source={createTestComposerSource(ref)} focused={false} />
    </ClientProvider>,
  );
  render(
    <ClientProvider client={fakeB}>
      <Toast />
      <Composer ref={ref} paneId="pane_b" source={createTestComposerSource(ref)} focused={false} />
    </ClientProvider>,
  );
  await settleActivityDiscovery(ref);
  await flushPendingTurnsProjectionForTests();
  const editors = screen.getAllByRole("textbox", { name: /^message$/i });
  expect(editors).toHaveLength(2);
  const [seatA, seatB] = editors;
  if (!seatA || !seatB) throw new Error("Missing one of the two seat composers");

  act(() => {
    requestQuoteInsert(ref, "> quoted line\n\n", "append", "pane_a");
  });

  await waitFor(() => expect(seatA.textContent).toBe("> quoted line\n\n"));
  expect(seatB.textContent).toBe("");
  expect(document.activeElement).toBe(seatA);
});

test("goal editing focuses only the requesting seat's composer when two share a ref", async () => {
  const user = userEvent.setup();
  const overrides = { evener: currentWorkEvener({ goal: true }) };
  // Both seats live in ONE tree, like an ordinary pane and its embedded
  // column do in production: effects run in mount order, so an unaddressed
  // request deterministically reaches every seat.
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", overrides));
  await threadsStore.getState().ensureThread("ref_a");
  render(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref="ref_a" paneId="pane_a" source={createTestComposerSource("ref_a")} focused={false} />
      <Composer ref="ref_a" paneId="pane_b" source={createTestComposerSource("ref_a")} focused={false} />
    </ClientProvider>,
  );
  await settleActivityDiscovery("ref_a");
  await flushPendingTurnsProjectionForTests();
  const editors = screen.getAllByRole("textbox", { name: /^message$/i });
  expect(editors).toHaveLength(2);
  const [seatA, seatB] = editors;
  if (!seatA || !seatB) throw new Error("Missing one of the two seat composers");
  const seatAFocused = vi.fn();
  seatA.addEventListener("focus", seatAFocused);

  const editButtons = screen.getAllByRole("button", { name: /Edit goal: Keep the session focused/ });
  expect(editButtons).toHaveLength(2);
  await user.click(editButtons[1] ?? seatB);

  await waitFor(() => expect(document.activeElement).toBe(seatB));
  expect(seatB.textContent).toBe("/goal Keep the session focused");
  // The request must be addressed: the other seat never takes focus, so
  // typing lands in the requesting seat's editor alone.
  expect(seatAFocused).not.toHaveBeenCalled();
});

test("the composer anchor carries its owning ref and pane id for the hold-hint overlay", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_anchor"));
  await threadsStore.getState().ensureThread("ref_anchor");
  render(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref="ref_anchor" paneId="pane_anchor" source={createTestComposerSource("ref_anchor")} focused={false} />
    </ClientProvider>,
  );
  await settleActivityDiscovery("ref_anchor");
  await flushPendingTurnsProjectionForTests();

  // The hold-hint resolver (shell/holdhints) matches an anchor by BOTH its ref
  // and its pane id, so the anchor must expose the owning pane id alongside the
  // session ref.
  const anchor = screen.getByTestId("composer-form").closest("[data-composer]");
  expect(anchor?.getAttribute("data-composer")).toBe("ref_anchor");
  expect(anchor?.getAttribute("data-composer-pane")).toBe("pane_anchor");
});

test("the current-task control opens this column's tasks without stealing cascade focus", async () => {
  const user = userEvent.setup();
  const overrides = { evener: currentWorkEvener({ task: true }) };
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("remote:parent", overrides));
  await threadsStore.getState().ensureThread("remote:parent");
  // An embedded column's composer: the host cascade covers this
  // conversation, and an ordinary pane for the same conversation sits
  // beside it.
  workspaceStore.setState({
    panes: [
      {
        id: "zoom",
        type: "sessionZoom",
        params: {
          ref: "remote:leaf",
          source: { type: "session", params: { ref: "remote:root" } },
          edges: [{ ownerRef: "remote:parent", childRef: "remote:leaf", delegateId: "d1" }],
        },
        slot: "main",
      },
      { id: "pane-parent", type: "session", params: { ref: "remote:parent" }, slot: "secondary" },
    ],
    focusedPaneId: "zoom",
  });
  // The sidebar describes the focused leaf, like the zoom's own scope does.
  activitySidebarStore.getState().retarget("remote:leaf");
  render(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref="remote:parent" paneId="zoom" source={createTestComposerSource("remote:parent")} focused={false} />
    </ClientProvider>,
  );
  await settleActivityDiscovery("remote:parent");
  await user.click(screen.getByRole("button", { name: /Open tasks: Finish the focused composer test/ }));

  // The request came from the parent column's composer: the sidebar opens
  // that conversation's Tasks, holding its scope, and the cascade keeps
  // focus.
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "tasks", ref: "remote:parent" });
  expect(activitySidebarStore.getState().explicitScopeRef).toBe("remote:parent");
  expect(workspaceStore.getState().focusedPaneId).toBe("zoom");
});

test("the composer persists the quote-inserted text as this ref's draft", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n");
  });
  await waitFor(() => expect(readComposerDraft("ref_a")).toEqual({ text: "> quoted line\n\n", skillNames: [] }));
});

// SHOULD-FIX: requestQuoteInsert's own placement param (quoteInsert.ts) -
// "append" (the default, exercised above) keeps a quote after whatever the
// user already typed; "prefix" (the command palette's own slash-command
// insert - CommandPalette.tsx's activateCommand) puts the addition FIRST
// instead, with no separator, so a command lands where it can actually
// parse even against a non-empty draft.

test("placement 'append' on a seeded draft matches the unqualified default: appended after a blank line", async () => {
  localStorage.setItem(draftStorageKey("ref_a"), "my own note");
  await mountComposer("ref_a");
  const editor = textarea();
  expect(editor.textContent).toBe("my own note");

  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n", "append");
  });

  await waitFor(() => expect(editor.textContent).toBe("my own note\n\n> quoted line\n\n"));
  expect(editorCursor(editor)).toBe((editor.textContent ?? "").length);
});

test("placement 'prefix' on a seeded draft inserts the addition BEFORE the existing text, with no separator", async () => {
  localStorage.setItem(draftStorageKey("ref_a"), "my own note");
  await mountComposer("ref_a");
  const editor = textarea();
  expect(editor.textContent).toBe("my own note");

  act(() => {
    requestQuoteInsert("ref_a", "/p:review ", "prefix");
  });

  await waitFor(() => expect(editor.textContent).toBe("/p:review my own note"));
  // The cursor lands right after the inserted invocation, not at the very
  // end of the merged text - see Composer.tsx's own comment on the effect.
  expect(editorCursor(editor)).toBe("/p:review ".length);
});

// --- composer-focus seam (composerFocus.ts) ---------------------------------
//
// A global Mod+I chord (owned elsewhere - see composerFocus.ts's own header
// comment) will call requestComposerFocus(ref) to move keyboard focus into
// this ref's Composer. These tests exercise that seam from the Composer
// side only, the same way the quote-insert tests above call
// requestQuoteInsert directly rather than rendering the chord's own owner.

test("a composer-focus request focuses this ref's textarea", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestComposerFocus("ref_a");
  });
  await waitFor(() => expect(document.activeElement).toBe(textarea()));
});

test("a composer-focus request for a DIFFERENT ref never focuses this composer", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestComposerFocus("ref_other");
  });
  await flushPendingTurnsProjectionForTests();
  expect(document.activeElement).not.toBe(textarea());
});

// The card is widgets/promptcard now, so the focus-ring RULE is that widget's
// own contract (promptcard.test.tsx pins the declaration). What stays this
// component's business is that it renders the shared card at all and that the
// seamless field inside really drives the card's focus state. Only the
// post-focus state is queried: jsdom's selector engine caches a :focus-within
// result per element, so an earlier "not yet focused" call on the same node
// would keep answering false afterwards.
test("focusing the message field lights the shared prompt card's own focus affordance", async () => {
  await mountComposer("ref_a");
  act(() => textarea().focus());
  expect(screen.getByTestId("composer-input-card").matches(":focus-within")).toBe(true);
});

// The composer and the spawn form are the SAME object: both render
// widgets/promptcard, not two components that merely resemble each other. The
// class on the rendered card is the proof that reaches across both files. The
// session chrome shares PromptCard's leading run with the attachment control,
// after the paperclip, so there is exactly one status row in this surface.
test("the composer's shared PromptCard leads with the attachment and inline session controls", async () => {
  await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0 },
      contextUsed: 64_000,
      contextWindow: 128_000,
      contextPressure: 0.5,
      contextAssumed: false,
    },
  });
  const card = screen.getByTestId("composer-input-card");
  const attach = within(card).getByTestId("composer-attach");
  const inline = within(card).getByTestId("session-chrome-inline");

  expect(card.className.split(" ")).toContain(promptCardStyles.card);
  expect(attach.compareDocumentPosition(inline) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(within(card).getByTestId("status-row-context")).toBeTruthy();
  expect(screen.queryAllByTestId("status-row")).toHaveLength(1);
});

// The chords moved out of the buttons into their tooltips, so each control's
// spoken name is now just its verb. These are the ONE place that asserts
// accessible names deliberately; everywhere else addresses controls by testid.
test("each control's spoken name is its bare verb - no chord glyphs in the name or the label", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(screen.getByRole("button", { name: "Stop" })).toBe(stopButton());
  act(() => replaceEditorText(textarea(), "draft"));
  expect(screen.getByRole("button", { name: "Send" })).toBe(submitButton());
  expect(screen.getByRole("button", { name: "Steer" })).toBe(steerButton());
  expect(screen.getByRole("button", { name: "Attach image" })).toBe(screen.getByTestId("composer-attach"));
});

// The boxed <kbd> runs are gone from inside the buttons - three nested boxes
// dominated the button they annotated. The chord still has to be DISCOVERABLE,
// which is what each button's Tooltip is for: controlTooltips.test.ts pins
// every label, and the hover tests below prove a label reaches its bubble.
test("no button renders a chord hint inside itself", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });

  const stop = stopButton();
  act(() => replaceEditorText(textarea(), "draft"));
  for (const control of [stop, submitButton(), steerButton()]) {
    expect(control.querySelector("kbd")).toBeNull();
    expect(control.textContent).not.toMatch(/↵/);
  }
});

// Density: the control row is the 24px (xs) size. Three nested gaps plus the
// card's padding plus a taller row stacked up to a block far taller than the
// input it framed.
test("every control in the composer's button row is the xs (24px) size", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });

  // IconButton overrides Button's own xs/sm/md with its square sizing (see
  // iconbutton.module.css), so the one icon control carries that module's squareXs.
  expect(screen.getByTestId("composer-attach").className.split(" ")).toContain(iconButtonStyles.squareXs);
  const stop = stopButton();
  act(() => replaceEditorText(textarea(), "draft"));
  for (const control of [stop, steerButton(), submitButton()]) {
    expect(control.className.split(" ")).toContain(buttonStyles.xs);
  }
});

// Stop is the WORD, in danger ink, not a filled square glyph: it is chrome, and
// chrome speaks. dangerQuiet keeps the hue on the label rather than as a fill
// competing with the primary control beside it.
test("Stop renders as the word in the dangerQuiet variant, not as an icon", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(stopButton().textContent).not.toBe("");
  expect(stopButton().querySelector("svg")).toBeNull();
  expect(stopButton().className.split(" ")).toContain(buttonStyles.dangerQuiet);
});

// The attach control stays an icon (a paperclip needs no word), and stays a
// real SVG rather than a "📎"/"+" character whose weight and baseline shift
// from font to font.
test("the attach control draws an SVG glyph, not a literal text character", async () => {
  await mountComposer("ref_a");
  const attach = screen.getByTestId("composer-attach");
  expect(attach.querySelector("svg")).toBeTruthy();
  expect(attach.textContent).toBe("");
});

// --- the verb cluster's order and emphasis ---------------------------------
//
// Stop and Send share the first slot; wide Steer stays to its right.
test("Stop and Send alternate before Steer", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  const card = screen.getByTestId("composer-input-card");
  const order = () =>
    [...card.querySelectorAll("button")]
      .map((b) => b.getAttribute("data-testid"))
      .filter((id) => id?.startsWith("composer-") && id !== "composer-attach");
  expect(order()).toEqual(["composer-stop", "composer-steer"]);
  act(() => replaceEditorText(textarea(), "draft"));
  expect(order()).toEqual(["composer-submit", "composer-steer"]);
});

// Jesse's own correction, honored exactly: both verbs stay, with distinct jobs.
// While a turn runs Steer is the primary (interrupt and redirect NOW) and Send
// sits beside it quiet, queueing until the agent stops. Idle, Send is the
// primary and sends immediately. A label never changes meaning under the user.
test("while a turn runs, Steer is primary and Send is quiet", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  act(() => replaceEditorText(textarea(), "draft"));
  expect(steerButton().className.split(" ")).toContain(buttonStyles.primary);
  expect(submitButton().className.split(" ")).toContain(buttonStyles.quiet);
});

test("with nothing running, Send is the primary, sends now, and there is no Steer to outrank it", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(submitButton().className.split(" ")).toContain(buttonStyles.primary);
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  // The idle half of Send's timing reaching its bubble (canQueue false).
  expect(hoverForTooltip(submitButton()).textContent).toBe(submitTooltipLabel({ canQueue: false, enterToSend: false }));
});

// The label is stable across states even though the ROUTE isn't: a mid-turn
// Send queues (turn/queue, proven by the routing tests below) but still reads
// "Send", because the change is one of timing, not of verb. The tooltip is
// where the timing is spelled out (controlTooltips.test.ts).
test("Send keeps its label while a turn runs", async () => {
  const idle = await mountComposerWithHandle("ref_idle", { status: { type: "idle" } });
  const idleLabel = submitButton().textContent;
  idle.unmount();
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  act(() => replaceEditorText(textarea(), "draft"));
  expect(idleLabel).not.toBe("");
  expect(submitButton().textContent).toBe(idleLabel);
});

// cezn: Send and Steer sit side by side while a turn runs, and Send's own
// label never changes whether it fires now or queues - only the tooltip
// above disambiguates, and it needs a 300ms hover nobody mid-flow gives it.
// This caption says the same thing the tooltip does, WITHOUT a hover -
// always on screen the instant the control row itself is, non-color
// (plain caption text), and it never touches the Send/Steer labels
// themselves (additive only, per Composer.tsx's own top-of-file reasoning
// for keeping one label).
test("the timing caption is absent when a turn is busy and queueing is available", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(screen.queryByText(/send queues until the agent stops/i)).toBeNull();
});

// Idle Send is unambiguous (there's no Steer beside it, and no timing
// question to answer), so the caption would be noise there - it only earns
// its place while the ambiguity it resolves actually exists.
test("the timing caption is absent when nothing is running", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

// A turn can be busy (Stop/Steer showing) while the source has explicitly
// advertised no queue capability - deriveSendQueueAvailability's own
// both-false branch. Send is disabled there (canCompose is false), so a
// caption claiming it queues would be a lie; this pins the caption to
// availability.canQueue, not to `busy` alone.
// status can read "active" before turn/started has populated activeTurnId
// (a hydrate cut inside that window, or a session holding queued work) -
// Stop/Steer follow the status there (isTurnActive), and the caption stays
// absent all the same.
test("the timing caption is absent while status reads active but no turn has actually started yet", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

test("the timing caption is absent when busy but the source advertises no queue capability", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    evener: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, queue: false },
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

// The timing caption has been removed entirely (kata mx43), but this test
// verifies the removal is complete by confirming it does not appear even in
// scenarios where it previously would have shown.
test("the timing caption is absent while an ask_user question is pending, even though the turn is busy and queueing is available", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    // askPending: see the comment on the other pendingAskTurns() call site above.
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0 },
      activeTurnId: "turn_1",
      askPending: true,
    },
    ...pendingAskTurns(),
  });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

// --- send / queue routing ---------------------------------------------------

test("idle session: submit posts turn/start with the composer text", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "ha");
  expect(submitButton().disabled).toBe(false);
  await user.click(submitButton());

  await flushPendingTurnsProjectionForTests();
  const call = fake.calls.find((c) => c.method === "turn/start");
  expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "ha" }] });
});
