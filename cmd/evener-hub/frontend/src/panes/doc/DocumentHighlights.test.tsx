// Tests-first checkpoint for the saved-highlight overlay component (Task 5).
//
// `./DocumentHighlights` is the production module this checkpoint precedes, so
// the static import below deliberately does NOT resolve yet: the file is a
// collection/setup failure until the source grant lands, and `tsc` reports the
// missing module instead of this file hiding it behind a stub. The approved
// props are `map`, `annotations`, `scrollElement`.
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
      setScroll(node?.parentElement ?? null);
    },
    [hostRef],
  );
  return (
    <div data-testid="scroll">
      <div ref={bindHost} data-testid="host">
        <DocumentMarkdown source={source} identity={identity} onMap={onMap} />
        {map && scroll && <DocumentHighlights map={map} annotations={annotations} scrollElement={scroll} />}
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
