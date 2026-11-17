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
import { fenceTokenTerminated, isMermaidCodeToken, MERMAID_FENCE, mermaidText } from "@evener/appwire-client";
import type { Token } from "marked";
import { markdownLexer } from "./lexer";
import { closeOpenMarkdown } from "./streaming";

// A markdown segment is a token slice. `window`, when set, is the part of that
// run still streaming: `tokens` then holds the settled prefix and `window` the
// bounded live window after it, so the two parse into one div (issue #3208).
export type MarkdownSegment =
  | { kind: "markdown"; tokens: Token[]; window?: string }
  | { kind: "mermaid"; text: string };

export function messageMayContainMermaid(source: string): boolean {
  return MERMAID_FENCE.test(source);
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

// --- live re-parse window (shared by the single-root and segmented paths) ---
// The windowed throttle here is the same pure, string-only split the single-root
// path has always used, lifted into this module so the segmented live path can
// reuse it for its streaming prose tail (issue #3208). It carries no renderer
// state, so it stays unit-testable without a DOM.

// Live re-parse throttle: a full marked + DOMPurify pass per streamed token
// is O(n^2) over a long stream. Below this source length every live render
// takes the same full-parse path as before (all existing live tests stay on
// it); past it, only the tail window is re-parsed per render while the
// settled head is served from a prefix-keyed cache. Grown past the window
// mid-stream, the head cache fills once per new head text and then hits.
// A tail that would extend a list still open in the head falls back to the
// full parse (the one shape whose split changes the HTML); every other block
// shape - fences, headings, quotes, tables, hr, and a fence or emphasis still
// open at the stream tail - takes the windowed path, with the open tail
// closed before it is parsed just as the whole-source path closes it.
export const LIVE_WINDOWED_MIN_LENGTH = 2000;
// The tail window re-parsed on every live render past the threshold above -
// sized to cover the in-progress paragraph still being written without
// re-parsing the whole document. Exported so a test can place a frame range at
// the boundary this value defines instead of restating the number.
export const LIVE_TAIL_WINDOW = 1000;

// The only block construct that can span the blank-line split point (and so
// tokenize differently as a standalone tail than as the tail of the whole):
// a LIST continues across a blank line when the next item repeats the open
// list's marker, so one list becomes two and the HTML changes. Every other
// CommonMark block is closed by a blank line - a paragraph, heading, fence,
// blockquote, table and hr all end there, so a standalone tail parses the
// same as it does inside the whole. Measured, not assumed: a corpus of head
// shapes x tail shapes (fences open and closed, all four list markers,
// quotes, tables, html blocks, open emphasis/code, definitions) reports no
// split whose incremental HTML differs from the whole-document parse once
// this rule is applied.
//
// A list marker's FAMILY is its bullet character (`-`, `+`, `*`) or its
// ordered delimiter (`.`, `)`) - not the number: marked merges `1.` into
// `2.` (verified), so the number cannot be part of the test, while `-` and
// `+` stay two lists and `1.` and `1)` stay two lists.
//
// The marker may be followed by a space, a tab, or end of line. End of line
// allows an optional \r: firstNonBlankLine splits the raw tail on \n, so a
// CRLF source hands these readers a line ending in \r, and `-` with no content
// (`-\r\n`, `2.\r\n`) is still an empty same-family item that marked joins to
// the head's list.
const BULLET_MARKER = /^ {0,3}([-+*])(?:[ \t]|\r?$)/;
const ORDERED_MARKER = /^ {0,3}\d{1,9}([.)])(?:[ \t]|\r?$)/;

function listMarkerFamily(line: string): string | null {
  const bullet = BULLET_MARKER.exec(line);
  if (bullet !== null) return bullet[1] ?? null;
  const ordered = ORDERED_MARKER.exec(line);
  if (ordered !== null) return ordered[1] ?? null;
  return null;
}

function firstNonBlankLine(text: string): string {
  for (const line of text.split("\n")) {
    if (line.trim() !== "") return line;
  }
  return "";
}

// A sentinel that can never equal a real marker family: an unreadable head
// (a lexer throw, or a first list item whose marker line does not parse) has
// a family nobody can compare, and listContinuesAcrossSplit declines on it.
export const LIST_FAMILY_UNKNOWN = "\u0000unknown";

// The marker family of the head's LAST top-level list, or null when the head
// does not end in a list. A head ending in a list is the one shape where the
// tail can extend an already-open block, so the caller compares families.
// Reads the token array the caller already lexed for the definition check - a
// head-change frame is exactly the O(n) frame the verdict cache exists for,
// and a second lex of the same settled text would pay that cost twice.
function lastListFamily(tokens: Token[]): string | null {
  for (let index = tokens.length - 1; index >= 0; index -= 1) {
    const token = tokens[index];
    if (token === undefined || token.type === "space") continue;
    if (token.type !== "list") return null;
    const firstItemRaw = (token as { items?: Array<{ raw?: string }> }).items?.[0]?.raw ?? "";
    return listMarkerFamily(firstNonBlankLine(firstItemRaw)) ?? LIST_FAMILY_UNKNOWN;
  }
  return null;
}

// Head-side hazards for the split: a link definition or table delimiter row
// in the head can resolve structure in the tail (a `[label]` use, table
// rows) that a standalone tail parse would leave literal, so any hit falls
// back to the full parse. The link-definition arm also matches `>`-quoted
// and list-item-nested definitions (valid anywhere a block can nest, and
// registered globally by marked): without it a head-side definition would
// leave the tail's `[label]` use literal under the windowed parse. Verified
// against the shared lexer: top-level, blockquote-nested, and list-nested
// definitions all trip this pattern while definition-free heads do not.
// Shapes the pattern cannot cover (an escaped label like `[foo\]bar]: /url`
// has no `[...]:` span for it to match; a definition indented as a list-item
// continuation sits past its 0-3-space allowance) are caught by the
// shared-lexer check in the gate below instead.
const HEAD_SPLIT_HAZARD =
  /^(?: {0,3}>[ \t]?)+ {0,3}\[[^\]\n]+\]:|^ {0,3}(?: {0,3}>[ \t]?)* {0,3}(?:[-+*]|\d{1,9}[.)])[ \t]+.*\[[^\]\n]+\]:|^ {0,3}\[[^\]\n]+\]:|^\s*:?-+:?(?:\s*\|\s*:?-+:?)+\s*\r?$/m;

// HTML blocks (CommonMark types 1-6: pre/script/style/textarea, comments,
// processing instructions, declarations, CDATA, block tags) run past blank
// lines until their terminator, so a split inside one severs it. A block start
// in the head, or a block ender in the tail (whose opener may sit anywhere
// upstream, including before the head), falls back to the full parse.
const HTML_BLOCK_START =
  /^ {0,3}(?:<(?:pre|script|style|textarea)(?:[\s>]|$)|<!--|<\?|<![A-Za-z]|<!\[CDATA\[|<\/?(?:address|article|aside|base|basefont|blockquote|body|caption|center|col|colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption|figure|footer|form|frame|frameset|h[1-6]|head|header|hr|html|iframe|legend|li|link|main|menu|menuitem|meta|nav|noframes|ol|optgroup|option|p|param|section|source|summary|table|tbody|td|tfoot|th|thead|title|tr|track|ul)(?:[\s>/]|$))/im;
const HTML_BLOCK_END = /<\/(?:pre|script|style|textarea)>|-->|\?>|\]\]>/i;

// Splits a long live source into a settled head (ending on a blank line) and
// the streaming tail after it, or null when there is no blank-line boundary
// whose tail is at most LIVE_TAIL_WINDOW (one very long paragraph still
// streaming). The boundary is the FIRST at/after the window edge, so the tail
// holds at most the window - taking the last boundary at/before the edge
// instead would accept a tail holding the entire remainder of a long
// paragraph. CRLF sources carry no "\n\n" span (a \r sits between the \n
// pair), so both line endings are searched and the earliest boundary wins.
// Leading blank lines of the tail are skipped - insignificant in both paths;
// a tail of nothing but blanks likewise declines the windowed path.
function splitLiveSource(source: string): { head: string; tail: string } | null {
  const edge = source.length - LIVE_TAIL_WINDOW;
  const lf = source.indexOf("\n\n", edge);
  const crlf = source.indexOf("\r\n\r\n", edge);
  const blank = lf === -1 ? crlf : crlf === -1 ? lf : Math.min(lf, crlf);
  if (blank === -1) return null;
  const separator = source.startsWith("\r\n\r\n", blank) ? 4 : 2;
  let tailStart = blank + separator;
  while (source.charAt(tailStart) === "\n" || source.charAt(tailStart) === "\r") tailStart += 1;
  if (tailStart >= source.length) return null;
  return { head: source.slice(0, blank + separator), tail: source.slice(tailStart) };
}

// The head-side gate verdicts cached per exact head text: every one of them
// (balance scan, hazard patterns, shared-lexer definition check) is a pure
// function of the head string, so a verdict computed for a head stays valid
// until the head text itself changes. Re-running them on every live render
// would reintroduce the O(n^2) the head cache exists to avoid. (The tail is
// window-bounded, so it lexes/scans per render with no cache.)
export interface HeadGateVerdict {
  headSource: string;
  balanced: boolean;
  hazard: boolean;
  htmlStart: boolean;
  hasDef: boolean;
  // The marker family of the head's last top-level list (null when the head
  // does not end in one). Read from the token stream, so it is computed here
  // with the rest of the verdict rather than per render: a per-render lex of
  // the settled head would reintroduce the O(n^2) this cache exists to avoid.
  listFamily: string | null;
}

function headGateVerdict(head: string, cache: { current: HeadGateVerdict | null }): HeadGateVerdict {
  const hit = cache.current;
  if (hit !== null && hit.headSource === head) return hit;
  // The regex gates below carry no /g flag, so .test is stateless and the
  // cached verdict cannot depend on lastIndex carryover.
  // ONE lex of the head feeds both the definition check and the list-family
  // read. A lexer throw fails closed on both arms: `hasDef` declines the
  // windowed path, and the family sentinel declines it a second time.
  let tokens: Token[] | null = null;
  try {
    tokens = markdownLexer.lexer(head);
  } catch {
    tokens = null;
  }
  const verdict: HeadGateVerdict = {
    headSource: head,
    balanced: closeOpenMarkdown(head) === head,
    hazard: HEAD_SPLIT_HAZARD.test(head),
    htmlStart: HTML_BLOCK_START.test(head),
    hasDef: tokens === null ? true : tokensContainDef(tokens),
    listFamily: tokens === null ? LIST_FAMILY_UNKNOWN : lastListFamily(tokens),
  };
  cache.current = verdict;
  return verdict;
}

// The tail's first non-blank line, when indented, could still belong to a
// list item open in the head (indented continuation joins it across the
// blank line), so it declines the windowed path. Non-indented content can
// never rejoin a head block across a blank line.
function tailStartsIndented(tail: string): boolean {
  const lines = tail.split("\n");
  for (const line of lines) {
    if (line.trim() === "") continue;
    return line.charAt(0) === " " || line.charAt(0) === "\t";
  }
  return false;
}

// True when the tail's first block would EXTEND a list still open at the end
// of the head. A blank line does not close a list - the next item repeats the
// marker and joins it - so parsing head and tail separately would render one
// list as two: the one shape where the split changes the HTML. Every other
// block ends at the blank line, so the halves parse the same apart as
// together (measured over a randomized head x tail corpus; see the comment
// above BULLET_MARKER).
//
// The head side comes from the cached verdict: it is read from the token
// stream, not a line scan, because only the token stream knows whether the
// head's last block really is a list (a `-` line inside a fence or a table
// cell is not one) - and it must not be re-lexed per render. The tail side is
// a line scan: the tail is window-bounded, so it costs the same per render as
// the regex gates beside it.
function listContinuesAcrossSplit(verdict: HeadGateVerdict, tail: string): boolean {
  if (verdict.listFamily === null) return false;
  // The sentinel equals no real family, so it would fall through to `false`
  // and ALLOW an unverified split. Decline instead, as the comments above
  // promise, so an unreadable head never takes the windowed path.
  if (verdict.listFamily === LIST_FAMILY_UNKNOWN) return true;
  return verdict.listFamily === listMarkerFamily(firstNonBlankLine(tail));
}

// A sound head/tail window split: the settled prefix and its streaming window,
// each self-contained so they lex and parse the same apart as together. The
// caller supplies the per-head verdict cache, so a settled prefix pays its
// scans once per distinct text. Null means "decline the windowed path" - the
// caller falls back to the full parse, exactly the pre-throttle behavior.
export interface LiveWindow {
  head: string;
  tail: string;
  closedTail: string;
}

export function splitLiveWindow(source: string, gateCache: { current: HeadGateVerdict | null }): LiveWindow | null {
  const split = splitLiveSource(source);
  if (split === null) return null;
  const verdict = headGateVerdict(split.head, gateCache);
  const closedTail = closeOpenMarkdown(split.tail);
  if (
    !verdict.balanced ||
    verdict.hazard ||
    verdict.hasDef ||
    containsLinkDefinition(split.tail) ||
    verdict.htmlStart ||
    HTML_BLOCK_END.test(split.tail) ||
    tailStartsIndented(split.tail) ||
    listContinuesAcrossSplit(verdict, split.tail)
  ) {
    return null;
  }
  return { head: split.head, tail: split.tail, closedTail };
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
  // The settled prose tail after the frozen head (issue #3208): `tailKey` is its
  // exact text, `tailTokens` its token slice. Served by identity while only the
  // bounded `window` after it keeps streaming, so the settled prose is lexed
  // once per distinct prefix instead of once per token.
  tailKey: string;
  tailTokens: Token[];
  // Same per-head verdict discipline as the widget's head-gate cache, for the
  // settled tail prefix's gate.
  tailGate: { current: HeadGateVerdict | null };
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
//
// The tail itself is windowed once it is long enough (issue #3208): a settled
// prose prefix after the frozen head is lexed once and served by identity, and
// only the bounded LIVE_TAIL_WINDOW after it re-lexes per render - the same
// splitLiveWindow the single-root path uses. That split declines when a link
// definition sits in the head or the window, or when the tail's first line
// would extend a list still open at the end of the head. A mermaid fence in
// the window also takes the whole-source lex: only the frozen head is checked
// for non-markdown segments, so a diagram sitting in the window would be
// parsed as markdown and flip to a code block until it settled. A tail too
// short to window, or one whose split is declined, keeps one slice exactly as
// before.
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
      tailKey: "",
      tailTokens: [],
      tailGate: { current: null },
    };
    cache.current = entry;
  }

  const tailSource = realSource.slice(entry.headSource.length);
  // A head-side definition resolves only under a whole-source lex (same gate
  // the windowed path forces); take it, demote rule and all, rather than split
  // head and tail separately. The tail's own definitions are caught by the
  // window gate below (or, for a short tail, by the whole-tail check).
  if (entry.headHasDef) {
    return splitMarkdownSegments(closeOpenMarkdown(realSource), realSource);
  }
  if (tailSource === "") {
    return entry.headSegments;
  }
  if (tailSource.length > LIVE_WINDOWED_MIN_LENGTH) {
    const split = splitLiveWindow(tailSource, entry.tailGate);
    if (split === null || messageMayContainMermaid(split.tail)) {
      // A declined split (a definition, an HTML block, or a list continuing
      // across the boundary), or a mermaid fence inside the window, which
      // would render as a code block while live: whole-source, exactly the
      // pre-throttle fallback.
      return splitMarkdownSegments(closeOpenMarkdown(realSource), realSource);
    }
    if (entry.tailKey !== split.head) {
      const prefixSegments = splitMarkdownSegments(split.head, split.head);
      // The frozen head is empty whenever it was rejected (a nested
      // list-continuation fence), so the tail is the whole source and the
      // settled prefix can still carry an earlier top-level closed diagram.
      // Windowing keeps only markdown tokens; decline rather than drop it.
      if (prefixSegments.some((segment) => segment.kind !== "markdown")) {
        return splitMarkdownSegments(closeOpenMarkdown(realSource), realSource);
      }
      entry.tailKey = split.head;
      entry.tailTokens = prefixSegments.flatMap((segment) => (segment.kind === "markdown" ? segment.tokens : []));
    }
    // A fresh segment object each render (never cached whole) so MarkdownSlice
    // re-renders and picks up the grown window while reusing the settled tokens.
    const windowSegment: MarkdownSegment = { kind: "markdown", tokens: entry.tailTokens, window: split.tail };
    if (entry.headSegments.length === 0) return [windowSegment];
    return [...entry.headSegments, windowSegment];
  }
  if (containsLinkDefinition(tailSource)) {
    return splitMarkdownSegments(closeOpenMarkdown(realSource), realSource);
  }

  const tailSegments = splitMarkdownSegments(closeOpenMarkdown(tailSource), tailSource);
  if (entry.headSegments.length === 0) return tailSegments;
  return [...entry.headSegments, ...tailSegments];
}
