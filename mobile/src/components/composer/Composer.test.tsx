// Composer component tests — primary action (send/steer/queue/stop),
// capability removal, model/effort summary, draft binding, reconnecting
// disable, and attachment preview removal. Uses fake stores and services so
// no wire or network is required.

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { StoreApi, UseBoundStore } from "zustand";
import { create } from "zustand";
import type {
  InputItem,
  MutationReceipt,
} from "../../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  MobileCapabilities,
  MobileConversation,
  MobileTimelineItem,
} from "../../conversation/model";
import type { ConversationService } from "../../services/conversation";
import type { AttachmentState } from "../../state/attachments";
import type { ConversationState } from "../../state/conversation";
import { Composer } from "./Composer";

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
    askPending: false,
    ...over,
  };
}

function makeReceipt(): MutationReceipt {
  return {
    clientMutationId: "cmid-1",
    disposition: "accepted",
    threadId: "thread-1",
    projectionState: "current",
  };
}

// A fake ConversationService that records calls without any network.
class FakeConversationService implements ConversationService {
  sendCalls: InputItem[][] = [];
  steerCalls: InputItem[][] = [];
  queueCalls: InputItem[][] = [];
  interruptCalls = 0;
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
    return this.receipt;
  }
  async steer(input: InputItem[]): Promise<MutationReceipt> {
    this.steerCalls.push(input);
    return this.receipt;
  }
  async queue(input: InputItem[]): Promise<MutationReceipt> {
    this.queueCalls.push(input);
    return this.receipt;
  }
  async interrupt(): Promise<MutationReceipt> {
    this.interruptCalls += 1;
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
  conversation: MobileConversation | null,
  over: Partial<ConversationState> = {},
): ConversationStoreHook {
  const setDraft = vi.fn();
  const send = vi.fn(
    async (_service: ConversationService, _input: InputItem[]) => {
      await _service.send(_input);
    },
  );
  const steer = vi.fn(
    async (_service: ConversationService, _input: InputItem[]) => {
      await _service.steer(_input);
    },
  );
  const queue = vi.fn(
    async (_service: ConversationService, _input: InputItem[]) => {
      await _service.queue(_input);
    },
  );
  const interrupt = vi.fn(async (_service: ConversationService) => {
    await _service.interrupt();
  });
  return create<ConversationState>(() => ({
    ref: conversation ? "ref-1" : null,
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
    setDraft: setDraft,
    send,
    steer,
    queue,
    interrupt,
    close: vi.fn(),
    applyNotification: vi.fn(),
    reset: vi.fn(),
    ...over,
  }));
}

function createFakeAttachmentStore(
  attachments: {
    id: string;
    handle: string;
    mediaType: string;
    name?: string;
  }[] = [],
): AttachmentStoreHook {
  return create<AttachmentState>((set) => ({
    attachments,
    error: null,
    add: vi.fn(),
    remove: (id: string) =>
      set((s) => ({
        attachments: s.attachments.filter((a) => a.id !== id),
      })),
    clear: vi.fn(),
    setError: vi.fn(),
  }));
}

afterEach(() => {
  cleanup();
});

// --- helper to render with all props ---------------------------------------

function renderComposer(
  conv: MobileConversation,
  opts: {
    status?: ConversationState["status"];
    draft?: string;
    over?: Partial<ConversationState>;
    attachments?: {
      id: string;
      handle: string;
      mediaType: string;
      name?: string;
    }[];
  } = {},
): {
  conversationStore: ConversationStoreHook;
  service: FakeConversationService;
  attachmentStore: AttachmentStoreHook;
} {
  const service = new FakeConversationService();
  const conversationStore = createFakeConversationStore(conv, {
    status: opts.status ?? "open",
    draft: opts.draft ?? "",
    ...(opts.over ?? {}),
  });
  const attachmentStore = createFakeAttachmentStore(opts.attachments);
  render(
    <Composer
      conversationStore={conversationStore}
      conversationService={service}
      attachmentStore={attachmentStore}
    />,
  );
  return { conversationStore, service, attachmentStore };
}

// --- tests ------------------------------------------------------------------

describe("Composer — primary action visibility", () => {
  it("shows a Send button when the session is idle (ready)", () => {
    renderComposer(makeConversation([], { status: "ready" }));
    expect(screen.getByTestId("composer-send")).toBeDefined();
  });

  it("shows a Steer button when the session is running", () => {
    renderComposer(makeConversation([], { status: "running" }));
    expect(screen.getByTestId("composer-steer")).toBeDefined();
  });

  it("shows a Queue button when in explicit queue mode (queue depth > 0)", () => {
    renderComposer(
      makeConversation([], {
        status: "running",
        queue: { depth: 1, preview: ["q"] },
      }),
    );
    expect(screen.getByTestId("composer-queue")).toBeDefined();
  });

  it("shows a Stop button during generating when interrupt is available", () => {
    renderComposer(makeConversation([], { status: "running" }));
    expect(screen.getByTestId("composer-stop")).toBeDefined();
  });

  it("Send button is disabled when the draft is empty", () => {
    renderComposer(makeConversation([], { status: "ready" }), { draft: "" });
    const send = screen.getByTestId("composer-send") as HTMLButtonElement;
    expect(send.disabled).toBe(true);
  });

  it("Send button is enabled when the draft has text", () => {
    renderComposer(makeConversation([], { status: "ready" }), {
      draft: "hello",
    });
    const send = screen.getByTestId("composer-send") as HTMLButtonElement;
    expect(send.disabled).toBe(false);
  });
});

describe("Composer — primary action invocation", () => {
  it("Send calls conversationStore.send with the draft text", async () => {
    const { conversationStore, service } = renderComposer(
      makeConversation([], { status: "ready" }),
      { draft: "my message" },
    );
    fireEvent.click(screen.getByTestId("composer-send"));
    // The store's send is called with the service and text input.
    expect(conversationStore.getState().send).toHaveBeenCalled();
    expect(service.sendCalls).toHaveLength(1);
    const input = service.sendCalls[0];
    expect(input).toEqual([{ type: "text", text: "my message" }]);
  });

  it("Steer calls conversationStore.steer with the draft text", () => {
    const { conversationStore, service } = renderComposer(
      makeConversation([], { status: "running" }),
      { draft: "steer this" },
    );
    fireEvent.click(screen.getByTestId("composer-steer"));
    expect(conversationStore.getState().steer).toHaveBeenCalled();
    expect(service.steerCalls).toHaveLength(1);
    expect(service.steerCalls[0]).toEqual([
      { type: "text", text: "steer this" },
    ]);
  });

  it("Queue calls conversationStore.queue with the draft text", () => {
    const { conversationStore, service } = renderComposer(
      makeConversation([], {
        status: "running",
        queue: { depth: 1, preview: ["q"] },
      }),
      { draft: "queued msg" },
    );
    fireEvent.click(screen.getByTestId("composer-queue"));
    expect(conversationStore.getState().queue).toHaveBeenCalled();
    expect(service.queueCalls).toHaveLength(1);
    expect(service.queueCalls[0]).toEqual([
      { type: "text", text: "queued msg" },
    ]);
  });

  it("Stop calls conversationStore.interrupt", () => {
    const { conversationStore, service } = renderComposer(
      makeConversation([], { status: "running" }),
    );
    fireEvent.click(screen.getByTestId("composer-stop"));
    expect(conversationStore.getState().interrupt).toHaveBeenCalled();
    expect(service.interruptCalls).toBe(1);
  });
});

describe("Composer — capability removal", () => {
  it("disables Send and shows Unavailable when send capability is false", () => {
    renderComposer(
      makeConversation([], {
        status: "ready",
        capabilities: { ...ALL_TRUE_CAPS, send: false },
      }),
      { draft: "hello" },
    );
    const send = screen.getByTestId("composer-send") as HTMLButtonElement;
    expect(send.disabled).toBe(true);
    expect(screen.getByText(/unavailable for this source/i)).toBeDefined();
  });

  it("disables Steer when steer capability is false", () => {
    renderComposer(
      makeConversation([], {
        status: "running",
        capabilities: { ...ALL_TRUE_CAPS, steer: false },
      }),
      { draft: "hello" },
    );
    const steer = screen.getByTestId("composer-steer") as HTMLButtonElement;
    expect(steer.disabled).toBe(true);
  });

  it("disables Queue when queue capability is false", () => {
    renderComposer(
      makeConversation([], {
        status: "running",
        queue: { depth: 1, preview: ["q"] },
        capabilities: { ...ALL_TRUE_CAPS, queue: false },
      }),
      { draft: "hello" },
    );
    const queue = screen.getByTestId("composer-queue") as HTMLButtonElement;
    expect(queue.disabled).toBe(true);
  });

  it("hides Stop when interrupt capability is false", () => {
    renderComposer(
      makeConversation([], {
        status: "running",
        capabilities: { ...ALL_TRUE_CAPS, interrupt: false },
      }),
    );
    expect(screen.queryByTestId("composer-stop")).toBeNull();
  });
});

describe("Composer — model/effort summary", () => {
  it("displays the model provider and reasoning effort", () => {
    renderComposer(
      makeConversation([], {
        modelProvider: "anthropic",
        reasoningEffort: "high",
      }),
    );
    expect(screen.getByTestId("composer-summary")).toBeDefined();
    expect(screen.getByTestId("composer-summary").textContent).toMatch(
      /anthropic/i,
    );
    expect(screen.getByTestId("composer-summary").textContent).toMatch(/high/i);
  });

  it("displays the model provider even without reasoning effort", () => {
    renderComposer(
      makeConversation([], {
        modelProvider: "openai",
        reasoningEffort: undefined,
      }),
    );
    expect(screen.getByTestId("composer-summary").textContent).toMatch(
      /openai/i,
    );
  });
});

describe("Composer — draft binding", () => {
  it("binds the text field to conversationStore.draft via setDraft", () => {
    const { conversationStore } = renderComposer(
      makeConversation([], { status: "ready" }),
      { draft: "" },
    );
    const field = screen.getByTestId("composer-draft") as HTMLTextAreaElement;
    fireEvent.change(field, { target: { value: "typed text" } });
    expect(conversationStore.getState().setDraft).toHaveBeenCalledWith(
      "typed text",
    );
  });

  it("reflects the current draft from the store", () => {
    renderComposer(makeConversation([], { status: "ready" }), {
      draft: "existing",
    });
    const field = screen.getByTestId("composer-draft") as HTMLTextAreaElement;
    expect(field.value).toBe("existing");
  });
});

describe("Composer — reconnecting disable", () => {
  it("disables mutations while the connection is not open (reconnecting)", () => {
    renderComposer(makeConversation([], { status: "ready" }), {
      status: "closed",
      draft: "hello",
    });
    const send = screen.getByTestId("composer-send") as HTMLButtonElement;
    expect(send.disabled).toBe(true);
  });

  it("disables the draft field while reconnecting", () => {
    renderComposer(makeConversation([], { status: "ready" }), {
      status: "closed",
    });
    const field = screen.getByTestId("composer-draft") as HTMLTextAreaElement;
    expect(field.disabled).toBe(true);
  });
});

describe("Composer — attachment preview", () => {
  it("renders pending attachment previews from the attachment store", () => {
    renderComposer(makeConversation([], { status: "ready" }), {
      attachments: [
        { id: "a1", handle: "h1", mediaType: "image/jpeg", name: "p1.jpg" },
        { id: "a2", handle: "h2", mediaType: "image/png", name: "p2.png" },
      ],
    });
    expect(screen.getByText("p1.jpg")).toBeDefined();
    expect(screen.getByText("p2.png")).toBeDefined();
  });

  it("removes a preview via the remove button", () => {
    const { attachmentStore } = renderComposer(
      makeConversation([], { status: "ready" }),
      {
        attachments: [
          { id: "a1", handle: "h1", mediaType: "image/jpeg", name: "p1.jpg" },
        ],
      },
    );
    fireEvent.click(screen.getByTestId("composer-remove-attachment-a1"));
    expect(attachmentStore.getState().attachments).toHaveLength(0);
  });
});

describe("Composer — voice and attachment actions (placeholders)", () => {
  it("renders an attachment action button", () => {
    renderComposer(makeConversation([], { status: "ready" }));
    expect(screen.getByTestId("composer-attach")).toBeDefined();
  });

  it("renders a voice action button (placeholder)", () => {
    renderComposer(makeConversation([], { status: "ready" }));
    expect(screen.getByTestId("composer-voice")).toBeDefined();
  });
});
