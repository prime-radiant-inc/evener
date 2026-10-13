import type { ChangeEvent } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./select.module.css";

export interface SelectOption {
  value: string;
  label: string;
  /** Listed but not pickable - a choice that exists but is unavailable right
   * now (an offline host). Say why in the label, since a native option has no
   * room for anything else. */
  disabled?: boolean;
}

export interface SelectProps {
  value: string;
  onChange: (event: ChangeEvent<HTMLSelectElement>) => void;
  options: SelectOption[];
  disabled?: boolean;
  id?: string;
  name?: string;
}

const BASE_CLASS = {
  select: requireClass(styles.select, "select.module.css", "select"),
};

/** A native <select>, restyled to match the widget set - no custom
 * listbox/positioning JS - the pickers that need richer rows (ModelCatalog,
 * PathField) each own theirs. Controlled only,
 * mirroring Input. */
export function Select({ value, onChange, options, disabled = false, id, name }: SelectProps) {
  return (
    <select id={id} name={name} className={BASE_CLASS.select} value={value} onChange={onChange} disabled={disabled}>
      {options.map((option) => (
        <option key={option.value} value={option.value} disabled={option.disabled}>
          {option.label}
        </option>
      ))}
    </select>
  );
}
