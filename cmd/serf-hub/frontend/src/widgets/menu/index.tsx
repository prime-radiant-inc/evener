import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { FocusScope } from "../focusscope";
import { requireClass } from "../internal/requireClass";
import styles from "./menu.module.css";

export interface MenuItem {
  id: string;
  label: string;
  onSelect: () => void;
  disabled?: boolean;
}

export interface MenuProps {
  /** Visible content of the trigger button Menu renders (its own aria and
   * open/close wiring - see this task's report for why the trigger isn't
   * the imported Button widget). */
  trigger: ReactNode;
  items: MenuItem[];
}

const CLASS = {
  root: requireClass(styles.root, "menu.module.css", "root"),
  trigger: requireClass(styles.trigger, "menu.module.css", "trigger"),
  popup: requireClass(styles.popup, "menu.module.css", "popup"),
  item: requireClass(styles.item, "menu.module.css", "item"),
  itemDisabled: requireClass(styles.itemDisabled, "menu.module.css", "itemDisabled"),
};

function firstEnabledIndex(items: MenuItem[]): number {
  return items.findIndex((item) => !item.disabled);
}

function lastEnabledIndex(items: MenuItem[]): number {
  for (let i = items.length - 1; i >= 0; i--) {
    if (!items[i]!.disabled) return i;
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
    if (!items[i]!.disabled) return i;
  }
  return from;
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
 */
export function Menu({ trigger, items }: MenuProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(() => firstEnabledIndex(items));
  const rootRef = useRef<HTMLDivElement>(null);
  const itemRefs = useRef<(HTMLLIElement | null)[]>([]);

  function openMenu() {
    setActiveIndex(firstEnabledIndex(items));
    setIsOpen(true);
  }

  function closeMenu() {
    setIsOpen(false);
  }

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

  function handleTriggerKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (!isOpen) openMenu();
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
  // rootRef too, so this never fights handleTriggerClick's own toggle.
  useEffect(() => {
    if (!isOpen) return;
    function onDocumentMouseDown(event: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) {
        closeMenu();
      }
    }
    document.addEventListener("mousedown", onDocumentMouseDown);
    return () => document.removeEventListener("mousedown", onDocumentMouseDown);
  }, [isOpen]);

  return (
    <div ref={rootRef} className={CLASS.root}>
      <button
        type="button"
        className={CLASS.trigger}
        aria-haspopup="menu"
        aria-expanded={isOpen}
        onClick={handleTriggerClick}
        onKeyDown={handleTriggerKeyDown}
      >
        {trigger}
      </button>
      {isOpen && (
        <FocusScope trap>
          <ul role="menu" className={CLASS.popup} onKeyDown={handleMenuKeyDown}>
            {items.map((item, index) => (
              <li
                key={item.id}
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
        </FocusScope>
      )}
    </div>
  );
}
