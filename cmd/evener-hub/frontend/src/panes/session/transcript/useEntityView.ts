import type { ThreadModel } from "@evener/appwire-client";
import { buildEntityView, type EntityView, watchFoldKey } from "@evener/appwire-client";
import { useMemo } from "react";
import { retainedActivityTree, useActivityPanelStore } from "../../../stores/activityPanel";

export function useEntityView(sessionRef: string, model: ThreadModel): Map<string, EntityView> {
  const tree = useActivityPanelStore((state) => retainedActivityTree(state.entries.get(sessionRef)));
  const load = useActivityPanelStore((state) => state.entries.get(sessionRef)?.load);
  const stale = load?.kind === "ready" && load.staleError !== undefined;
  const ended = load?.kind === "ended";
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
