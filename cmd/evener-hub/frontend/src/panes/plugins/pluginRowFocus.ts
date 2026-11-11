// Returning keyboard focus to the row whose detail body just went away: a
// collapse over a completed install or remove reshapes the row (the split row
// and the whole-row button remount each other), and unmounts both the opener
// a modal confirm remembered and the body's own focused control, so the
// modal's own focus restore no-ops and focus falls to <body> - the
// lost-focus case rehomeFocus waits for. A row wrapper that carries
// data-plugin (and data-marketplace, where a row is addressed by pair) is
// the stable handle: it keeps its identity across the reshape, so focus can
// return to the row's main target - the whole-row button when installed, the
// split row's main area when not, the row's switch in the launcher, the
// first tabbable inside the row either way.

import { pluginComponentsKey } from "@evener/appwire-client/state/extensions";
import { type KeyboardEvent, type RefObject, useEffect, useRef } from "react";
import { rehomeFocus } from "../../widgets";

/** The id an expanded row's detail body carries, so its opener can point at
 * it with aria-controls. One function for opener and body, so the two cannot
 * disagree. The launcher addresses a row by name alone. */
export function pluginDetailBodyId(plugin: string, marketplace?: string): string {
  const name = encodeURIComponent(plugin);
  return marketplace === undefined
    ? `plugin-details-${name}`
    : `plugin-details-${name}-${encodeURIComponent(marketplace)}`;
}

/** The expanded row owns Escape for the details it opened: opening leaves
 * focus on the row, so Escape from the row must dismiss the details rather
 * than bubble to an ancestor overlay or pane. preventDefault is OverlayPanel's
 * own contract - the ancestor sees the Escape handled and keeps itself open. */
export function handleRowEscape(event: KeyboardEvent<HTMLElement>, expanded: boolean, onOpen: () => void): void {
  if (expanded && event.key === "Escape" && !event.defaultPrevented) {
    event.preventDefault();
    onOpen();
  }
}

/** The settings lists address a row by the plugin@marketplace pair; one
 * comparison, so the accordion and the row lookup cannot drift apart. */
export function samePluginTarget(
  a: { plugin: string; marketplace: string },
  b: { plugin: string; marketplace: string },
): boolean {
  return pluginComponentsKey(a) === pluginComponentsKey(b);
}

/** Focuses the row for `target`, if it is rendered under `root`; a row hidden
 * behind a collapsed marketplace or a filter miss is not, and focus stays
 * where it was. The launcher addresses rows by name alone; a target carrying
 * a marketplace must match it. */
export function focusPluginRow(root: ParentNode | null, target: { plugin: string; marketplace?: string }): void {
  if (root === null) return;
  const row = [...root.querySelectorAll<HTMLElement>("[data-plugin]")].find(
    (el) =>
      el.dataset.plugin === target.plugin &&
      (target.marketplace === undefined || el.dataset.marketplace === target.marketplace),
  );
  rehomeFocus(row ?? null);
}

/** Rehomes focus to the row the closing detail body belonged to: the target
 * is remembered while `open` is non-null, and on the close transition focus
 * returns to the row's main target when the collapse lost it (rehomeFocus's
 * judgement). A close that kept focus - collapsing through the row's own
 * action - leaves focus alone. */
export function usePluginRowFocusRehome(
  open: { plugin: string; marketplace?: string } | null,
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
    focusPluginRow(root.current, target);
  }, [open, root]);
}
