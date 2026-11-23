import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { appliedReceipt } from "../../../mutationReceiptTestUtils";
import { resetActivitySidebarStoreForTests } from "../../../shell/activitybar/activitySidebarStore";
import { paletteStore } from "../../../shell/palette/paletteController";
import { resetWorkspaceStoreForTests } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { useCommandCatalog } from "../../../stores/commandCatalog";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetPrefsStoreForTests } from "../../../stores/prefs";
import { holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests } from "../../../stores/threads";
import { ALL_CAPABILITIES as FULL_CAPABILITIES } from "../../../threadTestUtils";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { toastKinds } from "../../../widgets/toast/toastTestUtils";
import { createTestComposerSource } from "../testing/composerSource";
import { editorCursor, insertEditorTextAt, pastePngInto, replaceEditorText, selectEditorText } from "../testing/editor";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { resetComposerFocusStoreForTests } from "./composerFocus";
import {
  Composer,
  currentWorkEvener,
  installCanvasStubs,
  installGatedDecodeStub,
  installStalledDecodeStub,
  mountComposer,
  mountComposerWithHandle,
  pendingAskTurns,
  seedRejectedRecovery,
  seedRejectedRecoveryWithAttachment,
  steerButton,
  submitButton,
  testThread,
  textarea,
} from "./composerTestUtils";
import { readComposerDraft, readComposerDraftSnapshot, readDraft, writeComposerDraft } from "./draft";
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
      await seedRejectedRecoveryWithAttachment(storage, "ref_a");
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
    // The drain route's entry point is the composer's own Steer control
    // (composer-steer): with queue depth it routes to drain
    // (decideSteerRoute), the same single-destination rule the retired
    // strip's header button used to carry.
    const actionButton = () => (drain ? (screen.getByTestId("composer-steer") as HTMLButtonElement) : submitButton());
    const user = userEvent.setup();
    if (!recovery) pastePngInto(textarea(), "original.png");
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(actionButton().disabled).toBe(false));
    const submittedText = textarea().textContent;
    const originalAttachment = recovery ? "proof.png" : "original.png";
    // Remounting re-adopts the submitted marker: the text still spells
    // "[image 1]" but its bytes went with the request. That number stays
    // reserved while an undo could bring the occurrence back, so the
    // replacement image takes the next free one instead of pairing its bytes
    // with the adopted literal at 1. Every other branch is the same text.
    const expectedAfterReplacement = remount ? "[image 2]" : submittedText;

    const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
    const committed = hold.reached;
    try {
      await user.click(actionButton());
      // The commit's store publications re-render the pending stack; they land
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
        insertEditorTextAt(textarea(), submittedText.length, " edited");
        insertEditorTextAt(textarea(), submittedText.length, "", submittedText.length + " edited".length);
      }
      expect(textarea().textContent).toBe(expectedAfterReplacement);
    } finally {
      await act(async () => hold.release());
      await flushPendingTurnsProjectionForTests();
    }
    const remainingText = remount ? expectedAfterReplacement : edited && recovery ? "edit me " : "";
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
  // The trigger owns the tile's rounding: on touch the tile stops clipping so
  // the remove control can straddle its corner, so the button rounds and
  // clips itself through the widget API, not a tag selector in the tile CSS.
  expect(openButton.getAttribute("data-radius")).toBe("inherit");

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
  const user = userEvent.setup();
  installFailingDecodeStub();
  await mountComposer("ref_a");
  const editor = textarea();
  await user.click(editor);

  act(() => {
    pastePngInto(editor);
    insertEditorTextAt(editor, 0, "keep ");
    insertEditorTextAt(editor, "keep [image 1]".length, " tail");
  });
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
  insertEditorTextAt(textarea(), "[image 1]".length, "hello");
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
  act(() => textarea().focus());

  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  const clickSpy = vi.spyOn(input, "click");

  await user.click(screen.getByTestId("composer-attach"));
  expect(clickSpy).toHaveBeenCalledTimes(1);
});

// Android Chrome's picker for an image-only input browses existing pictures
// and has no camera; the attach control is the only way to one there.
function installAndroidBrowser(): void {
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue(
    "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Mobile Safari/537.36",
  );
}

// Opens the attach control's menu and presses each choice in turn, recording
// what the hidden input was asked for at the moment it opened.
async function captureRequestedByEachChoice(): Promise<(string | null)[]> {
  const user = userEvent.setup();
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  const captures: (string | null)[] = [];
  vi.spyOn(input, "click").mockImplementation(() => {
    captures.push(input.getAttribute("capture"));
  });
  const trigger = within(screen.getByTestId("composer-attach")).getByRole("button");
  await user.click(trigger);
  const count = screen.getAllByRole("menuitem").length;
  for (let index = 0; index < count; index += 1) {
    if (index > 0) await user.click(trigger);
    await user.click(screen.getAllByRole("menuitem")[index] as HTMLElement);
  }
  return captures;
}

test("where the browser's picker already has a camera, attach opens it directly with no menu", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");
  act(() => textarea().focus());
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  const clickSpy = vi.spyOn(input, "click").mockImplementation(() => {});

  await user.click(screen.getByTestId("composer-attach"));
  expect(screen.queryByRole("menu")).toBeNull();
  expect(clickSpy).toHaveBeenCalledTimes(1);
  expect(input.hasAttribute("capture")).toBe(false);
});

test("on Android, attach offers the picker and the camera, and only the camera choice asks for it", async () => {
  installAndroidBrowser();
  await mountComposer("ref_a");
  act(() => textarea().focus());

  const captures = await captureRequestedByEachChoice();
  expect(captures).toHaveLength(2);
  expect(captures.filter((capture) => capture !== null)).toHaveLength(1);
});

// The two choices share one input, so the camera request must not outlive the
// open that made it: choosing images after taking a photo has to reach the
// picker again. Running the choices camera-last then picker-again covers the
// order the first pass does not.
test("on Android, a camera request does not stick to the next picker open", async () => {
  installAndroidBrowser();
  await mountComposer("ref_a");
  act(() => textarea().focus());

  const first = await captureRequestedByEachChoice();
  const second = await captureRequestedByEachChoice();
  expect(second).toEqual(first);
});

test("a photo taken on Android is staged like any picked image", async () => {
  installAndroidBrowser();
  installCanvasStubs();
  await mountComposer("ref_a");
  act(() => textarea().focus());

  const captures = await captureRequestedByEachChoice();
  expect(captures.some((capture) => capture !== null)).toBe(true);
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  const photo = new File([new Uint8Array([1, 2, 3])], "image.jpg", { type: "image/jpeg" });
  fireEvent.change(input, { target: { files: [photo] } });

  await waitFor(() => expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy());
});

// Driven through the real Composer, the real draft store and the real
// attachment pipeline: one reader stages an image and its draft lands under
// the shared ref, then the OTHER reader - whose source existed before that
// write, so it no longer owns the ref's draft revision - is mounted and
// adopts the text with an empty attachment list. Bytes never enter
// localStorage, so the adopted anchor has nothing behind it and
// buildComposerInput would ship it as literal text with no image item: the
// model is told about an image it cannot see.
test("an adopted draft with a marker and no bytes refuses to send and keeps the draft", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const ref = "ref_a";
  // The second reader's source is created first, exactly as a session reader
  // mounted in step 1 predates the cascade reader's typing in step 3.
  const adoptingSource = createTestComposerSource(ref);

  const first = await mountComposerWithHandle(ref);
  const firstEditor = textarea();
  await user.type(firstEditor, "Cascade reader draft");
  pastePngInto(firstEditor, "cascade.png");
  await screen.findByRole("button", { name: "View cascade.png" });
  expect(readComposerDraft(ref)).toEqual({ text: "Cascade reader draft[image 1]", skillNames: [] });
  first.unmount();

  // The remount: cached text arrives, the attachment list does not.
  const second = await mountComposerWithHandle(ref, {}, { source: adoptingSource });
  expect(textarea().textContent).toBe("Cascade reader draft[image 1]");
  // No tile: the bytes stayed with the reader that staged them.
  expect(screen.queryByRole("button", { name: "Remove cascade.png" })).toBeNull();
  const delivered: unknown[] = [];
  second.fake.on("turn/start", (params) => {
    delivered.push(params.input);
    return {
      receipt: appliedReceipt(params.clientMutationId, "thread_a"),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  second.fake.on("turn/queue", (params) => {
    delivered.push(params.input);
    return { receipt: appliedReceipt(params.clientMutationId, "thread_a") };
  });

  // The form's own submit reaches the request boundary without any button
  // gating, so this pins the refusal rather than a disabled control.
  fireEvent.submit(screen.getByTestId("composer-form"));
  await flushPendingTurnsProjectionForTests();
  expect(delivered).toEqual([]);
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
  expect(submitButton().disabled).toBe(true);
  expect(textarea().textContent).toBe("Cascade reader draft[image 1]");
  expect(readComposerDraft(ref)).toEqual({ text: "Cascade reader draft[image 1]", skillNames: [] });

  // The notice's own action is the repair the refusal asks for: one ordinary
  // edit that drops the reference, after which the draft sends unchanged.
  await user.click(screen.getByRole("button", { name: "Remove the reference" }));
  expect(textarea().textContent).toBe("Cascade reader draft");
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  await waitFor(() => expect(submitButton().disabled).toBe(false));
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(second.fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
});

// The working path the refusal must not block: a composer that staged the
// image itself holds the bytes, so it sends the marker AND the image item.
test("a composer holding the image bytes sends the marker with its image item", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  pastePngInto(textarea(), "shot.png");
  await screen.findByRole("button", { name: "View shot.png" });
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();

  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  const call = fake.calls.find((candidate) => candidate.method === "turn/start");
  if (call === undefined) throw new Error("expected the staged image to send");
  const input = (call.params as { input: Array<{ type: string; text?: string; data?: string; name?: string }> }).input;
  expect(input.filter((item) => item.type === "image")).toHaveLength(1);
  expect(input.find((item) => item.type === "image")?.data).toBeTruthy();
  const textItem = input.find((item) => item.type === "text");
  expect(textItem?.text?.includes("[image 1]")).toBe(false);
  expect(textItem?.text).toContain("shot.png");
});

// The false positive this fix must not ship: a marker the person typed is
// prose this composer wrote, so it is never an adopted anchor and still sends
// as the literal text it is.
test("a marker the person typed with no attachment still sends as literal text", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  const editor = textarea();
  // "[[": userEvent reads a single "[" as the start of a key descriptor, so
  // the escaped input types the same bracket the draft keeps.
  await user.type(editor, "look at [[image 1]");
  expect(editor.textContent).toBe("look at [image 1]");
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();

  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  const calls = fake.calls.filter((candidate) => candidate.method === "turn/start");
  expect(calls).toHaveLength(1);
  const call = calls[0];
  if (call === undefined) throw new Error("expected the typed marker to send");
  const input = (call.params as { input: Array<{ type: string; text?: string }> }).input;
  expect(input).toEqual([{ type: "text", text: "look at [image 1]" }]);
});

// Removing two missing references around a selected COMMAND must not turn
// that command into plain text. A command has no skill of the same name to
// re-derive from, so its chip exists only while the draft's own mention
// carries it - which is exactly what one write covering both deletions
// destroys: patchSelectionText reads the write as a single replacement span
// and drops every atom inside it.
test("removing two missing references preserves a selected command between them", async () => {
  const user = userEvent.setup();
  const ref = "ref_unbacked_around_command";
  // Prose on both sides, so the two removals are real splices inside a
  // sentence rather than the whole document. The command sits between them.
  const text = "before [image 1] /inspect [image 2] after";
  writeComposerDraft(ref, {
    text,
    skillNames: [],
    commandNames: ["inspect"],
    mentions: [{ kind: "command", name: "inspect", offset: text.indexOf("/inspect") }],
    stagedImageMarkers: [1, 2],
  });
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, commandInput: true },
      queue: { revision: 0 },
    },
  });
  expect(within(textarea()).getAllByTestId("composer-command-chip")).toHaveLength(1);
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Remove the reference" }));

  // Both anchors are gone, the prose around them survives, and the command is
  // still an atom in its new place. React can batch the two splices into one
  // commit, which the editor reads as a single replacement span - that is the
  // shape that used to turn the selected command back into plain text, so the
  // chip's survival here is the assertion that the repair restores atomically.
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  expect(within(textarea()).getAllByTestId("composer-command-chip")).toHaveLength(1);
  const draft = readComposerDraft(ref);
  expect(draft.text).toBe("before  /inspect  after");
  expect(draft.commandNames).toEqual(["inspect"]);
  expect(draft.mentions).toEqual([{ kind: "command", name: "inspect", offset: "before  ".length }]);
});

// Two spellings of ONE number in one draft: the person typed a literal
// "[image 1]", and a staging gesture later appended an adopted occurrence of
// the same number. The record names the adopted occurrence by its offsets, so
// the repair removes THAT one and leaves the literal the person typed - which
// is prose, and sendable.
test("removing a missing reference leaves an identical literal the person typed", async () => {
  const user = userEvent.setup();
  const ref = "ref_unbacked_duplicate_literal";
  const text = "[image 1] tail [image 1]";
  const adoptedStart = text.indexOf("[image 1]", 1);
  writeComposerDraft(ref, {
    text,
    skillNames: [],
    stagedImageMarkers: [1],
    stagedImageRanges: [{ marker: 1, start: adoptedStart, end: adoptedStart + "[image 1]".length }],
  });
  const fake = await mountComposer(ref);
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
  await waitFor(() => expect(submitButton().disabled).toBe(true));

  await user.click(screen.getByRole("button", { name: "Remove the reference" }));

  // The literal keeps its exact place; only the adopted occurrence is gone.
  await waitFor(() => expect(textarea().textContent).toBe("[image 1] tail "));
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  await waitFor(() => expect(submitButton().disabled).toBe(false));

  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  const call = fake.calls.find((candidate) => candidate.method === "turn/start");
  if (call === undefined) throw new Error("expected the repaired draft to send");
  expect(call.params).toMatchObject({ input: [{ type: "text", text: "[image 1] tail " }] });
});

// The same duplicate survives a reload. The persisted record carries offsets,
// so the reader marks the adopted occurrence and NOT the identical literal -
// and one repair is enough to send.
test("a reloaded duplicate marks only the adopted occurrence and repairs in one press", async () => {
  const user = userEvent.setup();
  const ref = "ref_unbacked_duplicate_reload";
  const text = "[image 1] tail [image 1]";
  const adoptedStart = text.indexOf("[image 1]", 1);
  writeComposerDraft(ref, {
    text,
    skillNames: [],
    stagedImageMarkers: [1],
    stagedImageRanges: [{ marker: 1, start: adoptedStart, end: adoptedStart + "[image 1]".length }],
  });

  // The first mount is the writer: what it persists is what the second reads.
  const first = await mountComposerWithHandle(ref);
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
  first.unmount();
  // The raw snapshot, not readComposerDraft: the provenance fields are exactly
  // what a reloading reader needs, and readComposerDraft strips them so a
  // rendering caller sees only what the person typed.
  expect(readComposerDraftSnapshot(ref)).toMatchObject({
    text,
    stagedImageMarkers: [1],
    stagedImageRanges: [{ marker: 1, start: adoptedStart, end: adoptedStart + "[image 1]".length }],
  });

  const second = await mountComposerWithHandle(ref);
  second.fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  expect(textarea().textContent).toBe(text);
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Remove the reference" }));

  await waitFor(() => expect(textarea().textContent).toBe("[image 1] tail "));
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  await waitFor(() => expect(submitButton().disabled).toBe(false));
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(second.fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
});

// Deleting the missing reference clears the refusal while it is absent, and a
// REAL undo must bring the refusal back with it. This is the whole reason the
// provenance is a document mark rather than a number list beside the text:
// the editor's own history restores the mark and the occurrence together, so
// there is no record a delete could retire and an undo could fail to restore.
test("a real undo after deleting the only missing reference restores the refusal", async () => {
  const user = userEvent.setup();
  const ref = "ref_unbacked_undo";
  writeComposerDraft(ref, { text: "kept [image 1]", skillNames: [], stagedImageMarkers: [1] });
  const fake = await mountComposer(ref);
  const delivered: unknown[] = [];
  fake.on("turn/start", (params) => {
    delivered.push(params.input);
    return {
      receipt: appliedReceipt(params.clientMutationId, "thread_a"),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();

  // Delete the ONLY occurrence, so the draft is blank. The adoption record must
  // survive that: the undo below brings the occurrence back with its mark.
  selectEditorText(textarea(), "kept ".length, "kept [image 1]".length);
  await user.keyboard("{Backspace}");
  await waitFor(() => expect(textarea().textContent).toBe("kept "));
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  await waitFor(() => expect(submitButton().disabled).toBe(false));

  // The editor's own history, through its own keybinding.
  await user.keyboard("{Control>}z{/Control}");
  await waitFor(() => expect(textarea().textContent).toBe("kept [image 1]"));
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
  await waitFor(() => expect(submitButton().disabled).toBe(true));

  fireEvent.submit(screen.getByTestId("composer-form"));
  await flushPendingTurnsProjectionForTests();
  expect(delivered).toEqual([]);
});

// While an agent question is pending the composer's own surface is hidden and
// inert, so the repair beside it must not be reachable - it edits a draft the
// person cannot see. It is gated on the same askPending the editor is, so
// resolving the question brings it back (the ungated tests above cover that
// half).
test("the missing-reference repair is unreachable while an agent question is pending", async () => {
  const ref = "ref_unbacked_ask";
  writeComposerDraft(ref, { text: "kept [image 1]", skillNames: [], stagedImageMarkers: [1] });
  const fake = await mountComposer(ref, {
    status: { type: "active" },
    evener: {
      ref,
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0 },
      activeTurnId: "turn_1",
      askPending: true,
    },
    ...pendingAskTurns(),
  } as never);

  // The gated editor is not on screen at all, and neither is the repair.
  expect(screen.queryByRole("textbox", { name: "Message" })).toBeNull();
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  expect(screen.queryByRole("button", { name: "Remove the reference" })).toBeNull();
  expect(readComposerDraft(ref)).toEqual({ text: "kept [image 1]", skillNames: [] });
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// An adopted missing image leaves Steer with nothing it can do, so both Steer
// surfaces must say so instead of accepting a press that silently returns.
// The press-time guard stays for the keyboard chord.
test("steer states the missing-reference refusal rather than silently returning", async () => {
  const user = userEvent.setup();
  const ref = "ref_unbacked_steer";
  writeComposerDraft(ref, { text: "kept [image 1]", skillNames: [], stagedImageMarkers: [1] });
  const fake = await mountComposer(ref, {
    status: { type: "active" },
    evener: {
      ref,
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });

  const steer = steerButton();
  expect(steer.getAttribute("aria-disabled")).toBe("true");
  await user.click(steer);
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/steer")).toEqual([]);
  // The chord still reaches the press-time guard, and names its reason.
  fireEvent.keyDown(textarea(), { key: "Enter", shiftKey: true });
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/steer")).toEqual([]);
});

// Provenance is a mark on the occurrence itself, so a RETYPED occurrence is
// not the adopted one: it is a plain text node with no mark, and it sends as
// the prose it is. With prose remaining beside it, the person gets their own
// text back out.
test("retyping the missing reference after deleting it sends as literal prose", async () => {
  const user = userEvent.setup();
  const ref = "ref_unbacked_retyped";
  writeComposerDraft(ref, { text: "kept [image 1]", skillNames: [], stagedImageMarkers: [1] });
  const fake = await mountComposer(ref);
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();

  // Delete the occurrence, leaving the prose around it.
  selectEditorText(textarea(), "kept ".length, "kept [image 1]".length);
  await user.keyboard("{Backspace}");
  await waitFor(() => expect(textarea().textContent).toBe("kept "));

  // Type the same literal again, by hand. It is NOT the adopted occurrence.
  await user.type(textarea(), "[[image 1]");
  await waitFor(() => expect(textarea().textContent).toBe("kept [image 1]"));
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  await waitFor(() => expect(submitButton().disabled).toBe(false));

  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  const calls = fake.calls.filter((candidate) => candidate.method === "turn/start");
  expect(calls).toHaveLength(1);
  const call = calls[0];
  if (call === undefined) throw new Error("expected the retyped literal to send");
  const input = (call.params as { input: Array<{ type: string; text?: string }> }).input;
  // The wire carries the literal exactly as typed, with no image item.
  expect(input).toEqual([{ type: "text", text: "kept [image 1]" }]);
});

// The replacement image takes a fresh number even when the deleted occurrence
// was the whole draft, and the undo that follows still refuses the restored
// occurrence. Both halves have to hold at once: a reservation retired with the
// text would let the replacement take number 1, which the undo brings back.
test("attaching after deleting the only occurrence takes a fresh marker and the undo still refuses", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const ref = "ref_unbacked_replacement";
  writeComposerDraft(ref, { text: "[image 1]", skillNames: [], stagedImageMarkers: [1] });
  const fake = await mountComposer(ref);
  const delivered: unknown[] = [];
  fake.on("turn/start", (params) => {
    delivered.push(params.input);
    return {
      receipt: appliedReceipt(params.clientMutationId, "thread_a"),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();

  // Delete the only occurrence, making the draft blank.
  replaceEditorText(textarea(), "");
  await waitFor(() => expect(textarea().textContent).toBe(""));
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();

  // Attach a replacement: it must NOT reuse number 1, which the undo below
  // brings back.
  pastePngInto(textarea(), "replacement.png");
  await screen.findByRole("button", { name: "View replacement.png" });
  expect(textarea().textContent).toBe("[image 2]");
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  await waitFor(() => expect(submitButton().disabled).toBe(false));

  // Undo the deletion. The restored occurrence comes back WITH its mark, so
  // the refusal returns beside the replacement tile's own marker.
  await user.keyboard("{Control>}z{/Control}");
  await waitFor(() => expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy());
  await waitFor(() => expect(submitButton().disabled).toBe(true));
  expect(screen.getByRole("button", { name: "View replacement.png" })).toBeTruthy();

  fireEvent.submit(screen.getByTestId("composer-form"));
  await flushPendingTurnsProjectionForTests();
  expect(delivered).toEqual([]);
});

// Reopening a draft whose only reference is missing leaves an adopted
// "[image 1]" the composer cannot back. Attaching a new image with the caret
// immediately BEFORE that literal must not disturb it: the two markers share
// "[image ", so a splice INFERRED from the before/after pair reads as a
// replacement through the untouched one and drops its provenance. Losing the
// provenance is silent and expensive - Send becomes available and ships
// "[image 1]" as prose with no image behind it.
test("attaching immediately before a missing reference keeps that reference adopted and Send blocked", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const ref = "ref_unbacked_before_insert";
  writeComposerDraft(ref, {
    text: "[image 1]",
    skillNames: [],
    stagedImageMarkers: [1],
    stagedImageRanges: [{ marker: 1, start: 0, end: "[image 1]".length }],
  });
  const fake = await mountComposer(ref);
  const delivered: unknown[] = [];
  fake.on("turn/start", (params) => {
    delivered.push(params.input);
    return {
      receipt: appliedReceipt(params.clientMutationId, "thread_a"),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
  await waitFor(() => expect(submitButton().disabled).toBe(true));

  // Caret at offset 0, immediately before the adopted literal.
  selectEditorText(textarea(), 0);
  pastePngInto(textarea(), "fresh.png");
  await screen.findByRole("button", { name: "View fresh.png" });

  expect(textarea().textContent).toBe("[image 2][image 1]");
  // The untouched literal still refuses, and the new tile stays attached.
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
  await waitFor(() => expect(submitButton().disabled).toBe(true));
  expect(screen.getByRole("button", { name: "View fresh.png" })).toBeTruthy();

  fireEvent.submit(screen.getByTestId("composer-form"));
  await flushPendingTurnsProjectionForTests();
  expect(delivered).toEqual([]);

  // Removing the reference leaves the fresh attachment's own marker alone, and
  // the draft then sends with its bytes.
  await user.click(screen.getByRole("button", { name: "Remove the reference" }));
  expect(textarea().textContent).toBe("[image 2]");
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  await waitFor(() => expect(submitButton().disabled).toBe(false));
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  const call = fake.calls.find((candidate) => candidate.method === "turn/start");
  if (call === undefined) throw new Error("expected the repaired draft to send");
  const input = (call.params as { input: Array<{ type: string; text?: string; name?: string }> }).input;
  expect(input.filter((item) => item.type === "image").map((item) => item.name)).toEqual(["fresh.png"]);
  expect(input.find((item) => item.type === "text")?.text?.includes("[image")).toBe(false);
});

// Two missing references, and the person edited the FIRST one: deleting its
// closing bracket leaves "[image 1", which serialization already refuses to
// call an adopted occurrence. The document mark survives that edit, though, so
// a repair that trusted the mark alone would delete the edited fragment along
// with the second reference - and persist that deletion. Only intact marked
// occurrences are anchors; the fragment and the chip between the two
// references are text the person must keep.
test("removing a missing reference preserves an edited fragment and the chip after it, in one undo", async () => {
  const user = userEvent.setup();
  const ref = "ref_unbacked_edited_fragment";
  const text = "[image 1] /inspect [image 2]";
  writeComposerDraft(ref, {
    text,
    skillNames: [],
    commandNames: ["inspect"],
    mentions: [{ kind: "command", name: "inspect", offset: text.indexOf("/inspect") }],
    stagedImageMarkers: [1, 2],
  });
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, commandInput: true },
      queue: { revision: 0 },
    },
  });
  expect(within(textarea()).getAllByTestId("composer-command-chip")).toHaveLength(1);
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
  await waitFor(() => expect(submitButton().disabled).toBe(true));

  // Delete just the closing bracket of the first reference.
  const editorText = textarea().textContent ?? "";
  const closingBracket = editorText.indexOf("]");
  selectEditorText(textarea(), closingBracket, closingBracket + 1);
  await user.keyboard("{Backspace}");
  await waitFor(() => expect(textarea().textContent).toBe("[image 1 /inspect [image 2]"));
  // Still refusing: the SECOND reference is intact and unbacked.
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Remove the reference" }));

  // Only the intact occurrence is gone. The edited fragment and the command
  // chip between the two references survive, and the chip is still an atom.
  await waitFor(() => expect(textarea().textContent).toBe("[image 1 /inspect "));
  expect(within(textarea()).getAllByTestId("composer-command-chip")).toHaveLength(1);
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  await waitFor(() => expect(submitButton().disabled).toBe(false));

  // ONE undo restores the whole repair, not each deletion separately.
  await user.keyboard("{Control>}z{/Control}");
  await waitFor(() => expect(textarea().textContent).toBe("[image 1 /inspect [image 2]"));
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
});

// A REAL staging gesture wrote the anchor at offset 0, and a separate literal
// "[image 1]" sits beside it in the same draft - typed after the anchor, or
// pasted BEFORE it. In the second case the paste moves the anchor to a new
// offset, so a record still naming offset 0 names the literal instead.
// After a reload the composer holds no tiles, so only the record's own
// occurrence can say which spelling is the anchor whose image is gone. A
// number-only record marks both, and the one repair press deletes prose the
// person wrote - so the record must name the staged occurrence at the position
// the editor moved it to, the repair must remove only it, the literal must
// survive, and Send must come back.
test.each([
  {
    slot: "a literal typed after the anchor",
    ref: "ref_unbacked_staged_plus_typed",
    at: "end" as const,
    pasted: " typed [image 1]",
  },
  {
    slot: "a literal pasted before the anchor",
    ref: "ref_unbacked_staged_after_pasted_literal",
    at: "start" as const,
    pasted: "[image 1] is literal. ",
  },
])("a reloaded staged anchor with $slot repairs only the anchor", async ({ ref, at, pasted }) => {
  installCanvasStubs();
  const user = userEvent.setup();

  // Step 1: the real staging gesture, at the head of an empty draft. This is
  // the write under test: it must say WHICH "[image 1]" it wrote.
  const first = await mountComposerWithHandle(ref);
  selectEditorText(textarea(), 0);
  pastePngInto(textarea(), "staged.png");
  await screen.findByRole("button", { name: "View staged.png" });
  expect(textarea().textContent).toBe("[image 1]");
  // Step 2: a separate literal of the same number, inserted at the caret
  // rather than pasted over the whole document: replacing it would rewrite the
  // anchor and retire the very occurrence under test.
  const text = at === "start" ? `${pasted}[image 1]` : `[image 1]${pasted}`;
  const stagedStart = at === "start" ? pasted.length : 0;
  selectEditorText(textarea(), at === "start" ? 0 : (textarea().textContent?.length ?? 0));
  await user.paste(pasted);
  await waitFor(() => expect(textarea().textContent).toBe(text));
  // The draft says which occurrence the gesture wrote, not merely which number
  // it used - and the position is the one the editor moved the anchor to. A
  // record left at the gesture's original offset names the pasted literal.
  expect(readComposerDraftSnapshot(ref)).toMatchObject({
    text,
    stagedImageMarkers: [1],
    stagedImageRanges: [{ marker: 1, start: stagedStart, end: stagedStart + "[image 1]".length }],
  });
  first.unmount();

  // Step 3: the reload. No tiles come back, so the staged anchor is the
  // missing reference and the surviving literal is prose.
  const second = await mountComposerWithHandle(ref);
  second.fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  expect(textarea().textContent).toBe(text);
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
  await waitFor(() => expect(submitButton().disabled).toBe(true));

  // Step 4: the repair the notice offers. Only the anchor goes.
  const remaining = pasted;
  await user.click(screen.getByRole("button", { name: "Remove the reference" }));
  await waitFor(() => expect(textarea().textContent).toBe(remaining));
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();

  // Step 5: Send is enabled, and the surviving literal travels as the prose it
  // is - with no image item behind it.
  await waitFor(() => expect(submitButton().disabled).toBe(false));
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  const call = second.fake.calls.find((candidate) => candidate.method === "turn/start");
  if (call === undefined) throw new Error("expected the repaired draft to send");
  expect(call.params).toMatchObject({ input: [{ type: "text", text: remaining }] });
});

// Removing the tile is not a repair: the anchor's own literal leaves the text
// with it, and a literal the person typed of the same number survives. The
// removal writes the draft, so a save that still saw the tile in the store
// would name that surviving literal's number as staged - and a reload, holding
// no tiles, would then call the person's prose a missing reference and disable
// Send for a draft that needs no repair at all.
test("removing a staged tile leaves a surviving literal of the same number sendable after a reload", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const ref = "ref_unbacked_removed_tile_literal";

  const first = await mountComposerWithHandle(ref);
  selectEditorText(textarea(), 0);
  pastePngInto(textarea(), "staged.png");
  await screen.findByRole("button", { name: "View staged.png" });
  selectEditorText(textarea(), textarea().textContent?.length ?? 0);
  await user.paste(" typed [image 1]");
  await waitFor(() => expect(textarea().textContent).toBe("[image 1] typed [image 1]"));

  await user.click(screen.getByRole("button", { name: "Remove staged.png" }));
  await waitFor(() => expect(textarea().textContent).toBe(" typed [image 1]"));
  // Nothing in this draft has bytes behind it any more, so the record must not
  // carry a staged number or its range: either one would mark the surviving
  // literal as a missing reference after a reload.
  expect(readComposerDraftSnapshot(ref).stagedImageMarkers).toBeUndefined();
  expect(readComposerDraftSnapshot(ref).stagedImageRanges).toBeUndefined();
  first.unmount();

  const second = await mountComposerWithHandle(ref);
  second.fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  expect(textarea().textContent).toBe(" typed [image 1]");
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  await waitFor(() => expect(submitButton().disabled).toBe(false));

  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  const call = second.fake.calls.find((candidate) => candidate.method === "turn/start");
  if (call === undefined) throw new Error("expected the literal to send");
  expect(call.params).toMatchObject({ input: [{ type: "text", text: " typed [image 1]" }] });
});

test("removing a staged tile when an identical literal sits before its marker preserves the literal", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  await mountComposerWithHandle("ref_a");
  await user.click(textarea());
  act(() => {
    pastePngInto(textarea(), "shot.png");
  });
  await waitFor(() => expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy());
  await waitFor(() => expect(screen.getByRole("button", { name: "View shot.png" })).toBeTruthy());

  selectEditorText(textarea(), 0);
  await user.paste("[image 1] is literal. ");
  expect(textarea().textContent).toBe("[image 1] is literal. [image 1]");

  fireEvent.click(screen.getByRole("button", { name: "Remove shot.png" }));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Remove shot.png" })).toBeNull());

  expect(textarea().textContent).toBe("[image 1] is literal. ");
  expect(readComposerDraftSnapshot("ref_a").stagedImageMarkers).toBeUndefined();
  expect(readComposerDraftSnapshot("ref_a").stagedImageRanges).toBeUndefined();
});

test("deleting a staged marker and undoing restores its staged provenance across reload", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");
  act(() => {
    pastePngInto(textarea(), "shot.png");
  });
  await waitFor(() => expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy());
  await waitFor(() => expect(screen.getByRole("button", { name: "View shot.png" })).toBeTruthy());
  expect(textarea().textContent).toBe("[image 1]");

  // Delete the staged marker, then undo the deletion.
  insertEditorTextAt(textarea(), 0, "", "[image 1]".length);
  expect(textarea().textContent).toBe("");
  fireEvent.keyDown(textarea(), { key: "z", ctrlKey: true });
  expect(textarea().textContent).toBe("[image 1]");
  expect(readComposerDraftSnapshot("ref_a").stagedImageRanges).toEqual([{ marker: 1, start: 0, end: 9 }]);

  // Reopen in a fresh composer: the restored occurrence is refused until repaired.
  cleanup();
  await mountComposer("ref_a");
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
  expect(submitButton().disabled).toBe(true);
  fireEvent.click(within(screen.getByTestId("composer-unbacked-image")).getByRole("button"));
  expect(textarea().textContent).toBe("");
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
});

test("attaching a replacement over a selected missing reference preserves undo for an earlier prose edit", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  writeComposerDraft("ref_a", {
    text: "hello [image 1] world",
    skillNames: [],
    stagedImageMarkers: [1],
    stagedImageRanges: [{ marker: 1, start: 6, end: 15 }],
  });
  await mountComposerWithHandle("ref_a");
  await user.click(textarea());
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();

  // Edit prose before the missing reference.
  selectEditorText(textarea(), 5);
  await user.paste(" there");
  expect(textarea().textContent).toBe("hello there [image 1] world");

  // Select the missing reference and attach its replacement.
  selectEditorText(textarea(), "hello there ".length, "hello there [image 1]".length);
  act(() => {
    pastePngInto(textarea(), "replacement.png");
  });
  await waitFor(() => expect(screen.getByRole("button", { name: "Remove replacement.png" })).toBeTruthy());
  await waitFor(() => expect(screen.getByRole("button", { name: "View replacement.png" })).toBeTruthy());
  expect(textarea().textContent).toBe("hello there [image 2] world");
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();

  // Undo reverses the earlier prose edit while keeping the replacement image attached.
  fireEvent.keyDown(textarea(), { key: "z", ctrlKey: true });
  expect(textarea().textContent).toBe("hello [image 2] world");
  expect(screen.getByRole("button", { name: "Remove replacement.png" })).toBeTruthy();
});

test("deleting and retyping the closing bracket of a missing reference restores the refusal", async () => {
  const user = userEvent.setup();
  writeComposerDraft("ref_a", {
    text: "[image 1]",
    skillNames: [],
    stagedImageMarkers: [1],
    stagedImageRanges: [{ marker: 1, start: 0, end: 9 }],
  });
  await mountComposerWithHandle("ref_a");
  await user.click(textarea());
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();

  // Delete only the closing bracket: incomplete fragment is sendable.
  insertEditorTextAt(textarea(), 8, "", 9);
  expect(textarea().textContent).toBe("[image 1");
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();

  // Type the closing bracket back: the original missing reference is refused again.
  selectEditorText(textarea(), 8);
  await user.paste("]");
  expect(textarea().textContent).toBe("[image 1]");
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
  expect(submitButton().disabled).toBe(true);
});

test("pasting over the opening bracket of a missing reference keeps it refused and repair preserves the pasted prefix", async () => {
  const user = userEvent.setup();
  writeComposerDraft("ref_a", {
    text: "[image 1]",
    skillNames: [],
    stagedImageMarkers: [1],
    stagedImageRanges: [{ marker: 1, start: 0, end: 9 }],
  });
  await mountComposerWithHandle("ref_a");
  await user.click(textarea());
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();

  selectEditorText(textarea(), 0, 1);
  await user.paste("See [");
  expect(textarea().textContent).toBe("See [image 1]");
  expect(screen.getByTestId("composer-unbacked-image")).toBeTruthy();
  expect(submitButton().disabled).toBe(true);

  fireEvent.click(within(screen.getByTestId("composer-unbacked-image")).getByRole("button"));
  expect(textarea().textContent).toBe("See ");
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
});

// The other order: the person deletes the staged ANCHOR and leaves the tile
// behind - a tile whose text reference is gone but which is still attached.
// The persistence pass must not read "a tile holds this number" as "the
// surviving occurrence was staged". The staging gesture recorded the exact
// span it wrote, and that span is gone, so the surviving literal is prose. A
// record that named its number anyway would mark the literal after a reload,
// disable Send, and let one repair press delete what the person typed.
test("deleting a staged anchor while its tile remains leaves the surviving literal sendable after a reload", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const ref = "ref_unbacked_deleted_anchor_kept_tile";

  const first = await mountComposerWithHandle(ref);
  selectEditorText(textarea(), 0);
  pastePngInto(textarea(), "staged.png");
  await screen.findByRole("button", { name: "View staged.png" });
  selectEditorText(textarea(), textarea().textContent?.length ?? 0);
  await user.paste(" typed [image 1]");
  await waitFor(() => expect(textarea().textContent).toBe("[image 1] typed [image 1]"));

  // Delete the STAGED occurrence (offsets 0..9), leaving the literal and the
  // tile's own button in place.
  selectEditorText(textarea(), 0, "[image 1]".length);
  await user.keyboard("{Backspace}");
  await waitFor(() => expect(textarea().textContent).toBe(" typed [image 1]"));
  expect(screen.getByRole("button", { name: "View staged.png" })).toBeTruthy();
  // The record names no provenance for the surviving literal, so it survives
  // the reload as the prose it is.
  expect(readComposerDraftSnapshot(ref).stagedImageMarkers).toBeUndefined();
  expect(readComposerDraftSnapshot(ref).stagedImageRanges).toBeUndefined();
  first.unmount();

  const second = await mountComposerWithHandle(ref);
  second.fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  expect(textarea().textContent).toBe(" typed [image 1]");
  // No repair is needed, so nothing is refused and Send is available.
  expect(screen.queryByTestId("composer-unbacked-image")).toBeNull();
  await waitFor(() => expect(submitButton().disabled).toBe(false));

  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  const call = second.fake.calls.find((candidate) => candidate.method === "turn/start");
  if (call === undefined) throw new Error("expected the literal to send");
  expect(call.params).toMatchObject({ input: [{ type: "text", text: " typed [image 1]" }] });
});
