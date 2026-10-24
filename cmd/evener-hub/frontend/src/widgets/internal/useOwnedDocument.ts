import { type RefObject, useEffect, useState } from "react";
import { ownerDocumentOf } from "./documentRealm";

interface DocumentWatch {
  readonly observer: MutationObserver;
  readonly subscribers: Set<() => void>;
}

// One observer per document, however many widgets follow ownership in it: rail
// rows and transcript controls mount these widgets repeatedly, and a subtree
// observer per widget would multiply document-wide work by their count.
const watches = new WeakMap<Document, DocumentWatch>();

function watchDocument(doc: Document, subscriber: () => void): () => void {
  let watch = watches.get(doc);
  if (watch === undefined) {
    const subscribers = new Set<() => void>();
    const observer = new MutationObserver(() => {
      for (const notify of subscribers) notify();
    });
    // The removal that starts an adoption happens in the document the node is
    // leaving, so that document's mutations are the signal.
    observer.observe(doc.documentElement, { childList: true, subtree: true });
    watch = { observer, subscribers };
    watches.set(doc, watch);
  }
  watch.subscribers.add(subscriber);
  return () => {
    watch?.subscribers.delete(subscriber);
    if (watch !== undefined && watch.subscribers.size === 0) {
      watch.observer.disconnect();
      watches.delete(doc);
    }
  };
}

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
    return watchDocument(doc, () => {
      const next = ownerDocumentOf(root);
      if (next !== doc) setDoc(next);
    });
  }, [doc, enabled, ref]);
  return doc;
}
