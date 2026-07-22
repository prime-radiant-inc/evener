// The interim model picker (floor §1.4, W6-interim ruling): a model/list
// Combobox in the Wave-5 ModelSwitch shape. The rich REST /api/models catalog
// (display names, capability badges, provider grouping, Recent, pricing) is
// Jesse-decided Wave 8, not W6 - so this shows a plain provider/model list.
// Wire-free: the parent injects loadModels (a harness-scoped model/list call).
import { useState } from "react";
import type { ModelDescriptor } from "../../protocol/types.gen";
import { Button, Chip, Combobox, type ComboboxOption } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { modelLabel } from "./harnessModels";
import styles from "./modelField.module.css";

const CLASS = {
  row: requireClass(styles.row, "modelField.module.css", "row"),
  error: requireClass(styles.error, "modelField.module.css", "error"),
};

interface ModelOption extends ComboboxOption {
  qualified: string;
}

function toOptions(models: ModelDescriptor[]): ModelOption[] {
  return models.map((m) => ({
    id: `${m.provider}/${m.model}`,
    label: modelLabel(m.provider, m.model),
    qualified: `${m.provider}/${m.model}`,
  }));
}

function filterOptions(options: ModelOption[], query: string): ModelOption[] {
  const q = query.trim().toLowerCase();
  if (!q) return options;
  return options.filter((opt) => opt.label.toLowerCase().includes(q));
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export interface ModelFieldProps {
  value: string; // qualified "provider/model", or "" for the harness default
  onChange: (qualified: string) => void;
  loadModels: () => Promise<ModelDescriptor[]>;
}

export function ModelField({ value, onChange, loadModels }: ModelFieldProps) {
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [allOptions, setAllOptions] = useState<ModelOption[]>([]);
  const [query, setQuery] = useState("");

  async function openPicker() {
    setOpen(true);
    setQuery("");
    setError(null);
    setLoading(true);
    try {
      const models = await loadModels();
      setAllOptions(toOptions(models));
    } catch (err) {
      setError(`Couldn't load models: ${errorMessage(err)}`);
    } finally {
      setLoading(false);
    }
  }

  function handlePick(option: ModelOption) {
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
