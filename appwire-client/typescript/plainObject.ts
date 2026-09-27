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
// from a fresh copy. Any other object (a Date, a Blob, a Map) keeps its content
// out of its own keys, so it is the same value only as itself.
export function sameJsonValue(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true;
  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length) return false;
    return left.every((item, index) => sameJsonValue(item, right[index]));
  }
  if (!isJsonObject(left) || !isJsonObject(right)) return false;
  const keys = Object.keys(left);
  return (
    keys.length === Object.keys(right).length &&
    keys.every((key) => Object.hasOwn(right, key) && sameJsonValue(left[key], right[key]))
  );
}

// A plain object from any realm. isPlainObject's prototype check would reject
// one whose Object.prototype is another realm's, which is what a record read
// back from the tests' fake-indexeddb carries, so this reads the object's tag.
function isJsonObject(value: unknown): value is Record<string, unknown> {
  return Object.prototype.toString.call(value) === "[object Object]";
}
