// @vitest-environment node

import type { HostForwardedResult } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { describe, expect, test, vi } from "vitest";
import { HOST_DEPENDENT_DISCOVERY_METHODS, hostRequest, isLocalHost, LOCAL_HOST } from "./index";

describe("isLocalHost", () => {
  test("treats the local id and an absent/empty host as local", () => {
    expect(isLocalHost(LOCAL_HOST)).toBe(true);
    expect(isLocalHost("")).toBe(true);
    expect(isLocalHost(undefined)).toBe(true);
    expect(isLocalHost(null)).toBe(true);
  });

  test("treats any other host id as remote", () => {
    expect(isLocalHost("alpha")).toBe(false);
  });
});

describe("hostRequest (local host)", () => {
  test("issues the plain method on the local connection, byte-for-byte", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/git/head", () => ({ head: "main" }));

    const result = await hostRequest(fake, LOCAL_HOST, "evener/git/head", { cwd: "/srv/app" });

    expect(result).toEqual({ head: "main" });
    expect(fake.calls).toEqual([{ method: "evener/git/head", params: { cwd: "/srv/app" } }]);
  });

  test("does not wrap when no host has been selected", async () => {
    const fake = new FakeClient("ready");
    fake.on("model/list", () => ({ data: [] }));

    await hostRequest(fake, undefined, "model/list", {});

    expect(fake.calls).toEqual([{ method: "model/list", params: {} }]);
  });
});

describe("hostRequest (remote host)", () => {
  test("wraps the call in evener/host/request with host, method, and params", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/host/request", () => ({}) as unknown as HostForwardedResult);

    await hostRequest(fake, "alpha", "evener/path/validate", { path: "/srv/app", kind: "dir" });

    expect(fake.calls).toEqual([
      {
        method: "evener/host/request",
        params: { host: "alpha", method: "evener/path/validate", params: { path: "/srv/app", kind: "dir" } },
      },
    ]);
  });

  test("forwards through a client that offers only request", async () => {
    const calls: unknown[][] = [];
    const client = {
      request: async (...args: unknown[]) => {
        calls.push(args);
        return { data: [] };
      },
    } as unknown as Parameters<typeof hostRequest>[0];
    await hostRequest(client, "paradise-park", "model/list", {});
    expect(calls).toEqual([
      ["evener/host/request", { host: "paradise-park", method: "model/list", params: {} }, undefined],
    ]);
  });

  test("returns the forwarded method's own result verbatim", async () => {
    const fake = new FakeClient("ready");
    fake.on(
      "evener/host/request",
      () => ({ data: [{ provider: "openai", model: "gpt" }] }) as unknown as HostForwardedResult,
    );

    const result = await hostRequest(fake, "alpha", "model/list", {});

    expect(result).toEqual({ data: [{ provider: "openai", model: "gpt" }] });
  });

  test("propagates the proxy's rejection unchanged", async () => {
    const fake = new FakeClient("ready");
    const refusal = new Error('method "evener/instance/deleteAll" is not a permitted remote admin method');
    fake.on("evener/host/request", () => {
      throw refusal;
    });

    await expect(hostRequest(fake, "alpha", "evener/instance/list", {})).rejects.toBe(refusal);
  });

  test.each([...HOST_DEPENDENT_DISCOVERY_METHODS])("routes %s through the proxy for a remote host", async (method) => {
    const fake = new FakeClient("ready");
    fake.on("evener/host/request", () => ({}) as unknown as HostForwardedResult);

    await hostRequest(fake, "alpha", method, {} as never);

    expect(fake.calls).toEqual([{ method: "evener/host/request", params: { host: "alpha", method, params: {} } }]);
  });
});

describe("hostRequest deadlines", () => {
  test.each([LOCAL_HOST, "alpha"])("preserves the caller deadline for %s", async (host) => {
    const fake = new FakeClient("ready");
    fake.on("model/list", () => ({ data: [] }));
    fake.on("evener/host/request", () => ({ data: [] }) as unknown as HostForwardedResult);
    const request = vi.spyOn(fake, "request");
    const params = {};
    const opts = { timeoutMs: 90_000 };

    await hostRequest(fake, host, "model/list", params, opts);

    if (host === LOCAL_HOST) {
      expect(request).toHaveBeenCalledWith("model/list", params, opts);
    } else {
      expect(request).toHaveBeenCalledWith("evener/host/request", { host, method: "model/list", params }, opts);
    }
  });
});
