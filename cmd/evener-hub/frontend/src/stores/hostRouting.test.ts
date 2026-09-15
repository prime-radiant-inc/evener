// @vitest-environment node
import { describe, expect, test } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { HostForwardedResult, MethodName } from "@evener/appwire-client";
import { HOST_DEPENDENT_DISCOVERY_METHODS, hostRequest, isLocalHost, LOCAL_HOST } from "./hostRouting";

// Every method the spawn form issues against a selected host (component 06
// §"Frontend changes", acceptance criterion 10; component 07 §"Proxy method").
// Spelled literally so the test fails if the shipped set drifts from the spec's
// discovery set (and therefore from the remote proxy's allow-list).
const SPEC_DISCOVERY_METHODS: readonly MethodName[] = [
  "model/list",
  "evener/harnesses/list",
  "evener/launch/resolve",
  "evener/launch/schema",
  "evener/paths/complete",
  "evener/path/validate",
  "evener/dirs/create",
  "evener/projects/recent",
  "evener/spawn/slashCatalog",
  "evener/git/head",
  "evener/plugin/preview",
  "evener/instance/list",
];

describe("hostRouting discovery inventory", () => {
  test("names exactly the spec's host-dependent discovery set", () => {
    expect([...HOST_DEPENDENT_DISCOVERY_METHODS].sort()).toEqual([...SPEC_DISCOVERY_METHODS].sort());
  });
});

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

  test.each([...SPEC_DISCOVERY_METHODS])("routes %s through the proxy for a remote host", async (method) => {
    const fake = new FakeClient("ready");
    fake.on("evener/host/request", () => ({}) as unknown as HostForwardedResult);

    await hostRequest(fake, "alpha", method, {} as never);

    expect(fake.calls).toEqual([{ method: "evener/host/request", params: { host: "alpha", method, params: {} } }]);
  });
});
