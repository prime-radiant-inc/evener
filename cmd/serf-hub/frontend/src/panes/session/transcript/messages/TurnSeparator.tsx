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
import type { TurnModel } from "../../../../protocol/model";
import { usePrefsStore } from "../../../../stores/prefs";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { turnMetaParts } from "./turnMeta";
import styles from "./turnseparator.module.css";

const CLASS = {
  row: requireClass(styles.row, "turnseparator.module.css", "row"),
};

export interface TurnSeparatorProps {
  turn: TurnModel;
}

export function TurnSeparator({ turn }: TurnSeparatorProps) {
  const showTimings = usePrefsStore((s) => s.transcript.roundTimings);
  const showTokens = usePrefsStore((s) => s.transcript.tokenCounts);
  const showCost = usePrefsStore((s) => s.showCost);
  const parts = turnMetaParts(turn);
  const segments = [
    showTimings ? parts.duration : undefined,
    showTokens ? parts.tokens : undefined,
    showCost ? parts.cost : undefined,
  ].filter((s): s is string => Boolean(s));
  // A turn with none of the three enabled or none of the three present yet
  // (still in progress, or a source that never reports any of them) shows
  // nothing rather than an empty row - "fields may be absent" per the wave-4
  // binding constraints, never a fabricated placeholder. Filtering before
  // the join is also what keeps a suppressed middle segment from leaving a
  // doubled " · ".
  if (segments.length === 0) return null;
  return (
    <div className={CLASS.row} data-testid="turn-separator">
      {segments.join(" · ")}
    </div>
  );
}
