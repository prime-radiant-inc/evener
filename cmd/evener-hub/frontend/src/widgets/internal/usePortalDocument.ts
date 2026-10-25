import { type RefObject, useLayoutEffect, useState } from "react";
import { useOwnedDocument } from "./useOwnedDocument";

/**
 * The document a portal-only widget's panel belongs to, or null until its mount
 * marker has attached.
 *
 * A widget whose only DOM output is a portal has no element to read a realm
 * from on its first render: rendering the panel then would mount it in the
 * opener and move it after paint. The marker attaches during commit, and the
 * layout effect here re-renders before paint with the owning document, so the
 * panel's first mount is already in the right window.
 *
 * The document itself comes from `useOwnedDocument`, so it also follows a
 * Dockview move while mounted. Observation starts when the marker attaches,
 * and `enabled` lets a widget that stays mounted while closed stop it: a
 * closed panel has nothing to follow, and the owner document is re-read from
 * the node when it opens.
 */
export function usePortalDocument<T extends Node>(ref: RefObject<T | null>, enabled = true): Document | null {
  const [attached, setAttached] = useState(false);
  // The hook's render-time read answers from the node whenever the ref is
  // attached, which is exactly the value this re-render needs.
  const ownerDocument = useOwnedDocument(ref, enabled && attached);
  // After every commit, not keyed on `attached`: a marker may attach in a later
  // commit, and the ref object's identity does not change then. Track the live
  // state rather than only the first attach, so a marker that detaches renders
  // the panel away and one that re-attaches brings it back.
  useLayoutEffect(() => {
    const attachedNow = ref.current !== null;
    if (attached !== attachedNow) setAttached(attachedNow);
  });
  return attached && ref.current !== null ? ownerDocument : null;
}
