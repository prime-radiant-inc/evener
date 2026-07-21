// StreamingText is the imperative leaf for in-flight streaming text (a
// live agentMessage/reasoning delta tail, or - in wave 4's raw fallback
// view - any other still-streaming item). It appends ONLY the chunks not
// yet flushed to a single DOM text node, never touching what's already
// there, so a burst of deltas never re-renders (or re-diffs) settled text.
import { useLayoutEffect, useRef } from "react";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./streamingtext.module.css";

export interface StreamingTextProps {
  chunks: string[];
  /** Called after new chunks are flushed, with the full joined text so far.
   * Not called when a re-render carries no new chunks. */
  onCommit?(text: string): void;
  /** True while this leaf can still receive further deltas - shows the
   * design system's one reserved "streaming" motion, a blinking caret
   * glyph (docs/superpowers/plans/2026-07-20-webui-rewrite-wave2-design-
   * system.md, Motion: "streaming caret blink"). Defaults to true, since
   * every current caller (RawItemView/AgentMessageItem/ThinkBlock) only
   * ever mounts this component while genuinely live in the first place -
   * they swap to a settled view entirely rather than flipping this prop.
   * Honest by construction: the caret exists only while `live` says so,
   * never as an idle/decorative animation - see streamingtext.module.css's
   * own comment for the CSS side of that contract. */
  live?: boolean;
}

const CLASS = {
  root: requireClass(styles.root, "streamingtext.module.css", "root"),
  live: requireClass(styles.live, "streamingtext.module.css", "live"),
};

/**
 * `chunks` only ever grows by appending (see protocol/reducer.ts's
 * item/agentMessage/delta and item/reasoning/summaryTextDelta: both spread
 * the previous array plus one new entry, never replacing or reordering an
 * existing one) - so "how many chunks have been rendered" is fully captured
 * by a single count, tracked in a ref rather than by diffing content. A
 * chunks array that is the same length as (or shorter than - defensive)
 * what's already rendered is treated as nothing-new, regardless of whether
 * it's the SAME array reference or a fresh one with identical content.
 *
 * Plain string concatenation (Text.appendData) is inherently safe for a
 * UTF-16 surrogate pair split across two chunks: concatenation operates on
 * UTF-16 code units, and the browser only interprets surrogate pairs into
 * glyphs at paint time, over the text node's final joined content - not per
 * appendData() call. Nothing here ever slices a chunk's own string, which
 * is the only thing that could actually corrupt a lone surrogate.
 *
 * The root span declares NO JSX children, ever - every character is added
 * via direct DOM calls in a layout effect, outside React's own
 * reconciliation, so a parent re-render can never clobber (or redundantly
 * diff) content already committed here.
 */
export function StreamingText({ chunks, onCommit, live = true }: StreamingTextProps) {
  const rootRef = useRef<HTMLSpanElement | null>(null);
  const textNodeRef = useRef<Text | null>(null);
  const renderedCountRef = useRef(0);

  useLayoutEffect(() => {
    if (!textNodeRef.current) {
      textNodeRef.current = document.createTextNode("");
      rootRef.current?.appendChild(textNodeRef.current);
    }
    if (chunks.length > renderedCountRef.current) {
      const added = chunks.slice(renderedCountRef.current).join("");
      textNodeRef.current.appendData(added);
      renderedCountRef.current = chunks.length;
      onCommit?.(textNodeRef.current.data);
    }
  }, [chunks, onCommit]);

  const className = live ? `${CLASS.root} ${CLASS.live}` : CLASS.root;
  return <span ref={rootRef} className={className} data-testid="streaming-text" />;
}
