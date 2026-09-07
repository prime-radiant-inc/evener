import DOMPurify from "dompurify";
import { Marked, type RendererObject, type Tokens } from "marked";
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
const LIVE_WINDOWED_MIN_LENGTH = 2000;
// The tail window re-parsed on every live render past the threshold above -
// sized to cover the in-progress block (a long paragraph, a fenced code
// block still being written) without re-parsing the whole document.
const LIVE_TAIL_WINDOW = 1000;

// Finds the split point for the windowed live path: the last blank-line
// boundary at or before `source.length - LIVE_TAIL_WINDOW`, so the head ends
// on a settled block edge and the tail starts a fresh block. Falls back to a
// hard cut at the window edge when the tail window holds no blank line (one
// very long paragraph still streaming) - the tail's inline-scope parse below
// makes that safe.
function findWindowBoundary(source: string): number {
  const edge = source.length - LIVE_TAIL_WINDOW;
  const boundary = source.lastIndexOf("\n\n", edge);
  return boundary === -1 ? edge : boundary + 2;
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
  // the live auto-close, so formatting still previews while streaming.
  // Settled renders (live=false) always take the full-parse path below
  // unchanged, so the final HTML is byte-identical with or without this
  // throttle.
  const headCacheRef = useRef<{ headSource: string; headHtml: string } | null>(null);
  const html = useMemo(() => {
    if (!live || source.length <= LIVE_WINDOWED_MIN_LENGTH) {
      const rawHtml = md.parse(live ? closeOpenMarkdown(source) : source, { async: false });
      return DOMPurify.sanitize(rawHtml, SANITIZE_CONFIG);
    }
    const boundary = findWindowBoundary(source);
    const headSource = source.slice(0, boundary);
    const tailSource = source.slice(boundary);
    const cached = headCacheRef.current;
    const headHtml =
      cached !== null && cached.headSource === headSource
        ? cached.headHtml
        : DOMPurify.sanitize(md.parse(headSource, { async: false }), SANITIZE_CONFIG);
    if (cached === null || cached.headSource !== headSource) {
      headCacheRef.current = { headSource, headHtml };
    }
    // The tail is a live preview, not a reopenable document: parsing it
    // standalone can leave block structure open at its start (a list item, a
    // fence), so it is kept to inline scope - the tail's own block boundary
    // (see findWindowBoundary) plus closeOpenMarkdown already closed its
    // emphasis/code spans, and any block wrapper marked emits here is
    // dropped in favor of its inner content.
    const tailClosed = closeOpenMarkdown(tailSource);
    const tailHtml = DOMPurify.sanitize(md.parseInline(tailClosed, { async: false }) as string, SANITIZE_CONFIG);
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
