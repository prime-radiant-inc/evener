import type {
  AskQuestionRef,
  ItemFailureSignals,
  ItemImage,
  ItemModel,
  ThreadModel,
  Turn,
  TurnModel,
} from "@evener/appwire-client";
// The native shim between the package's thread model and the phone timeline.
// It shrinks as seam 2 lands (SDK migration plan, D21–D24): the conversation is
// hydrated by reducer.hydrateThread (D22), every notification folds through
// reducer.applyNotification and the rows are this file's projection of the
// model it returns (D23c), and D24 replaces the display rows with
// transcriptDisplay/projector.ts entries and deletes this file.
//
// projectConversation folds a ThreadModel's turns into the display rows. No
// DOM, no network, no clock — given the same model it produces the same rows.
// Protocol DTOs never cross into React props; only the package's model types
// and the display rows declared here do.
//
// Forward-compatibility is load-bearing: an unknown ItemModel.type never
// disappears and never exposes raw HTML. It becomes a neutral collapsed
// activity row. Every Hub/user/agent/tool/filename field is untrusted plain
// text here; the sanitizer (markdown.ts) is the only place assistant Markdown
// is interpreted, and that runs downstream of this projection.

import {
  hasItemFailure,
  isInProgressStatus,
  joinedReasoningParagraphs,
  liveAskQuestions,
  parseAskUserQuestions,
  pendingTextJoined,
} from "@evener/appwire-client";

// --- the conversation native holds -------------------------------------------

// The package ThreadModel, as reducer.hydrateThread produces it, plus the
// display rows this shim projects from its turns; D24 removes `items`. Every
// notification folds into the model (state/conversation.ts) and the rows are
// re-projected from it, so `items` is always a function of `turns` — the
// older pages loadOlder prepends are the one exception, until D23d moves
// them into the model too.
export type MobileConversation = ThreadModel & {
  items: MobileTimelineItem[];
  // Whether a question is waiting on the user right now. Derived on every
  // publish and kept BESIDE the model's own askPending, never written into
  // it: the wire field is the hub's thread-level signal and snapshot
  // authoritative (the reducer never recomputes it), while this projection's
  // output is the model the next frame folds into — writing a derived value
  // there would latch it on. The reducer never reads this field.
  questionsPending: boolean;
};

export function projectConversation(model: ThreadModel): MobileConversation {
  // The asks the package says are answerable right now, derived once and
  // handed to the projector that renders them as question rows.
  const asks = askQuestionsByCall(model);
  return {
    ...model,
    items: projectTimeline(model, asks),
    // Two facts, neither standing in for the other: the hub's own askPending
    // (carried through untouched) covers an ask whose item this window does
    // not hold, and the package's live-ask rule — the same one that produced
    // the question rows above — covers an ask that settled between snapshots
    // and is answerable now. The phone asks for either.
    questionsPending: model.askPending || asks.size > 0,
  };
}

// --- display rows (D24 replaces these with the package's projector) ----------
// Every field is untrusted plain text; only assistant Markdown is sanitized
// later (markdown.ts). Treat string fields as display-only, never executable.

// One image attachment row entry: the package's ItemImage (src is the resolved
// fetch URL, name the wire's own field carried alongside) keyed for display.
export type AttachmentRef = ItemImage & { id: string };

// Lifecycle state of a collapsed activity row (tool call, reasoning, or an
// unknown forward-compatible item). "running" while in progress, "completed"
// on clean settlement, "failed" when the wire carried an error (status stays
// "completed" even for errored calls — error presence is the real signal).
export type ActivityState = "running" | "completed" | "failed";

// Durable activity family discriminator, independent of the display `label`.
// The projection sets this from the item's *type* — commandExecution (tool)
// vs reasoning vs anything else — never from the label string, so a
// commandExecution whose toolName is "Reasoning" is still family "tool" and a
// reasoning item is family "reasoning". Closed type: tool | reasoning | unknown.
// Consumers branch on `family`, never on `label`, so a renamed or localized
// label cannot change an item's family.
export type ActivityFamily = "tool" | "reasoning" | "unknown";

// Expandable detail behind a one-line activity card. Every field is plain
// text — never raw HTML — and may be truncated by the renderer. `arguments`
// is the tool's argumentsJSON verbatim (untrusted JSON text), `output` is the
// tool result text, `error` is the tool-result error text. `callId` lets a
// diagnostics disclosure cite the stable identifier without exposing it in
// the default collapsed row.
export interface ActivityDetail {
  description?: string;
  arguments?: string;
  output?: string;
  error?: string;
  exitCode?: number;
  durationMs?: number;
  callId?: string;
}

export interface ActivityMember {
  id: string;
  label: string;
  family: ActivityFamily;
  state: ActivityState;
  detail: ActivityDetail;
  transcriptKey?: string;
  position?: { entry: number; item: number };
}

// Tone of a steering/lifecycle notice row. "info" for ordinary steering/system
// notices, "warning" for loop detection / turn limit / provider failure, and
// "system" for environment / prelude scaffold that is purely informational.
export type NoticeTone = "info" | "warning" | "system";

export type NoticeOrigin = "steering" | "system";

export type NoticeFamily =
  | "informational"
  | "warning"
  | "hidden-instruction"
  | "system-prelude"
  | "lifecycle"
  | "diagnostic"
  | "unknown-system";

// The mobile timeline item union. A pure projection of one thread's turns
// into the families the phone timeline renders. Discriminated by `kind`.
export type MobileTimelineItem = (
  | { kind: "user"; id: string; text: string; transcriptEntryIndex?: number }
  | { kind: "assistant"; id: string; markdown: string; streaming: boolean }
  | {
      kind: "activity";
      id: string;
      label: string;
      // Durable activity-family discriminator, independent of `label`. The
      // projection sets this from the item's type (commandExecution → "tool",
      // reasoning → "reasoning", anything else → "unknown"), never from the
      // label text. Required: every activity constructor MUST set it to a
      // concrete ActivityFamily; consumers branch on `family`, never `label`.
      family: ActivityFamily;
      state: ActivityState;
      detail: ActivityDetail;
      members?: ActivityMember[];
    }
  | {
      kind: "notice";
      id: string;
      origin: NoticeOrigin;
      steeringKind?: string;
      eventKind?: string;
      exitCode?: number;
      family: NoticeFamily;
      tone: NoticeTone;
      text: string;
    }
  // The pending ask_user questions of one call, each carrying that call's id
  // (AskQuestionRef.callId); the composer renders them as interactive cards
  // with a single "Send answers" action.
  | { kind: "question"; id: string; questions: AskQuestionRef[] }
  | { kind: "failure"; id: string; title: string; detail: string }
  | { kind: "attachments"; id: string; items: AttachmentRef[] }
) & {
  transcriptKey?: string;
  sourceTranscriptKey?: string;
  position?: { entry: number; item: number };
};

// --- item classification ------------------------------------------------------

// Steering kinds that warrant a warning tone rather than info.
const WARNING_STEERING_KINDS = new Set([
  "loop-detected",
  "turn-limit",
  "provider-failure",
]);

// systemMessage eventKinds that warrant a warning tone.
const WARNING_EVENT_KINDS = new Set(["loop_detection", "turn_limit", "error"]);

const HIDDEN_EVENT_KINDS = new Set(["system_prompt", "prompt_loaded"]);
const PRELUDE_EVENT_KINDS = new Set(["environment"]);
const DIAGNOSTIC_EVENT_KINDS = new Set(["round_timings"]);
const LIFECYCLE_EVENT_KINDS = new Set([
  "plugin_loaded",
  "skill_activated",
  "hook_completed",
  "context_compaction",
  "compaction",
  "goal_ended",
  "fork_summary",
  "tool_repair",
  "model_switch",
]);

function systemFamily(eventKind: string | undefined): NoticeFamily {
  if (eventKind && WARNING_EVENT_KINDS.has(eventKind)) return "warning";
  if (eventKind && HIDDEN_EVENT_KINDS.has(eventKind))
    return "hidden-instruction";
  if (eventKind && PRELUDE_EVENT_KINDS.has(eventKind)) return "system-prelude";
  if (eventKind && DIAGNOSTIC_EVENT_KINDS.has(eventKind)) return "diagnostic";
  if (eventKind && LIFECYCLE_EVENT_KINDS.has(eventKind)) return "lifecycle";
  return "unknown-system";
}

function isUserMessage(item: ItemModel): boolean {
  return item.type === "userMessage";
}

function isAgentMessage(item: ItemModel): boolean {
  return item.type === "agentMessage";
}

function isReasoning(item: ItemModel): boolean {
  return item.type === "reasoning";
}

function isCommandExecution(item: ItemModel): boolean {
  return item.type === "commandExecution";
}

function isAskUser(item: ItemModel): boolean {
  return isCommandExecution(item) && item.toolName === "ask_user";
}

function isSteering(item: ItemModel): boolean {
  return item.type === "steering";
}

function isSystemMessage(item: ItemModel): boolean {
  return item.type === "systemMessage";
}

// A live item can arrive without any status of its own while the turn that
// contains it is still running — a sparse running tool/reasoning row would
// otherwise read as settled. Such an item is active exactly when its turn
// is; an item that carries its own status always keeps it.
function isActiveItem(
  item: ItemFailureSignals,
  turnStatus: string | undefined,
): boolean {
  if (item.status !== undefined) return isInProgressStatus(item.status);
  return isInProgressStatus(turnStatus);
}

// --- activity state ----------------------------------------------------------

function activityState(
  item: ItemFailureSignals,
  turnStatus: string | undefined,
): ActivityState {
  if (hasItemFailure(item)) return "failed";
  if (isActiveItem(item, turnStatus)) return "running";
  return "completed";
}

function toolLabel(item: ItemModel): string {
  return item.toolName ?? item.description?.trim() ?? "Tool";
}

// --- image / attachment projection -------------------------------------------

// Attachment rows keyed by their item and index; the package resolved each
// image's src at hydrate (url, inline bytes, sha route, path, name).
function attachmentRows(
  itemId: string,
  images: ItemImage[] | undefined,
  prefix = "",
): AttachmentRef[] | undefined {
  if (!images?.length) return undefined;
  return images.map((img, i) => ({ id: `${itemId}:${prefix}${i}`, ...img }));
}

function itemAttachments(item: ItemModel): AttachmentRef[] | undefined {
  // Human steering uses the same message and image presentation as user input.
  if (isUserMessage(item) || (isSteering(item) && item.source === "user")) {
    return attachmentRows(item.id, item.images);
  }
  if (isCommandExecution(item)) {
    return attachmentRows(item.id, item.outputImages, "out:");
  }
  return undefined;
}

// --- pending ask_user questions ---------------------------------------------

// The package's rule for which ask_user calls are still answerable
// (settled without error, after the last user message), grouped by call so
// each call becomes one question row.
function askQuestionsByCall(model: ThreadModel): Map<string, AskQuestionRef[]> {
  const byCall = new Map<string, AskQuestionRef[]>();
  for (const ref of liveAskQuestions(model)) {
    const questions = byCall.get(ref.callId);
    if (questions) questions.push(ref);
    else byCall.set(ref.callId, [ref]);
  }
  return byCall;
}

// --- single-item projection (pre-cluster) -----------------------------------
// Returns either a MobileTimelineItem (final, not clusterable) or an activity
// pre-item carrying a cluster family for the clustering pass, plus any
// attachments emitted alongside it.

interface PreActivity {
  family: string;
  item: Extract<MobileTimelineItem, { kind: "activity" }>;
}

type PreResult =
  | { kind: "final"; item: MobileTimelineItem; attachments?: AttachmentRef[] }
  | { kind: "activity"; pre: PreActivity; attachments?: AttachmentRef[] };

// The item's text as the reader sees it: the settled text plus any in-flight
// delta chunks a live reducer has accumulated (none on a hydrated item).
function itemMarkdown(item: ItemModel): string {
  return item.pendingText
    ? item.text + pendingTextJoined(item.pendingText)
    : item.text;
}

// A reasoning item's text as the reader sees it: the package's own reading of
// reasoningSummaries — one paragraph per summaryIndex, seeded from the item's
// text at hydrate and extended by item/reasoning/summaryTextDelta — as one
// string for the collapsed row. The model keeps those chunks across a settle
// (reducer.ts's mergeReasoning), so a completion carrying no text of its own
// shows what streamed in; an item with no chunks at all shows its text.
function reasoningText(item: ItemModel): string {
  const paragraphs = joinedReasoningParagraphs(item.reasoningSummaries);
  return paragraphs.length === 0 ? item.text : paragraphs.join("\n\n");
}

function projectItem(
  item: ItemModel,
  turn: TurnModel,
  asks: ReadonlyMap<string, AskQuestionRef[]>,
): PreResult {
  // Human steering uses the same message and image presentation as user input.
  if (isUserMessage(item) || (isSteering(item) && item.source === "user")) {
    const attachments = itemAttachments(item);
    return {
      kind: "final",
      item: {
        kind: "user",
        id: item.id,
        text: item.text,
        ...(isUserMessage(item) && item.transcriptEntryIndex !== undefined
          ? { transcriptEntryIndex: item.transcriptEntryIndex }
          : {}),
      },
      attachments,
    };
  }

  // Assistant message — streaming while the item itself is in flight. The
  // item's own status is the per-item liveness signal (the web reads the same
  // field: TurnBlock.tsx's isItemLive), so a message that settles inside a
  // turn that keeps running stops saying "Writing…" at once. An item that
  // carries no status of its own is live exactly while its turn is.
  if (isAgentMessage(item)) {
    const streaming = isActiveItem(item, turn.status);
    return {
      kind: "final",
      item: {
        kind: "assistant",
        id: item.id,
        markdown: itemMarkdown(item),
        streaming,
      },
    };
  }

  // Reasoning — collapsed, labeled activity. Web's transcript display never
  // calls hasItemFailure on a reasoning item (its own hasFailureStatus check
  // there only gates disclosure visibility, never a render state), so
  // reasoning state here is running/completed only — never "failed" — the
  // same as the incremental store path (state/conversation.ts). family is
  // "reasoning" regardless of any label text; a commandExecution whose
  // toolName is "Reasoning" is NOT routed here (it stays family "tool" below).
  if (isReasoning(item)) {
    const state: ActivityState = isActiveItem(item, turn.status)
      ? "running"
      : "completed";
    return {
      kind: "activity",
      pre: {
        family: `unknown:${item.type}`,
        item: {
          kind: "activity",
          id: item.id,
          label: "Reasoning",
          family: "reasoning",
          state,
          detail: { ...activityDetail(item), output: reasoningText(item) },
        },
      },
    };
  }

  // Pending ask_user — a question item (conversational boundary, not clustered).
  const questions = isAskUser(item)
    ? asks.get(item.callId ?? item.id)
    : undefined;
  if (questions) {
    return {
      kind: "final",
      item: { kind: "question", id: item.id, questions },
    };
  }

  // Tool call (commandExecution, including answered/errored ask_user) — activity.
  // family is always "tool" for a commandExecution, even when toolName is
  // "Reasoning"; the discriminator is derived from the item type, never the
  // label. callId is preserved exactly for diagnostics disclosure.
  if (isCommandExecution(item)) {
    const attachments = itemAttachments(item);
    const failed = hasItemFailure(item);
    const family = failed ? `failed:${item.id}` : "tool";
    return {
      kind: "activity",
      pre: {
        family,
        item: {
          kind: "activity",
          id: item.id,
          label: toolLabel(item),
          family: "tool",
          state: activityState(item, turn.status),
          detail: activityDetail(item),
        },
      },
      attachments,
    };
  }

  // Daemon steering — notice, without user image attachments.
  if (isSteering(item)) {
    const tone: NoticeTone =
      item.steeringKind && WARNING_STEERING_KINDS.has(item.steeringKind)
        ? "warning"
        : "info";
    return {
      kind: "final",
      item: {
        kind: "notice",
        id: item.id,
        origin: "steering",
        steeringKind: item.steeringKind,
        family: tone === "warning" ? "warning" : "informational",
        tone,
        text: item.text,
      },
    };
  }

  // System message — notice.
  if (isSystemMessage(item)) {
    const tone: NoticeTone =
      item.eventKind && WARNING_EVENT_KINDS.has(item.eventKind)
        ? "warning"
        : "system";
    return {
      kind: "final",
      item: {
        kind: "notice",
        id: item.id,
        origin: "system",
        family: systemFamily(item.eventKind),
        tone,
        text: item.text,
        ...(item.eventKind ? { eventKind: item.eventKind } : {}),
        ...(item.exitCode !== undefined ? { exitCode: item.exitCode } : {}),
      },
    };
  }

  // A warning the reducer folded into the active turn (reducer.ts's `case
  // "warning"`): its own item type, carrying the notice's title and hint
  // beside the message text. It is a notice with a warning tone, like every
  // other "something to know, not a failed turn" row here — the phone reads
  // that tone as critical (timeline.ts's isCriticalNotice) and the web renders
  // the same item as its own warning message, never as a turn failure.
  if (item.type === "warning") {
    const title = item.warning?.title;
    const hint = item.warning?.hint;
    return {
      kind: "final",
      item: {
        kind: "notice",
        id: item.id,
        origin: "system",
        family: "warning",
        tone: "warning",
        text: [title, item.text, hint].filter((part) => part).join("\n"),
      },
    };
  }

  // Unknown / forward-compatible item type — neutral collapsed activity, never
  // disappearing, never exposing raw HTML. The dangerous text lives in detail
  // as plain text the renderer escapes; the label stays neutral. family is
  // "unknown" for any item type the projection does not recognize.
  return {
    kind: "activity",
    pre: {
      family:
        activityState(item, turn.status) === "failed"
          ? `failed:${item.id}`
          : `unknown:${item.type}`,
      item: {
        kind: "activity",
        id: item.id,
        label: "Activity",
        family: "unknown",
        state: activityState(item, turn.status),
        detail: { ...activityDetail(item), output: item.text || item.output },
      },
    },
  };
}

function activityDescription(item: ItemModel): string | undefined {
  if (item.description?.trim() || !isAskUser(item)) return item.description;
  const questions = parseAskUserQuestions(item);
  if (!questions) return item.description;
  // Describe the posted questions without inferring answers from later input.
  return `Questions: ${questions
    .map((question, index) => question.header.trim() || `Question ${index + 1}`)
    .join("; ")}`;
}

// The wire item carries no duration of its own; like the web's transcript,
// the span is the settled item's own timestamps. Undefined while either is
// missing (a running call, or a producer that stamps neither).
function itemDurationMs(item: ItemModel): number | undefined {
  if (item.startedAt === undefined || item.completedAt === undefined) {
    return undefined;
  }
  return Date.parse(item.completedAt) - Date.parse(item.startedAt);
}

function activityDetail(item: ItemModel): ActivityDetail {
  return {
    description: activityDescription(item),
    arguments: item.argumentsJSON,
    output: item.output,
    error: item.error,
    exitCode: item.exitCode,
    durationMs: itemDurationMs(item),
    callId: item.callId,
  };
}

// --- clustering pass ---------------------------------------------------------
// Merge consecutive activity pre-items that share a cluster family into a
// single activity row keyed by the first member. The cluster state is running
// if any member is running; otherwise completed (failed members never join a
// run, so a cluster is never failed).

function clusterActivities(
  preItems: PreActivity[],
): Extract<MobileTimelineItem, { kind: "activity" }>[] {
  const result: Extract<MobileTimelineItem, { kind: "activity" }>[] = [];
  let run: PreActivity[] = [];

  const flush = () => {
    if (run.length === 0) return;
    const first = run[0]?.item;
    if (first) {
      if (run.length === 1) {
        result.push(first);
      } else {
        const state: ActivityState = run.some((p) => p.item.state === "running")
          ? "running"
          : "completed";
        const members: ActivityMember[] = run.map(({ item }) => ({
          id: item.id,
          label: item.label,
          family: item.family,
          state: item.state,
          detail: item.detail,
          ...(item.transcriptKey ? { transcriptKey: item.transcriptKey } : {}),
          ...(item.position ? { position: item.position } : {}),
        }));
        result.push({ ...first, state, members });
      }
    }
    run = [];
  };

  for (const pre of preItems) {
    const last = run[run.length - 1];
    if (last && last.family === pre.family) {
      run.push(pre);
    } else {
      flush();
      run = [pre];
    }
  }
  flush();
  return result;
}

// --- timeline projection -----------------------------------------------------

export function projectTimeline(
  model: ThreadModel,
  // The answerable asks, when the caller has already derived them
  // (projectConversation does, for askPending).
  asks: ReadonlyMap<string, AskQuestionRef[]> = askQuestionsByCall(model),
): MobileTimelineItem[] {

  // Project every item in order, preserving whether it is a final item or a
  // clusterable activity pre-item. Attachments emitted alongside an item
  // follow that item in the timeline.
  interface Ordered {
    type: "final" | "activity";
    item: MobileTimelineItem;
    pre?: PreActivity;
    attachments?: AttachmentRef[];
  }
  const ordered: Ordered[] = [];

  for (const turn of model.turns) {
    for (const item of turn.items) {
      const result = projectItem(item, turn, asks);
      if (result.kind === "final") {
        ordered.push({
          type: "final",
          item: {
            ...result.item,
            ...(item.transcriptKey
              ? { transcriptKey: item.transcriptKey }
              : {}),
            ...(item.position ? { position: item.position } : {}),
          },
          attachments: result.attachments,
        });
      } else {
        ordered.push({
          type: "activity",
          item: {
            ...result.pre.item,
            ...(item.transcriptKey
              ? { transcriptKey: item.transcriptKey }
              : {}),
            ...(item.position ? { position: item.position } : {}),
          },
          pre: {
            ...result.pre,
            item: {
              ...result.pre.item,
              ...(item.transcriptKey
                ? { transcriptKey: item.transcriptKey }
                : {}),
              ...(item.position ? { position: item.position } : {}),
            },
          },
          attachments: result.attachments,
        });
      }
    }
    // A turn error produces a failure item at the end of that turn's items.
    if (turn.error) {
      ordered.push({
        type: "final",
        item: failureItem(turn.error as NonNullable<Turn["error"]>, turn.id),
      });
    }
  }

  // Second pass: cluster consecutive activity rows that share a family, then
  // rebuild the timeline in original order.
  const items: MobileTimelineItem[] = [];
  let activityRun: PreActivity[] = [];
  let activityAttachments: Array<{ id: string; items: AttachmentRef[]; sourceTranscriptKey?: string }> = [];

  const flushActivityRun = () => {
    if (activityRun.length === 0) return;
    const clustered = clusterActivities(activityRun);
    for (const a of clustered) items.push(a);
    for (const attachment of activityAttachments) {
      items.push({ kind: "attachments", ...attachment });
    }
    activityRun = [];
    activityAttachments = [];
  };

  for (const entry of ordered) {
    if (entry.type === "activity" && entry.pre) {
      const last = activityRun[activityRun.length - 1];
      if (last && last.family === entry.pre.family) {
        activityRun.push(entry.pre);
      } else {
        flushActivityRun();
        activityRun = [entry.pre];
      }
      if (entry.attachments && entry.attachments.length > 0) {
        activityAttachments.push({
          id: `${entry.item.id}:attachments`,
          items: entry.attachments,
          sourceTranscriptKey: entry.item.transcriptKey ?? entry.item.id,
        });
      }
    } else {
      flushActivityRun();
      items.push(entry.item);
    }
    // Attachments follow the item that produced them.
    if (
      !(entry.type === "activity" && entry.pre) &&
      entry.attachments &&
      entry.attachments.length > 0
    ) {
      items.push({
        kind: "attachments",
        id: `${entry.item.id}:attachments`,
        items: entry.attachments,
        ...(entry.item.transcriptKey
          ? { sourceTranscriptKey: entry.item.transcriptKey }
          : {}),
      });
    }
  }
  flushActivityRun();

  return items;
}

function failureItem(
  error: NonNullable<Turn["error"]>,
  turnID: string,
): Extract<MobileTimelineItem, { kind: "failure" }> {
  const title = error.title ?? error.message;
  const parts = [error.message];
  if (error.hint) parts.push(error.hint);
  if (error.additionalDetails) parts.push(error.additionalDetails);
  return {
    kind: "failure",
    id: `failure:${turnID}:${title}`,
    title,
    detail: parts.join("\n"),
  };
}
