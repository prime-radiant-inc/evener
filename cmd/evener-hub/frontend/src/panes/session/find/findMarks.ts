// Find's marks on the mounted transcript. Find never renders into transcript
// rows: it locates the entry a hit names among the rows the virtual list has
// mounted, flags the current one with an attribute the stylesheet outlines,
// and paints the matching words through the CSS Custom Highlight API, which
// styles ranges without touching the DOM React owns. Where that API is missing
// the outline alone marks the hit.
import { AT_BOTTOM_THRESHOLD_PX } from "../transcript/flow/scrollMetrics";
import type { FindHit } from "./findHits";

export const FIND_CURRENT_ATTRIBUTE = "data-find-current";
/** Marks the element of a message row that holds the message's own text. */
export const FIND_BODY_ATTRIBUTE = "data-message-body";
const HIGHLIGHT_ALL = "evener-find";
const HIGHLIGHT_CURRENT = "evener-find-current";
// A bound on painted ranges, so a one-letter query over a long mounted window
// cannot stall a frame; the count in the bar is unaffected.
const MAX_RANGES = 1500;

/** The mounted transcript as Find reads it, gathered in one pass: each
 * entry's own anchor and the span of rows the virtual list has mounted. */
interface MountedTranscript {
  entries: Map<string, HTMLElement>;
  firstRow: number;
  lastRow: number;
}

function readMounted(port: HTMLElement): MountedTranscript {
  const mounted: MountedTranscript = {
    entries: new Map(),
    firstRow: Number.POSITIVE_INFINITY,
    lastRow: Number.NEGATIVE_INFINITY,
  };
  for (const anchor of port.querySelectorAll<HTMLElement>("[data-view-anchor-id]")) {
    const row = Number(anchor.getAttribute("data-view-anchor-index"));
    if (Number.isFinite(row)) {
      mounted.firstRow = Math.min(mounted.firstRow, row);
      mounted.lastRow = Math.max(mounted.lastRow, row);
    }
    mounted.entries.set(anchor.getAttribute("data-view-anchor-id") ?? "", anchor);
  }
  return mounted;
}

type HighlightRegistry = { set(name: string, highlight: unknown): void; delete(name: string): void };
type HighlightView = Window & {
  CSS?: { highlights?: HighlightRegistry };
  Highlight?: new (...ranges: Range[]) => unknown;
};

// The window a transcript renders into: a popped-out session has its own
// document, and only that document's registry paints there.
function viewOf(port: HTMLElement | null): HighlightView | null {
  return (port?.ownerDocument.defaultView as HighlightView | null | undefined) ?? null;
}

function highlightRegistry(view: HighlightView | null): HighlightRegistry | null {
  return view?.Highlight === undefined ? null : (view.CSS?.highlights ?? null);
}

// An entry's text as Find matches it - lowercased, whitespace collapsed, what
// is hidden from readers left out - with, for each code unit, the text node
// and offset it came from. Lowercasing can change length ("İ" becomes two
// units) and a phrase can span elements (a colour run, a code block's line),
// so only this mapping says where a match sits on screen.
interface ReadText {
  text: string;
  nodes: Text[];
  nodeOf: number[];
  offsetOf: number[];
}

// Whether text inside `element` runs on from the text beside it, as formatting
// within a sentence does. Anything else - a paragraph, a list item, a code
// block's line - starts and ends its own words.
function runsOn(element: Element, known: Map<Element, boolean>): boolean {
  let inline = known.get(element);
  if (inline === undefined) {
    const display = (element.ownerDocument.defaultView ?? window).getComputedStyle(element).display;
    inline = display.startsWith("inline") || display === "contents";
    known.set(element, inline);
  }
  return inline;
}

function readText(element: HTMLElement): ReadText {
  const read: ReadText = { text: "", nodes: [], nodeOf: [], offsetOf: [] };
  const put = (unit: string, node: number, offset: number) => {
    read.text += unit;
    read.nodeOf.push(node);
    read.offsetOf.push(offset);
  };
  const known = new Map<Element, boolean>();
  // Set on entering a block or passing a line break since the last text.
  let broken = false;
  const walker = element.ownerDocument.createTreeWalker(element, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
    acceptNode: (node) =>
      node.nodeType === Node.ELEMENT_NODE && (node as Element).getAttribute("aria-hidden") === "true"
        ? NodeFilter.FILTER_REJECT
        : NodeFilter.FILTER_ACCEPT,
  });
  for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
    // By node type, not class: another window's elements are another realm's.
    if (node.nodeType === Node.ELEMENT_NODE) {
      const inner = node as Element;
      if (inner.tagName === "BR" || !runsOn(inner, known)) broken = true;
      continue;
    }
    const value = node.nodeValue ?? "";
    const previous = read.nodes.at(-1);
    const index = read.nodes.push(node as Text) - 1;
    // Leaving a block separates words too: the blocks the previous text sat
    // in that this text does not.
    for (
      let left = previous?.parentElement;
      !broken && left != null && left !== element && !left.contains(node);
      left = left.parentElement
    ) {
      if (!runsOn(left, known)) broken = true;
    }
    if (broken && previous !== undefined && !read.text.endsWith(" ")) put(" ", index, 0);
    broken = false;
    for (let offset = 0; offset < value.length; offset += 1) {
      const char = value.charAt(offset);
      if (/\s/.test(char)) {
        if (!read.text.endsWith(" ")) put(" ", index, offset);
        continue;
      }
      for (const unit of char.toLowerCase()) put(unit, index, offset);
    }
  }
  return read;
}

function rangesIn(element: HTMLElement, needle: string, budget: number): Range[] {
  const ranges: Range[] = [];
  const read = readText(element);
  for (
    let at = read.text.indexOf(needle);
    at >= 0 && ranges.length < budget;
    at = read.text.indexOf(needle, at + needle.length)
  ) {
    const last = at + needle.length - 1;
    const startNode = read.nodes[read.nodeOf[at] ?? -1];
    const endNode = read.nodes[read.nodeOf[last] ?? -1];
    if (startNode === undefined || endNode === undefined) continue;
    const range = element.ownerDocument.createRange();
    range.setStart(startNode, read.offsetOf[at] ?? 0);
    range.setEnd(endNode, (read.offsetOf[last] ?? 0) + 1);
    ranges.push(range);
  }
  return ranges;
}

// Highlight names are document-wide, so each transcript's ranges are kept
// apart here and a document's registrations hold the union of its
// transcripts': marking or clearing one leaves another's words painted.
const painted = new Map<HTMLElement, { all: Range[]; current: Range[] }>();

function register(view: HighlightView, registry: HighlightRegistry): void {
  const Painted = view.Highlight;
  if (Painted === undefined) return;
  const all: Range[] = [];
  const current: Range[] = [];
  for (const [port, ranges] of painted) {
    // A transcript that left its document takes its ranges with it.
    if (!port.isConnected) painted.delete(port);
    else if (port.ownerDocument.defaultView === view) {
      all.push(...ranges.all);
      current.push(...ranges.current);
    }
  }
  if (all.length > 0) registry.set(HIGHLIGHT_ALL, new Painted(...all));
  else registry.delete(HIGHLIGHT_ALL);
  if (current.length > 0) registry.set(HIGHLIGHT_CURRENT, new Painted(...current));
  else registry.delete(HIGHLIGHT_CURRENT);
}

interface MarkedTranscript {
  /** The current hit's entry, when mounted. */
  current: HTMLElement | null;
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
  const view = viewOf(port);
  const registry = highlightRegistry(view);
  const scope: ParentNode = port ?? document;
  let current: HTMLElement | null = null;
  const all: Range[] = [];
  const mine: Range[] = [];
  if (port && needle !== "") {
    const mounted = readMounted(port);
    for (const hit of hits) {
      // Hits far outside the mounted window cost nothing.
      if (hit.rowIndex < mounted.firstRow || hit.rowIndex > mounted.lastRow) continue;
      const entry = mounted.entries.get(hit.key);
      if (entry === undefined) continue;
      const isCurrent = hit.key === currentKey;
      if (isCurrent) current = entry;
      if (!registry) continue;
      // A message row also carries its speaker and time; only what the
      // message says is searched, so only that is painted and scrolled to.
      const body = entry.querySelector<HTMLElement>(`[${FIND_BODY_ATTRIBUTE}]`) ?? entry;
      const found = rangesIn(body, needle, MAX_RANGES - all.length - mine.length);
      (isCurrent ? mine : all).push(...found);
    }
  }
  for (const marked of scope.querySelectorAll(`[${FIND_CURRENT_ATTRIBUTE}]`)) {
    if (marked !== current) marked.removeAttribute(FIND_CURRENT_ATTRIBUTE);
  }
  current?.setAttribute(FIND_CURRENT_ATTRIBUTE, "");
  if (registry && port) {
    if (all.length > 0 || mine.length > 0) painted.set(port, { all, current: mine });
    else painted.delete(port);
    if (view) register(view, registry);
  }
  return { current, focus: mine[0] ?? null };
}

/** Where the scroll port should sit so the current hit reads comfortably: a
 * third of the way down, or untouched when it is already well in view. Null
 * means leave the port alone. */
function scrollTopRevealing(port: HTMLElement, target: DOMRect): number | null {
  const view = port.getBoundingClientRect();
  if (view.height <= 0) return null;
  const top = target.top - view.top;
  const bottom = target.bottom - view.top;
  if (top >= view.height * 0.1 && bottom <= view.height * 0.8) return null;
  return Math.max(0, port.scrollTop + top - view.height * 0.3);
}

/** Where the scroll port should sit for a landing on `target`, or null to
 * leave it alone: the match a third of the way down when it is not already
 * well in view, and in every case measurably off the live end. A reader
 * within the end threshold is still at the end to the virtual list, which
 * follows appended output there whatever the scroll owner has been told; one
 * who chose a place to read has to be off the end for later output to leave
 * them on it. */
export function landingScrollTop(port: HTMLElement, target: DOMRect): number | null {
  const end = port.scrollHeight - port.clientHeight;
  const reveal = scrollTopRevealing(port, target);
  const offEnd = end - 2 * AT_BOTTOM_THRESHOLD_PX;
  // With too little to scroll the whole conversation is on screen and there
  // is no end to step off.
  const atEnd = offEnd >= 0 && end - Math.min(reveal ?? port.scrollTop, end) <= AT_BOTTOM_THRESHOLD_PX;
  const top = atEnd ? offEnd : reveal;
  return top === port.scrollTop ? null : top;
}
