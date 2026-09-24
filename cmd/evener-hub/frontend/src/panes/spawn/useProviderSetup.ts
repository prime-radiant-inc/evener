import { useCallback, useEffect, useState } from "react";
import { useConnectionStore } from "../../stores/connection";
import { credentialsStore, retryHostRead, useCredentialsStore, useHostInstances } from "../../stores/credentials";
import { hostRequest, isLocalHost, LOCAL_HOST } from "../../stores/hostRouting";

/** Credential configuration is read from the hub, never a browser first-run flag.
 * A failed lookup is unknown; it must not send a configured user through setup.
 *
 * The instance list is host-dependent (component 07b): the selected host's own
 * registry answers, read through evener/host/request for a remote host, so the
 * spawn form describes the machine it is about to launch on. The controller's
 * rows stay the package store's (stores/credentials.ts's own store); a remote
 * host's land in its partition, so neither load can move the other. */
export function useProviderSetup(host: string = LOCAL_HOST) {
  const { client, state: connection } = useConnectionStore();
  const localState = useCredentialsStore();
  const hostState = useHostInstances(host);
  const targetIsLocal = isLocalHost(host);
  const { instances, loading, error, writesRefused } = targetIsLocal ? localState : hostState;
  const load = useCallback(() => {
    // The controller's own listing is the package store's read.
    return credentialsStore.getState().fetch();
  }, []);
  // The retry path carries the same host, never a silent fallback to the
  // controller's instances. A remote host's read is owned by useHostInstances,
  // so its retry asks the store for whatever that needs - the registry first
  // when the registry's own read is what failed.
  const retry = useCallback(() => {
    if (targetIsLocal) return credentialsStore.getState().fetch();
    retryHostRead(host);
    return Promise.resolve();
  }, [targetIsLocal, host]);
  const [checkedClient, setCheckedClient] = useState<typeof client>(null);
  const [keyless, setKeyless] = useState<{ instances: typeof instances; status: "ready" | "missing" | "error" } | null>(
    null,
  );

  useEffect(() => {
    let cancelled = false;
    setCheckedClient(null);
    if (client && connection === "ready") {
      // A remote host's listing is read by useHostInstances (the one place that
      // knows the registry revision); the controller's own listing is still read
      // here. `checkedClient` marks the same moment either way - the effect
      // registered by useHostInstances above has already started that read.
      const attempted = targetIsLocal ? load() : Promise.resolve();
      void attempted
        .finally(() => {
          if (!cancelled) setCheckedClient(client);
        })
        .catch(() => {});
    }
    return () => {
      cancelled = true;
    };
  }, [client, connection, load, targetIsLocal]);

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
