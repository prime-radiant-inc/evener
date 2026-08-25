export type ConceptId = "stillwater" | "constellation" | "field-notes";
export type Platform = "ios" | "android";
export type ScenarioId =
  | "baseline"
  | "loading"
  | "empty"
  | "offline"
  | "error"
  | "needs-attention"
  | "multi-agent"
  | "question"
  | "completed"
  | "voice"
  | "long-content";
export type Appearance = "system" | "light" | "dark";
export type TextScale = "standard" | "large" | "accessibility";
export type RootTab = "sessions" | "search" | "new" | "settings";
export type Route =
  | { kind: "gallery" }
  | { kind: "root"; tab: RootTab }
  | { kind: "conversation"; sessionId: string; focusItemId?: string }
  | { kind: "work"; sessionId: string }
  | { kind: "voice"; sessionId: string };

export type SessionState =
  | "needs-answer"
  | "needs-permission"
  | "running"
  | "waiting"
  | "completed"
  | "failed";

export interface SessionRecord {
  id: string;
  title: string;
  project: string;
  state: SessionState;
  summary: string;
  updatedLabel: string;
}

export type TranscriptItem =
  | { id: string; sessionId: string; kind: "user"; body: string }
  | { id: string; sessionId: string; kind: "assistant"; body: string }
  | {
      id: string;
      sessionId: string;
      kind: "tool";
      label: string;
      status: SessionState;
      arguments: string;
      output: string;
    }
  | { id: string; sessionId: string; kind: "question"; questionId: string }
  | {
      id: string;
      sessionId: string;
      kind: "error";
      title: string;
      detail: string;
    }
  | {
      id: string;
      sessionId: string;
      kind: "attachment";
      name: string;
      mediaType: string;
      description: string;
    };

export interface QuestionOption {
  id: string;
  label: string;
  detail: string;
  recommended: boolean;
}

export interface QuestionFixture {
  id: string;
  prompt: string;
  mode: "single" | "multiple";
  options: readonly QuestionOption[];
  allowNote: boolean;
  allowFallback: boolean;
  allowDecide: boolean;
  allowSkip: boolean;
}

export interface WorkNode {
  id: string;
  sessionId: string;
  parentId: string | null;
  kind: "task" | "subagent" | "job";
  title: string;
  state: SessionState;
  phase: string;
  elapsedLabel: string;
  output: string;
}

export interface SearchDocument {
  id: string;
  sessionId: string;
  itemId: string | null;
  kind: "session" | "project" | "transcript" | "tool" | "task";
  title: string;
  body: string;
}

export interface ModelChoice {
  id: string;
  provider: string;
  model: string;
  label: string;
}
export interface ProjectChoice {
  id: string;
  label: string;
  path: string;
}
export interface VoiceStep {
  id: string;
  state:
    | "idle"
    | "ready"
    | "listening"
    | "processing"
    | "speaking"
    | "interrupted"
    | "denied"
    | "error";
  caption: string;
  level: number;
}
export interface HubPresentationFixture {
  id: string;
  name: string;
  context: string;
  state: "connected" | "offline";
}
export interface UsageFixture {
  tokens: number;
  costLabel: string;
  durationLabel: string;
  contextPercent: number;
}

export interface PrototypeFixture {
  version: 1;
  sessions: readonly SessionRecord[];
  transcript: readonly TranscriptItem[];
  questions: readonly QuestionFixture[];
  work: readonly WorkNode[];
  search: readonly SearchDocument[];
  recentProjects: readonly ProjectChoice[];
  models: readonly ModelChoice[];
  efforts: readonly ("low" | "medium" | "high")[];
  voiceSteps: readonly VoiceStep[];
  hubs: readonly HubPresentationFixture[];
  usage: UsageFixture;
}

export interface FixtureDiagnostic {
  code:
    | "fixture-invalid"
    | "fixture-version"
    | "preference-invalid"
    | "preference-version";
  path: string;
}

export interface DiagnosticSink {
  report(diagnostic: FixtureDiagnostic): void;
}
