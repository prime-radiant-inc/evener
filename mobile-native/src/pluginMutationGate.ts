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
// It is an affordance, not a correctness mechanism, and it does not need to be
// one: the hub serializes every plugin AND marketplace mutation itself, under
// the same exclusive flock held for the whole write (internal/plugins/locks.go's
// lockStore - install.go's Install :119, Upgrade :195, Remove :318,
// SetEnabled/SetAutoUpgrade via mutateEntry :287 and gc.go:47, and
// marketplaces.go's AddMarketplace :164, RemoveMarketplace :247 and
// RefreshMarketplace :881, all acquire it). Two writes that do overlap are
// ordered there, and the second waits up to 30s for the first. What this gate
// buys is a user not firing a second write while the first is still in
// flight, watching the list jump between two intermediate answers, or - for
// a write mid-lock-wait - a button that sits looking hung for as long as the
// hub's own lock timeout, which the client's own request timeout roughly
// matches. So a mutation that outlives the component it was started from is
// a UX gap, not a data race.

import { createFrameworkFreeStore } from "@evener/appwire-client";

/** What a screen shows when the gate refuses: the refusal is not a failure of
 * the write the user asked for, so it must not read like one. */
export const PLUGIN_MUTATION_BUSY = "Another plugin change is still running. Wait for it to finish.";

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
