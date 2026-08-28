// Component tests for the wired NewSessionScreen — verifies the form
// (project path, initial prompt, model/effort), validation, start success
// navigates to the new conversation, and start error shows an inline error.

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { StoreApi, UseBoundStore } from "zustand";
import { create } from "zustand";
import type {
  HarnessDescriptor,
  ModelDescriptor,
  Thread,
  Turn,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  NewSessionParams,
  NewSessionService,
} from "../services/newSession";
import type { NavigationState } from "../state/navigation";
import {
  NewSessionScreen,
  type NewSessionScreenProps,
} from "./NewSessionScreen";

afterEach(() => {
  cleanup();
});

// --- fake new session service ------------------------------------------------

class FakeNewSessionService implements NewSessionService {
  startCalls = 0;
  lastParams: NewSessionParams | null = null;
  thread: Thread = makeThread();
  turn: Turn = makeTurn();
  shouldReject: Error | null = null;
  recentProjectsList: string[] = [];
  harnessList: HarnessDescriptor[] = [];
  modelList: ModelDescriptor[] = [];
  recentModelList: ModelDescriptor[] = [];

  async start(
    params: NewSessionParams,
  ): Promise<{ thread: Thread; turn: Turn }> {
    this.startCalls += 1;
    this.lastParams = params;
    if (this.shouldReject !== null) throw this.shouldReject;
    return { thread: this.thread, turn: this.turn };
  }

  async recentProjects(): Promise<string[]> {
    return this.recentProjectsList;
  }

  async harnesses(): Promise<HarnessDescriptor[]> {
    return this.harnessList;
  }

  async models(): Promise<{
    data: ModelDescriptor[];
    recent?: ModelDescriptor[];
  }> {
    return { data: this.modelList, recent: this.recentModelList };
  }
}

function makeThread(over: Partial<Thread> = {}): Thread {
  return {
    id: "thread-new-1",
    sessionId: "session-new-1",
    preview: "Started: fix the bug",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1_000_000,
    updatedAt: 1_000_000,
    status: { type: "idle" },
    cwd: "/tmp",
    cliVersion: "1.0.0",
    source: "local",
    evener: {
      ref: "ref-new-1",
      capabilities: {
        send: true,
        steer: true,
        interrupt: true,
        compact: true,
        clear: true,
        forkFromTurn: true,
        shutdown: true,
        changeModel: true,
        changeVisionModel: true,
        queue: true,
        goal: true,
        rename: true,
      },
      queue: { revision: 0 },
    },
    ...over,
  };
}

function makeTurn(): Turn {
  return { id: "turn-1", itemsView: "default", status: "completed" };
}

// --- fake navigation store ---------------------------------------------------

type NavigationStoreHook = UseBoundStore<StoreApi<NavigationState>>;

function createFakeNavigationStore(): NavigationStoreHook {
  return create<NavigationState>((set, get) => ({
    tab: "new",
    conversationStack: [],
    activeConversation: null,
    setTab: () => {},
    pushConversation: vi.fn((entry) =>
      set((state) => ({
        conversationStack: [...state.conversationStack, entry],
        activeConversation: entry,
      })),
    ) as NavigationState["pushConversation"],
    popConversation: () => {},
    popAllConversations: () => {},
    clearConversations: () => {},
    canGoBack: () => get().conversationStack.length > 0,
  }));
}

// --- helpers -----------------------------------------------------------------

function renderNewSession(
  opts: {
    service?: FakeNewSessionService;
    navigation?: NavigationStoreHook;
  } = {},
) {
  const service = opts.service ?? new FakeNewSessionService();
  const navigation = opts.navigation ?? createFakeNavigationStore();
  const props: NewSessionScreenProps = { service, navigation };
  return render(<NewSessionScreen {...props} />);
}

// --- tests -------------------------------------------------------------------

describe("NewSessionScreen — form validation", () => {
  it("Start button is disabled when project path is empty", () => {
    renderNewSession();
    const startBtn = screen.getByRole("button", { name: /start/i });
    expect(startBtn).toBeDisabled();
  });

  it("Start button is enabled when project path is non-empty", () => {
    renderNewSession();
    const input = screen.getByLabelText(/project path/i);
    fireEvent.change(input, { target: { value: "/home/jesse/work" } });
    const startBtn = screen.getByRole("button", { name: /start/i });
    expect(startBtn).not.toBeDisabled();
  });

  it("prompt field is optional and does not affect Start enablement", () => {
    renderNewSession();
    const projectInput = screen.getByLabelText(/project path/i);
    fireEvent.change(projectInput, { target: { value: "/tmp" } });
    expect(screen.getByRole("button", { name: /start/i })).not.toBeDisabled();
  });
});

describe("NewSessionScreen — start success", () => {
  it("calls service.start() with project path and prompt", async () => {
    const service = new FakeNewSessionService();
    renderNewSession({ service });

    const projectInput = screen.getByLabelText(/project path/i);
    fireEvent.change(projectInput, { target: { value: "/home/work" } });
    const promptInput = screen.getByLabelText(/initial prompt/i);
    fireEvent.change(promptInput, { target: { value: "fix the bug" } });
    fireEvent.click(screen.getByRole("button", { name: /start/i }));

    await waitFor(() => expect(service.startCalls).toBe(1));
    expect(service.lastParams?.cwd).toBe("/home/work");
    expect(service.lastParams?.input).toEqual([
      { type: "text", text: "fix the bug" },
    ]);
  });

  it("navigates to the new conversation on success", async () => {
    const service = new FakeNewSessionService();
    service.thread = makeThread({
      id: "thread-xyz",
      preview: "My new session",
      evener: { ...makeThread().evener, ref: "local:thread-xyz" },
    });
    const navigation = createFakeNavigationStore();
    renderNewSession({ service, navigation });

    const projectInput = screen.getByLabelText(/project path/i);
    fireEvent.change(projectInput, { target: { value: "/tmp" } });
    fireEvent.click(screen.getByRole("button", { name: /start/i }));

    await waitFor(() => expect(service.startCalls).toBe(1));
    expect(navigation.getState().pushConversation).toHaveBeenCalledWith({
      sessionId: "local:thread-xyz",
      title: "My new session",
    });
  });

  it("navigates using thread.preview as title when name is absent", async () => {
    const service = new FakeNewSessionService();
    service.thread = makeThread({
      id: "thread-abc",
      preview: "preview text only",
      evener: { ...makeThread().evener, ref: "local:thread-abc" },
    });
    const navigation = createFakeNavigationStore();
    renderNewSession({ service, navigation });

    const projectInput = screen.getByLabelText(/project path/i);
    fireEvent.change(projectInput, { target: { value: "/tmp" } });
    fireEvent.click(screen.getByRole("button", { name: /start/i }));

    await waitFor(() => expect(service.startCalls).toBe(1));
    expect(navigation.getState().pushConversation).toHaveBeenCalledWith({
      sessionId: "local:thread-abc",
      title: "preview text only",
    });
  });

  it("does not send input when prompt is empty", async () => {
    const service = new FakeNewSessionService();
    renderNewSession({ service });

    const projectInput = screen.getByLabelText(/project path/i);
    fireEvent.change(projectInput, { target: { value: "/tmp" } });
    fireEvent.click(screen.getByRole("button", { name: /start/i }));

    await waitFor(() => expect(service.startCalls).toBe(1));
    expect(service.lastParams?.input).toBeUndefined();
  });
});

describe("NewSessionScreen — start error", () => {
  it("shows inline error message when start fails", async () => {
    const service = new FakeNewSessionService();
    service.shouldReject = new Error("Invalid project path");
    renderNewSession({ service });

    const projectInput = screen.getByLabelText(/project path/i);
    fireEvent.change(projectInput, { target: { value: "/bad" } });
    fireEvent.click(screen.getByRole("button", { name: /start/i }));

    expect(
      await screen.findByText(/invalid project path/i),
    ).toBeInTheDocument();
  });

  it("does not navigate when start fails", async () => {
    const service = new FakeNewSessionService();
    service.shouldReject = new Error("server error");
    const navigation = createFakeNavigationStore();
    renderNewSession({ service, navigation });

    const projectInput = screen.getByLabelText(/project path/i);
    fireEvent.change(projectInput, { target: { value: "/bad" } });
    fireEvent.click(screen.getByRole("button", { name: /start/i }));

    await waitFor(() => expect(service.startCalls).toBe(1));
    expect(navigation.getState().pushConversation).not.toHaveBeenCalled();
  });

  it("clears error after a successful retry", async () => {
    const service = new FakeNewSessionService();
    service.shouldReject = new Error("first attempt fails");
    renderNewSession({ service });

    const projectInput = screen.getByLabelText(/project path/i);
    fireEvent.change(projectInput, { target: { value: "/tmp" } });
    fireEvent.click(screen.getByRole("button", { name: /start/i }));
    expect(await screen.findByText(/first attempt fails/i)).toBeInTheDocument();

    service.shouldReject = null;
    fireEvent.click(screen.getByRole("button", { name: /start/i }));
    await waitFor(() => expect(service.startCalls).toBe(2));
    expect(screen.queryByText(/first attempt fails/i)).not.toBeInTheDocument();
  });
});

describe("NewSessionScreen — recent projects autocomplete", () => {
  it("loads recent projects on mount and shows them as suggestions", async () => {
    const service = new FakeNewSessionService();
    service.recentProjectsList = ["/home/work", "/home/play"];
    renderNewSession({ service });

    await waitFor(() => expect(service.recentProjectsList).toBeDefined());
    expect(screen.getByText("/home/work")).toBeInTheDocument();
    expect(screen.getByText("/home/play")).toBeInTheDocument();
  });

  it("clicking a recent project fills the project path field", async () => {
    const service = new FakeNewSessionService();
    service.recentProjectsList = ["/home/work"];
    renderNewSession({ service });

    const recentBtn = await screen.findByText("/home/work");
    fireEvent.click(recentBtn);

    const projectInput = screen.getByLabelText(
      /project path/i,
    ) as HTMLInputElement;
    expect(projectInput.value).toBe("/home/work");
  });
});

describe("NewSessionScreen — model discovery and selection", () => {
  it("loads discovered models and shows provider/model labels", async () => {
    const service = new FakeNewSessionService();
    service.modelList = [
      { provider: "anthropic", model: "claude-sonnet-4-5" },
      { provider: "openai", model: "gpt-5" },
    ];
    renderNewSession({ service });

    expect(
      await screen.findByRole("option", {
        name: "anthropic / claude-sonnet-4-5",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("option", { name: "openai / gpt-5" }),
    ).toBeInTheDocument();
  });

  it("forwards the selected provider and model when starting", async () => {
    const service = new FakeNewSessionService();
    service.modelList = [{ provider: "openai", model: "gpt-5" }];
    renderNewSession({ service });

    const modelSelect = await screen.findByLabelText(/model/i);
    fireEvent.change(modelSelect, { target: { value: "openai\u0000gpt-5" } });
    fireEvent.change(screen.getByLabelText(/project path/i), {
      target: { value: "/tmp" },
    });
    fireEvent.click(screen.getByRole("button", { name: /start/i }));

    await waitFor(() => expect(service.startCalls).toBe(1));
    expect(service.lastParams?.modelProvider).toBe("openai");
    expect(service.lastParams?.model).toBe("gpt-5");
  });

  it("keeps the default model selection out of thread/start params", async () => {
    const service = new FakeNewSessionService();
    service.modelList = [{ provider: "openai", model: "gpt-5" }];
    renderNewSession({ service });

    await screen.findByRole("option", { name: "openai / gpt-5" });
    fireEvent.change(screen.getByLabelText(/project path/i), {
      target: { value: "/tmp" },
    });
    fireEvent.click(screen.getByRole("button", { name: /start/i }));

    await waitFor(() => expect(service.startCalls).toBe(1));
    expect(service.lastParams?.modelProvider).toBeUndefined();
    expect(service.lastParams?.model).toBeUndefined();
  });
});
