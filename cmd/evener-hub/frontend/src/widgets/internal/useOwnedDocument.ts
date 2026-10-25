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

/** How long a detached node may keep looking for its destination document,
 * and how often. A Dockview move removes and re-inserts within a frame. */
const ADOPTION_GRACE_MS = 2000;
const ADOPTION_RECHECK_MS = 16;

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
    let recheck: ReturnType<typeof setTimeout> | undefined;
    let detachedAt: number | undefined;
    const check = () => {
      const next = ownerDocumentOf(root);
      if (next !== doc) {
        setDoc(next);
        return;
      }
      // A move can detach the node and insert it later. The removal is the
      // only mutation the watched document sees; the insertion happens in a
      // document nothing is watching yet, so re-check until it lands. The
      // grace period starts when the detachment is seen, not when the effect
      // attached: a menu open for hours can still be moved. Bounded, so a node
      // removed for good (a closed pane) stops polling.
      if (root.isConnected) {
        detachedAt = undefined;
        return;
      }
      detachedAt ??= Date.now();
      if (Date.now() - detachedAt < ADOPTION_GRACE_MS) {
        recheck = setTimeout(check, ADOPTION_RECHECK_MS);
      }
    };
    const stop = watchDocument(doc, check);
    return () => {
      stop();
      if (recheck !== undefined) clearTimeout(recheck);
    };
  }, [doc, enabled, ref]);
  return doc;
}
