import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { forwardRef, type ReactNode, useImperativeHandle, useRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { __resetSheetHistory, __sheetHistorySettled } from "../../ui/Sheet";
import { ConversationFrame } from "./ConversationFrame";
import type {
  ConversationFrameProps,
  ConversationFrameState,
  ConversationSkin,
} from "./contract";
import type { ConversationFrameAction } from "./primitives";

const transcript = vi.hoisted(() => ({
  props: null as null | Record<string, unknown>,
  anchor: {
    threadKey: "thread-display-key",
    itemKey: "failure-item",
    offsetPx: -11,
    following: false,
  },
  events: [] as string[],
  restoreAnchor: vi.fn<(anchor: unknown) => Promise<void>>(async () => {}),
  focusFeed: vi.fn(),
}));

vi.mock("./VirtualTranscript", () => ({
  VirtualTranscript: forwardRef(function MockVirtualTranscript(
    props: {
      readonly children?: ReactNode;
      readonly items?: readonly { readonly key: string }[];
      readonly renderItem?: (
        item: { readonly key: string },
        index: number,
        focused: boolean,
      ) => ReactNode;
      readonly locked?: boolean;
      readonly "data-frame-part"?: string;
    },
    ref,
  ) {
    transcript.props = props as Record<string, unknown>;
    const feedRef = useRef<HTMLDivElement>(null);
    useImperativeHandle(ref, () => ({
      captureAnchor() {
        transcript.events.push("capture");
        return transcript.anchor;
      },
      async restoreAnchor(anchor: unknown) {
        transcript.events.push("restore");
        await transcript.restoreAnchor(anchor);
      },
      scrollToTail() {},
      focusKey() {},
      focusFeed() {
        transcript.focusFeed();
        feedRef.current?.focus();
      },
    }));
    if (props.children !== undefined) {
      return (
        <section
          data-frame-part={props["data-frame-part"]}
          data-page-scroll-owner="true"
        >
          {props.children}
        </section>
      );
    }
    return (
      <div
        ref={feedRef}
        data-frame-part={props["data-frame-part"]}
        role="feed"
        aria-label="Conversation transcript"
        data-page-scroll-owner={props.locked ? undefined : "true"}
        data-scroll-locked={props.locked ? "true" : undefined}
        aria-hidden={props.locked ? "true" : undefined}
        inert={props.locked ? true : undefined}
        tabIndex={-1}
      >
        {props.items?.map((item, index) => (
          <div key={item.key}>{props.renderItem?.(item, index, false)}</div>
        ))}
      </div>
    );
  }),
}));

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

afterEach(async () => {
  cleanup();
  await __sheetHistorySettled();
  __resetSheetHistory();
  history.replaceState(null, "");
  transcript.props = null;
  transcript.events = [];
  transcript.restoreAnchor.mockReset();
  transcript.restoreAnchor.mockResolvedValue();
  transcript.focusFeed.mockClear();
});

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

  it("keeps question controls editable after a terminal mutation failure", () => {
    const conversation = readyState().conversation;
    if (conversation === null) throw new Error("fixture");
    const frameProps = props({
      conversation: {
        ...conversation,
        items: [
          {
            key: "failed-question-item",
            sourceKind: "question",
            body: bounded("Recover question"),
            label: bounded("Question"),
            tone: "attention",
            streaming: false,
            questionKey: "failed-question",
            evidenceKey: null,
            sequence: "failed-1",
          },
        ],
        questions: [
          {
            key: "failed-question",
            header: bounded("Header"),
            prompt: bounded("Choose again"),
            options: [
              {
                key: "recover-option",
                label: bounded("Recover"),
                detail: bounded("Try again"),
              },
            ],
            multiple: false,
            why: null,
            ifUnanswered: null,
          },
        ],
      },
      composer: {
        ...readyState().composer,
        pending: {
          kind: "send",
          status: "failed",
          draftSnapshot: "failed draft",
          generation: 12,
        },
        error: bounded("Mutation failed"),
      },
    });
    render(<ConversationFrame {...frameProps} />);
    expect(screen.getByRole("radio", { name: "Recover" })).toBeEnabled();
    expect(screen.getByRole("textbox", { name: "Note" })).toBeEnabled();
    fireEvent.click(screen.getByRole("radio", { name: "Recover" }));
    expect(frameProps.dispatch).toHaveBeenCalledWith({
      type: "setQuestionDraft",
      key: "failed-question",
      value: {
        selectedOptionKeys: ["recover-option"],
        note: "",
        resolution: null,
      },
    });
  });

  it("announces streaming only at start, phrase boundaries, and completion", () => {
    const conversation = readyState().conversation;
    if (conversation === null) throw new Error("fixture");
    const assistant = (body: string, streaming: boolean) => ({
      key: "assistant-stream",
      sourceKind: "assistant" as const,
      body: bounded(body),
      label: null,
      tone: "running" as const,
      streaming,
      questionKey: null,
      evidenceKey: null,
      sequence: "stream-1",
    });
    const stateWith = (body: string, streaming: boolean) =>
      readyState({
        conversation: {
          ...conversation,
          items: [assistant(body, streaming)],
        },
      });
    const frameProps = props();
    const { rerender } = render(
      <ConversationFrame {...frameProps} state={stateWith("Hello", false)} />,
    );
    expect(screen.queryByRole("status")).toBeNull();

    rerender(
      <ConversationFrame {...frameProps} state={stateWith("Hello", true)} />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      "Assistant message streaming",
    );

    rerender(
      <ConversationFrame
        {...frameProps}
        state={stateWith("Hello world", true)}
      />,
    );
    expect(screen.queryByRole("status")).toBeNull();

    rerender(
      <ConversationFrame
        {...frameProps}
        state={stateWith("Hello world.", true)}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Hello world.");

    rerender(
      <ConversationFrame
        {...frameProps}
        state={stateWith("Hello world.", false)}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      "Assistant message completed",
    );
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

  it("activates the exact measured transcript path and opens only bounded evidence by opaque key", async () => {
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
    frameProps.dispatch.mockImplementation((action) => {
      transcript.events.push(`dispatch:${action.type}`);
    });
    const { rerender } = render(<ConversationFrame {...frameProps} />);
    expect(transcript.props).toEqual(
      expect.objectContaining({
        threadKey: "thread-display-key",
        items: evidenceConversation.items,
        renderMode: "frame-owned",
      }),
    );
    expect(transcript.props).not.toHaveProperty("children");
    expect(screen.queryByText("Redacted body")).toBeNull();
    const trigger = screen.getByRole("button", { name: "Show evidence" });
    trigger.focus();
    fireEvent.click(trigger);
    expect(frameProps.dispatch).toHaveBeenCalledWith({
      type: "openEvidence",
      evidenceKey: "opaque-evidence-key",
      triggerKey: "failure-item",
    });
    expect(transcript.events.slice(0, 2)).toEqual([
      "capture",
      "dispatch:openEvidence",
    ]);
    rerender(
      <ConversationFrame
        {...frameProps}
        state={{
          ...frameProps.state,
          openEvidenceKey: "opaque-evidence-key",
          evidenceTriggerKey: "failure-item",
        }}
      />,
    );
    const dialog = screen.getByRole("dialog", { name: "Bounded evidence" });
    expect(dialog).toHaveTextContent("Redacted body");
    const lockedMain = screen.getByRole("main", { hidden: true });
    expect(lockedMain).toHaveAttribute("aria-label", "Conversation");
    expect(lockedMain).toHaveAttribute("aria-hidden", "true");
    expect(
      document.querySelectorAll('[data-page-scroll-owner="true"]'),
    ).toHaveLength(1);
    const lockedFeed = lockedMain.querySelector<HTMLElement>('[role="feed"]');
    expect(lockedFeed).not.toBeNull();
    expect(lockedFeed).toHaveAttribute("aria-label", "Conversation transcript");
    expect(lockedFeed).toHaveAttribute("data-scroll-locked", "true");

    fireEvent.click(
      within(dialog).getByRole("button", {
        name: "Close activity and evidence",
      }),
    );
    await waitFor(() =>
      expect(frameProps.dispatch).toHaveBeenCalledWith({
        type: "closeEvidence",
      }),
    );
    expect(transcript.events).toContain("restore");
    rerender(<ConversationFrame {...frameProps} />);
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Show evidence" }),
      ).toHaveFocus(),
    );
  });

  it("uses exact activity controls and never discloses hidden prelude or orphan evidence", () => {
    const conversation = readyState().conversation;
    if (conversation === null) throw new Error("fixture");
    const hidden = ["HIDDEN", "SYSTEM", "PRELUDE", "SOURCE"].join("_");
    render(
      <ConversationFrame
        {...props({
          conversation: {
            ...conversation,
            items: [
              {
                key: "safe-activity",
                sourceKind: "tool",
                semanticKind: "tool",
                label: bounded("Tool completed"),
                preview: null,
                duration: bounded("1 second"),
                tone: "success",
                state: "completed",
                evidenceKey: "safe-activity-evidence",
                sequence: "safe",
              },
              {
                key: "hidden-prelude",
                sourceKind: "system",
                semanticKind: "system-context",
                label: bounded("Context available"),
                preview: null,
                duration: null,
                tone: "idle",
                state: "unavailable",
                evidenceKey: null,
                sequence: "hidden",
              },
            ],
            evidence: [
              {
                key: "safe-activity-evidence",
                family: "tool",
                title: bounded("Tool activity"),
                sections: [
                  { heading: bounded("Result"), body: bounded("Safe result") },
                ],
                redacted: false,
              },
              {
                key: "orphan-hidden-evidence",
                family: "system",
                title: bounded(hidden),
                sections: [
                  { heading: bounded("Hidden"), body: bounded(hidden) },
                ],
                redacted: true,
              },
            ],
          },
        })}
      />,
    );

    expect(screen.getByRole("button", { name: "Show activity" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Show evidence" })).toBeNull();
    expect(document.body.textContent).not.toContain(hidden);
    expect(document.body.innerHTML).not.toContain(hidden);
    expect(
      screen.getByText("Context available").closest("article"),
    ).not.toHaveAccessibleDescription(hidden);
  });

  it("closes stale evidence keys without title or source fallback", () => {
    const frameProps = props({
      openEvidenceKey: "stale-evidence-key",
      evidenceTriggerKey: "user-1",
    });
    render(<ConversationFrame {...frameProps} />);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(frameProps.dispatch).toHaveBeenCalledWith({ type: "closeEvidence" });
  });

  it("restores the measured anchor then focuses the feed when the trigger is evicted", async () => {
    const conversation = readyState().conversation;
    if (conversation === null) throw new Error("fixture");
    const evidenceConversation = {
      ...conversation,
      items: [
        {
          key: "evicted-marker",
          sourceKind: "tool" as const,
          semanticKind: "tool" as const,
          label: bounded("Evictable tool"),
          preview: null,
          duration: null,
          tone: "success" as const,
          state: "completed" as const,
          evidenceKey: "evicted-evidence",
          sequence: "evict",
        },
      ],
      evidence: [
        {
          key: "evicted-evidence",
          family: "tool" as const,
          title: bounded("Evicted evidence"),
          sections: [{ heading: bounded("Result"), body: bounded("Done") }],
          redacted: false,
        },
      ],
    };
    const frameProps = props({ conversation: evidenceConversation });
    const view = render(<ConversationFrame {...frameProps} />);
    fireEvent.click(screen.getByRole("button", { name: "Show activity" }));
    view.rerender(
      <ConversationFrame
        {...frameProps}
        state={{
          ...frameProps.state,
          conversation: { ...evidenceConversation, items: [] },
          openEvidenceKey: "evicted-evidence",
          evidenceTriggerKey: "evicted-marker",
        }}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Close activity and evidence" }),
    );
    await waitFor(() =>
      expect(frameProps.dispatch).toHaveBeenCalledWith({
        type: "closeEvidence",
      }),
    );
    view.rerender(
      <ConversationFrame
        {...frameProps}
        state={{
          ...frameProps.state,
          conversation: { ...evidenceConversation, items: [] },
          openEvidenceKey: null,
          evidenceTriggerKey: null,
        }}
      />,
    );
    await waitFor(() => expect(transcript.focusFeed).toHaveBeenCalled());
    expect(
      screen.getByRole("feed", { name: "Conversation transcript" }),
    ).toHaveFocus();
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
