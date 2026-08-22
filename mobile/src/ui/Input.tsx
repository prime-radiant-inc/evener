import type { InputHTMLAttributes, JSX } from "react";

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  readonly label?: string;
}

/**
 * Focused text input with a 44px minimum height and accent focus ring. The
 * `label` renders as a grouped row header above the field.
 */
export function Input({
  label,
  className,
  id,
  ...rest
}: InputProps): JSX.Element {
  const inputId = id ?? rest.name;
  const cls = `evener-input${className ? ` ${className}` : ""}`;
  if (label === undefined) {
    return <input id={inputId} className={cls.trim()} {...rest} />;
  }
  return (
    <span className="evener-form-row">
      <label className="evener-form-row__label" htmlFor={inputId}>
        {label}
      </label>
      <input id={inputId} className={cls.trim()} {...rest} />
    </span>
  );
}
