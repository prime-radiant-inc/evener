import { describe, expect, it } from "vitest";
import {
  createHttpService,
  decodeJsonBody,
  decodeTextBody,
  encodeJsonBody,
  type HttpServiceError,
  isHttpServiceError,
  MAX_REQUEST_BODY_BYTES,
  MAX_RESPONSE_BODY_BYTES,
  REQUEST_ID_HEADER,
} from "./nativeHttp";
import type { TauriBridge, TauriChannel } from "./tauri";

type InvokeArgs = Record<string, unknown> | ArrayBuffer | Uint8Array;
interface Invocation {
  readonly cmd: string;
  readonly args: InvokeArgs;
  readonly options?: { readonly headers: HeadersInit };
}

interface ResponseScript {
  readonly status: number;
  readonly body: Uint8Array;
  readonly mediaType?: string | null;
  readonly headers?: Record<string, string>;
  readonly metadataExtra?: Record<string, unknown>;
}

function binaryBridge(script: ResponseScript): TauriBridge & {
  readonly invocations: Invocation[];
  readonly uploadedBodies: Uint8Array[];
} {
  const invocations: Invocation[] = [];
  const uploadedBodies: Uint8Array[] = [];
  const bridge: TauriBridge = {
    async invoke<T>(
      cmd: string,
      args: InvokeArgs = {},
      options?: { readonly headers: HeadersInit },
    ): Promise<T> {
      invocations.push({ cmd, args, options });
      if (cmd === "hub_http_upload_body") {
        uploadedBodies.push(new Uint8Array(args as Uint8Array));
        return undefined as T;
      }
      if (cmd === "hub_http_request") {
        const objectArgs = args as Record<string, unknown>;
        const requestId = (objectArgs.request as { requestId: string })
          .requestId;
        const channel = objectArgs.onResponse as TauriChannel<unknown>;
        channel.onmessage({
          requestId,
          status: script.status,
          headers: script.headers ?? {},
          mediaType: script.mediaType ?? null,
          bodyLength: script.body.byteLength,
          ...script.metadataExtra,
        });
        return script.body as T;
      }
      return undefined as T;
    },
    createChannel<T>(onMessage: (response: T) => void) {
      return { id: 1, onmessage: onMessage };
    },
  };
  return Object.assign(bridge, { invocations, uploadedBodies });
}

const PROFILE = "11111111-1111-1111-1111-111111111111";
const REQUEST_ID = "request_123456789";

function service(bridge: TauriBridge) {
  return createHttpService(bridge, { newRequestId: () => REQUEST_ID });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (cause: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("nativeHttp — binary request and response fidelity", () => {
  it("uploads NUL/non-UTF8 JPEG-like bytes raw and preserves the raw response", async () => {
    const requestBody = new Uint8Array([0xff, 0xd8, 0, 0x80, 0xff, 0xd9]);
    const responseBody = new Uint8Array([0xff, 0xd8, 0, 1, 0xfe, 0xd9]);
    const bridge = binaryBridge({
      status: 200,
      body: responseBody,
      mediaType: "image/jpeg",
      headers: { contentType: "image/jpeg", etag: '"abc"' },
    });
    const response = await service(bridge).request({
      activeProfileId: PROFILE,
      method: "POST",
      path: "/images/upload",
      body: requestBody,
      mediaType: "image/jpeg",
    });
    expect(bridge.uploadedBodies).toEqual([requestBody]);
    expect(response).toEqual({
      status: 200,
      headers: { contentType: "image/jpeg", etag: '"abc"' },
      mediaType: "image/jpeg",
      body: responseBody,
    });
    const upload = bridge.invocations.find(
      (invocation) => invocation.cmd === "hub_http_upload_body",
    );
    expect(upload?.options?.headers).toEqual({
      [REQUEST_ID_HEADER]: REQUEST_ID,
    });
  });

  it("keeps text and JSON as explicit caller codecs", async () => {
    const json = { ok: true, text: "hello" };
    const jsonBytes = encodeJsonBody(json);
    expect(decodeJsonBody(jsonBytes)).toEqual(json);
    expect(
      decodeTextBody(new TextEncoder().encode("plain text\u0000tail")),
    ).toBe("plain text\u0000tail");
  });

  it("preserves multipart bytes and a boundary-bearing media type", async () => {
    const multipart = new TextEncoder().encode(
      '--boundary\r\nContent-Disposition: form-data; name="x"\r\n\r\nvalue\r\n--boundary--\r\n',
    );
    const bridge = binaryBridge({ status: 201, body: new Uint8Array() });
    await service(bridge).request({
      activeProfileId: PROFILE,
      method: "POST",
      path: "/api/upload",
      body: multipart,
      mediaType: "multipart/form-data; boundary=boundary",
    });
    expect(Array.from(bridge.uploadedBodies[0] ?? [])).toEqual(
      Array.from(multipart),
    );
  });

  it("preserves a non-2xx status and body", async () => {
    const body = new TextEncoder().encode("validation failed\u0000details");
    const response = await service(
      binaryBridge({
        status: 422,
        body,
        mediaType: "text/plain",
        headers: { contentType: "text/plain; charset=utf-8" },
      }),
    ).request({
      activeProfileId: PROFILE,
      method: "POST",
      path: "/api/sessions",
    });
    expect(response.status).toBe(422);
    expect(Array.from(response.body)).toEqual(Array.from(body));
  });

  it("allows exactly 8 MiB and rejects one byte more before invoke", async () => {
    const exact = new Uint8Array(MAX_REQUEST_BODY_BYTES);
    const bridge = binaryBridge({ status: 200, body: new Uint8Array() });
    await expect(
      service(bridge).request({
        activeProfileId: PROFILE,
        method: "POST",
        path: "/api/upload",
        body: exact,
        mediaType: "application/json",
      }),
    ).resolves.toBeDefined();

    const rejectedBridge = binaryBridge({
      status: 200,
      body: new Uint8Array(),
    });
    await expect(
      service(rejectedBridge).request({
        activeProfileId: PROFILE,
        method: "POST",
        path: "/api/upload",
        body: new Uint8Array(MAX_REQUEST_BODY_BYTES + 1),
      }),
    ).rejects.toMatchObject({ code: "validation_failed" });
    expect(rejectedBridge.invocations).toHaveLength(0);
  });

  it("rejects response metadata over exactly 20 MiB", async () => {
    const bridge = binaryBridge({
      status: 200,
      body: new Uint8Array(),
      metadataExtra: { bodyLength: MAX_RESPONSE_BODY_BYTES + 1 },
    });
    await expect(
      service(bridge).request({
        activeProfileId: PROFILE,
        method: "GET",
        path: "/images/large",
      }),
    ).rejects.toMatchObject({ code: "decode_failed" });
  });

  it("rejects secret-bearing metadata extras", async () => {
    const bridge = binaryBridge({
      status: 200,
      body: new Uint8Array(),
      metadataExtra: { token: "secret" },
    });
    await expect(
      service(bridge).request({
        activeProfileId: PROFILE,
        method: "GET",
        path: "/api/x",
      }),
    ).rejects.toMatchObject({ code: "decode_failed" });
  });
});

describe("nativeHttp — canonical path defense", () => {
  it.each([
    "/api/%2e%2e/x",
    "/api/%2E%2e/x",
    "/api/%252e%252e/x",
    "/api/%2f/x",
    "/api//x",
    "/api/..\\x",
    "//api/x",
    "/API/x",
    "/api/%GG",
  ])("rejects %s before invoke", async (path) => {
    const bridge = binaryBridge({ status: 200, body: new Uint8Array() });
    await expect(
      service(bridge).request({
        activeProfileId: PROFILE,
        method: "GET",
        path,
      }),
    ).rejects.toMatchObject({ code: "validation_failed" });
    expect(bridge.invocations).toHaveLength(0);
  });

  it("splits the query before path validation", async () => {
    const bridge = binaryBridge({ status: 200, body: new Uint8Array() });
    await expect(
      service(bridge).request({
        activeProfileId: PROFILE,
        method: "GET",
        path: "/api/x?next=/../secret\\value",
      }),
    ).resolves.toBeDefined();
  });
});

describe("nativeHttp — native cancellation", () => {
  it("invokes cancel exactly once and waits for execute and cancel settlement", async () => {
    let requestReject: ((cause: unknown) => void) | undefined;
    let cancelResolve: (() => void) | undefined;
    const execute = new Promise<never>((_resolve, reject) => {
      requestReject = reject;
    });
    const cancel = new Promise<void>((resolve) => {
      cancelResolve = resolve;
    });
    const requestStarted = deferred<void>();
    const cancelStarted = deferred<void>();
    const calls: string[] = [];
    const bridge: TauriBridge = {
      async invoke<T>(cmd: string): Promise<T> {
        calls.push(cmd);
        if (cmd === "hub_http_request") {
          requestStarted.resolve(undefined);
          return execute as Promise<T>;
        }
        if (cmd === "hub_http_cancel") {
          cancelStarted.resolve(undefined);
          return cancel as Promise<T>;
        }
        return undefined as T;
      },
      createChannel<T>(onmessage: (value: T) => void) {
        return { id: 1, onmessage };
      },
    };
    const controller = new AbortController();
    const pending = service(bridge).request({
      activeProfileId: PROFILE,
      method: "GET",
      path: "/api/slow",
      signal: controller.signal,
    });
    await requestStarted.promise;
    controller.abort();
    controller.abort();
    await cancelStarted.promise;
    requestReject?.(new Error("native request cancelled"));
    let settled = false;
    void pending.catch(() => {
      settled = true;
    });
    await Promise.resolve();
    expect(settled).toBe(false);
    cancelResolve?.();
    await expect(pending).rejects.toMatchObject({ code: "cancelled" });
    expect(calls.filter((cmd) => cmd === "hub_http_cancel")).toHaveLength(1);
  });

  it("does not invoke when already aborted and ignores abort after completion", async () => {
    const controller = new AbortController();
    controller.abort();
    const bridge = binaryBridge({ status: 200, body: new Uint8Array() });
    await expect(
      service(bridge).request({
        activeProfileId: PROFILE,
        method: "GET",
        path: "/api/x",
        signal: controller.signal,
      }),
    ).rejects.toMatchObject({ code: "cancelled" });
    expect(bridge.invocations).toHaveLength(0);

    const after = new AbortController();
    const completedBridge = binaryBridge({
      status: 200,
      body: new Uint8Array(),
    });
    await service(completedBridge).request({
      activeProfileId: PROFILE,
      method: "GET",
      path: "/api/x",
      signal: after.signal,
    });
    after.abort();
    expect(
      completedBridge.invocations.filter(
        (invocation) => invocation.cmd === "hub_http_cancel",
      ),
    ).toHaveLength(0);
  });
});

describe("nativeHttp — redacted errors", () => {
  it("drops a secret-bearing native rejection", async () => {
    const bridge: TauriBridge = {
      async invoke<T>(): Promise<T> {
        throw new Error("Bearer raw-secret-token");
      },
      createChannel<T>(onmessage: (value: T) => void) {
        return { id: 1, onmessage };
      },
    };
    const error = await service(bridge)
      .request({ activeProfileId: PROFILE, method: "GET", path: "/api/x" })
      .catch((cause) => cause);
    expect(isHttpServiceError(error)).toBe(true);
    expect((error as HttpServiceError).message).not.toContain(
      "raw-secret-token",
    );
    expect((error as HttpServiceError).cause).toBeUndefined();
  });
});
