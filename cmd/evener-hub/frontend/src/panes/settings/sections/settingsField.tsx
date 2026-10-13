import type { ReactNode } from "react";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./settingsField.module.css";

const CLASS = {
  dim: requireClass(styles.dim, "settingsField.module.css", "dim"),
  code: requireClass(styles.code, "settingsField.module.css", "code"),
};

/** A settings PropertyList row's value, tagged with the row's stable id as
 * `data-field` so callers and tests can tell rows apart without reading their
 * labels. PropertyList takes no per-row attributes, so the id rides on the
 * value it wraps. */
export function FieldValue({ field, children }: { field: string; children: ReactNode }) {
  return <span data-field={field}>{children}</span>;
}

/** A dim trailing note within a settings PropertyList value (bearer-token age,
 * past-index size, hub version's commit, ...) - mirrors the legacy
 * templates' own `<span class="val-text"><span class="dim">...</span></span>`
 * wrapping, flattened to one span since nothing here needs the outer one's
 * own styling hook. */
export function FieldDim({ children }: { children: ReactNode }) {
  return <span className={CLASS.dim}>{children}</span>;
}

/** An inline monospace mention (a path, a filename, a hub.toml key) within
 * help text or a value - e.g. General's "Edit `~/.config/evener/hub.toml`..." copy. */
export function Code({ children }: { children: ReactNode }) {
  return <code className={CLASS.code}>{children}</code>;
}
