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
  quoteDepth: number;
  quotePrefix: string;
  // extra list-item content indent to strip, on each subsequent quoted
  // line, before checking it against this fence - set when the fence
  // opened inside a list item nested under a blockquote (0 otherwise).
  childIndent: number;
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

function listItemIndent(line: string): number {
  return line.match(/^ */)?.[0].length ?? 0;
}

function listItemContentIndent(line: string): number | undefined {
  const match = /^( *)([-+*]|\d{1,9}[.)])([ \t]+|$)/.exec(line);
  if (!match) return undefined;
  const leadingIndent = match[1]?.length ?? 0;
  const markerWidth = match[2]?.length ?? 0;
  const followingWidth = match[3]?.length ?? 0;
  return leadingIndent + markerWidth + (followingWidth === 0 ? 1 : Math.min(followingWidth, 4));
}

function removeIndent(line: string, indent: number): string {
  let index = 0;
  while (index < indent && line.charAt(index) === " ") index += 1;
  return line.slice(index);
}

function isThematicBreak(line: string): boolean {
  return /^(?: {0,3}(?:\*[ \t]*){3,}| {0,3}(?:-[ \t]*){3,}| {0,3}(?:_[ \t]*){3,})$/.test(line);
}

function isSetextUnderline(line: string): boolean {
  return /^ {0,3}(?:=+|-+)[ \t]*$/.test(line);
}

type BlockquoteLine = { content: string; depth: number; prefix: string };
// Tracks an active list item nested under a blockquote, so its deindented
// child lines can be classified with the same block-boundary rules as any
// other line. `quoteDepth` is the depth of a nested blockquote CURRENTLY
// open within the child content (0 when the child is not inside one) - it
// carries across lines the same way `blockquoteDepth` does at the top
// level, instead of being recomputed from scratch on every line.
type BlockquoteListContainer = { contentIndent: number; depth: number; quoteDepth: number };

function isQuoteSpace(char: string | undefined): boolean {
  return char === " " || char === "\t";
}

function blockquoteLine(line: string): BlockquoteLine | undefined {
  let index = 0;
  while (index < 3 && line.charAt(index) === " ") index += 1;
  if (line.charAt(index) !== ">") return undefined;

  let depth = 0;
  while (line.charAt(index) === ">") {
    depth += 1;
    index += 1;
    if (isQuoteSpace(line.charAt(index))) index += 1;

    const nestedIndentStart = index;
    let nestedIndent = 0;
    while (nestedIndent < 3 && line.charAt(index) === " ") {
      nestedIndent += 1;
      index += 1;
    }
    if (line.charAt(index) !== ">") {
      index = nestedIndentStart;
      break;
    }
  }

  return { content: line.slice(index), depth, prefix: line.slice(0, index) };
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

// A fence opened by a child line of a quoted list item - `quoteDepth` and
// `quotePrefix` come from the OUTER blockquote line, not from this scan, so
// the caller fills them in.
type ChildFenceOpen = { char: "`" | "~"; length: number; childIndent: number };

// Scans one CONTENT line of a list item nested under a blockquote (already
// stripped of the `>` prefix, still carrying its list-item indentation) and
// classifies it with the same block-boundary rules used at the top level,
// updating `stack` and `container.quoteDepth` in place. `container` is the
// SAME object across calls for consecutive child lines, so its `quoteDepth`
// persists - that persistence, plus reclassifying every line rather than
// only the first, is what lets a nested quote or fence stay open (or die at
// a real boundary) across more than one child line.
function scanQuotedListChild(
  quotedContent: string,
  container: BlockquoteListContainer,
  stack: OpenMarker[],
  openFence: (open: ChildFenceOpen) => void,
): void {
  const childLine = removeIndent(quotedContent, container.contentIndent);
  const childQuote = blockquoteLine(childLine);

  if (childQuote !== undefined) {
    if (childQuote.depth !== container.quoteDepth) stack.length = 0;
    if (childQuote.content.trim() === "") {
      stack.length = 0;
    } else if (isAtxHeading(childQuote.content) || isThematicBreak(childQuote.content)) {
      stack.length = 0;
      scanInline(childQuote.content, stack);
    } else if (isSetextUnderline(childQuote.content)) {
      stack.length = 0;
    } else {
      const childFenceRun = fenceOpener(childQuote.content);
      if (childFenceRun !== undefined) {
        stack.length = 0;
        openFence({
          char: childFenceRun.charAt(0) as "`" | "~",
          length: childFenceRun.length,
          childIndent: container.contentIndent,
        });
      } else {
        scanInline(childQuote.content, stack);
      }
    }
    container.quoteDepth = childQuote.depth;
    return;
  }

  // No nested quote marker on THIS line. If the container was tracking an
  // open nested quote, it just ended (lazily continuing quoted content,
  // without its own `>`, is not something this scanner supports - it dies
  // at the boundary like any other unrecognized case).
  const wasInNestedQuote = container.quoteDepth !== 0;
  container.quoteDepth = 0;
  if (isIndentedCodeBlock(childLine)) {
    stack.length = 0; // nested indented code: literal, nothing to scan
    return;
  }
  const childFenceRun = fenceOpener(childLine);
  if (childFenceRun !== undefined) {
    stack.length = 0;
    openFence({
      char: childFenceRun.charAt(0) as "`" | "~",
      length: childFenceRun.length,
      childIndent: container.contentIndent,
    });
    return;
  }
  if (isThematicBreak(childLine) || isSetextUnderline(childLine)) {
    stack.length = 0; // no content to scan
    return;
  }
  if (isAtxHeading(childLine) || isListItem(childLine)) {
    // A heading or a new nested list item interrupts the parent paragraph
    // (same as at the top level) but still scans its own inline markers.
    // A nested list item also deepens the indent frame: its OWN content
    // indent is relative to this already-deindented childLine, so it adds
    // onto (not replaces) the container's accumulated contentIndent - the
    // next child line must deindent past both levels to reach its content.
    if (isListItem(childLine)) {
      const nestedContentIndent = listItemContentIndent(childLine);
      if (nestedContentIndent !== undefined) container.contentIndent += nestedContentIndent;
    }
    stack.length = 0;
    scanInline(childLine, stack);
    return;
  }
  // Plain paragraph-continuation text - the open stack carries through
  // unless a nested quote just ended, which is itself a boundary.
  if (wasInNestedQuote) stack.length = 0;
  scanInline(childLine, stack);
}

export function closeOpenMarkdown(source: string): string {
  const lines = source.split("\n");
  let fence: OpenFence | null = null;
  let stack: OpenMarker[] = [];
  let paragraph: "none" | "paragraph" | "blockquote" = "none";
  let blockquoteDepth = 0;
  let listContainerIndent: number | null = null;
  let blockquoteListContainer: BlockquoteListContainer | null = null;

  for (const line of lines) {
    const fenceRun = fenceOpener(line);
    if (fence) {
      // Inside a fence the only live syntax is a closing fence of the same
      // character at least as long as the opener.
      const quoted = fence.quoteDepth === 0 ? undefined : blockquoteLine(line);
      if (fence.quoteDepth > 0 && (quoted === undefined || quoted.depth < fence.quoteDepth)) {
        fence = null;
      } else {
        const content = fence.quoteDepth === 0 ? line : quoted?.depth === fence.quoteDepth ? quoted.content : undefined;
        const deindented =
          content === undefined || fence.childIndent === 0 ? content : removeIndent(content, fence.childIndent);
        const closingRun = deindented === undefined ? undefined : fenceCloser(deindented);
        if (closingRun !== undefined && closingRun.charAt(0) === fence.char && closingRun.length >= fence.length) {
          fence = null;
        }
        continue;
      }
    }
    if (fenceRun !== undefined) {
      fence = {
        char: fenceRun.charAt(0) as "`" | "~",
        length: fenceRun.length,
        quoteDepth: 0,
        quotePrefix: "",
        childIndent: 0,
      };
      stack = []; // a fence interrupts a paragraph; its open emphasis dies here
      paragraph = "none";
      blockquoteDepth = 0;
      listContainerIndent = null;
      blockquoteListContainer = null;
      continue;
    }
    if (line.trim() === "") {
      stack = []; // emphasis cannot span a block boundary
      paragraph = "none";
      blockquoteDepth = 0;
      listContainerIndent = null;
      blockquoteListContainer = null;
      continue;
    }
    if (isIndentedCodeBlock(line)) {
      if (blockquoteListContainer !== null && paragraph === "blockquote") {
        stack = [];
        paragraph = "none";
        blockquoteDepth = 0;
        blockquoteListContainer = null;
        continue;
      }
      if (listContainerIndent !== null) {
        stack = [];
        paragraph = "none";
        continue;
      }
      if (paragraph === "none") {
        stack = [];
        continue;
      }
      scanInline(line, stack);
      continue;
    }

    const quoted = blockquoteLine(line);
    if (quoted !== undefined) {
      listContainerIndent = null;
      const quoteContinuesParagraph = paragraph === "blockquote" && blockquoteDepth === quoted.depth;
      if (!quoteContinuesParagraph) {
        stack = [];
        blockquoteListContainer = null;
      }
      if (quoted.content.trim() === "") {
        stack = [];
        paragraph = "none";
        blockquoteDepth = quoted.depth;
        blockquoteListContainer = null;
        continue;
      }
      if (isIndentedCodeBlock(quoted.content)) {
        if (!quoteContinuesParagraph || blockquoteListContainer?.depth !== quoted.depth) {
          // No active list child to deindent into - this is either fresh
          // indented content abandoning the previous block, or ordinary
          // continuation text of a quoted paragraph.
          if (!quoteContinuesParagraph) {
            stack = [];
            paragraph = "none";
          } else {
            scanInline(quoted.content, stack);
            paragraph = "blockquote";
          }
          blockquoteListContainer = null;
        } else {
          scanQuotedListChild(quoted.content, blockquoteListContainer, stack, (openedFence) => {
            fence = { ...openedFence, quoteDepth: quoted.depth, quotePrefix: quoted.prefix };
          });
          paragraph = "blockquote";
        }
        blockquoteDepth = quoted.depth;
        continue;
      }
      const quotedFenceRun = fenceOpener(quoted.content);
      if (quotedFenceRun !== undefined) {
        fence = {
          char: quotedFenceRun.charAt(0) as "`" | "~",
          length: quotedFenceRun.length,
          quoteDepth: quoted.depth,
          quotePrefix: quoted.prefix,
          childIndent: 0,
        };
        stack = [];
        paragraph = "none";
        blockquoteDepth = quoted.depth;
        blockquoteListContainer = null;
        continue;
      }
      const quotedIsListItem = isListItem(quoted.content);
      if (isAtxHeading(quoted.content) || quotedIsListItem || isThematicBreak(quoted.content)) {
        stack = [];
        const contentIndent = listItemContentIndent(quoted.content);
        blockquoteListContainer =
          quotedIsListItem && contentIndent !== undefined
            ? { contentIndent, depth: quoted.depth, quoteDepth: 0 }
            : null;
      }
      if (isSetextUnderline(quoted.content)) {
        stack = [];
        paragraph = "none";
        blockquoteDepth = quoted.depth;
        blockquoteListContainer = null;
        continue;
      }
      scanInline(quoted.content, stack);
      paragraph = isAtxHeading(quoted.content) || isThematicBreak(quoted.content) ? "none" : "blockquote";
      blockquoteDepth = quoted.depth;
      continue;
    }

    const wasLazyBlockquote = paragraph === "blockquote";
    const lazyBlockquoteDepth = blockquoteDepth;
    paragraph = "none";
    blockquoteDepth = 0;
    if (isSetextUnderline(line) || isThematicBreak(line)) {
      stack = [];
      listContainerIndent = null;
      blockquoteListContainer = null;
      continue;
    }
    if (isAtxHeading(line)) {
      stack = [];
      listContainerIndent = null;
      blockquoteListContainer = null;
      scanInline(line, stack);
      continue;
    }
    if (isListItem(line)) {
      stack = [];
      scanInline(line, stack);
      paragraph = "paragraph";
      listContainerIndent = listItemIndent(line);
      blockquoteListContainer = null;
      continue;
    }
    scanInline(line, stack);
    if (wasLazyBlockquote) {
      paragraph = "blockquote";
      blockquoteDepth = lazyBlockquoteDepth;
    } else {
      paragraph = "paragraph";
      blockquoteListContainer = null;
    }
  }

  if (fence) return `${source}\n${fence.quotePrefix}${fence.char.repeat(fence.length)}`;
  let closers = "";
  for (let index = stack.length - 1; index >= 0; index -= 1) {
    const entry = stack[index];
    if (entry !== undefined) closers += entry.marker;
  }
  return source + closers;
}
