import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  MobileCapabilities,
  MobileConversation,
} from "../conversation/model";
import type { NativeBridge } from "../native/client";
import type { ConversationService } from "../services/conversation";
import { createActivityStore } from "../state/activity";
import { createConnectionStore } from "../state/connection";
import { createConversationStore } from "../state/conversation";
import { createNavigationStore } from "../state/navigation";
import { createPreferencesStore } from "../state/preferences";
import { createRosterStore } from "../state/roster";
import { FakeProfileService } from "../test/fakeProfileService";
import {
  LiveConceptHost,
  type LiveConceptHostProps,
  type LiveConceptHostRuntime,
} from "./LiveConceptHost";
import { createLiveConceptUiStore } from "./live-ui-store";

const capabilities: MobileCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  queue: true,
  goal: false,
  rename: false,
};

function conversation(items: MobileConversation["items"]): MobileConversation {
  return {
    id: "thread-private-id",
    sessionId: "session-private-id",
    name: "Projected conversation",
    preview: "preview",
    modelProvider: "provider",
    status: "running",
    items,
    capabilities,
    queue: { depth: 0, preview: [] },
    usage: {},
    askPending: false,
  };
}

function makeHarness(profileId = "profile-a") {
  const profileService = new FakeProfileService({
    profiles: [
      { id: profileId, name: "Profile", origin: "https://hub.example" },
    ],
    activeProfileId: profileId,
    generation: 7,
  });
  const connection = createConnectionStore(profileService);
  connection.setState({
    status: "ready",
    activeProfileId: profileId,
    generation: 7,
    reachability: { [profileId]: "reachable" },
  });
  const navigation = createNavigationStore();
  const preferences = createPreferencesStore();
  const rosterStore = createRosterStore();
  rosterStore.setState({
    entries: [
      {
        ref: "private-conversation-ref",
        title: "Roster projector sentinel",
        project: "Private project label",
        status: "active",
        updatedAt: 1_700_000_000_000,
        attention: "needsYou",
      },
    ],
    sessionsVisible: true,
  });
  const conversationStore = createConversationStore();
  const activityStore = createActivityStore();
  const writes: string[] = [];
  const uiStore = createLiveConceptUiStore({
    read: () => null,
    write: (value) => writes.push(value),
    remove: vi.fn(),
  });
  const runtime: LiveConceptHostRuntime = {
    connection,
    navigation,
    preferences,
    rosterStore,
    rosterService: null,
    conversationStore,
    conversationService: null,
    activityStore,
    native: {} as NativeBridge,
    profileId,
  };
  const callbacks = {
    onOpenConceptSwitcher: vi.fn(),
    onOpenConversation: vi.fn(),
    onBack: vi.fn(),
    onOpenNew: vi.fn(),
    onOpenSettings: vi.fn(),
    onOpenVoice: vi.fn(),
  };
  const props: LiveConceptHostProps = {
    runtime,
    uiStore,
    platform: "ios",
    surface: "sessions",
    ...callbacks,
  };
  return {
    props,
    runtime,
    uiStore,
    writes,
    callbacks,
    profileService,
  };
}

async function openConversation(
  harness: ReturnType<typeof makeHarness>,
  value: MobileConversation,
): Promise<void> {
  const service = {
    open: vi.fn(async () => value),
    subscribeNotifications: vi.fn(() => () => undefined),
  } as unknown as ConversationService;
  await act(async () => {
    await harness.runtime.conversationStore
      .getState()
      .open(service, "private-conversation-ref");
  });
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("LiveConceptHost ownership", () => {
  it("renders exactly one selected renderer and one main surface", () => {
    const harness = makeHarness();
    const { container } = render(<LiveConceptHost {...harness.props} />);

    expect(container.querySelectorAll("[data-concept-root]")).toHaveLength(1);
    expect(container.querySelectorAll("main")).toHaveLength(1);
    expect(container.querySelector(".concept-stillwater")).not.toBeNull();

    act(() => harness.uiStore.getState().setConcept("constellation"));
    expect(container.querySelectorAll("[data-concept-root]")).toHaveLength(1);
    expect(container.querySelectorAll("main")).toHaveLength(1);
    expect(container.querySelector(".concept-constellation")).not.toBeNull();
  });

  it("owns no browser history listener", () => {
    const addEventListener = vi.spyOn(window, "addEventListener");
    const harness = makeHarness();
    render(<LiveConceptHost {...harness.props} />);

    const eventTypes = addEventListener.mock.calls.map(([type]) => type);
    expect(eventTypes).not.toContain("popstate");
    expect(eventTypes).not.toContain("hashchange");
  });

  it("routes switcher, private roster lookup, and Back to RootShell", () => {
    const harness = makeHarness();
    const { rerender } = render(<LiveConceptHost {...harness.props} />);

    fireEvent.click(screen.getByRole("button", { name: /Switch concept/i }));
    expect(harness.callbacks.onOpenConceptSwitcher).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByText("Roster projector sentinel"));
    expect(harness.callbacks.onOpenConversation).toHaveBeenCalledWith(
      "private-conversation-ref",
    );
    expect(document.body.textContent).not.toContain("private-conversation-ref");

    rerender(<LiveConceptHost {...harness.props} surface="conversation" />);
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(harness.callbacks.onBack).toHaveBeenCalledTimes(1);
  });

  it("switches presentation without reconnecting or changing live/local state", async () => {
    const harness = makeHarness();
    await openConversation(
      harness,
      conversation([
        { kind: "user", id: "item-1", text: "Conversation projector sentinel" },
      ]),
    );
    const pendingMutation = {
      kind: "queue" as const,
      status: "pending" as const,
      draftSnapshot: "exact-draft-sentinel",
      draftRevisionAtSubmit: 17,
      generation:
        harness.runtime.conversationStore.getState().conversationGeneration,
      mutationId: 23,
    };
    harness.runtime.conversationStore
      .getState()
      .setDraft("exact-draft-sentinel");
    harness.runtime.conversationStore.setState({ pendingMutation });
    const questionDraft = {
      selectedOptionKeys: ["opaque-option"],
      note: "question draft sentinel",
      resolution: null,
    } as const;
    act(() => {
      harness.uiStore.getState().toggleTool("opaque-tool");
      harness.uiStore.getState().toggleWork("opaque-work");
      harness.uiStore
        .getState()
        .setQuestionDraft("opaque-question", questionDraft);
      harness.uiStore.getState().setScrollAnchor("anchor-scope", {
        itemKey: "opaque-item",
        offset: 19,
      });
    });
    const conversationBefore = harness.runtime.conversationStore.getState();
    const uiBefore = harness.uiStore.getState();
    const connectionBefore = harness.runtime.connection.getState();
    const health = vi.spyOn(harness.profileService, "health");
    const select = vi.spyOn(harness.profileService, "select");

    render(<LiveConceptHost {...harness.props} surface="conversation" />);
    act(() => harness.uiStore.getState().setConcept("field-notes"));

    const conversationAfter = harness.runtime.conversationStore.getState();
    const uiAfter = harness.uiStore.getState();
    expect(conversationAfter.ref).toBe(conversationBefore.ref);
    expect(conversationAfter.draft).toBe("exact-draft-sentinel");
    expect(conversationAfter.pendingMutation).toBe(pendingMutation);
    expect(uiAfter.expandedToolKeys).toEqual(uiBefore.expandedToolKeys);
    expect(uiAfter.expandedWorkKeys).toEqual(uiBefore.expandedWorkKeys);
    expect(uiAfter.questionDrafts).toEqual(uiBefore.questionDrafts);
    expect(uiAfter.scrollAnchors).toEqual(uiBefore.scrollAnchors);
    expect(harness.runtime.connection.getState()).toBe(connectionBefore);
    expect(harness.runtime.connection.getState().generation).toBe(7);
    expect(health).not.toHaveBeenCalled();
    expect(select).not.toHaveBeenCalled();
    expect(harness.writes).toEqual(["field-notes"]);
  });

  it("projects roster, conversation with truncation ownership, and strict activity", async () => {
    const harness = makeHarness();
    const oversized = "x".repeat(70_000);
    await openConversation(
      harness,
      conversation([
        {
          kind: "assistant",
          id: "truncated-source-item",
          markdown: oversized,
          streaming: false,
        },
      ]),
    );
    harness.runtime.conversationStore.setState({
      olderCursor: "private-older-cursor",
    });
    const generation =
      harness.runtime.conversationStore.getState().conversationGeneration;
    harness.runtime.activityStore.getState().setLiveView(
      {
        tasks: [{ status: "active", count: 3 }],
        work: [
          {
            kind: "job",
            label: "Activity projector sentinel",
            tone: "running",
            diagnostics: {
              rawId: "private-job-id",
              operationName: "shell",
              statusClass: "running",
            },
          },
        ],
        usage: { totalTokens: 321 },
        capabilities,
      },
      {
        threadId: "thread-private-id",
        ref: "private-conversation-ref",
        generation,
      },
    );

    const sessions = render(<LiveConceptHost {...harness.props} />);
    expect(screen.getByText("Roster projector sentinel")).toBeInTheDocument();
    act(() => harness.runtime.rosterStore?.getState().setSearch("no-match"));
    sessions.rerender(
      <LiveConceptHost {...harness.props} surface="conversation" />,
    );
    expect(screen.getAllByText("Private project label").length).toBeGreaterThan(
      0,
    );
    expect(
      screen.getByRole("button", { name: "Load older messages" }),
    ).toBeEnabled();
    expect(
      sessions.container.querySelector(".sw-updated-label")?.textContent,
    ).not.toBe("");
    expect(document.querySelector('[data-truncated="true"]')).not.toBeNull();
    sessions.rerender(<LiveConceptHost {...harness.props} surface="work" />);
    expect(screen.getByText("Activity projector sentinel")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("private-job-id");
  });

  it("maps injected platform, appearance, text scale, and independent capabilities", async () => {
    const harness = makeHarness();
    await openConversation(harness, conversation([]));
    const currentConversation =
      harness.runtime.conversationStore.getState().conversation;
    if (currentConversation === null) {
      throw new Error("conversation setup did not open");
    }
    act(() => {
      harness.runtime.preferences.getState().setTheme("dark");
      harness.runtime.preferences
        .getState()
        .setContentSize("accessibilityLarge");
      harness.runtime.conversationStore.setState({
        conversation: {
          ...currentConversation,
          capabilities: {
            ...capabilities,
            send: false,
            steer: true,
            queue: false,
            interrupt: true,
          },
        },
        pendingMutation: {
          kind: "steer",
          status: "pending",
          draftSnapshot: "capability-sentinel",
          draftRevisionAtSubmit: 1,
          generation:
            harness.runtime.conversationStore.getState().conversationGeneration,
          mutationId: 1,
        },
      });
    });

    const { container } = render(
      <LiveConceptHost
        {...harness.props}
        platform="android"
        surface="conversation"
      />,
    );
    const root = container.querySelector("[data-concept-root]");
    expect(root).toHaveAttribute("data-platform", "android");
    expect(root).toHaveAttribute("data-appearance", "dark");
    expect(root).toHaveAttribute("data-text-scale", "accessibility");
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Steer" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Queue" })).toBeDisabled();
    expect(screen.getByRole("button", { name: /Interrupt/i })).toBeEnabled();
  });

  it("clears only thread-keyed local UI after a profile ID change", () => {
    const harness = makeHarness();
    act(() => {
      harness.uiStore.getState().setConcept("constellation");
      harness.uiStore.getState().setWorkOpen(true);
      harness.uiStore.getState().setComposerMode("queue");
      harness.uiStore.getState().toggleTool("tool-key");
      harness.uiStore.getState().toggleWork("work-key");
      harness.uiStore.getState().setQuestionDraft("question-key", {
        selectedOptionKeys: ["option-key"],
        note: "note",
        resolution: null,
      });
      harness.uiStore.getState().setFocusedItemKey("item-key");
      harness.uiStore.getState().setScrollAnchor("scope", {
        itemKey: "item-key",
        offset: 8,
      });
    });
    const { rerender } = render(<LiveConceptHost {...harness.props} />);
    expect(harness.uiStore.getState().expandedToolKeys).toContain("tool-key");

    rerender(
      <LiveConceptHost
        {...harness.props}
        runtime={{ ...harness.runtime, profileId: "profile-b" }}
      />,
    );

    const state = harness.uiStore.getState();
    expect(state.concept).toBe("constellation");
    expect(state.workOpen).toBe(false);
    expect(state.composerMode).toBe("send");
    expect(state.expandedToolKeys.size).toBe(0);
    expect(state.expandedWorkKeys.size).toBe(0);
    expect(state.questionDrafts).toEqual({});
    expect(state.focusedItemKey).toBeNull();
    expect(state.scrollAnchors).toEqual({});
  });

  it("does not erase valid local state on initial mount or render prototype content", () => {
    const harness = makeHarness();
    act(() => {
      harness.uiStore.getState().toggleTool("preserved-on-mount");
      harness.uiStore.getState().setFocusedItemKey("focused-on-mount");
    });
    render(<LiveConceptHost {...harness.props} />);

    expect(harness.uiStore.getState().expandedToolKeys).toContain(
      "preserved-on-mount",
    );
    expect(harness.uiStore.getState().focusedItemKey).toBe("focused-on-mount");
    expect(screen.queryByText(/^Search$/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Lab Controls/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/synthetic/i)).not.toBeInTheDocument();
  });
});
