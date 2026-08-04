// closeOpenMarkdown takes a possibly-TRUNCATED markdown document (a stream
// still in flight) and returns it with every construct left open at the tail
// closed: unterminated fenced code blocks, inline code spans, and the inline
// emphasis markers `**`, `*`, and `~~`. The Markdown widget's `live` prop
// pipes the stream through this before parsing, so a thought or message
// renders its formatting WHILE it streams instead of showing literal `**`
// until the closer arrives.
//
// This is a heuristic scanner, not a CommonMark delimiter processor - the
// full algorithm needs the whole document, which is exactly what a live
// stream does not have yet. The contract is best-effort preview, never
// correctness of the settled render (the settled path always parses the
// true source, without this pass). The rules it implements:
//
//   - Fenced code blocks (``` or ~~~) toggle on fence lines; an open fence
//     at the tail closes with a matching fence on a new line. Nothing inside
//     a fence is scanned - it is code, not markdown.
//   - Inline code spans toggle on backtick runs of equal length; while a
//     span is open, every other marker is literal.
//   - `**`, `*`, and `~~` toggle with a simplified flanking rule: a run can
//     OPEN only when the character after it is not whitespace (so bullet
//     markers `* item` and math `2 * 3` never open), and can CLOSE only when
//     the character before it is not whitespace. Escaped markers (\*) are
//     literal. A closing run matches the most recent open of the SAME
//     marker, popping any markers opened inside it (CommonMark's crossover
//     rule, e.g. "**a *b** c*").
//   - Emphasis dies at block boundaries (blank lines, fence lines, and the
//     recognized heading/list/quote boundaries) - CommonMark emphasis cannot
//     span blocks, so an opener abandoned by a block boundary is never closed.
//   - `_` / `__` are deliberately NEVER auto-closed: intraword underscores
//     (snake_case) would false-positive constantly, and marked's own GFM
//     rules refuse intraword `_` emphasis for the same reason.
//
// Closers append in reverse-open order so nested emphasis nests correctly
// ("**a *b" + "***" parses as <strong>a <em>b</em></strong>).

interface OpenFence {
  char: "`" | "~";
  length: number;
}

interface OpenCodeSpan {
  kind: "code";
  marker: string; // the backtick run that opened it, e.g. "`" or "``"
}

interface OpenEmphasis {
  kind: "emphasis";
  marker: "**" | "*" | "~~";
}

type OpenMarker = OpenCodeSpan | OpenEmphasis;

const FENCE_OPENER = /^ {0,3}(`{3,}|~{3,})(.*)$/;
const FENCE_CLOSER = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;

function fenceOpener(line: string): string | undefined {
  const match = FENCE_OPENER.exec(line);
  if (!match) return undefined;
  const run = match[1];
  const info = match[2] ?? "";
  // CommonMark rejects a backtick fence whose info string contains a
  // backtick; treating it as ordinary text avoids inventing a code block.
  if (run?.startsWith("`") && info.includes("`")) return undefined;
  return run;
}

function fenceCloser(line: string): string | undefined {
  return FENCE_CLOSER.exec(line)?.[1];
}

function isIndentedCodeBlock(line: string): boolean {
  return line.startsWith("    ") || line.startsWith("\t");
}

function isAtxHeading(line: string): boolean {
  return /^ {0,3}#{1,6}(?:[ \t]+|$)/.test(line);
}

function isListItem(line: string): boolean {
  return /^ {0,3}(?:[-+*](?:[ \t]+|$)|\d{1,9}[.)](?:[ \t]+|$))/.test(line);
}

function isThematicBreak(line: string): boolean {
  return /^(?: {0,3}(?:\*[ \t]*){3,}| {0,3}(?:-[ \t]*){3,}| {0,3}(?:_[ \t]*){3,})$/.test(line);
}

function isSetextUnderline(line: string): boolean {
  return /^ {0,3}(?:=+|-+)[ \t]*$/.test(line);
}

type BlockquoteLine = { content: string; depth: number };

function blockquoteLine(line: string): BlockquoteLine | undefined {
  const prefix = /^ {0,3}(?:>[ \t]?)+/.exec(line)?.[0];
  if (prefix === undefined) return undefined;
  return { content: line.slice(prefix.length), depth: (prefix.match(/>/g) ?? []).length };
}

function isWhitespace(char: string | undefined): boolean {
  return char === undefined || /\s/.test(char);
}

// Scans one line of paragraph text, updating the open-marker stack in place.
// Only called for lines OUTSIDE a fenced code block.
function scanInline(line: string, stack: OpenMarker[]): void {
  let i = 0;
  const top = () => stack[stack.length - 1];

  while (i < line.length) {
    const char = line.charAt(i);

    // While a code span is open, nothing but its matching backtick run is
    // syntax - everything else (including backslashes and emphasis) is
    // literal code content.
    const active = top();
    if (active?.kind === "code") {
      if (char !== "`") {
        i += 1;
        continue;
      }
      let runLength = 0;
      while (line[i + runLength] === "`") runLength += 1;
      if (runLength === active.marker.length) stack.pop();
      i += runLength;
      continue;
    }

    if (char === "\\") {
      i += 2; // escaped character is literal, never a marker
      continue;
    }

    if (char === "`") {
      let runLength = 0;
      while (line[i + runLength] === "`") runLength += 1;
      stack.push({ kind: "code", marker: "`".repeat(runLength) });
      i += runLength;
      continue;
    }

    if (char === "*" || (char === "~" && line[i + 1] === "~")) {
      let runLength = 0;
      while (line[i + runLength] === char) runLength += 1;
      const previous = line[i - 1];
      const next = line[i + runLength];
      const canOpen = !isWhitespace(next);
      const canClose = !isWhitespace(previous);
      i += runLength;

      if (char === "~") {
        // GFM strikethrough is exactly "~~"; a longer tilde run's extra
        // tildes are literal, so only consume pairs.
        if (runLength % 2 === 1) runLength -= 1;
        if (runLength === 0) continue;
        matchEmphasis("~~", canOpen, canClose, stack);
        continue;
      }

      // Split a run of '*' into "**" pairs plus a trailing "*" - "***bold"
      // opens strong THEN em, and closes the same way.
      let remaining = runLength;
      while (remaining > 0) {
        const marker = remaining >= 2 ? "**" : "*";
        remaining -= marker.length;
        matchEmphasis(marker, canOpen, canClose, stack);
      }
      continue;
    }

    i += 1;
  }
}

// A run that can close matches the most recent open of the SAME marker,
// popping anything opened inside it (CommonMark's crossover nesting); a run
// that cannot close but can open pushes a new open. A run that can do
// neither (whitespace-flanked on both sides) is literal and ignored.
function matchEmphasis(marker: "**" | "*" | "~~", canOpen: boolean, canClose: boolean, stack: OpenMarker[]): void {
  if (canClose) {
    for (let index = stack.length - 1; index >= 0; index -= 1) {
      const entry = stack[index];
      if (entry === undefined || entry.kind === "code") break; // code spans do not nest emphasis
      if (entry.marker === marker) {
        stack.length = index;
        return;
      }
    }
  }
  if (canOpen) stack.push({ kind: "emphasis", marker });
}

export function closeOpenMarkdown(source: string): string {
  const lines = source.split("\n");
  let fence: OpenFence | null = null;
  let stack: OpenMarker[] = [];
  let paragraph: "none" | "paragraph" | "blockquote" = "none";
  let blockquoteDepth = 0;

  for (const line of lines) {
    const fenceRun = fenceOpener(line);
    if (fence) {
      // Inside a fence the only live syntax is a closing fence of the same
      // character at least as long as the opener.
      const closingRun = fenceCloser(line);
      if (closingRun !== undefined && closingRun.charAt(0) === fence.char && closingRun.length >= fence.length) {
        fence = null;
      }
      continue;
    }
    if (fenceRun !== undefined) {
      fence = { char: fenceRun.charAt(0) as "`" | "~", length: fenceRun.length };
      stack = []; // a fence interrupts a paragraph; its open emphasis dies here
      paragraph = "none";
      blockquoteDepth = 0;
      continue;
    }
    if (line.trim() === "") {
      stack = []; // emphasis cannot span a block boundary
      paragraph = "none";
      blockquoteDepth = 0;
      continue;
    }
    if (isIndentedCodeBlock(line)) {
      if (paragraph === "none") {
        stack = [];
        continue;
      }
      scanInline(line, stack);
      continue;
    }

    const quoted = blockquoteLine(line);
    if (quoted !== undefined) {
      if (paragraph !== "blockquote" || blockquoteDepth !== quoted.depth) stack = [];
      if (quoted.content.trim() === "") {
        stack = [];
        paragraph = "none";
        blockquoteDepth = quoted.depth;
        continue;
      }
      if (isAtxHeading(quoted.content) || isListItem(quoted.content) || isThematicBreak(quoted.content)) {
        stack = [];
      }
      if (isSetextUnderline(quoted.content)) {
        stack = [];
        paragraph = "none";
        blockquoteDepth = quoted.depth;
        continue;
      }
      scanInline(quoted.content, stack);
      paragraph = isAtxHeading(quoted.content) || isThematicBreak(quoted.content) ? "none" : "blockquote";
      blockquoteDepth = quoted.depth;
      continue;
    }

    paragraph = "none";
    blockquoteDepth = 0;
    if (isSetextUnderline(line) || isThematicBreak(line)) {
      stack = [];
      continue;
    }
    if (isAtxHeading(line)) {
      stack = [];
      scanInline(line, stack);
      continue;
    }
    if (isListItem(line)) {
      stack = [];
      scanInline(line, stack);
      paragraph = "paragraph";
      continue;
    }
    scanInline(line, stack);
    paragraph = "paragraph";
  }

  if (fence) return `${source}\n${fence.char.repeat(fence.length)}`;
  let closers = "";
  for (let index = stack.length - 1; index >= 0; index -= 1) {
    const entry = stack[index];
    if (entry !== undefined) closers += entry.marker;
  }
  return source + closers;
}
