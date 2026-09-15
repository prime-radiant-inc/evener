// @vitest-environment node
import { beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import { resolveGitLocation, resolveHeadBranch } from "./gitLocation";

describe("resolveGitLocation (composer: branch + origin)", () => {
  let client: FakeClient;

  beforeEach(() => {
    client = new FakeClient();
  });

  test("asks for the origin remote and returns the branch and origin url", async () => {
    client.on("evener/git/head", ({ cwd, includeOrigin }) => {
      expect(cwd).toBe("/repo");
      expect(includeOrigin).toBe(true);
      return { head: "feature/x", originUrl: "git@github.com:owner/repo.git" };
    });

    expect(await resolveGitLocation(client, "/repo")).toEqual({
      branch: "feature/x",
      originUrl: "git@github.com:owner/repo.git",
    });
    expect(client.calls).toEqual([{ method: "evener/git/head", params: { cwd: "/repo", includeOrigin: true } }]);
  });

  // A repo with no origin remote is normal, and the wire omits the field.
  test("treats an absent originUrl as empty", async () => {
    client.on("evener/git/head", () => ({ head: "main" }));
    expect(await resolveGitLocation(client, "/repo")).toEqual({ branch: "main", originUrl: "" });
  });

  test("fails soft to empty values when the request throws", async () => {
    client.on("evener/git/head", () => {
      throw new Error("hub unavailable");
    });
    expect(await resolveGitLocation(client, "/repo")).toEqual({ branch: "", originUrl: "" });
  });

  test("does not request for an empty cwd", async () => {
    expect(await resolveGitLocation(client, "  ")).toEqual({ branch: "", originUrl: "" });
    expect(client.calls).toHaveLength(0);
  });
});

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
