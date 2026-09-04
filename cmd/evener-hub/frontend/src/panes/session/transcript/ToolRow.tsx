// ToolRow is THE tool-call row. Every tool renderer composes this one
// component, so no renderer owns a layout of its own — a descriptor decides
// only its own CONTENT (what the verb/target is, what meta it shows, what its
// expanded body looks like), never how the row is arranged.
//
// THE ROW GRAMMAR, two lines when an intent exists (one otherwise):
//
//     rail:   [kind icon, 50%]            (in the gutter, beside line 1)
//     line 1: [✗ failure glyph?] [status?] intent[Open?][chevron inline]
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
//   - a COLLAPSED row with both an intent and a summary STACKS them: the
//     intent (the agent's stated rationale, italic) on the first line, the
//     verb/target summary demoted to a quiet second line (truncation
//     per above). Composing both onto one line was tried (tiered density)
//     and reverted on review: two clamped, truncated fragments read worse
//     than one full line plus one clamped one.
//   - the chevron rides INLINE at the end of the headline text - inside the
//     intent when there is one, otherwise inside the summary - wrapping with
//     the words it opens. The intent-only Open variant is the exception: its
//     valid sibling order is intent, Open, chevron (never Open beyond the
//     disclosure arrow), with one overlay trigger owning the whole line;
//   - the failure glyph appears ONLY on a failed call and reserves no space
//     otherwise (A2 — see the deliberate-inconsistency note below);
//   - the intent is the agent's own stated reason for the call
//     (ItemModel.description) and LEADS the text, because it is the one part
//     written for a human; verb/target recede to a quiet line under it, in
//     the same sans face - fixed-width is reserved for shell, whose summary
//     IS a command (descriptor monoSummary);
//   - verb/target/meta are one string the descriptor's summary() produced;
//   - affordances are trailing controls (the open affordance) and they ride
//     immediately AFTER the text they open: inline at the end of the summary
//     when there is one, which - with an intent present - is the demoted
//     second line, not the rationale line. An intent-only row (no summary)
//     trails the control on the intent line instead, the one line it has: a
//     sibling flex item directly AFTER the trigger's visible content (never
//     nested inside the overlay trigger - a button inside a button is not
//     valid), kept adjacent by the [data-intent-trailing] content's
//     flex:0 1 auto + max-width reservation (toolcallitem.module.css) - never
//     sprung to the line's far end. The one
//     exception: a descriptor whose summary quotes its target verbatim
//     (read_file's openBesideInline) anchors the control mid-summary via
//     trailingAfter - between the file name and the line range it opens.
//
// A row with no intent is a single line: summary, then affordances, then
// the chevron if there is something to expand.
import { type ReactNode, useId } from "react";
import { Chevron, FailureGlyph, ToolIcon, type ToolIconKind } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./toolcallitem.module.css";

const CLASS = {
  row: requireClass(styles.row, "toolcallitem.module.css", "row"),
  trigger: requireClass(styles.trigger, "toolcallitem.module.css", "trigger"),
  intentTriggerContent: requireClass(styles.intentTriggerContent, "toolcallitem.module.css", "intentTriggerContent"),
  intentOverlayTrigger: requireClass(styles.intentOverlayTrigger, "toolcallitem.module.css", "intentOverlayTrigger"),
  summaryLine: requireClass(styles.summaryLine, "toolcallitem.module.css", "summaryLine"),
  intent: requireClass(styles.intent, "toolcallitem.module.css", "intent"),
  summary: requireClass(styles.summary, "toolcallitem.module.css", "summary"),
  mono: requireClass(styles.mono, "toolcallitem.module.css", "mono"),
  status: requireClass(styles.status, "toolcallitem.module.css", "status"),
  rowIcon: requireClass(styles.rowIcon, "toolcallitem.module.css", "rowIcon"),
  demoted: requireClass(styles.demoted, "toolcallitem.module.css", "demoted"),
  clamped: requireClass(styles.clamped, "toolcallitem.module.css", "clamped"),
  clampedHead: requireClass(styles.clampedHead, "toolcallitem.module.css", "clampedHead"),
  clampedTail: requireClass(styles.clampedTail, "toolcallitem.module.css", "clampedTail"),
  summaryTrailing: requireClass(styles.summaryTrailing, "toolcallitem.module.css", "summaryTrailing"),
  intentTrailing: requireClass(styles.intentTrailing, "toolcallitem.module.css", "intentTrailing"),
  summaryMeta: requireClass(styles.summaryMeta, "toolcallitem.module.css", "summaryMeta"),
  chevron: requireClass(styles.chevron, "toolcallitem.module.css", "chevron"),
  bodyTrigger: requireClass(styles.bodyTrigger, "toolcallitem.module.css", "bodyTrigger"),
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
  intent?: string;
  /** The tool-FAMILY glyph, riding inline at the start of the tool-use line
   * (the descriptor's `icon` field); a summary-less row rides it on the
   * intent line instead. Absent renders no icon. */
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
   * progression/health signal before human-facing intent text. */
  status?: ReactNode;
  /** Hover text for details that are real but must not be the headline — the
   * shell exit code, per A2. */
  title?: string;
  /** Stable ID of the conditionally rendered body controlled by this trigger. */
  bodyId?: string;
  /** Whether the summary line is open (two-level disclosure: the intent
   * button controls this, the .bodyTrigger chevron controls `expanded`).
   * Ignored when `onToggleSummary` is absent (legacy single-level mode). */
  summaryOpen?: boolean;
  /** Toggle handler for the summary line. When present on an intent-bearing
   * row, the intent button switches from controlling `expanded` to
   * controlling `summaryOpen`, and a separate .bodyTrigger chevron controls
   * `expanded`. Intent-less rows are unchanged regardless. */
  onToggleSummary?: () => void;
  /** When true, the summary line is hidden even if `summaryOpen` is true -
   * the descriptor's summaryHiddenWhenExpanded, derived by the caller
   * (ToolCallItem) as `expanded && descriptor.summaryHiddenWhenExpanded`.
   * The body chevron moves to the intent line (data-intent-trailing). */
  summaryHidden?: boolean;
}

/** The one rule for reading a tool call's stated intent (ItemModel.description):
 * trimmed, and blank means ABSENT. Shared with the subagent activity feed
 * (tools/subagentModule.tsx), which presents the same field very differently
 * (a numbered feed of a child's steps) but must agree on when it exists at all -
 * otherwise a whitespace-only description is a line in one surface and nothing
 * in the other. */
export function statedIntentOf(item: { description?: string }): string | undefined {
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
 * disclosure trigger: that element's onClick (below) fires onToggle for
 * every click that reaches it, which - since this is the SAME bubbled
 * event - would otherwise toggle the row as well as navigate the link.
 *
 * Located by search (indexOf), NOT by the positional-prefix rule
 * `trailingAfter` uses, and deliberately so. That rule exists because a
 * searched anchor could place the trailing CONTROL at a coincidental
 * occurrence - a different spot in the line, carrying a different meaning
 * (kata ledger #97). This anchor has no such wrong answer available: it is
 * the complete href, so every occurrence of it is the same characters
 * denoting the same target, and marking up the first is the same link as
 * marking up any other. A prefix contract here would also be unbuildable
 * without asking every descriptor for text it does not have - the URL sits
 * mid-summary by construction ("Fetched <url> · N bytes"), never at the
 * start. What the search does owe is that it marks up ONE occurrence and
 * leaves the visible text byte-identical; toolRowGrammar.test.tsx pins
 * both. */
function linkifySummary(text: string, href: string | undefined): ReactNode {
  if (href === undefined) return text;
  const start = text.indexOf(href);
  if (start === -1) return text;
  return (
    <>
      {text.slice(0, start)}
      <a href={href} target="_blank" rel="noopener noreferrer">
        {href}
      </a>
      {text.slice(start + href.length)}
    </>
  );
}

export function ToolRow({
  summary,
  summaryLink,
  intent,
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
  bodyId,
  summaryOpen = false,
  onToggleSummary,
  summaryHidden = false,
}: ToolRowProps) {
  const generatedBodyId = useId();
  const disclosureBodyId = bodyId ?? generatedBodyId;
  const summaryRegionId = useId();
  const statedIntent = statedIntentOf({ description: intent });
  const hasIntent = statedIntent !== undefined;
  const hasSummary = summary.trim() !== "";
  // Two-level disclosure is opt-in via onToggleSummary, and only applies to
  // intent-bearing rows. Intent-less rows keep the legacy overlay pattern
  // (one trigger controls the body) regardless of which props are passed.
  const twoLevel = hasIntent && onToggleSummary !== undefined;
  const summaryVisible = summaryOpen && !summaryHidden;
  // `status` is typed ReactNode, so it admits values that render nothing and
  // carry no accessible name - null, undefined, false (the common
  // `condition && <Node/>` idiom) - alongside a real status node. Only a
  // value that can actually render counts as "status present" for both the
  // wrapping span below and the aria-label fallback gate: treating null or
  // false as present would render an empty span and, on the expandable
  // branch, suppress the fallback label with nothing left to name the row.
  const hasStatus = status !== undefined && status !== null && status !== false;
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
  // above): inside the intent when there is one, otherwise inside the summary.
  // The intent-only Open form moves it after the sibling control below so Open
  // can never land on the far side of the disclosure arrow.
  const chevron = expandable ? (
    <span
      className={CLASS.chevron}
      aria-hidden="true"
      data-open={(twoLevel ? summaryVisible : expanded) ? "true" : "false"}
      data-testid="tool-row-chevron"
    >
      <Chevron />
    </span>
  ) : null;
  const failureNode = failed ? <FailureGlyph /> : null;
  // The id lets the intent-only overlay trigger name the status as its
  // description: the visible status is a SIBLING of that trigger (valid DOM
  // order text/Open/chevron), so without aria-describedby a focused trigger
  // no longer announces it (the pre-overlay trigger contained it).
  const statusId = useId();
  const statusNode = hasStatus ? (
    <span id={statusId} className={CLASS.status} data-testid="tool-row-status">
      {status}
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
  // An intent-only row has no tool-call line for affordances to ride, so they
  // ride the DISCLOSURE line - the one line it has (see the grammar above).
  // The disclosure trigger is a <button>, so the control cannot nest inside
  // it. The intent-only variant therefore uses a full-line overlay trigger;
  // its visible content, the control, and the aria-hidden chevron are valid
  // siblings in exactly that visual order. data-intent-trailing is the
  // stylesheet hook that keeps all three on line 1.
  const intentLineTrailing =
    hasIntent && !hasSummary && anchorSplit === undefined && trailing !== undefined && trailing !== null ? (
      <span className={CLASS.intentTrailing} data-testid="tool-row-intent-trailing">
        {trailing}
      </span>
    ) : null;
  const showIntentTrailing = intentLineTrailing !== null;
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
      {failureNode}
      {statusNode}
      {hasIntent && (
        <span className={CLASS.intent} data-testid="tool-row-intent">
          {statedIntent}
          {chevron}
        </span>
      )}
      {hasSummary && (
        <span
          className={`${CLASS.summary}${monoSummary ? ` ${CLASS.mono}` : ""}${
            hasIntent ? ` ${CLASS.demoted}${expanded ? "" : ` ${CLASS.clamped}`}` : ""
          }`}
          data-testid="tool-row-summary"
          title={hasIntent ? summary : undefined}
        >
          {hasIntent && !expanded ? (
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
          {!hasIntent && chevron}
          {/* Affordances ride the TOOL-CALL line (see the grammar above):
              inline at the end of the summary text, so with an intent present
              they sit on the demoted second line - not the rationale line.
              Skipped when anchorSplit already placed the control mid-summary. */}
          {hasIntent && trailing && anchorSplit === undefined ? (
            <span className={CLASS.summaryTrailing}>{trailing}</span>
          ) : null}
        </span>
      )}
      {!hasIntent && !hasSummary && chevron}
      {(!hasIntent || !hasSummary) && anchorSplit === undefined ? trailing : null}
    </>
  );

  if (!expandable) {
    return (
      <div className={CLASS.row} data-testid="tool-row" data-intent={hasIntent ? "true" : undefined} title={title}>
        {content}
      </div>
    );
  }

  const summaryContent = (
    <>
      {hasSummary && (
        <span
          className={`${CLASS.summary}${monoSummary ? ` ${CLASS.mono}` : ""}${
            hasIntent ? ` ${CLASS.demoted}${expanded ? "" : ` ${CLASS.clamped}`}` : ""
          }`}
          data-testid="tool-row-summary"
          title={hasIntent ? summary : undefined}
        >
          {hasIntent && !expanded ? (
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
          {hasIntent && trailing && anchorSplit === undefined ? (
            <span className={CLASS.summaryTrailing}>{trailing}</span>
          ) : null}
        </span>
      )}
      {/* Rows with no intent trail the control at the summary line's end. An
          intent-only row never reaches this fallback: its control rides the
          disclosure line via intentLineTrailing (the summaryLine div below
          only mounts when there is no such slot). */}
      {!hasIntent && anchorSplit === undefined ? trailing : null}
    </>
  );
  // The accessible name for a body disclosure trigger: the failure prefix
  // (if failed), the summary text (if any), or a bare "Tool call" fallback.
  // Used by both the intent-less overlay trigger and the two-level body
  // chevron (.bodyTrigger).
  const summaryLabel = [
    failed ? "Failed" : undefined,
    hasSummary ? summary : undefined,
    !hasSummary && !failed ? "Tool call" : undefined,
  ]
    .filter((part): part is string => part !== undefined)
    .join(" ");
  // The .bodyTrigger chevron button: a separate disclosure for the body that
  // coexists with the intent button's summary disclosure in two-level mode.
  // On the summary line it is an OVERLAY (absolute, full width/height) so the
  // entire summary line is clickable to toggle the body — same pattern as the
  // intent-less overlay trigger. The chevron rides at the end as a visual
  // indicator. On the intent line (summary hidden, body expanded) it is a
  // normal flex item beside the intent button.
  const bodyTriggerButton = twoLevel ? (
    <button
      type="button"
      className={CLASS.bodyTrigger}
      data-testid="tool-row-body-trigger"
      aria-expanded={expanded}
      aria-controls={disclosureBodyId}
      aria-label={summaryLabel}
      onClick={() => onToggle?.()}
    >
      <span className={CLASS.chevron} aria-hidden="true" data-open={expanded ? "true" : "false"}>
        <Chevron />
      </span>
    </button>
  ) : null;
  // When the summary is not visible but the body is expanded, the body
  // chevron rides the intent line (data-intent-trailing makes the intent
  // button shrink to share it). When the summary is visible, the body
  // chevron rides the end of the summary line instead.
  const bodyTriggerOnIntentLine = twoLevel && !summaryVisible && expanded;
  const bodyTriggerOnSummaryLine = twoLevel && summaryVisible;

  // Intent button attributes differ between two-level and legacy modes.
  // In two-level mode the intent button controls the summary disclosure;
  // in legacy mode it controls the body disclosure directly.
  const triggerExpanded = twoLevel ? summaryVisible : expanded;
  const triggerControls = twoLevel ? (summaryVisible ? summaryRegionId : undefined) : disclosureBodyId;
  const triggerOnClick = twoLevel ? () => onToggleSummary?.() : () => onToggle?.();

  return (
    <div
      className={CLASS.row}
      data-testid="tool-row"
      data-intent={hasIntent ? "true" : undefined}
      data-intent-trailing={showIntentTrailing || bodyTriggerOnIntentLine ? "true" : undefined}
      data-body-trigger-intent={bodyTriggerOnIntentLine ? "true" : undefined}
      title={title}
    >
      {hasIntent && showIntentTrailing ? (
        <>
          <button
            type="button"
            className={`${CLASS.trigger} ${CLASS.intentOverlayTrigger}`}
            data-testid="tool-row-trigger"
            aria-expanded={triggerExpanded}
            aria-controls={triggerControls}
            aria-label={`${failed ? "Failed " : ""}${statedIntent}`}
            aria-describedby={hasStatus ? statusId : undefined}
            onClick={triggerOnClick}
          />
          <span className={CLASS.intentTriggerContent} data-testid="tool-row-intent-trigger-content">
            {iconNode}
            {failureNode}
            {statusNode}
            <span className={CLASS.intent} data-testid="tool-row-intent">
              {statedIntent}
            </span>
          </span>
          {intentLineTrailing}
          {chevron}
        </>
      ) : hasIntent ? (
        <button
          type="button"
          className={CLASS.trigger}
          data-testid="tool-row-trigger"
          aria-expanded={triggerExpanded}
          aria-controls={triggerControls}
          onClick={triggerOnClick}
        >
          {iconNode}
          {failureNode}
          {statusNode}
          <span className={CLASS.intent} data-testid="tool-row-intent">
            {statedIntent}
            {chevron}
          </span>
        </button>
      ) : (
        <>
          {iconNode}
          {failureNode}
          {statusNode}
        </>
      )}
      {!showIntentTrailing && intentLineTrailing}
      {bodyTriggerOnIntentLine && bodyTriggerButton}
      {!hasIntent && <div className={CLASS.summaryLine}>{summaryContent}</div>}
      {!hasIntent && (
        <button
          type="button"
          className={CLASS.trigger}
          data-testid="tool-row-trigger"
          aria-expanded={expanded}
          aria-controls={disclosureBodyId}
          aria-label={summaryLabel}
          onClick={() => onToggle?.()}
        >
          {chevron}
        </button>
      )}
      {hasIntent && twoLevel && bodyTriggerOnSummaryLine && (
        <div id={summaryRegionId} className={CLASS.summaryLine} data-body-trigger="true">
          {bodyTriggerButton}
          {summaryContent}
        </div>
      )}
      {hasIntent && !twoLevel && !showIntentTrailing && <div className={CLASS.summaryLine}>{summaryContent}</div>}
    </div>
  );
}
