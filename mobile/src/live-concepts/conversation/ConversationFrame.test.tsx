import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import {
  act,
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
    onExternalLink: vi.fn<(url: string) => Promise<void>>(async () => {}),
    onAnchorChange: vi.fn(),
    onUnseenChange: vi.fn(),
    onFocusIntentChange: vi.fn(),
  } satisfies ConversationFrameProps;
}

function deferredOperation(): {
  readonly promise: Promise<void>;
  readonly resolve: () => void;
  readonly reject: (reason: unknown) => void;
} {
  let resolvePromise: (() => void) | null = null;
  let rejectPromise: ((reason: unknown) => void) | null = null;
  const promise = new Promise<void>((resolve, reject) => {
    resolvePromise = resolve;
    rejectPromise = reject;
  });
  return {
    promise,
    resolve: () => resolvePromise?.(),
    reject: (reason) => rejectPromise?.(reason),
  };
}

function evidenceConversation(
  definitions: ReadonlyArray<{
    readonly itemKey: string;
    readonly evidenceKey: string;
    readonly title: string;
    readonly body: string;
  }>,
) {
  const conversation = readyState().conversation;
  if (conversation === null) throw new Error("fixture");
  return {
    ...conversation,
    items: definitions.map((definition, index) => ({
      key: definition.itemKey,
      sourceKind: "failure" as const,
      body: bounded(`Failure ${index + 1}`),
      label: bounded("Failure"),
      tone: "failed" as const,
      streaming: false,
      questionKey: null,
      evidenceKey: definition.evidenceKey,
      sequence: `evidence-${index + 1}`,
    })),
    evidence: definitions.map((definition) => ({
      key: definition.evidenceKey,
      family: "failure" as const,
      title: bounded(definition.title),
      sections: [
        { heading: bounded("Detail"), body: bounded(definition.body) },
      ],
      redacted: true,
    })),
  };
}

function deferredRestore(): {
  readonly promise: Promise<void>;
  readonly resolve: () => void;
} {
  let resolvePromise: (() => void) | null = null;
  const promise = new Promise<void>((resolve) => {
    resolvePromise = resolve;
  });
  return {
    promise,
    resolve() {
      resolvePromise?.();
    },
  };
}

function evidenceTrigger(itemKey: string): HTMLButtonElement {
  const trigger = document.querySelector<HTMLButtonElement>(
    `button[data-evidence-trigger-key="${itemKey}"]`,
  );
  if (trigger === null) throw new Error(`missing trigger ${itemKey}`);
  return trigger;
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
    const frameProps = props();
    const frame = render(<ConversationFrame {...frameProps} />);
    const root = frame.getByRole("main", { name: "Conversation" });
    expect(root).toHaveAttribute("data-concept-root", "true");
    expect(root).toHaveAttribute("data-surface", "conversation");
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
    expect(frame.getByRole("button", { name: "Voice" })).toBeVisible();
    expect(frame.getByRole("button", { name: "Switch concept" })).toBeVisible();
    fireEvent.click(frame.getByRole("button", { name: "Voice" }));
    expect(frameProps.dispatch).toHaveBeenCalledWith({ type: "openVoice" });
  });

  it("owns assistant Markdown and link routing while keeping other narrative plain", () => {
    const conversation = readyState().conversation;
    if (conversation === null) throw new Error("fixture");
    const frameProps = props({
      conversation: {
        ...conversation,
        items: [
          {
            key: "assistant-link",
            sourceKind: "assistant",
            body: bounded(
              "**Safe** [docs](https://example.com/path) ![image](https://example.com/x.png)",
            ),
            label: null,
            tone: "idle",
            streaming: false,
            questionKey: null,
            evidenceKey: null,
            sequence: "1",
          },
          {
            key: "user-plain",
            sourceKind: "user",
            body: bounded("[not a link](https://example.com/private)"),
            label: null,
            tone: "idle",
            streaming: false,
            questionKey: null,
            evidenceKey: null,
            sequence: "2",
          },
        ],
      },
    });
    render(<ConversationFrame {...frameProps} />);
    expect(screen.getByText("Safe").tagName).toBe("STRONG");
    expect(document.querySelector("img")).toBeNull();
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(
      screen.getByText("[not a link](https://example.com/private)"),
    ).toBeVisible();
    fireEvent.click(screen.getByRole("link", { name: "docs" }));
    expect(frameProps.onExternalLink).toHaveBeenCalledWith(
      "https://example.com/path",
    );
  });

  it("shows only a fixed current link failure and clears it on the next attempt", async () => {
    const conversation = readyState().conversation;
    if (conversation === null) throw new Error("fixture");
    const stale = deferredOperation();
    const current = deferredOperation();
    const failed = deferredOperation();
    const onExternalLink = vi
      .fn<(url: string) => Promise<void>>()
      .mockReturnValueOnce(stale.promise)
      .mockReturnValueOnce(current.promise)
      .mockReturnValueOnce(failed.promise)
      .mockResolvedValueOnce();
    const frameProps = {
      ...props({
        conversation: {
          ...conversation,
          items: [
            {
              key: "assistant-races",
              sourceKind: "assistant" as const,
              body: bounded(
                "[first](https://example.com/private?token=first) [second](https://example.com/second)",
              ),
              label: null,
              tone: "idle" as const,
              streaming: false,
              questionKey: null,
              evidenceKey: null,
              sequence: "1",
            },
          ],
        },
      }),
      onExternalLink,
    };
    render(<ConversationFrame {...frameProps} />);
    fireEvent.click(screen.getByRole("link", { name: "first" }));
    fireEvent.click(screen.getByRole("link", { name: "second" }));
    await act(async () => current.resolve());
    await act(async () =>
      stale.reject(
        new Error("https://example.com/private?token=must-not-render"),
      ),
    );
    expect(screen.queryByText("Unable to open link")).toBeNull();

    fireEvent.click(screen.getByRole("link", { name: "first" }));
    await act(async () =>
      failed.reject(
        new Error("https://example.com/private?token=must-not-render"),
      ),
    );
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Unable to open link",
      ),
    );
    expect(screen.getByRole("alert")).not.toHaveTextContent("example.com");
    fireEvent.click(screen.getByRole("link", { name: "second" }));
    expect(screen.queryByText("Unable to open link")).toBeNull();
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
      type: "toggleTool",
      key: "opaque-evidence-key",
    });
    expect(frameProps.dispatch).toHaveBeenCalledWith({
      type: "openEvidence",
      evidenceKey: "opaque-evidence-key",
      triggerKey: "failure-item",
    });
    expect(transcript.events.slice(0, 3)).toEqual([
      "capture",
      "dispatch:toggleTool",
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
    expect(
      frameProps.dispatch.mock.calls.filter(
        ([action]) =>
          action.type === "toggleTool" && action.key === "opaque-evidence-key",
      ),
    ).toHaveLength(2);
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

  it("routes an initially stale evidence key through measured restoration before close", async () => {
    const restoration = deferredRestore();
    transcript.restoreAnchor.mockReturnValueOnce(restoration.promise);
    const frameProps = props({
      openEvidenceKey: "stale-evidence-key",
      evidenceTriggerKey: "user-1",
    });
    render(<ConversationFrame {...frameProps} />);
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() =>
      expect(transcript.restoreAnchor).toHaveBeenCalledTimes(1),
    );
    expect(frameProps.dispatch).not.toHaveBeenCalledWith({
      type: "closeEvidence",
    });
    restoration.resolve();
    await waitFor(() =>
      expect(frameProps.dispatch).toHaveBeenCalledWith({
        type: "closeEvidence",
      }),
    );
  });

  it.each([
    {
      name: "evidence eviction",
      mutate: (state: ConversationFrameState) => ({
        ...state,
        conversation:
          state.conversation === null
            ? null
            : { ...state.conversation, evidence: [] },
      }),
    },
    {
      name: "trigger mismatch",
      mutate: (state: ConversationFrameState) => ({
        ...state,
        evidenceTriggerKey: "different-trigger",
      }),
    },
    {
      name: "stale key",
      mutate: (state: ConversationFrameState) => ({
        ...state,
        openEvidenceKey: "stale-key-while-open",
      }),
    },
    {
      name: "replacement while open",
      mutate: (state: ConversationFrameState) => ({
        ...state,
        conversation:
          state.conversation === null
            ? null
            : {
                ...state.conversation,
                evidence: [
                  {
                    key: "replacement-key",
                    family: "failure" as const,
                    title: bounded("Replacement must not flash"),
                    sections: [
                      {
                        heading: bounded("Replacement"),
                        body: bounded("Replacement detail must not flash"),
                      },
                    ],
                    redacted: true,
                  },
                ],
              },
      }),
    },
  ])(
    "restores before publishing close for $name and never flashes fallback detail",
    async ({ mutate }) => {
      const conversation = evidenceConversation([
        {
          itemKey: "invalidated-item",
          evidenceKey: "invalidated-evidence",
          title: "Current bounded evidence",
          body: "Current bounded detail",
        },
      ]);
      const frameProps = props({ conversation });
      frameProps.dispatch.mockImplementation((action) => {
        transcript.events.push(`dispatch:${action.type}`);
      });
      const view = render(<ConversationFrame {...frameProps} />);
      fireEvent.click(evidenceTrigger("invalidated-item"));
      const openState: ConversationFrameState = {
        ...frameProps.state,
        openEvidenceKey: "invalidated-evidence",
        evidenceTriggerKey: "invalidated-item",
      };
      view.rerender(<ConversationFrame {...frameProps} state={openState} />);
      expect(screen.getByText("Current bounded detail")).toBeVisible();

      const restoration = deferredRestore();
      transcript.restoreAnchor.mockReturnValueOnce(restoration.promise);
      view.rerender(
        <ConversationFrame {...frameProps} state={mutate(openState)} />,
      );
      await waitFor(() =>
        expect(transcript.restoreAnchor).toHaveBeenCalledTimes(1),
      );
      expect(screen.queryByText("Current bounded detail")).toBeNull();
      expect(
        screen.queryByText("Replacement detail must not flash"),
      ).toBeNull();
      expect(frameProps.dispatch).not.toHaveBeenCalledWith({
        type: "closeEvidence",
      });
      expect(transcript.focusFeed).not.toHaveBeenCalled();
      expect(screen.getByRole("main", { hidden: true })).toHaveAttribute(
        "inert",
      );

      restoration.resolve();
      await waitFor(() =>
        expect(frameProps.dispatch).toHaveBeenCalledWith({
          type: "closeEvidence",
        }),
      );
      expect(transcript.events.indexOf("restore")).toBeLessThan(
        transcript.events.indexOf("dispatch:closeEvidence"),
      );
    },
  );

  it("closes safely after a rejected restore, clears the gate, and never focuses the stale trigger", async () => {
    const conversation = evidenceConversation([
      {
        itemKey: "rejected-item",
        evidenceKey: "rejected-evidence",
        title: "Rejected restore evidence",
        body: "Rejected restore detail",
      },
    ]);
    const frameProps = props({ conversation });
    frameProps.dispatch.mockImplementation((action) => {
      transcript.events.push(`dispatch:${action.type}`);
    });
    const view = render(<ConversationFrame {...frameProps} />);
    const trigger = evidenceTrigger("rejected-item");
    fireEvent.click(trigger);
    const openState: ConversationFrameState = {
      ...frameProps.state,
      openEvidenceKey: "rejected-evidence",
      evidenceTriggerKey: "rejected-item",
    };
    view.rerender(<ConversationFrame {...frameProps} state={openState} />);
    transcript.restoreAnchor.mockRejectedValueOnce(new Error("restore failed"));

    fireEvent.click(
      screen.getByRole("button", { name: "Close activity and evidence" }),
    );
    await waitFor(() =>
      expect(frameProps.dispatch).toHaveBeenCalledWith({
        type: "closeEvidence",
      }),
    );
    expect(transcript.events.indexOf("restore")).toBeLessThan(
      transcript.events.indexOf("dispatch:closeEvidence"),
    );
    view.rerender(<ConversationFrame {...frameProps} />);
    await waitFor(() => expect(transcript.focusFeed).toHaveBeenCalledTimes(1));
    expect(trigger).not.toHaveFocus();
    await __sheetHistorySettled();

    transcript.restoreAnchor.mockResolvedValueOnce();
    fireEvent.click(evidenceTrigger("rejected-item"));
    view.rerender(<ConversationFrame {...frameProps} state={openState} />);
    fireEvent.click(
      screen.getByRole("button", { name: "Close activity and evidence" }),
    );
    await waitFor(() =>
      expect(transcript.restoreAnchor).toHaveBeenCalledTimes(2),
    );
  });

  it("ignores a late close restore after a newer evidence item opens", async () => {
    const conversation = evidenceConversation([
      {
        itemKey: "older-item",
        evidenceKey: "older-evidence",
        title: "Older evidence",
        body: "Older detail",
      },
      {
        itemKey: "newer-item",
        evidenceKey: "newer-evidence",
        title: "Newer evidence",
        body: "Newer detail",
      },
    ]);
    const frameProps = props({ conversation });
    const view = render(<ConversationFrame {...frameProps} />);
    fireEvent.click(evidenceTrigger("older-item"));
    view.rerender(
      <ConversationFrame
        {...frameProps}
        state={{
          ...frameProps.state,
          openEvidenceKey: "older-evidence",
          evidenceTriggerKey: "older-item",
        }}
      />,
    );
    const olderRestore = deferredRestore();
    transcript.restoreAnchor.mockReturnValueOnce(olderRestore.promise);
    fireEvent.click(
      screen.getByRole("button", { name: "Close activity and evidence" }),
    );
    await waitFor(() =>
      expect(transcript.restoreAnchor).toHaveBeenCalledTimes(1),
    );

    fireEvent.click(evidenceTrigger("newer-item"));
    view.rerender(
      <ConversationFrame
        {...frameProps}
        state={{
          ...frameProps.state,
          openEvidenceKey: "newer-evidence",
          evidenceTriggerKey: "newer-item",
        }}
      />,
    );
    await __sheetHistorySettled();
    expect(await screen.findByText("Newer detail")).toBeVisible();
    frameProps.dispatch.mockClear();
    olderRestore.resolve();
    await act(async () => {});
    expect(frameProps.dispatch).not.toHaveBeenCalledWith({
      type: "closeEvidence",
    });
    expect(screen.getByText("Newer detail")).toBeVisible();
  });

  it("does not dispatch or focus after unmount while restoration is pending", async () => {
    const conversation = evidenceConversation([
      {
        itemKey: "unmounted-item",
        evidenceKey: "unmounted-evidence",
        title: "Unmounted evidence",
        body: "Unmounted detail",
      },
    ]);
    const frameProps = props({ conversation });
    const view = render(<ConversationFrame {...frameProps} />);
    fireEvent.click(evidenceTrigger("unmounted-item"));
    view.rerender(
      <ConversationFrame
        {...frameProps}
        state={{
          ...frameProps.state,
          openEvidenceKey: "unmounted-evidence",
          evidenceTriggerKey: "unmounted-item",
        }}
      />,
    );
    const restoration = deferredRestore();
    transcript.restoreAnchor.mockReturnValueOnce(restoration.promise);
    fireEvent.click(
      screen.getByRole("button", { name: "Close activity and evidence" }),
    );
    await waitFor(() =>
      expect(transcript.restoreAnchor).toHaveBeenCalledTimes(1),
    );
    frameProps.dispatch.mockClear();
    view.unmount();

    restoration.resolve();
    await act(async () => {});
    expect(frameProps.dispatch).not.toHaveBeenCalled();
    expect(transcript.focusFeed).not.toHaveBeenCalled();
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
