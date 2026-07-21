// TurnSeparator: a compact, ink-mid row summarizing one turn's real
// server-measured timing/usage/cost. Deliberately NOT registered via
// registerItemRenderer - the locked ItemRenderProps registry dispatches by
// ItemModel.type, but a turn separator is a per-TURN concept (it takes a
// whole TurnModel, not one item), so there is no item type to register it
// under. TurnBlock.tsx (T1-owned) needs one line added at merge to actually
// place this in the tree - see the wave-4 T2 report for the exact wiring.
import type { TurnModel } from "../../../../protocol/model";
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
  const parts = turnMetaParts(turn);
  const segments = [parts.duration, parts.tokens, parts.cost].filter((s): s is string => Boolean(s));
  // A turn with none of the three yet (still in progress, or a source that
  // never reports any of them) shows nothing rather than an empty row -
  // "fields may be absent" per the wave-4 binding constraints, never a
  // fabricated placeholder.
  if (segments.length === 0) return null;
  return (
    <div className={CLASS.row} data-testid="turn-separator">
      {segments.join(" · ")}
    </div>
  );
}
