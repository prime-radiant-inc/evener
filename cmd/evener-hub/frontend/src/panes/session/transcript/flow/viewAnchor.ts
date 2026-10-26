import type { RefObject } from "react";
import type { VirtualListHandle } from "../../../../widgets";
import type { TranscriptReadView } from "../transcriptReadView";
import { isAtBottom, readScrollMetrics, type ScrollMetrics } from "./scrollMetrics";
import type { CapturedTranscriptView } from "./transcriptViewRegistry";

export interface ViewAnchorPosition {
  id: string;
  /** Item position in the unfiltered transcript; shared across every representation. */
  sourceIndex: number;
  /** VirtualList row containing this entry. */
  index: number;
  /** Row top relative to the scroll viewport top. */
  offset: number;
  /** Measured entry height; used to identify content crossing the viewport top. */
  height?: number;
  /** User/agent content survives every focused representation. */
  isMessage: boolean;
  /** For a folded tool run (toolRuns.ts): the entry ids it stands in for. A
   * capture on any of them resolves to this anchor. */
  members?: readonly string[];
}

export interface ViewAnchor {
  id: string;
  sourceIndex: number;
  offset: number;
  isMessage: boolean;
}

export interface RestoredViewAnchor {
  id: string;
  index: number;
  offset: number;
}

export function captureTopAnchor(position: ViewAnchorPosition): ViewAnchor {
  return {
    id: position.id,
    sourceIndex: position.sourceIndex,
    offset: position.offset,
    isMessage: position.isMessage,
  };
}

// The rendered position that stands for a captured anchor's source item. One
// source item wears a different id per view - intent:<id> in Intent, <id> in
// Tools/Full, tools:<id>:… for a summary, run:<id> once it folds - so after
// the literal id, match the source identity (sourceIdentity strips the view
// prefixes) with the source index agreeing where the capture carries one,
// then a folded run whose members carry the item (roborev on PR #947).
export function positionForAnchor(
  anchor: { id: string; sourceIndex?: number },
  positions: readonly ViewAnchorPosition[],
): ViewAnchorPosition | undefined {
  const exact = positions.find((position) => position.id === anchor.id);
  if (exact) return exact;
  const identity = sourceIdentity(anchor.id);
  const sameSource = positions.find(
    (position) =>
      sourceIdentity(position.id) === identity &&
      (anchor.sourceIndex === undefined || position.sourceIndex === anchor.sourceIndex),
  );
  if (sameSource) return sameSource;
  return positions.find((position) =>
    position.members?.some((member) => member === anchor.id || sourceIdentity(member) === identity),
  );
}

export function restoreTopAnchor(
  anchor: ViewAnchor,
  positions: readonly ViewAnchorPosition[],
): RestoredViewAnchor | undefined {
  const same = positionForAnchor(anchor, positions);
  if (same) return { id: same.id, index: same.index, offset: anchor.offset };

  const nearest = positions
    .filter((position) => position.isMessage)
    .sort((a, b) => {
      const distance = Math.abs(a.sourceIndex - anchor.sourceIndex) - Math.abs(b.sourceIndex - anchor.sourceIndex);
      // Equal-distance ties resolve to the preceding message, keeping the
      // content the reader just passed rather than skipping forward.
      return distance || a.sourceIndex - b.sourceIndex;
    })[0];
  return nearest ? { id: nearest.id, index: nearest.index, offset: anchor.offset } : undefined;
}

export function readAnchorPositions(el: HTMLElement): ViewAnchorPosition[] {
  const viewportTop = el.getBoundingClientRect().top;
  return Array.from(el.querySelectorAll<HTMLElement>("[data-view-anchor-id]")).map((row, renderedIndex) => {
    const rect = (closedGroupSummary(row) ?? row).getBoundingClientRect();
    const sourceIndex = Number(row.dataset.viewAnchorSourceIndex ?? renderedIndex);
    const members = row.dataset.viewAnchorMembers;
    return {
      id: row.dataset.viewAnchorId ?? "",
      sourceIndex,
      index: Number(row.dataset.viewAnchorIndex ?? sourceIndex),
      offset: rect.top - viewportTop,
      height: rect.height,
      isMessage: row.dataset.viewAnchorMessage === "true",
      ...(members ? { members: members.split(",") } : {}),
    };
  });
}

export function topVisiblePosition(positions: readonly ViewAnchorPosition[]): ViewAnchorPosition | undefined {
  // Overscan entries are rendered above the viewport. The anchor is the entry
  // whose measured box actually crosses the viewport top, not the first DOM
  // entry and not merely the first entry whose top happens to be nonnegative.
  const crossing = positions
    .filter((position) => position.offset <= 0 && position.offset + (position.height ?? 0) > 0)
    .sort((a, b) => b.offset - a.offset)[0];
  if (crossing) return crossing;
  return positions.filter((position) => position.offset >= 0).sort((a, b) => a.offset - b.offset)[0];
}

interface CapturedFocusMetadata {
  readonly anchorId: string;
  readonly sourceIdentity: string;
  readonly sourceIndex?: number;
  readonly descendantPath: readonly number[];
  readonly element: HTMLElement;
}

const capturedAnchorMetadata = new WeakMap<CapturedTranscriptView, ViewAnchor>();
const capturedFocusMetadata = new WeakMap<CapturedTranscriptView, CapturedFocusMetadata>();
export const retainedPlacements = new WeakMap<HTMLElement, { logicalRef?: string; captured: CapturedTranscriptView }>();

function sourceIdentity(id: string): string {
  if (id.startsWith("intent:")) return id.slice("intent:".length);
  // A folded tool run's anchor is its first entry's id under the run prefix
  // (toolRuns.ts): a focus captured on that entry before the turn settled
  // still resolves to the run it folded into.
  if (id.startsWith("run:")) return id.slice("run:".length);
  if (id.startsWith("tools:")) return id.slice("tools:".length).split(":")[0] ?? id;
  return id;
}

function descendantPath(root: HTMLElement, element: HTMLElement): readonly number[] {
  const path: number[] = [];
  let current: HTMLElement = element;
  while (current !== root) {
    const parent = current.parentElement;
    if (!parent) return [];
    const index = Array.from(parent.children).indexOf(current);
    if (index < 0) return [];
    path.unshift(index);
    current = parent;
  }
  return path;
}

function descendantAtPath(root: HTMLElement, path: readonly number[]): HTMLElement | undefined {
  let current: HTMLElement = root;
  for (const index of path) {
    const next = current.children[index];
    if (!next) return undefined;
    if (!(next instanceof HTMLElement)) return undefined;
    current = next;
  }
  return current;
}

function isFocusableDescendant(element: HTMLElement): boolean {
  return element.matches("button, a[href], input, select, textarea, [tabindex]");
}

function sourceIndexFromDataset(element: HTMLElement): number | undefined {
  const value = Number(element.dataset.viewAnchorSourceIndex);
  return Number.isSafeInteger(value) ? value : undefined;
}

function focusMetadataFor(el: HTMLElement): CapturedFocusMetadata | undefined {
  const active = el.ownerDocument.activeElement;
  if (!(active instanceof HTMLElement)) return undefined;
  const anchor = Array.from(el.querySelectorAll<HTMLElement>("[data-view-anchor-id]")).find(
    (candidate) => candidate === active || candidate.contains(active),
  );
  const anchorId = anchor?.dataset.viewAnchorId;
  if (!anchor || !anchorId) return undefined;
  return {
    anchorId,
    sourceIdentity: sourceIdentity(anchorId),
    sourceIndex: sourceIndexFromDataset(anchor),
    descendantPath: descendantPath(anchor, active),
    element: active,
  };
}

/** Capture the state that must survive a projected transcript replacement. */
export function captureTranscriptView(
  el: HTMLElement,
  measure: (element: HTMLElement) => ScrollMetrics = readScrollMetrics,
  measureAnchors: (element: HTMLElement) => ViewAnchorPosition[] = readAnchorPositions,
  positioningRevision?: number,
): CapturedTranscriptView {
  const metrics = measure(el);
  const scrollable = Math.max(0, metrics.scrollHeight - metrics.clientHeight);
  const firstVisible = topVisiblePosition(measureAnchors(el));
  const focusMetadata = focusMetadataFor(el);
  const captured: CapturedTranscriptView = {
    anchorId: firstVisible?.id,
    anchorOffset: firstVisible?.offset ?? 0,
    normalizedOffset: scrollable > 0 ? metrics.scrollTop / scrollable : 0,
    followingBottom: isAtBottom(metrics),
    focusedEntryId: focusMetadata?.anchorId,
    ...(positioningRevision !== undefined ? { positioningRevision } : {}),
    ...(firstVisible?.height && firstVisible.height > 0 && el.clientWidth > 0 && el.clientHeight > 0
      ? {
          readingPoint: {
            entryHeight: firstVisible.height,
            viewportHeight: el.clientHeight,
            viewportWidth: el.clientWidth,
          },
        }
      : {}),
  };
  if (firstVisible) capturedAnchorMetadata.set(captured, captureTopAnchor(firstVisible));
  if (focusMetadata) capturedFocusMetadata.set(captured, focusMetadata);
  return captured;
}

export function captureTranscriptFocus(el: HTMLElement, captured: CapturedTranscriptView): CapturedTranscriptView {
  const focusMetadata = focusMetadataFor(el);
  // Pending geometry preserves the reading point, but focus can move in the rendered view.
  const next = { ...captured, focusedEntryId: focusMetadata?.anchorId };
  const anchorMetadata = capturedAnchorMetadata.get(captured);
  if (anchorMetadata) capturedAnchorMetadata.set(next, anchorMetadata);
  if (focusMetadata) capturedFocusMetadata.set(next, focusMetadata);
  return next;
}

export function readingPointOffset(
  captured: CapturedTranscriptView,
  entryHeight: number,
  viewportHeight: number,
  viewportWidth: number,
): number {
  const point = captured.readingPoint;
  if (!point || captured.anchorOffset > 0) return captured.anchorOffset;
  // A viewport-height-only change leaves the entry as it was, so the reading
  // line stays exactly where it was. An entry that changed height at the same
  // width (a disclosure, say) still takes the bounded rules below.
  if (point.viewportWidth === viewportWidth && point.entryHeight === entryHeight) return captured.anchorOffset;
  const oldDepth = Math.max(0, point.entryHeight - point.viewportHeight);
  if (-captured.anchorOffset > oldDepth) {
    // The entry's top is above the viewport and its tail is showing, so the
    // reader is reading where it ends: keep that end where it was rather than
    // filling the pane with the entry.
    const tail = point.entryHeight + captured.anchorOffset;
    return Math.min(0, tail - entryHeight);
  }
  const progress = oldDepth === 0 ? 0 : -captured.anchorOffset / oldDepth;
  const newDepth = Math.max(0, entryHeight - viewportHeight);
  return progress === 0 || newDepth === 0 ? 0 : -progress * newDepth;
}

export function anchorFromCapture(
  captured: CapturedTranscriptView,
  candidates: readonly ViewAnchorPosition[],
): ViewAnchor | undefined {
  const metadata = capturedAnchorMetadata.get(captured);
  if (metadata) return metadata;
  if (captured.anchorId === undefined) return undefined;
  const source = positionForAnchor({ id: captured.anchorId }, candidates);
  if (!source) return undefined;
  return captureTopAnchor({ ...source, offset: captured.anchorOffset });
}

// A folded run's anchor answers for every entry it folded (its members): a
// focus captured on the third call of a run that has since folded restores to
// the run - its summary when closed, the row itself when open.
function membersMatch(members: readonly string[] | undefined, metadata: CapturedFocusMetadata): boolean {
  return (
    members?.some((member) => member === metadata.anchorId || sourceIdentity(member) === metadata.sourceIdentity) ??
    false
  );
}

function focusCandidateMatches(candidate: ViewAnchorPosition, metadata: CapturedFocusMetadata): boolean {
  if (candidate.id === metadata.anchorId) return true;
  if (membersMatch(candidate.members, metadata)) return true;
  if (sourceIdentity(candidate.id) !== metadata.sourceIdentity) return false;
  return metadata.sourceIndex === undefined || candidate.sourceIndex === metadata.sourceIndex;
}

function focusNodeMatches(candidate: HTMLElement, metadata: CapturedFocusMetadata): boolean {
  if (candidate.dataset.viewAnchorId === metadata.anchorId) return true;
  if (membersMatch(candidate.dataset.viewAnchorMembers?.split(","), metadata)) return true;
  if (sourceIdentity(candidate.dataset.viewAnchorId ?? "") !== metadata.sourceIdentity) return false;
  const sourceIndex = sourceIndexFromDataset(candidate);
  return metadata.sourceIndex === undefined || sourceIndex === undefined || sourceIndex === metadata.sourceIndex;
}

// A closed disclosure that owns the anchor: a folded tool run and an intent
// group each wrap their own <details> in one anchor that lists its members
// (TurnBlock's foldedAnchorFor), and their entries mount only while open, so
// while closed the summary is the thing to measure and focus.
function closedGroupSummary(anchor: HTMLElement): HTMLElement | undefined {
  const summary = anchor.querySelector(
    ':scope > details:is([data-testid="tool-run"], [data-testid="intent-group"]):not([open]) > summary',
  );
  return summary instanceof HTMLElement ? summary : undefined;
}

function focusAnchor(anchor: HTMLElement, metadata: CapturedFocusMetadata): boolean {
  const summary = closedGroupSummary(anchor);
  if (summary) {
    summary.focus();
    if (summary.ownerDocument.activeElement === summary) return true;
  }
  if (metadata.element.isConnected && anchor.contains(metadata.element)) {
    metadata.element.focus();
    if (metadata.element.ownerDocument.activeElement === metadata.element) return true;
  }
  const descendant = descendantAtPath(anchor, metadata.descendantPath);
  if (descendant && isFocusableDescendant(descendant)) {
    descendant.focus();
    if (descendant.ownerDocument.activeElement === descendant) return true;
  }
  // Production anchors are divs. Make only this programmatic fallback
  // focusable; tab order remains unchanged because -1 is not tabbable.
  anchor.tabIndex = -1;
  anchor.focus();
  return anchor.ownerDocument.activeElement === anchor;
}

type FocusRestoreResult = "not-focused" | "restored" | "waiting" | "missing";

export function focusCapturedEntry(
  el: HTMLElement,
  captured: CapturedTranscriptView,
  candidates: readonly ViewAnchorPosition[],
  listRef: RefObject<VirtualListHandle | null> | undefined,
  pending: PendingTranscriptViewRestore,
): FocusRestoreResult {
  if (captured.focusedEntryId === undefined) return "not-focused";
  const metadata =
    capturedFocusMetadata.get(captured) ??
    ({
      anchorId: captured.focusedEntryId,
      sourceIdentity: sourceIdentity(captured.focusedEntryId),
      descendantPath: [],
      element: el.ownerDocument.activeElement instanceof HTMLElement ? el.ownerDocument.activeElement : el,
    } satisfies CapturedFocusMetadata);
  const focused = Array.from(el.querySelectorAll<HTMLElement>("[data-view-anchor-id]")).find((candidate) =>
    focusNodeMatches(candidate, metadata),
  );
  if (focused) {
    return focusAnchor(focused, metadata) ? "restored" : "missing";
  }

  const logicalFocus = candidates.find((candidate) => focusCandidateMatches(candidate, metadata));
  if (logicalFocus) {
    if (!pending.focusScrollRequested && listRef?.current) {
      pending.focusScrollRequested = true;
      listRef.current.scrollToIndex(logicalFocus.index, { align: "start" });
      return "waiting";
    }
    return pending.focusScrollRequested ? "waiting" : "missing";
  }
  return "missing";
}

export interface PendingTranscriptViewRestore {
  readonly captured: CapturedTranscriptView;
  readonly readView?: TranscriptReadView;
  readonly positioningRevision?: number;
  readonly restoreFocus: boolean;
  target?: RestoredViewAnchor;
  scrollRequested: boolean;
  anchorRestored: boolean;
  focusScrollRequested: boolean;
  viewportReflow: boolean;
}
