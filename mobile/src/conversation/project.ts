import type {
  AskQuestionRef,
  AskUserOption,
  InputItem,
  ItemImage,
  OutputImage,
  Thread,
  ThreadItem,
  ThreadModel,
  Turn,
} from "@evener/appwire-client";
// The native shim between the wire and the package's thread model. It shrinks
// as seam 2 lands (SDK migration plan, D21–D24): D22 replaces projectThread's
// hydrate half with reducer.hydrateThread, D23 the store's notification
// appliers with reducer.applyNotification, and D24 replaces the display rows
// below with transcriptDisplay/projector.ts entries and deletes this file.
//
// projectThread folds a wire Thread (types.gen.ts) into a MobileConversation.
// No DOM, no network, no clock — given the same Thread it produces the same
// MobileConversation. Protocol DTOs never cross into React props; only the
// package's model types and the display rows declared here do.
//
// Forward-compatibility is load-bearing: an unknown ThreadItem.type never
// disappears and never exposes raw HTML. It becomes a neutral collapsed
// activity row. Every Hub/user/agent/tool/filename field is untrusted plain
// text here; the sanitizer (markdown.ts) is the only place assistant Markdown
// is interpreted, and that runs downstream of this projection.

import {
  hasItemFailure,
  isInProgressStatus,
} from "@evener/appwire-client";

// --- the conversation native holds -------------------------------------------

// The package ThreadModel's thread-level contract — every field a native screen
// reads, in the web's shape — plus the display rows the shim still projects
// itself. A full ThreadModel is assignable here, which is what D22's
// hydrateThread hands in; D24 removes `items`.
export type MobileConversation = Pick<
  ThreadModel,
  | "threadId"
  | "instanceId"
  | "name"
  | "status"
  | "resumeRequired"
  | "modelProvider"
  | "visionModel"
  | "reasoningEffort"
  | "reasoningEffortLevels"
  | "supportsReasoning"
  | "capabilities"
  | "queue"
  | "usage"
  | "cost"
  | "goal"
  | "tasks"
  | "askPending"
  | "pendingEscalations"
  | "activeTurnId"
> & {
  items: MobileTimelineItem[];
};

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
// The projection sets this from the wire item's *type* — commandExecution
// (tool) vs reasoning vs anything else — never from the label string, so a
// commandExecution whose toolName is "Reasoning" is still family "tool" and a
// reasoning item is family "reasoning". Closed type: tool | reasoning | unknown.
// Consumers branch on `family`, never on `label`, so a renamed or localized
// label cannot change an item's family.
export type ActivityFamily = "tool" | "reasoning" | "unknown";

// Expandable detail behind a one-line activity card. Every field is plain
// text — never raw HTML — and may be truncated by the renderer. `arguments`
// is the tool's argumentsJson verbatim (untrusted JSON text), `output` is the
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
      // projection sets this from the wire item's type (commandExecution →
      // "tool", reasoning → "reasoning", anything else → "unknown"), never from
      // the label text. Required: every activity constructor MUST set it to a
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

// A live item can arrive without any status of its own while the turn that
// contains it is still running — a sparse running tool/reasoning row would
// otherwise read as settled. Such an item is active exactly when its turn
// is; an item that carries its own status always keeps it. Exported so the
// store's incremental projection applies the same rule against the turn
// status it derives from the active turn.
export function isActiveItem(
  item: ThreadItem,
  turnStatus: string | undefined,
): boolean {
  if (item.status !== undefined) return isInProgressStatus(item.status);
  return isInProgressStatus(turnStatus);
}

// --- activity state ----------------------------------------------------------

// Exported so the store's incremental projection settles a tool item exactly
// as the canonical projector does, instead of keeping a second copy.
export function activityState(
  item: ThreadItem,
  turnStatus: string | undefined,
): ActivityState {
  if (hasItemFailure(item)) return "failed";
  if (isActiveItem(item, turnStatus)) return "running";
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
  return { id: `${itemId}:${index}`, src, name: img.name };
}

function outputAttachment(
  itemId: string,
  index: number,
  img: OutputImage,
): AttachmentRef {
  const src = img.url ?? img.path ?? img.name ?? img.source ?? "";
  return { id: `${itemId}:out:${index}`, src, name: img.name };
}

// --- ask_user question parsing ----------------------------------------------
// Mirrors parseAskUserQuestions (protocol/askShared.ts): defensive throughout.
// Malformed argumentsJson degrades to a fallback (undefined) rather than
// throwing, since this is untrusted wire JSON.

interface ParsedAskQuestion {
  header: string;
  question: string;
  options: AskUserOption[];
  multiSelect: boolean;
  why?: string;
  ifUnanswered?: string;
}

function parseOption(raw: unknown): AskUserOption | undefined {
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
    .filter((o): o is AskUserOption => o !== undefined);
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
      !hasItemFailure(item) &&
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
  // Human steering uses the same message and image presentation as user input.
  if (isUserMessage(item) || (isSteering(item) && item.source === "user")) {
    const attachments = projectItemAttachments(item);
    return {
      kind: "final",
      item: {
        kind: "user",
        id: item.id,
        text: item.text ?? "",
        ...(isUserMessage(item) && item.transcriptEntryIndex !== undefined
          ? { transcriptEntryIndex: item.transcriptEntryIndex }
          : {}),
      },
      attachments,
    };
  }

  // Assistant message — streaming while the turn is still in progress.
  if (isAgentMessage(item)) {
    const markdown = `${item.text ?? ""}${item.delta ?? ""}`;
    const streaming = isInProgressStatus(turn.status);
    return {
      kind: "final",
      item: { kind: "assistant", id: item.id, markdown, streaming },
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
          detail: { ...activityDetail(item), output: item.text },
        },
      },
    };
  }

  // Pending ask_user — a question item (conversational boundary, not clustered).
  if (isAskUser(item) && pendingAsks.has(item.id)) {
    const parsed = parseAskUserQuestions(item);
    if (parsed) {
      const callId = item.callId ?? item.id;
      const questions: AskQuestionRef[] = parsed.map((q, idx) => ({
        key: `${callId}:${idx}`,
        callId,
        header: q.header,
        question: q.question,
        options: q.options,
        multiSelect: q.multiSelect,
        why: q.why,
        ifUnanswered: q.ifUnanswered,
      }));
      return {
        kind: "final",
        item: { kind: "question", id: item.id, questions },
      };
    }
  }

  // Tool call (commandExecution, including answered/errored ask_user) — activity.
  // family is always "tool" for a commandExecution, even when toolName is
  // "Reasoning"; the discriminator is derived from the wire type, never the
  // label. callId is preserved exactly for diagnostics disclosure.
  if (isCommandExecution(item)) {
    const attachments = projectItemAttachments(item);
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
        ...(item.eventKind ? { eventKind: item.eventKind } : {}),
        ...(item.exitCode !== undefined ? { exitCode: item.exitCode } : {}),
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
        detail: { ...activityDetail(item), output: item.text ?? item.output },
      },
    },
  };
}

function activityDescription(item: ThreadItem): string | undefined {
  if (item.description?.trim() || !isAskUser(item)) return item.description;
  const questions = parseAskUserQuestions(item);
  if (!questions) return item.description;
  // Describe the posted questions without inferring answers from later input.
  return `Questions: ${questions
    .map((question, index) => question.header.trim() || `Question ${index + 1}`)
    .join("; ")}`;
}

function activityDetail(item: ThreadItem): ActivityDetail {
  return {
    description: activityDescription(item),
    arguments: item.argumentsJson,
    output: item.output,
    error: item.error,
    exitCode: item.exitCode,
    durationMs: item.durationMs,
    callId: item.callId,
  };
}

export function projectItemAttachments(
  item: ThreadItem,
): AttachmentRef[] | undefined {
  if (isUserMessage(item) || (isSteering(item) && item.source === "user")) {
    if (!item.images?.length) return undefined;
    return item.images.map((img, i) => inputAttachment(item.id, i, img));
  }
  if (isCommandExecution(item)) {
    if (!item.outputImages?.length) return undefined;
    return item.outputImages.map((img, i) => outputAttachment(item.id, i, img));
  }
  return undefined;
}

// --- clustering pass ---------------------------------------------------------
// Merge consecutive activity pre-items that share a cluster family into a
// single activity row keyed by the first member. The cluster state is running
// if any member is running; otherwise completed (failed members never join a
// run, so a cluster is never failed).

export function clusterActivities(
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
      ordered.push({ type: "final", item: failureItem(turn.error, turn.id) });
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

  // Thread-level fields follow reducer.hydrateThread's defaults (name,
  // visionModel, reasoning profile, usage, cost) so a screen reading this
  // shape reads the same values the web does. Two derivations stay native's
  // until D22 reconciles them with hydrateThread: instanceId defaults to the
  // thread id (the store fences on it), and pendingEscalations keeps the
  // threadId/ref filter.
  return {
    threadId: thread.id,
    activeTurnId:
      thread.evener.activeTurnId ||
      thread.turns?.find((turn) => isInProgressStatus(turn.status))?.id,
    instanceId: thread.evener.instanceId ?? thread.id,
    name: thread.name ?? "",
    modelProvider: thread.modelProvider,
    visionModel: thread.evener.visionModel ?? "",
    status: thread.status,
    resumeRequired: thread.evener.resumeRequired === true,
    items,
    capabilities: thread.evener.capabilities,
    queue: thread.evener.queue,
    usage: thread.evener.usage ?? null,
    cost: thread.evener.cost ?? null,
    reasoningEffort: thread.evener.reasoningEffort,
    reasoningEffortLevels: thread.evener.reasoningEffortLevels ?? [],
    supportsReasoning: thread.evener.supportsReasoning ?? false,
    goal: thread.evener.goal ?? null,
    tasks: thread.evener.tasks ?? null,
    askPending: pendingAsks.size > 0,
    pendingEscalations: (thread.evener.pendingEscalations ?? []).filter(
      (value) =>
        value.threadId === thread.id && value.ref === thread.evener.ref,
    ),
  };
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

