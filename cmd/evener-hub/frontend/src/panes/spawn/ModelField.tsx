// The spawn model picker receives one harness/cwd-scoped, already-enriched
// model/list catalog from Spawn. Keeping the loader at the pane level lets the
// top-level field, advanced options, and the effort preview share one request.
// value/onChange retain the field contract while the parent supplies the
// harness/cwd-scoped loader.
import { ModelCatalog } from "../../widgets";
import type { ModelCatalogEntry } from "../../widgets/modelCatalog";

export interface ModelFieldProps {
  value: string; // qualified "provider/model", or "" for the harness default
  onChange: (qualified: string) => void;
  loadCatalog: () => Promise<ModelCatalog>;
  /** Overrides the closed trigger's empty-value text ("(default)" unless a
   * caller overrides it) - the spawn form passes a real-required label when
   * the daemon has no default model to fall back to (kata xgk8). */
  emptyLabel?: string;
  /** Reports the full picked entry (with reasoningEffortLevels /
   * supportsReasoning) so the spawn form's Effort ladder is correct
   * immediately, without waiting for the pane-level catalog to catch up. */
  onPickEntry?: (entry: ModelCatalogEntry) => void;
}

export function ModelField({ value, onChange, loadCatalog, emptyLabel, onPickEntry }: ModelFieldProps) {
  return (
    <ModelCatalog
      value={value}
      onChange={onChange}
      loadCatalog={loadCatalog}
      onPickEntry={onPickEntry}
      emptyLabel={emptyLabel}
    />
  );
}
