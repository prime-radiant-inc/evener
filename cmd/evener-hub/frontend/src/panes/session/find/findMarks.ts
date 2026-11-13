// Find's marks on the mounted transcript. Find never renders into transcript
// rows: it locates the entry a hit names among the rows the virtual list has
// mounted, flags the current one with an attribute the stylesheet outlines,
// and paints the matching words through the CSS Custom Highlight API, which
// styles ranges without touching the DOM React owns. Where that API is missing
// the outline alone marks the hit.
import type { FindHit } from "./findHits";

export const FIND_CURRENT_ATTRIBUTE = "data-find-current";
const HIGHLIGHT_ALL = "evener-find";
const HIGHLIGHT_CURRENT = "evener-find-current";
// A bound on painted ranges, so a one-letter query over a long mounted window
// cannot stall a frame; the count in the bar is unaffected.
const MAX_RANGES = 1500;

interface LocatedHit {
  element: HTMLElement;
  /** False when the element is the closed fold standing in for the entry. */
  exact: boolean;
}

/** The mounted transcript as Find reads it, gathered in one pass: each
 * entry's own anchor, each tool call inside an open run, the fold standing in
 * for entries it hides, and the span of rows the virtual list has mounted. */
interface MountedTranscript {
  entries: Map<string, HTMLElement>;
  calls: Map<string, HTMLElement>;
  folds: Map<string, HTMLElement>;
  firstRow: number;
  lastRow: number;
}

function readMounted(port: HTMLElement): MountedTranscript {
  const mounted: MountedTranscript = {
    entries: new Map(),
    calls: new Map(),
    folds: new Map(),
    firstRow: Number.POSITIVE_INFINITY,
    lastRow: Number.NEGATIVE_INFINITY,
  };
  for (const anchor of port.querySelectorAll<HTMLElement>("[data-view-anchor-id]")) {
    const row = Number(anchor.getAttribute("data-view-anchor-index"));
    if (Number.isFinite(row)) {
      mounted.firstRow = Math.min(mounted.firstRow, row);
      mounted.lastRow = Math.max(mounted.lastRow, row);
    }
    const members = anchor.getAttribute("data-view-anchor-members");
    if (members === null) {
      mounted.entries.set(anchor.getAttribute("data-view-anchor-id") ?? "", anchor);
      continue;
    }
    for (const id of members.split(",")) mounted.folds.set(id, anchor);
    for (const call of anchor.querySelectorAll<HTMLElement>("[data-item-id]")) {
      mounted.calls.set(call.getAttribute("data-item-id") ?? "", call);
    }
  }
  return mounted;
}

// The mounted element for a hit: the entry's own anchor, the tool call inside
// an open run, or - while its fold is still closed - the fold.
function locate(mounted: MountedTranscript, hit: FindHit): LocatedHit | null {
  const fold = mounted.folds.get(hit.key) ?? mounted.folds.get(hit.itemId);
  if (fold !== undefined) {
    const call = mounted.calls.get(hit.itemId);
    return call !== undefined && fold.contains(call) ? { element: call, exact: true } : { element: fold, exact: false };
  }
  const entry = mounted.entries.get(hit.key);
  return entry === undefined ? null : { element: entry, exact: true };
}

function highlightRegistry(): { set(name: string, highlight: unknown): void; delete(name: string): void } | null {
  if (typeof CSS === "undefined" || typeof Highlight === "undefined") return null;
  return (CSS as unknown as { highlights?: ReturnType<typeof highlightRegistry> }).highlights ?? null;
}

// Lowercased text with, for each of its code units, the offset in the
// original it came from. Lowercasing can change length ("İ" becomes two
// units), so an offset found in the lowercased text is not an offset on screen.
function lowered(text: string): { text: string; origin: (offset: number) => number } {
  const whole = text.toLowerCase();
  if (whole.length === text.length) return { text: whole, origin: (offset) => offset };
  const origins: number[] = [];
  let out = "";
  for (let index = 0; index < text.length; index += 1) {
    const piece = text.charAt(index).toLowerCase();
    out += piece;
    for (let unit = 0; unit < piece.length; unit += 1) origins.push(index);
  }
  return { text: out, origin: (offset) => origins[offset] ?? text.length };
}

function rangesIn(element: HTMLElement, needle: string, budget: number): Range[] {
  const ranges: Range[] = [];
  const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node !== null && ranges.length < budget; node = walker.nextNode()) {
    const { text, origin } = lowered(node.nodeValue ?? "");
    for (
      let at = text.indexOf(needle);
      at >= 0 && ranges.length < budget;
      at = text.indexOf(needle, at + needle.length)
    ) {
      const range = document.createRange();
      range.setStart(node, origin(at));
      range.setEnd(node, origin(at + needle.length - 1) + 1);
      ranges.push(range);
    }
  }
  return ranges;
}

interface MarkedTranscript {
  /** The current hit's element, when mounted. */
  current: LocatedHit | null;
  /** Where the current hit's first painted match sits, when it has one. */
  focus: Range | null;
}

/** Marks the mounted hits and returns where the current one is. `needle` is
 * the shared find needle; an empty one clears every mark. */
export function markTranscript(
  port: HTMLElement | null,
  hits: readonly FindHit[],
  currentKey: string | null,
  needle: string,
): MarkedTranscript {
  const registry = highlightRegistry();
  const scope: ParentNode = port ?? document;
  let current: LocatedHit | null = null;
  const all: Range[] = [];
  const mine: Range[] = [];
  if (port && needle !== "") {
    const mounted = readMounted(port);
    for (const hit of hits) {
      // Hits far outside the mounted window cost nothing.
      if (hit.rowIndex < mounted.firstRow || hit.rowIndex > mounted.lastRow) continue;
      const located = locate(mounted, hit);
      if (!located) continue;
      const isCurrent = hit.key === currentKey;
      if (isCurrent) current = located;
      if (!registry || !located.exact) continue;
      const found = rangesIn(located.element, needle, MAX_RANGES - all.length - mine.length);
      (isCurrent ? mine : all).push(...found);
    }
  }
  for (const marked of scope.querySelectorAll(`[${FIND_CURRENT_ATTRIBUTE}]`)) {
    if (marked !== current?.element) marked.removeAttribute(FIND_CURRENT_ATTRIBUTE);
  }
  current?.element.setAttribute(FIND_CURRENT_ATTRIBUTE, current.exact ? "entry" : "fold");
  if (registry) {
    if (all.length > 0) registry.set(HIGHLIGHT_ALL, new Highlight(...all));
    else registry.delete(HIGHLIGHT_ALL);
    if (mine.length > 0) registry.set(HIGHLIGHT_CURRENT, new Highlight(...mine));
    else registry.delete(HIGHLIGHT_CURRENT);
  }
  return { current, focus: mine[0] ?? null };
}

/** Where the scroll port should sit so the current hit reads comfortably: a
 * third of the way down, or untouched when it is already well in view. Null
 * means leave the port alone. */
export function scrollTopRevealing(port: HTMLElement, target: DOMRect): number | null {
  const view = port.getBoundingClientRect();
  if (view.height <= 0) return null;
  const top = target.top - view.top;
  const bottom = target.bottom - view.top;
  if (top >= view.height * 0.1 && bottom <= view.height * 0.8) return null;
  return Math.max(0, port.scrollTop + top - view.height * 0.3);
}
