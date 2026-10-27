import type {
  AppwireClientLike,
  LaunchConfigLayer,
  ScopedReadState,
  SpawnSlashCatalogResponse,
} from "@evener/appwire-client";
import { hostRequest } from "../../stores/hostRouting";
import { useScopedHostRead } from "./useScopedHostRead";

export type SpawnSlashCatalogLoadState = ScopedReadState<SpawnSlashCatalogResponse>;

export interface UseSpawnSlashCatalogArgs {
  client: AppwireClientLike;
  cwd: string;
  // The selected host (component 07b): the catalog is resolved by the host's
  // own hub through evener/host/request; "local" keeps the plain call.
  host?: string;
  harness?: string;
  launchOverrides: LaunchConfigLayer;
  pluginRevision: number;
  enabled?: boolean;
}

const DISABLED_STATE: SpawnSlashCatalogLoadState = { status: "ready", response: { commands: [], skills: [] } };

export function useSpawnSlashCatalog(args: UseSpawnSlashCatalogArgs): {
  state: SpawnSlashCatalogLoadState;
  retry(): void;
} {
  const { client, cwd, host = "local", harness = "", launchOverrides, pluginRevision, enabled = true } = args;
  return useScopedHostRead({
    client,
    host,
    cwd,
    inputKey: `${JSON.stringify(launchOverrides)}\u0000${harness}\u0000${pluginRevision}`,
    enabled,
    disabledState: DISABLED_STATE,
    read: () =>
      hostRequest(client, host, "evener/spawn/slashCatalog", {
        cwd,
        ...(harness ? { harness } : {}),
        ...(Object.keys(launchOverrides).length > 0 ? { launchOverrides } : {}),
      }),
  });
}
