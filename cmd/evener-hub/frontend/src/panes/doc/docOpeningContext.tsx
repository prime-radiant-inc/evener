// The doc-opening provenance provider and helper (Task 4). A real opening
// producer (a file card, a prose link) reads the origin captured for the scope
// it renders in and merges it into the doc params it already builds, so a saved
// browser note can later reconcile against the ORIGINAL opening scope rather
// than whatever pane currently has focus.
//
// The context is supplied by the real producer surfaces: a Session pane for its
// own ref, a read-only Transcript pane for its ref, and EACH Zoom column for its
// displayed requestedRef. No producer looks up a focused pane, and no context
// (missing provider) leaves the params origin-less - which stays readable and
// session-bound, never silently assigned a same-ref owner.
import { createContext, type ReactNode, useContext } from "react";
import type { PaneLifetime } from "../../shell/paneLifetime";
import type { DocOpeningOrigin } from "./openDoc";

const DocOpeningOriginContext = createContext<DocOpeningOrigin | undefined>(undefined);

export function DocOpeningProvider({ origin, children }: { origin?: DocOpeningOrigin; children: ReactNode }) {
  return <DocOpeningOriginContext.Provider value={origin}>{children}</DocOpeningOriginContext.Provider>;
}

/** The captured opening origin for the current scope, or undefined with no provider. */
export function useDocOpeningOrigin(): DocOpeningOrigin | undefined {
  return useContext(DocOpeningOriginContext);
}

// The origin a pane's displayed session scope carries at opening time. Only the
// lifetime's OWN session source, still alive and matching the displayed ref,
// earns a browser-origin carrying that composer's runtime identity; every other
// scope (a child/focused column, a transcript source, a disposed lifetime) gets
// an explicit read-only source pane, never the source's owner.
export function docOpeningOriginFor(lifetime: PaneLifetime, displayedSessionRef: string): DocOpeningOrigin {
  if (lifetime.alive && lifetime.sourceType === "session" && lifetime.sourceRef === displayedSessionRef) {
    const composer = lifetime.composer;
    if (composer) return { kind: "browser-origin", paneId: lifetime.paneId, compositionOwnerId: composer.sourceId };
  }
  return { kind: "browser-readonly-session", sourcePaneId: lifetime.paneId };
}
