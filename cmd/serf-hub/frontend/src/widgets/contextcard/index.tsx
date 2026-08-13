// Structure adapted from Beautiful UI's Context card
// (https://www.beautifului.dev), MIT License, Copyright (c) 2026 Shane
// Levine — see LICENSES/beautiful-ui.txt. Values and markup translated
// into serf's CSS-module + token system; nothing is copy-pasted.
import { requireClass } from "../internal/requireClass";
import { ToolIcon } from "../toolicon";
import styles from "./contextcard.module.css";

export interface ContextCardProps {
  source: string;
  snippet: string;
  /** e.g. "1.2k chars" - shown as a trailing caption when given. */
  meta?: string;
  /** When given, the whole card renders as a link to it. */
  href?: string;
}

const CLASS = {
  card: requireClass(styles.card, "contextcard.module.css", "card"),
  sourceRow: requireClass(styles.sourceRow, "contextcard.module.css", "sourceRow"),
  glyph: requireClass(styles.glyph, "contextcard.module.css", "glyph"),
  source: requireClass(styles.source, "contextcard.module.css", "source"),
  snippet: requireClass(styles.snippet, "contextcard.module.css", "snippet"),
  meta: requireClass(styles.meta, "contextcard.module.css", "meta"),
};

// A web source gets the globe glyph, anything else (a file path, a doc
// name) gets the file glyph - the same "shape of source" distinction
// ToolIcon's own kinds already draw for tool rows.
function glyphKindFor(href: string | undefined): "globe" | "file" {
  return href !== undefined && /^https?:\/\//.test(href) ? "globe" : "file";
}

/**
 * A compact retrieval-context card: a source line with a leading glyph, a
 * clamped snippet, and an optional trailing meta caption. Renders as an
 * `<a>` (with the standard focus ring) when `href` is given, a plain `<div>`
 * otherwise.
 */
export function ContextCard({ source, snippet, meta, href }: ContextCardProps) {
  const content = (
    <>
      <div className={CLASS.sourceRow}>
        <span className={CLASS.glyph}>
          <ToolIcon kind={glyphKindFor(href)} size={12} />
        </span>
        <span className={CLASS.source}>{source}</span>
        {meta !== undefined && <span className={CLASS.meta}>{meta}</span>}
      </div>
      <p className={CLASS.snippet}>{snippet}</p>
    </>
  );

  if (href !== undefined) {
    return (
      <a className={CLASS.card} href={href}>
        {content}
      </a>
    );
  }

  return <div className={CLASS.card}>{content}</div>;
}
