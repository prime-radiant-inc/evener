import type { ReactNode } from "react";
import type { ContentSizeCategory } from "../../native/contract";
import type {
  ActivityMarkerDisplayItem,
  Appearance,
  BoundedDisplayText,
  ConceptId,
  LiveConnectionView,
  LiveConversationView,
  NarrativeDisplayItem,
  Platform,
} from "../model";
import type {
  ComposerMode,
  ConversationFrameAction,
  LiveComposerView,
  QuestionDraft,
} from "./primitives";

export interface ComposerAppearance {
  readonly density: "compact" | "comfortable";
  readonly accent: "forest" | "luminous" | "rust";
}

export interface NarrativeItemRenderProps {
  readonly item: NarrativeDisplayItem;
  readonly body: ReactNode;
  readonly focused: boolean;
}

export interface ActivityMarkerRenderProps {
  readonly item: ActivityMarkerDisplayItem;
  readonly focused: boolean;
}

export interface ChromeRenderProps {
  readonly title: BoundedDisplayText;
  readonly project: BoundedDisplayText;
  readonly status: BoundedDisplayText;
  readonly updatedLabel: BoundedDisplayText | null;
}

export interface ConversationSkin {
  readonly id: ConceptId;
  readonly className: string;
  readonly composerAppearance: ComposerAppearance;
  renderNarrativeItem(props: NarrativeItemRenderProps): ReactNode;
  renderActivityMarker(props: ActivityMarkerRenderProps): ReactNode;
  renderConversationChrome(props: ChromeRenderProps): ReactNode;
}

export interface ConversationAnchor {
  readonly threadKey: string;
  readonly itemKey: string;
  readonly offsetPx: number;
  readonly following: boolean;
}

export interface ConversationFrameState {
  readonly concept: ConceptId;
  readonly platform: Platform;
  readonly appearance: Appearance;
  readonly contentSize: ContentSizeCategory;
  readonly reducedMotion: boolean;
  readonly phase: "loading" | "empty" | "ready" | "read-error";
  readonly connection: LiveConnectionView;
  readonly conversation: LiveConversationView | null;
  readonly composer: LiveComposerView;
  readonly composerMode: ComposerMode;
  readonly questionDrafts: Readonly<Record<string, QuestionDraft>>;
  readonly anchor: ConversationAnchor | null;
  readonly focusedItemKey: string | null;
  readonly unseen: number;
  readonly openEvidenceKey: string | null;
  readonly evidenceTriggerKey: string | null;
}

export interface ConversationFrameProps {
  readonly state: ConversationFrameState;
  readonly skin: ConversationSkin;
  readonly dispatch: (action: ConversationFrameAction) => void;
  readonly onAnchorChange: (anchor: ConversationAnchor) => void;
  readonly onUnseenChange: (count: number) => void;
  readonly onFocusIntentChange: (key: string | null) => void;
}
