// One plugin or marketplace write at a time, across every store that starts
// one.
//
// The hub serializes every plugin and marketplace mutation itself, under one
// exclusive lock held for the whole write (internal/plugins/locks.go's
// lockStore, acquired by install.go, gc.go and marketplaces.go alike). A
// second write that overlaps waits there instead of racing, so a client that
// sends two concurrently sees a 30s lock wait race a 30s request timeout.
// That is a property of the hub's write path, not of one screen: the gate
// therefore lives under the stores both frontends build
// (state/extensions/{plugins,marketplaces}.ts take it as a port) rather than
// on a screen that could forget it, and the screens read isBusy/subscribe off
// the shared one to disable their buttons.
//
// It is an affordance, not a correctness mechanism: a refusal saves the user
// from firing a second write and watching the list jump between two
// intermediate answers, or a button that sits looking hung for as long as the
// hub's own lock wait.

import { createFrameworkFreeStore } from "../../frameworkFreeStore";

/** What a screen shows when the gate refuses: the refusal is not a failure of
 * the write the user asked for, so it must not read like one. */
export const HUB_WRITE_BUSY = "Another change is still running. Wait for it to finish.";

export interface HubWriteGate {
  /** True while a write is running: what every surface disables on. */
  isBusy(): boolean;
  /** Runs `action` unless one is already running, resolving true if it ran
   * and false if it was refused. A failure propagates to the caller, which
   * owns the copy it shows; either way the gate opens again. */
  run(action: () => Promise<void>): Promise<boolean>;
  /** Fires on every transition of isBusy(); returns the unsubscribe. */
  subscribe(listener: () => void): () => void;
}

export function createHubWriteGate(): HubWriteGate {
  // The package's own store: nothing here needs a listener set of its own, and
  // its setState notifies every subscriber, which is what a surface binds to.
  const store = createFrameworkFreeStore<{ busy: boolean }>(() => ({ busy: false }));

  return {
    isBusy: () => store.getState().busy,
    async run(action) {
      if (store.getState().busy) return false;
      store.setState({ busy: true });
      try {
        await action();
        return true;
      } finally {
        store.setState({ busy: false });
      }
    },
    subscribe(listener) {
      return store.subscribe(() => listener());
    },
  };
}

/** How a gated store write tells a refusal apart from a failure: the stores
 * reject with this when the shared gate was already held, and a screen maps it
 * to the busy copy rather than an error. */
export class HubWriteBusyError extends Error {
  constructor() {
    super(HUB_WRITE_BUSY);
    this.name = "HubWriteBusyError";
  }
}

export function isHubWriteBusy(error: unknown): boolean {
  return error instanceof HubWriteBusyError;
}

/** What a gated mutation did, in the four shapes a caller's copy needs: it
 * ran, the gate refused it while another write held it, it was blocked by an
 * unavailable connection, or it failed. */
export type GatedMutationOutcome = "ran" | "refused" | "not-ready" | "failed";

/** Runs `action` (a gated store mutation) and reduces it to the four outcomes
 * a screen tells apart: the store's own HubWriteBusyError becomes a refusal,
 * distinct from a failure, whose error is swallowed because the caller owns
 * the copy it shows. A not-ready request stays distinct from busy so the
 * caller does not claim another change is running when the connection is the
 * reason nothing ran; the predicate is checked before `action`, so a request
 * issued while disconnected never reaches the store and never takes the
 * gate's lock. */
export async function runGatedMutation(
  ready: () => boolean,
  action: () => Promise<void>,
): Promise<GatedMutationOutcome> {
  if (!ready()) return "not-ready";
  try {
    await action();
    return "ran";
  } catch (error) {
    return isHubWriteBusy(error) ? "refused" : "failed";
  }
}
