// The rich model catalog picker. A search input over ONE always-expanded,
// internally-scrolling list: Recent first, then the provider groups, then a
// dim in-place line per provider the hub couldn't reach. value/onChange
// MIRROR the interim ModelField contract (value is a qualified
// "provider/model" or "" for the harness default); loadCatalog is injected
// (harness-scoped at the spawn site, unscoped at the settings site), so the
// widget itself is wire-free and both swap sites drop it in with a
// one-import change.
//
// The list rendering is this widget's own rather than a shared generic
// options-list: it needs provider group heads, non-interactive diagnostic
// lines, and a list expanded the moment it opens.
import { type JSX, type KeyboardEvent, useEffect, useId, useMemo, useRef, useState } from "react";
// Import siblings directly, never through the widgets barrel: this module is
// itself barrel-exported, so importing the barrel here would be a cycle (the
// same reason collectioneditor imports ../button directly).
import { requireClass } from "../internal/requireClass";
import { Popover } from "../popover";
import { Skeleton } from "../skeleton";
import styles from "./modelCatalog.module.css";
import { buildPickerRows, pickableRows } from "./pickerRows";

const CLASS = {
  trigger: requireClass(styles.trigger, "modelCatalog.module.css", "trigger"),
  triggerValue: requireClass(styles.triggerValue, "modelCatalog.module.css", "triggerValue"),
  triggerDefault: requireClass(styles.triggerDefault, "modelCatalog.module.css", "triggerDefault"),
  chevron: requireClass(styles.chevron, "modelCatalog.module.css", "chevron"),
  srOnly: requireClass(styles.srOnly, "modelCatalog.module.css", "srOnly"),
  popoverPanel: requireClass(styles.popoverPanel, "modelCatalog.module.css", "popoverPanel"),
  panel: requireClass(styles.panel, "modelCatalog.module.css", "panel"),
  input: requireClass(styles.input, "modelCatalog.module.css", "input"),
  error: requireClass(styles.error, "modelCatalog.module.css", "error"),
  list: requireClass(styles.list, "modelCatalog.module.css", "list"),
  groupRow: requireClass(styles.groupRow, "modelCatalog.module.css", "groupRow"),
  row: requireClass(styles.row, "modelCatalog.module.css", "row"),
  rowActive: requireClass(styles.rowActive, "modelCatalog.module.css", "rowActive"),
  rowName: requireClass(styles.rowName, "modelCatalog.module.css", "rowName"),
  check: requireClass(styles.check, "modelCatalog.module.css", "check"),
  meta: requireClass(styles.meta, "modelCatalog.module.css", "meta"),
  unavailable: requireClass(styles.unavailable, "modelCatalog.module.css", "unavailable"),
};

const SKELETON_LINES = 4;

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

export interface ModelCatalogPanelProps {
  loading: boolean;
  error: string | null;
  catalog: ModelCatalog | null;
  /** The current qualified "provider/model" (or "" for the harness default):
   * pre-fills the input, marks the current row, and scrolls it into view. */
  value: string;
  onPick: (entry: ModelCatalogEntry) => void;
}

function rowDomId(listboxId: string, key: string): string {
  return `${listboxId}-${key}`;
}

/**
 * The open picker's content only (input + grouped list) with no trigger or
 * closed-state rendering of its own - extracted so a caller that needs its
 * own trigger affordance (ModelSwitch's status-row chip, ModelCatalog's own
 * chip-as-button trigger below, both opening this as a floating popover) can
 * reuse the rich rendering without duplicating it.
 *
 * The ARIA 1.2 combobox-with-listbox pattern: role="combobox" on the input,
 * a role="listbox" sibling, aria-activedescendant tracking the highlighted
 * option - real DOM focus never leaves the input, so typing is never
 * interrupted. The listbox is ALWAYS shown while the panel is open
 * (aria-expanded stays true): the panel itself is the popup, and an empty
 * picker over a hidden list was the defect this replaced.
 *
 * Dismissal is the enclosing Popover's job (Escape bubbles to its panel
 * handler, outside-click is its document listener). There is no Cancel
 * button, and no blur handler: focus staying in the input is the whole point
 * of the activedescendant pattern.
 */
export function ModelCatalogPanel({ loading, error, catalog, value, onPick }: ModelCatalogPanelProps): JSX.Element {
  // null means "the user hasn't typed yet": the input SHOWS the current value
  // (selected, so the first keystroke replaces it) while the list stays
  // unfiltered. Once typing starts, the typed text is both the input's value
  // and the query - including when it's cleared back to "".
  const [typed, setTyped] = useState<string | null>(null);
  const [activeIndex, setActiveIndex] = useState(-1);
  const inputRef = useRef<HTMLInputElement>(null);
  const listboxId = useId();

  const text = typed ?? value;
  const query = typed ?? "";
  const rows = useMemo(() => buildPickerRows(catalog, query), [catalog, query]);
  const picks = useMemo(() => pickableRows(rows), [rows]);
  const activeKey = activeIndex >= 0 && activeIndex < picks.length ? picks[activeIndex]?.key : undefined;
  // The current model can appear TWICE (once under Recent, once under its
  // provider group), but a single-select listbox may have exactly one
  // aria-selected option - so the marker goes on the first occurrence only.
  const currentKey = useMemo(() => picks.find((row) => row.option.qualified === value)?.key, [picks, value]);
  const listShown = !loading && error === null;

  // Focus the input and select all of it, so the first keystroke replaces the
  // pre-filled value wholesale. Mount-only: the panel stays mounted across
  // loading -> loaded, and re-selecting then would fight a user already typing.
  useEffect(() => {
    const input = inputRef.current;
    if (!input) return;
    input.focus();
    input.select();
  }, []);

  // Until the user types, the highlight follows the CURRENT value (so the
  // list opens showing where you already are, and ArrowDown continues from
  // there). This only re-runs when the rows or the value change - arrow keys
  // move the highlight without fighting it, since they change neither.
  useEffect(() => {
    if (typed !== null) return;
    setActiveIndex(picks.findIndex((row) => row.option.qualified === value));
  }, [picks, value, typed]);

  // Keep the highlighted row visible inside the list's own scroll container -
  // both for the current value on open and for keyboard walks past the fold.
  // scrollIntoView is called optionally: jsdom implements none at all.
  useEffect(() => {
    if (activeKey === undefined) return;
    document.getElementById(rowDomId(listboxId, activeKey))?.scrollIntoView?.({ block: "nearest" });
  }, [activeKey, listboxId]);

  function pickAt(index: number): boolean {
    const row = picks[index];
    if (!row) return false;
    onPick(row.option.entry);
    return true;
  }

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        if (picks.length === 0) break;
        setActiveIndex((current) => Math.min(current + 1, picks.length - 1));
        break;
      case "ArrowUp":
        event.preventDefault();
        if (picks.length === 0) break;
        setActiveIndex((current) => Math.max(current - 1, 0));
        break;
      case "Home":
        if (picks.length === 0) break;
        event.preventDefault();
        setActiveIndex(0);
        break;
      case "End":
        if (picks.length === 0) break;
        event.preventDefault();
        setActiveIndex(picks.length - 1);
        break;
      case "Enter": {
        if (pickAt(activeIndex)) {
          event.preventDefault();
          break;
        }
        // Nothing highlighted (no current value, nothing typed yet): an
        // exactly-typed id or display name is still an unambiguous answer.
        const wanted = text.trim();
        const exact = picks.findIndex((row) => row.option.qualified === wanted || row.option.label === wanted);
        if (exact >= 0) {
          event.preventDefault();
          pickAt(exact);
        }
        break;
      }
      default:
        break;
    }
  }

  return (
    <div className={CLASS.panel}>
      <input
        ref={inputRef}
        role="combobox"
        className={CLASS.input}
        value={text}
        onChange={(event) => {
          setTyped(event.target.value);
          // The first match is the answer the user is narrowing toward, so
          // Enter right after typing picks it.
          setActiveIndex(0);
        }}
        onKeyDown={handleKeyDown}
        aria-expanded={listShown}
        aria-autocomplete="list"
        aria-controls={listShown ? listboxId : undefined}
        aria-activedescendant={activeKey !== undefined ? rowDomId(listboxId, activeKey) : undefined}
        aria-label="Model"
      />
      {loading && <Skeleton lines={SKELETON_LINES} />}
      {error !== null && (
        <p className={CLASS.error} role="alert">
          {error}
        </p>
      )}
      {listShown && (
        // <ul role="listbox">/<li role="option"> is the WAI-ARIA APG
        // combobox-with-listbox-popup pattern's own example markup
        // (w3.org/WAI/ARIA/apg/patterns/combobox) - not an interactive role
        // bolted onto an arbitrary static element, Biome's role-vs-element
        // heuristic just doesn't special-case ul/li for it.
        <ul
          // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ul/li is the ARIA APG's own listbox markup, see above
          role="listbox"
          id={listboxId}
          aria-label="Model"
          className={CLASS.list}
        >
          {rows.map((row) => {
            if (row.kind === "group") {
              return (
                <li key={row.key} role="presentation" className={CLASS.groupRow}>
                  {row.label}
                </li>
              );
            }
            if (row.kind === "unavailable") {
              return (
                <li key={row.key} role="presentation" className={CLASS.unavailable}>
                  {row.text}
                </li>
              );
            }
            const current = row.key === currentKey;
            return (
              // Real focus never leaves the input (ARIA 1.2 activedescendant
              // pattern): aria-activedescendant above tracks the "virtual"
              // active option, and handleKeyDown's own Enter case already
              // calls this same pick - so this <li> is deliberately not
              // focusable and needs no onKeyDown of its own.
              // biome-ignore lint/a11y/useFocusableInteractive: activedescendant pattern, real focus stays on the input, see above
              // biome-ignore lint/a11y/useKeyWithClickEvents: activedescendant pattern, Enter on the input already does this, see above
              <li
                key={row.key}
                id={rowDomId(listboxId, row.key)}
                // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ARIA APG listbox markup, see the ul above
                role="option"
                aria-selected={current}
                className={`${CLASS.row} ${row.key === activeKey ? CLASS.rowActive : ""}`}
                // Selecting with the mouse must not blur the input (the ARIA
                // 1.2 pattern keeps real focus there throughout).
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => onPick(row.option.entry)}
              >
                <span className={CLASS.rowName}>{row.option.label}</span>
                {current && (
                  <span className={CLASS.check} aria-hidden="true">
                    ✓
                  </span>
                )}
                {row.meta !== "" && <span className={CLASS.meta}>{row.meta}</span>}
              </li>
            );
          })}
        </ul>
      )}
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
  const triggerRef = useRef<HTMLButtonElement>(null);

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

  // Popover's FocusScope is opted out of focus management entirely
  // (autoFocus={false}) so the panel's own input can hold focus and its
  // selection, which means returning focus to the trigger on close is this
  // component's job - otherwise focus falls to <body>.
  function closePicker() {
    setOpen(false);
    triggerRef.current?.focus();
  }

  // Picking deliberately does NOT refocus the trigger: it's a completed
  // choice, and yanking focus back to the chip would fight a keyboard user
  // tabbing onward through the form.
  function pick(entry: ModelCatalogEntry) {
    setOpen(false);
    onChange(`${entry.provider}/${entry.model}`);
  }

  return (
    <Popover
      open={open}
      onClose={closePicker}
      // The picker's own list scrolls, and a page scroll behind it must not
      // dismiss a picker mid-interaction.
      closeOnScroll={false}
      // The panel's input owns focus and its own text selection - see
      // closePicker for why FocusScope must not manage focus here.
      autoFocus={false}
      // The trigger is a form control: it fills its field slot so it lines up
      // with the Input/Select siblings beside it.
      stretchTrigger
      trigger={
        <button
          ref={triggerRef}
          type="button"
          className={CLASS.trigger}
          onClick={() => (open ? closePicker() : void openPicker())}
        >
          {/* Plain text, not a Chip: the trigger already draws the control's
              own border, and a bordered chip inside it read as a double
              border. */}
          <span className={`${CLASS.triggerValue} ${value === "" ? CLASS.triggerDefault : ""}`}>
            {value === "" ? "(default)" : value}
          </span>
          <span className={CLASS.chevron} aria-hidden="true">
            ▾
          </span>
          <span className={CLASS.srOnly}>— change model</span>
        </button>
      }
    >
      <div className={CLASS.popoverPanel}>
        <ModelCatalogPanel loading={loading} error={error} catalog={catalog} value={value} onPick={pick} />
      </div>
    </Popover>
  );
}
