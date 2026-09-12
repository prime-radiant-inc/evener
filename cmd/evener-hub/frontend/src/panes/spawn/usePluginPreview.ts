import { useCallback, useEffect, useRef, useState } from "react";
import { errorText } from "../../protocol/errors";
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type { LaunchConfigLayer, PluginPreviewResponse } from "../../protocol/types.gen";

export const PLUGIN_PREVIEW_DEBOUNCE_MS = 250;

export type PluginPreviewLoadState =
  | { status: "loading"; response?: PluginPreviewResponse }
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
  const [result, setResult] = useState<{
    client: AppwireClientLike;
    requestKey: string;
    state: PluginPreviewLoadState;
  } | null>(null);
  const lastResponse = useRef<{
    client: AppwireClientLike;
    cwd: string;
    logicalKey: string;
    response: PluginPreviewResponse;
  } | null>(null);
  const launchOverridesRef = useRef(launchOverrides);
  launchOverridesRef.current = launchOverrides;
  const serializedOverrides = JSON.stringify(launchOverrides);
  const logicalKey = `${cwd}\u0000${serializedOverrides}\u0000${pluginRevision}`;
  const requestKey = `${logicalKey}\u0000${retryRevision}`;

  const retry = useCallback(() => setRetryRevision((revision) => revision + 1), []);

  useEffect(() => {
    if (!enabled) {
      lastResponse.current = null;
      setResult(null);
      return undefined;
    }

    let active = true;
    const setState = (state: PluginPreviewLoadState) => setResult({ client, requestKey, state });
    // Keep the previous response mounted while a same-cwd refresh (a selection
    // toggle, a revision bump, a retry) is in flight, so the panel doesn't
    // collapse to an empty loading state and back. Another client or cwd has
    // no authority to reuse the stale list.
    const cached = lastResponse.current;
    setState(
      cached?.client === client && cached.cwd === cwd
        ? { status: "loading", response: cached.response }
        : { status: "loading" },
    );

    const timer = setTimeout(() => {
      const currentOverrides = launchOverridesRef.current;
      const params = Object.keys(currentOverrides).length > 0 ? { cwd, launchOverrides: currentOverrides } : { cwd };
      void client.request("evener/plugin/preview", params).then(
        (response) => {
          if (active) {
            lastResponse.current = { client, cwd, logicalKey, response };
            setState({ status: "ready", response });
          }
        },
        (error) => {
          if (!active) return;
          const cached = lastResponse.current;
          const response = cached?.client === client && cached.logicalKey === logicalKey ? cached.response : undefined;
          setState(
            response
              ? { status: "error", message: errorText(error), response }
              : { status: "error", message: errorText(error) },
          );
        },
      );
    }, PLUGIN_PREVIEW_DEBOUNCE_MS);

    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [client, cwd, enabled, logicalKey, requestKey]);

  // Effects reset state after consumers have already rendered. Never expose
  // another request's ready/error state during that first render: Spawn may
  // otherwise reconcile the new draft against the previous project's preview.
  const cached = lastResponse.current;
  const state: PluginPreviewLoadState =
    enabled && result?.client === client && result.requestKey === requestKey
      ? result.state
      : enabled && cached?.client === client && cached.cwd === cwd
        ? { status: "loading", response: cached.response }
        : { status: "loading" };
  return { state, retry };
}
