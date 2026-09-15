import { useCallback, useEffect, useState } from "react";
import { useConnectionStore } from "../../stores/connection";
import { useCredentialsStore } from "../../stores/credentials";
import { hostRequest, LOCAL_HOST } from "../../stores/hostRouting";

/** Credential configuration is read from the hub, never a browser first-run flag.
 * A failed lookup is unknown; it must not send a configured user through setup. */
export function useProviderSetup(host: string = LOCAL_HOST) {
  const { client, state: connection } = useConnectionStore();
  const { instances, loading, error, writesRefused, fetchHost } = useCredentialsStore();
  // The instance list is host-dependent (component 07b): fetch it from the
  // selected host. The retry path must carry the same host, not silently fall
  // back to the controller's instances.
  const retry = useCallback(() => fetchHost(host), [fetchHost, host]);
  const [checkedClient, setCheckedClient] = useState<typeof client>(null);
  const [keyless, setKeyless] = useState<{ instances: typeof instances; status: "ready" | "missing" | "error" } | null>(
    null,
  );

  useEffect(() => {
    let cancelled = false;
    setCheckedClient(null);
    if (client && connection === "ready") {
      void fetchHost(host)
        .finally(() => {
          if (!cancelled) setCheckedClient(client);
        })
        .catch(() => {});
    }
    return () => {
      cancelled = true;
    };
  }, [client, connection, fetchHost, host]);

  const configured = instances.some(
    (instance) =>
      !instance.hidden &&
      (instance.credentialRequired
        ? instance.activeSource !== "none" && instance.activeSource !== ""
        : !instance.implicit),
  );
  const implicitKeyless = instances.some(
    (instance) => !instance.hidden && !instance.credentialRequired && instance.implicit,
  );
  // The registry includes an implicit Ollama instance even on a fresh install
  // without a local server. It is available only if it actually offers models.
  // Explicitly configured endpoints retain their configuration on network failure.
  useEffect(() => {
    let cancelled = false;
    setKeyless(null);
    if (
      client &&
      connection === "ready" &&
      checkedClient === client &&
      !loading &&
      !error &&
      !configured &&
      implicitKeyless
    ) {
      void hostRequest(client, host, "model/list", {}).then(
        (response) => {
          if (cancelled) return;
          const available = (response.data ?? []).some((model) =>
            instances.some(
              (instance) => !instance.hidden && !instance.credentialRequired && instance.name === model.provider,
            ),
          );
          setKeyless({ instances, status: available ? "ready" : "missing" });
        },
        () => {
          if (!cancelled) setKeyless({ instances, status: "error" });
        },
      );
    }
    return () => {
      cancelled = true;
    };
  }, [client, connection, host, checkedClient, loading, error, configured, implicitKeyless, instances]);
  const status =
    !client || connection !== "ready" || checkedClient !== client || loading
      ? "loading"
      : error || writesRefused
        ? "error"
        : configured
          ? "ready"
          : implicitKeyless
            ? keyless?.instances === instances
              ? keyless.status
              : "loading"
            : "missing";
  return { status, instances, retry };
}
