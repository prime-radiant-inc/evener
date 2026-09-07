// The structured reply format shared by Evener clients. Replies are sent as
// user messages; transcript renderers also read their headers and resolutions.
// Callers validate selected labels and fallback availability before composing.
// An unresolved selection (null) composes as an explicit skip.
export type AskResolution =
  | { kind: "option"; labels: string[] }
  | { kind: "free"; text: string }
  | { kind: "decide"; leaning: string }
  | { kind: "fallback" }
  | { kind: "skip" };

// AskAnswerItem is the minimal shape composeAskAnswers needs from one
// question: its header (for the "[Header]" tag, JSON-encoded when it contains
// framing characters), its current resolution,
// its optional note, and - only for a fallback resolution - the model's own
// if_unanswered text to embed verbatim.
export interface AskAnswerItem {
  header?: string;
  resolution: AskResolution | null;
  note: string;
  ifUnanswered?: string;
}

function askAnswerHeader(header: string | undefined, index: number): string {
  const value = header ?? `Question ${index + 1}`;
  if (!/[\]\r\n]/u.test(value)) return value;
  return JSON.stringify(value)
    .replace(/\u2028/g, "\\u2028")
    .replace(/\u2029/g, "\\u2029");
}

// Preserve the reply format's string escaping, including hexadecimal C0 escapes.
export function quoteGoString(s: string | undefined): string {
  const value = s ?? "";
  let out = '"';
  for (let i = 0; i < value.length; i++) {
    const ch = value[i];
    const code = value.charCodeAt(i);
    if (ch === "\\") out += "\\\\";
    else if (ch === '"') out += '\\"';
    else if (ch === "\n") out += "\\n";
    else if (ch === "\t") out += "\\t";
    else if (ch === "\r") out += "\\r";
    else if (code < 0x20) out += `\\x${code.toString(16).padStart(2, "0")}`;
    else out += ch;
  }
  return `${out}"`;
}

// An unanswered item and an explicit skip use the same representation.
function askResolutionText(item: AskAnswerItem): string {
  const r = item.resolution;
  if (r === null) return "skipped (no answer)";
  if (r.kind === "option" && r.labels.length > 0) {
    return r.labels.map(quoteGoString).join(", ");
  }
  if (r.kind === "free") return `free text: ${quoteGoString(r.text)}`;
  if (r.kind === "decide") {
    let s = "you decide";
    if (r.leaning?.trim()) s += ` — leaning: ${quoteGoString(r.leaning)}`;
    return s;
  }
  if (r.kind === "fallback") return `do your stated fallback (${quoteGoString(item.ifUnanswered ?? "")})`;
  // r.kind === "skip", or an "option" resolution with an empty labels array
  // (shouldn't occur in practice - the UI never commits an option
  // resolution with nothing checked - but degrades the same as skip rather
  // than composing a bare "→ " with nothing after it).
  return "skipped (no answer)";
}

// Number every question in posting order across the pending ask_user calls.
// Every resolution can carry an optional note; composition does not submit it.
export function composeAskAnswers(items: readonly AskAnswerItem[]): string {
  const lines = ["[answers]"];
  items.forEach((item, idx) => {
    let line = `${idx + 1}. [${askAnswerHeader(item.header, idx)}] → ${askResolutionText(item)}`;
    if (item.note.trim()) line += ` — note: ${quoteGoString(item.note)}`;
    lines.push(line);
  });
  return lines.join("\n");
}
