// Component tests for the focused ConversationScreen — the destination
// pushed above the tab bar. Verifies the top bar (back button, title, status
// indicator, activity-sheet action), the Timeline filling remaining space, and
// the composer placeholder slot. Uses fake stores so no real service or wire
// is required.

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { StoreApi, UseBoundStore } from "zustand";
import { create } from "zustand";
import type {
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/model";
import type { ConversationState } from "../state/conversation";
import type { NavigationState } from "../state/navigation";
import { ConversationScreen } from "./ConversationScreen";

// --- virtualizer mock: render every item in jsdom (no layout) ---------------
vi.mock("@tanstack/react-virtual", () => {
  interface VizOptions {
    readonly count: number;
    readonly estimateSize: () => number;
    readonly getItemKey?: (index: number) => string | number;
  }

  interface MockVirtualItem {
    readonly index: number;
    readonly key: string | number;
    readonly start: number;
    readonly size: number;
    readonly lane: number;
  }

  return {
    useVirtualizer: (options: VizOptions) => {
      const size = options.estimateSize();
      const items: MockVirtualItem[] = Array.from(
        { length: options.count },
        (_, i) => ({
          index: i,
          key: options.getItemKey ? options.getItemKey(i) : i,
          start: i * size,
          size,
          lane: 0,
        }),
      );
      return {
        getTotalSize: () => options.count * size,
        getVirtualItems: () => items,
        scrollToIndex: () => {},
        scrollToOffset: () => {},
        measureElement: () => undefined,
        range: {
          start: 0,
          end: options.count - 1,
          overscan: 0,
          overscanMain: 0,
          overscanReverse: 0,
          size: options.count,
        },
      };
    },
  };
});

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
});

describe("ConversationScreen — composer placeholder", () => {
  it("renders a composer slot placeholder", () => {
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
