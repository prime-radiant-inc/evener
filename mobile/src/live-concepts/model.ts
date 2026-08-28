export type ConceptId = "stillwater" | "constellation" | "field-notes";
export type LiveConceptSurface = "sessions" | "conversation" | "work";
export type Platform = "ios" | "android";
export type Appearance = "system" | "light" | "dark";
export type TextScale = "standard" | "accessibility";
export type DisplayTone =
  | "attention"
  | "running"
  | "success"
  | "failed"
  | "idle"
  | "unknown";

export interface LiveConnectionView {
  status: "connecting" | "connected" | "offline" | "error";
  evidence?: {
    serverVersion: string;
    protocolVersion: string;
    appVersion: string;
    bundleId: string;
    originDigest: string;
    profileGeneration: number;
    lifecycleGeneration: number;
    lifecyclePhase: "active" | "inactive" | "background" | "foreground";
    handshakeGeneration: number;
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

export interface LiveTranscriptItem {
  key: string;
  kind: "user" | "assistant" | "tool" | "question" | "failure" | "attachment";
  label: string;
  body: string;
  tone: DisplayTone;
  streaming: boolean;
  truncated: boolean;
  questionKey: string | null;
  sequenceLabel: string;
}

export interface LiveQuestionView {
  key: string;
  header: string;
  prompt: string;
  options: ReadonlyArray<{ key: string; label: string; detail: string }>;
  multiple: boolean;
}

export interface LiveConversationView {
  threadKey: string;
  title: string;
  project: string;
  status: string;
  items: readonly LiveTranscriptItem[];
  questions: readonly LiveQuestionView[];
  olderAvailable: boolean;
  tone: DisplayTone;
  updatedLabel: string | null;
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
