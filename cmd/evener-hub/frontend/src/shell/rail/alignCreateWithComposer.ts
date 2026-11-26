// The wide touch workspace can split its dock, wrap the status bar, or grow
// the composer. Only a focused conversation that reaches the bottom edge
// owns the rail action's baseline.
export function alignCreateWithComposer(action: HTMLButtonElement | null) {
  if (!action) return;
  const pane = action.closest("[data-pane-scaffold-root]");
  const workspace = pane?.closest('[data-testid="workspace-host"]');
  const footer = action.closest('[data-testid="pane-footer"]');
  const status = pane?.querySelector(':scope > [data-testid="pane-edge-footer"]');
  if (!pane || !workspace || !footer || !status) return;
  const body = action.ownerDocument.body;
  function clear() {
    body.removeAttribute("data-docked-composer-action");
    body.style.removeProperty("--docked-composer-action-center");
  }
  const measure = () => {
    const bounds = action.getBoundingClientRect();
    const bottom = workspace.getBoundingClientRect().bottom;
    if (!bounds.height || Math.abs(bottom - pane.getBoundingClientRect().bottom) > 1) {
      clear();
      return;
    }
    body.style.setProperty("--docked-composer-action-center", `${bottom - bounds.top - bounds.height / 2}px`);
    body.setAttribute("data-docked-composer-action", "");
  };
  const resize = new ResizeObserver(measure);
  for (const element of [action, footer, status, pane, workspace]) resize.observe(element);
  measure();
  return () => {
    resize.disconnect();
    clear();
  };
}
