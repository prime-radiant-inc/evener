import type { NavigationInvalidatedPayload, NavigationInvalidationTarget } from "../../protocol/types.gen";
import type { NavigationInvalidationWaiter } from "./revalidator";
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
  const matchesSession = (payload: NavigationInvalidatedPayload): boolean =>
    payload.targets.some(
      (target) =>
        target.kind === "all_loaded_projects" ||
        (target.kind === "section" && (target.section === "live" || target.section === "needs_you")) ||
        (target.kind === "pin_section" && target.sectionId === ctx.pinSectionId) ||
        (target.kind === "project" && target.projectKey === ctx.projectKey),
    );
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
