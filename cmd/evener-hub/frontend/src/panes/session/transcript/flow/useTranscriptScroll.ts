// useTranscriptScroll: the transcript pane's one scroll-behavior hook -
// stick-to-bottom, the new-content pill's count/needs-you state, near-top
// loadOlder triggering, and view-mode scroll-anchor capture/restore. Kept
// as a single hook (rather than several independently-attaching ones)
// because every one of these concerns reads and reacts to the SAME scroll
// element and the SAME "did the turn/item shape change" signal - splitting
// them would mean multiple native scroll listeners racing on one DOM node
// for no benefit.
//
// What this hook deliberately does NOT own anymore: raw scroll-position
// COMPENSATION. Prepend (loadOlder) anchoring, estimate->measured settle
// correction, and follow-on-append are the VirtualList's own job since it
// grew an end-anchor (widgets/virtuallist's anchorToEnd, backed by
// virtual-core 3.17's anchorTo:"end" + followOnAppend): the virtualizer
// reads REAL DOM geometry at the moment of the mutation, where this hook's
// hand-rolled scrollTop math had to trust estimated sizes and could strand
// a freshly-opened session mid-transcript (or mis-time a yank). Doing both
// would double-compensate every prepend.
//
// Design notes (see this task's report for the fuller reasoning):
//  - "At bottom before the mutation" is measured continuously by the native
//    scroll listener into wasAtBottomRef, NOT re-measured inside the
//    content-changed effect (which runs AFTER the DOM already committed the
//    new turns/items) - the ref always reflects the last REAL scroll
//    position, which is inherently "pre-mutation" relative to whatever
//    content change happens next.
//  - `measure` is the injectable seam this wave's binding constraints call
//    for (jsdom performs no real layout - see scrollMetrics.ts and
//    VirtualList's own test suite doc comment); production defaults to
//    reading the real DOM, tests substitute a controlled fake.
//  - A prepend is detected by diffing the FIRST turn's id across renders,
//    not an out-of-band "a loadOlder call is in flight" flag: a live append
//    can land while a loadOlder request is still in flight, and diffing the
//    data's own shape stays correct regardless of that interleaving.
import { type RefObject, useCallback, useLayoutEffect, useRef, useState } from "react";
import type { ThreadModel, TurnModel } from "../../../../protocol/model";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";
import { isDormantTranscript } from "../transcriptVisibility";
import { isAtBottom, isNearTop, readScrollMetrics, type ScrollMetrics } from "./scrollMetrics";
import { type CapturedTranscriptView, registerTranscriptView } from "./transcriptViewRegistry";

export interface UseTranscriptScrollOptions {
  ref: string;
  model: ThreadModel | undefined;
  listRef: RefObject<VirtualListHandle | null>;
  loadOlder: () => Promise<void>;
  /** Injectable measurement seam - defaults to the real DOM (readScrollMetrics). */
  measure?: (el: HTMLElement) => ScrollMetrics;
  /** Identity of the currently rendered transcript representation. */
  viewKey?: string;
  /** Injectable stable-entry geometry seam; production reads data-view-anchor elements. */
  measureAnchors?: (el: HTMLElement) => ViewAnchorPosition[];
  /** All entries in the active representation, including those in virtualized-out rows. */
  anchorEntries?: readonly Omit<ViewAnchorPosition, "offset" | "height">[];
  /** Number of rows in the active transformed representation. */
  renderedRowCount?: number;
  /** Source turn id to active transformed-row index. */
  sourceTurnRowIndexes?: ReadonlyMap<string, number>;
  /**
   * The session pane's pending-questions dock is a virtual row
   * (TranscriptBody's trailingRow), and an in-progress ask_user item
   * COMPLETING activates it without any turn/item shape change - neither
   * itemCount nor firstTurnId nor failedTurns moves, so the content-changed
   * effect never fires for it. This option carries the dock's own pending
   * signal (askDockStore via Session.tsx's useAskDockPending) so the hook
   * can treat its rising edge as new content: a reader who is not at the
   * bottom gets the new-content pill (needs-you styling comes free -
   * isAttentionWorthy reads the wire's askPending), and jumpToBottom lands
   * on the dock row because renderedRowCount counts it (same PR's count
   * fix). A reader at the bottom gets nothing: the end-anchored list
   * already followed the appended row into view.
   */
  askDockPending?: boolean;
  /**
   * The dock pending set's activation counter (askDockStore's
   * activationEpoch). The pill edge keys on THIS, not the boolean: a
   * snapshot resync can atomically replace the pending set (old batch
   * answered elsewhere, new one pending) without the boolean ever leaving
   * true, and that replacement is exactly the moment a scrolled-away reader
   * most needs the pill. Same-set additions don't bump it - the reader was
   * already told.
   */
  askDockActivationEpoch?: number;
}

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

export function restoreTopAnchor(
  anchor: ViewAnchor,
  positions: readonly ViewAnchorPosition[],
): RestoredViewAnchor | undefined {
  const exact = positions.find((position) => position.id === anchor.id);
  if (exact) return { id: exact.id, index: exact.index, offset: anchor.offset };
  // The entry has folded into a tool run since it was captured: the run's
  // anchor is the row that now stands where the entry stood.
  const owner = positions.find((position) => position.members?.includes(anchor.id));
  if (owner) return { id: owner.id, index: owner.index, offset: anchor.offset };

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

function readAnchorPositions(el: HTMLElement): ViewAnchorPosition[] {
  const viewportTop = el.getBoundingClientRect().top;
  return Array.from(el.querySelectorAll<HTMLElement>("[data-view-anchor-id]")).map((row, renderedIndex) => {
    const rect = row.getBoundingClientRect();
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

function topVisiblePosition(positions: readonly ViewAnchorPosition[]): ViewAnchorPosition | undefined {
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
  };
  if (firstVisible) capturedAnchorMetadata.set(captured, captureTopAnchor(firstVisible));
  if (focusMetadata) capturedFocusMetadata.set(captured, focusMetadata);
  return captured;
}

function anchorFromCapture(
  captured: CapturedTranscriptView,
  candidates: readonly ViewAnchorPosition[],
): ViewAnchor | undefined {
  const metadata = capturedAnchorMetadata.get(captured);
  if (metadata) return metadata;
  const source = candidates.find((candidate) => candidate.id === captured.anchorId);
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

// A closed disclosure that owns the anchor: an intent-group entry's anchor
// sits INSIDE its <details>, a folded tool run's anchor wraps its own
// <details> (TurnBlock's runAnchorFor), so the two are looked up from
// opposite directions. Either way the summary is the thing to focus.
function closedGroupSummary(anchor: HTMLElement): HTMLElement | undefined {
  const id = anchor.dataset.viewAnchorId ?? "";
  let summary: Element | null | undefined;
  if (id.startsWith("intent:")) {
    const details = anchor.closest<HTMLDetailsElement>('details[data-testid="intent-group"]:not([open])');
    summary = details?.querySelector(":scope > summary");
  } else if (id.startsWith("run:")) {
    summary = anchor.querySelector(':scope > details[data-testid="tool-run"]:not([open]) > summary');
  }
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

function focusCapturedEntry(
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

export interface UseTranscriptViewRegistrationOptions {
  enabled: boolean;
  id: string;
  layout?: string;
  viewKey?: string;
  listRef?: RefObject<VirtualListHandle | null>;
  measure?: (el: HTMLElement) => ScrollMetrics;
  measureAnchors?: (el: HTMLElement) => ViewAnchorPosition[];
  anchorEntries?: readonly Omit<ViewAnchorPosition, "offset" | "height">[];
  renderedRowCount?: number;
  focusFallback?: () => void;
  announce?: (summary: string) => void;
}

export interface UseTranscriptViewRegistrationResult {
  restoreAfterMeasurement(): void;
}

interface PendingTranscriptViewRestore {
  readonly captured: CapturedTranscriptView;
  target?: RestoredViewAnchor;
  scrollRequested: boolean;
  anchorRestored: boolean;
  focusScrollRequested: boolean;
}

/**
 * Registers the shared body with the transition registry without taking
 * ownership of ordinary append/prepend scrolling. The body calls the result
 * from VirtualList's measurement callback; the view-key layout effect covers
 * hosts whose row measurement callback does not fire for a config commit.
 */
export function useTranscriptViewRegistration(
  options: UseTranscriptViewRegistrationOptions,
): UseTranscriptViewRegistrationResult {
  const { enabled, id, layout, viewKey } = options;
  const optionsRef = useRef(options);
  optionsRef.current = options;
  const pendingRef = useRef<PendingTranscriptViewRestore | null>(null);

  const restoreAfterMeasurement = useCallback(() => {
    const pending = pendingRef.current;
    if (!pending) return;
    const currentOptions = optionsRef.current;
    const el = currentOptions.listRef?.current?.getScrollElement();
    if (!el) return;

    const measure = currentOptions.measure ?? readScrollMetrics;
    const measureAnchors = currentOptions.measureAnchors ?? readAnchorPositions;
    const measured = measureAnchors(el);
    const candidates = currentOptions.anchorEntries?.map((entry) => ({ ...entry, offset: 0 })) ?? measured;
    if (pending.captured.followingBottom) {
      const count = currentOptions.renderedRowCount ?? 0;
      if (count > 0) currentOptions.listRef?.current?.scrollToIndex(count - 1, { align: "end" });
      const focusResult = focusCapturedEntry(el, pending.captured, candidates, currentOptions.listRef, pending);
      if (focusResult === "waiting") return;
      if (focusResult === "missing") currentOptions.focusFallback?.();
      pendingRef.current = null;
      return;
    }

    const anchor = anchorFromCapture(pending.captured, candidates);
    if (!pending.anchorRestored) {
      const pendingTargetStillExists =
        pending.target === undefined || candidates.some((candidate) => candidate.id === pending.target?.id);
      if (!pendingTargetStillExists) {
        pending.target = undefined;
        pending.scrollRequested = false;
      }
      const restored = pending.target ?? (anchor ? restoreTopAnchor(anchor, candidates) : undefined);
      if (!restored) {
        const metrics = measure(el);
        const scrollable = Math.max(0, metrics.scrollHeight - metrics.clientHeight);
        el.scrollTop = Math.max(0, Math.min(1, pending.captured.normalizedOffset)) * scrollable;
        pending.anchorRestored = true;
      } else {
        const current = measured.find((position) => position.id === restored.id);
        if (current) {
          el.scrollTop += current.offset - restored.offset;
          pending.anchorRestored = true;
        } else if (!pending.scrollRequested) {
          pending.target = restored;
          pending.scrollRequested = true;
          currentOptions.listRef?.current?.scrollToIndex(restored.index, { align: "start" });
          return;
        } else {
          return;
        }
      }
    }

    const focusResult = focusCapturedEntry(el, pending.captured, candidates, currentOptions.listRef, pending);
    if (focusResult === "waiting") return;
    if (focusResult === "missing") currentOptions.focusFallback?.();
    pendingRef.current = null;
  }, []);

  const capture = useCallback((): CapturedTranscriptView => {
    const currentOptions = optionsRef.current;
    const el = currentOptions.listRef?.current?.getScrollElement();
    if (!el) {
      return {
        anchorOffset: 0,
        normalizedOffset: 0,
        followingBottom: false,
      };
    }
    return captureTranscriptView(el, currentOptions.measure, currentOptions.measureAnchors);
  }, []);

  const restore = useCallback((captured: CapturedTranscriptView): void => {
    pendingRef.current = {
      captured,
      scrollRequested: false,
      anchorRestored: false,
      focusScrollRequested: false,
    };
  }, []);

  const announce = useCallback((summary: string) => {
    optionsRef.current.announce?.(summary);
  }, []);

  useLayoutEffect(() => {
    if (!enabled) return;
    return registerTranscriptView({
      id,
      layout,
      capture,
      restore,
      announce,
    });
  }, [announce, capture, enabled, id, layout, restore]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: viewKey is deliberately trigger-only
  useLayoutEffect(() => {
    restoreAfterMeasurement();
  }, [restoreAfterMeasurement, viewKey]);

  return { restoreAfterMeasurement };
}

export interface UseTranscriptScrollResult {
  /** Items rendered since the reader last was at (or returned to) the bottom. */
  pillCount: number;
  /** True whenever the jump-to-latest pill should be on offer: the reader is
   * away from the bottom (even with nothing new - the pill is a scroll-
   * position affordance first, per docs/web-ui/decisions.md's "a
   * jump-to-latest pill when scrolled up"), OR unseen items/failures are
   * pending. Sourced from real DOM geometry in the scroll listener, so a
   * jump that lands short leaves it true instead of stranding the reader. */
  pillVisible: boolean;
  /** True while the pill is showing AND the thread is currently attention-worthy
   * (askPending, or a status the pane's own Cadence mapping treats as needs-you) -
   * recomputed live every render, so a later status flip upgrades the pill
   * in place even if it lands after the reader scrolled away or after the
   * content that produced it. */
  pillNeedsYou: boolean;
  /** True while a failed turn the reader hasn't seen yet is anchored (see
   * "the error anchor" below) - outranks pillNeedsYou per the pinned
   * contract (contracts-transcript-scroll-liveness.md §5: "error outranks
   * a simultaneous needs-you state"). Exposed independently of
   * pillNeedsYou rather than pre-resolved here: precedence is a rendering
   * decision for whoever consumes both (NewContentPill), not this hook's
   * job to collapse into one value. */
  pillError: boolean;
  /** Direction for the NewContentPill's chevron arrow. "up" when the error
   * anchor (if active) is above the current viewport, "down" otherwise
   * (normal case: new content below, or no error anchor). */
  pillArrowDirection: "up" | "down";
  /** Scrolls to the last turn and clears the pill's count/error state -
   * unless an error anchor is active, in which case it jumps to THAT turn's
   * index instead (see "the error anchor" below). The pill's VISIBILITY is
   * not cleared by the click itself: it stays on offer until the landing's
   * scroll event (or an at-bottom measurement at click time) confirms
   * arrival. Also the target for a manual click on NewContentPill. */
  jumpToBottom: () => void;
  /** Capture the top stable row immediately before changing view mode. */
  captureViewAnchor: () => void;
  /** Finish a pending restore after VirtualList reports new measurements. */
  restoreViewAnchorAfterMeasurement: () => void;
}

function totalItemCount(model: ThreadModel | undefined): number {
  if (!model) return 0;
  let total = 0;
  for (const turn of model.turns) total += turn.items.length;
  return total;
}

// Mirrors Session.tsx's own cadenceStateForStatus mapping (awaiting/warning
// -> needs-you) rather than importing it: flow/ is composed BY Session.tsx,
// not the other way around (same "deliberately separate, parallel small
// mapping function" precedent Session.tsx itself follows relative to
// shell/rail/RailRow.tsx's cadenceStateFor - see its own comment). askPending
// is checked independently since it need not always coincide with status.type.
function isAttentionWorthy(model: ThreadModel | undefined): boolean {
  if (!model) return false;
  return model.askPending || model.status.type === "awaiting" || model.status.type === "warning";
}

// The error-anchor's failure signal is TURN-level (this rewrite's own
// TurnModel.status/error, stamped by turn/completed - see reducer.test.ts's
// EventError-shape tests), not legacy's tool-call-level "did this one call
// fail" (contracts-transcript-scroll-liveness.md §5 line 114 talks about a
// finalized tool call; the w4 fix-round report's item-9 writeup already
// established turn.status === "failed" as this rewrite's equivalent
// signal, confirmed against appwire/types.go's TurnStatusFailed). error is
// `unknown` on TurnModel, so this only ever checks presence, never shape.
function isFailedTurn(turn: TurnModel): boolean {
  return turn.status === "failed" || turn.error !== undefined;
}

// A COUNT (not a boolean, not "the first failed turn's index/id") - failed-
// ness is terminal (a turn never un-fails) and turns only ever append or
// get prepended, never removed, so this only ever goes UP. Deliberately a
// count rather than the first-failed index: a turn's own failure can be
// resolved (seen live at the bottom, or anchored-then-cleared) while an
// EARLIER-index turn stays the "first" failed one by array position -
// pinning the dependency to "the first index" would then never change
// again and a genuinely new, later failure would never get a chance to be
// evaluated. Recomputed every render (same O(turns) cost class as
// totalItemCount above) but its VALUE is identical across a pure streaming
// delta (which touches item text/pendingText, never turn.status/error), so
// it doesn't defeat the "per-delta work never re-runs the content-changed
// effect" property - see that effect's own dependency-array comment.
function failedTurnCount(model: ThreadModel | undefined): number {
  if (!model) return 0;
  let n = 0;
  for (const turn of model.turns) if (isFailedTurn(turn)) n++;
  return n;
}

export function useTranscriptScroll({
  ref,
  model,
  listRef,
  loadOlder,
  measure = readScrollMetrics,
  viewKey = "everything",
  measureAnchors = readAnchorPositions,
  anchorEntries,
  renderedRowCount: renderedRowCountInput,
  sourceTurnRowIndexes,
  askDockPending = false,
  askDockActivationEpoch = 0,
}: UseTranscriptScrollOptions): UseTranscriptScrollResult {
  const [pillCount, setPillCount] = useState(0);
  // The first failed turn's index, while the reader hasn't seen it yet
  // (null = no active anchor). State (not just a ref) because clearing it
  // from inside the scroll listener (see handleScroll below) must trigger
  // a re-render so pillError updates live, exactly like pillCount already
  // does for the same reason.
  const [errorAnchorIndex, setErrorAnchorIndex] = useState<number | null>(null);
  // Arrow direction for the pill: "up" when the anchor is above the visible
  // range, "down" otherwise (normal case or no anchor). Updated whenever
  // scroll position changes (in handleScroll), so it's always in sync with
  // the current viewport state.
  const [pillArrowDirection, setPillArrowDirection] = useState<"up" | "down">("down");
  // "The reader is away from the bottom" as RENDER state (wasAtBottomRef is
  // the same fact as a ref, for the long-lived scroll closure). This is what
  // lets the pill be a scroll-position affordance - visible whenever the
  // reader has scrolled back, even with zero new items - rather than only a
  // new-content counter. Updated everywhere wasAtBottomRef is written:
  // handleScroll (the common path), the one-time mount init, and the per-ref
  // reset. jumpToBottom SETS it in the error-anchor branch (the landing is
  // deliberately not the bottom) and, in the bottom-seeking branch, clears
  // it ONLY from a measurement that already reads at-bottom (where no scroll
  // - and so no landing event - will happen); otherwise the scroll triggered
  // by the jump fires handleScroll on landing, which clears it only once the
  // reader has ACTUALLY arrived - so a jump that lands short leaves the pill
  // on offer instead of vanishing into a stranded mid-transcript position.
  const [awayFromBottom, setAwayFromBottom] = useState(false);

  const wasAtBottomRef = useRef(true);
  const firstTurnIdRef = useRef<string | undefined>(undefined);
  const baselineItemCountRef = useRef(0);
  const initializedRef = useRef(false);
  // The ref this hook's per-ref state was initialized for. The Session pane
  // is NOT keyed by ref (DockHost's PaneHost renders <Component params=...>
  // with no key), so clicking a different session in the sidebar updates
  // params.ref on the SAME mounted component instance - useTranscriptScroll's
  // own refs (initializedRef, wasAtBottomRef, baselineItemCountRef,
  // firstTurnIdRef, resolvedFailedTurnIdsRef, errorAnchorIndex) all persist
  // across that change. Without a reset, the new session opens wherever the
  // virtualizer defaults (initializedRef is already true, so the mount
  // effect's scroll-to-bottom is skipped) and stick-to-bottom / pill counts
  // are computed against the PREVIOUS session's scroll state. refForInitRef
  // detects the change and re-initializes every per-ref piece of state so
  // the new session is treated as a fresh open (always land at the bottom).
  const refForInitRef = useRef<string | undefined>(ref);
  const pendingViewAnchorRef = useRef<{
    anchor: ViewAnchor | undefined;
    proportion: number;
    target?: RestoredViewAnchor;
    scrollRequested?: boolean;
  } | null>(null);
  // Turn IDs whose failure (if any) has already been accounted for - seen
  // live while at the bottom, already anchored-and-cleared, or currently
  // the active anchor (added the moment it's chosen - see the
  // content-changed effect below). Keyed by ID rather than a scan
  // POSITION/watermark deliberately: a turn can be observed as
  // not-yet-failed by one effect run (e.g. triggered by an unrelated
  // item's growth) and only fail on a LATER run - a position-based "how
  // far have I scanned" cutoff would already have advanced past it by
  // then and could never find it again, even after the dependency array
  // is fixed to notice the failure at all (this was the actual review
  // finding: the wire's real turn/completed EventError path streams items
  // normally, THEN settles via a bare stamp with no new items - see
  // failedTurnCount's own comment for the trigger half of that fix). IDs
  // are also prepend-safe for free: unlike errorAnchorIndex (a position,
  // shifted explicitly below), a turn's identity doesn't change when
  // older turns are prepended in front of it. The active target's row index is
  // resolved from its source turn id on every transformed-row update.
  const resolvedFailedTurnIdsRef = useRef<Set<string>>(new Set());
  // Latest-ref mirror of errorAnchorIndex (state) for the same reason
  // itemCountRef/renderedRowCountRef/modelRef exist: handleScroll is a
  // long-lived closure (attached once per mount/hasContent transition, not
  // every render - see that effect's own comment) and the content-changed
  // effect's dependency array deliberately excludes it, so both must read
  // the CURRENT value through a ref rather than close over a stale one.
  const errorAnchorIndexRef = useRef<number | null>(null);
  errorAnchorIndexRef.current = errorAnchorIndex;
  const errorAnchorTurnIdRef = useRef<string | undefined>(undefined);

  // "Latest" ref so the scroll listener - attached far less often than every
  // render - never invokes a stale loadOlder closure. Necessary specifically
  // because useTranscript.ts's own loadOlder callback changes identity on
  // every loadingOlder flip (its de-dupe guard reads loadingOlder from that
  // same closure); calling a stale one could read a stale de-dupe flag.
  const loadOlderRef = useRef(loadOlder);
  loadOlderRef.current = loadOlder;

  const itemCount = totalItemCount(model);
  const turnsLength = model?.turns.length ?? 0;
  const renderedRowCount = renderedRowCountInput ?? turnsLength;
  const firstTurnId = model?.turns[0]?.id;
  // Content-changed effect trigger (see that effect's own dependency-array
  // comment for why itemCount/firstTurnId alone can't reach a bare-stamp
  // turn failure).
  const failedTurns = failedTurnCount(model);
  // NOT turnsLength > 0 (kata cmjb): a real evener session's transcript
  // always carries at least the synthetic prelude turn (isDormantTranscript's
  // own comment), so turnsLength is already 1 - and this would already be
  // true - before the VirtualList this hook depends on has ever mounted;
  // Session.tsx renders EmptyTranscript, not the real transcript, for that
  // exact state. A dormant session's turns.length going 1 -> 2 (the prelude
  // gains its first real turn) would then leave hasContent unchanged, so the
  // mount effect below would silently never re-run at the one render where
  // VirtualList actually appears - initializedRef stuck false, no
  // scroll-to-bottom, no scroll listener, no stick-to-bottom, for the rest
  // of the pane's mounted life. isDormantTranscript mirrors Session.tsx's
  // own render condition exactly, so this flips at the SAME transition
  // VirtualList actually mounts at.
  const hasContent = !isDormantTranscript(model?.turns ?? []);
  // Track hasContent transitions so a false->true flip (VirtualList remounts
  // after the model briefly went undefined - e.g. a store resync that clears
  // the thread, or the same ref re-hydrating) re-runs the one-time
  // scroll-to-bottom. initializedRef alone can't see this: it was set true on
  // the first hydration and never resets, so a remount on the SAME ref would
  // skip the scroll-to-bottom and strand the reader at the virtualizer's
  // default top position. (A ref CHANGE is handled separately by
  // refForInitRef below; this handles the same-ref remount.)
  const prevHasContentRef = useRef(hasContent);
  // Also kept in refs for the scroll listener/jumpToBottom, which are not
  // re-created on every render (see the effects below).
  const itemCountRef = useRef(itemCount);
  itemCountRef.current = itemCount;
  const renderedRowCountRef = useRef(renderedRowCount);
  renderedRowCountRef.current = renderedRowCount;
  const sourceTurnRowIndexesRef = useRef(sourceTurnRowIndexes);
  sourceTurnRowIndexesRef.current = sourceTurnRowIndexes;
  // Latest ref for model itself: the content-changed effect below needs the
  // full turns array (to size a detected prepend), but must NOT re-run on
  // every streaming delta just because `model` is a fresh object reference -
  // its dependency array is itemCount/firstTurnId (primitives) precisely so
  // a pure text-delta re-render (which changes neither) is free, matching
  // the wave's own "per-delta work never re-renders the settled transcript"
  // constraint in spirit even though this hook's own work is cheap either way.
  const modelRef = useRef(model);
  modelRef.current = model;

  const rowIndexForTurn = useCallback((turnId: string, sourceIndex: number): number => {
    const mapped = sourceTurnRowIndexesRef.current?.get(turnId) ?? sourceIndex;
    return Math.max(0, Math.min(mapped, Math.max(0, renderedRowCountRef.current - 1)));
  }, []);

  const clearPill = useCallback(() => {
    setPillCount(0);
    baselineItemCountRef.current = itemCountRef.current;
    // The reader is caught up (this fires both on a manual scroll-to-bottom
    // and on jumpToBottom below) - any pending error anchor is resolved
    // too, the same "they saw it" reasoning the at-bottom append path
    // already uses when deciding whether to set one in the first place.
    // Synchronous ref assignment alongside the state setter, exactly like
    // baselineItemCountRef above: a caller that immediately re-reads
    // errorAnchorIndexRef in the same tick (handleScroll does) must see
    // the cleared value without waiting for the next render.
    setErrorAnchorIndex(null);
    errorAnchorIndexRef.current = null;
    errorAnchorTurnIdRef.current = undefined;
    // With the anchor gone the pill's next jump heads for the bottom, never
    // up - and because the pill now STAYS VISIBLE after an anchor click, a
    // stale "up" arrow would otherwise render on the plain "latest" pill
    // until the next scroll event recomputes it.
    setPillArrowDirection("down");
  }, []);

  const jumpToBottom = useCallback(() => {
    const anchor = errorAnchorIndexRef.current;
    if (anchor !== null) {
      // Jumps INTO the transcript, not to the bottom - wasAtBottomRef must
      // stay false, or the very next mutation's stick-to-bottom check would
      // yank the reader away from the row they just navigated to (see
      // "the error anchor" - the whole point of an anchor is to land THERE).
      listRef.current?.scrollToIndex(anchor, { align: "start" });
      wasAtBottomRef.current = false;
      // Mirror into render state like every other wasAtBottomRef write site:
      // the landing is not the bottom, so the pill stays on offer.
      setAwayFromBottom(true);
    } else {
      const count = renderedRowCountRef.current;
      // Bottom state is NEVER set optimistically: wasAtBottomRef and
      // awayFromBottom only come from measured geometry. So measure BEFORE
      // requesting any scroll - scrollToIndex can synchronously move the DOM
      // to its estimate-derived end, which would make an after-the-fact
      // measurement describe the jump's own unconfirmed landing rather than
      // the reader's position at click time.
      const el = listRef.current?.getScrollElement();
      const m = el ? measure(el) : undefined;
      if (m && isAtBottom(m)) {
        // Already at the true bottom by measurement: no scroll will happen,
        // so no landing event will fire - confirm arrival from the
        // measurement itself.
        wasAtBottomRef.current = true;
        setAwayFromBottom(false);
      } else {
        // The pre-jump measurement is authoritative for the reader's CURRENT
        // position: they are away from the bottom. Write that into both
        // trackers before requesting the jump - the DOM can move without a
        // scroll event (content growth above the viewport, measurement
        // corrections), leaving the trackers stale at at-bottom, and a stale
        // at-bottom would let an append in the landing window auto-stick and
        // would hide the pill the moment clearPill() runs below.
        wasAtBottomRef.current = false;
        setAwayFromBottom(true);
        // scrollToIndex engages the virtualizer's own machinery (scrollState
        // -> measurement during the scroll, the reconcile loop, and the
        // anchorToEnd pinning that holds the end across later
        // estimate->measured corrections). But its target offset derives from
        // the measurement cache - ESTIMATES for every row between here and
        // the end that has never been rendered - so the landing it computes
        // is not guaranteed to be the true bottom, and whether a correction
        // arrives afterward is timing-dependent (the reconcile loop settles
        // after one stable frame; ResizeObserver delivery is async). That
        // shortfall was the unreliable jump: the pill had already been
        // cleared, and with no new content arriving there was no affordance
        // left to recover with.
        //
        // So after engaging the virtualizer, pin the scroll element to its
        // true DOM maximum directly - exact by construction, whatever the
        // estimates say. Later measurement corrections only ever change
        // scrollHeight, and the end-anchor (threshold 4px; the pin leaves the
        // distance at 0) keeps the viewport pinned to the new true end.
        //
        // Arrival is confirmed only by the landing's own scroll event
        // (handleScroll): until then the reader is still away, so an append
        // in that window increments the pill instead of auto-sticking on an
        // unconfirmed jump, and a landing that later corrections leave short
        // keeps the pill on offer.
        if (count > 0) listRef.current?.scrollToIndex(count - 1, { align: "end" });
        if (el) {
          // Pin from LIVE geometry, re-measured after scrollToIndex: the
          // pre-jump measurement m is only for the at-bottom classification
          // above - engaging the virtualizer may already have corrected the
          // DOM maximum, and a pin computed from the stale value could land
          // short.
          const live = measure(el);
          el.scrollTop = Math.max(0, live.scrollHeight - live.clientHeight);
        }
      }
    }
    clearPill();
  }, [listRef, clearPill, measure]);

  const captureViewAnchor = useCallback(() => {
    const el = listRef.current?.getScrollElement();
    if (!el) return;
    const m = measure(el);
    const scrollable = Math.max(0, m.scrollHeight - m.clientHeight);
    const positions = measureAnchors(el);
    const firstVisible = topVisiblePosition(positions);
    pendingViewAnchorRef.current = {
      anchor: firstVisible ? captureTopAnchor(firstVisible) : undefined,
      proportion: scrollable > 0 ? m.scrollTop / scrollable : 0,
    };
  }, [listRef, measure, measureAnchors]);

  const restoreViewAnchorAfterMeasurement = useCallback(() => {
    const pending = pendingViewAnchorRef.current;
    if (!pending) return;
    const el = listRef.current?.getScrollElement();
    if (!el) return;

    const measured = measureAnchors(el);
    const positions = anchorEntries?.map((entry) => ({ ...entry, offset: 0 })) ?? measured;
    const restored = pending.target ?? (pending.anchor ? restoreTopAnchor(pending.anchor, positions) : undefined);
    if (!restored) {
      const m = measure(el);
      el.scrollTop = pending.proportion * Math.max(0, m.scrollHeight - m.clientHeight);
      pendingViewAnchorRef.current = null;
      return;
    }

    const current = measured.find((position) => position.id === restored.id);
    if (current) {
      el.scrollTop += current.offset - restored.offset;
      pendingViewAnchorRef.current = null;
      return;
    }

    // Keep the target and desired offset pending. VirtualList's onChange seam
    // calls this function again after scrollToIndex renders/measures the row.
    // Mark first because react-virtual can notify synchronously from the call.
    if (!pending.scrollRequested) {
      pending.target = restored;
      pending.scrollRequested = true;
      listRef.current?.scrollToIndex(restored.index, { align: "start" });
    }
  }, [listRef, measure, measureAnchors, anchorEntries]);

  // Mount / (re)attach. Guarded by initializedRef so the one-time restore-
  // or-default-to-bottom positioning and baseline recording happen exactly
  // once per "the scroll element became available" transition (initial
  // mount, or VirtualList mounting for the first time once a previously-
  // empty thread gets its first turn) - hasContent is what makes this rerun
  // for that later transition, since a plain RefObject mutation alone
  // triggers no rerun.
  // biome-ignore lint/correctness/useExhaustiveDependencies: both flags below are deliberate, re-verified against this rule - see the two comments inside
  useLayoutEffect(() => {
    // Ref change on a persistent Session instance (sidebar click to a
    // different session reuses the pane - see refForInitRef's own comment):
    // reset every per-ref piece of state so the new session is treated as a
    // fresh open. This runs before the initializedRef guard below so that
    // guard re-executes its scroll-to-bottom for the new ref.
    //
    // A same-ref remount (hasContent false->true: the model briefly went
    // undefined and VirtualList unmounted, then re-hydrated) is caught here
    // too: initializedRef was left true from the first hydration, so without
    // this reset the remount would skip the scroll-to-bottom and strand the
    // reader at the top.
    //
    // This block runs BEFORE the scroll-element null check below: a ref
    // change or remount can land on a render where VirtualList hasn't mounted
    // yet (model undefined), so `el` is null - the reset must still fire so
    // the LATER render where VirtualList appears treats it as a fresh open.
    const hasContentRemounted = !prevHasContentRef.current && hasContent;
    if (refForInitRef.current !== ref || hasContentRemounted) {
      refForInitRef.current = ref;
      initializedRef.current = false;
      wasAtBottomRef.current = true;
      setAwayFromBottom(false);
      firstTurnIdRef.current = undefined;
      baselineItemCountRef.current = 0;
      pendingViewAnchorRef.current = null;
      resolvedFailedTurnIdsRef.current = new Set();
      setErrorAnchorIndex(null);
      errorAnchorIndexRef.current = null;
      errorAnchorTurnIdRef.current = undefined;
      setPillCount(0);
    }
    prevHasContentRef.current = hasContent;

    const el = listRef.current?.getScrollElement();
    if (!el) return;

    if (!initializedRef.current) {
      // Opening a session always lands at the end (kata cmjb, Jesse's call):
      // the latest content is what a reader clicks in for. This deliberately
      // replaced the earlier per-ref restore of a stored scroll offset — the
      // whole persistence (threads.ts scrollPositions + the debounced writer
      // that lived below) was removed with it, not just bypassed.
      const count = renderedRowCountRef.current;
      if (count > 0) listRef.current?.scrollToIndex(count - 1, { align: "end" });
      const m = measure(el);
      wasAtBottomRef.current = isAtBottom(m);
      setAwayFromBottom(!wasAtBottomRef.current);
      firstTurnIdRef.current = firstTurnId;
      baselineItemCountRef.current = itemCountRef.current;
      // Turns present at mount (e.g. a cold-opened session whose history
      // already contains a failed turn) are not "newly appended" - see the
      // content-changed effect's failed-turn scan below. model is read
      // directly (not through modelRef) since this block runs exactly once,
      // at whichever render actually mounts - same reasoning as firstTurnId
      // just above, and the same pattern the mount effect's own closing
      // comment already documents for that field.
      for (const t of model?.turns ?? []) {
        if (isFailedTurn(t)) resolvedFailedTurnIdsRef.current.add(t.id);
      }
      initializedRef.current = true;
    }

    function handleScroll() {
      // el is already narrowed non-null above, but that narrowing doesn't
      // carry into this nested closure's own type - it's the same `const`,
      // never reassigned, so re-checking it here is a formality, not a real
      // possibility.
      if (!el) return;
      const m = measure(el);
      wasAtBottomRef.current = isAtBottom(m);
      setAwayFromBottom(!wasAtBottomRef.current);
      if (wasAtBottomRef.current) clearPill();
      // The error anchor also clears on its own once its failed turn
      // scrolls into the rendered range - narrower than clearPill above
      // (only the anchor, not the rest of the pill: other still-unseen
      // content below it is unrelated and must stay counted). A1's
      // getVisibleRange() is exactly this widget-level lever; null (nothing
      // measured/rendered) reads as "don't know, assume not visible".
      const anchor = errorAnchorIndexRef.current;
      const range = listRef.current?.getVisibleRange();
      if (anchor !== null) {
        if (range && anchor >= range.startIndex && anchor <= range.endIndex) {
          setErrorAnchorIndex(null);
          errorAnchorTurnIdRef.current = undefined;
        }
        // Update arrow direction: point up if anchor is above the visible
        // range, down otherwise. When no range is yet available (before
        // mount), assume down (the normal case).
        if (range && anchor < range.startIndex) {
          setPillArrowDirection("up");
        } else {
          setPillArrowDirection("down");
        }
      } else {
        // No error anchor - always point down (new content is below).
        setPillArrowDirection("down");
      }
      // useTranscript.ts's own loadOlder has no internal catch - a rejected
      // thread/turns/list request propagates through its returned promise
      // uncaught unless the caller handles it; best-effort here, matching
      // Session.tsx's own ensureThread(ref).catch(() => {}) precedent for
      // the exact same shape of gap. (A dedicated unit test asserting "no
      // unhandledRejection fires" was attempted and abandoned - vitest's
      // own runner appears to intercept process-level unhandledRejection
      // dispatch in a way a per-test process.on listener can't reliably
      // observe here, so it couldn't discriminate the buggy state from the
      // fixed one; the full-suite exit-code check DOES catch this class of
      // regression - confirmed empirically: a genuinely uncaught rejection
      // exits 1 even though the individual test that triggered it "passes".)
      if (isNearTop(m.scrollTop)) loadOlderRef.current().catch(() => {});
    }

    el.addEventListener("scroll", handleScroll);
    return () => el.removeEventListener("scroll", handleScroll);
    // firstTurnId is intentionally NOT a dependency: it's only read inside
    // the initializedRef-guarded one-time block above, which - since
    // initializedRef never resets - executes exactly once per mount, at
    // whichever render actually flips hasContent (or the first render, if
    // content is there from the start). That render's closure already has
    // the fresh firstTurnId; every later change to firstTurnId (e.g. a
    // loadOlder prepend) happens after initializedRef.current is already
    // true, when this gated block no longer runs at all - so a later
    // effect re-run with a stale firstTurnId in its closure, if one ever
    // happened, still wouldn't read it. The listener itself also needs no
    // re-attachment when content changes: clearPill/loadOlderRef.current
    // read fresh state at call time regardless.
    //
    // hasContent is listed despite not being read in this effect's body at
    // all - it exists purely to force a re-run at the "VirtualList mounts
    // for the first time" transition (a ref becoming non-null triggers no
    // re-render/effect on its own; hasContent flipping does).
  }, [ref, listRef, measure, clearPill, hasContent]);

  // A mode change commits a different row set into the same VirtualList. This
  // layout effect runs after that commit and after the list's own layout work,
  // so the stable row can first be brought into the measured window, then have
  // its exact viewport offset corrected synchronously before paint.
  // biome-ignore lint/correctness/useExhaustiveDependencies: viewKey is deliberately trigger-only
  useLayoutEffect(() => {
    restoreViewAnchorAfterMeasurement();
  }, [viewKey, restoreViewAnchorAfterMeasurement]);

  // The ask dock's activation edge (see the options' own doc comments): new
  // answerable content appeared below without any transcript shape change.
  // Keyed on the activation EPOCH, not the pending boolean, so an atomic
  // pending-set replacement (a resync swapping an answered-elsewhere batch
  // for a new one) re-fires it while the boolean never left true. Declared
  // AFTER the mount effect above so a ref change (which resets
  // wasAtBottomRef to true for the fresh open) is already reflected when
  // this edge evaluates, and a session OPENED with an already-pending ask
  // never fires it - initial mount scrolls to the end, so the dock starts
  // visible and there is nothing unseen to count.
  const prevAskDockEpochRef = useRef(askDockActivationEpoch);
  useLayoutEffect(() => {
    const previous = prevAskDockEpochRef.current;
    prevAskDockEpochRef.current = askDockActivationEpoch;
    if (askDockActivationEpoch === previous || askDockActivationEpoch === 0) return;
    if (!initializedRef.current || wasAtBottomRef.current) return;
    setPillCount((count) => count + 1);
  }, [askDockActivationEpoch]);

  // Content-changed reaction: fires only when the turn/item SHAPE actually
  // changes (item count, the first turn's identity, or the failed-turn
  // count, all primitives) - never on a pure streaming-text delta, which
  // changes model.turns's object reference but none of those values, so
  // React skips re-running an effect whose primitive deps didn't change.
  // biome-ignore lint/correctness/useExhaustiveDependencies: failedTurns is deliberately trigger-only (never read in the body below) - see its own doc comment above for why a bare-stamp turn failure needs it anyway
  useLayoutEffect(() => {
    if (!initializedRef.current) return;
    const el = listRef.current?.getScrollElement();
    if (!el) return;

    const currentModel = modelRef.current;
    const prevFirstTurnId = firstTurnIdRef.current;
    const isPrepend = prevFirstTurnId !== undefined && firstTurnId !== undefined && firstTurnId !== prevFirstTurnId;

    if (isPrepend && currentModel) {
      const prevIndex = currentModel.turns.findIndex((t) => t.id === prevFirstTurnId);
      if (prevIndex === -1) {
        // Not a simple prepend (e.g. a full resync after reconnect) - don't
        // misattribute; re-baseline entirely rather than guess. An existing
        // anchor's index can't be trusted to still mean anything, and
        // neither can the resolved-turn bookkeeping (a turn's very identity
        // may not be stable across an unpredictable resync) - drop it
        // wholesale rather than risk stale entries.
        baselineItemCountRef.current = itemCount;
        resolvedFailedTurnIdsRef.current = new Set();
        setErrorAnchorIndex(null);
        errorAnchorTurnIdRef.current = undefined;
      } else {
        const prependedCount = currentModel.turns.slice(0, prevIndex).reduce((sum, t) => sum + t.items.length, 0);
        // Prepended history is backfill, not "new" - advance the baseline by
        // exactly what loadOlder added so the pill count stays unaffected.
        baselineItemCountRef.current += prependedCount;
        // The active error target is keyed by source turn id. Re-resolve its
        // transformed row below instead of adding the source prepend count;
        // several source turns may occupy one rendered row.
        const activeTurnId = errorAnchorTurnIdRef.current;
        if (activeTurnId !== undefined) {
          const activeSourceIndex = currentModel.turns.findIndex((turn) => turn.id === activeTurnId);
          if (activeSourceIndex >= 0) setErrorAnchorIndex(rowIndexForTurn(activeTurnId, activeSourceIndex));
        }
        // Prepended (historical) turns are backfill, not new - same
        // "backfill, not new" reasoning as baselineItemCountRef just above,
        // applied to failure tracking: a failed turn the reader is only
        // now paging UP into is already-known history, not a live event to
        // anchor on. Without this, a later append-triggered scan could find
        // it as "the first unresolved failed turn" and wrongly anchor on
        // stale history instead of (or ahead of) a genuinely new failure.
        for (const t of currentModel.turns.slice(0, prevIndex)) {
          if (isFailedTurn(t)) resolvedFailedTurnIdsRef.current.add(t.id);
        }
        // No scrollTop correction here on purpose: the end-anchored
        // VirtualList (anchorToEnd) re-anchors the visible row across a
        // prepend with real per-item geometry. This hook used to add the
        // scrollHeight delta by hand; doing both double-shifts the viewport.
      }
    } else {
      // Failed-turn tracking runs independent of item growth - the real
      // wire's turn/completed EventError path settles with a BARE stamp
      // (no items - see isFailedTurn's own comment), so a failure can
      // arrive without ever moving `unseen` off zero below. failedTurns
      // (the dependency that gets this effect to fire at all for that
      // case) is what makes this reachable; wasAtBottomRef alone then
      // decides the outcome: at the bottom, every currently-unresolved
      // failure is "seen" and resolved in bulk (matching "a failed turn
      // arriving at the bottom never creates an anchor"); scrolled away,
      // the FIRST unresolved one becomes the anchor, but only while none is
      // already active (contracts §5's anchor points at a single row - a
      // later failure doesn't steal it, it stays pending for its own turn
      // once this one clears).
      if (currentModel) {
        if (wasAtBottomRef.current) {
          for (const t of currentModel.turns) {
            if (isFailedTurn(t)) resolvedFailedTurnIdsRef.current.add(t.id);
          }
        } else if (errorAnchorIndexRef.current === null) {
          const firstUnresolved = currentModel.turns.find(
            (t) => isFailedTurn(t) && !resolvedFailedTurnIdsRef.current.has(t.id),
          );
          if (firstUnresolved) {
            resolvedFailedTurnIdsRef.current.add(firstUnresolved.id);
            errorAnchorTurnIdRef.current = firstUnresolved.id;
            setErrorAnchorIndex(rowIndexForTurn(firstUnresolved.id, currentModel.turns.indexOf(firstUnresolved)));
          }
        }
      }

      const unseen = itemCount - baselineItemCountRef.current;
      if (unseen > 0) {
        if (wasAtBottomRef.current) {
          const count = renderedRowCountRef.current;
          if (count > 0) listRef.current?.scrollToIndex(count - 1, { align: "end" });
          wasAtBottomRef.current = true;
          baselineItemCountRef.current = itemCount;
        } else {
          setPillCount(unseen);
        }
      }
    }

    firstTurnIdRef.current = firstTurnId;
    // model is read via modelRef.current (see above), not closed over here,
    // specifically so this effect does NOT re-run on every streaming delta.
    // failedTurns is the one exception to "primitives derived from model
    // don't need model itself in this list" being sufficient: a turn's
    // failure can flip with NEITHER itemCount NOR firstTurnId changing (the
    // bare-stamp settle above), so without it this whole failed-turn branch
    // would silently never run for that real wire shape.
  }, [itemCount, firstTurnId, failedTurns, listRef, measure, renderedRowCount, sourceTurnRowIndexes, rowIndexForTurn]);

  // A transformed row set can change without changing the source turn/item
  // shape (for example, an Intent run coalescing three turns into one row).
  // Keep an active failure target coupled to its source turn id, not its old
  // source-turn index.
  // biome-ignore lint/correctness/useExhaustiveDependencies: row count/map are deliberate trigger dependencies for ref-backed target remapping
  useLayoutEffect(() => {
    const turnId = errorAnchorTurnIdRef.current;
    if (turnId === undefined || modelRef.current === undefined) return;
    const sourceIndex = modelRef.current.turns.findIndex((turn) => turn.id === turnId);
    if (sourceIndex < 0) return;
    const rowIndex = rowIndexForTurn(turnId, sourceIndex);
    if (errorAnchorIndexRef.current !== rowIndex) setErrorAnchorIndex(rowIndex);
  }, [renderedRowCount, sourceTurnRowIndexes, rowIndexForTurn]);

  // pillVisible is the union of every reason to offer the jump: scrolled
  // back (the always-on case), unseen items pending, or an unseen failure
  // anchored. The latter two imply the former in practice (both are only
  // ever set while wasAtBottomRef is false, which handleScroll had already
  // mirrored into awayFromBottom) - listing them just keeps the value right
  // even in the gap between a content change and the next scroll event.
  const pillVisible = awayFromBottom || pillCount > 0 || errorAnchorIndex !== null;

  return {
    pillCount,
    pillVisible,
    // needs-you is gated on VISIBILITY, not on a nonzero count: an awaiting
    // flip that lands after the reader scrolled away (no new items at all)
    // still upgrades the on-offer pill in place. The dock's own pending
    // signal is part of the predicate, not just the model: model.askPending
    // is snapshot-authoritative (only hydrateThread sets it - no
    // notification carries it), so a live-arriving ask would otherwise read
    // as generic "new" content until the next snapshot.
    pillNeedsYou: pillVisible && (isAttentionWorthy(model) || askDockPending),
    pillError: errorAnchorIndex !== null,
    pillArrowDirection,
    jumpToBottom,
    captureViewAnchor,
    restoreViewAnchorAfterMeasurement,
  };
}
