import type { JSX, ReactNode } from "react";

export interface FormRowProps {
  readonly label: string;
  readonly htmlFor?: string;
  readonly children: ReactNode;
}

/**
 * Labeled form row within a grouped list: label above the control.
 */
export function FormRow({
  label,
  htmlFor,
  children,
}: FormRowProps): JSX.Element {
  return (
    <span className="evener-form-row">
      <label className="evener-form-row__label" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
    </span>
  );
}
