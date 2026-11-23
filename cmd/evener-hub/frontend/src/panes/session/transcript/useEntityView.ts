import type { SessionActivityCollection, ThreadModel } from "@evener/appwire-client";
import { type EntityView, watchFoldKey } from "@evener/appwire-client";
import { useMemo } from "react";
import { useSessionActivity } from "../../../stores/sessionActivity";
import { sessionEntityViews } from "../../../transcriptDisplay/entityViews";

const ENTITY_COLLECTIONS: readonly SessionActivityCollection[] = ["jobs", "delegates"];

export function useEntityView(sessionRef: string, model: ThreadModel): Map<string, EntityView> {
  const { snapshot } = useSessionActivity(sessionRef, "session", ENTITY_COLLECTIONS);
  const watchKey = watchFoldKey(model.turns);

  // watchKey represents every watch-fold input; prose-only turns changes must
  // not rebuild the shared map, while watch payload enrichment must.
  // biome-ignore lint/correctness/useExhaustiveDependencies: watchKey covers the watch-relevant subset of model.turns
  return useMemo(
    () => sessionEntityViews({ sessionRef, snapshot, delegates: model.delegates, turns: model.turns }),
    [sessionRef, snapshot, model.delegates, watchKey],
  );
}
