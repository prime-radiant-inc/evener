// TurnBlock is the turn skeleton VirtualList windows over: it renders one
// turn's items, in wire order, each through the item-renderer registry
// (types.ts's itemRendererFor - a raw fallback for every type without a
// dedicated renderer yet). The side-effect import below registers
// ToolCallItem for "commandExecution" items the moment TurnBlock itself is
// ever imported, regardless of what else the app happens to have loaded -
// the real SessionPane composition must never depend on import ORDER to
// get tool calls rendered correctly.

import type { ItemModel, ProjectedEntry, ProjectedTurn, ThreadModel, TurnModel } from "@evener/appwire-client";
import { displayTurnStatus, entryDisplayKey, scopedDisclosureId } from "@evener/appwire-client";
import type { ReactNode } from "react";
import {
  disclosureScopeForSession,
  expandDetailsByDefault,
  type TranscriptRenderContextValue,
  useTranscriptRenderContext,
} from "../../../transcriptDisplay/renderContext";
import "./tools";
import { Disclosure } from "../../../widgets";
import { disclosureDefault, isDisclosureOpen } from "../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../widgets/internal/requireClass";
import transcriptStyles from "../session.module.css";
import { SeenDivider } from "./flow/SeenDivider";
import { rowRoleFor } from "./layoutRoles";
import { TurnSeparator } from "./messages";
import { ToolCallItem } from "./ToolCallItem";
import { ToolRunGroup } from "./ToolRunGroup";
import { TurnDoneEndCap } from "./TurnDoneEndCap";
import { TurnFailureEndCap } from "./TurnFailureEndCap";
import { toolRendererFor } from "./toolRenderers";
import { foldTurnEntries, type ToolRun } from "./toolRuns";
import styles from "./turnblock.module.css";
import { asTurnError } from "./turnFailure";
import { itemRendererFor, threadFingerprintForItem } from "./types";

export interface TurnBlockProps {
  turn: ProjectedTurn | TurnModel;
  // The owning session's ref, threaded from Session.tsx so the turn-failure
  // end-cap can wire its recovery action (re-issue the turn). Optional: the
  // diagnostic renders without it, only the recovery button is withheld until
  // Session.tsx passes it down.
  sessionRef?: string;
  // The transcript-wide set of exchange-opening agent item ids, threaded from
  // Session.tsx so item renderers can know whether to show an eyebrow.
  exchangeOpeners?: ReadonlySet<string>;
  // The transcript-wide set of exchange-closing agent item ids (the last
  // text-bearing agentMessage before the next userMessage or transcript
  // end), threaded from Session.tsx so the terminal message can carry its
  // own treatment.
  exchangeClosers?: ReadonlySet<string>;
  // The session's short model/provider label, threaded from Session.tsx.
  agentLabel?: string;
  // Renders the "you left off here" marker (SeenDivider, kata g2ez) above
  // this turn's content. Session.tsx sets this on whichever single turn
  // useSeenDivider.ts names as the boundary - defaults false so every
  // other turn is unaffected.
  showSeenDivider?: boolean;
  // VirtualList windows whole turns while scroll coordination anchors at
  // projected-entry granularity. The projector supplies each entry's stable
  // source index; this is the row index that contains it.
  viewAnchorIndex?: number;
  /** Suppressed on a fragment that precedes a cross-turn intent group. */
  showTurnSeparator?: boolean;
  renderContext?: TranscriptRenderContextValue;
  thread?: ThreadModel;
}

const CLASS = {
  turn: requireClass(styles.turn, "turnblock.module.css", "turn"),
  runContent: requireClass(styles.runContent, "turnblock.module.css", "runContent"),
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

export function projectedEntryAnchor(entry: ProjectedEntry, viewAnchorIndex: number | undefined) {
  if (viewAnchorIndex === undefined) return undefined;
  return {
    // The stable display key, not the wire id: a streamed reply keeps its
    // anchor when history records its round (transcriptProjector's
    // displayKeyFor).
    "data-view-anchor-id": entryDisplayKey(entry),
    "data-view-anchor-index": viewAnchorIndex,
    "data-view-anchor-source-index": entry.sourceIndex,
    "data-view-anchor-turn-id": entry.turnId,
    "data-view-anchor-message": entry.kind === "item" && entry.isMessage,
  } as const;
}

// A folded run stands in for its entries in the anchor list too: it borrows
// the first folded entry's turn and source index, under the run's own id, so
// no two anchors can ever claim the same id when the run is open.
function runAnchorFor(run: ToolRun, viewAnchorIndex: number | undefined) {
  const first = run.entries[0];
  if (!first) return undefined;
  const anchor = projectedEntryAnchor({ ...first, id: run.id, displayKey: run.id }, viewAnchorIndex);
  if (!anchor) return undefined;
  // The ids this anchor stands in for, so a scroll position or focus
  // captured on the second or third call (useTranscriptScroll) still finds
  // its way back to the run once the calls have folded.
  return { ...anchor, "data-view-anchor-members": run.entries.map((entry) => entry.id).join(",") };
}

// A closed intent group mounts none of its entries, so the group itself
// stands in for them: its first entry's anchor, listing every entry it holds.
// Open, each entry carries its own anchor instead.
function closedIntentGroupAnchorFor(
  entries: readonly Extract<ProjectedEntry, { kind: "intent" }>[],
  viewAnchorIndex: number | undefined,
) {
  const first = entries[0];
  if (!first) return undefined;
  const anchor = projectedEntryAnchor(first, viewAnchorIndex);
  if (!anchor) return undefined;
  return { ...anchor, "data-view-anchor-members": entries.map((entry) => entry.id).join(",") };
}

export interface ProjectedIntentGroupProps {
  entries: readonly Extract<ProjectedEntry, { kind: "intent" }>[];
  rowId?: string;
  sourceTurnIds?: readonly string[];
  separatorTurn?: TurnModel;
  viewAnchorIndex?: number;
  showSeenDivider?: boolean;
  sessionRef?: string;
  renderContext?: TranscriptRenderContextValue;
  thread?: ThreadModel;
}

export function ProjectedIntentGroup({
  entries,
  rowId,
  sourceTurnIds = [],
  separatorTurn,
  viewAnchorIndex,
  showSeenDivider = false,
  sessionRef,
  renderContext,
  thread,
}: ProjectedIntentGroupProps) {
  const context = useTranscriptRenderContext();
  const { config } = context;
  const scope = disclosureScopeForSession(context, undefined);
  const identity = rowId ?? `intent-group:${entries[0]?.id ?? "empty"}`;
  const disclosureKey = scopedDisclosureId(scope, identity);
  const namedIntent = config.content.kind === "preset" && config.content.level === "intent";
  const fallback = namedIntent || expandDetailsByDefault(config) || disclosureDefault(scope, identity, false);
  const open = isDisclosureOpen(disclosureKey, fallback);
  return (
    <>
      {showSeenDivider && <SeenDivider />}
      <div
        className={transcriptStyles.intentGroup}
        data-transcript-row-id={rowId}
        data-transcript-source-turn-ids={sourceTurnIds.join(",") || undefined}
        {...(open ? undefined : closedIntentGroupAnchorFor(entries, viewAnchorIndex))}
      >
        <Disclosure
          variant="rule"
          id={disclosureKey}
          defaultOpen={fallback}
          data-testid="intent-group"
          summary={`${entries.length} action${entries.length === 1 ? "" : "s"}`}
        >
          <div className={transcriptStyles.intentGroupItems}>
            <div className={transcriptStyles.intentGroupCard}>
              {entries.map((entry) => (
                <div key={entryDisplayKey(entry)} {...projectedEntryAnchor(entry, viewAnchorIndex)}>
                  <ToolCallItem
                    item={entry.item}
                    turn={{ id: entry.turnId, status: "completed", items: [entry.item] }}
                    live={entry.item.status === "inProgress"}
                    sessionRef={sessionRef}
                    projectedSummary={!entry.item.description?.trim() ? entry.rationale : undefined}
                    renderContext={renderContext ?? context}
                    thread={thread}
                    threadFingerprint={threadFingerprintForItem(
                      entry.item,
                      thread,
                      toolRendererFor(entry.item.toolName ?? "").summarySuffix?.(entry.item, thread),
                    )}
                  />
                </div>
              ))}
            </div>
          </div>
        </Disclosure>
        {separatorTurn && separatorTurn.status === "completed" && !asTurnError(separatorTurn.error) && (
          <div className={transcriptStyles.intentGroupItems} data-testid="turn-done-row">
            <TurnDoneEndCap turn={separatorTurn} />
          </div>
        )}
        {separatorTurn && <TurnSeparator turn={separatorTurn} />}
      </div>
    </>
  );
}

function projectedForDirectTurn(turn: TurnModel): ProjectedTurn {
  const entries = turn.items.map((item, sourceIndex) => ({
    kind: "item" as const,
    id: item.id,
    turnId: turn.id,
    sourceIndex,
    item,
    isMessage: item.type === "userMessage" || item.type === "agentMessage",
  }));
  return { id: turn.id, source: turn, entries, visibleItems: turn.items };
}

function isProjectedTurn(turn: ProjectedTurn | TurnModel): turn is ProjectedTurn {
  return "source" in turn && "entries" in turn && "visibleItems" in turn;
}

/** Where a row sits in a tool card: a run of adjacent tool rows reads as ONE
 * rounded surface with hairline dividers (turnblock.module.css), so each
 * row's wrapper says whether it opens the card, continues it, or closes it. */
type ToolCardPosition = "only" | "first" | "middle" | "last";

/** Whether a laid-out entry renders a tool row: a folded run, or a tool call
 * whose descriptor does not suppress its whole row. A suppressed call renders
 * nothing, so it must neither join a card nor split one. */
function rendersToolRow(entry: ProjectedEntry | ToolRun): boolean | "skip" {
  if (entry.kind === "run") return true;
  if (entry.kind === "intent") return false;
  if (entry.item.type !== "commandExecution") return false;
  return toolRendererFor(entry.item.toolName ?? "").suppress?.(entry.item) ? "skip" : true;
}

/** Each laid-out entry's position in its tool card, or undefined for a row
 * that is not a tool row. */
function toolCardPositions(laidOut: readonly (ProjectedEntry | ToolRun)[]): (ToolCardPosition | undefined)[] {
  const member = laidOut.map(rendersToolRow);
  const neighbour = (from: number, step: 1 | -1): boolean => {
    for (let index = from + step; index >= 0 && index < member.length; index += step) {
      if (member[index] !== "skip") return member[index] === true;
    }
    return false;
  };
  return member.map((is, index) => {
    if (is !== true) return undefined;
    const before = neighbour(index, -1);
    const after = neighbour(index, 1);
    if (before && after) return "middle";
    if (before) return "last";
    return after ? "first" : "only";
  });
}

export function TurnBlock({
  turn,
  sessionRef,
  exchangeOpeners,
  exchangeClosers,
  agentLabel,
  showSeenDivider = false,
  viewAnchorIndex,
  showTurnSeparator = true,
  renderContext,
  thread,
}: TurnBlockProps) {
  const providerContext = useTranscriptRenderContext();
  const itemRenderContext = renderContext ?? providerContext;
  const projectedTurn = isProjectedTurn(turn) ? turn : projectedForDirectTurn(turn);
  const sourceTurn = projectedTurn.source;
  // A failed turn carries a TurnError (only genuine failures do - the projector
  // sets it alongside status "failed", never on a completed or user-cancelled
  // turn); its presence is the signal to close the turn with a diagnostic
  // end-cap, corroborated by the honest status "failed" the wire stamps.
  const failure = asTurnError(sourceTurn.error);
  // Spec "Turn status": running state lives in the overlay, never the wire's
  // own status - an open turn the thread does not name as its running turn
  // displays as interrupted (a crash-left-open turn, or one whose execution
  // stood down), not as running forever. A display rule only; sourceTurn's
  // own status is untouched.
  const displayStatus = displayTurnStatus(sourceTurn, thread ?? {});
  const visibleItems = projectedTurn.visibleItems;
  const allItemsVisible =
    visibleItems.length === sourceTurn.items.length &&
    visibleItems.every((item, index) => item === sourceTurn.items[index]);
  const shownTurn: TurnModel = allItemsVisible ? sourceTurn : { ...sourceTurn, items: [...visibleItems] };
  const viewAnchorFor = (entry: ProjectedEntry) => projectedEntryAnchor(entry, viewAnchorIndex);
  // Once a turn has settled, a run of uneventful tool calls collapses to one
  // row (critique R9, toolRuns.ts). "inProgress" is the wire's own live turn
  // status (the projector reads the same literal), so a turn still working
  // keeps every call visible as it arrives.
  const laidOut = foldTurnEntries(projectedTurn);
  const cardPositions = toolCardPositions(laidOut);
  const renderedEntries: ReactNode[] = [];
  for (let index = 0; index < laidOut.length; index += 1) {
    const entry = laidOut[index];
    if (!entry) continue;
    if (entry.kind === "run") {
      renderedEntries.push(
        <div
          key={entry.id}
          className={CLASS.runContent}
          data-testid="run-content"
          data-tool-card={cardPositions[index]}
          // The whole run is one position for scroll coordination, open or
          // closed - see ToolRunGroup's header.
          {...runAnchorFor(entry, viewAnchorIndex)}
        >
          <ToolRunGroup
            run={entry}
            turn={shownTurn}
            sessionRef={sessionRef}
            renderContext={itemRenderContext}
            thread={thread}
          />
        </div>,
      );
      continue;
    }
    if (entry.kind === "intent") {
      const group: Extract<ProjectedEntry, { kind: "intent" }>[] = [entry];
      while (laidOut[index + 1]?.kind === "intent") {
        index += 1;
        const next = laidOut[index];
        if (next?.kind === "intent") group.push(next);
      }
      renderedEntries.push(
        <ProjectedIntentGroup
          key={`intent-group:${group[0]?.id}`}
          entries={group}
          viewAnchorIndex={viewAnchorIndex}
          sessionRef={sessionRef}
          renderContext={itemRenderContext}
          thread={thread}
        />,
      );
      continue;
    }
    const item = entry.item;
    const ItemRenderer = itemRendererFor(item.type);
    const opensExchange = exchangeOpeners?.has(item.id);
    const closesExchange = exchangeClosers?.has(item.id);
    // One display key per entry: the row key, the renderer's stable identity
    // and the anchor all share it.
    const displayKey = entryDisplayKey(entry);
    const renderedItem = (
      <ItemRenderer
        item={item}
        turn={shownTurn}
        live={isItemLive(item)}
        sessionRef={sessionRef}
        displayKey={displayKey}
        opensExchange={opensExchange}
        closesExchange={closesExchange}
        agentLabel={agentLabel}
        projectedSummary={entry.kind === "critical" ? entry.summary : undefined}
        contentFree={entry.kind === "thinking"}
        contentFreeLive={entry.kind === "thinking" ? entry.live : undefined}
        redacted={entry.kind === "critical" && entry.redacted}
        renderContext={itemRenderContext}
        thread={thread}
        threadFingerprint={threadFingerprintForItem(
          item,
          thread,
          toolRendererFor(item.toolName ?? "").summarySuffix?.(item, thread),
        )}
      />
    );
    if (rowRoleFor(item, { opensExchange }) === "speaker") {
      renderedEntries.push(
        <div key={displayKey} {...viewAnchorFor(entry)}>
          {renderedItem}
        </div>,
      );
    } else {
      renderedEntries.push(
        <div
          key={displayKey}
          className={CLASS.runContent}
          data-testid="run-content"
          data-tool-card={cardPositions[index]}
          {...viewAnchorFor(entry)}
        >
          {renderedItem}
        </div>,
      );
    }
  }
  return (
    <>
      {showSeenDivider && <SeenDivider />}
      <div
        className={CLASS.turn}
        data-testid="turn-block"
        data-turn-id={sourceTurn.id}
        data-turn-status={displayStatus}
      >
        {renderedEntries}
        {failure && <TurnFailureEndCap error={failure} turnId={sourceTurn.id} sessionRef={sessionRef} />}
        {!failure && displayStatus === "completed" && showTurnSeparator && (
          <div className={CLASS.runContent} data-testid="turn-done-row">
            <TurnDoneEndCap turn={sourceTurn} />
          </div>
        )}
        {showTurnSeparator && <TurnSeparator turn={sourceTurn} />}
      </div>
    </>
  );
}
