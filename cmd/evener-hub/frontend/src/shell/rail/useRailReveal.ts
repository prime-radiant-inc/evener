import type { NavigationSessionSummary } from "@evener/appwire-client";
import { isSettledGone, projectNodeExpansionKey } from "@evener/appwire-client/state/navigation";
import { type RefObject, useCallback, useEffect, useRef } from "react";
import {
  type ArchivedList,
  archivedListKey,
  loadMoreArchivedList,
  refreshArchivedList,
} from "../../stores/archivedList";
import { navigationStore } from "../../stores/navigation/store";
import { ARCHIVED_SECTION_KEY, LIVE_SECTION_KEY, PROJECTS_SECTION_KEY, TEST_RUNS_SECTION_KEY } from "./railExpansion";
import { pinSectionDisclosureID, type RailGroupingMode, revealExpansionIds } from "./railNodes";
import type { RailResources } from "./railPending";
import { allRailProjects, resourceData, resourceState } from "./railResources";

interface RevealRequestGuard {
  target: string;
  token: symbol;
}

interface RailRevealInputs {
  revealTarget: string | null | undefined;
  onRevealConsumed: (() => void) | undefined;
  resources: RailResources;
  archivedLists: Record<string, ArchivedList>;
  expandedOverrides: ReadonlyMap<string, boolean>;
  isExpanded: (id: string, defaultOpen: boolean) => boolean;
  setExpanded: (id: string, value: boolean) => void;
  groupingMode: RailGroupingMode;
  bodyRef: RefObject<HTMLElement | null>;
}

/** Scrolls the rail to the row for revealTarget, opening each fold on the way
 * and loading whatever resource the row lives in, one step per run until the
 * row renders or the target is settled gone. */
export function useRailReveal({
  revealTarget,
  onRevealConsumed,
  resources,
  archivedLists,
  expandedOverrides,
  isExpanded,
  setExpanded,
  groupingMode,
  bodyRef,
}: RailRevealInputs): void {
  const revealLookupInFlight = useRef<RevealRequestGuard | null>(null);
  const revealResourceRequests = useRef(new Map<string, RevealRequestGuard>());
  const revealCompletedTarget = useRef<string | null>(null);
  const revealRequestTarget = useRef<string | null>(null);

  useEffect(() => {
    if (revealRequestTarget.current === revealTarget) return;
    revealRequestTarget.current = revealTarget ?? null;
    revealLookupInFlight.current = null;
    revealResourceRequests.current.clear();
    revealCompletedTarget.current = null;
  }, [revealTarget]);

  const requestRevealResource = useCallback(
    (target: string, key: string, request: () => Promise<unknown> | undefined): void => {
      if (revealRequestTarget.current !== target || revealResourceRequests.current.has(key)) return;
      const guard: RevealRequestGuard = { target, token: Symbol(key) };
      revealResourceRequests.current.set(key, guard);
      let result: Promise<unknown> | undefined;
      try {
        result = request();
      } catch (error) {
        result = Promise.reject(error);
      }
      if (!result) {
        if (revealResourceRequests.current.get(key) === guard) revealResourceRequests.current.delete(key);
        return;
      }
      void result.catch(() => {
        if (revealRequestTarget.current !== target || revealResourceRequests.current.get(key) !== guard) return;
        revealResourceRequests.current.delete(key);
      });
    },
    [],
  );
  const consumeReveal = useCallback(() => {
    if (!revealTarget || revealCompletedTarget.current === revealTarget) return;
    revealCompletedTarget.current = revealTarget;
    onRevealConsumed?.();
  }, [revealTarget, onRevealConsumed]);

  // A reveal lands on a row that may be sitting inside a section fold the
  // reader closed (or that starts closed, like Archived). Open it and report
  // that the caller should re-run rather than scroll to a hidden row.
  const openRevealSection = useCallback(
    (key: string, defaultOpen: boolean) => {
      if (isExpanded(key, defaultOpen)) return true;
      setExpanded(key, true);
      return false;
    },
    [isExpanded, setExpanded],
  );
  // Where a revealed project's row lives: the fold it is rendered in, whether
  // that fold starts open, and the catalog that carries it. Archived is the
  // tier that folds under "Archived sessions" and starts closed; a test-run
  // project folds - and is catalogued - under "Test runs".
  const projectPlacement = useCallback(
    (location: { project_key?: string; tier?: string }) => {
      // A whole-archived project's sessions keep their own tier, so the
      // catalog is the reliable membership test there and the tier is not.
      const archived =
        location.tier === "archived" ||
        resources.archivedProjects.some((project) => project.key === location.project_key);
      if (archived)
        return { sectionKey: ARCHIVED_SECTION_KEY, defaultOpen: false, catalog: "archived_projects" as const };
      if (resources.testRuns.some((project) => project.key === location.project_key))
        return { sectionKey: TEST_RUNS_SECTION_KEY, defaultOpen: true, catalog: "test_runs" as const };
      return { sectionKey: PROJECTS_SECTION_KEY, defaultOpen: true, catalog: "projects" as const };
    },
    [resources],
  );

  useEffect(() => {
    if (!revealTarget) return;
    const row = Array.from(bodyRef.current?.querySelectorAll<HTMLElement>("[data-session-ref]") ?? []).find(
      (element) => element.dataset.sessionRef === revealTarget,
    );
    if (row && revealCompletedTarget.current !== revealTarget) {
      row.scrollIntoView({ block: "center", behavior: "smooth" });
      consumeReveal();
      return;
    }
    // The chain must name folds that actually render: only the Projects
    // section honors the grouping, so grouped ids apply to it alone while
    // the always-flat tiers keep flat ids whatever the mode. A test run's
    // archived rows still route to the archived-group fold; a whole-archived
    // project renders every row under its own node instead.
    const chain = [
      ...revealExpansionIds(resources.projects, resources.live, revealTarget, groupingMode),
      ...revealExpansionIds(resources.testRuns, [], revealTarget, "flat"),
      ...revealExpansionIds(resources.archivedProjects, [], revealTarget, "flat", { rowsUnderProjectNode: true }),
    ];
    const nextFold = chain.find((id) => expandedOverrides.get(id) !== true);
    if (nextFold) {
      setExpanded(nextFold, true);
      return;
    }
    const currentState = navigationStore.getState();
    const locationKey = { kind: "location", ref: revealTarget } as const;
    // Only a settled tombstone consumes the reveal: a stale one retained
    // across a generation reset may precede the fresh response showing the
    // session present again, and consuming here is irreversible.
    if (isSettledGone(resourceState(currentState, locationKey))) {
      consumeReveal();
      return;
    }
    const location = resourceData<{
      project_key?: string;
      tier?: string;
      pin_section_id?: string;
      session?: NavigationSessionSummary;
      top_level?: boolean;
      top_level_ref?: string;
    }>(currentState, locationKey);
    if (!location) {
      if (revealLookupInFlight.current?.target !== revealTarget) {
        const target = revealTarget;
        const guard: RevealRequestGuard = { target, token: Symbol(`location:${target}`) };
        revealLookupInFlight.current = guard;
        let request: Promise<unknown>;
        try {
          request = Promise.resolve(navigationStore.getState().lookupLocation(target));
        } catch (error) {
          request = Promise.reject(error);
        }
        void request
          .catch(() => undefined)
          .finally(() => {
            if (revealRequestTarget.current === target && revealLookupInFlight.current === guard)
              revealLookupInFlight.current = null;
          });
      }
      return;
    }
    if (!location.session) {
      consumeReveal();
      return;
    }
    // Delegate refs reveal their carrier in the flat navigation rail. An
    // archived fork original has its own inline row; the loaded fold chain
    // above reveals that row after its continuation arrives.
    const carrierRef =
      location.top_level === false && location.top_level_ref && location.top_level_ref !== revealTarget
        ? location.top_level_ref
        : null;
    const archivedForkOriginal = location.tier === "archived" && location.session.kind === "fork";
    if (carrierRef !== null && !archivedForkOriginal) {
      const carrierRow = Array.from(bodyRef.current?.querySelectorAll<HTMLElement>("[data-session-ref]") ?? []).find(
        (element) => element.dataset.sessionRef === carrierRef,
      );
      if (carrierRow && revealCompletedTarget.current !== revealTarget) {
        carrierRow.scrollIntoView({ block: "center", behavior: "smooth" });
        consumeReveal();
        return;
      }
      const carrierChain = [
        ...revealExpansionIds(resources.projects, resources.live, carrierRef, groupingMode),
        ...revealExpansionIds(resources.testRuns, [], carrierRef, "flat"),
        ...revealExpansionIds(resources.archivedProjects, [], carrierRef, "flat", { rowsUnderProjectNode: true }),
      ];
      const nextCarrierFold = carrierChain.find((id) => expandedOverrides.get(id) !== true);
      if (nextCarrierFold) {
        setExpanded(nextCarrierFold, true);
        return;
      }
    }
    if (location.project_key) {
      const projectState = resourceState(currentState, { kind: "project", projectKey: location.project_key });
      if (isSettledGone(projectState)) {
        consumeReveal();
        return;
      }
      const projectID = projectNodeExpansionKey(location.project_key);
      if (expandedOverrides.get(projectID) !== true) {
        setExpanded(projectID, true);
        return;
      }
      // One decision covers both which fold must open and which catalog holds
      // the row; deriving them separately is how a test run ends up opening
      // one fold while loading another catalog's rows.
      const placement = projectPlacement(location);
      if (!openRevealSection(placement.sectionKey, placement.defaultOpen)) return;
      requestRevealResource(revealTarget, `catalog:${placement.catalog}`, () =>
        navigationStore.getState().loadCatalog(placement.catalog),
      );
      requestRevealResource(revealTarget, `project:${location.project_key}`, () =>
        navigationStore.getState().loadProject(location.project_key as string),
      );
      // An archived row lives in its project's archived list, not in a
      // navigation resource: page the list until the row loads, then the fold
      // chain above opens it. A list that ends without the row cannot reveal it.
      if (location.tier === "archived") {
        const project = allRailProjects(resources).find((candidate) => candidate.key === location.project_key);
        const catalog = project?.catalog;
        const list = catalog ? archivedLists[archivedListKey(catalog, location.project_key)] : undefined;
        // A nested row (a fork original) is found through the row that carries
        // it, the location's top_level_ref.
        const rowRef = location.top_level === false ? location.top_level_ref : revealTarget;
        // The count effect creates the list when navigation counts archived
        // rows. A location can resolve to one while the count is still 0 (a
        // lagging revision, a deep link ahead of the count), so fetch it here.
        if (catalog && !list && (project?.archived_total ?? 0) === 0)
          requestRevealResource(revealTarget, `archived:${catalog}:${location.project_key}:first`, () =>
            refreshArchivedList(catalog, location.project_key as string),
          );
        if (catalog && list && !list.loading && !list.rows.some((row) => row.ref === rowRef)) {
          const cursor = list.nextCursor;
          if (cursor)
            requestRevealResource(revealTarget, `archived:${catalog}:${cursor}`, () =>
              loadMoreArchivedList(catalog, location.project_key as string),
            );
          else if (list.error === null) consumeReveal();
          // A failed first page has no cursor to page from, and the count
          // effect skips it (the count has not moved). Ask once per reveal.
          else
            requestRevealResource(revealTarget, `archived:${catalog}:${location.project_key}:retry`, () =>
              refreshArchivedList(catalog, location.project_key as string),
            );
        }
      }
      return;
    }
    if (location.pin_section_id) {
      if (!openRevealSection(pinSectionDisclosureID(location.pin_section_id), true)) return;
      requestRevealResource(revealTarget, "pin_catalog", () => navigationStore.getState().loadPinCatalog());
      requestRevealResource(revealTarget, `pin:${location.pin_section_id}`, () =>
        navigationStore.getState().loadPinSection(location.pin_section_id as string),
      );
      return;
    }
    const section = location.tier === "needs_you" ? "needs_you" : "live";
    // needs_you has no fold of its own, and the rail renders none of its rows
    // today, so Live is the only section that can hide this one.
    if (section === "live" && !openRevealSection(LIVE_SECTION_KEY, true)) return;
    requestRevealResource(revealTarget, `section:${section}`, () => navigationStore.getState().loadSection(section));
  }, [
    revealTarget,
    resources,
    expandedOverrides,
    groupingMode,
    consumeReveal,
    setExpanded,
    requestRevealResource,
    openRevealSection,
    projectPlacement,
    archivedLists,
    bodyRef,
  ]);
}
