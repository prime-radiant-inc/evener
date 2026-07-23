import {
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
import { FocusScope } from "../focusscope";
import { requireClass } from "../internal/requireClass";
import styles from "./menu.module.css";

export interface MenuItem {
  id: string;
  label: string;
  onSelect: () => void;
  disabled?: boolean;
}

export type MenuVariant = "default" | "quiet";

export interface MenuProps {
  /** Visible content of the trigger button Menu renders (its own aria and
   * open/close wiring - see this task's report for why the trigger isn't
   * the imported Button widget). */
  trigger: ReactNode;
  items: MenuItem[];
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
}

const CLASS = {
  root: requireClass(styles.root, "menu.module.css", "root"),
  trigger: requireClass(styles.trigger, "menu.module.css", "trigger"),
  triggerQuiet: requireClass(styles.triggerQuiet, "menu.module.css", "triggerQuiet"),
  popup: requireClass(styles.popup, "menu.module.css", "popup"),
  item: requireClass(styles.item, "menu.module.css", "item"),
  itemDisabled: requireClass(styles.itemDisabled, "menu.module.css", "itemDisabled"),
};

function firstEnabledIndex(items: MenuItem[]): number {
  return items.findIndex((item) => !item.disabled);
}

function itemAt(items: MenuItem[], i: number): MenuItem {
  const item = items[i];
  if (!item) throw new Error(`menu item index ${i} out of range for ${items.length} items`);
  return item;
}

function lastEnabledIndex(items: MenuItem[]): number {
  for (let i = items.length - 1; i >= 0; i--) {
    if (!itemAt(items, i).disabled) return i;
  }
  return -1;
}

// Next enabled index stepping by `delta` (+1/-1) from `from`, wrapping
// around the ends. Returns `from` unchanged if every item is disabled.
function stepEnabledIndex(items: MenuItem[], from: number, delta: 1 | -1): number {
  const n = items.length;
  if (n === 0) return -1;
  let i = from;
  for (let step = 0; step < n; step++) {
    i = (i + delta + n) % n;
    if (!itemAt(items, i).disabled) return i;
  }
  return from;
}

interface MenuPosition {
  top: number;
  left: number;
}

// Clear space the popup always keeps from the viewport's own edge (matches
// --space-2 - a plain number here, not a custom-property read, since this
// feeds arithmetic against getBoundingClientRect() values, not a CSS
// declaration).
const EDGE_MARGIN = 8;
// Gap between the trigger and the popup when it opens below/above it -
// matches the pre-portal popup's own `top: calc(100% + var(--space-1))`.
const TRIGGER_GAP = 4;

// One axis (horizontal or vertical) of the flip-then-clamp placement: try
// `primary` (the popup's default, unflipped offset - left-aligned to the
// trigger, or opening below it); if the popup would overflow the far edge
// there, try `flipped` instead (the other side - right-aligned to the
// trigger, or opening above it); if NEITHER fits, clamp `primary` into
// [0, viewportSize] so the popup stays as fully within the viewport as the
// available space allows, rather than settling for whichever of the two
// overflows less.
function resolveAxis(primary: number, flipped: number, size: number, viewportSize: number): number {
  if (primary + size <= viewportSize - EDGE_MARGIN) return primary;
  if (flipped >= EDGE_MARGIN) return flipped;
  return Math.max(EDGE_MARGIN, Math.min(primary, viewportSize - size - EDGE_MARGIN));
}

// The popup's position:fixed viewport coordinates, computed fresh every
// time it opens from the trigger's and popup's OWN measured rects (never
// assumed) - see the measuring useLayoutEffect below for why the popup has
// to already be in the DOM, rendered at some placeholder position, before
// this can run.
function computeMenuPosition(triggerRect: DOMRect, popupSize: { width: number; height: number }): MenuPosition {
  return {
    left: resolveAxis(triggerRect.left, triggerRect.right - popupSize.width, popupSize.width, window.innerWidth),
    top: resolveAxis(
      triggerRect.bottom + TRIGGER_GAP,
      triggerRect.top - TRIGGER_GAP - popupSize.height,
      popupSize.height,
      window.innerHeight,
    ),
  };
}

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
 * The popup itself renders through a portal to document.body, position:
 * fixed off the trigger's own getBoundingClientRect() (see
 * computeMenuPosition above), instead of as a normal absolutely-positioned
 * child - a Menu nested anywhere with a clipping ancestor (a rail row's own
 * scrollable list, a status row hard against the viewport's edge) otherwise
 * gets cut off, at its container's edge or the viewport's, with no way for
 * a descendant to escape either via CSS alone. Reopening always re-measures
 * rather than trusting a stale position; a scroll anywhere or a resize
 * while open closes the menu rather than continuously repositioning it -
 * simpler, and this app has no overlay that needs to survive a scroll today
 * (Dialog is a full-viewport modal with nothing to reposition; ModelSwitch's
 * own popover isn't portaled, so it scrolls for free with whatever it's
 * anchored to).
 */
export function Menu({ trigger, items, triggerTabIndex, variant = "default" }: MenuProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(() => firstEnabledIndex(items));
  const [position, setPosition] = useState<MenuPosition | null>(null);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popupRef = useRef<HTMLUListElement>(null);
  const itemRefs = useRef<(HTMLLIElement | null)[]>([]);
  const triggerId = useId();

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
    setActiveIndex(firstEnabledIndex(items));
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

  // Measures the trigger and the popup's own natural (unclamped) size and
  // corrects the naive position openMenu set to the actual flipped/clamped
  // one - see computeMenuPosition above. This is a layout effect, so it -
  // and the re-render its setPosition call causes - both complete before
  // the browser's next paint, in the same commit as the mount itself: nothing
  // ever visibly jumps, even though the popup is visible (never
  // visibility: hidden) for the brief moment between the two. Hiding it for
  // that moment was tried and reverted - see this task's report: it made
  // the popup unreachable to FocusScope's own tabbable() scan (an ancestor
  // walk that treats a hidden ancestor as making every descendant
  // untabbable, correctly, since that's the general case a widget nested
  // inside e.g. a collapsed accordion needs) for however long the popup
  // stayed hidden, which could lose the race against FocusScope's own
  // mount effect - moving focus nowhere instead of the first item.
  useLayoutEffect(() => {
    if (!isOpen) return;
    const triggerEl = triggerRef.current;
    const popupEl = popupRef.current;
    if (!triggerEl || !popupEl) return;
    setPosition(computeMenuPosition(triggerEl.getBoundingClientRect(), popupEl.getBoundingClientRect()));
  }, [isOpen]);

  function focusIndex(index: number) {
    if (index === -1) return;
    setActiveIndex(index);
    itemRefs.current[index]?.focus();
  }

  function selectItem(item: MenuItem) {
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
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        focusIndex(stepEnabledIndex(items, activeIndex, 1));
        break;
      case "ArrowUp":
        event.preventDefault();
        focusIndex(stepEnabledIndex(items, activeIndex, -1));
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
        const item = items[activeIndex];
        if (item) {
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
      default:
        break;
    }
  }

  // Outside click closes the menu. A click on the trigger itself is inside
  // rootRef too, so this never fights handleTriggerClick's own toggle; a
  // click inside the popup is inside popupRef, which rootRef alone can't
  // see now that the popup renders through a portal elsewhere in the DOM.
  useEffect(() => {
    if (!isOpen) return;
    function onDocumentMouseDown(event: MouseEvent) {
      const target = event.target as Node;
      if (rootRef.current?.contains(target) || popupRef.current?.contains(target)) return;
      closeMenu();
    }
    document.addEventListener("mousedown", onDocumentMouseDown);
    return () => document.removeEventListener("mousedown", onDocumentMouseDown);
  }, [isOpen, closeMenu]);

  // A scroll anywhere (capture-phase, so this also catches a scrollable
  // ancestor's own scroll, not just window's - "scroll" doesn't bubble, but
  // capture still reaches every ancestor on the way down to whatever
  // actually scrolled) or a viewport resize closes the menu - see this
  // component's own doc comment above for why closing, not repositioning.
  useEffect(() => {
    if (!isOpen) return;
    window.addEventListener("scroll", closeMenu, true);
    window.addEventListener("resize", closeMenu);
    return () => {
      window.removeEventListener("scroll", closeMenu, true);
      window.removeEventListener("resize", closeMenu);
    };
  }, [isOpen, closeMenu]);

  const triggerClassName = variant === "quiet" ? `${CLASS.trigger} ${CLASS.triggerQuiet}` : CLASS.trigger;

  return (
    <div ref={rootRef} className={CLASS.root}>
      <button
        ref={triggerRef}
        id={triggerId}
        type="button"
        tabIndex={triggerTabIndex}
        className={triggerClassName}
        aria-haspopup="menu"
        aria-expanded={isOpen}
        onClick={handleTriggerClick}
        onKeyDown={handleTriggerKeyDown}
      >
        {trigger}
      </button>
      {isOpen &&
        position &&
        createPortal(
          <FocusScope trap>
            {/* <ul role="menu">/<li role="menuitem"> is the WAI-ARIA APG menu
                pattern's own example markup (w3.org/WAI/ARIA/apg/patterns/menu),
                not an interactive role on an arbitrary static element. */}
            <ul
              ref={popupRef}
              // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ul/li is the ARIA APG's own menu markup, see above
              role="menu"
              aria-labelledby={triggerId}
              className={CLASS.popup}
              style={{ top: position.top, left: position.left }}
              onKeyDown={handleMenuKeyDown}
            >
              {items.map((item, index) => (
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
                  tabIndex={!item.disabled && index === activeIndex ? 0 : -1}
                  aria-disabled={item.disabled === true ? true : undefined}
                  className={`${CLASS.item} ${item.disabled ? CLASS.itemDisabled : ""}`}
                  ref={(node) => {
                    itemRefs.current[index] = node;
                  }}
                  onClick={() => selectItem(item)}
                >
                  {item.label}
                </li>
              ))}
            </ul>
          </FocusScope>,
          document.body,
        )}
    </div>
  );
}
