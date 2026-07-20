/**
 * CSS Modules import as an index signature (`{ [key: string]: string }`),
 * so under this project's noUncheckedIndexedAccess every `styles.foo`
 * access is typed `string | undefined` - TypeScript can't know the module
 * actually has a "foo" class. requireClass turns a missing class into a
 * loud, immediate error (at module load, for a widget's class-lookup
 * tables; at the assertion site, for a test comparing against one) instead
 * of a silently-wrong className (template-literal interpolation coerces
 * `undefined` to the string `"undefined"` without a type error, which is
 * how this went unnoticed in Button until Cadence's tests - which compare
 * against actual `styles.*` values instead of just checking classes are
 * pairwise distinct - surfaced it).
 *
 * Canonical pattern for every widget: build a `Record<Variant, string>` (or
 * a flat object of base classes) once, at module scope, by running each
 * `styles.foo` through this. Never call `styles.foo` directly in render.
 */
export function requireClass(value: string | undefined, moduleLabel: string, name: string): string {
  if (value === undefined) throw new Error(`${moduleLabel} is missing the "${name}" class`);
  return value;
}
