import assert from "node:assert/strict";
import { mkdtemp, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { runSavedItemsCLI } from "./saved-items-cli.mjs";
import { runSavedItems, safeSavedItemsSummary } from "./saved-items-logic.mjs";

const url = "ws://127.0.0.1:1/rpc";
const nav = { generation_id: "g", targets: [] };
const responses = {
  favorite: { ok: true, navigation: nav },
  archive: { ok: true, navigation: nav },
  projectDelete: { deleted: ["s1"], skipped: [], navigation: nav },
  sessionDelete: { deleted: ["s1"], skipped: [], navigation: nav },
};
function fixture(response, onConnect) {
  const calls = [];
  return {
    calls,
    hub: {
      async connect() {
        calls.push({ method: "connect" });
        onConnect?.();
      },
      async request(method, params) {
        calls.push({ method, params });
        if (response instanceof Error) throw response;
        return response;
      },
    },
  };
}
async function optedIn(run) {
  const old = process.env.EVENER_SAVED_ITEMS_MUTATION;
  const oldURL = process.env.EVENER_RPC_URL;
  process.env.EVENER_SAVED_ITEMS_MUTATION = "1";
  process.env.EVENER_RPC_URL = url;
  try {
    await run();
  } finally {
    if (old === undefined) delete process.env.EVENER_SAVED_ITEMS_MUTATION;
    else process.env.EVENER_SAVED_ITEMS_MUTATION = old;
    if (oldURL === undefined) delete process.env.EVENER_RPC_URL;
    else process.env.EVENER_RPC_URL = oldURL;
  }
}
const cases = [
  [
    "favorite",
    "evener/favorite/set",
    { kind: "project", id: "p", favorited: true, reviewed: { kind: "project", id: "p" } },
  ],
  [
    "archive",
    "evener/archive/set",
    {
      kind: "project",
      id: "p",
      workingDir: "/w",
      archived: true,
      reviewed: { kind: "project", id: "p", workingDir: "/w" },
    },
  ],
  [
    "projectDelete",
    "evener/project/delete",
    {
      key: "p",
      workingDir: "/w",
      reviewed: { key: "p", workingDir: "/w" },
      confirmTarget: { key: "p", workingDir: "/w" },
    },
  ],
  [
    "sessionDelete",
    "evener/session/delete",
    { ref: "local:s", reviewed: { ref: "local:s" }, confirmTarget: { ref: "local:s" } },
  ],
];
test("saved-item actions capture reviewed inputs before connect and dispatch once", async () => {
  await optedIn(async () => {
    for (const [action, method, params] of cases) {
      const input = structuredClone(params);
      const captured = structuredClone(input);
      const f = fixture(responses[action], () => {
        input.reviewed = { changed: true };
      });
      const result = await runSavedItems(f.hub, { action, params: input, ownedHub: url });
      assert.equal(result.outcome, "acknowledged");
      assert.equal(f.calls.filter((call) => call.method === method).length, 1);
      const wire = Object.fromEntries(
        Object.entries(captured).filter(([key]) => !["reviewed", "confirmTarget"].includes(key)),
      );
      assert.deepEqual(f.calls.find((call) => call.method === method).params, wire);
    }
  });
});
test("deletions require exact confirmation and reject before connect", async () => {
  await optedIn(async () => {
    for (const [action, , params] of cases.slice(2)) {
      const f = fixture(responses[action]);
      await assert.rejects(
        runSavedItems(f.hub, {
          action,
          params: { ...params, confirmTarget: { ref: "wrong", key: "wrong" } },
          ownedHub: url,
        }),
      );
      assert.deepEqual(f.calls, []);
    }
  });
});
test("missing ownership rejects all actions before connect", async () => {
  const f = fixture(responses.favorite);
  await assert.rejects(runSavedItems(f.hub, { action: "favorite", params: cases[0][2], ownedHub: url }));
  assert.deepEqual(f.calls, []);
});
test("unknown parameters and empty local refs reject before connect", async () => {
  await optedIn(async () => {
    const unknown = fixture(responses.favorite);
    await assert.rejects(
      runSavedItems(unknown.hub, { action: "favorite", params: { ...cases[0][2], typo: true }, ownedHub: url }),
    );
    assert.deepEqual(unknown.calls, []);
    const empty = fixture(responses.sessionDelete);
    await assert.rejects(
      runSavedItems(empty.hub, {
        action: "sessionDelete",
        params: { ref: "local:", reviewed: { ref: "local:" }, confirmTarget: { ref: "local:" } },
        ownedHub: url,
      }),
    );
    assert.deepEqual(empty.calls, []);
  });
});
test("malformed navigation targets become uncertain without replay", async () => {
  await optedIn(async () => {
    const f = fixture({ ok: true, navigation: { generation_id: "g", targets: [{ kind: "unknown" }] } });
    const result = await runSavedItems(f.hub, {
      action: "favorite",
      params: { kind: "project", id: "p", favorited: true, reviewed: { kind: "project", id: "p" } },
      ownedHub: url,
    });
    assert.equal(result.outcome, "uncertain");
    assert.equal(f.calls.filter((call) => call.method === "evener/favorite/set").length, 1);
  });
});
test("navigation target union accepts zero revisions and wildcard, rejects missing selectors", async () => {
  await optedIn(async () => {
    for (const target of [{ kind: "project", projectKey: "p", revision: 0 }, { kind: "all_loaded_projects" }]) {
      const f = fixture({ ok: true, navigation: { generation_id: "g", targets: [target] } });
      const result = await runSavedItems(f.hub, {
        action: "favorite",
        params: { kind: "project", id: "p", favorited: true, reviewed: { kind: "project", id: "p" } },
        ownedHub: url,
      });
      assert.equal(result.outcome, "acknowledged");
    }
    for (const target of [
      { kind: "project", revision: 1 },
      { kind: "all_loaded_projects", revision: 1 },
      { kind: "section", sectionId: "x", revision: 1 },
    ]) {
      const f = fixture({ ok: true, navigation: { generation_id: "g", targets: [target] } });
      const result = await runSavedItems(f.hub, {
        action: "favorite",
        params: { kind: "project", id: "p", favorited: true, reviewed: { kind: "project", id: "p" } },
        ownedHub: url,
      });
      assert.equal(result.outcome, "uncertain");
      assert.equal(f.calls.filter((call) => call.method === "evener/favorite/set").length, 1);
    }
  });
});
test("lost and malformed replies are uncertain and never replayed", async () => {
  await optedIn(async () => {
    for (const response of [new Error("private"), { ok: true }]) {
      const f = fixture(response);
      const result = await runSavedItems(f.hub, {
        action: "favorite",
        params: { kind: "project", id: "p", favorited: true, reviewed: { kind: "project", id: "p" } },
        ownedHub: url,
      });
      assert.equal(result.outcome, "uncertain");
      assert.equal(f.calls.filter((call) => call.method === "evener/favorite/set").length, 1);
    }
  });
});
test("summary contains counts without response bodies", () => {
  assert.deepEqual(
    safeSavedItemsSummary({
      action: "projectDelete",
      outcome: "acknowledged",
      execution: "unverified",
      readback: responses.projectDelete,
    }),
    {
      action: "projectDelete",
      outcome: "acknowledged",
      execution: "unverified",
      deletedCount: 1,
      skippedCount: 0,
    },
  );
});
test("CLI requires a new private output before connecting and accepts injected ownership", async () => {
  const f = fixture(responses.favorite);
  await assert.rejects(runSavedItemsCLI({ EVENER_SAVED_ITEMS_ACTION: "favorite" }, f.hub), /OUTPUT_FILE/);
  assert.deepEqual(f.calls, []);
  const dir = await mkdtemp(join(tmpdir(), "saved-items-"));
  try {
    const paramsFile = join(dir, "params.json");
    await writeFile(
      paramsFile,
      JSON.stringify({ kind: "project", id: "p", favorited: true, reviewed: { kind: "project", id: "p" } }),
    );
    const lines = [];
    const result = await runSavedItemsCLI(
      {
        EVENER_SAVED_ITEMS_ACTION: "favorite",
        EVENER_SAVED_ITEMS_PARAMS_FILE: paramsFile,
        EVENER_SAVED_ITEMS_OUTPUT_FILE: join(dir, "r.json"),
        EVENER_SAVED_ITEMS_MUTATION: "1",
        EVENER_RPC_URL: url,
        EVENER_SAVED_ITEMS_OWNED_HUB: url,
      },
      f.hub,
      { stdout: (line) => lines.push(line) },
    );
    assert.equal(result.outcome, "acknowledged");
    assert.equal((await stat(join(dir, "r.json"))).mode & 0o777, 0o600);
    assert.deepEqual(JSON.parse(lines[0]), {
      action: "favorite",
      outcome: "acknowledged",
      execution: "unverified",
      outputWritten: true,
    });
    const occupied = join(dir, "occupied.json");
    await writeFile(occupied, "keep", { mode: 0o600 });
    const before = f.calls.length;
    await assert.rejects(
      runSavedItemsCLI(
        {
          EVENER_SAVED_ITEMS_ACTION: "favorite",
          EVENER_SAVED_ITEMS_PARAMS_FILE: paramsFile,
          EVENER_SAVED_ITEMS_OUTPUT_FILE: occupied,
          EVENER_SAVED_ITEMS_MUTATION: "1",
          EVENER_RPC_URL: url,
          EVENER_SAVED_ITEMS_OWNED_HUB: url,
        },
        f.hub,
      ),
      /EEXIST/,
    );
    assert.equal(f.calls.length, before);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
