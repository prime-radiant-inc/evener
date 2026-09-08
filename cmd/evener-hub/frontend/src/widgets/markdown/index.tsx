import DOMPurify from "dompurify";
import { Marked, type RendererObject, type Token, type Tokens } from "marked";
import { useMemo, useRef } from "react";
import codeblockStyles from "../codeblock/codeblock.module.css";
import { requireClass } from "../internal/requireClass";
import styles from "./markdown.module.css";
import { closeOpenMarkdown } from "./streaming";

export interface MarkdownProps {
  source: string;
  /** True while `source` is a possibly-truncated stream still in flight:
   * constructs left open at the tail (unterminated `**`/`*`/`~~`, inline
   * code, fenced code blocks) are closed before parsing via
   * closeOpenMarkdown, so formatting renders WHILE streaming instead of
   * showing literal marker source. Settled renders must NOT pass this - a
   * genuinely unterminated final source stays honestly literal. */
  live?: boolean;
}

const CLASS = {
  root: requireClass(styles.root, "markdown.module.css", "root"),
  inlineCode: requireClass(styles.inlineCode, "markdown.module.css", "inlineCode"),
};

const CODEBLOCK_CLASS = {
  root: requireClass(codeblockStyles.root, "codeblock.module.css", "root"),
  header: requireClass(codeblockStyles.header, "codeblock.module.css", "header"),
  language: requireClass(codeblockStyles.language, "codeblock.module.css", "language"),
  pre: requireClass(codeblockStyles.pre, "codeblock.module.css", "pre"),
  code: requireClass(codeblockStyles.code, "codeblock.module.css", "code"),
};

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

// Overrides the four token renderers that need to differ from marked's
// HTML-string defaults; everything else (headings, paragraphs, lists,
// blockquotes, emphasis, hr) uses marked's own safe defaults and is styled
// by plain-tag descendant selectors scoped under .root in
// markdown.module.css (CSS Modules only renames `.class`/`#id` selectors,
// so a bare `h1`/`p`/`a` inside a `.root h1 { }` rule is untouched - this
// is the standard way to style dangerouslySetInnerHTML content without
// hand-building markup for every token type).
const renderer: RendererObject = {
  // Renders through CodeBlock's own stylesheet so a fenced block looks
  // identical to the standalone CodeBlock widget. It's static HTML, not a
  // mounted CodeBlock instance - dangerouslySetInnerHTML can't host live
  // React children - so there's no copy button here; that's a deliberate,
  // documented scope cut, the same way CodeBlock itself skips a
  // highlighter this wave.
  code({ text, lang }: Tokens.Code) {
    const language = lang?.trim().split(/\s+/)[0];
    const header = language
      ? `<div class="${CODEBLOCK_CLASS.header}"><span class="${CODEBLOCK_CLASS.language}">${escapeHtml(language)}</span></div>`
      : "";
    return (
      `<div class="${CODEBLOCK_CLASS.root}">${header}` +
      `<pre class="${CODEBLOCK_CLASS.pre}"><code class="${CODEBLOCK_CLASS.code}">${escapeHtml(text)}</code></pre></div>`
    );
  },
  // Inline `` `code` `` spans get their own class rather than reusing
  // CodeBlock's block-level classes (which are sized/bordered for a
  // multi-line block, not a word inline in a sentence).
  codespan({ text }: Tokens.Codespan) {
    return `<code class="${CLASS.inlineCode}">${escapeHtml(text)}</code>`;
  },
  // Every markdown-syntax link (including GFM autolinks) opens in a new
  // tab without granting it `window.opener` access.
  link({ href, title, tokens }: Tokens.Link) {
    const label = this.parser.parseInline(tokens);
    const titleAttr = title ? ` title="${escapeHtml(title)}"` : "";
    return `<a href="${escapeHtml(href)}"${titleAttr} target="_blank" rel="noopener noreferrer">${label}</a>`;
  },
  // NO RAW HTML PASSTHROUGH: literal HTML typed directly in the markdown
  // source (block or inline - both token types land here) is shown as
  // visible, escaped text instead of being interpreted as markup. This is
  // the actual mechanism behind that rule; DOMPurify below is a second,
  // independent layer (it also neutralizes dangerous href/src schemes on
  // markdown-SYNTAX links/images, which this override doesn't touch).
  html({ text }: Tokens.HTML | Tokens.Tag) {
    return escapeHtml(text);
  },
};

// A dedicated instance (not the module-level default export's global
// `marked.use(...)`) so this widget's renderer never leaks into any other
// consumer of the `marked` package that might get added to this app later.
const md = new Marked({ gfm: true, renderer });

// The app's shared default-options lexer: the same GFM tokenizer the widget
// parses with, minus the widget's custom renderer (lexer output never renders
// - renderer choice cannot change the token stream). Lexer-only consumers
// import this instead of constructing their own `new Marked({ gfm: true })`,
// so the tokenizer is initialized once. Kept in this module rather than a
// new one so the single constructible-`marked` comment above stays true in
// exactly one place; this instance carries no renderer, so it cannot leak
// widget markup anywhere. Pure `marked` (no DOMPurify, no CSS, no React), so
// node-environment suites can import it freely.
export const markdownLexer = new Marked({ gfm: true });

// Sanitizes the OUTPUT html, not the markdown source - this is DOMPurify's
// documented pairing with marked (marked itself performs no sanitization).
// The allowlist is every tag/attribute marked's defaults or the overrides
// above can produce, and nothing else. GFM pipe tables are supported: marked
// tokenizes them (gfm: true) into <table>/<thead>/<tbody>/<tr>/<th>/<td> with
// an align="left|center|right" attribute per aligned column, so all six
// table tags plus `align` are allowlisted. The only path to a real <table>
// is that tokenizer: the html() override below escapes all authored raw
// HTML to text before DOMPurify ever sees it, so no markdown-typed <table>
// can sneak through. Still absent: img - images tokenize fine but aren't
// allowlisted yet (no consumer needs them this wave). Disallowed elements
// don't uniformly "degrade to their text content" though - verified
// directly against this exact config: a bare <img> has no text content to
// keep, so it just vanishes. Whichever way a given element behaves,
// nothing dangerous survives - they fail safe, just not via one single
// mechanism.
const SANITIZE_CONFIG = {
  ALLOWED_TAGS: [
    "p",
    "br",
    "hr",
    "h1",
    "h2",
    "h3",
    "h4",
    "h5",
    "h6",
    "strong",
    "em",
    "del",
    "code",
    "pre",
    "a",
    "ul",
    "ol",
    "li",
    "blockquote",
    "div",
    "span",
    "table",
    "thead",
    "tbody",
    "tr",
    "th",
    "td",
  ],
  // "class" is allowlisted globally (not scoped to specific tags) but is
  // currently inert against anything a markdown AUTHOR controls: the only
  // class attributes this pipeline ever produces are the ones the code
  // above writes itself (CODEBLOCK_CLASS/CLASS.inlineCode) - html() above
  // escapes all raw source HTML to text before DOMPurify ever sees it, so
  // there's no path today for a class value to originate from user input.
  // `align` is the legacy HTML attribute marked emits for GFM column
  // alignment (:---/:---:/---:); it carries no script surface and is
  // harmless on table cells. If a future change widens ALLOWED_TAGS to let
  // raw source HTML through in some form, that safety stops being
  // automatic - re-check whether "class"/"align" should stay this
  // permissive at that point.
  ALLOWED_ATTR: ["href", "title", "target", "rel", "class", "align"],
};

// Live re-parse throttle: a full marked + DOMPurify pass per streamed token
// is O(n^2) over a long stream. Below this source length every live render
// takes the same full-parse path as before (all existing live tests stay on
// it); past it, only the tail window is re-parsed per render while the
// settled head is served from a prefix-keyed cache. Grown past the window
// mid-stream, the head cache fills once per new head text and then hits.
// Any stream whose tail carries block structure falls back to the full parse
// (slower, exactly today's behavior - correctness first); only a
// paragraphs-only tail takes the windowed path.
const LIVE_WINDOWED_MIN_LENGTH = 2000;
// The tail window re-parsed on every live render past the threshold above -
// sized to cover the in-progress paragraph still being written without
// re-parsing the whole document.
const LIVE_TAIL_WINDOW = 1000;

// Block constructs that can tokenize differently as a standalone document
// than as the tail of a larger one, so the windowed live path below must not
// engage while the tail window contains them: a list continues over blank
// lines (one list vs two changes the HTML), a fenced block or table can span
// the split point, and a link definition resolves references anywhere. Any
// hit falls back to the full parse. Paragraph-only tails are the sound case:
// no other CommonMark block spans a blank line, and raw HTML needs no gate -
// the html() override escapes it to text identically in both paths.
const TAIL_BLOCK_MARKER =
  /^ {0,3}#{1,6}(?:[ \t]+|\r?$)|^ {0,3}(?:=+|-+)[ \t]*\r?$|^ {0,3}(?:[-+*](?:[ \t]+|\r?$)|\d{1,9}[.)](?:[ \t]+|\r?$))|^ {0,3}(`{3,}|~{3,})|^ {0,3}>|^ {0,3}(?:\*[ \t]*){3,}\r?$|^ {0,3}(?:-[ \t]*){3,}\r?$|^ {0,3}(?:_[ \t]*){3,}\r?$|^\s*\||^\s*:?-+:?(?:\s*\|\s*:?-+:?)+\s*\r?$|^ {0,3}\[[^\]\n]+\]:|<!\[CDATA\[|\]\]>/m;

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
// lines until their terminator, so a split inside one severs it with no
// markdown marker in the tail for TAIL_BLOCK_MARKER to trip. A block start in
// the head, or a block ender in the tail (whose opener may sit anywhere
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
// paragraph. Leading blank lines of the tail are skipped - insignificant in
// both paths; a tail of nothing but blanks likewise declines the windowed
// path.
function splitLiveSource(source: string): { head: string; tail: string } | null {
  const edge = source.length - LIVE_TAIL_WINDOW;
  const blank = source.indexOf("\n\n", edge);
  if (blank === -1) return null;
  let tailStart = blank + 2;
  while (source.charAt(tailStart) === "\n") tailStart += 1;
  if (tailStart >= source.length) return null;
  return { head: source.slice(0, blank + 2), tail: source.slice(tailStart) };
}

// Exact link-definition detection through the shared lexer: a definition on
// either side of the split registers globally with marked and resolves
// `[label]` uses anywhere in the whole - including across the split - that a
// standalone tail parse would leave literal, so any `def` token, top level
// or nested in a blockquote/list/table, falls back to the full parse. A
// lexer failure fails closed to the full parse as well.
function containsLinkDefinition(source: string): boolean {
  let tokens: Token[];
  try {
    tokens = markdownLexer.lexer(source);
  } catch {
    return true;
  }
  return tokensContainDef(tokens);
}

function tokensContainDef(tokens: Token[]): boolean {
  for (const token of tokens) {
    if (token.type === "def") return true;
    if ("tokens" in token && tokensContainDef(token.tokens ?? [])) return true;
    if ("items" in token) {
      for (const item of token.items) {
        if (tokensContainDef(item.tokens)) return true;
      }
    }
    if (token.type === "table") {
      for (const cell of [...token.header, ...token.rows.flat()]) {
        if (tokensContainDef(cell.tokens)) return true;
      }
    }
  }
  return false;
}

// The head-side lexer check cached per exact head text: re-lexing the head
// on every live render would reintroduce the O(n^2) the head HTML cache
// exists to avoid, so a head that lexes clean stays clean-keyed until its
// text changes. (The tail is window-bounded, so it lexes per render with no
// cache.)
function headHasLinkDefinition(
  head: string,
  cache: { current: { headSource: string; headHasDef: boolean } | null },
): boolean {
  const hit = cache.current;
  if (hit !== null && hit.headSource === head) return hit.headHasDef;
  const headHasDef = containsLinkDefinition(head);
  cache.current = { headSource: head, headHasDef };
  return headHasDef;
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

/**
 * Renders a markdown source string: marked tokenizes and generates HTML,
 * DOMPurify sanitizes it against a fixed allowlist, and the result is set
 * as innerHTML. Fenced code blocks render through CodeBlock's own
 * stylesheet; links always open in a new tab without opener access; raw
 * HTML in the source is never interpreted as markup. With `live`, the
 * source is treated as a truncated stream and its open constructs are
 * closed before parsing (see streaming.ts).
 */
export function Markdown({ source, live = false }: MarkdownProps) {
  // Live streams re-render per streamed token, and each render re-parses the
  // whole message (marked + DOMPurify) - O(n^2) over a long stream. Past the
  // window above, the settled head is served from a cache keyed on its own
  // exact prefix text (a changed head is a cache miss and re-parses, never
  // stale output), while only the tail window is re-parsed per render with
  // the live auto-close, so formatting still previews while streaming. The
  // windowed path engages ONLY when the split is sound (see the gates
  // below); anything else takes the full-parse fallback, which is exactly
  // the pre-throttle behavior for every input. Settled renders (live=false)
  // always take the settled full-parse path unchanged, so the final HTML is
  // byte-identical with or without this throttle.
  const headCacheRef = useRef<{ headSource: string; headHtml: string } | null>(null);
  // Backs headHasLinkDefinition below - same cache discipline as headCacheRef:
  // keyed on the head's own exact text, never stale, misses re-lex once.
  const headDefCacheRef = useRef<{ headSource: string; headHasDef: boolean } | null>(null);
  const html = useMemo(() => {
    if (!live || source.length <= LIVE_WINDOWED_MIN_LENGTH) {
      const rawHtml = md.parse(live ? closeOpenMarkdown(source) : source, { async: false });
      return DOMPurify.sanitize(rawHtml, SANITIZE_CONFIG);
    }
    // The windowed path is sound only when NOTHING spans the split. Its
    // keystone is whole-source balance: closeOpenMarkdown is identity exactly
    // when the live input has no construct left open, so no fence, code span,
    // or emphasis can still be awaiting its closer in the tail - had one been
    // severed, the whole would not be identity. The head must be closed at
    // the boundary too (a fence opened in the head and closed in the tail is
    // whole-balanced but still spans the split - the tail's fence run trips
    // TAIL_BLOCK_MARKER for that shape, and this check backstops it). The
    // only cross-boundary structure a balanced whole can otherwise carry is
    // forward references (link definitions, table delimiters) and blank-line-
    // spanning HTML blocks, excluded by HEAD_SPLIT_HAZARD and the HTML gates;
    // the tail must be paragraphs-only on fresh content (TAIL_BLOCK_MARKER,
    // tailStartsIndented). Anything else takes the full-parse fallback, which
    // is exactly the pre-throttle behavior for every input. The tail keeps
    // the full block parse - never an inline-only one.
    const split = splitLiveSource(source);
    if (
      split === null ||
      closeOpenMarkdown(split.head) !== split.head ||
      HEAD_SPLIT_HAZARD.test(split.head) ||
      headHasLinkDefinition(split.head, headDefCacheRef) ||
      containsLinkDefinition(split.tail) ||
      HTML_BLOCK_START.test(split.head) ||
      HTML_BLOCK_END.test(split.tail) ||
      TAIL_BLOCK_MARKER.test(split.tail) ||
      tailStartsIndented(split.tail) ||
      closeOpenMarkdown(source) !== source
    ) {
      const rawHtml = md.parse(closeOpenMarkdown(source), { async: false });
      return DOMPurify.sanitize(rawHtml, SANITIZE_CONFIG);
    }
    const cached = headCacheRef.current;
    const headHtml =
      cached !== null && cached.headSource === split.head
        ? cached.headHtml
        : DOMPurify.sanitize(md.parse(split.head, { async: false }), SANITIZE_CONFIG);
    if (cached === null || cached.headSource !== split.head) {
      headCacheRef.current = { headSource: split.head, headHtml };
    }
    const tailHtml = DOMPurify.sanitize(md.parse(closeOpenMarkdown(split.tail), { async: false }), SANITIZE_CONFIG);
    return headHtml + tailHtml;
  }, [source, live]);

  // Reviewed: this is the narrow, legitimate case for dangerouslySetInnerHTML
  // (rendering markdown-to-HTML has no alternative in React without a full
  // HTML-to-element parser) - defense in depth above: marked's html()
  // override escapes raw source HTML to text before it's ever markup,
  // every custom renderer escapes its own string interpolations, and
  // DOMPurify.sanitize() re-checks the generated output against a fixed
  // allowlist as a second, independent layer (its default safe-URI-scheme
  // filtering for href/src is untouched - SANITIZE_CONFIG never sets
  // ALLOWED_URI_REGEXP). See this file's own comments above for the rest.
  // biome-ignore lint/security/noDangerouslySetInnerHtml: sanitized via DOMPurify + escaped renderer overrides, see above
  return <div className={CLASS.root} dangerouslySetInnerHTML={{ __html: html }} />;
}
