// Pure AppWire-to-mobile thread projection. projectThread folds a wire
// Thread (protocol/types.gen.ts) into a MobileConversation view model that
// React components consume. No DOM, no network, no clock — given the same
// Thread it produces the same MobileConversation. Protocol DTOs never cross
// into React props; only the mobile view models in model.ts do.
//
// Forward-compatibility is load-bearing: an unknown ThreadItem.type never
// disappears and never exposes raw HTML. It becomes a neutral collapsed
// activity row. Every Hub/user/agent/tool/filename field is untrusted plain
// text here; the sanitizer (markdown.ts) is the only place assistant Markdown
// is interpreted, and that runs downstream of this projection.

import type {
  EvenerUsage,
  InputItem,
  OutputImage,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  Turn,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  ActivityDetail,
  ActivityState,
  AttachmentRef,
  MobileAskOption,
  MobileAskQuestion,
  MobileCapabilities,
  MobileConversation,
  MobileQueue,
  MobileTimelineItem,
  MobileUsage,
  NoticeFamily,
  NoticeTone,
} from "./model";

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

function isUserMessage(item: ThreadItem): boolean {
  return item.type === "userMessage";
}

function isAgentMessage(item: ThreadItem): boolean {
  return item.type === "agentMessage";
}

function isReasoning(item: ThreadItem): boolean {
  return item.type === "reasoning";
}

function isCommandExecution(item: ThreadItem): boolean {
  return item.type === "commandExecution";
}

function isAskUser(item: ThreadItem): boolean {
  return isCommandExecution(item) && item.toolName === "ask_user";
}

function isSteering(item: ThreadItem): boolean {
  return item.type === "steering";
}

function isSystemMessage(item: ThreadItem): boolean {
  return item.type === "systemMessage";
}

function toolCallFailed(item: ThreadItem): boolean {
  return item.error !== undefined && item.error !== "";
}

// --- activity state ----------------------------------------------------------

function activityState(item: ThreadItem): ActivityState {
  if (toolCallFailed(item)) return "failed";
  if (item.status === "inProgress") return "running";
  return "completed";
}

function toolLabel(item: ThreadItem): string {
  return item.toolName ?? item.description?.trim() ?? "Tool";
}

// --- image / attachment projection -------------------------------------------

function inlineImageSrc(img: InputItem): string | undefined {
  if (
    img.data === undefined ||
    img.data === "" ||
    img.mediaType === undefined ||
    img.mediaType === ""
  ) {
    return undefined;
  }
  return `data:${img.mediaType};base64,${img.data}`;
}

function inputAttachment(
  itemId: string,
  index: number,
  img: InputItem,
): AttachmentRef {
  const src = img.url ?? inlineImageSrc(img) ?? img.path ?? img.name ?? "";
  return {
    id: `${itemId}:${index}`,
    src,
    name: img.name,
    mediaType: img.mediaType,
  };
}

function outputAttachment(
  itemId: string,
  index: number,
  img: OutputImage,
): AttachmentRef {
  const src = img.url ?? img.path ?? img.name ?? img.source ?? "";
  return {
    id: `${itemId}:out:${index}`,
    src,
    name: img.name,
    mediaType: img.mediaType,
  };
}

// --- ask_user question parsing ----------------------------------------------
// Mirrors the Hub's parseAskUserQuestions (askShared.ts): defensive throughout.
// Malformed argumentsJson degrades to a fallback (undefined) rather than
// throwing, since this is untrusted wire JSON.

interface ParsedAskQuestion {
  header: string;
  question: string;
  options: MobileAskOption[];
  multiSelect: boolean;
  why?: string;
  ifUnanswered?: string;
}

function parseOption(raw: unknown): MobileAskOption | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const obj = raw as Record<string, unknown>;
  if (typeof obj.label !== "string" || typeof obj.detail !== "string")
    return undefined;
  return {
    label: obj.label,
    detail: obj.detail,
    recommended: obj.recommended === true,
  };
}

function parseQuestion(
  raw: unknown,
  index: number,
): ParsedAskQuestion | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const obj = raw as Record<string, unknown>;
  if (
    (obj.header !== undefined && typeof obj.header !== "string") ||
    typeof obj.question !== "string" ||
    !Array.isArray(obj.options)
  ) {
    return undefined;
  }
  const options = obj.options
    .map(parseOption)
    .filter((o): o is MobileAskOption => o !== undefined);
  if (options.length === 0) return undefined;
  return {
    header:
      typeof obj.header === "string" ? obj.header : `Question ${index + 1}`,
    question: obj.question,
    options,
    multiSelect: obj.multi_select === true,
    why: typeof obj.why === "string" ? obj.why : undefined,
    ifUnanswered:
      typeof obj.if_unanswered === "string" ? obj.if_unanswered : undefined,
  };
}

function parseAskUserQuestions(
  item: ThreadItem,
): ParsedAskQuestion[] | undefined {
  if (item.argumentsJson === undefined || item.argumentsJson === "")
    return undefined;
  let args: unknown;
  try {
    args = JSON.parse(item.argumentsJson);
  } catch {
    return undefined;
  }
  if (typeof args !== "object" || args === null) return undefined;
  const raw = (args as Record<string, unknown>).questions;
  if (!Array.isArray(raw)) return undefined;
  const questions = raw
    .map((q, i) => parseQuestion(q, i))
    .filter((q): q is ParsedAskQuestion => q !== undefined);
  return questions.length > 0 ? questions : undefined;
}

// A pending (answerable) ask_user: completed, no error, parseable questions,
// AND positioned AFTER the most recent userMessage (a later [answers] reply
// resolves the whole pending set at once). Mirrors liveAskQuestions.
function pendingAskUserIds(turns: readonly Turn[]): Set<string> {
  const items = turns.flatMap((turn) => turn.items ?? []);
  let lastUserIndex = -1;
  items.forEach((item, i) => {
    if (isUserMessage(item)) lastUserIndex = i;
  });
  const pending = new Set<string>();
  items.slice(lastUserIndex + 1).forEach((item) => {
    if (
      isAskUser(item) &&
      item.status === "completed" &&
      !toolCallFailed(item) &&
      parseAskUserQuestions(item) !== undefined
    ) {
      pending.add(item.id);
    }
  });
  return pending;
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

function projectItem(
  item: ThreadItem,
  turn: Turn,
  pendingAsks: Set<string>,
): PreResult {
  // User message — may carry input images that emit an attachments item.
  if (isUserMessage(item)) {
    const attachments = itemInputAttachments(item);
    return {
      kind: "final",
      item: { kind: "user", id: item.id, text: item.text ?? "" },
      attachments,
    };
  }

  // Assistant message — streaming while the turn is running.
  if (isAgentMessage(item)) {
    const markdown = `${item.text ?? ""}${item.delta ?? ""}`;
    const streaming = turn.status === "running";
    return {
      kind: "final",
      item: { kind: "assistant", id: item.id, markdown, streaming },
    };
  }

  // Reasoning — collapsed, labeled activity. family is "reasoning" regardless
  // of any label text; a commandExecution whose toolName is "Reasoning" is NOT
  // routed here (it stays family "tool" below).
  if (isReasoning(item)) {
    return {
      kind: "activity",
      pre: {
        family: `unknown:${item.type}`,
        item: {
          kind: "activity",
          id: item.id,
          label: "Reasoning",
          family: "reasoning",
          state: activityState(item),
          detail: { output: item.text },
        },
      },
    };
  }

  // Pending ask_user — a question item (conversational boundary, not clustered).
  if (isAskUser(item) && pendingAsks.has(item.id)) {
    const parsed = parseAskUserQuestions(item);
    if (parsed) {
      const callId = item.callId ?? item.id;
      const questions: MobileAskQuestion[] = parsed.map((q, idx) => ({
        key: `${callId}:${idx}`,
        header: q.header,
        question: q.question,
        options: q.options,
        multiSelect: q.multiSelect,
        why: q.why,
        ifUnanswered: q.ifUnanswered,
      }));
      return {
        kind: "final",
        item: {
          kind: "question",
          id: item.id,
          batch: { callId, questions },
        },
      };
    }
  }

  // Tool call (commandExecution, including answered/errored ask_user) — activity.
  // family is always "tool" for a commandExecution, even when toolName is
  // "Reasoning"; the discriminator is derived from the wire type, never the
  // label. callId is preserved exactly for diagnostics disclosure.
  if (isCommandExecution(item)) {
    const attachments = itemOutputAttachments(item);
    const failed = toolCallFailed(item);
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
          state: activityState(item),
          detail: activityDetail(item),
        },
      },
      attachments,
    };
  }

  // Steering — notice.
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
        family: tone === "warning" ? "warning" : "informational",
        tone,
        text: item.text ?? "",
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
        text: item.text ?? "",
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
      family: `unknown:${item.type}`,
      item: {
        kind: "activity",
        id: item.id,
        label: "Activity",
        family: "unknown",
        state: activityState(item),
        detail: { output: item.text },
      },
    },
  };
}

function activityDetail(item: ThreadItem): ActivityDetail {
  return {
    arguments: item.argumentsJson,
    output: item.output,
    error: item.error,
    exitCode: item.exitCode,
    durationMs: item.durationMs,
    callId: item.callId,
  };
}

function itemInputAttachments(item: ThreadItem): AttachmentRef[] | undefined {
  if (!item.images || item.images.length === 0) return undefined;
  return item.images.map((img, i) => inputAttachment(item.id, i, img));
}

function itemOutputAttachments(item: ThreadItem): AttachmentRef[] | undefined {
  if (!item.outputImages || item.outputImages.length === 0) return undefined;
  return item.outputImages.map((img, i) => outputAttachment(item.id, i, img));
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
        result.push({ ...first, state });
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

// --- top-level projection ----------------------------------------------------

export function projectThread(thread: Thread): MobileConversation {
  const turns = thread.turns ?? [];
  const pendingAsks = pendingAskUserIds(turns);

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

  for (const turn of turns) {
    const items = turn.items ?? [];
    for (const item of items) {
      const result = projectItem(item, turn, pendingAsks);
      if (result.kind === "final") {
        ordered.push({
          type: "final",
          item: result.item,
          attachments: result.attachments,
        });
      } else {
        ordered.push({
          type: "activity",
          item: result.pre.item,
          pre: result.pre,
          attachments: result.attachments,
        });
      }
    }
    // A turn error produces a failure item at the end of that turn's items.
    if (turn.error) {
      ordered.push({ type: "final", item: failureItem(turn.error) });
    }
  }

  // Second pass: cluster consecutive activity rows that share a family, then
  // rebuild the timeline in original order.
  const items: MobileTimelineItem[] = [];
  let activityRun: PreActivity[] = [];

  const flushActivityRun = () => {
    if (activityRun.length === 0) return;
    const clustered = clusterActivities(activityRun);
    for (const a of clustered) items.push(a);
    activityRun = [];
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
    } else {
      flushActivityRun();
      items.push(entry.item);
    }
    // Attachments follow the item that produced them.
    if (entry.attachments && entry.attachments.length > 0) {
      items.push({
        kind: "attachments",
        id: `${entry.item.id}:attachments`,
        items: entry.attachments,
      });
    }
  }
  flushActivityRun();

  return {
    id: thread.id,
    sessionId: thread.sessionId,
    name: thread.name,
    preview: thread.preview,
    modelProvider: thread.modelProvider,
    status: thread.status.type,
    items,
    capabilities: projectCapabilities(thread.evener.capabilities),
    queue: projectQueue(thread.evener.queue),
    usage: projectUsage(thread.evener),
    reasoningEffort: thread.evener.reasoningEffort,
    reasoningEffortLevels: thread.evener.reasoningEffortLevels,
    supportsReasoning: thread.evener.supportsReasoning,
    askPending: pendingAsks.size > 0,
  };
}

function failureItem(
  error: NonNullable<Turn["error"]>,
): Extract<MobileTimelineItem, { kind: "failure" }> {
  const title = error.title ?? error.message;
  const parts = [error.message];
  if (error.hint) parts.push(error.hint);
  if (error.additionalDetails) parts.push(error.additionalDetails);
  return {
    kind: "failure",
    id: `failure:${title}`,
    title,
    detail: parts.join("\n"),
  };
}

function projectCapabilities(caps: ThreadCapabilities): MobileCapabilities {
  return { ...caps };
}

function projectQueue(queue: Thread["evener"]["queue"]): MobileQueue {
  const depth = queue.depth ?? 0;
  const preview = queue.preview ?? queue.texts ?? [];
  return { depth, preview };
}

function projectUsage(evener: Thread["evener"]): MobileUsage {
  const usage: EvenerUsage | undefined = evener.usage;
  return {
    inputTokens: usage?.inputTokens,
    outputTokens: usage?.outputTokens,
    cacheReadTokens: usage?.cacheReadTokens,
    totalTokens: usage?.totalTokens,
    cost: evener.cost,
    contextUsed: evener.contextUsed,
    contextWindow: evener.contextWindow,
    contextRemaining: evener.contextRemaining,
    contextPressure: evener.contextPressure,
  };
}
