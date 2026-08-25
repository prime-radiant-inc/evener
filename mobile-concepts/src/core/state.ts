import type {
  Appearance,
  ConceptId,
  Platform,
  PrototypeFixture,
  RootTab,
  Route,
  ScenarioId,
  TextScale,
  VoiceStep,
} from "./model";
import type { PersistedPreferencesV1 } from "./persistence";
import type { ScenarioProjection } from "./scenarios";

export interface QuestionAnswerState {
  selectedOptionIds: readonly string[];
  note: string;
  resolution: "answer" | "fallback" | "decide" | "skip" | null;
  submitted: boolean;
}

export interface NewSessionDraft {
  project: string;
  prompt: string;
  modelId: string;
  effort: "low" | "medium" | "high";
  outcome: "editing" | "starting" | "success" | "failure";
  errorCode: string | null;
}

export interface VoicePrototypeState {
  stepIndex: number;
  muted: boolean;
  stopped: boolean;
  ended: boolean;
}

export interface VoicePreferences {
  speakResponses: boolean;
  rate: "slow" | "normal" | "fast";
}

export interface PrototypeState {
  concept: ConceptId | null;
  platform: Platform;
  scenario: ScenarioId;
  appearance: Appearance;
  textScale: TextScale;
  reducedMotion: boolean;
  route: Route;
  history: Route[];
  overlay: "concept-switcher" | "lab-controls" | null;
  refreshState: "idle" | "refreshing" | "complete";
  sessionQuery: string;
  globalQuery: string;
  selectedSessionId: string | null;
  focusedItemId: string | null;
  expandedToolIds: ReadonlySet<string>;
  expandedWorkIds: ReadonlySet<string>;
  draft: string;
  composerMode: "send" | "steer" | "queue" | "running";
  syntheticTurn: "none" | "starting" | "complete" | "stopped";
  answers: Readonly<Record<string, QuestionAnswerState>>;
  newSession: NewSessionDraft;
  voicePreferences: VoicePreferences;
  voice: VoicePrototypeState;
  readonly sourceFixture: PrototypeFixture;
  projection: ScenarioProjection;
  resetGeneration: number;
}

export interface InitialStateOptions {
  platform: Platform;
  preferences: PersistedPreferencesV1;
  sourceFixture?: PrototypeFixture;
  projection?: ScenarioProjection;
}

export type PrototypeAction =
  | { type: "selectConcept"; concept: ConceptId }
  | { type: "setScenario"; scenario: ScenarioId }
  | { type: "setAppearance"; appearance: Appearance }
  | { type: "setTextScale"; textScale: TextScale }
  | { type: "setReducedMotion"; reducedMotion: boolean }
  | { type: "navigateRoot"; tab: RootTab }
  | { type: "openSession"; sessionId: string; focusItemId?: string }
  | { type: "openWork"; sessionId: string }
  | { type: "openVoice"; sessionId: string }
  | {
      type: "openOverlay";
      overlay: "concept-switcher" | "lab-controls";
    }
  | { type: "goBack" }
  | { type: "refreshSessions" }
  | { type: "completeRefresh" }
  | { type: "setSessionQuery"; value: string }
  | { type: "setGlobalQuery"; value: string }
  | { type: "openSearchResult"; resultId: string }
  | { type: "toggleTool"; itemId: string }
  | { type: "toggleWork"; nodeId: string }
  | { type: "setDraft"; value: string }
  | { type: "setComposerMode"; mode: "send" | "steer" | "queue" }
  | { type: "submitComposer" }
  | { type: "completeSyntheticTurn" }
  | { type: "stopSyntheticTurn" }
  | {
      type: "setQuestionOption";
      questionId: string;
      optionId: string;
      selected: boolean;
    }
  | { type: "setQuestionNote"; questionId: string; note: string }
  | {
      type: "resolveQuestion";
      questionId: string;
      resolution: "answer" | "fallback" | "decide" | "skip";
    }
  | { type: "setNewSessionProject"; value: string }
  | { type: "selectRecentProject"; projectId: string }
  | { type: "setNewSessionPrompt"; value: string }
  | { type: "setNewSessionModel"; modelId: string }
  | {
      type: "setNewSessionEffort";
      effort: "low" | "medium" | "high";
    }
  | { type: "submitNewSession" }
  | { type: "completeNewSession"; result: "success" | "failure" }
  | { type: "setSpeakResponses"; enabled: boolean }
  | { type: "setSpeechRate"; rate: "slow" | "normal" | "fast" }
  | { type: "advanceVoice" }
  | { type: "setVoiceState"; state: VoiceStep["state"] }
  | { type: "toggleVoiceMute" }
  | { type: "stopVoice" }
  | { type: "endVoice" }
  | { type: "reset" };
