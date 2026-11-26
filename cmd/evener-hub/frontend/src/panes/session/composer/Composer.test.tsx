import type { Thread } from "@evener/appwire-client";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
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
import { useCommandCatalog } from "../../../stores/commandCatalog";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetPrefsStoreForTests } from "../../../stores/prefs";
import { answerActivityRead } from "../../../stores/sessionActivityTestUtils";
import { holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../stores/threads";
import { readModuleCss, topRuleBlock } from "../../../styles/cssBlock";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { ALL_CAPABILITIES as FULL_CAPABILITIES } from "../../../threadTestUtils";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import statusRowStyles from "../chrome/statusrow.module.css";
import { settleActivityDiscovery } from "../testing/activityDiscovery";
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
  PAST_THREAD_CAPABILITIES,
  pendingAskTurns,
  REVIEW_RELEASE_CATALOG,
  readResponse,
  seedRejectedRecovery,
  slashOptions,
  textarea,
} from "./composerTestUtils";
import { readDraft, writeComposerDraft } from "./draft";
import { DUO_COVER_QUERY, LANDSCAPE_PHONE_QUERY } from "./narrowComposer";
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

test.each([
  { name: "wide desktop", compact: false, inlineSteer: true },
  { name: "narrow desktop", compact: false, inlineSteer: false, width: 382 },
  { name: "Duo cover", compact: false, inlineSteer: true, width: 450, duo: true },
  { name: "phone portrait", compact: true, inlineSteer: false, width: 360, phone: true },
  { name: "iPhone Safari", compact: true, inlineSteer: false, width: 360, phone: true, safari: true },
  { name: "iPad Safari", compact: false, inlineSteer: true, safari: true },
  { name: "hosted iPhone", compact: true, inlineSteer: false, width: 360, phone: true, hosted: true },
  { name: "Duo landscape", compact: true, inlineSteer: true, width: 526, phoneQuery: LANDSCAPE_PHONE_QUERY },
  {
    name: "hosted Duo landscape",
    compact: true,
    inlineSteer: true,
    width: 526,
    phoneQuery: LANDSCAPE_PHONE_QUERY,
    hosted: true,
  },
])(
  "$name keeps the empty composer folded until content arrives",
  async ({ compact, inlineSteer, width, duo, phone, phoneQuery, hosted, safari }) => {
    const restoreViewport =
      phone || phoneQuery || duo ? installMobileViewport(duo ? DUO_COVER_QUERY : phoneQuery) : undefined;
    if (width !== undefined) installNarrowComposer(width);
    const ua =
      hosted || safari
        ? vi.spyOn(navigator, "userAgent", "get").mockReturnValue(hosted ? "EvenerNative/1" : phone ? "iPhone" : "iPad")
        : undefined;
    if (hosted) vi.stubGlobal("ReactNativeWebView", { postMessage: vi.fn() });
    try {
      await mountComposerWithHandle("ref_a", { status: { type: "active" } });
      const editor = textarea();
      const card = screen.getByTestId("composer-input-card");
      expect(card.closest("[data-iphone-composer]") !== null).toBe(!!(hosted || safari));
      const order = () =>
        [...card.querySelectorAll("button")]
          .map((button) => button.getAttribute("data-testid"))
          .filter((id) => id === "composer-stop" || id === "composer-submit" || id === "composer-steer");

      expect(editor.style.minHeight).toBe("1lh");
      expect(card.getAttribute("data-folded")).toBe("true");
      expect(card.getAttribute("data-folded-compact")).toBe(compact ? "true" : null);
      expect(screen.getByTestId("composer-attach")).toBeTruthy();
      expect(order()).toEqual(["composer-stop"]);

      act(() => editor.focus());
      expect(editor.style.minHeight).toBe("1lh");
      expect(card.getAttribute("data-folded")).toBe("true");
      expect(card.getAttribute("data-folded-compact")).toBe(compact ? "true" : null);
      expect(order()).toEqual(["composer-stop"]);

      act(() => replaceEditorText(editor, "draft"));
      expect(editor.style.minHeight).toBe(hosted || (safari && phone) ? "1lh" : "2lh");
      expect(card.hasAttribute("data-folded")).toBe(false);
      expect(card.hasAttribute("data-folded-compact")).toBe(false);
      expect(order()).toEqual(inlineSteer ? ["composer-steer", "composer-submit"] : ["composer-submit"]);

      act(() => editor.blur());
      expect(card.hasAttribute("data-folded")).toBe(false);
    } finally {
      cleanup();
      restoreViewport?.();
      if (hosted) vi.unstubAllGlobals();
      ua?.mockRestore();
    }
  },
);

const PHONE_CHROME_CASES = [
  { name: "phone portrait at rest", query: MOBILE_QUERY, state: "resting", cold: false },
  { name: "phone portrait engaged", query: MOBILE_QUERY, state: "engaged", cold: false },
  { name: "phone portrait ask-pending", query: MOBILE_QUERY, state: "ask", cold: false },
  { name: "coarse landscape at rest", query: LANDSCAPE_PHONE_QUERY, state: "resting", cold: false },
  { name: "coarse landscape engaged", query: LANDSCAPE_PHONE_QUERY, state: "engaged", cold: false },
  { name: "coarse landscape ask-pending", query: LANDSCAPE_PHONE_QUERY, state: "ask", cold: false },
  { name: "cold follow-up engaged", query: MOBILE_QUERY, state: "engaged", cold: true },
] as const;

// The phone shell keeps model, status, and effort out of the composer in every
// state; those live in the Overview sheet and Settings (design-system.md
// section 12). This table is the single owner of that assertion.
test.each(PHONE_CHROME_CASES)(
  "the phone composer mounts no model, status, or effort chrome: $name",
  async ({ query, state, cold }) => {
    onTestFinished(installMobileViewport(query));
    await mountComposer(
      "ref_a",
      cold
        ? {
            status: { type: "notLoaded" },
            evener: { ref: "ref_a", capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
          }
        : state === "ask"
          ? { ...pendingAskTurns(), evener: { ...currentWorkEvener({}), askPending: true } }
          : {},
    );

    const card = screen.getByTestId("composer-input-card");
    if (state === "resting") {
      expect(card.getAttribute("data-folded")).toBe("true");
      expect(card.getAttribute("data-folded-compact")).toBe("true");
    } else if (state === "engaged") {
      act(() => replaceEditorText(textarea(), "draft"));
      await waitFor(() => expect(card.hasAttribute("data-folded")).toBe(false));
      expect(card.hasAttribute("data-folded-compact")).toBe(false);
    } else {
      expect(card.hasAttribute("hidden")).toBe(true);
      expect(screen.queryByRole("textbox", { name: /message/i })).toBeNull();
    }

    expect(screen.queryByTestId("model-switch-trigger")).toBeNull();
    expect(screen.queryByTestId("status-row")).toBeNull();
    expect(screen.queryByTestId("status-row-effort")).toBeNull();
    expect(screen.getByTestId("session-chrome-menu")).toBeTruthy();
  },
);

test("clearing a focused draft keeps the card open until an outside click", async () => {
  await mountComposerWithHandle("ref_a", { status: { type: "active" } });
  const editor = textarea();
  const card = screen.getByTestId("composer-input-card");
  act(() => editor.focus());
  expect(card.getAttribute("data-folded")).toBe("true");
  act(() => replaceEditorText(editor, "draft"));
  expect(card.hasAttribute("data-folded")).toBe(false);

  act(() => replaceEditorText(editor, ""));
  expect(card.hasAttribute("data-folded")).toBe(false);
  expect(screen.getByTestId("composer-stop")).toBeTruthy();

  await act(async () => {
    fireEvent.pointerDown(document.body);
    editor.blur();
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    fireEvent.click(document.body);
  });
  await waitFor(() => expect(card.getAttribute("data-folded")).toBe("true"));
});

test("the composer's inline status row packs its facts to the far end", async () => {
  await mountComposerWithHandle("ref_a");
  act(() => textarea().focus());
  await waitFor(() => expect(screen.getByTestId("status-row").className.split(" ")).toContain(statusRowStyles.rowEnd));
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

test("a phone composer hides the visible current-work rows but keeps their announcement", async ({
  onTestFinished,
}) => {
  onTestFinished(installMobileViewport());
  const task = "task-4c1a";
  const goal = "goal-7e92";
  await mountComposer("ref_a", {
    evener: {
      ...currentWorkEvener({}),
      tasks: { total: 1, done: 0, current: { id: 1, description: task } },
      goal: { objective: goal, status: "active", iterations: 1 },
    },
  });

  // Nothing arrived above the card to move the phone's persistent controls.
  expect(screen.getByTestId("composer-input-card")).toBeTruthy();
  expect(screen.queryByTestId("current-work")).toBeNull();
  // The live region still carries the work, so a screen reader hears what the
  // visual rows no longer show.
  const announcements = screen
    .getAllByRole("status")
    .filter((node) => node.getAttribute("aria-atomic") === "true")
    .map((node) => node.textContent ?? "");
  expect(announcements).toHaveLength(1);
  expect(announcements[0]).toContain(task);
  expect(announcements[0]).toContain(goal);
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
// current-task button opens no sessionTasks pane. The phone shell hides the
// visible current-work rows (the test above pins that); its Tasks entry is the
// top bar's Overview sheet.
test("clicking the current task opens the activity sidebar's tasks tab, never a pane, on desktop", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    evener: currentWorkEvener({ task: true }),
  });

  await user.click(screen.getByRole("button", { name: "Open tasks: Finish the focused composer test" }));
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, ref: "ref_a", tab: "tasks" });
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

test("the composer region fills pane height and bottom-anchors the replacement slot", () => {
  const css = readModuleCss(import.meta.url, "composer.module.css");
  expect(css).toContain("flex: 1 1 auto");
  expect(css).toContain("min-height: 0");
  expect(css).toContain("justify-content: flex-end");
});

// The words beside the Send and Stop glyphs collapse by PANE width, not
// viewport width: a docked pane squeezed narrow on a desktop display needs the
// same icon-only controls the phone gets, and a viewport media query cannot see that
// (the overflowguard's 390px-pane-in-desktop-window measurement proved it).
// The 559px boundary matches the status row's own first compression threshold (statusrow.module.css).
test("the Send and Stop words collapse to glyphs below the compact pane threshold", () => {
  const css = readModuleCss(import.meta.url, "composer.module.css");
  const shared = readModuleCss(import.meta.url, "../../../styles/sendbutton.module.css");
  expect(topRuleBlock(css, ".composer")).toMatch(/container-type:\s*inline-size/);
  const sendWord = /composes:\s*sendWord from "\.\.\/\.\.\/\.\.\/styles\/sendbutton\.module\.css"/;
  expect(topRuleBlock(css, ".submitLabel")).toMatch(sendWord);
  expect(topRuleBlock(css, ".stopLabelStack")).toMatch(sendWord);
  expect(topRuleBlock(css, ".stopLabelStack")).not.toMatch(/display:/);
  expect(topRuleBlock(css, ":where(.stopLabelStack)")).toMatch(/display:\s*grid/);
  expect(css).toMatch(/\.stopLabelStack > span\s*\{[^}]*grid-area:\s*1 \/ 1/);
  expect(shared).toMatch(/@container \(max-width: 559px\)[\s\S]*?\.sendWord\s*\{[^}]*display:\s*none/);
  // The floor reaches coarse pointers as well as phone widths: a 951pt
  // landscape Duo is past the 899px phone boundary while its composer pane
  // still folds the words away, so the icon-only action keeps 44px there.
  expect(css).toMatch(
    /@media \(max-width: 899px\), \(any-pointer: coarse\) \{\s*\.composer\s+:global\(button\):has\(> \.submitLabel\),\s*\.composer\s+:global\(button\):has\(> \.stopLabelStack\)\s*\{[^}]*min-width:\s*var\(--tap-min\)/,
  );
});

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  localStorage.clear();
  resetPrefsStoreForTests();
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
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
    expect(screen.getByRole("tab", { name: "About", selected: true })).toBeTruthy();
    if (mobile && invocation === 0) {
      await waitFor(() => expect(screen.getByTestId("activity-sidebar").contains(document.activeElement)).toBe(true));
    }
  }
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "sessionDetails")).toBe(false);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
});

// The phone's activity counts live in the top bar's Overview sheet
// (design-system.md section 12), so the composer never grows an activity line,
// resting or engaged.
test("a phone composer never grows an activity line", async ({ onTestFinished }) => {
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
  await settleActivityDiscovery("ref_a");

  const form = screen.getByTestId("composer-form");
  expect(form.querySelector("[data-activity-tab]")).toBeNull();
  expect(screen.queryByTestId("composer-activity-line")).toBeNull();
  expect(screen.getByTestId("composer-submit")).toBeTruthy();

  act(() => replaceEditorText(screen.getByRole("textbox", { name: "Message" }), "draft"));
  expect(screen.getByTestId("composer-input-card").hasAttribute("data-folded")).toBe(false);
  expect(screen.queryByTestId("composer-activity-line")).toBeNull();
  expect(screen.getByTestId("composer-submit")).toBeTruthy();
  expect(form.querySelector("[data-activity-tab]")).toBeNull();
  await settleActivityDiscovery("ref_a");
});

test("a phone composer leaves inspection actions to the top bar at rest and with content", async ({
  onTestFinished,
}) => {
  onTestFinished(installMobileViewport());
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_phone_header_menu"));
  await threadsStore.getState().ensureThread("ref_phone_header_menu");
  const paneId = workspaceStore.getState().openPane("session", { ref: "ref_phone_header_menu" });
  render(
    <ClientProvider client={fake}>
      <Composer
        ref="ref_phone_header_menu"
        paneId={paneId}
        source={createTestComposerSource("ref_phone_header_menu")}
        focused={false}
      />
    </ClientProvider>,
  );
  const editor = await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();

  for (const withContent of [false, true]) {
    if (withContent) {
      act(() => replaceEditorText(editor, "draft"));
      await waitFor(() => expect(screen.getByTestId("composer-input-card").hasAttribute("data-folded")).toBe(false));
    }
    await user.click(screen.getByRole("button", { name: "Session actions" }));
    expect(screen.queryByRole("menuitem", { name: /find/i })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: /Overview/ })).toBeNull();
    expect(screen.getByRole("menuitem", { name: "Verbosity…" })).toBeTruthy();
    await user.keyboard("{Escape}");
  }
});

// The phone's activity counts live in the top bar's Overview sheet
// (design-system.md section 12), so no seat - not a cascade column's, not
// an ordinary pane's - renders a composer activity line. The sheet's
// behavior is SessionChrome.test.tsx's "one Overview sheet" test.

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

  // Real content engages the full row; focus alone no longer does.
  await act(async () => {
    editor.focus();
    replaceEditorText(editor, "draft");
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

  // The press released without destroying the draft or its expanded controls.
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  expect(screen.getByTestId("composer-submit")).toBeTruthy();
});
