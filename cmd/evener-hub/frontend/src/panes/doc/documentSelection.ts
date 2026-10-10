// Finished-selection detection for the browser reader (Task 5).
//
// Observes the root's OWNING document and window (never a global), so a
// selection made inside a different browsing context (an iframe document) is
// read from that context's own Selection. A finished gesture belongs to the
// mapped root and to its recorded pointer: a press that begins outside the root
// never arms, and only the arming pointer's release finishes or abandons the
// interaction. A keyboard completion needs an actual selection change during
// the interaction, so pressing Shift without extending the selection does not
// reopen a retained passage. Nothing here calls preventDefault, so the browser's
// own selection, Copy, focus and handles stay usable.
import type { SourceRange } from "@evener/appwire-client";
import type { BoundDocumentMap } from "../../widgets/markdown/documentSourceMap";

export interface FinishedDocumentSelection {
  displayedSnapshotId: string;
  mappingId: string;
  sourceRange: SourceRange;
  selectedRenderedText: string;
}

export interface ObserveFinishedDocumentSelectionsInput {
  root: HTMLElement;
  map: BoundDocumentMap;
  onFinish(capture: FinishedDocumentSelection): void;
}

export function observeFinishedDocumentSelections(input: ObserveFinishedDocumentSelectionsInput): () => void {
  const { root, map, onFinish } = input;
  const doc = root.ownerDocument;
  const ownerWindow = doc.defaultView;
  let activePointer: number | null = null;
  let shiftActive = false;
  let shiftBaseline: SourceRange | null = null;
  let disposed = false;

  // The current verified nonempty mapped range, or null for an unmapped,
  // collapsed or stale selection.
  const mappedRange = (): SourceRange | null => {
    const selection = doc.getSelection();
    if (!selection) return null;
    const capture = map.capture(selection);
    if (capture.state !== "mapped" || capture.sourceRange.end <= capture.sourceRange.start) return null;
    return capture.sourceRange;
  };

  const emit = (selection: Selection): void => {
    const capture = map.capture(selection);
    if (capture.state !== "mapped" || capture.sourceRange.end <= capture.sourceRange.start) return;
    onFinish({
      displayedSnapshotId: map.identity.displayedSnapshotId,
      mappingId: map.identity.mappingId,
      sourceRange: capture.sourceRange,
      selectedRenderedText: capture.selectedRenderedText,
    });
  };
  const finish = (): void => {
    if (disposed) return;
    const selection = doc.getSelection();
    if (selection) emit(selection);
  };

  // Realm-safe containment: an iframe root's nodes are instances of that
  // document's own Node constructor.
  const withinRoot = (target: EventTarget | null): boolean => {
    if (!target) return false;
    const NodeCtor = ownerWindow?.Node;
    const isNode = NodeCtor ? target instanceof NodeCtor : target instanceof Node;
    return isNode && root.contains(target as Node);
  };

  const onPointerDown = (event: PointerEvent): void => {
    if (disposed) return;
    if (event.isPrimary === false) return;
    if (event.button !== 0) return;
    if (!withinRoot(event.target)) return;
    activePointer = event.pointerId;
  };
  const onPointerUp = (event: PointerEvent): void => {
    if (disposed || activePointer === null || event.pointerId !== activePointer) return;
    activePointer = null;
    finish();
  };
  const onPointerCancel = (event: PointerEvent): void => {
    if (activePointer === null || event.pointerId !== activePointer) return;
    activePointer = null;
  };
  const onKeyDown = (event: KeyboardEvent): void => {
    if (disposed || event.key !== "Shift" || shiftActive) return;
    shiftActive = true;
    shiftBaseline = mappedRange();
  };
  const onKeyUp = (event: KeyboardEvent): void => {
    if (disposed || event.key !== "Shift" || !shiftActive) return;
    shiftActive = false;
    const baseline = shiftBaseline;
    shiftBaseline = null;
    const current = mappedRange();
    if (!current) return;
    // A keyboard completion needs the selection to have actually changed during
    // the interaction; the identical range is not a new passage, even when the
    // same rendered word is selected at a different source range.
    if (baseline && baseline.start === current.start && baseline.end === current.end) return;
    finish();
  };
  // Registered so a selection change is observed without ever completing a
  // passage on its own: touch-handle adjustment must stay usable.
  const onSelectionChange = (): void => {};

  doc.addEventListener("pointerdown", onPointerDown);
  doc.addEventListener("pointerup", onPointerUp);
  doc.addEventListener("pointercancel", onPointerCancel);
  doc.addEventListener("selectionchange", onSelectionChange);
  doc.addEventListener("keydown", onKeyDown);
  doc.addEventListener("keyup", onKeyUp);

  return () => {
    disposed = true;
    activePointer = null;
    shiftActive = false;
    shiftBaseline = null;
    doc.removeEventListener("pointerdown", onPointerDown);
    doc.removeEventListener("pointerup", onPointerUp);
    doc.removeEventListener("pointercancel", onPointerCancel);
    doc.removeEventListener("selectionchange", onSelectionChange);
    doc.removeEventListener("keydown", onKeyDown);
    doc.removeEventListener("keyup", onKeyUp);
  };
}
