// entityViews.tsx is the single source EntityRef resolves ids from: the entity
// views available to it in this subtree. It is deliberately NOT part of
// TranscriptRenderContext. Surfaces outside the transcript subtree read the
// map too - the Overview activity sidebar's watch detail strip does - and they
// have no disclosure/config state to fabricate, so they cannot wrap their
// rows in TranscriptRenderProvider. A context that carries only the map keeps
// those readers from inventing transcript state, and it moves the map's
// invalidation off the transcript render context: replacing the map now
// re-renders EntityRef consumers alone, not every transcript row.
//
// No provider means no map: EntityRef renders the id as plain text, exactly as
// it did before any map reached it.
import {
  buildEntityView,
  type EntityView,
  type EvenerDelegateInfo,
  projectSessionActivity,
  type SessionActivitySnapshot,
  type TurnModel,
} from "@evener/appwire-client";
import { createContext, type ReactNode, useContext } from "react";

/** Builds the entity map a surface can share from evidence it already holds:
 * the session-activity snapshot, plus a transcript's optional turns and live
 * delegate overlay. It owns no reads. */
export function sessionEntityViews({
  sessionRef,
  snapshot,
  delegates,
  turns = [],
}: {
  sessionRef: string;
  snapshot: SessionActivitySnapshot | null;
  delegates?: EvenerDelegateInfo[];
  turns?: TurnModel[];
}): Map<string, EntityView> {
  const tree = snapshot ? (projectSessionActivity(snapshot).tree ?? undefined) : undefined;
  const stale = Boolean(
    snapshot?.delegates.error || snapshot?.jobs.error || snapshot?.delegates.unavailable || snapshot?.jobs.unavailable,
  );
  const ended = snapshot?.context?.availability === "retained";
  return buildEntityView({ sessionRef, tree, delegates, turns, stale, ended });
}

const EntityOwnerRefContext = createContext<string | undefined>(undefined);

const EntityViewsContext = createContext<ReadonlyMap<string, EntityView> | undefined>(undefined);

/** Supplies the entity views EntityRef resolves ids from. The value is the map
 * itself, so a stable map is a stable context value and only a real map
 * replacement re-renders the consumers below. */
export function EntityViewsProvider({
  entities,
  ownerRef,
  children,
}: {
  entities?: ReadonlyMap<string, EntityView>;
  ownerRef?: string;
  children: ReactNode;
}): ReactNode {
  return (
    <EntityOwnerRefContext.Provider value={ownerRef}>
      <EntityViewsContext.Provider value={entities}>{children}</EntityViewsContext.Provider>
    </EntityOwnerRefContext.Provider>
  );
}

/** Returns the entity views available here, or undefined when no provider owns
 * them (a direct render, or a surface outside the transcript subtree). */
export function useEntityViews(): ReadonlyMap<string, EntityView> | undefined {
  return useContext(EntityViewsContext);
}

export function useEntityOwnerRef(): string | undefined {
  return useContext(EntityOwnerRefContext);
}
