import DOMPurify from "dompurify";
import type { Token } from "marked";
import { memo, type Ref, useMemo, useRef } from "react";
import { MermaidDiagram } from "../mermaid";
import { MARKDOWN_CLASS as CLASS, markdownParser as md, SANITIZE_CONFIG } from "./rendering";
import {
  type HeadGateVerdict,
  LIVE_WINDOWED_MIN_LENGTH,
  type LiveSegmentsCache,
  messageMayContainMermaid,
  splitLiveMarkdownSegments,
  splitLiveWindow,
  splitMarkdownSegments,
} from "./segments";
import { closeOpenMarkdown } from "./streaming";

export interface MarkdownProps {
  source: string;
  /** Lets a caller attach actions to sanitized content without a layout wrapper. */
  ref?: Ref<HTMLDivElement>;
  /** True while `source` is a possibly-truncated stream still in flight:
   * constructs left open at the tail (unterminated `**`/`*`/`~~`, inline
   * code, fenced code blocks) are closed before parsing via
   * closeOpenMarkdown, so formatting renders WHILE streaming instead of
   * showing literal marker source. Settled renders must NOT pass this - a
   * genuinely unterminated final source stays honestly literal. */
  live?: boolean;
}

export { markdownLexer } from "./lexer";

// One prose slice of a segmented message: the same parse+sanitize pipeline as
// the single-root path, but over a token array carved out by segments.ts (a
// token array, never re-serialized source, so whole-document references stay
// resolved). Memoized on the token array so a settling diagram elsewhere in
// the message does not re-sanitize untouched prose. `window` (issue #3208) is
// the still-streaming tail of a long settled run: it sanitizes into the SAME
// div, so a split at a paragraph boundary keeps the spacing a single parse
// would, while the settled `tokens` keep their identity and their memo hits.
const MarkdownSlice = memo(function MarkdownSlice({
  segment,
}: {
  segment: { kind: "markdown"; tokens: Token[]; window?: string };
}) {
  const html = useMemo(() => DOMPurify.sanitize(md.parser(segment.tokens), SANITIZE_CONFIG), [segment.tokens]);
  const windowHtml = useMemo(
    () =>
      segment.window === undefined
        ? ""
        : DOMPurify.sanitize(md.parse(closeOpenMarkdown(segment.window), { async: false }), SANITIZE_CONFIG),
    [segment.window],
  );
  // biome-ignore lint/security/noDangerouslySetInnerHtml: same sanitized pipeline as the single-root path, see above
  return <div className={CLASS.root} dangerouslySetInnerHTML={{ __html: html + windowHtml }} />;
});

/**
 * Renders a markdown source string: marked tokenizes and generates HTML,
 * DOMPurify sanitizes it against a fixed allowlist, and the result is set
 * as innerHTML. Fenced code blocks render through CodeBlock's own
 * stylesheet; links always open in a new tab without opener access; raw
 * HTML in the source is never interpreted as markup. With `live`, the
 * source is treated as a truncated stream and its open constructs are
 * closed before parsing (see streaming.ts).
 */
export function Markdown({ source, live = false, ref }: MarkdownProps) {
  // Branch to the segmented path only when the source may carry a mermaid
  // fence; every other message takes the single-root path byte-identically.
  const segmented = messageMayContainMermaid(source);
  // Live re-parse throttle for the single-root path. Past the window
  // threshold the settled head is served from `headCacheRef` (keyed on its own
  // exact prefix text - a changed head is a cache miss and re-parses, never
  // stale output) while only the bounded tail window re-parses per render, so
  // formatting still previews while streaming. splitLiveWindow (segments.ts)
  // owns the soundness gate: it declines (returns null) whenever the split
  // could change the parse - a link definition, a table delimiter, a block
  // construct or HTML block spanning the boundary, an open fence - and that
  // fallback below is exactly the pre-throttle full parse. Settled renders
  // (live=false) always take the settled full-parse path unchanged, so the
  // final HTML is byte-identical with or without this throttle.
  const headCacheRef = useRef<{ headSource: string; headHtml: string } | null>(null);
  // Backs the head-side gate verdicts splitLiveWindow caches: keyed on the
  // head's own exact text, never stale, misses evaluate once.
  const headGateCacheRef = useRef<HeadGateVerdict | null>(null);
  const html = useMemo(() => {
    if (segmented) return "";
    if (!live || source.length <= LIVE_WINDOWED_MIN_LENGTH) {
      const rawHtml = md.parse(live ? closeOpenMarkdown(source) : source, { async: false });
      return DOMPurify.sanitize(rawHtml, SANITIZE_CONFIG);
    }
    const split = splitLiveWindow(source, headGateCacheRef);
    if (split === null) {
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
    const tailHtml = DOMPurify.sanitize(md.parse(split.closedTail, { async: false }), SANITIZE_CONFIG);
    return headHtml + tailHtml;
  }, [source, live, segmented]);

  // Segmented path (a mermaid fence is present). Live mode caches the segmentation
  // of the frozen head (the source through the last CLOSED mermaid fence, keyed on
  // its exact text) and re-segments only the tail per render, with the stream's
  // open constructs closed and a still-open mermaid fence demoted within the tail
  // (segments.ts). So a long stream no longer pays a whole-message lex+close on
  // every token; the head's slice arrays keep identity and their MarkdownSlice memo
  // hits. Settled renders (live=false) segment the source directly, unchanged. The
  // hook is called unconditionally so hook order is stable as a source grows into
  // (or out of) a fence; the settled single-root `html` is empty on this branch and
  // vice versa.
  const liveSegmentCacheRef = useRef<LiveSegmentsCache | null>(null);
  const segments = useMemo(
    () =>
      segmented
        ? live
          ? splitLiveMarkdownSegments(source, liveSegmentCacheRef)
          : splitMarkdownSegments(source, null)
        : null,
    [source, live, segmented],
  );

  // Reviewed: this is the narrow, legitimate case for dangerouslySetInnerHTML
  // (rendering markdown-to-HTML has no alternative in React without a full
  // HTML-to-element parser) - defense in depth above: marked's html()
  // override escapes raw source HTML to text before it's ever markup,
  // every custom renderer escapes its own string interpolations, and
  // DOMPurify.sanitize() re-checks the generated output against a fixed
  // allowlist as a second, independent layer (its default safe-URI-scheme
  // filtering for href/src is untouched - SANITIZE_CONFIG never sets
  // ALLOWED_URI_REGEXP). See this file's own comments above for the rest.
  if (!segmented) {
    // biome-ignore lint/security/noDangerouslySetInnerHtml: sanitized via DOMPurify + escaped renderer overrides, see above
    return <div ref={ref} className={CLASS.root} dangerouslySetInnerHTML={{ __html: html }} />;
  }

  // Keys combine a sequence position with content-derived values, so a
  // settling fence (same position, different content) or a theme flip never
  // reuses the wrong DOM node. A plain local counter rather than the map
  // callback's index: biome forbids keys sourced from the array index.
  let sequence = 0;
  return (
    <div ref={ref}>
      {(segments ?? []).map((segment) => {
        sequence += 1;
        return segment.kind === "mermaid" ? (
          <MermaidDiagram key={`mermaid-${sequence}-${segment.text.length}`} source={segment.text} />
        ) : (
          <MarkdownSlice
            key={`markdown-${sequence}-${segment.tokens.length}-${segment.tokens[0]?.raw.length ?? 0}`}
            segment={segment}
          />
        );
      })}
    </div>
  );
}

export function MarkdownBubble({
  source,
  density = "default",
  dataTestId,
}: {
  source: string;
  density?: "default" | "compact";
  dataTestId?: string;
}) {
  return (
    <div
      className={`${CLASS.bubble}${density === "compact" ? ` ${CLASS.bubbleCompact}` : ""}`}
      data-testid={dataTestId}
    >
      <Markdown source={source} />
    </div>
  );
}
