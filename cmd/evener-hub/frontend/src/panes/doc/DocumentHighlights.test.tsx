// Component tests for the saved-highlight overlay (Task 5). `./DocumentHighlights`
// is the accepted production module; the approved props are `map`, `annotations`,
// `scrollElement`. The restored interaction reveals a note on hover, keyboard
// focus and a completed click/tap for every annotation overlapping the touched
// passage; the synthetic pointer cases at the end add cancelled-press and
// unrelated-release protection for the forthcoming interaction design.
//
// The component consumes the REAL bound document map and REAL DOM. The shared
// geometry seam supplies the rectangles jsdom cannot lay out, for BOTH the Range
// and the scroll element; a separate seam supplies the missing font-set and
// visualViewport platform capabilities. Saved `AnnotationView` values are
// literal typed data checked against the shared contract (no fake repository
// methods). Assertions cover the frozen observable markers
// (`data-document-highlight`, `data-operation-id`, `data-annotation-note`) and
// the non-text-decoration, note-reveal, overlap and geometry-update behavior.
// jsdom proves none of this as real layout.
import {
  type AcceptedSourceLease,
  type AnnotationView,
  type CapturedPassage,
  type DocumentKey,
  displayedFingerprint,
  type PreparedAnnotation,
  type SourceRange,
  sourcePositions,
} from "@evener/appwire-client";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { StrictMode, useCallback, useState } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { DocumentMarkdown } from "../../widgets/markdown/DocumentMarkdown";
import type { BoundDocumentMap } from "../../widgets/markdown/documentSourceMap";
import { DocumentHighlights } from "./DocumentHighlights";
import { FIRST, SECOND, SOURCE } from "./testing/annotationFixtures";
import { DocumentGeometrySeam, type TestRect } from "./testing/documentGeometryTestUtils";

const DOCUMENT_KEY: DocumentKey = {
  namespace: { kind: "browser", origin: "https://example.test" },
  sessionRef: "root",
  path: "NOTES.md",
};

// Literal typed saved annotations: the shape is the shared contract, so a
// contract change fails this file rather than a private fixture drifting.
function annotationView(
  operationId: string,
  sourceRange: SourceRange,
  note: string,
  handoff: AnnotationView["handoff"] = { state: "applied", draftRevision: 1 },
): AnnotationView {
  const evidence = { wholeRevision: "rev-A", fingerprint: displayedFingerprint(SOURCE, false) };
  const positions = sourcePositions(SOURCE, sourceRange);
  const capture: CapturedPassage = {
    evidence,
    sourceRange,
    start: positions.start,
    endInclusive: positions.endInclusive,
    selectedRenderedText: "same",
    contextBefore: "",
    contextAfter: "",
    mappingId: "map-1",
    displayedSnapshotId: "snap-1",
  };
  const lease: AcceptedSourceLease = {
    key: DOCUMENT_KEY,
    namespaceIncarnation: "inc-1",
    epoch: "epoch-1",
    headRevision: 1,
    displayedSnapshotId: "snap-1",
  };
  const operation: PreparedAnnotation = {
    operationId,
    lease,
    capture,
    note,
    destination: { kind: "browser-restored-session" },
  };
  return {
    operation,
    receipt: { operationId, epoch: "epoch-1", sessionSequence: 1 },
    handoff,
  };
}

const IDENTITY = { displayedSnapshotId: "snap-1", mappingId: "map-1" };
const SCROLL_RECT: TestRect = { left: 0, top: 0, right: 200, bottom: 100 };
const FIRST_RECT: TestRect = { left: 10, top: 10, right: 20, bottom: 20 };

// The real nested reader shape (DocPane): the scroll owner is the PaneScaffold
// body, and the positioned markdown host is a CHILD of it, inset by the scroll
// owner's padding. `DocumentHighlights` mounts inside that host, so a band's
// containing block is the host's padding box — here 24px inside the scroll
// owner's rectangle (the desktop `--space-5`) — never the scroll owner's own
// rectangle. These two rectangles are deliberately distinct, so a band measured
// against the scroll owner lands 24px off every axis.
const NESTED_SCROLL_RECT: TestRect = { left: 0, top: 0, right: 200, bottom: 100 };
const NESTED_HOST_RECT: TestRect = { left: 24, top: 24, right: 176, bottom: 76 };
// A fully-inside fragment and a fragment the scroll clip trims on two edges, so
// the same case proves the origin AND that clipping/size survive.
const NESTED_INSIDE_RECT: TestRect = { left: 40, top: 50, right: 70, bottom: 65 };
const NESTED_CLIPPED_RECT: TestRect = { left: 30, top: 55, right: 220, bottom: 130 };

const geometry = new DocumentGeometrySeam();
// Installed Vitest runs finished hooks in `stack` (reverse-registration) order,
// so this local hook runs BEFORE testSetup's global React cleanup. Each rendered
// view therefore registers its own unmount here, and every unmount runs before
// the geometry/platform seams are restored.
const unmountHooks: Array<() => void> = [];
const restoreHooks: Array<() => void> = [];
function onUnmount(fn: () => void): void {
  unmountHooks.push(fn);
}
function onRestore(fn: () => void): void {
  restoreHooks.push(fn);
}

beforeEach(({ onTestFinished }) => {
  geometry.install();
  onTestFinished(() => {
    const failures: unknown[] = [];
    for (const hook of unmountHooks.splice(0).reverse()) {
      try {
        hook();
      } catch (error) {
        failures.push(error);
      }
    }
    for (const hook of restoreHooks.splice(0).reverse()) {
      try {
        hook();
      } catch (error) {
        failures.push(error);
      }
    }
    try {
      geometry.restore();
    } catch (error) {
      failures.push(error);
    }
    if (failures.length === 1) throw failures[0];
    if (failures.length > 1) throw new AggregateError(failures);
  });
});
afterEach(() => {
  window.getSelection()?.removeAllRanges();
  vi.restoreAllMocks();
});

// jsdom ships neither FontFaceSet nor visualViewport; this supplies both as plain
// EventTargets so a genuine event dispatch can drive the production listeners.
function installFontAndViewportCapabilities(): { fonts: EventTarget; viewport: EventTarget } {
  const fonts = new EventTarget();
  const viewport = new EventTarget();
  const fontsBefore = Object.getOwnPropertyDescriptor(document, "fonts");
  const viewportBefore = Object.getOwnPropertyDescriptor(window, "visualViewport");
  Object.defineProperty(document, "fonts", { configurable: true, value: fonts });
  Object.defineProperty(window, "visualViewport", { configurable: true, value: viewport });
  onRestore(() => {
    if (fontsBefore) Object.defineProperty(document, "fonts", fontsBefore);
    else Reflect.deleteProperty(document, "fonts");
    if (viewportBefore) Object.defineProperty(window, "visualViewport", viewportBefore);
    else Reflect.deleteProperty(window, "visualViewport");
  });
  return { fonts, viewport };
}

// jsdom ships no ResizeObserver. This supplies a minimal, real class so a
// genuine observed-element size change can drive the production re-measure.
// It is NOT a geometry fake: the test still supplies every rectangle through the
// geometry seam; this only delivers the browser's "the observed element changed
// size" notification, which jsdom cannot compute.
interface InstalledResizeObserver {
  cb: ResizeObserverCallback;
  targets: Element[];
  disconnected: boolean;
}
function installResizeObserverCapability(): {
  observers: InstalledResizeObserver[];
  fire: () => void;
} {
  const observers: InstalledResizeObserver[] = [];
  class StubResizeObserver {
    cb: ResizeObserverCallback;
    targets: Element[] = [];
    disconnected = false;
    constructor(cb: ResizeObserverCallback) {
      this.cb = cb;
      observers.push(this as unknown as InstalledResizeObserver);
    }
    observe(target: Element): void {
      this.targets.push(target);
    }
    unobserve(): void {}
    disconnect(): void {
      this.disconnected = true;
    }
  }
  const before = Object.getOwnPropertyDescriptor(window, "ResizeObserver");
  Object.defineProperty(window, "ResizeObserver", { configurable: true, value: StubResizeObserver });
  onRestore(() => {
    if (before) Object.defineProperty(window, "ResizeObserver", before);
    else Reflect.deleteProperty(window, "ResizeObserver");
  });
  const fire = (): void => {
    for (const observer of observers) {
      const entry = { target: observer.targets[0] } as unknown as ResizeObserverEntry;
      observer.cb([entry], observer as unknown as ResizeObserver);
    }
  };
  return { observers, fire };
}

interface ReaderProps {
  source: string;
  annotations: AnnotationView[];
  identity?: { displayedSnapshotId: string; mappingId: string };
  mapRef: { current: BoundDocumentMap | null };
}

// The real reader shape: the document scope IS the scroll owner, the map is the
// real bound map, and DocumentHighlights consumes both. `onMap` is stabilized
// for the same `mapRef` so an unrelated re-render never rebinds the document,
// while a changed identity/source still publishes a fresh map.
function Reader({ source, annotations, identity = IDENTITY, mapRef }: ReaderProps) {
  const [map, setMap] = useState<BoundDocumentMap | null>(null);
  const [scroll, setScroll] = useState<HTMLDivElement | null>(null);
  const onMap = useCallback(
    (value: BoundDocumentMap | null) => {
      mapRef.current = value;
      setMap(value);
    },
    [mapRef],
  );
  return (
    <div ref={setScroll} data-testid="scroll">
      <DocumentMarkdown source={source} identity={identity} onMap={onMap} />
      {map && scroll && <DocumentHighlights map={map} annotations={annotations} scrollElement={scroll} />}
    </div>
  );
}

interface NestedReaderProps {
  source: string;
  annotations: AnnotationView[];
  identity?: { displayedSnapshotId: string; mappingId: string };
  mapRef: { current: BoundDocumentMap | null };
  hostRef: { current: HTMLDivElement | null };
}

// The real DocPane nesting: a positioned markdown host (`position: relative`)
// sits INSIDE the scroll owner, and `DocumentHighlights` mounts inside that
// host. `bindHost` publishes the host's parent as the scroll owner, exactly as
// `DocPane.bindHost` does, so the host — not the scroll owner — is the
// containing block every absolutely positioned band is measured against.
function NestedReader({ source, annotations, identity = IDENTITY, mapRef, hostRef }: NestedReaderProps) {
  const [map, setMap] = useState<BoundDocumentMap | null>(null);
  const [scroll, setScroll] = useState<HTMLElement | null>(null);
  const [content, setContent] = useState<HTMLDivElement | null>(null);
  const onMap = useCallback(
    (value: BoundDocumentMap | null) => {
      mapRef.current = value;
      setMap(value);
    },
    [mapRef],
  );
  const bindHost = useCallback(
    (node: HTMLDivElement | null) => {
      hostRef.current = node;
      setContent(node);
      setScroll(node?.parentElement ?? null);
    },
    [hostRef],
  );
  return (
    <div data-testid="scroll">
      <div ref={bindHost} data-testid="host">
        <DocumentMarkdown source={source} identity={identity} onMap={onMap} />
        {map && scroll && (
          <DocumentHighlights map={map} annotations={annotations} scrollElement={scroll} contentElement={content} />
        )}
      </div>
    </div>
  );
}

function textNodes(root: Node): Text[] {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  for (let node = walker.nextNode(); node; node = walker.nextNode()) nodes.push(node as Text);
  return nodes;
}

function selectRenderedText(root: HTMLElement, text: string, occurrence = 0): void {
  const nodes = textNodes(root);
  let total = "";
  for (const node of nodes) total += node.data;
  let start = -1;
  for (let i = 0; i <= occurrence; i++) start = total.indexOf(text, start + 1);
  if (start < 0) throw new Error(`rendered text unavailable: ${JSON.stringify(text)}`);
  const range = document.createRange();
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
  const selection = document.getSelection();
  if (!selection) throw new Error("selection unavailable");
  selection.removeAllRanges();
  selection.addRange(range);
}

function bands(scroll: HTMLElement): HTMLElement[] {
  return Array.from(scroll.querySelectorAll<HTMLElement>("[data-document-highlight]"));
}

function bandFor(scroll: HTMLElement, operationId: string): HTMLElement {
  const band = bands(scroll).find((candidate) => candidate.getAttribute("data-operation-id") === operationId);
  if (!band) throw new Error(`missing band for ${operationId}`);
  return band;
}

function noteTexts(scroll: HTMLElement): string[] {
  return Array.from(scroll.querySelectorAll("[data-annotation-note]")).map((node) => node.textContent ?? "");
}

interface RenderedReader {
  view: ReturnType<typeof render>;
  mapRef: { current: BoundDocumentMap | null };
  map: BoundDocumentMap;
  scroll: HTMLDivElement;
}

// Render a reader whose scroll owner reports `SCROLL_RECT`, configure the single
// FIRST-fragment geometry, and trigger the production re-measure through a real
// scroll event.
function renderReader(annotations: AnnotationView[], source = SOURCE): RenderedReader {
  const mapRef: { current: BoundDocumentMap | null } = { current: null };
  const view = render(<Reader source={source} annotations={annotations} mapRef={mapRef} />);
  onUnmount(() => view.unmount());
  const scroll = screen.getByTestId("scroll") as HTMLDivElement;
  geometry.setElementRect(scroll, SCROLL_RECT);
  const map = mapRef.current;
  if (!map) throw new Error("map not bound");
  return { view, mapRef, map, scroll };
}

const single = [annotationView("op-1", FIRST, "note")];

test("renders one non-text band per clipped fragment with the frozen attributes and operation identity", () => {
  const { view, map, scroll } = renderReader([
    annotationView("op-multi", { start: FIRST.start, end: SECOND.end }, "note"),
  ]);
  geometry.configure(map, { start: FIRST.start, end: SECOND.end }, [
    [{ left: 10.5, top: 6.25, right: 40.75, bottom: 16.5 }],
    [{ left: 10.5, top: 26.25, right: 40.75, bottom: 36.5 }],
  ]);
  fireEvent.scroll(scroll);

  const rendered = bands(scroll);
  expect(rendered).toHaveLength(2);
  for (const band of rendered) {
    expect(band.getAttribute("data-operation-id")).toBe("op-multi");
    // A band is a decoration: it carries no text of its own.
    expect(band.textContent).toBe("");
  }
  expect(rendered[0]?.style.left).toBe("10.5px");
  expect(rendered[0]?.style.top).toBe("6.25px");
  expect(rendered[0]?.style.width).toBe("30.25px");
  expect(rendered[0]?.style.height).toBe("10.25px");
  expect(rendered[1]?.style.top).toBe("26.25px");
  view.unmount();
});

test("does not change authored text, text-node parentage or the selection string, including through note reveal", () => {
  const { view, map, scroll } = renderReader(single);
  const prose = scroll.firstElementChild;
  if (!(prose instanceof HTMLElement)) throw new Error("missing prose root");
  const paragraph = prose.querySelector("p");
  if (!paragraph) throw new Error("missing paragraph");
  const textBefore = prose.textContent;
  const firstNode = paragraph.firstChild;
  if (!firstNode) throw new Error("missing authored text node");

  selectRenderedText(prose, "same");
  const selection = document.getSelection();
  if (!selection) throw new Error("selection unavailable");
  const selectedBefore = selection.toString();

  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);
  expect(bands(scroll)).toHaveLength(1);

  // Reveal the saved note and re-check the same invariants.
  fireEvent.click(bandFor(scroll, "op-1"));
  expect(noteTexts(scroll)).toEqual(["note"]);

  expect(prose.textContent).toBe(textBefore);
  expect(paragraph.firstChild).toBe(firstNode);
  expect(firstNode.parentNode).toBe(paragraph);
  expect(selection.rangeCount).toBe(1);
  expect(selection.toString()).toBe(selectedBefore);
  view.unmount();
});

test("reveals the literal inert note on hover, focus and click", () => {
  const note = "Keep this\n<script>window.pwned=1</script> /same";
  for (const interaction of ["mouseOver", "focus", "click"] as const) {
    const { view, map, scroll } = renderReader([annotationView("op-1", FIRST, note)]);
    geometry.configure(map, FIRST, [[FIRST_RECT]]);
    fireEvent.scroll(scroll);
    const band = bandFor(scroll, "op-1");

    if (interaction === "mouseOver") fireEvent.mouseOver(band);
    else if (interaction === "focus") {
      // Real focus only: the note must be revealed by focusing the element, not
      // by a synthetic focus dispatch.
      act(() => {
        band.focus();
      });
      expect(scroll.ownerDocument.activeElement).toBe(band);
      expect(band.tabIndex).toBeGreaterThanOrEqual(0);
    } else fireEvent.click(band);

    expect(noteTexts(scroll)).toEqual([note]);
    // The note renders literally, never as parsed markup.
    expect(document.querySelector("script")).toBeNull();
    view.unmount();
  }
});

// The revealed note must never trap the underlying prose: a repeated tap on
// the same band, a stationary tap outside every band, and focus leaving the
// bands and the revealed surface all dismiss it.
test("dismisses the revealed note on a repeated tap, an outside tap and focus departure", () => {
  const { view, map, scroll } = renderReader([annotationView("op-1", FIRST, "note")]);
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);
  const band = bandFor(scroll, "op-1");

  // Reveal, then a repeated tap on the same band toggles the note away.
  fireEvent.click(band);
  expect(noteTexts(scroll)).toEqual(["note"]);
  fireEvent.click(band);
  expect(noteTexts(scroll)).toEqual([]);

  // Reveal again; a tap on the prose outside every band clears the note so the
  // underlying text is reachable again.
  fireEvent.click(band);
  expect(noteTexts(scroll)).toEqual(["note"]);
  fireEvent.click(scroll, { clientX: 500, clientY: 500 });
  expect(noteTexts(scroll)).toEqual([]);

  // Reveal via real keyboard focus; focus departure dismisses.
  act(() => {
    band.focus();
  });
  expect(noteTexts(scroll)).toEqual(["note"]);
  act(() => {
    band.blur();
  });
  expect(noteTexts(scroll)).toEqual([]);
  view.unmount();
});

test("keeps overlapping notes reachable together for the touched passage across hover, focus and click", () => {
  const overlapping = [
    annotationView("op-a", FIRST, "note A"),
    annotationView("op-b", { start: FIRST.start, end: SECOND.end }, "note B"),
  ];
  for (const interaction of ["mouseOver", "focus", "click"] as const) {
    const { view, map, scroll } = renderReader(overlapping);
    geometry.configure(map, FIRST, [[FIRST_RECT]]);
    geometry.configure(map, { start: FIRST.start, end: SECOND.end }, [
      [{ left: 10, top: 10, right: 20, bottom: 20 }],
      [{ left: 10, top: 30, right: 20, bottom: 40 }],
    ]);
    fireEvent.scroll(scroll);

    const first = bandFor(scroll, "op-a");
    if (interaction === "mouseOver") fireEvent.mouseOver(first);
    else if (interaction === "focus") {
      act(() => {
        first.focus();
      });
      expect(scroll.ownerDocument.activeElement).toBe(first);
      expect(first.tabIndex).toBeGreaterThanOrEqual(0);
    } else fireEvent.click(first);
    expect([...noteTexts(scroll)].sort()).toEqual(["note A", "note B"]);

    // Touching the second operation keeps both reachable: neither permanently
    // hides the other.
    fireEvent.click(bandFor(scroll, "op-b"));
    expect([...noteTexts(scroll)].sort()).toEqual(["note A", "note B"]);
    view.unmount();
  }
});

test("recomputes bands after scroll and resize changes", () => {
  const { view, map, scroll } = renderReader(single);
  // Bands are positioned against the overlay, which jsdom cannot lay out; supply
  // its rectangle as the scroll owner's content origin, moving with the scroll.
  const overlay = scroll.lastElementChild;
  if (!(overlay instanceof HTMLElement)) throw new Error("missing positioned overlay");
  geometry.setElementRect(overlay, { left: 0, top: 0, right: 200, bottom: 100 });
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);
  expect(bandFor(scroll, "op-1").style.top).toBe("10px");

  // Scroll: the same rect moves up by the scroll offset while the clip holds.
  scroll.scrollTop = 40;
  geometry.setElementRect(overlay, { left: 0, top: -40, right: 200, bottom: 60 });
  fireEvent.scroll(scroll);
  expect(bandFor(scroll, "op-1").style.top).toBe("50px");

  // Viewport resize: the scroll owner's clip rect moves.
  geometry.setElementRect(scroll, { left: 5, top: 5, right: 205, bottom: 105 });
  geometry.setElementRect(overlay, { left: 5, top: -35, right: 205, bottom: 65 });
  fireEvent(window, new Event("resize"));
  expect(bandFor(scroll, "op-1").style.left).toBe("5px");
  expect(bandFor(scroll, "op-1").style.top).toBe("45px");
  view.unmount();
});

test("recomputes bands when the pane element itself changes size (no window or visualViewport event)", () => {
  const { observers, fire } = installResizeObserverCapability();
  const { view, map, scroll } = renderReader(single);
  geometry.configure(map, FIRST, [[{ left: 10, top: 10, right: 20, bottom: 20 }]]);
  fireEvent.scroll(scroll);
  expect(bandFor(scroll, "op-1").style.left).toBe("10px");

  // The pane element narrows on its own: no scroll, no window resize and no
  // font/visualViewport event fires. A pane-only reflow must still re-measure
  // the same verified ranges at their new geometry.
  expect(observers.length).toBeGreaterThan(0);
  expect(observers.some((observer) => observer.targets.includes(scroll))).toBe(true);
  geometry.configure(map, FIRST, [[{ left: 30, top: 10, right: 40, bottom: 20 }]]);
  act(() => {
    fire();
  });
  expect(bandFor(scroll, "op-1").style.left).toBe("30px");

  view.unmount();
  expect(observers.every((observer) => observer.disconnected)).toBe(true);
});

test("recomputes bands when the rendered content grows with no pane, scroll or viewport event", () => {
  const { observers } = installResizeObserverCapability();
  const mapRef: { current: BoundDocumentMap | null } = { current: null };
  const hostRef: { current: HTMLDivElement | null } = { current: null };
  const view = render(<NestedReader source={SOURCE} annotations={single} mapRef={mapRef} hostRef={hostRef} />);
  onUnmount(() => view.unmount());
  const scroll = screen.getByTestId("scroll") as HTMLDivElement;
  const host = hostRef.current;
  if (!host) throw new Error("markdown host not bound");
  const map = mapRef.current;
  if (!map) throw new Error("map not bound");
  geometry.setElementRect(scroll, NESTED_SCROLL_RECT);
  geometry.setElementRect(host, NESTED_HOST_RECT);
  const overlay = host.children[1];
  if (!(overlay instanceof HTMLElement)) throw new Error("positioned overlay is not an element");
  geometry.setElementRect(overlay, NESTED_HOST_RECT);
  geometry.configure(map, FIRST, [[NESTED_INSIDE_RECT]]);
  fireEvent.scroll(scroll);
  expect(bands(scroll)).toHaveLength(1);
  expect(bands(scroll)[0]?.style.left).toBe("16px");

  // Mermaid renders asynchronously: a diagram can grow the rendered content
  // after mount while the pane's own box, the viewport and the fonts are all
  // unchanged, so none of the existing observers fire. The content host's own
  // layout growth must re-measure the same verified ranges at their new
  // geometry, or saved highlights stay over the wrong passage until an
  // unrelated scroll or resize.
  const contentObserver = observers.find((observer) => !observer.disconnected && observer.targets.includes(host));
  expect(contentObserver).toBeTruthy();
  geometry.configure(map, FIRST, [
    [{ ...NESTED_INSIDE_RECT, left: NESTED_INSIDE_RECT.left + 12, top: NESTED_INSIDE_RECT.top + 8 }],
  ]);
  act(() => {
    contentObserver?.cb(
      [{ target: host } as unknown as ResizeObserverEntry],
      contentObserver as unknown as ResizeObserver,
    );
  });
  expect(bands(scroll)).toHaveLength(1);
  expect(bands(scroll)[0]?.style.left).toBe("28px");
  expect(bands(scroll)[0]?.style.top).toBe("34px");

  view.unmount();
  expect(observers.every((observer) => observer.disconnected)).toBe(true);
});

test("recomputes bands after a font-set load and visualViewport events", () => {
  const { fonts, viewport } = installFontAndViewportCapabilities();
  const { view, map, scroll } = renderReader(single);
  geometry.configure(map, FIRST, [[{ left: 10, top: 10, right: 20, bottom: 20 }]]);
  fireEvent.scroll(scroll);
  expect(bandFor(scroll, "op-1").style.left).toBe("10px");

  // A font change re-measures the same verified ranges at new geometry.
  geometry.configure(map, FIRST, [[{ left: 30, top: 10, right: 40, bottom: 20 }]]);
  act(() => {
    fonts.dispatchEvent(new Event("loadingdone"));
  });
  expect(bandFor(scroll, "op-1").style.left).toBe("30px");

  geometry.configure(map, FIRST, [[{ left: 50, top: 10, right: 60, bottom: 20 }]]);
  act(() => {
    viewport.dispatchEvent(new Event("resize"));
  });
  expect(bandFor(scroll, "op-1").style.left).toBe("50px");

  geometry.configure(map, FIRST, [[{ left: 70, top: 10, right: 80, bottom: 20 }]]);
  act(() => {
    viewport.dispatchEvent(new Event("scroll"));
  });
  expect(bandFor(scroll, "op-1").style.left).toBe("70px");

  view.unmount();
});

test("recomputes bands after a map replacement", () => {
  const mapRef: { current: BoundDocumentMap | null } = { current: null };
  const annotations = single;
  const view = render(<Reader source={SOURCE} annotations={annotations} mapRef={mapRef} />);
  onUnmount(() => view.unmount());
  const scroll = screen.getByTestId("scroll") as HTMLDivElement;
  geometry.setElementRect(scroll, SCROLL_RECT);
  const first = mapRef.current;
  if (!first) throw new Error("map not bound");
  geometry.configure(first, FIRST, [[{ left: 10, top: 10, right: 20, bottom: 20 }]]);
  fireEvent.scroll(scroll);
  expect(bandFor(scroll, "op-1").style.left).toBe("10px");

  view.rerender(
    <Reader
      source={SOURCE}
      annotations={annotations}
      identity={{ displayedSnapshotId: "snap-2", mappingId: "map-2" }}
      mapRef={mapRef}
    />,
  );
  const second = mapRef.current;
  if (!second) throw new Error("replacement map not bound");
  expect(second).not.toBe(first);
  geometry.configure(second, FIRST, [[{ left: 30, top: 30, right: 40, bottom: 40 }]]);
  fireEvent.scroll(scroll);
  expect(bandFor(scroll, "op-1").style.left).toBe("30px");
  expect(bandFor(scroll, "op-1").style.top).toBe("30px");
  view.unmount();
});

test("detaches its decorations on unmount and remeasures on a fresh remount", () => {
  const first = renderReader(single);
  geometry.configure(first.map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(first.scroll);
  expect(bands(first.scroll)).toHaveLength(1);
  const retainedCount = bands(first.scroll).length;

  first.view.unmount();

  // The retained host is disconnected from the live document, and the live
  // document exposes no highlight or note decorations.
  expect(first.scroll.isConnected).toBe(false);
  const detachedBands = bands(first.scroll);
  const detachedGeometry = detachedBands.map((band) => ({ top: band.style.top, left: band.style.left }));
  expect(document.querySelectorAll("[data-document-highlight]")).toHaveLength(0);
  expect(document.querySelectorAll("[data-annotation-note]")).toHaveLength(0);

  // Events after unmount neither reinsert connected decorations nor change the
  // detached bands' measured geometry.
  fireEvent.scroll(first.scroll);
  expect(document.querySelectorAll("[data-document-highlight]")).toHaveLength(0);
  expect(detachedBands.length).toBeLessThanOrEqual(retainedCount);
  expect(bands(first.scroll)).toHaveLength(detachedBands.length);
  expect(bands(first.scroll).map((band) => ({ top: band.style.top, left: band.style.left }))).toEqual(detachedGeometry);
  expect(bands(first.scroll).every((band) => !band.isConnected)).toBe(true);

  const second = renderReader(single);
  geometry.configure(second.map, FIRST, [[{ left: 60, top: 60, right: 70, bottom: 70 }]]);
  fireEvent.scroll(second.scroll);
  expect(bandFor(second.scroll, "op-1").style.left).toBe("60px");
  fireEvent.click(bandFor(second.scroll, "op-1"));
  expect(noteTexts(second.scroll)).toEqual(["note"]);
  second.view.unmount();
});

test("keeps bands connected and reveal working through StrictMode effect replay", () => {
  const mapRef: { current: BoundDocumentMap | null } = { current: null };
  const view = render(
    <StrictMode>
      <Reader source={SOURCE} annotations={single} mapRef={mapRef} />
    </StrictMode>,
  );
  onUnmount(() => view.unmount());
  const scroll = screen.getByTestId("scroll") as HTMLDivElement;
  geometry.setElementRect(scroll, SCROLL_RECT);
  const map = mapRef.current;
  if (!map) throw new Error("map not bound");
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);

  const rendered = bands(scroll);
  expect(rendered).toHaveLength(1);
  expect(rendered[0]?.isConnected).toBe(true);
  fireEvent.click(bandFor(scroll, "op-1"));
  expect(noteTexts(scroll)).toEqual(["note"]);
  view.unmount();
});

test("disposes its scroll, window, font and visualViewport listeners on unmount", () => {
  const { fonts, viewport } = installFontAndViewportCapabilities();
  const fontsAdd = vi.spyOn(fonts, "addEventListener");
  const fontsRemove = vi.spyOn(fonts, "removeEventListener");
  const viewportAdd = vi.spyOn(viewport, "addEventListener");
  const viewportRemove = vi.spyOn(viewport, "removeEventListener");
  const addElement = vi.spyOn(HTMLElement.prototype, "addEventListener");
  const removeElement = vi.spyOn(HTMLElement.prototype, "removeEventListener");
  const addWindow = vi.spyOn(window, "addEventListener");
  const removeWindow = vi.spyOn(window, "removeEventListener");

  const { view, map, scroll } = renderReader(single);
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);

  const elementRegistrations = addElement.mock.calls
    .map((call, index) => ({ target: addElement.mock.instances[index], type: call[0], listener: call[1] }))
    .filter((registration) => registration.target === scroll);
  const windowRegistrations = addWindow.mock.calls.map((call) => ({ type: call[0], listener: call[1] }));
  const fontRegistrations = fontsAdd.mock.calls.map((call) => ({ type: call[0], listener: call[1] }));
  const viewportRegistrations = viewportAdd.mock.calls.map((call) => ({ type: call[0], listener: call[1] }));
  expect(elementRegistrations.length).toBeGreaterThan(0);
  expect(windowRegistrations.length).toBeGreaterThan(0);
  expect(fontRegistrations.length).toBeGreaterThan(0);
  expect(viewportRegistrations.length).toBeGreaterThan(0);

  view.unmount();

  const removedOnElement = (registration: { type: string; listener: unknown }) =>
    removeElement.mock.calls.some(
      (call, index) =>
        removeElement.mock.instances[index] === scroll &&
        call[0] === registration.type &&
        call[1] === registration.listener,
    );
  for (const registration of elementRegistrations) expect(removedOnElement(registration)).toBe(true);
  for (const registration of windowRegistrations)
    expect(
      removeWindow.mock.calls.some((call) => call[0] === registration.type && call[1] === registration.listener),
    ).toBe(true);
  for (const registration of fontRegistrations)
    expect(
      fontsRemove.mock.calls.some((call) => call[0] === registration.type && call[1] === registration.listener),
    ).toBe(true);
  for (const registration of viewportRegistrations)
    expect(
      viewportRemove.mock.calls.some((call) => call[0] === registration.type && call[1] === registration.listener),
    ).toBe(true);
});

test("positions bands against the positioned markdown host, not the padded scroll owner", () => {
  const mapRef: { current: BoundDocumentMap | null } = { current: null };
  const hostRef: { current: HTMLDivElement | null } = { current: null };
  const view = render(<NestedReader source={SOURCE} annotations={single} mapRef={mapRef} hostRef={hostRef} />);
  onUnmount(() => view.unmount());
  const scroll = screen.getByTestId("scroll") as HTMLDivElement;
  const host = hostRef.current;
  if (!host) throw new Error("markdown host not bound");
  // The overlay is `inset: 0` in the host, so it is the band's containing block;
  // the host is the overlay's. Both are layout jsdom cannot compute, so the seam
  // supplies each rectangle, exactly as it supplies the scroll owner's.
  if (host.children.length !== 2)
    throw new Error(`positioned overlay not mounted: host has ${host.children.length} children`);
  const overlay = host.children[1];
  if (!(overlay instanceof HTMLElement)) throw new Error("positioned overlay is not an element");
  const map = mapRef.current;
  if (!map) throw new Error("map not bound");

  geometry.setElementRect(scroll, NESTED_SCROLL_RECT);
  geometry.setElementRect(host, NESTED_HOST_RECT);
  geometry.setElementRect(overlay, NESTED_HOST_RECT);
  geometry.configure(map, FIRST, [[NESTED_INSIDE_RECT, NESTED_CLIPPED_RECT]]);
  fireEvent.scroll(scroll);

  // The host sits 24px inside the scroll owner's rectangle, so each band's
  // inline left/top is the fragment's viewport edge MINUS the host edge; the
  // widths/heights are the scroll-clipped fragment sizes, unchanged.
  const placed = bands(scroll);
  expect(placed).toHaveLength(2);
  expect(placed[0]?.style.left).toBe("16px");
  expect(placed[0]?.style.top).toBe("26px");
  expect(placed[0]?.style.width).toBe("30px");
  expect(placed[0]?.style.height).toBe("15px");
  expect(placed[1]?.style.left).toBe("6px");
  expect(placed[1]?.style.top).toBe("31px");
  expect(placed[1]?.style.width).toBe("170px");
  expect(placed[1]?.style.height).toBe("45px");

  // Scroll the owner: the host and the fragments move together with the content,
  // so the band keeps the SAME position relative to the host. A patch that
  // caches the host rectangle once, or subtracts a constant, drifts here.
  scroll.scrollLeft = 4;
  scroll.scrollTop = 20;
  geometry.setElementRect(host, { left: 20, top: 4, right: 172, bottom: 56 });
  geometry.setElementRect(overlay, { left: 20, top: 4, right: 172, bottom: 56 });
  geometry.configure(map, FIRST, [
    [
      { left: 36, top: 30, right: 66, bottom: 45 },
      { left: 26, top: 35, right: 216, bottom: 110 },
    ],
  ]);
  fireEvent.scroll(scroll);

  const rescrolled = bands(scroll);
  expect(rescrolled).toHaveLength(2);
  expect(rescrolled[0]?.style.left).toBe("16px");
  expect(rescrolled[0]?.style.top).toBe("26px");
  expect(rescrolled[0]?.style.width).toBe("30px");
  expect(rescrolled[0]?.style.height).toBe("15px");
  expect(rescrolled[1]?.style.left).toBe("6px");
  expect(rescrolled[1]?.style.top).toBe("31px");
  expect(rescrolled[1]?.style.width).toBe("174px");
  expect(rescrolled[1]?.style.height).toBe("65px");

  view.unmount();
});

// ---------------------------------------------------------------------------
// Copyable pending reference (whole-UI finding 5). A saved operation whose
// captured recipient cannot be resolved stays pending, so its approved snapshot
// reference never reaches a draft. Revealing its note must expose the complete,
// independently literal reference as selectable/inert plain text, marked
// pending, WITHOUT folding it into the `[data-annotation-note]` wrapper that
// existing cases compare exactly. The reader-level proof that a real saved
// legacy-origin operation yields this is in DocPane.test.tsx.
// ---------------------------------------------------------------------------
test("reveals a complete copyable pending reference for a saved operation whose origin is unavailable", () => {
  const note = "Legacy note.";
  const { view, map, scroll } = renderReader([
    annotationView("op-legacy", FIRST, note, { state: "pending", reason: "owner-unavailable" }),
  ]);
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);

  // Nothing is exposed before the note is revealed.
  expect(scroll.querySelectorAll("[data-annotation-reference]")).toHaveLength(0);
  fireEvent.click(bandFor(scroll, "op-legacy"));

  // The note keeps rendering alone in its own wrapper; the reference is separate.
  expect(noteTexts(scroll)).toEqual([note]);
  const reference = scroll.querySelector<HTMLElement>("[data-annotation-reference]");
  expect(reference?.getAttribute("data-handoff-state")).toBe("pending");
  expect(reference?.textContent).toBe(
    'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Legacy note."\n' +
      'Captured whole-file revision: "rev-A".\n' +
      'Context before: ""; after: "".\n' +
      "Verify the quotation and context against current content before relying on these captured coordinates.",
  );
  // The reference renders as inert literal text, never parsed/active markup.
  expect(reference?.querySelector("a, script")).toBeNull();
  expect(scroll.ownerDocument.querySelector("script")).toBeNull();
  view.unmount();
});

test("interactions inside the copyable pending reference keep the revealed note and its selectable surface", () => {
  const note = "Legacy note.";
  const { view, map, scroll } = renderReader([
    annotationView("op-legacy", FIRST, note, { state: "pending", reason: "owner-unavailable" }),
  ]);
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);
  fireEvent.click(bandFor(scroll, "op-legacy"));
  const reference = scroll.querySelector<HTMLElement>("[data-annotation-reference]");
  if (!reference) throw new Error("pending reference not revealed");

  // A stationary press, its release and the trailing click inside the
  // reference belong to its own selectable surface: none of them dismisses
  // the revealed note or switches the active annotation, so the reference's
  // text can be selected and copied.
  act(() => {
    pointerEvent("pointerdown", reference, { pointerId: 31, clientX: 5, clientY: 250 });
    pointerEvent("pointerup", reference, { pointerId: 31, clientX: 5, clientY: 250, buttons: 0 });
  });
  fireEvent.click(reference, { clientX: 5, clientY: 250 });
  const retained = scroll.querySelector<HTMLElement>("[data-annotation-reference]");
  expect(retained).not.toBeNull();
  expect(noteTexts(scroll)).toEqual([note]);
  view.unmount();
});

// Synthetic component event coverage. These dispatch the pointer events a real
// browser would deliver to the component in jsdom; they are NOT trusted
// real-browser input and are labelled as synthetic here. The restored component
// reveals a note on a completed `click` (and on hover/focus). The cases below
// separate a cancelled press, an armed pointer A followed by an unrelated
// pointer B release, and a matching A down/up lifecycle, using DISTINCT concrete
// pointer IDs so pointer identity is actually exercised. These are protection
// tests for the forthcoming interaction design, not a TDD claim for any
// discarded implementation.
function pointerEvent(
  type: "pointerdown" | "pointerup" | "pointermove" | "pointercancel",
  target: EventTarget,
  init: PointerEventInit = {},
): boolean {
  return target.dispatchEvent(
    new PointerEvent(type, {
      bubbles: true,
      cancelable: true,
      composed: true,
      button: 0,
      buttons: 1,
      pointerId: 1,
      pointerType: "mouse",
      isPrimary: true,
      ...init,
    }),
  );
}

test("separates a cancelled press, an armed A with an unrelated B release, and a matching A lifecycle (synthetic component events)", () => {
  const { view, map, scroll } = renderReader(single);
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);
  const band = bandFor(scroll, "op-1");
  expect(noteTexts(scroll)).toEqual([]);

  // 1. A press that the browser cancels (no completed click) reveals nothing.
  act(() => {
    pointerEvent("pointerdown", band, { pointerId: 11, clientX: 30, clientY: 20 });
    pointerEvent("pointercancel", band, { pointerId: 11, clientX: 30, clientY: 20 });
  });
  expect(noteTexts(scroll)).toEqual([]);

  // 2. Pointer A arms the band, then an UNRELATED pointer B releases: nothing is
  //    revealed before B's release and nothing after it.
  act(() => {
    pointerEvent("pointerdown", band, { pointerId: 21, clientX: 30, clientY: 20 });
  });
  expect(noteTexts(scroll)).toEqual([]);
  act(() => {
    pointerEvent("pointerup", band, { pointerId: 22, clientX: 30, clientY: 20, buttons: 0 });
  });
  expect(noteTexts(scroll)).toEqual([]);

  // 3. A matching A down/up lifecycle reveals the note. A real browser fires the
  //    `click` after that sequence; jsdom does not synthesize it, so the click is
  //    dispatched explicitly (labelled) while the down/up lifecycle is retained.
  act(() => {
    pointerEvent("pointerdown", band, { pointerId: 31, clientX: 30, clientY: 20 });
    pointerEvent("pointerup", band, { pointerId: 31, clientX: 30, clientY: 20, buttons: 0 });
    band.click(); // the browser's post-up click, dispatched explicitly in jsdom
  });
  expect(noteTexts(scroll)).toEqual(["note"]);
  view.unmount();
});

test("preserves the browser selection through a drag that reveals no note (synthetic component events)", () => {
  const { view, map, scroll } = renderReader(single);
  const prose = scroll.firstElementChild;
  if (!(prose instanceof HTMLElement)) throw new Error("missing prose root");
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);
  const band = bandFor(scroll, "op-1");

  selectRenderedText(prose, "same");
  const selection = document.getSelection();
  if (!selection) throw new Error("selection unavailable");
  const selectedBefore = selection.toString();

  act(() => {
    pointerEvent("pointerdown", band, { pointerId: 41, clientX: 30, clientY: 20 });
    pointerEvent("pointermove", band, { pointerId: 41, clientX: 60, clientY: 60, buttons: 1 });
    pointerEvent("pointerup", band, { pointerId: 41, clientX: 60, clientY: 60, buttons: 0 });
  });

  expect(noteTexts(scroll)).toEqual([]);
  expect(selection.rangeCount).toBe(1);
  expect(selection.toString()).toBe(selectedBefore);
  view.unmount();
});

// The restored component resolves note reveal by pointer COORDINATES against the
// saved band rectangles, so an ordinary hover or click whose target is the PROSE
// beneath a (non-hit-testable) band still reveals the note. These are the missing
// behavior tests: against the accepted target-only activation they fail, because
// a prose target has no `[data-document-highlight]` ancestor to resolve.
test("reveals a saved band by hover and click coordinates when the pointer targets the prose beneath it", () => {
  const { view, map, scroll } = renderReader(single);
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);
  const prose = scroll.firstElementChild;
  if (!(prose instanceof HTMLElement)) throw new Error("missing prose root");
  const band = bandFor(scroll, "op-1");
  geometry.setElementRect(band, { left: 10, top: 10, right: 50, bottom: 30 });

  expect(noteTexts(scroll)).toEqual([]);
  // A hover outside the band's rectangle reveals nothing.
  fireEvent.mouseOver(prose, { clientX: 5, clientY: 5 });
  expect(noteTexts(scroll)).toEqual([]);
  // A hover whose coordinates fall inside the band reveals the note.
  fireEvent.mouseOver(prose, { clientX: 30, clientY: 20 });
  expect(noteTexts(scroll)).toEqual(["note"]);
  view.unmount();

  // A click whose TARGET is the prose but whose coordinates fall inside the band
  // also reveals (the band itself is not hit-testable).
  const second = renderReader(single);
  geometry.configure(second.map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(second.scroll);
  const prose2 = second.scroll.firstElementChild;
  if (!(prose2 instanceof HTMLElement)) throw new Error("missing prose root");
  geometry.setElementRect(bandFor(second.scroll, "op-1"), { left: 10, top: 10, right: 50, bottom: 30 });
  expect(noteTexts(second.scroll)).toEqual([]);
  fireEvent.click(prose2, { clientX: 30, clientY: 20 });
  expect(noteTexts(second.scroll)).toEqual(["note"]);
  second.view.unmount();
});

// A popped-out document renders in its OWN window, whose MouseEvent and Node
// are different constructors from this module's globals. The coordinate
// reveal must still work there: an instanceof check against the global
// constructors sees a foreign-realm event as nothing, and hovering the prose
// beneath a band could never reveal the note.
test("reveals a saved band by hover coordinates inside a popped-out document's own window", () => {
  const frame = document.createElement("iframe");
  document.body.append(frame);
  onUnmount(() => frame.remove());
  const owner = frame.contentDocument;
  const ownerWindow = frame.contentWindow;
  if (!owner || !ownerWindow) throw new Error("missing iframe document");
  expect(ownerWindow.MouseEvent).not.toBe(MouseEvent);
  geometry.installWindow(ownerWindow);

  const host = owner.createElement("div");
  owner.body.append(host);
  const mapRef: { current: BoundDocumentMap | null } = { current: null };
  const view = render(<Reader source={SOURCE} annotations={single} mapRef={mapRef} />, { container: host });
  onUnmount(() => view.unmount());
  const scroll = host.querySelector('[data-testid="scroll"]');
  // Realm-safe: the scroll owner belongs to the iframe's document, so the
  // global HTMLElement cannot see it.
  if (!(scroll instanceof ownerWindow.HTMLElement)) throw new Error("missing scroll owner");
  geometry.setElementRect(scroll, SCROLL_RECT);
  const map = mapRef.current;
  if (!map) throw new Error("map not bound");
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);
  const prose = scroll.firstElementChild;
  if (!(prose instanceof ownerWindow.HTMLElement)) throw new Error("missing prose root");
  geometry.setElementRect(bandFor(scroll, "op-1"), { left: 10, top: 10, right: 50, bottom: 30 });

  expect(noteTexts(scroll)).toEqual([]);
  act(() => {
    prose.dispatchEvent(
      new ownerWindow.MouseEvent("mouseover", { bubbles: true, cancelable: true, clientX: 30, clientY: 20 }),
    );
  });
  expect(noteTexts(scroll)).toEqual(["note"]);
  view.unmount();
});

// ---------------------------------------------------------------------------
// Discriminating diagnostics for the REJECTED candidate's new pointer and wheel
// arbitration. These are SYNTHETIC jsdom cases and TESTS-AFTER, not the missing
// pre-implementation RED. They pin the measured band rectangle and target the
// PROSE (the band is non-hit-testable), so they isolate the coordinate path
// instead of an already-revealing `band.click()`. A matching pointer-up without
// a compatibility click diagnoses the candidate helper's own behavior; it is NOT
// a new product requirement. The real browser remains the scrolling/Copy
// authority; the scroll seam below represents only layout jsdom cannot compute.
// ---------------------------------------------------------------------------

// A scroll-position seam: jsdom lays nothing out and keeps `scrollTop` at 0, so
// this represents the one piece of layout it cannot compute. It asserts the
// component's arbitration (did it move the outer stack and cancel the event),
// not a fake scroll engine.
function pinScrollable(element: HTMLElement, initial = 0): void {
  const before = Object.getOwnPropertyDescriptor(element, "scrollTop");
  let value = initial;
  Object.defineProperty(element, "scrollTop", {
    configurable: true,
    get: () => value,
    set: (next: number) => {
      value = next;
    },
  });
  onRestore(() => {
    if (before) Object.defineProperty(element, "scrollTop", before);
    else Reflect.deleteProperty(element, "scrollTop");
  });
}

interface PinnedReader {
  view: ReturnType<typeof render>;
  mapRef: { current: BoundDocumentMap | null };
  scroll: HTMLDivElement;
  prose: HTMLElement;
  band: HTMLElement;
}

// A fresh reader whose band rectangle is pinned and whose pointer target is the
// prose beneath it.
function pinnedProseReader(): PinnedReader {
  const { view, mapRef, map, scroll } = renderReader(single);
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);
  const prose = scroll.firstElementChild;
  if (!(prose instanceof HTMLElement)) throw new Error("missing prose root");
  const band = bandFor(scroll, "op-1");
  geometry.setElementRect(band, { left: 10, top: 10, right: 50, bottom: 30 });
  return { view, mapRef, scroll, prose, band };
}

test("isolates matching pointer-up reveal from cancel, unrelated pointer, Shift and drag (synthetic, tests-after)", () => {
  // Matching down/up over the band's rectangle, with NO click: the candidate
  // helper reveals on its own pointer-up.
  {
    const { view, scroll, prose } = pinnedProseReader();
    expect(noteTexts(scroll)).toEqual([]);
    act(() => {
      pointerEvent("pointerdown", prose, { pointerId: 51, clientX: 30, clientY: 20 });
      pointerEvent("pointerup", prose, { pointerId: 51, clientX: 30, clientY: 20, buttons: 0 });
    });
    expect(noteTexts(scroll)).toEqual(["note"]);
    view.unmount();
  }
  // Cancellation of the arming pointer reveals nothing.
  {
    const { view, scroll, prose } = pinnedProseReader();
    act(() => {
      pointerEvent("pointerdown", prose, { pointerId: 61, clientX: 30, clientY: 20 });
      pointerEvent("pointercancel", prose, { pointerId: 61, clientX: 30, clientY: 20 });
    });
    expect(noteTexts(scroll)).toEqual([]);
    view.unmount();
  }
  // An unrelated pointer's release reveals nothing (the arming id must match).
  {
    const { view, scroll, prose } = pinnedProseReader();
    act(() => {
      pointerEvent("pointerdown", prose, { pointerId: 71, clientX: 30, clientY: 20 });
    });
    expect(noteTexts(scroll)).toEqual([]);
    act(() => {
      pointerEvent("pointerup", prose, { pointerId: 72, clientX: 30, clientY: 20, buttons: 0 });
    });
    expect(noteTexts(scroll)).toEqual([]);
    view.unmount();
  }
  // A Shift press is a selection adjustment, never a note tap.
  {
    const { view, scroll, prose } = pinnedProseReader();
    act(() => {
      pointerEvent("pointerdown", prose, { pointerId: 81, clientX: 30, clientY: 20, shiftKey: true });
      pointerEvent("pointerup", prose, { pointerId: 81, clientX: 30, clientY: 20, buttons: 0, shiftKey: true });
    });
    expect(noteTexts(scroll)).toEqual([]);
    view.unmount();
  }
  // A drag (movement past the tap threshold) reveals nothing.
  {
    const { view, scroll, prose } = pinnedProseReader();
    act(() => {
      pointerEvent("pointerdown", prose, { pointerId: 91, clientX: 30, clientY: 20 });
      pointerEvent("pointermove", prose, { pointerId: 91, clientX: 30, clientY: 60, buttons: 1 });
      pointerEvent("pointerup", prose, { pointerId: 91, clientX: 30, clientY: 60, buttons: 0 });
    });
    expect(noteTexts(scroll)).toEqual([]);
    view.unmount();
  }
});

// RoboRev round 32 low: a Shift-click is a selection adjustment, and the tap
// machinery already refuses Shift presses - but the click path's toggle ran
// unconditionally, so extending a text selection across a band revealed or
// dismissed a saved note. The trailing click of a Shift press carries the same
// modifier: both the selection and the note's visibility must stay untouched.
test("a Shift-click extends the selection without revealing or dismissing a note", () => {
  // Extending a selection over an unrevealed band reveals nothing.
  {
    const { view, scroll, prose } = pinnedProseReader();
    expect(noteTexts(scroll)).toEqual([]);
    act(() => {
      pointerEvent("pointerdown", prose, { pointerId: 95, clientX: 30, clientY: 20, shiftKey: true });
      pointerEvent("pointerup", prose, { pointerId: 95, clientX: 30, clientY: 20, buttons: 0, shiftKey: true });
      fireEvent.click(prose, { clientX: 30, clientY: 20, shiftKey: true });
    });
    expect(noteTexts(scroll)).toEqual([]);
    view.unmount();
  }
  // Extending a selection over a revealed note's band leaves the note
  // visible: the press never armed a tap, so its trailing click cannot
  // dismiss either.
  {
    const { view, scroll, prose, band } = pinnedProseReader();
    fireEvent.click(band);
    expect(noteTexts(scroll)).toEqual(["note"]);
    act(() => {
      pointerEvent("pointerdown", prose, { pointerId: 96, clientX: 30, clientY: 20, shiftKey: true });
      pointerEvent("pointerup", prose, { pointerId: 96, clientX: 30, clientY: 20, buttons: 0, shiftKey: true });
      fireEvent.click(prose, { clientX: 30, clientY: 20, shiftKey: true });
    });
    expect(noteTexts(scroll)).toEqual(["note"]);
    view.unmount();
  }
});

// RoboRev round 34 low: a selection drag reveals nothing, but the browser
// fires a compatibility click at the drag's release point after pointerup.
// That trailing click reached activate and toggled the note it landed on,
// bypassing the movement guard - the synthetic drag tests omitted it. A drag
// consumes its own trailing click the way a tap does; keyboard-activated
// clicks keep the full toggle path.
test("a selection drag's trailing click neither reveals nor dismisses a note", () => {
  // A drag across an unrevealed band reveals nothing, even though its
  // release lands on the band and the browser clicks there.
  {
    const { view, scroll, prose } = pinnedProseReader();
    expect(noteTexts(scroll)).toEqual([]);
    act(() => {
      pointerEvent("pointerdown", prose, { pointerId: 97, clientX: 30, clientY: 20 });
      pointerEvent("pointermove", prose, { pointerId: 97, clientX: 38, clientY: 20, buttons: 1 });
      pointerEvent("pointerup", prose, { pointerId: 97, clientX: 38, clientY: 20, buttons: 0 });
      fireEvent.click(prose, { clientX: 38, clientY: 20 });
    });
    expect(noteTexts(scroll)).toEqual([]);
    view.unmount();
  }
  // A drag across a revealed note's band leaves the note visible: the drag
  // is a selection gesture, and its trailing click cannot dismiss either.
  {
    const { view, scroll, prose, band } = pinnedProseReader();
    fireEvent.click(band);
    expect(noteTexts(scroll)).toEqual(["note"]);
    act(() => {
      pointerEvent("pointerdown", prose, { pointerId: 98, clientX: 30, clientY: 20 });
      pointerEvent("pointermove", prose, { pointerId: 98, clientX: 38, clientY: 20, buttons: 1 });
      pointerEvent("pointerup", prose, { pointerId: 98, clientX: 38, clientY: 20, buttons: 0 });
      fireEvent.click(prose, { clientX: 38, clientY: 20 });
    });
    expect(noteTexts(scroll)).toEqual(["note"]);
    view.unmount();
  }
});

// RoboRev round 36 low: the overlay's gesture state lived inside the effect
// that rebinds on every map or annotations change, so a revalidation landing
// mid-drag - a peer annotation arriving while the user selects - rebound the
// pointer handlers over empty gesture state. The fresh handlers ignored the
// armed drag's release, recorded no consumed click, and the browser's trailing
// click at the release point toggled the note it happened to land on. The
// gesture must survive the rebind: the drag keeps its armed pointer, its
// movement marks it a selection, and its trailing click is consumed.
test("a mid-drag revalidation keeps the armed drag so its trailing click reveals nothing", () => {
  const { view, mapRef, scroll, prose } = pinnedProseReader();
  expect(noteTexts(scroll)).toEqual([]);
  act(() => {
    pointerEvent("pointerdown", prose, { pointerId: 99, clientX: 30, clientY: 20 });
  });
  // A revalidation lands mid-drag: the annotations prop changes identity and
  // the overlay effect rebinds its handlers while the pointer is still down.
  act(() => {
    view.rerender(<Reader source={SOURCE} annotations={[annotationView("op-1", FIRST, "note")]} mapRef={mapRef} />);
  });
  // The drag moves past the tap threshold and releases over the band; the
  // browser fires its compatibility click at the release point.
  act(() => {
    pointerEvent("pointermove", prose, { pointerId: 99, clientX: 38, clientY: 20, buttons: 1 });
    pointerEvent("pointerup", prose, { pointerId: 99, clientX: 38, clientY: 20, buttons: 0 });
    fireEvent.click(prose, { clientX: 38, clientY: 20 });
  });
  expect(noteTexts(scroll)).toEqual([]);
  view.unmount();
});

// A reader displaying a revealed pending reference whose stack is pinned and
// scrollable, so the wheel arbitration can be observed without a live browser.
function pendingReferenceReader(): {
  view: ReturnType<typeof render>;
  scroll: HTMLDivElement;
  stack: HTMLElement;
  reference: HTMLElement;
} {
  const { view, map, scroll } = renderReader([
    annotationView("op-legacy", FIRST, "Legacy note.", { state: "pending", reason: "owner-unavailable" }),
  ]);
  geometry.configure(map, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scroll);
  const band = bandFor(scroll, "op-legacy");
  geometry.setElementRect(band, { left: 10, top: 10, right: 50, bottom: 30 });
  fireEvent.click(band);
  const reference = scroll.querySelector<HTMLElement>("[data-annotation-reference]");
  if (!reference) throw new Error("the pending reference was not revealed");
  const stack = reference.parentElement;
  if (!(stack instanceof HTMLElement)) throw new Error("the note stack is missing");
  geometry.setElementRect(stack, { left: 0, top: 0, right: 120, bottom: 60 });
  pinScrollable(stack, 0);
  return { view, scroll, stack, reference };
}

// Each wheel-arbitration observation runs in its own case, so a failing first
// assertion can never hide the others. Synthetic, tests-after; the scroll seam
// represents only layout jsdom cannot compute.
test("a wheel over the interactive pending reference does not move the outer stack (synthetic, tests-after)", () => {
  const { view, stack, reference } = pendingReferenceReader();
  fireEvent.wheel(reference, { clientX: 30, clientY: 20, deltaY: 40 });
  expect(stack.scrollTop).toBe(0);
  view.unmount();
});

test("a wheel over the interactive pending reference is not cancelled by the outer forwarding (synthetic, tests-after)", () => {
  const { view, reference } = pendingReferenceReader();
  const notPrevented = fireEvent.wheel(reference, { clientX: 30, clientY: 20, deltaY: 40 });
  expect(notPrevented).toBe(true);
  view.unmount();
});

test("a wheel over the non-interactive stack region is forwarded to the outer stack and cancelled (synthetic, tests-after)", () => {
  const { view, scroll, stack } = pendingReferenceReader();
  const notPrevented = fireEvent.wheel(scroll, { clientX: 30, clientY: 20, deltaY: 40 });
  expect(stack.scrollTop).toBe(40);
  expect(notPrevented).toBe(false);
  view.unmount();
});
