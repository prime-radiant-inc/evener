import assert from "node:assert/strict";
import test from "node:test";
import { runHubNotificationsCLI } from "./hub-notifications-cli.mjs";
import { decodeNotification, readSnapshot, runHubNotifications } from "./hub-notifications-logic.mjs";

const base = { provider: "fixture", activeSource: "none" };
test("decodes Batch 4 payloads with private-safe fields", () => {
  const samples = [
    ["evener/auth/updated", base],
    [
      "evener/attention/changed",
      {
        changed: [{ threadId: "t", title: "T", project: "p", level: "error", prevLevel: "working" }],
        summary: { needsYou: 1, error: 1, working: 0 },
      },
    ],
    ["evener/marketplace/updated", {}],
    ["evener/plugin/updated", {}],
    [
      "evener/sandbox/escalation/requested",
      {
        threadId: "t",
        ref: "local:t",
        escalationId: "e",
        mode: "read",
        tool: "cat",
        kind: "path",
        deniedPath: "/private",
      },
    ],
    ["evener/sandbox/escalation/resolved", { threadId: "t", ref: "local:t", escalationId: "e" }],
    [
      "evener/settings/transcriptDisplay/changed",
      {
        layout: "desktop",
        revision: 2,
        config: {
          version: 1,
          content: { kind: "plain" },
          advanced: {
            roundTimings: false,
            tokenCounts: false,
            estimatedCost: false,
            systemEvents: false,
            promptEvents: false,
            hookExits: "none",
          },
        },
      },
    ],
    ["evener/settings/keybindings/changed", { version: 1, revision: 3, rules: [] }],
  ];
  for (const [method, params] of samples)
    assert.equal(decodeNotification({ method, params }, { ref: "local:t" }).method, method);
});
test("rejects malformed matching and sandbox identity payloads", () => {
  assert.throws(() => decodeNotification({ method: "evener/attention/changed", params: { changed: [], summary: {} } }));
  assert.throws(() =>
    decodeNotification(
      {
        method: "evener/sandbox/escalation/requested",
        params: {
          threadId: "t",
          ref: "local:t",
          escalationId: "e",
          mode: "read",
          tool: "cat",
          kind: "path",
          deniedPath: "/x",
          partiallyRan: "no",
        },
      },
      { ref: "local:t" },
    ),
  );
  assert.throws(() =>
    decodeNotification({
      method: "evener/settings/keybindings/changed",
      params: { version: 1, revision: 1, rules: [{ action: 4, chord: null }] },
    }),
  );
  assert.equal(
    decodeNotification(
      {
        method: "evener/sandbox/escalation/resolved",
        params: { threadId: "t", ref: "local:other", escalationId: "e" },
      },
      { ref: "local:t" },
    ),
    null,
  );
});
test("reads only authoritative hub snapshots", async () => {
  const calls = [];
  const hub = {
    request: async (method, params) => {
      calls.push({ method, params });
      return method === "evener/auth/list"
        ? { providers: [] }
        : method === "evener/marketplace/list"
          ? { marketplaces: [] }
          : { plugins: [] };
    },
  };
  const result = await readSnapshot(hub, {
    methods: ["evener/auth/updated", "evener/marketplace/updated", "evener/plugin/updated"],
  });
  assert.deepEqual(result, { auth: { providers: [] }, marketplaces: { marketplaces: [] }, plugins: { plugins: [] } });
  assert.deepEqual(
    calls.map((call) => call.method),
    ["evener/auth/list", "evener/marketplace/list", "evener/plugin/list"],
  );
});
test("returns bounded read outcome and final-change metadata", async () => {
  let reads = 0;
  const hub = {
    onNotification: () => () => {},
    onStateChange: () => () => {},
    connect: async () => {},
    close: () => {},
    request: async (method) => {
      reads++;
      assert.equal(method, "evener/auth/list");
      return { providers: [] };
    },
  };
  const result = await runHubNotifications(hub, { methods: ["evener/auth/updated"], observeDurationMs: 0 });
  assert.equal(result.outcome, "read");
  assert.equal(reads, 2);
  assert.deepEqual(result.events, []);
});
test("CLI reserves private output before connect and emits metadata only", async () => {
  const { mkdtemp, writeFile, stat, readFile, rm } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const dir = await mkdtemp(join(tmpdir(), "hub-notifications-"));
  const output = join(dir, "result.json");
  await writeFile(output, "occupied\n");
  let connects = 0;
  const occupiedHub = {
    connect: async () => {
      connects++;
    },
  };
  await assert.rejects(
    runHubNotificationsCLI({ EVENER_HUB_NOTIFICATIONS_OUTPUT_FILE: output }, occupiedHub, { log: () => {} }),
  );
  assert.equal(connects, 0);
  assert.equal(await readFile(output, "utf8"), "occupied\n");
  await rm(output);
  const calls = [];
  const hub = {
    onNotification: () => () => {},
    onStateChange: () => () => {},
    connect: async () => {},
    close: () => {},
    request: async (method) => {
      calls.push(method);
      return { providers: [] };
    },
  };
  const methodsFile = join(dir, "methods.json");
  await writeFile(methodsFile, JSON.stringify(["evener/auth/updated"]));
  const lines = [];
  const result = await runHubNotificationsCLI(
    {
      EVENER_HUB_NOTIFICATIONS_OUTPUT_FILE: output,
      EVENER_HUB_NOTIFICATIONS_METHODS_FILE: methodsFile,
      EVENER_HUB_NOTIFICATIONS_OBSERVE_MS: "0",
    },
    hub,
    { log: (line) => lines.push(JSON.parse(line)) },
  );
  assert.equal(result.outcome, "read");
  assert.deepEqual(Object.keys(lines[0]).sort(), [
    "changedDuringReadback",
    "connectionInterrupted",
    "eventCount",
    "outcome",
    "outputWritten",
    "overflow",
  ]);
  assert.equal((await stat(output)).mode & 0o777, 0o600);
  await rm(dir, { recursive: true, force: true });
});

test("sandbox requires an explicit thread ref before connecting or reading", async () => {
  const hub = {
    request: () => {
      throw new Error("must not request");
    },
  };
  await assert.rejects(
    runHubNotifications(hub, { methods: ["evener/sandbox/escalation/requested"], observeDurationMs: 0 }),
  );
});
