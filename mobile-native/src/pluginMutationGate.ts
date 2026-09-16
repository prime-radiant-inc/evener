// One plugin mutation at a time, across both surfaces that start one.
//
// The plugins screen renders the marketplace browser and the installed list as
// a ternary, so switching tabs unmounts the one that was showing. A mutation it
// started keeps running - it is the store's request, not the component's - and
// any "busy" flag that lived inside that component is gone with it, leaving the
// surface that took its place enabled over a plugin write that has not landed.
// The gate outlives both: the screen owns it and hands it to the browser, so a
// second write is refused wherever it is started from.
//
// This is the screen's own lock, deliberately, not the store's: the web starts
// plugin mutations from two surfaces with nothing but per-button flags between
// them (marketplacesPlugins/BrowseSection.tsx's installBusy and
// PluginDetailSheet.tsx's four), and the shared core stays the wire truth both
// frontends agree on rather than one host's UI rule.

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
  let busy = false;
  const listeners = new Set<() => void>();

  function set(next: boolean): void {
    busy = next;
    for (const listener of Array.from(listeners)) listener();
  }

  return {
    isBusy: () => busy,
    async run(action) {
      if (busy) return false;
      set(true);
      try {
        await action();
        return true;
      } finally {
        set(false);
      }
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}
