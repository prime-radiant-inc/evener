import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { createDir, preflightDir } from "./preflight";

describe("preflightDir", () => {
  test("a valid directory preflights ok", async () => {
    const fake = new FakeClient("ready");
    fake.on("serf/path/validate", () => ({ path: "/tmp/p", valid: true }));

    expect(await preflightDir(fake, "/tmp/p")).toEqual({ kind: "ok" });
  });

  test("validates via serf/path/validate with kind:dir", async () => {
    const fake = new FakeClient("ready");
    fake.on("serf/path/validate", () => ({ path: "/tmp/p", valid: true }));

    await preflightDir(fake, "/tmp/p");

    expect(fake.calls[0]?.method).toBe("serf/path/validate");
    expect(fake.calls[0]?.params).toEqual({ path: "/tmp/p", kind: "dir" });
  });

  test("an invalid directory aborts with the validator's message", async () => {
    const fake = new FakeClient("ready");
    fake.on("serf/path/validate", () => ({ path: "/nope", valid: false, error: "no such directory" }));

    expect(await preflightDir(fake, "/nope")).toEqual({ kind: "abort", message: "no such directory" });
  });

  test("fails OPEN when the validate check itself throws (spawn.js:573-580)", async () => {
    const fake = new FakeClient("ready");
    fake.on("serf/path/validate", () => {
      throw new Error("rpc down");
    });

    expect(await preflightDir(fake, "/tmp/p")).toEqual({ kind: "ok" });
  });
});

describe("createDir", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  function jsonResponse(body: unknown, status = 200): Response {
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: status === 200 ? "OK" : "Error",
      json: () => Promise.resolve(body),
    } as Response;
  }

  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  test("POSTs /api/dirs/create with the path", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ path: "/tmp/new", created: true }));

    await createDir("/tmp/new");

    expect(fetchMock).toHaveBeenCalledWith("/api/dirs/create", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ path: "/tmp/new" }),
    });
  });

  test("throws the server's error message on failure", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "a file already exists at that path" }, 409));

    await expect(createDir("/tmp/file")).rejects.toThrow("a file already exists at that path");
  });

  test("falls back to the HTTP status when the error body has no message", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({}, 500));

    await expect(createDir("/tmp/x")).rejects.toThrow("HTTP 500");
  });
});
