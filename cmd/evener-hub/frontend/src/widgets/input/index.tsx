import type { ChangeEvent, InputHTMLAttributes, Ref } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./input.module.css";

export interface InputProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "className" | "style" | "size"> {
  value: string;
  onChange: (event: ChangeEvent<HTMLInputElement>) => void;
  placeholder?: string;
  disabled?: boolean;
  required?: boolean;
  autoComplete?: string;
  type?: "text" | "password" | "email" | "search" | "number" | "tel" | "url";
  id?: string;
  name?: string;
  /** Accessible name for a field with no visible `<label>` (a filter whose
   * placeholder is its only visual cue). Prefer a `<label htmlFor>` where the
   * layout has room for one. */
  "aria-label"?: string;
  "aria-describedby"?: string;
  /** Fill the containing grid/flex cell instead of resting at the field's
   * intrinsic width. Off, the field keeps its default width. */
  fill?: boolean;
  /** The roomier field (the command palette): symmetric `--space-3` padding
   * and the body size, floored at the phone's control size. Named `padding`
   * rather than `size` so the native numeric `size` attribute stays part of
   * the pass-through surface. */
  padding?: "default" | "roomy";
  /** The native `<input>`, for a caller that must focus or select its text
   * rather than query the DOM for it. */
  ref?: Ref<HTMLInputElement>;
}

const BASE_CLASS = {
  input: requireClass(styles.input, "input.module.css", "input"),
  fillWidth: requireClass(styles.fillWidth, "input.module.css", "fillWidth"),
  roomy: requireClass(styles.roomy, "input.module.css", "roomy"),
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
  required,
  autoComplete,
  type = "text",
  id,
  name,
  "aria-label": ariaLabel,
  "aria-describedby": ariaDescribedBy,
  fill = false,
  padding = "default",
  ref,
  ...rest
}: InputProps) {
  return (
    <input
      {...rest}
      ref={ref}
      type={type}
      id={id}
      name={name}
      className={`${BASE_CLASS.input}${fill ? ` ${BASE_CLASS.fillWidth}` : ""}${
        padding === "roomy" ? ` ${BASE_CLASS.roomy}` : ""
      }`}
      value={value}
      onChange={onChange}
      placeholder={placeholder}
      disabled={disabled}
      required={required}
      autoComplete={autoComplete}
      aria-label={ariaLabel}
      aria-describedby={ariaDescribedBy}
    />
  );
}
