// The inherited-items computation collection controls share: the entries a
// session would inherit from lower config layers, which the settings form and
// the spawn pane's advanced options render as grayed "(default)" ghost rows.
// The effective layer already contains the local overrides (resolve includes
// them), so subtracting the local keys yields exactly the inherited entries
// under each kind's merge semantics.
import { isPlainObject } from "./plainObject";
import type { MCPServerSpec } from "./types.gen";

/** Effective value minus local entries, keyed by the caller's key extractor.
 * Returns [] when the effective value is absent or the wrong shape. */
export function inheritedItems<T>(
  effective: unknown,
  local: readonly T[],
  key: (item: T) => string,
  fromEffective: (value: unknown) => T[],
): T[] {
  const localKeys = new Set(local.map(key));
  return fromEffective(effective).filter((item) => !localKeys.has(key(item)));
}

// --- shape adapters: coerce an unknown effective-layer value into the typed
// array the caller's key extractor expects ---

export function asStringList(value: unknown): string[] {
  return Array.isArray(value) ? (value as string[]) : [];
}

export function asEnvEntries(value: unknown): [string, string][] {
  return isPlainObject(value) ? Object.entries(value as Record<string, string>) : [];
}

/** Same as asEnvEntries but as {name, value} objects — the shape
 * EnvMapField's inheritedItems prop expects. */
export function asEnvObjects(value: unknown): { name: string; value: string }[] {
  return asEnvEntries(value).map(([name, val]) => ({ name, value: val }));
}

export function asMcpList(value: unknown): MCPServerSpec[] {
  return Array.isArray(value) && value.every((item) => typeof item === "object" && item !== null && "name" in item)
    ? (value as MCPServerSpec[])
    : [];
}
