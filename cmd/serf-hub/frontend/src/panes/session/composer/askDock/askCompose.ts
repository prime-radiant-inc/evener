// The [answers] reply composition (parity contracts-composer-queue-pending.md
// §"Ask User"/test-ask-compose.js): byte-exact, golden-string tested, ported
// verbatim from cmd/serf-hub/assets/renderer.js's composeAskAnswers/
// askResolutionText/quoteGoString (renderer.js:6980-7031). This is not
// cosmetic formatting - the composed text round-trips through the daemon's
// own reply parser, so every character here is load-bearing.

// AskResolution mirrors legacy's exactly-one-of-5-kinds rule (spec §4.3,
// renderer.js:5874-5882): option (single or multi_select), free text,
// decide (with an optional leaning), fallback (only offered when the
// question carries if_unanswered), and skip. `null` (no resolution chosen
// yet) composes identically to an explicit skip - see askResolutionText.
export type AskResolution =
  | { kind: "option"; labels: string[] }
  | { kind: "free"; text: string }
  | { kind: "decide"; leaning: string }
  | { kind: "fallback" }
  | { kind: "skip" };

// AskAnswerItem is the minimal shape composeAskAnswers needs from one
// question: its header (for the "[Header]" tag), its current resolution,
// its optional note, and - only for a fallback resolution - the model's own
// if_unanswered text to embed verbatim.
export interface AskAnswerItem {
  header?: string;
  resolution: AskResolution | null;
  note: string;
  ifUnanswered?: string;
}

function askAnswerHeader(header: string | undefined, index: number): string {
  return header ?? `Question ${index + 1}`;
}

// quoteGoString mirrors Go's %q escaping (renderer.js:6975-6994) exactly:
// backslash, double-quote, \n, \t, \r get their own escape; every other C0
// control character (code < 0x20) becomes \xHH; everything else passes
// through unchanged. Iterates by UTF-16 code unit (not code point,
// `for...of`) to match the legacy implementation verbatim - astral
// characters (surrogate pairs) fall through the same "else" branch either
// way since neither half is < 0x20, so this only matters for exact-parity
// paranoia, not observable behavior.
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

// askResolutionText renders one question's resolution per spec §4.3's exact
// vocabulary (renderer.js:6996-7014). An item with no resolution composes
// identically to an explicit skip ("skipped (no answer)") - the reply
// format defines exactly 5 resolution kinds, not a 6th "unanswered" kind.
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

// composeAskAnswers renders the [answers] reply (spec §4.3, byte-exact):
// global numbering in posting order across every ask_user call in the
// pending set, one resolution per line, every line carrying its header and
// an optional trailing note (the annotation is universal, not chip-only).
export function composeAskAnswers(items: AskAnswerItem[]): string {
  const lines = ["[answers]"];
  items.forEach((item, idx) => {
    let line = `${idx + 1}. [${askAnswerHeader(item.header, idx)}] → ${askResolutionText(item)}`;
    if (item.note.trim()) line += ` — note: ${quoteGoString(item.note)}`;
    lines.push(line);
  });
  return lines.join("\n");
}
