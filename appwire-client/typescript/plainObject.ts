// The shared plain-object guard and JSON-value equality, used across this
// package and by the apps through the package root.
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

// Structural equality for JSON-shaped values: scalars, arrays and plain
// objects, compared by content. Every read of stored or fetched data hands back
// new objects even when nothing changed, so content is what tells a real change
// from a fresh copy. Objects compare by their own enumerable keys, so this
// answers for JSON data only: a Blob or a Date has no such keys and would
// compare equal to any other.
export function sameJsonValue(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true;
  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length) return false;
    return left.every((item, index) => sameJsonValue(item, right[index]));
  }
  if (typeof left !== "object" || typeof right !== "object" || left === null || right === null) return false;
  const leftRecord = left as Record<string, unknown>;
  const rightRecord = right as Record<string, unknown>;
  const keys = Object.keys(leftRecord);
  return (
    keys.length === Object.keys(rightRecord).length &&
    keys.every((key) => Object.hasOwn(rightRecord, key) && sameJsonValue(leftRecord[key], rightRecord[key]))
  );
}
