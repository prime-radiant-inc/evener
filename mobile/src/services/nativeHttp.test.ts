import { describe, expect, it } from "vitest";
import {
  createHttpService,
  type HttpService,
  type HttpServiceError,
  type HubHttpResponse,
  isHttpServiceError,
} from "./nativeHttp";
import type { TauriBridge } from "./tauri";

// ---------------------------------------------------------------------------
// Fake TauriBridge — records invoke commands/args and returns scripted results
// ---------------------------------------------------------------------------

interface ScriptedInvoke {
  readonly cmd: string;
  readonly result: unknown;
  readonly error?: string;
  /** Optional delay gate: if set, invoke hangs until abort fires. */
  readonly stall?: boolean;
}

interface FakeBridge extends TauriBridge {
  readonly invocations: { cmd: string; args: Record<string, unknown> }[];
}

function fakeBridge(scripts: ScriptedInvoke[]): FakeBridge {
  const invocations: { cmd: string; args: Record<string, unknown> }[] = [];
  const queue = [...scripts];
  const bridge: TauriBridge = {
    async invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
      invocations.push({ cmd, args: args ?? {} });
      const next = queue.shift();
      if (next && next.cmd !== cmd) {
        throw new Error(`unexpected command: wanted ${next.cmd} got ${cmd}`);
      }
      if (next?.error) {
        throw new Error(next.error);
      }
      return next?.result as T;
    },
    createChannel<T>(_onMessage: (response: T) => void) {
      return { id: 0, onmessage: _onMessage } as never;
    },
  };
  return Object.assign(bridge, { invocations });
}

const PROFILE = "11111111-1111-1111-1111-111111111111";

describe("nativeHttp — typed allowlisted request", () => {
  it("invokes hub_http_request with camelCase {activeProfileId,method,path}", async () => {
    const bridge = fakeBridge([
      { cmd: "hub_http_request", result: { status: 200, body: { ok: true } } },
    ]);
    const http = createHttpService(bridge);
    const res = await http.request({
      activeProfileId: PROFILE,
      method: "GET",
      path: "/api/sessions",
    });
    expect(res).toEqual({ status: 200, body: { ok: true } });
    expect(bridge.invocations[0]).toEqual({
      cmd: "hub_http_request",
      args: {
        activeProfileId: PROFILE,
        method: "GET",
        path: "/api/sessions",
        body: null,
        mediaType: null,
      },
    });
  });

  it("sends body and mediaType for a POST with JSON", async () => {
    const bridge = fakeBridge([
      { cmd: "hub_http_request", result: { status: 201, body: { id: "s1" } } },
    ]);
    const http = createHttpService(bridge);
    await http.request({
      activeProfileId: PROFILE,
      method: "POST",
      path: "/api/sessions",
      body: { name: "demo" },
      mediaType: "application/json",
    });
    expect(bridge.invocations[0]?.args).toEqual({
      activeProfileId: PROFILE,
      method: "POST",
      path: "/api/sessions",
      body: { name: "demo" },
      mediaType: "application/json",
    });
  });

  it("rejects a disallowed method (DELETE)", async () => {
    const bridge = fakeBridge([
      { cmd: "hub_http_request", result: { status: 200, body: {} } },
    ]);
    const http = createHttpService(bridge);
    await expect(
      http.request({
        activeProfileId: PROFILE,
        method: "DELETE",
        path: "/api/sessions",
      }),
    ).rejects.toThrow(/method/i);
    expect(bridge.invocations).toHaveLength(0);
  });

  it("rejects a disallowed method (CONNECT)", async () => {
    const bridge = fakeBridge([]);
    const http = createHttpService(bridge);
    await expect(
      http.request({
        activeProfileId: PROFILE,
        method: "CONNECT",
        path: "/api/x",
      }),
    ).rejects.toThrow(/method/i);
  });

  it.each(["GET", "POST", "PUT", "PATCH"] as const)(
    "allows method %s",
    async (method) => {
      const bridge = fakeBridge([
        { cmd: "hub_http_request", result: { status: 200, body: {} } },
      ]);
      const http = createHttpService(bridge);
      await expect(
        http.request({
          activeProfileId: PROFILE,
          method,
          path: "/api/x",
        }),
      ).resolves.toEqual({ status: 200, body: {} });
    },
  );

  it("rejects a path outside the allowlist (/etc/passwd)", async () => {
    const bridge = fakeBridge([]);
    const http = createHttpService(bridge);
    await expect(
      http.request({
        activeProfileId: PROFILE,
        method: "GET",
        path: "/etc/passwd",
      }),
    ).rejects.toThrow(/path/i);
    expect(bridge.invocations).toHaveLength(0);
  });

  it.each(["/api/sessions", "/docs/intro", "/images/logo.png"])(
    "allows path prefix %s",
    async (path) => {
      const bridge = fakeBridge([
        { cmd: "hub_http_request", result: { status: 200, body: {} } },
      ]);
      const http = createHttpService(bridge);
      await expect(
        http.request({
          activeProfileId: PROFILE,
          method: "GET",
          path,
        }),
      ).resolves.toEqual({ status: 200, body: {} });
    },
  );

  it("rejects a disallowed media type (text/html)", async () => {
    const bridge = fakeBridge([]);
    const http = createHttpService(bridge);
    await expect(
      http.request({
        activeProfileId: PROFILE,
        method: "POST",
        path: "/api/x",
        body: { a: 1 },
        mediaType: "text/html",
      }),
    ).rejects.toThrow(/media|type/i);
  });

  it("rejects a path with dot segments (traversal)", async () => {
    const bridge = fakeBridge([]);
    const http = createHttpService(bridge);
    await expect(
      http.request({
        activeProfileId: PROFILE,
        method: "GET",
        path: "/api/../etc/passwd",
      }),
    ).rejects.toThrow(/path/i);
  });
});

describe("nativeHttp — response fidelity", () => {
  it("preserves status code and JSON body exactly", async () => {
    const bridge = fakeBridge([
      {
        cmd: "hub_http_request",
        result: {
          status: 404,
          body: { error: "not found", code: 404, items: [1, 2, 3] },
        },
      },
    ]);
    const http = createHttpService(bridge);
    const res: HubHttpResponse = await http.request({
      activeProfileId: PROFILE,
      method: "GET",
      path: "/api/missing",
    });
    expect(res.status).toBe(404);
    expect(res.body).toEqual({
      error: "not found",
      code: 404,
      items: [1, 2, 3],
    });
  });

  it("preserves a null body", async () => {
    const bridge = fakeBridge([
      { cmd: "hub_http_request", result: { status: 204, body: null } },
    ]);
    const http = createHttpService(bridge);
    const res = await http.request({
      activeProfileId: PROFILE,
      method: "GET",
      path: "/api/empty",
    });
    expect(res.status).toBe(204);
    expect(res.body).toBeNull();
  });

  it("preserves an array body", async () => {
    const bridge = fakeBridge([
      {
        cmd: "hub_http_request",
        result: { status: 200, body: ["a", "b", "c"] },
      },
    ]);
    const http = createHttpService(bridge);
    const res = await http.request({
      activeProfileId: PROFILE,
      method: "GET",
      path: "/api/list",
    });
    expect(res.body).toEqual(["a", "b", "c"]);
  });

  it("preserves a string body (text/plain)", async () => {
    const bridge = fakeBridge([
      {
        cmd: "hub_http_request",
        result: { status: 200, body: "hello world" },
      },
    ]);
    const http = createHttpService(bridge);
    const res = await http.request({
      activeProfileId: PROFILE,
      method: "GET",
      path: "/docs/readme",
      mediaType: "text/plain",
    });
    expect(res.body).toBe("hello world");
  });

  it("preserves a numeric body", async () => {
    const bridge = fakeBridge([
      { cmd: "hub_http_request", result: { status: 200, body: 42 } },
    ]);
    const http = createHttpService(bridge);
    const res = await http.request({
      activeProfileId: PROFILE,
      method: "GET",
      path: "/api/count",
    });
    expect(res.body).toBe(42);
  });

  it("rejects a response carrying an extra token field", async () => {
    const bridge = fakeBridge([
      {
        cmd: "hub_http_request",
        result: { status: 200, body: {}, token: "secret" },
      },
    ]);
    const http = createHttpService(bridge);
    await expect(
      http.request({
        activeProfileId: PROFILE,
        method: "GET",
        path: "/api/x",
      }),
    ).rejects.toThrow(/token|field/i);
  });

  it("rejects a response with a non-number status", async () => {
    const bridge = fakeBridge([
      { cmd: "hub_http_request", result: { status: "200", body: {} } },
    ]);
    const http = createHttpService(bridge);
    await expect(
      http.request({
        activeProfileId: PROFILE,
        method: "GET",
        path: "/api/x",
      }),
    ).rejects.toThrow(/status|number/i);
  });
});

describe("nativeHttp — cancellation", () => {
  it("rejects with a cancellation error when the signal aborts", async () => {
    const bridge = fakeBridge([]);
    const http = createHttpService(bridge);
    const controller = new AbortController();
    const promise = http.request({
      activeProfileId: PROFILE,
      method: "GET",
      path: "/api/slow",
      signal: controller.signal,
    });
    controller.abort();
    const err = await promise.catch((e) => e);
    expect(isHttpServiceError(err)).toBe(true);
    expect((err as HttpServiceError).code).toBe("cancelled");
  });

  it("does not invoke when already aborted", async () => {
    const bridge = fakeBridge([
      { cmd: "hub_http_request", result: { status: 200, body: {} } },
    ]);
    const http = createHttpService(bridge);
    const controller = new AbortController();
    controller.abort();
    await expect(
      http.request({
        activeProfileId: PROFILE,
        method: "GET",
        path: "/api/x",
        signal: controller.signal,
      }),
    ).rejects.toThrow(/cancel/i);
    expect(bridge.invocations).toHaveLength(0);
  });
});

describe("nativeHttp — no caller auth/header escape", () => {
  it("the request DTO carries no headers/authorization/cookie field", async () => {
    const bridge = fakeBridge([
      { cmd: "hub_http_request", result: { status: 200, body: {} } },
    ]);
    const http = createHttpService(bridge);
    await http.request({
      activeProfileId: PROFILE,
      method: "GET",
      path: "/api/x",
    });
    const args = bridge.invocations[0]?.args ?? {};
    expect(args).not.toHaveProperty("headers");
    expect(args).not.toHaveProperty("authorization");
    expect(args).not.toHaveProperty("cookie");
    expect(args).not.toHaveProperty("Authorization");
  });

  it("the typed input rejects an injected headers field", async () => {
    const bridge = fakeBridge([
      { cmd: "hub_http_request", result: { status: 200, body: {} } },
    ]);
    const http = createHttpService(bridge);
    // The input type does not allow headers; passing one through an unknown
    // cast must still be stripped before invoke (only known fields are forwarded).
    const injected = {
      activeProfileId: PROFILE,
      method: "GET",
      path: "/api/x",
      headers: { Authorization: "Bearer leak" },
    } as unknown as Parameters<HttpService["request"]>[0];
    await http.request(injected);
    expect(bridge.invocations[0]?.args).not.toHaveProperty("headers");
  });
});

describe("nativeHttp — structured errors", () => {
  it("wraps a backend rejection in HttpServiceError without echoing the secret", async () => {
    const bridge = fakeBridge([
      {
        cmd: "hub_http_request",
        result: null,
        error: "no capability for profile Bearer AAEC-secret",
      },
    ]);
    const http = createHttpService(bridge);
    const err = await http
      .request({
        activeProfileId: PROFILE,
        method: "GET",
        path: "/api/x",
      })
      .catch((e) => e);
    expect(isHttpServiceError(err)).toBe(true);
    expect((err as Error).message).not.toMatch(/AAEC-secret/);
    expect((err as Error).message).not.toMatch(/Bearer/);
  });

  it("maps a method-not-allowed validation to a typed error", async () => {
    const bridge = fakeBridge([]);
    const http = createHttpService(bridge);
    const err = await http
      .request({
        activeProfileId: PROFILE,
        method: "TRACE",
        path: "/api/x",
      })
      .catch((e) => e);
    expect(isHttpServiceError(err)).toBe(true);
    expect((err as HttpServiceError).code).toBe("validation_failed");
  });

  it("error cause is undefined (no secret reachable)", async () => {
    const bridge = fakeBridge([
      {
        cmd: "hub_http_request",
        result: null,
        error: "transport error with token xyz",
      },
    ]);
    const http = createHttpService(bridge);
    const err = await http
      .request({
        activeProfileId: PROFILE,
        method: "GET",
        path: "/api/x",
      })
      .catch((e) => e);
    expect(isHttpServiceError(err)).toBe(true);
    expect((err as HttpServiceError).cause).toBeUndefined();
  });
});
