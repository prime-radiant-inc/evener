import { useCallback, useEffect, useRef, useState } from "react";
import { errorText } from "../../protocol/errors";
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type { LaunchConfigLayer, PluginPreviewResponse } from "../../protocol/types.gen";

export const PLUGIN_PREVIEW_DEBOUNCE_MS = 250;

export type PluginPreviewLoadState =
  | { status: "loading" }
  | { status: "ready"; response: PluginPreviewResponse }
  | { status: "error"; message: string };

export interface UsePluginPreviewArgs {
  client: AppwireClientLike;
  cwd: string;
  launchOverrides: LaunchConfigLayer;
  pluginRevision: number;
}

export function usePluginPreview(args: UsePluginPreviewArgs): {
  state: PluginPreviewLoadState;
  retry(): void;
} {
  const { client, cwd, launchOverrides, pluginRevision } = args;
  const [retryRevision, setRetryRevision] = useState(0);
  const [state, setState] = useState<PluginPreviewLoadState>({ status: "loading" });
  const latestKey = useRef("");
  const launchOverridesRef = useRef(launchOverrides);
  launchOverridesRef.current = launchOverrides;
  const serializedOverrides = JSON.stringify(launchOverrides);

  const retry = useCallback(() => setRetryRevision((revision) => revision + 1), []);

  useEffect(() => {
    const baseKey = `${cwd}\u0000${serializedOverrides}`;
    const requestKey = `${baseKey}\u0000${pluginRevision}\u0000${retryRevision}`;
    latestKey.current = requestKey;
    setState({ status: "loading" });

    const timer = setTimeout(() => {
      const currentOverrides = launchOverridesRef.current;
      const params = Object.keys(currentOverrides).length > 0 ? { cwd, launchOverrides: currentOverrides } : { cwd };
      void client.request("evener/plugin/preview", params).then(
        (response) => {
          if (latestKey.current === requestKey) setState({ status: "ready", response });
        },
        (error) => {
          if (latestKey.current === requestKey) setState({ status: "error", message: errorText(error) });
        },
      );
    }, PLUGIN_PREVIEW_DEBOUNCE_MS);

    return () => clearTimeout(timer);
  }, [client, cwd, pluginRevision, retryRevision, serializedOverrides]);

  return { state, retry };
}
