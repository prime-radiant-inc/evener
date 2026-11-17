// The document a pane lives in.
//
// Dockview moves a panel's DOM into a popout window, which is a separate
// Document with its own visibility and its own constructors. A pane-bound
// observer - a rendering clock, a document-level listener - must watch the
// reader's window, not the opener's, or a pane the user is looking at in a
// popout goes quiet the moment the opener's tab is hidden.
//
// DockHost provides this per panel from chromeStore's paneDocuments, which
// PaneScaffold publishes on every render and DockHost republishes at the
// popout move itself (Dockview moves the DOM without re-rendering the panes).
// The default is the module document: shell chrome that only ever lives on the
// opener, a pane whose scaffold registers no document, and the mobile host,
// which has no popouts.
import { createContext, useContext } from "react";

export const PaneDocumentContext = createContext<Document | null>(null);

/** The document the enclosing pane is rendered into, or the module document
 * when nothing in the tree names one. */
export function usePaneDocument(): Document {
  return useContext(PaneDocumentContext) ?? document;
}
