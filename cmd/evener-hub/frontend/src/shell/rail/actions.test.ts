// @vitest-environment node
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { connectionStore } from "../../stores/connection";
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
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
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
    const navigation = { generation_id: "g", targets: [] };
    fetchMock.mockResolvedValueOnce(jsonResponse({ ok: true, changed: true, section, navigation }));

    await expect(renamePinSection("section/one", "New name")).resolves.toEqual({ section, navigation });
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
  test("uses the typed AppWire method and preserves navigation targets", async () => {
    const client = new FakeClient();
    const response = {
      ok: true as const,
      navigation: { generation_id: "generation-2", targets: [{ kind: "pin_catalog" as const, revision: 7 }] },
    };
    client.on("evener/favorite/set", (params) => {
      expect(params).toEqual({ kind: "project", id: "proj-key", favorited: false });
      return response;
    });

    await expect(setFavorite(client, "project", "proj-key", false)).resolves.toEqual(response);
    expect(client.calls).toEqual([
      { method: "evener/favorite/set", params: { kind: "project", id: "proj-key", favorited: false } },
    ]);
  });

  test("propagates AppWire failures", async () => {
    const client = new FakeClient();
    client.on("evener/favorite/set", () => {
      throw new Error("favorite store error: boom");
    });
    await expect(setFavorite(client, "project", "x", true)).rejects.toThrow("favorite store error: boom");
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
  test("sends the typed AppWire request for a session and returns its receipt", async () => {
    const response = { ok: true, navigation: { generation_id: "g1", targets: [] } };
    const client = new FakeClient();
    client.on("evener/archive/set", (params) => {
      expect(params).toEqual({ kind: "session", id: "s1", archived: true });
      return response;
    });
    connectionStore.getState().connect(client);

    await expect(setArchived("session", "s1", true)).resolves.toEqual(response);
    expect(client.calls).toEqual([
      { method: "evener/archive/set", params: { kind: "session", id: "s1", archived: true } },
    ]);
  });

  test("omits workingDir for a session", async () => {
    const client = new FakeClient();
    client.on("evener/archive/set", () => ({ ok: true, navigation: { generation_id: "g1", targets: [] } }));
    connectionStore.getState().connect(client);

    await setArchived("session", "local:abc", true);
    expect(client.calls[0]?.params).toEqual({ kind: "session", id: "local:abc", archived: true });
  });

  test("includes workingDir for a project", async () => {
    const client = new FakeClient();
    client.on("evener/archive/set", () => ({ ok: true, navigation: { generation_id: "g1", targets: [] } }));
    connectionStore.getState().connect(client);

    await setArchived("project", "proj-key", true, "/home/user/proj");
    expect(client.calls[0]?.params).toEqual({
      kind: "project",
      id: "proj-key",
      archived: true,
      workingDir: "/home/user/proj",
    });
  });

  test("propagates an AppWire failure", async () => {
    const client = new FakeClient();
    client.on("evener/archive/set", () => {
      throw new Error("archive store error: boom");
    });
    connectionStore.getState().connect(client);

    await expect(setArchived("session", "x", true)).rejects.toThrow("archive store error: boom");
  });
});

describe("deleteProject", () => {
  test("sends the typed AppWire request and returns its result", async () => {
    const response = {
      deleted: ["a", "b"],
      skipped: [],
      navigation: { generation_id: "g1", targets: [] },
    };
    const client = new FakeClient();
    client.on("evener/project/delete", (params) => {
      expect(params).toEqual({ key: "proj-key", workingDir: "/home/user/proj" });
      return response;
    });
    connectionStore.getState().connect(client);

    await expect(deleteProject("proj-key", "/home/user/proj")).resolves.toEqual(response);
    expect(client.calls).toEqual([
      { method: "evener/project/delete", params: { key: "proj-key", workingDir: "/home/user/proj" } },
    ]);
  });

  test("propagates an AppWire conflict", async () => {
    const client = new FakeClient();
    client.on("evener/project/delete", () => {
      throw new Error("project has live sessions");
    });
    connectionStore.getState().connect(client);

    await expect(deleteProject("proj-key", "/dir")).rejects.toThrow("project has live sessions");
  });

  test("rejects when no AppWire client is connected", async () => {
    await expect(deleteProject("proj-key", "/dir")).rejects.toThrow("project delete action: no client connected");
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
