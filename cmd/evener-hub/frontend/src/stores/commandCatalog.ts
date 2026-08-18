import { create } from "zustand";
import type { CommandDescriptor, CommandListResponse } from "../protocol/types.gen";
import { connectionStore } from "./connection";

interface CommandCatalogState {
  commands: CommandDescriptor[];
  loaded: boolean;
  refresh: () => Promise<void>;
}

// Plugin and user-global slash commands, loaded lazily by the command palette.
// A failed catalog request leaves built-ins and slash fallthrough available.
export const useCommandCatalog = create<CommandCatalogState>((set) => ({
  commands: [],
  loaded: false,
  refresh: async () => {
    const client = connectionStore.getState().client;
    if (!client) return;
    try {
      const response = (await client.request("serf/command/list", {})) as CommandListResponse;
      set({ commands: response.commands ?? [], loaded: true });
    } catch {
      set({ commands: [], loaded: true });
    }
  },
}));
