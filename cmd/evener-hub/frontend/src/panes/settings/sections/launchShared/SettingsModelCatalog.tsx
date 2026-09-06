import { useEffect } from "react";
import { useConnectionStore } from "../../../../stores/connection";
import { useCredentialsStore } from "../../../../stores/credentials";
import { ModelCatalog, type ModelCatalogProps } from "../../../../widgets/modelCatalog";
import { fetchModelCatalog } from "../../../../widgets/modelCatalog/catalogClient";

/** Settings catalogs are unscoped and refresh with the provider registry. */
export function SettingsModelCatalog(props: Omit<ModelCatalogProps, "loadCatalog" | "revision">) {
  const client = useConnectionStore((state) => state.client);
  const connectionState = useConnectionStore((state) => state.state);
  const fetch = useCredentialsStore((state) => state.fetch);
  useEffect(() => {
    if (client && connectionState === "ready") void fetch();
  }, [client, connectionState, fetch]);
  const instances = useCredentialsStore((state) => state.instances);
  return <ModelCatalog {...props} loadCatalog={fetchModelCatalog} revision={instances} />;
}
