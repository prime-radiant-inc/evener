import { useMemo } from "react";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { useSessionActivity, useSubagentCounts } from "../../stores/sessionActivity";
import { deriveScope } from "./statusScope";

export function useActivityScope(sessionRef: string) {
  // Observe the summary without requesting any collection pages. Only nav
  // resource changes can invalidate the navigation part of this scope.
  const resources = useNavigationStore((state) => state.resources);
  const { snapshot } = useSessionActivity(sessionRef);
  const subagents = useSubagentCounts(sessionRef);
  // biome-ignore lint/correctness/useExhaustiveDependencies: resources invalidates the memo's imperative navigation read.
  return useMemo(
    () => deriveScope(navigationStore.getState(), sessionRef, snapshot, subagents),
    [resources, sessionRef, snapshot, subagents],
  );
}
