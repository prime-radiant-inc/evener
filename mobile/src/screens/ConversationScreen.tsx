import { type JSX, useEffect, useRef, useState } from "react";
import type { StoreApi, UseBoundStore } from "zustand";
import { ActivitySheet } from "../components/activity/ActivitySheet";
import { AskComposer } from "../components/composer/AskComposer";
import { Composer } from "../components/composer/Composer";
import { Timeline } from "../components/timeline/Timeline";
import {
  createFollowState,
  type FollowState,
  onNewItems,
  onTapNewActivity,
} from "../conversation/follow";
import type { MobileTimelineItem } from "../conversation/model";
import type { ContentSizeCategory } from "../native/contract";
import type { ActivityView } from "../services/activity";
import type { ConversationService } from "../services/conversation";
import type { AttachmentState } from "../state/attachments";
import type { ConversationState, LoadOlderResult } from "../state/conversation";
import type { NavigationState } from "../state/navigation";

export interface ConversationScreenProps {
  /** The conversation store hook (Zustand). */
  readonly conversationStore: UseBoundStore<StoreApi<ConversationState>>;
  /** The navigation store hook (Zustand) — provides the back action and title fallback. */
  readonly navigationStore: UseBoundStore<StoreApi<NavigationState>>;
  /** The conversation service (for loadOlder, send/steer/queue/interrupt). */
  readonly conversationService?: ConversationService;
  /** The attachment store hook (Zustand) — pending image attachments. */
  readonly attachmentStore?: UseBoundStore<StoreApi<AttachmentState>>;
  /** The activity view to display in the sheet. Optional; when absent the sheet builds from the conversation. */
  readonly activityView?: ActivityView | null;
  /** Called when the user taps the voice button in the composer. */
  readonly onShowVoice?: () => void;
  /** Exact native Dynamic Type category owned by the platform preference store. */
  readonly contentSize: ContentSizeCategory;
}

/**
 * The focused conversation destination pushed above the tab bar. Layout:
 * - Top bar: back button (44px), title, status indicator, activity-sheet action.
 * - Timeline fills the remaining space (one scroller).
 * - Composer dock at the bottom — AskComposer replaces the normal Composer
 *   while askPending is true so structured ask_user questions take over.
 *
 * The Timeline is the sole scroller; the top bar and composer are fixed flex
 * children. Follow mode and the new-activity pill are driven by the local
 * FollowState, reset on conversation change.
 */
export function ConversationScreen({
  conversationStore,
  navigationStore,
  conversationService,
  attachmentStore,
  activityView: activityViewProp,
  onShowVoice,
  contentSize,
}: ConversationScreenProps): JSX.Element {
  const conversation = conversationStore((s) => s.conversation);
  const status = conversationStore((s) => s.status);
  const error = conversationStore((s) => s.error);
  const navTitle =
    navigationStore((s) => s.activeConversation?.title) ?? "Conversation";
  const navigationConversationId =
    navigationStore((s) => s.activeConversation?.sessionId) ??
    "no-conversation";

  const [follow, setFollow] = useState<FollowState>(createFollowState);
  const [activityOpen, setActivityOpen] = useState(false);
  const prevItemCount = useRef(0);

  // Reset follow mode when the conversation changes (session/profile switch).
  const convId = conversation?.id;
  // biome-ignore lint/correctness/useExhaustiveDependencies: convId is the reset trigger; the body intentionally does not reference it.
  useEffect(() => {
    setFollow(() => createFollowState());
    prevItemCount.current = 0;
    setActivityOpen(false);
  }, [convId]);

  // Track new items for the unseen count.
  const itemCount = conversation?.items.length ?? 0;
  if (conversation !== null && itemCount > prevItemCount.current) {
    const newCount = itemCount - prevItemCount.current;
    if (prevItemCount.current > 0) {
      setFollow((f) => onNewItems(f, newCount));
    }
    prevItemCount.current = itemCount;
  } else if (conversation !== null) {
    prevItemCount.current = itemCount;
  }

  function handleTapNewActivity(): void {
    setFollow((f) => onTapNewActivity(f));
  }

  async function handleLoadOlder(): Promise<LoadOlderResult> {
    if (conversationService === undefined) return { status: "ignored" };
    return conversationStore.getState().loadOlder(conversationService);
  }

  function handleFollowingChange(following: boolean): void {
    setFollow((state) =>
      following
        ? { following: true, unseen: 0 }
        : state.following
          ? { following: false, unseen: state.unseen }
          : state,
    );
  }

  // Loading state.
  if (status === "opening" && conversation === null) {
    return (
      <main className="evener-conversation">
        <TopBar
          title={navTitle}
          status="unknown"
          onBack={() => navigationStore.getState().popConversation()}
          onActivity={() => setActivityOpen(true)}
        />
        <div className="evener-conversation__loading">Loading…</div>
      </main>
    );
  }

  // Error state (open failed).
  if (status === "error" && conversation === null) {
    return (
      <main className="evener-conversation">
        <TopBar
          title={navTitle}
          status="attention"
          onBack={() => navigationStore.getState().popConversation()}
          onActivity={() => setActivityOpen(true)}
        />
        <div className="evener-conversation__error" role="alert">
          {error ?? "Failed to open conversation"}
        </div>
      </main>
    );
  }

  // When conversation is null but not loading/error, render an empty timeline
  // so the one-scroller structural invariant always holds. The top bar still
  // shows the navigation title fallback.
  const items: MobileTimelineItem[] = conversation?.items ?? [];
  const title = conversation?.name ?? navTitle;
  const statusKind = conversation ? mapStatus(conversation.status) : "unknown";

  // Build the activity view from the conversation projection. Tasks and work
  // data come from the raw Thread via the activity service (when available);
  // capabilities, usage, and reasoning come from the MobileConversation.
  const activityView: ActivityView | null =
    activityViewProp !== undefined
      ? activityViewProp
      : conversation !== null
        ? activityViewFromConversation(conversation)
        : null;

  return (
    <main className="evener-conversation">
      <TopBar
        title={title}
        status={statusKind}
        onBack={() => navigationStore.getState().popConversation()}
        onActivity={() => setActivityOpen(true)}
      />
      <Timeline
        threadKey={conversation?.id ?? navigationConversationId}
        contentSize={contentSize}
        items={items}
        following={follow.following}
        onFollowingChange={handleFollowingChange}
        unseen={follow.unseen}
        onTapNewActivity={handleTapNewActivity}
        loadOlder={handleLoadOlder}
      />
      {conversationService !== undefined && attachmentStore !== undefined ? (
        conversation?.askPending ? (
          <AskComposer
            conversationStore={conversationStore}
            conversationService={conversationService}
            attachmentStore={attachmentStore}
          />
        ) : (
          <Composer
            conversationStore={conversationStore}
            conversationService={conversationService}
            attachmentStore={attachmentStore}
            onVoice={onShowVoice}
          />
        )
      ) : (
        <div
          className="evener-conversation__composer"
          data-testid="composer-placeholder"
        >
          <div
            className="evener-conversation__composer-slot"
            aria-hidden="true"
          />
        </div>
      )}
      {conversationService !== undefined ? (
        <ActivitySheet
          open={activityOpen}
          onClose={() => setActivityOpen(false)}
          view={activityView}
          conversationService={conversationService}
        />
      ) : null}
    </main>
  );
}

type StatusKind =
  | "reachable"
  | "reconnecting"
  | "offline"
  | "unknown"
  | "attention";

function mapStatus(status: string): StatusKind {
  switch (status) {
    case "ready":
      return "reachable";
    case "running":
      return "reachable";
    case "reconnecting":
      return "reconnecting";
    case "offline":
      return "offline";
    case "error":
      return "attention";
    default:
      return "unknown";
  }
}

interface TopBarProps {
  readonly title: string;
  readonly status: StatusKind;
  readonly onBack: () => void;
  readonly onActivity: () => void;
}

function TopBar({
  title,
  status,
  onBack,
  onActivity,
}: TopBarProps): JSX.Element {
  return (
    <header className="evener-conversation__topbar">
      <button
        type="button"
        className="evener-icon-button evener-conversation__back"
        aria-label="Back"
        onClick={onBack}
      >
        ‹
      </button>
      <span className="evener-conversation__title">{title}</span>
      <span
        className="evener-conversation__status"
        data-status={status}
        data-testid="conversation-status"
        role="img"
        aria-label={statusLabel(status)}
      >
        <span aria-hidden="true">{statusGlyph(status)}</span>
      </span>
      <button
        type="button"
        className="evener-icon-button evener-conversation__activity"
        aria-label="Activity"
        data-testid="activity-button"
        onClick={onActivity}
      >
        ☰
      </button>
    </header>
  );
}

function statusGlyph(status: StatusKind): string {
  switch (status) {
    case "reachable":
      return "✓";
    case "reconnecting":
      return "↻";
    case "offline":
      return "✕";
    case "attention":
      return "!";
    default:
      return "?";
  }
}

function statusLabel(status: StatusKind): string {
  switch (status) {
    case "reachable":
      return "Connected";
    case "reconnecting":
      return "Reconnecting";
    case "offline":
      return "Offline";
    case "attention":
      return "Needs attention";
    default:
      return "Unknown";
  }
}

// Build an ActivityView from the MobileConversation projection. Tasks and work
// are not carried in the MobileConversation (they come from the raw Thread's
// diagnostics), so they are empty here. Capabilities, usage, and reasoning are
// projected 1:1. When the full Thread is available, use createActivityService()
// .projectActivity(thread) instead for complete task/work data.
function activityViewFromConversation(
  conv: NonNullable<ConversationState["conversation"]>,
): ActivityView {
  return {
    tasks: [],
    work: [],
    usage: {
      inputTokens: conv.usage.inputTokens,
      outputTokens: conv.usage.outputTokens,
      cacheReadTokens: conv.usage.cacheReadTokens,
      totalTokens: conv.usage.totalTokens,
      cost: conv.usage.cost,
      contextUsed: conv.usage.contextUsed,
      contextWindow: conv.usage.contextWindow,
      contextRemaining: conv.usage.contextRemaining,
      contextPressure: conv.usage.contextPressure,
    },
    capabilities: conv.capabilities,
    reasoningEffort: conv.reasoningEffort,
    reasoningEffortLevels: conv.reasoningEffortLevels,
    supportsReasoning: conv.supportsReasoning,
  };
}
