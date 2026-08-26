// TDD surface tests for the live Constellation concept module. Renders the
// ConstellationRenderer with a single LiveConceptState per surface and
// asserts the live contract presentation: grouped roster rows, real
// capability controls, streaming/truncated transcript markers, nested work
// hierarchy, connected-work relationship rails, explicit attention signal,
// current-work marker, reduced-motion semantics, and the absence of every
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
import type { LiveConceptState } from "../contract";
import type {
  LiveActivityView,
  LiveConversationView,
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

function conversationView(
  overrides: Partial<LiveConversationView> = {},
): LiveConversationView {
  return {
    threadKey: "session-a",
    title: "Wire handshake",
    project: "evener-hub",
    status: "running",
    items: [
      {
        key: "m1",
        kind: "user",
        label: "You",
        body: "Why is the handshake failing?",
        tone: "idle",
        streaming: false,
        truncated: false,
      },
      {
        key: "m2",
        kind: "assistant",
        label: "Assistant",
        body: "Investigating the AppWire transport.",
        tone: "running",
        streaming: true,
        truncated: false,
      },
      {
        key: "m3",
        kind: "tool",
        label: "run_audit",
        body: "Audited 12 files.",
        tone: "running",
        streaming: false,
        truncated: false,
      },
      {
        key: "m4",
        kind: "assistant",
        label: "Assistant",
        body: "Found a race in the handshake.",
        tone: "idle",
        streaming: false,
        truncated: true,
      },
    ],
    questions: [],
    olderAvailable: false,
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

  it("dispatches refreshRoster when refresh is activated", () => {
    const { dispatch } = renderConstellation(
      buildState({
        surface: "sessions",
        roster: rosterView({ status: "ready" }),
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /refresh/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "refreshRoster" });
  });

  it("surfaces a roster error state", () => {
    renderConstellation(
      buildState({
        surface: "sessions",
        roster: rosterView({ status: "error", error: "Roster unavailable" }),
      }),
    );
    expect(
      screen.getByRole("heading", { name: /roster unavailable/i, level: 2 }),
    ).toBeInTheDocument();
  });

  it("renders an empty roster state when there are no groups", () => {
    renderConstellation(
      buildState({ surface: "sessions", roster: rosterView({ groups: [] }) }),
    );
    expect(screen.getByText(/no matching sessions/i)).toBeInTheDocument();
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

  it("discloses tool output through the live Disclosure primitive", () => {
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

  it("renders the composer with send, steer, and queue modes", () => {
    renderConstellation(buildState({ surface: "conversation" }));
    expect(screen.getByRole("button", { name: "Send" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Steer" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Queue" })).toBeInTheDocument();
  });

  it("dispatches setDraft and submit in the active composer mode", () => {
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
    const textarea = screen.getByRole("textbox");
    fireEvent.change(textarea, { target: { value: "steer the thread" } });
    expect(dispatch).toHaveBeenCalledWith({
      type: "setDraft",
      value: "steer the thread",
    });

    fireEvent.click(screen.getByRole("button", { name: /submit message/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "submit", mode: "send" });
  });

  it("dispatches interrupt when a turn is running", () => {
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

  it("surfaces a composer error state", () => {
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
          error: "Send failed",
        },
      }),
    );
    expect(screen.getByText(/send failed/i)).toBeInTheDocument();
  });

  it("dispatches openWork when the work button is activated", () => {
    const { dispatch } = renderConstellation(
      buildState({ surface: "conversation" }),
    );
    fireEvent.click(screen.getByRole("button", { name: /^work$/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "openWork" });
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

  it("discloses work detail through the live Disclosure primitive", () => {
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

  it("renders usage with tokens, cost, duration, and context", () => {
    renderConstellation(buildState({ surface: "work" }));
    const main = screen.getByRole("main");
    expect(within(main).getByText("Usage")).toBeInTheDocument();
    expect(within(main).getByText("15K tokens")).toBeInTheDocument();
    expect(within(main).getByText("$0.42")).toBeInTheDocument();
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
      expect(within(root).queryByText(/search/i)).not.toBeInTheDocument();
      expect(within(root).queryByText(/new session/i)).not.toBeInTheDocument();
      expect(within(root).queryByText(/settings/i)).not.toBeInTheDocument();
      expect(within(root).queryByText(/voice/i)).not.toBeInTheDocument();
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
    // Boundary is enforced by check-live-concepts-boundary.mjs; this is a
    // complementary assertion that the renderer module has no prototype
    // symbol leakage.
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
