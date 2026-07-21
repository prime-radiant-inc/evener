// Ports the tabbable-element semantics of the old serf-hub UI's
// cmd/serf-hub/assets/focus-trap.js (SerfFocusTrap) - same focusable
// selector list, same disabled/tabindex=-1/hidden filtering, same
// ancestor-walk visibility check. Internal to the focusscope widget: Dialog/
// Sheet/Menu never call this directly, they go through <FocusScope trap>.

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled]):not([type=hidden])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
  "summary",
].join(",");

// Walks up from `el` checking computed style on each ancestor, so an
// element hidden by an ancestor's display:none or visibility:hidden isn't
// treated as tabbable even though it matches FOCUSABLE_SELECTOR itself.
function isRendered(el: Element): boolean {
  if (el instanceof HTMLElement && el.hidden) return false;
  let node: Element | null = el;
  while (node) {
    const style = getComputedStyle(node);
    if (style.display === "none") return false;
    if (style.visibility === "hidden") return false;
    node = node.parentElement;
  }
  return true;
}

/** Every tabbable descendant of `root`, in document order. */
export function tabbable(root: HTMLElement): HTMLElement[] {
  const nodes = root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR);
  const out: HTMLElement[] = [];
  for (const node of nodes) {
    if (node.getAttribute("tabindex") === "-1") continue;
    if (!isRendered(node)) continue;
    out.push(node);
  }
  return out;
}
