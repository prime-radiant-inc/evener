// Focused surface tests for the live Field Notes concept. Covers the three
// milestone surfaces (sessions, conversation, work) plus the chronology rail,
// stable item sequence markers, user/assistant margin labels, current-record
// state, and work-ledger annotations. Dates/times come from live display
// values (roster updatedLabel / conversation status), never hardcoded
// fixture chapter/date text.

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
  DisplayTone,
  LiveActivityView,
  LiveConceptSurface,
  LiveConnectionView,
  LiveConversationView,
  LiveRosterView,
  LiveTranscriptItem,
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
    textScale: "standard",
    reducedMotion: false,
    surface: "sessions",
    connection: { status: "connected" } satisfies LiveConnectionView,
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

function conversationWithItems(): LiveConversationView {
  const items: readonly LiveTranscriptItem[] = [
    {
      key: "item-1",
      kind: "user",
      label: "Your note",
      body: "Open the reviewed workbook.",
      tone: "idle",
      streaming: false,
      truncated: false,
    },
    {
      key: "item-2",
      kind: "assistant",
      label: "Assistant",
      body: "Reading the live record now.",
      tone: "running",
      streaming: true,
      truncated: false,
    },
    {
      key: "item-3",
      kind: "tool",
      label: "read_file",
      body: "Read 24 lines from renderer.",
      tone: "running",
      streaming: false,
      truncated: false,
    },
  ];
  return {
    threadKey: "session-b",
    title: "Compile typed modules",
    project: "evener-core",
    status: "Running · updated 12 minutes ago",
    items,
    questions: [],
    olderAvailable: false,
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

// ---------------------------------------------------------------------------
// Module contract
// ---------------------------------------------------------------------------

describe("Field Notes live module contract", () => {
  it("exposes the exact live module label and id", () => {
    expect(fieldNotesModule.id).toBe("field-notes");
    expect(fieldNotesModule.label).toBe("Field Notes");
  });

  it("renders the concept root with live platform and appearance", () => {
    renderState(baseState({ platform: "android", appearance: "dark" }));
    const root = document.querySelector("[data-concept-root]");
    expect(root).toHaveAttribute("data-platform", "android");
    expect(root).toHaveAttribute("data-appearance", "dark");
  });

  it("carries no prototype navigation, lab, or synthetic controls", () => {
    renderState(baseState({ surface: "sessions" }));
    expect(screen.queryByRole("navigation", { name: "Primary" })).toBeNull();
    expect(screen.queryByText(/lab controls/i)).toBeNull();
    expect(screen.queryByText(/synthetic/i)).toBeNull();
    expect(screen.queryByTestId("lab-controls")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Sessions surface
// ---------------------------------------------------------------------------

describe("Field Notes sessions surface", () => {
  it("renders roster groups and rows from the live contract", () => {
    renderState(baseState({ surface: "sessions", roster: rosterWithRows() }));
    const main = mainFor("sessions");
    const group = main.querySelector('[data-session-group-id="needsYou"]');
    expect(group).toBeVisible();
    expect(
      within(group as HTMLElement).getByText("Refactor renderer module"),
    ).toBeVisible();
    expect(
      within(group as HTMLElement).getByText("evener-mobile"),
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
    const attentionRow = main.querySelector('[data-current-state="attention"]');
    expect(attentionRow).toBeVisible();
    const runningRow = main.querySelector('[data-current-state="running"]');
    expect(runningRow).toBeVisible();
  });

  it("dispatches setRosterQuery and refreshRoster through the live intent", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({ surface: "sessions", roster: rosterWithRows() }),
      dispatch,
    );
    const filter = screen.getByRole("searchbox", { name: "Filter sessions" });
    fireEvent.change(filter, { target: { value: "evener" } });
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
    const main = mainFor("sessions");
    expect(main.querySelector("[data-connection-status]")).toHaveAttribute(
      "data-connection-status",
      "offline",
    );
  });
});

// ---------------------------------------------------------------------------
// Conversation surface — chronology rail, stable markers, margin labels
// ---------------------------------------------------------------------------

describe("Field Notes conversation surface", () => {
  it("keeps transcript items in stable chronological DOM order with sequence markers", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
      }),
    );
    const main = mainFor("conversation");
    const items = [...main.querySelectorAll("[data-transcript-item-id]")];
    expect(
      items.map((el) => el.getAttribute("data-transcript-item-id")),
    ).toEqual(["item-1", "item-2", "item-3"]);
    const markers = [...main.querySelectorAll("[data-chronology-marker]")];
    expect(markers).toHaveLength(3);
    for (const [index, marker] of markers.entries()) {
      expect(marker).toHaveTextContent(
        `Record ${String(index + 1).padStart(2, "0")}`,
      );
    }
    expect(main.querySelector("[data-chronology-rail]")).toBeVisible();
  });

  it("derives the chronology stamp from the live conversation status, not a fixture date", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
      }),
    );
    const main = mainFor("conversation");
    const markers = [...main.querySelectorAll("[data-chronology-marker]")];
    expect(markers.length).toBeGreaterThan(0);
    for (const marker of markers) {
      expect(marker).toHaveTextContent("Running · updated 12 minutes ago");
    }
    expect(main).not.toHaveTextContent("Monday, 24 August");
  });

  it("renders user and assistant margin labels", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
      }),
    );
    const main = mainFor("conversation");
    expect(main.querySelectorAll("[data-margin-label='user']")).toHaveLength(1);
    expect(
      main.querySelectorAll("[data-margin-label='assistant']"),
    ).toHaveLength(1);
    expect(main.querySelector("[data-margin-label='user']")).toHaveTextContent(
      "Your note",
    );
    expect(
      main.querySelector("[data-margin-label='assistant']"),
    ).toHaveTextContent("Assistant · response");
  });

  it("marks the running tool as the current-record item", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
      }),
    );
    const main = mainFor("conversation");
    const current = main.querySelector('[data-current-record="true"]');
    expect(current).toBeVisible();
    expect(current).toHaveAttribute("data-transcript-item-id", "item-3");
  });

  it("toggles a tool disclosure through the live toggleTool intent", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
      }),
      dispatch,
    );
    const main = mainFor("conversation");
    const toolButton = within(main).getByRole("button", { name: "read_file" });
    fireEvent.click(toolButton);
    expect(dispatch).toHaveBeenCalledWith({
      type: "toggleTool",
      key: "item-3",
    });
  });

  it("dispatches openWork from the conversation session actions", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
      }),
      dispatch,
    );
    fireEvent.click(screen.getByRole("button", { name: /work/i }));
    expect(dispatch).toHaveBeenCalledWith({ type: "openWork" });
  });

  it("submits the composer draft through the live submit intent", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
        composer: { ...baseComposer(), draft: "Steer toward tests" },
      }),
      dispatch,
    );
    fireEvent.click(screen.getByRole("button", { name: "Submit message" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "submit", mode: "send" });
  });

  it("renders a conversation summary header with the live title and project", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
      }),
    );
    const main = mainFor("conversation");
    expect(main).toHaveTextContent("Compile typed modules");
    expect(main).toHaveTextContent("evener-core");
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
    expect(child).toBeVisible();
    expect(child).toHaveAttribute("data-work-parent-id", "task-1");
    expect(child?.querySelector("[data-ledger-annotation]")).toBeVisible();
    // The nested child lives inside the parent's list subtree.
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
    const main = mainFor("work");
    const current = main.querySelector('[data-current-work="true"]');
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
    const main = mainFor("work");
    const button = within(main).getByRole("button", {
      name: "Compile typed modules",
    });
    fireEvent.click(button);
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
// Heading outline coherence across all three surfaces
// ---------------------------------------------------------------------------

describe("Field Notes heading outline", () => {
  it.each([
    ["sessions", baseState({ surface: "sessions", roster: rosterWithRows() })],
    [
      "conversation",
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
      }),
    ],
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
