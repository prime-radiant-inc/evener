// The package's one plain-object guard. Four modules used to carry private
// copies - client.ts's isRecord, toolCallText.ts's isPlainObject,
// activityData.ts's stricter prototype-checking isPlainObject, and the guard
// inlined in launchInherited.ts's asEnvEntries (#1425). The strict variant
// wins: activityData.ts asks it of every node of the recursive evener/jobs/list
// tree, where rejecting a class instance or other non-plain object before
// walking its keys is the point. The other three sites read JSON-derived wire
// values, whose prototypes are always Object.prototype, so the prototype check
// is a no-op there rather than a behaviour change.
export function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}
