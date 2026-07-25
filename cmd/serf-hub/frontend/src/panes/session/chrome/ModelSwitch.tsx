// ModelSwitch: the mid-session model-switch trigger. The current model label
// IS the trigger (quiet hover affordance + a small chevron) - clicking it
// opens the SAME rich catalog picker the spawn flow uses (ModelCatalogPanel:
// search over one always-expanded grouped list, capability/cost/context
// metadata, Recent, provider diagnostics in place), in the SAME shared
// floating Popover (widgets/popover, closeOnScroll={false}) - so opening it
// never shifts the status row's layout and a scroll never dismisses it.
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
import { useRef, useState } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { type ModelCatalog, type ModelCatalogEntry, ModelCatalogPanel, Popover, useToasts } from "../../../widgets";
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
  trigger: requireClass(styles.trigger, "modelswitch.module.css", "trigger"),
  value: requireClass(styles.value, "modelswitch.module.css", "value"),
  chevron: requireClass(styles.chevron, "modelswitch.module.css", "chevron"),
  srOnly: requireClass(styles.srOnly, "modelswitch.module.css", "srOnly"),
  popoverPanel: requireClass(styles.popoverPanel, "modelswitch.module.css", "popoverPanel"),
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
  const triggerRef = useRef<HTMLButtonElement>(null);

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

  // Popover's FocusScope is opted out of focus management (autoFocus={false})
  // so the panel's input can own focus and its selection - which makes
  // restoring focus to the trigger on close this component's job.
  function closePicker() {
    setOpen(false);
    triggerRef.current?.focus();
  }

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
    <Popover
      open={open}
      onClose={closePicker}
      // The picker's own list scrolls, and the transcript behind it scrolls
      // too: neither may dismiss a picker mid-interaction.
      closeOnScroll={false}
      // The panel's input owns focus and its own text selection - see
      // closePicker for why FocusScope must not manage focus here.
      autoFocus={false}
      trigger={
        <button
          ref={triggerRef}
          type="button"
          className={CLASS.trigger}
          data-testid="model-switch-trigger"
          onClick={() => (open ? closePicker() : void openPicker())}
          disabled={disabled}
        >
          {/* Plain text, not a Chip: the trigger already draws the control's
              own hover box, and a bordered chip inside it reads as a double
              border - the same rule widgets/pathfield's and
              widgets/modelCatalog's triggers follow. */}
          <span className={CLASS.value} data-testid="model-switch-value">
            {modelLabel(model.modelProvider, model.model)}
          </span>
          <span className={CLASS.chevron} aria-hidden="true">
            ▾
          </span>
          <span className={CLASS.srOnly}>— change model</span>
        </button>
      }
    >
      <div className={CLASS.popoverPanel}>
        <ModelCatalogPanel
          loading={loading}
          error={error}
          catalog={catalog}
          value={modelLabel(model.modelProvider, model.model)}
          onPick={(entry) => void handlePick(entry)}
        />
      </div>
    </Popover>
  );
}
