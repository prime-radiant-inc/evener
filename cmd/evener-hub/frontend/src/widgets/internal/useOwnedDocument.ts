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
  // The node itself answers during every render, so a widget moved while
  // observation was off (a closed menu) opens in its new document on the first
  // commit instead of being corrected after it has already focused there. The
  // tracked state exists to schedule renders for moves that happen while the
  // widget is live.
  const ownerDocument = ref.current === null ? doc : ownerDocumentOf(ref.current);
  // The ref attaches during commit, after this render resolved a null node, so
  // a widget mounted directly in a popout starts on the opener: re-read the
  // attached node after every commit and correct the render it schedules.
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
    let stopped = false;
    // One pending recheck at a time: every document mutation calls `check`,
    // and a second chain would be untracked by `recheck` and survive cleanup.
    const schedule = () => {
      if (stopped || recheck !== undefined) return;
      recheck = setTimeout(() => {
        recheck = undefined;
        check();
      }, ADOPTION_RECHECK_MS);
    };
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
        schedule();
      }
    };
    const stop = watchDocument(doc, check);
    return () => {
      stopped = true;
      stop();
      if (recheck !== undefined) clearTimeout(recheck);
    };
  }, [doc, enabled, ref]);
  return ownerDocument;
}
