// The rich model catalog picker (wave 8 T2 fills; T1 ships this compiling
// interim-Combobox stub so the two swap sites - spawn ModelField and the W7
// settings modelPicker - can adopt it with a one-import change and nothing
// regresses mid-wave). value/onChange MIRROR the interim ModelField contract
// (ModelField.tsx:40-44): value is a qualified "provider/model" or "" for the
// harness default. loadCatalog is injected (harness-scoped) for testability;
// it returns the /api/models envelope's models[] + recent[].
//
// T2 fills the rich UI (options grouped by provider, capability badges, cost, a
// Recent section from recent[]) INSIDE this module, keeping value/onChange
// identical so neither swap site changes again.
import { type JSX, useState } from "react";
// Import siblings directly, never through the widgets barrel: this module is
// itself barrel-exported, so importing the barrel here would be a cycle (the
// same reason collectioneditor/pathpicker import ../button directly).
import { Button } from "../button";
import { Chip } from "../chip";
import { Combobox, type ComboboxOption } from "../combobox";
import { requireClass } from "../internal/requireClass";
import styles from "./modelCatalog.module.css";

const CLASS = {
  row: requireClass(styles.row, "modelCatalog.module.css", "row"),
  error: requireClass(styles.error, "modelCatalog.module.css", "error"),
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

export interface ModelCatalog {
  models: ModelCatalogEntry[];
  recent: ModelCatalogEntry[];
}

export interface ModelCatalogProps {
  value: string;
  onChange: (qualified: string) => void;
  loadCatalog: () => Promise<ModelCatalog>;
}

interface CatalogOption extends ComboboxOption {
  qualified: string;
}

function toOptions(models: ModelCatalogEntry[]): CatalogOption[] {
  return models.map((m) => ({
    id: `${m.provider}/${m.model}`,
    label: m.displayName,
    qualified: `${m.provider}/${m.model}`,
  }));
}

function filterOptions(options: CatalogOption[], query: string): CatalogOption[] {
  const q = query.trim().toLowerCase();
  if (!q) return options;
  return options.filter((opt) => opt.label.toLowerCase().includes(q));
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function ModelCatalog({ value, onChange, loadCatalog }: ModelCatalogProps): JSX.Element {
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [allOptions, setAllOptions] = useState<CatalogOption[]>([]);
  const [query, setQuery] = useState("");

  async function openPicker() {
    setOpen(true);
    setQuery("");
    setError(null);
    setLoading(true);
    try {
      const catalog = await loadCatalog();
      setAllOptions(toOptions(catalog.models));
    } catch (err) {
      setError(`Couldn't load models: ${errorMessage(err)}`);
    } finally {
      setLoading(false);
    }
  }

  function handlePick(option: CatalogOption) {
    setOpen(false);
    onChange(option.qualified);
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

  return (
    <div className={CLASS.row}>
      {loading ? (
        <Chip>Loading models…</Chip>
      ) : error ? (
        <p className={CLASS.error} role="alert">
          {error}
        </p>
      ) : (
        <Combobox
          options={filterOptions(allOptions, query)}
          onQuery={setQuery}
          onPick={(option) => handlePick(option)}
          aria-label="Model"
        />
      )}
      <Button variant="quiet" size="sm" onClick={() => setOpen(false)}>
        Cancel
      </Button>
    </div>
  );
}
