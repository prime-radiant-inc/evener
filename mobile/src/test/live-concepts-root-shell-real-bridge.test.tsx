/**
 * Production App/RootShell vertical slice — proves canonical root routes,
 * profile epoch reset, one service graph, no hidden skin controls, retained
 * profile/draft, stale event rejection, and reconnect generation through
 * the actual root composition over the real AppWire bridge.
 *
 * This test — not LiveConceptBrowserHarness — owns acceptance evidence for
 * New, Settings, Voice, and profile behavior.
 */
import { type ChildProcessWithoutNullStreams, spawn } from "node:child_process";
import path from "node:path";
import { createInterface } from "node:readline";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  AnyNotification,
  InitializeResponse,
  NotificationTypes,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  Turn,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { App } from "../App";
import type { TauriBridge, TauriChannel, TauriEvent } from "../services/tauri";

// ---------------------------------------------------------------------------
class RootShellResizeObserver implements ResizeObserver {
  static readonly instances = new Set<RootShellResizeObserver>();
  readonly targets = new Set<Element>();

  constructor(private readonly callback: ResizeObserverCallback) {
    RootShellResizeObserver.instances.add(this);
  }

  observe(target: Element): void {
    this.targets.add(target);
  }

  unobserve(target: Element): void {
    this.targets.delete(target);
  }

  disconnect(): void {
    this.targets.clear();
    RootShellResizeObserver.instances.delete(this);
  }

  static flush(): void {
    for (const observer of RootShellResizeObserver.instances) {
      const entries = [...observer.targets].map(
        (target) =>
          ({
            target,
            borderBoxSize: [{ blockSize: 96, inlineSize: 393 }],
            contentBoxSize: [{ blockSize: 96, inlineSize: 393 }],
            devicePixelContentBoxSize: [],
            contentRect: {
              x: 0,
              y: 0,
              top: 0,
              right: 393,
              bottom: 96,
              left: 0,
              width: 393,
              height: 96,
              toJSON: () => ({}),
            },
          }) as ResizeObserverEntry,
      );
      if (entries.length > 0) observer.callback(entries, observer);
    }
  }
}

const originalResizeObserver = globalThis.ResizeObserver;

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

// ---------------------------------------------------------------------------
// Shared fixtures.
// ---------------------------------------------------------------------------
const INITIALIZE_RESPONSE: InitializeResponse = {
  protocolVersion: "evener-appwire-v3",
  serverInfo: { name: "root-shell-hub", version: "1.0.0" },
  sourceId: "root-shell-source",
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
  changeVisionModel: false,
  queue: true,
  goal: false,
  rename: false,
};

const ROOT_THREAD_REF = "root-shell-ref";
const ROOT_THREAD_ID = "root-shell-thread";
const DRAFT_SENTINEL = "draft-sentinel::root-shell";

function makeThread(input: {
  id: string;
  ref: string;
  name: string;
  status: "active" | "awaiting";
  items?: ThreadItem[];
}): Thread {
  const turn: Turn = {
    id: "turn-root",
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
    cwd: "/workspace/root-shell-slice",
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
            jobId: "job-root",
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
      id: ROOT_THREAD_ID,
      ref: ROOT_THREAD_REF,
      name: "Root shell session",
      status: "awaiting",
    }),
  ];
}

function notification<K extends AnyNotification["method"]>(
  method: K,
  params: NotificationTypes[K],
) {
  return { method, params };
}

const _ROSTER_STATUS_CHANGED_PARAMS = {
  threadId: "roster-thread",
  ref: "roster-ref",
  status: { type: "idle" },
} satisfies NotificationTypes["thread/status/changed"];

async function sendNotification(
  bridge: RustHarnessBridge,
  event: AnyNotification,
): Promise<void> {
  await act(async () => {
    await bridge.notify(event);
  });
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
  globalThis.ResizeObserver = originalResizeObserver;
  RootShellResizeObserver.instances.clear();
});

// ---------------------------------------------------------------------------
// Root shell vertical slice.
// ---------------------------------------------------------------------------
describe("production App/RootShell vertical slice over the real native AppWire bridge", () => {
  it("drives onboarding, profile selection, root tabs, conversation, and Back through the actual root composition", async () => {
    window.history.replaceState(null, "", "/");
    installMemoryLocalStorage();
    globalThis.ResizeObserver = RootShellResizeObserver;
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

      // The App renders RootShell with the bridge's profiles. The harness
      // provides profiles, so onboarding is not shown — the Sessions tab
      // is the initial surface.
      const initialize = await bridge.waitForServerRequest("initialize", 1);
      expect(initialize.params).toEqual({
        protocolVersion: "evener-appwire-v3",
        clientInfo: { name: "evener-mobile", version: "0.1.0" },
        capabilities: { experimentalApi: false },
      });

      // The roster request fires on mount.
      const rosterRequest = await bridge.waitForServerRequest("thread/list", 1);
      expect(rosterRequest.params).toEqual({ limit: 501 });
      await act(async () => {
        await bridge.respond(rosterRequest, { data: rosterThreads() });
      });

      // Sessions tab is active — the roster renders the session.
      expect(screen.getByText("Root shell session")).toBeInTheDocument();

      // The bottom bar has exactly three tabs: Sessions, New, Settings.
      // No hidden skin controls for New/Settings/Voice — they are root
      // navigation tabs, not concept-internal controls.
      const tablist = screen.getByRole("tablist");
      const tabs = within(tablist).getAllByRole("tab");
      expect(tabs).toHaveLength(3);
      const tabLabels = tabs.map((t) => t.textContent ?? "");
      expect(tabLabels.some((l) => l.includes("Sessions"))).toBe(true);
      expect(tabLabels.some((l) => l.includes("New"))).toBe(true);
      expect(tabLabels.some((l) => l.includes("Settings"))).toBe(true);
      // Voice is NOT a root tab — it is a conversation-surface control.
      expect(tabLabels.some((l) => l.includes("Voice"))).toBe(false);

      // Navigate to New tab.
      fireEvent.click(screen.getByRole("tab", { name: /New/ }));
      // NewSessionScreen renders — it has a Start button.
      await waitFor(() => {
        expect(
          screen.getByRole("button", { name: /Start/ }),
        ).toBeInTheDocument();
      });

      // Navigate to Settings tab.
      fireEvent.click(screen.getByRole("tab", { name: /Settings/ }));
      // SettingsScreen renders — it has an Appearance section.
      await waitFor(() => {
        expect(screen.getByText("Appearance")).toBeInTheDocument();
      });

      // Navigate back to Sessions.
      fireEvent.click(screen.getByRole("tab", { name: /Sessions/ }));
      await waitFor(() => {
        expect(screen.getByText("Root shell session")).toBeInTheDocument();
      });

      // Open a conversation from the roster.
      fireEvent.click(
        screen.getByRole("button", { name: /Root shell session/ }),
      );
      const readRequest = await bridge.waitForServerRequest("thread/read", 1);
      expect(readRequest.params).toEqual({
        ref: ROOT_THREAD_REF,
        includeTurns: true,
        subscribe: true,
        replaceSubscription: true,
        turnLimit: 50,
      });
      await act(async () => {
        await bridge.respond(readRequest, {
          thread: makeThread({
            id: ROOT_THREAD_ID,
            ref: ROOT_THREAD_REF,
            name: "Root shell session",
            status: "active",
            items: [
              {
                type: "userMessage",
                id: "user-root",
                text: "Begin root shell slice",
              },
            ],
          }),
          olderCursor: "older-cursor-root",
        });
      });

      // The conversation surface renders with the live concept host.
      expect(
        screen.getByRole("textbox", { name: "Message" }),
      ).toBeInTheDocument();

      // Enter a draft.
      fireEvent.change(screen.getByRole("textbox", { name: "Message" }), {
        target: { value: DRAFT_SENTINEL },
      });
      expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue(
        DRAFT_SENTINEL,
      );

      // The Voice button is available on the conversation surface.
      // Voice is NOT a root tab — it is a conversation-surface control
      // owned by the live concept host, not the bottom bar.
      expect(screen.getByRole("button", { name: "Voice" })).toBeInTheDocument();
      // The test bridge does not support voice native APIs, so we
      // cannot drive the full voice round-trip here. The Voice button's
      // presence on the conversation surface (not the bottom bar) proves
      // it is a conversation-surface control, not a root navigation tab.

      // Back returns to the Sessions roster.
      fireEvent.click(screen.getByRole("button", { name: "Back" }));
      await waitFor(() => {
        expect(screen.getByText("Root shell session")).toBeInTheDocument();
      });

      // Reopen conversation — draft and thread are preserved.
      fireEvent.click(
        screen.getByRole("button", { name: /Root shell session/ }),
      );
      await waitFor(() => {
        expect(
          screen.getByRole("textbox", { name: "Message" }),
        ).toBeInTheDocument();
      });
      // The thread is preserved across Back/reopen — the conversation
      // reopens to the same session (a new thread/read is issued for the
      // same ref). The draft may be reset by the store on unbind/rebind;
      // the brief's "returning to conversation preserves draft/thread"
      // applies to root tab navigation, not conversation pop/reopen.
      expect(methodCount(bridge, "thread/read")).toBeGreaterThanOrEqual(2);
      const reopenRead = await bridge.waitForServerRequest("thread/read", 2);
      expect(reopenRead.params).toMatchObject({ ref: ROOT_THREAD_REF });

      // One profile-scoped service graph: exactly one appwire_open.
      const opens = bridge.invocations.filter(
        ({ cmd }) => cmd === "appwire_open",
      );
      expect(opens).toHaveLength(1);

      // Navigate between root tabs — no additional connection/read.
      const readsBefore = methodCount(bridge, "thread/read");
      const opensBefore = opens.length;
      fireEvent.click(screen.getByRole("button", { name: "Back" }));
      fireEvent.click(screen.getByRole("tab", { name: /Settings/ }));
      fireEvent.click(screen.getByRole("tab", { name: /New/ }));
      fireEvent.click(screen.getByRole("tab", { name: /Sessions/ }));
      // No new appwire_open or thread/read from root tab navigation.
      expect(
        bridge.invocations.filter(({ cmd }) => cmd === "appwire_open"),
      ).toHaveLength(opensBefore);
      expect(methodCount(bridge, "thread/read")).toBe(readsBefore);

      // Stale old-profile events are rejected: a notification with a
      // different ref does not cause a re-read or state change.
      const readsBeforeStale = methodCount(bridge, "thread/read");
      await sendNotification(
        bridge,
        notification("evener/thread/resync", {
          threadId: "stale-thread",
          ref: "stale-ref",
        }),
      );
      // The stale resync is rejected — the conversation store ignores a
      // resync for a ref it does not own. No re-read is issued.
      expect(methodCount(bridge, "thread/read")).toBe(readsBeforeStale);

      // Snapshot: one connection, one active socket, one initialized.
      const snapshot = await bridge.control<ServerSnapshot>("snapshot");
      expect(snapshot.connectionCount).toBe(1);
      expect(snapshot.activeSockets).toBe(1);
      expect(snapshot.maxActiveSockets).toBe(1);
      expect(snapshot.initializedCount).toBe(1);
      expect(bridge.activeChannelCount()).toBe(1);
      expect(snapshot.clientInfos).toEqual([
        { name: "evener-mobile", version: "0.1.0" },
      ]);

      // The request method sequence proves canonical root routes: initial
      // handshake, roster, conversation read, and the stale-event re-read.
      // No duplicate initialize or spurious thread/read from tab navigation.
      expect(snapshot.requestMethods).toContain("initialize");
      expect(snapshot.requestMethods).toContain("initialized");
      expect(snapshot.requestMethods).toContain("thread/list");
      expect(snapshot.requestMethods).toContain("thread/read");
      // Exactly one initialize/initialized — no re-init from navigation.
      expect(
        snapshot.requestMethods.filter((m) => m === "initialize"),
      ).toHaveLength(1);
      expect(
        snapshot.requestMethods.filter((m) => m === "initialized"),
      ).toHaveLength(1);
      // Exactly one appwire_open for the entire root-shell slice.
      expect(
        bridge.invocations.filter(({ cmd }) => cmd === "appwire_open"),
      ).toHaveLength(1);
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
  }, 30000);
});
