import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { Thread } from "@evener/appwire-client";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { MotionProvider } from "../../../motion";
import {
  activitySidebarStore,
  resetActivitySidebarStoreForTests,
} from "../../../shell/activitybar/activitySidebarStore";
import { ActivitySidebar } from "../../../shell/activitybar/activitySidebarTestUtils";
import { ClientProvider } from "../../../shell/clientContext";
import { paletteStore } from "../../../shell/palette/paletteController";
import { MOBILE_QUERY } from "../../../shell/useIsMobile";
import { isPaneOpen, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { resetActivityPanelStoreForTests } from "../../../stores/activityPanel";
import { useCommandCatalog } from "../../../stores/commandCatalog";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetPrefsStoreForTests } from "../../../stores/prefs";
import { answerActivityRead } from "../../../stores/sessionActivityTestUtils";
import { holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../stores/threads";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { ALL_CAPABILITIES as FULL_CAPABILITIES } from "../../../threadTestUtils";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { createTestComposerSource } from "../testing/composerSource";
import { editorCursor, pastePngInto, replaceEditorText, selectEditorText } from "../testing/editor";
import { installMobileViewport } from "../testing/mobileViewport";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { resetComposerFocusStoreForTests } from "./composerFocus";
import {
  Composer,
  currentWorkEvener,
  installCanvasStubs,
  installGatedDecodeStub,
  installNarrowComposer,
  installStalledDecodeStub,
  mountComposer,
  mountComposerWithHandle,
  pendingAskTurns,
  REVIEW_RELEASE_CATALOG,
  readResponse,
  seedRejectedRecovery,
  slashOptions,
  textarea,
} from "./composerTestUtils";
import { readDraft, writeComposerDraft } from "./draft";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";
import { resetQuoteInsertStoreForTests } from "./quoteInsert";

beforeAll(async () => {
  installLocalStorage(new MemoryStorage());
  await import("../index");
});

class ControlledDiscardStorage extends MutationOutboxIndexedDB {
  discardStarted: Promise<void> = Promise.resolve();
  private pausedClientMutationId: string | null = null;
  private markDiscardStarted: (() => void) | undefined;
  private releaseDiscard: (() => void) | undefined;
  private discardGate: Promise<void> = Promise.resolve();

  pauseDiscard(clientMutationId: string): void {
    this.pausedClientMutationId = clientMutationId;
    this.discardStarted = new Promise((resolve) => {
      this.markDiscardStarted = resolve;
    });
    this.discardGate = new Promise((resolve) => {
      this.releaseDiscard = resolve;
    });
  }

  release(): void {
    this.releaseDiscard?.();
  }

  override async discardRecovery(clientMutationId: string, shouldDiscard?: () => boolean): Promise<boolean> {
    if (clientMutationId === this.pausedClientMutationId) {
      this.markDiscardStarted?.();
      await this.discardGate;
    }
    return super.discardRecovery(clientMutationId, shouldDiscard);
  }
}

function installDuoCoverViewport(): void {
  vi.stubGlobal("matchMedia", (media: string) => ({
    media,
    matches: media === "(min-width: 440px) and (max-width: 500px) and (max-aspect-ratio: 3 / 4)",
    addEventListener() {},
    removeEventListener() {},
  }));
}

test("the Duo cover composer expands on focus and retains its two-line draft height", async () => {
  installDuoCoverViewport();
  installNarrowComposer(450);
  await mountComposerWithHandle("ref_a");
  const editor = textarea();
  expect(editor.style.minHeight).toBe("1lh");
  act(() => editor.focus());
  await waitFor(() => expect(editor.style.minHeight).toBe("2lh"));
  await replaceEditorText(editor, "a saved draft");
  expect(editor.style.minHeight).toBe("2lh");
  act(() => editor.blur());
  await waitFor(() => expect(editor.style.minHeight).toBe("2lh"));
});

test("a narrow desktop pane keeps its two-line resting height", async () => {
  installNarrowComposer(382);
  await mountComposerWithHandle("ref_a");
  expect(textarea().style.minHeight).toBe("2lh");
});

test("the wide empty composer keeps its two-line resting height", async () => {
  await mountComposerWithHandle("ref_a");
  expect(textarea().style.minHeight).toBe("2lh");
});

test("onEditingChange follows IME composition, not editor focus or draft content", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  writeComposerDraft("ref_a", { text: "saved draft", skillNames: [] });
  const onEditingChange = vi.fn();
  render(
    <ClientProvider client={fake}>
      <Composer
        ref="ref_a"
        source={createTestComposerSource("ref_a")}
        focused={false}
        onEditingChange={onEditingChange}
      />
    </ClientProvider>,
  );
  await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();
  const editor = screen.getByRole("textbox", { name: "Message" });
  // Draft content alone is not editing.
  expect(editor.textContent).toContain("saved draft");
  expect(onEditingChange).not.toHaveBeenCalled();

  // Focus alone is not editing: a session open autofocuses this editor, and
  // the floating control must stay available.
  fireEvent.focus(editor);
  expect(onEditingChange).not.toHaveBeenCalled();

  fireEvent.compositionStart(editor);
  expect(onEditingChange).toHaveBeenLastCalledWith(true);
  // An IME composition keeps the editing state even across a transient blur.
  fireEvent.blur(editor);
  expect(onEditingChange).toHaveBeenLastCalledWith(true);
  fireEvent.compositionEnd(editor);
  expect(onEditingChange).toHaveBeenLastCalledWith(false);
  expect(readDraft("ref_a")).toBe("saved draft");
});

test("while ask_pending is open, the message textbox is hidden and the dock is not the composer's surface", async () => {
  await mountComposer("ref_a", {
    ...pendingAskTurns(),
    // askPending is the wire's own source for a pending ask (deriveAskQuestions.ts);
    // a thread whose turns carry a completed, unanswered ask_user call must also
    // carry the flag the hub's own stampAskPendingOnStatusChange would stamp.
    evener: { ...currentWorkEvener({ task: true, goal: true }), askPending: true },
  });

  // The answering surface moved to the transcript's trailing row (Session.tsx
  // passes AskDock as TranscriptBody's trailingRow; AskDock.test.tsx and
  // Session.test.tsx prove that half). The composer keeps its own half of the
  // contract: hiding the input row while a question is pending.
  expect(screen.queryByRole("textbox", { name: /message/i })).toBeNull();
  expect(screen.queryByTestId("current-work")).toBeNull();
  expect(document.querySelector("[data-ask-response-dock]")).toBeNull();
});

test("renders current work directly before the compose card", async () => {
  await mountComposer("ref_a", {
    evener: currentWorkEvener({ task: true, goal: true }),
  });

  const currentWork = screen.getByTestId("current-work");
  const composerCard = screen.getByTestId("composer-input-card");
  expect(currentWork.compareDocumentPosition(composerCard) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
});

test("the current-work goal row carries the goal's live status", async () => {
  await mountComposer("ref_a", {
    evener: currentWorkEvener({ goal: true }),
  });

  expect(screen.getByTestId("current-work-goal-status").dataset.state).toBe("active");
});

test("clicking the current goal fills and focuses an empty composer with an editable goal command", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    evener: currentWorkEvener({ goal: true }),
  });

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));

  const editor = textarea();
  expect(editor.textContent).toBe("/goal Keep the session focused");
  expect(document.activeElement).toBe(editor);
  expect(screen.queryByRole("dialog", { name: "Replace draft?" })).toBeNull();
});

test("clicking the current goal confirms before replacing an existing draft", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    evener: currentWorkEvener({ goal: true }),
  });
  const editor = textarea();
  await user.type(editor, "ud");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  expect(screen.getByRole("dialog", { name: "Replace draft?" })).toBeTruthy();
  expect(editor.textContent).toBe("ud");

  await user.click(screen.getByRole("button", { name: "Keep draft" }));
  expect(screen.queryByRole("dialog", { name: "Replace draft?" })).toBeNull();
  expect(editor.textContent).toBe("ud");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));
  expect(editor.textContent).toBe("/goal Keep the session focused");
  expect(document.activeElement).toBe(editor);
});

test("editing the goal confirms before replacing whitespace-only draft text", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { evener: currentWorkEvener({ goal: true }) });
  const editor = textarea();
  replaceEditorText(editor, " \n\t");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));

  expect(screen.getByRole("dialog", { name: "Replace draft?" })).toBeTruthy();
  expect(editor.textContent).toBe(" \n\t");
});

test("editing the goal confirms before replacing a draft that contains only attachments", async () => {
  installStalledDecodeStub();
  const user = userEvent.setup();
  await mountComposer("ref_a", { evener: currentWorkEvener({ goal: true }) });
  const editor = textarea();
  act(() => pastePngInto(editor));
  replaceEditorText(editor, "");
  expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));

  expect(screen.getByRole("dialog", { name: "Replace draft?" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy();
});

test("confirmed goal replacement clears settled attachments and persists an ordinary goal draft", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  await mountComposer("ref_a", { evener: currentWorkEvener({ goal: true }) });
  const editor = textarea();
  act(() => pastePngInto(editor));
  await screen.findByRole("button", { name: "View shot.png" });
  replaceEditorText(editor, "");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));

  expect(editor.textContent).toBe("/goal Keep the session focused");
  expect(screen.queryByRole("button", { name: "Remove shot.png" })).toBeNull();
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");
});

test("confirmed goal replacement invalidates pending attachments without later changing the goal command", async () => {
  const gate = installGatedDecodeStub();
  const user = userEvent.setup();
  await mountComposer("ref_a", { evener: currentWorkEvener({ goal: true }) });
  const editor = textarea();
  act(() => pastePngInto(editor));
  replaceEditorText(editor, "");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));
  expect(editor.textContent).toBe("/goal Keep the session focused");

  await act(async () => {
    await gate.release();
  });
  expect(editor.textContent).toBe("/goal Keep the session focused");
  expect(screen.queryAllByRole("button", { name: /^Remove/ })).toHaveLength(0);
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");
});

test("an empty rejected drain stays a notice and leaves the caret stable", async () => {
  // A rejected turn/drainAsSteer recovers with no input. The 2026-11-09
  // lifecycle ruling makes it a notice, not a draft, so mount restore must not
  // activate it into the empty composer.
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "", { method: "turn/drainAsSteer", input: [] });
  await mountComposer("ref_a", { status: { type: "idle" } });
  await flushPendingTurnsProjectionForTests();
  // The refused row stays in the strip as the notice; nothing was activated or
  // re-persisted, so the next keystroke lands at the caret already there.
  expect(await storage.getRecovery(recovered.clientMutationId)).toBeDefined();
  expect(screen.getByRole("button", { name: "Edit message" })).toBeDefined();
  const editor = textarea();
  expect(editor.textContent).toBe("");

  const user = userEvent.setup();
  selectEditorText(editor, 0);
  await user.keyboard("h");

  expect(editor.textContent).toBe("h");
  expect(editorCursor(editor)).toBe(1);
});

test("confirmed goal replacement exits recovery without deleting its durable recovery row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "recover this later");
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: currentWorkEvener({ goal: true }),
  });
  await flushPendingTurnsProjectionForTests();
  const editor = textarea();
  expect(editor.textContent).toBe("recover this later");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));
  await flushPendingTurnsProjectionForTests();

  expect(editor.textContent).toBe("/goal Keep the session focused");
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");
  expect(await storage.getRecovery(recovered.clientMutationId)).toBeDefined();
  expect(screen.getByText("recover this later")).toBeTruthy();
});

test("goal replacement preserves a recovery row whose empty-draft discard is already pending", async () => {
  const storage = new ControlledDiscardStorage();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "clear me locally");
  storage.pauseDiscard(recovered.clientMutationId);
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: currentWorkEvener({ goal: true }),
  });
  await flushPendingTurnsProjectionForTests();
  await user.clear(textarea());
  await storage.discardStarted;

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");

  storage.release();
  await flushPendingTurnsProjectionForTests();

  expect((await storage.listRecovery("ref_a")).map((row) => row.clientMutationId)).toEqual([
    recovered.clientMutationId,
  ]);
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");
});

test("goal replacement preserves the consolidated recovery owner while its commit is unobserved", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const folded = await seedRejectedRecovery(storage, "ref_a", "first recovery");
  const owner = await seedRejectedRecovery(storage, "ref_a", "second recovery");
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: currentWorkEvener({ goal: true }),
  });
  await flushPendingTurnsProjectionForTests();
  // Mount restore picks the NEWEST rejected composition this seat owns.
  expect(textarea().textContent).toBe("second recovery");
  const sourceRow = screen.getByText("first recovery").closest("li");
  if (!sourceRow) throw new Error("missing first recovery row");
  const hold = holdNextWriteTransaction(["recovery", "outbox"]);
  try {
    await user.click(within(sourceRow).getByRole("button", { name: "Edit message" }));
    await hold.reached;
    await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
    await user.click(screen.getByRole("button", { name: "Replace draft" }));
    expect(readDraft("ref_a")).toBe("/goal Keep the session focused");
    await act(async () => hold.release());
    await flushPendingTurnsProjectionForTests();
    const reopened = new MutationOutboxIndexedDB();
    try {
      const records = await reopened.listRecovery("ref_a");
      expect(records.map((row) => row.clientMutationId)).toEqual([owner.clientMutationId]);
      expect(records[0]?.composerText).toBe("second recovery\n\nfirst recovery");
      expect(records[0]?.payload.input).toEqual([{ type: "text", text: "second recovery\n\nfirst recovery" }]);
      expect(await reopened.getRecovery(folded.clientMutationId)).toBeUndefined();
      expect(textarea().textContent).toBe("/goal Keep the session focused");
      expect(readDraft("ref_a")).toBe("/goal Keep the session focused");
    } finally {
      reopened.close();
    }
  } finally {
    hold.release();
  }
});

test("goal replacement closes slash completion and resets selection", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    evener: { ...currentWorkEvener({ goal: true }), diagnostics: { commands: REVIEW_RELEASE_CATALOG } },
  });
  const editor = textarea();
  await user.type(editor, "hi /re");
  await user.keyboard("{ArrowDown}");
  expect(slashOptions()[1]?.getAttribute("aria-selected")).toBe("true");

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  await user.click(screen.getByRole("button", { name: "Replace draft" }));
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();

  await user.clear(editor);
  await user.type(editor, "hi /re");
  expect(slashOptions()[0]?.getAttribute("aria-selected")).toBe("true");
});

test("goal replacement focus waits until an ended follow-up textarea mounts", async () => {
  const user = userEvent.setup();
  let overrides: Partial<Thread> = {
    status: { type: "closed" },
    evener: {
      ...currentWorkEvener({ goal: true }),
      capabilities: { ...FULL_CAPABILITIES, send: false },
    },
  };
  const fake = await mountComposer("ref_a", overrides);
  fake.on("thread/read", () => readResponse("ref_a", overrides));
  expect(screen.queryByRole("textbox", { name: /^message$/i })).toBeNull();

  await user.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
  expect(readDraft("ref_a")).toBe("/goal Keep the session focused");

  overrides = {
    ...overrides,
    evener: { ...currentWorkEvener({ goal: true }), capabilities: FULL_CAPABILITIES },
  };
  await act(async () => {
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
  });

  // The follow-up card mounts once the resync's read lands, so the editor is
  // re-queried on every poll rather than cached before the mount settles.
  await waitFor(() => expect(document.activeElement).toBe(textarea()));
  expect(textarea().textContent).toBe("/goal Keep the session focused");
});

// Desktop Tasks everywhere is the activity sidebar, preselected to its tasks
// tab (the same retarget the rail row and the chrome menu share) - the
// current-task button opens no sessionTasks pane. Mobile keeps the Sheet (the
// mobile test below pins that).
test("clicking the current task opens the activity sidebar's tasks tab, never a pane, on desktop", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    evener: currentWorkEvener({ task: true }),
  });

  await user.click(screen.getByRole("button", { name: "Open tasks: Finish the focused composer test" }));
  expect(activitySidebarStore.getState().open).toBe(true);
  expect(activitySidebarStore.getState().tab).toBe("tasks");
  expect(isPaneOpen(workspaceStore.getState(), "sessionTasks", { ref: "ref_a" })).toBe(false);
  // act(): the chrome subscribes to focus-derived state (its Activity check
  // reads currentSessionRef), so this raw store mutation re-renders it.
  act(() => {
    workspaceStore.setState({ focusedPaneId: null });
  });

  // Idempotent: a second click keeps the sidebar on the tasks tab and still
  // opens no pane.
  await user.click(screen.getByRole("button", { name: "Open tasks: Finish the focused composer test" }));
  expect(activitySidebarStore.getState().open).toBe(true);
  expect(activitySidebarStore.getState().tab).toBe("tasks");
  expect(
    workspaceStore
      .getState()
      .panes.filter((pane) => pane.type === "sessionTasks" && (pane.params as { ref?: string }).ref === "ref_a"),
  ).toHaveLength(0);
});

test("clicking the current task opens the existing mobile tasks sheet for this session", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    evener: currentWorkEvener({ task: true }),
  });
  let calledRef: unknown;
  fake.on("evener/tasks/list", (params) => {
    calledRef = params.ref;
    return { data: [] };
  });

  await user.click(screen.getByRole("button", { name: "Open tasks: Finish the focused composer test" }));
  await waitFor(() => expect(calledRef).toBe("ref_a"));
  expect(isPaneOpen(workspaceStore.getState(), "sessionTasks", { ref: "ref_a" })).toBe(false);
  restoreViewport();
});

test("the composer region fills pane height and bottom-anchors the replacement slot", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "composer.module.css"), "utf8");
  expect(css).toContain("flex: 1 1 auto");
  expect(css).toContain("min-height: 0");
  expect(css).toContain("justify-content: flex-end");
});

// The word beside the paper plane collapses by PANE width, not viewport
// width: a docked pane squeezed narrow on a desktop display needs the same
// icon-only Send the phone gets, and a viewport media query cannot see that
// (the overflowguard's 390px-pane-in-desktop-window measurement proved it).
// The 559px boundary matches the status row's own first compression threshold (statusrow.module.css).
test("the Send button's word collapses to the glyph below the compact pane threshold", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "composer.module.css"), "utf8");
  expect(css).toMatch(/\.composer\s*\{[^}]*container-type:\s*inline-size/);
  expect(css).toMatch(/@container \(max-width: 559px\)[\s\S]*?\.submitLabel\s*\{[^}]*display:\s*none/);
});

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  localStorage.clear();
  resetPrefsStoreForTests();
  resetConnectionStoreForTests();
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

test.each([false, true])("repeated composer /status targets its ref, phone=%s", async (mobile) => {
  vi.stubGlobal("matchMedia", (media: string) => ({
    media,
    matches: mobile && media === MOBILE_QUERY,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
  const user = userEvent.setup();
  const { fake } = await mountComposerWithHandle(
    "ref_a",
    {},
    {
      focused: true,
      prepare: (fake) => fake.on("evener/thread/activity/read", answerActivityRead),
    },
  );
  act(() => {
    workspaceStore.setState({
      panes: [
        { id: "command-a", type: "session", params: { ref: "ref_a" }, slot: "main" },
        { id: "focused-b", type: "session", params: { ref: "ref_b" }, slot: "secondary" },
      ],
      focusedPaneId: "focused-b",
    });
  });
  render(
    <MotionProvider>
      <ActivitySidebar mobile={mobile} />
    </MotionProvider>,
  );
  for (let invocation = 0; invocation < 2; invocation += 1) {
    const editor = textarea();
    await user.click(editor);
    act(() => replaceEditorText(editor, "/status"));
    await user.keyboard("{Enter}");
    expect(editor.textContent).toBe("/status ");
    await user.keyboard("{Meta>}{Enter}{/Meta}");
    await waitFor(() => expect(editor.textContent).toBe(""));
    expect(workspaceStore.getState().panes.some((pane) => pane.type === "sessionDetails")).toBe(false);
    await waitFor(() =>
      expect(activitySidebarStore.getState()).toMatchObject({
        open: true,
        tab: "about",
        ref: "ref_a",
      }),
    );
    expect(screen.getByRole("radio", { name: "About" }).getAttribute("aria-checked")).toBe("true");
    if (mobile && invocation === 0) {
      await waitFor(() => expect(screen.getByTestId("activity-sidebar").contains(document.activeElement)).toBe(true));
    }
  }
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "sessionDetails")).toBe(false);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
});

// A phone's composer rests by shedding what an empty editor does not need -
// attach, the status chips and the activity chips - while the session menu
// and Send stay, so a landscape phone spends its height on the transcript
// and session actions stay reachable without focusing first. Focus engages
// the full row again.
test("a resting phone composer sheds chips and attach until focus engages the full row", async ({ onTestFinished }) => {
  onTestFinished(installMobileViewport());
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  render(
    <ClientProvider client={fake}>
      <Composer ref="ref_a" source={createTestComposerSource("ref_a")} focused={false} />
    </ClientProvider>,
  );
  await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();

  // At rest: no attach, no status chips, no activity chips. The menu-only
  // chrome keeps the session actions reachable, and Send stays.
  expect(screen.queryByTestId("composer-attach")).toBeNull();
  expect(screen.queryByTestId("session-chrome-inline")).toBeNull();
  expect(document.querySelector('[data-testid="composer-form"] [data-activity-tab]')).toBeNull();
  expect(screen.getByTestId("session-chrome-menu")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Session actions" })).toBeTruthy();
  expect(screen.getByTestId("composer-submit")).toBeTruthy();

  // Focus engages the full row.
  fireEvent.focus(screen.getByRole("textbox", { name: "Message" }));
  await waitFor(() => expect(screen.getByTestId("composer-attach")).toBeTruthy());
  expect(screen.getByTestId("session-chrome-inline")).toBeTruthy();
  expect(screen.getByTestId("composer-submit")).toBeTruthy();
  expect(document.querySelector('[data-testid="composer-form"] [data-activity-tab]')).toBeTruthy();
  expect(screen.queryByTestId("session-chrome-menu")).toBeNull();
});

// A cascade column's chrome renders the conversation's activity controls
// below the footer (CascadeColumn's ScopeActivityControls), so its seat
// declares that ownership and the phone composer row keeps no second set of
// chips beside the status chrome, even engaged.
test("a composer whose seat owns the activity counts never mounts chips", async ({ onTestFinished }) => {
  onTestFinished(installMobileViewport());
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_owned_counts"));
  await threadsStore.getState().ensureThread("ref_owned_counts");
  render(
    <ClientProvider client={fake}>
      <Composer
        ref="ref_owned_counts"
        source={createTestComposerSource("ref_owned_counts")}
        focused={false}
        ownsActivityCounts
      />
    </ClientProvider>,
  );
  await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();

  // The engaged row keeps the status chrome and attach, but no chips.
  fireEvent.focus(screen.getByRole("textbox", { name: "Message" }));
  await waitFor(() => expect(screen.getByTestId("session-chrome-inline")).toBeTruthy());
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  expect(document.querySelector('[data-testid="composer-form"] [data-activity-tab]')).toBeNull();
});

// A turn in flight keeps its Stop control even while the editor rests
// collapsed: stopping a runaway turn must never depend on focusing first.
test("a collapsed mobile composer still offers Stop while a turn is running", async ({ onTestFinished }) => {
  onTestFinished(installMobileViewport());
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "active" } } as Partial<Thread>));
  await threadsStore.getState().ensureThread("ref_a");
  render(
    <ClientProvider client={fake}>
      <Composer ref="ref_a" source={createTestComposerSource("ref_a")} focused={false} />
    </ClientProvider>,
  );
  await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(screen.getByTestId("composer-stop")).toBeTruthy());
});

// A press on a row control can blur the focused editor to body without
// moving focus, and the press's focus transfer lands only a task after its
// click. The collapse must hold through the press - clearing engagement on
// the blur's frame would unmount the pressed control and lose its action -
// and then rest once the press's release reconciles where focus sits.
test("a phone press keeps the pressed control mounted through the press", async ({ onTestFinished }) => {
  onTestFinished(installMobileViewport());
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  render(
    <ClientProvider client={fake}>
      <Composer ref="ref_a" source={createTestComposerSource("ref_a")} focused={false} />
    </ClientProvider>,
  );
  const editor = await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();

  // The editor's focus engages the full row.
  await act(async () => {
    editor.focus();
  });
  await waitFor(() => expect(screen.getByTestId("composer-attach")).toBeTruthy());

  // A press on Attach blurs the editor to body without focusing the button;
  // the pressed control must stay mounted through the press and its click.
  await act(async () => {
    fireEvent.pointerDown(screen.getByTestId("composer-attach"));
  });
  await act(async () => {
    editor.blur();
  });
  // jsdom schedules the frame on a ~16ms timer, so a plain task yield never
  // reaches it; wait real milliseconds inside act() so the blur's frame has
  // fired before the mid-press assertion.
  await act(async () => {
    await new Promise((resolve) => {
      setTimeout(resolve, 50);
    });
  });
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  await act(async () => {
    fireEvent.click(screen.getByTestId("composer-attach"));
  });
  await act(async () => {
    await nextMacrotask();
  });

  // The press released and focus stayed on body: the row rests again, with
  // the controls that never shed still in it.
  await waitFor(() => expect(screen.queryByTestId("composer-attach")).toBeNull());
  expect(screen.getByTestId("composer-submit")).toBeTruthy();
});
