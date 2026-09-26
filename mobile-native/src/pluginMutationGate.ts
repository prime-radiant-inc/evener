// One plugin or marketplace mutation at a time, across both surfaces that
// start one.
//
// The plugins screen renders the marketplace browser and the installed list as
// a ternary, so switching tabs unmounts the one that was showing. A mutation it
// started keeps running - it is the store's request, not the component's - and
// any "busy" flag that lived inside that component is gone with it, leaving the
// surface that took its place enabled over a write that has not landed. The
// gate outlives both: the screen owns it and hands it to the browser, so a
// second write is refused wherever it is started from - a marketplace add,
// remove or refresh takes it exactly as an install does, rather than a
// view-local flag of its own, for the same reason.
//
// This is the screen's own lock, deliberately, not the store's: the web starts
// plugin mutations from two surfaces with nothing but per-button flags between
// them (marketplacesPlugins/BrowseSection.tsx's installBusy and
// PluginDetailSheet.tsx's four), and the shared core stays the wire truth both
// frontends agree on rather than one host's UI rule.
//
// It is an affordance, not a correctness mechanism: the hub serializes every
// plugin and marketplace mutation itself, under the same exclusive lock held
// for the whole write (internal/plugins/locks.go's lockStore, acquired by
// install.go, gc.go and marketplaces.go alike). A second write that overlaps
// waits there instead of racing. What this gate buys is a user not firing
// that second write and watching the list jump between two intermediate
// answers, or a button that sits looking hung for as long as the hub's own
// lock wait.

import { createFrameworkFreeStore } from "@evener/appwire-client";

/** What a screen shows when the gate refuses: the refusal is not a failure of
 * the write the user asked for, so it must not read like one. */
export const PLUGIN_MUTATION_BUSY = "Another change is still running. Wait for it to finish.";

export interface PluginMutationGate {
  /** True while a mutation is running: what both surfaces disable on. */
  isBusy(): boolean;
  /** Runs `action` unless one is already running, resolving true if it ran
   * and false if it was refused. A failure propagates to the caller, which
   * owns the copy it shows; either way the gate opens again. */
  run(action: () => Promise<void>): Promise<boolean>;
  /** Fires on every transition of isBusy(); returns the unsubscribe. */
  subscribe(listener: () => void): () => void;
}

export function createPluginMutationGate(): PluginMutationGate {
  // The package's own store: nothing here needs a listener set of its own, and
  // its setState notifies every subscriber, which is what a screen binds to.
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

/** What a gated mutation did, in the four shapes a caller's copy needs: it
 * ran, the gate refused it while another write held it, it was blocked by an
 * unavailable connection, or it failed. */
export type GatedMutationOutcome = "ran" | "refused" | "not-ready" | "failed";

/** Runs `action` under `gate` and reduces it to the four outcomes a screen
 * tells apart: a refusal stays distinct from a failure, and a failure's error
 * is swallowed because the caller owns the copy it shows. A not-ready request
 * stays distinct from busy so the caller does not claim another change is
 * running when the connection is the reason nothing ran. Every screen that
 * writes through the gate goes through here, so all of them agree on which
 * outcome reads as busy and which as "this one may not have landed" - and,
 * with the live `ready` predicate, on refusing a write AppWire would reject anyway: the predicate is
 * checked before the gate itself, so a request issued while disconnected
 * never reaches `action` and never takes the gate's lock. */
export async function runGatedMutation(
  gate: PluginMutationGate,
  ready: () => boolean,
  action: () => Promise<void>,
): Promise<GatedMutationOutcome> {
  if (!ready()) return "not-ready";
  try {
    return (await gate.run(action)) ? "ran" : "refused";
  } catch {
    return "failed";
  }
}
