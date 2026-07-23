// ModelSwitch: the mid-session model-switch Combobox, unblocked by the T1
// addendum (`threadsStore.listModels`, commit da1b43f85 on w5-interaction,
// cherry-picked here). Shows the current model as a passive chip; a
// "Change model" trigger reveals a Combobox loaded from the catalog.
//
// The catalog is re-fetched on every open rather than cached in component
// state: listModels() is ALREADY session-lifetime cached in the store
// (da1b43f85's own doc comment), so a repeat call after the first is
// effectively free - keeping a SECOND, component-local cache on top would
// only add a staleness risk (a remount, e.g. a dockview tab switch, would
// otherwise show a pointless "Loading models…" flash the store already
// has the answer for, or worse, diverge from the store's own cache
// lifetime) for no benefit.
//
// reasoningEffortLevels/supportsReasoning need no special handling here:
// StatusRow already re-renders from the live ThreadModel on every store
// change, so once thread/model/changed lands (protocol/reducer.ts's own
// case, which updates the reasoning ladder alongside modelProvider/model),
// the existing ReasoningEffortControl picks up the new model's profile
// for free.
import { useEffect, useRef, useState } from "react";
import type { ThreadModel } from "../../../protocol/model";
import type { ModelDescriptor } from "../../../protocol/types.gen";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, Combobox, type ComboboxOption, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { isTurnActive } from "../composer/submitRouting";
import styles from "./modelswitch.module.css";
import { modelLabel } from "./statusFormat";

export interface ModelSwitchProps {
  sessionRef: string;
  model: ThreadModel;
}

const CLASS = {
  row: requireClass(styles.row, "modelswitch.module.css", "row"),
  loading: requireClass(styles.loading, "modelswitch.module.css", "loading"),
};

interface ModelOption extends ComboboxOption {
  provider: string;
  model: string;
}

function toOptions(models: ModelDescriptor[]): ModelOption[] {
  return models.map((m) => ({
    id: `${m.provider}/${m.model}`,
    label: modelLabel(m.provider, m.model),
    provider: m.provider,
    model: m.model,
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

export function ModelSwitch({ sessionRef, model }: ModelSwitchProps) {
  const toasts = useToasts();
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [allOptions, setAllOptions] = useState<ModelOption[]>([]);
  const [query, setQuery] = useState("");
  const pickerRef = useRef<HTMLDivElement>(null);

  // A model switch mid-turn is refused by the daemon, so the trigger follows
  // the LIVE turn state, not only the static changeModel capability - the same
  // isTurnActive predicate Composer's own Stop/Steer gate uses.
  const busy = isTurnActive(model.status.type, model.activeTurnId);

  async function openPicker() {
    if (busy || !model.capabilities.changeModel) return;
    setOpen(true);
    setQuery("");
    setLoading(true);
    try {
      const resp = await threadsStore.getState().listModels();
      setAllOptions(toOptions(resp.data));
    } catch (err) {
      toasts.push("error", `Couldn't load models: ${errorMessage(err)}`);
      setOpen(false);
    } finally {
      setLoading(false);
    }
  }

  function closePicker() {
    setOpen(false);
  }

  // Escape and an outside click dismiss the open picker (not only its Cancel
  // button). Both listen on document rather than the picker subtree so they
  // fire wherever focus/the pointer actually is - the same containment idiom
  // widgets/menu uses for its own outside-click. Escape stays two-stage for
  // free: while the Combobox popup is open the Combobox consumes Escape and
  // stops it (index.tsx's own case), so the first Escape only closes the
  // popup and a second closes the picker here.
  useEffect(() => {
    if (!open) return;
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") setOpen(false);
    }
    function onMouseDown(event: MouseEvent) {
      if (pickerRef.current && !pickerRef.current.contains(event.target as Node)) setOpen(false);
    }
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("mousedown", onMouseDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("mousedown", onMouseDown);
    };
  }, [open]);

  async function handlePick(option: ModelOption) {
    // Optimistic close (no rollback on failure) - matches the legacy
    // picker's own convention (parity-m5-composer.md §H): "Selecting a
    // model closes the picker immediately... a rejection surfaces as a
    // toast, with no rollback of the (already-closed) picker."
    setOpen(false);
    try {
      await threadsStore.getState().setModel(sessionRef, option.provider, option.model);
    } catch (err) {
      toasts.push("error", `Couldn't change model: ${errorMessage(err)}`);
    }
  }

  if (!open) {
    return (
      <div className={CLASS.row}>
        <Chip>{modelLabel(model.modelProvider, model.model)}</Chip>
        <Button
          variant="quiet"
          size="sm"
          onClick={() => void openPicker()}
          disabled={busy || !model.capabilities.changeModel}
        >
          Change model
        </Button>
      </div>
    );
  }

  return (
    <div className={CLASS.row} ref={pickerRef}>
      {loading ? (
        <span className={CLASS.loading}>Loading models…</span>
      ) : (
        <Combobox
          options={filterOptions(allOptions, query)}
          onQuery={setQuery}
          onPick={(option) => void handlePick(option)}
          aria-label="Model"
        />
      )}
      <Button variant="quiet" size="sm" onClick={closePicker}>
        Cancel
      </Button>
    </div>
  );
}
