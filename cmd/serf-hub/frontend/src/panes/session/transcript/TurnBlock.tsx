// TurnBlock is the turn skeleton VirtualList windows over: it renders one
// turn's items, in wire order, each through the item-renderer registry
// (types.ts's itemRendererFor - a raw fallback for every type without a
// dedicated renderer yet). The side-effect import below registers
// ToolCallItem for "commandExecution" items the moment TurnBlock itself is
// ever imported, regardless of what else the app happens to have loaded -
// the real SessionPane composition must never depend on import ORDER to
// get tool calls rendered correctly.
import "./ToolCallItem";
import "./tools";
import { useMemo } from "react";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { usePrefsStore } from "../../../stores/prefs";
import { requireClass } from "../../../widgets/internal/requireClass";
import { SeenDivider } from "./flow/SeenDivider";
import { TurnSeparator } from "./messages";
import { ToolCallCluster } from "./ToolCallCluster";
import { TurnFailureEndCap } from "./TurnFailureEndCap";
import { shouldGroup, toolRunFor } from "./toolGrouping";
import { itemScopeKey } from "./tools/subagentModuleStore";
import { visibleItems } from "./transcriptVisibility";
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
  // The transcript-wide set of exchange-opening agent item ids, threaded from
  // Session.tsx so item renderers can know whether to show an eyebrow.
  exchangeOpeners?: ReadonlySet<string>;
  // The session's short model/provider label, threaded from Session.tsx.
  agentLabel?: string;
  // Renders the "you left off here" marker (SeenDivider, kata g2ez) above
  // this turn's content. Session.tsx sets this on whichever single turn
  // useSeenDivider.ts names as the boundary - defaults false so every
  // other turn is unaffected.
  showSeenDivider?: boolean;
}

const CLASS = {
  turn: requireClass(styles.turn, "turnblock.module.css", "turn"),
  agentContent: requireClass(styles.agentContent, "turnblock.module.css", "agentContent"),
};

// Gutter classification (slack-lean speaker treatments, spec
// docs/web-ui/specs/2026-07-29-transcript-slack-lean-messages.md decisions
// 3-4): MARGIN items render full-width, exactly as before; everything else -
// agent work is the common case, structural rows are the explicit exceptions
// - is agent-side CONTENT and takes the indent. Unknown current-or-future
// wire types default to content. Note the wire's own type name for a system
// notice is "systemMessage" (SystemNoticeItem's registerItemRenderer call
// site), not "systemNotice".
const MARGIN_ITEM_TYPES: ReadonlySet<string> = new Set(["userMessage", "steering", "systemMessage", "warning"]);

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

export function TurnBlock({ turn, sessionRef, exchangeOpeners, agentLabel, showSeenDivider = false }: TurnBlockProps) {
  // A failed turn carries a TurnError (only genuine failures do - the projector
  // sets it alongside status "failed", never on a completed or user-cancelled
  // turn); its presence is the signal to close the turn with a diagnostic
  // end-cap, corroborated by the honest status "failed" the wire stamps.
  const failure = asTurnError(turn.error);
  // Settings -> Transcript's hook-exit and prompt-loaded toggles hide whole
  // items. Apply them HERE, to the turn the renderers receive, rather than
  // letting each renderer bow out: SystemNoticeItem computes its
  // consecutive-run grouping from turn.items, so an item hidden any later
  // would still be counted by the group it was meant to leave. Subscribing
  // to each toggle individually keeps a flip in Settings re-rendering the
  // transcript live, and leaves every unrelated pref change inert.
  const roundTimings = usePrefsStore((s) => s.transcript.roundTimings);
  const hookExitsAll = usePrefsStore((s) => s.transcript.hookExitsAll);
  const hookExitsNormal = usePrefsStore((s) => s.transcript.hookExitsNormal);
  const promptLoaded = usePrefsStore((s) => s.transcript.promptLoaded);
  const shown = useMemo(
    () => visibleItems(turn.items, { roundTimings, hookExitsAll, hookExitsNormal, promptLoaded }),
    [turn.items, roundTimings, hookExitsAll, hookExitsNormal, promptLoaded],
  );
  // Reuse the turn object outright when nothing is hidden (visibleItems is
  // identity-stable then), so the memoized renderers' `turn` prop churns no
  // more than it already did.
  const shownTurn = shown === turn.items ? turn : { ...turn, items: shown };
  return (
    <>
      {showSeenDivider && <SeenDivider />}
      <div className={CLASS.turn} data-testid="turn-block" data-turn-id={turn.id}>
        {shown.map((item) => {
          const run = toolRunFor(shown, item.id);
          if (run && shouldGroup(run)) {
            if (!run.isFirst) return null;
            // A ToolCallCluster renders a run of agent tool calls, so it is
            // agent-side content and takes the indent too. The key moves to
            // the wrapper so the cluster's identity is unchanged.
            return (
              <div key={itemScopeKey(sessionRef, item.id)} className={CLASS.agentContent} data-testid="agent-content">
                <ToolCallCluster items={run.items} turn={shownTurn} sessionRef={sessionRef} />
              </div>
            );
          }
          const ItemRenderer = itemRendererFor(item.type);
          // Margin items (userMessage, steering, systemMessage, warning)
          // render unwrapped, full width, exactly as before; the memoized
          // renderers and their keys are untouched. Everything else is
          // agent-side content and takes the gutter indent - EXCEPT an
          // exchange-opening agentMessage: its own speaker header is the
          // avatar row (spec classification: "avatar row"), and the avatar
          // belongs IN the gutter at the margin, not indented into the
          // content column it heads.
          const isAvatarRow = item.type === "agentMessage" && exchangeOpeners?.has(item.id) === true;
          if (MARGIN_ITEM_TYPES.has(item.type) || isAvatarRow) {
            return (
              <ItemRenderer
                key={item.id}
                item={item}
                turn={shownTurn}
                live={isItemLive(item)}
                sessionRef={sessionRef}
                opensExchange={exchangeOpeners?.has(item.id)}
                agentLabel={agentLabel}
              />
            );
          }
          return (
            <div key={item.id} className={CLASS.agentContent} data-testid="agent-content">
              <ItemRenderer
                item={item}
                turn={shownTurn}
                live={isItemLive(item)}
                sessionRef={sessionRef}
                opensExchange={exchangeOpeners?.has(item.id)}
                agentLabel={agentLabel}
              />
            </div>
          );
        })}
        {failure && <TurnFailureEndCap error={failure} turn={turn} sessionRef={sessionRef} />}
        <TurnSeparator turn={turn} />
      </div>
    </>
  );
}
