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
import { Popover } from "../popover";
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
  trigger: requireClass(styles.trigger, "modelCatalog.module.css", "trigger"),
  chevron: requireClass(styles.chevron, "modelCatalog.module.css", "chevron"),
  srOnly: requireClass(styles.srOnly, "modelCatalog.module.css", "srOnly"),
  popoverPanel: requireClass(styles.popoverPanel, "modelCatalog.module.css", "popoverPanel"),
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

export interface ModelCatalogPanelProps {
  loading: boolean;
  error: string | null;
  catalog: ModelCatalog | null;
  onPick: (entry: ModelCatalogEntry) => void;
  onCancel: () => void;
}

/**
 * The open picker's content only (Recent quick-picks, searchable list,
 * diagnostics affordance, Cancel) with no trigger/closed-state rendering of
 * its own - extracted so a caller that needs its own trigger affordance
 * (e.g. ModelSwitch's status-row chip, or ModelCatalog's own chip-as-button
 * trigger below, both opening this as a floating popover) can reuse the
 * rich rendering without duplicating it. ModelCatalog itself is this
 * component plus its own trigger and open/loading/catalog state.
 */
export function ModelCatalogPanel({ loading, error, catalog, onPick, onCancel }: ModelCatalogPanelProps): JSX.Element {
  const [query, setQuery] = useState("");
  const [showDiagnostics, setShowDiagnostics] = useState(false);

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
                  onClick={() => onPick(entry)}
                >
                  {entry.displayName || `${entry.provider}/${entry.model}`}
                </Button>
              ))}
            </div>
          )}
          <Combobox<CatalogOption>
            options={options}
            onQuery={setQuery}
            onPick={(option) => onPick(option.entry)}
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
      <Button variant="quiet" size="sm" onClick={onCancel}>
        Cancel
      </Button>
    </div>
  );
}

/**
 * The closed state IS the trigger (4y12): the current-value chip plus a
 * chevron, clicking either opens the rich picker as a floating Popover
 * (portaled, never reflows) rather than swapping in an inline sibling that
 * shifts layout - mirrors ModelSwitch's own trigger shape
 * (panes/session/chrome/ModelSwitch.tsx:118-131) so both consumers
 * (spawn's ModelField, Settings' launchShared modelPicker field) inherit
 * one combobox interaction from this shared widget.
 */
export function ModelCatalog({ value, onChange, loadCatalog }: ModelCatalogProps): JSX.Element {
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null);

  async function openPicker() {
    setOpen(true);
    setError(null);
    setLoading(true);
    try {
      setCatalog(await loadCatalog());
    } catch (err) {
      setError(`Couldn't load models: ${errorMessage(err)}`);
    } finally {
      setLoading(false);
    }
  }

  function closePicker() {
    setOpen(false);
  }

  function pick(entry: ModelCatalogEntry) {
    setOpen(false);
    onChange(`${entry.provider}/${entry.model}`);
  }

  return (
    <Popover
      open={open}
      onClose={closePicker}
      trigger={
        <button type="button" className={CLASS.trigger} onClick={() => (open ? closePicker() : void openPicker())}>
          <Chip>{value === "" ? "(default)" : value}</Chip>
          <span className={CLASS.chevron} aria-hidden="true">
            ▾
          </span>
          <span className={CLASS.srOnly}>— change model</span>
        </button>
      }
    >
      <div className={CLASS.popoverPanel}>
        <ModelCatalogPanel loading={loading} error={error} catalog={catalog} onPick={pick} onCancel={closePicker} />
      </div>
    </Popover>
  );
}
