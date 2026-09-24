import type {
  AskQuestionRef,
  ItemImage,
  ItemModel,
  ThreadItemEventKind,
  ThreadModel,
  TranscriptDisplayConfigV1,
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
  hasWarningText,
  isActiveItem,
  joinedReasoningParagraphs,
  joinWarningParts,
  liveAskQuestions,
  parseAskUserQuestions,
  pendingTextJoined,
  projectThread,
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
};

export function projectConversation(model: ThreadModel): MobileConversation {
  return { ...model, items: projectTimeline(model) };
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

// A system notice's family, keyed by the package projector's own event-kind
// vocabulary. That vocabulary is `ThreadItemEventKind` (transcriptProjector.ts's
// KNOWN_EVENT_KINDS, re-exported as THREAD_ITEM_EVENT_KINDS), so this record is
// exhaustive: a new wire kind fails to compile until it is classified, and a kind
// outside the vocabulary stays "unknown-system". The family is the one native
// grouping concept the projector has no counterpart for, so it survives here
// while the visible/hidden/critical decision is the projector's own (see
// systemEventVisible). Every kind that warrants a warning *tone* is exactly a
// family-"warning" kind, so the tone is derived from the family below.
const SYSTEM_NOTICE_FAMILY_ENTRIES: Record<ThreadItemEventKind, NoticeFamily> = {
  system_prompt: "hidden-instruction",
  prompt_loaded: "hidden-instruction",
  environment: "system-prelude",
  round_timings: "diagnostic",
  loop_detection: "warning",
  turn_limit: "warning",
  error: "warning",
  plugin_loaded: "lifecycle",
  skill_activated: "lifecycle",
  hook_completed: "lifecycle",
  context_compaction: "lifecycle",
  compaction: "lifecycle",
  goal_ended: "lifecycle",
  fork_summary: "lifecycle",
  tool_repair: "lifecycle",
  model_switch: "lifecycle",
  "notes-context": "lifecycle",
};

// The exhaustive Record above is checked at compile time; this Map is the
// lookup, so a name that collides with an Object.prototype member
// ("constructor", "toString", "__proto__") misses instead of answering with a
// prototype value.
const SYSTEM_NOTICE_FAMILY = new Map<string, NoticeFamily>(
  Object.entries(SYSTEM_NOTICE_FAMILY_ENTRIES),
);

function systemNoticeFamily(eventKind: string | undefined): NoticeFamily {
  if (eventKind === undefined || eventKind === "") return "unknown-system";
  return SYSTEM_NOTICE_FAMILY.get(eventKind) ?? "unknown-system";
}

// The projector reads only `turns` from a ThreadModel to classify a system
// event, so the probe carries inert values for the rest of the required shape.
const SYSTEM_EVENT_PROBE: Omit<ThreadModel, "turns"> = {
  ref: "system-event-probe",
  threadId: "system-event-probe",
  name: "system-event-probe",
  status: { type: "idle" },
  modelProvider: "",
  model: "",
  visionModel: "",
  askPending: false,
  pendingEscalations: [],
  queue: null,
  tasks: null,
  jobsUpdatedAt: null,
  jobsTreeRevision: null,
  lastFrameAt: 0,
  capabilities: {
    send: false,
    steer: false,
    interrupt: false,
    compact: false,
    clear: false,
    forkFromTurn: false,
    shutdown: false,
    changeModel: false,
    changeVisionModel: false,
    sharedNotes: false,
    queue: false,
    goal: false,
    rename: false,
  },
  goal: null,
  humanNote: "",
  agentNote: "",
  sessionUrls: [],
  contextUsed: 0,
  contextWindow: 0,
  contextPressure: 0,
  usage: null,
  workMillis: 0,
  reasoningEffortLevels: [],
  supportsReasoning: false,
  cwd: "",
};

// The projector's own visible/hidden/critical decision for one system event,
// consumed rather than re-derived: its gate sets (transcriptProjector.ts:98-121)
// are internal to the package, so native asks the exported projectThread instead
// of copying them. A one-item probe thread answers "did this event survive".
//
// The verdict depends only on the event kind, the exit-code class, and the four
// advanced gates (systemDecision reads nothing else), so one result per distinct
// input is cached: a transcript's notices re-check the same handful of keys on
// every re-projection instead of rebuilding a probe thread each time.
const SYSTEM_EVENT_VISIBILITY = new Map<string, boolean>();

function exitCodeClass(exitCode: number | undefined): string {
  if (exitCode === undefined) return "?";
  return exitCode === 0 ? "0" : "!";
}

// A one-item thread for classifying a single system event. projectThread reads
// only `turns`, so the rest of the required shape is inert.
export function systemEventProbe(
  eventKind: string | undefined,
  exitCode: number | undefined,
): ThreadModel {
  return {
    ...SYSTEM_EVENT_PROBE,
    turns: [
      {
        id: "system-event-probe",
        status: "completed",
        items: [
          {
            id: "system-event-probe",
            turnId: "system-event-probe",
            type: "systemMessage",
            text: "",
            eventKind,
            exitCode,
          },
        ],
      },
    ],
  };
}

export function systemEventVisible(
  eventKind: string | undefined,
  exitCode: number | undefined,
  config: TranscriptDisplayConfigV1,
): boolean {
  // The projector renders a kind outside its vocabulary unconditionally, and
  // SYSTEM_NOTICE_FAMILY is exactly that vocabulary, so answer without a probe
  // (and keep the cache below bounded to the known kinds).
  if (eventKind === undefined || eventKind === "" || !SYSTEM_NOTICE_FAMILY.has(eventKind))
    return true;
  const advanced = config.advanced;
  const key = `${eventKind}\u0000${exitCodeClass(exitCode)}\u0000${advanced.hookExits}\u0000${advanced.promptEvents ? 1 : 0}${advanced.roundTimings ? 1 : 0}${advanced.systemEvents ? 1 : 0}`;
  const cached = SYSTEM_EVENT_VISIBILITY.get(key);
  if (cached !== undefined) return cached;
  const visible = projectThread(systemEventProbe(eventKind, exitCode), config).turns[0]?.entries.length === 1;
  SYSTEM_EVENT_VISIBILITY.set(key, visible);
  return visible;
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

// --- activity state ----------------------------------------------------------

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

// Exported for the live store (state/conversation.ts): the folded model item
// (reducer.applyNotification's mergeItemImages) already carries the
// "absent/empty input images means unchanged" rule projectItemAttachments
// below cannot honor from a raw wire item alone.
export function itemAttachments(item: ItemModel): AttachmentRef[] | undefined {
  // Human steering uses the same message and image presentation as user input.
  if (isUserMessage(item) || (isSteering(item) && item.source === "user")) {
    return attachmentRows(item.id, item.images);
  }
  if (isCommandExecution(item)) {
    return attachmentRows(item.id, item.outputImages, "out:");
  }
  return undefined;
}

// activityState settles a tool/reasoning row's lifecycle state. Unexported:
// the store's own live path (item/started, item/completed) that used to need
// this as a second copy is gone — the D23c-2b cutover reads every row from
// projectConversation now — so only this file's own projectItem calls it.
function activityState(
  item: ItemModel,
  turn: Pick<TurnModel, "status">,
): ActivityState {
  if (hasItemFailure(item)) return "failed";
  if (isActiveItem(item, turn.status)) return "running";
  return "completed";
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

// liveAskQuestions has no memory of its own (its own doc comment) — it rescans
// every turn's items and re-parses every pending ask_user's argumentsJson on
// every call, gated on model.askPending (#1731 piece A round 4). Keyed on
// model.turns AND model.askPending, this reuses ONE scan for every caller
// that shares both: projectTimeline's own default argument below, and
// pendingQuestions (mobile-native/src/questionAnswers.ts), which both run
// against the same conversation within one publish. It does not make the scan
// itself incremental — the reducer (reducer.ts's mapTurn/settleFirstMatchingTurn)
// returns a new turns array on every fold, even when only the newest turn
// changed, so a delta still pays for one scan; this removes paying for it twice
// or more within that one delta.
//
// askPending has to be part of the key, not just turns: a status-only frame
// (conversation.ts's changesRows) can flip askPending while handing back the
// SAME turns reference, and a memo keyed on turns alone would serve the
// answer it cached under the OLD flag — an already-projected tool row
// staying stuck instead of becoming its question row, or the reverse.
const asksByTurns = new WeakMap<
  readonly TurnModel[],
  { askPending: boolean; asks: ReadonlyMap<string, AskQuestionRef[]> }
>();

export function liveAsksFor(
  model: ThreadModel,
): ReadonlyMap<string, AskQuestionRef[]> {
  const cached = asksByTurns.get(model.turns);
  if (cached !== undefined && cached.askPending === model.askPending) {
    return cached.asks;
  }
  const asks = askQuestionsByCall(model);
  asksByTurns.set(model.turns, { askPending: model.askPending, asks });
  return asks;
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

// A reasoning item's text as the reader sees it. Two different fields can be
// stale, depending on how the item settled. A completion that CARRIES text
// re-seeds reasoningSummaries from it (reducer.ts's mergeReasoning), so the
// two read equal lengths and text wins the tie as the authoritative value.
// A SPARSE completion — one that omits text — leaves text as the stale
// item/started seed while the summaries hold everything the deltas streamed
// (appendReasoningDelta appends to summaries only, never to text), so the
// longer joined summary is the honest read and preferring text would revert
// the row to the seed (RoboRev round 14). Comparing lengths distinguishes
// the two without a third model field.
function reasoningText(item: ItemModel): string {
  const joinedSummary = joinedReasoningParagraphs(item.reasoningSummaries).join("\n\n");
  return joinedSummary.length > item.text.length ? joinedSummary : item.text || joinedSummary;
}

// Returns null for an item with nothing to show — the timeline then carries
// no row for it.
function projectItem(
  item: ItemModel,
  turn: TurnModel,
  asks: ReadonlyMap<string, AskQuestionRef[]>,
): PreResult | null {
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
    const state: ActivityState = isActiveItem(item, turn.status) ? "running" : "completed";
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
          state: activityState(item, turn),
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
    const family = systemNoticeFamily(item.eventKind);
    // A family-"warning" kind is exactly the set that warrants a warning tone
    // (loop_detection, turn_limit, error), so the tone follows the family rather
    // than a second event-kind table.
    const tone: NoticeTone = family === "warning" ? "warning" : "system";
    return {
      kind: "final",
      item: {
        kind: "notice",
        id: item.id,
        origin: "system",
        family,
        tone,
        text: item.text,
        ...(item.eventKind ? { eventKind: item.eventKind } : {}),
        ...(item.exitCode !== undefined ? { exitCode: item.exitCode } : {}),
      },
    };
  }

  // Warning — the same attention row the live applier emits (see warningItem).
  if (item.type === "warning") {
    // A warning carrying no title, no message and no hint has nothing to
    // show: the web renders no row for exactly this case (WarningItem.tsx
    // returns null when every part is blank), so neither does the phone.
    // The reducer's own fold already fills a blank message with the bounded
    // raw frame (rawWarningFrame), so only a hand-built model can reach this.
    if (joinWarningParts([item.warning?.title, item.text, item.warning?.hint]) === "") {
      return null;
    }
    return { kind: "final", item: warningItem(item) };
  }

  // Unknown / forward-compatible item type — neutral collapsed activity, never
  // disappearing, never exposing raw HTML. The dangerous text lives in detail
  // as plain text the renderer escapes; the label stays neutral. family is
  // "unknown" for any item type the projection does not recognize.
  const state = activityState(item, turn);
  return {
    kind: "activity",
    pre: {
      family: state === "failed" ? `failed:${item.id}` : `unknown:${item.type}`,
      item: {
        kind: "activity",
        id: item.id,
        label: "Activity",
        family: "unknown",
        state,
        detail: { ...activityDetail(item), output: item.text || item.output },
      },
    },
  };
}

// A warning's display row. It is an attention row, not an activity: the live
// row applier (state/conversation.ts's case "warning") emits kind "failure"
// with title as its own field and the message+hint joined as detail, and this
// canonical projection must produce the same row for the same model item or
// the row changes kind (and loses its attention treatment) the moment a reread
// replaces the live row. The generic unknown-activity fallback used to swallow
// warnings here (label "Activity", family "unknown", always shown in full by
// mobile-native's activityMode) - the web (WarningItem.tsx) renders a warning
// as its own attention banner, and the package transcript projector
// (transcriptProjector.ts) routes type "warning" to a critical entry.
// item.warning rides an untyped wire param map through the reducer's fold, so
// hasWarningText guards a non-string runtime value the same way WarningItem.tsx
// does before the title reaches a React Native <Copy> child.
function warningItem(
  item: ItemModel,
): Extract<MobileTimelineItem, { kind: "failure" }> {
  const rawTitle = item.warning?.title;
  const title = hasWarningText(rawTitle) ? rawTitle : "Warning";
  return {
    kind: "failure",
    id: item.id,
    title,
    detail: joinWarningParts([item.text, item.warning?.hint]),
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

// One run of consecutive same-family activities becomes one row: its first
// member's identity and detail, running if any member runs, with the members
// carried for the renderer that expands them. A run of one is that row itself.
// The caller groups by family (projectTimeline's flushActivityRun), so this is
// handed a homogeneous run and does no regrouping of its own.
function clusterActivityRun(
  run: PreActivity[],
): Extract<MobileTimelineItem, { kind: "activity" }> | undefined {
  const first = run[0]?.item;
  if (first === undefined) return undefined;
  if (run.length === 1) return first;
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
  return { ...first, state, members };
}

// --- timeline projection -----------------------------------------------------

// One projected row before clustering, carrying whether it may still be merged
// into a run and any attachments it emitted alongside itself.
interface Ordered {
  type: "final" | "activity";
  item: MobileTimelineItem;
  pre?: PreActivity;
  attachments?: AttachmentRef[];
}

// One turn's rows, and what outside the turn they depended on.
interface TurnRows {
  entries: Ordered[];
  // A turn's own items decide its rows, with one exception: whether an ask_user
  // call is still answerable is a whole-model question (a later turn's user
  // message answers an earlier ask — deriveAskQuestions.ts). Each entry records
  // the calls this turn consumed and whether they were answerable, so the rows
  // are reused only while that still holds.
  askState: Array<[string, boolean]>;
}

// Per-turn rows, keyed on the TurnModel reference. The reducer hands a turn back
// UNTOUCHED — by reference — when a frame did not change it (reducer.ts's mapTurn
// and settleFirstMatchingTurn), so a delta into the newest turn leaves every older
// turn's rows exactly as they were. Re-deriving them per frame is the transcript's
// whole width of work, including a JSON parse per ask and two Date.parse calls per
// timed tool call, for one item's text. A WeakMap so a dropped turn's rows go with
// it.
const turnRowCache = new WeakMap<TurnModel, TurnRows>();

function rowsForTurn(
  turn: TurnModel,
  asks: ReadonlyMap<string, AskQuestionRef[]>,
): Ordered[] {
  const cached = turnRowCache.get(turn);
  if (
    cached !== undefined &&
    cached.askState.every(([callId, answerable]) => asks.has(callId) === answerable)
  ) {
    return cached.entries;
  }
  const entries: Ordered[] = [];
  const askState: Array<[string, boolean]> = [];
  for (const item of turn.items) {
    if (isAskUser(item)) {
      const callId = item.callId ?? item.id;
      askState.push([callId, asks.has(callId)]);
    }
    const result = projectItem(item, turn, asks);
    if (result === null) continue;
    const identity = {
      ...(item.transcriptKey ? { transcriptKey: item.transcriptKey } : {}),
      ...(item.position ? { position: item.position } : {}),
    };
    if (result.kind === "final") {
      entries.push({
        type: "final",
        item: { ...result.item, ...identity },
        attachments: result.attachments,
      });
    } else {
      const item_ = { ...result.pre.item, ...identity };
      entries.push({
        type: "activity",
        item: item_,
        pre: { ...result.pre, item: item_ },
        attachments: result.attachments,
      });
    }
  }
  // A turn error produces a failure item at the end of that turn's items.
  if (turn.error) {
    entries.push({
      type: "final",
      item: failureItem(turn.error as NonNullable<Turn["error"]>, turn.id),
    });
  }
  turnRowCache.set(turn, { entries, askState });
  return entries;
}

// The attachments row that follows the row which produced it. It points back at
// its source by transcript key, so a page or a reread that reissues the source
// under a new wire id does not orphan its images. An activity's images name the
// source's id when it has no key — a clustered member's row can be rebuilt around
// a different member, and the id is then the only handle left — while any other
// row leaves the field off and lets the reader of the row derive it from the
// row's own id (state/conversation.ts's attachmentSourceId).
function attachmentsRow(
  source: MobileTimelineItem,
  attachments: AttachmentRef[],
  fallbackToId = false,
): { id: string; items: AttachmentRef[]; sourceTranscriptKey?: string } {
  const key = source.transcriptKey ?? (fallbackToId ? source.id : undefined);
  return {
    id: `${source.id}:attachments`,
    items: attachments,
    ...(key === undefined ? {} : { sourceTranscriptKey: key }),
  };
}

export function projectTimeline(
  model: ThreadModel,
  // The answerable asks, when the caller has already derived them.
  asks: ReadonlyMap<string, AskQuestionRef[]> = liveAsksFor(model),
): MobileTimelineItem[] {
  // Project every item in order, preserving whether it is a final item or a
  // clusterable activity pre-item. Attachments emitted alongside an item
  // follow that item in the timeline. Rows already derived for an unchanged turn
  // come from the cache above; clustering then runs over the whole result,
  // because a run of activities can span a turn boundary.
  const ordered: Ordered[] = [];
  for (const turn of model.turns) ordered.push(...rowsForTurn(turn, asks));

  // Second pass: cluster consecutive activity rows that share a family, then
  // rebuild the timeline in original order.
  const items: MobileTimelineItem[] = [];
  let activityRun: PreActivity[] = [];
  let activityAttachments: Array<{ id: string; items: AttachmentRef[]; sourceTranscriptKey?: string }> = [];

  const flushActivityRun = () => {
    if (activityRun.length === 0) return;
    const clustered = clusterActivityRun(activityRun);
    if (clustered !== undefined) items.push(clustered);
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
        activityAttachments.push(attachmentsRow(entry.item, entry.attachments, true));
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
      items.push({ kind: "attachments", ...attachmentsRow(entry.item, entry.attachments) });
    }
  }
  flushActivityRun();

  return items;
}

// The identity a turn's failure row carries — the turn id under the
// failure: prefix. Exported because the store's page-ownership checks read
// the same identity loadOlder recorded for a paginated failure row (the
// failure row is the turn's, not an item's, so this is the one spelling
// both sides must agree on).
export function failureRowIdentity(turnID: string): string {
  return `failure:${turnID}`;
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
    // The id names the turn only, never the error's prose: rowsForTurn calls
    // this once per turn (at most one error per turn), so turnID alone is
    // already unique, and the display bound (mobile/src/state/conversation.ts)
    // cuts title/detail but not id — an id built from unbounded prose would
    // stay oversized forever in timelineIdentity, page-ownership sets, list
    // keys and this row's own JSON serialization.
    id: failureRowIdentity(turnID),
    title,
    detail: parts.join("\n"),
  };
}

// --- row identity -------------------------------------------------------------
// Which row is which, for every consumer that has to decide whether two rows are
// the same row: the store's page merge and its cap bookkeeping, and any reader
// deduping a reissued row. transcriptKey first, because the hub reissues an item
// under a new wire id while its transcript key stands.

export function attachmentSourceId(item: MobileTimelineItem): string | null {
  return item.kind === "attachments" && item.id.endsWith(":attachments")
    ? item.id.slice(0, -":attachments".length)
    : null;
}

export function attachmentSourceIdentity(item: MobileTimelineItem): string | null {
  return item.kind === "attachments"
    ? (item.sourceTranscriptKey ?? attachmentSourceId(item))
    : null;
}

export function timelineIdentity(item: MobileTimelineItem): string {
  return item.transcriptKey ?? item.id;
}

// The canonical identity of a clustered activity member — the same
// transcriptKey-first rule timelineIdentity applies to a top-level row.
export function activityIdentity(activity: ActivityMember): string {
  return activity.transcriptKey ?? activity.id;
}

// The identities a row IS: its own, plus every clustered member's. Distinct
// from timelineIdentities, which also carries the identity of the row an
// attachment belongs to — an attachment is not a duplicate of its source.
export function ownTimelineIdentities(item: MobileTimelineItem): Set<string> {
  const identities = new Set([timelineIdentity(item)]);
  if (item.kind === "activity" && item.members) {
    for (const member of item.members) {
      identities.add(activityIdentity(member));
    }
  }
  return identities;
}

export function timelineIdentities(item: MobileTimelineItem): Set<string> {
  const identities = ownTimelineIdentities(item);
  const source = attachmentSourceIdentity(item);
  if (source !== null) identities.add(source);
  return identities;
}

// --- display bounds: limits and truncation helpers (centralized) -----------
// What a row may cost the reader's device. The store applies these on every
// publish; they live here with the row shape they cut.

export const MAX_ITEM_BYTES = 64 * 1024; // 64 KiB in UTF-8 bytes
export const TRUNCATION_MARKER = "… truncated";
export const RETAINED_ITEM_CAP = 500;

// Truncate a string to maxBytes in UTF-8, ending with "… truncated" exactly
// once whenever the limit is large enough to hold the marker. Iterates
// Unicode scalar values (not UTF-16 code units) so no surrogate pairs are
// split and no U+FFFD replacement chars are produced. The result never
// exceeds maxBytes.
const textEncoder = new TextEncoder();
const markerBytes = textEncoder.encode(TRUNCATION_MARKER);

// UTF-8 byte length without materialising the bytes: every publish asks this of
// every retained row, and all but the oversized ones only need the answer, not the
// encoding. Counting code points is O(length) with no allocation, where
// TextEncoder.encode allocates a byte array as large as the text (and the row that
// is 64 KiB of text allocates it on every publish).
function utf8Length(text: string): number {
  let bytes = 0;
  for (const cp of text) {
    const code = cp.codePointAt(0) ?? 0;
    bytes += code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
  }
  return bytes;
}

export function truncateText(text: string, maxBytes: number): string {
  if (utf8Length(text) <= maxBytes) return text;
  // The byte limit is the hard contract: the marker is best effort. Every
  // publish re-applies this to whatever text a row carries, so a limit too
  // small to hold the marker yields the longest prefix that fits, with no
  // marker, rather than a marker that busts the limit.
  const fitsMarker = maxBytes >= markerBytes.length;
  const marker = fitsMarker ? TRUNCATION_MARKER : "";
  const markerLength = fitsMarker ? markerBytes.length : 0;
  const targetBytes = Math.max(0, maxBytes - markerLength);
  // Iterate code points (for...of iterates Unicode scalar values) to find
  // the longest prefix whose UTF-8 encoding fits within targetBytes. This
  // avoids splitting surrogate pairs and never produces U+FFFD.
  let byteLen = 0;
  let cutIdx = 0;
  for (const cp of text) {
    const cpBytes = utf8Length(cp);
    if (byteLen + cpBytes > targetBytes) break;
    byteLen += cpBytes;
    cutIdx += cp.length;
  }
  return text.slice(0, cutIdx) + marker;
}

// One text field, cut to the display bound.
export type BoundText = (text: string) => string;

// Apply the bound to an activity detail's text-bearing fields (description,
// arguments, output, error). Shared by an activity's own top-level detail and
// each of its clustered members' details, so both are bounded the same way. The
// description is the summary line a collapsed row shows
// (mobile-native/src/transcriptPresentation.ts's actionSummary), so it is read
// as much as the output is.
function truncateActivityDetail(detail: ActivityDetail, bound: BoundText): ActivityDetail {
  return {
    ...detail,
    description: detail.description ? bound(detail.description) : detail.description,
    arguments: detail.arguments ? bound(detail.arguments) : detail.arguments,
    output: detail.output ? bound(detail.output) : detail.output,
    error: detail.error ? bound(detail.error) : detail.error,
  };
}

// One question's prose and option text, bounded like every other row's:
// header, question, why, ifUnanswered, and every option's label and detail
// (AskUserOption.detail is a required string, never undefined). Used only by
// truncateItem's "question" case below: the DISPLAY rows a reader scrolls
// carry these cut copies. Nothing that answers a question reads them —
// mobile-native's pendingQuestions (questionAnswers.ts) asks the model for
// the canonical refs through liveAsksFor, the same call these rows were
// projected from — so a submitted answer always names exactly the label the
// agent offered, never a cut remnant, and two options sharing a prefix
// longer than the bound stay distinguishable to the answer composer.
export function boundQuestion(
  question: AskQuestionRef,
  bound: BoundText,
): AskQuestionRef {
  return {
    ...question,
    header: bound(question.header),
    question: bound(question.question),
    ...(question.why === undefined ? {} : { why: bound(question.why) }),
    ...(question.ifUnanswered === undefined
      ? {}
      : { ifUnanswered: bound(question.ifUnanswered) }),
    options: question.options.map((option) => ({
      ...option,
      label: bound(option.label),
      detail: bound(option.detail),
    })),
  };
}

// Apply the bound to every text a row carries for the reader. Native transcript
// projection expands a clustered activity's members directly, so each member's
// own detail is bounded too — not just the cluster's top-level detail (the first
// member's). A pasted user message, a daemon notice and a tool failure's stack
// are as large as anything that streams, so each kind that carries prose is here.
export function truncateItem(item: MobileTimelineItem, bound: BoundText): MobileTimelineItem {
  switch (item.kind) {
    case "user":
      return { ...item, text: bound(item.text) };
    case "assistant":
      return { ...item, markdown: bound(item.markdown) };
    case "notice":
      return { ...item, text: bound(item.text) };
    case "failure":
      return { ...item, title: bound(item.title), detail: bound(item.detail) };
    case "question":
      return {
        ...item,
        questions: item.questions.map((question) => boundQuestion(question, bound)),
      };
    case "activity":
      // The label is rendered twice on the phone — the disclosure line and its
      // accessibility label (mobile-native/src/TimelineItem.tsx) — so it is
      // bounded like the detail it heads, for the row and for every member.
      return {
        ...item,
        label: bound(item.label),
        detail: truncateActivityDetail(item.detail, bound),
        ...(item.members
          ? {
              members: item.members.map((member) => ({
                ...member,
                label: bound(member.label),
                detail: truncateActivityDetail(member.detail, bound),
              })),
            }
          : {}),
      };
    case "attachments":
      // Only the display name is bounded — it is plain display text the
      // renderer inserts into accessibility labels and modal copy. src is a
      // data URI or a resolved fetch URL, and cutting it yields something the
      // renderer cannot decode, so it passes through verbatim.
      return {
        ...item,
        items: item.items.map((attachment) => ({
          ...attachment,
          name: attachment.name ? bound(attachment.name) : attachment.name,
        })),
      };
    default:
      // A forward-compatible row kind: nothing here knows its fields, so it
      // passes through untouched rather than guessed at.
      return item;
  }
}

// Enforce the 500-item retained cap. Always retains the NEWEST items (end
// of array) so the live tail is preserved for interactive scrolling.
//
// The projector always emits an attachment immediately after the row it
// belongs to, so a source/attachment pair can only ever straddle the cut at
// the front of the slice — an attachment kept while its source, one slot
// earlier, was not. Dropping the source and leaving the attachment is worse
// than dropping both: the orphan's lingering identity (timelineIdentities
// exposes an attachment's source alongside its own) makes loadOlder treat a
// genuine older-page copy of that source as an already-seen duplicate
// (conversation.ts's F10 admission rule), permanently refusing to recover
// it. Drop every leading attachment whose source did not survive the cut.
export function capItems(items: MobileTimelineItem[]): MobileTimelineItem[] {
  if (items.length <= RETAINED_ITEM_CAP) return items;
  const sliced = items.slice(items.length - RETAINED_ITEM_CAP);
  // The identity set is only ever consulted for a leading run of orphaned
  // attachments (the loop below breaks the moment sliced[orphaned] is not an
  // attachment) — skip building it over all 500 rows on the common publish
  // where the cut did not land on an attachment at all.
  if (attachmentSourceIdentity(sliced[0]) === null) return sliced;
  const keptIds = new Set(sliced.flatMap((item) => [...ownTimelineIdentities(item)]));
  let orphaned = 0;
  while (orphaned < sliced.length) {
    const sourceId = attachmentSourceIdentity(sliced[orphaned]);
    if (sourceId === null || keptIds.has(sourceId)) break;
    orphaned++;
  }
  return orphaned === 0 ? sliced : sliced.slice(orphaned);
}
