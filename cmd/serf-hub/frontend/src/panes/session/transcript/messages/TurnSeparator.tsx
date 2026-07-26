// TurnSeparator: a compact, ink-mid row summarizing one turn's real
// server-measured timing/usage/cost. Deliberately NOT registered via
// registerItemRenderer - the locked ItemRenderProps registry dispatches by
// ItemModel.type, but a turn separator is a per-TURN concept (it takes a
// whole TurnModel, not one item), so there is no item type to register it
// under. TurnBlock.tsx places it directly instead, as the last child of the
// turn it belongs to.
//
// Each of the three segments is opt-in, gated on its own preference
// (Settings -> Transcript's "Round timings"/"Token counts", Settings ->
// Display's "Show estimated cost"), all default off - so the transcript
// carries prose and tool calls by default and this row only appears for
// someone who asked for it. Read through usePrefsStore selectors so
// flipping a toggle in Settings re-renders the transcript live.
import { Fragment, type ReactNode } from "react";
import type { TurnModel } from "../../../../protocol/model";
import { usePrefsStore } from "../../../../stores/prefs";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { turnMetaParts } from "./turnMeta";
import styles from "./turnseparator.module.css";

const CLASS = {
  row: requireClass(styles.row, "turnseparator.module.css", "row"),
  duration: requireClass(styles.duration, "turnseparator.module.css", "duration"),
};

// t8nc: tokens self-label via "↑/↓", cost via "$" - duration alone was a
// bare number with no attachment, so a turn with ONLY "Round timings" on
// (the other two segments are each their own separate opt-in) rendered as
// a lone "10s" sitting on the line with nothing marking it as a duration.
// A small clock face (currentColor, same hand-drawn-SVG idiom as
// UserMessageItem's ForkGlyph) attaches it - contributing nothing to the
// row's own .textContent, so every wire-format assertion on this row's
// text stays exactly what it already was.
function DurationGlyph() {
  return (
    <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true">
      <circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" strokeWidth="1.3" />
      <line x1="8" y1="8" x2="8" y2="4.6" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
      <line x1="8" y1="8" x2="10.4" y2="9.6" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
    </svg>
  );
}

export interface TurnSeparatorProps {
  turn: TurnModel;
}

export function TurnSeparator({ turn }: TurnSeparatorProps) {
  const showTimings = usePrefsStore((s) => s.transcript.roundTimings);
  const showTokens = usePrefsStore((s) => s.transcript.tokenCounts);
  const showCost = usePrefsStore((s) => s.showCost);
  const parts = turnMetaParts(turn);
  const segments: { key: string; node: ReactNode }[] = [];
  if (showTimings && parts.duration) {
    segments.push({
      key: "duration",
      node: (
        <span className={CLASS.duration}>
          <DurationGlyph />
          {parts.duration}
        </span>
      ),
    });
  }
  if (showTokens && parts.tokens) segments.push({ key: "tokens", node: parts.tokens });
  if (showCost && parts.cost) segments.push({ key: "cost", node: parts.cost });
  // A turn with none of the three enabled or none of the three present yet
  // (still in progress, or a source that never reports any of them) shows
  // nothing rather than an empty row - "fields may be absent" per the wave-4
  // binding constraints, never a fabricated placeholder. Building the array
  // gated on both the pref AND the part being present is also what keeps a
  // suppressed middle segment from leaving a doubled " · ".
  if (segments.length === 0) return null;
  return (
    <div className={CLASS.row} data-testid="turn-separator">
      {segments.map(({ key, node }, i) => (
        <Fragment key={key}>
          {i > 0 && " · "}
          {node}
        </Fragment>
      ))}
    </div>
  );
}
