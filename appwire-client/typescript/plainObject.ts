// The plain-object guard shared by the client's JSON-RPC initialize boundary
// (client.ts), the tool-call argument/output readers (toolCallText.ts), the
// recursive activity-tree parser (activityData.ts), and the launch
// inherited-items adapters (launchInherited.ts).
//
// This is the shared guard for those callers. transcriptDisplayConfig.ts and
// state/navigation/codec.ts still carry their own local isRecord for the
// config/wire shapes they decode; folding them into this module is a
// deliberate follow-up rather than an oversight.
//
// It is deliberately strict: a record is a non-null, non-array object whose
// prototype is Object.prototype or null. The activity-tree parser walks the
// untrusted evener/jobs/list tree and must reject class instances and other
// non-plain objects before reading their keys. The other three sites read
// JSON-derived values, whose prototypes are always Object.prototype, so the
// same strictness is free there.
export function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}
