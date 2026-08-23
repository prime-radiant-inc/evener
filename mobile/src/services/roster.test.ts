// RosterService tests with a fake AppwireClient. Covers:
// - list() wraps thread/list and maps each Thread to a RosterEntry
// - ref comes from evener.ref, title from name or preview, project from cwd
// - status comes from status.type
// - updatedAt is passed through as ms
// - attention classification: awaiting/askPending => needsYou, active => running, else recent
// - cursor passthrough to thread/list params and nextCursor in the result
// - refresh() delegates to list() with no cursor
// - list() forwards cursor and limit

import { describe, expect, it } from "vitest";
import type {
  AnyNotification,
  MethodName,
  MethodTypes,
  Thread,
  ThreadListResponse,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { createRosterService, type RosterEntry } from "./roster";

// --- minimal fake client (same shape as conversation.test.ts) ----------------

type RequestHandler = (params: unknown) => unknown | Promise<unknown>;

class FakeAppwireClient {
  readonly calls: { method: string; params: unknown }[] = [];
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
    this.calls.push({ method, params });
    const handler = this.handlers.get(method);
    if (!handler) {
      return Promise.reject(
        new Error(`FakeAppwireClient: no handler for "${method}"`),
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

// --- thread factory ----------------------------------------------------------

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
        queue: true,
        goal: true,
        rename: true,
      },
      queue: { revision: 0 },
    },
    ...over,
  };
}

// --- tests ------------------------------------------------------------------

describe("RosterService", () => {
  it("list() calls thread/list and maps threads to roster entries", async () => {
    const client = new FakeAppwireClient();
    const threads = [
      makeThread({
        id: "t1",
        name: "My Session",
        preview: "preview text",
        cwd: "/home/jesse/work",
        updatedAt: 2_000_000,
        status: { type: "idle" },
        evener: {
          ref: "ref-t1",
          capabilities: {} as never,
          queue: { revision: 0 },
        },
      }),
    ];
    client.on("thread/list", () => ({ data: threads }) as ThreadListResponse);

    const service = createRosterService(client);
    const result = await service.list();

    expect(client.calls[0]?.method).toBe("thread/list");
    const entry = result.threads[0] as RosterEntry;
    expect(entry.ref).toBe("ref-t1");
    expect(entry.title).toBe("My Session");
    expect(entry.project).toBe("/home/jesse/work");
    expect(entry.status).toBe("idle");
    expect(entry.updatedAt).toBe(2_000_000);
    expect(entry.attention).toBe("recent");
  });

  it("title falls back to preview when name is absent", async () => {
    const client = new FakeAppwireClient();
    const threads = [
      makeThread({
        id: "t2",
        name: undefined,
        preview: "fix the bug",
        evener: {
          ref: "ref-t2",
          capabilities: {} as never,
          queue: { revision: 0 },
        },
      }),
    ];
    client.on("thread/list", () => ({ data: threads }) as ThreadListResponse);

    const service = createRosterService(client);
    const result = await service.list();
    expect((result.threads[0] as RosterEntry).title).toBe("fix the bug");
  });

  it("classifies awaiting status as needsYou", async () => {
    const client = new FakeAppwireClient();
    const threads = [
      makeThread({
        id: "t3",
        status: { type: "awaiting" },
        evener: {
          ref: "ref-t3",
          capabilities: {} as never,
          queue: { revision: 0 },
          askPending: false,
        },
      }),
    ];
    client.on("thread/list", () => ({ data: threads }) as ThreadListResponse);

    const service = createRosterService(client);
    const result = await service.list();
    expect((result.threads[0] as RosterEntry).attention).toBe("needsYou");
  });

  it("classifies askPending true as needsYou even when status is idle", async () => {
    const client = new FakeAppwireClient();
    const threads = [
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
    ];
    client.on("thread/list", () => ({ data: threads }) as ThreadListResponse);

    const service = createRosterService(client);
    const result = await service.list();
    const entry = result.threads[0] as RosterEntry;
    expect(entry.attention).toBe("needsYou");
    expect(entry.askPending).toBe(true);
  });

  it("classifies active status as running", async () => {
    const client = new FakeAppwireClient();
    const threads = [
      makeThread({
        id: "t5",
        status: { type: "active" },
        evener: {
          ref: "ref-t5",
          capabilities: {} as never,
          queue: { revision: 0 },
        },
      }),
    ];
    client.on("thread/list", () => ({ data: threads }) as ThreadListResponse);

    const service = createRosterService(client);
    const result = await service.list();
    expect((result.threads[0] as RosterEntry).attention).toBe("running");
  });

  it("classifies idle status as recent", async () => {
    const client = new FakeAppwireClient();
    const threads = [
      makeThread({
        id: "t6",
        status: { type: "idle" },
        evener: {
          ref: "ref-t6",
          capabilities: {} as never,
          queue: { revision: 0 },
        },
      }),
    ];
    client.on("thread/list", () => ({ data: threads }) as ThreadListResponse);

    const service = createRosterService(client);
    const result = await service.list();
    expect((result.threads[0] as RosterEntry).attention).toBe("recent");
  });

  it("forwards cursor to thread/list params", async () => {
    const client = new FakeAppwireClient();
    client.on("thread/list", () => ({ data: [] }) as ThreadListResponse);

    const service = createRosterService(client);
    await service.list("cursor-abc");

    const params = client.calls[0]?.params as { cursor?: string };
    expect(params.cursor).toBe("cursor-abc");
  });

  it("returns nextCursor from the response", async () => {
    const client = new FakeAppwireClient();
    client.on(
      "thread/list",
      () => ({ data: [], nextCursor: "next-123" }) as ThreadListResponse,
    );

    const service = createRosterService(client);
    const result = await service.list();
    expect(result.nextCursor).toBe("next-123");
  });

  it("refresh() calls list() with no cursor", async () => {
    const client = new FakeAppwireClient();
    client.on("thread/list", () => ({ data: [] }) as ThreadListResponse);

    const service = createRosterService(client);
    await service.refresh();

    const params = client.calls[0]?.params as { cursor?: string };
    expect(params.cursor).toBeUndefined();
  });

  it("maps multiple threads preserving order", async () => {
    const client = new FakeAppwireClient();
    const threads = [
      makeThread({
        id: "a",
        status: { type: "active" },
        evener: {
          ref: "ref-a",
          capabilities: {} as never,
          queue: { revision: 0 },
        },
      }),
      makeThread({
        id: "b",
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
    expect(result.threads.map((e) => e.attention)).toEqual([
      "running",
      "needsYou",
      "recent",
    ]);
  });

  it("propagates errors from thread/list", async () => {
    const client = new FakeAppwireClient();
    client.on("thread/list", () => {
      throw new Error("server down");
    });

    const service = createRosterService(client);
    await expect(service.list()).rejects.toThrow("server down");
  });
});
