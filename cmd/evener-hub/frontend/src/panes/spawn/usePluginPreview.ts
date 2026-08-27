import { useCallback, useEffect, useRef, useState } from "react";
import { errorText } from "../../protocol/errors";
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type { LaunchConfigLayer, PluginPreviewResponse } from "../../protocol/types.gen";

export const PLUGIN_PREVIEW_DEBOUNCE_MS = 250;

export type PluginPreviewLoadState =
  | { status: "loading" }
  | { status: "ready"; response: PluginPreviewResponse }
  | { status: "error"; message: string; response?: PluginPreviewResponse };

export interface UsePluginPreviewArgs {
  client: AppwireClientLike;
  cwd: string;
  launchOverrides: LaunchConfigLayer;
  pluginRevision: number;
  enabled?: boolean;
}

export function usePluginPreview(args: UsePluginPreviewArgs): {
  state: PluginPreviewLoadState;
  retry(): void;
} {
  const { client, cwd, launchOverrides, pluginRevision, enabled = true } = args;
  const [retryRevision, setRetryRevision] = useState(0);
  const [state, setState] = useState<PluginPreviewLoadState>({ status: "loading" });
  const latestKey = useRef("");
  const lastResponse = useRef<{ logicalKey: string; response: PluginPreviewResponse } | null>(null);
  const launchOverridesRef = useRef(launchOverrides);
  launchOverridesRef.current = launchOverrides;
  const serializedOverrides = JSON.stringify(launchOverrides);

  const retry = useCallback(() => setRetryRevision((revision) => revision + 1), []);

  useEffect(() => {
    if (!enabled) {
      latestKey.current = "";
      lastResponse.current = null;
      setState({ status: "loading" });
      return undefined;
    }

    const baseKey = `${cwd}\u0000${serializedOverrides}`;
    const logicalKey = `${baseKey}\u0000${pluginRevision}`;
    const requestKey = `${logicalKey}\u0000${retryRevision}`;
    latestKey.current = requestKey;
    setState({ status: "loading" });

    const timer = setTimeout(() => {
      const currentOverrides = launchOverridesRef.current;
      const params = Object.keys(currentOverrides).length > 0 ? { cwd, launchOverrides: currentOverrides } : { cwd };
      void client.request("evener/plugin/preview", params).then(
        (response) => {
          if (latestKey.current === requestKey) {
            lastResponse.current = { logicalKey, response };
            setState({ status: "ready", response });
          }
        },
        (error) => {
          if (latestKey.current !== requestKey) return;
          const cached = lastResponse.current;
          const response = cached?.logicalKey === logicalKey ? cached.response : undefined;
          setState(
            response
              ? { status: "error", message: errorText(error), response }
              : { status: "error", message: errorText(error) },
          );
        },
      );
    }, PLUGIN_PREVIEW_DEBOUNCE_MS);

    return () => clearTimeout(timer);
  }, [client, cwd, enabled, pluginRevision, retryRevision, serializedOverrides]);

  return { state, retry };
}
