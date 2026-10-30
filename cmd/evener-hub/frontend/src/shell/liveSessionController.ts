import { adjacentLiveSessionRef } from "./rail/liveSessionCycle";

// Live-session navigation for session.liveNext/session.livePrevious. The
// controller owns the demand state machine: which page loads are in flight,
// who owns each, and whether a completing load may still navigate. Every read
// of the world comes through LiveSessionDeps, so it runs without stores or a
// DOM.
//
// The live section pages forward like needs_you. At the last LOADED live row,
// next demand-loads the following page and continues into it rather than
// wrapping over the loaded subset, which would skip every live session behind
// the remaining count. previous wraps within loaded rows (pages only page
// forward, so the true tail is unknowable until every page is in); an
// UNLOADED section instead demand-loads through the remaining pages to the
// tail.
export interface LiveSessionDeps {
  generation(): string;
  liveRefs(): readonly string[];
  liveRemaining(): number;
  /** The live section has no loaded rows yet and a read would settle it. */
  canDemandUnloaded(): boolean;
  nextOffset(): number;
  /** Resolves true when the page settled with data, false on a failed read. */
  loadPage(offset: number): Promise<boolean>;
  focusedPaneId(): string | null;
  focusedSessionRef(): string | null;
  /** Monotonic; changes whenever the route does. */
  routeEpoch(): number;
  /** A palette, modal or editable focus swallows a completing navigation. */
  completionBlocked(): boolean;
  openLiveSession(ref: string): void;
}

export interface LiveSessionController {
  next(): void;
  previous(): void;
  dispose(): void;
}

type Direction = "next" | "previous";

interface DemandOwner {
  intent: number;
  generation: string;
  pane: string | null;
  ref: string | null;
  routeEpoch: number;
  beforeRefs: ReadonlySet<string>;
}

export function createLiveSessionController(deps: LiveSessionDeps): LiveSessionController {
  let mounted = true;
  // Bumped by a new demand or a direct navigation. A press that dedupes
  // against a still-fresh in-flight demand is the same intent and does not
  // bump, or the demand's own completion would go inert.
  let intent = 0;
  let seenGeneration = deps.generation();
  // In-flight loads only, keyed by page and direction: an opposite-direction
  // press against the same page is a new intent that supersedes the old one.
  const demanded = new Map<string, DemandOwner>();

  // The guards the owner captured still match the current world. Palette and
  // modal are completion-only checks: the dispatcher does not deliver the
  // chord while either is open.
  const ownerFresh = (owner: DemandOwner): boolean =>
    deps.generation() === owner.generation &&
    deps.routeEpoch() === owner.routeEpoch &&
    deps.focusedPaneId() === owner.pane &&
    deps.focusedSessionRef() === owner.ref;

  function demandPage(direction: Direction, beforeRefs: ReadonlySet<string>): void {
    const offset = deps.nextOffset();
    const key = `${offset}:${direction}`;
    const existing = demanded.get(key);
    // A fresh owner is the same intent, still waiting on this page. A stale
    // one (the user left and returned, the client reconnected) is adopted by
    // rebinding fresh guards, so its completion navigates when the load lands.
    if (existing && ownerFresh(existing)) return;
    intent++;
    const owner: DemandOwner = {
      intent,
      generation: deps.generation(),
      pane: deps.focusedPaneId(),
      ref: deps.focusedSessionRef(),
      routeEpoch: deps.routeEpoch(),
      beforeRefs,
    };
    demanded.set(key, owner);
    void deps
      .loadPage(offset)
      .then((loaded) => {
        // A newer press adopted this load by rebinding the record: only the
        // current owner's completion acts.
        if (demanded.get(key) !== owner) return;
        // A failed load is no longer in flight, so the next press retries.
        demanded.delete(key);
        if (!loaded) return;
        // The rows belong to the generation, route, pane and session the
        // press named; focus moving into an editable mid-flight means the
        // keydown would have been swallowed, so navigating would surprise a
        // typing user.
        if (
          !mounted ||
          owner.intent !== intent ||
          deps.generation() !== owner.generation ||
          deps.routeEpoch() !== owner.routeEpoch ||
          deps.focusedPaneId() !== owner.pane ||
          deps.focusedSessionRef() !== owner.ref ||
          deps.completionBlocked()
        )
          return;
        const refs = deps.liveRefs();
        if (direction === "next") {
          const newlyLoaded = refs.find((ref) => !owner.beforeRefs.has(ref));
          if (newlyLoaded) deps.openLiveSession(newlyLoaded);
          return;
        }
        // previous: the tail sits behind any remaining pages.
        if (deps.liveRemaining() > 0) {
          demandPage("previous", owner.beforeRefs);
          return;
        }
        const last = refs[refs.length - 1];
        if (last) deps.openLiveSession(last);
      })
      .catch(() => {
        if (demanded.get(key) === owner) demanded.delete(key);
      });
  }

  // A reconnect makes every recorded demand belong to the previous generation.
  function resetOnNewGeneration(): void {
    const generation = deps.generation();
    if (generation === seenGeneration) return;
    seenGeneration = generation;
    demanded.clear();
  }

  return {
    next() {
      resetOnNewGeneration();
      const refs = deps.liveRefs();
      const current = deps.focusedSessionRef();
      const atLastLoaded = current !== null && refs.length > 0 && refs[refs.length - 1] === current;
      if ((refs.length === 0 && deps.canDemandUnloaded()) || (atLastLoaded && deps.liveRemaining() > 0)) {
        demandPage("next", new Set(refs));
        return;
      }
      intent++; // a direct navigation supersedes an in-flight demand
      const target = adjacentLiveSessionRef(refs, current, "next");
      if (target !== null) deps.openLiveSession(target);
    },
    previous() {
      resetOnNewGeneration();
      const refs = deps.liveRefs();
      const current = deps.focusedSessionRef();
      if (refs.length === 0) {
        if (deps.canDemandUnloaded()) demandPage("previous", new Set(refs));
        return;
      }
      // A previous wrap targets the tail; with more pages on the server the
      // tail is not loaded, so demand through to it rather than landing on
      // the last loaded row.
      const index = current === null ? -1 : refs.indexOf(current);
      if (index <= 0 && deps.liveRemaining() > 0) {
        demandPage("previous", new Set(refs));
        return;
      }
      intent++;
      const target = adjacentLiveSessionRef(refs, current, "previous");
      if (target !== null) deps.openLiveSession(target);
    },
    dispose() {
      mounted = false;
    },
  };
}
