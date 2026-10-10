import type { SessionActivityCollection, ThreadModel } from "@evener/appwire-client";
import { buildEntityView, type EntityView, projectSessionActivity, watchFoldKey } from "@evener/appwire-client";
import { useMemo } from "react";
import { useSessionActivity } from "../../../stores/sessionActivity";

const ENTITY_COLLECTIONS: readonly SessionActivityCollection[] = ["jobs", "delegates"];

export function useEntityView(sessionRef: string, model: ThreadModel): Map<string, EntityView> {
  const { snapshot } = useSessionActivity(sessionRef, "session", ENTITY_COLLECTIONS);
  const tree = useMemo(() => (snapshot ? (projectSessionActivity(snapshot).tree ?? undefined) : undefined), [snapshot]);
  const stale = Boolean(
    snapshot?.delegates.error || snapshot?.jobs.error || snapshot?.delegates.unavailable || snapshot?.jobs.unavailable,
  );
  const ended = snapshot?.context?.availability === "retained";
  const watchKey = watchFoldKey(model.turns);

  // watchKey represents every watch-fold input; prose-only turns changes must
  // not rebuild the shared map, while watch payload enrichment must.
  // biome-ignore lint/correctness/useExhaustiveDependencies: watchKey covers the watch-relevant subset of model.turns
  return useMemo(
    () =>
      buildEntityView({
        sessionRef,
        tree,
        delegates: model.delegates,
        turns: model.turns,
        stale,
        ended,
      }),
    [sessionRef, tree, model.delegates, watchKey, stale, ended],
  );
}
