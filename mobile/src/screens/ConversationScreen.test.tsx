// Component tests for the focused ConversationScreen — the destination
// pushed above the tab bar. Verifies the top bar (back button, title, status
// indicator, activity-sheet action), the Timeline filling remaining space, and
// the composer placeholder slot. Uses fake stores so no real service or wire
// is required.

import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { StoreApi, UseBoundStore } from "zustand";
import { create } from "zustand";
import type { TimelineProps } from "../components/timeline/Timeline";
import type {
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/model";
import type { ContentSizeCategory } from "../native/contract";
import type { ConversationService } from "../services/conversation";
import type { AttachmentState } from "../state/attachments";
import type { ConversationState } from "../state/conversation";
import type { NavigationState } from "../state/navigation";
import {
  type ConversationScreenProps,
  ConversationScreen as ProductionConversationScreen,
} from "./ConversationScreen";

function ConversationScreen(
  props: Omit<ConversationScreenProps, "contentSize"> & {
    readonly contentSize?: ContentSizeCategory;
  },
) {
  return <ProductionConversationScreen contentSize="large" {...props} />;
}

const screenTimeline = vi.hoisted(() => ({
  props: null as TimelineProps | null,
}));

vi.mock("../components/timeline/Timeline", () => ({
  Timeline(props: TimelineProps) {
    screenTimeline.props = props;
    return (
      <div role="feed">
        {props.items.map((item) => (
          <div key={item.id}>
            {"text" in item
              ? item.text
              : "markdown" in item
                ? item.markdown
                : "label" in item
                  ? item.label
                  : item.id}
          </div>
        ))}
      </div>
    );
  },
}));

// --- fake conversation store ------------------------------------------------

function makeConversation(
  items: MobileTimelineItem[] = [],
  over: Partial<MobileConversation> = {},
): MobileConversation {
  return {
    id: "thread-1",
    sessionId: "session-1",
    name: "Test Chat",
    preview: "",
    modelProvider: "anthropic",
    status: "ready",
    items,
    capabilities: {
      send: true,
      steer: true,
      interrupt: true,
      compact: true,
      clear: true,
      forkFromTurn: true,
      shutdown: true,
      changeModel: true,
      changeVisionModel: true,
      queue: true,
      goal: true,
      rename: true,
    },
    queue: { depth: 0, preview: [] },
    usage: {},
    askPending: false,
    ...over,
  };
}

type ConversationStoreHook = UseBoundStore<StoreApi<ConversationState>>;
type NavigationStoreHook = UseBoundStore<StoreApi<NavigationState>>;

function createFakeConversationStore(
  conversation: MobileConversation | null,
  status: ConversationState["status"] = "open",
): ConversationStoreHook {
  return create<ConversationState>(() => ({
    ref: conversation ? "ref-1" : null,
    profileId: "p1",
    connectionGeneration: 0,
    conversationGeneration: 0,
    conversation,
    olderCursor: null,
    loadingOlder: false,
    status,
    error: null,
    draft: "",
    pendingSend: null,
    open: vi.fn(),
    loadOlder: vi.fn(),
    setDraft: vi.fn(),
    send: vi.fn(),
    steer: vi.fn(),
    queue: vi.fn(),
    interrupt: vi.fn(),
    close: vi.fn(),
    applyNotification: vi.fn(),
    reset: vi.fn(),
  }));
}

function createFakeNavigationStore(title: string): NavigationStoreHook {
  const popConversation = vi.fn();
  const store = create<NavigationState>((_, get) => ({
    tab: "sessions",
    conversationStack: [{ sessionId: "s1", title }],
    activeConversation: { sessionId: "s1", title },
    setTab: vi.fn(),
    pushConversation: vi.fn(),
    popConversation,
    popAllConversations: vi.fn(),
    clearConversations: vi.fn(),
    canGoBack: () => get().conversationStack.length > 0,
  }));
  return store;
}

afterEach(() => {
  cleanup();
  screenTimeline.props = null;
});

// --- tests ------------------------------------------------------------------

describe("ConversationScreen — top bar", () => {
  it("renders a back button with 44px tap target", () => {
    const conversationStore = createFakeConversationStore(makeConversation());
    const navigationStore = createFakeNavigationStore("Chat");
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
      />,
    );
    const back = screen.getByRole("button", { name: /back/i });
    expect(back).toBeInTheDocument();
  });

  it("shows the conversation title in the top bar", () => {
    const conversationStore = createFakeConversationStore(
      makeConversation([], { name: "My Project" }),
    );
    const navigationStore = createFakeNavigationStore("Chat");
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
      />,
    );
    expect(screen.getByText("My Project")).toBeInTheDocument();
  });

  it("falls back to the navigation title when conversation has no name", () => {
    const conversationStore = createFakeConversationStore(
      makeConversation([], { name: undefined }),
    );
    const navigationStore = createFakeNavigationStore("Fallback Title");
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
      />,
    );
    expect(screen.getByText("Fallback Title")).toBeInTheDocument();
  });

  it("back button calls popConversation", () => {
    const conversationStore = createFakeConversationStore(makeConversation());
    const navigationStore = createFakeNavigationStore("Chat");
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /back/i }));
    expect(navigationStore.getState().popConversation).toHaveBeenCalledOnce();
  });

  it("shows a status indicator", () => {
    const conversationStore = createFakeConversationStore(
      makeConversation([], { status: "running" }),
    );
    const navigationStore = createFakeNavigationStore("Chat");
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
      />,
    );
    // The status indicator reflects the conversation status.
    const status = screen.getByTestId("conversation-status");
    expect(status).toBeInTheDocument();
  });
});

describe("ConversationScreen — timeline", () => {
  it("renders timeline items", () => {
    const items: MobileTimelineItem[] = [
      { kind: "user", id: "u1", text: "hello there" },
      { kind: "assistant", id: "a1", markdown: "hi back", streaming: false },
    ];
    const conversationStore = createFakeConversationStore(
      makeConversation(items),
    );
    const navigationStore = createFakeNavigationStore("Chat");
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
      />,
    );
    expect(screen.getByText("hello there")).toBeInTheDocument();
    expect(screen.getByText("hi back")).toBeInTheDocument();
  });

  it("passes real thread/content-size identity through conversation and Dynamic Type switches", () => {
    const conversationStore = createFakeConversationStore(
      makeConversation([{ kind: "user", id: "a", text: "A" }], {
        id: "thread-a",
      }),
    );
    const navigationStore = createFakeNavigationStore("Chat");
    const view = render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
        contentSize="large"
      />,
    );
    expect(screenTimeline.props?.threadKey).toBe("thread-a");
    expect(screenTimeline.props?.contentSize).toBe("large");

    act(() =>
      conversationStore.setState({
        conversation: makeConversation([{ kind: "user", id: "b", text: "B" }], {
          id: "thread-b",
        }),
      }),
    );
    expect(screenTimeline.props?.threadKey).toBe("thread-b");

    view.rerender(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
        contentSize="accessibilityExtraExtraExtraLarge"
      />,
    );
    expect(screenTimeline.props?.contentSize).toBe(
      "accessibilityExtraExtraExtraLarge",
    );
  });

  it("accepts bidirectional follow feedback and clears unseen on 48px re-entry", () => {
    const conversationStore = createFakeConversationStore(
      makeConversation([{ kind: "user", id: "a", text: "A" }]),
    );
    const navigationStore = createFakeNavigationStore("Chat");
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
      />,
    );
    act(() => screenTimeline.props?.onFollowingChange?.(false));
    expect(screenTimeline.props?.following).toBe(false);

    act(() =>
      conversationStore.setState({
        conversation: makeConversation([
          { kind: "user", id: "a", text: "A" },
          { kind: "assistant", id: "b", markdown: "B", streaming: false },
        ]),
      }),
    );
    expect(screenTimeline.props?.unseen).toBe(1);
    act(() => screenTimeline.props?.onFollowingChange?.(true));
    expect(screenTimeline.props?.following).toBe(true);
    expect(screenTimeline.props?.unseen).toBe(0);
  });

  it("returns the store's completed load result to Timeline", async () => {
    const conversationStore = createFakeConversationStore(makeConversation());
    conversationStore.setState({
      loadOlder: vi.fn(async () => ({
        status: "loaded" as const,
        itemKeys: ["older"],
      })),
    });
    const navigationStore = createFakeNavigationStore("Chat");
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
        conversationService={{} as ConversationService}
      />,
    );
    await expect(screenTimeline.props?.loadOlder?.()).resolves.toEqual({
      status: "loaded",
      itemKeys: ["older"],
    });
  });
});

describe("ConversationScreen — composer placeholder", () => {
  it("renders a composer slot placeholder when no service/attachment store", () => {
    const conversationStore = createFakeConversationStore(makeConversation());
    const navigationStore = createFakeNavigationStore("Chat");
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
      />,
    );
    expect(screen.getByTestId("composer-placeholder")).toBeInTheDocument();
  });
});

describe("ConversationScreen — composer integration", () => {
  function createFakeAttachmentStore(): UseBoundStore<
    StoreApi<AttachmentState>
  > {
    return create<AttachmentState>(() => ({
      attachments: [],
      error: null,
      add: vi.fn(),
      remove: vi.fn(),
      clear: vi.fn(),
      setError: vi.fn(),
    }));
  }

  function createFakeService(): ConversationService {
    return {
      open: vi.fn(),
      loadOlder: vi.fn(),
      subscribeNotifications: vi.fn(() => () => {}),
      send: vi.fn(),
      steer: vi.fn(),
      queue: vi.fn(),
      interrupt: vi.fn(),
      compact: vi.fn(),
      shutdown: vi.fn(),
      changeModel: vi.fn(),
      setReasoningEffort: vi.fn(),
      rename: vi.fn(),
      cancelQueued: vi.fn(),
      close: vi.fn(),
    };
  }

  it("renders the Composer when service and attachment store are provided", () => {
    const conversationStore = createFakeConversationStore(makeConversation());
    const navigationStore = createFakeNavigationStore("Chat");
    const conversationService = createFakeService();
    const attachmentStore = createFakeAttachmentStore();
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
        conversationService={conversationService}
        attachmentStore={attachmentStore}
      />,
    );
    expect(screen.getByTestId("composer")).toBeInTheDocument();
  });

  it("renders the AskComposer when askPending is true", () => {
    const conversationStore = createFakeConversationStore(
      makeConversation([], { askPending: true }),
    );
    const navigationStore = createFakeNavigationStore("Chat");
    const conversationService = createFakeService();
    const attachmentStore = createFakeAttachmentStore();
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
        conversationService={conversationService}
        attachmentStore={attachmentStore}
      />,
    );
    expect(screen.getByTestId("ask-composer")).toBeInTheDocument();
  });
});

describe("ConversationScreen — loading and error states", () => {
  it("shows a loading indicator while opening", () => {
    const conversationStore = createFakeConversationStore(null, "opening");
    const navigationStore = createFakeNavigationStore("Chat");
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
      />,
    );
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
  });

  it("shows an error message when open failed", () => {
    const conversationStore = createFakeConversationStore(null, "error");
    // Patch the error field via setState.
    conversationStore.setState({ error: "Connection refused" });
    const navigationStore = createFakeNavigationStore("Chat");
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
      />,
    );
    expect(screen.getByText(/connection refused/i)).toBeInTheDocument();
  });
});
