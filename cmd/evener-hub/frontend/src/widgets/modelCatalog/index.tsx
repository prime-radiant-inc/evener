// The rich model catalog picker. A search input over ONE always-expanded,
// internally-scrolling list: Recent first, then the provider groups, then a
// dim in-place line per provider the hub couldn't reach. value/onChange
// MIRROR the ModelField contract (value is a qualified
// "provider/model" or "" for the harness default); loadCatalog is injected
// (harness-scoped at the spawn site, unscoped at the settings site), so the
// widget itself is wire-free and both swap sites drop it in with a
// one-import change.
//
// Each row's content is this widget's own (provider group heads,
// non-interactive diagnostic lines); the listbox container, option row and
// Arrow/Home/End engine are the shared internal ones every picker uses.

import {
  buildPickerRows,
  friendlyLaunchErrorMessage,
  type ModelCatalogEntry,
  type ModelCatalog as ModelCatalogShape,
  pickableModelRows,
} from "@evener/appwire-client";
import { type JSX, type KeyboardEvent, useEffect, useMemo, useRef, useState } from "react";
import { InlineNotice } from "../inlinenotice";
// Import siblings directly, never through the widgets barrel: this module is
// itself barrel-exported, so importing the barrel here would be a cycle (the
// same reason collectioneditor imports ../button directly).
import { Listbox, ListboxOption } from "../internal/listbox";
import { PickerTrigger } from "../internal/pickerTrigger";
import { requireClass } from "../internal/requireClass";
import { ariaActiveDescendant, optionDomId, useListbox } from "../internal/useListbox";
import { Popover } from "../popover";
import { Skeleton } from "../skeleton";
import styles from "./modelCatalog.module.css";

export type { ModelCatalogEntry } from "@evener/appwire-client";

const CLASS = {
  trigger: requireClass(styles.trigger, "modelCatalog.module.css", "trigger"),
  triggerValue: requireClass(styles.triggerValue, "modelCatalog.module.css", "triggerValue"),
  triggerDefault: requireClass(styles.triggerDefault, "modelCatalog.module.css", "triggerDefault"),
  chevron: requireClass(styles.chevron, "modelCatalog.module.css", "chevron"),
  popoverPanel: requireClass(styles.popoverPanel, "modelCatalog.module.css", "popoverPanel"),
  panel: requireClass(styles.panel, "modelCatalog.module.css", "panel"),
  input: requireClass(styles.input, "modelCatalog.module.css", "input"),
  groupRow: requireClass(styles.groupRow, "modelCatalog.module.css", "groupRow"),
  row: requireClass(styles.row, "modelCatalog.module.css", "row"),
  rowActive: requireClass(styles.rowActive, "modelCatalog.module.css", "rowActive"),
  rowName: requireClass(styles.rowName, "modelCatalog.module.css", "rowName"),
  check: requireClass(styles.check, "modelCatalog.module.css", "check"),
  meta: requireClass(styles.meta, "modelCatalog.module.css", "meta"),
  warning: requireClass(styles.warning, "modelCatalog.module.css", "warning"),
  unavailable: requireClass(styles.unavailable, "modelCatalog.module.css", "unavailable"),
  panelSheet: requireClass(styles.panelSheet, "modelCatalog.module.css", "panelSheet"),
  listSheet: requireClass(styles.listSheet, "modelCatalog.module.css", "listSheet"),
  sheetRowItem: requireClass(styles.sheetRowItem, "modelCatalog.module.css", "sheetRowItem"),
  sheetRow: requireClass(styles.sheetRow, "modelCatalog.module.css", "sheetRow"),
  sheetRowSelected: requireClass(styles.sheetRowSelected, "modelCatalog.module.css", "sheetRowSelected"),
  sheetRowMain: requireClass(styles.sheetRowMain, "modelCatalog.module.css", "sheetRowMain"),
  sheetRowName: requireClass(styles.sheetRowName, "modelCatalog.module.css", "sheetRowName"),
  sheetRowMeta: requireClass(styles.sheetRowMeta, "modelCatalog.module.css", "sheetRowMeta"),
};

const SKELETON_LINES = 4;

// Keep the historical type/value declaration merge: consumers import the
// catalog shape and the picker component from this module under the same name.
export interface ModelCatalog extends ModelCatalogShape {}

// The widget requires a display label, while the generated AppWire descriptor
// makes it optional because daemon/source callers may know only an identity.
// Keeping the generated type as the source of truth prevents this view model
// from drifting when the model/list contract changes.
export interface ModelCatalogProps {
  value: string;
  onChange: (qualified: string) => void;
  loadCatalog: () => Promise<ModelCatalog>;
  /** Changes when the caller knows the available catalog may have changed. */
  revision?: unknown;
  /** Reports the full picked entry (with reasoningEffortLevels /
   * supportsReasoning) the moment a model is selected, so a caller that
   * derives per-model metadata (e.g. the spawn form's Effort ladder) doesn't
   * have to wait for its own independently-loaded catalog to catch up — the
   * picker already loaded the entry and must not discard that metadata. */
  onPickEntry?: (entry: ModelCatalogEntry) => void;
  /** The closed trigger's text when `value` is "" - "(default)" unless a
   * caller overrides it. Every field with a real fallback (Advanced options,
   * Settings) keeps the default; the spawn form's top-level Model field
   * overrides it when the daemon has no default to fall back to (kata
   * xgk8) - "(default)" there would claim an already-answered field that a
   * submit is about to refuse. */
  emptyLabel?: string;
}

export interface ModelCatalogPanelProps {
  loading: boolean;
  error: string | null;
  catalog: ModelCatalog | null;
  /** The current qualified "provider/model" (or "" for the harness default):
   * pre-fills the input, marks the current row, and scrolls it into view. */
  value: string;
  onPick: (entry: ModelCatalogEntry) => void;
  /** "sheet" renders the mobile variant used inside a bottom Sheet: no search
   * input (a phone picker is scrolled, not autocompleted - the design system's
   * mobile-forms rule that a feature reuses its existing catalog panel rather
   * than a second picker implementation), rows sized to the 48px tap floor,
   * and the same grouped list/roles as the default combobox variant. */
  variant?: "default" | "sheet";
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
export function ModelCatalogPanel({
  loading,
  error,
  catalog,
  value,
  onPick,
  variant = "default",
}: ModelCatalogPanelProps): JSX.Element {
  // null means "the user hasn't typed yet": the input SHOWS the current value
  // (selected, so the first keystroke replaces it) while the list stays
  // unfiltered. Once typing starts, the typed text is both the input's value
  // and the query - including when it's cleared back to "".
  const [typed, setTyped] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  // Typing re-filters the rows, and the first match of the NEW list is the
  // answer the user is narrowing toward - so Enter right after typing picks
  // it. The key cannot be chosen in the onChange (the new rows do not exist
  // yet), so the effect below activates the first row once one exists. It
  // re-runs on every edit, including an edit that leaves the rows unchanged
  // (Backspace clearing the prefill to the empty query), and stays pending
  // while the query matches nothing - so an async catalog arrival still
  // activates the row the user is now looking at.
  const firstMatchQueryRef = useRef<string | null>(null);
  const isSheet = variant === "sheet";

  // The sheet variant has no input to own focus or drive a query: the list is
  // the whole unfiltered catalog, and focus stays with the enclosing Sheet's
  // FocusScope (the first tabbable option).
  const query = isSheet ? "" : (typed ?? "");
  const rows = useMemo(() => buildPickerRows(catalog, query), [catalog, query]);
  const picks = useMemo(() => pickableModelRows(rows), [rows]);
  const pickKeys = useMemo(() => picks.map((row) => row.key), [picks]);
  const { listboxId, activeIndex, activeDescendantKey, activeKeyDropped, setActiveKey, handleNavigationKey } =
    useListbox(pickKeys);
  // The current model can appear TWICE (once under Recent, once under its
  // provider group), but a single-select listbox may have exactly one
  // aria-selected option - so the marker goes on the first occurrence only.
  const currentKey = useMemo(() => picks.find((row) => row.option.qualified === value)?.key, [picks, value]);
  const listShown = !loading && error === null;
  const text = typed ?? value;

  // The sheet variant opens already scrolled to the current value, matching
  // what the combobox variant's activedescendant effect does for its list.
  // Mount-only: the panel stays mounted across loading -> loaded, and
  // re-scrolling then would fight a user already scrolling.
  useEffect(() => {
    if (!isSheet || currentKey === undefined) return;
    document.getElementById(optionDomId(listboxId, currentKey))?.scrollIntoView?.({ block: "center" });
  }, [isSheet, currentKey, listboxId]);

  // Focus the input and select all of it, so the first keystroke replaces the
  // pre-filled value wholesale. Mount-only: the panel stays mounted across
  // loading -> loaded, and re-selecting then would fight a user already typing.
  // Sheet variant: no input, so this is a no-op (guarded to avoid a null focus).
  useEffect(() => {
    if (isSheet) return;
    const input = inputRef.current;
    if (!input) return;
    input.focus();
    input.select();
  }, [isSheet]);

  // Until the user types, the highlight follows the CURRENT value (so the
  // list opens showing where you already are, and ArrowDown continues from
  // there). This only re-runs when the rows or the value change - arrow keys
  // move the highlight without fighting it, since they change neither.
  // Sheet variant: the activedescendant model is unused (rows are real focusable
  // buttons, no input owns virtual focus), so this effect would do a redundant
  // state update + re-render whose output the sheet markup never reads.
  useEffect(() => {
    if (isSheet || typed !== null) return;
    setActiveKey(picks.find((row) => row.option.qualified === value)?.key);
  }, [isSheet, picks, value, typed, setActiveKey]);

  useEffect(() => {
    if (firstMatchQueryRef.current !== typed || typed === null) return;
    if (picks.length === 0) return;
    firstMatchQueryRef.current = null;
    setActiveKey(picks[0]?.key);
  }, [typed, picks, setActiveKey]);

  // A background catalog refresh re-keys rows (their keys carry the row's
  // position), so the row the user is on can stop resolving even though the
  // list still shows it. Re-activate the first match rather than leave Enter
  // with no highlight; a key that still resolves is the user's chosen row and
  // is left alone.
  useEffect(() => {
    if (typed === null || !activeKeyDropped || picks.length === 0) return;
    setActiveKey(picks[0]?.key);
  }, [typed, activeKeyDropped, picks, setActiveKey]);

  function pickAt(index: number): boolean {
    const row = picks[index];
    if (!row) return false;
    onPick(row.option.entry);
    return true;
  }

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (handleNavigationKey(event)) return;
    if (event.key !== "Enter") return;
    if (pickAt(activeIndex)) {
      event.preventDefault();
      return;
    }
    // Nothing highlighted (no current value, nothing typed yet): an
    // exactly-typed id or display name is still an unambiguous answer.
    const wanted = text.trim();
    const exact = picks.findIndex((row) => row.option.qualified === wanted || row.option.label === wanted);
    if (exact >= 0) {
      event.preventDefault();
      pickAt(exact);
    }
  }

  return (
    <div className={`${CLASS.panel} ${isSheet ? CLASS.panelSheet : ""}`}>
      {!isSheet && (
        <input
          ref={inputRef}
          role="combobox"
          className={CLASS.input}
          value={text}
          onChange={(event) => {
            setTyped(event.target.value);
            firstMatchQueryRef.current = event.target.value;
          }}
          onKeyDown={handleKeyDown}
          aria-expanded={listShown}
          aria-autocomplete="list"
          aria-controls={listShown ? listboxId : undefined}
          aria-activedescendant={ariaActiveDescendant(listboxId, activeDescendantKey)}
          aria-label="Model"
        />
      )}
      {loading && <Skeleton lines={SKELETON_LINES} />}
      {error !== null && <InlineNotice tone="danger">{error}</InlineNotice>}
      {listShown && (
        <Listbox id={listboxId} label="Model" className={isSheet ? CLASS.listSheet : undefined}>
          {rows.map((row) => {
            if (row.kind === "group") {
              return (
                <li key={row.key} role="presentation" className={CLASS.groupRow}>
                  {row.label}
                </li>
              );
            }
            if (row.kind === "warning") {
              return (
                <li key={row.key} role="presentation" className={CLASS.warning} data-testid="model-warning">
                  <span aria-hidden="true">⚠ </span>
                  {row.text}
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
            if (isSheet) {
              // A real button per option: with no input to own focus, each
              // row must be independently focusable for the Sheet's FocusScope
              // trap and keyboard activation. Same listbox semantics
              // (role=option, aria-selected) as the combobox variant.
              return (
                <li key={row.key} role="presentation" className={CLASS.sheetRowItem}>
                  <button
                    type="button"
                    id={optionDomId(listboxId, row.key)}
                    role="option"
                    aria-selected={current}
                    className={`${CLASS.sheetRow} ${current ? CLASS.sheetRowSelected : ""}`}
                    onClick={() => onPick(row.option.entry)}
                  >
                    <span className={CLASS.sheetRowMain}>
                      <span className={CLASS.sheetRowName}>{row.option.label}</span>
                      {current && (
                        <span className={CLASS.check} aria-hidden="true">
                          ✓
                        </span>
                      )}
                    </span>
                    {row.meta !== "" && <span className={CLASS.sheetRowMeta}>{row.meta}</span>}
                  </button>
                </li>
              );
            }
            return (
              <ListboxOption
                key={row.key}
                id={optionDomId(listboxId, row.key)}
                selected={current}
                active={row.key === activeDescendantKey}
                className={CLASS.row}
                activeClassName={CLASS.rowActive}
                onSelect={() => onPick(row.option.entry)}
              >
                <span className={CLASS.rowName}>{row.option.label}</span>
                {current && (
                  <span className={CLASS.check} aria-hidden="true">
                    ✓
                  </span>
                )}
                {row.meta !== "" && <span className={CLASS.meta}>{row.meta}</span>}
              </ListboxOption>
            );
          })}
        </Listbox>
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
export function ModelCatalog({
  value,
  onChange,
  loadCatalog,
  revision,
  onPickEntry,
  emptyLabel = "(default)",
}: ModelCatalogProps): JSX.Element {
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: revision deliberately invalidates the supplied catalog
  useEffect(() => {
    if (!open) return;
    let canceled = false;
    setError(null);
    setCatalog(null);
    setLoading(true);
    void Promise.resolve()
      .then(loadCatalog)
      .then(
        (result) => {
          if (!canceled) {
            setCatalog(result);
            setLoading(false);
          }
        },
        (err) => {
          if (!canceled) {
            setError(`Couldn't load models: ${friendlyLaunchErrorMessage(err)}`);
            setLoading(false);
          }
        },
      );
    return () => {
      canceled = true;
    };
  }, [open, loadCatalog, revision]);

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
    onPickEntry?.(entry);
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
        <PickerTrigger
          ref={triggerRef}
          open={open}
          onClick={() => (open ? closePicker() : setOpen(true))}
          ariaMode="listbox"
          actionLabel="change model"
          value={value === "" ? emptyLabel : value}
          fill
          valueState={value === "" ? "default" : undefined}
          classes={{
            trigger: CLASS.trigger,
            value: `${CLASS.triggerValue} ${value === "" ? CLASS.triggerDefault : ""}`,
            chevron: CLASS.chevron,
          }}
        />
      }
    >
      <div className={CLASS.popoverPanel}>
        <ModelCatalogPanel loading={loading} error={error} catalog={catalog} value={value} onPick={pick} />
      </div>
    </Popover>
  );
}
