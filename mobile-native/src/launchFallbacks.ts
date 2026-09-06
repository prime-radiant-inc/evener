import { launchFieldConflictMessage } from "./launchScalar";

export function addFallback(items: readonly string[], model: string): string[] {
  if (items.includes(model)) throw Error("Already added.");
  return [...items, model];
}

export function collectFallbacks(items: string[], explicitEmpty: boolean) {
  return items.length || explicitEmpty ? items : undefined;
}

function equal(a: string[] | undefined, b: string[] | undefined) {
  if (!a || !b) return a === b;
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

export function assertFallbacksCurrent(
  original: string[] | undefined,
  current: string[] | undefined,
  next: string[] | undefined,
) {
  if (!equal(original, current) && !equal(current, next))
    throw Error(launchFieldConflictMessage);
}
