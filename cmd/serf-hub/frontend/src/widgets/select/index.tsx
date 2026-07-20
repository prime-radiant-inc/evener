import type { ChangeEvent } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./select.module.css";

export interface SelectOption {
  value: string;
  label: string;
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
 * listbox/positioning JS (Combobox covers richer cases). Controlled only,
 * mirroring Input. */
export function Select({ value, onChange, options, disabled = false, id, name }: SelectProps) {
  return (
    <select
      id={id}
      name={name}
      className={BASE_CLASS.select}
      value={value}
      onChange={onChange}
      disabled={disabled}
    >
      {options.map((option) => (
        <option key={option.value} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  );
}
