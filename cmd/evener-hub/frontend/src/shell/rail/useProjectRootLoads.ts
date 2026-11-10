import { useCallback, useEffect, useRef } from "react";
import { navigationStore } from "../../stores/navigation/store";
import { type IsExpanded, projectLoadExpansionKeys, type RailGroupingMode } from "./railNodes";
import type { RailResources } from "./railPending";
import { allRailProjects, resourceState } from "./railResources";

/** Loads each expanded project's root once per client generation, and hands
 * the rail the in-flight guard plus a stable per-project retry callback. */
export function useProjectRootLoads(
  resources: RailResources,
  navigationMode: string,
  isExpanded: IsExpanded,
  groupingMode: RailGroupingMode,
) {
  const rootLoadsInFlight = useRef(new Set<string>());
  const rootGeneration = useRef("");
  const loadProjectRoot = useCallback((key: string) => {
    if (rootLoadsInFlight.current.has(key)) return;
    rootLoadsInFlight.current.add(key);
    void Promise.resolve(navigationStore.getState().loadProject(key))
      .catch(() => undefined)
      .finally(() => rootLoadsInFlight.current.delete(key));
  }, []);
  const currentLoadProjectRoot = useRef(loadProjectRoot);
  const projectRetryCallbacks = useRef(new Map<string, () => void>());
  const projectRetryCallback = useCallback((key: string): (() => void) => {
    const cached = projectRetryCallbacks.current.get(key);
    if (cached) return cached;
    const retry = () => {
      rootLoadsInFlight.current.delete(key);
      currentLoadProjectRoot.current(key);
    };
    projectRetryCallbacks.current.set(key, retry);
    return retry;
  }, []);
  useEffect(() => {
    currentLoadProjectRoot.current = loadProjectRoot;
    const ownedKeys = new Set(allRailProjects(resources).map((project) => project.key));
    for (const key of projectRetryCallbacks.current.keys()) {
      if (!ownedKeys.has(key)) projectRetryCallbacks.current.delete(key);
    }
  }, [loadProjectRoot, resources]);
  useEffect(() => {
    if (navigationMode !== "v3") return;
    const generation = navigationStore.getState().clientGenerationID;
    if (generation !== rootGeneration.current) {
      rootLoadsInFlight.current.clear();
      rootGeneration.current = generation;
    }
    for (const project of allRailProjects(resources)) {
      const expanded = projectLoadExpansionKeys(project, groupingMode).some((id) =>
        isExpanded(id, project.default_expanded ?? false),
      );
      if (
        !expanded ||
        project.loaded === true ||
        project.resourceError !== undefined ||
        (project.session_count ?? 0) === 0 ||
        resourceState(navigationStore.getState(), { kind: "project", projectKey: project.key })?.normalized
          ?.presence === "gone" ||
        rootLoadsInFlight.current.has(project.key)
      )
        continue;
      loadProjectRoot(project.key);
    }
  }, [navigationMode, resources, isExpanded, loadProjectRoot, groupingMode]);
  return { rootLoadsInFlight, loadProjectRoot, projectRetryCallback };
}
