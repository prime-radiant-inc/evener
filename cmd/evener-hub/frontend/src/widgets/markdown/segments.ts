// Splits a message at top-level CLOSED mermaid fences. Web markdown slices
// are token arrays (never re-serialized source), so link definitions resolve
// exactly as a whole-document parse: marked v18 resolves references at lex
// time against the lexer's document-wide registry (verified in the spec's
// review round: parser output is identical with a corrupted registry, and a
// def-stripped slice still renders its links).
//
// Live mode passes the closeOpenMarkdown output as closedSource and the
// pre-close string as realSource; a mermaid fence that is still open in the
// real source is DEMOTED to a markdown slice (it renders as a code block
// until its closing fence arrives - the spec's locked streaming decision).
// Identification compares the real source's own lex, never raw-vs-source
// suffix checks: marked normalizes CRLF at lex, so a CRLF fence's raw does
// not suffix-match the source (verified).
//
// The fence helpers below (MERMAID_FENCE, isMermaidCodeToken, mermaidText,
// FENCE_CLOSE_LINE, fenceTokenTerminated) are mirrored in the native sibling
// mobile-native/src/markdownSegments.ts - keep the two in sync (their bodies
// deliberately differ where noted there).
import type { Token } from "marked";
import { markdownLexer } from "./lexer";

export type MarkdownSegment = { kind: "markdown"; tokens: Token[] } | { kind: "mermaid"; text: string };

// Cheap gate so non-mermaid messages never pay for a second lex. A false
// positive (a nested or quoted fence) only takes the segmented path; a false
// negative would skip it, so the pattern allows leading indentation and both
// fence characters.
const MERMAID_FENCE = /^ {0,3}(?:`{3,}|~{3,})[ \t]*mermaid(?:[ \t\r]|$)/im;

export function messageMayContainMermaid(source: string): boolean {
  return MERMAID_FENCE.test(source);
}

function isMermaidCodeToken(token: Token): boolean {
  return (
    token.type === "code" &&
    // marked passes the info string through as lang; the first word decides,
    // case-insensitive, so "```mermaid title" and "```Mermaid" both render.
    (token as { lang?: string }).lang?.trim().split(/\s+/)[0]?.toLowerCase() === "mermaid"
  );
}

// The diagram source is the fenced block's content, restored to its
// newline-terminated source form. marked strips exactly one trailing newline
// from a code token's text at lex, so a non-empty text is missing that final
// "\n" - the test pins the restored value ("graph TD; A-->B\n"), and mermaid
// consumes a newline-terminated string unchanged.
function mermaidText(token: Token): string {
  const text = (token as { text?: string }).text ?? "";
  return text.length > 0 ? `${text}\n` : text;
}

const FENCE_CLOSE_LINE = /^ {0,3}(?:`{3,}|~{3,})[ \t]*$/;

// A code token's raw ends with a closing fence line exactly when the fence
// terminated in the source. Normalized for CRLF (marked normalizes at lex,
// so raw never contains "\r"; the normalize call is belt-and-suspenders for
// the lexer's own future changes).
function fenceTokenTerminated(raw: string): boolean {
  const lines = raw.replace(/\r\n/g, "\n").split("\n");
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    const line = lines[index] ?? "";
    if (line.trim() === "") continue;
    return FENCE_CLOSE_LINE.test(line);
  }
  return false;
}

// The real source's tail is an open mermaid fence exactly when its last
// non-space top-level token is an unterminated mermaid code token (an open
// fence swallows the rest of the document, so it is always the tail).
function realSourceTailIsOpenMermaid(realSource: string): boolean {
  let tokens: Token[];
  try {
    tokens = markdownLexer.lexer(realSource);
  } catch {
    return false; // lexer failure fails open: no demote, today's behavior
  }
  for (let index = tokens.length - 1; index >= 0; index -= 1) {
    const token = tokens[index];
    if (token === undefined || token.type === "space") continue;
    return isMermaidCodeToken(token) && !fenceTokenTerminated((token as { raw?: string }).raw ?? "");
  }
  return false;
}

export function splitMarkdownSegments(closedSource: string, realSource: string | null): MarkdownSegment[] {
  const tokens = markdownLexer.lexer(closedSource);
  const segments: MarkdownSegment[] = [];
  let markdownRun: Token[] = [];
  const flush = () => {
    if (markdownRun.length > 0) segments.push({ kind: "markdown", tokens: markdownRun });
    markdownRun = [];
  };
  for (const token of tokens) {
    if (isMermaidCodeToken(token)) {
      flush();
      segments.push({ kind: "mermaid", text: mermaidText(token) });
    } else {
      markdownRun.push(token);
    }
  }
  flush();
  // Demote: a live stream whose real source ends in an open mermaid fence
  // produced a closed-lex mermaid segment only because the auto-closer
  // appended the fence. Turn that last segment back into markdown so it
  // renders as the code block it still is.
  if (realSource !== null && realSourceTailIsOpenMermaid(realSource)) {
    const last = segments[segments.length - 1];
    if (last?.kind === "mermaid") {
      // The first lex above already produced this token; reuse its last entry
      // rather than re-lexing the whole source to find the same fence.
      const demoted = tokens.slice(-1);
      segments[segments.length - 1] = { kind: "markdown", tokens: demoted };
    }
  }
  return segments;
}
