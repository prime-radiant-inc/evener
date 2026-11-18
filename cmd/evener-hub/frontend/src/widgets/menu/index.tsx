import {
  type FocusEvent,
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { LayerFocusScope } from "../focusscope";
import { requireClass } from "../internal/requireClass";
import { useDismissOnOutsidePress } from "../internal/useDismissOnOutsidePress";
import { useOwnedDocument } from "../internal/useOwnedDocument";
import { usePresence } from "../internal/usePresence";
import { watchViewport } from "../internal/viewportWatch";
// Flip-then-clamp overlay placement, shared verbatim with the Popover
// primitive (§3.4). computeMenuPosition/resolveAxis/EDGE_MARGIN/TRIGGER_GAP
// were extracted from here into widgets/popover/computePosition.ts so Menu
// and Popover position identically; the aliases keep every call site below
// byte-identical to the pre-extraction Menu.
import {
  computePopoverPosition as computeMenuPosition,
  EDGE_MARGIN,
  type PopoverPosition as MenuPosition,
  TRIGGER_GAP,
  transformOriginFor,
} from "../popover/computePosition";
import styles from "./menu.module.css";

export interface MenuItem {
  id: string;
  label: string;
  onSelect: () => void;
  disabled?: boolean;
}

export interface MenuSeparator {
  kind: "separator";
  id: string;
}

export type MenuEntry = MenuItem | MenuSeparator;

export function isSeparator(entry: MenuEntry): entry is MenuSeparator {
  return "kind" in entry && entry.kind === "separator";
}

// Actionable = keyboard-reachable: separators and disabled items are both
// skipped by roving navigation, so every index helper below uses this one
// predicate instead of reading .disabled directly.
function isActionable(entry: MenuEntry): boolean {
  return !isSeparator(entry) && !entry.disabled;
}

export type MenuVariant = "default" | "quiet";

/** The trigger's content: visible content that also names it, or an
 * icon-only trigger whose accessible name is `label`. The icon-only form is
 * square at IconButton's sm size (28px) with the same phone tap floor, and
 * its glyph is hidden from assistive tech. */
export type MenuTriggerContent =
  | {
      /** Visible content of the trigger button Menu renders (its own aria and
       * open/close wiring - see this task's report for why the trigger isn't
       * the imported Button widget). */
      trigger: ReactNode;
      icon?: never;
      label?: never;
    }
  | { icon: ReactNode; label: string; trigger?: never };

export type MenuProps = MenuTriggerContent & {
  items: MenuEntry[];
  /** Forwarded to the trigger button's own tabIndex. Omitted (the default)
   * leaves the button's native tabIndex (0) untouched - every existing
   * consumer of this widget is unaffected. Pass -1 when Menu is nested
   * inside another widget that already owns its own single Tab stop via
   * roving tabindex (e.g. a tree row - see shell/rail/RailRow.tsx): without
   * this, the trigger becomes an ADDITIONAL always-focusable Tab stop on
   * every row simultaneously, which breaks the "exactly one Tab stop at a
   * time" contract that roving tabindex depends on. The trigger stays
   * reachable by click either way; -1 only removes it from sequential Tab
   * navigation, not from focus entirely (see handleTriggerKeyDown's own
   * stopPropagation for the other half of that same containment - a
   * consumed key must not also reach the ancestor that owns Tab order). */
  triggerTabIndex?: number;
  /** "quiet" drops the trigger's border for a "ghost" icon-only look: no
   * border at rest (the base trigger's rest background is already
   * transparent, for every variant), the same hover wash the default
   * trigger already has, plus a persistent highlight while its own popup is
   * open - the one affordance this variant keeps, having given up the
   * border (see menu.module.css's own .triggerQuiet). For a trigger nested
   * in a dense row alongside other quiet/borderless controls (a rail row's
   * actions, a session's status row - see shell/rail/RailRow.tsx and
   * SessionActionsMenu.tsx), the default's permanent bordered pill reads as
   * visually loud. Omitted (the default) keeps every existing consumer's
   * look unchanged. */
  variant?: MenuVariant;
};

const CLASS = {
  root: requireClass(styles.root, "menu.module.css", "root"),
  trigger: requireClass(styles.trigger, "menu.module.css", "trigger"),
  triggerQuiet: requireClass(styles.triggerQuiet, "menu.module.css", "triggerQuiet"),
  triggerIcon: requireClass(styles.triggerIcon, "menu.module.css", "triggerIcon"),
  triggerGlyph: requireClass(styles.triggerGlyph, "menu.module.css", "triggerGlyph"),
  popup: requireClass(styles.popup, "menu.module.css", "popup"),
  popupNarrow: requireClass(styles.popupNarrow, "menu.module.css", "popupNarrow"),
  item: requireClass(styles.item, "menu.module.css", "item"),
  itemDisabled: requireClass(styles.itemDisabled, "menu.module.css", "itemDisabled"),
  separator: requireClass(styles.separator, "menu.module.css", "separator"),
};

// The nearest ancestor that scrolls the trigger itself, if any: a rail list, a
// pane's scroller. That box is where content can be inserted above the trigger
// and slide it down without changing any box's size.
function isScrollContainer(element: Element, ownerWindow: Window): boolean {
  const overflowY = ownerWindow.getComputedStyle(element).overflowY;
  return overflowY === "auto" || overflowY === "scroll";
}

function scrollContainerOf(element: HTMLElement, ownerWindow: Window): HTMLElement | null {
  const view = ownerWindow;
  for (let ancestor = element.parentElement; ancestor !== null; ancestor = ancestor.parentElement) {
    if (isScrollContainer(ancestor, view)) return ancestor;
  }
  return null;
}

// The outermost ancestor inside the body: everything in normal flow that could
// push the trigger down, and nothing in the body beside it (a portaled sibling
// subtree is not laid out against this trigger).
function topLevelWrapperOf(element: HTMLElement, ownerDocument: Document): HTMLElement {
  let wrapper = element;
  while (wrapper.parentElement !== null && wrapper.parentElement !== ownerDocument.body) {
    wrapper = wrapper.parentElement;
  }
  return wrapper;
}

// The popup's own min-width in menu.module.css. Below this the stylesheet's
// floor would override the visible-viewport cap, so the floor yields instead
// and the labels wrap.
const NARROW_POPUP_WIDTH = 180;

// `offsetWidth` reports whole pixels while the cap it is compared against can
// be fractional (a pinch zoom at 2.4x leaves 180.25). A popup within this
// distance of its cap is at the cap.
const CAP_ROUNDING = 1;
// How far a browser's corrected offset may land from the whole-pixel endpoint
// `scrollHeight - clientHeight` describes and still be that correction. Layout
// endpoints can be fractional while those two properties report integers.
const CLAMP_ROUNDING = 1;
// The same pixel, for the focused row's own metrics: `offsetTop` reports whole
// pixels while `scrollTop` does not, so the browser's fractional anchoring
// adjustment and the visible interval it leaves arrive a fraction apart. A
// difference within this distance is the reading, not the view.
const CLIP_ROUNDING = 1;

function firstEnabledIndex(items: MenuEntry[]): number {
  return items.findIndex(isActionable);
}

/** Scroll `popup` the least that brings `row` back inside its scrollport, and
 * nothing at all when the row is already visible.
 *
 * The popup's own scrollTop is the only thing touched. scrollIntoView would
 * answer the same question through geometry, but on a pinch-zoomed page a row's
 * position is measured against the VISUAL viewport, so it can pan the page -
 * and this runs before the pass that writes the popup's new coordinates, so the
 * pan would head for where the popup used to be. Arithmetic in the popup's own
 * scroll space has no such reach. */
function revealRow(popup: HTMLElement, row: HTMLElement): void {
  const rowTop = row.offsetTop;
  const rowBottom = rowTop + row.offsetHeight;
  const portHeight = popup.offsetHeight;
  if (rowTop < popup.scrollTop) popup.scrollTop = rowTop;
  else if (rowBottom > popup.scrollTop + portHeight) {
    // A row taller than the scrollport cannot be shown whole, and the least
    // scroll that puts its END inside takes its BEGINNING out - the label of
    // the action the reader's Enter would invoke. Prefer the start and let
    // them scroll the rest, which is the direction they would read it anyway.
    popup.scrollTop = row.offsetHeight > portHeight ? rowTop : rowBottom - portHeight;
  }
  // The same question on the other axis. A zoomed-in viewport can cap the popup
  // narrower than a glyph, so the row's own text runs past its right edge and
  // the vertical scroll above cannot reach it. The popup's scroll space is the
  // only thing moved, for the same reason it is on the vertical axis: this runs
  // before placement writes the popup's new coordinates, and scrollIntoView
  // would pan the page toward where it used to be.
  const rowLeft = row.offsetLeft;
  const rowRight = rowLeft + row.offsetWidth;
  const portWidth = popup.offsetWidth;
  // Start, not end, when the row is wider than the popup - the same choice the
  // vertical branch makes, and for the same reason: the beginning of the
  // action's label is what the reader is reading toward.
  if (rowRight > popup.scrollLeft + portWidth) {
    popup.scrollLeft = row.offsetWidth > portWidth ? rowLeft : rowRight - portWidth;
  } else if (rowLeft < popup.scrollLeft) popup.scrollLeft = rowLeft;
}

/** What the reveal question is asked of: the row's place in the popup's scroll
 * space, and the height of the scrollport that decides whether it fits.
 *
 * One recorded observation, because there is one question - is the focused row
 * inside the popup's visible area, and did layout put it outside - and every
 * other fact this widget can observe (which ids the list holds, whether the
 * popup's own box changed, whether the focused id vanished) is a proxy for it.
 * Each proxy acquired a case the others did not cover: a row inserted above
 * moves the row without changing the popup's box, a label that wraps moves it
 * without changing any id, and a shrink moves the scrollport under a row that
 * never moved at all.
 *
 * The scrollport's OFFSET is deliberately not part of it: scrollTop is the
 * reader's, and a row they scrolled away from is not a row layout hid. */
interface RowObservation {
  top: number;
  height: number;
  width: number;
  portHeight: number;
  portWidth: number;
  /** The popup's scroll offset when this look was taken. Each observation is
   * read against its own offset: a clamp moves the whole list under a still
   * viewport, and judging the old frame at the new offset invents clipping
   * that no layout produced. */
  scrolledTo: number;
}

const observeRow = (popup: HTMLElement, row: HTMLElement): RowObservation => ({
  top: row.offsetTop,
  height: row.offsetHeight,
  width: row.offsetWidth,
  portHeight: popup.offsetHeight,
  portWidth: popup.offsetWidth,
  scrolledTo: popup.scrollTop,
});

/** What one look at a scroller saw: how far it was scrolled, and where the
 * trigger sat inside its content. */
interface ScrollerRecord {
  scrollTop: number;
  /** The trigger's place in the scroller's CONTENT, not in the viewport: the
   * trigger's rect top minus the scroller's own, with the offset added back. A
   * scroller's screen position does not move while its own content scrolls, so
   * a difference between two of these is a difference the content made. */
  contentTop: number;
  /** Rows inside the same scroller, and where each sat in the content. The
   * trigger alone cannot tell a reader's drag from the browser holding a
   * DIFFERENT row still: when a live row changes bands with both bands still
   * populated and no height changed, the trigger's own content position does
   * not move at all, and the browser corrects the offset around whichever row
   * it anchored. A row the browser held still moves in the content by exactly
   * the offset it gave back.
   *
   * A record of several, because which row that is belongs to the browser: a
   * heading, a row of either band, or the first row of the block the reader's
   * view starts in can each be the anchor, and only the one the browser
   * actually held can explain the offset. */
  witnesses: WitnessPosition[];
}

/** Where `triggerEl` sits inside `scroller`'s content: the difference between
 * two readings is what the content above the trigger did, with the reader's own
 * scroll taken back out. */
function contentTopOf(triggerEl: HTMLElement, scroller: Element): number {
  return triggerEl.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop;
}

/** How many visible rows are asked whether the browser held them still. The
 * anchor is always near the top of the visible block, so the scan's first few
 * boxed leaves cover it; the cap keeps a long transcript from turning every
 * classified scroll into a walk of its whole subtree. */
const WITNESS_LIMIT = 8;

/** Rows the browser could have anchored on: the INNERMOST VISIBLE boxed
 * elements in the scroller, outside the trigger's subtree.
 *
 * Inner-most rather than first-with-a-box, because the two differ exactly where
 * this matters. A section wrapper around the rows - the rail's Live band - has a
 * box, but its top does not move when a session changes bands INSIDE it, so a
 * witness taken there reports that nothing happened while the rows under it
 * moved. The innermost box is inside a row, and anything inside a row moves
 * with that row. A box that is glued to the viewport (fixed or sticky) does not
 * move with the content either, so it is not a witness; those are rare enough
 * in a scroller that skipping them is cheap.
 *
 * VISIBLE, because a heading of the same section is a boxed leaf too and sits
 * first in document order. A heading the reader has scrolled past keeps its
 * place in the content when a live row changes bands beneath it, so a witness
 * taken there reports that nothing happened while the browser corrects the
 * offset around the row it is actually looking at. Only a box inside the
 * scrollport can be the row the browser held still.
 *
 * A SET rather than one, because which row the browser anchors on is its
 * choice, not this widget's. Asking them all and accepting the first that
 * accounts for the offset finds whatever the browser actually held, where
 * betting on a single box keeps selecting a row the update happened to leave
 * alone.
 *
 * A GROUP THAT HOLDS THE TRIGGER is descended into rather than skipped. In the
 * rail the trigger is a button on a session row INSIDE the Live band, so the
 * band's wrapper contains both the trigger and every row the browser could have
 * anchored - skipping the wrapper would leave the section that holds the
 * trigger with no witness at all, which is every menu in the rail. The
 * trigger's own subtree is still excluded, one element at a time, because the
 * trigger's row moves with the trigger and a witness that always moves with it
 * can never explain anything. */
function witnessesIn(scroller: Element, triggerEl: HTMLElement, ownerWindow: Window): Element[] {
  const port = scroller.getBoundingClientRect();
  // The visible band in the same viewport coordinates the candidates report.
  // A scroller that resolves no height at all (jsdom, an unstubbed fixture)
  // leaves the band empty, and then no box can be shown to be in view.
  const portTop = port.top;
  const portBottom = port.top + (scroller.clientHeight || port.height);
  const found: Element[] = [];
  // Walked one ROW GROUP at a time, and a group whose box is outside the
  // scrollport is skipped before any of its descendants is looked at. A rail
  // scrolled to the bottom holds hundreds of rows the reader cannot see, and
  // enumerating all of them to reject them one by one reads geometry for every
  // row in the list on every placement. The group's own box answers the same
  // question at the cost of one read per group.
  const groups = Array.from(scroller.children);
  for (const group of groups) {
    if (found.length >= WITNESS_LIMIT) break;
    if (triggerEl.contains(group)) continue;
    const groupRect = group.getBoundingClientRect();
    if (groupRect.height > 0 && (groupRect.bottom <= portTop || groupRect.top >= portBottom)) continue;
    // The group ITSELF counts: in a flat list each row is a direct child of the
    // scroller, and skipping down to its descendants would put no row in the
    // set at all.
    for (const node of [group, ...Array.from(group.querySelectorAll("*"))]) {
      if (found.length >= WITNESS_LIMIT) break;
      if (node.contains(triggerEl) || triggerEl.contains(node)) continue;
      const rect = node.getBoundingClientRect();
      if (rect.height <= 0) continue;
      // Above the scrollport's top edge, or below its bottom: the reader cannot
      // see it, so it is not what the browser anchored the view on.
      if (rect.bottom <= portTop || rect.top >= portBottom) continue;
      // A box with a boxed descendant is a container, and the movement this
      // looks for happens BELOW it. Descending as far as the boxes go lands
      // inside a row, where the movement is visible.
      const inner = Array.from(node.querySelectorAll("*")).some(
        (child) => !child.contains(triggerEl) && child.getBoundingClientRect().height > 0,
      );
      if (inner) continue;
      const position = ownerWindow.getComputedStyle(node).position;
      if (position === "fixed" || position === "sticky") continue;
      found.push(node);
    }
  }
  return found;
}

/** One look at a scroller: its offset, the trigger's place in the content, and
 * the places of the rows the browser could be anchoring on. All positions are
 * content coordinates, so a difference between two looks is something the
 * CONTENT did. */
interface WitnessPosition {
  node: Element;
  top: number;
}

function recordFor(triggerEl: HTMLElement, scroller: Element, ownerWindow: Window): ScrollerRecord {
  // The set is taken fresh on EVERY look, including the rows that have just
  // become visible. A set of survivors only goes stale in a way that defeats
  // its purpose: a live list replaces rows as fast as it reorders them, so
  // after a few updates every recorded element can be one the browser no
  // longer looks at, and the row it IS holding has never been in the set at
  // all. The next adjustment then has no element that moved with it, and the
  // menu closes on an update nobody caused.
  //
  // Comparing two looks is still safe, because the positions compared are the
  // ones the EARLIER record captured: an element is only used when it was
  // recorded then and is still connected now.
  const witnesses: WitnessPosition[] = witnessesIn(scroller, triggerEl, ownerWindow).map((node) => ({
    node,
    top: contentTopOf(node as HTMLElement, scroller),
  }));
  return {
    scrollTop: scroller.scrollTop,
    contentTop: contentTopOf(triggerEl, scroller),
    witnesses,
  };
}

/** Re-record where the trigger sits inside every scroller that holds it, for the
 * scrollers whose offset has not moved since the last look.
 *
 * A placement pass follows a LAYOUT change rather than a scroll. When a live
 * row arrives inside the visible area the trigger moves deeper into the content
 * while the offset does not move at all, so the recorded position is stale from
 * the moment the menu re-anchors around it. A later anchoring adjustment then
 * measures its offset step against a content position that already includes
 * the earlier shift, the two no longer cancel, and the menu closes on an update
 * the reader did not cause.
 *
 * A scroller whose offset HAS moved is left exactly as it was: that movement is
 * a scroll this widget has not classified yet, and overwriting the offset here
 * would erase the only evidence of what the scroll did - a clamp or an
 * anchoring adjustment would then read as the reader leaving. */
function refreshScrollerBaselines(
  triggerEl: HTMLElement,
  ownerWindow: Window,
  records: WeakMap<Element, ScrollerRecord>,
): void {
  for (let node: HTMLElement | null = triggerEl; node !== null; node = node.parentElement) {
    if (!isScrollContainer(node, ownerWindow)) continue;
    const recorded = records.get(node);
    if (recorded !== undefined && recorded.scrollTop !== node.scrollTop) continue;
    records.set(node, recordFor(triggerEl, node, ownerWindow));
  }
}

/** What a scroller that holds the trigger just did, read from its own numbers.
 *
 * A `scroll` from it has three causes, and only the last one takes the trigger
 * away from the reader:
 *
 * - a CLAMP: the offset the last look saw pointed past an end that then moved
 *   up under it, and what is left is exactly the new maximum. The viewport or a
 *   container grew, with no reader behind it. Nothing about the window is
 *   needed, which is what makes this catch a container that grew while
 *   `innerHeight` stayed put.
 * - an ANCHORING adjustment: content arrived above the trigger, so the browser
 *   moved the offset by the same amount it moved the trigger deeper into the
 *   content. That is what holds the reader's view still while a live list grows
 *   above them, and the trigger is exactly where they left it.
 * - a SCROLL: the offset moved and the trigger's place in the content did not,
 *   which is the trigger sliding out from under a popup fixed in the viewport's
 *   coordinates. That is the reader leaving.
 *
 * The first two are a reader's scroll with nobody behind them, and both are
 * read from the geometry rather than trusted from the event. A reader who
 * scrolls up lands anywhere below the maximum, and one who scrolls to the end
 * lands ON the maximum from an offset that was already legal - so `previous >
 * maximum` is what separates a clamp from a reader. Each call records what it
 * saw, so the next one compares against it. */
function classifyScrollerScroll(
  scroller: Element,
  triggerEl: HTMLElement | null,
  records: WeakMap<Element, ScrollerRecord>,
): "clamp" | "anchoring" | "scrolled" {
  if (triggerEl === null) return "scrolled";
  const now = scroller.scrollTop;
  const previous = records.get(scroller);
  const contentTop = contentTopOf(triggerEl, scroller);
  records.set(scroller, recordFor(triggerEl, scroller, scroller.ownerDocument.defaultView ?? window));
  if (previous === undefined) return "scrolled";
  const maximum = scroller.scrollHeight - scroller.clientHeight;
  // The browser may land a correction a fraction short of the endpoint the
  // whole-pixel `scrollHeight - clientHeight` describes: 199.5 against 200 is a
  // clamp, and an exact equality calls it a reader's scroll and closes the
  // menu. The rounding is allowed only within a pixel - and only as the SECOND
  // half of the reading. The first half still has to hold: the offset this
  // scroller last reported pointed PAST the range it now allows. A reader who
  // scrolls to the end arrives from an offset that was already legal, so the
  // tolerance cannot turn an ordinary scroll into a correction.
  if (previous.scrollTop > maximum && Math.abs(now - maximum) <= CLAMP_ROUNDING) return "clamp";
  const moved = now - previous.scrollTop;
  const contentMoved = contentTop - previous.contentTop;
  // The offset moved, in the same direction as the trigger's place in the
  // content, by exactly as much. Nothing but an insertion or removal above the
  // trigger moves the two together like that: a reader's drag moves the offset
  // and leaves the content alone, so `contentMoved` is zero. This one reading
  // needs no other witness - the equality IS the statement that the content
  // above the trigger changed by what the reader did not scroll.
  //
  // Both directions count, because a rail both gains and loses rows. A session
  // arriving above the anchor grows the offset; one LEAVING above it shrinks
  // the offset by the height that went, and slides the rest of the content up
  // with it. The reader is sitting still in both, and in both the trigger is
  // where they left it.
  //
  // UNEQUAL is not evidence on its own, and this is the trap: a row arriving
  // BETWEEN the anchor and the trigger grows the trigger's content position by
  // its height without asking the browser for any correction at all, so a
  // reader who scrolled 12px in the same moment shows 12 of offset against 40
  // of content movement - larger, same direction, and nothing to do with
  // anchoring. The difference has to be paid for by the row the browser
  // actually held, which is what the witness set below is for.
  if (moved !== 0 && Math.abs(contentMoved - moved) <= CLAMP_ROUNDING) return "anchoring";
  // The trigger cannot always say so. A Live session moving from Idle to
  // Working crosses the row the browser anchored on: both bands stay populated,
  // the moved row keeps its height, and the trigger's own place in the content
  // does not change at all - while the browser still corrects the offset to
  // hold the row it anchored. The rows that were VISIBLE in the same scroller
  // answer the same question from where the browser is actually looking: the
  // one it held still moved in the content by exactly the offset it gave back.
  // A reader's drag moves them with the content, by that same amount and no
  // explanation, so this cannot be read from a scroll they made.
  //
  // ANY of them, because the anchor is the browser's pick: it holds the row the
  // reader's view starts at, which can be a heading, a row of the band above,
  // or the first row that stayed put while a neighbour changed bands. Requiring
  // a particular one to be the anchor asks this widget to guess; one row moving
  // by EXACTLY the offset is the statement that the browser held THAT row. At
  // least as much is not the same statement: an insertion of its own moves a
  // row below it by more than the offset, and accepting that would let any
  // live update excuse any scroll the reader makes at the same moment.
  //
  // A witness the update removed has no position to report, and a detached
  // element's rect is zeros - which would read as a huge movement in whichever
  // direction the numbers happened to fall. It says nothing, so it is skipped.
  for (const witness of previous.witnesses) {
    if (!witness.node.isConnected) continue;
    const witnessMoved = contentTopOf(witness.node as HTMLElement, scroller) - witness.top;
    if (witnessMoved === 0) continue;
    // Within a pixel, because a rect can be fractional while the offset the
    // browser corrected is whole. Anything larger is a row that moved for its
    // own reasons and says nothing about where the browser's view sits.
    if (moved !== 0 && Math.abs(witnessMoved - moved) <= CLAMP_ROUNDING) return "anchoring";
  }
  return "scrolled";
}

function itemAt(items: MenuEntry[], i: number): MenuEntry {
  const item = items[i];
  if (!item) throw new Error(`menu item index ${i} out of range for ${items.length} items`);
  return item;
}

function lastEnabledIndex(items: MenuEntry[]): number {
  for (let i = items.length - 1; i >= 0; i--) {
    if (isActionable(itemAt(items, i))) return i;
  }
  return -1;
}

// Next enabled index stepping by `delta` (+1/-1) from `from`, wrapping
// around the ends. Returns `from` unchanged if every item is disabled.
function stepEnabledIndex(items: MenuEntry[], from: number, delta: 1 | -1): number {
  const n = items.length;
  if (n === 0) return -1;
  let i = from;
  for (let step = 0; step < n; step++) {
    i = (i + delta + n) % n;
    if (isActionable(itemAt(items, i))) return i;
  }
  return from;
}

// MenuPosition, EDGE_MARGIN, TRIGGER_GAP, resolveAxis, and computeMenuPosition
// now live in widgets/popover/computePosition.ts (imported above), shared with
// the Popover primitive - see that module and this file's import comment.

/**
 * Trigger + popup menu: click or Enter/Space/ArrowDown/ArrowUp on the
 * trigger opens it; ArrowUp/Down/Home/End rove focus among items (skipping
 * disabled ones), Enter/Space activates the focused item, Escape or an
 * outside click closes it. Built on FocusScope with trap=true - while open,
 * Tab/Shift+Tab cycle within the menu rather than leaving it (this widget
 * has exactly one Tab stop at a time via roving tabindex, so in practice
 * Tab is a no-op that leaves focus on the same item; arrow keys are the
 * intended navigation, matching the task's "typeahead NOT required" YAGNI
 * call for the rest of this widget's scope). No typeahead.
 *
 * The popup itself renders through a portal to the body of the document the
 * menu lives in (the opener's; a Dockview popout's when mounted there), position:
 * fixed off the trigger's own getBoundingClientRect() (see
 * computeMenuPosition above), instead of as a normal absolutely-positioned
 * child - a Menu nested anywhere with a clipping ancestor (a rail row's own
 * scrollable list, a status row hard against the viewport's edge) otherwise
 * gets cut off, at its container's edge or the viewport's, with no way for
 * a descendant to escape either via CSS alone. Reopening always re-measures
 * rather than trusting a stale position. A document scroll, or a scroll
 * INSIDE a container that holds the trigger, closes the menu (the trigger's
 * rect moves out from under the fixed popup). A viewport resize re-anchors
 * it to the trigger's new rect instead of closing: the resize that matters
 * most is the software keyboard leaving when this menu's own FocusScope
 * takes focus out of the editor that opened it, and closing there would
 * swallow the tap that opened the menu (see the resize handler below). A
 * scroll anywhere else - the session transcript scrolling beside
 * its own chrome's "..." menu, an unrelated pane - leaves the trigger's rect
 * exactly where it was, so the menu stays open (kata: the session's "..."
 * menu must survive transcript scrolling).
 */
export function Menu({ trigger, icon, label, items, triggerTabIndex, variant = "default" }: MenuProps) {
  const [isOpen, setIsOpen] = useState(false);
  // The active item is tracked by its STABLE ID, never by its index. The item
  // list is the caller's: SessionMenu drops "Find in session..." when the
  // viewport height crosses 480px (SessionChrome), and any caller may add,
  // remove or reorder entries while the popup is open. An index is a snapshot
  // of the list at the moment it was written; when an earlier entry
  // disappears, that index names a DIFFERENT item, and Enter/Space would
  // activate something the person never chose - "Pin this session..." focused,
  // a resize drops Find, and the stored index now names "Archive".
  const [activeId, setActiveId] = useState<string | undefined>(() => {
    const first = items[firstEnabledIndex(items)];
    return first === undefined || isSeparator(first) ? undefined : first.id;
  });
  const [position, setPosition] = useState<MenuPosition | null>(null);
  const [origin, setOrigin] = useState<string | undefined>(undefined);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popupRef = useRef<HTMLUListElement>(null);
  // The popup stays mounted while it leaves; everything that answers the user
  // (dismissal, focus, aria-expanded) keys off isOpen.
  const { mounted, phase } = usePresence(isOpen, popupRef);
  // Follows Dockview moving this menu's DOM between windows without a render,
  // so the portal, listeners and placement rebind to the window it is in now.
  const ownerDocument = useOwnedDocument(rootRef, mounted);
  const itemRefs = useRef<(HTMLLIElement | null)[]>([]);
  const triggerId = useId();

  // Resolve the tracked id against the list as it is NOW. The id may name an
  // entry this render no longer has (the caller removed it) or one that has
  // become disabled; either way it is not actionable, and the caller's next
  // enabled entry is where roving focus belongs.
  const indexOfActive = useCallback(() => {
    const index = items.findIndex((entry) => !isSeparator(entry) && entry.id === activeId);
    if (index !== -1 && isActionable(itemAt(items, index))) return index;
    return firstEnabledIndex(items);
  }, [items, activeId]);

  // Opens with a naive, unflipped position (left-aligned to the trigger,
  // below it - the same fixed geometry the pre-portal popup always used)
  // computed synchronously from the trigger's OWN rect, which is all that's
  // available yet - the popup itself hasn't rendered a single frame, so its
  // real size can't be measured until it has. The measuring layout effect
  // below corrects this to the actual flipped/clamped position on the very
  // next tick, before the browser paints (see that effect's own comment);
  // this is deliberately never null and the popup is deliberately never
  // hidden in between - see that same comment for why.
  function openMenu() {
    const first = items[firstEnabledIndex(items)];
    setActiveId(first === undefined || isSeparator(first) ? undefined : first.id);
    const triggerRect = triggerRef.current?.getBoundingClientRect();
    setPosition(triggerRect ? { top: triggerRect.bottom + TRIGGER_GAP, left: triggerRect.left } : { top: 0, left: 0 });
    setIsOpen(true);
  }

  // useCallback (not a plain function like openMenu) so the outside-click
  // and scroll/resize effects below can depend on it honestly without
  // re-attaching their listeners on every render: closeMenu closes over
  // nothing but the setIsOpen setter, which React itself guarantees is
  // stable.
  const closeMenu = useCallback(() => {
    setIsOpen(false);
  }, []);

  // Where the focused row sat the last time this widget looked, and in what
  // scrollport. The reveal runs on the answer to one question, so this is the
  // one fact it is answered from: layout moved the row (or the scrollport under
  // it) AND the row is now outside the visible area. Anything else - a list
  // change, a vanished id, a resized popup - is only a REASON to re-ask.
  const rowObservationRef = useRef<RowObservation | null>(null);
  // Whether the row focus landed on still has to be brought into the popup's
  // view. Armed whenever focus settles on a row by the widget's own doing -
  // the open, and a replacement after a list change - and spent by the next
  // placement pass, which is the first moment the popup's box is known.
  const initialRevealPendingRef = useRef(false);

  // Where the trigger's rect was at the last placement pass, and whether the
  // window has grown since the pass before it. A scroll of a scroller that
  // holds the trigger means "the trigger moved out from under the fixed popup",
  // and that is true exactly when the trigger's rect is no longer where this
  // recorded it. A browser CLAMPING an offset is a scroll event with no user
  // behind it: the rail's maximum offset shrinks, the browser pulls scrollTop
  // back, and the trigger does not move on screen at all.
  //
  // The growth is recorded separately from the rect because a clamp has a CAUSE
  // and only one: a taller viewport leaves less content below the current
  // offset. It cannot be read off the rect's own record, because the pass the
  // resize itself runs would have overwritten it before the clamp arrived. It
  // is consumed by the next scroll, so it can only ever excuse the one
  // correction that resize caused.
  const anchoredTriggerRef = useRef<{ left: number; top: number } | null>(null);
  const grewBeforeScrollRef = useRef(false);
  const lastViewportHeightRef = useRef(0);
  // What each scroller that holds the trigger looked like at the last look: its
  // offset, and where the trigger sat inside its content. Two numbers, because
  // the offset alone cannot say who moved it. A clamp is the offset pulled back
  // to a maximum that shrank under it - the case a window height cannot see at
  // all, since the rail's list grows when its Needs You chip disappears with
  // `innerHeight` untouched. An anchoring adjustment is the offset growing by
  // exactly the amount the trigger moved deeper into the content, which is a
  // live list growing above the reader. A reader's own scroll moves the offset
  // and leaves the trigger where it was in the content.
  const scrollerRecordsRef = useRef(new WeakMap<Element, ScrollerRecord>());

  /** Reveal the focused row if it is outside the popup's visible area, and do
   * nothing in every other case.
   *
   * This is the ONLY place that decides to reveal, and it decides on one
   * question: is the focused row clipped? Every other fact the widget can
   * observe - a changed id list, a vanished focused id, a resized popup, a text
   * node that arrived - is a reason to RE-ASK, never a reason to reveal on its
   * own. Asking from four doors and answering in one place is what keeps a
   * newly-noticed trigger from becoming a new mechanism.
   *
   * `reason` says only what the prior observation means. "layout" is a pass
   * that followed some change to the page, so a row that layout did not move is
   * a row the reader scrolled away from and must be left alone. "focus" is a
   * replacement this widget just focused because the row it was on was removed:
   * there is no earlier position to compare against and no scroll of the
   * reader's to preserve, so the visibility check alone decides. */
  const revealFocusedRowIfClipped = useCallback(
    (popup: HTMLElement, reason: "layout" | "focus" = "layout") => {
      const row = itemRefs.current.find((node) => node !== null && node === ownerDocument.activeElement) ?? null;
      if (row === null) {
        // Focus is not on a row (an initial open, or the reader moved it), so no
        // row's geometry is this widget's to keep in view.
        rowObservationRef.current = null;
        return;
      }
      const now = observeRow(popup, row);
      const before = rowObservationRef.current;
      rowObservationRef.current = now;
      if (reason === "layout") {
        // `before === null` is the first look: the row was just focused and the
        // popup was placed around it, so layout has not moved it out of view.
        if (before === null) return;
        // A scroll that has not been delivered yet is not a layout change. The
        // reader swipes the popup and, in the same moment, the keyboard's inset
        // settles and a resize places the popup - so this pass sees a new
        // offset while the row still sits where it sat in the content. Nothing
        // about the row moved; the reader moved the window over it, and the
        // position recorded here is theirs. Reading that offset as layout
        // clipping is what jumps them back to the top of the list. When nothing
        // else about the row changed there is nothing to compare at all.
        //
        // Nothing else means the row's own box and the scrollport too. A label
        // that wraps holds the row's TOP still - the offset moved under it and
        // the row grew downward - and returning here on the top alone would
        // both skip the reveal and save the clipped geometry as the baseline,
        // so the next pass would have no earlier unclipped frame left to notice
        // it against.
        const rowUnchanged =
          now.top === before.top && now.height === before.height && now.portHeight === before.portHeight;
        if (now.scrolledTo !== before.scrolledTo && rowUnchanged) return;
        // The offset moved AND the row moved in the content by a DIFFERENT
        // amount, so the two did not travel together: part of the picture is
        // the reader's own scroll, undelivered when the swipe and the update
        // land in the same pass. Comparing the earlier look at the offset it
        // was taken at then measures the row against a view the reader has
        // already left, and a row they scrolled away from reads as a row
        // layout just clipped. Judging both looks at the offset the reader is
        // at NOW takes their scroll back out and leaves only what layout did.
        //
        // A clamp or an anchoring adjustment moves the row and the offset by
        // the SAME amount - that is what makes it one - so it takes the other
        // branch and keeps each look at its own offset, which is what lets a
        // clamp cancel.
        //
        // A CLAMP is the other way the two deltas differ while nobody
        // scrolled. A local session stops: Stop and Steer go above the reader
        // and Delete arrives below them, so the content shrinks, the popup's
        // maximum offset falls, and the browser pulls the offset back by LESS
        // than the row moved up. Unequal deltas alone therefore cannot mean
        // "the reader scrolled" - they also mean "the list shrank under an
        // offset that was past its new end". A clamp is read from the popup's
        // own range, the same way a scroller clamp is: the earlier offset was
        // past the maximum this pass allows, and what is left is exactly that
        // maximum. Then each look is judged at the offset it was taken at, so
        // the part of the row on screen cancels and only layout's own movement
        // counts.
        // The scrollport's height here is the one the comparison above already
        // uses, so the range and the interval cannot disagree about how tall
        // the visible area is.
        const maximum = popup.scrollHeight - now.portHeight;
        const clamped = before.scrolledTo > maximum && Math.abs(now.scrolledTo - maximum) <= CLAMP_ROUNDING;
        // The row and the offset are read at DIFFERENT precisions: `offsetTop`
        // is an integer, `scrollTop` is not. A browser's 36.5px anchoring
        // adjustment therefore reports 37px of row movement against 36.5px of
        // offset, and an exact comparison calls a correction the reader never
        // made a swipe. One pixel is the whole of that gap, and it is the same
        // pixel the clamp reading above already allows for the same reason.
        const rowMoved = now.top - before.top;
        const offsetMoved = now.scrolledTo - before.scrolledTo;
        const readerScrolled =
          !clamped && now.scrolledTo !== before.scrolledTo && Math.abs(rowMoved - offsetMoved) > CLAMP_ROUNDING;
        const was = readerScrolled ? { ...before, scrolledTo: now.scrolledTo } : before;
        // Each look is judged at the offset it was TAKEN at. A clamp moves the
        // whole list under a viewport that did not move, so reading the earlier
        // look at the later offset says the row climbed while the scrollport
        // stayed put - clipping no layout produced - and reading the later look
        // at the earlier offset says the same thing backwards. Measured where
        // each was taken, a clamp cancels: the row moved by exactly what the
        // offset gave back, so the part of it on screen is the part that was.
        // The part of the row the scrollport shows, measured from the row's OWN
        // top. Hidden-pixel counts cannot answer this: a row that grew by 16
        // adds 16 hidden pixels, and so does a row pushed 16 further under the
        // edge - one is new content below the fold, the other is the reader's
        // view of the row going away. Absolute coordinates cannot either: a
        // clamp moves the row and the offset together, so the two looks differ
        // in absolute terms while the same part of the row is on screen.
        // Relative to the row, a clamp cancels and only the row's own geometry
        // is left in the numbers.
        const showStart = (obs: RowObservation) => Math.max(0, obs.scrolledTo - obs.top);
        const showEnd = (obs: RowObservation) => Math.min(obs.height, obs.scrolledTo + obs.portHeight - obs.top);
        // The intervals are compared at that same pixel. A sub-pixel difference
        // between two looks is the rounding above, not a change in what the
        // reader can see: treating half a pixel as lost visibility scrolls the
        // popup for a correction that left the row exactly as it was.
        const fullyVisible = (obs: RowObservation) =>
          showStart(obs) <= CLIP_ROUNDING && showEnd(obs) >= obs.height - CLIP_ROUNDING;
        if (fullyVisible(now)) {
          // The popup can be capped narrower than a glyph - a zoomed-in
          // viewport leaves a few pixels of text room however the insets
          // yield - and the horizontal scroll is the only thing that can bring
          // it back. This is the same question on the other axis: is the row
          // inside the box the reader can see? It reads the row's own width
          // against the popup's, so nothing here is measured in two coordinate
          // systems.
          if (now.width > now.portWidth) revealRow(popup, row);
          return;
        }
        // Three ways layout can take a row out of sight, and one for rows it
        // never showed:
        //
        // - the row was fully in sight and is not any more. A label that wraps
        //   at the bottom edge slides past it this way, and the row was never
        //   hidden, so nothing about its hidden pixels says so;
        // - the part on show now starts further into the row than it did: the
        //   beginning went under the top edge;
        // - the part on show now ends sooner than the part the reader could
        //   still read. A row that shrinks pulls its own end up without the
        //   scrollport cutting anything, and one that grows pushes its end down
        //   into space that was never on screen - neither takes away what the
        //   reader was looking at. So the earlier interval is not compared
        //   against the row's old height but against the height it now has:
        //   the intersection of what was visible then with what the row still
        //   contains. That surviving portion is the part a resize can cut
        //   into - an unwrapped row with a shortened scrollport shows less of
        //   itself than the reader could read, even though the row shrank;
        // - a row with NO pixels on show in the earlier look is a row the reader
        //   or the list had already taken away. It stays that way however it
        //   grows or however the scrollport shrinks, because no pixel of it was
        //   ever on screen to lose.
        const becameClipped = fullyVisible(was);
        const wasOnScreen = showEnd(was) > showStart(was);
        const lostItsStart = showStart(now) > showStart(was) + CLIP_ROUNDING;
        // What the reader could still read a moment ago that the row still
        // contains: the earlier interval, cut to the row's present height.
        const survivedEnd = Math.min(showEnd(was), now.height);
        const lostItsEnd = showEnd(now) < survivedEnd - CLIP_ROUNDING;
        if (!becameClipped && !(wasOnScreen && (lostItsStart || lostItsEnd))) return;
      }
      // A row inserted BELOW the focused one moves nothing above it, and the
      // reader's own scroll to the lower actions must survive that: the guards
      // above are what return there, since the row's own frame did not move.
      revealRow(popup, row);
    },
    [ownerDocument],
  );
  // Measures the trigger and the popup's own natural (unclamped) size and
  // moves the popup to the flipped/clamped position - see computeMenuPosition
  // above. Runs in the commit that mounts the popup, and again on every
  // viewport resize while it is open (the resize handler below), so the popup
  // follows its trigger rather than closing when the viewport changes under
  // it.
  const placeMenu = useCallback(() => {
    const triggerEl = triggerRef.current;
    const popupEl = popupRef.current;
    if (!triggerEl || !popupEl) return;
    // The trigger's own viewport: a menu in a Dockview popout must flip and
    // clamp against that window's edges, not the opener's.
    const ownerWindow = ownerDocument.defaultView ?? window;
    const triggerRect = triggerEl.getBoundingClientRect();
    anchoredTriggerRef.current = { left: triggerRect.left, top: triggerRect.top };
    // The popup's own size is its untransformed layout box (offset*), not its
    // rect: this runs in the commit that mounts it, while the entrance still
    // holds it at its scaled-down `from` keyframe, and a rect would place a
    // popup that is about to be larger than the box it was measured as.
    //
    // The visible region is the VISUAL viewport, not the layout viewport: a
    // pinch zoom (or any visual-viewport shrink) leaves innerHeight describing
    // an area the reader cannot see, so flipping and clamping against it puts
    // actions offscreen. Rect and offsets are both in layout-viewport CSS
    // pixels, so translating the trigger into the visual viewport's own
    // coordinates, placing there, and translating back keeps the shared
    // flip-then-clamp math unchanged. Without a visual viewport the two are
    // the same region and the translation is zero.
    const visualViewport = ownerWindow.visualViewport;
    const visible = visualViewport
      ? {
          left: visualViewport.offsetLeft,
          top: visualViewport.offsetTop,
          width: visualViewport.width,
          height: visualViewport.height,
        }
      : { left: 0, top: 0, width: ownerWindow.innerWidth, height: ownerWindow.innerHeight };
    const localTriggerRect = {
      ...triggerRect,
      left: triggerRect.left - visible.left,
      right: triggerRect.right - visible.left,
      top: triggerRect.top - visible.top,
      bottom: triggerRect.bottom - visible.top,
    } as DOMRect;
    // The stylesheet caps the popup at 100dvh minus both margins, which is the
    // LAYOUT viewport. After a pinch zoom the visible area can be half of that,
    // so the popup keeps a height it cannot show: it is placed at the visible
    // top edge and still runs past the visible bottom, and its own scrollport
    // ends out there too, so the lower actions cannot be scrolled to at all.
    // Capping to the visible height fixes the box AND the scrollport, and it is
    // written before the measurement below so offsetHeight reports the capped
    // box in a browser. Not a dismissal: a shorter popup scrolls, and the menu
    // stays open.
    //
    // The width needs the same treatment, and additionally has to let the
    // popup's own 180px min-width yield: zoomed in far enough, the visible
    // width is narrower than that floor, and an uncapped popup would be pinned
    // to the visible left edge with its labels running off the right - at every
    // pan position, since the pin follows the edge. A narrower popup WRAPS, so
    // the text stays readable instead of being clipped. max-width cannot beat
    // min-width in CSS, so both are written.
    const maxBlockSize = `${Math.max(0, visible.height - 2 * EDGE_MARGIN)}px`;
    if (popupEl.style.maxBlockSize !== maxBlockSize) popupEl.style.maxBlockSize = maxBlockSize;
    const availableWidth = visible.width - 2 * EDGE_MARGIN;
    const maxInlineSize = `${Math.max(0, availableWidth)}px`;
    if (popupEl.style.maxInlineSize !== maxInlineSize) popupEl.style.maxInlineSize = maxInlineSize;
    const minInlineSize = availableWidth < NARROW_POPUP_WIDTH ? "0" : "";
    if (popupEl.style.minWidth !== minInlineSize) popupEl.style.minWidth = minInlineSize;
    // The stylesheet sizes the popup to `max-content`, so the width measured
    // here is the popup's own request capped by the visible width: below the
    // cap it is what the labels want, at the cap it is as much as the reader
    // can see. Only that second case wraps - the cap is then what forced the
    // break, and without the break a long label (the session activity count at
    // the largest text setting is one) runs past the scrollport and reading it
    // needs a horizontal scroll. Wrapping is keyed on the measurement rather
    // than on the cap being smaller than the stylesheet's floor: a cap at or
    // above 180 still narrows the popup. Comparing the cap against the layout
    // viewport instead would wrap every menu, since the cap always reserves
    // both edge margins.
    //
    // A zoom can leave the cap fractional (180.25) while offsetWidth reports
    // the rounded integer (180), so the comparison allows that rounding: a
    // popup within a pixel of the cap is at it, which is the case that still
    // needs the break.
    const capped = popupEl.offsetWidth >= availableWidth - CAP_ROUNDING;
    popupEl.classList.toggle(CLASS.popupNarrow, capped);
    const placed = computeMenuPosition(
      localTriggerRect,
      { width: popupEl.offsetWidth, height: popupEl.offsetHeight },
      { width: visible.width, height: visible.height },
    );
    // A shorter scrollport - or a narrower popup, whose labels wrap onto more
    // lines and so make every row taller - can leave the row the reader is on
    // below it. Enter would then activate a row whose label and focus ring are
    // out of sight. Width counts as well as height because an already-capped
    // popup that narrows further wraps another line with its box height
    // unchanged, and because the cap applying at all is one way to narrow.
    // Revealing on every pass is what must not happen: it would undo the
    // reader's own scroll to the lower actions the moment a pan or a row
    // appearing above re-places the popup.
    //
    // The reveal moves the POPUP's own scrollTop and nothing else.
    // scrollIntoView would answer the same question through geometry, but on a
    // pinch-zoomed page the row's position is measured against the VISUAL
    // viewport, so it can pan the page itself - before this pass has written
    // the popup's new coordinates, it pans toward where the popup used to be.
    // Arithmetic in the popup's own scroll space has no such reach.
    revealFocusedRowIfClipped(popupEl);
    // The one reveal that is not about layout moving a row: the row focus
    // landed on when the menu opened. FocusScope focuses it with
    // `preventScroll` so the page never moves, which also means the browser
    // will not scroll the popup for it - and in a capped or zoomed popup that
    // row can already be below the scrollport before anything has changed.
    // Enter would then invoke an action the reader has never seen. This runs
    // once, on the first pass after the open, and only ever moves the popup's
    // own scroll.
    // Only write when it actually moved: the observers below can report a
    // mutation that changes nothing about placement (a text node elsewhere in
    // the same scroller), and a state write per notification would re-render
    // the open popup on every one of them.
    const next = { left: placed.left + visible.left, top: placed.top + visible.top };
    setPosition((current) =>
      current !== null && current.left === next.left && current.top === next.top ? current : next,
    );
    // This pass has just laid the popup out around wherever the trigger is NOW,
    // so that is the position any later scroll is judged against - see
    // refreshScrollerBaselines.
    refreshScrollerBaselines(triggerEl, ownerWindow, scrollerRecordsRef.current);
    // The origin is read against the trigger's rect, so it takes the same
    // translated position: `placed` is in the visual viewport's coordinates,
    // and comparing it with a layout-coordinate trigger picks the wrong edge
    // once the visual viewport is panned far enough to cross the trigger.
    setOrigin(transformOriginFor(triggerRect, next));
  }, [ownerDocument, revealFocusedRowIfClipped]);

  // Re-measure ONCE on the next frame. A resize event is not the moment the
  // trigger has finished moving: the docked pane reflows after it, and the
  // footer can be somewhere else by the next frame with no further event to
  // say so. The next frame is the widget's own "after layout" point - a
  // getBoundingClientRect there forces the settled layout rather than reading
  // the rect the resize happened to catch - and it is the point a widget can
  // reach: none of them knows Dockview, so none subscribes to its layout
  // event. Coalesced through one pending frame, so a burst of notifications -
  // a resize, a visual-viewport change and an observation together -
  // re-measures once.
  const frameRef = useRef<number | null>(null);
  const placeAfterLayout = useCallback(() => {
    if (frameRef.current !== null) return;
    const ownerWindow = ownerDocument.defaultView ?? window;
    frameRef.current = ownerWindow.requestAnimationFrame(() => {
      frameRef.current = null;
      placeMenu();
    });
  }, [ownerDocument, placeMenu]);
  // A menu that leaves mid-frame must not place itself afterwards, and this
  // document may no longer be the one it is leaving in. Closing cancels the
  // frame HERE rather than in the cleanup below, because the cleanup for a
  // document change is not a close: without this the frame a resize queued
  // would run while the popup plays its exit (it stays mounted through that -
  // usePresence) and re-place it away from where the reader last saw it.
  useEffect(() => {
    if (isOpen || frameRef.current === null) return;
    (ownerDocument.defaultView ?? window).cancelAnimationFrame(frameRef.current);
    frameRef.current = null;
  }, [isOpen, ownerDocument]);
  // Unmount, and a move to another document, have no close to cancel the frame.
  useEffect(() => {
    return () => {
      if (frameRef.current === null) return;
      (ownerDocument.defaultView ?? window).cancelAnimationFrame(frameRef.current);
      frameRef.current = null;
    };
  }, [ownerDocument]);

  // The measuring pass above is a layout effect, so it - and the re-render
  // its setPosition call causes - both complete before the browser's next
  // paint, in the same commit as the mount itself: nothing ever visibly
  // jumps, even though the popup is visible (never visibility: hidden) for
  // the brief moment between the naive position openMenu set and this one.
  // Hiding it for that moment was tried and reverted - see this task's
  // report: it made the popup unreachable to FocusScope's own tabbable() scan
  // (an ancestor walk that treats a hidden ancestor as making every
  // descendant untabbable, correctly, since that's the general case a widget
  // nested inside e.g. a collapsed accordion needs) for however long the
  // popup stayed hidden, which could lose the race against FocusScope's own
  // mount effect - moving focus nowhere instead of the first item.
  useLayoutEffect(() => {
    if (!isOpen) return;
    // The open is the one moment focus is placed on a row the reader has not
    // seen yet. Whatever the popup's box turns out to be, that row has to be
    // inside it before the menu is anyone's to press Enter on.
    initialRevealPendingRef.current = true;
    placeMenu();
  }, [isOpen, placeMenu]);

  // The entry the person is on can leave the list while the popup is open
  // (SessionMenu drops "Find in session..." when the viewport crosses 480px).
  // React reuses the surviving nodes by their `key`, so focus stays on the
  // same entry when the list merely shifts. What does NOT survive is focus on
  // the entry that was removed: the browser drops it to <body>, and the next
  // Enter would reach nothing at all. Put it on the surviving entry the id
  // resolution names. Keyed on the id signature, not on `items` itself: a
  // caller that rebuilds its array every render must not re-run this.
  const itemIds = items.map((entry) => entry.id).join("\u0000");
  const resolvedActiveIndex = indexOfActive();
  // The id of the row that last held DOM focus inside the popup, or null when
  // no row has. An initial open has none, which is what makes it not a
  // recovery: FocusScope owns that move and focuses with preventScroll, so
  // taking the open over here would focus a row while the popup still sits at
  // its provisional below-trigger position - and on a pinch-zoomed page a plain
  // focus() pans the reader's view toward the coordinate the placement pass is
  // about to leave. Only a row that was here and is now gone may move focus,
  // and that is the one thing that may move the popup's own scroll.
  const lastFocusedIdRef = useRef<string | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: itemIds is the deliberate trigger-only dep - the id signature, not the array's identity, is what says the caller's list changed, and callers rebuild that array every render; indexOfActive is called at effect time, so the closure that runs is the current one
  useLayoutEffect(() => {
    // `isOpen` false means the popup is leaving: FocusScope has already handed
    // focus back to whatever opened the menu, and re-focusing an item here
    // would take it away again (the Escape-returns-focus test is the oracle).
    // This effect answers only a focus DROP while the menu is live.
    if (!isOpen) {
      lastFocusedIdRef.current = null;
      return;
    }
    const popup = popupRef.current;
    const focused = ownerDocument.activeElement;
    // Focus in the popup: the entry the person was on survived the list change
    // (React reuses it by key), so it is what they are looking at - but entries
    // arriving ABOVE it may have pushed it out of the scrollport, which is the
    // one thing a list change can do to a row whose identity survived. Enter
    // would then invoke an action the reader cannot see.
    if (popup !== null && focused !== null && popup.contains(focused)) {
      // The same question the placement pass asks, asked from the other door:
      // whether this list change moved the row holding focus out of sight. It
      // is what catches a label that wraps (no id changes, no popup box
      // changes) as surely as it catches a row arriving above.
      revealFocusedRowIfClipped(popup);
      return;
    }
    // A row was focused and that row is still in the list: nothing was lost, so
    // this is not a dropped focus and the popup's scroll is not ours to move.
    const lost = lastFocusedIdRef.current;
    if (lost === null || items.some((entry) => entry.id === lost)) return;
    lastFocusedIdRef.current = null;
    // Focus anywhere else that is not the popup's own scope means focus was
    // dropped to <body> by the entry's removal, which is the case this
    // recovers. A focus that has moved to a real control (the person tabbed
    // away, a Dialog took it) is theirs, not ours to reclaim.
    if (focused !== null && focused !== ownerDocument.body) return;
    const index = indexOfActive();
    if (index === -1) return;
    // focusIndex, not node.focus(): the replacement BECOMES the active item.
    // Focusing it without adopting it leaves activeId naming the entry that
    // left, so a caller that brings that entry back re-points activeId at a
    // row the person is no longer on - and the id would then name the
    // action while focus names a different row.
    focusIndex(index, true);
  }, [isOpen, ownerDocument, itemIds]);

  // Record which row holds focus, so a list change can tell a row that LEFT
  // from one that merely moved. Nothing writes this in the recovery effect:
  // the id has to survive the change that removes the row, and by then the
  // browser has already dropped focus to <body>.
  function handlePopupFocus(event: FocusEvent<HTMLUListElement>) {
    const target: EventTarget = event.target;
    const index = itemRefs.current.findIndex((node) => node !== null && node === target);
    if (index === -1) return;
    lastFocusedIdRef.current = itemAt(items, index).id;
    // The observation starts when the row is focused: the spot it sits in then
    // is what every later pass is compared against, so "layout moved it" means
    // moved since the reader arrived rather than since the last time this
    // widget happened to look.
    const popup = popupRef.current;
    if (popup !== null) rowObservationRef.current = observeRow(popup, target as HTMLElement);
    // The open's reveal belongs here when it is still pending: the first
    // placement pass runs before FocusScope has moved focus onto a row, so this
    // is the first moment there is a row to bring into view. It is still the
    // open's reveal - once per open, and only ever the popup's own scroll.
    if (initialRevealPendingRef.current && popup !== null) {
      initialRevealPendingRef.current = false;
      revealRow(popup, target as HTMLElement);
    }
  }

  // `reveal` is for a replacement focused OUT of a keypress: a row the reader
  // did not move to. It is revealed inside the popup's own scroll, and the
  // focus itself carries preventScroll so the browser cannot pan the page to
  // it. Arrow-key navigation keeps the plain focus(), whose native scroll
  // already stays within the popup.
  function focusIndex(index: number, reveal = false) {
    if (index === -1) return;
    const entry = itemAt(items, index);
    if (isSeparator(entry)) return;
    setActiveId(entry.id);
    const node = itemRefs.current[index] ?? null;
    if (node === null) return;
    node.focus(reveal ? { preventScroll: true } : undefined);
    // The same question, asked for a row this widget just focused: it is the
    // reader's replacement, not a row they scrolled to, so clipping is the
    // whole test.
    const popup = popupRef.current;
    if (reveal && popup !== null) revealFocusedRowIfClipped(popup, "focus");
  }

  // The index of the item that actually holds DOM focus, or -1 when focus is
  // not on one of this popup's items. This is the only truthful answer to
  // "which item is this keypress about": activeId can name an entry that has
  // left the list, and an item can become disabled in place while focus stays
  // on it, and neither of those is visible from the id alone.
  function focusedIndex(): number {
    const focused = ownerDocument.activeElement;
    if (focused === null) return -1;
    return itemRefs.current.findIndex((node) => node !== null && node === focused);
  }

  function selectItem(item: MenuEntry) {
    if (isSeparator(item)) return;
    if (item.disabled) return;
    item.onSelect();
    closeMenu();
  }

  function handleTriggerClick() {
    if (isOpen) closeMenu();
    else openMenu();
  }

  // Consume-then-stop, mirroring handleMenuKeyDown's own Escape precedent
  // below: every key this handler gives a MEANING to must stop there,
  // never also reach an ancestor that might independently react to the
  // same keypress (e.g. a Tree row's own onKeyDown - see
  // shell/rail/RailRow.tsx). ArrowDown/ArrowUp open the menu, so they're
  // stopped whether or not this particular press is what actually opened
  // it (openMenu() is idempotent-guarded by `!isOpen`, but the KEY's
  // meaning - "this is this trigger's open shortcut" - holds regardless).
  // Enter/Space get stopPropagation only, never preventDefault: the
  // browser's own native <button> activation (a click, via onClick) is
  // what actually opens the menu for those two, and preventDefault would
  // suppress that default action entirely, breaking it.
  function handleTriggerKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    switch (event.key) {
      case "ArrowDown":
      case "ArrowUp":
        event.preventDefault();
        event.stopPropagation();
        if (!isOpen) openMenu();
        break;
      case "Enter":
      case " ":
        event.stopPropagation();
        break;
      default:
        break;
    }
  }

  function handleMenuKeyDown(event: KeyboardEvent<HTMLUListElement>) {
    // Roving and activation both start from the item that is actually
    // focused, resolved against the list as it is NOW - not from a stored
    // index. See activeId's own comment: an index written before the caller
    // changed `items` names a different entry after the change.
    const focused = focusedIndex();
    const current = focused === -1 ? indexOfActive() : focused;
    // A key this popup handles stays here. The popup is PORTALLED, but React
    // still runs the event up the component tree, so an enclosing widget that
    // also answers the same key sees it - a rail row's menu sits inside a Tree
    // row, whose own ArrowDown moves focus to the next row. The menu would
    // then be open with focus outside it and Escape would no longer reach the
    // popup. preventDefault alone does not stop that: the enclosing handler is
    // looking at the key, not at the default action.
    const handled = ["ArrowDown", "ArrowUp", "Home", "End", "Enter", " ", "Escape"];
    if (handled.includes(event.key)) event.stopPropagation();
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        focusIndex(stepEnabledIndex(items, current, 1));
        break;
      case "ArrowUp":
        event.preventDefault();
        focusIndex(stepEnabledIndex(items, current, -1));
        break;
      case "Home":
        event.preventDefault();
        focusIndex(firstEnabledIndex(items));
        break;
      case "End":
        event.preventDefault();
        focusIndex(lastEnabledIndex(items));
        break;
      case "Enter":
      case " ": {
        // The item under focus, which is what the person is looking at. A
        // stale index here is how a stray Enter archives a session.
        const item = items[current];
        if (item !== undefined) {
          event.preventDefault();
          selectItem(item);
        }
        break;
      }
      case "Escape":
        // This handler only exists in the tree while the menu is open
        // (it's on the popup <ul>, which {isOpen && ...} gates), so
        // reaching this case always means there's a popup here to close -
        // stop the event from also reaching an enclosing overlay (e.g. a
        // Dialog this menu is nested in).
        event.preventDefault();
        event.stopPropagation();
        closeMenu();
        break;
      case "Tab":
        // Tab is FocusScope's job while focus sits on a menu item, and its
        // trap wraps at the ends of the tabbable list. A row disabled in place
        // is focused but NOT tabbable, so it is not one of those ends: the
        // browser would continue from it in DOM order and, when the single
        // tab stop sits before it, walk straight out of the open menu. Route
        // Tab from such a row to the item the roving id resolves to - which
        // keeps the trap intact - and leave Enter/Space on it doing nothing.
        if (focused !== -1 && !isActionable(itemAt(items, focused))) {
          event.preventDefault();
          focusIndex(indexOfActive());
        }
        break;
      default:
        break;
    }
  }

  // Outside press closes the menu. A press on the trigger itself is inside
  // rootRef too, so this never fights handleTriggerClick's own toggle; a press
  // inside the popup is inside popupRef, which rootRef alone can't see now that
  // the popup renders through a portal elsewhere in the DOM.
  useDismissOnOutsidePress({
    ownerDocument,
    enabled: isOpen,
    isInside: (target) =>
      rootRef.current?.contains(target as Node) === true || popupRef.current?.contains(target as Node) === true,
    onDismiss: closeMenu,
  });

  // A scroll that can move the trigger out from under the fixed popup
  // closes the menu; a scroll anywhere else leaves it open (see this
  // component's own doc comment above for the rule and its reasoning). The
  // listener is capture-phase because "scroll" doesn't bubble - capture
  // still reaches every ancestor on the way down to whatever actually
  // scrolled, so one window listener sees every scroller.
  //
  // The event is not the decision, though. A scroller that holds the trigger
  // emits `scroll` for two different reasons, and only one of them moves the
  // trigger:
  //
  // - the reader scrolled it, which slides the trigger out from under a popup
  //   that is fixed in the viewport's coordinates, and
  // - the browser CLAMPED it, because a viewport that grew left less content
  //   below the current offset than the offset assumed. The rail near its end
  //   is the everyday case: make the window taller and its maximum scroll
  //   offset drops, so the browser pulls scrollTop back with no user behind it
  //   and emits `scroll` for the correction.
  //
  // Reading which happened is one geometry question, and the same one the
  // reveal path asks: did the thing I anchored to move? A clamp leaves the
  // trigger's rect exactly where the last placement pass recorded it. Anything
  // that does move it is a scroll that really did take the trigger away, so
  // the dismissal stands.
  useEffect(() => {
    if (!isOpen) return;
    // The widget's own window: a popout's scrolls and resizes never reach the
    // opener's window.
    const ownerWindow = ownerDocument.defaultView ?? window;
    // The height the next resize is compared against, and no growth to excuse
    // yet. Seeded here rather than at module scope so a popout's own window is
    // the one measured.
    lastViewportHeightRef.current = ownerWindow.innerHeight;
    grewBeforeScrollRef.current = false;
    // The offset each scroller held when this menu opened. Without this, the
    // first scroll of the trigger's own container could only be judged by the
    // rect, and a clamp that arrives as the very first event would be read as
    // the reader leaving.
    const seeded = scrollerRecordsRef.current;
    const triggerEl = triggerRef.current;
    for (let node: HTMLElement | null = triggerEl; node !== null; node = node.parentElement) {
      if (!isScrollContainer(node, ownerWindow)) continue;
      if (triggerEl !== null) seeded.set(node, recordFor(triggerEl, node, ownerWindow));
    }
    function onScrollCapture(event: Event) {
      const target = event.target;
      if (target === null) return;
      // nodeType, not instanceof: an element in another realm keeps that
      // realm's prototype, so the opener's Element/Document reject it.
      const nodeType = (target as Node).nodeType;
      // The whole document/window scrolling moves every trigger; a
      // container scrolling moves the trigger only when the trigger lives
      // inside it. Anything else (e.g. the session transcript beside this
      // menu's chrome) can't move the popup's anchor, so the menu stays.
      if (
        nodeType === 9 ||
        target === ownerWindow ||
        (nodeType === 1 && rootRef.current !== null && (target as Element).contains(rootRef.current))
      ) {
        // A scroll this scroller emitted with nobody behind it - the browser
        // correcting an offset a bigger scrollport made invalid, or holding the
        // reader's view still while a live list grows above the trigger - is
        // read from the geometry, never from the event. See
        // classifyScrollerScroll for the three causes and how they separate.
        //
        // Either correction re-anchors even when the trigger's rect changed:
        // the content the trigger sits in moved, so the trigger really does
        // move, and the popup has to follow it. What neither may do is move the
        // trigger OFF SCREEN - a correction never carries it out of the
        // viewport, so one that did is a reader's scroll however the numbers
        // read.
        const anchored = anchoredTriggerRef.current;
        const rect = triggerRef.current?.getBoundingClientRect();
        const grew = grewBeforeScrollRef.current;
        grewBeforeScrollRef.current = false;
        const stayed =
          anchored !== null && rect !== undefined && rect.left === anchored.left && rect.top === anchored.top;
        const cause =
          nodeType === 1 && target !== ownerWindow
            ? classifyScrollerScroll(target as Element, triggerRef.current, scrollerRecordsRef.current)
            : "scrolled";
        const stillOnScreen =
          rect !== undefined &&
          rect.top + rect.height > 0 &&
          rect.top < ownerWindow.innerHeight &&
          rect.left + rect.width > 0 &&
          rect.left < ownerWindow.innerWidth;
        if (cause !== "scrolled" && stillOnScreen) placeNowAndAfterLayout();
        else if (stayed && grew) placeNowAndAfterLayout();
        else closeMenu();
      }
    }
    // A resize re-anchors, it never dismisses. The resize this menu actually
    // sees on a phone is its own doing: opening moved focus out of the
    // composer's editor (FocusScope above), the software keyboard closed, and
    // index.html's `interactive-widget=resizes-content` grew the layout
    // viewport back by the keyboard's height - a window `resize` with no
    // pointer behind it. Closing on that swallowed the very tap that opened
    // the menu. A reader's dismissal is a press, and a press outside
    // the menu still closes it at the press (useDismissOnOutsidePress above);
    // it does not arrive as a resize. A real layout change (a window drag, a
    // rotation) therefore keeps the menu open and correctly attached instead
    // of making it vanish.
    // Measure now (so the popup is never a frame behind the event) AND once
    // more after layout (so it also lands on wherever Dockview's deferred
    // layout put the trigger - see placeAfterLayout above). It still never
    // closes: a resize is not a press.
    const placeNowAndAfterLayout = () => {
      placeMenu();
      placeAfterLayout();
    };
    // A clamp can only follow a window that GREW, and the scroll it emits comes
    // after this handler has already re-anchored. Recording the growth here is
    // what lets the scroll be read as the browser's correction rather than the
    // reader's. Shrinking the window cannot clamp anything up: it makes more
    // content below the offset, not less.
    const resizeToPlace = () => {
      const now = ownerWindow.innerHeight;
      if (now > lastViewportHeightRef.current) grewBeforeScrollRef.current = true;
      lastViewportHeightRef.current = now;
      placeNowAndAfterLayout();
    };
    return watchViewport(rootRef.current, onScrollCapture, resizeToPlace);
  }, [isOpen, closeMenu, ownerDocument, placeMenu, placeAfterLayout]);

  // A resize event is not the only thing that moves the trigger, and the
  // resize itself is not the moment the trigger has finished moving:
  //
  // - iOS Safari resizes only the VISUAL viewport when the keyboard leaves.
  //   The mobile shell spends that as `padding-bottom: var(--keyboard-inset)`
  //   (AppShell.module.css), which slides the composer and this trigger with
  //   it, and the window's own `resize` never fires. useKeyboardInset listens
  //   on that same visual viewport, as NextNeedsYou's list already does.
  // - A desktop window resize reaches this widget before Dockview finishes its
  //   own layout: Dockview resizes from an observed box, delivered a frame
  //   after the window's resize, so the trigger's rect at the resize event is
  //   its OLD one and the footer then moves again with no further window
  //   event.
  //
  // Both are the same fact - the trigger ended up somewhere else - so both get
  // the same answer: re-measure once the trigger has actually moved, after
  // layout, instead of trusting the rect a resize event happened to catch. A
  // ResizeObserver on the trigger covers a trigger whose box changes, and
  // fires after layout; the visual-viewport listener covers a trigger that is
  // only translated. Feature-detected, as Popover does it: jsdom has no
  // ResizeObserver, and the resize handler above is then the whole behavior.
  useEffect(() => {
    if (!isOpen) return;
    const ownerWindow = ownerDocument.defaultView ?? window;
    const visualViewport = ownerWindow.visualViewport;
    // `resize` covers the viewport changing size; `scroll` covers it moving
    // without changing size. On iOS the visual viewport scrolls under a
    // fixed-position layout when the keyboard's inset settles, and that moves
    // the trigger relative to the popup with no `resize` and no window scroll
    // at all - so the layout moves and nothing else says so. Both route
    // through the same scheduler, which coalesces them into one frame.
    //
    // Neither is a dismissal: a visual-viewport scroll is the layout moving
    // under the popup, not the reader scrolling away from it. The dismissal
    // paths stay where they are - a document scroll or a scroll of the
    // trigger's own container still closes the menu (onScrollCapture below).
    // A visual viewport's own events never reach that window capture
    // listener, so the two cannot fight.
    visualViewport?.addEventListener("resize", placeAfterLayout);
    visualViewport?.addEventListener("scroll", placeAfterLayout);
    const Observer = ownerWindow.ResizeObserver;
    const observer = typeof Observer === "undefined" ? null : new Observer(placeAfterLayout);
    // The trigger's box covers a trigger that resized; the POPUP's box covers
    // a caller that changed the item set, which changes the popup's height
    // without touching the trigger at all - adding "Find in session..." to an
    // upward-opening popup would otherwise keep its old top and overlap the
    // trigger. Popover observes its own panel for the same reason. A pure
    // translation of a fixed-size trigger fires NEITHER of those, and the
    // resize frame alone can run before the pane reflows - which is why the
    // trigger's ancestors, whose boxes the pane's reflow does change, are
    // observed as well.
    const triggerEl = triggerRef.current;
    const popupEl = popupRef.current;
    if (observer !== null) {
      if (triggerEl !== null) observer.observe(triggerEl);
      if (popupEl !== null) observer.observe(popupEl);
      // The trigger's own box is constant in the case that matters most: the
      // docked pane reflows and translates the footer, and the composer's
      // fixed-size button moves without resizing. What does change size is
      // the pane around it, so every ancestor up to the body is observed too.
      // That is what makes this the pane's own layout notification without
      // the widget knowing Dockview exists - the walk is short (a handful of
      // wrappers), and redundant notifications coalesce into one frame.
      let ancestor = triggerEl?.parentElement ?? null;
      while (ancestor !== null && ancestor !== ownerDocument.body) {
        observer.observe(ancestor);
        ancestor = ancestor.parentElement;
      }
    }
    // A box observer answers "something changed size". The remaining way the
    // trigger moves with every box unchanged is content arriving ABOVE it: a
    // session entering the rail's Live section pushes the pinned section - and
    // the trigger in it - down, while the fixed-height scroller, the sections,
    // the trigger and the popup all keep their sizes. That insertion is not a
    // resize and no event names it, so it is watched as what it is: a mutation
    // in the box that lays the trigger out.
    //
    // The scope is the trigger's scroll container when it has one - the only
    // box whose own content can slide the trigger without resizing anything -
    // and otherwise the trigger's top-level wrapper, which is everything in
    // normal flow that could push it.
    //
    // A subtree watch reaches siblings too: in the shell that wrapper holds the
    // transcript beside the composer, and streaming replaces transcript DOM
    // continuously - a record per token, each scheduling a synchronous geometry
    // read. Those records cannot move this trigger, because a scroller's
    // content is laid out on its own and does not displace anything outside it.
    // So a record is ignored when it sits inside a scroller the trigger is NOT
    // in. That covers a trigger with no scroller of its own (the composer
    // button) as well as one inside the rail: in both, the transcript is
    // somebody else's scroller.
    const ancestorsOfTrigger = new Set<Element>();
    for (let node: HTMLElement | null = triggerEl; node !== null; node = node.parentElement) {
      ancestorsOfTrigger.add(node);
    }
    const Mutations = ownerWindow.MutationObserver;
    const movedTheTrigger = (records: MutationRecord[]): boolean =>
      records.some((record) => {
        const target = record.target;
        // A character-data record's target is a Text node, which has no
        // parentElement of its own - and the text node is already INSIDE the
        // box that lays it out, so the walk starts at its parent. Starting
        // below it would skip the scroller that decides the answer.
        const from = target.nodeType === 1 ? (target as Element) : (target.parentElement ?? null);
        // From the node ITSELF: an insertion into a scroller has that scroller
        // as the record's target, and starting at its parent would walk right
        // past the scroller that decides the answer.
        for (let node: Element | null = from; node !== null; node = node.parentElement) {
          if (!ancestorsOfTrigger.has(node) && isScrollContainer(node, ownerWindow)) return false;
        }
        return true;
      });
    const mutations: MutationObserver | null =
      typeof Mutations === "undefined" || triggerEl === null
        ? null
        : new Mutations((records) => {
            if (movedTheTrigger(records)) placeAfterLayout();
          });
    if (mutations !== null && triggerEl !== null) {
      // characterData, because a heading above the trigger can be RENAMED in
      // place: another tab's session title gets longer, the text node's value
      // changes with no node inserted or removed, the heading wraps, and the
      // trigger moves down. The sizes of every observed box can stay identical,
      // so there is no other notification this widget would receive.
      mutations.observe(scrollContainerOf(triggerEl, ownerWindow) ?? topLevelWrapperOf(triggerEl, ownerDocument), {
        childList: true,
        characterData: true,
        subtree: true,
      });
    }
    // A label inside the popup can change without any box resizing: an activity
    // count arrives on Overview and the row wraps onto a second line, pushing a
    // lower row out of the scrollport. No id changes, the popup's own box does
    // not change, and the row that moved is not the one whose box changed - so
    // this is watched as what it is, a content change inside the popup, and the
    // pass then asks the same geometry question it asks everywhere else.
    const popupMutations: MutationObserver | null =
      typeof Mutations === "undefined" || popupEl === null ? null : new Mutations(() => placeAfterLayout());
    if (popupMutations !== null && popupEl !== null) {
      popupMutations.observe(popupEl, { childList: true, characterData: true, subtree: true });
    }
    // The reader scrolling INSIDE the popup never reaches the capture handler
    // above: the popup is portalled out of rootRef, so it is not "a scroller
    // that holds the trigger" and it must not be - a popup scroll is not a
    // dismissal. It IS however the offset every later look must be judged
    // against, because the reader is entitled to where they scrolled. Without
    // this the recorded frame stays at the offset from when focus arrived, and
    // a later clamp reads a row the reader had long since scrolled past as a
    // row that was on screen.
    const onPopupScroll = () => {
      const popup = popupRef.current;
      const row = itemRefs.current.find((node) => node !== null && node === ownerDocument.activeElement) ?? null;
      if (popup === null || row === null) return;
      // A scroll tells this widget where the reader is; it does not tell it
      // whether LAYOUT also moved the row. When a reflow and a scroll land in
      // the same frame the scroll event can arrive first, and overwriting the
      // baseline here would hand the queued placement frame a "before" that
      // already contains the reflow - the clip it was queued to find would
      // compare equal, and the reveal would be skipped while Enter still
      // invoked a clipped action.
      //
      // Asking the reveal instead of writing the baseline by hand is the same
      // decision the placement pass makes: it compares the earlier look with
      // the row as it is now, records the new look, and reveals only when that
      // difference is layout's rather than the reader's. A plain swipe moves
      // the offset and leaves the row's box alone, so nothing is revealed and
      // the offset simply becomes the new baseline.
      revealFocusedRowIfClipped(popup, "layout");
    };
    popupEl?.addEventListener("scroll", onPopupScroll, { passive: true });
    return () => {
      visualViewport?.removeEventListener("resize", placeAfterLayout);
      visualViewport?.removeEventListener("scroll", placeAfterLayout);
      observer?.disconnect();
      mutations?.disconnect();
      popupMutations?.disconnect();
      popupEl?.removeEventListener("scroll", onPopupScroll);
    };
  }, [isOpen, ownerDocument, placeAfterLayout, revealFocusedRowIfClipped]);

  const iconOnly = label !== undefined;
  const triggerClassName = [
    CLASS.trigger,
    variant === "quiet" ? CLASS.triggerQuiet : "",
    iconOnly ? CLASS.triggerIcon : "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div ref={rootRef} className={CLASS.root}>
      <button
        ref={triggerRef}
        id={triggerId}
        type="button"
        tabIndex={triggerTabIndex}
        className={triggerClassName}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={isOpen}
        onClick={handleTriggerClick}
        onKeyDown={handleTriggerKeyDown}
      >
        {iconOnly ? (
          <span className={CLASS.triggerGlyph} aria-hidden="true">
            {icon}
          </span>
        ) : (
          trigger
        )}
      </button>
      {mounted &&
        position &&
        createPortal(
          <LayerFocusScope trap released={phase === "closing"}>
            {/* <ul role="menu">/<li role="menuitem"> is the WAI-ARIA APG menu
                pattern's own example markup (w3.org/WAI/ARIA/apg/patterns/menu),
                not an interactive role on an arbitrary static element. */}
            <ul
              ref={popupRef}
              // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ul/li is the ARIA APG's own menu markup, see above
              role="menu"
              aria-labelledby={triggerId}
              className={CLASS.popup}
              data-motion={phase}
              inert={phase === "closing"}
              style={{ top: position.top, left: position.left, transformOrigin: origin }}
              onKeyDown={handleMenuKeyDown}
              onFocus={handlePopupFocus}
            >
              {items.map((item, index) =>
                isSeparator(item) ? (
                  // A menu separator is deliberately NOT focusable (roving
                  // navigation skips it, exactly like a disabled item), so
                  // the focusable-separator ARIA attributes (aria-valuenow
                  // et al.) don't apply; <li role="separator"> inside the
                  // same <ul role="menu"> is the APG's own menu markup, not
                  // an <hr> (which isn't valid as a ul child).
                  // biome-ignore lint/a11y/useFocusableInteractive lint/a11y/useSemanticElements lint/a11y/useAriaPropsForRole lint/a11y/noNoninteractiveElementToInteractiveRole: unfocusable APG menu separator, see above
                  <li key={item.id} role="separator" className={CLASS.separator} />
                ) : (
                  // Roving tabindex + delegated keydown: exactly one item has
                  // tabIndex 0 at a time (real focus lives here, not via
                  // aria-activedescendant), and handleMenuKeyDown on the ul
                  // above already selects the active item on Enter/Space -
                  // that handler sees every key bubbled up from whichever <li>
                  // is actually focused, so it doesn't need its own onKeyDown.
                  // biome-ignore lint/a11y/useKeyWithClickEvents: keydown is delegated to the ul's handleMenuKeyDown via bubbling, see above
                  <li
                    key={item.id}
                    // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ARIA APG menu markup, see the ul above
                    role="menuitem"
                    tabIndex={!item.disabled && index === resolvedActiveIndex ? 0 : -1}
                    aria-disabled={item.disabled === true ? true : undefined}
                    className={`${CLASS.item} ${item.disabled ? CLASS.itemDisabled : ""}`}
                    ref={(node) => {
                      itemRefs.current[index] = node;
                    }}
                    onClick={() => selectItem(item)}
                  >
                    {item.label}
                  </li>
                ),
              )}
            </ul>
          </LayerFocusScope>,
          // The owning document's body: a popout is a separate document.
          ownerDocument.body,
        )}
    </div>
  );
}
