import { useEffect, useState } from "react";
import { useConnectionStore } from "../../stores/connection";
import { useCredentialsStore } from "../../stores/credentials";

/** Credential configuration is read from the hub, never a browser first-run flag.
 * A failed lookup is unknown; it must not send a configured user through setup. */
export function useProviderSetup() {
  const { client, state: connection } = useConnectionStore();
  const { instances, loading, error, writesRefused, fetch } = useCredentialsStore();
  const [checkedClient, setCheckedClient] = useState<typeof client>(null);
  const [keyless, setKeyless] = useState<{ instances: typeof instances; status: "ready" | "missing" | "error" } | null>(
    null,
  );

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
      ((!instance.credentialRequired && !instance.implicit) ||
        (instance.activeSource !== "none" && instance.activeSource !== "")),
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
      void client.request("model/list", {}).then(
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
  }, [client, connection, checkedClient, loading, error, configured, implicitKeyless, instances]);
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
  return { status, instances, retry: fetch };
}
