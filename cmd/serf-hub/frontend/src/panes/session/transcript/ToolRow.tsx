// ToolRow is THE tool-call row. Every tool renderer composes this one
// component, so no renderer owns a layout of its own — a descriptor decides
// only its own CONTENT (what the verb/target is, what meta it shows, what its
// expanded body looks like), never how the row is arranged.
//
// THE ROW GRAMMAR, one line, in document order:
//
//     [✗ failure glyph?] [purpose] verb target [· meta] [affordances] [chevron?]
//
//   - the failure glyph appears ONLY on a failed call and reserves no space
//     otherwise (A2 — see the deliberate-inconsistency note below);
//   - the purpose is the agent's own stated reason for the call
//     (ItemModel.description) and LEADS, because it is the one part written for
//     a human; verb/target recede to a quiet mono line under it;
//   - verb/target/meta are one string the descriptor's summary() produced;
//   - affordances are trailing controls (e.g. "Open beside");
//   - the chevron appears only when there is something to expand.
//
// A row with no inline body is a single line: no reserved glyph column, no
// separate meta row, no chevron.
import type { ReactNode } from "react";
import { FailureGlyph } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./toolcallitem.module.css";

const CLASS = {
  row: requireClass(styles.row, "toolcallitem.module.css", "row"),
  purpose: requireClass(styles.purpose, "toolcallitem.module.css", "purpose"),
  summary: requireClass(styles.summary, "toolcallitem.module.css", "summary"),
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
}: ToolRowProps) {
  const statedPurpose = statedPurposeOf({ description: purpose });
  const hasPurpose = statedPurpose !== undefined;
  const content = (
    <>
      {/* Only failure earns a glyph, and a clean row reserves NO space for one.
          That is the OPPOSITE of the rail's signal gutter (shell/rail, which
          always reserves 6px) and it is deliberate: the rail needs one stable
          left edge down a long list of sibling rows, whereas a tool row sits
          inside flowing prose, where a blank reserved column reads as a stray
          indent. Different context, different answer. */}
      {failed && <FailureGlyph />}
      {hasPurpose && (
        <span className={CLASS.purpose} data-testid="tool-row-purpose">
          {statedPurpose}
        </span>
      )}
      <span className={hasPurpose ? `${CLASS.summary} ${CLASS.demoted}` : CLASS.summary} data-testid="tool-row-summary">
        {summary}
        {/* Nested rather than appended to `summary` itself: when a purpose is
            present the parent span is ALSO .demoted (--ink-low, below AA per
            design-system.md's own measured figures) - the duration needs its
            own color override so it never inherits that failing contrast just
            because it happens to share the summary's demoted line. */}
        {duration && (
          <span className={CLASS.duration} data-testid="tool-row-duration">
            {" · "}
            {duration}
          </span>
        )}
      </span>
      {trailing}
      {expandable && (
        <span
          className={CLASS.chevron}
          aria-hidden="true"
          data-open={expanded ? "true" : "false"}
          data-testid="tool-row-chevron"
        >
          ▸
        </span>
      )}
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
