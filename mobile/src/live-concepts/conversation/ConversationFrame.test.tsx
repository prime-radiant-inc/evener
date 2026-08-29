import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConversationFrame } from "./ConversationFrame";
import type {
  ConversationFrameProps,
  ConversationFrameState,
  ConversationSkin,
} from "./contract";
import type { ConversationFrameAction } from "./primitives";

const bounded = (text: string) => ({
  text,
  truncated: false,
  originalUtf8Bytes: text.length,
});

const skin: ConversationSkin = {
  id: "stillwater",
  className: "plain-test-skin",
  composerAppearance: { density: "comfortable", accent: "forest" },
  renderNarrativeItem: ({ item, body }) => (
    <div data-skin-item={item.key}>{body}</div>
  ),
  renderActivityMarker: ({ item }) => <span>{item.label.text}</span>,
  renderConversationChrome: ({ title, status }) => (
    <div>
      <strong>{title.text}</strong>
      <span>{status.text}</span>
    </div>
  ),
};

function readyState(
  overrides: Partial<ConversationFrameState> = {},
): ConversationFrameState {
  return {
    concept: "stillwater",
    platform: "ios",
    appearance: "system",
    contentSize: "large",
    reducedMotion: false,
    phase: "ready",
    connection: { status: "connected" },
    conversation: {
      threadKey: "thread-display-key",
      title: bounded("Conversation title"),
      project: bounded("Project"),
      status: bounded("Running"),
      items: [
        {
          key: "user-1",
          sourceKind: "user",
          body: bounded("Hello"),
          label: null,
          tone: "idle",
          streaming: false,
          questionKey: null,
          evidenceKey: null,
          sequence: "1",
        },
      ],
      evidence: [],
      questions: [],
      olderAvailable: false,
      tone: "running",
      updatedLabel: null,
    },
    composer: {
      draft: "",
      canSend: true,
      canSteer: true,
      canQueue: true,
      canInterrupt: true,
      pending: null,
      accepted: null,
      error: null,
    },
    composerMode: "send",
    questionDrafts: {},
    anchor: null,
    focusedItemKey: null,
    unseen: 0,
    openEvidenceKey: null,
    evidenceTriggerKey: null,
    ...overrides,
  };
}

function props(overrides: Partial<ConversationFrameState> = {}) {
  return {
    state: readyState(overrides),
    skin,
    dispatch: vi.fn<(action: ConversationFrameAction) => void>(),
    onAnchorChange: vi.fn(),
    onUnseenChange: vi.fn(),
    onFocusIntentChange: vi.fn(),
  } satisfies ConversationFrameProps;
}

afterEach(cleanup);

describe("ConversationFrame", () => {
  it("owns the stable direct frame composition and the only page scroll owner", () => {
    const frame = render(<ConversationFrame {...props()} />);
    const root = frame.getByRole("main", { name: "Conversation" });
    expect(
      [...root.children].map((node) => node.getAttribute("data-frame-part")),
    ).toEqual(["chrome", "transcript", "status", "composer"]);
    expect(
      root.querySelectorAll("[data-page-scroll-owner='true']"),
    ).toHaveLength(1);
    expect(frame.getByRole("textbox", { name: "Message" })).toBeVisible();
    expect(frame.getByRole("button", { name: "Submit message" })).toBeVisible();
    expect(frame.getByRole("button", { name: "Back" })).toBeVisible();
    expect(frame.getByRole("button", { name: "Work" })).toBeVisible();
    expect(frame.getByRole("button", { name: "Switch concept" })).toBeVisible();
  });

  it.each([
    ["loading", "Loading conversation", true],
    ["empty", "No conversation", false],
  ] as const)(
    "renders %s state without inventing data",
    (phase, label, hasConversation) => {
      const state = readyState({
        phase,
        conversation: hasConversation ? readyState().conversation : null,
      });
      render(<ConversationFrame {...props()} state={state} />);
      expect(screen.getByText(label)).toBeVisible();
      expect(screen.getByRole("textbox", { name: "Message" })).toBeDisabled();
    },
  );

  it("keeps last-good transcript editable while offline and exposes read retry", () => {
    const frameProps = props({
      phase: "read-error",
      connection: { status: "offline" },
      composer: {
        ...readyState().composer,
        draft: "exact restored draft",
        error: bounded("Unable to refresh conversation"),
      },
    });
    render(<ConversationFrame {...frameProps} />);
    expect(screen.getByText("Hello")).toBeVisible();
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue(
      "exact restored draft",
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Unable to refresh conversation",
    );
    fireEvent.click(screen.getByRole("button", { name: "Retry conversation" }));
    expect(frameProps.dispatch).toHaveBeenCalledWith({ type: "retryRead" });
  });

  it("focuses retry when a read error has no last-good data", () => {
    render(
      <ConversationFrame
        {...props({ phase: "read-error", conversation: null })}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Retry conversation" }),
    ).toHaveFocus();
  });

  it("announces pending, accepted, and failed mutations with one actionable alert", () => {
    const { rerender } = render(
      <ConversationFrame
        {...props({
          composer: {
            ...readyState().composer,
            pending: {
              kind: "send",
              status: "pending",
              draftSnapshot: "sent",
              generation: 1,
            },
          },
        })}
      />,
    );
    expect(screen.getByRole("status", { name: "Send pending" })).toBeVisible();
    rerender(
      <ConversationFrame
        {...props({
          composer: {
            ...readyState().composer,
            accepted: { kind: "queue", disposition: "replayed" },
          },
        })}
      />,
    );
    expect(
      screen.getByRole("status", { name: "Queue replayed by Hub" }),
    ).toBeVisible();
    rerender(
      <ConversationFrame
        {...props({
          composer: {
            ...readyState().composer,
            pending: {
              kind: "steer",
              status: "failed",
              draftSnapshot: "restore",
              generation: 2,
            },
            error: bounded("Mutation failed"),
          },
        })}
      />,
    );
    expect(screen.getAllByRole("alert")).toHaveLength(1);
    expect(screen.getByRole("alert")).toHaveTextContent("Mutation failed");
  });

  it("renders a bounded malformed-question fallback", () => {
    const conversation = readyState().conversation;
    if (conversation === null) throw new Error("fixture");
    render(
      <ConversationFrame
        {...props({
          conversation: {
            ...conversation,
            items: [
              {
                key: "bad-question",
                sourceKind: "question",
                body: bounded("Question body"),
                label: bounded("Question"),
                tone: "attention",
                streaming: false,
                questionKey: null,
                evidenceKey: null,
                sequence: "2",
              },
            ],
          },
        })}
      />,
    );
    expect(
      screen.getByRole("alert", { name: "Question unavailable" }),
    ).toBeVisible();
  });

  it("owns question drafts and dispatches a full draft before the one submit intent", () => {
    const conversation = readyState().conversation;
    if (conversation === null) throw new Error("fixture");
    const frameProps = props({
      conversation: {
        ...conversation,
        items: [
          {
            key: "question-item",
            sourceKind: "question",
            body: bounded("Choose"),
            label: bounded("Question"),
            tone: "attention",
            streaming: false,
            questionKey: "question-1",
            evidenceKey: null,
            sequence: "3",
          },
        ],
        questions: [
          {
            key: "question-1",
            header: bounded("Header"),
            prompt: bounded("Pick options"),
            options: [
              { key: "one", label: bounded("One"), detail: bounded("First") },
              { key: "two", label: bounded("Two"), detail: bounded("Second") },
            ],
            multiple: true,
            why: null,
            ifUnanswered: bounded("Fallback"),
          },
        ],
      },
      questionDrafts: {
        "question-1": {
          selectedOptionKeys: ["one"],
          note: "because",
          resolution: null,
        },
      },
    });
    render(<ConversationFrame {...frameProps} />);
    fireEvent.click(screen.getByRole("checkbox", { name: "Two" }));
    expect(frameProps.dispatch).toHaveBeenCalledWith({
      type: "setQuestionDraft",
      key: "question-1",
      value: {
        selectedOptionKeys: ["one", "two"],
        note: "because",
        resolution: null,
      },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Note" }), {
      target: { value: "updated" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Submit answer" }));
    expect(frameProps.dispatch).toHaveBeenNthCalledWith(3, {
      type: "setQuestionDraft",
      key: "question-1",
      value: {
        selectedOptionKeys: ["one"],
        note: "because",
        resolution: "answer",
      },
    });
    expect(frameProps.dispatch).toHaveBeenNthCalledWith(4, {
      type: "submitQuestion",
      key: "question-1",
    });
    for (const [name, resolution] of [
      ["Use fallback", "fallback"],
      ["Let Evener decide", "decide"],
      ["Skip question", "skip"],
    ] as const) {
      frameProps.dispatch.mockClear();
      fireEvent.click(screen.getByRole("button", { name }));
      expect(frameProps.dispatch).toHaveBeenNthCalledWith(1, {
        type: "setQuestionDraft",
        key: "question-1",
        value: { selectedOptionKeys: ["one"], note: "because", resolution },
      });
      expect(frameProps.dispatch).toHaveBeenNthCalledWith(2, {
        type: "submitQuestion",
        key: "question-1",
      });
    }
  });

  it("replaces a single-option selection instead of accumulating display labels", () => {
    const conversation = readyState().conversation;
    if (conversation === null) throw new Error("fixture");
    const frameProps = props({
      conversation: {
        ...conversation,
        items: [
          {
            key: "single-question-item",
            sourceKind: "question",
            body: bounded("Choose one"),
            label: bounded("Question"),
            tone: "attention",
            streaming: false,
            questionKey: "single-question",
            evidenceKey: null,
            sequence: "4",
          },
        ],
        questions: [
          {
            key: "single-question",
            header: bounded("Header"),
            prompt: bounded("Pick one"),
            options: [
              {
                key: "opaque-a",
                label: bounded("Display A"),
                detail: bounded("First"),
              },
              {
                key: "opaque-b",
                label: bounded("Display B"),
                detail: bounded("Second"),
              },
            ],
            multiple: false,
            why: null,
            ifUnanswered: null,
          },
        ],
      },
      questionDrafts: {
        "single-question": {
          selectedOptionKeys: ["opaque-a"],
          note: "",
          resolution: null,
        },
      },
    });
    render(<ConversationFrame {...frameProps} />);
    fireEvent.click(screen.getByRole("radio", { name: "Display B" }));
    expect(frameProps.dispatch).toHaveBeenCalledWith({
      type: "setQuestionDraft",
      key: "single-question",
      value: {
        selectedOptionKeys: ["opaque-b"],
        note: "",
        resolution: null,
      },
    });
  });

  it("dispatches only opaque evidence keys and renders bounded evidence", () => {
    const conversation = readyState().conversation;
    if (conversation === null) throw new Error("fixture");
    const evidenceConversation = {
      ...conversation,
      items: [
        {
          key: "failure-item",
          sourceKind: "failure" as const,
          body: bounded("Bounded failure"),
          label: bounded("Failure"),
          tone: "failed" as const,
          streaming: false,
          questionKey: null,
          evidenceKey: "opaque-evidence-key",
          sequence: "5",
        },
      ],
      evidence: [
        {
          key: "opaque-evidence-key",
          family: "failure" as const,
          title: bounded("Bounded evidence"),
          sections: [
            { heading: bounded("Detail"), body: bounded("Redacted body") },
          ],
          redacted: true,
        },
      ],
    };
    const frameProps = props({ conversation: evidenceConversation });
    const { rerender } = render(<ConversationFrame {...frameProps} />);
    fireEvent.click(screen.getByRole("button", { name: "Open evidence" }));
    expect(frameProps.dispatch).toHaveBeenCalledWith({
      type: "openEvidence",
      evidenceKey: "opaque-evidence-key",
      triggerKey: "failure-item",
    });
    rerender(
      <ConversationFrame
        {...frameProps}
        state={{
          ...frameProps.state,
          openEvidenceKey: "opaque-evidence-key",
        }}
      />,
    );
    expect(
      screen.getByRole("dialog", { name: "Bounded evidence" }),
    ).toHaveTextContent("Redacted body");
    fireEvent.click(screen.getByRole("button", { name: "Close evidence" }));
    expect(frameProps.dispatch).toHaveBeenCalledWith({ type: "closeEvidence" });
  });

  it("renders resolved questions without mutation controls", () => {
    const conversation = readyState().conversation;
    if (conversation === null) throw new Error("fixture");
    render(
      <ConversationFrame
        {...props({
          conversation: {
            ...conversation,
            items: [
              {
                key: "q",
                sourceKind: "question",
                body: bounded("Body"),
                label: bounded("Question"),
                tone: "idle",
                streaming: false,
                questionKey: "q1",
                evidenceKey: null,
                sequence: "1",
              },
            ],
            questions: [
              {
                key: "q1",
                header: bounded("H"),
                prompt: bounded("Resolved prompt"),
                options: [],
                multiple: false,
                why: null,
                ifUnanswered: null,
              },
            ],
          },
          questionDrafts: {
            q1: { selectedOptionKeys: [], note: "", resolution: "decide" },
          },
        })}
      />,
    );
    expect(
      screen.getByRole("status", { name: "Question resolved" }),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "Submit answer" })).toBeNull();
  });

  it("keeps the conversation package one-way and neutral", () => {
    const directory = __dirname;
    for (const name of readdirSync(directory).filter(
      (entry) => /\.(ts|tsx)$/u.test(entry) && !entry.endsWith(".test.tsx"),
    )) {
      const source = readFileSync(path.join(directory, name), "utf8");
      expect(source, name).not.toMatch(/from\s+["']\.\.\/contract["']/u);
      expect(source, name).not.toMatch(/\bLiveConcept(State|Intent)\b/u);
    }
    const parent = readFileSync(
      path.join(directory, "..", "contract.ts"),
      "utf8",
    );
    expect(parent).toContain('from "./conversation/contract"');
    expect(parent).toContain('from "./conversation/primitives"');
  });
});
