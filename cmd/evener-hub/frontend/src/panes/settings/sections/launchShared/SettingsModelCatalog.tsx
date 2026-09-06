import { useCredentialsStore } from "../../../../stores/credentials";
import { ModelCatalog, type ModelCatalogProps } from "../../../../widgets/modelCatalog";
import { fetchModelCatalog } from "../../../../widgets/modelCatalog/catalogClient";

/** Settings catalogs are unscoped and refresh with the provider registry. */
export function SettingsModelCatalog(props: Omit<ModelCatalogProps, "loadCatalog" | "revision">) {
  const instances = useCredentialsStore((state) => state.instances);
  return <ModelCatalog {...props} loadCatalog={fetchModelCatalog} revision={instances} />;
}
