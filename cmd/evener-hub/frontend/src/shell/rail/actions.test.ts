// @vitest-environment node
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
  assignSessionPin,
  deletePinSection,
  deleteProject,
  deleteSession,
  isRailRequestStatus,
  listPinSections,
  renamePinSection,
  renameSession,
  setArchived,
  setFavorite,
  unpinSession,
} from "./actions";

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

describe("named pin sections", () => {
  test("lists all pin sections with same-origin credentials and parses the response", async () => {
    const sections = [{ id: "section/one", name: "Research", member_count: 2 }];
    fetchMock.mockResolvedValueOnce(jsonResponse(sections));

    await expect(listPinSections()).resolves.toEqual(sections);
    expect(fetchMock).toHaveBeenCalledWith("/api/pin-sections", { credentials: "same-origin" });
  });

  test("assigns by name with the exact POST body and parses the canonical assignment", async () => {
    const response = {
      ok: true,
      changed: true,
      assignment: {
        session_ref: "local:s1",
        section: { id: "canonical", name: "Research", member_count: 1 },
      },
    };
    fetchMock.mockResolvedValueOnce(jsonResponse(response));

    await expect(assignSessionPin("local:s1", { section_name: "Research" })).resolves.toEqual(response);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/session-pin",
      JSON_INIT({ session_ref: "local:s1", section_name: "Research" }),
    );
  });

  test("assigns by section ID without changing the target shape", async () => {
    const response = {
      ok: true,
      changed: false,
      assignment: { session_ref: "local:s1", section: { id: "s/1", name: "Client", member_count: 1 } },
    };
    fetchMock.mockResolvedValueOnce(jsonResponse(response));

    await assignSessionPin("local:s1", { section_id: "s/1" });
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/session-pin",
      JSON_INIT({ session_ref: "local:s1", section_id: "s/1" }),
    );
  });

  test("assignment failures preserve HTTP status for structured not-found handling", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "pin section not found" }, 404));

    const error = await assignSessionPin("local:s1", { section_id: "deleted" }).catch((cause: unknown) => cause);

    expect(error).toBeInstanceOf(Error);
    expect((error as Error).message).toBe("pin section not found");
    expect(isRailRequestStatus(error, 404)).toBe(true);
    expect(isRailRequestStatus(error, 409)).toBe(false);
  });

  test("unpins with an encoded query ref, DELETE, same-origin credentials, and parses success", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ ok: true, changed: true, assignment: { session_ref: "local:s/1" } }),
    );

    await expect(unpinSession("local:s/1")).resolves.toEqual({ ok: true, changed: true });
    expect(fetchMock).toHaveBeenCalledWith("/api/session-pin?ref=local%3As%2F1", {
      method: "DELETE",
      credentials: "same-origin",
    });
  });

  test("renames through an encoded section URL and returns the canonical summary", async () => {
    const section = { id: "section/one", name: "New name", member_count: 3 };
    fetchMock.mockResolvedValueOnce(jsonResponse({ ok: true, changed: true, section }));

    await expect(renamePinSection("section/one", "New name")).resolves.toEqual(section);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/pin-sections/section%2Fone",
      expect.objectContaining({
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        credentials: "same-origin",
        body: JSON.stringify({ name: "New name" }),
      }),
    );
  });

  test("deletes through an encoded section URL and parses the durable member count", async () => {
    const response = { ok: true, changed: true, member_count: 4 };
    fetchMock.mockResolvedValueOnce(jsonResponse(response));

    await expect(deletePinSection("section/one")).resolves.toEqual(response);
    expect(fetchMock).toHaveBeenCalledWith("/api/pin-sections/section%2Fone", {
      method: "DELETE",
      credentials: "same-origin",
    });
  });

  test.each([
    ["list", () => listPinSections()],
    ["assign", () => assignSessionPin("local:s1", { section_id: "missing" })],
    ["unpin", () => unpinSession("local:s1")],
    ["rename", () => renamePinSection("conflict", "Research")],
    ["delete", () => deletePinSection("missing")],
  ])("propagates JSON error messages for %s", async (_label, request) => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "named pin failure" }, 409));
    await expect(request()).rejects.toThrow("named pin failure");
  });
});

describe("setFavorite", () => {
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
    await expect(setFavorite("project", "x", true)).rejects.toThrow("favorite store error: boom");
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
