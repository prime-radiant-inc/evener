// @vitest-environment node
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { deleteProject, deleteSession, renameSession, setArchived, setFavorite } from "./actions";

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : "Error",
    json: () => Promise.resolve(body),
  } as Response;
}

function emptyResponse(status = 204): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: "No Content",
    json: () => Promise.reject(new Error("no body")),
  } as Response;
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const JSON_INIT = (body: unknown) => ({
  method: "POST",
  headers: { "Content-Type": "application/json" },
  credentials: "same-origin",
  body: JSON.stringify(body),
});

describe("setFavorite", () => {
  test("POSTs /api/favorite with exact kind/id/favorited body", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ok: true }));
    await setFavorite("session", "local:abc", true);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/favorite",
      JSON_INIT({ kind: "session", id: "local:abc", favorited: true }),
    );
  });

  test("works for kind=project", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ok: true }));
    await setFavorite("project", "proj-key", false);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/favorite",
      JSON_INIT({ kind: "project", id: "proj-key", favorited: false }),
    );
  });

  test("rejects with the server's error message on failure", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "favorite store error: boom" }, 500));
    await expect(setFavorite("session", "x", true)).rejects.toThrow("favorite store error: boom");
  });
});

describe("renameSession", () => {
  test("POSTs /api/sessions/<url-encoded ref>/rename with exact name body", async () => {
    fetchMock.mockResolvedValueOnce(emptyResponse(204));
    await renameSession("local:abc/def", "New name");
    expect(fetchMock).toHaveBeenCalledWith("/api/sessions/local%3Aabc%2Fdef/rename", JSON_INIT({ name: "New name" }));
  });

  test("resolves on a 204 No Content response with no body to parse", async () => {
    fetchMock.mockResolvedValueOnce(emptyResponse(204));
    await expect(renameSession("ref", "name")).resolves.toBeUndefined();
  });

  test("rejects with the server's error message on failure", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "name is required" }, 400));
    await expect(renameSession("ref", "")).rejects.toThrow("name is required");
  });
});

describe("setArchived", () => {
  test("POSTs /api/archive for a session using the provided canonical session ID", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ok: true }));
    await setArchived("session", "s1", true);
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({ kind: "session", id: "s1", archived: true });
  });

  test("POSTs /api/archive for a session with no working_dir field at all", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ok: true }));
    await setArchived("session", "local:abc", true);
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({ kind: "session", id: "local:abc", archived: true });
  });

  test("POSTs /api/archive for a project with working_dir included", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ok: true }));
    await setArchived("project", "proj-key", true, "/home/user/proj");
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/archive",
      JSON_INIT({ kind: "project", id: "proj-key", archived: true, working_dir: "/home/user/proj" }),
    );
  });

  test("rejects with the server's error message on failure", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "archive store error: boom" }, 500));
    await expect(setArchived("session", "x", true)).rejects.toThrow("archive store error: boom");
  });
});

describe("deleteProject", () => {
  test("POSTs /api/project/delete with exact key/working_dir body and returns the parsed result", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ deleted: ["a", "b"], skipped: [] }));
    const result = await deleteProject("proj-key", "/home/user/proj");
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/project/delete",
      JSON_INIT({ key: "proj-key", working_dir: "/home/user/proj" }),
    );
    expect(result).toEqual({ deleted: ["a", "b"], skipped: [] });
  });

  test("a 409 conflict (live sessions) rejects with the server's error message", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "project has live sessions", live: ["sess1"] }, 409));
    await expect(deleteProject("proj-key", "/dir")).rejects.toThrow("project has live sessions");
  });
});

describe("deleteSession", () => {
  test("POSTs /api/sessions/<url-encoded ref>/delete and returns the parsed result", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ deleted: ["local:abc"], skipped: [] }));
    const result = await deleteSession("local:abc/def");
    expect(fetchMock).toHaveBeenCalledWith("/api/sessions/local%3Aabc%2Fdef/delete", JSON_INIT({}));
    expect(result).toEqual({ deleted: ["local:abc"], skipped: [] });
  });

  test("a refused delete (live or reserved target) resolves with the session in skipped, not an error", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ deleted: [], skipped: [{ id: "abc", reason: "resumed live" }] }));
    const result = await deleteSession("local:abc");
    expect(result).toEqual({ deleted: [], skipped: [{ id: "abc", reason: "resumed live" }] });
  });

  test("rejects with the server's error message on failure", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "invalid session ID: boom" }, 400));
    await expect(deleteSession("local:abc")).rejects.toThrow("invalid session ID: boom");
  });
});
