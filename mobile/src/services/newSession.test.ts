// NewSessionService tests with a fake AppwireClient. Covers:
// - start() wraps thread/start, forwards params, returns {thread, turn}
// - start() forwards optional model/effort fields
// - recentProjects() wraps evener/projects/recent and returns string[]
// - harnesses() wraps evener/harnesses/list and returns HarnessDescriptor[]
// - errors propagate

import { describe, expect, it } from "vitest";
import type {
  AnyNotification,
  HarnessDescriptor,
  HarnessListResponse,
  MethodName,
  MethodTypes,
  ModelDescriptor,
  ModelListResponse,
  ProjectsRecentResponse,
  Thread,
  ThreadStartResponse,
  Turn,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { createNewSessionService } from "./newSession";

// --- minimal fake client ----------------------------------------------------

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

// --- factories ---------------------------------------------------------------

function makeThread(over: Partial<Thread> = {}): Thread {
  return {
    id: "thread-new",
    sessionId: "session-new",
    preview: "started session",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1_000_000,
    updatedAt: 1_000_000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "local",
    evener: {
      ref: "ref-new",
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

function makeTurn(over: Partial<Turn> = {}): Turn {
  return {
    id: "turn-1",
    itemsView: "default",
    status: "completed",
    ...over,
  };
}

// --- tests ------------------------------------------------------------------

describe("NewSessionService", () => {
  it("start() calls thread/start with cwd and input", async () => {
    const client = new FakeAppwireClient();
    const thread = makeThread();
    const turn = makeTurn();
    client.on("thread/start", () => ({ thread, turn }) as ThreadStartResponse);

    const service = createNewSessionService(client);
    const result = await service.start({
      cwd: "/home/jesse/work",
      input: [{ type: "text", text: "fix the bug" }],
    });

    expect(client.calls[0]?.method).toBe("thread/start");
    const params = client.calls[0]?.params as { cwd: string; input?: unknown };
    expect(params.cwd).toBe("/home/jesse/work");
    expect(params.input).toEqual([{ type: "text", text: "fix the bug" }]);
    expect(result.thread.id).toBe("thread-new");
    expect(result.turn.id).toBe("turn-1");
  });

  it("start() forwards optional model and effort fields", async () => {
    const client = new FakeAppwireClient();
    client.on(
      "thread/start",
      () =>
        ({
          thread: makeThread(),
          turn: makeTurn(),
        }) as ThreadStartResponse,
    );

    const service = createNewSessionService(client);
    await service.start({
      cwd: "/tmp",
      modelProvider: "openai",
      model: "gpt-4",
      reasoningEffort: "high",
    });

    const params = client.calls[0]?.params as {
      modelProvider?: string;
      model?: string;
      reasoningEffort?: string;
    };
    expect(params.modelProvider).toBe("openai");
    expect(params.model).toBe("gpt-4");
    expect(params.reasoningEffort).toBe("high");
  });

  it("start() forwards optional harness field", async () => {
    const client = new FakeAppwireClient();
    client.on(
      "thread/start",
      () =>
        ({
          thread: makeThread(),
          turn: makeTurn(),
        }) as ThreadStartResponse,
    );

    const service = createNewSessionService(client);
    await service.start({ cwd: "/tmp", harness: "evener" });

    const params = client.calls[0]?.params as { harness?: string };
    expect(params.harness).toBe("evener");
  });

  it("start() with empty input sends no input field", async () => {
    const client = new FakeAppwireClient();
    client.on(
      "thread/start",
      () =>
        ({
          thread: makeThread(),
          turn: makeTurn(),
        }) as ThreadStartResponse,
    );

    const service = createNewSessionService(client);
    await service.start({ cwd: "/tmp" });

    const params = client.calls[0]?.params as { input?: unknown };
    expect(params.input).toBeUndefined();
  });

  it("start() propagates errors from thread/start", async () => {
    const client = new FakeAppwireClient();
    client.on("thread/start", () => {
      throw new Error("invalid project path");
    });

    const service = createNewSessionService(client);
    await expect(service.start({ cwd: "/bad" })).rejects.toThrow(
      "invalid project path",
    );
  });

  it("recentProjects() calls evener/projects/recent and returns string[]", async () => {
    const client = new FakeAppwireClient();
    client.on(
      "evener/projects/recent",
      () => ({ data: ["/a", "/b", "/c"] }) as ProjectsRecentResponse,
    );

    const service = createNewSessionService(client);
    const projects = await service.recentProjects();

    expect(client.calls[0]?.method).toBe("evener/projects/recent");
    expect(projects).toEqual(["/a", "/b", "/c"]);
  });

  it("harnesses() calls evener/harnesses/list and returns descriptors", async () => {
    const client = new FakeAppwireClient();
    const harnesses: HarnessDescriptor[] = [
      { id: "evener", label: "Evener" },
      { id: "codex", label: "Codex" },
    ];
    client.on(
      "evener/harnesses/list",
      () => ({ data: harnesses }) as HarnessListResponse,
    );

    const service = createNewSessionService(client);
    const result = await service.harnesses();

    expect(client.calls[0]?.method).toBe("evener/harnesses/list");
    expect(result).toHaveLength(2);
    expect(result[0]?.id).toBe("evener");
    expect(result[1]?.label).toBe("Codex");
  });

  it("models() calls model/list with its scope and returns the response", async () => {
    const client = new FakeAppwireClient();
    const models: ModelDescriptor[] = [
      { provider: "anthropic", model: "claude-sonnet-4-5" },
      { provider: "openai", model: "gpt-5" },
    ];
    const response: ModelListResponse = {
      data: models,
      recent: [models[1] as ModelDescriptor],
      diagnostics: [],
    };
    client.on("model/list", () => response);

    const service = createNewSessionService(client);
    const result = await service.models({
      cwd: "/tmp/project",
      harness: "evener",
    });

    expect(client.calls[0]).toEqual({
      method: "model/list",
      params: { cwd: "/tmp/project", harness: "evener" },
    });
    expect(result).toEqual(response);
  });

  it("models() defaults to an empty scope object", async () => {
    const client = new FakeAppwireClient();
    client.on("model/list", () => ({ data: [] }) as ModelListResponse);

    await createNewSessionService(client).models();

    expect(client.calls[0]).toEqual({ method: "model/list", params: {} });
  });

  it("recentProjects() propagates errors", async () => {
    const client = new FakeAppwireClient();
    client.on("evener/projects/recent", () => {
      throw new Error("unavailable");
    });

    const service = createNewSessionService(client);
    await expect(service.recentProjects()).rejects.toThrow("unavailable");
  });

  it("harnesses() propagates errors", async () => {
    const client = new FakeAppwireClient();
    client.on("evener/harnesses/list", () => {
      throw new Error("unavailable");
    });

    const service = createNewSessionService(client);
    await expect(service.harnesses()).rejects.toThrow("unavailable");
  });
});
