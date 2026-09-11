import { useCallback, useEffect, useRef, useState } from "react";
import { errorText } from "../../protocol/errors";
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type { LaunchConfigLayer, SpawnSlashCatalogResponse } from "../../protocol/types.gen";

export const SPAWN_SLASH_CATALOG_DEBOUNCE_MS = 250;

export type SpawnSlashCatalogLoadState =
  | { status: "loading"; response?: SpawnSlashCatalogResponse }
  | { status: "ready"; response: SpawnSlashCatalogResponse }
  | { status: "error"; message: string; response?: SpawnSlashCatalogResponse };

export interface UseSpawnSlashCatalogArgs {
  client: AppwireClientLike;
  cwd: string;
  harness?: string;
  launchOverrides: LaunchConfigLayer;
  pluginRevision: number;
  enabled?: boolean;
}

export function useSpawnSlashCatalog(args: UseSpawnSlashCatalogArgs): {
  state: SpawnSlashCatalogLoadState;
  retry(): void;
} {
  const { client, cwd, harness = "", launchOverrides, pluginRevision, enabled = true } = args;
  const [retryRevision, setRetryRevision] = useState(0);
  const [result, setResult] = useState<{
    client: AppwireClientLike;
    requestKey: string;
    state: SpawnSlashCatalogLoadState;
  } | null>(null);
  const latestKey = useRef("");
  const lastResponse = useRef<{ cwd: string; logicalKey: string; response: SpawnSlashCatalogResponse } | null>(null);
  const launchOverridesRef = useRef(launchOverrides);
  const harnessRef = useRef(harness);
  // Latest-value sync for the debounced callback below: assigned in an effect,
  // not the render body, so a concurrent render never tears the values. The
  // effect runs before the settle timer it feeds can fire, so the callback
  // always reads the current render's values.
  useEffect(() => {
    launchOverridesRef.current = launchOverrides;
    harnessRef.current = harness;
  }, [launchOverrides, harness]);
  // Client generation: bumped whenever the client identity changes, stamped
  // into the request guard below. A reconnect that swaps the client object
  // while a request is in flight must not let the old client's late response
  // commit under a reused key — the key alone only covers catalog inputs.
  const clientGenerationRef = useRef(0);
  const lastClientRef = useRef(client);
  useEffect(() => {
    if (lastClientRef.current !== client) {
      lastClientRef.current = client;
      clientGenerationRef.current += 1;
    }
  }, [client]);
  const serializedOverrides = JSON.stringify(launchOverrides);
  const logicalKey = `${cwd}\u0000${serializedOverrides}\u0000${harness}\u0000${pluginRevision}`;
  const requestKey = `${logicalKey}\u0000${retryRevision}`;

  const retry = useCallback(() => setRetryRevision((revision) => revision + 1), []);

  useEffect(() => {
    if (!enabled) {
      latestKey.current = "";
      lastResponse.current = null;
      setResult(null);
      return undefined;
    }

    latestKey.current = requestKey;
    const setState = (state: SpawnSlashCatalogLoadState) => setResult({ client, requestKey, state });
    // Keep the previous response mounted while a same-cwd refresh (a selection
    // toggle, a revision bump, a retry) is in flight, so the panel doesn't
    // collapse to an empty loading state and back. A cwd change drops it: the
    // stale list belongs to another directory.
    const cached = lastResponse.current;
    setState(
      cached !== null && cached.cwd === cwd ? { status: "loading", response: cached.response } : { status: "loading" },
    );

    const timer = setTimeout(() => {
      const currentOverrides = launchOverridesRef.current;
      const currentHarness = harnessRef.current;
      const requestGeneration = clientGenerationRef.current;
      const hasOverrides = Object.keys(currentOverrides).length > 0;
      const params = {
        cwd,
        ...(currentHarness ? { harness: currentHarness } : {}),
        ...(hasOverrides ? { launchOverrides: currentOverrides } : {}),
      };
      void client.request("evener/spawn/slashCatalog", params).then(
        (response) => {
          if (latestKey.current === requestKey && clientGenerationRef.current === requestGeneration) {
            lastResponse.current = { cwd, logicalKey, response };
            setState({ status: "ready", response });
          }
        },
        (error) => {
          if (latestKey.current !== requestKey || clientGenerationRef.current !== requestGeneration) return;
          const cachedResponse = lastResponse.current;
          const response = cachedResponse?.logicalKey === logicalKey ? cachedResponse.response : undefined;
          setState(
            response
              ? { status: "error", message: errorText(error), response }
              : { status: "error", message: errorText(error) },
          );
        },
      );
    }, SPAWN_SLASH_CATALOG_DEBOUNCE_MS);

    return () => clearTimeout(timer);
  }, [client, cwd, enabled, logicalKey, requestKey]);

  // Effects reset state after consumers have already rendered. Never expose
  // another request's ready/error state during that first render: SpawnForm
  // stays mounted across draft switches, and the new draft would otherwise
  // complete from the previous directory's catalog for one commit. The
  // same-cwd cached-loading and disabled ready-empty contracts are derived
  // here exactly as the effect resets them.
  const cached = lastResponse.current;
  const state: SpawnSlashCatalogLoadState = !enabled
    ? { status: "ready", response: { commands: [], skills: [] } }
    : result?.client === client && result.requestKey === requestKey
      ? result.state
      : cached !== null && cached.cwd === cwd
        ? { status: "loading", response: cached.response }
        : { status: "loading" };
  return { state, retry };
}
