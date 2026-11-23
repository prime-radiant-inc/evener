import { APPROVAL_DECISION_EVENT_KIND } from "./approvalDecision";
import {
  hasFailureStatus,
  hasItemFailure,
  isActiveItem,
  isInProgressStatus,
  isNonZeroExit,
  isTurnError,
} from "./itemFailure";
import { MEMORY_CONTEXT_EVENT_KIND } from "./memoryContext";
import type { ItemModel, ThreadModel, TurnModel } from "./model";
import { QUIET_YIELD_EVENT_KIND } from "./quietYield";
import { isParentSteer } from "./steeringLabels";
import { ERROR_EVENT_KIND } from "./systemEventCopy";
import {
  type ContentVector,
  configFingerprint,
  contentVectorForConfig,
  type HookExitDetail,
  hidesDaemonSteering,
  hidesThinkingTraces,
  informationalNoticesVisible,
  normalizeConfig,
  sharedNotesVisible,
  type TranscriptDisplayConfigV1,
} from "./transcriptDisplayConfig";
import { comparePositions } from "./transcriptHistory";
import { THREAD_ITEM_EVENT_KINDS } from "./types.gen";
import { isInformationalWarning } from "./warnings";
import { hasWarningText } from "./warningText";

export const ACTION_SUMMARY_UNAVAILABLE = "Action summary unavailable";

export type ProjectedEntry =
  | {
      kind: "item";
      id: string;
      /**
       * The stable key a renderer keys this row by, when it differs from the
       * source item's id (see displayKeyFor): a streaming reply or thought keeps one key
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
       * A compact trace row for a reasoning item while the `reasoning`
       * content flag is off: live current thoughts AND settled thoughts at
       * every level but the chat preset. The renderer derives live (the
       * pulsing "Thinking…" label, streaming body) vs settled (the collapsed
       * "Thought · ~N tokens" disclosure) from the item and turn; the
       * thought's text renders only inside the unfoldable disclosure.
       */
      kind: "thinking";
      /** The projector's own live rule (isLiveCurrentReasoning: the turn is
       * in progress, the item is active, and it is the turn's tail), carried
       * on the entry so every renderer answers from one source. */
      live: boolean;
      id: string;
      displayKey?: string;
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
      displayKey?: string;
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

// The daemon's event-kind vocabulary, generated from appwire's
// AllThreadItemEventKinds, so a new kind is known (and governed by the
// system-events gate) the moment it is generated. The projector treats a value
// outside this set as an unknown event and deliberately renders it.
const KNOWN_EVENT_KINDS: ReadonlySet<string> = new Set(THREAD_ITEM_EVENT_KINDS);

// The prompt events: system_prompt is the full prompt scaffold, prompt_loaded
// the quiet "Loaded prompt X (N B)" line naming the same event. Both answer to
// one "Prompt loaded" setting on every client.
export const PROMPT_EVENT_KINDS: ReadonlySet<string> = new Set(["system_prompt", "prompt_loaded"]);
const TURN_TIMING_EVENT_KIND = "round_timings";
const HOOK_EVENT_KIND = "hook_completed";
// The system events shown at every level: a persisted turn failure, a warning
// notice, and an interrupted-turn notice are the rows a reader hunts for. This
// decides visibility only; each client chooses how to draw them.
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
//
// Exported because it is THE live rule for a reasoning item: the projector
// stamps it on each "thinking" entry, the web renderer asks it directly, and
// the phone reads the stamped bit - one function, so the three cannot drift.
export function isLiveCurrentReasoning(item: ItemModel, turn: TurnModel): boolean {
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

// A streaming reply or reasoning item is an overlay item ("stream:<round>/<attempt>:<type>")
// until its round is recorded, when it becomes a history item with a new id.
// Both carry the round's id, so the round's first item of each type takes a round display
// key that the stream and the recorded item share, and a renderer that keys rows
// by it reconciles the reply instead of remounting it. Only the first takes it
// (a round can record two replies or thoughts); a call-bearing communicate preview takes
// none (its message records with no round id). Everything else keys by its id.
function displayKeyFor(item: ItemModel, keyedRounds: Set<string>): string | undefined {
  if ((item.type !== "agentMessage" && item.type !== "reasoning") || !item.roundId || item.callId) return undefined;
  const key = `round:${item.roundId}:${item.type}`;
  if (keyedRounds.has(key)) return undefined;
  keyedRounds.add(key);
  return key;
}

// The stable key a renderer keys an entry by: an item entry's round display key
// when it has one, otherwise the entry's own id. A hand-built entry without a
// displayKey keys by its id.
export function entryDisplayKey(entry: ProjectedEntry): string {
  return entry.kind === "intent" ? entry.id : (entry.displayKey ?? entry.id);
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
  displayKey?: string,
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
    displayKey,
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

// An automatic memory refresh: its own collapsed "Refreshed my memory"
// disclosure, never a generic lifecycle line.
function isMemoryContextNotice(item: ItemModel): boolean {
  return item.type === "systemMessage" && item.eventKind === MEMORY_CONTEXT_EVENT_KIND;
}

// A daemon steer: instructions the daemon sent the agent, never a steer the
// human wrote (source "user", the human-note kind included) and not a message
// from the session's parent agent (source "parent").
function isDaemonSteer(item: ItemModel): boolean {
  return item.type === "steering" && item.source !== "user" && !isParentSteer(item.source);
}

function systemDecision(item: ItemModel, config: TranscriptDisplayConfigV1, vector: ContentVector): Decision {
  const eventKind = item.eventKind;
  if (eventKind === undefined || eventKind === "" || !KNOWN_EVENT_KINDS.has(eventKind)) return "item";

  // A memory refresh belongs to the actions view: it shows from Tools up, where
  // a call is a row of its own, and stays out of Chat and Intent, where actions
  // fold to one-line intent proxies (Jesse, 2026-11-16). The system-events gate
  // never governs it. See eligibleDisclosure for its baseline opt-out.
  if (isMemoryContextNotice(item)) return vector.toolCalls ? "item" : "hidden";

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
  // A quiet communicate({}) yield leaves a muted one-liner at every display
  // detail level, independent of Advanced.systemEvents (Jesse, 2026-11-23).
  if (eventKind === QUIET_YIELD_EVENT_KIND) return "item";

  if (eventKind === HOOK_EVENT_KIND) {
    if (config.advanced.hookExits === "all") return "item";
    if (isNonZeroExit(item)) return "critical";
    if (config.advanced.hookExits === "successful" && item.exitCode === 0) return "item";
    return "hidden";
  }

  if (PROMPT_EVENT_KINDS.has(eventKind)) return config.advanced.promptEvents ? "item" : "hidden";
  if (eventKind === TURN_TIMING_EVENT_KIND) return config.advanced.roundTimings ? "item" : "hidden";
  // Shared-notes snapshots are internal: they stay out of the chat preset even
  // when Advanced.systemEvents is enabled (sharedNotesVisible).
  if (eventKind === "notes-context") return sharedNotesVisible(config) ? "item" : "hidden";
  // Everything else, environment snapshots included, is a routine diagnostic.
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
    // Thinking begins at Intent. Chat shows no live, settled, or critical
    // reasoning row; the turn's own failure presentation remains independent.
    if (hidesThinkingTraces(config.content)) return "hidden";
    // With reasoning off, a live current thought becomes a compact trace
    // row: a collapsed, reader-unfoldable disclosure whose closed label is
    // the pulsing "Thinking…" placeholder grammar. Only an in-progress turn
    // takes the live treatment - a terminal turn's agent is not thinking
    // any more.
    if (isLiveCurrentReasoning(item, turn)) return "thinking";
    if (hasFailureStatus(item) || isTerminalTurn(turn)) return "critical";
    // A settled healthy thought stays as a compact trace row. The renderer
    // derives live vs settled itself; a stale inProgress item on a completed
    // turn renders settled.
    return "thinking";
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
  // A memory refresh ignores every general expansion baseline, including
  // Full's open-everything baseline; only an explicit reader choice opens it.
  // Keeping it out of the baseline inventory is that posture's other half.
  if (isMemoryContextNotice(item)) return false;
  // Demoted quiet-yield commentary stays collapsed until the reader opens it,
  // even at the Full preset.
  if (item.type === "systemMessage" && item.eventKind === QUIET_YIELD_EVENT_KIND) return false;
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
  // Chat's hidden reasoning never returns as a terminal fallback. The source
  // turn still owns and renders any genuine turn failure.
  if (redactsReasoning(sourceItem, vector) && hidesThinkingTraces(config.content)) {
    return undefined;
  }
  // A hidden snapshot cannot explain a failure and must not defeat filtering.
  if (sourceItem.type === "systemMessage" && sourceItem.eventKind === "notes-context" && !sharedNotesVisible(config)) {
    return undefined;
  }
  // The actions view's hidden refresh never comes back either: it is not news of
  // a failure, and a turn that shows only intent proxies stays that way.
  if (isMemoryContextNotice(sourceItem) && !vector.toolCalls) return undefined;
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
function positionOrderedItems(items: readonly ItemModel[]): ItemModel[] {
  const all = [...items];
  all.sort((left, right) => comparePositions(left.position, right.position));
  return all;
}

function sourceIndexRanks(turns: readonly TurnModel[]): Map<ItemModel, number> {
  const all: ItemModel[] = [];
  for (const turn of turns) for (const item of turn.items) all.push(item);
  const ranks = new Map<ItemModel, number>();
  for (const [index, item] of positionOrderedItems(all).entries()) ranks.set(item, index);
  return ranks;
}

// The identity history merges an item by (transcriptHistory's itemIdentity),
// and the identity a rank is looked up by when a derivation is reused across
// frames. Items are immutable between merges, so an item whose PAYLOAD changed
// - every streamed delta - is a new object with the same identity and
// position. Keying ranks by identity is what lets one changed turn be
// re-projected while every other turn's ranks stand.
function itemRankIdentity(item: Pick<ItemModel, "id" | "transcriptKey">): string {
  return item.transcriptKey ?? item.id;
}

/**
 * One turn's contribution to a projection, plus what the incremental
 * projector needs to decide whether it can be reused for the next model.
 */
interface TurnProjection {
  /** The exact turn object this was derived from. A contribution is reused
   * only while the model still holds this object: turns are immutable here, so
   * an identical reference means identical entries, anchors and disclosure
   * ids. */
  readonly turn: TurnModel;
  readonly projected: ProjectedTurn;
  /** Anchors carrying their GLOBAL projected index, so a reused turn hands
   * back the same objects. */
  readonly anchors: readonly ProjectedAnchor[];
  readonly eligible: readonly string[];
  /** The round display keys displayKeyFor actually claimed here, in order.
   * Replaying them onto the next pass's keyedRounds set rebuilds the
   * cross-turn state a later turn's display keys depend on. */
  readonly tookRoundKeys: readonly string[];
  readonly entryCount: number;
  /** This turn's first entry's projected index. A contribution can only be
   * reused while the running index reaches the same place, or its anchors'
   * global indexes would be stale. */
  readonly startIndex: number;
  /**
   * This turn's items as their (identity, position) sequence. Two turns with
   * equal signatures occupy the same slice of the position-sorted document, so
   * if every turn that changed still matches its cached signature, the global
   * rank order did not move and the cached ranks stand. Text, status and every
   * other payload field are deliberately absent: a streamed delta changes those
   * and must not invalidate ranks.
   */
  readonly rankSignature: string;
}

// displayKeyFor gives a round's key to the first item that claims it, in
// projection order. Two passes over the same turns can hand the same round's
// key to different turns when a turn's payload changes type, so a reused turn's
// claim is only safe to stand when the turns rebuilt before it claim exactly
// what they claimed last time.
function sameRoundKeys(left: readonly string[], right: readonly string[]): boolean {
  if (left.length !== right.length) return false;
  for (const [index, key] of left.entries()) {
    if (key !== right[index]) return false;
  }
  return true;
}

function rankSignature(turn: TurnModel): string {
  let signature = "";
  for (const item of turn.items) {
    const position = item.position;
    // \u0001 and \u0002 cannot occur in an id or a number, so one item's
    // fields can never blend into the next item's.
    signature += `${itemRankIdentity(item)}\u0001${position?.entry ?? ""},${position?.item ?? ""},${
      position?.sub ?? ""
    }\u0002`;
  }
  return signature;
}

// One turn's entries, anchors and disclosure inventory. Pure: everything that
// is not the turn itself arrives as an argument, so the whole-thread and the
// per-turn (incremental) paths share exactly one implementation and cannot
// drift.
function projectTurn(
  turn: TurnModel,
  normalized: TranscriptDisplayConfigV1,
  vector: ContentVector,
  sourceIndexOf: (item: ItemModel) => number,
  keyedRounds: Set<string>,
  startIndex: number,
): TurnProjection {
  const anchors: ProjectedAnchor[] = [];
  const eligibleDisclosureIds: string[] = [];
  const entries: ProjectedEntry[] = [];
  const visibleItems: ItemModel[] = [];
  const sourceIndexByItem = new Map<ItemModel, number>();
  const tookRoundKeys: string[] = [];
  let projectedIndex = startIndex;
  for (const item of turn.items) {
    const itemSourceIndex = sourceIndexOf(item);
    sourceIndexByItem.set(item, itemSourceIndex);
    const decision = decisionFor(item, turn, normalized, vector);
    if (decision === "hidden") continue;

    let entry: ProjectedEntry;
    const displayKey = displayKeyFor(item, keyedRounds);
    if (displayKey !== undefined) tookRoundKeys.push(displayKey);
    if (decision === "item") {
      entry = itemEntry(item, turn.id, itemSourceIndex, displayKey);
    } else if (decision === "thinking") {
      entry = {
        kind: "thinking",
        id: item.id,
        displayKey,
        turnId: turn.id,
        sourceIndex: itemSourceIndex,
        item,
        live: isLiveCurrentReasoning(item, turn),
      };
    } else if (decision === "intent") {
      entry = intentEntry(item, turn.id, itemSourceIndex);
    } else {
      entry = criticalEntry(item, turn.id, itemSourceIndex, redactsReasoning(item, vector), displayKey);
    }
    visibleItems.push(item);
    entries.push(entry);
    addAnchor(anchors, entry, projectedIndex);
    projectedIndex += 1;

    if (entry.kind === "item" && eligibleDisclosure(item)) {
      // The renderer keys a disclosure by the entry's display key (a
      // reasoning row by its round key), so the baseline inventory names
      // that same key - otherwise the open-everything baseline reaches no
      // disclosure for the row and a reader's earlier close survives a
      // level switch.
      eligibleDisclosureIds.push(entryDisplayKey(entry));
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
  return {
    turn,
    projected: { id: turn.id, source: turn, entries, visibleItems },
    anchors,
    eligible: eligibleDisclosureIds,
    tookRoundKeys,
    entryCount: entries.length,
    startIndex,
    rankSignature: rankSignature(turn),
  };
}

function assembleProjection(
  parts: readonly TurnProjection[],
  normalized: TranscriptDisplayConfigV1,
): TranscriptProjection {
  return {
    turns: parts.map((part) => part.projected),
    anchors: parts.flatMap((part) => part.anchors),
    metadata: { ...normalized.advanced },
    eligibleDisclosureIds: parts.flatMap((part) => part.eligible),
  };
}

// Projects every turn in order, threading the round display keys and the
// running projected index from one turn into the next. Both whole-thread
// derivations - projectThread and the incremental projector's fallback - come
// through here, so the two cannot drift apart.
function projectTurns(
  turns: readonly TurnModel[],
  normalized: TranscriptDisplayConfigV1,
  vector: ContentVector,
  sourceIndexOf: (item: ItemModel) => number,
): TurnProjection[] {
  // Rounds whose first reply already took the round key (see displayKeyFor).
  const keyedRounds = new Set<string>();
  const parts: TurnProjection[] = [];
  let projectedIndex = 0;
  for (const turn of turns) {
    const part = projectTurn(turn, normalized, vector, sourceIndexOf, keyedRounds, projectedIndex);
    parts.push(part);
    projectedIndex += part.entryCount;
  }
  return parts;
}

export function projectThread(model: ThreadModel, config: TranscriptDisplayConfigV1): TranscriptProjection {
  const normalized = normalizeConfig(config);
  const vector = contentVectorForConfig(normalized);
  const sourceIndexRank = sourceIndexRanks(model.turns);
  // Always present: sourceIndexRanks just built this map from these same
  // turns. The fallback is never taken; it only avoids a non-null assertion.
  const parts = projectTurns(model.turns, normalized, vector, (item) => sourceIndexRank.get(item) ?? 0);
  return assembleProjection(parts, normalized);
}

interface HeldProjection {
  readonly configKey: string;
  readonly normalized: TranscriptDisplayConfigV1;
  readonly vector: ContentVector;
  /** The turn array this derivation read, so a model whose turns array did not
   * move (a status or queue frame) costs one identity check. */
  readonly turns: readonly TurnModel[];
  readonly parts: readonly TurnProjection[];
  /** Item identity to global rank, or undefined when two items share an
   * identity. Reuse then cannot address items unambiguously, so the projector
   * falls back to a full derivation. */
  readonly rankByIdentity: ReadonlyMap<string, number> | undefined;
  readonly projection: TranscriptProjection;
}

/**
 * A projector that keeps its previous derivation and reuses every turn whose
 * inputs did not change. A streamed delta replaces one turn object and leaves
 * every other turn's object alone, so the position sort, the per-item
 * classification, and every other turn's entries, anchors and disclosure ids
 * all stand: only the streaming turn is re-projected.
 *
 * Correctness rests on four checks, and failing any one falls back to the full
 * derivation (projectThread). That fallback is what makes out-of-order and
 * older-sequence arrivals safe rather than special-cased.
 *
 *  1. the config fingerprint is unchanged;
 *  2. the turn list has the same length and the same turn id at every index;
 *  3. every turn that changed still has the same (identity, position) item
 *     sequence, so the position-sorted document - and therefore every global
 *     sourceIndex - is unchanged. An insert, a delete or a move anywhere in a
 *     changed turn fails this and takes the full path;
 *  4. a reused turn's running projected index still matches its start index,
 *     so its anchors' global indexes stay true.
 *
 * The turn ids and the reused prefix are what make a changed round key safe: a
 * rebuilt turn re-claims its display keys in order, and a turn that moved its
 * own position in the turn list never reaches the reuse path at all.
 */
export interface TranscriptProjector {
  /** Derives the projection of `model` at `config`, reusing whatever the
   * previous call already derived. Reference-equal to the previous projection
   * when nothing it reads changed. */
  project(model: ThreadModel, config: TranscriptDisplayConfigV1): TranscriptProjection;
}

class IncrementalTranscriptProjector implements TranscriptProjector {
  private held: HeldProjection | undefined;

  project(model: ThreadModel, config: TranscriptDisplayConfigV1): TranscriptProjection {
    const held = this.held;
    if (held === undefined) return this.rebuild(model, config);
    const configKey = configFingerprint(config);
    if (configKey !== held.configKey) return this.rebuild(model, config);
    if (model.turns === held.turns) return held.projection;
    const turns = model.turns;
    const previous = held.parts;
    if (turns.length !== previous.length) return this.rebuild(model, config);
    let firstChanged = -1;
    for (const [index, turn] of turns.entries()) {
      const cached = previous[index];
      if (cached === undefined || cached.turn.id !== turn.id) return this.rebuild(model, config);
      if (firstChanged === -1 && cached.turn !== turn) firstChanged = index;
    }
    // Every turn is the same object, so the two models project identically: the
    // projection reads nothing else off the model.
    if (firstChanged === -1) {
      this.held = { ...held, turns };
      return held.projection;
    }
    // A changed turn whose items moved in the position-sorted document
    // invalidates every rank at or after it, so only the full path is safe.
    for (let index = firstChanged; index < turns.length; index += 1) {
      const turn = turns[index];
      const cached = previous[index];
      if (turn === undefined || cached === undefined || turn === cached.turn) continue;
      if (rankSignature(turn) !== cached.rankSignature) return this.rebuild(model, config);
    }
    const ranks = held.rankByIdentity;
    if (ranks === undefined) return this.rebuild(model, config);
    const parts: TurnProjection[] = [];
    // Rounds whose first reply already took the round key (see displayKeyFor),
    // replayed from the reused turns so a rebuilt turn's display key matches
    // what a full derivation would give it.
    const keyedRounds = new Set<string>();
    // Whether the round keys claimed so far still match what this pass started
    // from. A rebuilt turn can start claiming a key an earlier pass gave to a
    // later turn, or stop claiming one that a later turn would then take; either
    // way every later cached entry's displayKey is now untrustworthy, so reuse
    // stops for the rest of the list. A streamed delta keeps claiming the same
    // round key, so the common path stays incremental.
    let keysMatchPreviousPass = true;
    let projectedIndex = 0;
    for (const [index, turn] of turns.entries()) {
      const cached = previous[index];
      const reused =
        keysMatchPreviousPass && cached !== undefined && cached.turn === turn && cached.startIndex === projectedIndex
          ? cached
          : undefined;
      if (reused !== undefined) {
        for (const key of reused.tookRoundKeys) keyedRounds.add(key);
        parts.push(reused);
        projectedIndex += reused.entryCount;
        continue;
      }
      const part = projectTurn(
        turn,
        held.normalized,
        held.vector,
        (item) => ranks.get(itemRankIdentity(item)) ?? 0,
        keyedRounds,
        projectedIndex,
      );
      if (cached !== undefined && !sameRoundKeys(part.tookRoundKeys, cached.tookRoundKeys)) {
        keysMatchPreviousPass = false;
      }
      parts.push(part);
      projectedIndex += part.entryCount;
    }
    const projection = assembleProjection(parts, held.normalized);
    this.held = { ...held, turns, parts, projection };
    return projection;
  }

  private rebuild(model: ThreadModel, config: TranscriptDisplayConfigV1): TranscriptProjection {
    const configKey = configFingerprint(config);
    const normalized = normalizeConfig(config);
    const vector = contentVectorForConfig(normalized);
    // The same position-sorted document and the same rank assignment
    // projectThread uses, so the fallback and the reference derivation cannot
    // disagree about a source index.
    const byObject = sourceIndexRanks(model.turns);
    const byIdentity = new Map<string, number>();
    let itemCount = 0;
    for (const turn of model.turns) itemCount += turn.items.length;
    for (const [item, index] of byObject) {
      byIdentity.set(itemRankIdentity(item), index);
    }
    const parts = projectTurns(model.turns, normalized, vector, (item) => byObject.get(item) ?? 0);
    const projection = assembleProjection(parts, normalized);
    this.held = {
      configKey,
      normalized,
      vector,
      turns: model.turns,
      parts,
      // A shared identity would make the identity lookup ambiguous; the
      // object-keyed map stays authoritative and reuse is disabled.
      rankByIdentity: byIdentity.size === itemCount ? byIdentity : undefined,
      projection,
    };
    return projection;
  }
}

/** Creates a projector that reuses one model's derivation across calls. One per
 * live transcript surface: it holds only its last model, so two panes reading
 * different threads must not share one. */
export function createTranscriptProjector(): TranscriptProjector {
  return new IncrementalTranscriptProjector();
}
