// Mobile conversation view models. These are the only shapes the React layer
// ever sees — project.ts folds AppWire wire types (Thread/Turn/ThreadItem from
// protocol/types.gen.ts) into this model, so protocol DTOs never cross the
// boundary into React props. Every Hub/user/agent/tool/filename field is
// untrusted plain text here; only assistant Markdown is sanitized later
// (markdown.ts). Treat string fields as display-only, never executable.

// One image or document attachment, normalized from InputItem/OutputImage.
// `src` is the already-resolved fetch URL (url ?? inline-data ?? path ?? name);
// name is carried alongside unresolved so a caption can show provenance.
export interface AttachmentRef {
  id: string;
  src: string;
  name?: string;
  mediaType?: string;
}

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
  arguments?: string;
  output?: string;
  error?: string;
  exitCode?: number;
  durationMs?: number;
  callId?: string;
}

// Tone of a steering/lifecycle notice row. "info" for ordinary steering/system
// notices, "warning" for loop detection / turn limit / provider failure, and
// "system" for environment / prelude scaffold that is purely informational.
export type NoticeTone = "info" | "warning" | "system";

// One option in a structured ask_user question. Plain text only.
export interface MobileAskOption {
  label: string;
  detail: string;
  recommended?: boolean;
}

// One flattened, individually-addressable question inside an ask_user batch.
// `key` is stable and re-derivable (callId:idx), so the same call replayed
// after a reconnect re-derives identical keys.
export interface MobileAskQuestion {
  key: string;
  header: string;
  question: string;
  options: MobileAskOption[];
  multiSelect: boolean;
  why?: string;
  ifUnanswered?: string;
}

// A batch of pending ask_user questions from one call. The composer renders
// these as interactive cards with a single "Send answers" action.
export interface AskBatch {
  callId: string;
  questions: MobileAskQuestion[];
}

// The mobile timeline item union. A pure projection of one thread's turns
// into the families the phone timeline renders. Discriminated by `kind`.
export type MobileTimelineItem =
  | { kind: "user"; id: string; text: string }
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
    }
  | { kind: "notice"; id: string; tone: NoticeTone; text: string }
  | { kind: "question"; id: string; batch: AskBatch }
  | { kind: "failure"; id: string; title: string; detail: string }
  | { kind: "attachments"; id: string; items: AttachmentRef[] };

// Capability projection: ThreadCapabilities booleans mapped 1:1 to mobile
// action availability. A false capability removes the action and leaves an
// "Unavailable for this source" explanation. Reasoning support is separate —
// it surfaces only when the thread reports supported levels.
export interface MobileCapabilities {
  send: boolean;
  steer: boolean;
  interrupt: boolean;
  compact: boolean;
  clear: boolean;
  forkFromTurn: boolean;
  shutdown: boolean;
  changeModel: boolean;
  queue: boolean;
  goal: boolean;
  rename: boolean;
}

// Queue chip projection from QueueState. `depth` is the queued turn count;
// `preview` is the queued text previews (plain text, untrusted).
export interface MobileQueue {
  depth: number;
  preview: string[];
}

// Usage projection from EvenerThread.usage plus context fields. Numbers are
// pass-through; cost is a plain string from the wire.
export interface MobileUsage {
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  totalTokens?: number;
  cost?: string;
  contextUsed?: number;
  contextWindow?: number;
  contextRemaining?: number;
  contextPressure?: number;
}

// The full mobile conversation view model. projectThread produces this from a
// Thread; React components consume only this shape, never the wire Thread.
export interface MobileConversation {
  id: string;
  sessionId: string;
  name?: string;
  preview: string;
  modelProvider: string;
  status: string;
  items: MobileTimelineItem[];
  capabilities: MobileCapabilities;
  queue: MobileQueue;
  usage: MobileUsage;
  reasoningEffort?: string;
  reasoningEffortLevels?: string[];
  supportsReasoning?: boolean;
  askPending: boolean;
}
