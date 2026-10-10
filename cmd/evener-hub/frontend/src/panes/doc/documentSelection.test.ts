// Tests-first checkpoint for finished-selection detection (Task 5).
//
// `./documentSelection` is the production module this checkpoint precedes, so
// the static import below deliberately does NOT resolve yet: the file is a
// collection/setup failure until the source grant lands, and `tsc` reports the
// missing module instead of this file hiding it behind a stub or an optional
// call. The approved interfaces exercised here are `FinishedDocumentSelection`
// and `observeFinishedDocumentSelections({ root, map, onFinish })`.
//
// Every case drives a REAL bound document map and the real Selection/Range APIs
// with native event dispatch; `onFinish` observes the production observer's
// output, never replaces it. The owning-document case mounts a real secondary
// browsing document (an iframe) with its own Selection/Range. These
// deterministic event-state tests do not prove trusted browser gestures or a
// physical phone's selection handles.
import type { SourceRange } from "@evener/appwire-client";
import { render } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { DocumentMarkdown } from "../../widgets/markdown/DocumentMarkdown";
import { type BoundDocumentMap, bindDocumentMap, renderDocument } from "../../widgets/markdown/documentSourceMap";
import { type FinishedDocumentSelection, observeFinishedDocumentSelections } from "./documentSelection";
import { FIRST, SECOND, SOURCE } from "./testing/annotationFixtures";

// The completion stabilization is elapsed-time behavior, so the file runs on a
// fake clock (never a fixed sleep), faking only the timer pair the observer may
// schedule with so React's own scheduling is untouched. A completed gesture may
// emit immediately or after a short scheduled stabilization; flushing the clock
// by a generous margin accommodates both without asserting the exact delay.
const FLUSH_MS = 500;
const ACTIVE_TOUCH_MS = 150;

const teardowns: Array<() => void> = [];
function onTeardown(fn: () => void): void {
  teardowns.push(fn);
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
});
afterEach(() => {
  const failures: unknown[] = [];
  // Tear down observers (and everything else) before restoring timers and the
  // global selection, even when the test threw.
  for (const teardown of teardowns.splice(0).reverse()) {
    try {
      teardown();
    } catch (error) {
      failures.push(error);
    }
  }
  vi.useRealTimers();
  window.getSelection()?.removeAllRanges();
  // Restore any listener spies the test installed, even after a failed
  // assertion.
  vi.restoreAllMocks();
  if (failures.length === 1) throw failures[0];
  if (failures.length > 1) throw new AggregateError(failures);
});

interface MountedDocument {
  view: ReturnType<typeof render>;
  holder: { map: BoundDocumentMap | null };
  root: HTMLElement;
}

// The real bound map and its document scope: `holder.map` follows remounts so a
// replacement binding can be read after `rerender`.
function mountDocument(source: string, displayedSnapshotId = "snap-1", mappingId = "map-1"): MountedDocument {
  const holder: { map: BoundDocumentMap | null } = { map: null };
  const view = render(
    createElement(DocumentMarkdown, {
      source,
      identity: { displayedSnapshotId, mappingId },
      onMap: (value: BoundDocumentMap | null) => {
        holder.map = value;
      },
    }),
  );
  onTeardown(() => view.unmount());
  if (!holder.map) throw new Error("document not bound");
  const root = view.container.firstElementChild;
  if (!(root instanceof HTMLElement)) throw new Error("missing document root");
  return { view, holder, root };
}

function observe(
  root: HTMLElement,
  map: BoundDocumentMap,
  onFinish: (capture: FinishedDocumentSelection) => void,
): void {
  onTeardown(observeFinishedDocumentSelections({ root, map, onFinish }));
}

function textNodes(root: Node): Text[] {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  for (let node = walker.nextNode(); node; node = walker.nextNode()) nodes.push(node as Text);
  return nodes;
}

// Test-local construction of a Range for a literal rendered passage: it walks
// the real text nodes, never the production mapper.
function rangeForText(owner: Document, root: HTMLElement, text: string, occurrence = 0): Range {
  const nodes = textNodes(root);
  let total = "";
  for (const node of nodes) total += node.data;
  let start = -1;
  for (let i = 0; i <= occurrence; i++) start = total.indexOf(text, start + 1);
  if (start < 0) throw new Error(`rendered text unavailable: ${JSON.stringify(text)}`);
  const range = owner.createRange();
  let offset = 0;
  let started = false;
  for (const node of nodes) {
    const end = offset + node.data.length;
    if (!started && start < end) {
      range.setStart(node, start - offset);
      started = true;
    }
    if (started && start + text.length <= end) {
      range.setEnd(node, start + text.length - offset);
      break;
    }
    offset = end;
  }
  if (!started) throw new Error(`rendered text start unavailable: ${JSON.stringify(text)}`);
  return range;
}

function selectText(root: HTMLElement, text: string, occurrence = 0): void {
  const owner = root.ownerDocument;
  const selection = owner.getSelection();
  if (!selection) throw new Error("selection unavailable");
  selection.removeAllRanges();
  selection.addRange(rangeForText(owner, root, text, occurrence));
}

// A backward selection keeps the same document-ordered Range a real browser
// reports for a right-to-left drag.
function selectTextBackward(root: HTMLElement, text: string, occurrence = 0): void {
  const owner = root.ownerDocument;
  const range = rangeForText(owner, root, text, occurrence);
  const selection = owner.getSelection();
  if (!selection) throw new Error("selection unavailable");
  selection.removeAllRanges();
  selection.setBaseAndExtent(range.endContainer, range.endOffset, range.startContainer, range.startOffset);
}

function selectionchange(owner: Document = document): boolean {
  // Cancelable so a dispatch result of `true` proves the observer did not
  // preventDefault; a noncancelable event would make that assertion vacuous.
  return owner.dispatchEvent(new Event("selectionchange", { cancelable: true }));
}

function pointer(
  type: "pointerdown" | "pointerup" | "pointercancel" | "pointermove",
  target: EventTarget,
  kind: "mouse" | "touch" = "mouse",
  pointerId = kind === "touch" ? 2 : 1,
): boolean {
  return target.dispatchEvent(
    new PointerEvent(type, {
      bubbles: true,
      cancelable: true,
      composed: true,
      button: 0,
      buttons: type === "pointerup" || type === "pointercancel" ? 0 : 1,
      pointerId,
      pointerType: kind,
      isPrimary: true,
    }),
  );
}

function firstParagraph(root: HTMLElement): HTMLElement {
  const paragraph = root.querySelector("p");
  // Realm-safe: an iframe document has its own HTMLElement constructor, so the
  // global one cannot see its nodes.
  const ElementCtor = root.ownerDocument.defaultView?.HTMLElement;
  if (!paragraph || !ElementCtor || !(paragraph instanceof ElementCtor)) throw new Error("missing rendered paragraph");
  return paragraph as HTMLElement;
}

const expected = (range: SourceRange, text = "same"): FinishedDocumentSelection => ({
  displayedSnapshotId: "snap-1",
  mappingId: "map-1",
  sourceRange: range,
  selectedRenderedText: text,
});

test("a completed primary mouse drag emits the exact identity, literal range and selected text", () => {
  const { holder, root } = mountDocument(SOURCE);
  const onFinish = vi.fn();
  observe(root, holder.map as BoundDocumentMap, onFinish);

  const outside = document.createElement("button");
  outside.textContent = "outside";
  document.body.append(outside);
  onTeardown(() => outside.remove());
  outside.focus();

  const paragraph = firstParagraph(root);
  selectText(root, "same");
  // The gesture must not be cancelled to make completion happen.
  expect(pointer("pointerdown", paragraph)).toBe(true);
  expect(selectionchange()).toBe(true);
  expect(onFinish).not.toHaveBeenCalled();

  const released = pointer("pointerup", paragraph);
  vi.advanceTimersByTime(FLUSH_MS);

  expect(released).toBe(true);
  expect(onFinish).toHaveBeenCalledTimes(1);
  expect(onFinish).toHaveBeenCalledWith(expected(FIRST));
  // Selection and focus survive the completed gesture.
  expect(document.activeElement).toBe(outside);
  expect(document.getSelection()?.toString()).toBe("same");
  expect(document.getSelection()?.rangeCount).toBe(1);
});

test("a backward drag still maps the selected repeated occurrence, not the first", () => {
  const { holder, root } = mountDocument(SOURCE);
  const onFinish = vi.fn();
  observe(root, holder.map as BoundDocumentMap, onFinish);
  selectTextBackward(root, "same", 1);

  const paragraph = firstParagraph(root);
  pointer("pointerdown", paragraph);
  pointer("pointerup", paragraph);
  vi.advanceTimersByTime(FLUSH_MS);

  expect(onFinish).toHaveBeenCalledTimes(1);
  expect(onFinish).toHaveBeenCalledWith(expected(SECOND));
});

test("an active drag with a moving selection does not emit before pointer release", () => {
  const { holder, root } = mountDocument(SOURCE);
  const onFinish = vi.fn();
  observe(root, holder.map as BoundDocumentMap, onFinish);
  const paragraph = firstParagraph(root);

  pointer("pointerdown", paragraph);
  selectText(root, "same");
  selectionchange();
  pointer("pointermove", paragraph);
  selectText(root, "same", 1);
  selectionchange();
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).not.toHaveBeenCalled();

  pointer("pointerup", paragraph);
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).toHaveBeenCalledTimes(1);
  expect(onFinish).toHaveBeenCalledWith(expected(SECOND));
});

test("a completed Shift keyboard selection emits only the verified nonempty current capture", () => {
  const { holder, root } = mountDocument(SOURCE);
  const onFinish = vi.fn();
  observe(root, holder.map as BoundDocumentMap, onFinish);

  document.dispatchEvent(new KeyboardEvent("keydown", { key: "Shift", bubbles: true }));
  selectText(root, "same", 1);
  selectionchange();
  document.dispatchEvent(new KeyboardEvent("keyup", { key: "Shift", bubbles: true }));
  vi.advanceTimersByTime(FLUSH_MS);

  expect(onFinish).toHaveBeenCalledTimes(1);
  expect(onFinish).toHaveBeenCalledWith(expected(SECOND));
});

test("ordinary key events produce no passage", () => {
  const { holder, root } = mountDocument(SOURCE);
  const onFinish = vi.fn();
  observe(root, holder.map as BoundDocumentMap, onFinish);
  selectText(root, "same");

  document.dispatchEvent(new KeyboardEvent("keydown", { key: "a", bubbles: true }));
  document.dispatchEvent(new KeyboardEvent("keyup", { key: "a", bubbles: true }));
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).not.toHaveBeenCalled();
});

test("collapsed, outside, chrome and stale selections never produce a passage", () => {
  const { holder, root } = mountDocument(SOURCE);
  const onFinish = vi.fn();
  observe(root, holder.map as BoundDocumentMap, onFinish);
  const paragraph = firstParagraph(root);

  // Collapsed caret inside a verified run.
  const caret = paragraph.firstChild;
  if (!caret) throw new Error("missing rendered text");
  const selection = document.getSelection();
  if (!selection) throw new Error("selection unavailable");
  selection.removeAllRanges();
  selection.collapse(caret, 2);
  pointer("pointerdown", paragraph);
  pointer("pointerup", paragraph);
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).not.toHaveBeenCalled();

  // A selection whose endpoints are outside the document scope.
  const outside = document.createElement("p");
  outside.textContent = "outside prose";
  document.body.append(outside);
  try {
    const outsideRange = document.createRange();
    outsideRange.selectNodeContents(outside);
    selection.removeAllRanges();
    selection.addRange(outsideRange);
    pointer("pointerdown", paragraph);
    pointer("pointerup", paragraph);
    vi.advanceTimersByTime(FLUSH_MS);
    expect(onFinish).not.toHaveBeenCalled();
  } finally {
    outside.remove();
  }

  // A non-run ("chrome") text node inside the scope.
  const chrome = document.createElement("span");
  chrome.textContent = "chrome text";
  root.append(chrome);
  try {
    const chromeRange = document.createRange();
    chromeRange.selectNodeContents(chrome);
    selection.removeAllRanges();
    selection.addRange(chromeRange);
    pointer("pointerdown", paragraph);
    pointer("pointerup", paragraph);
    vi.advanceTimersByTime(FLUSH_MS);
    expect(onFinish).not.toHaveBeenCalled();
  } finally {
    chrome.remove();
  }

  // A disposed map reports the selection as stale.
  selectText(root, "same");
  (holder.map as BoundDocumentMap).dispose();
  pointer("pointerdown", paragraph);
  pointer("pointerup", paragraph);
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).not.toHaveBeenCalled();
});

test("touch adjustment is not completed by elapsed time while the interaction is active", () => {
  const { holder, root } = mountDocument(SOURCE);
  const onFinish = vi.fn();
  observe(root, holder.map as BoundDocumentMap, onFinish);
  const paragraph = firstParagraph(root);

  pointer("pointerdown", paragraph, "touch");
  selectText(root, "same");
  selectionchange();
  vi.advanceTimersByTime(ACTIVE_TOUCH_MS);
  expect(onFinish).not.toHaveBeenCalled();

  pointer("pointerup", paragraph, "touch");
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).toHaveBeenCalledTimes(1);
  expect(onFinish).toHaveBeenCalledWith(expected(FIRST));
});

test("a cancelled or superseded interaction never emits a stale capture", () => {
  const { holder, root } = mountDocument(SOURCE);
  const onFinish = vi.fn();
  observe(root, holder.map as BoundDocumentMap, onFinish);
  const paragraph = firstParagraph(root);

  // Cancellation while the interaction is still active.
  pointer("pointerdown", paragraph);
  selectText(root, "same");
  selectionchange();
  pointer("pointercancel", paragraph);
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).not.toHaveBeenCalled();

  // A newer interaction supersedes the cancelled one and emits only its own
  // capture.
  pointer("pointerdown", paragraph);
  selectText(root, "same", 1);
  pointer("pointerup", paragraph);
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).toHaveBeenCalledTimes(1);
  expect(onFinish).toHaveBeenCalledWith(expected(SECOND));
});

test("a pointer interaction outside the mapped root never finishes the retained passage", () => {
  const { holder, root } = mountDocument(SOURCE);
  const onFinish = vi.fn();
  observe(root, holder.map as BoundDocumentMap, onFinish);
  // A valid mapped passage is already selected and stays selected.
  selectText(root, "same");

  const outside = document.createElement("button");
  outside.textContent = "outside";
  document.body.append(outside);
  onTeardown(() => outside.remove());

  pointer("pointerdown", outside);
  pointer("pointerup", outside);
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).not.toHaveBeenCalled();

  // A new in-root drag still finishes with the exact expected range/identity.
  selectText(root, "same", 1);
  pointer("pointerdown", firstParagraph(root));
  pointer("pointerup", firstParagraph(root));
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).toHaveBeenCalledTimes(1);
  expect(onFinish).toHaveBeenCalledWith(expected(SECOND));
});

test.each(["pointerup", "pointercancel"] as const)(
  "a different pointer's %s does not end the active interaction",
  (end) => {
    const { holder, root } = mountDocument(SOURCE);
    const onFinish = vi.fn();
    observe(root, holder.map as BoundDocumentMap, onFinish);
    const paragraph = firstParagraph(root);

    // Primary in-root interaction under pointer 11 with a valid selection.
    pointer("pointerdown", paragraph, "mouse", 11);
    selectText(root, "same");
    // A different pointer's end must not finish or abandon it.
    pointer(end, paragraph, "mouse", 12);
    vi.advanceTimersByTime(FLUSH_MS);
    expect(onFinish).not.toHaveBeenCalled();

    // Releasing the original pointer emits exactly once.
    pointer("pointerup", paragraph, "mouse", 11);
    vi.advanceTimersByTime(FLUSH_MS);
    expect(onFinish).toHaveBeenCalledTimes(1);
    expect(onFinish).toHaveBeenCalledWith(expected(FIRST));
  },
);

test("releasing Shift without changing the selection never re-finishes the retained passage", () => {
  const { holder, root } = mountDocument(SOURCE);
  const onFinish = vi.fn();
  observe(root, holder.map as BoundDocumentMap, onFinish);
  // A valid passage is already selected before Shift is ever pressed.
  selectText(root, "same");

  document.dispatchEvent(new KeyboardEvent("keydown", { key: "Shift", bubbles: true }));
  document.dispatchEvent(new KeyboardEvent("keyup", { key: "Shift", bubbles: true }));
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).not.toHaveBeenCalled();

  // An actual changed selection while Shift is held still emits.
  document.dispatchEvent(new KeyboardEvent("keydown", { key: "Shift", bubbles: true }));
  selectText(root, "same", 1);
  selectionchange();
  document.dispatchEvent(new KeyboardEvent("keyup", { key: "Shift", bubbles: true }));
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).toHaveBeenCalledTimes(1);
  expect(onFinish).toHaveBeenCalledWith(expected(SECOND));
});

test("registers on the root's owning document and removes every listener on disposal", () => {
  const { holder, root } = mountDocument(SOURCE);
  const owner = root.ownerDocument;
  expect(owner).toBe(document);
  const add = vi.spyOn(owner, "addEventListener");
  const remove = vi.spyOn(owner, "removeEventListener");
  const onFinish = vi.fn();
  const dispose = observeFinishedDocumentSelections({ root, map: holder.map as BoundDocumentMap, onFinish });
  onTeardown(dispose);

  const registered = add.mock.calls.map(([type, listener]) => [type, listener] as const);
  const types = new Set(registered.map(([type]) => type));
  for (const type of ["pointerdown", "pointerup", "pointercancel", "selectionchange", "keydown", "keyup"]) {
    expect(types.has(type)).toBe(true);
  }

  dispose();
  for (const [type, listener] of registered) {
    expect(
      remove.mock.calls.some(([removedType, removedListener]) => removedType === type && removedListener === listener),
    ).toBe(true);
  }

  // Outcome alongside registration: the owning document's events no longer
  // produce a passage after disposal.
  selectText(root, "same");
  pointer("pointerdown", firstParagraph(root));
  pointer("pointerup", firstParagraph(root));
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).not.toHaveBeenCalled();
});

test("observes the root's owning document, not the global selection", () => {
  const frame = document.createElement("iframe");
  document.body.append(frame);
  onTeardown(() => frame.remove());
  const owner = frame.contentDocument;
  const ownerWindow = frame.contentWindow;
  if (!owner || !ownerWindow) throw new Error("missing iframe document");
  expect(owner).not.toBe(document);

  const rendered = renderDocument(SOURCE, { displayedSnapshotId: "snap-1", mappingId: "map-1" });
  const root = owner.createElement("div");
  owner.body.append(root);
  for (const segment of rendered.segments) {
    if (segment.kind !== "markdown") continue;
    const holder = owner.createElement("div");
    holder.innerHTML = segment.html;
    root.append(holder);
  }
  const map = bindDocumentMap(root, rendered.map);
  onTeardown(() => map.dispose());

  const onFinish = vi.fn();
  const dispose = observeFinishedDocumentSelections({ root, map, onFinish });
  onTeardown(dispose);

  // The owning document selects FIRST; the global document selects elsewhere.
  selectText(root, "same");
  const globalText = document.createTextNode("global passage");
  document.body.append(globalText);
  onTeardown(() => globalText.remove());
  const globalRange = document.createRange();
  globalRange.selectNodeContents(globalText);
  const globalSelection = document.getSelection();
  if (!globalSelection) throw new Error("global selection unavailable");
  globalSelection.removeAllRanges();
  globalSelection.addRange(globalRange);

  expect(owner.getSelection()?.toString()).toBe("same");
  expect(document.getSelection()?.toString()).toBe("global passage");

  const paragraph = firstParagraph(root);
  pointer("pointerdown", paragraph);
  selectionchange(owner);
  pointer("pointerup", paragraph);
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).toHaveBeenCalledTimes(1);
  expect(onFinish).toHaveBeenCalledWith(expected(FIRST));

  // Disposal stops output from the owning document.
  dispose();
  selectText(root, "same", 1);
  pointer("pointerdown", paragraph);
  pointer("pointerup", paragraph);
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).toHaveBeenCalledTimes(1);
});

test("does not keep emitting after the map is disposed or rebound by a remount", () => {
  const mounted = mountDocument(SOURCE);
  const onFinish = vi.fn();
  observe(mounted.root, mounted.holder.map as BoundDocumentMap, onFinish);
  const paragraph = firstParagraph(mounted.root);

  // A disposed map cannot complete a later gesture.
  selectText(mounted.root, "same");
  (mounted.holder.map as BoundDocumentMap).dispose();
  pointer("pointerdown", paragraph);
  pointer("pointerup", paragraph);
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).not.toHaveBeenCalled();

  // A replacement binding on the same scope: the old observer holds the old map
  // and stays silent, while a fresh observer bound to the new map emits.
  mounted.view.rerender(
    createElement(DocumentMarkdown, {
      source: SOURCE,
      identity: { displayedSnapshotId: "snap-2", mappingId: "map-2" },
      onMap: (value: BoundDocumentMap | null) => {
        mounted.holder.map = value;
      },
    }),
  );
  const fresh = mounted.holder.map;
  if (!fresh) throw new Error("replacement map not bound");
  selectText(mounted.root, "same");
  pointer("pointerdown", firstParagraph(mounted.root));
  pointer("pointerup", firstParagraph(mounted.root));
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFinish).not.toHaveBeenCalled();

  const onFresh = vi.fn();
  observe(mounted.root, fresh, onFresh);
  selectText(mounted.root, "same");
  pointer("pointerdown", firstParagraph(mounted.root));
  pointer("pointerup", firstParagraph(mounted.root));
  vi.advanceTimersByTime(FLUSH_MS);
  expect(onFresh).toHaveBeenCalledWith({
    displayedSnapshotId: "snap-2",
    mappingId: "map-2",
    sourceRange: FIRST,
    selectedRenderedText: "same",
  });
});
