// Browser-verification mount for the small annotation component cases.
//
// WHAT THIS PROVES THAT jsdom CANNOT: the saved-highlight overlay's geometry,
// the note stack's real scrolling and the browser's own selection/Copy behavior
// all depend on real layout and real input. This page mounts the PRODUCTION
// scaffold, markdown renderer, source binding, saved-highlight overlay and
// comment controls with explicit fixture annotations, and exposes a small set
// of pure DOM observation helpers for the runner. All input is driven over CDP
// (genuine mouse, keyboard and emulated touch) by
// scripts/documentannotationguard/components.mjs; nothing here simulates a
// scheduler, mocks product logic or injects synthetic events.
//
// The mount source strings mirror scripts/documentannotationguard/component-fixtures.mjs.
// The runner asserts the page rendered the fixture's own literal text, so a
// drift between the two fails loudly rather than passing silently.
import {
  type AcceptedSourceLease,
  type AnnotationCommentWork,
  type AnnotationView,
  type CapturedPassage,
  type DocumentKey,
  displayedFingerprint,
  type PreparedAnnotation,
  type SourceRange,
  sourcePositions,
} from "@evener/appwire-client";
import { useCallback, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { DocumentCommentBox } from "../panes/doc/DocumentCommentBox";
import { DocumentHighlights } from "../panes/doc/DocumentHighlights";
import { PaneScaffold } from "../widgets";
import { DocumentMarkdown } from "../widgets/markdown/DocumentMarkdown";
import type { BoundDocumentMap } from "../widgets/markdown/documentSourceMap";
import "../styles/tokens.css";
import "../styles/global.css";

declare global {
  interface Window {
    __annotationGuardReady?: boolean;
    __annotationGuard?: {
      caseRect(id: string): { left: number; top: number; width: number; height: number } | null;
      describeLiteral(
        id: string,
        literal: string,
        start: number,
        end: number,
      ): {
        nodeData: string;
        text: string;
        rects: GuardRect[];
        clipped: GuardRect[];
        clip: GuardRect;
        ownerScrollTop: number;
        ownerScrollLeft: number;
      } | null;
      scrollState(id: string): { scrollTop: number; scrollHeight: number; clientHeight: number } | null;
      samplePixels(base64: string, width: number, height: number, points: GuardPoint[]): Promise<GuardPixel[]>;
      analyzePaint(
        base64: string,
        width: number,
        height: number,
        fragments: GuardRect[],
        control: GuardRect,
        tolerance: number,
      ): Promise<GuardPaintAnalysis>;
      visibleNotePoint(id: string): GuardPoint | null;
    };
    __guardWheels?: { trusted: boolean; defaultPrevented: boolean; deltaY: number }[];
  }
}

interface GuardRect {
  left: number;
  top: number;
  right: number;
  bottom: number;
}
interface GuardPoint {
  x: number;
  y: number;
}
interface GuardPixel extends GuardPoint {
  r: number;
  g: number;
  b: number;
  a: number;
}
interface GuardFragmentAnalysis {
  left: number;
  top: number;
  right: number;
  bottom: number;
  expectedExtentLeft: number;
  expectedExtentRight: number;
  paintedFraction: number;
  extentLeft: number | null;
  extentRight: number | null;
  covered: boolean;
  inkFraction: number;
  paintedBackground: number[];
}
interface GuardPaintAnalysis {
  controlInk: number;
  controlBackground: number[];
  fragments: GuardFragmentAnalysis[];
}

// --- fixture mount inputs (mirror component-fixtures.mjs) -------------------

const DOC_KEY: DocumentKey = {
  namespace: { kind: "browser", origin: "https://example.test" },
  sessionRef: "guard",
  path: "NOTES.md",
};

const PANE_SOURCE =
  "Alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu pi rho.\n\n" +
  "Second paragraph keeps unrelated words entirely separate from the band.\n";
const PANE_SELECTED = "gamma delta epsilon zeta eta";

const NESTED_TARGET = "Nested target passage words here";
const NESTED_SOURCE =
  `${NESTED_TARGET} and trailing words on the same line.\n\n` +
  Array.from(
    { length: 26 },
    (_, index) => `Filler paragraph ${index + 1} keeps the document body long enough to scroll on its own.`,
  ).join("\n\n") +
  "\n";
const NESTED_NOTES = [
  Array.from(
    { length: 16 },
    (_, index) => `Long nested reference line ${index + 1} keeps the pending reference taller than its own scroll cap.`,
  ).join("\n"),
  Array.from(
    { length: 14 },
    (_, index) => `Second overlapping note line ${index + 1} adds to the revealed stack height.`,
  ).join("\n"),
  Array.from(
    { length: 14 },
    (_, index) => `Third overlapping note line ${index + 1} adds to the revealed stack height.`,
  ).join("\n"),
];

const SELECTION_SOURCE =
  "Selection alpha beta gamma delta epsilon zeta theta.\n\nSecond paragraph plain text for the selection case.\n";

const OVERLAP_SOURCE =
  "Overlap alpha beta gamma delta epsilon passage.\n\n" +
  Array.from(
    { length: 18 },
    (_, index) => `Overlap filler paragraph ${index + 1} keeps the reading body scrollable under the fixed viewport.`,
  ).join("\n\n") +
  "\n";
const OVERLAP_NOTES = ["Overlapping note one.", "Overlapping note two."];

const COMMENT_SOURCE = "Comment control source passage with enough words to stay visible.\n\nAnother paragraph.\n";
const COMMENT_PASSAGE =
  "This capture passage is deliberately long so the comment box has to wrap it instead of growing a horizontal scrollbar.";
const COMMENT_NOTE =
  "A deliberately long saved note.\nIt has several lines.\nEach line must stay literally preserved in the editor.";
const COMMENT_SAVED_NOTE = [
  "A long saved note that must wrap inside the revealed stack.",
  `One unbroken token: ${"W".repeat(120)}`,
  "A fourth line.",
  "A fifth line so the note is tall as well as wide.",
].join("\n");

function rangeOf(source: string, selected: string): SourceRange {
  const start = source.indexOf(selected);
  if (start < 0) throw new Error(`guard fixture passage not found: ${JSON.stringify(selected)}`);
  return { start, end: start + selected.length };
}

function makeCapture(source: string, range: SourceRange, selected: string): CapturedPassage {
  const positions = sourcePositions(source, range);
  return {
    evidence: { wholeRevision: "rev-guard", fingerprint: displayedFingerprint(source, false) },
    sourceRange: range,
    start: positions.start,
    endInclusive: positions.endInclusive,
    selectedRenderedText: selected,
    contextBefore: "",
    contextAfter: "",
    mappingId: "map-guard",
    displayedSnapshotId: "snap-guard",
  };
}

function makeView(opId: string, source: string, range: SourceRange, note: string, pending: boolean): AnnotationView {
  const capture = makeCapture(source, range, source.slice(range.start, range.end));
  const lease: AcceptedSourceLease = {
    key: DOC_KEY,
    namespaceIncarnation: "inc-guard",
    epoch: "epoch-guard",
    headRevision: 1,
    displayedSnapshotId: "snap-guard",
  };
  const operation: PreparedAnnotation = {
    operationId: opId,
    lease,
    capture,
    note,
    destination: { kind: "browser-restored-session" },
  };
  return {
    operation,
    receipt: { operationId: opId, epoch: "epoch-guard", sessionSequence: 1 },
    handoff: pending ? { state: "pending", reason: "owner-unavailable" } : { state: "applied", draftRevision: 1 },
  };
}

interface CaseSpec {
  id: string;
  source: string;
  width: number;
  height?: number;
  annotations: AnnotationView[];
  comment?: { passage: string; note: string };
}

const PANE_VIEW = [
  makeView(
    "pane-1",
    PANE_SOURCE,
    rangeOf(PANE_SOURCE, PANE_SELECTED),
    "Pane resize keeps the saved highlight painted.",
    false,
  ),
];
const NESTED_RANGE = rangeOf(NESTED_SOURCE, NESTED_TARGET);
const NESTED_VIEW = NESTED_NOTES.map((note, index) =>
  makeView(
    `nested-${index + 1}`,
    NESTED_SOURCE,
    { start: NESTED_RANGE.start + index, end: NESTED_RANGE.end - index },
    note,
    true,
  ),
);
const SELECTION_VIEW = [
  makeView(
    "selection-band",
    SELECTION_SOURCE,
    rangeOf(SELECTION_SOURCE, "beta gamma"),
    "Saved band crossed by the selection.",
    false,
  ),
];
const OVERLAP_RANGE = rangeOf(OVERLAP_SOURCE, "alpha beta gamma delta");
const OVERLAP_VIEW = OVERLAP_NOTES.map((note, index) =>
  makeView(
    `overlap-${index + 1}`,
    OVERLAP_SOURCE,
    { start: OVERLAP_RANGE.start, end: OVERLAP_RANGE.end - index * 6 },
    note,
    false,
  ),
);
const COMMENT_VIEW = [
  makeView("comment-note", COMMENT_SOURCE, rangeOf(COMMENT_SOURCE, "source passage"), COMMENT_SAVED_NOTE, false),
];

const CASE_SPECS: CaseSpec[] = [
  { id: "pane-resize", source: PANE_SOURCE, width: 760, annotations: PANE_VIEW },
  { id: "nested-scroll", source: NESTED_SOURCE, width: 760, height: 700, annotations: NESTED_VIEW },
  { id: "selection", source: SELECTION_SOURCE, width: 760, annotations: SELECTION_VIEW },
  { id: "overlap-access", source: OVERLAP_SOURCE, width: 760, annotations: OVERLAP_VIEW },
  {
    id: "comment-controls",
    source: COMMENT_SOURCE,
    width: 760,
    height: 640,
    annotations: COMMENT_VIEW,
    comment: { passage: COMMENT_PASSAGE, note: COMMENT_NOTE },
  },
];

function GuardReader({ spec, onBound }: { spec: CaseSpec; onBound: (id: string) => void }) {
  const [map, setMap] = useState<BoundDocumentMap | null>(null);
  const [scroll, setScroll] = useState<HTMLElement | null>(null);
  const [work, setWork] = useState<AnnotationCommentWork>({ note: spec.comment?.note ?? "", captureFrozen: false });
  const identity = useMemo(() => ({ displayedSnapshotId: `snap-${spec.id}`, mappingId: `map-${spec.id}` }), [spec.id]);
  const bindHost = useCallback((node: HTMLDivElement | null) => {
    setScroll(node?.parentElement ?? null);
  }, []);
  const onMap = useCallback(
    (value: BoundDocumentMap | null) => {
      setMap(value);
      if (value) onBound(spec.id);
    },
    [onBound, spec.id],
  );
  const capture = useMemo<CapturedPassage | undefined>(() => {
    if (!spec.comment) return undefined;
    // The comment box only reads `capture.selectedRenderedText`; the stored
    // source range must still be a valid slice of the fixture source.
    const range = rangeOf(spec.source, spec.source.split("\n\n")[0] ?? spec.source);
    return makeCapture(spec.source, range, spec.comment.passage);
  }, [spec]);

  const footer = spec.comment ? (
    <DocumentCommentBox
      work={{ ...work, capture }}
      onNote={(note) => setWork((current) => ({ ...current, note, captureFrozen: true }))}
      onSend={() => {}}
      onCancel={() => setWork({ note: "", captureFrozen: false })}
      saving={false}
      status="unsaved"
    />
  ) : undefined;

  return (
    <PaneScaffold title={`${spec.id}.md`} scaffoldMarker="doc-reader-scroll" focused={false} footer={footer}>
      <div ref={bindHost} style={{ position: "relative" }}>
        <DocumentMarkdown source={spec.source} identity={identity} onMap={onMap} />
        {map && scroll && <DocumentHighlights map={map} annotations={spec.annotations} scrollElement={scroll} />}
      </div>
    </PaneScaffold>
  );
}

// --- pure DOM observation helpers (no product logic) ------------------------

function caseElement(id: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(`[data-case="${id}"]`);
}
function ownerElement(id: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(`[data-case="${id}"] [data-pane-scaffold]`);
}
function textNodeWith(data: string): Text | null {
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if ((node as Text).data === data) return node as Text;
  }
  return null;
}
function toRect(rect: DOMRect | { left: number; top: number; right: number; bottom: number }): GuardRect {
  return { left: rect.left, top: rect.top, right: rect.right, bottom: rect.bottom };
}

// Implementation-neutral paint analysis: decode the case screenshot and, per
// browser text-range fragment, measure the PAINTED EXTENT along a strip just
// below the fragment's top edge (above the glyphs) and the fraction of
// GLYPH-ROW pixels that are ink against the fragment's own painted background.
// It reads no CSS highlight registry and no DOM band, so a DOM band and a later
// native paint are judged identically.
async function analyzePaintPixels(
  base64: string,
  width: number,
  height: number,
  fragments: GuardRect[],
  control: GuardRect,
  tolerance: number,
): Promise<GuardPaintAnalysis> {
  const image = new Image();
  image.src = `data:image/png;base64,${base64}`;
  await image.decode();
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  const context = canvas.getContext("2d");
  if (!context) throw new Error("guard: no 2d context for paint analysis");
  context.drawImage(image, 0, 0);

  const clampX = (x: number): number => Math.max(0, Math.min(width - 1, Math.round(x)));
  const clampY = (y: number): number => Math.max(0, Math.min(height - 1, Math.round(y)));
  const pixel = (x: number, y: number): number[] => {
    const data = context.getImageData(clampX(x), clampY(y), 1, 1).data;
    return [data[0] ?? 0, data[1] ?? 0, data[2] ?? 0];
  };
  const diff = (a: number[], b: number[]): number =>
    Math.max(
      Math.abs((a[0] ?? 0) - (b[0] ?? 0)),
      Math.abs((a[1] ?? 0) - (b[1] ?? 0)),
      Math.abs((a[2] ?? 0) - (b[2] ?? 0)),
    );
  const luminance = (p: number[]): number => 0.2126 * (p[0] ?? 0) + 0.7152 * (p[1] ?? 0) + 0.0722 * (p[2] ?? 0);
  // Ink fraction over the glyph rows, sampling two pixels in from each edge so
  // a band's own left/right border is never mistaken for a glyph.
  const inkFraction = (rect: GuardRect, background: number[]): number => {
    const y0 = Math.round(rect.top + (rect.bottom - rect.top) * 0.35);
    const y1 = Math.round(rect.top + (rect.bottom - rect.top) * 0.65);
    let ink = 0;
    let total = 0;
    for (let y = y0; y <= y1; y += 1) {
      for (let x = Math.ceil(rect.left) + 2; x <= Math.floor(rect.right) - 2; x += 1) {
        total += 1;
        if (luminance(background) - luminance(pixel(x, y)) > 40) ink += 1;
      }
    }
    return total > 0 ? ink / total : 0;
  };

  const controlBackground = pixel((control.left + control.right) / 2, control.top + 2);
  const controlInk = inkFraction(control, controlBackground);
  const analyzed = fragments.map((rect) => {
    const stripY = rect.top + 2;
    const background = pixel((rect.left + rect.right) / 2, stripY);
    // The DOM Range is [left, right): its right edge is exclusive. Raster
    // extents are inclusive integer pixel columns, so the independently
    // expected last painted column is ceil(right) - 1. A full-row scan keeps a
    // large overshoot observable instead of clipping it at a small margin that
    // could be mistaken for an in-tolerance edge.
    const expectedExtentLeft = Math.max(0, Math.floor(rect.left));
    const expectedExtentRight = Math.min(width - 1, Math.ceil(rect.right) - 1);
    const runs: { left: number; right: number }[] = [];
    let runLeft: number | null = null;
    let painted = 0;
    for (let x = 0; x < width; x += 1) {
      const isPainted = diff(pixel(x, stripY), controlBackground) >= tolerance;
      if (x >= expectedExtentLeft && x <= expectedExtentRight && isPainted) painted += 1;
      if (isPainted && runLeft === null) runLeft = x;
      if (!isPainted && runLeft !== null) {
        runs.push({ left: runLeft, right: x - 1 });
        runLeft = null;
      }
    }
    if (runLeft !== null) runs.push({ left: runLeft, right: width - 1 });
    const overlapping = runs
      .map((run) => ({
        ...run,
        overlap: Math.max(0, Math.min(run.right, expectedExtentRight) - Math.max(run.left, expectedExtentLeft) + 1),
      }))
      .filter((run) => run.overlap > 0)
      .sort((a, b) => b.overlap - a.overlap);
    const observed = overlapping[0] ?? null;
    const extentLeft = observed?.left ?? null;
    const extentRight = observed?.right ?? null;
    const covered =
      extentLeft !== null &&
      extentRight !== null &&
      Math.abs(extentLeft - expectedExtentLeft) <= 1 &&
      Math.abs(extentRight - expectedExtentRight) <= 1;
    return {
      left: rect.left,
      top: rect.top,
      right: rect.right,
      bottom: rect.bottom,
      expectedExtentLeft,
      expectedExtentRight,
      paintedFraction:
        expectedExtentRight >= expectedExtentLeft ? painted / (expectedExtentRight - expectedExtentLeft + 1) : 0,
      extentLeft,
      extentRight,
      covered,
      inkFraction: extentLeft !== null ? inkFraction(rect, background) : 0,
      paintedBackground: background,
    };
  });
  return { controlInk, controlBackground, fragments: analyzed };
}

// A point over the currently-visible part of a note (never a reference), so a
// reachability wheel targets the outer stack's own non-interactive content as
// the content scrolls. Recomputed each call; no scrollIntoView.
function visibleNotePoint(id: string): GuardPoint | null {
  const stack = document.querySelector(`[data-case="${id}"] [data-annotation-reference]`)?.parentElement;
  if (!stack) return null;
  const sr = stack.getBoundingClientRect();
  const notes = [...stack.querySelectorAll("[data-annotation-note]")];
  for (const note of notes) {
    const r = note.getBoundingClientRect();
    const top = Math.max(r.top, sr.top);
    const bottom = Math.min(r.bottom, sr.bottom);
    if (bottom - top >= 8) {
      const x = Math.max(sr.left + 4, Math.min(sr.right - 4, (r.left + r.right) / 2));
      return { x, y: top + 6 };
    }
  }
  return { x: (sr.left + sr.right) / 2, y: sr.top + 6 };
}

const helpers = {
  caseRect(id: string) {
    const element = caseElement(id);
    if (!element) return null;
    const rect = element.getBoundingClientRect();
    return { left: rect.left, top: rect.top, width: rect.width, height: rect.height };
  },
  describeLiteral(id: string, literal: string, start: number, end: number) {
    const owner = ownerElement(id);
    if (!owner) return null;
    const node = textNodeWith(literal);
    if (!node || !owner.contains(node)) return null;
    const range = document.createRange();
    range.setStart(node, start);
    range.setEnd(node, end);
    const rects = [...range.getClientRects()].filter((rect) => rect.width > 0 && rect.height > 0).map(toRect);
    const clip = toRect(owner.getBoundingClientRect());
    const clipped = rects
      .map((rect) => ({
        left: Math.max(rect.left, clip.left),
        top: Math.max(rect.top, clip.top),
        right: Math.min(rect.right, clip.right),
        bottom: Math.min(rect.bottom, clip.bottom),
      }))
      .filter((rect) => rect.right > rect.left && rect.bottom > rect.top);
    return {
      nodeData: node.data,
      text: range.toString(),
      rects,
      clipped,
      clip,
      ownerScrollTop: owner.scrollTop,
      ownerScrollLeft: owner.scrollLeft,
    };
  },
  scrollState(id: string) {
    const owner = ownerElement(id);
    if (!owner) return null;
    return { scrollTop: owner.scrollTop, scrollHeight: owner.scrollHeight, clientHeight: owner.clientHeight };
  },
  async samplePixels(base64: string, width: number, height: number, points: GuardPoint[]) {
    const image = new Image();
    image.src = `data:image/png;base64,${base64}`;
    await image.decode();
    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;
    const context = canvas.getContext("2d");
    if (!context) throw new Error("guard: no 2d context for pixel sampling");
    context.drawImage(image, 0, 0);
    return points.map((point) => {
      const x = Math.max(0, Math.min(width - 1, Math.round(point.x)));
      const y = Math.max(0, Math.min(height - 1, Math.round(point.y)));
      const data = context.getImageData(x, y, 1, 1).data;
      return { x: point.x, y: point.y, r: data[0] ?? 0, g: data[1] ?? 0, b: data[2] ?? 0, a: data[3] ?? 0 };
    });
  },
  analyzePaint(
    base64: string,
    width: number,
    height: number,
    fragments: GuardRect[],
    control: GuardRect,
    tolerance: number,
  ) {
    return analyzePaintPixels(base64, width, height, fragments, control, tolerance);
  },
  visibleNotePoint,
};
window.__annotationGuard = helpers;

// Observe the wheel's default action as the page delivers it. Bubble phase, so
// a product handler that calls preventDefault on the target's ancestor is seen.
window.__guardWheels = [];
document.addEventListener(
  "wheel",
  (event) => {
    window.__guardWheels?.push({
      trusted: event.isTrusted,
      defaultPrevented: event.defaultPrevented,
      deltaY: event.deltaY,
    });
  },
  { passive: true },
);

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("documentannotationguard.html is missing #root");

// Readiness means the PRODUCTION tree committed and every case's real source
// map is bound - not that a render was requested. The flag is set from the map
// callbacks, so a slow commit cannot turn the first case into a false
// missing-passage failure.
function App() {
  const bound = useRef<Set<string>>(new Set());
  const markBound = useCallback((id: string) => {
    if (bound.current.has(id)) return;
    bound.current.add(id);
    if (bound.current.size >= CASE_SPECS.length) window.__annotationGuardReady = true;
  }, []);
  return (
    <main>
      {CASE_SPECS.map((spec) => (
        <section
          key={spec.id}
          data-case={spec.id}
          style={{
            width: spec.width,
            height: spec.height ?? 320,
            margin: 8,
            border: "1px solid #555",
            display: "block",
          }}
        >
          <GuardReader spec={spec} onBound={markBound} />
        </section>
      ))}
    </main>
  );
}

createRoot(rootEl).render(<App />);
