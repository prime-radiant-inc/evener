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
 * Dockview move while mounted.
 */
export function usePortalDocument<T extends Node>(ref: RefObject<T | null>): Document | null {
  // The hook's render-time read answers from the node whenever the ref is
  // attached, which is exactly the value this re-render needs.
  const ownerDocument = useOwnedDocument(ref);
  const [attached, setAttached] = useState(false);
  useLayoutEffect(() => {
    if (!attached && ref.current !== null) setAttached(true);
  }, [attached, ref]);
  return attached && ref.current !== null ? ownerDocument : null;
}
