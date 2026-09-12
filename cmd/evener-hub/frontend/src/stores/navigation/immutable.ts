export function cloneAndDeepFreezeJSON<T>(value: T): T {
  if (Array.isArray(value)) {
    return Object.freeze(value.map((item) => cloneAndDeepFreezeJSON(item))) as T;
  }
  if (value !== null && typeof value === "object") {
    const clone = Object.fromEntries(Object.entries(value).map(([key, item]) => [key, cloneAndDeepFreezeJSON(item)]));
    return Object.freeze(clone) as T;
  }
  return value;
}

export function equalJSON(left: unknown, right: unknown): boolean {
  if (left === right) return true;
  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length) return false;
    return left.every((item, index) => equalJSON(item, right[index]));
  }
  if (left === null || right === null || typeof left !== "object" || typeof right !== "object") return false;
  const leftRecord = left as Record<string, unknown>;
  const rightRecord = right as Record<string, unknown>;
  const leftKeys = Object.keys(leftRecord);
  const rightKeys = Object.keys(rightRecord);
  if (leftKeys.length !== rightKeys.length) return false;
  return leftKeys.every((key) => Object.hasOwn(rightRecord, key) && equalJSON(leftRecord[key], rightRecord[key]));
}
