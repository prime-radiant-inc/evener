// AskComposer component tests — structured ask_user question cards that
// replace the normal composer during askPending. Covers single/multi select,
// free text, notes, decide, fallback, skip, multiple questions, exact
// [answers] payload composition, in-flight state, settlement from another
// client, and conflict draft recovery. Uses fake stores so no wire is needed.

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { StoreApi, UseBoundStore } from "zustand";
import { create } from "zustand";
import type {
  InputItem,
  MutationReceipt,
} from "../../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  AskBatch,
  MobileCapabilities,
  MobileConversation,
  MobileTimelineItem,
} from "../../conversation/model";
import type { ConversationService } from "../../services/conversation";
import type { AttachmentState } from "../../state/attachments";
import type { ConversationState } from "../../state/conversation";
import { AskComposer } from "./AskComposer";

// --- fixtures ---------------------------------------------------------------

const ALL_TRUE_CAPS: MobileCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function makeReceipt(): MutationReceipt {
  return {
    clientMutationId: "cmid-1",
    disposition: "accepted",
    threadId: "thread-1",
    projectionState: "current",
  };
}

function question(
  key: string,
  over: Partial<{
    header: string;
    question: string;
    options: { label: string; detail: string; recommended?: boolean }[];
    multiSelect: boolean;
    why: string;
    ifUnanswered: string;
  }> = {},
): AskBatch["questions"][number] {
  return {
    key,
    header: "Deploy?",
    question: "Ship to production?",
    options: [
      { label: "Yes", detail: "deploy now", recommended: true },
      { label: "No", detail: "hold off" },
    ],
    multiSelect: false,
    ...over,
  };
}

function questionItem(id: string, batch: AskBatch): MobileTimelineItem {
  return { kind: "question", id, batch };
}

function makeConversation(
  items: MobileTimelineItem[] = [],
  over: Partial<MobileConversation> = {},
): MobileConversation {
  return {
    id: "thread-1",
    sessionId: "session-1",
    preview: "",
    modelProvider: "anthropic",
    status: "ready",
    items,
    capabilities: ALL_TRUE_CAPS,
    queue: { depth: 0, preview: [] },
    usage: {},
    askPending: true,
    ...over,
  };
}

function batch(questions: AskBatch["questions"]): AskBatch {
  return { callId: "call-1", questions };
}

// A fake ConversationService that records send calls.
class FakeConversationService implements ConversationService {
  sendCalls: InputItem[][] = [];
  sendShouldReject: Error | null = null;
  receipt = makeReceipt();

  async open(): Promise<MobileConversation> {
    return makeConversation();
  }
  async loadOlder(): Promise<{
    items: MobileTimelineItem[];
    nextCursor?: string;
  }> {
    return { items: [] };
  }
  subscribeNotifications(): () => void {
    return () => {};
  }
  async send(input: InputItem[]): Promise<MutationReceipt> {
    this.sendCalls.push(input);
    if (this.sendShouldReject) throw this.sendShouldReject;
    return this.receipt;
  }
  async steer(): Promise<MutationReceipt> {
    return this.receipt;
  }
  async queue(): Promise<MutationReceipt> {
    return this.receipt;
  }
  async interrupt(): Promise<MutationReceipt> {
    return this.receipt;
  }
  async compact(): Promise<void> {}
  async shutdown(): Promise<void> {}
  async changeModel(): Promise<void> {}
  async setReasoningEffort(): Promise<void> {}
  async rename(): Promise<void> {}
  async cancelQueued(): Promise<{
    removedText: string;
    removedImages?: number;
    receipt: MutationReceipt;
  }> {
    return { removedText: "", receipt: this.receipt };
  }
  close(): void {}
}

// --- fake stores ------------------------------------------------------------

type ConversationStoreHook = UseBoundStore<StoreApi<ConversationState>>;
type AttachmentStoreHook = UseBoundStore<StoreApi<AttachmentState>>;

function createFakeConversationStore(
  conversation: MobileConversation,
  over: Partial<ConversationState> = {},
): ConversationStoreHook {
  const send = vi.fn(
    async (_service: ConversationService, _input: InputItem[]) => {
      await _service.send(_input);
    },
  );
  const setDraft = vi.fn();
  return create<ConversationState>(() => ({
    ref: "ref-1",
    profileId: "p1",
    connectionGeneration: 0,
    conversationGeneration: 0,
    conversation,
    olderCursor: null,
    loadingOlder: false,
    status: "open",
    error: null,
    draft: "",
    pendingSend: null,
    open: vi.fn(),
    loadOlder: vi.fn(),
    setDraft,
    send,
    steer: vi.fn(),
    queue: vi.fn(),
    interrupt: vi.fn(),
    close: vi.fn(),
    applyNotification: vi.fn(),
    reset: vi.fn(),
    ...over,
  }));
}

function createFakeAttachmentStore(): AttachmentStoreHook {
  return create<AttachmentState>(() => ({
    attachments: [],
    error: null,
    add: vi.fn(),
    remove: vi.fn(),
    clear: vi.fn(),
    setError: vi.fn(),
  }));
}

afterEach(() => {
  cleanup();
});

// --- render helper -----------------------------------------------------------

function renderAsk(
  conv: MobileConversation,
  over: Partial<ConversationState> = {},
): {
  conversationStore: ConversationStoreHook;
  service: FakeConversationService;
  attachmentStore: AttachmentStoreHook;
} {
  const service = new FakeConversationService();
  const conversationStore = createFakeConversationStore(conv, over);
  const attachmentStore = createFakeAttachmentStore();
  render(
    <AskComposer
      conversationStore={conversationStore}
      conversationService={service}
      attachmentStore={attachmentStore}
    />,
  );
  return { conversationStore, service, attachmentStore };
}

// Extract the composed [answers] text from the first send call, safely
// handling noUncheckedIndexedAccess.
function sentText(service: FakeConversationService): string {
  const call = service.sendCalls[0];
  if (call === undefined) return "";
  const item = call[0] as { text?: string } | undefined;
  return item?.text ?? "";
}

// --- tests ------------------------------------------------------------------

describe("AskComposer — single select", () => {
  it("renders a question card with header, question, and options", () => {
    const item = questionItem("q1", batch([question("q1")]));
    renderAsk(makeConversation([item]));
    expect(screen.getByText("Deploy?")).toBeDefined();
    expect(screen.getByText("Ship to production?")).toBeDefined();
    expect(screen.getByText("Yes")).toBeDefined();
    expect(screen.getByText("No")).toBeDefined();
  });

  it("selecting an option and sending composes the [answers] payload", () => {
    const item = questionItem("q1", batch([question("q1")]));
    const { service } = renderAsk(makeConversation([item]));
    fireEvent.click(screen.getByText("Yes"));
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    expect(service.sendCalls).toHaveLength(1);
    const text = sentText(service);
    expect(text).toBe('[answers]\n1. [Deploy?] → "Yes"');
  });

  it("switching selection replaces the prior choice", () => {
    const item = questionItem("q1", batch([question("q1")]));
    const { service } = renderAsk(makeConversation([item]));
    fireEvent.click(screen.getByText("Yes"));
    fireEvent.click(screen.getByText("No"));
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe('[answers]\n1. [Deploy?] → "No"');
  });
});

describe("AskComposer — multi select", () => {
  it("allows multiple options to be selected in a multi-select question", () => {
    const q = question("q1", {
      multiSelect: true,
      options: [
        { label: "A", detail: "a" },
        { label: "B", detail: "b" },
        { label: "C", detail: "c" },
      ],
    });
    const item = questionItem("q1", batch([q]));
    const { service } = renderAsk(makeConversation([item]));
    fireEvent.click(screen.getByText("A"));
    fireEvent.click(screen.getByText("C"));
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe('[answers]\n1. [Deploy?] → "A", "C"');
  });

  it("deselects a previously selected option on second click", () => {
    const q = question("q1", {
      multiSelect: true,
      options: [
        { label: "A", detail: "a" },
        { label: "B", detail: "b" },
      ],
    });
    const item = questionItem("q1", batch([q]));
    const { service } = renderAsk(makeConversation([item]));
    fireEvent.click(screen.getByText("A"));
    fireEvent.click(screen.getByText("B"));
    fireEvent.click(screen.getByText("A"));
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe('[answers]\n1. [Deploy?] → "B"');
  });
});

describe("AskComposer — free text", () => {
  it("renders a free-text input when the question has no options", () => {
    const q: AskBatch["questions"][number] = {
      key: "q1",
      header: "Notes",
      question: "Anything to add?",
      options: [],
      multiSelect: false,
    };
    const item = questionItem("q1", batch([q]));
    renderAsk(makeConversation([item]));
    const field = screen.getByTestId("ask-free-text-q1") as HTMLTextAreaElement;
    expect(field).toBeDefined();
  });

  it("composes a free-text resolution", () => {
    const q: AskBatch["questions"][number] = {
      key: "q1",
      header: "Notes",
      question: "Anything to add?",
      options: [],
      multiSelect: false,
    };
    const item = questionItem("q1", batch([q]));
    const { service } = renderAsk(makeConversation([item]));
    const field = screen.getByTestId("ask-free-text-q1") as HTMLTextAreaElement;
    fireEvent.change(field, { target: { value: "ship it Friday" } });
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe('[answers]\n1. [Notes] → free text: "ship it Friday"');
  });
});

describe("AskComposer — decide", () => {
  it("renders a decide action and composes you decide without a leaning", () => {
    const q: AskBatch["questions"][number] = {
      key: "q1",
      header: "Choose",
      question: "Which path?",
      options: [
        { label: "A", detail: "a" },
        { label: "B", detail: "b" },
      ],
      multiSelect: false,
    };
    const item = questionItem("q1", batch([q]));
    const { service } = renderAsk(makeConversation([item]));
    fireEvent.click(screen.getByTestId("ask-decide-q1"));
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe("[answers]\n1. [Choose] → you decide");
  });

  it("composes you decide with a leaning when text is provided", () => {
    const q: AskBatch["questions"][number] = {
      key: "q1",
      header: "Choose",
      question: "Which path?",
      options: [{ label: "A", detail: "a" }],
      multiSelect: false,
    };
    const item = questionItem("q1", batch([q]));
    const { service } = renderAsk(makeConversation([item]));
    fireEvent.click(screen.getByTestId("ask-decide-q1"));
    const leaning = screen.getByTestId(
      "ask-decide-leaning-q1",
    ) as HTMLInputElement;
    fireEvent.change(leaning, { target: { value: "probably yes" } });
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe(
      '[answers]\n1. [Choose] → you decide — leaning: "probably yes"',
    );
  });
});

describe("AskComposer — fallback", () => {
  it("renders a fallback action when ifUnanswered is present and composes it", () => {
    const q: AskBatch["questions"][number] = {
      key: "q1",
      header: "Deploy?",
      question: "Ship it?",
      options: [{ label: "Yes", detail: "y" }],
      multiSelect: false,
      ifUnanswered: "assume yes",
    };
    const item = questionItem("q1", batch([q]));
    const { service } = renderAsk(makeConversation([item]));
    fireEvent.click(screen.getByTestId("ask-fallback-q1"));
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe(
      '[answers]\n1. [Deploy?] → do your stated fallback ("assume yes")',
    );
  });
});

describe("AskComposer — skip", () => {
  it("renders a skip action and composes skipped (no answer)", () => {
    const q: AskBatch["questions"][number] = {
      key: "q1",
      header: "Deploy?",
      question: "Ship it?",
      options: [{ label: "Yes", detail: "y" }],
      multiSelect: false,
    };
    const item = questionItem("q1", batch([q]));
    const { service } = renderAsk(makeConversation([item]));
    fireEvent.click(screen.getByTestId("ask-skip-q1"));
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe("[answers]\n1. [Deploy?] → skipped (no answer)");
  });
});

describe("AskComposer — notes", () => {
  it("attaches a note to a resolution, suffixed after a dash", () => {
    const item = questionItem("q1", batch([question("q1")]));
    const { service } = renderAsk(makeConversation([item]));
    fireEvent.click(screen.getByText("Yes"));
    const note = screen.getByTestId("ask-note-q1") as HTMLTextAreaElement;
    fireEvent.change(note, { target: { value: "please double check first" } });
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe(
      '[answers]\n1. [Deploy?] → "Yes" — note: "please double check first"',
    );
  });

  it("a whitespace-only note is treated as no note", () => {
    const item = questionItem("q1", batch([question("q1")]));
    const { service } = renderAsk(makeConversation([item]));
    fireEvent.click(screen.getByText("Yes"));
    const note = screen.getByTestId("ask-note-q1") as HTMLTextAreaElement;
    fireEvent.change(note, { target: { value: "   " } });
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe('[answers]\n1. [Deploy?] → "Yes"');
  });
});

describe("AskComposer — several questions in one batch", () => {
  it("composes all answers numbered globally in posting order", () => {
    const items = [
      questionItem(
        "q1",
        batch([
          question("q1", { header: "First" }),
          question("q2", {
            header: "Second",
            options: [
              { label: "A", detail: "a" },
              { label: "B", detail: "b" },
            ],
          }),
        ]),
      ),
    ];
    const { service } = renderAsk(makeConversation(items));
    // First question: select Yes
    fireEvent.click(screen.getByTestId("ask-option-q1-Yes"));
    // Second question: select B
    fireEvent.click(screen.getByText("B"));
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe('[answers]\n1. [First] → "Yes"\n2. [Second] → "B"');
  });
});

describe("AskComposer — untouched questions compose as skip", () => {
  it("an unanswered question composes identically to an explicit skip", () => {
    const items = [
      questionItem(
        "q1",
        batch([
          question("q1", { header: "First" }),
          question("q2", { header: "Second" }),
        ]),
      ),
    ];
    const { service } = renderAsk(makeConversation(items));
    // Answer only the first; leave the second untouched.
    fireEvent.click(screen.getByTestId("ask-option-q1-Yes"));
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const text = sentText(service);
    expect(text).toBe(
      '[answers]\n1. [First] → "Yes"\n2. [Second] → skipped (no answer)',
    );
  });
});

describe("AskComposer — in-flight state", () => {
  it("disables the send button while answers are in flight", () => {
    const item = questionItem("q1", batch([question("q1")]));
    let resolveSend: () => void = () => {};
    const service = new FakeConversationService();
    service.send = vi.fn(
      () =>
        new Promise<MutationReceipt>((resolve) => {
          resolveSend = () => resolve(service.receipt);
        }),
    );
    const conversationStore = createFakeConversationStore(
      makeConversation([item]),
    );
    const attachmentStore = createFakeAttachmentStore();
    render(
      <AskComposer
        conversationStore={conversationStore}
        conversationService={service}
        attachmentStore={attachmentStore}
      />,
    );
    fireEvent.click(screen.getByText("Yes"));
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    const btn = screen.getByTestId("ask-send-answers") as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    resolveSend();
  });
});

describe("AskComposer — settlement from another client", () => {
  it("stops rendering questions when askPending becomes false", () => {
    const item = questionItem("q1", batch([question("q1")]));
    const conversationStore = createFakeConversationStore(
      makeConversation([item], { askPending: true }),
    );
    const service = new FakeConversationService();
    const attachmentStore = createFakeAttachmentStore();
    const { rerender } = render(
      <AskComposer
        conversationStore={conversationStore}
        conversationService={service}
        attachmentStore={attachmentStore}
      />,
    );
    expect(screen.getByText("Deploy?")).toBeDefined();
    // Simulate another client settling the ask.
    conversationStore.setState({
      conversation: makeConversation([item], { askPending: false }),
    });
    rerender(
      <AskComposer
        conversationStore={conversationStore}
        conversationService={service}
        attachmentStore={attachmentStore}
      />,
    );
    expect(screen.queryByText("Deploy?")).toBeNull();
  });
});

describe("AskComposer — conflict draft recovery", () => {
  it("restores answers as draft and shows an error on send conflict", async () => {
    const item = questionItem("q1", batch([question("q1")]));
    const service = new FakeConversationService();
    service.sendShouldReject = new Error("Conflict: answers already submitted");
    const conversationStore = createFakeConversationStore(
      makeConversation([item]),
    );
    const attachmentStore = createFakeAttachmentStore();
    render(
      <AskComposer
        conversationStore={conversationStore}
        conversationService={service}
        attachmentStore={attachmentStore}
      />,
    );
    fireEvent.click(screen.getByText("Yes"));
    fireEvent.click(screen.getByTestId("ask-send-answers"));
    // Wait for the promise to settle.
    await Promise.resolve();
    await Promise.resolve();
    // The store's draft is restored with the composed answers text.
    expect(conversationStore.getState().draft).toBe(
      '[answers]\n1. [Deploy?] → "Yes"',
    );
    expect(conversationStore.getState().error).toBeTruthy();
  });
});

describe("AskComposer — send capability gate", () => {
  it("disables send when capabilities.send is false", () => {
    const item = questionItem("q1", batch([question("q1")]));
    renderAsk(
      makeConversation([item], {
        capabilities: { ...ALL_TRUE_CAPS, send: false },
      }),
    );
    const btn = screen.getByTestId("ask-send-answers") as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });
});

describe("AskComposer — ask mode unmounts normal inputs", () => {
  it("does not render a normal composer draft field", () => {
    const item = questionItem("q1", batch([question("q1")]));
    renderAsk(makeConversation([item]));
    expect(screen.queryByTestId("composer-draft")).toBeNull();
  });
});

describe("AskComposer — why / ifUnanswered display", () => {
  it("shows the why text when present", () => {
    const q = question("q1", { why: "Need confirmation before deploy" });
    const item = questionItem("q1", batch([q]));
    renderAsk(makeConversation([item]));
    expect(screen.getByText("Need confirmation before deploy")).toBeDefined();
  });

  it("shows the ifUnanswered fallback text when present", () => {
    const q = question("q1", { ifUnanswered: "assume yes" });
    const item = questionItem("q1", batch([q]));
    renderAsk(makeConversation([item]));
    expect(screen.getByText(/assume yes/i)).toBeDefined();
  });
});
