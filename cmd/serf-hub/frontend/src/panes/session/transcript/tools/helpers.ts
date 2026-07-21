// Shared formatting/parsing helpers for the per-tool descriptors in this
// directory. Ground-truth notes (see the wave-4 task-3 report for full
// citations): ItemModel carries only the tool call's OWN output TEXT
// (item.output) and its input arguments (item.argumentsJSON) - the wire's
// ThreadItem.error and ThreadItem.raw (tool_state) fields are both dropped
// by protocol/reducer.ts's wireItemToModel before they ever reach a
// component, so every helper here works from plain text/JSON-args, never
// from a structured tool_state snapshot the legacy renderer-tools.js relied
// on.

// clip is a head-truncation: text at or under `max` passes through
// unchanged; over budget, keeps the first `max` chars and appends a single
// ellipsis character (mirrors renderer-format.js's clip()).
export function clip(text: string, max: number): string {
  return text.length <= max ? text : text.slice(0, max) + "…";
}

// tailSlice keeps the LAST `max` chars, advancing the cut point by one when
// it would otherwise land inside a UTF-16 surrogate pair (dropping the
// orphaned low surrogate rather than rendering U+FFFD) - mirrors
// renderer-format.js's tailSlice, used for shell/read's live streaming tail
// and the tail-fold on success.
export function tailSlice(text: string, max: number): string {
  if (text.length <= max) return text;
  let cut = text.length - max;
  const code = text.charCodeAt(cut);
  if (code >= 0xdc00 && code <= 0xdfff) cut += 1;
  return text.slice(cut);
}

// tailFold no-ops under `max` chars; over budget, prefixes an honest
// elision line before the kept tail - mirrors renderer-tools.js's
// tailFoldOutput, used wherever a settled (non-error) tool output is shown
// so a human always knows text was dropped rather than seeing a
// silently-incomplete transcript.
export function tailFold(text: string, max: number): string {
  if (text.length <= max) return text;
  return `earlier output not retained — showing the last ${max.toLocaleString("en-US")} chars\n${tailSlice(text, max)}`;
}

// formatToolDuration mirrors renderer-format.js's formatToolDuration: a
// sub-1000ms duration floors at 1ms (rounding, never "0ms"); 1s-10s shows
// one decimal with a trailing ".0" stripped; 10s and up rounds to whole
// seconds.
export function formatToolDuration(ms: number): string {
  if (ms < 1000) return `${Math.max(1, Math.round(ms))}ms`;
  if (ms < 10000) {
    const seconds = (ms / 1000).toFixed(1);
    return `${seconds.endsWith(".0") ? seconds.slice(0, -2) : seconds}s`;
  }
  return `${Math.round(ms / 1000)}s`;
}

// formatByteCount never unit-scales (no KB/MB) - renderer-format.js's
// formatBytes is documented as deliberately literal; singular only for
// exactly 1 byte.
export function formatByteCount(n: number): string {
  return `${n} byte${n === 1 ? "" : "s"}`;
}

// lineCount splits on "\n" and drops exactly one trailing empty element (the
// artifact of a final newline), never more than one - mirrors
// renderer-tools.js's splitOutputLines, used for grep/ls/glob's hit/entry/
// match counts.
export function lineCount(text: string): number {
  if (text === "") return 0;
  const lines = text.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  return lines.length;
}

// parseArgs defensively decodes a tool call's argumentsJSON into a plain
// object: undefined input, malformed JSON, or a well-formed-but-non-object
// JSON value (array/string/number/null) all degrade to {} rather than
// throwing - every per-tool descriptor reads optional fields off this with
// `str`/direct indexing, so a blank object is always a safe zero value.
export function parseArgs(argumentsJSON: string | undefined): Record<string, unknown> {
  if (argumentsJSON === undefined) return {};
  try {
    const parsed: unknown = JSON.parse(argumentsJSON);
    return isPlainObject(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

// parseJSONObject is parseArgs' sibling for parsing a tool's OWN qualifying
// output text as JSON (only the `delegate` tool's output is actually
// JSON - see this module's own file header) - returns undefined (not {})
// on any failure, so a caller can distinguish "no JSON here, fall back to
// plain text" from "valid JSON, happens to be empty".
export function parseJSONObject(text: string | undefined): Record<string, unknown> | undefined {
  if (text === undefined) return undefined;
  try {
    const parsed: unknown = JSON.parse(text);
    return isPlainObject(parsed) ? parsed : undefined;
  } catch {
    return undefined;
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// rememberedArgs mitigates a confirmed gap in the wire/reducer this
// directory does not own: internal/appprojector/appwire_projection.go's
// EventToolCallEnd case never sets ThreadItem.ArgumentsJSON on the
// item/completed struct literal (only item/started carries it), and
// protocol/reducer.ts's item/completed handler replaces the whole item via
// wireItemToModel(item) with no argumentsJSON fallback - verified live
// against the real reducer (hydrate -> item/started -> item/completed),
// not assumed. Every descriptor in this directory reads its target/summary
// fields off argumentsJSON, so without this a settled tool call - the
// overwhelming majority of what a transcript actually shows - would
// silently render blank targets. This caches a call's parsed args the
// first time they're seen non-empty (i.e. while the item/started event's
// argumentsJSON is intact), keyed by callId (falling back to the item id
// for the rare item with no callId), and serves that cache once the
// wire-settled item's own args go missing. This is a same-directory,
// same-session-lifetime cache: a session opened cold on an already-
// completed historical turn (which never rendered an item/started for
// that call) has no entry to fall back to and still degrades to {} - the
// durable fix belongs in protocol/reducer.ts (preserve argumentsJSON from
// the prior item the way mergeReasoning already preserves
// reasoningSummaries), which is outside this stream's file ownership; see
// the wave-4 task-3 report for the full citation trail.
const argsCache = new Map<string, Record<string, unknown>>();

export function rememberedArgs(item: { id: string; callId?: string; argumentsJSON?: string }): Record<string, unknown> {
  const key = item.callId ?? item.id;
  const parsed = parseArgs(item.argumentsJSON);
  if (Object.keys(parsed).length > 0) {
    argsCache.set(key, parsed);
    return parsed;
  }
  return argsCache.get(key) ?? parsed;
}

// trailingBracketFooter extracts the inner text of a trailing "[...]"
// segment - the shape several agent-side formatters end their plain-text
// tool output in (agent/session_tools_shell.go's formatShellResult,
// agent/session_tools_jobs.go's formatJobStop/formatDelegateSend/
// formatJobReadOutput). Returns undefined when the (right-trimmed) text
// doesn't end in a closing bracket at all - a still-running/backgrounded
// call, or a tool with no footer convention.
export function trailingBracketFooter(text: string): string | undefined {
  const trimmed = text.trimEnd();
  if (!trimmed.endsWith("]")) return undefined;
  const openIdx = trimmed.lastIndexOf("[");
  if (openIdx === -1) return undefined;
  return trimmed.slice(openIdx + 1, trimmed.length - 1);
}

// str reads a string-typed field off a parsed-args/output object, undefined
// for a missing or non-string value - every descriptor's target/summary
// logic uses this rather than trusting the wire's untyped JSON directly.
export function str(obj: Record<string, unknown>, key: string): string | undefined {
  const value = obj[key];
  return typeof value === "string" ? value : undefined;
}
