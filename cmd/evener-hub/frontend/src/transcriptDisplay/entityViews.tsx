// entityViews.tsx is the single source EntityRef resolves ids from: the entity
// views available to it in this subtree. It is deliberately NOT part of
// TranscriptRenderContext. The session chrome renders EntityRef too - in a
// dense row's detail strip - and the chrome has no disclosure/config state to
// fabricate, so it cannot wrap its rows in TranscriptRenderProvider. A context
// that carries only the map keeps the chrome's owner (ActivityPanelBody) from
// inventing transcript state, and it moves the map's invalidation off the
// transcript render context: replacing the map now re-renders EntityRef
// consumers alone, not every transcript row.
//
// No provider means no map: EntityRef renders the id as plain text, exactly as
// it did before any map reached it.
import type { EntityView } from "@evener/appwire-client";
import { createContext, type ReactNode, useContext } from "react";

const EntityViewsContext = createContext<ReadonlyMap<string, EntityView> | undefined>(undefined);

/** Supplies the entity views EntityRef resolves ids from. The value is the map
 * itself, so a stable map is a stable context value and only a real map
 * replacement re-renders the consumers below. */
export function EntityViewsProvider({
  entities,
  children,
}: {
  entities?: ReadonlyMap<string, EntityView>;
  children: ReactNode;
}): ReactNode {
  return <EntityViewsContext.Provider value={entities}>{children}</EntityViewsContext.Provider>;
}

/** Returns the entity views available here, or undefined when no provider owns
 * them (a direct render, or the chrome before ActivityPanelBody mounts). */
export function useEntityViews(): ReadonlyMap<string, EntityView> | undefined {
  return useContext(EntityViewsContext);
}
