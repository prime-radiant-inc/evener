// TurnBlock is the turn skeleton VirtualList windows over: it renders one
// turn's items, in wire order, each through the item-renderer registry
// (types.ts's itemRendererFor - a raw fallback for every type without a
// dedicated renderer yet). The side-effect import below registers
// ToolCallItem for "commandExecution" items the moment TurnBlock itself is
// ever imported, regardless of what else the app happens to have loaded -
// the real SessionPane composition must never depend on import ORDER to
// get tool calls rendered correctly.
import "./ToolCallItem";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { requireClass } from "../../../widgets/internal/requireClass";
import { TurnSeparator } from "./messages";
import { TurnFailureEndCap } from "./TurnFailureEndCap";
import styles from "./turnblock.module.css";
import { asTurnError } from "./turnFailure";
import { itemRendererFor } from "./types";

export interface TurnBlockProps {
  turn: TurnModel;
  // The owning session's ref, threaded from Session.tsx so the turn-failure
  // end-cap can wire its recovery action (re-issue the turn). Optional: the
  // diagnostic renders without it, only the recovery button is withheld until
  // Session.tsx passes it down.
  sessionRef?: string;
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

export function TurnBlock({ turn, sessionRef }: TurnBlockProps) {
  // A failed turn carries a TurnError (only genuine failures do - the projector
  // sets it alongside status "failed", never on a completed or user-cancelled
  // turn); its presence is the signal to close the turn with a diagnostic
  // end-cap, corroborated by the honest status "failed" the wire stamps.
  const failure = asTurnError(turn.error);
  return (
    <div className={CLASS.turn} data-testid="turn-block" data-turn-id={turn.id}>
      {turn.items.map((item) => {
        const ItemRenderer = itemRendererFor(item.type);
        return <ItemRenderer key={item.id} item={item} turn={turn} live={isItemLive(item)} sessionRef={sessionRef} />;
      })}
      {failure && <TurnFailureEndCap error={failure} turn={turn} sessionRef={sessionRef} />}
      <TurnSeparator turn={turn} />
    </div>
  );
}
