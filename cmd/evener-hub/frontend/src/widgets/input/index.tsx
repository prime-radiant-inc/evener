import type { ChangeEvent } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./input.module.css";

export interface InputProps {
  value: string;
  onChange: (event: ChangeEvent<HTMLInputElement>) => void;
  placeholder?: string;
  disabled?: boolean;
  type?: "text" | "password" | "email" | "search" | "number" | "tel" | "url";
  id?: string;
  name?: string;
  "aria-describedby"?: string;
}

const BASE_CLASS = {
  input: requireClass(styles.input, "input.module.css", "input"),
};

/** A single-line text field. Controlled only (value/onChange, mirroring
 * Button's raw-DOM-event onClick convention) - labeling is the consumer's
 * job via a standard `<label htmlFor>` wired to `id`, same as a native
 * input. */
export function Input({
  value,
  onChange,
  placeholder,
  disabled = false,
  type = "text",
  id,
  name,
  "aria-describedby": ariaDescribedBy,
}: InputProps) {
  return (
    <input
      type={type}
      id={id}
      name={name}
      className={BASE_CLASS.input}
      value={value}
      onChange={onChange}
      placeholder={placeholder}
      disabled={disabled}
      aria-describedby={ariaDescribedBy}
    />
  );
}
