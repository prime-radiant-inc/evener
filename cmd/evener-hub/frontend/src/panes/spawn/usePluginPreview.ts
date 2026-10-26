import type { AppwireClientLike, LaunchConfigLayer, PluginPreviewResponse } from "@evener/appwire-client";
import { hostRequest } from "../../stores/hostRouting";
import { type ScopedReadState, useScopedHostRead } from "./useScopedHostRead";

export type PluginPreviewLoadState = ScopedReadState<PluginPreviewResponse>;

export interface UsePluginPreviewArgs {
  client: AppwireClientLike;
  cwd: string;
  // The selected host (component 07b): the preview is resolved by the host's
  // own hub through evener/host/request; "local" keeps the plain call.
  host?: string;
  launchOverrides: LaunchConfigLayer;
  pluginRevision: number;
  enabled?: boolean;
}

export function usePluginPreview(args: UsePluginPreviewArgs): {
  state: PluginPreviewLoadState;
  retry(): void;
} {
  const { client, cwd, host = "local", launchOverrides, pluginRevision, enabled = true } = args;
  return useScopedHostRead({
    client,
    host,
    cwd,
    inputKey: `${JSON.stringify(launchOverrides)}\u0000${pluginRevision}`,
    enabled,
    read: () =>
      hostRequest(
        client,
        host,
        "evener/plugin/preview",
        Object.keys(launchOverrides).length > 0 ? { cwd, launchOverrides } : { cwd },
      ),
  });
}
