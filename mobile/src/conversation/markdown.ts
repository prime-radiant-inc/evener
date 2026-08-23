import DOMPurify from "dompurify";
import { Marked, type RendererObject, type Tokens } from "marked";

/**
 * Branded HTML string. Only `AssistantMessage` may consume this via
 * `dangerouslySetInnerHTML`; every other component renders escaped plain text.
 * The opaque `__trusted` symbol cannot be constructed outside this module.
 */
const TRUSTED = Symbol("trusted");
export interface TrustedHTMLString {
  readonly __trusted: typeof TRUSTED;
  readonly html: string;
}

/**
 * Explicit tag allowlist. `img` is intentionally absent — Markdown images are
 * not enabled.
 */
const ALLOWED_TAGS = [
  "p",
  "br",
  "strong",
  "em",
  "code",
  "pre",
  "ul",
  "ol",
  "li",
  "blockquote",
  "h1",
  "h2",
  "h3",
  "h4",
  "h5",
  "h6",
  "a",
  "hr",
  "table",
  "thead",
  "tbody",
  "tr",
  "th",
  "td",
  "del",
  "ins",
  "sub",
  "sup",
] as const;

const ALLOWED_ATTR = ["href", "colspan", "rowspan"] as const;

/**
 * Reject every URL protocol except `http:` and `https:`. DOMPurify applies this
 * to all URI-bearing attributes (`href`). Relative/anchor URLs and non-URI
 * values are permitted; anything starting with `javascript:`, `data:`,
 * `mailto:`, etc. is dropped.
 */
const ALLOWED_URI_REGEXP = /^(https?:|[^a-z]|[a-z+.-]+(?:[^a-z+.-:]|$))/i;

/**
 * Shared `Marked` instance configured to strip raw HTML at the token level.
 * Using `use()` overrides only the `html` renderer method, leaving all default
 * renderers (paragraph, heading, list, etc.) intact so no raw HTML ever reaches
 * DOMPurify.
 */
const markdownParser = new Marked({ gfm: true, breaks: false });
markdownParser.use({
  renderer: {
    html: () => "",
  } satisfies Partial<RendererObject>,
});

/**
 * Normalize a string so malformed Unicode (lone surrogates) cannot propagate to
 * the DOM parser as unpaired surrogates. Lone surrogates become U+FFFD.
 */
function sanitizeUnicode(input: string): string {
  const wellFormed = (
    String.prototype as unknown as { wellFormed?: () => string }
  ).wellFormed;
  if (typeof wellFormed === "function") {
    return wellFormed.call(input);
  }
  return input;
}

/**
 * Parse Markdown with raw HTML stripped at the token level, then sanitize the
 * resulting HTML through DOMPurify with an explicit tag/attribute allowlist and
 * a strict URL-protocol filter. Returns a branded `TrustedHTMLString` that only
 * `AssistantMessage` may consume.
 */
export function renderSafeMarkdown(source: string): TrustedHTMLString {
  const safe = sanitizeUnicode(source);
  const raw = markdownParser.parse(safe) as string;
  const clean = DOMPurify.sanitize(raw, {
    ALLOWED_TAGS: [...ALLOWED_TAGS],
    ALLOWED_ATTR: [...ALLOWED_ATTR],
    ALLOWED_URI_REGEXP,
    ALLOW_ARIA_ATTR: false,
    ALLOW_DATA_ATTR: false,
    FORBID_TAGS: [
      "style",
      "script",
      "form",
      "input",
      "iframe",
      "object",
      "embed",
    ],
    FORBID_ATTR: ["style", "onerror", "onload", "onclick"],
  });
  return { __trusted: TRUSTED, html: clean };
}

/**
 * A text-only renderer that renders every token to plain text, dropping URLs,
 * code fences, emphasis delimiters, list markers, and raw HTML. Used for voice
 * synthesis. The marked `Parser` binds `this.parser` onto the renderer object
 * before invoking any method, so each method recurses through `this.parser`
 * using `parseInline` for inline token contexts and `parse` for block contexts.
 */
const speakableRenderer: Partial<RendererObject> = {
  space: () => "",
  code: ({ text }: Tokens.Code) => text,
  blockquote: function ({ tokens }: Tokens.Blockquote) {
    return this.parser.parse(tokens) as string;
  },
  html: () => "",
  def: () => "",
  heading: function ({ tokens }: Tokens.Heading) {
    return this.parser.parseInline(tokens) as string;
  },
  hr: () => "",
  list: function (token: Tokens.List) {
    return token.items
      .map((item) => this.parser.parse(item.tokens) as string)
      .join("\n");
  },
  listitem: function (item: Tokens.ListItem) {
    return this.parser.parse(item.tokens) as string;
  },
  checkbox: () => "",
  paragraph: function ({ tokens }: Tokens.Paragraph) {
    return this.parser.parseInline(tokens) as string;
  },
  table: function (token: Tokens.Table) {
    return [token.header, ...token.rows]
      .map((row) =>
        row
          .map((cell) => this.parser.parseInline(cell.tokens) as string)
          .join(" "),
      )
      .join("\n");
  },
  tablerow: () => "",
  tablecell: () => "",
  strong: function ({ tokens }: Tokens.Strong) {
    return this.parser.parseInline(tokens) as string;
  },
  em: function ({ tokens }: Tokens.Em) {
    return this.parser.parseInline(tokens) as string;
  },
  del: function ({ tokens }: Tokens.Del) {
    return this.parser.parseInline(tokens) as string;
  },
  codespan: ({ text }: Tokens.Codespan) => text,
  br: () => "\n",
  link: function ({ tokens }: Tokens.Link) {
    return this.parser.parseInline(tokens) as string;
  },
  image: ({ text }: Tokens.Image) => text,
  text: (token: Tokens.Text | Tokens.Escape) => token.text,
};

/**
 * Dedicated `Marked` instance for speakable-text rendering. `use()` merges the
 * text-only renderer over the defaults so only the overridden methods change;
 * `this.parser` is bound by the parser at render time.
 */
const speakableParser = new Marked({ gfm: true, breaks: false });
speakableParser.use({ renderer: speakableRenderer });

/**
 * Strip all Markdown formatting to plain text for voice synthesis. Raw HTML is
 * removed, links keep only their text, code/list/heading/blockquote markers are
 * dropped, and emphasis delimiters are removed.
 */
export function toSpeakableText(source: string): string {
  const safe = sanitizeUnicode(source);
  const out = speakableParser.parse(safe) as string;
  return out.trim();
}
