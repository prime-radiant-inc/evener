import type { ItemModel, ThreadModel, TurnModel } from "../protocol/model";
import {
  type ContentVector,
  type HookExitDetail,
  normalizeConfig,
  presetContent,
  type TranscriptDisplayConfigV1,
} from "./config";

export const ACTION_SUMMARY_UNAVAILABLE = "Action summary unavailable";

export type ProjectedEntry =
  | { kind: "item"; id: string; turnId: string; sourceIndex: number; item: ItemModel; isMessage: boolean }
  | {
      kind: "intent";
      id: `intent:${string}`;
      turnId: string;
      sourceIndex: number;
      sourceItemId: string;
      rationale: string;
    }
  | {
      kind: "critical";
      id: string;
      turnId: string;
      sourceIndex: number;
      sourceItemId?: string;
      item: ItemModel;
      summary: string;
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
// `environment` is intentionally a routine low-level system event: its
// visibility is governed by Advanced.systemEvents, just like the other known
// diagnostic announcements.
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
]);

const PROMPT_EVENT_KINDS = new Set(["system_prompt", "prompt_loaded"]);
const TURN_TIMING_EVENT_KIND = "round_timings";
const HOOK_EVENT_KIND = "hook_completed";
const CRITICAL_SYSTEM_EVENT_KINDS = new Set(["error", "tool_repair"]);

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

function contentVector(config: TranscriptDisplayConfigV1): ContentVector {
  return config.content.kind === "preset" ? presetContent(config.content.level) : config.content;
}

function isMessage(item: ItemModel): boolean {
  return MESSAGE_TYPES.has(item.type);
}

function isNonZeroExit(item: ItemModel): boolean {
  return typeof item.exitCode === "number" && item.exitCode !== 0;
}

function hasFailureStatus(item: ItemModel): boolean {
  return item.status === "failed" || item.status === "interrupted";
}

function hasItemFailure(item: ItemModel): boolean {
  return (item.error !== undefined && item.error.trim() !== "") || hasFailureStatus(item) || isNonZeroExit(item);
}

function isActiveItem(item: ItemModel, turn: TurnModel): boolean {
  // Current wire status is `inProgress`. The turn check covers an older or
  // partial item frame that has not carried its item status yet.
  return item.status === "inProgress" || (turn.status === "inProgress" && item.status === undefined);
}

function isTerminalTurn(turn: TurnModel): boolean {
  return turn.status === "failed" || turn.status === "interrupted";
}

function itemSummary(item: ItemModel): string {
  const description = item.description?.trim();
  if (description) return description;
  const warningTitle = item.warning?.title?.trim();
  if (warningTitle) return warningTitle;
  const text = item.text.trim();
  if (text) return text;
  if (item.eventKind === "error") return "Turn failed";
  if (item.type === "systemMessage") return "System event";
  return ACTION_SUMMARY_UNAVAILABLE;
}

function toolSummary(item: ItemModel): string {
  return item.description?.trim() || ACTION_SUMMARY_UNAVAILABLE;
}

function itemEntry(item: ItemModel, turnId: string, sourceIndex: number): ProjectedEntry {
  return {
    kind: "item",
    id: item.id,
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
  };
}

function criticalEntry(item: ItemModel, turnId: string, sourceIndex: number): ProjectedCriticalEntry {
  return {
    kind: "critical",
    id: item.id,
    turnId,
    sourceIndex,
    sourceItemId: item.id,
    item,
    summary: item.type === "commandExecution" ? toolSummary(item) : itemSummary(item),
  };
}

type Decision = "item" | "intent" | "critical" | "hidden";

function systemDecision(item: ItemModel, config: TranscriptDisplayConfigV1): Decision {
  const eventKind = item.eventKind;
  if (eventKind === undefined || eventKind === "" || !KNOWN_EVENT_KINDS.has(eventKind)) return "item";

  if (CRITICAL_SYSTEM_EVENT_KINDS.has(eventKind)) return "critical";

  if (eventKind === HOOK_EVENT_KIND) {
    if (config.advanced.hookExits === "all") return "item";
    if (isNonZeroExit(item)) return "critical";
    if (config.advanced.hookExits === "successful" && item.exitCode === 0) return "item";
    return "hidden";
  }

  if (PROMPT_EVENT_KINDS.has(eventKind)) return config.advanced.promptEvents ? "item" : "hidden";
  if (eventKind === TURN_TIMING_EVENT_KIND) return config.advanced.roundTimings ? "item" : "hidden";
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
    const missingPurpose = !item.description?.trim();
    const failure = hasItemFailure(item);
    const active = isActiveItem(item, turn);

    // Questions and approvals are interaction rows at every regular level.
    if (interaction) return "critical";
    // The ordinary item shape has no projected summary field. Keep a
    // purpose-less call on the critical path at every level so its renderer
    // receives the exact neutral summary instead of inventing one from the
    // tool name.
    if (vector.toolCalls && missingPurpose) return "critical";
    if (vector.toolCalls) return "item";
    if (failure || active || (isTerminalTurn(turn) && !vector.toolCalls)) return "critical";
    if (vector.toolIntent) return "intent";
    // Chat omits routine action rows, but a call with no purpose is not
    // routine-readable content: keep it as the neutral critical contract.
    return missingPurpose ? "critical" : "hidden";
  }

  if (item.type === "reasoning") {
    if (vector.reasoning) return "item";
    return hasFailureStatus(item) || isActiveItem(item, turn) || isTerminalTurn(turn) ? "critical" : "hidden";
  }

  if (item.type === "systemMessage") return systemDecision(item, config);

  // These live item types are always actionable/attention-worthy. Keeping the
  // check by type also makes warnings and steering independent of their prose.
  if (item.type === "warning" || item.type === "steering") return "critical";

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
): ProjectedCriticalEntry | undefined {
  if (!isTerminalTurn(turn)) return undefined;
  const sourceItem = turn.items.at(-1);
  if (!sourceItem) return undefined;
  const sourceIndex = sourceIndexByItem.get(sourceItem);
  if (sourceIndex === undefined) return undefined;
  return criticalEntry(sourceItem, turn.id, sourceIndex);
}

export function projectThread(model: ThreadModel, config: TranscriptDisplayConfigV1): TranscriptProjection {
  const normalized = normalizeConfig(config);
  const vector = contentVector(normalized);
  const turns: ProjectedTurn[] = [];
  const anchors: ProjectedAnchor[] = [];
  const eligibleDisclosureIds: string[] = [];
  let sourceIndex = 0;
  let projectedIndex = 0;

  for (const turn of model.turns) {
    const entries: ProjectedEntry[] = [];
    const visibleItems: ItemModel[] = [];
    const sourceIndexByItem = new Map<ItemModel, number>();
    for (const item of turn.items) {
      const itemSourceIndex = sourceIndex;
      sourceIndex += 1;
      sourceIndexByItem.set(item, itemSourceIndex);
      const decision = decisionFor(item, turn, normalized, vector);
      if (decision === "hidden") continue;

      let entry: ProjectedEntry;
      if (decision === "item") {
        entry = itemEntry(item, turn.id, itemSourceIndex);
      } else if (decision === "intent") {
        entry = intentEntry(item, turn.id, itemSourceIndex);
      } else {
        entry = criticalEntry(item, turn.id, itemSourceIndex);
      }
      visibleItems.push(item);
      entries.push(entry);
      addAnchor(anchors, entry, projectedIndex);
      projectedIndex += 1;

      if (entry.kind === "item" && eligibleDisclosure(item)) eligibleDisclosureIds.push(item.id);
    }
    if (entries.length === 0) {
      const fallback = terminalFallbackEntry(turn, sourceIndexByItem);
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
