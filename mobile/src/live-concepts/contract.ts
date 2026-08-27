import type { ComponentType } from "react";
import type { StoreApi } from "zustand";
import type { UseBoundStore } from "zustand/react";
import type { NativeBridge } from "../native/client";
import type { ConversationService } from "../services/conversation";
import type { RosterService } from "../services/roster";
import type { ActivityState } from "../state/activity";
import type { ConnectionState } from "../state/connection";
import type { ConversationState } from "../state/conversation";
import type { NavigationState } from "../state/navigation";
import type { PreferencesState } from "../state/preferences";
import type { RosterState } from "../state/roster";
import type {
  Appearance,
  ConceptId,
  LiveActivityView,
  LiveConceptSurface,
  LiveConnectionView,
  LiveConversationView,
  LiveRosterView,
  Platform,
  TextScale,
} from "./model";

export interface ConversationMutationState {
  kind: "send" | "steer" | "queue" | "interrupt";
  status: "pending" | "failed";
  draftSnapshot: string | null;
  generation: number;
}

export interface LiveComposerView {
  draft: string;
  canSend: boolean;
  canSteer: boolean;
  canQueue: boolean;
  canInterrupt: boolean;
  pending: ConversationMutationState | null;
  error: string | null;
}

export interface QuestionDraft {
  selectedOptionKeys: readonly string[];
  note: string;
  resolution: "answer" | "fallback" | "decide" | "skip" | null;
}

export interface ScrollAnchor {
  itemKey: string;
  offset: number;
}

export interface LiveConceptUiState {
  concept: ConceptId;
  workOpen: boolean;
  composerMode: "send" | "steer" | "queue";
  expandedToolKeys: ReadonlySet<string>;
  expandedWorkKeys: ReadonlySet<string>;
  questionDrafts: Readonly<Record<string, QuestionDraft>>;
  focusedItemKey: string | null;
  scrollAnchors: Readonly<Record<string, ScrollAnchor>>;
}

export interface LiveConceptState {
  concept: ConceptId;
  platform: Platform;
  appearance: Appearance;
  textScale: TextScale;
  reducedMotion: boolean;
  surface: LiveConceptSurface;
  connection: LiveConnectionView;
  roster: LiveRosterView;
  conversation: LiveConversationView | null;
  activity: LiveActivityView | null;
  composer: LiveComposerView;
  ui: LiveConceptUiState;
}

export type LiveConceptIntent =
  | { type: "switchConcept"; concept: ConceptId }
  | { type: "openConceptSwitcher" }
  | { type: "refreshRoster" }
  | { type: "setRosterQuery"; value: string }
  | { type: "openConversation"; key: string }
  | { type: "openWork" }
  | { type: "closeWork" }
  | { type: "setDraft"; value: string }
  | { type: "setComposerMode"; mode: "send" | "steer" | "queue" }
  | { type: "submit"; mode: "send" | "steer" | "queue" }
  | { type: "interrupt" }
  | { type: "toggleTool"; key: string }
  | { type: "toggleWork"; key: string }
  | { type: "setQuestionDraft"; key: string; value: QuestionDraft }
  | { type: "submitQuestion"; key: string }
  | { type: "goBack" }
  | { type: "openNew" }
  | { type: "openSettings" }
  | { type: "openVoice" };

export interface LiveConceptRendererProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

export interface LiveConceptModule {
  id: ConceptId;
  label: string;
  Renderer: ComponentType<LiveConceptRendererProps>;
}

type BoundStore<State> = UseBoundStore<StoreApi<State>>;

export interface LiveConceptRuntime {
  connection: BoundStore<ConnectionState>;
  navigation: BoundStore<NavigationState>;
  preferences: BoundStore<PreferencesState>;
  rosterStore: BoundStore<RosterState> | null;
  rosterService: RosterService | null;
  conversationStore: BoundStore<ConversationState>;
  conversationService: ConversationService | null;
  activityStore: BoundStore<ActivityState>;
  native: NativeBridge;
  profileId: string | null;
}

export interface LiveConceptHostProps {
  runtime: LiveConceptRuntime;
  surface: "sessions" | "conversation" | "work";
  onOpenConceptSwitcher(): void;
  onBack(): void;
  onOpenNew(): void;
  onOpenSettings(): void;
  onOpenVoice(): void;
}
