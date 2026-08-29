// Focused tests for the Field Notes-owned Sessions and Work surfaces.
// Conversation structure and behavior belong to ConversationFrame; this file
// only proves the renderer leaves that surface empty for the host-owned frame.

import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  LiveComposerView,
  LiveConceptIntent,
  LiveConceptState,
} from "../contract";
import type {
  BoundedDisplayText,
  ConversationDisplayItem,
  DisplayTone,
  LiveActivityView,
  LiveConceptSurface,
  LiveConnectionView,
  LiveConversationView,
  LiveRosterView,
  LiveWorkItem,
} from "../model";
import { fieldNotesModule } from "./index";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  delete document.documentElement.dataset.reducedMotion;
});

// ---------------------------------------------------------------------------
// Live state builders. No fixture/scenario/synthetic infrastructure — only
// the live contract model. Display labels are live values supplied by the host.
// ---------------------------------------------------------------------------

function baseState(
  overrides: Partial<LiveConceptState> = {},
): LiveConceptState {
  return {
    concept: "field-notes",
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
    } satisfies LiveConnectionView,
    roster: emptyRoster(),
    conversation: null,
    activity: null,
    composer: baseComposer(),
    ui: {
      concept: "field-notes",
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

function emptyRoster(): LiveRosterView {
  return {
    status: "ready",
    query: "",
    groups: [],
    hasMore: false,
    error: null,
  };
}

function baseComposer(): LiveComposerView {
  return {
    draft: "",
    canSend: true,
    canSteer: true,
    canQueue: true,
    canInterrupt: false,
    pending: null,
    accepted: null,
    error: null,
  };
}

function rosterWithRows(): LiveRosterView {
  return {
    status: "ready",
    query: "",
    groups: [
      {
        id: "needsYou",
        label: "Needs you",
        rows: [
          {
            key: "session-a",
            title: "Refactor renderer module",
            project: "evener-mobile",
            summary: "Live conversation awaiting a steer.",
            updatedLabel: "3 minutes ago",
            tone: "attention" satisfies DisplayTone,
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
            title: "Compile typed modules",
            project: "evener-core",
            summary: "Agent is building the work tree.",
            updatedLabel: "12 minutes ago",
            tone: "running" satisfies DisplayTone,
            connectedWorkCount: 4,
          },
        ],
      },
    ],
    hasMore: false,
    error: null,
  };
}

function bounded(text: string, truncated = false): BoundedDisplayText {
  return {
    text,
    truncated,
    originalUtf8Bytes: new TextEncoder().encode(text).length,
  };
}

interface TranscriptFixture {
  key: string;
  kind: "user" | "assistant" | "tool" | "question";
  label?: string;
  body?: string;
  tone?: DisplayTone;
  streaming?: boolean;
  truncated?: boolean;
  questionKey?: string | null;
  sequenceLabel?: string;
}

function transcriptItem(overrides: TranscriptFixture): ConversationDisplayItem {
  const label = overrides.label ?? "Item";
  const body = overrides.body ?? "Body text.";
  const tone = overrides.tone ?? "idle";
  const sequence = overrides.sequenceLabel ?? "seq-1";
  if (overrides.kind === "tool") {
    return {
      key: overrides.key,
      sourceKind: "tool",
      semanticKind: "tool",
      label: bounded(label),
      preview: bounded(body, overrides.truncated),
      duration: null,
      tone,
      state: tone === "running" ? "running" : "completed",
      evidenceKey: null,
      sequence,
    };
  }
  return {
    key: overrides.key,
    sourceKind: overrides.kind,
    label: bounded(label),
    body: bounded(body, overrides.truncated),
    tone,
    streaming: overrides.streaming ?? false,
    questionKey: overrides.questionKey ?? null,
    evidenceKey: null,
    sequence,
  };
}

function conversationWithItems(
  overrides: Partial<LiveConversationView> = {},
): LiveConversationView {
  const items: readonly ConversationDisplayItem[] = [
    transcriptItem({
      key: "item-1",
      kind: "user",
      label: "Your note",
      body: "Open the reviewed workbook.",
      tone: "idle",
      sequenceLabel: "seq-A1",
    }),
    transcriptItem({
      key: "item-2",
      kind: "assistant",
      label: "Assistant",
      body: "Reading the live record now.",
      tone: "running",
      streaming: true,
      sequenceLabel: "seq-A2",
    }),
    transcriptItem({
      key: "item-3",
      kind: "tool",
      label: "read_file",
      body: "Read 24 lines from renderer.",
      tone: "running",
      sequenceLabel: "seq-A3",
    }),
  ];
  return {
    threadKey: "session-b",
    title: bounded("Compile typed modules"),
    project: bounded("evener-core"),
    status: bounded("Running"),
    items,
    evidence: [],
    questions: [],
    olderAvailable: false,
    tone: "running" satisfies DisplayTone,
    updatedLabel: bounded("12 minutes ago"),
    ...overrides,
  };
}

function activityWithWorkTree(): LiveActivityView {
  const work: readonly LiveWorkItem[] = [
    {
      key: "task-1",
      kind: "task",
      title: "Compile typed modules",
      detail: "go build ./...",
      tone: "running",
      children: [
        {
          key: "job-1",
          kind: "job",
          title: "Vet packages",
          detail: "go vet",
          tone: "success",
          children: [],
        },
      ],
    },
    {
      key: "delegate-1",
      kind: "delegate",
      title: "Review renderer",
      detail: "Audit chronology rail.",
      tone: "idle",
      children: [],
    },
  ];
  return {
    tasks: [
      { status: "active", count: 1 },
      { status: "open", count: 2 },
      { status: "done", count: 5 },
    ],
    work,
    usage: {
      totalTokens: 12_400,
      cost: "$0.04",
      contextPressure: 0.43,
      durationMs: 184_000,
    },
  };
}

function renderState(
  state: LiveConceptState,
  dispatch?: (i: LiveConceptIntent) => void,
) {
  const handler = dispatch ?? vi.fn();
  const Renderer = fieldNotesModule.Renderer;
  render(<Renderer state={state} dispatch={handler} />);
  return handler;
}

function mainFor(surface: LiveConceptSurface): HTMLElement {
  const main = screen.getByRole("main");
  expect(main).toHaveAttribute("data-surface", surface);
  return main;
}

function conceptRoot(): HTMLElement {
  const root = document.querySelector("[data-concept-root]");
  if (!root) throw new Error("Missing concept root");
  return root as HTMLElement;
}

// ---------------------------------------------------------------------------
// Module contract
// ---------------------------------------------------------------------------

describe("Field Notes live module contract", () => {
  it("exposes the exact live module label and id", () => {
    expect(fieldNotesModule.id).toBe("field-notes");
    expect(fieldNotesModule.label).toBe("Field Notes");
  });

  it("leaves conversation rendering to the shared production frame", () => {
    const Renderer = fieldNotesModule.Renderer;
    const { container } = render(
      <Renderer
        state={baseState({
          surface: "conversation",
          conversation: conversationWithItems(),
        })}
        dispatch={vi.fn()}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("renders the concept root with live platform and appearance", () => {
    renderState(baseState({ platform: "android", appearance: "dark" }));
    const root = conceptRoot();
    expect(root).toHaveAttribute("data-platform", "android");
    expect(root).toHaveAttribute("data-appearance", "dark");
  });

  it("carries no prototype navigation, lab, or synthetic controls", () => {
    renderState(baseState({ surface: "sessions" }));
    expect(screen.queryByRole("navigation", { name: "Primary" })).toBeNull();
    expect(screen.queryByText(/lab controls/i)).toBeNull();
    expect(screen.queryByText(/synthetic/i)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Concept-switch trigger on every surface
// ---------------------------------------------------------------------------

describe("Field Notes concept-switch trigger", () => {
  it.each([
    ["sessions", baseState({ surface: "sessions", roster: rosterWithRows() })],
    [
      "work",
      baseState({
        surface: "work",
        activity: activityWithWorkTree(),
        conversation: conversationWithItems(),
      }),
    ],
  ] as const)("renders a concept-switch trigger on %s", (_surface, state) => {
    const dispatch = vi.fn();
    renderState(state, dispatch);
    const trigger = screen.getByRole("button", { name: /switch concept/i });
    expect(trigger).toBeVisible();
    fireEvent.click(trigger);
    expect(dispatch).toHaveBeenCalledWith({ type: "openConceptSwitcher" });
  });
});

// ---------------------------------------------------------------------------
// Sessions surface
// ---------------------------------------------------------------------------

describe("Field Notes sessions surface", () => {
  it("exposes concise landmarks and title-based roster actions", () => {
    renderState(baseState({ surface: "sessions", roster: rosterWithRows() }));
    const container = document.body;
    expect(
      screen.getByRole("region", {
        name: "Field Notes sessions",
      }),
    ).toBeVisible();
    expect(
      screen.getByRole("button", {
        name: "Open Refactor renderer module; status attention",
      }),
    ).toBeVisible();
    expect(
      screen.getByRole("status", {
        name: "Field Notes sessions; 2 sessions; complete list",
      }),
    ).toBeVisible();
    expect(container.querySelector("[aria-label*='session-a']")).toBeNull();
    expect(container.querySelector("[aria-label*='sha256:']")).toBeNull();
  });

  it("renders roster groups and rows from the live contract", () => {
    renderState(baseState({ surface: "sessions", roster: rosterWithRows() }));
    const main = mainFor("sessions");
    const group = main.querySelector('[data-session-group-id="needsYou"]');
    expect(group).toBeVisible();
    expect(
      within(group as HTMLElement).getByText("Refactor renderer module"),
    ).toBeVisible();
  });

  it("emits openConversation when a roster row is opened", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({ surface: "sessions", roster: rosterWithRows() }),
      dispatch,
    );
    fireEvent.click(screen.getByText("Refactor renderer module"));
    expect(dispatch).toHaveBeenCalledWith({
      type: "openConversation",
      key: "session-a",
    });
  });

  it("shows the live updatedLabel rather than a hardcoded chapter date", () => {
    renderState(baseState({ surface: "sessions", roster: rosterWithRows() }));
    const main = mainFor("sessions");
    expect(main).toHaveTextContent("3 minutes ago");
    expect(main).toHaveTextContent("12 minutes ago");
    expect(main).not.toHaveTextContent("Monday, 24 August");
  });

  it("marks current/attention rows with a current-record state marker", () => {
    renderState(baseState({ surface: "sessions", roster: rosterWithRows() }));
    const main = mainFor("sessions");
    expect(
      main.querySelector('[data-current-state="attention"]'),
    ).toBeVisible();
    expect(main.querySelector('[data-current-state="running"]')).toBeVisible();
  });

  it("dispatches setRosterQuery and refreshRoster through the live intent", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({ surface: "sessions", roster: rosterWithRows() }),
      dispatch,
    );
    fireEvent.change(
      screen.getByRole("searchbox", { name: "Filter sessions" }),
      {
        target: { value: "evener" },
      },
    );
    expect(dispatch).toHaveBeenCalledWith({
      type: "setRosterQuery",
      value: "evener",
    });
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "refreshRoster" });
  });

  it("renders a connection-aware banner from the live connection view", () => {
    renderState(
      baseState({
        surface: "sessions",
        connection: { status: "offline" },
        roster: rosterWithRows(),
      }),
    );
    expect(
      mainFor("sessions").querySelector("[data-connection-status]"),
    ).toHaveAttribute("data-connection-status", "offline");
  });

  it("renders roster loading state with a loading marker", () => {
    renderState(
      baseState({
        surface: "sessions",
        roster: { ...emptyRoster(), status: "loading" },
      }),
    );
    const main = mainFor("sessions");
    expect(main.querySelector("[data-roster-status]")).toHaveAttribute(
      "data-roster-status",
      "loading",
    );
  });

  it("renders roster idle state", () => {
    renderState(
      baseState({
        surface: "sessions",
        roster: { ...emptyRoster(), status: "idle" },
      }),
    );
    expect(
      mainFor("sessions").querySelector("[data-roster-status]"),
    ).toHaveAttribute("data-roster-status", "idle");
  });

  it("renders roster offline state", () => {
    renderState(
      baseState({
        surface: "sessions",
        connection: { status: "offline" },
        roster: { ...emptyRoster(), status: "offline" },
      }),
    );
    expect(
      mainFor("sessions").querySelector("[data-roster-status]"),
    ).toHaveAttribute("data-roster-status", "offline");
  });

  it("renders roster error state with the actual roster.error message", () => {
    renderState(
      baseState({
        surface: "sessions",
        roster: {
          ...emptyRoster(),
          status: "error",
          error: "Hub returned 503",
        },
      }),
    );
    const main = mainFor("sessions");
    expect(main.querySelector("[data-roster-status]")).toHaveAttribute(
      "data-roster-status",
      "error",
    );
    expect(main).toHaveTextContent("Hub returned 503");
  });

  it("renders an empty state when the roster is ready with no rows", () => {
    renderState(baseState({ surface: "sessions", roster: emptyRoster() }));
    const main = mainFor("sessions");
    expect(main).toHaveTextContent(/no matching records/i);
  });
});

// ---------------------------------------------------------------------------
// Work surface — work-ledger annotations and current-record work
// ---------------------------------------------------------------------------

describe("Field Notes work surface", () => {
  it("renders the work-ledger root and nested annotations", () => {
    renderState(
      baseState({
        surface: "work",
        activity: activityWithWorkTree(),
        conversation: conversationWithItems(),
      }),
    );
    const main = mainFor("work");
    expect(main.querySelector("ul[data-work-ledger]")).toBeVisible();
    const root = main.querySelector('[data-work-node-id="task-1"]');
    expect(root).toBeVisible();
    expect(root).toHaveAttribute("data-work-kind", "task");
    expect(root?.querySelector("[data-ledger-annotation]")).toBeVisible();
  });

  it("records parent ids and visible ledger annotations for nesting", () => {
    renderState(
      baseState({
        surface: "work",
        activity: activityWithWorkTree(),
        conversation: conversationWithItems(),
      }),
    );
    const main = mainFor("work");
    const child = main.querySelector('[data-work-node-id="job-1"]');
    expect(child).toHaveAttribute("data-work-parent-id", "task-1");
    expect(child?.querySelector("[data-ledger-annotation]")).toBeVisible();
    const parent = main.querySelector('[data-work-node-id="task-1"]');
    expect(child?.closest("li")?.parentElement?.closest("li")).toContainElement(
      parent as HTMLElement,
    );
  });

  it("marks the running work node as the current-record work item", () => {
    renderState(
      baseState({
        surface: "work",
        activity: activityWithWorkTree(),
        conversation: conversationWithItems(),
      }),
    );
    const current = mainFor("work").querySelector('[data-current-work="true"]');
    expect(current).toBeVisible();
    expect(current).toHaveAttribute("data-work-node-id", "task-1");
  });

  it("toggles a work node disclosure through the live toggleWork intent", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({
        surface: "work",
        activity: activityWithWorkTree(),
        conversation: conversationWithItems(),
      }),
      dispatch,
    );
    fireEvent.click(
      within(mainFor("work")).getByRole("button", {
        name: "Compile typed modules",
      }),
    );
    expect(dispatch).toHaveBeenCalledWith({
      type: "toggleWork",
      key: "task-1",
    });
  });

  it("renders live usage from the activity view, not a fixture", () => {
    renderState(
      baseState({
        surface: "work",
        activity: activityWithWorkTree(),
        conversation: conversationWithItems(),
      }),
    );
    const main = mainFor("work");
    expect(main).toHaveTextContent("12.4K tokens");
    expect(main).toHaveTextContent("$0.04");
  });

  it("dispatches closeWork to leave the work surface", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({
        surface: "work",
        activity: activityWithWorkTree(),
        conversation: conversationWithItems(),
      }),
      dispatch,
    );
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "closeWork" });
  });
});

// ---------------------------------------------------------------------------
// Heading outline coherence across owned surfaces
// ---------------------------------------------------------------------------

describe("Field Notes heading outline", () => {
  it.each([
    ["sessions", baseState({ surface: "sessions", roster: rosterWithRows() })],
    [
      "work",
      baseState({
        surface: "work",
        activity: activityWithWorkTree(),
        conversation: conversationWithItems(),
      }),
    ],
  ] as const)("forms one coherent heading outline on %s", (_surface, state) => {
    renderState(state);
    const main = screen.getByRole("main");
    const headings = within(main).getAllByRole("heading");
    const levels = headings.map((h) => Number(h.tagName.slice(1)));
    expect(levels[0]).toBe(1);
    for (const [index, level] of levels.entries()) {
      if (index === 0) continue;
      const previous = levels[index - 1];
      if (previous === undefined) throw new Error("Missing previous heading");
      expect(level).toBeLessThanOrEqual(previous + 1);
    }
    expect(main.querySelectorAll("h1")).toHaveLength(1);
  });
});
