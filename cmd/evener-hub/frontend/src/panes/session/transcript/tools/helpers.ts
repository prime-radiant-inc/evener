// Shared formatting/parsing helpers for the per-tool descriptors in this
// directory. Ground-truth note: the model carries the tool call's own output
// TEXT (item.output), input arguments (item.argumentsJSON), error/denial
// message (item.error), typed shell exit code (item.exitCode), and optional
// direct producer state (item.raw), all mapped by protocol/reducer.ts's
// wireItemToModel. These helpers work from text and JSON arguments because
// their current callers do not share a stable structured state shape; a body
// that has a useful producer state parses item.raw at its own domain boundary.

// clip is a head-truncation: text at or under `max` passes through
// unchanged; over budget, keeps the first `max` chars and appends a single
// ellipsis character (mirrors renderer-format.js's clip()).
export function clip(text: string, max: number): string {
  return text.length <= max ? text : `${text.slice(0, max)}…`;
}

// clipJobID keeps the random suffix visible because jobs from one session
// share their owner prefix.
export function clipJobID(jobID: string): string {
  const max = 26;
  if (jobID.length <= max) return jobID;
  const suffix = jobID.slice(-12);
  const prefixLength = max - suffix.length - 1;
  return `${jobID.slice(0, prefixLength)}…${suffix}`;
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

// parseJSONObject is parseArgs' sibling for parsing a tool's own output text
// when that producer's output contract is whole-object JSON (for example
// job_status and delegate). It does not parse item.raw: raw is already the
// producer's direct structured state, and callers that need it validate that
// domain shape explicitly. Returns undefined (not {}) on any failure, so a
// caller can distinguish "no JSON here, fall back to plain text" from
// "valid JSON, happens to be empty".
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
