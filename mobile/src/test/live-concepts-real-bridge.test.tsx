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
type ClientFrameDisposition =
  | { readonly kind: "accepted"; readonly frame: ServerFrame }
  | { readonly kind: "rejected"; readonly error: string };

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
  let settled = false;
  let resolve!: (value: T) => void;
  let reject!: (cause: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = (value) => {
      if (settled) return;
      settled = true;
      res(value);
    };
    reject = (cause) => {
      if (settled) return;
      settled = true;
      rej(cause);
    };
  });
  return {
    promise,
    resolve,
    reject,
    get settled() {
      return settled;
    },
  };
}

interface ServerSnapshot {
  readonly connectionCount: number;
  readonly activeSockets: number;
  readonly maxActiveSockets: number;
  readonly initializedCount: number;
  readonly clientInfos: Array<{ name: string; version: string }>;
  readonly requestMethods: string[];
}

interface HarnessStartOptions {
  readonly manual?: boolean;
  readonly binary?: string;
  readonly autoInitialize?: boolean;
}

class RustHarnessBridge implements TauriBridge {
  readonly invocations: Array<{ cmd: string; args: JsonObject }> = [];
  readonly channelEvents: JsonObject[] = [];
  readonly serverRequests: ServerFrame[] = [];
  readonly protocolErrors: string[] = [];
  readonly profiles: readonly string[];
  private readonly child: ChildProcessWithoutNullStreams;
  private readonly pending = new Map<
    number,
    { resolve(value: unknown): void; reject(cause: unknown): void }
  >();
  private readonly channels = new Map<number, TauriChannel<unknown>>();
  private readonly commandWaiters = new Map<
    string,
    Array<{ resolve(): void; reject(cause: unknown): void }>
  >();
  private readonly requestWaiters = new Map<
    string,
    Array<{ resolve(): void; reject(cause: unknown): void }>
  >();
  private readonly clientFrameDispositionWaiters: Array<{
    resolve(value: ClientFrameDisposition): void;
    reject(cause: unknown): void;
  }> = [];
  private readonly deliveredFrameWaiters: Array<{
    predicate(frame: ServerFrame): boolean;
    resolve(): void;
    reject(cause: unknown): void;
  }> = [];
  private readonly childClosed: Promise<void>;
  private readonly autoInitialize: boolean;
  private nextRequestId = 1;
  private nextChannelId = 1;
  private stderr = "";
  private exited = false;
  private stopPromise: Promise<void> | null = null;

  private constructor(
    child: ChildProcessWithoutNullStreams,
    profiles: readonly string[],
    childClosed: Promise<void>,
    autoInitialize: boolean,
  ) {
    this.child = child;
    this.profiles = profiles;
    this.childClosed = childClosed;
    this.autoInitialize = autoInitialize;
  }

  static async start(
    options: HarnessStartOptions = {},
  ): Promise<RustHarnessBridge> {
    const binary =
      options.binary ??
      path.join(
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
    const childClosed = deferred<void>();
    let bridge: RustHarnessBridge | undefined;
    let stderr = "";
    child.once("close", () => childClosed.resolve(undefined));
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
      else {
        bridge.exited = true;
        bridge.failPending(cause);
      }
    });
    try {
      const profiles = await ready.promise;
      bridge = new RustHarnessBridge(
        child,
        profiles,
        childClosed.promise,
        options.autoInitialize !== false,
      );
      bridge.stderr = stderr;
      if (options.manual !== false) await bridge.control("manualServer");
      return bridge;
    } catch (cause) {
      if (bridge !== undefined) {
        await bridge.stop();
      } else {
        if (!child.stdin.destroyed) child.stdin.end();
        if (!childClosed.settled) child.kill();
        await childClosed.promise;
      }
      throw cause;
    }
  }

  invoke<T = unknown>(
    cmd: string,
    rawArgs: Record<string, unknown> | ArrayBuffer | Uint8Array = {},
  ): Promise<T> {
    if (rawArgs instanceof ArrayBuffer || ArrayBuffer.isView(rawArgs)) {
      return Promise.reject(new Error("AppWire harness accepts JSON commands"));
    }
    this.invocations.push({ cmd, args: rawArgs });
    return this.send<T>({ kind: "invoke", cmd, args: rawArgs }).then(
      (value) => {
        const waiter = this.commandWaiters.get(cmd)?.shift();
        waiter?.resolve();
        return value;
      },
      (cause: unknown) => {
        const waiter = this.commandWaiters.get(cmd)?.shift();
        waiter?.reject(cause);
        throw cause;
      },
    );
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
    waiters.push({
      resolve: () => done.resolve(undefined),
      reject: done.reject,
    });
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
    waiters.push({
      resolve: () => done.resolve(undefined),
      reject: done.reject,
    });
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

  waitForNextClientFrameDisposition(): Promise<ClientFrameDisposition> {
    const done = deferred<ClientFrameDisposition>();
    this.clientFrameDispositionWaiters.push({
      resolve: done.resolve,
      reject: done.reject,
    });
    return done.promise;
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

  stop(): Promise<void> {
    if (this.stopPromise === null) this.stopPromise = this.stopOnce();
    return this.stopPromise;
  }

  private async stopOnce(): Promise<void> {
    if (this.exited) {
      await this.childClosed;
      return;
    }
    const shutdown = this.control("shutdown").catch(() => undefined);
    if (!this.child.stdin.destroyed) this.child.stdin.end();
    await this.childClosed;
    await shutdown;
  }

  private async sendServerFrame(frame: ServerFrame): Promise<void> {
    const delivered = deferred<void>();
    const waiter = {
      predicate: (candidate: ServerFrame) =>
        frame.id !== undefined
          ? candidate.id === frame.id
          : candidate.method === frame.method,
      resolve: () => delivered.resolve(undefined),
      reject: delivered.reject,
    };
    this.deliveredFrameWaiters.push(waiter);
    try {
      await this.control("serverFrame", { frame });
    } catch (cause) {
      const index = this.deliveredFrameWaiters.indexOf(waiter);
      if (index >= 0) this.deliveredFrameWaiters.splice(index, 1);
      delivered.reject(cause);
    }
    await delivered.promise;
  }

  private send<T>(request: JsonObject): Promise<T> {
    const id = this.nextRequestId++;
    const completion = deferred<unknown>();
    this.pending.set(id, completion);
    if (this.exited || this.child.stdin.destroyed) {
      const cause = new Error("Rust harness is closed");
      this.pending.delete(id);
      completion.reject(cause);
      return completion.promise as Promise<T>;
    }
    this.child.stdin.write(
      `${JSON.stringify({ id, ...request })}\n`,
      (cause) => {
        if (cause === null || cause === undefined) return;
        const current = this.pending.get(id);
        if (current !== completion) return;
        this.pending.delete(id);
        completion.reject(cause);
      },
    );
    return completion.promise as Promise<T>;
  }

  private receive(message: JsonObject): void {
    if (message.kind === "serverRequest") {
      const frame = message.frame as ServerFrame;
      this.serverRequests.push(frame);
      this.clientFrameDispositionWaiters.shift()?.resolve({
        kind: "accepted",
        frame,
      });
      const waiter = this.requestWaiters.get(frame.method ?? "")?.shift();
      waiter?.resolve();
      if (
        this.autoInitialize &&
        frame.method === "initialize" &&
        frame.id !== undefined
      ) {
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
    if (message.kind === "serverProtocolError") {
      this.protocolErrors.push(
        String(message.error ?? "manual server protocol error"),
      );
      this.clientFrameDispositionWaiters.shift()?.resolve({
        kind: "rejected",
        error: this.protocolErrors.at(-1) ?? "manual server protocol error",
      });
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
    for (const waiters of this.commandWaiters.values()) {
      for (const waiter of waiters) waiter.reject(cause);
    }
    this.commandWaiters.clear();
    for (const waiters of this.requestWaiters.values()) {
      for (const waiter of waiters) waiter.reject(cause);
    }
    this.requestWaiters.clear();
    const dispositions = this.clientFrameDispositionWaiters.splice(0);
    for (const waiter of dispositions) waiter.reject(cause);
    const delivered = this.deliveredFrameWaiters.splice(0);
    for (const waiter of delivered) waiter.reject(cause);
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
const TOOL_STARTED_TEXT = "tool-start-only";
const TOOL_DELTA_TEXT = "::delta-only";
const TOOL_COMPLETED_TEXT = "tool-completed-authoritative";
const AUTHORITATIVE_TEXT = "Authoritative assistant after resync";
const AUTHORITATIVE_TOOL_TEXT = "authoritative tool after resync";
const AUTHORITATIVE_NEW_TEXT = "Authoritative new item";
const AUTHORITATIVE_REASONING_SETTLED = "Reasoning settled authoritatively";

function makeThread(input: {
  id: string;
  ref: string;
  name: string;
  status: "active" | "awaiting";
  items?: ThreadItem[];
  tasks?: { total: number; done: number };
  job?: { id: string; type: string; status: string; outputBytes: number };
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
            jobId: input.job?.id ?? "job-live",
            jobType: input.job?.type ?? "shell",
            status: input.job?.status ?? "running",
            outputBytes: input.job?.outputBytes ?? 2048,
          },
        ],
      },
      queue: { depth: 0, revision: 7, preview: [] },
      tasks: input.tasks ?? { total: 3, done: 1 },
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
        text: AUTHORITATIVE_TEXT,
        status: "inProgress",
      },
      {
        type: "systemMessage",
        id: "reasoning-live",
        text: AUTHORITATIVE_REASONING_SETTLED,
      },
      {
        type: "commandExecution",
        id: "tool-live",
        toolName: "exec_command",
        callId: "call-live",
        output: AUTHORITATIVE_TOOL_TEXT,
        status: "completed",
      },
      {
        type: "agentMessage",
        id: "authoritative-new",
        text: AUTHORITATIVE_NEW_TEXT,
        status: "inProgress",
      },
    ],
    tasks: { total: 5, done: 4 },
    job: {
      id: "job-authoritative",
      type: "authoritative_job",
      status: "completed",
      outputBytes: 4096,
    },
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

function clientMutationId(request: ServerFrame): string {
  const value = (request.params as { clientMutationId?: unknown })
    .clientMutationId;
  expect(typeof value).toBe("string");
  expect(value).not.toBe("");
  return value as string;
}

function expectReceiptCorrelation(
  request: ServerFrame,
  receipt: MutationReceipt,
  kind: "send" | "steer" | "queue" | "interrupt",
): void {
  expect(receipt.clientMutationId).toBe(clientMutationId(request));
  expect(receipt.disposition).toBe("accepted");
  expect(receipt.threadId).toBe(ATTENTION_THREAD_ID);
  expect(receipt.projectionState).toBe("current");
  if (kind === "send") expect(receipt.turnId).toBe("turn-receipt");
  else expect(receipt.turnId).toBeUndefined();
  if (kind === "queue") {
    expect(receipt.queueEntryIds).toEqual(["queue-entry-live"]);
  } else {
    expect(receipt.queueEntryIds).toBeUndefined();
  }
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

function activeThreadKey(): string {
  return (
    document
      .querySelector<HTMLElement>("[data-concept-root]")
      ?.getAttribute("data-thread-key") ?? ""
  );
}

function expectUniqueOpaqueKeys(keys: readonly string[]): void {
  expect(keys.every((key) => key.length > 0)).toBe(true);
  expect(new Set(keys).size).toBe(keys.length);
  for (const key of keys) {
    expect(key).not.toContain(ATTENTION_REF);
    expect(key).not.toContain(RUNNING_REF);
  }
}

function expectRawRefsAbsentFromLiveConcept(): void {
  const root = document.querySelector<HTMLElement>("[data-concept-root]");
  expect(root).not.toBeNull();
  if (root === null) return;
  expect(root.textContent ?? "").not.toContain(ATTENTION_REF);
  expect(root.textContent ?? "").not.toContain(RUNNING_REF);
  const attributeValues = [root, ...root.querySelectorAll<HTMLElement>("*")]
    .flatMap((element) =>
      [...element.attributes].map(
        (attribute) => `${attribute.name}=${attribute.value}`,
      ),
    )
    .join("\n");
  expect(attributeValues).not.toContain(ATTENTION_REF);
  expect(attributeValues).not.toContain(RUNNING_REF);
  expect(root.outerHTML).not.toContain(ATTENTION_REF);
  expect(root.outerHTML).not.toContain(RUNNING_REF);
}

function expectConcept(className: string): void {
  expect(document.querySelectorAll("[data-concept-root]")).toHaveLength(1);
  expect(document.querySelector(`.${className}`)).not.toBeNull();
  expectRawRefsAbsentFromLiveConcept();
}

function chooseConcept(name: "Stillwater" | "Constellation" | "Field Notes") {
  fireEvent.click(screen.getByRole("button", { name: "Switch concept" }));
  fireEvent.click(screen.getByRole("button", { name: new RegExp(name) }));
}

function expectConversationEvidence(
  expectedKeys: readonly string[],
  expectedThreadKey: string,
  expectedDraft: string,
): void {
  expect(transcriptKeys()).toEqual(expectedKeys);
  expectUniqueOpaqueKeys(transcriptKeys());
  expect(activeThreadKey()).toBe(expectedThreadKey);
  expect(screen.getByText(STREAMED_TEXT)).toBeInTheDocument();
  expect(screen.getByText("Reasoning")).toBeInTheDocument();
  expect(screen.getByText(REASONING_TEXT)).toBeInTheDocument();
  expect(screen.getByText("exec_command")).toBeInTheDocument();
  expect(screen.getByText(TOOL_COMPLETED_TEXT)).toBeInTheDocument();
  expect(
    screen.queryByText(`${TOOL_STARTED_TEXT}${TOOL_DELTA_TEXT}`),
  ).toBeNull();
  const completedTool = screen
    .getByText("exec_command")
    .closest<HTMLElement>("[data-transcript-item-id]");
  expect(completedTool).not.toHaveAttribute("data-streaming", "true");
  expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue(
    expectedDraft,
  );
  expect(
    screen.getByRole("button", { name: /^Load older/ }),
  ).toBeInTheDocument();
  expectRawRefsAbsentFromLiveConcept();
}

function expectWorkEvidence(
  expectedKeys: readonly string[],
  expectedThreadKey: string,
): void {
  expect(workKeys()).toEqual(expectedKeys);
  expectUniqueOpaqueKeys(workKeys());
  expect(activeThreadKey()).toBe(expectedThreadKey);
  expect(screen.getByText("shell")).toBeInTheDocument();
  expect(screen.getByText("2 open")).toBeInTheDocument();
  expect(screen.getByText("1 done")).toBeInTheDocument();
  expect(screen.getByText("0 active")).toBeInTheDocument();
  const usage = document.querySelector("[data-work-usage]");
  expect(usage).not.toBeNull();
  expect(usage).toHaveTextContent("4.1K");
  expect(usage).toHaveTextContent("$0.42");
  expectRawRefsAbsentFromLiveConcept();
}

function keyForRenderedText(text: string): string {
  return (
    screen
      .getByText(text)
      .closest<HTMLElement>("[data-transcript-item-id]")
      ?.getAttribute("data-transcript-item-id") ?? ""
  );
}

function expectAuthoritativeConversationEvidence(
  expectedKeys: readonly string[],
  expectedThreadKey: string,
): void {
  expect(transcriptKeys()).toEqual(expectedKeys);
  expectUniqueOpaqueKeys(transcriptKeys());
  expect(activeThreadKey()).toBe(expectedThreadKey);
  expect(screen.getByText(AUTHORITATIVE_TEXT)).toBeInTheDocument();
  expect(screen.getByText(AUTHORITATIVE_NEW_TEXT)).toBeInTheDocument();
  expect(screen.getByText(AUTHORITATIVE_REASONING_SETTLED)).toBeInTheDocument();
  expect(screen.getByText(AUTHORITATIVE_TOOL_TEXT)).toBeInTheDocument();
  expect(screen.queryByText(STREAMED_TEXT)).toBeNull();
  expect(screen.queryByText(REASONING_TEXT)).toBeNull();
  expect(screen.queryByText(TOOL_COMPLETED_TEXT)).toBeNull();
  expectRawRefsAbsentFromLiveConcept();
}

function expectAuthoritativeWorkEvidence(
  expectedKeys: readonly string[],
  expectedThreadKey: string,
): void {
  expect(workKeys()).toEqual(expectedKeys);
  expectUniqueOpaqueKeys(workKeys());
  expect(activeThreadKey()).toBe(expectedThreadKey);
  expect(screen.getByText("authoritative_job")).toBeInTheDocument();
  expect(screen.queryByText("shell")).toBeNull();
  expect(screen.getByText("1 open")).toBeInTheDocument();
  expect(screen.getByText("4 done")).toBeInTheDocument();
  expect(screen.getByText("0 active")).toBeInTheDocument();
  expectRawRefsAbsentFromLiveConcept();
}

function pendingMutationElement(): HTMLElement | null {
  return document.querySelector<HTMLElement>(
    "[data-composer-pending], [data-pending-mutation]",
  );
}

function expectComposerPending(
  kind: "send" | "steer" | "queue" | "interrupt",
  expectedDraft: string,
): void {
  const pending = pendingMutationElement();
  expect(pending).not.toBeNull();
  expect(pending).toHaveTextContent(new RegExp(kind, "i"));
  const textbox = screen.getByRole("textbox", { name: "Message" });
  expect(textbox).toBeDisabled();
  expect(textbox).toHaveValue(expectedDraft);
}

function expectComposerReady(expectedDraft: string): void {
  expect(pendingMutationElement()).toBeNull();
  const textbox = screen.getByRole("textbox", { name: "Message" });
  expect(textbox).toBeEnabled();
  expect(textbox).toHaveValue(expectedDraft);
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
  expectComposerPending(mode === "Steer" ? "steer" : "queue", text);
  const kind = mode === "Steer" ? "steer" : "queue";
  const receipt = receiptFor(request, kind);
  expectReceiptCorrelation(request, receipt, kind);
  await act(async () => {
    await bridge.respond(request, {
      receipt,
    });
  });
  expectComposerReady("");
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

interface RawHarnessConnection {
  readonly connectionId: string;
  readonly channel: TauriChannel<unknown>;
}

async function openRawHarnessConnection(
  bridge: RustHarnessBridge,
): Promise<RawHarnessConnection> {
  const profileId = bridge.profiles[0];
  if (profileId === undefined) throw new Error("harness profile missing");
  const channel = bridge.createChannel<unknown>(() => undefined);
  const opened = await bridge.invoke<{
    connectionId: string;
    profileId: string;
    generation: number;
  }>("appwire_open", {
    request: { profileId },
    onEvent: channel,
  });
  return { connectionId: opened.connectionId, channel };
}

async function sendRawClientFrame(
  bridge: RustHarnessBridge,
  connection: RawHarnessConnection,
  frame: ServerFrame,
): Promise<void> {
  await bridge.invoke("appwire_send", {
    request: {
      connectionId: connection.connectionId,
      frame: JSON.stringify(frame),
    },
  });
}

async function expectRawClientFrameRejected(
  bridge: RustHarnessBridge,
  connection: RawHarnessConnection,
  frame: ServerFrame,
  expected: RegExp,
): Promise<void> {
  const disposition = bridge.waitForNextClientFrameDisposition();
  await sendRawClientFrame(bridge, connection, frame);
  const result = await disposition;
  expect(result.kind).toBe("rejected");
  if (result.kind === "rejected") expect(result.error).toMatch(expected);
}

async function expectConcurrentRawOpenRejected(
  bridge: RustHarnessBridge,
): Promise<void> {
  const profileId = bridge.profiles[0];
  if (profileId === undefined) throw new Error("harness profile missing");
  const channel = bridge.createChannel<unknown>(() => undefined);
  try {
    await expect(
      bridge.invoke("appwire_open", {
        request: { profileId },
        onEvent: channel,
      }),
    ).rejects.toThrow();
  } finally {
    channel.dispose();
  }
}

async function expectRawServerFrameRejected(
  bridge: RustHarnessBridge,
  frame: ServerFrame,
  expected: RegExp,
): Promise<void> {
  await expect(bridge.control("serverFrame", { frame })).rejects.toThrow(
    expected,
  );
}

async function completeRawHandshake(
  bridge: RustHarnessBridge,
  connection: RawHarnessConnection,
  initializeId: number,
): Promise<void> {
  const initializeCount = methodCount(bridge, "initialize") + 1;
  await sendRawClientFrame(bridge, connection, {
    id: initializeId,
    method: "initialize",
    params: {
      protocolVersion: "evener-appwire-v3",
      clientInfo: { name: "guard-test", version: "1" },
      capabilities: { experimentalApi: false },
    },
  });
  const initialize = await bridge.waitForServerRequest(
    "initialize",
    initializeCount,
  );
  await bridge.respond(initialize, INITIALIZE_RESPONSE);
  const initializedCount = methodCount(bridge, "initialized") + 1;
  await sendRawClientFrame(bridge, connection, {
    method: "initialized",
    params: {},
  });
  await bridge.waitForServerRequest("initialized", initializedCount);
}

async function completeRawPing(
  bridge: RustHarnessBridge,
  connection: RawHarnessConnection,
  id: number,
): Promise<void> {
  const count = methodCount(bridge, "ping") + 1;
  await sendRawClientFrame(bridge, connection, {
    id,
    method: "ping",
    params: {},
  });
  const ping = await bridge.waitForServerRequest("ping", count);
  await bridge.respond(ping, {});
}

async function closeRawHarnessConnection(
  bridge: RustHarnessBridge,
  connection: RawHarnessConnection | null,
): Promise<void> {
  if (connection === null) return;
  await bridge.invoke("appwire_close", {
    request: { connectionId: connection.connectionId },
  });
  connection.channel.dispose();
}

afterEach(() => {
  cleanup();
  tauriHarness.bridge = null;
  if (fakeTimersActive) {
    vi.useRealTimers();
    fakeTimersActive = false;
  }
});

describe("production App live concepts over the real native AppWire bridge", () => {
  it("rejects and reaps a missing harness process during startup", async () => {
    await expect(
      RustHarnessBridge.start({
        binary: path.join(
          process.cwd(),
          "src-tauri",
          "target",
          "debug",
          "examples",
          "missing-appwire-stdio-harness",
        ),
      }),
    ).rejects.toThrow();
  });

  it("strictly validates manual protocol shape, phase, correlation, and connection ownership", async () => {
    const bridge = await RustHarnessBridge.start({ autoInitialize: false });
    let connection: RawHarnessConnection | null = null;
    try {
      connection = await openRawHarnessConnection(bridge);

      // AwaitInitialize: the first frame must be one exact initialize request.
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 1, method: "initialize", result: {} },
        /shape|result/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { method: "evener/tree/changed", params: {} },
        /first|initialize/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { method: "initialize", params: {} },
        /request|initialize/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 2, method: "initialized", params: {} },
        /initialize request|first/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 3, method: "ping", params: {} },
        /initialize/i,
      );

      const initializeCount = methodCount(bridge, "initialize") + 1;
      await sendRawClientFrame(bridge, connection, {
        id: 41,
        method: "initialize",
        params: {
          protocolVersion: "evener-appwire-v3",
          clientInfo: { name: "guard-test", version: "1" },
          capabilities: { experimentalApi: false },
        },
      });
      const initialize = await bridge.waitForServerRequest(
        "initialize",
        initializeCount,
      );

      // AwaitInitializeResponse: no second client frame is legal.
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 42, method: "initialize", params: {} },
        /response|additional|initialize/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 43, method: "ping", params: {} },
        /response|additional/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { method: "evener/tree/changed", params: {} },
        /response|additional/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { method: "initialized", params: {} },
        /response|initialize/i,
      );

      // Server responses are mutually exclusive and must correlate.
      await expectRawServerFrameRejected(
        bridge,
        { id: 41, result: {}, error: { code: -1, message: "both" } },
        /exactly one|result.*error/i,
      );
      await expectRawServerFrameRejected(
        bridge,
        { id: 41, method: "initialize", result: {} },
        /shape|method/i,
      );
      await expectRawServerFrameRejected(
        bridge,
        { id: 41 },
        /exactly one|result|error/i,
      );
      await expectRawServerFrameRejected(
        bridge,
        { method: "evener/tree/changed", result: {} },
        /shape|result/i,
      );
      await expectRawServerFrameRejected(
        bridge,
        { id: 999, result: {} },
        /unknown|unmatched/i,
      );

      await bridge.respond(initialize, INITIALIZE_RESPONSE);

      // AwaitInitialized: only exact id-less initialized is legal from client.
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 44, method: "ping", params: {} },
        /initialized/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 45, method: "initialize", params: {} },
        /initialized|repeat|reserved/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 46, method: "initialized", params: {} },
        /notification|initialized/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { method: "initialize", params: {} },
        /initialized|initialize/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { method: "evener/tree/changed", params: {} },
        /initialized/i,
      );
      await expect(
        bridge.notify(notification("evener/tree/changed", {})),
      ).rejects.toThrow(/initialized/i);

      const initializedCount = methodCount(bridge, "initialized") + 1;
      await sendRawClientFrame(bridge, connection, {
        method: "initialized",
        params: {},
      });
      await bridge.waitForServerRequest("initialized", initializedCount);

      // Ready rejects reserved/mixed client frames but accepts ordinary ones.
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 47, method: "initialize", params: {} },
        /repeat|reserved|initialize/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 48, method: "initialized", params: {} },
        /reserved|notification|initialized/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { method: "initialize", params: {} },
        /reserved|initialize/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { method: "initialized", params: {} },
        /repeat|reserved|initialized/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 49, result: {} },
        /shape|method/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { id: 49, method: "ping", result: {} },
        /shape|result/i,
      );
      await expectRawClientFrameRejected(
        bridge,
        connection,
        { method: "evener/tree/changed", error: { code: -1 } },
        /shape|error/i,
      );

      const clientNotificationCount =
        methodCount(bridge, "evener/tree/changed") + 1;
      await sendRawClientFrame(bridge, connection, {
        method: "evener/tree/changed",
        params: {},
      });
      await bridge.waitForServerRequest(
        "evener/tree/changed",
        clientNotificationCount,
      );

      const pingCount = methodCount(bridge, "ping") + 1;
      await sendRawClientFrame(bridge, connection, {
        id: 50,
        method: "ping",
        params: {},
      });
      const ping = await bridge.waitForServerRequest("ping", pingCount);
      await expectRawServerFrameRejected(
        bridge,
        { id: 50, result: {}, error: { code: -1, message: "both" } },
        /exactly one|result.*error/i,
      );
      await expectRawServerFrameRejected(
        bridge,
        { id: 50, method: "ping", result: {} },
        /shape|method/i,
      );
      await expectRawServerFrameRejected(
        bridge,
        { id: 50 },
        /exactly one|result|error/i,
      );
      await expectRawServerFrameRejected(
        bridge,
        { method: "evener/tree/changed", result: {} },
        /shape|result/i,
      );
      await expectRawServerFrameRejected(
        bridge,
        { id: 999, result: {} },
        /unknown|unmatched/i,
      );
      await bridge.respond(ping, {});
      await bridge.notify(notification("evener/tree/changed", {}));

      // A second concurrent manual connection must not reset this owner.
      await expectConcurrentRawOpenRejected(bridge);
      await completeRawPing(bridge, connection, 51);

      // Ending owner one drops only its pending IDs. A sequential connection
      // may start a fresh phase and reuse the old owner's still-pending ID.
      const abandonedPingCount = methodCount(bridge, "ping") + 1;
      await sendRawClientFrame(bridge, connection, {
        id: 70,
        method: "ping",
        params: {},
      });
      await bridge.waitForServerRequest("ping", abandonedPingCount);
      await closeRawHarnessConnection(bridge, connection);
      connection = null;
      connection = await openRawHarnessConnection(bridge);
      await completeRawHandshake(bridge, connection, 70);
      await completeRawPing(bridge, connection, 71);
      await bridge.notify(notification("evener/tree/changed", {}));
    } finally {
      const closed = closeRawHarnessConnection(bridge, connection);
      const stopped = bridge.stop();
      expect(bridge.stop()).toBe(stopped);
      const [closeResult, stopResult] = await Promise.allSettled([
        closed,
        stopped,
      ]);
      expect(stopResult.status).toBe("fulfilled");
      expect(closeResult.status).toBe("fulfilled");
    }
  });

  it("refuses to switch a connected built-in server into manual mode", async () => {
    const bridge = await RustHarnessBridge.start({ manual: false });
    let connection: RawHarnessConnection | null = null;
    try {
      connection = await openRawHarnessConnection(bridge);
      await expect(bridge.control("manualServer")).rejects.toThrow(
        /before connection|traffic/i,
      );
    } finally {
      const closed = closeRawHarnessConnection(bridge, connection);
      const stopped = bridge.stop();
      const [closeResult, stopResult] = await Promise.allSettled([
        closed,
        stopped,
      ]);
      expect(stopResult.status).toBe("fulfilled");
      expect(closeResult.status).toBe("fulfilled");
    }
  });

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
      expectUniqueOpaqueKeys(stableRosterKeys);

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
            output: TOOL_STARTED_TEXT,
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
          delta: TOOL_DELTA_TEXT,
        }),
      );
      fireEvent.click(screen.getByRole("button", { name: "exec_command" }));
      expect(
        screen.getByText(`${TOOL_STARTED_TEXT}${TOOL_DELTA_TEXT}`),
      ).toBeInTheDocument();
      const runningTool = screen
        .getByText("exec_command")
        .closest<HTMLElement>("[data-transcript-item-id]");
      expect(runningTool).toHaveAttribute("data-streaming", "true");
      await sendNotification(
        bridge,
        notification("item/completed", {
          ...common,
          item: {
            type: "commandExecution",
            id: "tool-live",
            toolName: "exec_command",
            callId: "call-live",
            output: TOOL_COMPLETED_TEXT,
            status: "completed",
          },
        }),
      );
      expect(methodCount(bridge, "thread/read")).toBe(1);
      expect(screen.getByText(TOOL_COMPLETED_TEXT)).toBeInTheDocument();
      expect(
        screen.queryByText(`${TOOL_STARTED_TEXT}${TOOL_DELTA_TEXT}`),
      ).toBeNull();
      const completedTool = screen
        .getByText("exec_command")
        .closest<HTMLElement>("[data-transcript-item-id]");
      expect(completedTool).toHaveAttribute("data-streaming", "false");

      fireEvent.click(screen.getByRole("button", { name: "Reasoning" }));
      fireEvent.change(screen.getByRole("textbox", { name: "Message" }), {
        target: { value: DRAFT_SENTINEL },
      });
      const stableTranscriptKeys = transcriptKeys();
      expect(stableTranscriptKeys).toHaveLength(4);
      expectUniqueOpaqueKeys(stableTranscriptKeys);
      const stableThreadKey = activeThreadKey();
      expectUniqueOpaqueKeys([stableThreadKey]);
      const notificationReasoningKey = keyForRenderedText(REASONING_TEXT);
      expect(notificationReasoningKey).not.toBe("");
      expectConversationEvidence(
        stableTranscriptKeys,
        stableThreadKey,
        DRAFT_SENTINEL,
      );

      chooseConcept("Constellation");
      expectConcept("concept-constellation");
      expectConversationEvidence(
        stableTranscriptKeys,
        stableThreadKey,
        DRAFT_SENTINEL,
      );
      chooseConcept("Field Notes");
      expectConcept("concept-field-notes");
      expectConversationEvidence(
        stableTranscriptKeys,
        stableThreadKey,
        DRAFT_SENTINEL,
      );

      fireEvent.click(screen.getByRole("button", { name: "Work" }));
      const stableWorkKeys = workKeys();
      expect(stableWorkKeys).toHaveLength(1);
      expectWorkEvidence(stableWorkKeys, stableThreadKey);
      chooseConcept("Stillwater");
      expectConcept("concept-stillwater");
      expectWorkEvidence(stableWorkKeys, stableThreadKey);
      chooseConcept("Constellation");
      expectConcept("concept-constellation");
      expectWorkEvidence(stableWorkKeys, stableThreadKey);
      chooseConcept("Field Notes");
      expectConcept("concept-field-notes");
      expectWorkEvidence(stableWorkKeys, stableThreadKey);
      expectUniqueOpaqueKeys([
        ...stableRosterKeys,
        stableThreadKey,
        ...stableTranscriptKeys,
        ...stableWorkKeys,
      ]);
      fireEvent.click(screen.getByRole("button", { name: "Close" }));
      expectConversationEvidence(
        stableTranscriptKeys,
        stableThreadKey,
        DRAFT_SENTINEL,
      );

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
      expectComposerPending("send", DRAFT_SENTINEL);

      chooseConcept("Stillwater");
      expectComposerPending("send", DRAFT_SENTINEL);
      chooseConcept("Constellation");
      expectComposerPending("send", DRAFT_SENTINEL);
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

      const sendReceipt = receiptFor(pendingSend, "send");
      expectReceiptCorrelation(pendingSend, sendReceipt, "send");
      await act(async () => {
        await bridge.respond(pendingSend, {
          turn: {
            id: "turn-receipt",
            itemsView: "full",
            status: "running",
          },
          receipt: sendReceipt,
        });
      });
      expectComposerReady("");

      const steer = await submitMutation(
        bridge,
        "Steer",
        "steer-sentinel::production-appwire",
        "turn/steer",
      );
      const queue = await submitMutation(
        bridge,
        "Queue",
        "queue-sentinel::production-appwire",
        "turn/queue",
      );
      const interruptDraft = "interrupt-draft::production-appwire";
      fireEvent.change(screen.getByRole("textbox", { name: "Message" }), {
        target: { value: interruptDraft },
      });
      expectComposerReady(interruptDraft);
      const interruptCount = methodCount(bridge, "turn/interrupt") + 1;
      fireEvent.click(screen.getByRole("button", { name: "Interrupt" }));
      const interrupt = await bridge.waitForServerRequest(
        "turn/interrupt",
        interruptCount,
      );
      expect(interrupt.params).toMatchObject({ ref: ATTENTION_REF });
      expectComposerPending("interrupt", interruptDraft);
      const interruptReceipt = receiptFor(interrupt, "interrupt");
      expectReceiptCorrelation(interrupt, interruptReceipt, "interrupt");
      await act(async () => {
        await bridge.respond(interrupt, {
          receipt: interruptReceipt,
        });
      });
      expectComposerReady(interruptDraft);
      expectUniqueOpaqueKeys([
        clientMutationId(pendingSend),
        clientMutationId(steer),
        clientMutationId(queue),
        clientMutationId(interrupt),
      ]);
      fireEvent.change(screen.getByRole("textbox", { name: "Message" }), {
        target: { value: "" },
      });
      expectComposerReady("");

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
      const authoritativeTranscriptKeys = transcriptKeys();
      expect(authoritativeTranscriptKeys).toHaveLength(5);
      expectUniqueOpaqueKeys(authoritativeTranscriptKeys);
      expect(authoritativeTranscriptKeys).not.toEqual(stableTranscriptKeys);
      expect(authoritativeTranscriptKeys).toContain(notificationReasoningKey);
      expect(
        authoritativeTranscriptKeys.filter((key) =>
          stableTranscriptKeys.includes(key),
        ),
      ).toHaveLength(4);
      expectAuthoritativeConversationEvidence(
        authoritativeTranscriptKeys,
        stableThreadKey,
      );

      chooseConcept("Field Notes");
      expectConcept("concept-field-notes");
      expectAuthoritativeConversationEvidence(
        authoritativeTranscriptKeys,
        stableThreadKey,
      );
      chooseConcept("Stillwater");
      expectConcept("concept-stillwater");
      expectAuthoritativeConversationEvidence(
        authoritativeTranscriptKeys,
        stableThreadKey,
      );

      fireEvent.click(screen.getByRole("button", { name: "Work" }));
      const authoritativeWorkKeys = workKeys();
      expect(authoritativeWorkKeys).toHaveLength(1);
      expect(authoritativeWorkKeys).not.toEqual(stableWorkKeys);
      expectAuthoritativeWorkEvidence(authoritativeWorkKeys, stableThreadKey);
      chooseConcept("Constellation");
      expectConcept("concept-constellation");
      expectAuthoritativeWorkEvidence(authoritativeWorkKeys, stableThreadKey);
      chooseConcept("Field Notes");
      expectConcept("concept-field-notes");
      expectAuthoritativeWorkEvidence(authoritativeWorkKeys, stableThreadKey);
      expectUniqueOpaqueKeys([
        ...stableRosterKeys,
        stableThreadKey,
        ...authoritativeTranscriptKeys,
        ...authoritativeWorkKeys,
      ]);
      fireEvent.click(screen.getByRole("button", { name: "Close" }));
      expectAuthoritativeConversationEvidence(
        authoritativeTranscriptKeys,
        stableThreadKey,
      );

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
        const closeCompleted = bridge.waitForCommand("appwire_close");
        await act(async () => {
          cleanup();
        });
        const closeInitiated = bridge.invocations.some(
          ({ cmd }) => cmd === "appwire_close",
        );
        if (!closeInitiated) void closeCompleted.catch(() => undefined);
        const stopped = bridge.stop();
        if (closeInitiated) {
          const [closeResult, stopResult] = await Promise.allSettled([
            closeCompleted,
            stopped,
          ]);
          expect(stopResult.status).toBe("fulfilled");
          expect(closeResult.status).toBe("fulfilled");
        } else {
          await stopped;
          expect(closeInitiated).toBe(true);
        }
      } else {
        await bridge.stop();
      }
      tauriHarness.bridge = null;
    }
  });
});
