// The spawn model picker. Wave 8 restores the rich searchable catalog (display
// names, capability badges, cost, Recent) that W6 deferred to a plain list: it
// adapts the harness/cwd-scoped model/list result the spawn form injects
// (loadModels - the authoritative launchable SET) into the ModelCatalog widget,
// enriching it best-effort with the /api/models metadata that model/list
// doesn't carry (mergeScopedCatalog). The scoped list keeps the SET correct for
// every harness; the enrichment only adds badges/cost/Recent, so an /api/models
// failure degrades to the plain scoped list rather than an empty picker.
// value/onChange are unchanged, so the spawn form's call site stays untouched.
import { useCallback } from "react";
import type { ModelDescriptor } from "../../protocol/types.gen";
import { ModelCatalog } from "../../widgets";
import { fetchModelCatalog } from "../../widgets/modelCatalog/catalogClient";
import { mergeScopedCatalog } from "../../widgets/modelCatalog/scopedCatalog";

export interface ModelFieldProps {
  value: string; // qualified "provider/model", or "" for the harness default
  onChange: (qualified: string) => void;
  loadModels: () => Promise<ModelDescriptor[]>;
  // The spawn harness + working dir, scoping the /api/models enrichment to the
  // SAME set model/list is scoped to (so a non-default harness enriches its own
  // models, not the default serf catalog). Optional: absent = an unscoped
  // enrichment, exactly what the settings swap site wants.
  harness?: string;
  cwd?: string;
}

export function ModelField({ value, onChange, loadModels, harness, cwd }: ModelFieldProps) {
  const loadCatalog = useCallback(async (): Promise<ModelCatalog> => {
    // The scoped model/list is the authoritative launchable SET; the /api/models
    // catalog only enriches it (badges/cost/Recent), scoped to the SAME
    // harness+cwd so a non-default harness enriches its own models rather than
    // the default serf catalog. The two loads are independent, so run them
    // together; a failed enrichment degrades to the plain scoped list
    // (mergeScopedCatalog tolerates null), while a failed model/list still
    // rejects loadCatalog so the picker surfaces the real error.
    const [scoped, enrichment] = await Promise.all([
      loadModels(),
      fetchModelCatalog({ harness, cwd }).catch(() => null),
    ]);
    return mergeScopedCatalog(scoped, enrichment);
  }, [loadModels, harness, cwd]);

  return <ModelCatalog value={value} onChange={onChange} loadCatalog={loadCatalog} />;
}
