import { type ChildProcessWithoutNullStreams, spawn } from "node:child_process";
import path from "node:path";
import { createInterface } from "node:readline";
import { describe, expect, it } from "vitest";
import { createAppwireClient } from "./appwireSocket";
import type { TauriBridge, TauriChannel } from "./tauri";

type JsonObject = Record<string, unknown>;

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (cause: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

class RustHarnessBridge implements TauriBridge {
  readonly invocations: Array<{ cmd: string; args: JsonObject }> = [];
  readonly channelEvents: JsonObject[] = [];
  readonly profiles: readonly string[];
  private readonly child: ChildProcessWithoutNullStreams;
  private readonly pending = new Map<
    number,
    { resolve(value: unknown): void; reject(cause: unknown): void }
  >();
  private readonly channels = new Map<number, TauriChannel<unknown>>();
  private readonly commandWaiters = new Map<string, Array<() => void>>();
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
      if (!bridge) {
        ready.reject(
          new Error(`Rust harness exited before ready (${code}): ${stderr}`),
        );
      } else {
        bridge.failPending(
          new Error(`Rust harness exited (${code}): ${bridge.stderr}`),
        );
      }
    });
    const profiles = await ready.promise;
    bridge = new RustHarnessBridge(child, profiles);
    bridge.stderr = stderr;
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

  listen(): Promise<() => void> {
    return Promise.resolve(() => {});
  }

  control<T = unknown>(action: string): Promise<T> {
    return this.send<T>({ kind: "control", action });
  }

  waitForCommand(command: string): Promise<void> {
    const done = deferred<void>();
    const waiters = this.commandWaiters.get(command) ?? [];
    waiters.push(() => done.resolve(undefined));
    this.commandWaiters.set(command, waiters);
    return done.promise;
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

  private send<T>(request: JsonObject): Promise<T> {
    const id = this.nextRequestId++;
    const completion = deferred<unknown>();
    this.pending.set(id, completion);
    this.child.stdin.write(`${JSON.stringify({ id, ...request })}\n`);
    return completion.promise as Promise<T>;
  }

  private receive(message: JsonObject): void {
    if (message.kind === "channel") {
      this.channelEvents.push(message.payload as JsonObject);
      const channel = this.channels.get(message.channelId as number);
      channel?.onmessage(message.payload);
      return;
    }
    if (message.kind !== "response") return;
    const id = message.id as number;
    const completion = this.pending.get(id);
    if (!completion) return;
    this.pending.delete(id);
    if (message.ok === true) completion.resolve(message.value);
    else completion.reject(new Error("Rust harness command failed"));
  }

  private failPending(cause: Error): void {
    for (const completion of this.pending.values()) completion.reject(cause);
    this.pending.clear();
  }
}

interface ServerSnapshot {
  readonly connectionCount: number;
  readonly activeSockets: number;
  readonly maxActiveSockets: number;
  readonly initializedCount: number;
  readonly clientInfos: Array<{ name: string; version: string }>;
  readonly requestMethods: string[];
}

describe("AppwireClient + TauriSocket + real Rust command/AppwireManager", () => {
  it("performs real handshake, correlation, reconnect, profile switch, and stale rejection", async () => {
    const bridge = await RustHarnessBridge.start();
    try {
      const [profileOne, profileTwo] = bridge.profiles;
      expect(profileOne).toBeDefined();
      expect(profileTwo).toBeDefined();
      const rejectedChannel = bridge.createChannel<unknown>(() => undefined);
      await expect(
        bridge.invoke("appwire_open", {
          request: { profileId: profileOne, token: "must-be-rejected" },
          onEvent: rejectedChannel,
        }),
      ).rejects.toThrow("Rust harness command failed");
      rejectedChannel.dispose();
      expect(bridge.activeChannelCount()).toBe(0);
      const notifications: string[] = [];
      const firstCurrent = deferred<void>();
      const client = createAppwireClient({
        bridge,
        url: "ignored-by-native-adapter",
        profileId: profileOne as string,
      });
      client.onNotification((notification) => {
        notifications.push(notification.method);
        if (notification.method === "evener/tree/changed") {
          firstCurrent.resolve(undefined);
        }
      });
      await client.connect();
      await firstCurrent.promise;
      await expect(client.request("ping", {})).resolves.toEqual({});

      let snapshot = await bridge.control<ServerSnapshot>("snapshot");
      expect(snapshot.clientInfos[0]).toEqual({
        name: "evener-mobile",
        version: "0.1.0",
      });
      expect(snapshot.initializedCount).toBe(1);
      expect(snapshot.requestMethods).toContain("initialize");
      expect(snapshot.requestMethods).toContain("initialized");
      expect(snapshot.requestMethods).toContain("ping");
      expect(snapshot.maxActiveSockets).toBe(1);

      const reconnecting = deferred<void>();
      const reconnected = deferred<void>();
      const secondCurrent = deferred<void>();
      const stopState = client.onStateChange((state) => {
        if (state === "reconnecting") reconnecting.resolve(undefined);
      });
      const stopReady = client.onReady(() => reconnected.resolve(undefined));
      client.onNotification((notification) => {
        if (notification.method === "evener/tree/changed") {
          secondCurrent.resolve(undefined);
        }
      });
      await bridge.control("serverClose");
      await reconnecting.promise;
      client.retryNow();
      await reconnected.promise;
      await secondCurrent.promise;
      stopState();
      stopReady();

      await bridge.control("releaseHeld");
      expect(notifications).not.toContain("thread/closed");
      snapshot = await bridge.control<ServerSnapshot>("snapshot");
      expect(snapshot.connectionCount).toBe(2);
      expect(snapshot.activeSockets).toBe(1);
      expect(snapshot.maxActiveSockets).toBe(1);
      expect(snapshot.initializedCount).toBe(2);

      const backendClose = bridge.waitForCommand("appwire_close");
      client.close();
      await backendClose;
      await bridge.invoke("profile_select", {
        request: { profileId: profileTwo },
      });

      const profileTwoNotifications: string[] = [];
      const profileTwoCurrent = deferred<void>();
      const secondClient = createAppwireClient({
        bridge,
        url: "ignored-by-native-adapter",
        profileId: profileTwo as string,
      });
      secondClient.onNotification((notification) => {
        profileTwoNotifications.push(notification.method);
        if (notification.method === "evener/tree/changed") {
          profileTwoCurrent.resolve(undefined);
        }
      });
      await secondClient.connect();
      await profileTwoCurrent.promise;
      await bridge.control("releaseHeld");
      expect(profileTwoNotifications).toEqual(["evener/tree/changed"]);
      expect(notifications).not.toContain("thread/closed");

      snapshot = await bridge.control<ServerSnapshot>("snapshot");
      expect(snapshot.connectionCount).toBe(3);
      expect(snapshot.activeSockets).toBe(1);
      expect(snapshot.maxActiveSockets).toBe(1);
      expect(snapshot.initializedCount).toBe(3);
      const generations = new Set(
        bridge.channelEvents
          .map((event) => event.generation)
          .filter((generation): generation is number =>
            Number.isSafeInteger(generation),
          ),
      );
      expect([...generations].sort((left, right) => left - right)).toEqual([
        1, 2, 3,
      ]);

      const finalClose = bridge.waitForCommand("appwire_close");
      secondClient.close();
      await finalClose;
      await bridge.invoke("profile_select", {
        request: { profileId: profileOne },
      });
      snapshot = await bridge.control<ServerSnapshot>("snapshot");
      expect(snapshot.activeSockets).toBe(0);
      expect(bridge.activeChannelCount()).toBe(0);
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
      await bridge.stop();
    }
  });
});
