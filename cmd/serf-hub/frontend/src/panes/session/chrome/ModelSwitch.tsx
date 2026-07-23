// ModelSwitch: the mid-session model-switch trigger. The current model chip
// IS the trigger (quiet hover affordance + a small chevron) - clicking it
// opens the SAME rich catalog picker the spawn flow uses (ModelCatalogPanel:
// search, capability badges, cost, context window, Recent, provider
// diagnostics), as a floating popover so opening it never shifts the status
// row's layout.
//
// The launchable SET still comes from threadsStore.listModels() (session-
// lifetime cached in the store, da1b43f85's own doc comment - a repeat call
// after the first is effectively free) since that's what's actually valid to
// switch THIS session to; mergeScopedCatalog enriches those bare provider/
// model pairs with the unscoped /api/models catalog's metadata, exactly the
// way ModelField.tsx's settings (unscoped) call site already does - so this
// reuses both the rendering AND the enrichment plumbing rather than
// duplicating either.
//
// reasoningEffortLevels/supportsReasoning need no special handling here:
// StatusRow already re-renders from the live ThreadModel on every store
// change, so once thread/model/changed lands (protocol/reducer.ts's own
// case, which updates the reasoning ladder alongside modelProvider/model),
// the existing ReasoningEffortControl picks up the new model's profile
// for free.
import { useEffect, useRef, useState } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { Chip, type ModelCatalog, type ModelCatalogEntry, ModelCatalogPanel, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { fetchModelCatalog } from "../../../widgets/modelCatalog/catalogClient";
import { mergeScopedCatalog } from "../../../widgets/modelCatalog/scopedCatalog";
import { isTurnActive } from "../composer/submitRouting";
import styles from "./modelswitch.module.css";
import { modelLabel } from "./statusFormat";

export interface ModelSwitchProps {
  sessionRef: string;
  model: ThreadModel;
}

const CLASS = {
  anchor: requireClass(styles.anchor, "modelswitch.module.css", "anchor"),
  trigger: requireClass(styles.trigger, "modelswitch.module.css", "trigger"),
  chevron: requireClass(styles.chevron, "modelswitch.module.css", "chevron"),
  srOnly: requireClass(styles.srOnly, "modelswitch.module.css", "srOnly"),
  popover: requireClass(styles.popover, "modelswitch.module.css", "popover"),
};

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function ModelSwitch({ sessionRef, model }: ModelSwitchProps) {
  const toasts = useToasts();
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null);
  const pickerRef = useRef<HTMLDivElement>(null);

  // A model switch mid-turn is refused by the daemon, so the trigger follows
  // the LIVE turn state, not only the static changeModel capability - the same
  // isTurnActive predicate Composer's own Stop/Steer gate uses.
  const busy = isTurnActive(model.status.type, model.activeTurnId);
  const disabled = busy || !model.capabilities.changeModel;

  async function openPicker() {
    if (disabled) return;
    setOpen(true);
    setError(null);
    setLoading(true);
    try {
      const [scoped, enrichment] = await Promise.all([
        threadsStore.getState().listModels(),
        fetchModelCatalog().catch(() => null),
      ]);
      setCatalog(mergeScopedCatalog(scoped.data, enrichment));
    } catch (err) {
      setError(`Couldn't load models: ${errorMessage(err)}`);
    } finally {
      setLoading(false);
    }
  }

  function closePicker() {
    setOpen(false);
  }

  // Escape and an outside click dismiss the open popover - same containment
  // idiom widgets/menu uses for its own outside-click.
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

  async function handlePick(entry: ModelCatalogEntry) {
    // Optimistic close (no rollback on failure) - matches the legacy
    // picker's own convention (parity-m5-composer.md §H): "Selecting a
    // model closes the picker immediately... a rejection surfaces as a
    // toast, with no rollback of the (already-closed) picker."
    setOpen(false);
    try {
      await threadsStore.getState().setModel(sessionRef, entry.provider, entry.model);
    } catch (err) {
      toasts.push("error", `Couldn't change model: ${errorMessage(err)}`);
    }
  }

  return (
    <div className={CLASS.anchor} ref={pickerRef}>
      <button
        type="button"
        className={CLASS.trigger}
        onClick={() => (open ? closePicker() : void openPicker())}
        disabled={disabled}
      >
        <Chip>{modelLabel(model.modelProvider, model.model)}</Chip>
        <span className={CLASS.chevron} aria-hidden="true">
          ▾
        </span>
        <span className={CLASS.srOnly}>— change model</span>
      </button>
      {open && (
        <div className={CLASS.popover}>
          <ModelCatalogPanel
            loading={loading}
            error={error}
            catalog={catalog}
            onPick={(entry) => void handlePick(entry)}
            onCancel={closePicker}
          />
        </div>
      )}
    </div>
  );
}
