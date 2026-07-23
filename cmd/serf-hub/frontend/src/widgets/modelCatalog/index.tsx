// The rich model catalog picker (wave 8 T2). A searchable popup over the
// /api/models envelope: options grouped by provider, each row carrying its
// display name, capability badges, cost, and context window; a Recent
// quick-pick section; and an on-demand affordance listing providers the hub
// couldn't reach. value/onChange MIRROR the interim ModelField contract
// (value is a qualified "provider/model" or "" for the harness default);
// loadCatalog is injected (harness-scoped at the spawn site, unscoped at the
// settings site), so the widget itself is wire-free and both swap sites drop
// it in with a one-import change.
import { type JSX, useState } from "react";
// Import siblings directly, never through the widgets barrel: this module is
// itself barrel-exported, so importing the barrel here would be a cycle (the
// same reason collectioneditor/pathpicker import ../button directly).
import { Button } from "../button";
import { Chip } from "../chip";
import { Combobox } from "../combobox";
import { requireClass } from "../internal/requireClass";
import {
  type CatalogOption,
  capabilityLabels,
  contextWindowLabel,
  filterCatalog,
  formatCost,
  toCatalogOptions,
  withGroupHeads,
} from "./catalogView";
import styles from "./modelCatalog.module.css";

const CLASS = {
  row: requireClass(styles.row, "modelCatalog.module.css", "row"),
  panel: requireClass(styles.panel, "modelCatalog.module.css", "panel"),
  error: requireClass(styles.error, "modelCatalog.module.css", "error"),
  recent: requireClass(styles.recent, "modelCatalog.module.css", "recent"),
  recentLabel: requireClass(styles.recentLabel, "modelCatalog.module.css", "recentLabel"),
  optionRow: requireClass(styles.optionRow, "modelCatalog.module.css", "optionRow"),
  groupHead: requireClass(styles.groupHead, "modelCatalog.module.css", "groupHead"),
  optionMain: requireClass(styles.optionMain, "modelCatalog.module.css", "optionMain"),
  optionName: requireClass(styles.optionName, "modelCatalog.module.css", "optionName"),
  optionMeta: requireClass(styles.optionMeta, "modelCatalog.module.css", "optionMeta"),
  metaItem: requireClass(styles.metaItem, "modelCatalog.module.css", "metaItem"),
  diagnostics: requireClass(styles.diagnostics, "modelCatalog.module.css", "diagnostics"),
  diagnosticsList: requireClass(styles.diagnosticsList, "modelCatalog.module.css", "diagnosticsList"),
};

// display_name in the /api/models envelope.
export interface ModelCatalogEntry {
  provider: string;
  model: string;
  displayName: string;
  contextWindow?: number;
  supportsTools?: boolean;
  supportsVision?: boolean;
  maxOutputTokens?: number;
  supportsWebSearch?: boolean;
  supportsReasoning?: boolean;
  inputCostPerMillion?: number;
  outputCostPerMillion?: number;
  reasoningEffortLevels?: string[];
}

// One launch-check diagnostic from the /api/models?diagnostics=1 envelope
// (mirrors appwire.ModelListDiagnostic): why a configured provider couldn't
// list its models. Optional on ModelCatalog - the bare-array default response
// carries none, and a loader that skips diagnostics simply omits the field.
export interface ModelCatalogDiagnostic {
  provider?: string;
  source?: string;
  title?: string;
  message: string;
  hint?: string;
}

export interface ModelCatalog {
  models: ModelCatalogEntry[];
  recent: ModelCatalogEntry[];
  diagnostics?: ModelCatalogDiagnostic[];
}

export interface ModelCatalogProps {
  value: string;
  onChange: (qualified: string) => void;
  loadCatalog: () => Promise<ModelCatalog>;
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function CatalogRow({ option }: { option: CatalogOption }): JSX.Element {
  const caps = capabilityLabels(option.entry);
  const cost = formatCost(option.entry);
  const context = contextWindowLabel(option.entry);
  return (
    <div className={CLASS.optionRow}>
      {option.groupHead !== undefined && <span className={CLASS.groupHead}>{option.groupHead}</span>}
      <div className={CLASS.optionMain}>
        <span className={CLASS.optionName}>{option.label}</span>
        {caps.map((cap) => (
          <Chip key={cap}>{cap}</Chip>
        ))}
      </div>
      {(cost !== null || context !== null) && (
        <div className={CLASS.optionMeta}>
          {cost !== null && <span className={CLASS.metaItem}>{cost}</span>}
          {context !== null && <span className={CLASS.metaItem}>{context}</span>}
        </div>
      )}
    </div>
  );
}

export function ModelCatalog({ value, onChange, loadCatalog }: ModelCatalogProps): JSX.Element {
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null);
  const [query, setQuery] = useState("");
  const [showDiagnostics, setShowDiagnostics] = useState(false);

  async function openPicker() {
    setOpen(true);
    setQuery("");
    setError(null);
    setShowDiagnostics(false);
    setLoading(true);
    try {
      setCatalog(await loadCatalog());
    } catch (err) {
      setError(`Couldn't load models: ${errorMessage(err)}`);
    } finally {
      setLoading(false);
    }
  }

  function pick(qualified: string) {
    setOpen(false);
    onChange(qualified);
  }

  if (!open) {
    return (
      <div className={CLASS.row}>
        <Chip>{value === "" ? "(default)" : value}</Chip>
        <Button variant="quiet" size="sm" onClick={() => void openPicker()}>
          Change model
        </Button>
      </div>
    );
  }

  const options = catalog ? withGroupHeads(filterCatalog(toCatalogOptions(catalog.models), query)) : [];
  const recent = catalog?.recent ?? [];
  const diagnostics = catalog?.diagnostics ?? [];

  return (
    <div className={CLASS.panel}>
      {loading ? (
        <Chip>Loading models…</Chip>
      ) : error ? (
        <p className={CLASS.error} role="alert">
          {error}
        </p>
      ) : (
        <>
          {recent.length > 0 && (
            <div className={CLASS.recent}>
              <span className={CLASS.recentLabel}>Recent</span>
              {recent.map((entry) => (
                <Button
                  key={`${entry.provider}/${entry.model}`}
                  variant="quiet"
                  size="sm"
                  onClick={() => pick(`${entry.provider}/${entry.model}`)}
                >
                  {entry.displayName || `${entry.provider}/${entry.model}`}
                </Button>
              ))}
            </div>
          )}
          <Combobox<CatalogOption>
            options={options}
            onQuery={setQuery}
            onPick={(option) => pick(option.qualified)}
            renderOption={(option) => <CatalogRow option={option} />}
            aria-label="Model"
          />
          {diagnostics.length > 0 && (
            <div className={CLASS.diagnostics}>
              <Button variant="quiet" size="sm" onClick={() => setShowDiagnostics((v) => !v)}>
                {diagnostics.length} {diagnostics.length === 1 ? "provider" : "providers"} unavailable
              </Button>
              {showDiagnostics && (
                <ul className={CLASS.diagnosticsList}>
                  {diagnostics.map((diag) => (
                    <li key={`${diag.provider ?? ""}:${diag.message}`}>
                      {diag.provider ? `${diag.provider}: ` : ""}
                      {diag.message}
                      {diag.hint ? ` — ${diag.hint}` : ""}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </>
      )}
      <Button variant="quiet" size="sm" onClick={() => setOpen(false)}>
        Cancel
      </Button>
    </div>
  );
}
