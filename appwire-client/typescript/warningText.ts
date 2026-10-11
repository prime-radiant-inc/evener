// The one reading of "is this warning text actually content": a non-blank
// string. The reducer's warning fold, warnings.ts's warningWords and both
// clients' warning rows all ask it, so none of them shows a field another
// treats as empty. A leaf module that imports nothing, so any of them can use
// it without an import cycle.

// True when value is a non-blank string. A type predicate, so a caller
// narrows `unknown` in one step; it rejects a non-string runtime value
// outright, so a malformed wire frame's field never reaches a renderer.
export function hasWarningText(value: unknown): value is string {
  return typeof value === "string" && /\S/.test(value);
}
