import type { ContentSizeCategory } from "../native/contract";

export type ConceptId = "stillwater" | "constellation" | "field-notes";
export type LiveConceptSurface = "sessions" | "conversation" | "work";
export type Platform = "ios" | "android";
export type Appearance = "system" | "light" | "dark";
export type TextScale = ContentSizeCategory;
export type DisplayTone =
  | "attention"
  | "running"
  | "success"
  | "failed"
  | "idle"
  | "unknown";

export interface LiveConnectionView {
  status: "connecting" | "reconnecting" | "connected" | "offline" | "error";
  evidence?: {
    serverName: string;
    serverVersion: string;
    protocolVersion: string;
    appVersion: string;
  };
}

export interface LiveRosterRow {
  key: string;
  title: string;
  project: string;
  summary: string;
  updatedLabel: string;
  tone: DisplayTone;
  connectedWorkCount: number;
}

export interface LiveRosterView {
  status: "idle" | "loading" | "ready" | "error" | "offline";
  query: string;
  groups: ReadonlyArray<{
    id: "needsYou" | "running" | "recent";
    label: string;
    rows: readonly LiveRosterRow[];
  }>;
  hasMore: boolean;
  error: string | null;
}

export interface BoundedDisplayText {
  readonly text: string;
  readonly truncated: boolean;
  readonly originalUtf8Bytes: number;
}

export type ConversationDisplayItem =
  | NarrativeDisplayItem
  | ActivityMarkerDisplayItem;

export interface NarrativeDisplayItem {
  readonly key: string;
  readonly sourceKind: "user" | "assistant" | "question" | "failure";
  readonly body: BoundedDisplayText;
  readonly label: BoundedDisplayText | null;
  readonly tone: DisplayTone;
  readonly streaming: boolean;
  readonly questionKey: string | null;
  readonly evidenceKey: string | null;
  readonly sequence: string;
}

export interface ActivityMarkerDisplayItem {
  readonly key: string;
  readonly sourceKind:
    | "notice"
    | "system"
    | "reasoning"
    | "tool"
    | "attachment"
    | "diagnostic"
    | "unknown";
  readonly semanticKind:
    | "notice"
    | "warning-notice"
    | "system-context"
    | "system-activity"
    | "reasoning"
    | "tool"
    | "attachment"
    | "activity";
  readonly label: BoundedDisplayText;
  readonly preview: BoundedDisplayText | null;
  readonly duration: BoundedDisplayText | null;
  readonly tone: DisplayTone;
  readonly state: "running" | "completed" | "failed" | "unavailable";
  readonly evidenceKey: string | null;
  readonly sequence: string;
}

export interface EvidenceSection {
  readonly heading: BoundedDisplayText;
  readonly body: BoundedDisplayText;
}

export type EvidenceDisplayFamily =
  | ActivityMarkerDisplayItem["sourceKind"]
  | "failure";

export interface EvidenceDisplayItem {
  readonly key: string;
  readonly family: EvidenceDisplayFamily;
  readonly title: BoundedDisplayText;
  readonly sections: readonly EvidenceSection[];
  readonly redacted: boolean;
}

export interface LiveQuestionView {
  readonly key: string;
  readonly header: BoundedDisplayText;
  readonly prompt: BoundedDisplayText;
  readonly options: ReadonlyArray<{
    readonly key: string;
    readonly label: BoundedDisplayText;
    readonly detail: BoundedDisplayText;
  }>;
  readonly multiple: boolean;
  readonly why: BoundedDisplayText | null;
  readonly ifUnanswered: BoundedDisplayText | null;
}

export interface LiveConversationView {
  readonly threadKey: string;
  readonly title: BoundedDisplayText;
  readonly project: BoundedDisplayText;
  readonly status: BoundedDisplayText;
  readonly items: readonly ConversationDisplayItem[];
  readonly evidence: readonly EvidenceDisplayItem[];
  readonly questions: readonly LiveQuestionView[];
  readonly olderAvailable: boolean;
  readonly tone: DisplayTone;
  readonly updatedLabel: BoundedDisplayText | null;
}

export interface LiveWorkItem {
  key: string;
  kind: "task" | "delegate" | "job" | "watch";
  title: string;
  detail: string;
  tone: DisplayTone;
  children: LiveWorkItem[];
}

export interface LiveActivityView {
  tasks: ReadonlyArray<{
    status: "active" | "open" | "done";
    count: number;
  }>;
  work: readonly LiveWorkItem[];
  usage: {
    totalTokens?: number;
    cost?: string;
    contextPressure?: number;
    durationMs?: number;
  };
}
