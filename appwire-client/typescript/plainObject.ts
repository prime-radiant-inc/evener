// The shared plain-object guard, used across this package and by the apps
// through the package root.
//
// It is deliberately strict: a record is a non-null, non-array object whose
// prototype is Object.prototype or null. The activity-tree parser
// (activityData.ts) walks the untrusted evener/jobs/list tree and must reject
// class instances and other non-plain objects before reading their keys. Every
// other caller reads JSON-derived values, whose prototypes are always
// Object.prototype, so the same strictness is free there.
export function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}
