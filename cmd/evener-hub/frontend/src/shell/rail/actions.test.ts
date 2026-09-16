// @vitest-environment node
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { WireError } from "../../protocol/errors";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { connectionStore } from "../../stores/connection";
import {
  assignSessionPin,
  deletePinSection,
  deleteProject,
  deleteSession,
  isPinSectionNotFound,
  projectOwnership,
  renamePinSection,
  setArchived,
  setFavorite,
  unpinSession,
} from "./actions";

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
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

// A project decision is keyed by (source, project ID), so every project-level
// request the rail issues must name the source that owns the row: omitting it
// addresses this hub's own project with the same ID, and storing a favorite or
// archive under that key leaves the remote row unchanged.
describe("source-qualified project mutations", () => {
  test("classifies the owning sources a summary reports", () => {
    expect(projectOwnership(undefined)).toEqual({ local: true, hosts: [] });
    expect(projectOwnership([])).toEqual({ local: true, hosts: [] });
    expect(projectOwnership(["local"])).toEqual({ local: true, hosts: [] });
    expect(projectOwnership(["host-a"])).toEqual({ local: false, hosts: ["host-a"] });
    expect(projectOwnership(["local", "host-a"])).toEqual({ local: true, hosts: ["host-a"] });
    expect(projectOwnership(["host-a", "host-a"])).toEqual({ local: false, hosts: ["host-a"] });
  });

  test("a remote-only project favorite carries the owning source", async () => {
    const client = new FakeClient();
    const response = { ok: true as const, navigation: { generation_id: "g1", targets: [] } };
    client.on("evener/favorite/set", (params) => {
      expect(params).toEqual({ kind: "project", id: "p", favorited: true, source: "host-a" });
      return response;
    });

    await expect(setFavorite(client, "project", "p", true, ["host-a"])).resolves.toEqual(response);
    expect(client.calls).toEqual([
      { method: "evener/favorite/set", params: { kind: "project", id: "p", favorited: true, source: "host-a" } },
    ]);
  });

  test("a merged project favorite addresses every owner, spelling the controller's own source as no field", async () => {
    const client = new FakeClient();
    client.on("evener/favorite/set", () => ({ ok: true, navigation: { generation_id: "g1", targets: [] } }));

    await setFavorite(client, "project", "p", false, ["local", "host-a"]);

    expect(client.calls.map((call) => call.params)).toEqual([
      { kind: "project", id: "p", favorited: false },
      { kind: "project", id: "p", favorited: false, source: "host-a" },
    ]);
  });

  test("a project summary with no reported sources keeps the controller-local request", async () => {
    const client = new FakeClient();
    client.on("evener/favorite/set", () => ({ ok: true, navigation: { generation_id: "g1", targets: [] } }));

    await setFavorite(client, "project", "p", true);

    expect(client.calls).toEqual([
      { method: "evener/favorite/set", params: { kind: "project", id: "p", favorited: true } },
    ]);
  });

  test("a remote-only project archive carries the owning source alongside workingDir", async () => {
    const client = new FakeClient();
    client.on("evener/archive/set", () => ({ ok: true, navigation: { generation_id: "g1", targets: [] } }));
    connectionStore.getState().connect(client);

    await setArchived("project", "p", true, "/host-a/proj", ["host-a"]);

    expect(client.calls).toEqual([
      {
        method: "evener/archive/set",
        params: { kind: "project", id: "p", archived: true, workingDir: "/host-a/proj", source: "host-a" },
      },
    ]);
  });

  test("a merged project archive archives under every owning source", async () => {
    const client = new FakeClient();
    client.on("evener/archive/set", () => ({ ok: true, navigation: { generation_id: "g1", targets: [] } }));
    connectionStore.getState().connect(client);

    await setArchived("project", "p", true, undefined, ["local", "host-a"]);

    expect(client.calls.map((call) => call.params)).toEqual([
      { kind: "project", id: "p", archived: true },
      { kind: "project", id: "p", archived: true, source: "host-a" },
    ]);
  });

  test("session archive is keyed by the ref alone and never carries a source", async () => {
    const client = new FakeClient();
    client.on("evener/archive/set", () => ({ ok: true, navigation: { generation_id: "g1", targets: [] } }));
    connectionStore.getState().connect(client);

    await setArchived("session", "host-a:r1", true, undefined, ["host-a"]);

    expect(client.calls).toEqual([
      { method: "evener/archive/set", params: { kind: "session", id: "host-a:r1", archived: true } },
    ]);
  });

  test("deleteProject refuses a remote-only project without issuing a request", async () => {
    const client = new FakeClient();
    connectionStore.getState().connect(client);

    await expect(deleteProject("p", "/host-a/proj", ["host-a"])).rejects.toThrow(
      "project delete is local-only; this project also belongs to host-a",
    );
    expect(client.calls).toEqual([]);
  });

  test("deleteProject refuses a merged project instead of deleting this hub's own rows", async () => {
    const client = new FakeClient();
    connectionStore.getState().connect(client);

    await expect(deleteProject("p", "/shared/proj", ["local", "host-a", "host-b"])).rejects.toThrow(
      "project delete is local-only; this project also belongs to host-a, host-b",
    );
    expect(client.calls).toEqual([]);
  });

  test("deleteProject keeps the plain local request for a controller-only project", async () => {
    const response = { deleted: ["a"], skipped: [], navigation: { generation_id: "g1", targets: [] } };
    const client = new FakeClient();
    client.on("evener/project/delete", () => response);
    connectionStore.getState().connect(client);

    await expect(deleteProject("p", "/local/proj", ["local"])).resolves.toEqual(response);
    expect(client.calls).toEqual([
      { method: "evener/project/delete", params: { key: "p", workingDir: "/local/proj" } },
    ]);
  });
});
