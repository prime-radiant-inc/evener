// ModelSwitchTrigger: the app's one "the model label IS the button" control.
// A quiet trigger carrying the current model plus a small chevron, opening the
// SAME rich catalog picker every other model surface uses (ModelCatalogPanel:
// search over one always-expanded grouped list, capability/cost/context
// metadata, Recent, provider diagnostics in place) in the SAME shared floating
// Popover (widgets/popover, closeOnScroll={false}) - so opening it never
// shifts the row it sits in and a scroll never dismisses it.
//
// Value-controlled and presentational. It owns only the picker's transient
// state (open/loading/error/catalog) and nothing about WHOSE model it is: the
// caller supplies the label, the current qualified value, a loader, and what a
// pick means. The session's mid-turn switch (ModelSwitch) sends
// thread/model/set; the spawn pane records the choice for its next launch
// (panes/spawn/Spawn.tsx). Both get the identical affordance because it is the
// identical component, which is the whole point of issue #198.
import { useRef, useState } from "react";
import { friendlyLaunchErrorMessage, sessionActionHeadline } from "../../../protocol/errors";
import { Chevron, type ModelCatalog, type ModelCatalogEntry, ModelCatalogPanel, Popover } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./modelswitch.module.css";

export interface ModelSwitchTriggerProps {
  /** The trigger's visible text. Usually the current qualified model id, but a
   * caller with no model chosen yet shows its own empty-value word instead
   * ("(default)", or a required-choice label) - which is why this is separate
   * from `value`. */
  label: string;
  /** The current qualified "provider/model" (or "" for none), passed straight
   * to the panel: it pre-fills the search field, marks the current row, and
   * scrolls it into view. */
  value: string;
  /** Loads the catalog on open. Rejections are framed and shown inside the
   * open panel rather than thrown away or toasted. */
  loadCatalog: () => Promise<ModelCatalog>;
  /** Reports the chosen entry. The picker closes optimistically first, so a
   * caller whose own write fails surfaces that its own way (parity-m5-composer
   * §H: no rollback of an already-closed picker). */
  onPick: (entry: ModelCatalogEntry) => void;
  disabled?: boolean;
  "data-testid"?: string;
  /** Hook for the visible label span, so a test can read the value without the
   * screen-reader suffix riding along in the button's own textContent. */
  valueTestId?: string;
}

const CLASS = {
  trigger: requireClass(styles.trigger, "modelswitch.module.css", "trigger"),
  value: requireClass(styles.value, "modelswitch.module.css", "value"),
  chevron: requireClass(styles.chevron, "modelswitch.module.css", "chevron"),
  srOnly: requireClass(styles.srOnly, "modelswitch.module.css", "srOnly"),
  popoverPanel: requireClass(styles.popoverPanel, "modelswitch.module.css", "popoverPanel"),
};

export function ModelSwitchTrigger({
  label,
  value,
  loadCatalog,
  onPick,
  disabled = false,
  "data-testid": testId,
  valueTestId,
}: ModelSwitchTriggerProps) {
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  async function openPicker(): Promise<void> {
    if (disabled) return;
    setOpen(true);
    setError(null);
    setLoading(true);
    try {
      setCatalog(await loadCatalog());
    } catch (err) {
      // Not sessionActionError: that composes its detail from errorText, which
      // is the RAW rejection text - fine for a WireError (the hub wrote it for
      // a person) but not for AppwireClient's own internal "cannot call ...
      // while state is closed" rejections, which is exactly what a
      // mid-teardown model/list lands here. Same headline rule
      // (sessionActionHeadline), friendlyLaunchErrorMessage detail instead -
      // it also replaces the daemon-missing family's raw launch-check text
      // with actionable copy (T3).
      const headline = sessionActionHeadline("Couldn't load models", err);
      setError(`${headline}: ${friendlyLaunchErrorMessage(err)}`);
    } finally {
      setLoading(false);
    }
  }

  // Popover's FocusScope is opted out of focus management (autoFocus={false})
  // so the panel's input can own focus and its selection - which makes
  // restoring focus to the trigger on close this component's job.
  function closePicker(): void {
    setOpen(false);
    triggerRef.current?.focus();
  }

  function handlePick(entry: ModelCatalogEntry): void {
    // Optimistic close (no rollback on failure) - matches the legacy picker's
    // own convention (parity-m5-composer.md §H): "Selecting a model closes the
    // picker immediately... a rejection surfaces as a toast, with no rollback
    // of the (already-closed) picker."
    setOpen(false);
    onPick(entry);
  }

  return (
    <Popover
      open={open}
      onClose={closePicker}
      // The picker's own list scrolls, and whatever sits behind it scrolls
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
          data-testid={testId}
          title={label}
          onClick={() => (open ? closePicker() : void openPicker())}
          disabled={disabled}
        >
          {/* Plain text, not a Chip: the trigger already draws the control's
              own hover box, and a bordered chip inside it reads as a double
              border - the same rule widgets/pathfield's and
              widgets/modelCatalog's triggers follow. */}
          <span className={CLASS.value} data-testid={valueTestId}>
            {label}
          </span>
          <span className={CLASS.chevron} aria-hidden="true">
            <Chevron direction="down" />
          </span>{" "}
          {/* That separating space is load-bearing: the accessible name is this
              button's children concatenated, and each child's own text is
              trimmed first, so a space INSIDE either span would be dropped and
              the name would run together as "…sonnet-4-5— change model". A
              whitespace-only text node between them survives that trim and
              renders nothing of its own (an all-whitespace anonymous flex item
              is not laid out). */}
          <span className={CLASS.srOnly}>— change model</span>
        </button>
      }
    >
      <div className={CLASS.popoverPanel}>
        <ModelCatalogPanel loading={loading} error={error} catalog={catalog} value={value} onPick={handlePick} />
      </div>
    </Popover>
  );
}
