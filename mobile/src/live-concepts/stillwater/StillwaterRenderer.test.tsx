// Focused surface tests for the Stillwater live concept renderer. Covers all
// three surfaces (Sessions, Conversation, Work) against one LiveConceptState:
// grouped roster rows, real capability controls, pending/error state, streaming
// and truncated markers, work hierarchy, open-switcher intent, production
// dispatch callbacks, disclosure state, and the absence of Search/New/Settings/
// Voice/Lab/synthetic/fixture controls inside the renderer.

import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, expectTypeOf, it, vi } from "vitest";
import type {
  LiveConceptIntent,
  LiveConceptModule,
  LiveConceptState,
  QuestionDraft,
} from "../contract";
import { stillwaterModule } from "./index";
import { StillwaterRenderer } from "./StillwaterRenderer";

afterEach(() => {
  cleanup();
});

function buildState(
  overrides: Partial<LiveConceptState> = {},
): LiveConceptState {
  return {
    concept: "stillwater",
    platform: "ios",
    appearance: "system",
    textScale: "standard",
    reducedMotion: false,
    surface: "sessions",
    connection: { status: "connected" },
    roster: {
      status: "ready",
      query: "",
      groups: [
        {
          id: "needsYou",
          label: "Needs You",
          rows: [
            {
              key: "sess-1",
              title: "Fix auth flow",
              project: "evener/mobile",
              summary: "Token refresh loop",
              updatedLabel: "2 minutes ago",
              tone: "attention",
              connectedWorkCount: 3,
            },
          ],
        },
        {
          id: "running",
          label: "Running",
          rows: [
            {
              key: "sess-2",
              title: "Build report",
              project: "evener/agent",
              summary: "Generating coverage",
              updatedLabel: "5 minutes ago",
              tone: "running",
              connectedWorkCount: 2,
            },
          ],
        },
        {
          id: "recent",
          label: "Recent",
          rows: [
            {
              key: "sess-3",
              title: "Patch types",
              project: "evener/core",
              summary: "Type narrowing",
              updatedLabel: "1 hour ago",
              tone: "idle",
              connectedWorkCount: 0,
            },
          ],
        },
      ],
      hasMore: false,
      error: null,
    },
    conversation: {
      threadKey: "sess-1",
      title: "Fix auth flow",
      project: "evener/mobile",
      status: "running",
      tone: "running",
      updatedLabel: "2 minutes ago",
      items: [
        {
          key: "msg-1",
          kind: "user",
          label: "1",
          body: "Check the refresh loop",
          tone: "idle",
          streaming: false,
          truncated: false,
          questionKey: null,
          sequenceLabel: "1",
        },
        {
          key: "msg-2",
          kind: "assistant",
          label: "2",
          body: "Looking into it...",
          tone: "running",
          streaming: true,
          truncated: false,
          questionKey: null,
          sequenceLabel: "2",
        },
        {
          key: "msg-3",
          kind: "tool",
          label: "read_file",
          body: "Read auth.ts — 240 lines",
          tone: "success",
          streaming: false,
          truncated: false,
          questionKey: null,
          sequenceLabel: "3",
        },
        {
          key: "msg-4",
          kind: "assistant",
          label: "4",
          body: "The token refresh logic has a race condition...",
          tone: "idle",
          streaming: false,
          truncated: true,
          questionKey: null,
          sequenceLabel: "4",
        },
        {
          key: "msg-5",
          kind: "question",
          label: "5",
          body: "Which approach do you prefer?",
          tone: "attention",
          streaming: false,
          truncated: false,
          questionKey: "q-1",
          sequenceLabel: "5",
        },
        {
          key: "msg-6",
          kind: "failure",
          label: "6",
          body: "Connection lost during generation",
          tone: "failed",
          streaming: false,
          truncated: false,
          questionKey: null,
          sequenceLabel: "6",
        },
        {
          key: "msg-7",
          kind: "attachment",
          label: "screenshot.png",
          body: "Screenshot of the error dialog",
          tone: "idle",
          streaming: false,
          truncated: false,
          questionKey: null,
          sequenceLabel: "7",
        },
      ],
      questions: [
        {
          key: "q-1",
          header: "Choose one",
          prompt: "Which approach do you prefer?",
          options: [
            {
              key: "opt-a",
              label: "Retry with backoff",
              detail: "Exponential backoff before retry",
            },
            {
              key: "opt-b",
              label: "Fail fast",
              detail: "Surface the error immediately",
            },
          ],
          multiple: false,
        },
      ],
      olderAvailable: true,
    },
    activity: {
      tasks: [
        { status: "active", count: 2 },
        { status: "open", count: 1 },
        { status: "done", count: 5 },
      ],
      work: [
        {
          key: "task-1",
          kind: "task",
          title: "Fix auth flow",
          detail: "Token refresh loop in auth.ts",
          tone: "running",
          children: [
            {
              key: "delegate-1",
              kind: "delegate",
              title: "Investigate refresh",
              detail: "Subagent analyzing auth.ts",
              tone: "running",
              children: [],
            },
            {
              key: "job-1",
              kind: "job",
              title: "Run tests",
              detail: "auth.test.ts",
              tone: "idle",
              children: [],
            },
          ],
        },
        {
          key: "watch-1",
          kind: "watch",
          title: "File watch",
          detail: "Watching src/auth/*",
          tone: "running",
          children: [],
        },
      ],
      usage: {
        totalTokens: 12_500,
        cost: "$0.42",
        contextPressure: 0.65,
        durationMs: 120_000,
      },
    },
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
      concept: "stillwater",
      workOpen: false,
      composerMode: "send",
      expandedToolKeys: new Set<string>(),
      expandedWorkKeys: new Set<string>(),
      questionDrafts: {},
      focusedItemKey: null,
      scrollAnchors: {},
    },
    ...overrides,
  };
}

function renderSurface(state: LiveConceptState) {
  const dispatch = vi.fn<(intent: LiveConceptIntent) => void>();
  const result = render(
    <StillwaterRenderer state={state} dispatch={dispatch} />,
  );
  return { ...result, dispatch };
}

const surfaces = ["sessions", "conversation", "work"] as const;

describe("stillwaterModule", () => {
  it("satisfies LiveConceptModule with id, label, and Renderer", () => {
    expectTypeOf(stillwaterModule).toMatchTypeOf<LiveConceptModule>();
    expect(stillwaterModule.id).toBe("stillwater");
    expect(stillwaterModule.label).toBe("Stillwater");
    expect(stillwaterModule.Renderer).toBe(StillwaterRenderer);
  });
});

describe.each(surfaces)("StillwaterRenderer on %s surface", (surface) => {
  it("renders the surface route", () => {
    const state = buildState({ surface });
    const { container } = renderSurface(state);
    expect(container.querySelector(".concept-stillwater")).toHaveAttribute(
      "data-surface",
      surface,
    );
    expect(screen.getByRole("main")).toHaveAttribute("data-route", surface);
  });

  it("renders no forbidden controls", () => {
    const state = buildState({ surface });
    const { container } = renderSurface(state);
    expect(screen.queryByRole("button", { name: /^search$/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /new session/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /^settings$/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /^voice$/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /lab/i })).toBeNull();
    expect(container.querySelector("[data-synthetic-turn]")).toBeNull();
    expect(container.querySelector("[data-new-session-state]")).toBeNull();
    expect(container.querySelector("[data-search-state]")).toBeNull();
    expect(container.querySelector("[data-fictional]")).toBeNull();
  });

  it("dispatches openConceptSwitcher from the switcher button", () => {
    const state = buildState({ surface });
    const { dispatch } = renderSurface(state);
    fireEvent.click(screen.getByRole("button", { name: /switch concept/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "openConceptSwitcher" });
  });
});

describe("StillwaterRenderer sessions surface", () => {
  it("renders grouped roster rows and opens a conversation by key", () => {
    const state = buildState({ surface: "sessions" });
    const { dispatch, container } = renderSurface(state);
    for (const group of state.roster.groups) {
      const region = container.querySelector(
        `[data-session-group-id="${group.id}"]`,
      );
      expect(region).not.toBeNull();
      expect(
        within(region as HTMLElement).getByRole("heading", {
          name: group.label,
        }),
      ).toBeVisible();
    }
    const firstRow = state.roster.groups[0]?.rows[0];
    if (!firstRow) throw new Error("Test state needs a roster row");
    fireEvent.click(
      within(
        container.querySelector(
          `[data-session-id="${firstRow.key}"]`,
        ) as HTMLElement,
      ).getByRole("button"),
    );
    expect(dispatch).toHaveBeenCalledWith({
      type: "openConversation",
      key: firstRow.key,
    });
  });

  it("dispatches setRosterQuery from the filter input", () => {
    const state = buildState({ surface: "sessions" });
    const { dispatch } = renderSurface(state);
    fireEvent.change(
      screen.getByRole("searchbox", { name: "Filter sessions" }),
      { target: { value: "auth" } },
    );
    expect(dispatch).toHaveBeenCalledWith({
      type: "setRosterQuery",
      value: "auth",
    });
  });

  it("dispatches refreshRoster from the refresh button", () => {
    const state = buildState({ surface: "sessions" });
    const { dispatch } = renderSurface(state);
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "refreshRoster" });
  });

  it("shows roster error with actual error text", () => {
    const base = buildState();
    const state = buildState({
      surface: "sessions",
      roster: {
        ...base.roster,
        status: "error",
        error: "Failed to load sessions",
        hasMore: true,
      },
    });
    const { container } = renderSurface(state);
    expect(
      container.querySelector('[data-roster-status="error"]'),
    ).toBeVisible();
    expect(screen.getByText("Failed to load sessions")).toBeVisible();
    expect(
      container.querySelector('[data-roster-has-more="true"]'),
    ).toBeVisible();
  });

  it("shows roster loading state", () => {
    const state = buildState({
      surface: "sessions",
      roster: { ...buildState().roster, status: "loading" },
    });
    const { container } = renderSurface(state);
    expect(
      container.querySelector('[data-roster-status="loading"]'),
    ).toBeVisible();
    expect(screen.getByText(/loading sessions/i)).toBeVisible();
  });

  it("shows roster idle state with empty groups", () => {
    const state = buildState({
      surface: "sessions",
      roster: { ...buildState().roster, status: "idle", groups: [] },
    });
    const { container } = renderSurface(state);
    expect(
      container.querySelector('[data-roster-status="idle"]'),
    ).toBeVisible();
    expect(screen.getByText(/no matching sessions/i)).toBeVisible();
  });

  it("shows roster offline state", () => {
    const state = buildState({
      surface: "sessions",
      connection: { status: "offline" },
      roster: { ...buildState().roster, status: "offline" },
    });
    const { container } = renderSurface(state);
    expect(
      container.querySelector('[data-roster-status="offline"]'),
    ).toBeVisible();
  });

  it("disables refresh while roster is loading", () => {
    const state = buildState({
      surface: "sessions",
      roster: { ...buildState().roster, status: "loading" },
    });
    renderSurface(state);
    expect(screen.getByRole("button", { name: "Refresh" })).toBeDisabled();
  });
});

describe("StillwaterRenderer conversation surface", () => {
  it("renders transcript items with streaming and truncated markers", () => {
    const state = buildState({ surface: "conversation" });
    const { container } = renderSurface(state);
    expect(container.querySelector('[data-streaming="true"]')).toBeVisible();
    expect(container.querySelector('[data-truncated="true"]')).toBeVisible();
    expect(
      container.querySelector('[data-older-available="true"]'),
    ).toBeVisible();
  });

  it("uses conversation.tone in the summary, never hardcoded running", () => {
    const state = buildState({
      surface: "conversation",
      conversation: {
        ...buildState().conversation!,
        tone: "attention",
      },
    });
    const { container } = renderSurface(state);
    const summary = container.querySelector(".sw-session-summary");
    expect(summary).not.toBeNull();
    expect(
      (summary as HTMLElement).querySelector('[data-status-state="attention"]'),
    ).not.toBeNull();
    expect(
      (summary as HTMLElement).querySelector('[data-status-state="running"]'),
    ).toBeNull();
  });

  it("displays updatedLabel in the summary when non-null", () => {
    const state = buildState({
      surface: "conversation",
      conversation: {
        ...buildState().conversation!,
        updatedLabel: "3 minutes ago",
      },
    });
    renderSurface(state);
    expect(screen.getByText("3 minutes ago")).toBeVisible();
  });

  it("omits updatedLabel from the summary when null", () => {
    const state = buildState({
      surface: "conversation",
      conversation: {
        ...buildState().conversation!,
        updatedLabel: null,
      },
    });
    renderSurface(state);
    expect(screen.queryByText("2 minutes ago")).toBeNull();
  });

  it("discloses a tool and dispatches toggleTool", () => {
    const state = buildState({ surface: "conversation" });
    const toolItem = state.conversation?.items.find(
      (item) => item.kind === "tool",
    );
    if (!toolItem) throw new Error("Test state needs a tool item");
    const { dispatch, container } = renderSurface(state);
    const disclosure = within(
      container.querySelector(
        `[data-transcript-item-id="${toolItem.key}"]`,
      ) as HTMLElement,
    ).getByRole("button");
    expect(disclosure).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(disclosure);
    expect(dispatch).toHaveBeenCalledWith({
      type: "toggleTool",
      key: toolItem.key,
    });
  });

  it("reflects expanded disclosure state from ui.expandedToolKeys", () => {
    const state = buildState({
      surface: "conversation",
      ui: {
        ...buildState().ui,
        expandedToolKeys: new Set(["msg-3"]),
      },
    });
    const { container } = renderSurface(state);
    const disclosure = within(
      container.querySelector(
        `[data-transcript-item-id="msg-3"]`,
      ) as HTMLElement,
    ).getByRole("button");
    expect(disclosure).toHaveAttribute("aria-expanded", "true");
  });

  it("renders a question card via item.questionKey, selects an option, and submits", () => {
    const base = buildState();
    const question = base.conversation?.questions[0];
    if (!question) throw new Error("Test state needs a question");
    const state = buildState({
      surface: "conversation",
      ui: {
        ...base.ui,
        questionDrafts: {
          [question.key]: {
            selectedOptionKeys: ["opt-a"],
            note: "",
            resolution: null,
          },
        },
      },
    });
    const { dispatch, container } = renderSurface(state);
    const form = container.querySelector(
      `[data-question-id="${question.key}"]`,
    );
    expect(form).not.toBeNull();
    const option = question.options[1];
    if (!option) throw new Error("Question needs a second option");
    fireEvent.click(within(form as HTMLElement).getByLabelText(option.label));
    expect(dispatch).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "setQuestionDraft",
        key: question.key,
      }),
    );
    fireEvent.click(
      within(form as HTMLElement).getByRole("button", {
        name: "Submit answer",
      }),
    );
    expect(dispatch).toHaveBeenCalledWith({
      type: "submitQuestion",
      key: question.key,
    });
  });

  it("disables question submit until a draft with a selection is provided", () => {
    const base = buildState();
    const question = base.conversation?.questions[0];
    if (!question) throw new Error("Test state needs a question");

    // No draft — submit disabled
    const noDraft = buildState({ surface: "conversation" });
    renderSurface(noDraft);
    expect(
      screen.getByRole("button", { name: "Submit answer" }),
    ).toBeDisabled();
    cleanup();

    // Draft with selection — submit enabled
    const withDraft = buildState({
      surface: "conversation",
      ui: {
        ...base.ui,
        questionDrafts: {
          [question.key]: {
            selectedOptionKeys: ["opt-a"],
            note: "",
            resolution: null,
          },
        },
      },
    });
    renderSurface(withDraft);
    expect(screen.getByRole("button", { name: "Submit answer" })).toBeEnabled();
  });

  it("handles missing question linkage visibly", () => {
    const state = buildState({
      surface: "conversation",
      conversation: {
        ...buildState().conversation!,
        items: [
          {
            key: "msg-orphan",
            kind: "question",
            label: "5",
            body: "Orphan question",
            tone: "attention",
            streaming: false,
            truncated: false,
            questionKey: "q-missing",
            sequenceLabel: "5",
          },
        ],
        questions: [],
      },
    });
    const { container } = renderSurface(state);
    const article = container.querySelector(
      '[data-transcript-item-id="msg-orphan"]',
    );
    expect(article).not.toBeNull();
    expect(
      within(article as HTMLElement).getByText(/question unavailable/i),
    ).toBeVisible();
  });

  it("shows resolved question state when resolution is set", () => {
    const questionKey = "q-1";
    const draft: QuestionDraft = {
      selectedOptionKeys: ["opt-a"],
      note: "",
      resolution: "answer",
    };
    const state = buildState({
      surface: "conversation",
      ui: {
        ...buildState().ui,
        questionDrafts: { [questionKey]: draft },
      },
    });
    const { container } = renderSurface(state);
    const resolved = container.querySelector(
      `[data-question-id="${questionKey}"][data-question-resolution="answer"]`,
    );
    expect(resolved).not.toBeNull();
  });

  it("dispatches openWork from the Work button", () => {
    const state = buildState({ surface: "conversation" });
    const { dispatch } = renderSurface(state);
    fireEvent.click(screen.getByRole("button", { name: "Work" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "openWork" });
  });

  it("dispatches goBack from the back button", () => {
    const state = buildState({ surface: "conversation" });
    const { dispatch } = renderSurface(state);
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "goBack" });
  });

  it("dispatches setDraft from the textarea", () => {
    const state = buildState({ surface: "conversation" });
    const { dispatch } = renderSurface(state);
    fireEvent.change(screen.getByRole("textbox", { name: "Message" }), {
      target: { value: "Continue the check" },
    });
    expect(dispatch).toHaveBeenCalledWith({
      type: "setDraft",
      value: "Continue the check",
    });
  });

  it("dispatches setComposerMode from mode buttons with accurate aria-pressed", () => {
    const state = buildState({ surface: "conversation" });
    const { dispatch } = renderSurface(state);
    fireEvent.click(screen.getByRole("button", { name: "Steer" }));
    expect(dispatch).toHaveBeenCalledWith({
      type: "setComposerMode",
      mode: "steer",
    });
  });

  it("shows accurate aria-pressed on the active mode button", () => {
    const state = buildState({
      surface: "conversation",
      ui: { ...buildState().ui, composerMode: "queue" },
    });
    renderSurface(state);
    expect(screen.getByRole("button", { name: "Queue" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("button", { name: "Send" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
    expect(screen.getByRole("button", { name: "Steer" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });

  it("dispatches submit with the selected ui.composerMode from the primary submit", () => {
    const state = buildState({
      surface: "conversation",
      composer: { ...buildState().composer, draft: "hello" },
      ui: { ...buildState().ui, composerMode: "steer" },
    });
    const { dispatch } = renderSurface(state);
    fireEvent.click(screen.getByRole("button", { name: "Submit" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "submit", mode: "steer" });
  });

  it("disables the primary submit when draft is empty", () => {
    const state = buildState({ surface: "conversation" });
    renderSurface(state);
    expect(screen.getByRole("button", { name: "Submit" })).toBeDisabled();
  });

  it("disables the primary submit when a mutation is pending", () => {
    const state = buildState({
      surface: "conversation",
      composer: {
        ...buildState().composer,
        draft: "hi",
        pending: {
          kind: "send",
          status: "pending",
          draftSnapshot: "hi",
          generation: 1,
        },
      },
    });
    renderSurface(state);
    expect(screen.getByRole("button", { name: "Submit" })).toBeDisabled();
  });

  it("disables mode buttons per capability flags", () => {
    const state = buildState({
      surface: "conversation",
      composer: {
        ...buildState().composer,
        canSend: true,
        canSteer: false,
        canQueue: false,
      },
    });
    renderSurface(state);
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Steer" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Queue" })).toBeDisabled();
  });

  it("enables textarea when any text mode is available and no pending", () => {
    const state = buildState({
      surface: "conversation",
      composer: {
        ...buildState().composer,
        canSend: true,
        canSteer: false,
        canQueue: false,
      },
    });
    renderSurface(state);
    expect(screen.getByRole("textbox", { name: "Message" })).toBeEnabled();
  });

  it("disables textarea when all modes are unavailable", () => {
    const state = buildState({
      surface: "conversation",
      composer: {
        ...buildState().composer,
        canSend: false,
        canSteer: false,
        canQueue: false,
      },
    });
    renderSurface(state);
    expect(screen.getByRole("textbox", { name: "Message" })).toBeDisabled();
  });

  it("disables textarea when a mutation is pending", () => {
    const state = buildState({
      surface: "conversation",
      composer: {
        ...buildState().composer,
        draft: "hi",
        pending: {
          kind: "send",
          status: "pending",
          draftSnapshot: "hi",
          generation: 1,
        },
      },
    });
    renderSurface(state);
    expect(screen.getByRole("textbox", { name: "Message" })).toBeDisabled();
  });

  it("shows composer pending state and dispatches interrupt", () => {
    const state = buildState({
      surface: "conversation",
      composer: {
        ...buildState().composer,
        draft: "hi",
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
      },
    });
    const { dispatch } = renderSurface(state);
    expect(screen.getByText(/sending/i)).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: /interrupt/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "interrupt" });
  });

  it("shows composer failed mutation state", () => {
    const state = buildState({
      surface: "conversation",
      composer: {
        ...buildState().composer,
        pending: {
          kind: "steer",
          status: "failed",
          draftSnapshot: "hi",
          generation: 1,
        },
      },
    });
    renderSurface(state);
    expect(screen.getByText(/steer failed/i)).toBeVisible();
  });

  it("shows composer error state", () => {
    const state = buildState({
      surface: "conversation",
      composer: {
        ...buildState().composer,
        error: "Send failed — retry",
      },
    });
    renderSurface(state);
    expect(screen.getByText("Send failed — retry")).toBeVisible();
  });
});

describe("StillwaterRenderer work surface", () => {
  it("renders work hierarchy with nested children", () => {
    const state = buildState({ surface: "work" });
    const { container } = renderSurface(state);
    const root = container.querySelector(
      '[data-work-node-id="task-1"]',
    ) as HTMLElement;
    expect(root).not.toBeNull();
    const child = container.querySelector('[data-work-node-id="delegate-1"]');
    expect(child).not.toBeNull();
    const taskLi = root.closest("li");
    expect(
      taskLi?.querySelector('[data-work-node-id="delegate-1"]'),
    ).not.toBeNull();
  });

  it("discloses a work node and dispatches toggleWork", () => {
    const state = buildState({ surface: "work" });
    const item = state.activity?.work[0];
    if (!item) throw new Error("Test state needs a work item");
    const { dispatch, container } = renderSurface(state);
    const disclosure = within(
      container.querySelector(
        `[data-work-node-id="${item.key}"]`,
      ) as HTMLElement,
    ).getByRole("button");
    fireEvent.click(disclosure);
    expect(dispatch).toHaveBeenCalledWith({
      type: "toggleWork",
      key: item.key,
    });
  });

  it("reflects expanded disclosure state from ui.expandedWorkKeys", () => {
    const state = buildState({
      surface: "work",
      ui: {
        ...buildState().ui,
        expandedWorkKeys: new Set(["task-1"]),
      },
    });
    const { container } = renderSurface(state);
    const disclosure = within(
      container.querySelector(`[data-work-node-id="task-1"]`) as HTMLElement,
    ).getByRole("button");
    expect(disclosure).toHaveAttribute("aria-expanded", "true");
  });

  it("renders usage panel with formatted values", () => {
    const state = buildState({ surface: "work" });
    const { container } = renderSurface(state);
    const usage = container.querySelector("[data-work-usage]");
    expect(usage).not.toBeNull();
    expect(
      within(usage as HTMLElement).getByText("12.5K tokens"),
    ).toBeVisible();
    expect(within(usage as HTMLElement).getByText("$0.42")).toBeVisible();
    expect(within(usage as HTMLElement).getByText("2m")).toBeVisible();
  });

  it("renders task summary counts", () => {
    const state = buildState({ surface: "work" });
    renderSurface(state);
    expect(screen.getByText(/2 active/i)).toBeVisible();
    expect(screen.getByText(/1 open/i)).toBeVisible();
    expect(screen.getByText(/5 done/i)).toBeVisible();
  });

  it("dispatches goBack from the back button", () => {
    const state = buildState({ surface: "work" });
    const { dispatch } = renderSurface(state);
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "goBack" });
  });
});
