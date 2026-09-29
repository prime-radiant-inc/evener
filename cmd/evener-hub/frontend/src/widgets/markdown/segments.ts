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
import { closeOpenMarkdown } from "./streaming";

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

// The opening fence run on a token raw's first line (opener char + length), and
// a bare closing-fence line. A closer only terminates when it shares the
// opener's character and is at least as long (CommonMark), so a `~~~` line
// inside a backtick fence is content, not a closer.
const FENCE_OPEN_LINE = /^ {0,3}(`{3,}|~{3,})/;
const FENCE_CLOSE_LINE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;

// A code token's raw ends with a closing fence line exactly when the fence
// terminated in the source. Normalized for CRLF (marked normalizes at lex,
// so raw never contains "\r"; the normalize call is belt-and-suspenders for
// the lexer's own future changes).
function fenceTokenTerminated(raw: string): boolean {
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

// The candidate head is a real freeze point only when the lexer agrees: its
// last non-space top-level token must be a TERMINATED mermaid code token - the
// same definition splitMarkdownSegments splits on. A fence indented as a
// list-item continuation is lexed as a code token INSIDE the list, not a
// top-level code token, so the candidate is rejected and no head is frozen.
function headEndsAtTerminatedMermaid(headSource: string): boolean {
  let tokens: Token[];
  try {
    tokens = markdownLexer.lexer(headSource);
  } catch {
    return false;
  }
  for (let index = tokens.length - 1; index >= 0; index -= 1) {
    const token = tokens[index];
    if (token === undefined || token.type === "space") continue;
    return isMermaidCodeToken(token) && fenceTokenTerminated((token as { raw?: string }).raw ?? "");
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

// Exact link-definition detection through the shared lexer: a definition on
// either side of a split registers globally with marked and resolves `[label]`
// uses anywhere in the whole - including across the split - that a standalone
// slice parse would leave literal, so any `def` token, top level or nested in a
// blockquote/list/table, forces a whole-source lex. A lexer failure fails
// closed to the whole-source path as well. Shared by the windowed single-root
// path (message-level def gates) and the segmented live path (Finding B: a def
// on one side of a closed diagram must still resolve a use on the other).
export function containsLinkDefinition(source: string): boolean {
  let tokens: Token[];
  try {
    tokens = markdownLexer.lexer(source);
  } catch {
    return true;
  }
  return tokensContainDef(tokens);
}

function tokensContainDef(tokens: Token[]): boolean {
  // Any unexpected shape (malformed tokens, a future marked token type with
  // unguarded nesting) fails closed to the whole-source lex: a missed `def`
  // would resolve differently under separate slices, while a spurious fallback
  // is exactly the pre-throttle behavior.
  try {
    for (const token of tokens) {
      if (token.type === "def") return true;
      if ("tokens" in token && tokensContainDef(token.tokens ?? [])) return true;
      if ("items" in token) {
        for (const item of token.items ?? []) {
          if (tokensContainDef(item?.tokens ?? [])) return true;
        }
      }
      if (token.type === "table") {
        for (const cell of [...(token.header ?? []), ...(token.rows ?? []).flat()]) {
          if (tokensContainDef(cell?.tokens ?? [])) return true;
        }
      }
    }
    return false;
  } catch {
    return true;
  }
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
    // A fence that never terminated in the source is NOT a diagram: the settled
    // path (realSource null, so no demote) must leave it a code block, exactly
    // as the live path and the native sibling do. On the live paths every
    // closedSource fence is auto-closed and every frozen head ends at a
    // terminated fence, so this gate is a no-op there.
    if (isMermaidCodeToken(token) && fenceTokenTerminated((token as { raw?: string }).raw ?? "")) {
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

export interface LiveSegmentsCache {
  // The raw scan candidate the entry was computed for - the cache key. It
  // differs from headSource exactly when the candidate was REJECTED (a
  // list-continuation fence): keying on the candidate lets that steady state
  // hit too, instead of re-running the validation lex on every render.
  key: string;
  headSource: string;
  headSegments: MarkdownSegment[];
  // Whether the head text carries a link definition, computed once per distinct
  // head on the cache-miss path. A def on either side of a closed diagram must
  // force a whole-source lex so its use on the other side still resolves live.
  headHasDef: boolean;
}

// A top-level fence line (up to three leading spaces) and its info string.
// Mirrors marked's CommonMark fence rules: a backtick fence whose info contains
// a backtick is not a fence at all.
const ANY_FENCE_OPENER = /^ {0,3}(`{3,}|~{3,})(.*)$/;
const BARE_FENCE_CLOSER = /^ {0,3}(`{3,}|~{3,})$/;

// The end offset of the last top-level CLOSED mermaid fence in the raw stream,
// or 0 when none has closed. The prefix up to this offset is the consumer's
// frozen head: it ends just past a terminated fence line, so it needs neither
// the tail's live auto-close nor the demote rule. Scanned line by line (not
// token by token): a fence's trailing newline is tokenized differently
// depending on what follows it, which would make the same head boundary
// alternate between 38 and 37 and defeat the cache. The scan tracks ALL top
// fences, so a mermaid-looking line inside a non-mermaid fence is code, not an
// opener. The line scan is the only per-render cost; its candidate is validated
// against the token stream by the caller (splitLiveMarkdownSegments) on the
// cache-MISS path only, where a fence indented as a list-item continuation
// line-matches the opener pattern but lexes inside the list, not as a top-level
// code token, and is rejected.
function lastClosedMermaidFenceEnd(source: string): number {
  const lines = source.split("\n");
  let offset = 0;
  let open: { char: string; length: number; mermaid: boolean } | null = null;
  let end = 0;
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index] ?? "";
    if (open === null) {
      const opener = ANY_FENCE_OPENER.exec(line);
      if (opener !== null) {
        const run = opener[1] ?? "";
        const info = opener[2] ?? "";
        if (!(run.startsWith("`") && info.includes("`"))) {
          open = {
            char: run.charAt(0),
            length: run.length,
            mermaid: info.trim().split(/\s+/)[0]?.toLowerCase() === "mermaid",
          };
        }
      }
    } else {
      const closer = BARE_FENCE_CLOSER.exec(line.replace(/[ \t\r]+$/, ""));
      if (closer !== null && (closer[1] ?? "").charAt(0) === open.char && (closer[1] ?? "").length >= open.length) {
        if (open.mermaid) end = offset + line.length + (index < lines.length - 1 ? 1 : 0);
        open = null;
      }
    }
    offset += line.length + (index < lines.length - 1 ? 1 : 0);
  }
  return end;
}

// Live segmentation with the frozen head cached on its exact text. The head
// (the source through the last CLOSED mermaid fence) is segmented once and
// served by object identity on every later render, so MarkdownSlice's per-slice
// memo actually hits while a long tail streams; only the tail is re-segmented
// each render, with closeOpenMarkdown and the demote rule applied within the
// tail (an open mermaid fence at the tail demotes, exactly as the whole-source
// path did). A changed head is a cache miss and re-segments, never stale - the
// same discipline as the windowed path's head cache. With no closed fence the
// head is empty and the whole source is the tail: identical to splitMarkdownSegments.
// The candidate head's token-stream validation (a list-continuation fence must
// not freeze a head) and the head's def verdict run ONLY on that miss path, so
// a steady stream pays just the per-render line scan plus the tail work. A
// definition on either side of a closed diagram forces a whole-source lex so a
// cross-diagram reference resolves live, exactly as it does at settle.
export function splitLiveMarkdownSegments(
  realSource: string,
  cache: { current: LiveSegmentsCache | null },
): MarkdownSegment[] {
  const end = lastClosedMermaidFenceEnd(realSource);
  const candidate = realSource.slice(0, end);
  let entry = cache.current;
  if (entry === null || entry.key !== candidate) {
    // Cache miss (a changed head): validation lex, head segmentation, and the
    // head's def verdict all run only here. The entry keys on the candidate
    // text and validation is a pure function of it, so a hit - including a
    // steady REJECTED candidate, which keeps headSource "" - skips the lex.
    let headSource = candidate;
    if (headSource !== "" && !headEndsAtTerminatedMermaid(headSource)) headSource = "";
    entry = {
      key: candidate,
      headSource,
      headSegments: headSource === "" ? [] : splitMarkdownSegments(headSource, null),
      headHasDef: headSource !== "" && containsLinkDefinition(headSource),
    };
    cache.current = entry;
  }

  const tailSource = realSource.slice(entry.headSource.length);
  // A definition on either side of a closed diagram resolves only under a
  // whole-source lex (same gate the windowed path forces); take it, demote rule
  // and all, rather than lex head and tail separately.
  if (entry.headHasDef || containsLinkDefinition(tailSource)) {
    return splitMarkdownSegments(closeOpenMarkdown(realSource), realSource);
  }

  const tailSegments = tailSource === "" ? [] : splitMarkdownSegments(closeOpenMarkdown(tailSource), tailSource);
  if (entry.headSegments.length === 0) return tailSegments;
  return [...entry.headSegments, ...tailSegments];
}
