// @vitest-environment node

import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { beforeEach, describe, expect, test } from "vitest";
import { resolveHeadBranch } from "./gitLocation";

describe("resolveHeadBranch (spawn: branch only)", () => {
  let client: FakeClient;

  beforeEach(() => {
    client = new FakeClient();
  });

  test("requests the cwd over AppWire and returns the branch", async () => {
    client.on("evener/git/head", ({ cwd }) => {
      expect(cwd).toBe("/home/me/my project");
      return { head: "main" };
    });

    expect(await resolveHeadBranch(client, "/home/me/my project")).toBe("main");
    expect(client.calls).toEqual([{ method: "evener/git/head", params: { cwd: "/home/me/my project" } }]);
  });

  // The chip never renders the origin, so the hub must not be asked to send it.
  test("does not ask for the origin remote", async () => {
    client.on("evener/git/head", () => {
      throw new Error("origin must not be requested");
    });
    await expect(resolveHeadBranch(client, "/repo")).resolves.toBe("");
    const [call] = client.calls;
    expect(call?.params).toEqual({ cwd: "/repo" });
    expect(call?.params).not.toHaveProperty("includeOrigin");
  });

  test("returns an empty string when the server reports no branch (not a git repo)", async () => {
    client.on("evener/git/head", () => ({ head: "" }));
    expect(await resolveHeadBranch(client, "/tmp/plain")).toBe("");
  });

  test("fails soft to an empty string when the request throws", async () => {
    client.on("evener/git/head", () => {
      throw new Error("hub unavailable");
    });
    expect(await resolveHeadBranch(client, "/tmp/x")).toBe("");
  });

  test("does not request for an empty cwd", async () => {
    expect(await resolveHeadBranch(client, "  ")).toBe("");
    expect(client.calls).toHaveLength(0);
  });
});
