// ToolRow is THE tool-call row. Every tool renderer composes this one
// component, so no renderer owns a layout of its own — a descriptor decides
// only its own CONTENT (what the verb/target is, what meta it shows, what its
// expanded body looks like), never how the row is arranged.
//
// THE ROW GRAMMAR, two lines when a purpose exists (one otherwise):
//
//     rail:   [kind icon, 50%]            (in the gutter, beside line 1)
//     line 1: [✗ failure glyph?] [status?] purpose[chevron inline]
//     line 2: verb target [· meta] [affordances]
//
//   Line 2's truncation: COLLAPSED it middle-truncates (head … tail, the
//   command's ending always visible - the file being written, the branch
//   being merged - and the full text on the hover title); EXPANDED it wraps
//   in full, so an open row always shows the whole call. The one exception:
//   a descriptor whose expanded body already shows the summary's content
//   (shell - the body renders the command pretty-printed) drops line 2
//   entirely while open (summaryHiddenWhenExpanded), so the call never
//   appears twice.
//
//   - the kind icon sits in the RAIL beside the rationale line (Jesse's
//     review call): pulled --speaker-gutter left into the padding the
//     runContent wrapper reserves, at 50% opacity - the kind is ambient
//     context, not content. Below the breakpoint (no gutter) it leads the
//     rationale line inline, same 50%;
//   - a COLLAPSED row with both a purpose and a summary STACKS them: the
//     purpose (the agent's stated rationale, italic) on the first line, the
//     verb/target summary demoted to a quiet second line (truncation
//     per above). Composing both onto one line was tried (tiered density)
//     and reverted on review: two clamped, truncated fragments read worse
//     than one full line plus one clamped one.
//   - the chevron rides INLINE at the end of the headline text - inside the
//     purpose when there is one, otherwise inside the summary - wrapping with
//     the words it opens. It is never a flex item of the row: right-justified
//     at the end of the line it sat a column of whitespace away from its own
//     rationale (Jesse's review call), the very defect the trailing placement
//     was supposed to fix;
//   - the failure glyph appears ONLY on a failed call and reserves no space
//     otherwise (A2 — see the deliberate-inconsistency note below);
//   - the purpose is the agent's own stated reason for the call
//     (ItemModel.description) and LEADS the text, because it is the one part
//     written for a human; verb/target recede to a quiet line under it, in
//     the same sans face - fixed-width is reserved for shell, whose summary
//     IS a command (descriptor monoSummary);
//   - verb/target/meta are one string the descriptor's summary() produced;
//   - affordances are trailing controls (e.g. "Open beside") and they ride the
//     TOOL-CALL line: inline at the end of the summary when there is one,
//     which - with a purpose present - is the demoted second line, not the
//     rationale line. A purpose-only row (no summary) trails them on the
//     purpose line instead, the one line it has. The one exception: a
//     descriptor whose summary quotes its target verbatim (read_file's
//     openBesideInline) anchors the control mid-summary via trailingAfter -
//     between the file name and the line range it opens.
//
// A row with no purpose is a single line: summary, then affordances, then
// the chevron if there is something to expand.
import type { ReactNode } from "react";
import { Chevron, FailureGlyph, ToolIcon, type ToolIconKind } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./toolcallitem.module.css";

const CLASS = {
  row: requireClass(styles.row, "toolcallitem.module.css", "row"),
  purpose: requireClass(styles.purpose, "toolcallitem.module.css", "purpose"),
  summary: requireClass(styles.summary, "toolcallitem.module.css", "summary"),
  mono: requireClass(styles.mono, "toolcallitem.module.css", "mono"),
  status: requireClass(styles.status, "toolcallitem.module.css", "status"),
  rowIcon: requireClass(styles.rowIcon, "toolcallitem.module.css", "rowIcon"),
  demoted: requireClass(styles.demoted, "toolcallitem.module.css", "demoted"),
  clamped: requireClass(styles.clamped, "toolcallitem.module.css", "clamped"),
  clampedHead: requireClass(styles.clampedHead, "toolcallitem.module.css", "clampedHead"),
  clampedTail: requireClass(styles.clampedTail, "toolcallitem.module.css", "clampedTail"),
  summaryTrailing: requireClass(styles.summaryTrailing, "toolcallitem.module.css", "summaryTrailing"),
  summaryMeta: requireClass(styles.summaryMeta, "toolcallitem.module.css", "summaryMeta"),
  chevron: requireClass(styles.chevron, "toolcallitem.module.css", "chevron"),
};

export interface ToolRowProps {
  /** The descriptor's own one-line verb/target/meta string. */
  summary: string;
  /** A URL literally present in `summary` that should render as a real link
   * (the descriptor's `summaryLink`, kata xw3t) rather than plain text.
   * Applied only to the FULL, untruncated rendering of `summary` - never
   * inside the collapsed head/tail clamp (character-position truncation can
   * cut a URL mid-way, or split it across the two independently
   * ellipsis-clamped spans, with no sound "which half is clickable" answer)
   * - opening the row shows the summary in full, with the link. Undefined
   * (every descriptor but web_fetch, today) or a value not literally found
   * inside `summary` renders unchanged. */
  summaryLink?: string;
  /** The agent's stated reason for the call (ItemModel.description). Blank or
   * absent renders nothing at all — no placeholder, no empty separator. */
  purpose?: string;
  /** The tool-FAMILY glyph, riding inline at the start of the tool-use line
   * (the descriptor's `icon` field); a summary-less row rides it on the
   * purpose line instead. Absent renders no icon. */
  icon?: ToolIconKind;
  /** Fixed-width summary text (shell, whose summary IS a command). Default
   * is the sans face - Jesse's review call: fixed-width everywhere made
   * every tool read like a terminal. */
  monoSummary?: boolean;
  failed: boolean;
  expandable: boolean;
  expanded: boolean;
  onToggle?: () => void;
  /** Trailing controls (e.g. the "Open beside" button). */
  trailing?: ReactNode;
  /** The COMPLETE PREFIX of `summary` after which `trailing` rides INLINE
   * (the one case today: read_file's "open beside" control lands between the
   * file name and the "· lines N-M" meta - descriptor openBesideInline).
   * Verified with `summary.startsWith(trailingAfter)`, never searched: a
   * bare substring search (indexOf or lastIndexOf) is ambiguous whenever the
   * anchor text also occurs elsewhere in `summary`, in EITHER direction
   * (kata ledger #97 - a file literally named the same word `readLineRange`
   * puts in the meta suffix collides one way, a coincidental match later in
   * the string collides the other way). A from-the-start prefix has no such
   * direction to be ambiguous in. Absent, or not a literal prefix of
   * `summary`, keeps the default end-of-line placement (same "never a dead
   * anchor" contract as summaryLink). */
  trailingAfter?: string;
  /** Optional status rail content for tools that need a one-glance
   * progression/health signal before human-facing purpose text. */
  status?: ReactNode;
  /** Hover text for details that are real but must not be the headline — the
   * shell exit code, per A2. */
  title?: string;
}

/** The one rule for reading a tool call's stated purpose (ItemModel.description):
 * trimmed, and blank means ABSENT. Shared with the subagent activity feed
 * (tools/subagentModule.tsx), which presents the same field very differently
 * (a numbered feed of a child's steps) but must agree on when it exists at all -
 * otherwise a whitespace-only description is a line in one surface and nothing
 * in the other. */
export function statedPurposeOf(item: { description?: string }): string | undefined {
  const trimmed = item.description?.trim();
  return trimmed === undefined || trimmed === "" ? undefined : trimmed;
}

/** The collapsed second line's middle-truncation split: head gets ~60% of the
 * characters and ellipsis-clamps under pressure; the tail always renders in
 * full, because a command's ENDING is the part end-truncation kept hiding
 * (the file being written, the branch being merged). Split on code POINTS
 * (Array.from), never UTF-16 units - a cut through a surrogate pair would
 * render a replacement glyph.
 *
 * The cut must never sit ADJACENT to whitespace: the head and tail render as
 * separate flex items (.clamped is display:flex), and CSS white-space
 * processing removes whitespace at a flex item's line edges. A raw 60% cut
 * through "Ran go test ./..." leaves the space at the tail's start, and the
 * browser renders "Ran go test./...". Walk the cut left across any boundary
 * whitespace so every space stays INTERIOR to one span and survives. */
function middleSplit(text: string): [head: string, tail: string] {
  const chars = Array.from(text);
  let cut = Math.ceil(chars.length * 0.6);
  const isSpace = (i: number) => /\s/.test(chars[i] ?? "");
  while (cut > 0 && cut < chars.length && (isSpace(cut - 1) || isSpace(cut))) {
    cut--;
  }
  return [chars.slice(0, cut).join(""), chars.slice(cut).join("")];
}

/** Makes exactly the substring of `text` equal to `href` a real link (same
 * target/rel idiom as tcp9's expanded-body link), leaving the rest as plain
 * text; renders `text` unchanged if `href` is undefined or isn't literally
 * present in it (a descriptor bug, never a mismatched or fabricated href -
 * kata xw3t's own "never a dead anchor" carryover from tcp9). stopPropagation
 * on the anchor's own click keeps it from also toggling the enclosing
 * <summary>'s disclosure: that element's onClick (below) unconditionally
 * preventDefaults every click that reaches it, which - since this is the
 * SAME bubbled event - would otherwise cancel the link's native navigation
 * too, not just skip the toggle. */
function linkifySummary(text: string, href: string | undefined): ReactNode {
  if (href === undefined) return text;
  const start = text.indexOf(href);
  if (start === -1) return text;
  return (
    <>
      {text.slice(0, start)}
      <a href={href} target="_blank" rel="noopener noreferrer" onClick={(e) => e.stopPropagation()}>
        {href}
      </a>
      {text.slice(start + href.length)}
    </>
  );
}

export function ToolRow({
  summary,
  summaryLink,
  purpose,
  icon,
  monoSummary,
  failed,
  expandable,
  expanded,
  onToggle,
  trailing,
  trailingAfter,
  title,
  status,
}: ToolRowProps) {
  const statedPurpose = statedPurposeOf({ description: purpose });
  const hasPurpose = statedPurpose !== undefined;
  const hasSummary = summary.trim() !== "";
  // The inline-affordance anchor (trailingAfter): split the summary into the
  // text up to the anchor's end and the rest, so the trailing control can
  // ride BETWEEN them. Undefined when there is no trailing control, no
  // anchor, or the anchor is not a literal PREFIX of summary.
  //
  // This is a prefix check (startsWith), never a substring search: searching
  // for the anchor anywhere in summary is ambiguous whenever the anchor text
  // recurs elsewhere, in EITHER direction - an earlier coincidental match can
  // win just as easily as a later one (e.g. read_file's own summary always
  // contains the literal word "lines" in its meta suffix, so a file named
  // "lines" collides with it). A from-the-start prefix has no direction left
  // to be ambiguous in: the caller supplies the complete prefix it means, not
  // a fragment ToolRow has to go find (kata ledger #97).
  const anchorSplit = ((): [before: string, after: string] | undefined => {
    if (trailing === undefined || trailing === null || trailingAfter === undefined) return undefined;
    if (!summary.startsWith(trailingAfter)) return undefined;
    return [trailingAfter, summary.slice(trailingAfter.length)];
  })();
  // The chevron rides INLINE at the end of the headline text (see the grammar
  // above): inside the purpose when there is one, otherwise inside the
  // summary - never a flex item of the row, so nothing can justify it away
  // from the words it opens.
  const chevron = expandable ? (
    <span
      className={CLASS.chevron}
      aria-hidden="true"
      data-open={expanded ? "true" : "false"}
      data-testid="tool-row-chevron"
    >
      <Chevron />
    </span>
  ) : null;
  // The kind icon sits in the RAIL beside the rationale line (Jesse's review
  // call: pull the tool-use and thought icons into the gutter, at 50%
  // opacity, on the rationale's line - not inline in the text). It is the
  // row's first flex item: the .rowIcon rules pull it --speaker-gutter to the
  // left, into the padding the runContent wrapper reserves, and pin it to the
  // first line (the rationale) at all times. Below the breakpoint the gutter
  // is gone and the icon simply leads the line inline, at the same 50%.
  const iconNode =
    icon !== undefined ? (
      <span className={CLASS.rowIcon} data-testid="tool-row-icon" aria-hidden="true">
        <ToolIcon kind={icon} />
      </span>
    ) : null;
  // The collapsed second line's middle-truncation, WITH the inline-affordance
  // variant: when anchorSplit places the trailing control mid-summary, the
  // control becomes a flex item of the clamped line between the anchor's end
  // and the remaining meta (read_file: ".../sheet.test.tsx [open] · lines
  // 1-260"). An anchor ending inside the clamped head puts the control right
  // after the head; one ending inside the tail (a long path spans the
  // truncation cut) keeps the path's visible tail whole and puts the control
  // between it and the meta. Either way every character renders exactly once.
  const clampedSummary = ((): ReactNode => {
    const [head, tail] = middleSplit(summary);
    if (anchorSplit === undefined) {
      return (
        <>
          <span className={CLASS.clampedHead} data-testid="tool-row-summary-head">
            {head}
          </span>
          <span className={CLASS.clampedTail} data-testid="tool-row-summary-tail">
            {tail}
          </span>
        </>
      );
    }
    const [before, after] = anchorSplit;
    if (before.length <= head.length) {
      return (
        <>
          <span className={CLASS.clampedHead} data-testid="tool-row-summary-head">
            {before}
          </span>
          <span className={CLASS.summaryTrailing} data-testid="tool-row-trailing">
            {trailing}
          </span>
          <span className={`${CLASS.clampedTail} ${CLASS.summaryMeta}`} data-testid="tool-row-summary-tail">
            {after}
          </span>
        </>
      );
    }
    return (
      <>
        <span className={CLASS.clampedHead} data-testid="tool-row-summary-head">
          {head}
        </span>
        <span className={CLASS.clampedTail} data-testid="tool-row-summary-tail">
          {before.slice(head.length)}
        </span>
        <span className={CLASS.summaryTrailing} data-testid="tool-row-trailing">
          {trailing}
        </span>
        <span className={CLASS.summaryMeta} data-testid="tool-row-summary-meta">
          {after}
        </span>
      </>
    );
  })();
  const content = (
    <>
      {iconNode}
      {/* Only failure earns a glyph, and a clean row reserves NO space for one.
          That is the OPPOSITE of the rail's signal gutter (shell/rail, which
          always reserves 6px) and it is deliberate: the rail needs one stable
          left edge down a long list of sibling rows, whereas a tool row sits
          inside flowing prose, where a blank reserved column reads as a stray
          indent. Different context, different answer. */}
      {failed && <FailureGlyph />}
      {status !== undefined && (
        <span className={CLASS.status} data-testid="tool-row-status">
          {status}
        </span>
      )}
      {hasPurpose && (
        <span className={CLASS.purpose} data-testid="tool-row-purpose">
          {statedPurpose}
          {chevron}
        </span>
      )}
      {hasSummary && (
        <span
          className={`${CLASS.summary}${monoSummary ? ` ${CLASS.mono}` : ""}${
            hasPurpose ? ` ${CLASS.demoted}${expanded ? "" : ` ${CLASS.clamped}`}` : ""
          }`}
          data-testid="tool-row-summary"
          title={hasPurpose ? summary : undefined}
        >
          {hasPurpose && !expanded ? (
            clampedSummary
          ) : anchorSplit !== undefined ? (
            <>
              {linkifySummary(anchorSplit[0], summaryLink)}
              <span className={CLASS.summaryTrailing} data-testid="tool-row-trailing">
                {trailing}
              </span>
              {linkifySummary(anchorSplit[1], summaryLink)}
            </>
          ) : (
            linkifySummary(summary, summaryLink)
          )}
          {!hasPurpose && chevron}
          {/* Affordances ride the TOOL-CALL line (see the grammar above):
              inline at the end of the summary text, so with a purpose present
              they sit on the demoted second line - not the rationale line.
              Skipped when anchorSplit already placed the control mid-summary. */}
          {hasPurpose && trailing && anchorSplit === undefined ? (
            <span className={CLASS.summaryTrailing}>{trailing}</span>
          ) : null}
        </span>
      )}
      {!hasPurpose && !hasSummary && chevron}
      {(!hasPurpose || !hasSummary) && anchorSplit === undefined ? trailing : null}
    </>
  );

  if (!expandable) {
    return (
      <div className={CLASS.row} data-testid="tool-row" data-purpose={hasPurpose ? "true" : undefined} title={title}>
        {content}
      </div>
    );
  }

  return (
    // <summary> is natively keyboard-operable (implicit role="button";
    // Enter/Space synthesize the same click this handler already takes), which
    // is why A3's keyboard requirement needs no extra key handling here.
    //
    // aria-expanded: HTML-AAM maps a details element's first <summary> to
    // role=button, and aria-expanded IS supported on button - Biome's own role
    // table simply doesn't carry summary's implicit mapping. The attribute can
    // never disagree with the native details state either: ToolCallItem drives
    // <details open> from the same `expanded` value passed here.
    // biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable, see above
    // biome-ignore lint/a11y/useAriaPropsSupportedByRole: summary's implicit role is button, which supports aria-expanded, see above
    <summary
      className={CLASS.row}
      data-testid="tool-row"
      data-purpose={hasPurpose ? "true" : undefined}
      title={title}
      aria-expanded={expanded}
      // A descriptor can suppress BOTH the purpose (none stated) and the
      // summary (summaryHiddenWhenExpanded, open) at once, leaving nothing but
      // the aria-hidden chevron inside this <summary> - an unnamed disclosure.
      // aria-label REPLACES the computed accessible name entirely, including
      // any descendant name (FailureGlyph's "Failed", a status glyph's state
      // label), so the fallback applies ONLY when the row would otherwise
      // have no accessible name at all - not merely no purpose/summary. When
      // failed or status is set, their own accessible name stands instead.
      // The fallback is a stable label, not a restoration of the hidden
      // summary text (that suppression, ToolCallItem.tsx:259, is deliberate).
      aria-label={!hasPurpose && !hasSummary && !failed && status === undefined ? "Tool call" : undefined}
      onClick={(e) => {
        // Fully controlled: preventDefault stops the browser flipping
        // <details open> itself, so the caller's store stays the single source
        // of truth (the same posture as widgets/disclosure).
        e.preventDefault();
        onToggle?.();
      }}
    >
      {content}
    </summary>
  );
}
