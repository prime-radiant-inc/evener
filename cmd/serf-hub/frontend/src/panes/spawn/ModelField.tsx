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
}

export function ModelField({ value, onChange, loadModels }: ModelFieldProps) {
  const loadCatalog = useCallback(async (): Promise<ModelCatalog> => {
    const scoped = await loadModels();
    let enrichment: ModelCatalog | null = null;
    try {
      enrichment = await fetchModelCatalog();
    } catch {
      enrichment = null; // /api/models unavailable: show the scoped set, label-only.
    }
    return mergeScopedCatalog(scoped, enrichment);
  }, [loadModels]);

  return <ModelCatalog value={value} onChange={onChange} loadCatalog={loadCatalog} />;
}
