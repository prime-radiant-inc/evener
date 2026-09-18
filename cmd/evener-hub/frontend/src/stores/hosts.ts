import type { HostRow } from "@evener/appwire-client";
import { create, useStore } from "zustand";
import { connectionStore } from "./connection";

// hosts.ts is the Hosts settings section's store (component 08 slice 1):
// the evener/host/list cache plus the add/Connect/remove calls behind the
// section's rows, add dialog, and confirm. It follows the settingsOverview
// pattern: no persistent subscription, the client resolves fresh from
// connectionStore at request time, so reconnects need no rewiring.

export type HostsLoadState =
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "ready"; hosts: HostRow[] };

interface HostsStoreState {
  load: HostsLoadState;
  fetch: () => Promise<void>;
  /**
   * Quiet re-read for the section's background poll. Unlike fetch it never
   * flips the phase to "loading" (the rows stay rendered) and a failure keeps
   * the last snapshot instead of blanking the section, because the next tick
   * retries. The mid-attach poll calls it while any row reports a server-side
   * attach in flight, so a host that settles outside this tab's actions (the
   * SSH supervisor's background reconnect, another client's Connect) still
   * converges instead of sitting on "connecting" forever.
   */
  refresh: () => Promise<void>;
  add: (params: { name: string; address: string; keyPath?: string }) => Promise<HostRow>;
  connect: (name: string) => Promise<void>;
  remove: (name: string) => Promise<void>;
  resetForTests: () => void;
}

function requireClient() {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("hosts store: no client connected");
  }
  return client;
}

function errorText(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}

// At most one background refresh runs at a time; concurrent callers join the
// same promise (mirrors stores/daemonResidents.ts).
let refreshInflight: Promise<void> | null = null;

export const hostsStore = create<HostsStoreState>((set, get) => ({
  load: { phase: "loading" },

  fetch: async () => {
    set({ load: { phase: "loading" } });
    try {
      const res = await requireClient().request("evener/host/list", {});
      if (res.hosts === undefined) throw new Error("evener/host/list returned no hosts");
      set({ load: { phase: "ready", hosts: res.hosts } });
    } catch (err) {
      set({ load: { phase: "error", message: errorText(err) } });
    }
  },

  refresh: async () => {
    if (refreshInflight) return refreshInflight;
    refreshInflight = (async () => {
      try {
        const res = await requireClient().request("evener/host/list", {});
        if (res.hosts === undefined) throw new Error("evener/host/list returned no hosts");
        set({ load: { phase: "ready", hosts: res.hosts } });
      } catch {
        // A failed background poll keeps the last rows: blanking the section
        // on a transient failure would flash the empty state every tick, and
        // the next tick retries anyway.
      } finally {
        refreshInflight = null;
      }
    })();
    return refreshInflight;
  },

  add: async (params) => {
    const row = await requireClient().request("evener/host/add", {
      name: params.name,
      address: params.address,
      ...(params.keyPath ? { keyPath: params.keyPath } : {}),
    });
    // Re-read rather than appending: the server owns ordering and the row's
    // attached state, and the list read is cheap and never dials.
    await get().fetch();
    return row;
  },

  connect: async (name) => {
    // The same fire-and-wait the spawn picker's Connect trigger issues
    // (Spawn.tsx's connectHost): evener/host/attach, then re-read the row.
    // A failure throws to the caller — the row keeps its retry affordance.
    await requireClient().request("evener/host/attach", { host: name }, { timeoutMs: 35 * 60_000 });
    await get().fetch();
  },

  remove: async (name) => {
    await requireClient().request("evener/host/remove", { name });
    await get().fetch();
  },

  resetForTests: () => {
    refreshInflight = null;
    set({ load: { phase: "loading" } });
  },
}));

export function useHostsStore<T>(selector: (state: HostsStoreState) => T): T {
  return useStore(hostsStore, selector);
}
