import type { NavigationInvalidatedPayload, NavigationInvalidationTarget } from "@evener/appwire-client";
import {
  matchesTarget,
  type NavigationInvalidationWaiter,
  navigationParamsToResourceKey,
  type ResourceKey,
} from "@evener/appwire-client/state/navigation";
import { selectLiveRows, selectNeedsYouRows } from "./selectors";
import { awaitNavigationConvergence, navigationStore } from "./store";

export interface ShutdownTargetContext {
  pinSectionId?: string | null;
  projectKey?: string | null;
}

export interface ShutdownConvergence {
  targets: NavigationInvalidationTarget[];
  matchesSession: (payload: NavigationInvalidatedPayload) => boolean;
  sessionSettled: () => boolean;
  arm: () => NavigationInvalidationWaiter;
  converge: (first: NavigationInvalidationWaiter) => Promise<void>;
}

/** The resource scopes every shutdown changes: the two session tiers a live
 * session can leave. */
const SESSION_SECTION_KEYS: readonly ResourceKey[] = [
  navigationParamsToResourceKey({ resource: "section", section: "live", representationVersion: 3 }),
  navigationParamsToResourceKey({ resource: "section", section: "needs_you", representationVersion: 3 }),
];

/** Shared shutdown-convergence ritual for the rail and session-chrome
 * shutdown actions: invalidation targets, the receipt predicate, the
 * settled check (ref gone from the live tiers, which tree.go builds from
 * live roster entries, so ended sessions leave both), and waiter arming
 * with the unhandled-rejection guard attached once per waiter. */
export function buildShutdownConvergence(ref: string, ctx: ShutdownTargetContext): ShutdownConvergence {
  const targets: NavigationInvalidationTarget[] = [
    { kind: "section", section: "live" },
    { kind: "section", section: "needs_you" },
    ...(ctx.pinSectionId ? [{ kind: "pin_section", sectionId: ctx.pinSectionId } as const] : []),
    ...(ctx.projectKey ? [{ kind: "project", projectKey: ctx.projectKey } as const] : []),
  ];
  const scopeKeys: ResourceKey[] = [
    ...SESSION_SECTION_KEYS,
    ...(ctx.pinSectionId
      ? [
          navigationParamsToResourceKey({
            resource: "pin_section",
            sectionId: ctx.pinSectionId,
            representationVersion: 3,
          }),
        ]
      : []),
    ...(ctx.projectKey
      ? [
          navigationParamsToResourceKey({
            resource: "project",
            projectKey: ctx.projectKey,
            representationVersion: 3,
          }),
        ]
      : []),
  ];
  // One target rule for every consumer. A receipt wakes this waiter only when
  // it names a scope the shutdown covers. The all_loaded_projects wildcard
  // names every project resource, so it wakes a waiter with a project scope
  // and not one whose session left no project behind.
  const matchesSession = (payload: NavigationInvalidatedPayload): boolean =>
    payload.targets.some((target) => scopeKeys.some((key) => matchesTarget(key, target)));
  const sessionSettled = (): boolean => {
    const state = navigationStore.getState();
    return (
      selectLiveRows(state).every((row) => row.ref !== ref) && selectNeedsYouRows(state).every((row) => row.ref !== ref)
    );
  };
  const arm = (): NavigationInvalidationWaiter => {
    const waiter = navigationStore.getState().awaitNavigationInvalidation(matchesSession);
    void waiter.promise.catch(() => undefined);
    return waiter;
  };
  const converge = (first: NavigationInvalidationWaiter): Promise<void> =>
    awaitNavigationConvergence(first, targets, {
      settled: sessionSettled,
      rearm: arm,
    });
  return { targets, matchesSession, sessionSettled, arm, converge };
}
