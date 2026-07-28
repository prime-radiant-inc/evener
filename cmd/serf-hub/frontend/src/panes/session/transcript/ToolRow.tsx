// ToolRow is THE tool-call row. Every tool renderer composes this one
// component, so no renderer owns a layout of its own — a descriptor decides
// only its own CONTENT (what the verb/target is, what meta it shows, what its
// expanded body looks like), never how the row is arranged.
//
// THE ROW GRAMMAR, two lines when a purpose exists (one otherwise):
//
//     line 1: [✗ failure glyph?] [status?] purpose [affordances] [chevron?]
//     line 2: verb target [· meta] [· duration]
//
//   - a COLLAPSED row with both a purpose and a summary STACKS them: the
//     purpose (the agent's stated rationale, italic) on the first line, the
//     verb/target summary demoted to a quiet mono second line,
//     ellipsis-clamped (full text on title). Composing both onto one line
//     was tried (tiered density) and reverted on review: two clamped,
//     truncated fragments read worse than one full line plus one clamped
//     one.
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
//     written for a human; verb/target recede to a quiet mono line under it;
//   - verb/target/meta are one string the descriptor's summary() produced;
//   - affordances are trailing controls (e.g. "Open beside").
//
// A row with no purpose is a single line: summary, then affordances, then
// the chevron if there is something to expand.
import type { ReactNode } from "react";
import { Chevron, FailureGlyph } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./toolcallitem.module.css";

const CLASS = {
  row: requireClass(styles.row, "toolcallitem.module.css", "row"),
  purpose: requireClass(styles.purpose, "toolcallitem.module.css", "purpose"),
  summary: requireClass(styles.summary, "toolcallitem.module.css", "summary"),
  status: requireClass(styles.status, "toolcallitem.module.css", "status"),
  demoted: requireClass(styles.demoted, "toolcallitem.module.css", "demoted"),
  duration: requireClass(styles.duration, "toolcallitem.module.css", "duration"),
  chevron: requireClass(styles.chevron, "toolcallitem.module.css", "chevron"),
};

export interface ToolRowProps {
  /** The descriptor's own one-line verb/target/meta string. */
  summary: string;
  /** The agent's stated reason for the call (ItemModel.description). Blank or
   * absent renders nothing at all — no placeholder, no empty separator. */
  purpose?: string;
  failed: boolean;
  expandable: boolean;
  expanded: boolean;
  onToggle?: () => void;
  /** Trailing controls (e.g. the "Open beside" button). */
  trailing?: ReactNode;
  /** Optional status rail content for tools that need a one-glance
   * progression/health signal before human-facing purpose text. */
  status?: ReactNode;
  /** Hover text for details that are real but must not be the headline — the
   * shell exit code, per A2. */
  title?: string;
  /** The call's wall-clock duration (toolMeta.ts's toolCallDuration, already
   * formatted, e.g. "38ms"/"1.2s") — opt-in via Settings -> Transcript's
   * "Round timings" (ToolCallItem gates it), the same preference
   * TurnSeparator reads for the turn-level figure. Absent whenever the pref
   * is off, the call hasn't settled yet, or the wire never stamped a
   * start/end pair. */
  duration?: string;
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

export function ToolRow({
  summary,
  purpose,
  failed,
  expandable,
  expanded,
  onToggle,
  trailing,
  title,
  duration,
  status,
}: ToolRowProps) {
  const statedPurpose = statedPurposeOf({ description: purpose });
  const hasPurpose = statedPurpose !== undefined;
  const hasSummary = summary.trim() !== "";
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
  const content = (
    <>
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
          className={hasPurpose ? `${CLASS.summary} ${CLASS.demoted}` : CLASS.summary}
          data-testid="tool-row-summary"
          title={hasPurpose ? summary : undefined}
        >
          {summary}
          {/* Nested rather than appended to `summary` itself: a stable hook to
            query the duration apart from the demoted summary text (both
            .demoted and .duration render --ink-mid; kata rdry moved .demoted
            off the sub-AA --ink-low it used to inherit). */}
          {duration && (
            <span className={CLASS.duration} data-testid="tool-row-duration">
              {" · "}
              {duration}
            </span>
          )}
          {!hasPurpose && chevron}
        </span>
      )}
      {!hasPurpose && !hasSummary && chevron}
      {trailing}
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
