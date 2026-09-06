import type { LaunchOption } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
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
