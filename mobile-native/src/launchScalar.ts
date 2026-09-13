import type { LaunchOption } from "../../appwire-client/typescript/types.gen";
export const scalarKinds = new Set([
  "text",
  "multilineText",
  "integer",
  "select",
  "radio",
  "boolean",
  "modelPicker",
  "path",
]);
export const launchFieldConflictMessage =
  "This setting changed while you were editing. Your text is kept; cancel and reopen the setting to review its current value.";

/** A field sheet must not silently overwrite a newer explicit value. */
export function assertLaunchFieldCurrent(
  original: string,
  current: string,
  next: string | number | boolean | undefined,
) {
  if (current !== original && current !== String(next ?? ""))
    throw Error(launchFieldConflictMessage);
}

export function parseLaunchScalar(
  option: LaunchOption,
  raw: string,
): string | number | boolean | undefined {
  if (!scalarKinds.has(option.kind))
    throw Error("This setting requires a collection editor.");
  const value = raw.trim();
  if (!value) return undefined;
  if (option.kind === "boolean") {
    if (value !== "true" && value !== "false")
      throw Error("Choose an on or off value.");
    return value === "true";
  }
  if (option.kind === "integer") {
    const number = Number(value);
    if (!Number.isSafeInteger(number)) throw Error("Enter a whole number.");
    return number;
  }
  if (
    (option.kind === "select" || option.kind === "radio") &&
    !option.choices?.some(
      (choice) => choice.value === value && !choice.disabled,
    )
  )
    throw Error("Choose an available value.");
  return value;
}
