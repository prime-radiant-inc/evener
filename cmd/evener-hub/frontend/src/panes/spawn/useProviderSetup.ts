import { useEffect, useState } from "react";
import { useConnectionStore } from "../../stores/connection";
import { useCredentialsStore } from "../../stores/credentials";

/** Credential configuration is read from the hub, never a browser first-run flag.
 * A failed lookup is unknown; it must not send a configured user through setup. */
export function useProviderSetup() {
  const { client, state: connection } = useConnectionStore();
  const { instances, loading, error, writesRefused, fetch } = useCredentialsStore();
  const [checkedClient, setCheckedClient] = useState<typeof client>(null);

  useEffect(() => {
    let cancelled = false;
    setCheckedClient(null);
    if (client && connection === "ready") {
      void fetch()
        .finally(() => {
          if (!cancelled) setCheckedClient(client);
        })
        .catch(() => {});
    }
    return () => {
      cancelled = true;
    };
  }, [client, connection, fetch]);

  const configured = instances.some(
    (instance) =>
      !instance.hidden &&
      (!instance.credentialRequired || (instance.activeSource !== "none" && instance.activeSource !== "")),
  );
  const status =
    !client || connection !== "ready" || checkedClient !== client || loading
      ? "loading"
      : error || writesRefused
        ? "error"
        : configured
          ? "ready"
          : "missing";
  return { status, instances, retry: fetch };
}
