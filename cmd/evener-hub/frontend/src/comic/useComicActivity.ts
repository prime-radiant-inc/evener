import {
  buildEntityView,
  type EntityView,
  projectSessionActivity,
  type SessionActivitySnapshot,
  type ThreadModel,
} from "@evener/appwire-client";
import { useMemo } from "react";
import { useSessionActivity } from "../stores/sessionActivity";
import { type ComicDelegateStrip, comicActivityContext, projectComicDelegates } from "./delegates";
import type { ComicProjectionContext } from "./projection";

export function useComicActivity(
  ref: string,
  model: ThreadModel,
): {
  snapshot: SessionActivitySnapshot | null;
  entities: ReadonlyMap<string, EntityView>;
  strips: readonly ComicDelegateStrip[];
  context: ComicProjectionContext;
  loadMore(): Promise<void>;
} {
  const { snapshot, loadMore } = useSessionActivity(ref, "session", "delegates");
  return useMemo(() => {
    const entities = buildEntityView({
      sessionRef: model.ref,
      tree: snapshot ? (projectSessionActivity(snapshot).tree ?? undefined) : undefined,
      delegates: model.delegates,
      turns: model.turns,
      stale: Boolean(snapshot?.delegates.error || snapshot?.delegates.unavailable),
      ended: snapshot?.context?.availability === "retained",
    });
    return {
      snapshot,
      entities,
      strips: projectComicDelegates(model, entities),
      context: comicActivityContext(model.ref, snapshot?.context ?? null, entities),
      loadMore: () => loadMore("delegates"),
    };
  }, [snapshot, model, loadMore]);
}
