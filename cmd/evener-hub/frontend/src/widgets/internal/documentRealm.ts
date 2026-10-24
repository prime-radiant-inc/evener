/**
 * The Document and Window a node actually lives in.
 *
 * A Dockview popout is a separate Window with its own Document and its own
 * constructors. Module-scope `document`/`window` always name the opener's
 * realm, and a cross-realm `instanceof` check rejects nodes that were created
 * in - or adopted into - another realm. Resolve through the node instead, and
 * read realm crossovers from nodeType, which every realm reports identically
 * (element 1, document 9).
 *
 * Callers resolve from their own root element on each render/effect, never
 * from a global captured at import: a widget rendered through a portal into a
 * popout's body belongs to the popout from its first commit.
 */
/** The document that owns `node`, or the module document for no node. */
export function ownerDocumentOf(node: Node | null | undefined): Document {
  return node?.ownerDocument ?? document;
}

/** The window of the document that owns `node`, or the module window. */
export function ownerWindowOf(node: Node | null | undefined): Window {
  return ownerDocumentOf(node).defaultView ?? window;
}
