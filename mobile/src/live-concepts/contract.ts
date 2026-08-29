import type { ComponentType } from "react";
import type { StoreApi } from "zustand";
import type { UseBoundStore } from "zustand/react";
import type { NativeBridge } from "../native/client";
import type { ConversationService } from "../services/conversation";
import type { RosterService } from "../services/roster";
import type { LiveActivityState } from "../state/activity";
import type { ConnectionState } from "../state/connection";
import type { ConversationState } from "../state/conversation";
import type { NavigationState } from "../state/navigation";
import type { PreferencesState } from "../state/preferences";
import type { RosterState } from "../state/roster";
import type { ConversationSkin } from "./conversation/contract";
import type {
  ConversationFrameAction,
  LiveComposerView as ConversationLiveComposerView,
  ConversationMutationView,
  QuestionDraft,
} from "./conversation/primitives";
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

export type ConversationMutationState = ConversationMutationView;
export type * from "./conversation/contract";
export type * from "./conversation/primitives";
export type { ConversationLiveComposerView as LiveComposerView, QuestionDraft };

export interface ScrollAnchor {
  scrollTop: number;
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
  composer: Omit<ConversationLiveComposerView, "accepted"> & {
    readonly accepted?: ConversationLiveComposerView["accepted"];
  };
  ui: LiveConceptUiState;
}

type MutableAction<Action> = Action extends object
  ? { -readonly [Key in keyof Action]: Action[Key] }
  : Action;

export type LiveConceptIntent =
  | MutableAction<ConversationFrameAction>
  | { type: "switchConcept"; concept: ConceptId }
  | { type: "refreshRoster" }
  | { type: "setRosterQuery"; value: string }
  | { type: "openConversation"; key: string }
  | { type: "closeWork" }
  | { type: "toggleTool"; key: string }
  | { type: "toggleWork"; key: string }
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
  conversationSkin?: ConversationSkin;
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
  activityStore: BoundStore<LiveActivityState>;
  native: NativeBridge;
  profileId: string | null;
  connectionEvidence?: LiveConnectionView["evidence"];
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
