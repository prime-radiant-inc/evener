import { create } from "zustand";
import { useStore } from "zustand";
import type { HostRow } from "@evener/appwire-client";
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

  resetForTests: () => set({ load: { phase: "loading" } }),
}));

export function useHostsStore<T>(selector: (state: HostsStoreState) => T): T {
  return useStore(hostsStore, selector);
}
