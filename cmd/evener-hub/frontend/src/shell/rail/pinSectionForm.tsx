import { type ChangeEvent, useId } from "react";
import { FormRow, Input } from "../../widgets";

/** The pin-section name rules, shared by the create flow and the rename
 * dialog. Returns the message to show, or undefined when the trimmed name is
 * usable. */
export function validatePinSectionName(raw: string): string | undefined {
  const name = raw.trim();
  if (name === "") return "Section name is required";
  if (Array.from(name).length > 80) return "Section names must be 80 characters or fewer";
  return undefined;
}

/** The "Section name" field both pin-section forms render: the labeled row,
 * the input, and the error wiring. Typing reports the new value; clearing the
 * error that the last submit raised stays with the caller that owns it. */
export function PinSectionNameField({
  value,
  onChange,
  error,
  disabled,
}: {
  value: string;
  onChange: (value: string) => void;
  error?: string;
  disabled?: boolean;
}) {
  const inputID = useId();
  return (
    <FormRow label="Section name" htmlFor={inputID} error={error}>
      <Input
        id={inputID}
        value={value}
        onChange={(event: ChangeEvent<HTMLInputElement>) => onChange(event.target.value)}
        disabled={disabled}
        aria-describedby={error ? `${inputID}-error` : undefined}
      />
    </FormRow>
  );
}
