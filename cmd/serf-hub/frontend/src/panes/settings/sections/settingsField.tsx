import type { ReactNode } from "react";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./settingsField.module.css";

export interface SettingsFieldProps {
  label: string;
  value: ReactNode;
  help?: ReactNode;
}

const CLASS = {
  row: requireClass(styles.row, "settingsField.module.css", "row"),
  label: requireClass(styles.label, "settingsField.module.css", "label"),
  value: requireClass(styles.value, "settingsField.module.css", "value"),
  help: requireClass(styles.help, "settingsField.module.css", "help"),
  dim: requireClass(styles.dim, "settingsField.module.css", "dim"),
  code: requireClass(styles.code, "settingsField.module.css", "code"),
};

/**
 * One read-only label/value row for General/Hub/Storage (the three
 * overview-fed sections - parity-m7-settings.md §§2,16,17) - a `<div
 * class="row">` wrapping `<dt>`/`<dd>`/optional `<p>`, mirroring the legacy
 * templates' own exact per-row shape 1:1 (a `<div>` grouping dt+dd+p is
 * valid inside a `<dl>` per the HTML5 content model, same as every legacy
 * settings/{general,hub,storage}.html row). Purely presentational - no
 * state, no wire access; callers own fetching and pass already-resolved
 * strings (or richer ReactNode composed with FieldDim/Code below) as
 * `value`. Deliberately NOT FormRow: FormRow labels an editable control via
 * `<label htmlFor>`; every field here is inert display text, which is what
 * `<dt>`/`<dd>` are for.
 */
export function SettingsField({ label, value, help }: SettingsFieldProps) {
  return (
    <div className={CLASS.row}>
      <dt className={CLASS.label}>{label}</dt>
      <dd className={CLASS.value}>{value}</dd>
      {help !== undefined && <p className={CLASS.help}>{help}</p>}
    </div>
  );
}

/** A dim trailing note within a SettingsField's value (bearer-token age,
 * past-index size, hub version's commit, ...) - mirrors the legacy
 * templates' own `<span class="val-text"><span class="dim">...</span></span>`
 * wrapping, flattened to one span since nothing here needs the outer one's
 * own styling hook. */
export function FieldDim({ children }: { children: ReactNode }) {
  return <span className={CLASS.dim}>{children}</span>;
}

/** An inline monospace mention (a path, a filename, a hub.toml key) within
 * help text or a value - e.g. General's "Edit `~/.serf/hub.toml`..." copy. */
export function Code({ children }: { children: ReactNode }) {
  return <code className={CLASS.code}>{children}</code>;
}
