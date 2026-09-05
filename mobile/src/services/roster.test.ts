// RosterService tests with a scripted fake client. Covers the generation-2
// roster contract:
// - list() requests thread/list with { limit: 501 } and NEVER sends a cursor
// - 501 threads => 500 returned, hasMore === true
// - <=500 threads => all returned, hasMore === false, single request
// - projection: ref, title (name|preview), project (cwd), status, updatedAt
// - attention classification: awaiting/askPending => needsYou, active => running, else recent
// - refresh() delegates to list()
// - errors propagate

import { describe, expect, it } from "vitest";
import type {
  AnyNotification,
  MethodName,
  MethodTypes,
  Thread,
  ThreadListResponse,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { createRosterService, type RosterEntry } from "./roster";

// --- scripted fake client (records requests) --------------------------------

type RequestHandler = (params: unknown) => unknown | Promise<unknown>;

class ScriptedClient {
  readonly requests: { method: string; params: unknown }[] = [];
  private readonly handlers = new Map<string, RequestHandler>();

  on<M extends MethodName>(
    method: M,
    handler: (
      params: MethodTypes[M]["params"],
    ) => MethodTypes[M]["result"] | Promise<MethodTypes[M]["result"]>,
  ): void {
    this.handlers.set(method, handler as unknown as RequestHandler);
  }

  request<M extends MethodName>(
    method: M,
    params: MethodTypes[M]["params"],
  ): Promise<MethodTypes[M]["result"]> {
    this.requests.push({ method, params });
    const handler = this.handlers.get(method);
    if (!handler) {
      return Promise.reject(
        new Error(`ScriptedClient: no handler for "${method}"`),
      );
    }
    return Promise.resolve().then(
      () => handler(params) as MethodTypes[M]["result"],
    );
  }

  onNotification(_cb: (n: AnyNotification) => void): () => void {
    return () => {};
  }
}

// --- thread factory ---------------------------------------------------------

function makeThread(over: Partial<Thread> = {}): Thread {
  return {
    id: "thread-1",
    sessionId: "session-1",
    preview: "hello world",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1_000_000,
    updatedAt: 1_000_000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "local",
    evener: {
      ref: "ref-1",
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

function makeThreads(count: number, prefix = "t"): Thread[] {
  const threads: Thread[] = [];
  for (let i = 0; i < count; i++) {
    threads.push(
      makeThread({
        id: `${prefix}-${i}`,
        name: `Session ${i}`,
        updatedAt: 1_000_000 + i,
        evener: {
          ref: `ref-${prefix}-${i}`,
          capabilities: {} as never,
          queue: { revision: 0 },
        },
      }),
    );
  }
  return threads;
}

// --- tests ------------------------------------------------------------------

describe("RosterService", () => {
  it("list() calls thread/list with { limit: 501 }", async () => {
    const client = new ScriptedClient();
    client.on("thread/list", () => ({ data: [] }) as ThreadListResponse);

    const service = createRosterService(client);
    await service.list();

    expect(client.requests).toEqual([
      { method: "thread/list", params: { limit: 501 } },
    ]);
  });

  it("returns 500 threads and hasMore=true when 501 are returned", async () => {
    const client = new ScriptedClient();
    client.on(
      "thread/list",
      () =>
        ({
          data: makeThreads(501),
        }) as ThreadListResponse,
    );

    const service = createRosterService(client);
    const result = await service.list();

    expect(result.threads).toHaveLength(500);
    expect(result.hasMore).toBe(true);
  });

  it("returns all threads and hasMore=false when 500 are returned", async () => {
    const client = new ScriptedClient();
    client.on(
      "thread/list",
      () =>
        ({
          data: makeThreads(500),
        }) as ThreadListResponse,
    );

    const service = createRosterService(client);
    const result = await service.list();

    expect(result.threads).toHaveLength(500);
    expect(result.hasMore).toBe(false);
  });

  it("does not send a cursor in the request params", async () => {
    const client = new ScriptedClient();
    client.on("thread/list", () => ({ data: [] }) as ThreadListResponse);

    const service = createRosterService(client);
    await service.list();

    const params = client.requests[0]?.params as Record<string, unknown>;
    expect(params.cursor).toBeUndefined();
  });

  it("makes only one request regardless of row count", async () => {
    const client = new ScriptedClient();
    client.on(
      "thread/list",
      () =>
        ({
          data: makeThreads(500),
        }) as ThreadListResponse,
    );

    const service = createRosterService(client);
    await service.list();

    expect(client.requests).toHaveLength(1);
  });

  it("maps threads to roster entries preserving order", async () => {
    const client = new ScriptedClient();
    const threads = [
      makeThread({
        id: "a",
        name: "Alpha",
        cwd: "/home/jesse/work",
        updatedAt: 2_000_000,
        status: { type: "active" },
        evener: {
          ref: "ref-a",
          capabilities: {} as never,
          queue: { revision: 0 },
        },
      }),
      makeThread({
        id: "b",
        name: undefined,
        preview: "fix the bug",
        status: { type: "awaiting" },
        evener: {
          ref: "ref-b",
          capabilities: {} as never,
          queue: { revision: 0 },
        },
      }),
      makeThread({
        id: "c",
        status: { type: "idle" },
        evener: {
          ref: "ref-c",
          capabilities: {} as never,
          queue: { revision: 0 },
        },
      }),
    ];
    client.on("thread/list", () => ({ data: threads }) as ThreadListResponse);

    const service = createRosterService(client);
    const result = await service.list();

    expect(result.threads.map((e) => e.ref)).toEqual([
      "ref-a",
      "ref-b",
      "ref-c",
    ]);
    const e0 = result.threads[0] as RosterEntry;
    expect(e0.title).toBe("Alpha");
    expect(e0.project).toBe("/home/jesse/work");
    expect(e0.status).toBe("active");
    expect(e0.updatedAt).toBe(2_000_000);
    expect(e0.attention).toBe("running");
    const e1 = result.threads[1] as RosterEntry;
    expect(e1.title).toBe("fix the bug");
    expect(e1.attention).toBe("needsYou");
    const e2 = result.threads[2] as RosterEntry;
    expect(e2.attention).toBe("recent");
  });

  it("classifies askPending true as needsYou even when status is idle", async () => {
    const client = new ScriptedClient();
    client.on(
      "thread/list",
      () =>
        ({
          data: [
            makeThread({
              id: "t4",
              status: { type: "idle" },
              evener: {
                ref: "ref-t4",
                capabilities: {} as never,
                queue: { revision: 0 },
                askPending: true,
              },
            }),
          ],
        }) as ThreadListResponse,
    );

    const service = createRosterService(client);
    const result = await service.list();
    const entry = result.threads[0] as RosterEntry;
    expect(entry.attention).toBe("needsYou");
    expect(entry.askPending).toBe(true);
  });

  it("refresh() delegates to list()", async () => {
    const client = new ScriptedClient();
    client.on("thread/list", () => ({ data: [] }) as ThreadListResponse);

    const service = createRosterService(client);
    await service.refresh();

    expect(client.requests).toEqual([
      { method: "thread/list", params: { limit: 501 } },
    ]);
  });

  it("propagates errors from thread/list", async () => {
    const client = new ScriptedClient();
    client.on("thread/list", () => {
      throw new Error("server down");
    });

    const service = createRosterService(client);
    await expect(service.list()).rejects.toThrow("server down");
  });
});
