// The control that reveals a hidden delegate row inside the activity sidebar.
// `reveal` in run.mjs interpolates this function into the page, and the jsdom
// regression imports it, so the tested source is the source the page runs.
//
// AgentsTab renders current rows before the inactive disclosure and inactive
// rows after it. The fold is therefore closed exactly when no activity anchor
// follows it in document order. Do not infer that from the container's total
// anchor count: one current row would hide a closed fold beside it (#5000).
// Queries stay inside the one mounted activity sidebar, matching the driver's
// single-sidebar call sites.
export function revealControl(container, refusedSelector) {
  if (!container) return null;
  const buttons = [...container.querySelectorAll("button")];
  const usable = (button) => !button.matches(refusedSelector);
  const fold = buttons.find((button) => usable(button) && button.textContent.trim().startsWith("Inactive subagents ("));
  // 4 is Node.DOCUMENT_POSITION_FOLLOWING. The numeric constant keeps the
  // serialized function independent of globals and of a browsing context.
  const DOCUMENT_POSITION_FOLLOWING = 4;
  const inactiveVisible = fold !== undefined && [...container.querySelectorAll("[data-activity-anchor]")]
    .some((node) => !!(fold.compareDocumentPosition(node) & DOCUMENT_POSITION_FOLLOWING));
  if (fold && !inactiveVisible) return fold;
  return buttons.find((button) => usable(button)
    && (button.textContent.trim().startsWith("Show ") || button.textContent.trim() === "Load more subagents")) ?? null;
}

// The one-page-turn expression `reveal` evaluates. It re-checks the target on
// every poll, selects the control above, and presses it only when it is the
// topmost element at its center, as a real press would (#3804). The regression
// test evaluates this exact expression so the driver cannot wire a different
// selection without failing.
export function revealControlStep(selector, container, refusedSelector) {
  return `(() => {
    if (document.querySelector(${JSON.stringify(selector)})) return { revealed: true };
    const b = (${revealControl})(document.querySelector(${JSON.stringify(container)}), ${JSON.stringify(refusedSelector)});
    if (!b) return null;
    b.scrollIntoView({ block: 'center' });
    const r = b.getBoundingClientRect();
    if (!b.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2))) return null;
    const before = document.querySelector(${JSON.stringify(container)}).textContent;
    b.click();
    return { before };
  })()`;
}
