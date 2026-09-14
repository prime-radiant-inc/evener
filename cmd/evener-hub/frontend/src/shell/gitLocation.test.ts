// @vitest-environment node
import { beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import { resolveGitLocation } from "./gitLocation";

describe("resolveGitLocation (evener/git/head)", () => {
  let client: FakeClient;

  beforeEach(() => {
    client = new FakeClient();
  });

  test("returns the branch and origin url the hub reports", async () => {
    client.on("evener/git/head", ({ cwd }) => {
      expect(cwd).toBe("/repo");
      return { head: "feature/x", originUrl: "git@github.com:owner/repo.git" };
    });

    expect(await resolveGitLocation(client, "/repo")).toEqual({
      branch: "feature/x",
      originUrl: "git@github.com:owner/repo.git",
    });
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
