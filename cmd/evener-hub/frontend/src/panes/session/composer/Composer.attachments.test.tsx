import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { appliedReceipt } from "../../../mutationReceiptTestUtils";
import { resetActivitySidebarStoreForTests } from "../../../shell/activitybar/activitySidebarStore";
import { paletteStore } from "../../../shell/palette/paletteController";
import { resetWorkspaceStoreForTests } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { resetActivityPanelStoreForTests } from "../../../stores/activityPanel";
import { useCommandCatalog } from "../../../stores/commandCatalog";
import { connectionStore } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetPrefsStoreForTests } from "../../../stores/prefs";
import { holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests } from "../../../stores/threads";
import { ALL_CAPABILITIES as FULL_CAPABILITIES } from "../../../threadTestUtils";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { toastKinds } from "../../../widgets/toast/toastTestUtils";
import { createTestComposerSource } from "../testing/composerSource";
import { editorCursor, pastePngInto, replaceEditorText, selectEditorText } from "../testing/editor";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { resetComposerFocusStoreForTests } from "./composerFocus";
import {
  Composer,
  currentWorkEvener,
  installCanvasStubs,
  installGatedDecodeStub,
  installStalledDecodeStub,
  mountComposer,
  seedRejectedRecovery,
  seedRejectedRecoveryWithAttachment,
  submitButton,
  testThread,
  textarea,
} from "./composerTestUtils";
import { readComposerDraft, readDraft, writeComposerDraft, writeRecoveryPick } from "./draft";
import { refreshPendingTurnsProjection, resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";
import { resetQuoteInsertStoreForTests } from "./quoteInsert";

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

test("mixed image and text paste preserves both the marker and caption", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  const editor = textarea();
  pastePngInto(editor, "shot.png", "caption");
  await waitFor(() => expect(editor.textContent).toBe("[image 1]caption"));
  expect(readComposerDraft("ref_a")).toEqual({ text: "[image 1]caption", skillNames: [] });
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
});

test.each([
  { kind: "image-only", image: true, caption: "", replacement: "[image 1]" },
  { kind: "mixed image and text", image: true, caption: "caption", replacement: "[image 1]caption" },
  { kind: "plain-text control", image: false, caption: "caption", replacement: "caption" },
])("review regression: $kind paste replaces selected prose and skill atom", async ({ image, caption, replacement }) => {
  installCanvasStubs();
  const user = userEvent.setup();
  const ref = "ref_selected_paste";
  const before = "Keep before ";
  const selected = "replace /cleanup and prose";
  const after = " keep after";
  writeComposerDraft(ref, { text: before + selected + after, skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();
  expect(within(editor).getByTestId("composer-skill-chip").textContent).toBe("/cleanup");
  expect(readComposerDraft(ref)).toEqual({ text: before + selected + after, skillNames: ["cleanup"] });

  selectEditorText(editor, before.length, (before + selected).length);
  expect(editor.ownerDocument.getSelection()?.toString()).toBe(selected);
  if (image) {
    pastePngInto(editor, "shot.png", caption);
    await screen.findByRole("button", { name: "View shot.png" });
  } else {
    await user.paste(caption);
  }

  expect.soft(editor.textContent).toBe(before + replacement + after);
  expect.soft(within(editor).queryAllByTestId("composer-skill-chip")).toHaveLength(0);
  expect.soft(readComposerDraft(ref)).toEqual({ text: before + replacement + after, skillNames: [] });
});

test("same-text attachment removal consumes its cursor before the next native edit", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  await mountComposer("ref_a");
  const editor = textarea();

  pastePngInto(editor);
  await screen.findByRole("button", { name: "View shot.png" });
  replaceEditorText(editor, "");
  // The tile remains after its marker was manually erased. Removing it writes
  // the same empty text and cursor zero, but still commits the tile removal.
  await user.click(screen.getByRole("button", { name: "Remove shot.png" }));
  expect(screen.queryByRole("button", { name: "Remove shot.png" })).toBeNull();
  expect(editor.textContent).toBe("");

  selectEditorText(editor, 0);
  await user.paste("abc");
  expect(editor.textContent).toBe("abc");
  expect(editorCursor(editor)).toBe("abc".length);
  // Do not correct the selection between edits: a stale restoration would put
  // this character at the start despite preserving the first insertion's text.
  await user.keyboard("x");
  expect(editor.textContent).toBe("abcx");
  expect(readComposerDraft("ref_a")).toEqual({ text: "abcx", skillNames: [] });
});

test.each([
  { remount: true, edited: true, recovery: false, drain: false },
  { remount: false, edited: true, recovery: false, drain: false },
  { remount: false, edited: false, recovery: false, drain: false },
  { remount: false, edited: true, recovery: true, drain: false },
  { remount: false, edited: false, recovery: true, drain: false },
  { remount: false, edited: true, recovery: false, drain: true },
  { remount: false, edited: false, recovery: false, drain: true },
])(
  "committing respects attachment draft ownership ($remount, $edited, $recovery, $drain)",
  async ({ remount, edited, recovery, drain }) => {
    installCanvasStubs();
    if (recovery) {
      const storage = new MutationOutboxIndexedDB();
      setMutationStorageForTests(storage);
      const recovered = await seedRejectedRecoveryWithAttachment(storage, "ref_a");
      // The user's recorded pick: mount-restore reads the pick only.
      writeRecoveryPick(recovered.clientMutationId);
    }
    const fake = await mountComposer(
      "ref_a",
      drain
        ? {
            status: { type: "active" },
            evener: {
              ref: "ref_a",
              activeTurnId: "turn_1",
              capabilities: FULL_CAPABILITIES,
              queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["queued"], preview: ["queued"] },
            },
          }
        : {},
    );
    const method = drain ? "turn/drainAsSteer" : "turn/start";
    fake.on(method, () => new Promise<never>(() => undefined));
    const actionButton = () =>
      drain ? screen.getByRole<HTMLButtonElement>("button", { name: "Steer queue now" }) : submitButton();
    const user = userEvent.setup();
    if (!recovery) pastePngInto(textarea(), "original.png");
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(actionButton().disabled).toBe(false));
    const submittedText = textarea().textContent;
    const originalAttachment = recovery ? "proof.png" : "original.png";

    const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
    const committed = hold.reached;
    try {
      await user.click(actionButton());
      // The commit's store publications re-render the queue strip; they land
      // inside this act() rather than after it.
      await act(async () => {
        await committed;
      });
      if (remount) {
        cleanup();
        render(<Composer ref="ref_a" source={createTestComposerSource("ref_a")} focused={false} />);
        replaceEditorText(textarea(), "");
        pastePngInto(textarea(), "replacement.png");
        await waitFor(() => expect(screen.getByRole("button", { name: "Remove replacement.png" })).toBeTruthy());
      } else if (edited) {
        replaceEditorText(textarea(), `${submittedText} edited`);
        replaceEditorText(textarea(), submittedText);
      }
      expect(textarea().textContent).toBe(submittedText);
    } finally {
      await act(async () => hold.release());
      await flushPendingTurnsProjectionForTests();
    }
    const remainingText = remount ? submittedText : edited && recovery ? "edit me " : "";
    expect(textarea().textContent).toBe(remainingText);
    expect(readDraft("ref_a")).toBe(remainingText);
    const retainedAttachment = remount ? "replacement.png" : originalAttachment;
    expect(screen.queryByRole("button", { name: `Remove ${retainedAttachment}` }) !== null).toBe(remount);
    // The flush after the release has seen the dispatch out.
    expect(fake.calls.filter((call) => call.method === method)).toHaveLength(1);
  },
);

test.each(["keep marker", "delete marker", "add attachment", "replace attachment", "merge recovery"])(
  "a pending send retires only its submitted attachment (%s)",
  async (edit) => {
    installCanvasStubs();
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = await mountComposer("ref_a", { evener: currentWorkEvener({ goal: true }) });
    fake.on("turn/start", () => new Promise<never>(() => undefined));
    pastePngInto(textarea(), "original.png");
    await screen.findByRole("button", { name: "View original.png" });

    const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
    const committed = hold.reached;
    try {
      await act(async () => {
        fireEvent.click(submitButton());
        await committed;
      });
      if (edit === "replace attachment") {
        fireEvent.click(screen.getByRole("button", { name: "Edit goal: Keep the session focused" }));
        fireEvent.click(screen.getByRole("button", { name: "Replace draft" }));
      }
      replaceEditorText(textarea(), edit === "keep marker" ? "follow up [image 1]" : "follow up ");
      if (edit === "add attachment" || edit === "replace attachment") {
        const name = edit === "replace attachment" ? "original.png" : "replacement.png";
        pastePngInto(textarea(), name);
        await screen.findByRole("button", { name: `View ${name}` });
      }
      if (edit === "merge recovery") {
        // A send is already pending, so this branch proves the Edit refusal
        // and unchanged recovery payload; the other variants prove retirement.
        await seedRejectedRecovery(storage, "ref_a", "merge me");
        await act(async () => {
          await refreshPendingTurnsProjection("ref_a");
        });
        const row = screen.getByText("merge me").closest("li");
        if (!row) throw new Error("missing rejected queue row");
        const editRecovery = within(row).getByRole("button", { name: "Edit message" });
        expect(editRecovery.getAttribute("aria-disabled")).toBe("true");
        fireEvent.click(editRecovery);
      }
    } finally {
      await act(async () => hold.release());
      await flushPendingTurnsProjectionForTests();
    }
    expect(screen.queryByRole("button", { name: "Remove original.png" }) !== null).toBe(edit === "replace attachment");
    expect(screen.queryByRole("button", { name: "Remove replacement.png" }) !== null).toBe(edit === "add attachment");
    expect(textarea().textContent).toContain("follow up");
    if (edit === "merge recovery") {
      const [recovery] = await storage.listRecovery("ref_a");
      expect(recovery?.payload.input).toEqual([{ type: "text", text: "merge me" }]);
    } else {
      expect(readDraft("ref_a")).toBe(textarea().textContent);
    }
    fireEvent.click(submitButton());
    await flushPendingTurnsProjectionForTests();
    const records = await storage.listOutbox("ref_a");
    expect(records).toHaveLength(2);
    expect(records[1]?.attachments.map((item) => item.name)).toEqual(
      edit === "replace attachment" ? ["original.png"] : edit === "add attachment" ? ["replacement.png"] : [],
    );
  },
);

test("submitted markers around retained command and skill atoms preserve their exact identities", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const ref = "ref_retained_selections";
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  writeComposerDraft(ref, {
    text: "😀 /same /same /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 3 },
      { kind: "skill", name: "same", offset: 9 },
    ],
  });
  const fake = await mountComposer(ref, {
    evener: {
      ...testThread(ref).evener,
      capabilities: { ...FULL_CAPABILITIES, commandInput: true, skillInput: true },
    },
  });
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  const editor = textarea();
  selectEditorText(editor, 0);
  pastePngInto(editor, "before.png");
  await screen.findByRole("button", { name: "View before.png" });
  selectEditorText(editor, editor.textContent?.length ?? 0);
  pastePngInto(editor, "after.png");
  await screen.findByRole("button", { name: "View after.png" });
  expect(editor.textContent).toBe("[image 1]😀 /same /same /same[image 2]");
  expect(within(editor).getAllByTestId("composer-command-chip")).toHaveLength(1);
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);

  const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
  try {
    await act(async () => {
      fireEvent.click(submitButton());
      await hold.reached;
    });
    selectEditorText(editor, editor.textContent?.length ?? 0);
    await user.paste(" later");
  } finally {
    await act(async () => hold.release());
    await flushPendingTurnsProjectionForTests();
  }

  expect.soft(editor.textContent).toBe("😀 /same /same /same later");
  expect.soft(within(editor).queryAllByTestId("composer-command-chip")).toHaveLength(1);
  expect.soft(within(editor).queryAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect.soft(readComposerDraft(ref)).toEqual({
    text: "😀 /same /same /same later",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 3 },
      { kind: "skill", name: "same", offset: 9 },
    ],
  });
  expect(screen.queryByRole("button", { name: "Remove before.png" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Remove after.png" })).toBeNull();

  fireEvent.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  const records = await storage.listOutbox(ref);
  expect(records).toHaveLength(2);
  expect(records[0]?.attachments.map((item) => item.name)).toEqual(["before.png", "after.png"]);
  expect(records[1]?.attachments).toEqual([]);
  expect(records[1]?.payload.input).toEqual([
    {
      type: "text",
      text: "😀 /same /same /same later",
      mentions: [
        { kind: "command", name: "same", offset: 3 },
        { kind: "skill", name: "same", offset: 9 },
      ],
    },
    { type: "skill", name: "same" },
    { type: "command", name: "same" },
  ]);
});

test("the remove button names the specific attachment it removes", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  const editor = textarea();
  pastePngInto(editor, "shot.png");
  await waitFor(() => expect(editor.textContent).toBe("[image 1]"));

  expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy();
});

test("a successful submit includes the pasted image as a base64 InputAttachment", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  const editor = textarea();
  pastePngInto(editor);
  await waitFor(() => expect(editor.textContent).toBe("[image 1]"));
  await waitFor(() => expect(screen.queryByRole("button", { name: /remove/i })).toBeTruthy());

  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);
  const call = fake.calls.find((c) => c.method === "turn/start");
  const params = call?.params as { input: Array<{ type: string; mediaType?: string; data?: string }> };
  const imageEntry = params.input.find((i) => i.type === "image");
  expect(imageEntry?.mediaType).toBe("image/png");
  expect(typeof imageEntry?.data).toBe("string");
});

test("submitting while an attachment is still mid-encode is blocked with a toast, no request fires", async () => {
  installStalledDecodeStub();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  const editor = textarea();
  pastePngInto(editor);
  await waitFor(() => expect(editor.textContent).toBe("[image 1]"));
  await user.click(submitButton());

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
});

test("pasted image renders as a thumbnail tile with dimensions, remove button, and lightbox on click", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  await mountComposer("ref_a");

  const editor = textarea();
  pastePngInto(editor, "screenshot.png");
  await waitFor(() => expect(editor.textContent).toBe("[image 1]"));

  // The whole thumbnail is the control that opens the lightbox, and it is
  // named for the file it shows.
  const openButton = await screen.findByRole("button", { name: "View screenshot.png" });
  const thumbnail = openButton.querySelector("img") as HTMLImageElement;
  expect(thumbnail.src).toMatch(/^data:image\/png;base64,/);

  // Assert the dimensions are displayed
  expect(screen.getByText(/4×4/)).toBeTruthy();

  // Assert clicking the thumbnail opens the lightbox (before removing)
  await user.click(openButton);
  const lightboxImg = screen.getByRole("img", { name: "screenshot.png" }) as HTMLImageElement;
  expect(lightboxImg.src).toMatch(/^data:image\/png;base64,/);

  // Close the lightbox by clicking outside (Esc or backdrop)
  const dialogBackdrop = lightboxImg.parentElement?.parentElement;
  if (dialogBackdrop) fireEvent.click(dialogBackdrop);

  // Assert clicking the ✕ removes the attachment
  const removeButton = screen.getByRole("button", { name: /remove screenshot\.png/i });
  await user.click(removeButton);
  await waitFor(() => expect(editor.textContent).toBe(""));
});

// kata edhz. The settled tile used to draw its image twice - <ImageGallery>,
// whose own 96px thumbnail button came along with the lightbox it was
// imported for, PLUS a plain 80px <img> for the tile's own cover crop - as
// flex siblings in one 80x80 overflow:hidden box. Measured in a real headless
// Chrome against the real CSS: the gallery thumb rendered at y=-20..78 and
// the plain img at y=78..156 inside a tile spanning 24..104, so the two
// clipped crops met in a visible seam at y=78. Neither was vestigial; they
// were there for different reasons, and the tile now draws its own image and
// opens the shared Dialog itself.
//
// jsdom cannot see the seam (it computes no boxes at all), but it can see the
// cause: two image elements where the design has one. The geometric half is
// scripts/layoutguard/cases/edhz-attachment-tile-single-image.
test("a settled attachment tile draws exactly one image, not a stack of them (kata edhz)", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  pastePngInto(textarea(), "screenshot.png");
  await waitFor(() => expect(textarea().textContent).toBe("[image 1]"));
  const openButton = await screen.findByRole("button", { name: "View screenshot.png" });

  const tile = openButton.parentElement as HTMLElement;
  expect(tile.querySelectorAll("img")).toHaveLength(1);
});

// kata 39xe. A pending attachment and a settled one are the SAME element
// tree at the same list position, differing only in what fills the tile -
// so React updates the remove button rather than unmounting it, and a user
// who has tab-focus on that button when the decode lands keeps it.
//
// This asserts the mechanism (the identical node is still focused), not a
// side effect: an implementation that remounted an identically-labelled
// button would satisfy "a focused remove button exists" while still
// dropping the user's focus. Before the tile was unified, pending rendered
// a <Chip> and settled a <div>, React remounted across that type boundary,
// and this test failed on both of its last two assertions - activeElement
// was <body> and the captured node reported isConnected === false.
test("focus on an attachment's remove button survives its decode settling (kata 39xe)", async () => {
  const gate = installGatedDecodeStub();
  await mountComposer("ref_a");

  act(() => {
    pastePngInto(textarea(), "shot.png");
  });
  const removeButton = screen.getByRole("button", { name: "Remove shot.png" });
  act(() => removeButton.focus());
  expect(document.activeElement).toBe(removeButton);

  await act(async () => {
    await gate.release();
  });

  // The transition really happened: the tile now offers the decoded image,
  // so the assertions below are about the settled state, not a decode that
  // quietly never landed.
  expect(screen.getByRole("button", { name: "View shot.png" })).toBeTruthy();
  expect(removeButton.isConnected).toBe(true);
  expect(document.activeElement).toBe(removeButton);
});

// installFailingDecodeStub mirrors installCanvasStubs but rejects (via
// Image.onerror) on a microtask, instead of resolving - so a test gets a
// window between the synchronous marker-insertion and the decode's
// eventual rejection in which to make further synchronous changes (typing)
// before that rejection settles.
function installFailingDecodeStub(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  class FailingImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onerror?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FailingImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
}

// CRITICAL regression test (reviewer-reproduced): useAttachments' decode-
// failure path calls the composer's TextEditor.read()/write() long after
// the render that registered it. If read() ever mixes a live cursor with a
// stale per-render `text` closure, write()'s call to the (always-stable)
// setText silently reverts the ENTIRE composer to that stale value -
// discarding both the marker insertion and anything typed since, and
// desyncing the draft (a revert that bypasses writeDraft). Reproduction:
// paste an image whose decode later fails, then type SYNCHRONOUSLY (no
// yield to the microtask queue) before that rejection settles.
test("a failed decode strips its marker while a selection is held, caret at that selection's start", async () => {
  installFailingDecodeStub();
  await mountComposer("ref_a");
  const editor = textarea();

  act(() => {
    pastePngInto(editor);
  });
  replaceEditorText(editor, "keep [image 1] tail");
  expect(editor.textContent).toBe("keep [image 1] tail");

  // Hold a real forward selection while the decode fails. The field this
  // replaced reported the selection's lower offset, so the caret after the
  // strip belongs at the selection's start, not at its focus end.
  selectEditorText(editor, 0, 4);

  await waitFor(() => expect(screen.queryByRole("button", { name: /remove/i })).toBeNull());

  expect(editor.textContent).toBe("keep  tail");
  expect(editorCursor(editor)).toBe(0);
  expect(readComposerDraft("ref_a")).toEqual({ text: "keep  tail", skillNames: [] });
});

test("typing synchronously after a paste whose decode later fails survives - the failed marker alone is stripped (critical)", async () => {
  installFailingDecodeStub();
  await mountComposer("ref_a");

  // act() forces React to flush the paste's resulting state update
  // synchronously (pastePngInto's plain el.dispatchEvent isn't
  // auto-wrapped the way fireEvent/user-event are) - still entirely
  // synchronous JS, so this runs before the decode's microtask-deferred
  // rejection has any chance to fire.
  act(() => {
    pastePngInto(textarea());
  });
  expect(textarea().textContent).toBe("[image 1]");

  // Also synchronous (fireEvent, not user.type - no per-keystroke delay
  // that could yield to the microtask queue): types "hello" at the
  // (cursor-restored) end of the marker, landing entirely before the
  // decode's rejection settles.
  replaceEditorText(textarea(), "[image 1]hello");
  expect(textarea().textContent).toBe("[image 1]hello");
  expect(readComposerDraft("ref_a")).toEqual({ text: "[image 1]hello", skillNames: [] });

  // Now let the decode's rejection actually settle.
  await waitFor(() => expect(screen.queryByRole("button", { name: /remove/i })).toBeNull());

  expect(textarea().textContent).toBe("hello"); // typed text survives; only the failed marker is gone
  expect(readComposerDraft("ref_a")).toEqual({ text: "hello", skillNames: [] }); // draft matches, not stale
});

// Minor (reviewer-requested): two attachment gestures fired back-to-back
// with NO intervening render (both dispatched inside one `act()` block, so
// React has no chance to commit the first gesture's setText before the
// second gesture's own TextEditor.read() runs) must still chain correctly
// rather than the second clobbering the first.
test("two attachment gestures fired back-to-back with no intervening render still chain their markers, not clobber", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  act(() => {
    pastePngInto(textarea(), "a.png");
    pastePngInto(textarea(), "b.png");
  });

  expect(textarea().textContent).toBe("[image 1][image 2]");
  await waitFor(() => expect(screen.getAllByRole("button", { name: /remove/i })).toHaveLength(2));
});

// Removing an attachment while its decode is STILL IN FLIGHT, which the
// stalled stub pins for the whole gesture: the marker has to come out of the
// textarea even though there is no decoded image behind it yet, and
// useAttachments has to remember the removal so the decode that eventually
// lands doesn't resurrect it (kata kt4j). Removing a SETTLED attachment is
// the thumbnail/lightbox test above.
test("removing a still-encoding attachment strips its marker from the textarea", async () => {
  installStalledDecodeStub();
  const user = userEvent.setup();
  await mountComposer("ref_a");

  const editor = textarea();
  pastePngInto(editor);
  await waitFor(() => expect(editor.textContent).toBe("[image 1]"));

  await user.click(screen.getByRole("button", { name: /remove/i }));
  expect(editor.textContent).toBe("");
});

test("picking a file via the hidden input attaches it, same as paste/drop", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  // The picker input is `hidden` (only ever reached via the visible Attach
  // button proxying a click to it - see the next test) - user-event's own
  // upload() expects a pointer-interactable target, so this uses RTL's
  // fireEvent.change with a target.files override instead, the documented
  // idiom for a file input's change event.
  const file = new File([new Uint8Array([1, 2, 3])], "picked.png", { type: "image/png" });
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  fireEvent.change(input, { target: { files: [file] } });

  await waitFor(() => expect(textarea().textContent).toBe("[image 1]"));
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
});

test("clicking the attach button triggers the hidden file input", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");

  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  const clickSpy = vi.spyOn(input, "click");

  await user.click(screen.getByTestId("composer-attach"));
  expect(clickSpy).toHaveBeenCalledTimes(1);
});
