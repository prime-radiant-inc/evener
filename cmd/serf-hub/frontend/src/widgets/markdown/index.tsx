import DOMPurify from "dompurify";
import { Marked, type RendererObject, type Tokens } from "marked";
import { useMemo } from "react";
import codeblockStyles from "../codeblock/codeblock.module.css";
import { requireClass } from "../internal/requireClass";
import styles from "./markdown.module.css";

export interface MarkdownProps {
  source: string;
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

// Sanitizes the OUTPUT html, not the markdown source - this is DOMPurify's
// documented pairing with marked (marked itself performs no sanitization).
// The allowlist is deliberately small: every tag/attribute marked's
// defaults or the overrides above can produce, and nothing else. Notably
// absent: img/table and friends - GFM tables and images tokenize fine but
// aren't allowlisted yet (no consumer needs them this wave). Disallowed
// elements don't uniformly "degrade to their text content" though -
// verified directly against this exact config: a <thead>'s content
// (including its <th> cells) is dropped entirely, while a <tbody>'s
// <td> content survives as unwrapped text; a bare <img> has no text
// content to keep in the first place, so it just vanishes. Whichever way
// a given element behaves, nothing dangerous survives - they fail safe,
// just not via one single mechanism.
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
  ],
  // "class" is allowlisted globally (not scoped to specific tags) but is
  // currently inert against anything a markdown AUTHOR controls: the only
  // class attributes this pipeline ever produces are the ones the code
  // above writes itself (CODEBLOCK_CLASS/CLASS.inlineCode) - html() above
  // escapes all raw source HTML to text before DOMPurify ever sees it, so
  // there's no path today for a class value to originate from user input.
  // If a future change widens ALLOWED_TAGS to let raw source HTML through
  // in some form, that safety stops being automatic - re-check whether
  // "class" should stay this permissive at that point.
  ALLOWED_ATTR: ["href", "title", "target", "rel", "class"],
};

/**
 * Renders a markdown source string: marked tokenizes and generates HTML,
 * DOMPurify sanitizes it against a fixed allowlist, and the result is set
 * as innerHTML. Fenced code blocks render through CodeBlock's own
 * stylesheet; links always open in a new tab without opener access; raw
 * HTML in the source is never interpreted as markup.
 */
export function Markdown({ source }: MarkdownProps) {
  const html = useMemo(() => {
    const rawHtml = md.parse(source, { async: false });
    return DOMPurify.sanitize(rawHtml, SANITIZE_CONFIG);
  }, [source]);

  return <div className={CLASS.root} dangerouslySetInnerHTML={{ __html: html }} />;
}
