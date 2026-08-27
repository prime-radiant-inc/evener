// Focused surface tests for the live Field Notes concept. Covers the three
// milestone surfaces (sessions, conversation, work) plus the chronology rail,
// stable item sequence markers (adapter sequenceLabel, never index-derived),
// user/assistant margin labels, current-record state, work-ledger
// annotations, full questions, composer capability gating, interrupt,
// streaming/truncated markers, olderAvailable, roster status/error states,
// and the concept-switch trigger on every surface. Dates/times come from
// live display values (roster updatedLabel / conversation updatedLabel),
// never hardcoded fixture chapter/date text. Behavioral assertions only —
// no source-self-inspection.

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
  QuestionDraft,
} from "../contract";
import type {
  DisplayTone,
  LiveActivityView,
  LiveConceptSurface,
  LiveConnectionView,
  LiveConversationView,
  LiveQuestionView,
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

function transcriptItem(
  overrides: Partial<LiveTranscriptItem> & {
    key: string;
    kind: LiveTranscriptItem["kind"];
  },
): LiveTranscriptItem {
  return {
    label: "Item",
    body: "Body text.",
    tone: "idle",
    streaming: false,
    truncated: false,
    questionKey: null,
    sequenceLabel: "seq-1",
    ...overrides,
  };
}

function conversationWithItems(
  overrides: Partial<LiveConversationView> = {},
): LiveConversationView {
  const items: readonly LiveTranscriptItem[] = [
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
    title: "Compile typed modules",
    project: "evener-core",
    status: "Running",
    items,
    questions: [],
    olderAvailable: false,
    tone: "running" satisfies DisplayTone,
    updatedLabel: "12 minutes ago",
    ...overrides,
  };
}

function conversationWithQuestion(): LiveConversationView {
  const question: LiveQuestionView = {
    key: "q-1",
    header: "Permission",
    prompt: "Run the build now?",
    options: [
      { key: "opt-yes", label: "Yes", detail: "Build immediately" },
      { key: "opt-no", label: "No", detail: "Wait for review" },
    ],
    multiple: false,
  };
  const items: readonly LiveTranscriptItem[] = [
    transcriptItem({
      key: "q-item-1",
      kind: "question",
      label: "Permission",
      body: "Run the build now?",
      tone: "attention",
      questionKey: "q-1",
      sequenceLabel: "seq-Q1",
    }),
  ];
  return conversationWithItems({
    threadKey: "session-q",
    title: "Awaiting permission",
    project: "evener-core",
    status: "Needs answer",
    items,
    questions: [question],
    tone: "attention",
    updatedLabel: "just now",
  });
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
// Conversation surface — chronology rail, stable markers, margin labels,
// current-record, streaming/truncated, olderAvailable, composer, interrupt
// ---------------------------------------------------------------------------

describe("Field Notes conversation surface", () => {
  it("keeps transcript items in stable DOM order using adapter sequenceLabel markers", () => {
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
    // Markers show the adapter sequenceLabel verbatim, never an index-derived
    // chapter number.
    for (const [index, marker] of markers.entries()) {
      const expected = ["seq-A1", "seq-A2", "seq-A3"][index] ?? "";
      expect(marker).toHaveTextContent(expected);
      expect(marker).not.toHaveTextContent(/Record \d/);
      expect(marker).not.toHaveTextContent(/Chapter \d/);
    }
    expect(main.querySelector("[data-chronology-rail]")).toBeVisible();
  });

  it("uses authoritative updatedLabel for the chronology stamp when non-null", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems({ updatedLabel: "12 minutes ago" }),
      }),
    );
    const main = mainFor("conversation");
    const markers = [...main.querySelectorAll("[data-chronology-marker]")];
    for (const marker of markers) {
      expect(marker).toHaveTextContent("12 minutes ago");
    }
    expect(main).not.toHaveTextContent("Monday, 24 August");
  });

  it("falls back to an honest Live record label when updatedLabel is null", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems({ updatedLabel: null }),
      }),
    );
    const main = mainFor("conversation");
    const markers = [...main.querySelectorAll("[data-chronology-marker]")];
    expect(markers.length).toBeGreaterThan(0);
    for (const marker of markers) {
      expect(marker).toHaveTextContent("Live record");
    }
    // Must not fabricate a timestamp.
    expect(main).not.toHaveTextContent("12 minutes ago");
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

  it("renders streaming and truncated markers on transcript items", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems({
          items: [
            transcriptItem({
              key: "s-1",
              kind: "assistant",
              label: "Assistant",
              body: "Working…",
              tone: "running",
              streaming: true,
              truncated: true,
              sequenceLabel: "seq-S1",
            }),
          ],
        }),
      }),
    );
    const main = mainFor("conversation");
    const item = main.querySelector('[data-transcript-item-id="s-1"]');
    expect(item).toHaveAttribute("data-streaming", "true");
    expect(item).toHaveAttribute("data-truncated", "true");
  });

  it("renders an older-available affordance when olderAvailable is true", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems({ olderAvailable: true }),
      }),
    );
    expect(mainFor("conversation")).toHaveTextContent(/older records/i);
  });

  it("does not render an older-available affordance when olderAvailable is false", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems({ olderAvailable: false }),
      }),
    );
    expect(mainFor("conversation")).not.toHaveTextContent(/older records/i);
  });

  it("uses conversation.tone for the summary status label, never hardcoded", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems({ tone: "attention" }),
      }),
    );
    const main = mainFor("conversation");
    const summary = main.querySelector("[data-conversation-tone]");
    expect(summary).toHaveAttribute("data-conversation-tone", "attention");
    // The StatusLabel inside the summary reflects the tone via its marker.
    expect(summary?.querySelector("[data-status-state]")).toHaveAttribute(
      "data-status-state",
      "attention",
    );
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
    fireEvent.click(
      within(mainFor("conversation")).getByRole("button", {
        name: "read_file",
      }),
    );
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

  it("submits the composer draft through submit with the selected mode", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
        composer: { ...baseComposer(), draft: "Steer toward tests" },
        ui: {
          concept: "field-notes",
          workOpen: false,
          composerMode: "steer",
          expandedToolKeys: new Set(),
          expandedWorkKeys: new Set(),
          questionDrafts: {},
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
      dispatch,
    );
    fireEvent.click(screen.getByRole("button", { name: "Submit message" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "submit", mode: "steer" });
  });

  it("mode buttons dispatch setComposerMode and reflect aria-pressed", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
        ui: {
          concept: "field-notes",
          workOpen: false,
          composerMode: "send",
          expandedToolKeys: new Set(),
          expandedWorkKeys: new Set(),
          questionDrafts: {},
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
      dispatch,
    );
    const steer = screen.getByRole("button", { name: /^Steer$/i });
    expect(steer).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(steer);
    expect(dispatch).toHaveBeenCalledWith({
      type: "setComposerMode",
      mode: "steer",
    });
  });

  it("gates each mode button by its capability and disables while pending", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
        composer: {
          ...baseComposer(),
          canSend: true,
          canSteer: false,
          canQueue: true,
          pending: null,
        },
      }),
    );
    expect(screen.getByRole("button", { name: /^Send$/i })).toBeEnabled();
    expect(screen.getByRole("button", { name: /^Steer$/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /^Queue$/i })).toBeEnabled();
  });

  it("disables mode buttons while a mutation is pending", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
        composer: {
          ...baseComposer(),
          pending: {
            kind: "send",
            status: "pending",
            draftSnapshot: null,
            generation: 1,
          },
        },
      }),
    );
    for (const mode of ["Send", "Steer", "Queue"] as const) {
      expect(
        screen.getByRole("button", { name: new RegExp(`^${mode}$`, "i") }),
      ).toBeDisabled();
    }
  });

  it("keeps the textarea enabled when any mode is available and no pending", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
        composer: {
          ...baseComposer(),
          canSend: false,
          canSteer: true,
          canQueue: false,
          pending: null,
        },
      }),
    );
    expect(screen.getByRole("textbox", { name: "Message" })).toBeEnabled();
  });

  it("disables the textarea and submit while a mutation is pending", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
        composer: {
          ...baseComposer(),
          draft: "draft",
          pending: {
            kind: "send",
            status: "pending",
            draftSnapshot: null,
            generation: 1,
          },
        },
      }),
    );
    expect(screen.getByRole("textbox", { name: "Message" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Submit message" }),
    ).toBeDisabled();
  });

  it("renders a visible pending status while a mutation is pending", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
        composer: {
          ...baseComposer(),
          pending: {
            kind: "send",
            status: "pending",
            draftSnapshot: null,
            generation: 1,
          },
        },
      }),
    );
    expect(
      mainFor("conversation").querySelector("[data-composer-pending]"),
    ).toBeVisible();
  });

  it("renders a visible composer error from the live composer view", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
        composer: { ...baseComposer(), error: "Send rejected by Hub" },
      }),
    );
    const main = mainFor("conversation");
    expect(main.querySelector("[data-composer-error]")).toBeVisible();
    expect(main).toHaveTextContent("Send rejected by Hub");
  });

  it("renders an interrupt control gated by canInterrupt", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
        composer: { ...baseComposer(), canInterrupt: true },
      }),
      dispatch,
    );
    const interrupt = screen.getByRole("button", { name: /interrupt/i });
    expect(interrupt).toBeEnabled();
    fireEvent.click(interrupt);
    expect(dispatch).toHaveBeenCalledWith({ type: "interrupt" });
  });

  it("disables the interrupt control when canInterrupt is false", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems(),
        composer: { ...baseComposer(), canInterrupt: false },
      }),
    );
    expect(screen.getByRole("button", { name: /interrupt/i })).toBeDisabled();
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
// Questions — full question card via questionKey/options/notes/validity
// ---------------------------------------------------------------------------

describe("Field Notes conversation questions", () => {
  it("renders a question card linked by item.questionKey with options", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithQuestion(),
      }),
    );
    const main = mainFor("conversation");
    const card = main.querySelector('[data-question-id="q-1"]');
    expect(card).toBeVisible();
    expect(card).toHaveTextContent("Run the build now?");
    expect(within(card as HTMLElement).getByText("Yes")).toBeVisible();
    expect(within(card as HTMLElement).getByText("No")).toBeVisible();
  });

  it("dispatches setQuestionDraft when an option is selected", () => {
    const dispatch = vi.fn();
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithQuestion(),
      }),
      dispatch,
    );
    const yes = screen.getByRole("radio", { name: "Yes" });
    fireEvent.click(yes);
    expect(dispatch).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "setQuestionDraft",
        key: "q-1",
      }),
    );
  });

  it("dispatches submitQuestion when the submit answer button is pressed", () => {
    const dispatch = vi.fn();
    const draft: QuestionDraft = {
      selectedOptionKeys: ["opt-yes"],
      note: "",
      resolution: null,
    };
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithQuestion(),
        ui: {
          concept: "field-notes",
          workOpen: false,
          composerMode: "send",
          expandedToolKeys: new Set(),
          expandedWorkKeys: new Set(),
          questionDrafts: { "q-1": draft },
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
      dispatch,
    );
    fireEvent.click(screen.getByRole("button", { name: /submit answer/i }));
    expect(dispatch).toHaveBeenCalledWith({
      type: "submitQuestion",
      key: "q-1",
    });
  });

  it("gates submit answer on validity (no selection disables submit)", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithQuestion(),
      }),
    );
    expect(
      screen.getByRole("button", { name: /submit answer/i }),
    ).toBeDisabled();
  });

  it("renders a note field bound to the question draft", () => {
    const draft: QuestionDraft = {
      selectedOptionKeys: [],
      note: "consider ci",
      resolution: null,
    };
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithQuestion(),
        ui: {
          concept: "field-notes",
          workOpen: false,
          composerMode: "send",
          expandedToolKeys: new Set(),
          expandedWorkKeys: new Set(),
          questionDrafts: { "q-1": draft },
          focusedItemKey: null,
          scrollAnchors: {},
        },
      }),
    );
    expect(screen.getByRole("textbox", { name: "Note" })).toHaveValue(
      "consider ci",
    );
  });

  it("shows a visible missing-link notice when a question item has no matching question", () => {
    renderState(
      baseState({
        surface: "conversation",
        conversation: conversationWithItems({
          items: [
            transcriptItem({
              key: "orphan-q",
              kind: "question",
              label: "Orphan",
              body: "No question view for this item.",
              tone: "attention",
              questionKey: "missing-q",
              sequenceLabel: "seq-ORPHAN",
            }),
          ],
          questions: [],
        }),
      }),
    );
    const main = mainFor("conversation");
    expect(main).toHaveTextContent(/question unavailable/i);
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
