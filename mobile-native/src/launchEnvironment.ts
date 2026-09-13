import { launchFieldConflictMessage } from "./launchScalar";

export function addEnvironmentVariable(
  values: Record<string, string>,
  name: string,
  value: string,
): Record<string, string> {
  const key = name.trim();
  if (!key || key.includes("="))
    throw Error("Enter a variable name without =.");
  return { ...values, [key]: value };
}

/** An empty environment override inherits, matching the web launch collector. */
export function collectEnvironment(values: Record<string, string>) {
  return Object.keys(values).length ? values : undefined;
}

function equal(
  a: Record<string, string> | undefined,
  b: Record<string, string> | undefined,
) {
  if (!a || !b) return a === b;
  return (
    Object.keys(a).length === Object.keys(b).length &&
    Object.entries(a).every(
      ([key, value]) => Object.hasOwn(b, key) && b[key] === value,
    )
  );
}

export function assertEnvironmentCurrent(
  original: Record<string, string> | undefined,
  current: Record<string, string> | undefined,
  next: Record<string, string> | undefined,
) {
  if (!equal(original, current) && !equal(current, next))
    throw Error(launchFieldConflictMessage);
}
