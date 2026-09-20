// @vitest-environment node

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { HostForwardedResult } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { describe, expect, test } from "vitest";
import { HOST_DEPENDENT_DISCOVERY_METHODS, hostRequest, isLocalHost, LOCAL_HOST } from "./hostRouting";

// The CROSS-LANGUAGE half of this contract. The shipped set above and the Go
// proxy's allow-list (cmd/evener-hub/app_host_admin.go's remoteHostAdminMethods)
// are both pinned to the checked-in list at cmd/evener-hub/
// host_request_methods.txt, read here and by app_host_admin_test.go. That file
// carries the rationale for what belongs on the list; this test's job is to
// prove the shipped set still IS it, in both directions, so a method dropped
// from the product set (with or without the literal list it used to be spelled
// against here) fails rather than silently narrowing what the pane forwards.
const here = dirname(fileURLToPath(import.meta.url));
const SHARED_LIST_PATH = join(here, "../../../host_request_methods.txt");

function sharedForwardedMethods(): string[] {
  return readFileSync(SHARED_LIST_PATH, "utf8")
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "" && !line.startsWith("#"));
}

describe("hostRouting discovery inventory", () => {
  test("names exactly the shared forwarded-method list", () => {
    const shared = sharedForwardedMethods();
    // An empty or missing list would make this assertion vacuous, so it is
    // checked rather than assumed.
    expect(shared.length).toBeGreaterThan(0);
    expect([...HOST_DEPENDENT_DISCOVERY_METHODS].sort()).toEqual([...shared].sort());
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

  test.each([...HOST_DEPENDENT_DISCOVERY_METHODS])("routes %s through the proxy for a remote host", async (method) => {
    const fake = new FakeClient("ready");
    fake.on("evener/host/request", () => ({}) as unknown as HostForwardedResult);

    await hostRequest(fake, "alpha", method, {} as never);

    expect(fake.calls).toEqual([{ method: "evener/host/request", params: { host: "alpha", method, params: {} } }]);
  });
});
