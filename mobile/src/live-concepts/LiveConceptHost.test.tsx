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
import {
  createConversationStore,
  MAX_ITEM_BYTES,
  truncateText,
} from "../state/conversation";
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
    profileScopeEpoch: 1,
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
  ref = "private-conversation-ref",
): Promise<void> {
  const service = {
    open: vi.fn(async () => value),
    subscribeNotifications: vi.fn(() => () => undefined),
  } as unknown as ConversationService;
  await act(async () => {
    await harness.runtime.conversationStore.getState().open(service, ref);
  });
}

interface ProfileSourceSeed {
  ref: string;
  rosterTitle: string;
  project: string;
  threadId: string;
  sessionId: string;
  itemId: string;
  transcript: string;
  jobId: string;
  activity: string;
}

async function resetAllProfileSources(
  harness: ReturnType<typeof makeHarness>,
  seed: ProfileSourceSeed,
): Promise<void> {
  act(() => {
    harness.runtime.rosterStore?.getState().reset();
    harness.runtime.rosterStore?.setState({
      entries: [
        {
          ref: seed.ref,
          title: seed.rosterTitle,
          project: seed.project,
          status: "active",
          updatedAt: 1_800_000_000_000,
          attention: "recent",
        },
      ],
      sessionsVisible: true,
    });
    harness.runtime.conversationStore.getState().reset();
    harness.runtime.activityStore.getState().reset();
  });
  await openConversation(
    harness,
    {
      ...conversation([
        {
          kind: "user",
          id: seed.itemId,
          text: seed.transcript,
        },
      ]),
      id: seed.threadId,
      sessionId: seed.sessionId,
      name: `${seed.project} conversation`,
    },
    seed.ref,
  );
  const generation =
    harness.runtime.conversationStore.getState().conversationGeneration;
  act(() => {
    harness.runtime.activityStore.getState().setLiveView(
      {
        tasks: [{ status: "done", count: 1 }],
        work: [
          {
            kind: "job",
            label: seed.activity,
            tone: "terminal",
            diagnostics: {
              rawId: seed.jobId,
              operationName: "shell",
              statusClass: "completed",
            },
          },
        ],
        usage: {},
        capabilities,
      },
      {
        threadId: seed.threadId,
        ref: seed.ref,
        generation,
      },
    );
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
    const expectedFrozenBody = truncateText(oversized, MAX_ITEM_BYTES);
    const genuineLiteralMarker = "genuine short content … truncated";
    expect(new TextEncoder().encode(expectedFrozenBody)).toHaveLength(
      MAX_ITEM_BYTES,
    );
    await openConversation(
      harness,
      conversation([
        {
          kind: "assistant",
          id: "truncated-source-item",
          markdown: oversized,
          streaming: false,
        },
        {
          kind: "assistant",
          id: "literal-marker-item",
          markdown: genuineLiteralMarker,
          streaming: false,
        },
      ]),
    );
    expect(
      harness.runtime.conversationStore
        .getState()
        .getTruncatedItemIds()
        .has("truncated-source-item"),
    ).toBe(true);
    expect(
      harness.runtime.conversationStore
        .getState()
        .getTruncatedItemIds()
        .has("literal-marker-item"),
    ).toBe(false);
    expect(
      harness.runtime.conversationStore
        .getState()
        .conversation?.items.find(
          (item) => item.id === "truncated-source-item",
        ),
    ).toMatchObject({
      kind: "assistant",
      markdown: expectedFrozenBody,
    });
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
    const serializedSurfaces = [document.body.innerHTML];
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
    const assistantRows = sessions.container.querySelectorAll(
      ".sw-transcript-item--assistant",
    );
    expect(assistantRows).toHaveLength(2);
    expect(assistantRows[0]).toHaveAttribute("data-truncated", "true");
    expect(assistantRows[0]?.textContent).toBe(expectedFrozenBody);
    expect(assistantRows[1]).toHaveAttribute("data-truncated", "false");
    expect(assistantRows[1]?.textContent).toBe(genuineLiteralMarker);
    serializedSurfaces.push(document.body.innerHTML);
    sessions.rerender(<LiveConceptHost {...harness.props} surface="work" />);
    expect(screen.getByText("Activity projector sentinel")).toBeInTheDocument();
    serializedSurfaces.push(document.body.innerHTML);
    const serialized = serializedSurfaces.join("\n");
    for (const rawIdentifier of [
      "private-conversation-ref",
      "thread-private-id",
      "session-private-id",
      "truncated-source-item",
      "literal-marker-item",
      "private-older-cursor",
      "private-job-id",
    ]) {
      expect(serialized).not.toContain(rawIdentifier);
    }
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

  it("blocks stale profile data until a higher scope epoch accepts reset sources", async () => {
    const harness = makeHarness();
    await openConversation(
      harness,
      conversation([
        {
          kind: "user",
          id: "old-profile-item-id",
          text: "Old profile transcript sentinel",
        },
      ]),
    );
    const oldConversationGeneration =
      harness.runtime.conversationStore.getState().conversationGeneration;
    harness.runtime.activityStore.getState().setLiveView(
      {
        tasks: [{ status: "active", count: 1 }],
        work: [
          {
            kind: "job",
            label: "Old profile activity sentinel",
            tone: "running",
            diagnostics: {
              rawId: "old-profile-job-id",
              operationName: "shell",
              statusClass: "running",
            },
          },
        ],
        usage: {},
        capabilities,
      },
      {
        threadId: "thread-private-id",
        ref: "private-conversation-ref",
        generation: oldConversationGeneration,
      },
    );
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
    const resetProfileScope = vi.spyOn(
      harness.uiStore.getState(),
      "resetProfileScope",
    );
    const { container, rerender } = render(
      <LiveConceptHost {...harness.props} />,
    );
    expect(resetProfileScope).not.toHaveBeenCalled();
    expect(harness.uiStore.getState().expandedToolKeys).toContain("tool-key");
    const oldRosterButton = screen.getByRole("button", {
      name: /Roster projector sentinel/,
    });
    const oldOpaqueKey = oldRosterButton
      .closest("[data-session-id]")
      ?.getAttribute("data-session-id");
    expect(oldOpaqueKey).toBeTruthy();
    expect(oldOpaqueKey).not.toBe("private-conversation-ref");
    fireEvent.click(oldRosterButton);
    expect(harness.callbacks.onOpenConversation).toHaveBeenCalledWith(
      "private-conversation-ref",
    );
    harness.callbacks.onOpenConversation.mockClear();

    rerender(<LiveConceptHost {...harness.props} surface="conversation" />);
    expect(
      screen.getByText("Old profile transcript sentinel"),
    ).toBeInTheDocument();
    rerender(<LiveConceptHost {...harness.props} surface="work" />);
    expect(
      screen.getByText("Old profile activity sentinel"),
    ).toBeInTheDocument();

    const oldRosterState = harness.runtime.rosterStore?.getState();
    const oldConversationState = harness.runtime.conversationStore.getState();
    const oldActivityState = harness.runtime.activityStore.getState();
    const runtimeB = { ...harness.runtime, profileId: "profile-b" };

    rerender(
      <LiveConceptHost
        {...harness.props}
        runtime={runtimeB}
        surface="sessions"
      />,
    );

    expect(document.body.innerHTML).not.toContain("Roster projector sentinel");
    rerender(
      <LiveConceptHost
        {...harness.props}
        runtime={runtimeB}
        surface="conversation"
      />,
    );
    expect(document.body.innerHTML).not.toContain(
      "Old profile transcript sentinel",
    );
    rerender(
      <LiveConceptHost {...harness.props} runtime={runtimeB} surface="work" />,
    );
    expect(document.body.innerHTML).not.toContain(
      "Old profile activity sentinel",
    );
    expect(harness.runtime.rosterStore?.getState()).toBe(oldRosterState);
    expect(harness.runtime.conversationStore.getState()).toBe(
      oldConversationState,
    );
    expect(harness.runtime.activityStore.getState()).toBe(oldActivityState);
    expect(resetProfileScope).not.toHaveBeenCalled();

    act(() => {
      container.appendChild(oldRosterButton);
    });
    fireEvent.click(oldRosterButton);
    expect(harness.callbacks.onOpenConversation).not.toHaveBeenCalled();
    oldRosterButton.remove();

    await resetAllProfileSources(harness, {
      ref: "new-profile-ref",
      rosterTitle: "New profile roster sentinel",
      project: "New profile project",
      threadId: "new-profile-thread-id",
      sessionId: "new-profile-session-id",
      itemId: "new-profile-item-id",
      transcript: "New profile transcript sentinel",
      jobId: "new-profile-job-id",
      activity: "New profile activity sentinel",
    });

    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={2}
        runtime={runtimeB}
        surface="sessions"
      />,
    );
    expect(resetProfileScope).toHaveBeenCalledTimes(1);
    const state = harness.uiStore.getState();
    expect(state.concept).toBe("constellation");
    expect(state.workOpen).toBe(false);
    expect(state.composerMode).toBe("send");
    expect(state.expandedToolKeys.size).toBe(0);
    expect(state.expandedWorkKeys.size).toBe(0);
    expect(state.questionDrafts).toEqual({});
    expect(state.focusedItemKey).toBeNull();
    expect(state.scrollAnchors).toEqual({});
    expect(screen.getByText("New profile roster sentinel")).toBeInTheDocument();
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={2}
        runtime={runtimeB}
        surface="conversation"
      />,
    );
    expect(
      screen.getByText("New profile transcript sentinel"),
    ).toBeInTheDocument();
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={2}
        runtime={runtimeB}
        surface="work"
      />,
    );
    expect(
      screen.getByText("New profile activity sentinel"),
    ).toBeInTheDocument();
    expect(resetProfileScope).toHaveBeenCalledTimes(1);
  });

  it("uses monotonic epochs to block a rapid profile alias until epoch three", async () => {
    const harness = makeHarness();
    const runtimeA = harness.runtime;
    const runtimeB = { ...runtimeA, profileId: "profile-b" };
    const { rerender } = render(<LiveConceptHost {...harness.props} />);

    await resetAllProfileSources(harness, {
      ref: "profile-b-ref",
      rosterTitle: "Profile B roster sentinel",
      project: "Profile B project",
      threadId: "profile-b-thread-id",
      sessionId: "profile-b-session-id",
      itemId: "profile-b-item-id",
      transcript: "Profile B transcript sentinel",
      jobId: "profile-b-job-id",
      activity: "Profile B activity sentinel",
    });
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={2}
        runtime={runtimeB}
      />,
    );
    expect(screen.getByText("Profile B roster sentinel")).toBeInTheDocument();

    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={2}
        runtime={runtimeA}
      />,
    );
    expect(document.body.innerHTML).not.toContain("Profile B roster sentinel");
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={2}
        runtime={runtimeA}
        surface="conversation"
      />,
    );
    expect(document.body.innerHTML).not.toContain(
      "Profile B transcript sentinel",
    );
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={2}
        runtime={runtimeA}
        surface="work"
      />,
    );
    expect(document.body.innerHTML).not.toContain(
      "Profile B activity sentinel",
    );
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={1}
        runtime={runtimeA}
      />,
    );
    expect(document.body.innerHTML).not.toContain("Profile B roster sentinel");

    await resetAllProfileSources(harness, {
      ref: "profile-a-return-ref",
      rosterTitle: "Profile A return roster sentinel",
      project: "Profile A return project",
      threadId: "profile-a-return-thread-id",
      sessionId: "profile-a-return-session-id",
      itemId: "profile-a-return-item-id",
      transcript: "Profile A return transcript sentinel",
      jobId: "profile-a-return-job-id",
      activity: "Profile A return activity sentinel",
    });
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={3}
        runtime={runtimeA}
      />,
    );
    expect(
      screen.getByText("Profile A return roster sentinel"),
    ).toBeInTheDocument();
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={3}
        runtime={runtimeA}
        surface="conversation"
      />,
    );
    expect(
      screen.getByText("Profile A return transcript sentinel"),
    ).toBeInTheDocument();
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={3}
        runtime={runtimeA}
        surface="work"
      />,
    );
    expect(
      screen.getByText("Profile A return activity sentinel"),
    ).toBeInTheDocument();
  });

  it("accepts sources cleared before the profile change when epoch advances", async () => {
    const harness = makeHarness();
    const { rerender } = render(<LiveConceptHost {...harness.props} />);

    await resetAllProfileSources(harness, {
      ref: "precleared-profile-b-ref",
      rosterTitle: "Pre-cleared B roster sentinel",
      project: "Pre-cleared B project",
      threadId: "precleared-profile-b-thread-id",
      sessionId: "precleared-profile-b-session-id",
      itemId: "precleared-profile-b-item-id",
      transcript: "Pre-cleared B transcript sentinel",
      jobId: "precleared-profile-b-job-id",
      activity: "Pre-cleared B activity sentinel",
    });
    const runtimeB = { ...harness.runtime, profileId: "profile-b" };
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={2}
        runtime={runtimeB}
      />,
    );
    expect(
      screen.getByText("Pre-cleared B roster sentinel"),
    ).toBeInTheDocument();
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={2}
        runtime={runtimeB}
        surface="conversation"
      />,
    );
    expect(
      screen.getByText("Pre-cleared B transcript sentinel"),
    ).toBeInTheDocument();
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={2}
        runtime={runtimeB}
        surface="work"
      />,
    );
    expect(
      screen.getByText("Pre-cleared B activity sentinel"),
    ).toBeInTheDocument();
  });

  it("resets UI once per accepted epoch while preserving concept and initial state", async () => {
    const harness = makeHarness();
    act(() => {
      harness.uiStore.getState().toggleTool("preserved-on-mount");
      harness.uiStore.getState().setFocusedItemKey("focused-on-mount");
    });
    const resetProfileScope = vi.spyOn(
      harness.uiStore.getState(),
      "resetProfileScope",
    );
    const { rerender } = render(<LiveConceptHost {...harness.props} />);

    expect(resetProfileScope).not.toHaveBeenCalled();
    expect(harness.uiStore.getState().expandedToolKeys).toContain(
      "preserved-on-mount",
    );
    expect(harness.uiStore.getState().focusedItemKey).toBe("focused-on-mount");
    expect(screen.queryByText(/^Search$/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Lab Controls/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/synthetic/i)).not.toBeInTheDocument();

    act(() => harness.uiStore.getState().setConcept("field-notes"));
    await resetAllProfileSources(harness, {
      ref: "same-profile-new-scope-ref",
      rosterTitle: "Same profile new scope roster",
      project: "Same profile new scope project",
      threadId: "same-profile-new-scope-thread",
      sessionId: "same-profile-new-scope-session",
      itemId: "same-profile-new-scope-item",
      transcript: "Same profile new scope transcript",
      jobId: "same-profile-new-scope-job",
      activity: "Same profile new scope activity",
    });
    rerender(<LiveConceptHost {...harness.props} profileScopeEpoch={2} />);
    expect(resetProfileScope).toHaveBeenCalledTimes(1);
    expect(harness.uiStore.getState().concept).toBe("field-notes");
    expect(harness.uiStore.getState().expandedToolKeys.size).toBe(0);
    expect(harness.uiStore.getState().focusedItemKey).toBeNull();
    rerender(
      <LiveConceptHost
        {...harness.props}
        profileScopeEpoch={2}
        surface="conversation"
      />,
    );
    expect(resetProfileScope).toHaveBeenCalledTimes(1);
    expect(
      screen.getByText("Same profile new scope transcript"),
    ).toBeInTheDocument();
  });
});
