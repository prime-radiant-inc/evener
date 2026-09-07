// TurnBlock is the turn skeleton VirtualList windows over: it renders one
// turn's items, in wire order, each through the item-renderer registry
// (types.ts's itemRendererFor - a raw fallback for every type without a
// dedicated renderer yet). The side-effect import below registers
// ToolCallItem for "commandExecution" items the moment TurnBlock itself is
// ever imported, regardless of what else the app happens to have loaded -
// the real SessionPane composition must never depend on import ORDER to
// get tool calls rendered correctly.
import type { ReactNode } from "react";
import type { ItemModel, ThreadModel, TurnModel } from "../../../protocol/model";
import type { ProjectedEntry, ProjectedTurn } from "../../../transcriptDisplay/projector";
import {
  disclosureScopeForSession,
  expandDetailsByDefault,
  type TranscriptRenderContextValue,
  useTranscriptRenderContext,
} from "../../../transcriptDisplay/renderContext";
import "./tools";
import {
  disclosureDefault,
  isDisclosureOpen,
  scopedDisclosureId,
  toggleDisclosure,
} from "../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../widgets/internal/requireClass";
import transcriptStyles from "../session.module.css";
import { SeenDivider } from "./flow/SeenDivider";
import { rowRoleFor } from "./layoutRoles";
import { TurnSeparator } from "./messages";
import { ToolCallItem } from "./ToolCallItem";
import { ToolRunGroup } from "./ToolRunGroup";
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
    "data-view-anchor-id": entry.id,
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
  const anchor = projectedEntryAnchor({ ...first, id: run.id }, viewAnchorIndex);
  if (!anchor) return undefined;
  // The ids this anchor stands in for, so a scroll position or focus
  // captured on the second or third call (useTranscriptScroll) still finds
  // its way back to the run once the calls have folded.
  return { ...anchor, "data-view-anchor-members": run.entries.map((entry) => entry.id).join(",") };
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
      <details
        className={transcriptStyles.intentGroup}
        data-testid="intent-group"
        data-transcript-row-id={rowId}
        data-transcript-source-turn-ids={sourceTurnIds.join(",") || undefined}
        open={open}
      >
        {/* biome-ignore lint/a11y/noStaticElementInteractions: summary is natively keyboard-operable */}
        <summary
          className={transcriptStyles.intentGroupSummary}
          onClick={(event) => {
            event.preventDefault();
            toggleDisclosure(disclosureKey, fallback);
          }}
        >
          {entries.length} action{entries.length === 1 ? "" : "s"}
        </summary>
        <div className={transcriptStyles.intentGroupItems}>
          {entries.map((entry) => (
            <div key={entry.id} {...projectedEntryAnchor(entry, viewAnchorIndex)}>
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
      </details>
      {separatorTurn && <TurnSeparator turn={separatorTurn} />}
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

export function TurnBlock({
  turn,
  sessionRef,
  exchangeOpeners,
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
    const renderedItem = (
      <ItemRenderer
        item={item}
        turn={shownTurn}
        live={isItemLive(item)}
        sessionRef={sessionRef}
        opensExchange={exchangeOpeners?.has(item.id)}
        agentLabel={agentLabel}
        projectedSummary={entry.kind === "critical" ? entry.summary : undefined}
        renderContext={itemRenderContext}
        thread={thread}
        threadFingerprint={threadFingerprintForItem(
          item,
          thread,
          toolRendererFor(item.toolName ?? "").summarySuffix?.(item, thread),
        )}
      />
    );
    if (rowRoleFor(item, { opensExchange: exchangeOpeners?.has(item.id) }) === "speaker") {
      renderedEntries.push(
        <div key={entry.id} {...viewAnchorFor(entry)}>
          {renderedItem}
        </div>,
      );
    } else {
      renderedEntries.push(
        <div key={entry.id} className={CLASS.runContent} data-testid="run-content" {...viewAnchorFor(entry)}>
          {renderedItem}
        </div>,
      );
    }
  }
  return (
    <>
      {showSeenDivider && <SeenDivider />}
      <div className={CLASS.turn} data-testid="turn-block" data-turn-id={sourceTurn.id}>
        {renderedEntries}
        {failure && <TurnFailureEndCap error={failure} turn={sourceTurn} sessionRef={sessionRef} />}
        {showTurnSeparator && <TurnSeparator turn={sourceTurn} />}
      </div>
    </>
  );
}
