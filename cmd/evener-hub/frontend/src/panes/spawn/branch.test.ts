// @vitest-environment node
import { beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { resolveHeadBranch } from "./branch";

describe("resolveHeadBranch (floor §1.7, evener/git/head)", () => {
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
