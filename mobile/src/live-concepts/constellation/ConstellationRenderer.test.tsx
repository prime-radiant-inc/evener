// TDD surface tests for the live Constellation concept module. Renders the
// ConstellationRenderer with a single LiveConceptState per surface and
// asserts the live contract presentation: grouped roster rows, real
// renderer-owned Sessions and Work surfaces, nested work
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
  BoundedDisplayText,
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

function bounded(text: string, truncated = false): BoundedDisplayText {
  return {
    text,
    truncated,
    originalUtf8Bytes: new TextEncoder().encode(text).length,
  };
}

const questionView: LiveQuestionView = {
  key: "q1",
  header: bounded("Permission required"),
  prompt: bounded("Which transport should the audit use?"),
  options: [
    {
      key: "opt-a",
      label: bounded("WebSocket"),
      detail: bounded("Real-time bidirectional"),
    },
    {
      key: "opt-b",
      label: bounded("HTTP polling"),
      detail: bounded("Simpler but slower"),
    },
  ],
  multiple: false,
  why: null,
  ifUnanswered: null,
};

function conversationView(
  overrides: Partial<LiveConversationView> = {},
): LiveConversationView {
  return {
    threadKey: "session-a",
    title: bounded("Wire handshake"),
    project: bounded("evener-hub"),
    status: bounded("running"),
    tone: "running",
    updatedLabel: bounded("2 minutes ago"),
    items: [
      {
        key: "m1",
        sourceKind: "user",
        label: bounded("You"),
        body: bounded("Why is the handshake failing?"),
        tone: "idle",
        streaming: false,
        questionKey: null,
        evidenceKey: null,
        sequence: "1",
      },
      {
        key: "m2",
        sourceKind: "assistant",
        label: bounded("Assistant"),
        body: bounded("Investigating the AppWire transport."),
        tone: "running",
        streaming: true,
        questionKey: null,
        evidenceKey: null,
        sequence: "2",
      },
      {
        key: "m3",
        sourceKind: "tool",
        semanticKind: "tool",
        label: bounded("run_audit"),
        preview: bounded("Audited 12 files."),
        duration: null,
        tone: "running",
        state: "running",
        evidenceKey: null,
        sequence: "3",
      },
      {
        key: "m4",
        sourceKind: "assistant",
        label: bounded("Assistant"),
        body: bounded("Found a race in the handshake.", true),
        tone: "idle",
        streaming: false,
        questionKey: null,
        evidenceKey: null,
        sequence: "4",
      },
      {
        key: "m5",
        sourceKind: "question",
        label: bounded("Permission"),
        body: bounded("Which transport should the audit use?"),
        tone: "attention",
        streaming: false,
        questionKey: "q1",
        evidenceKey: null,
        sequence: "5",
      },
    ],
    evidence: [],
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
    textScale: "large",
    reducedMotion,
    surface,
    connection: {
      status: "connected",
      evidence: {
        serverName: "test-hub",
        serverVersion: "hub-commit-1",
        protocolVersion: "evener-appwire-v3",
        appVersion: "0.1.0",
      },
    },
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
      accepted: { kind: "send", disposition: "applied" },
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
    expect(constellationModule.conversationSkin?.id).toBe("constellation");
  });

  it("leaves the conversation surface entirely to the shared frame", () => {
    const { container } = renderConstellation(
      buildState({ surface: "conversation" }),
    );
    expect(container).toBeEmptyDOMElement();
    const source = readFileSync(
      "src/live-concepts/constellation/ConstellationRenderer.tsx",
      "utf8",
    );
    expect(source).not.toMatch(/ConversationView|conversationSkin/);
  });
});

/* --------------------------------- sessions -------------------------------- */

describe("Constellation sessions surface", () => {
  it("exposes concise landmarks and title-based roster actions", () => {
    const { container } = renderConstellation(
      buildState({ surface: "sessions" }),
    );
    expect(
      screen.getByRole("region", {
        name: "Constellation sessions",
      }),
    ).toBeVisible();
    expect(
      screen.getByRole("button", {
        name: "Open Wire handshake; status attention",
      }),
    ).toBeVisible();
    expect(
      screen.getByRole("status", {
        name: "Constellation sessions; 3 sessions; complete list",
      }),
    ).toBeVisible();
    expect(container.querySelector("[aria-label*='session-a']")).toBeNull();
    expect(container.querySelector("[aria-label*='sha256:']")).toBeNull();
  });

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
    for (const surface of ["sessions", "work"] as const) {
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
