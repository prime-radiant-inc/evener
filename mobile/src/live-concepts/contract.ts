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
import type {
  ConversationAnchor,
  ConversationSkin,
} from "./conversation/contract";
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

export interface ConversationUiMemory {
  readonly anchor: ConversationAnchor | null;
  readonly unseen: number;
  readonly evidenceKey: string | null;
  readonly evidenceTriggerKey: string | null;
  readonly focusedItemKey: string | null;
  readonly expandedEvidenceKeys: ReadonlySet<string>;
}

export interface LiveConceptUiState {
  readonly concept: ConceptId;
  readonly workOpen: boolean;
  readonly composerMode: "send" | "steer" | "queue";
  readonly expandedToolKeys: ReadonlySet<string>;
  readonly expandedWorkKeys: ReadonlySet<string>;
  readonly questionDrafts: Readonly<Record<string, QuestionDraft>>;
  /** Sessions/Work renderer compatibility fields; conversation state never uses them. */
  readonly focusedItemKey: null;
  readonly scrollAnchors: Readonly<Record<string, never>>;
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

export type RootOwnedIntent =
  | { readonly type: "switchConcept"; readonly concept: ConceptId }
  | { readonly type: "closeWork" }
  | { readonly type: "openNew" }
  | { readonly type: "openSettings" }
  | { readonly type: "openVoice" };

export type RosterIntent =
  | { readonly type: "refreshRoster" }
  | { readonly type: "setRosterQuery"; readonly value: string }
  | { readonly type: "openConversation"; readonly key: string };

export type WorkIntent = { readonly type: "toggleWork"; readonly key: string };

export type LiveConceptIntent =
  | ConversationFrameAction
  | RootOwnedIntent
  | RosterIntent
  | WorkIntent;

export interface LiveConceptRendererProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

export interface LiveConceptModule {
  readonly id: ConceptId;
  readonly label: string;
  readonly Renderer: ComponentType<LiveConceptRendererProps>;
  readonly conversationSkin: ConversationSkin;
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
