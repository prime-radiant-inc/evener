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
//   being merged); EXPANDED it wraps
//   in full, so an open row always shows the whole call. The one exception:
//   a descriptor whose expanded body already shows the summary's content
//   (shell - the body renders the command pretty-printed) swaps line 2 for
//   the descriptor's placeholder while open (the caller passes
//   summaryWhenExpanded as the summary text), so the call never appears
//   twice and the chevron keeps its line.
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
//     the words it opens, glued to the final word in an atomic .intentTail
//     unit so it can never wrap to a line by itself. The expanded summary
//     line glues its own trailing glyphs (the "Open beside" control, the
//     two-level body chevron) the same way, in a .summaryTail unit. The
//     intent-only Open variant is the exception: its valid sibling order is
//     intent, Open, chevron (never Open beyond the disclosure arrow), with
//     one overlay trigger owning the whole line;
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
import { Fragment, type ReactNode, useId, useMemo } from "react";
import { Chevron, FailureGlyph, ToolIcon, type ToolIconKind } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { EntityRef } from "./EntityRef";
import { type EntityTextSegment, type ProtectedSpan, segmentEntityIds } from "./entitySegments";
import { splitTrailingWord } from "./tailWord";
import styles from "./toolcallitem.module.css";

const CLASS = {
  row: requireClass(styles.row, "toolcallitem.module.css", "row"),
  trigger: requireClass(styles.trigger, "toolcallitem.module.css", "trigger"),
  intentTriggerContent: requireClass(styles.intentTriggerContent, "toolcallitem.module.css", "intentTriggerContent"),
  intentLine: requireClass(styles.intentLine, "toolcallitem.module.css", "intentLine"),
  intentOverlayTrigger: requireClass(styles.intentOverlayTrigger, "toolcallitem.module.css", "intentOverlayTrigger"),
  summaryLine: requireClass(styles.summaryLine, "toolcallitem.module.css", "summaryLine"),
  intent: requireClass(styles.intent, "toolcallitem.module.css", "intent"),
  intentTail: requireClass(styles.intentTail, "toolcallitem.module.css", "intentTail"),
  intentTailText: requireClass(styles.intentTailText, "toolcallitem.module.css", "intentTailText"),
  summaryTail: requireClass(styles.summaryTail, "toolcallitem.module.css", "summaryTail"),
  summaryTailText: requireClass(styles.summaryTailText, "toolcallitem.module.css", "summaryTailText"),
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
  /** Whether the row has any summary TEXT at all, independent of whether its
   * summary line is currently open. A collapsed two-level row passes an empty
   * `summary` string while the text still exists, so `summary`'s own presence
   * cannot answer this. Defaults to the passed `summary`'s presence for direct
   * callers. A summary-less intent-bearing row (a delegate, whose presentation
   * subagentModule owns) has nothing for the intent button to disclose: it
   * stays single-level, with the body trigger on the intent line, so no empty
   * summary region is ever mounted (#1253). */
  hasSummaryText?: boolean;
}

/** The atomic tail unit both glyph-bearing lines render through: the line's
 * final ATOMIC segment - a text run's final word (split out by
 * splitTrailingWord) or a whole entity id, already atomic by the segment
 * model's own rule - and the glyphs that trail it ride inside ONE
 * inline-flex unit, so a line that fills exactly moves the whole unit -
 * never a glyph alone onto a wrapped line of its own (the same mechanism as
 * NotificationCard's .secondaryTail; see the grammar above and the
 * stylesheet). `line` picks the name pair its markup reads:
 * .intentTail/.intentTailText on the intent line, .summaryTail/
 * .summaryTailText on the expanded summary line. Everything before the final
 * segment renders as it would anywhere else (summarySegmentNodes); within a
 * final TEXT segment the head and the final word linkify independently (one
 * occurrence each, as linkifySummary's contract pins): the split lands on
 * whitespace, and a summaryLink is a URL - whitespace-free - so no link can
 * straddle the boundary and be lost. A descriptor that repeated the href on
 * both sides of the boundary would mark one occurrence per half; no
 * descriptor does - web_fetch's summary carries the URL exactly once
 * (webTools.test.tsx pins the rule). */
function TailUnit({
  line,
  segments,
  summaryLink,
  children,
}: {
  line: "intent" | "summary";
  segments: SummarySegment[];
  summaryLink?: string;
  children?: ReactNode;
}) {
  const [unitClass, textClass] =
    line === "intent" ? [CLASS.intentTail, CLASS.intentTailText] : [CLASS.summaryTail, CLASS.summaryTailText];
  const last = segments.at(-1);
  // No text to glue to: render the glyphs bare rather than dropping them.
  // No call site passes an empty list today (the intent's one synthetic
  // segment, hasSummary's non-empty summary, the anchor arms' own gates),
  // and a future one must never lose the chevron or control to it.
  if (last === undefined) return <>{children}</>;
  const head = segments.slice(0, -1);
  if (last.kind === "entity") {
    // The final segment is a whole entity card: the unit wraps it and the
    // glyphs together - an id is the atomic unit already, no word split.
    return (
      <>
        {summarySegmentNodes(head, summaryLink)}
        <span className={unitClass}>
          <EntityRef id={last.id} embedded />
          {children}
        </span>
      </>
    );
  }
  const [headText, word] = splitTrailingWord(last.text);
  return (
    <>
      {summarySegmentNodes(head, summaryLink)}
      {headText !== "" ? linkifySummary(headText, summaryLink) : null}
      <span className={unitClass}>
        <span className={textClass}>{linkifySummary(word, summaryLink)}</span>
        {children}
      </span>
    </>
  );
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

/** A summary renders as an ordered sequence of ATOMIC segments: plain text
 * runs, which the collapsed line may split, and entity ids, which it may not.
 * An id is ONE `<EntityRef>` node wherever it lands - never a pair of text runs
 * straddling the head/tail clamp, and never clipped (a clipped id with an
 * ellipsis inside it is not even detectable as an id, so no card could attach
 * to it). The segmentation itself is shared with EntityText (entitySegments);
 * each caller maps its segments to its own nodes. */
type SummarySegment = EntityTextSegment;

/** The FIRST-occurrence span of the summary's URL - the one and only
 * occurrence linkifySummary links. Entity segmentation protects exactly this
 * span (summarySegments below), and linkifySummary renders from it, so the
 * two can never disagree about which occurrence is the link. */
function linkSpan(text: string, href: string | undefined): ProtectedSpan | undefined {
  if (href === undefined) return undefined;
  const start = text.indexOf(href);
  return start === -1 ? undefined : { start, end: start + href.length };
}

/** Splits a summary string into its text and entity segments, in order. The
 * summary's URL, when there is one, is a span entity segmentation must keep
 * WHOLE: a hub URL can embed one of the session's own entity ids
 * (".../jobs/job_<id>/log"), and an id-shaped substring inside the URL would
 * otherwise split the URL across segments - no single segment would hold the
 * whole URL, linkifySummary's whole-href match would find nothing in any
 * fragment, and the link would silently drop. The id inside the URL
 * therefore stays plain text: a card trigger nested inside the link it
 * lives in would open a card from inside the very link the row is drawing. */
function summarySegments(text: string, summaryLink: string | undefined): SummarySegment[] {
  return segmentEntityIds(text, linkSpan(text, summaryLink));
}

/** A segment's length in code POINTS, never UTF-16 units: a cut through a
 * surrogate pair would render a replacement glyph (the split's own rule). An
 * id is pure ASCII, so this is its character count. */
function segmentLength(segment: SummarySegment): number {
  return Array.from(segment.kind === "entity" ? segment.id : segment.text).length;
}

/** One entity id's span within the summary, in code POINTS. */
interface EntitySpan {
  start: number;
  end: number;
}

/** Each entity's code-point span, so a cut can be kept out of every one. */
function entitySpans(segments: SummarySegment[]): EntitySpan[] {
  const spans: EntitySpan[] = [];
  let offset = 0;
  for (const segment of segments) {
    const length = segmentLength(segment);
    if (segment.kind === "entity") spans.push({ start: offset, end: offset + length });
    offset += length;
  }
  return spans;
}

/** Every cut input for one segmented summary, derived once and carried
 * together: the summary's code-point characters, their total, and each
 * entity's span. The cuts below read these instead of re-walking the segments,
 * and no caller derives any of them a second time. */
interface SummaryMeasure {
  chars: string[];
  total: number;
  spans: EntitySpan[];
}

function measureSummary(text: string, segments: SummarySegment[]): SummaryMeasure {
  const chars = Array.from(text);
  return { chars, total: chars.length, spans: entitySpans(segments) };
}

/** The one span a cut falls STRICTLY inside, if any: a cut on a span's edge is
 * already outside that id, and id spans never overlap (findEntityIds skips
 * past each id it matches). */
function spanContaining(spans: EntitySpan[], cut: number): EntitySpan | undefined {
  return spans.find((span) => span.start < cut && cut < span.end);
}

/** The collapsed second line's middle-truncation cut (in code points): head
 * gets ~60% of the characters and ellipsis-clamps under pressure; the tail
 * always renders in full, because a command's ENDING is the part end-truncation
 * kept hiding (the file being written, the branch being merged).
 *
 * Two invariants hold the segments together. The cut never sits ADJACENT to
 * whitespace: the head and tail render as separate flex items (.clamped is
 * display:flex), and CSS white-space processing removes whitespace at a flex
 * item's line edges, so a raw 60% cut through "Ran go test ./..." leaves the
 * space at the tail's start and the browser renders "Ran go test./...". Walk
 * the cut left across any boundary whitespace so every space stays INTERIOR to
 * one span and survives. And the cut never lands INSIDE an entity id: when the
 * 60% target falls within one, the cut snaps to the id's nearer edge (ties to
 * its start) so the whole id stays on one side as one node. An edge that would
 * empty the head or the tail is never chosen - a one-sided split is not a
 * middle truncation. */
function middleTruncationCut(measure: SummaryMeasure): number {
  const { total, spans } = measure;
  let cut = Math.ceil(total * 0.6);
  const straddled = spanContaining(spans, cut);
  if (straddled) {
    const preferStart = cut - straddled.start <= straddled.end - cut && straddled.start > 0;
    // A snap to the id's end would empty the tail, so the start is then the
    // only cut left; ties and a start with no head snap there too.
    cut = preferStart || straddled.end >= total ? straddled.start : straddled.end;
  }
  return cut;
}

/** Walks an entity-safe cut left off any boundary whitespace - and, if that
 * walk enters an id, back to that id's start - so no character the browser
 * would collapse sits at a span edge and no id is ever split. */
function walkOffWhitespace(measure: SummaryMeasure, cut: number): number {
  const { chars, total, spans } = measure;
  const isSpace = (index: number) => /\s/.test(chars[index] ?? "");
  let position = cut;
  while (position > 0 && position < total) {
    const inside = spanContaining(spans, position);
    if (inside) {
      position = inside.start;
      continue;
    }
    if (isSpace(position - 1) || isSpace(position)) {
      position -= 1;
      continue;
    }
    break;
  }
  return position;
}

/** The collapsed line's middle-truncation cut over atomic segments: ~60% of the
 * code points, never inside an id and never adjacent to whitespace. */
function middleSplit(measure: SummaryMeasure): number {
  return walkOffWhitespace(measure, middleTruncationCut(measure));
}

/** Splits the segment list at a code-point boundary, preserving order. The cut
 * is kept entity-safe by the callers, so an entity segment never splits; a text
 * segment splits by code point. */
function splitSegments(segments: SummarySegment[], cut: number): [SummarySegment[], SummarySegment[]] {
  const head: SummarySegment[] = [];
  const tail: SummarySegment[] = [];
  let offset = 0;
  for (const segment of segments) {
    const length = segmentLength(segment);
    const end = offset + length;
    if (end <= cut) {
      head.push(segment);
    } else if (offset >= cut) {
      tail.push(segment);
    } else {
      // Only ever a text segment: the cut is entity-safe.
      const chars = Array.from(segment.kind === "entity" ? segment.id : segment.text);
      const local = cut - offset;
      head.push({ kind: "text", text: chars.slice(0, local).join("") });
      tail.push({ kind: "text", text: chars.slice(local).join("") });
    }
    offset = end;
  }
  return [head, tail];
}

/** Snaps a cut forward out of an entity id. `trailingAfter` is a literal
 * PREFIX of the summary, so its end is normally a segment boundary already; a
 * descriptor that ends its anchor mid-id is a bug, and this keeps the id ONE
 * node anyway (extending to its end is the faithful reading - the anchor names
 * everything up to the id it opens). */
function entitySafeCut(measure: SummaryMeasure, cut: number): number {
  return spanContaining(measure.spans, cut)?.end ?? cut;
}

/** Renders a run of segments: each id as ONE embedded `<EntityRef>` (embedded
 * because it rides inside the row's own disclosure control, so it takes no tab
 * stop of its own - ruling R13), each text run with the summary's URL, if any,
 * made a real link. */
function summarySegmentNodes(segments: SummarySegment[], href: string | undefined): ReactNode {
  const nodes: ReactNode[] = [];
  let offset = 0;
  for (const segment of segments) {
    // The segment's own code-point offset is its identity: segments are derived
    // deterministically from the summary, and offsets never repeat.
    nodes.push(
      segment.kind === "entity" ? (
        <EntityRef key={`e${offset}`} id={segment.id} embedded />
      ) : (
        <Fragment key={`t${offset}`}>{linkifySummary(segment.text, href)}</Fragment>
      ),
    );
    offset += segmentLength(segment);
  }
  return nodes;
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
  const span = linkSpan(text, href);
  if (span === undefined) return text;
  return (
    <>
      {text.slice(0, span.start)}
      <a href={href} target="_blank" rel="noopener noreferrer">
        {text.slice(span.start, span.end)}
      </a>
      {text.slice(span.end)}
    </>
  );
}

/** The inline-affordance control's seat: the entity-safe cut (in code points)
 * it trails, and the segments split at it - everything the control follows,
 * and everything after it. */
interface AnchorSeat {
  cut: number;
  before: SummarySegment[];
  after: SummarySegment[];
}

/** Every cut one summary's rendering needs, all derived from the ONE
 * measurement: the collapsed line's middle-truncation cut, and the trailing
 * control's seat when `trailingAfter` anchors it. Each is absent exactly when
 * its render path does not exist. */
interface SummaryCuts {
  clampCut: number | undefined;
  anchor: AnchorSeat | undefined;
}

/** Seats the inline-affordance control at `cut` (code points), snapping out of
 * any id first so the id stays whole on one side of the control. */
function seatAnchor(segments: SummarySegment[], measure: SummaryMeasure, cut: number): AnchorSeat {
  const safeCut = entitySafeCut(measure, cut);
  const [before, after] = splitSegments(segments, safeCut);
  return { cut: safeCut, before, after };
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
  status,
  bodyId,
  summaryOpen = false,
  onToggleSummary,
  hasSummaryText,
}: ToolRowProps) {
  const generatedBodyId = useId();
  const disclosureBodyId = bodyId ?? generatedBodyId;
  const summaryRegionId = useId();
  const statedIntent = statedIntentOf({ description: intent });
  const hasIntent = statedIntent !== undefined;
  const hasSummary = summary.trim() !== "";
  // The summary TEXT exists even when the line is collapsed and `summary` is
  // blanked by the caller; callers that blank it pass hasSummaryText so the
  // layout can tell "collapsed" from "nothing to show" (#1253).
  const summaryTextPresent = hasSummaryText ?? hasSummary;
  // Two-level disclosure is opt-in via onToggleSummary, and only applies to
  // intent-bearing rows. Intent-less rows keep the legacy overlay pattern
  // (one trigger controls the body) regardless of which props are passed.
  const twoLevel = hasIntent && onToggleSummary !== undefined;
  // There is only a summary line to disclose when there is summary TEXT. A
  // summary-less intent-bearing row (a delegate, subagentModule owns its
  // presentation) has nothing for the intent button to open: it stays
  // single-level - one body control and one chevron - so no empty summary
  // region is ever mounted, however the caller's summaryOpen prop defaults
  // (#1253). The delegate card depends on the body trigger, not the summary
  // region, so this keeps the card reachable without the stray line.
  const summaryToggle = twoLevel && summaryTextPresent;
  // On such a summary-less two-level row the intent button would be a second
  // control for the very same body disclosure (and a second chevron beside
  // it). It is suppressed: the .bodyTrigger is the row's one semantic body
  // control, and its chevron the one visible chevron (#1253 review).
  const intentControlSuppressed = twoLevel && !summaryToggle;
  // `status` is typed ReactNode, so it admits values that render nothing and
  // carry no accessible name - null, undefined, false (the common
  // `condition && <Node/>` idiom) - alongside a real status node. Only a
  // value that can actually render counts as "status present" for both the
  // wrapping span below and the aria-label fallback gate: treating null or
  // false as present would render an empty span and, on the expandable
  // branch, suppress the fallback label with nothing left to name the row.
  const hasStatus = status !== undefined && status !== null && status !== false;
  const hasTrailing = trailing !== undefined && trailing !== null;
  // The inline-affordance anchor (trailingAfter): the code-point cut the
  // trailing control rides at, so it sits BETWEEN the text up to the anchor's
  // end and the rest. Undefined when there is no trailing control, no anchor,
  // or the anchor is not a literal PREFIX of summary.
  //
  // This is a prefix check (startsWith), never a substring search: searching
  // for the anchor anywhere in summary is ambiguous whenever the anchor text
  // recurs elsewhere, in EITHER direction - an earlier coincidental match can
  // win just as easily as a later one (e.g. read_file's own summary always
  // contains the literal word "lines" in its meta suffix, so a file named
  // "lines" collides with it). A from-the-start prefix has no direction left
  // to be ambiguous in: the caller supplies the complete prefix it means, not
  // a fragment ToolRow has to go find (kata ledger #97).
  const anchorCut = useMemo((): number | undefined => {
    if (!hasTrailing || trailingAfter === undefined) return undefined;
    if (!summary.startsWith(trailingAfter)) return undefined;
    return Array.from(trailingAfter).length;
  }, [hasTrailing, summary, trailingAfter]);
  // The summary decomposed into atomic segments (text runs and entity ids), so
  // both the middle-truncation split and the inline-affordance anchor can place
  // their cuts BETWEEN segments - an id is one node on one side, never split.
  // Memoized on `summary`, all it reads: live rows re-render per item update,
  // and this segmentation is the row's expensive derivation.
  const segments = useMemo(() => summarySegments(summary, summaryLink), [summary, summaryLink]);
  // The cuts the summary's own rendering needs, derived in ONE measurement.
  // Only the paths that cut pay for it: a row that neither clamps (a no-intent
  // or expanded row) nor anchors a trailing control reads the full summary.
  const needsClampedSummary = hasIntent && !expanded;
  const cuts = useMemo((): SummaryCuts | undefined => {
    if (!needsClampedSummary && anchorCut === undefined) return undefined;
    const measure = measureSummary(summary, segments);
    return {
      clampCut: needsClampedSummary ? middleSplit(measure) : undefined,
      anchor: anchorCut === undefined ? undefined : seatAnchor(segments, measure, anchorCut),
    };
  }, [anchorCut, needsClampedSummary, segments, summary]);
  const clip = cuts?.clampCut;
  const anchor = cuts?.anchor;
  // The chevron rides INLINE at the end of the headline text (see the grammar
  // above): inside the intent when there is one, otherwise inside the summary.
  // The intent-only Open form moves it after the sibling control below so Open
  // can never land on the far side of the disclosure arrow.
  const chevron = expandable ? (
    <span
      className={CLASS.chevron}
      aria-hidden="true"
      data-open={(summaryToggle ? summaryOpen : expanded) ? "true" : "false"}
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
    hasIntent && !hasSummary && anchor === undefined && hasTrailing ? (
      <span className={CLASS.intentTrailing} data-testid="tool-row-intent-trailing">
        {trailing}
      </span>
    ) : null;
  const showIntentTrailing = intentLineTrailing !== null;
  // The collapsed second line's middle-truncation, WITH the inline-affordance
  // variant: when the anchor seats the trailing control mid-summary, the
  // control becomes a flex item of the clamped line between the anchor's end
  // and the remaining meta (read_file: ".../sheet.test.tsx [open] · lines
  // 1-260"). An anchor ending inside the clamped head puts the control right
  // after the head; one ending inside the tail (a long path spans the
  // truncation cut) keeps the path's visible tail whole and puts the control
  // between it and the meta. Either way every character renders exactly once.
  // A row that discards the clamp (no intent, or expanded) returns above
  // without deriving any of this.
  const clampedSummary = ((): ReactNode => {
    if (clip === undefined) return null;
    const [headSegments, tailSegments] = splitSegments(segments, clip);
    if (anchor === undefined) {
      return (
        <>
          <span className={CLASS.clampedHead} data-testid="tool-row-summary-head">
            {summarySegmentNodes(headSegments, undefined)}
          </span>
          <span className={CLASS.clampedTail} data-testid="tool-row-summary-tail">
            {summarySegmentNodes(tailSegments, undefined)}
          </span>
        </>
      );
    }
    if (anchor.cut <= clip) {
      return (
        <>
          <span className={CLASS.clampedHead} data-testid="tool-row-summary-head">
            {summarySegmentNodes(anchor.before, undefined)}
          </span>
          <span className={CLASS.summaryTrailing} data-testid="tool-row-trailing">
            {trailing}
          </span>
          <span className={`${CLASS.clampedTail} ${CLASS.summaryMeta}`} data-testid="tool-row-summary-tail">
            {summarySegmentNodes(anchor.after, undefined)}
          </span>
        </>
      );
    }
    // The anchor sits past the truncation cut: the path/id the control opens
    // stays whole in the tail, and the control rides between it and the meta.
    const [midSegments, metaSegments] = splitSegments(tailSegments, anchor.cut - clip);
    return (
      <>
        <span className={CLASS.clampedHead} data-testid="tool-row-summary-head">
          {summarySegmentNodes(headSegments, undefined)}
        </span>
        <span className={CLASS.clampedTail} data-testid="tool-row-summary-tail">
          {summarySegmentNodes(midSegments, undefined)}
        </span>
        <span className={CLASS.summaryTrailing} data-testid="tool-row-trailing">
          {trailing}
        </span>
        <span className={CLASS.summaryMeta} data-testid="tool-row-summary-meta">
          {summarySegmentNodes(metaSegments, undefined)}
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
        >
          {hasIntent && !expanded ? (
            clampedSummary
          ) : anchor !== undefined ? (
            <>
              {summarySegmentNodes(anchor.before, summaryLink)}
              <span className={CLASS.summaryTrailing} data-testid="tool-row-trailing">
                {trailing}
              </span>
              {summarySegmentNodes(anchor.after, summaryLink)}
            </>
          ) : (
            summarySegmentNodes(segments, summaryLink)
          )}
          {!hasIntent && chevron}
          {/* Affordances ride the TOOL-CALL line (see the grammar above):
              inline at the end of the summary text, so with an intent present
              they sit on the demoted second line - not the rationale line.
              Skipped when the anchor already placed the control mid-summary. */}
          {hasIntent && trailing && anchor === undefined ? (
            <span className={CLASS.summaryTrailing}>{trailing}</span>
          ) : null}
        </span>
      )}
      {!hasIntent && !hasSummary && chevron}
      {(!hasIntent || !hasSummary) && anchor === undefined ? trailing : null}
    </>
  );

  if (!expandable) {
    return (
      <div className={CLASS.row} data-testid="tool-row" data-intent={hasIntent ? "true" : undefined}>
        {content}
      </div>
    );
  }

  // When the summary line is collapsed (summaryOpen=false) but the body is
  // expanded, the body chevron rides the intent line (data-intent-trailing
  // makes the intent button shrink to share it). When the summary line is
  // open, the body chevron rides INLINE at the end of the summary text - a
  // sibling of the words it opens, never sprung to the line's far edge -
  // while the .bodyTrigger button stays behind it as an empty full-line
  // overlay so the whole line still toggles the body.
  //
  // A summary-less row has no summary line at all, so its body trigger always
  // rides the intent line - folded or open - or the reader could not reach
  // the body (#1253).
  const bodyTriggerOnSummaryLine = summaryToggle && summaryOpen;
  const bodyTriggerOnIntentLine = twoLevel && !bodyTriggerOnSummaryLine && (summaryTextPresent ? expanded : true);
  // The inline slot exists only when there is summary text to carry the
  // chevron; a summary-less line keeps the chevron inside the button.
  const bodyChevronInline = bodyTriggerOnSummaryLine && hasSummary;
  const bodyChevron = (
    <span
      className={CLASS.chevron}
      aria-hidden="true"
      data-open={expanded ? "true" : "false"}
      data-testid="tool-row-body-chevron"
    >
      <Chevron />
    </span>
  );

  const summaryContent = (
    <>
      {hasSummary && (
        <span
          className={`${CLASS.summary}${monoSummary ? ` ${CLASS.mono}` : ""}${
            hasIntent ? ` ${CLASS.demoted}${expanded ? "" : ` ${CLASS.clamped}`}` : ""
          }`}
          data-testid="tool-row-summary"
        >
          {hasIntent && !expanded ? (
            // Collapsed: the middle-truncating clamp is a nowrap flex line
            // and the trailing control / body chevron ride it as flex items,
            // so nothing here can wrap - no glue needed.
            <>
              {clampedSummary}
              {hasTrailing && anchor === undefined ? <span className={CLASS.summaryTrailing}>{trailing}</span> : null}
              {bodyChevronInline ? bodyChevron : null}
            </>
          ) : anchor !== undefined ? (
            // Expanded with the control anchored mid-summary (read_file):
            // the control trails the file path, whose wrap point always
            // carries the following meta onto the same line, so it has no
            // stranding geometry of its own and stays bare; the body chevron
            // trails the LAST text, so it glues to that text's final word.
            // A summary ending at the anchor itself (a binary read: no
            // lines meta) puts the control at the end of the text, where it
            // strands together with the chevron exactly like the plain
            // variant below - so there the anchor text's final word, the
            // control, and the chevron are ONE unit.
            anchor.after.length === 0 ? (
              <TailUnit line="summary" segments={anchor.before} summaryLink={summaryLink}>
                <span className={CLASS.summaryTrailing} data-testid="tool-row-trailing">
                  {trailing}
                </span>
                {bodyChevronInline ? bodyChevron : null}
              </TailUnit>
            ) : (
              <>
                {summarySegmentNodes(anchor.before, summaryLink)}
                <span className={CLASS.summaryTrailing} data-testid="tool-row-trailing">
                  {trailing}
                </span>
                {bodyChevronInline ? (
                  <TailUnit line="summary" segments={anchor.after} summaryLink={summaryLink}>
                    {bodyChevron}
                  </TailUnit>
                ) : (
                  summarySegmentNodes(anchor.after, summaryLink)
                )}
              </>
            )
          ) : hasIntent && (hasTrailing || bodyChevronInline) ? (
            // Expanded with the glyphs at the end of a plain (wrapping)
            // summary: glue them to the summary's final word. The
            // .bodyTrigger overlay button (below) carries the click target;
            // the chevron span sits inside the pointer-events-none
            // .summary, so it never double-hits.
            <TailUnit line="summary" segments={segments} summaryLink={summaryLink}>
              {hasTrailing ? <span className={CLASS.summaryTrailing}>{trailing}</span> : null}
              {bodyChevronInline ? bodyChevron : null}
            </TailUnit>
          ) : (
            summarySegmentNodes(segments, summaryLink)
          )}
        </span>
      )}
      {/* Rows with no intent trail the control at the summary line's end. An
          intent-only row never reaches this fallback: its control rides the
          disclosure line via intentLineTrailing (the summaryLine div below
          only mounts when there is no such slot). */}
      {!hasIntent && anchor === undefined ? trailing : null}
    </>
  );
  // The accessible name for a body disclosure trigger: the failure prefix
  // (if failed), the summary text (if any), else the stated intent ONLY on
  // the summary-less row whose intent control is suppressed, else a bare
  // "Tool call" fallback. Used by the intent-less overlay trigger and the
  // two-level body chevron (.bodyTrigger).
  //
  // The intent fallback is gated on intentControlSuppressed on purpose. A
  // NORMAL two-level row whose summary line is collapsed passes summary=""
  // while its summary TEXT still exists (hasSummaryText), and its body is
  // often expanded: there the intent overlay trigger is present and already
  // named from the intent (below), so naming the body trigger from the intent
  // too would put two adjacent, identically named buttons with different jobs
  // (toggle summary vs. toggle body) in front of a screen reader. Only the
  // bare summary-less row - one control, no intent trigger - takes the intent
  // name (#1253 review).
  const summaryLabel = [
    failed ? "Failed" : undefined,
    hasSummary ? summary : intentControlSuppressed && hasIntent ? statedIntent : undefined,
    !hasSummary && !(intentControlSuppressed && hasIntent) && !failed ? "Tool call" : undefined,
  ]
    .filter((part): part is string => part !== undefined)
    .join(" ");
  // The .bodyTrigger chevron button: a separate disclosure for the body that
  // coexists with the intent button's summary disclosure in two-level mode.
  // On the summary line it is an OVERLAY (absolute, full width/height) so the
  // entire summary line is clickable to toggle the body — same pattern as the
  // intent-less overlay trigger. Its chevron rides INLINE at the end of the
  // summary text instead (the span rendered inside .summary above), so it hugs
  // the words it opens; only a summary-less line keeps the chevron inside the
  // button. On the intent line (summary hidden, body expanded) it is a normal
  // flex item beside the intent button. Its accessible name comes from
  // summaryLabel (which falls back to the stated intent on a summary-less
  // row), and it names the status as its description the way the suppressed
  // intent trigger used to (#1253 review).
  const bodyTriggerButton = twoLevel ? (
    <button
      type="button"
      className={CLASS.bodyTrigger}
      data-testid="tool-row-body-trigger"
      aria-expanded={expanded}
      aria-controls={disclosureBodyId}
      aria-label={summaryLabel}
      aria-describedby={hasStatus ? statusId : undefined}
      onClick={() => onToggle?.()}
    >
      {bodyChevronInline ? null : bodyChevron}
    </button>
  ) : null;

  // Intent button attributes differ between two-level and legacy modes.
  // In two-level mode with a summary line the intent button controls the
  // summary disclosure; in legacy mode it controls the body disclosure
  // directly. (A summary-less two-level row renders no intent button at all -
  // its .bodyTrigger is the single control - so these never describe it.)
  const triggerExpanded = summaryToggle ? summaryOpen : expanded;
  const triggerControls = summaryToggle ? (summaryOpen ? summaryRegionId : undefined) : disclosureBodyId;
  const triggerOnClick = summaryToggle ? () => onToggleSummary?.() : () => onToggle?.();

  return (
    <div
      className={CLASS.row}
      data-testid="tool-row"
      data-intent={hasIntent ? "true" : undefined}
      data-intent-trailing={showIntentTrailing || bodyTriggerOnIntentLine ? "true" : undefined}
      data-body-trigger-intent={bodyTriggerOnIntentLine ? "true" : undefined}
      data-intent-suppressed={intentControlSuppressed ? "true" : undefined}
    >
      {hasIntent && showIntentTrailing ? (
        <>
          {/* A summary-less row keeps only its .bodyTrigger control (rendered
              below) - no second intent disclosure, no second chevron. */}
          {!intentControlSuppressed && (
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
          )}
          <span className={CLASS.intentTriggerContent} data-testid="tool-row-intent-trigger-content">
            {iconNode}
            {failureNode}
            {statusNode}
            <span className={CLASS.intent} data-testid="tool-row-intent">
              {statedIntent}
            </span>
          </span>
          {intentLineTrailing}
          {!intentControlSuppressed && chevron}
        </>
      ) : hasIntent && intentControlSuppressed ? (
        // The whole line - rail icon, status, and the bare intent - rides ONE
        // flex item so the reservation that keeps the body trigger on line 1
        // bounds the icon's full outer width too (see .intentLine). A bare
        // .intent direct child would start AFTER the icon, leaving the icon's
        // ~34px unbounded below the 700px breakpoint and wrapping the trigger
        // on a phone column (#1253 review).
        <span className={CLASS.intentLine} data-testid="tool-row-intent-line">
          {iconNode}
          {failureNode}
          {statusNode}
          <span className={CLASS.intent} data-testid="tool-row-intent">
            {statedIntent}
          </span>
        </span>
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
            <TailUnit line="intent" segments={[{ kind: "text", text: statedIntent ?? "" }]}>
              {chevron}
            </TailUnit>
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
