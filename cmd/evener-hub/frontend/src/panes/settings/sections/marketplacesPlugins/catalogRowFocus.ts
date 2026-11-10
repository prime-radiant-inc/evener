// Returning focus to a browse-tree catalog row after the action that closed
// its sheet or confirm reshaped it. Installing replaces the split row (main
// area + trailing Install) with the whole-row button, and removing does the
// reverse; that remount unmounts the element FocusScope remembered as the
// opener, so its restore no-ops and focus falls to <body>. The row's <li>
// keeps its identity across the reshape (MarketplaceNode keys it by plugin
// name and carries data-plugin/data-marketplace), so it is the stable
// handle: find the row by that pair and focus its main target.

/** Focuses the browse row for plugin@marketplace, if it is rendered under
 * `root`; a row hidden behind a collapsed marketplace or a filter miss is
 * not, and focus stays where it was. */
export function focusCatalogRow(root: ParentNode, target: { plugin: string; marketplace: string }): void {
  // Compared by attribute value, not a selector: the pair is matched exactly,
  // with no attribute-selector escaping to get right.
  const row = [...root.querySelectorAll<HTMLLIElement>("li[data-plugin]")].find(
    (li) => li.dataset.plugin === target.plugin && li.dataset.marketplace === target.marketplace,
  );
  // The main target is the first button inside the row in both shapes: the
  // whole-row button when installed, the split row's main area when not.
  row?.querySelector<HTMLButtonElement>("button")?.focus();
}
