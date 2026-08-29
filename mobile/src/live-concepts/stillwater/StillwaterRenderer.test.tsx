// Focused surface tests for the Stillwater live concept renderer. Covers all
// renderer-owned surfaces (Sessions and Work) against one LiveConceptState:
// grouped roster rows, work hierarchy, open-switcher intent, production dispatch
// callbacks, disclosure state, and the absence of Search/New/Settings/
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
} from "../contract";
import type { BoundedDisplayText } from "../model";
import { stillwaterModule } from "./index";
import { StillwaterRenderer } from "./StillwaterRenderer";

afterEach(() => {
  cleanup();
});

function bounded(text: string, truncated = false): BoundedDisplayText {
  return {
    text,
    truncated,
    originalUtf8Bytes: new TextEncoder().encode(text).length,
  };
}

function buildState(
  overrides: Partial<LiveConceptState> = {},
): LiveConceptState {
  return {
    concept: "stillwater",
    platform: "ios",
    appearance: "system",
    textScale: "large",
    reducedMotion: false,
    surface: "sessions",
    connection: {
      status: "connected",
      evidence: {
        serverName: "test-hub",
        serverVersion: "hub-commit-1",
        protocolVersion: "evener-appwire-v3",
        appVersion: "0.1.0",
      },
    },
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
      title: bounded("Fix auth flow"),
      project: bounded("evener/mobile"),
      status: bounded("running"),
      tone: "running",
      updatedLabel: bounded("2 minutes ago"),
      items: [
        {
          key: "msg-1",
          sourceKind: "user",
          label: bounded("1"),
          body: bounded("Check the refresh loop"),
          tone: "idle",
          streaming: false,
          questionKey: null,
          evidenceKey: null,
          sequence: "1",
        },
        {
          key: "msg-2",
          sourceKind: "assistant",
          label: bounded("2"),
          body: bounded("Looking into it..."),
          tone: "running",
          streaming: true,
          questionKey: null,
          evidenceKey: null,
          sequence: "2",
        },
        {
          key: "msg-3",
          sourceKind: "tool",
          semanticKind: "tool",
          label: bounded("read_file"),
          preview: bounded("Read auth.ts — 240 lines"),
          duration: null,
          tone: "success",
          state: "completed",
          evidenceKey: null,
          sequence: "3",
        },
        {
          key: "msg-4",
          sourceKind: "assistant",
          label: bounded("4"),
          body: bounded(
            "The token refresh logic has a race condition...",
            true,
          ),
          tone: "idle",
          streaming: false,
          questionKey: null,
          evidenceKey: null,
          sequence: "4",
        },
        {
          key: "msg-5",
          sourceKind: "question",
          label: bounded("5"),
          body: bounded("Which approach do you prefer?"),
          tone: "attention",
          streaming: false,
          questionKey: "q-1",
          evidenceKey: null,
          sequence: "5",
        },
        {
          key: "msg-6",
          sourceKind: "failure",
          label: bounded("6"),
          body: bounded("Connection lost during generation"),
          tone: "failed",
          streaming: false,
          questionKey: null,
          evidenceKey: null,
          sequence: "6",
        },
        {
          key: "msg-7",
          sourceKind: "attachment",
          semanticKind: "attachment",
          label: bounded("screenshot.png"),
          preview: bounded("Screenshot of the error dialog"),
          duration: null,
          tone: "idle",
          state: "completed",
          evidenceKey: null,
          sequence: "7",
        },
      ],
      evidence: [],
      questions: [
        {
          key: "q-1",
          header: bounded("Choose one"),
          prompt: bounded("Which approach do you prefer?"),
          options: [
            {
              key: "opt-a",
              label: bounded("Retry with backoff"),
              detail: bounded("Exponential backoff before retry"),
            },
            {
              key: "opt-b",
              label: bounded("Fail fast"),
              detail: bounded("Surface the error immediately"),
            },
          ],
          multiple: false,
          why: null,
          ifUnanswered: null,
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
      accepted: { kind: "send", disposition: "applied" },
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

const surfaces = ["sessions", "work"] as const;

describe("stillwaterModule", () => {
  it("satisfies LiveConceptModule with id, label, and Renderer", () => {
    expectTypeOf(stillwaterModule).toMatchTypeOf<LiveConceptModule>();
    expect(stillwaterModule.id).toBe("stillwater");
    expect(stillwaterModule.label).toBe("Stillwater");
    expect(stillwaterModule.Renderer).toBe(StillwaterRenderer);
    expect(stillwaterModule.conversationSkin.id).toBe("stillwater");
  });

  it("leaves conversation structure to the registered shared frame", () => {
    const { container } = renderSurface(
      buildState({ surface: "conversation" }),
    );
    expect(container).toBeEmptyDOMElement();
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
  it("exposes concise landmarks, roster results, and title-based row actions", () => {
    const { container } = renderSurface(buildState({ surface: "sessions" }));
    expect(
      screen.getByRole("region", {
        name: "Stillwater sessions",
      }),
    ).toBeVisible();
    expect(
      screen.getByRole("status", {
        name: "Stillwater sessions; 3 sessions; complete list",
      }),
    ).toBeVisible();
    expect(
      screen.getByRole("button", {
        name: "Open Fix auth flow; status attention",
      }),
    ).toBeVisible();
    expect(container.querySelector("[aria-label*='sess-1']")).toBeNull();
    expect(container.querySelector("[aria-label*='sha256:']")).toBeNull();
    expect(
      container.querySelector("[aria-label*='com.primeradiant.evener']"),
    ).toBeNull();
  });

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

describe("StillwaterRenderer work surface", () => {
  it("exposes concise work and usage labels without internal keys", () => {
    const { container } = renderSurface(buildState({ surface: "work" }));
    expect(
      screen.getByRole("article", {
        name: "Task Fix auth flow; status running",
      }),
    ).toBeVisible();
    expect(
      screen.getByRole("region", {
        name: "Usage summary",
      }),
    ).toBeVisible();
    expect(container.querySelector("[aria-label*='task-1']")).toBeNull();
  });

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
