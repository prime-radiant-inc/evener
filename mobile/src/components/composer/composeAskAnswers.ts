// composeAskAnswers — pure [answers] composition ported verbatim from the
// Hub's askCompose.ts (renderer.js:6980-7031). Every character is load-bearing
// because the text round-trips through the daemon's reply parser.
//
// Extracted from AskComposer.tsx so the canonical AskComposer and the Plan 2
// live intent dispatcher share one byte-exact implementation.

// AskResolution: the exactly-one-of-5-kinds rule. null = no resolution chosen
// (composes identically to an explicit skip).
export type AskResolution =
  | { kind: "option"; labels: string[] }
  | { kind: "free"; text: string }
  | { kind: "decide"; leaning: string }
  | { kind: "fallback" }
  | { kind: "skip" };

export interface AskAnswerItem {
  header?: string;
  resolution: AskResolution | null;
  note: string;
  ifUnanswered?: string;
}

// quoteGoString mirrors Go's %q escaping: backslash, double-quote, \n, \t, \r
// get their own escape; every other C0 control char (< 0x20) becomes \xHH;
// everything else passes through. Iterates by UTF-16 code unit to match the
// legacy implementation verbatim.
function quoteGoString(s: string | undefined): string {
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

function askAnswerHeader(header: string | undefined, index: number): string {
  return header ?? `Question ${index + 1}`;
}

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
  if (r.kind === "fallback") {
    return `do your stated fallback (${quoteGoString(item.ifUnanswered ?? "")})`;
  }
  return "skipped (no answer)";
}

export function composeAskAnswers(items: readonly AskAnswerItem[]): string {
  const lines = ["[answers]"];
  items.forEach((item, idx) => {
    let line = `${idx + 1}. [${askAnswerHeader(item.header, idx)}] → ${askResolutionText(item)}`;
    if (item.note.trim()) line += ` — note: ${quoteGoString(item.note)}`;
    lines.push(line);
  });
  return lines.join("\n");
}
