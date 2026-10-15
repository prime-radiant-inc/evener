// Finished-selection detection for the browser reader (Task 5).
//
// Observes the root's OWNING document and window (never a global), so a
// selection made inside a different browsing context (an iframe document) is
// read from that context's own Selection. A finished gesture belongs to the
// root's mount and to its recorded pointer: a press that begins outside the
// root never arms, and only the arming pointer's release finishes or abandons
// the interaction. A cancelled pointer stream hands the gesture to the OS: the
// native selection it leaves behind completes once it stops changing, so
// long-press selection and handle adjustments still open the comment box. The subscription lives for the mount, not for a render:
// revalidation reads rebind the map without cancelling an armed gesture, and
// the release reads the map current at that moment, so a selection over
// content that changed underneath it maps to nothing and emits no capture. A
// keyboard completion needs an actual selection change during
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
  // The map is read at EVENT time, not subscription time: revalidations and
  // re-binds must not cancel an armed gesture. Null when no accepted map is
  // installed for the current display, which captures nothing.
  getMap(): BoundDocumentMap | null;
  onFinish(capture: FinishedDocumentSelection): void;
}

export function observeFinishedDocumentSelections(input: ObserveFinishedDocumentSelectionsInput): () => void {
  const { root, getMap, onFinish } = input;
  const doc = root.ownerDocument;
  const ownerWindow = doc.defaultView;
  let activePointer: number | null = null;
  let shiftActive = false;
  let shiftBaseline: SourceRange | null = null;
  // The OS-owned selection a cancelled pointer stream leaves behind. The
  // baseline is the range the cancel interrupted; a settle that lands on the
  // identical range re-opens nothing (the same rule the keyboard path keeps).
  let nativeBaseline: SourceRange | null | undefined;
  let nativeTimer: ReturnType<typeof setTimeout> | null = null;
  let disposed = false;

  // How long the native selection must stop changing before it counts as
  // settled: handle adjustments arrive as a selectionchange burst, and each
  // one re-defers the settle so the passage completes once when they stop.
  const NATIVE_SETTLE_MS = 250;

  const clearNativeSettle = (): void => {
    nativeBaseline = undefined;
    if (nativeTimer !== null) {
      clearTimeout(nativeTimer);
      nativeTimer = null;
    }
  };

  const armNativeSettle = (): void => {
    if (nativeTimer !== null) clearTimeout(nativeTimer);
    nativeTimer = setTimeout(() => {
      nativeTimer = null;
      if (disposed) return;
      const baseline = nativeBaseline;
      nativeBaseline = undefined;
      const current = mappedRange();
      if (!current) return;
      if (baseline && baseline.start === current.start && baseline.end === current.end) return;
      finish();
    }, NATIVE_SETTLE_MS);
  };

  // The current verified nonempty mapped range, or null for an unmapped,
  // collapsed or stale selection.
  const mappedRange = (): SourceRange | null => {
    const map = getMap();
    const selection = doc.getSelection();
    if (!map || !selection) return null;
    const capture = map.capture(selection);
    if (capture.state !== "mapped" || capture.sourceRange.end <= capture.sourceRange.start) return null;
    return capture.sourceRange;
  };

  const emit = (selection: Selection): void => {
    const map = getMap();
    if (!map) return;
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
    // A new press owns the interaction: any settle the previous gesture's
    // cancellation left pending must not complete behind the new one.
    clearNativeSettle();
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
    // The OS took the gesture over for its own selection: completing or
    // adjusting that selection arrives as selectionchange with no pointer
    // behind it, so the first such change arms the settle that completes it
    // once it stops changing.
    nativeBaseline = mappedRange();
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
  // A selection change alone never completes a passage: while a press is
  // active its own release owns completion. The one exception is the OS-owned
  // selection a cancelled stream left behind - there each change re-defers
  // the armed settle, so native handle adjustments stay usable and the
  // passage completes once they stop.
  const onSelectionChange = (): void => {
    if (disposed || activePointer !== null || nativeBaseline === undefined) return;
    armNativeSettle();
  };

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
    clearNativeSettle();
    doc.removeEventListener("pointerdown", onPointerDown);
    doc.removeEventListener("pointerup", onPointerUp);
    doc.removeEventListener("pointercancel", onPointerCancel);
    doc.removeEventListener("selectionchange", onSelectionChange);
    doc.removeEventListener("keydown", onKeyDown);
    doc.removeEventListener("keyup", onKeyUp);
  };
}
