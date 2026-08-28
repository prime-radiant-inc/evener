// @vitest-environment node
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { WireError } from "../../protocol/errors";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { connectionStore } from "../../stores/connection";
import {
  assignSessionPin,
  deletePinSection,
  deleteProject,
  deleteSession,
  isPinSectionNotFound,
  renamePinSection,
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
  test("assigns by name through the typed method and returns the canonical assignment", async () => {
    const client = new FakeClient();
    const response = {
      ok: true as const,
      changed: true,
      assignment: {
        sessionRef: "local:s1",
        section: { id: "canonical", name: "Research", memberCount: 1 },
      },
      navigation: { generation_id: "generation-a", targets: [] },
    };
    client.on("evener/session-pin/assign", (params) => {
      expect(params).toEqual({ sessionRef: "local:s1", sectionName: "Research" });
      return response;
    });

    await expect(assignSessionPin(client, "local:s1", { section_name: "Research" })).resolves.toEqual(response);
  });

  test("assigns by section ID without changing the target shape", async () => {
    const client = new FakeClient();
    const response = {
      ok: true as const,
      changed: false,
      assignment: { sessionRef: "local:s1", section: { id: "s/1", name: "Client", memberCount: 1 } },
      navigation: { generation_id: "generation-a", targets: [] },
    };
    client.on("evener/session-pin/assign", (params) => {
      expect(params).toEqual({ sessionRef: "local:s1", sectionId: "s/1" });
      return response;
    });

    await expect(assignSessionPin(client, "local:s1", { section_id: "s/1" })).resolves.toEqual(response);
  });

  test("assignment failures preserve the resource-not-found discriminator", async () => {
    const client = new FakeClient();
    client.on("evener/session-pin/assign", () => {
      throw new WireError("pin section not found", -32602, { evenerErrorInfo: "resourceNotFound" });
    });

    const error = await assignSessionPin(client, "local:s1", { section_id: "deleted" }).catch(
      (cause: unknown) => cause,
    );

    expect((error as Error).message).toBe("pin section not found");
    expect(isPinSectionNotFound(error)).toBe(true);
    expect(isPinSectionNotFound(new WireError("bad name", -32602, { evenerErrorInfo: "invalidParams" }))).toBe(false);
  });

  test("unpins through the typed method and preserves the navigation receipt", async () => {
    const client = new FakeClient();
    const response = {
      ok: true as const,
      changed: true,
      assignment: { sessionRef: "local:s/1" },
      navigation: { generation_id: "generation-a", targets: [] },
    };
    client.on("evener/session-pin/unpin", (params) => {
      expect(params).toEqual({ sessionRef: "local:s/1" });
      return response;
    });

    await expect(unpinSession(client, "local:s/1")).resolves.toEqual(response);
  });

  test("renames through the typed method and returns the canonical summary", async () => {
    const client = new FakeClient();
    const section = { id: "section/one", name: "New name", memberCount: 3 };
    const navigation = { generation_id: "g", targets: [] };
    const response = { ok: true as const, changed: true, section, navigation };
    client.on("evener/pin-section/rename", (params) => {
      expect(params).toEqual({ sectionId: "section/one", name: "New name" });
      return response;
    });

    await expect(renamePinSection(client, "section/one", "New name")).resolves.toEqual(response);
  });

  test("deletes through the typed method and returns the durable member count", async () => {
    const client = new FakeClient();
    const response = {
      ok: true as const,
      changed: true,
      memberCount: 4,
      navigation: { generation_id: "generation-a", targets: [] },
    };
    client.on("evener/pin-section/delete", (params) => {
      expect(params).toEqual({ sectionId: "section/one" });
      return response;
    });

    await expect(deletePinSection(client, "section/one")).resolves.toEqual(response);
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
    client.on("evener/project/delete", () => response);
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
  test("uses the typed AppWire method and returns its navigation receipt", async () => {
    const client = new FakeClient();
    const response = {
      deleted: ["abc"],
      skipped: [],
      navigation: { generation_id: "generation-3", targets: [] },
    };
    client.on("evener/session/delete", (params) => {
      expect(params).toEqual({ ref: "local:abc" });
      return response;
    });

    await expect(deleteSession(client, "local:abc")).resolves.toEqual(response);
    expect(client.calls).toEqual([{ method: "evener/session/delete", params: { ref: "local:abc" } }]);
  });

  test("a refused delete (live or reserved target) resolves with the session in skipped, not an error", async () => {
    const client = new FakeClient();
    client.on("evener/session/delete", () => ({
      deleted: [],
      skipped: [{ id: "abc", reason: "resumed live" }],
      navigation: { generation_id: "generation-3", targets: [] },
    }));

    const result = await deleteSession(client, "local:abc");
    expect(result.skipped).toEqual([{ id: "abc", reason: "resumed live" }]);
  });

  test("propagates AppWire failures", async () => {
    const client = new FakeClient();
    client.on("evener/session/delete", () => {
      throw new Error("invalid session ID: boom");
    });

    await expect(deleteSession(client, "local:abc")).rejects.toThrow("invalid session ID: boom");
  });
});
