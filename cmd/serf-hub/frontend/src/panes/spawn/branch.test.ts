// @vitest-environment node
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resolveHeadBranch } from "./branch";

describe("resolveHeadBranch (floor §1.7, REST-only GET /api/git/head?cwd=)", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  function jsonResponse(body: unknown): Response {
    return { ok: true, status: 200, json: () => Promise.resolve(body) } as Response;
  }

  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  test("GETs /api/git/head with the cwd query param and returns the branch", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ branch: "main" }));

    expect(await resolveHeadBranch("/home/me/my project")).toBe("main");
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/git/head?cwd=%2Fhome%2Fme%2Fmy%20project",
      expect.objectContaining({ credentials: "same-origin" }),
    );
  });

  test("returns an empty string when the server reports no branch (not a git repo)", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ branch: "" }));
    expect(await resolveHeadBranch("/tmp/plain")).toBe("");
  });

  test("fails soft to an empty string when the request throws", async () => {
    fetchMock.mockRejectedValueOnce(new Error("network down"));
    expect(await resolveHeadBranch("/tmp/x")).toBe("");
  });

  test("fails soft to an empty string on a non-OK response", async () => {
    fetchMock.mockResolvedValueOnce({ ok: false, status: 500, json: () => Promise.resolve({}) } as Response);
    expect(await resolveHeadBranch("/tmp/x")).toBe("");
  });

  test("does not fetch for an empty cwd", async () => {
    expect(await resolveHeadBranch("  ")).toBe("");
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
