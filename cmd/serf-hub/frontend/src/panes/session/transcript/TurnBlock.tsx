// TurnBlock is the turn skeleton VirtualList windows over: it renders one
// turn's items, in wire order, each through the item-renderer registry
// (types.ts's itemRendererFor - a raw fallback for every type without a
// dedicated renderer yet). The side-effect import below registers
// ToolCallItem for "commandExecution" items the moment TurnBlock itself is
// ever imported, regardless of what else the app happens to have loaded -
// the real SessionPane composition must never depend on import ORDER to
// get tool calls rendered correctly.
import "./ToolCallItem";
import { TurnSeparator } from "./messages";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { itemRendererFor } from "./types";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./turnblock.module.css";

export interface TurnBlockProps {
  turn: TurnModel;
}

const CLASS = {
  turn: requireClass(styles.turn, "turnblock.module.css", "turn"),
};

// isItemLive is the per-item liveness signal every item renderer receives
// as `live` (ItemRenderProps.live): wire-accurate against the
// item/started -> item/completed status transition ("inProgress" ->
// "completed"/"failed"/...; reducer.ts's wireItemToModel carries the wire
// item's own `status` straight through). Exported for direct testing.
//
// Known wire gap this deliberately does NOT work around: a reasoning item
// never receives a live item/completed at all (no notification case emits
// one - only a full turn/completed settles it), so it reads as "live" for
// the whole rest of the turn once started. That's a per-type liveness
// nuance for T2's think-block renderer to address if needed; this generic
// signal stays a direct, honest reflection of the wire's own status field.
export function isItemLive(item: ItemModel): boolean {
  return item.status === "inProgress";
}

export function TurnBlock({ turn }: TurnBlockProps) {
  return (
    <div className={CLASS.turn} data-testid="turn-block" data-turn-id={turn.id}>
      {turn.items.map((item) => {
        const ItemRenderer = itemRendererFor(item.type);
        return <ItemRenderer key={item.id} item={item} turn={turn} live={isItemLive(item)} />;
      })}
      <TurnSeparator turn={turn} />
    </div>
  );
}
