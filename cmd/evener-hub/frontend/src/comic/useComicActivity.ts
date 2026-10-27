import {
  buildEntityView,
  type EntityView,
  projectSessionActivity,
  type SessionActivitySnapshot,
  type ThreadModel,
} from "@evener/appwire-client";
import { useMemo } from "react";
import { useSessionActivity } from "../stores/sessionActivity";
import { comicSessionRef } from "./comicSessionIdentity";
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
    // These stores publish independently during Clear. Never lend another
    // session's activity or ancestry to the model currently on screen.
    const current = snapshot?.context?.sessionId === (model.imageSessionId ?? model.threadId) ? snapshot : null;
    const ownerRef = comicSessionRef(model, current?.context);
    const entities = buildEntityView({
      sessionRef: ownerRef,
      tree: current ? (projectSessionActivity(current).tree ?? undefined) : undefined,
      delegates: model.delegates,
      turns: model.turns,
      stale: Boolean(current?.delegates.error || current?.delegates.unavailable),
      ended: current?.context?.availability === "retained",
    });
    return {
      snapshot: current,
      entities,
      strips: projectComicDelegates(model, entities, ownerRef),
      context: { ...comicActivityContext(ownerRef, current?.context ?? null, entities), ownerRef },
      loadMore: () => loadMore("delegates"),
    };
  }, [snapshot, model, loadMore]);
}
