// Returning focus to a browse-tree catalog row after the action that closed
// its sheet or confirm reshaped it. Installing replaces the split row (main
// area + trailing Install) with the whole-row button, and removing does the
// reverse; that remount unmounts the element FocusScope remembered as the
// opener, so the modal's own focus restore no-ops and focus falls to <body> -
// the lost-focus case rehomeFocus waits for. The row's <li> keeps its
// identity across the reshape (MarketplaceNode keys it by plugin name and
// carries data-plugin/data-marketplace), so it is the stable handle: find
// the row by that pair and hand focus to its main target - the whole-row
// button when installed, the split row's main area when not, the first
// tabbable inside the <li> either way.

import { type RefObject, useEffect, useRef } from "react";
import { rehomeFocus } from "../../../../widgets";

/** Focuses the browse row for plugin@marketplace, if it is rendered under
 * `root`; a row hidden behind a collapsed marketplace or a filter miss is
 * not, and focus stays where it was. */
export function focusCatalogRow(root: ParentNode | null, target: { plugin: string; marketplace: string }): void {
  if (root === null) return;
  // Compared by attribute value, not a selector: the pair is matched exactly,
  // with no attribute-selector escaping to get right.
  const row = [...root.querySelectorAll<HTMLLIElement>("li[data-plugin]")].find(
    (li) => li.dataset.plugin === target.plugin && li.dataset.marketplace === target.marketplace,
  );
  rehomeFocus(row ?? null);
}

/** Rehomes focus to the browse row the closing modal opened from: the target
 * is remembered while `open` is non-null, and on the close transition focus
 * returns to the row's main target when the modal's own restore lost the
 * opener to the reshape (rehomeFocus's judgement). A restore that worked - a
 * plain dismiss, an opener that still exists - leaves focus alone. */
export function useCatalogRowFocusRehome(
  open: { plugin: string; marketplace: string } | null,
  root: RefObject<HTMLElement | null>,
): void {
  const opened = useRef(open);
  useEffect(() => {
    if (open !== null) {
      opened.current = open;
      return;
    }
    const target = opened.current;
    if (target === null) return;
    opened.current = null;
    focusCatalogRow(root.current, target);
  }, [open, root]);
}
