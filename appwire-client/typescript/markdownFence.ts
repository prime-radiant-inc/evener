// The mermaid fence rules both apps' markdown renderers split a message on.
// Each app lexes with marked and keeps its own segment shape; what counts as a
// mermaid fence, what a terminated one is, and how its diagram source is
// recovered from a code token are decided here once. The token parameters are
// a structural slice of a marked token, so the package does not depend on marked.

// The slice of a marked token these rules read.
export interface FenceToken {
  type: string;
  lang?: string;
  text?: string;
}

// Cheap gate so non-mermaid messages never pay for a second lex. A false
// positive (a nested or quoted fence) only takes the segmented path; a false
// negative would skip it, so the pattern allows leading indentation and both
// fence characters.
export const MERMAID_FENCE = /^ {0,3}(?:`{3,}|~{3,})[ \t]*mermaid(?:[ \t\r]|$)/im;

// The opening fence run on a token raw's first line (opener char + length), and
// a bare closing-fence line. A closer only terminates when it shares the
// opener's character and is at least as long (CommonMark), so a `~~~` line
// inside a backtick fence is content, not a closer.
const FENCE_OPEN_LINE = /^ {0,3}(`{3,}|~{3,})/;
const FENCE_CLOSE_LINE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;

// marked passes the info string through as lang; the first word decides,
// case-insensitive, so "```mermaid title" and "```Mermaid" both count.
export function isMermaidCodeToken(token: FenceToken): boolean {
  return token.type === "code" && token.lang?.trim().split(/\s+/)[0]?.toLowerCase() === "mermaid";
}

// The diagram source is the fenced block's content, restored to its
// newline-terminated source form. marked strips exactly one trailing newline
// from a code token's text at lex, so a non-empty text is missing that final
// "\n"; mermaid consumes a newline-terminated string unchanged.
export function mermaidText(token: FenceToken): string {
  const text = token.text ?? "";
  return text.length > 0 ? `${text}\n` : text;
}

// A code token's raw ends with a closing fence line exactly when the fence
// terminated in the source. Normalized for CRLF; marked already normalizes at
// lex, so the replace guards a lexer that stops doing so.
export function fenceTokenTerminated(raw: string): boolean {
  const lines = raw.replace(/\r\n/g, "\n").split("\n");
  const opener = FENCE_OPEN_LINE.exec(lines[0] ?? "");
  if (opener === null) return false;
  const openerRun = opener[1] ?? "";
  const char = openerRun.charAt(0);
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    const line = lines[index] ?? "";
    if (line.trim() === "") continue;
    const closer = FENCE_CLOSE_LINE.exec(line);
    if (closer === null) return false;
    const closerRun = closer[1] ?? "";
    return closerRun.charAt(0) === char && closerRun.length >= openerRun.length;
  }
  return false;
}
