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

function highlightRegistry(): { set(name: string, highlight: unknown): void; delete(name: string): void } | null {
  if (typeof CSS === "undefined" || typeof Highlight === "undefined") return null;
  return (CSS as unknown as { highlights?: ReturnType<typeof highlightRegistry> }).highlights ?? null;
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
    const display = getComputedStyle(element).display;
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
  const walker = document.createTreeWalker(element, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
    acceptNode: (node) =>
      node instanceof Element && node.getAttribute("aria-hidden") === "true"
        ? NodeFilter.FILTER_REJECT
        : NodeFilter.FILTER_ACCEPT,
  });
  for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
    if (node instanceof Element) {
      if (node.tagName === "BR" || !runsOn(node, known)) broken = true;
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
    const range = document.createRange();
    range.setStart(startNode, read.offsetOf[at] ?? 0);
    range.setEnd(endNode, (read.offsetOf[last] ?? 0) + 1);
    ranges.push(range);
  }
  return ranges;
}

// Highlight names are document-wide, so each transcript's ranges are kept
// apart here and the registrations hold their union: marking or clearing one
// transcript leaves another's words painted.
const painted = new Map<HTMLElement, { all: Range[]; current: Range[] }>();

function register(registry: NonNullable<ReturnType<typeof highlightRegistry>>): void {
  // A transcript that left the document takes its ranges with it.
  for (const port of painted.keys()) if (!port.isConnected) painted.delete(port);
  const all = [...painted.values()].flatMap((ranges) => ranges.all);
  const current = [...painted.values()].flatMap((ranges) => ranges.current);
  if (all.length > 0) registry.set(HIGHLIGHT_ALL, new Highlight(...all));
  else registry.delete(HIGHLIGHT_ALL);
  if (current.length > 0) registry.set(HIGHLIGHT_CURRENT, new Highlight(...current));
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
  const registry = highlightRegistry();
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
      const found = rangesIn(entry, needle, MAX_RANGES - all.length - mine.length);
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
    register(registry);
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
