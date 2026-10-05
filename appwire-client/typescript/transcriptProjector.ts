import { APPROVAL_DECISION_EVENT_KIND } from "./approvalDecision";
import {
  hasFailureStatus,
  hasItemFailure,
  isActiveItem,
  isInProgressStatus,
  isNonZeroExit,
  isTurnError,
} from "./itemFailure";
import type { ItemModel, ThreadModel, TurnModel } from "./model";
import { comparePositions } from "./reducer";
import { ERROR_EVENT_KIND } from "./systemEventCopy";
import {
  type ContentVector,
  contentVectorForConfig,
  type HookExitDetail,
  hidesDaemonSteering,
  informationalNoticesVisible,
  normalizeConfig,
  sharedNotesVisible,
  type TranscriptDisplayConfigV1,
} from "./transcriptDisplayConfig";
import { isInformationalWarning } from "./warnings";
import { hasWarningText } from "./warningText";

export const ACTION_SUMMARY_UNAVAILABLE = "Action summary unavailable";

export type ProjectedEntry =
  | {
      kind: "item";
      id: string;
      /**
       * The stable key a renderer keys this row by, when it differs from the
       * source item's id (see displayKeyFor): a streaming reply keeps one key
       * from its first streamed frame through the recording of its round. A
       * hand-built entry may leave it off; read it through entryDisplayKey.
       */
      displayKey?: string;
      turnId: string;
      sourceIndex: number;
      item: ItemModel;
      isMessage: boolean;
    }
  | {
      /**
       * A content-free placeholder for a reasoning item that is the turn's
       * live current thought while the `reasoning` content flag is off. It
       * carries the source item only so the renderer can estimate a streaming
       * token count; the thought's text is never rendered from this entry.
       */
      kind: "thinking";
      id: string;
      turnId: string;
      sourceIndex: number;
      item: ItemModel;
    }
  | {
      kind: "intent";
      id: `intent:${string}`;
      turnId: string;
      sourceIndex: number;
      sourceItemId: string;
      rationale: string;
      failed: boolean;
      /** The source item, so a renderer can drill from an intent row into a full
       * tool-call row (verbosity = expansion). */
      item: ItemModel;
    }
  | {
      kind: "critical";
      id: string;
      turnId: string;
      sourceIndex: number;
      sourceItemId?: string;
      item: ItemModel;
      summary: string;
      /**
       * A critical reasoning item whose text must not render because the
       * `reasoning` content flag is off. The renderer shows a neutral failure
       * summary instead of the thought's live body, preview, or disclosure.
       */
      redacted: boolean;
    };

type ProjectedCriticalEntry = Extract<ProjectedEntry, { kind: "critical" }>;

export interface ProjectedTurn {
  readonly id: string;
  /** Unfiltered source retained for terminal status/error consumers only. */
  readonly source: TurnModel;
  readonly entries: readonly ProjectedEntry[];
  /** Source items that survived visibility filtering, for downstream grouping. */
  readonly visibleItems: readonly ItemModel[];
}

export interface ProjectedAnchor {
  readonly id: string;
  readonly sourceIndex: number;
  readonly index: number;
  readonly isMessage: boolean;
}

export interface TranscriptMetadataVisibility {
  readonly roundTimings: boolean;
  readonly tokenCounts: boolean;
  readonly estimatedCost: boolean;
  readonly systemEvents: boolean;
  readonly promptEvents: boolean;
  readonly hookExits: HookExitDetail;
}

export interface TranscriptProjection {
  readonly turns: readonly ProjectedTurn[];
  readonly anchors: readonly ProjectedAnchor[];
  readonly metadata: TranscriptMetadataVisibility;
  readonly eligibleDisclosureIds: readonly string[];
}

const MESSAGE_TYPES = new Set(["userMessage", "agentMessage"]);

// Keep this vocabulary in step with protocol/types.gen.ts. The projector treats
// a value outside this set as an unknown event and deliberately renders it.
// Environment and shared-notes snapshots are routine diagnostics. Notes also
// stay out of Conversation, even when Advanced.systemEvents is enabled.
const KNOWN_EVENT_KINDS = new Set([
  "system_prompt",
  "plugin_loaded",
  "skill_activated",
  "hook_completed",
  "prompt_loaded",
  "context_compaction",
  "compaction",
  "turn_limit",
  "loop_detection",
  "goal_ended",
  "fork_summary",
  "round_timings",
  "tool_repair",
  "model_switch",
  "error",
  "environment",
  "notes-context",
  "warning",
  "interrupted",
  APPROVAL_DECISION_EVENT_KIND,
]);

const PROMPT_EVENT_KINDS = new Set(["system_prompt", "prompt_loaded"]);
const TURN_TIMING_EVENT_KIND = "round_timings";
const HOOK_EVENT_KIND = "hook_completed";
// The system events critical at every level: a persisted turn failure, a
// warning notice, and an interrupted-turn notice are the rows a reader hunts
// for (SystemNoticeItem's FailureLine renders them; systemGrouping.ts keeps
// them out of runs). A tool-repair notice left this set for the
// informationalNotices gate below.
const CRITICAL_SYSTEM_EVENT_KINDS = new Set(["error", "warning", "interrupted"]);
const TOOL_REPAIR_EVENT_KIND = "tool_repair";

// ask_user is the current interaction tool. The other names are protocol/tool
// vocabulary used by compatible clients; matching exact names keeps this typed
// and avoids guessing from a tool's description or output prose.
const INTERACTION_TOOL_NAMES = new Set([
  "ask_user",
  "approval",
  "permission_request",
  "request_approval",
  "sandbox_approval",
  "sandbox_escalation",
]);

function isMessage(item: ItemModel): boolean {
  return MESSAGE_TYPES.has(item.type);
}

function isTerminalTurn(turn: TurnModel): boolean {
  return turn.status === "failed" || turn.status === "interrupted";
}

// The renderer streams only the reasoning item that is still the turn's current
// activity - the tail of turn.items (see ThinkBlock's isCurrentThought). The
// content-free placeholder must match that exactly: a superseded thought must
// not wear a "Thinking…" label, and neither must a thought on a turn that has
// settled. The `turn.status` guard matters because a completed turn can still
// carry a stale `inProgress` reasoning item in a snapshot or a race, and the
// loader must not pulse for an agent that is no longer thinking.
function isLiveCurrentReasoning(item: ItemModel, turn: TurnModel): boolean {
  return (
    isInProgressStatus(turn.status) && isActiveItem(item, turn.status) && turn.items[turn.items.length - 1] === item
  );
}

function itemSummary(item: ItemModel): string {
  const description = item.description?.trim();
  if (description) return description;
  // item.warning rides an untyped wire param map through the reducer's
  // `warning` fold, so title can be any JSON value at runtime despite
  // ItemModel's own type declaring it as string — hasWarningText is the
  // same guard WarningItem.tsx takes before calling .trim().
  const rawTitle = item.warning?.title;
  const warningTitle = hasWarningText(rawTitle) ? rawTitle.trim() : "";
  if (warningTitle) return warningTitle;
  const text = item.text.trim();
  if (text) return text;
  if (item.eventKind === ERROR_EVENT_KIND) return "Turn failed";
  if (item.type === "systemMessage") return "System event";
  return ACTION_SUMMARY_UNAVAILABLE;
}

function toolSummary(item: ItemModel): string {
  return item.description?.trim() || ACTION_SUMMARY_UNAVAILABLE;
}

// A streaming reply is an overlay item ("stream:<round>/<attempt>:agentMessage")
// until its round is recorded, when it becomes a history item with a new id.
// Both carry the round's id, so the round's first reply takes a round display
// key that the stream and the recorded item share, and a renderer that keys rows
// by it reconciles the reply instead of remounting it. Only the first takes it
// (a round can record two replies); a call-bearing communicate preview takes
// none (its message records with no round id). Everything else keys by its id.
function displayKeyFor(item: ItemModel, keyedRounds: Set<string>): string | undefined {
  if (item.type !== "agentMessage" || !item.roundId || item.callId) return undefined;
  const key = `round:${item.roundId}:agentMessage`;
  if (keyedRounds.has(key)) return undefined;
  keyedRounds.add(key);
  return key;
}

// The stable key a renderer keys an entry by: an item entry's round display key
// when it has one, otherwise the entry's own id. A hand-built entry without a
// displayKey keys by its id.
export function entryDisplayKey(entry: ProjectedEntry): string {
  return entry.kind === "item" ? (entry.displayKey ?? entry.id) : entry.id;
}

function itemEntry(
  item: ItemModel,
  turnId: string,
  sourceIndex: number,
  displayKey: string | undefined,
): ProjectedEntry {
  return {
    kind: "item",
    id: item.id,
    displayKey,
    turnId,
    sourceIndex,
    item,
    isMessage: isMessage(item),
  };
}

function intentEntry(item: ItemModel, turnId: string, sourceIndex: number): ProjectedEntry {
  const rationale = item.description?.trim() || ACTION_SUMMARY_UNAVAILABLE;
  return {
    kind: "intent",
    id: `intent:${item.id}`,
    turnId,
    sourceIndex,
    sourceItemId: item.id,
    rationale,
    failed: hasItemFailure(item),
    item,
  };
}

function criticalEntry(
  item: ItemModel,
  turnId: string,
  sourceIndex: number,
  redacted: boolean,
): ProjectedCriticalEntry {
  let summary: string;
  if (item.type === "commandExecution") {
    summary = toolSummary(item);
  } else if (item.type === "reasoning") {
    // Neutral, and never the thought's own text. A failed or interrupted
    // thought says so; the renderer renders this verbatim (one source of
    // truth for the redacted label).
    summary = hasFailureStatus(item) ? "Thought failed" : "Thought not shown";
  } else {
    summary = itemSummary(item);
  }
  return {
    kind: "critical",
    id: item.id,
    turnId,
    sourceIndex,
    sourceItemId: item.id,
    item,
    summary,
    redacted,
  };
}

type Decision = "item" | "intent" | "critical" | "thinking" | "hidden";

// A tool-repair notice (EventToolCallRepaired on the hub wire): the repair
// already succeeded by the time the notice exists, so it is informational
// the same way a coded context-budget warning is - quiet detail, not an
// actionable failure. Classification is by the typed wire eventKind, never
// the notice's prose.
function isToolRepairNotice(item: ItemModel): boolean {
  return item.type === "systemMessage" && item.eventKind === TOOL_REPAIR_EVENT_KIND;
}

// A daemon steer: instructions the daemon sent the agent, never a steer the
// human wrote (source "user", the human-note kind included).
function isDaemonSteer(item: ItemModel): boolean {
  return item.type === "steering" && item.source !== "user";
}

function systemDecision(item: ItemModel, config: TranscriptDisplayConfigV1, vector: ContentVector): Decision {
  const eventKind = item.eventKind;
  if (eventKind === undefined || eventKind === "" || !KNOWN_EVENT_KINDS.has(eventKind)) return "item";

  // A repair notice, and a coded "no action needed" warning notice (the
  // daemon's context-budget notices arrive this way), show only where
  // informationalNoticesVisible says so, at full. Where a repair does show,
  // the systemMessage renderer already gives it the quiet one-liner every
  // lifecycle notice gets (SystemNoticeItem's plain line).
  if (isToolRepairNotice(item) || isInformationalWarning(item)) {
    return informationalNoticesVisible(vector) ? "critical" : "hidden";
  }

  if (CRITICAL_SYSTEM_EVENT_KINDS.has(eventKind)) return "critical";
  // A human's Allow or Deny is a decision they made, like a question's
  // answer: its history row shows at every level.
  if (eventKind === APPROVAL_DECISION_EVENT_KIND) return "item";

  if (eventKind === HOOK_EVENT_KIND) {
    if (config.advanced.hookExits === "all") return "item";
    if (isNonZeroExit(item)) return "critical";
    if (config.advanced.hookExits === "successful" && item.exitCode === 0) return "item";
    return "hidden";
  }

  if (PROMPT_EVENT_KINDS.has(eventKind)) return config.advanced.promptEvents ? "item" : "hidden";
  if (eventKind === TURN_TIMING_EVENT_KIND) return config.advanced.roundTimings ? "item" : "hidden";
  if (eventKind === "notes-context") return sharedNotesVisible(config) ? "item" : "hidden";
  return config.advanced.systemEvents ? "item" : "hidden";
}

function decisionFor(
  item: ItemModel,
  turn: TurnModel,
  config: TranscriptDisplayConfigV1,
  vector: ContentVector,
): Decision {
  if (isMessage(item)) return "item";

  if (item.type === "commandExecution") {
    const interaction = INTERACTION_TOOL_NAMES.has(item.toolName ?? "");
    const missingIntent = !item.description?.trim();
    const failure = hasItemFailure(item);
    const active = isActiveItem(item, turn.status);

    // Questions and approvals are interaction rows at every regular level.
    if (interaction) return "critical";
    // At tool-call levels an intent-less call is an ordinary tool row: its
    // renderer derives the summary from the call's own arguments (read_file's
    // "Read <path> · lines N-M", shell's "Ran <cmd>", …). It is not routed to
    // the critical path, which used to force the neutral placeholder summary.
    if (vector.toolCalls) return "item";
    if (vector.toolIntent) return "intent";
    if (failure || active || (isTerminalTurn(turn) && !vector.toolCalls)) return "critical";
    // A Custom vector may disable both calls and intent. Even there, an
    // intent-less call is not routine-readable content: keep it visible as a
    // critical row (its renderer still derives a summary from the arguments).
    return missingIntent ? "critical" : "hidden";
  }

  if (item.type === "reasoning") {
    if (vector.reasoning) return "item";
    // With reasoning off, a live current thought becomes a content-free
    // placeholder: the reader sees that the agent is thinking without the
    // stream itself. Only an in-progress turn takes the placeholder - a
    // terminal turn's agent is not thinking any more - and an in-progress
    // thought that is NOT the turn's tail is hidden (the renderer would not
    // stream it either). Failure and terminal-turn visibility stay so a broken
    // turn still explains itself.
    if (isLiveCurrentReasoning(item, turn)) return "thinking";
    return hasFailureStatus(item) || isTerminalTurn(turn) ? "critical" : "hidden";
  }

  if (item.type === "systemMessage") return systemDecision(item, config, vector);

  // These live item types are always actionable/attention-worthy. Keeping the
  // check by type also makes warnings and steering independent of their prose.
  // One exception: an informational warning (a coded "no action needed"
  // notice - budget arithmetic, not a failure) is quiet detail, so it shows
  // only where informationalNoticesVisible says so: the full preset, or a
  // custom vector that matches full on every field.
  if (item.type === "warning") {
    if (isInformationalWarning(item)) return informationalNoticesVisible(vector) ? "critical" : "hidden";
    return "critical";
  }
  // Daemon steering hides at the chat preset; see hidesDaemonSteering for the
  // one rule. A steer the human wrote is the human's own words and stays
  // critical at every level.
  if (item.type === "steering") {
    if (!isDaemonSteer(item)) return "critical";
    return hidesDaemonSteering(config.content) ? "hidden" : "critical";
  }

  // Future item types render through the raw renderer instead of disappearing.
  return "item";
}

function eligibleDisclosure(item: ItemModel): boolean {
  if (item.type === "commandExecution" || item.type === "reasoning") return true;
  // A visible system item may be a scaffold or a grouped diagnostic row. Keep
  // its source id available to the disclosure layer; grouping happens after
  // this projection and therefore sees only surviving rows.
  return item.type === "systemMessage" && item.eventKind !== undefined && item.eventKind !== "";
}

// A reasoning item projected as critical while the `reasoning` content flag is
// off must render redacted: the reader asked not to see thoughts, and a broken
// turn is no licence to show them. The renderer shows a neutral failure summary
// instead (see ProjectedEntry's critical.redacted).
function redactsReasoning(item: ItemModel, vector: ContentVector): boolean {
  return item.type === "reasoning" && !vector.reasoning;
}

function addAnchor(anchors: ProjectedAnchor[], entry: ProjectedEntry, index: number): void {
  anchors.push({
    id: entry.id,
    sourceIndex: entry.sourceIndex,
    index,
    isMessage: entry.kind === "item" ? entry.isMessage : false,
  });
}

function terminalFallbackEntry(
  turn: TurnModel,
  sourceIndexByItem: ReadonlyMap<ItemModel, number>,
  vector: ContentVector,
  config: TranscriptDisplayConfigV1,
): ProjectedCriticalEntry | undefined {
  if (!isTerminalTurn(turn)) return undefined;
  const sourceItem = turn.items.at(-1);
  if (!sourceItem) return undefined;
  const sourceIndex = sourceIndexByItem.get(sourceItem);
  if (sourceIndex === undefined) return undefined;
  // The fallback exists so a terminal turn never renders empty - but it
  // must not resurrect an informational notice decisionFor just hid (a
  // failed turn whose only item is a context-budget warning or a
  // tool-repair notice). The trade holds only where a failure end cap will
  // actually render, which both this gate and the renderer decide by
  // isTurnError (the web's TurnFailureEndCap renders from asTurnError, the
  // same narrowing). An errorless or malformed-error terminal turn renders
  // no end cap, so its fallback keeps the never-empty guarantee.
  const hiddenInformationalNotice =
    (isInformationalWarning(sourceItem) || isToolRepairNotice(sourceItem)) && !informationalNoticesVisible(vector);
  if (hiddenInformationalNotice && isTurnError(turn.error)) {
    return undefined;
  }
  // A daemon steer the chat preset hid never comes back, not even as the
  // fallback of an errorless terminal turn: such a turn renders empty at
  // chat, exactly as the phone renders it (Jesse, 2026-10-03). The
  // informational-notice trade above keeps its narrower shape, trading only
  // where the error end cap renders.
  if (isDaemonSteer(sourceItem) && hidesDaemonSteering(config.content)) {
    return undefined;
  }
  // A hidden snapshot cannot explain a failure and must not defeat filtering.
  if (sourceItem.type === "systemMessage" && sourceItem.eventKind === "notes-context" && !sharedNotesVisible(config)) {
    return undefined;
  }
  return criticalEntry(sourceItem, turn.id, sourceIndex, redactsReasoning(sourceItem, vector));
}

// Ranks every item across every turn by its real document position, not by
// turn-then-item array order: turns sort by their OWN first item's position,
// but two turns' items can themselves interleave (reducer.ts's
// noticeHistoryTurn: "turns can interleave" - a delegate turn's items landing
// between a parent turn's, say). sourceIndex is a scroll anchor's identity
// across renders (useTranscriptScroll), so it must track real position, not
// this turn's own array slot. Array.prototype.sort is stable, so an item with
// no position (a legacy, pre-v6 thread) keeps today's turn-then-item order -
// comparePositions sorts every unpositioned item equal.
function sourceIndexRanks(turns: readonly TurnModel[]): Map<ItemModel, number> {
  const all: ItemModel[] = [];
  for (const turn of turns) for (const item of turn.items) all.push(item);
  all.sort((left, right) => comparePositions(left.position, right.position));
  const ranks = new Map<ItemModel, number>();
  for (const [index, item] of all.entries()) ranks.set(item, index);
  return ranks;
}

export function projectThread(model: ThreadModel, config: TranscriptDisplayConfigV1): TranscriptProjection {
  const normalized = normalizeConfig(config);
  const vector = contentVectorForConfig(normalized);
  const turns: ProjectedTurn[] = [];
  const anchors: ProjectedAnchor[] = [];
  const eligibleDisclosureIds: string[] = [];
  const sourceIndexRank = sourceIndexRanks(model.turns);
  // Rounds whose first reply already took the round key (see displayKeyFor).
  const keyedRounds = new Set<string>();
  let projectedIndex = 0;

  for (const turn of model.turns) {
    const entries: ProjectedEntry[] = [];
    const visibleItems: ItemModel[] = [];
    const sourceIndexByItem = new Map<ItemModel, number>();
    for (const item of turn.items) {
      // Always present: sourceIndexRanks just built this map from these same
      // turns. The fallback is never taken; it only avoids a non-null assertion.
      const itemSourceIndex = sourceIndexRank.get(item) ?? 0;
      sourceIndexByItem.set(item, itemSourceIndex);
      const decision = decisionFor(item, turn, normalized, vector);
      if (decision === "hidden") continue;

      let entry: ProjectedEntry;
      if (decision === "item") {
        entry = itemEntry(item, turn.id, itemSourceIndex, displayKeyFor(item, keyedRounds));
      } else if (decision === "thinking") {
        entry = { kind: "thinking", id: item.id, turnId: turn.id, sourceIndex: itemSourceIndex, item };
      } else if (decision === "intent") {
        entry = intentEntry(item, turn.id, itemSourceIndex);
      } else {
        entry = criticalEntry(item, turn.id, itemSourceIndex, redactsReasoning(item, vector));
      }
      visibleItems.push(item);
      entries.push(entry);
      addAnchor(anchors, entry, projectedIndex);
      projectedIndex += 1;

      if (entry.kind === "item" && eligibleDisclosure(item)) {
        eligibleDisclosureIds.push(item.id);
        // A commandExecution item's summary line has its own disclosure state
        // (Task 5's two-level summaryOpen), so its summary key is also
        // eligible for the Full-level baseline. Other eligible types
        // (reasoning, systemMessage) have no separate summary disclosure.
        if (item.type === "commandExecution") eligibleDisclosureIds.push(`summary:${item.id}`);
      }
    }
    if (entries.length === 0) {
      const fallback = terminalFallbackEntry(turn, sourceIndexByItem, vector, normalized);
      if (fallback) {
        visibleItems.push(fallback.item);
        entries.push(fallback);
        addAnchor(anchors, fallback, projectedIndex);
        projectedIndex += 1;
      }
    }
    turns.push({ id: turn.id, source: turn, entries, visibleItems });
  }

  return {
    turns,
    anchors,
    metadata: { ...normalized.advanced },
    eligibleDisclosureIds,
  };
}
