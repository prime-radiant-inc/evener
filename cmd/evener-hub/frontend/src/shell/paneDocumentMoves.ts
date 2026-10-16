// A live pane's DOM can move to another document: dockview's popout opens a
// window and adopts the group's existing nodes into it, and closing the
// popout moves them back. React never re-renders for an adoption, so an effect
// that bound DOCUMENT-level listeners at bind time keeps listening on the
// document the pane left - selections and gestures inside the popout window
// never arrive, and neither do the ones after the pane returns. The popout
// host announces each move through this module, and every affected surface
// re-derives its own node's current ownerDocument and rebinds.
export type PaneDocumentMoveListener = () => void;

const listeners = new Set<PaneDocumentMoveListener>();

export function subscribePaneDocumentMoves(listener: PaneDocumentMoveListener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function publishPaneDocumentMoved(): void {
  for (const listener of [...listeners]) {
    try {
      listener();
    } catch {
      // One consumer's fault cannot block the others from rebinding.
    }
  }
}
