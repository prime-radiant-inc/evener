// The keyboard/DOM contract shared by the app's input-driven listbox pickers:
// an active option identified by KEY (not list position, because rows arrive
// and are replaced asynchronously), its DOM id, the Arrow/Home/End map, and
// scroll-into-view. Real DOM focus stays on the combobox input and the
// active option is named through aria-activedescendant; Enter/click handling
// stays with the caller, whose rows mean different things.
//
// ModelCatalog and PathField both carried this engine (~120 duplicated lines
// with small drift) before it was extracted here.

import { type KeyboardEvent, useEffect, useId, useState } from "react";

/** The DOM id of an option row in a listbox. Keys are stable row identities,
 * not positions, so a row that moves keeps its id. */
export function optionDomId(listboxId: string, key: string): string {
  return `${listboxId}-${key}`;
}

/** aria-activedescendant for a listbox, or undefined when nothing is active. */
export function ariaActiveDescendant(listboxId: string, activeKey: string | undefined): string | undefined {
  return activeKey === undefined ? undefined : optionDomId(listboxId, activeKey);
}

export interface UseListboxResult {
  /** The listbox element's own id (React's useId), for the container and for
   * aria-controls on the driving input. */
  listboxId: string;
  /** The stored key's position among `keys`, or -1 when it names no row. */
  activeIndex: number;
  /** The active key only while it resolves to a listed row - the value an
   * aria-activedescendant may name. */
  activeDescendantKey: string | undefined;
  setActiveKey: (key: string | undefined) => void;
  /** ArrowDown/ArrowUp/Home/End, with the same clamp and preventDefault rules
   * both pickers had. Returns true when the key was consumed; Enter and every
   * other key stay with the caller. */
  handleNavigationKey: (event: KeyboardEvent<HTMLElement>) => boolean;
}

export function useListbox(keys: readonly string[]): UseListboxResult {
  const listboxId = useId();
  const [activeKey, setActiveKey] = useState<string | undefined>(undefined);
  const activeIndex = activeKey === undefined ? -1 : keys.indexOf(activeKey);
  const activeDescendantKey = activeIndex >= 0 ? keys[activeIndex] : undefined;

  // Keep the highlighted row visible inside the list's own scroll container -
  // both for the current value on open and for keyboard walks past the fold.
  // scrollIntoView is called optionally: jsdom implements none at all.
  useEffect(() => {
    if (activeKey === undefined) return;
    document.getElementById(optionDomId(listboxId, activeKey))?.scrollIntoView?.({ block: "nearest" });
  }, [activeKey, listboxId]);

  function handleNavigationKey(event: KeyboardEvent<HTMLElement>): boolean {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        if (keys.length > 0) {
          setActiveKey(keys[activeIndex < 0 ? 0 : Math.min(activeIndex + 1, keys.length - 1)]);
        }
        return true;
      case "ArrowUp":
        event.preventDefault();
        if (keys.length > 0) {
          setActiveKey(keys[activeIndex < 0 ? 0 : Math.max(activeIndex - 1, 0)]);
        }
        return true;
      case "Home":
        if (keys.length === 0) return false;
        event.preventDefault();
        setActiveKey(keys[0]);
        return true;
      case "End":
        if (keys.length === 0) return false;
        event.preventDefault();
        setActiveKey(keys[keys.length - 1]);
        return true;
      default:
        return false;
    }
  }

  return { listboxId, activeIndex, activeDescendantKey, setActiveKey, handleNavigationKey };
}
