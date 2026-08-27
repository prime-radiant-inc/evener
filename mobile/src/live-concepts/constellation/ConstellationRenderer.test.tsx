// TDD surface tests for the live Constellation concept module. Renders the
// ConstellationRenderer with a single LiveConceptState per surface and
// asserts the live contract presentation: grouped roster rows, real
// capability controls, streaming/truncated transcript markers, nested work
// hierarchy, connected-work relationship rails, explicit attention signal,
// current-work marker, full question interaction, olderAvailable
// affordance, reduced-motion semantics, and the absence of every
// prototype-only control (Search/New/Settings/Voice/Lab/synthetic).

import { readFileSync } from "node:fs";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { LiveConceptState, QuestionDraft } from "../contract";
import type {
  LiveActivityView,
  LiveConversationView,
  LiveQuestionView,
  LiveRosterView,
} from "../model";
import { ConstellationRenderer } from "./ConstellationRenderer";
import { constellationModule } from "./index";

const constellationCss = readFileSync(
  "src/live-concepts/constellation/constellation.css",
  "utf8",
);

afterEach(() => {
  cleanup();
});

/* --------------------------------- fixtures -------------------------------- */

function rosterView(overrides: Partial<LiveRosterView> = {}): LiveRosterView {
  return {
    status: "ready",
    query: "",
    hasMore: false,
    error: null,
    groups: [
      {
        id: "needsYou",
        label: "Needs you",
        rows: [
          {
            key: "session-a",
            title: "Wire handshake",
            project: "evener-hub",
            summary: "AppWire handshake is failing under load.",
            updatedLabel: "2 minutes ago",
            tone: "attention",
            connectedWorkCount: 2,
          },
        ],
      },
      {
        id: "running",
        label: "Running",
        rows: [
          {
            key: "session-b",
            title: "Live roster projector",
            project: "mobile",
            summary: "Projecting live roster views from the store.",
            updatedLabel: "just now",
            tone: "running",
            connectedWorkCount: 0,
          },
        ],
      },
      {
        id: "recent",
        label: "Recent",
        rows: [
          {
            key: "session-c",
            title: "Completed audit",
            project: "agent",
            summary: "Audit completed cleanly.",
            updatedLabel: "1 hour ago",
            tone: "success",
            connectedWorkCount: 0,
          },
        ],
      },
    ],
    ...overrides,
  };
}

const questionView: LiveQuestionView = {
  key: "q1",
  header: "Permission required",
  prompt: "Which transport should the audit use?",
  options: [
    { key: "opt-a", label: "WebSocket", detail: "Real-time bidirectional" },
    { key: "opt-b", label: "HTTP polling", detail: "Simpler but slower" },
  ],
  multiple: false,
};

function conversationView(
  overrides: Partial<LiveConversationView> = {},
): LiveConversationView {
  return {
    threadKey: "session-a",
    title: "Wire handshake",
    project: "evener-hub",
    status: "running",
    tone: "running",
    updatedLabel: "2 minutes ago",
    items: [
      {
        key: "m1",
        kind: "user",
        label: "You",
        body: "Why is the handshake failing?",
        tone: "idle",
        streaming: false,
        truncated: false,
        questionKey: null,
        sequenceLabel: "1",
      },
      {
        key: "m2",
        kind: "assistant",
        label: "Assistant",
        body: "Investigating the AppWire transport.",
        tone: "running",
        streaming: true,
        truncated: false,
        questionKey: null,
        sequenceLabel: "2",
      },
      {
        key: "m3",
        kind: "tool",
        label: "run_audit",
        body: "Audited 12 files.",
        tone: "running",
        streaming: false,
        truncated: false,
        questionKey: null,
        sequenceLabel: "3",
      },
      {
        key: "m4",
        kind: "assistant",
        label: "Assistant",
        body: "Found a race in the handshake.",
        tone: "idle",
        streaming: false,
        truncated: true,
        questionKey: null,
        sequenceLabel: "4",
      },
      {
        key: "m5",
        kind: "question",
        label: "Permission",
        body: "Which transport should the audit use?",
        tone: "attention",
        streaming: false,
        truncated: false,
        questionKey: "q1",
        sequenceLabel: "5",
      },
    ],
    questions: [questionView],
    olderAvailable: true,
    ...overrides,
  };
}

function activityView(
  overrides: Partial<LiveActivityView> = {},
): LiveActivityView {
  return {
    tasks: [
      { status: "active", count: 1 },
      { status: "open", count: 2 },
      { status: "done", count: 3 },
    ],
    work: [
      {
        key: "w1",
        kind: "task",
        title: "Fix handshake",
        detail: "Race condition in AppWire handshake.",
        tone: "running",
        children: [
          {
            key: "w1d1",
            kind: "delegate",
            title: "Audit transport",
            detail: "Audit the transport layer.",
            tone: "running",
            children: [],
          },
          {
            key: "w1d2",
            kind: "job",
            title: "Run tests",
            detail: "Run the handshake suite.",
            tone: "idle",
            children: [],
          },
        ],
      },
      {
        key: "w2",
        kind: "watch",
        title: "Watch logs",
        detail: "Watching for handshake errors.",
        tone: "idle",
        children: [],
      },
    ],
    usage: {
      totalTokens: 15_000,
      cost: "$0.42",
      contextPressure: 62,
      durationMs: 180_000,
    },
    ...overrides,
  };
}

function defaultQuestionDraft(): QuestionDraft {
  return { selectedOptionKeys: [], note: "", resolution: null };
}

function buildState(
  overrides: Partial<LiveConceptState> & {
    surface?: LiveConceptState["surface"];
    reducedMotion?: boolean;
  } = {},
): LiveConceptState {
  const { surface = "sessions", reducedMotion = false, ...rest } = overrides;
  return {
    concept: "constellation",
    platform: "ios",
    appearance: "dark",
    textScale: "standard",
    reducedMotion,
    surface,
    connection: { status: "connected" },
    roster: rosterView(),
    conversation: conversationView(),
    activity: activityView(),
    composer: {
      draft: "",
      canSend: true,
      canSteer: true,
      canQueue: true,
      canInterrupt: false,
      pending: null,
      error: null,
    },
    ui: {
      concept: "constellation",
      workOpen: false,
      composerMode: "send",
      expandedToolKeys: new Set<string>(),
      expandedWorkKeys: new Set<string>(),
      questionDrafts: {},
      focusedItemKey: null,
      scrollAnchors: {},
    },
    ...rest,
  };
}

function renderConstellation(state: LiveConceptState) {
  const dispatch = vi.fn();
  const result = render(
    <ConstellationRenderer state={state} dispatch={dispatch} />,
  );
  return { dispatch, ...result };
}

function conceptRoot(): HTMLElement {
  return screen.getByTestId("concept-root");
}

/* --------------------------------- module --------------------------------- */

describe("constellationModule", () => {
  it("exports the exact live module label contract", () => {
    expect(constellationModule.id).toBe("constellation");
    expect(constellationModule.label).toBe("Constellation");
    expect(constellationModule.Renderer).toBe(ConstellationRenderer);
  });
});

/* --------------------------------- sessions -------------------------------- */

describe("Constellation sessions surface", () => {
  it("renders grouped roster rows with group headers and counts", () => {
    renderConstellation(buildState({ surface: "sessions" }));
    const main = screen.getByRole("main");
    expect(main).toHaveAttribute("data-route", "sessions");

    expect(
      within(main).getByRole("heading", { name: "Needs you", level: 2 }),
    ).toBeInTheDocument();
    expect(
      within(main).getByRole("heading", { name: "Running", level: 2 }),
    ).toBeInTheDocument();
    expect(
      within(main).getByRole("heading", { name: "Recent", level: 2 }),
    ).toBeInTheDocument();

    const groupNeedsYou = within(main).getByTestId("session-group-needsYou");
    expect(groupNeedsYou).toHaveTextContent("1");
  });

  it("shows an explicit attention signal with a non-color data marker", () => {
    renderConstellation(buildState({ surface: "sessions" }));
    const main = screen.getByRole("main");
    const attention = within(main).getByTestId("attention-signal");
    expect(attention).toBeVisible();
    expect(attention).toHaveTextContent(/needs attention/i);
    expect(attention).toHaveAttribute("data-attention-state", "attention");
  });

  it("renders connected-work relationship rails with connection markers", () => {
    renderConstellation(buildState({ surface: "sessions" }));
    const main = screen.getByRole("main");
    const row = within(main).getByTestId("session-row-session-a");
    const rail = within(row).getByTestId("relationship-rail");
    expect(rail).toBeVisible();
    expect(rail).toHaveTextContent(/2 connected work items/i);
    expect(within(rail).getByTestId("connection-marker")).toBeInTheDocument();
  });

  it("omits a relationship rail when there is no connected work", () => {
    renderConstellation(buildState({ surface: "sessions" }));
    const main = screen.getByRole("main");
    const row = within(main).getByTestId("session-row-session-b");
    expect(
      within(row).queryByTestId("relationship-rail"),
    ).not.toBeInTheDocument();
  });

  it("dispatches openConversation when a session row is activated", () => {
    const { dispatch } = renderConstellation(
      buildState({ surface: "sessions" }),
    );
    fireEvent.click(screen.getByRole("button", { name: /wire handshake/i }));
    expect(dispatch).toHaveBeenCalledWith({
      type: "openConversation",
      key: "session-a",
    });
  });

  it("dispatches setRosterQuery when the roster filter changes", () => {
    const { dispatch } = renderConstellation(
      buildState({ surface: "sessions" }),
    );
    const input = screen.getByLabelText("Filter sessions");
    fireEvent.change(input, { target: { value: "handshake" } });
    expect(dispatch).toHaveBeenCalledWith({
      type: "setRosterQuery",
      value: "handshake",
    });
  });

  it("dispatches refreshRoster when refresh is activated while ready", () => {
    const { dispatch } = renderConstellation(
      buildState({
        surface: "sessions",
        roster: rosterView({ status: "ready" }),
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /refresh/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "refreshRoster" });
  });

  it("disables refresh while loading", () => {
    renderConstellation(
      buildState({
        surface: "sessions",
        roster: rosterView({ status: "loading" }),
      }),
    );
    expect(screen.getByRole("button", { name: /refresh/i })).toBeDisabled();
  });

  it("disables the filter input while loading", () => {
    renderConstellation(
      buildState({
        surface: "sessions",
        roster: rosterView({ status: "loading" }),
      }),
    );
    expect(screen.getByLabelText("Filter sessions")).toBeDisabled();
  });

  it("surfaces a roster loading state", () => {
    renderConstellation(
      buildState({
        surface: "sessions",
        roster: rosterView({ status: "loading" }),
      }),
    );
    expect(screen.getByText(/refreshing the live roster/i)).toBeInTheDocument();
  });

  it("surfaces a roster idle state", () => {
    renderConstellation(
      buildState({
        surface: "sessions",
        roster: rosterView({ status: "idle" }),
      }),
    );
    expect(screen.getByText(/idle/i)).toBeInTheDocument();
  });

  it("surfaces a roster offline state", () => {
    renderConstellation(
      buildState({
        surface: "sessions",
        roster: rosterView({ status: "offline" }),
      }),
    );
    expect(
      screen.getByRole("heading", { name: /roster offline/i, level: 2 }),
    ).toBeInTheDocument();
  });

  it("surfaces a roster error state with the actual error text", () => {
    renderConstellation(
      buildState({
        surface: "sessions",
        roster: rosterView({ status: "error", error: "Connection timed out" }),
      }),
    );
    expect(
      screen.getByRole("heading", { name: /roster unavailable/i, level: 2 }),
    ).toBeInTheDocument();
    expect(screen.getByText("Connection timed out")).toBeInTheDocument();
  });

  it("renders an empty roster state when there are no groups", () => {
    renderConstellation(
      buildState({ surface: "sessions", roster: rosterView({ groups: [] }) }),
    );
    expect(screen.getByText(/no matching sessions/i)).toBeInTheDocument();
  });

  it("renders a hasMore affordance when roster has more sessions", () => {
    renderConstellation(
      buildState({
        surface: "sessions",
        roster: rosterView({ hasMore: true }),
      }),
    );
    expect(screen.getByText(/more sessions available/i)).toBeInTheDocument();
  });

  it("omits the hasMore affordance when there are no more sessions", () => {
    renderConstellation(buildState({ surface: "sessions" }));
    expect(
      screen.queryByText(/more sessions available/i),
    ).not.toBeInTheDocument();
  });
});

/* ------------------------------ conversation ------------------------------- */

describe("Constellation conversation surface", () => {
  it("renders transcript items keyed by kind", () => {
    renderConstellation(buildState({ surface: "conversation" }));
    const main = screen.getByRole("main");
    expect(main).toHaveAttribute("data-route", "conversation");

    expect(
      within(main).getByText("Why is the handshake failing?"),
    ).toBeInTheDocument();
    expect(
      within(main).getByText("Investigating the AppWire transport."),
    ).toBeInTheDocument();
  });

  it("uses conversation.tone for the session summary status label, not the status string", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        conversation: conversationView({
          tone: "attention",
          status: "needs-answer",
        }),
      }),
    );
    const main = screen.getByRole("main");
    // The StatusLabel should show "Needs attention" from the tone, not the raw status string
    expect(within(main).getByText(/needs attention/i)).toBeInTheDocument();
    expect(within(main).queryByText("needs-answer")).not.toBeInTheDocument();
  });

  it("renders the conversation updatedLabel when present", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        conversation: conversationView({ updatedLabel: "3 hours ago" }),
      }),
    );
    expect(screen.getByText("3 hours ago")).toBeInTheDocument();
  });

  it("omits a fabricated updatedLabel when it is null", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        conversation: conversationView({ updatedLabel: null }),
      }),
    );
    expect(screen.queryByText(/null/i)).not.toBeInTheDocument();
  });

  it("marks streaming transcript items", () => {
    renderConstellation(buildState({ surface: "conversation" }));
    const main = screen.getByRole("main");
    const streaming = within(main).getByTestId("transcript-item-m2");
    expect(streaming).toHaveAttribute("data-streaming", "true");
  });

  it("marks truncated transcript items", () => {
    renderConstellation(buildState({ surface: "conversation" }));
    const main = screen.getByRole("main");
    const truncated = within(main).getByTestId("transcript-item-m4");
    expect(truncated).toHaveAttribute("data-truncated", "true");
  });

  it("marks the current running tool as current work", () => {
    renderConstellation(buildState({ surface: "conversation" }));
    const main = screen.getByRole("main");
    const tool = within(main).getByTestId("transcript-item-m3");
    expect(tool).toHaveAttribute("data-current-work", "true");
  });

  it("discloses tool output bound to expandedToolKeys", () => {
    const { dispatch } = renderConstellation(
      buildState({
        surface: "conversation",
        ui: {
          concept: "constellation",
          workOpen: false,
          composerMode: "send",
          expandedToolKeys: new Set(["m3"]),
          expandedWorkKeys: new Set(),
          questionDrafts: {},
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
    );
    const main = screen.getByRole("main");
    const toolItem = within(main).getByTestId("transcript-item-m3");
    expect(within(toolItem).getByRole("region")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /run_audit/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "toggleTool", key: "m3" });
  });

  it("does not disclose tool output when expandedToolKeys is empty", () => {
    renderConstellation(buildState({ surface: "conversation" }));
    const main = screen.getByRole("main");
    const toolItem = within(main).getByTestId("transcript-item-m3");
    expect(within(toolItem).queryByRole("region")).not.toBeInTheDocument();
  });

  it("renders the olderAvailable affordance when older messages exist", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        conversation: conversationView({ olderAvailable: true }),
      }),
    );
    expect(
      screen.getByRole("button", { name: /load older messages/i }),
    ).toBeInTheDocument();
  });

  it("omits the olderAvailable affordance when no older messages", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        conversation: conversationView({ olderAvailable: false }),
      }),
    );
    expect(
      screen.queryByRole("button", { name: /load older messages/i }),
    ).not.toBeInTheDocument();
  });

  it("dispatches loadOlder — never openConversation — when the older button is activated", () => {
    const { dispatch } = renderConstellation(
      buildState({
        surface: "conversation",
        conversation: conversationView({ olderAvailable: true }),
      }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: /load older messages/i }),
    );
    expect(dispatch).toHaveBeenCalledWith({ type: "loadOlder" });
    expect(dispatch).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "openConversation" }),
    );
  });

  it("disables the older button when a mutation is pending", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        conversation: conversationView({ olderAvailable: true }),
        composer: {
          draft: "",
          canSend: false,
          canSteer: false,
          canQueue: false,
          canInterrupt: true,
          pending: {
            kind: "send",
            status: "pending",
            draftSnapshot: "hi",
            generation: 1,
          },
          error: null,
        },
      }),
    );
    expect(
      screen.getByRole("button", { name: /load older messages/i }),
    ).toBeDisabled();
  });
});

/* ------------------------- composer (C1 fix) ------------------------------- */

describe("Constellation composer modes", () => {
  it("renders send, steer, and queue mode buttons with accurate aria-pressed", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        ui: {
          concept: "constellation",
          workOpen: false,
          composerMode: "steer",
          expandedToolKeys: new Set(),
          expandedWorkKeys: new Set(),
          questionDrafts: {},
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
    );
    expect(screen.getByRole("button", { name: "Send" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
    expect(screen.getByRole("button", { name: "Steer" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("button", { name: "Queue" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });

  it("dispatches setComposerMode when a mode button is activated", () => {
    const { dispatch } = renderConstellation(
      buildState({ surface: "conversation" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Steer" }));
    expect(dispatch).toHaveBeenCalledWith({
      type: "setComposerMode",
      mode: "steer",
    });
  });

  it("disables the send mode when canSend is false", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "",
          canSend: false,
          canSteer: true,
          canQueue: true,
          canInterrupt: false,
          pending: null,
          error: null,
        },
      }),
    );
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Steer" })).not.toBeDisabled();
    expect(screen.getByRole("button", { name: "Queue" })).not.toBeDisabled();
  });

  it("disables the steer mode when canSteer is false", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "",
          canSend: true,
          canSteer: false,
          canQueue: true,
          canInterrupt: false,
          pending: null,
          error: null,
        },
      }),
    );
    expect(screen.getByRole("button", { name: "Steer" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Send" })).not.toBeDisabled();
  });

  it("disables the queue mode when canQueue is false", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "",
          canSend: true,
          canSteer: true,
          canQueue: false,
          canInterrupt: false,
          pending: null,
          error: null,
        },
      }),
    );
    expect(screen.getByRole("button", { name: "Queue" })).toBeDisabled();
  });

  it("enables the textarea when any text mode is available and no pending", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "hello",
          canSend: false,
          canSteer: true,
          canQueue: false,
          canInterrupt: false,
          pending: null,
          error: null,
        },
      }),
    );
    expect(screen.getByPlaceholderText(/message or steer/i)).not.toBeDisabled();
  });

  it("disables the textarea when all text modes are unavailable", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "hello",
          canSend: false,
          canSteer: false,
          canQueue: false,
          canInterrupt: false,
          pending: null,
          error: null,
        },
      }),
    );
    expect(screen.getByPlaceholderText(/message or steer/i)).toBeDisabled();
  });

  it("disables the textarea when a mutation is pending", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "hello",
          canSend: true,
          canSteer: true,
          canQueue: true,
          canInterrupt: true,
          pending: {
            kind: "send",
            status: "pending",
            draftSnapshot: "hello",
            generation: 1,
          },
          error: null,
        },
      }),
    );
    expect(screen.getByPlaceholderText(/message or steer/i)).toBeDisabled();
  });

  it("dispatches setDraft on textarea change", () => {
    const { dispatch } = renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "fix the race",
          canSend: true,
          canSteer: true,
          canQueue: true,
          canInterrupt: false,
          pending: null,
          error: null,
        },
      }),
    );
    const textarea = screen.getByPlaceholderText(/message or steer/i);
    fireEvent.change(textarea, { target: { value: "steer the thread" } });
    expect(dispatch).toHaveBeenCalledWith({
      type: "setDraft",
      value: "steer the thread",
    });
  });

  it("uses ui.composerMode for the primary submit, not the last clicked mode", () => {
    const { dispatch } = renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "fix the race",
          canSend: true,
          canSteer: true,
          canQueue: true,
          canInterrupt: false,
          pending: null,
          error: null,
        },
        ui: {
          concept: "constellation",
          workOpen: false,
          composerMode: "queue",
          expandedToolKeys: new Set(),
          expandedWorkKeys: new Set(),
          questionDrafts: {},
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /submit message/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "submit", mode: "queue" });
  });

  it("disables the primary submit when the draft is empty", () => {
    renderConstellation(buildState({ surface: "conversation" }));
    expect(
      screen.getByRole("button", { name: /submit message/i }),
    ).toBeDisabled();
  });

  it("disables the primary submit when a mutation is pending", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "hello",
          canSend: true,
          canSteer: true,
          canQueue: true,
          canInterrupt: true,
          pending: {
            kind: "send",
            status: "pending",
            draftSnapshot: "hello",
            generation: 1,
          },
          error: null,
        },
      }),
    );
    expect(
      screen.getByRole("button", { name: /submit message/i }),
    ).toBeDisabled();
  });

  it("renders the interrupt control only when canInterrupt is true", () => {
    renderConstellation(buildState({ surface: "conversation" }));
    expect(
      screen.queryByRole("button", { name: /interrupt/i }),
    ).not.toBeInTheDocument();

    cleanup();
    renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "",
          canSend: false,
          canSteer: false,
          canQueue: false,
          canInterrupt: true,
          pending: {
            kind: "send",
            status: "pending",
            draftSnapshot: "hi",
            generation: 1,
          },
          error: null,
        },
      }),
    );
    expect(
      screen.getByRole("button", { name: /interrupt/i }),
    ).toBeInTheDocument();
  });

  it("dispatches interrupt when the interrupt control is activated", () => {
    const { dispatch } = renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "",
          canSend: false,
          canSteer: false,
          canQueue: false,
          canInterrupt: true,
          pending: {
            kind: "send",
            status: "pending",
            draftSnapshot: "hi",
            generation: 1,
          },
          error: null,
        },
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /interrupt/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "interrupt" });
  });

  it("surfaces a pending mutation state", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "",
          canSend: false,
          canSteer: false,
          canQueue: false,
          canInterrupt: true,
          pending: {
            kind: "send",
            status: "pending",
            draftSnapshot: "hi",
            generation: 1,
          },
          error: null,
        },
      }),
    );
    expect(screen.getByText(/sending/i)).toBeInTheDocument();
  });

  it("surfaces a composer error state with the actual error text", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "",
          canSend: true,
          canSteer: true,
          canQueue: true,
          canInterrupt: false,
          pending: null,
          error: "Network timeout",
        },
      }),
    );
    expect(screen.getByText("Network timeout")).toBeInTheDocument();
  });

  it("dispatches openWork when the work button is activated", () => {
    const { dispatch } = renderConstellation(
      buildState({ surface: "conversation" }),
    );
    fireEvent.click(screen.getByRole("button", { name: /^work$/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "openWork" });
  });
});

/* ------------------------- question interaction (I1) ---------------------- */

describe("Constellation question interaction", () => {
  it("renders a question card linked by item.questionKey", () => {
    renderConstellation(buildState({ surface: "conversation" }));
    const main = screen.getByRole("main");
    const questionItem = within(main).getByTestId("transcript-item-m5");
    expect(
      within(questionItem).getByText(questionView.prompt),
    ).toBeInTheDocument();
  });

  it("renders question options from the linked LiveQuestionView", () => {
    renderConstellation(buildState({ surface: "conversation" }));
    expect(screen.getByText("WebSocket")).toBeInTheDocument();
    expect(screen.getByText("HTTP polling")).toBeInTheDocument();
  });

  it("dispatches setQuestionDraft when an option is selected", () => {
    const { dispatch } = renderConstellation(
      buildState({ surface: "conversation" }),
    );
    const radio = screen.getByRole("radio", { name: /websocket/i });
    fireEvent.click(radio);
    expect(dispatch).toHaveBeenCalledWith({
      type: "setQuestionDraft",
      key: "q1",
      value: expect.objectContaining({
        selectedOptionKeys: ["opt-a"],
      }),
    });
  });

  it("dispatches setQuestionDraft when the note textarea changes", () => {
    const { dispatch } = renderConstellation(
      buildState({
        surface: "conversation",
        ui: {
          concept: "constellation",
          workOpen: false,
          composerMode: "send",
          expandedToolKeys: new Set(),
          expandedWorkKeys: new Set(),
          questionDrafts: {
            q1: { ...defaultQuestionDraft(), selectedOptionKeys: ["opt-a"] },
          },
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
    );
    const noteInput = screen.getByLabelText("Note");
    fireEvent.change(noteInput, { target: { value: "prefer websocket" } });
    expect(dispatch).toHaveBeenCalledWith({
      type: "setQuestionDraft",
      key: "q1",
      value: expect.objectContaining({
        note: "prefer websocket",
      }),
    });
  });

  it("disables the submit button when no option is selected (invalid)", () => {
    renderConstellation(buildState({ surface: "conversation" }));
    expect(
      screen.getByRole("button", { name: /submit answer/i }),
    ).toBeDisabled();
  });

  it("enables the submit button when an option is selected (valid)", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        ui: {
          concept: "constellation",
          workOpen: false,
          composerMode: "send",
          expandedToolKeys: new Set(),
          expandedWorkKeys: new Set(),
          questionDrafts: {
            q1: { ...defaultQuestionDraft(), selectedOptionKeys: ["opt-a"] },
          },
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
    );
    expect(
      screen.getByRole("button", { name: /submit answer/i }),
    ).not.toBeDisabled();
  });

  it("dispatches submitQuestion when the submit button is activated", () => {
    const { dispatch } = renderConstellation(
      buildState({
        surface: "conversation",
        ui: {
          concept: "constellation",
          workOpen: false,
          composerMode: "send",
          expandedToolKeys: new Set(),
          expandedWorkKeys: new Set(),
          questionDrafts: {
            q1: { ...defaultQuestionDraft(), selectedOptionKeys: ["opt-a"] },
          },
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /submit answer/i }));
    expect(dispatch).toHaveBeenCalledWith({
      type: "submitQuestion",
      key: "q1",
    });
  });

  it("disables the submit button when a mutation is pending", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        composer: {
          draft: "",
          canSend: false,
          canSteer: false,
          canQueue: false,
          canInterrupt: true,
          pending: {
            kind: "send",
            status: "pending",
            draftSnapshot: "hi",
            generation: 1,
          },
          error: null,
        },
        ui: {
          concept: "constellation",
          workOpen: false,
          composerMode: "send",
          expandedToolKeys: new Set(),
          expandedWorkKeys: new Set(),
          questionDrafts: {
            q1: { ...defaultQuestionDraft(), selectedOptionKeys: ["opt-a"] },
          },
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
    );
    expect(
      screen.getByRole("button", { name: /submit answer/i }),
    ).toBeDisabled();
  });

  it("shows a visible missing-link error when questionKey does not match a question", () => {
    renderConstellation(
      buildState({
        surface: "conversation",
        conversation: conversationView({
          items: [
            {
              key: "m-q",
              kind: "question",
              label: "Permission",
              body: "Unknown question",
              tone: "attention",
              streaming: false,
              truncated: false,
              questionKey: "missing-q",
              sequenceLabel: "1",
            },
          ],
          questions: [],
        }),
      }),
    );
    expect(screen.getByText(/question unavailable/i)).toBeInTheDocument();
  });
});

/* ----------------------------------- work ---------------------------------- */

describe("Constellation work surface", () => {
  it("renders the nested work hierarchy as semantic lists", () => {
    renderConstellation(buildState({ surface: "work" }));
    const main = screen.getByRole("main");
    expect(main).toHaveAttribute("data-route", "work");

    const lists = within(main).getAllByTestId("relationship-list");
    expect(lists.length).toBeGreaterThan(0);
  });

  it("keys every work node with kind, parent, and depth", () => {
    renderConstellation(buildState({ surface: "work" }));
    const main = screen.getByRole("main");

    const root = within(main).getByTestId("work-node-w1");
    expect(root).toHaveAttribute("data-work-kind", "task");
    expect(root).toHaveAttribute("data-work-parent-id", "root");
    expect(root).toHaveAttribute("data-work-depth", "0");

    const child = within(main).getByTestId("work-node-w1d1");
    expect(child).toHaveAttribute("data-work-kind", "delegate");
    expect(child).toHaveAttribute("data-work-parent-id", "w1");
    expect(child).toHaveAttribute("data-work-depth", "1");
  });

  it("nests child work nodes inside their parent", () => {
    renderConstellation(buildState({ surface: "work" }));
    const main = screen.getByRole("main");
    const parent = within(main).getByTestId("work-node-w1");
    const child = within(main).getByTestId("work-node-w1d1");
    expect(parent.closest("li")).toContainElement(
      child.closest("li") as HTMLElement,
    );
  });

  it("marks the current running work node", () => {
    renderConstellation(buildState({ surface: "work" }));
    const main = screen.getByRole("main");
    const running = within(main).getByTestId("work-node-w1");
    expect(running).toHaveAttribute("data-current-work", "true");
  });

  it("discloses work detail bound to expandedWorkKeys", () => {
    const { dispatch } = renderConstellation(
      buildState({
        surface: "work",
        ui: {
          concept: "constellation",
          workOpen: true,
          composerMode: "send",
          expandedToolKeys: new Set(),
          expandedWorkKeys: new Set(["w1"]),
          questionDrafts: {},
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
    );
    expect(screen.getByText("Fix handshake")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /fix handshake/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "toggleWork", key: "w1" });
  });

  it("does not disclose work detail when expandedWorkKeys is empty", () => {
    renderConstellation(buildState({ surface: "work" }));
    const main = screen.getByRole("main");
    // No Disclosure region should be present inside work nodes
    expect(
      within(main).queryAllByTestId("work-node-w1").length,
    ).toBeGreaterThan(0);
    const workNode = within(main).getByTestId("work-node-w1");
    expect(within(workNode).queryByRole("region")).not.toBeInTheDocument();
  });

  it("renders usage with formatted tokens, cost, duration, and context", () => {
    renderConstellation(buildState({ surface: "work" }));
    const main = screen.getByRole("main");
    expect(within(main).getByText("Usage")).toBeInTheDocument();
    expect(within(main).getByText("15K tokens")).toBeInTheDocument();
    expect(within(main).getByText("$0.42")).toBeInTheDocument();
    expect(within(main).getByText("3m")).toBeInTheDocument();
    expect(within(main).getByText("62%")).toBeInTheDocument();
  });

  it("renders an empty work state when there is no activity", () => {
    renderConstellation(
      buildState({
        surface: "work",
        activity: { tasks: [], work: [], usage: {} },
      }),
    );
    expect(screen.getByText(/no active work/i)).toBeInTheDocument();
  });

  it("dispatches closeWork when the back control is activated", () => {
    const { dispatch } = renderConstellation(buildState({ surface: "work" }));
    fireEvent.click(screen.getByRole("button", { name: /back/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "closeWork" });
  });
});

/* --------------------------- concept switcher / back ------------------------ */

describe("Constellation shell navigation intents", () => {
  it("dispatches openConceptSwitcher from the concept switcher control", () => {
    const { dispatch } = renderConstellation(
      buildState({ surface: "sessions" }),
    );
    fireEvent.click(screen.getByRole("button", { name: /switch concept/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "openConceptSwitcher" });
  });

  it("dispatches goBack from the conversation surface back control", () => {
    const { dispatch } = renderConstellation(
      buildState({ surface: "conversation" }),
    );
    fireEvent.click(screen.getByRole("button", { name: /back/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "goBack" });
  });
});

/* ----------------------------- reduced motion ------------------------------- */

describe("Constellation reduced-motion semantics", () => {
  it("reflects reduced motion on the concept root data attributes", () => {
    renderConstellation(
      buildState({ surface: "sessions", reducedMotion: true }),
    );
    const root = conceptRoot();
    expect(root).toHaveAttribute("data-reduced-motion", "true");
    expect(root).toHaveAttribute("data-motion", "reduced");
  });

  it("reports full motion when reduced motion is off", () => {
    renderConstellation(
      buildState({ surface: "sessions", reducedMotion: false }),
    );
    const root = conceptRoot();
    expect(root).toHaveAttribute("data-reduced-motion", "false");
    expect(root).toHaveAttribute("data-motion", "full");
  });

  it("preserves relationship rails and attention signals under reduced motion", () => {
    renderConstellation(
      buildState({ surface: "sessions", reducedMotion: true }),
    );
    expect(screen.getByTestId("relationship-rail")).toBeInTheDocument();
    expect(screen.getByTestId("attention-signal")).toBeInTheDocument();
  });

  it("disables the current-work pulse under reduced motion in CSS", () => {
    expect(constellationCss).toContain("prefers-reduced-motion: reduce");
    expect(constellationCss).toContain('[data-reduced-motion="true"]');
    expect(constellationCss).toContain("animation");
  });
});

/* ----------------------- absence of prototype controls ---------------------- */

describe("Constellation excludes prototype-only controls", () => {
  it("renders no bottom navigation, search, new, settings, voice, lab, or synthetic controls", () => {
    for (const surface of ["sessions", "conversation", "work"] as const) {
      cleanup();
      renderConstellation(buildState({ surface }));
      const root = conceptRoot();
      expect(within(root).queryByTestId("root-nav")).not.toBeInTheDocument();
      expect(within(root).queryByText(/lab controls/i)).not.toBeInTheDocument();
      expect(within(root).queryByText(/new session/i)).not.toBeInTheDocument();
      expect(within(root).queryByText(/settings/i)).not.toBeInTheDocument();
      expect(within(root).queryByText(/^voice$/i)).not.toBeInTheDocument();
      expect(
        within(root).queryByTestId("synthetic-turn"),
      ).not.toBeInTheDocument();
      expect(
        within(root).queryByText(/complete refresh/i),
      ).not.toBeInTheDocument();
      expect(
        within(root).queryByText(/complete response/i),
      ).not.toBeInTheDocument();
    }
  });

  it("renders no prototype or synthetic symbol leakage in source", () => {
    const source = readFileSync(
      "src/live-concepts/constellation/ConstellationRenderer.tsx",
      "utf8",
    );
    expect(source).not.toMatch(/\bPrototypeState\b/);
    expect(source).not.toMatch(/\bsyntheticTurn\b/);
    const fixtureProp = `.${"fixture"}`;
    expect(source).not.toContain(fixtureProp);
  });
});
