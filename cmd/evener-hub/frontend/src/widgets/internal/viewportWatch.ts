import { ownerWindowOf } from "./documentRealm";

/**
 * Fire `onScroll` (capture, so a scrollable descendant's own scroll counts) and
 * `onResize` for as long as the returned disposer is uninstalled, on the window
 * the node belongs to. A Dockview popout has its own window; the opener's never
 * sees that window's scrolls or resizes.
 */
export function watchViewport(
  node: Node | null | undefined,
  onScroll: EventListener,
  onResize: EventListener = onScroll,
): () => void {
  const win = ownerWindowOf(node);
  win.addEventListener("scroll", onScroll, true);
  win.addEventListener("resize", onResize);
  return () => {
    win.removeEventListener("scroll", onScroll, true);
    win.removeEventListener("resize", onResize);
  };
}
