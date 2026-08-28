import { type ChildProcessWithoutNullStreams, spawn } from "node:child_process";
import path from "node:path";
import { createInterface } from "node:readline";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  AnyNotification,
  InitializeResponse,
  MutationReceipt,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  Turn,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { App } from "../App";
import type { TauriBridge, TauriChannel, TauriEvent } from "../services/tauri";

type JsonObject = Record<string, unknown>;
type ServerFrame = JsonObject & {
  id?: number;
  method?: string;
  params?: unknown;
};

const tauriHarness = vi.hoisted(() => ({
  bridge: null as TauriBridge | null,
}));

vi.mock("@tauri-apps/api/core", () => ({
  invoke<T>(
    command: string,
    args?: Record<string, unknown> | ArrayBuffer | Uint8Array,
  ): Promise<T> {
    if (tauriHarness.bridge === null) {
      return Promise.reject(new Error("Rust AppWire harness is not installed"));
    }
    return tauriHarness.bridge.invoke<T>(command, args);
  },
  Channel: class<T> {
    readonly id: number;
    onmessage: (response: T) => void;
    onclose: (() => void) | null;
    dispose: () => void;
    cleanupCallback: () => void;
    toJSON: () => string;

    constructor(onMessage: (response: T) => void) {
      if (tauriHarness.bridge === null) {
        throw new Error("Rust AppWire harness is not installed");
      }
      const channel = tauriHarness.bridge.createChannel(
        onMessage,
      ) as TauriChannel<T> & {
        toJSON(): string;
      };
      const cleanup = channel.dispose.bind(channel);
      this.id = channel.id;
      this.onmessage = channel.onmessage;
      this.onclose = channel.onclose;
      this.dispose = cleanup;
      this.cleanupCallback = cleanup;
      this.toJSON = channel.toJSON.bind(channel);
    }
  },
}));

vi.mock("@tauri-apps/api/event", () => ({
  listen<T>(
    event: string,
    handler: (event: TauriEvent<T>) => void,
  ): Promise<() => void> {
    if (tauriHarness.bridge === null) {
      return Promise.reject(new Error("Rust AppWire harness is not installed"));
    }
    return tauriHarness.bridge.listen(event, handler);
  },
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (cause: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

interface ServerSnapshot {
  readonly connectionCount: number;
  readonly activeSockets: number;
  readonly maxActiveSockets: number;
  readonly initializedCount: number;
  readonly clientInfos: Array<{ name: string; version: string }>;
  readonly requestMethods: string[];
}

class RustHarnessBridge implements TauriBridge {
  readonly invocations: Array<{ cmd: string; args: JsonObject }> = [];
  readonly channelEvents: JsonObject[] = [];
  readonly serverRequests: ServerFrame[] = [];
  readonly profiles: readonly string[];
  private readonly child: ChildProcessWithoutNullStreams;
  private readonly pending = new Map<
    number,
    { resolve(value: unknown): void; reject(cause: unknown): void }
  >();
  private readonly channels = new Map<number, TauriChannel<unknown>>();
  private readonly commandWaiters = new Map<string, Array<() => void>>();
  private readonly requestWaiters = new Map<string, Array<() => void>>();
  private readonly deliveredFrameWaiters: Array<{
    predicate(frame: ServerFrame): boolean;
    resolve(): void;
  }> = [];
  private nextRequestId = 1;
  private nextChannelId = 1;
  private stderr = "";

  private constructor(
    child: ChildProcessWithoutNullStreams,
    profiles: readonly string[],
  ) {
    this.child = child;
    this.profiles = profiles;
  }

  static async start(): Promise<RustHarnessBridge> {
    const binary = path.join(
      process.cwd(),
      "src-tauri",
      "target",
      "debug",
      "examples",
      process.platform === "win32"
        ? "appwire_stdio_harness.exe"
        : "appwire_stdio_harness",
    );
    const child = spawn(binary, [], {
      cwd: process.cwd(),
      stdio: ["pipe", "pipe", "pipe"],
    });
    const ready = deferred<readonly string[]>();
    let bridge: RustHarnessBridge | undefined;
    let stderr = "";
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk: string) => {
      stderr += chunk;
      if (bridge) bridge.stderr += chunk;
    });
    createInterface({ input: child.stdout }).on("line", (line) => {
      const message = JSON.parse(line) as JsonObject;
      if (message.kind === "ready") {
        ready.resolve(message.profiles as readonly string[]);
        return;
      }
      bridge?.receive(message);
    });
    child.once("error", ready.reject);
    child.once("exit", (code) => {
      const cause = new Error(
        `Rust harness exited (${code}): ${bridge?.stderr ?? stderr}`,
      );
      if (!bridge) ready.reject(cause);
      else bridge.failPending(cause);
    });
    const profiles = await ready.promise;
    bridge = new RustHarnessBridge(child, profiles);
    bridge.stderr = stderr;
    await bridge.control("manualServer");
    return bridge;
  }

  invoke<T = unknown>(
    cmd: string,
    rawArgs: Record<string, unknown> | ArrayBuffer | Uint8Array = {},
  ): Promise<T> {
    if (rawArgs instanceof ArrayBuffer || ArrayBuffer.isView(rawArgs)) {
      return Promise.reject(new Error("AppWire harness accepts JSON commands"));
    }
    this.invocations.push({ cmd, args: rawArgs });
    return this.send<T>({ kind: "invoke", cmd, args: rawArgs }).finally(() => {
      const waiter = this.commandWaiters.get(cmd)?.shift();
      waiter?.();
    });
  }

  createChannel<T>(onMessage: (response: T) => void): TauriChannel<T> {
    const id = this.nextChannelId++;
    let disposed = false;
    const channel: TauriChannel<T> & { toJSON(): string } = {
      id,
      onmessage: onMessage,
      onclose: null,
      dispose: () => {
        if (disposed) return;
        disposed = true;
        this.channels.delete(id);
        channel.onclose?.();
      },
      toJSON: () => `__CHANNEL__:${id}`,
    };
    this.channels.set(id, channel as TauriChannel<unknown>);
    return channel;
  }

  listen<T>(
    _event: string,
    _handler: (event: TauriEvent<T>) => void,
  ): Promise<() => void> {
    return Promise.resolve(() => {});
  }

  control<T = unknown>(action: string, fields: JsonObject = {}): Promise<T> {
    return this.send<T>({ kind: "control", action, ...fields });
  }

  waitForCommand(command: string): Promise<void> {
    const done = deferred<void>();
    const waiters = this.commandWaiters.get(command) ?? [];
    waiters.push(() => done.resolve(undefined));
    this.commandWaiters.set(command, waiters);
    return done.promise;
  }

  waitForServerRequest(method: string, count: number): Promise<ServerFrame> {
    const existing = this.serverRequests.filter(
      (request) => request.method === method,
    )[count - 1];
    if (existing !== undefined) return Promise.resolve(existing);
    const done = deferred<void>();
    const waiters = this.requestWaiters.get(method) ?? [];
    waiters.push(() => done.resolve(undefined));
    this.requestWaiters.set(method, waiters);
    return done.promise.then(() => {
      const request = this.serverRequests.filter(
        (candidate) => candidate.method === method,
      )[count - 1];
      if (request === undefined) {
        throw new Error(`missing ${method} request ${count}`);
      }
      return request;
    });
  }

  async respond(request: ServerFrame, result: unknown): Promise<void> {
    if (!Number.isSafeInteger(request.id)) {
      throw new Error(`cannot respond to notification ${request.method ?? ""}`);
    }
    await this.sendServerFrame({ id: request.id, result });
  }

  notify(notification: AnyNotification): Promise<void> {
    return this.sendServerFrame(notification as unknown as ServerFrame);
  }

  activeChannelCount(): number {
    return this.channels.size;
  }

  async stop(): Promise<void> {
    const exited = deferred<void>();
    this.child.once("exit", () => exited.resolve(undefined));
    await this.control("shutdown");
    this.child.stdin.end();
    await exited.promise;
  }

  private async sendServerFrame(frame: ServerFrame): Promise<void> {
    const delivered = deferred<void>();
    this.deliveredFrameWaiters.push({
      predicate: (candidate) =>
        frame.id !== undefined
          ? candidate.id === frame.id
          : candidate.method === frame.method,
      resolve: () => delivered.resolve(undefined),
    });
    await this.control("serverFrame", { frame });
    await delivered.promise;
  }

  private send<T>(request: JsonObject): Promise<T> {
    const id = this.nextRequestId++;
    const completion = deferred<unknown>();
    this.pending.set(id, completion);
    this.child.stdin.write(`${JSON.stringify({ id, ...request })}\n`);
    return completion.promise as Promise<T>;
  }

  private receive(message: JsonObject): void {
    if (message.kind === "serverRequest") {
      const frame = message.frame as ServerFrame;
      this.serverRequests.push(frame);
      const waiter = this.requestWaiters.get(frame.method ?? "")?.shift();
      waiter?.();
      if (frame.method === "initialize" && frame.id !== undefined) {
        void this.respond(frame, INITIALIZE_RESPONSE).catch(
          (cause: unknown) => {
            this.failPending(
              cause instanceof Error ? cause : new Error(String(cause)),
            );
          },
        );
      }
      return;
    }
    if (message.kind === "channel") {
      const payload = message.payload as JsonObject;
      this.channelEvents.push(payload);
      const channel = this.channels.get(message.channelId as number);
      channel?.onmessage(payload);
      const delivered = decodeTextFrame(payload);
      if (delivered !== null) {
        const index = this.deliveredFrameWaiters.findIndex(({ predicate }) =>
          predicate(delivered),
        );
        if (index >= 0) {
          const [waiter] = this.deliveredFrameWaiters.splice(index, 1);
          waiter?.resolve();
        }
      }
      return;
    }
    if (message.kind !== "response") return;
    const id = message.id as number;
    const completion = this.pending.get(id);
    if (!completion) return;
    this.pending.delete(id);
    if (message.ok === true) completion.resolve(message.value);
    else
      completion.reject(new Error(String(message.error ?? "command failed")));
  }

  private failPending(cause: Error): void {
    for (const completion of this.pending.values()) completion.reject(cause);
    this.pending.clear();
  }
}

function decodeTextFrame(payload: JsonObject): ServerFrame | null {
  if (payload.type !== "text" || typeof payload.data !== "string") return null;
  return JSON.parse(payload.data) as ServerFrame;
}

const INITIALIZE_RESPONSE: InitializeResponse = {
  protocolVersion: "evener-appwire-v3",
  serverInfo: { name: "scripted-hub", version: "1.0.0" },
  sourceId: "scripted-source",
  features: {
    threadList: true,
    threadTurnsList: true,
    turnStart: true,
    turnSteer: true,
    threadClear: true,
    threadShutdown: true,
    forkFromTurn: true,
    tasks: true,
    transcriptList: true,
    modelList: true,
    directoryComplete: true,
    auth: true,
  },
};

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  queue: true,
  goal: false,
  rename: false,
};

const ATTENTION_REF = "live-attention-ref";
const ATTENTION_THREAD_ID = "live-attention-thread";
const RUNNING_REF = "live-running-ref";
const DRAFT_SENTINEL = "draft-sentinel::production-appwire";
const STREAMED_TEXT = "Scripted stream";
const REASONING_TEXT = "Plan route";
const TOOL_TEXT = "line done";

function makeThread(input: {
  id: string;
  ref: string;
  name: string;
  status: "active" | "awaiting";
  items?: ThreadItem[];
}): Thread {
  const turn: Turn = {
    id: "turn-live",
    itemsView: "full",
    status: "running",
    items: input.items ?? [],
    usage: { totalTokens: 4096 },
  };
  return {
    id: input.id,
    sessionId: input.id,
    preview: input.name,
    ephemeral: false,
    modelProvider: "scripted-provider",
    createdAt: 100,
    updatedAt: 200,
    status: { type: input.status },
    cwd: "/workspace/live-slice",
    cliVersion: "1.0.0",
    source: "evener",
    name: input.name,
    turns: [turn],
    evener: {
      ref: input.ref,
      activeTurnId: turn.id,
      capabilities: CAPABILITIES,
      diagnostics: {
        tools: [{ name: "exec_command", source: "builtin" }],
        jobs: [
          {
            jobId: "job-live",
            jobType: "shell",
            status: "running",
            outputBytes: 2048,
          },
        ],
      },
      queue: { depth: 0, revision: 7, preview: [] },
      tasks: { total: 3, done: 1 },
      usage: {
        inputTokens: 3000,
        outputTokens: 1096,
        cacheReadTokens: 512,
        totalTokens: 4096,
      },
      workMillis: 1250,
      cost: "$0.42",
      contextUsed: 4096,
      contextWindow: 8192,
      contextRemaining: 4096,
      contextPressure: 0.5,
      reasoningEffort: "high",
      reasoningEffortLevels: ["low", "medium", "high"],
      supportsReasoning: true,
    },
  };
}

function rosterThreads(): Thread[] {
  return [
    makeThread({
      id: ATTENTION_THREAD_ID,
      ref: ATTENTION_REF,
      name: "Attention vertical slice",
      status: "awaiting",
    }),
    makeThread({
      id: "live-running-thread",
      ref: RUNNING_REF,
      name: "Running vertical slice",
      status: "active",
    }),
  ];
}

function authoritativeThread(): Thread {
  return makeThread({
    id: ATTENTION_THREAD_ID,
    ref: ATTENTION_REF,
    name: "Attention vertical slice",
    status: "active",
    items: [
      { type: "userMessage", id: "user-live", text: "Begin vertical slice" },
      {
        type: "agentMessage",
        id: "assistant-live",
        text: STREAMED_TEXT,
        status: "inProgress",
      },
      {
        type: "reasoning",
        id: "reasoning-live",
        text: REASONING_TEXT,
        status: "inProgress",
      },
      {
        type: "commandExecution",
        id: "tool-live",
        toolName: "exec_command",
        callId: "call-live",
        output: TOOL_TEXT,
        status: "completed",
      },
    ],
  });
}

function receiptFor(
  request: ServerFrame,
  kind: "send" | "steer" | "queue" | "interrupt",
): MutationReceipt {
  const params = request.params as { clientMutationId: string };
  return {
    clientMutationId: params.clientMutationId,
    disposition: "accepted",
    threadId: ATTENTION_THREAD_ID,
    turnId: kind === "send" ? "turn-receipt" : undefined,
    queueEntryIds: kind === "queue" ? ["queue-entry-live"] : undefined,
    projectionState: "current",
  };
}

function sessionKeys(): string[] {
  return [...document.querySelectorAll<HTMLElement>("[data-session-id]")].map(
    (element) => element.dataset.sessionId ?? "",
  );
}

function transcriptKeys(): string[] {
  return [
    ...document.querySelectorAll<HTMLElement>("[data-transcript-item-id]"),
  ].map((element) => element.dataset.transcriptItemId ?? "");
}

function workKeys(): string[] {
  return [...document.querySelectorAll<HTMLElement>("[data-work-node-id]")].map(
    (element) => element.dataset.workNodeId ?? "",
  );
}

function expectConcept(className: string): void {
  expect(document.querySelectorAll("[data-concept-root]")).toHaveLength(1);
  expect(document.querySelector(`.${className}`)).not.toBeNull();
}

function chooseConcept(name: "Stillwater" | "Constellation" | "Field Notes") {
  fireEvent.click(screen.getByRole("button", { name: "Switch concept" }));
  fireEvent.click(screen.getByRole("button", { name: new RegExp(name) }));
}

function expectConversationEvidence(
  expectedKeys: readonly string[],
  expectedDraft: string,
): void {
  expect(transcriptKeys()).toEqual(expectedKeys);
  expect(screen.getByText(STREAMED_TEXT)).toBeInTheDocument();
  expect(screen.getByText("Reasoning")).toBeInTheDocument();
  expect(screen.getByText(REASONING_TEXT)).toBeInTheDocument();
  expect(screen.getByText("exec_command")).toBeInTheDocument();
  expect(screen.getByText(TOOL_TEXT)).toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue(
    expectedDraft,
  );
  expect(
    screen.getByRole("button", { name: /^Load older/ }),
  ).toBeInTheDocument();
}

function expectWorkEvidence(expectedKeys: readonly string[]): void {
  expect(workKeys()).toEqual(expectedKeys);
  expect(screen.getByText("shell")).toBeInTheDocument();
  const usage = document.querySelector("[data-work-usage]");
  expect(usage).not.toBeNull();
  expect(usage).toHaveTextContent("4.1K");
  expect(usage).toHaveTextContent("$0.42");
}

function notification(
  method: AnyNotification["method"],
  params: JsonObject,
): AnyNotification {
  return { method, params } as AnyNotification;
}

async function sendNotification(
  bridge: RustHarnessBridge,
  event: AnyNotification,
): Promise<void> {
  await act(async () => {
    await bridge.notify(event);
  });
}

async function submitMutation(
  bridge: RustHarnessBridge,
  mode: "Steer" | "Queue",
  text: string,
  method: "turn/steer" | "turn/queue",
): Promise<ServerFrame> {
  const count =
    bridge.serverRequests.filter((request) => request.method === method)
      .length + 1;
  fireEvent.click(screen.getByRole("button", { name: mode }));
  fireEvent.change(screen.getByRole("textbox", { name: "Message" }), {
    target: { value: text },
  });
  fireEvent.click(screen.getByRole("button", { name: /submit/i }));
  const request = await bridge.waitForServerRequest(method, count);
  expect(request.params).toMatchObject({
    ref: ATTENTION_REF,
    input: [{ type: "text", text }],
  });
  await act(async () => {
    await bridge.respond(request, {
      receipt: receiptFor(request, mode === "Steer" ? "steer" : "queue"),
    });
  });
  return request;
}

function methodCount(bridge: RustHarnessBridge, method: string): number {
  return bridge.serverRequests.filter((request) => request.method === method)
    .length;
}

function installMemoryLocalStorage(): void {
  const values = new Map<string, string>();
  const storage: Storage = {
    get length() {
      return values.size;
    },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => {
      values.delete(key);
    },
    setItem: (key, value) => {
      values.set(key, value);
    },
  };
  Object.defineProperty(window, "localStorage", {
    configurable: true,
    value: storage,
  });
}

let fakeTimersActive = false;

afterEach(() => {
  cleanup();
  tauriHarness.bridge = null;
  if (fakeTimersActive) {
    vi.useRealTimers();
    fakeTimersActive = false;
  }
});

describe("production App live concepts over the real native AppWire bridge", () => {
  it("runs one deterministic composed graph through all concepts and controls", async () => {
    window.history.replaceState(null, "", "/");
    installMemoryLocalStorage();
    const bridge = await RustHarnessBridge.start();
    tauriHarness.bridge = bridge;
    let mounted = false;
    try {
      const initialized = bridge.waitForServerRequest("initialized", 1);
      render(<App />);
      mounted = true;
      await act(async () => {
        await initialized;
      });

      const initialize = await bridge.waitForServerRequest("initialize", 1);
      expect(initialize.params).toEqual({
        protocolVersion: "evener-appwire-v3",
        clientInfo: { name: "evener-mobile", version: "0.1.0" },
        capabilities: { experimentalApi: false },
      });

      const rosterRequest = await bridge.waitForServerRequest("thread/list", 1);
      expect(rosterRequest.params).toEqual({ limit: 501 });
      await act(async () => {
        await bridge.respond(rosterRequest, { data: rosterThreads() });
      });

      expectConcept("concept-stillwater");
      expect(screen.getByText("Attention vertical slice")).toBeInTheDocument();
      expect(screen.getByText("Running vertical slice")).toBeInTheDocument();
      const stableRosterKeys = sessionKeys();
      expect(stableRosterKeys).toHaveLength(2);
      expect(stableRosterKeys).not.toContain(ATTENTION_REF);
      expect(stableRosterKeys).not.toContain(RUNNING_REF);

      chooseConcept("Constellation");
      expectConcept("concept-constellation");
      expect(sessionKeys()).toEqual(stableRosterKeys);
      chooseConcept("Field Notes");
      expectConcept("concept-field-notes");
      expect(sessionKeys()).toEqual(stableRosterKeys);
      chooseConcept("Stillwater");
      expectConcept("concept-stillwater");

      fireEvent.click(
        screen.getByRole("button", { name: /Attention vertical slice/ }),
      );
      const readRequest = await bridge.waitForServerRequest("thread/read", 1);
      expect(readRequest.params).toEqual({
        ref: ATTENTION_REF,
        includeTurns: true,
        subscribe: true,
        replaceSubscription: true,
        turnLimit: 50,
      });
      const initialReadThread = makeThread({
        id: ATTENTION_THREAD_ID,
        ref: ATTENTION_REF,
        name: "Attention vertical slice",
        status: "active",
        items: [
          {
            type: "userMessage",
            id: "user-live",
            text: "Begin vertical slice",
          },
        ],
      });
      await act(async () => {
        await bridge.respond(readRequest, {
          thread: initialReadThread,
          olderCursor: "older-cursor-live",
        });
      });

      const common = {
        threadId: ATTENTION_THREAD_ID,
        ref: ATTENTION_REF,
        turnId: "turn-live",
      };
      await sendNotification(
        bridge,
        notification("item/started", {
          ...common,
          item: {
            type: "agentMessage",
            id: "assistant-live",
            text: "Scripted ",
            status: "inProgress",
          },
        }),
      );
      await sendNotification(
        bridge,
        notification("item/agentMessage/delta", {
          ...common,
          itemId: "assistant-live",
          delta: "stream",
        }),
      );
      await sendNotification(
        bridge,
        notification("item/started", {
          ...common,
          item: {
            type: "reasoning",
            id: "reasoning-live",
            text: "Plan ",
            status: "inProgress",
          },
        }),
      );
      await sendNotification(
        bridge,
        notification("item/reasoning/summaryTextDelta", {
          ...common,
          itemId: "reasoning-live",
          summaryIndex: 0,
          delta: "route",
        }),
      );
      await sendNotification(
        bridge,
        notification("item/started", {
          ...common,
          item: {
            type: "commandExecution",
            id: "tool-live",
            toolName: "exec_command",
            callId: "call-live",
            output: "line ",
            status: "inProgress",
          },
        }),
      );
      await sendNotification(
        bridge,
        notification("item/toolOutput/delta", {
          ...common,
          itemId: "tool-live",
          callId: "call-live",
          delta: "done",
        }),
      );
      await sendNotification(
        bridge,
        notification("item/completed", {
          ...common,
          item: {
            type: "commandExecution",
            id: "tool-live",
            toolName: "exec_command",
            callId: "call-live",
            output: TOOL_TEXT,
            status: "completed",
          },
        }),
      );
      expect(methodCount(bridge, "thread/read")).toBe(1);

      fireEvent.click(screen.getByRole("button", { name: "Reasoning" }));
      fireEvent.click(screen.getByRole("button", { name: "exec_command" }));
      fireEvent.change(screen.getByRole("textbox", { name: "Message" }), {
        target: { value: DRAFT_SENTINEL },
      });
      const stableTranscriptKeys = transcriptKeys();
      expect(stableTranscriptKeys).toHaveLength(4);
      expect(stableTranscriptKeys).not.toContain(ATTENTION_REF);
      expectConversationEvidence(stableTranscriptKeys, DRAFT_SENTINEL);

      chooseConcept("Constellation");
      expectConcept("concept-constellation");
      expectConversationEvidence(stableTranscriptKeys, DRAFT_SENTINEL);
      chooseConcept("Field Notes");
      expectConcept("concept-field-notes");
      expectConversationEvidence(stableTranscriptKeys, DRAFT_SENTINEL);

      fireEvent.click(screen.getByRole("button", { name: "Work" }));
      const stableWorkKeys = workKeys();
      expect(stableWorkKeys).toHaveLength(1);
      expectWorkEvidence(stableWorkKeys);
      chooseConcept("Stillwater");
      expectConcept("concept-stillwater");
      expectWorkEvidence(stableWorkKeys);
      chooseConcept("Constellation");
      expectConcept("concept-constellation");
      expectWorkEvidence(stableWorkKeys);
      chooseConcept("Field Notes");
      expectConcept("concept-field-notes");
      expectWorkEvidence(stableWorkKeys);
      fireEvent.click(screen.getByRole("button", { name: "Close" }));
      expectConversationEvidence(stableTranscriptKeys, DRAFT_SENTINEL);

      const opensBeforePendingSwitch = bridge.invocations.filter(
        ({ cmd }) => cmd === "appwire_open",
      ).length;
      const sendCount = methodCount(bridge, "turn/start") + 1;
      fireEvent.click(screen.getByRole("button", { name: "Send" }));
      fireEvent.click(screen.getByRole("button", { name: /submit/i }));
      const pendingSend = await bridge.waitForServerRequest(
        "turn/start",
        sendCount,
      );
      expect(pendingSend.params).toMatchObject({
        ref: ATTENTION_REF,
        input: [{ type: "text", text: DRAFT_SENTINEL }],
      });
      expect(screen.getByRole("status")).toHaveTextContent(/send.*pending/i);

      chooseConcept("Stillwater");
      expect(screen.getByRole("status")).toHaveTextContent(/send/i);
      chooseConcept("Constellation");
      expect(screen.getByRole("status")).toHaveTextContent(/send/i);
      expect(methodCount(bridge, "turn/start")).toBe(1);
      expect(methodCount(bridge, "thread/read")).toBe(1);
      expect(
        bridge.invocations.filter(({ cmd }) => cmd === "appwire_open"),
      ).toHaveLength(opensBeforePendingSwitch);
      let snapshot = await bridge.control<ServerSnapshot>("snapshot");
      expect(snapshot).toMatchObject({
        connectionCount: 1,
        activeSockets: 1,
        maxActiveSockets: 1,
        initializedCount: 1,
      });
      expect(bridge.activeChannelCount()).toBe(1);

      await act(async () => {
        await bridge.respond(pendingSend, {
          turn: {
            id: "turn-receipt",
            itemsView: "full",
            status: "running",
          },
          receipt: receiptFor(pendingSend, "send"),
        });
      });

      await submitMutation(
        bridge,
        "Steer",
        "steer-sentinel::production-appwire",
        "turn/steer",
      );
      await submitMutation(
        bridge,
        "Queue",
        "queue-sentinel::production-appwire",
        "turn/queue",
      );
      const interruptCount = methodCount(bridge, "turn/interrupt") + 1;
      fireEvent.click(screen.getByRole("button", { name: "Interrupt" }));
      const interrupt = await bridge.waitForServerRequest(
        "turn/interrupt",
        interruptCount,
      );
      expect(interrupt.params).toMatchObject({ ref: ATTENTION_REF });
      await act(async () => {
        await bridge.respond(interrupt, {
          receipt: receiptFor(interrupt, "interrupt"),
        });
      });

      await sendNotification(
        bridge,
        notification("evener/thread/resync", {
          threadId: ATTENTION_THREAD_ID,
          ref: ATTENTION_REF,
        }),
      );
      const authoritativeRead = await bridge.waitForServerRequest(
        "thread/read",
        2,
      );
      expect(authoritativeRead.params).toEqual({
        ref: ATTENTION_REF,
        includeTurns: true,
        subscribe: true,
        replaceSubscription: true,
        turnLimit: 50,
      });
      await act(async () => {
        await bridge.respond(authoritativeRead, {
          thread: authoritativeThread(),
          olderCursor: "older-cursor-authoritative",
        });
      });
      expect(methodCount(bridge, "thread/read")).toBe(2);
      expect(transcriptKeys()).toEqual(stableTranscriptKeys);
      expect(screen.getByText(STREAMED_TEXT)).toBeInTheDocument();

      fireEvent.click(screen.getByRole("button", { name: "Back" }));
      expect(sessionKeys()).toEqual(stableRosterKeys);

      vi.useFakeTimers();
      fakeTimersActive = true;
      await sendNotification(bridge, notification("evener/tree/changed", {}));
      act(() => {
        vi.advanceTimersByTime(300);
      });
      const refreshedRoster = await bridge.waitForServerRequest(
        "thread/list",
        2,
      );
      expect(refreshedRoster.params).toEqual({ limit: 501 });
      const refreshedThreads = rosterThreads();
      const firstRefreshed = refreshedThreads[0];
      if (firstRefreshed === undefined)
        throw new Error("missing roster fixture");
      refreshedThreads[0] = {
        ...firstRefreshed,
        name: "Attention roster refreshed",
      };
      await act(async () => {
        await bridge.respond(refreshedRoster, { data: refreshedThreads });
      });
      vi.useRealTimers();
      fakeTimersActive = false;
      expect(
        screen.getByText("Attention roster refreshed"),
      ).toBeInTheDocument();
      expect(methodCount(bridge, "thread/list")).toBe(2);

      snapshot = await bridge.control<ServerSnapshot>("snapshot");
      expect(snapshot.clientInfos).toEqual([
        { name: "evener-mobile", version: "0.1.0" },
      ]);
      expect(snapshot.requestMethods).toEqual([
        "initialize",
        "initialized",
        "thread/list",
        "thread/read",
        "turn/start",
        "turn/steer",
        "turn/queue",
        "turn/interrupt",
        "thread/read",
        "thread/list",
      ]);
      expect(snapshot.connectionCount).toBe(1);
      expect(snapshot.activeSockets).toBe(1);
      expect(snapshot.maxActiveSockets).toBe(1);
      expect(snapshot.initializedCount).toBe(1);
      expect(bridge.activeChannelCount()).toBe(1);
      expect(
        bridge.invocations.filter(({ cmd }) => cmd === "appwire_open"),
      ).toHaveLength(1);
      expect(
        bridge.invocations.every(({ cmd, args }) => {
          if (cmd === "appwire_open") {
            return Object.keys(args).sort().join(",") === "onEvent,request";
          }
          if (cmd === "appwire_send" || cmd === "appwire_close") {
            return Object.keys(args).join(",") === "request";
          }
          return true;
        }),
      ).toBe(true);
    } finally {
      if (mounted) {
        const close = bridge.waitForCommand("appwire_close");
        cleanup();
        await close;
      }
      tauriHarness.bridge = null;
      await bridge.stop();
    }
  });
});
