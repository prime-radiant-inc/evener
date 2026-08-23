import { type JSX, useEffect, useRef, useState } from "react";
import type { StoreApi, UseBoundStore } from "zustand";
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
import type { ConversationService } from "../services/conversation";
import type { AttachmentState } from "../state/attachments";
import type { ConversationState } from "../state/conversation";
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
}: ConversationScreenProps): JSX.Element {
  const conversation = conversationStore((s) => s.conversation);
  const status = conversationStore((s) => s.status);
  const error = conversationStore((s) => s.error);
  const navTitle =
    navigationStore((s) => s.activeConversation?.title) ?? "Conversation";

  const [follow, setFollow] = useState<FollowState>(createFollowState);
  const prevItemCount = useRef(0);

  // Reset follow mode when the conversation changes (session/profile switch).
  const convId = conversation?.id;
  // biome-ignore lint/correctness/useExhaustiveDependencies: convId is the reset trigger; the body intentionally does not reference it.
  useEffect(() => {
    setFollow(() => createFollowState());
    prevItemCount.current = 0;
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

  function handleLoadOlder(): void {
    if (conversationService !== undefined) {
      void conversationStore.getState().loadOlder(conversationService);
    }
  }

  // Loading state.
  if (status === "opening" && conversation === null) {
    return (
      <main className="evener-conversation">
        <TopBar
          title={navTitle}
          status="unknown"
          onBack={() => navigationStore.getState().popConversation()}
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

  return (
    <main className="evener-conversation">
      <TopBar
        title={title}
        status={statusKind}
        onBack={() => navigationStore.getState().popConversation()}
      />
      <Timeline
        items={items}
        following={follow.following}
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
}

function TopBar({ title, status, onBack }: TopBarProps): JSX.Element {
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
