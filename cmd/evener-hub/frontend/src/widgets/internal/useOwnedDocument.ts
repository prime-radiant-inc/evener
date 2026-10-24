import { type RefObject, useEffect, useState } from "react";
import { ownerDocumentOf } from "./documentRealm";

/**
 * The document a widget's root node lives in, kept current when Dockview moves
 * that node between windows without a React render (a panel returned from a
 * closing popout, or adopted into a new one).
 *
 * A render-time read alone would leave an open overlay's portal, listeners and
 * placement on the window it used to be in. Watching the resolved document for
 * the root leaving it turns that move into a render, so every effect keyed on
 * the returned document rebinds.
 *
 * Observations run only while `enabled` - an idle menu should not watch the
 * whole document.
 */
export function useOwnedDocument<T extends Node>(ref: RefObject<T | null>, enabled = true): Document {
  const [doc, setDoc] = useState<Document>(() => ownerDocumentOf(ref.current));
  // Catch a move that happened before this effect ran (the ref attaches during
  // commit, after the initial state was computed).
  useEffect(() => {
    const next = ownerDocumentOf(ref.current);
    if (next !== doc) setDoc(next);
  });
  useEffect(() => {
    if (!enabled) return;
    const root = ref.current;
    if (root === null) return;
    const observer = new MutationObserver(() => {
      const next = ownerDocumentOf(root);
      if (next !== doc) setDoc(next);
    });
    // The removal that starts an adoption happens in the document we resolved.
    observer.observe(doc.documentElement, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, [doc, enabled, ref]);
  return doc;
}
