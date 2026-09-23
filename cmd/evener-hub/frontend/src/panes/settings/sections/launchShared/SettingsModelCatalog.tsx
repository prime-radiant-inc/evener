import { useCallback, useEffect } from "react";
import { useConnectionStore } from "../../../../stores/connection";
import { fetchHost, useCredentialsStore, useHostInstances } from "../../../../stores/credentials";
import { isLocalHost, LOCAL_HOST } from "../../../../stores/hostRouting";
import { ModelCatalog, type ModelCatalogProps } from "../../../../widgets/modelCatalog";
import { fetchModelCatalog, fetchModelCatalogForHost } from "../../../../widgets/modelCatalog/catalogClient";

export interface SettingsModelCatalogProps extends Omit<ModelCatalogProps, "loadCatalog" | "revision"> {
  /** The host whose layer is being edited (component 07b). Defaults to the
   * local hub, so a direct render is today's local picker. */
  host?: string;
}

/**
 * The launch/project model picker, scoped to `host` (component 07b). It renders
 * inside the host-scoped launch-evener and project panes, so it must offer the
 * SELECTED host's models, not the controller's: for the local hub it keeps the
 * controller's own plain model/list and provider instances byte-for-byte; for a
 * remote host it reads THAT host's model/list through evener/host/request, and
 * takes its revision from that host's own instance listing (hostInstancesStore)
 * so a credential change on the host refreshes the picker without ever moving
 * the controller's rows.
 */
export function SettingsModelCatalog({ host = LOCAL_HOST, ...props }: SettingsModelCatalogProps) {
  const client = useConnectionStore((state) => state.client);
  const connectionState = useConnectionStore((state) => state.state);
  const fetch = useCredentialsStore((state) => state.fetch);
  const controllerInstances = useCredentialsStore((state) => state.instances);
  const local = isLocalHost(host);
  // The controller's rows ARE the package store's; a remote host's are its own
  // partition (stores/credentials.ts's useHostInstances). Only the selected
  // host's own listing is read - never the controller's for a remote selection.
  const hostInstances = useHostInstances(host);
  useEffect(() => {
    if (!client || connectionState !== "ready") return;
    if (local) void fetch();
    else void fetchHost(host);
  }, [client, connectionState, fetch, local, host]);
  const instances = local ? controllerInstances : hostInstances.instances;
  // The local hub calls the shared model/list loader unchanged (so the
  // controller's own call, and the tests that spy it, are unaffected); a remote
  // host's loader wraps the same method in evener/host/request.
  const loadCatalog = useCallback(() => (local ? fetchModelCatalog() : fetchModelCatalogForHost(host)), [local, host]);
  return <ModelCatalog {...props} loadCatalog={loadCatalog} revision={instances} />;
}
