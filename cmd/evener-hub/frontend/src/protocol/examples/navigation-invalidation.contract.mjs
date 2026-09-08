import assert from "node:assert/strict";
import { test } from "node:test";
import { runNavigationInvalidation } from "./navigation-invalidation-logic.mjs";

function fake({ onRequest } = {}) {
  const callbacks = new Set();
  const calls = [];
  let readCount = 0;
  const hub = {
    callbacks,
    calls,
    connect: async () => calls.push("connect"),
    request: async (method, params) => {
      calls.push({ method, params });
      if (method === "evener/navigation/read") {
        readCount += 1;
        await onRequest?.(readCount, callbacks);
        return {
          status: "ok",
          generationId: readCount > 1 ? "g2" : "g1",
          revision: readCount,
          etag: `e${readCount}`,
          representation: "snapshot",
          data: manifestData(readCount > 1 ? "g2" : "g1", readCount),
        };
      }
      throw new Error(`unexpected ${method}`);
    },
    onNotification: (callback) => {
      callbacks.add(callback);
      return () => callbacks.delete(callback);
    },
    emit: (params) => {
      for (const callback of callbacks) callback({ method: "evener/navigation/invalidated", params });
    },
  };
  return hub;
}
const manifestData = (generationId, revision) => ({
  metadata: {
    generation_id: generationId,
    revision,
    sources: [],
    attentionSummary: {},
    sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  },
  entities: [],
  containers: [{ key: "manifest", owner: { kind: "resource_root", slot: "manifest" }, children: [] }],
});

const event = (generationId, sequence = 1) => ({
  generationId,
  sequence,
  targets: [{ kind: "manifest", revision: sequence }],
});

test("subscribes before first read and reconciles same-generation sequence gap", async () => {
  const hub = fake({
    onRequest: async (count, callbacks) => {
      if (count === 1)
        for (const callback of callbacks) callback({ method: "evener/navigation/invalidated", params: event("g1", 1) });
    },
  });
  const result = await runNavigationInvalidation(hub, { observe: async () => hub.emit(event("g1", 3)) });
  assert.equal(result.gap, true);
  assert.equal(result.reconciled, false);
  assert.equal(result.eventCount, 2);
  assert.equal(hub.calls.filter((call) => call?.method === "evener/navigation/read").length, 3);
});

test("captures generation change during reconciliation and marks overflow", async () => {
  const hub = fake({
    onRequest: async (count, callbacks) => {
      if (count === 2)
        for (const callback of callbacks) callback({ method: "evener/navigation/invalidated", params: event("g2", 9) });
    },
  });
  const result = await runNavigationInvalidation(hub, {
    maxEvents: 1,
    observe: async () => {
      hub.emit(event("g1", 1));
      hub.emit(event("g1", 2));
    },
  });
  assert.equal(result.generationChanged, true);
  assert.equal(result.outcome, "uncertain");
  assert.equal(result.reconciled, false);
  assert.equal(result.eventCount, 3);
  assert.equal(result.overflow, true);
});

test("retries when a notification arrives during readback and avoids false convergence", async () => {
  const callbacks = new Set();
  let reads = 0;
  const hub = {
    callbacks,
    calls: [],
    connect: async () => hub.calls.push("connect"),
    onNotification: (callback) => {
      callbacks.add(callback);
      return () => callbacks.delete(callback);
    },
    emit: (params) => {
      for (const callback of callbacks) callback({ method: "evener/navigation/invalidated", params });
    },
    request: async (method) => {
      hub.calls.push({ method });
      reads += 1;
      if (reads === 2) hub.emit(event("g2", 2));
      const generationId = reads < 3 ? "g1" : "g2";
      const revision = reads < 3 ? 1 : 2;
      return {
        status: "ok",
        representation: "snapshot",
        generationId,
        revision,
        etag: `e${reads}`,
        data: manifestData(generationId, revision),
      };
    },
  };
  const result = await runNavigationInvalidation(hub, { observe: async () => hub.emit(event("g1", 1)) });
  assert.equal(result.outcome, "read");
  assert.equal(result.readback.generationId, "g2");
  assert.equal(reads, 3);
  assert.equal(hub.callbacks.size, 0);
});

test("marks a same-generation readback current when it reaches the observed manifest revision", async () => {
  const hub = fake({
    onRequest: async (count, callbacks) => {
      if (count === 1)
        for (const callback of callbacks) callback({ method: "evener/navigation/invalidated", params: event("g1", 2) });
    },
  });
  let reads = 0;
  hub.request = async (method, params) => {
    hub.calls.push({ method, params });
    reads += 1;
    if (reads === 1) {
      for (const callback of hub.callbacks)
        callback({ method: "evener/navigation/invalidated", params: event("g1", 2) });
    }
    return {
      status: "ok",
      representation: "snapshot",
      generationId: "g1",
      revision: 2,
      etag: "e2",
      data: manifestData("g1", 2),
    };
  };
  const result = await runNavigationInvalidation(hub, { observe: async () => {} });
  assert.equal(result.outcome, "read");
  assert.equal(result.reconciled, true);
  assert.equal(result.readback.generationId, "g1");
});

test("rejects malformed responses and always unsubscribes", async () => {
  const hub = fake({ onRequest: async () => {} });
  hub.request = async () => ({
    status: "ok",
    representation: "snapshot",
    generationId: "g1",
    revision: 1,
    etag: "e",
  });
  await assert.rejects(runNavigationInvalidation(hub), /data/);
  assert.equal(hub.callbacks.size, 0);
});

test("rejects malformed invalidation without throwing through the emitter", async () => {
  const hub = fake();
  await assert.rejects(
    runNavigationInvalidation(hub, {
      observe: async () => hub.emit({ generationId: "g1", sequence: 1, targets: [{}] }),
    }),
    /target kind/,
  );
  assert.equal(hub.callbacks.size, 0);
});

test("unsubscribes when read fails", async () => {
  const hub = fake();
  hub.request = async () => {
    throw new Error("read failed");
  };
  await assert.rejects(runNavigationInvalidation(hub), /read failed/);
  assert.equal(hub.callbacks.size, 0);
});

test("rejects an unbounded observation duration before connecting", async () => {
  const hub = fake();
  await assert.rejects(runNavigationInvalidation(hub, { observeDurationMs: 10_001 }), /duration/);
  assert.deepEqual(hub.calls, []);
});

test("cleans up when reconciliation read fails", async () => {
  const hub = fake();
  let reads = 0;
  hub.request = async () => {
    reads += 1;
    if (reads === 2) throw new Error("reconciliation failed");
    return {
      status: "ok",
      representation: "snapshot",
      generationId: "g1",
      revision: 1,
      etag: "e",
      data: manifestData("g1", 1),
    };
  };
  await assert.rejects(
    runNavigationInvalidation(hub, { observe: async () => hub.emit(event("g1", 1)) }),
    /reconciliation failed/,
  );
  assert.equal(hub.callbacks.size, 0);
});

test("CLI reserves private output and writes metadata-only stdout", async () => {
  const { runNavigationInvalidationCLI } = await import("./navigation-invalidation-cli.mjs");
  const { mkdtemp, readFile, rm, stat } = await import("node:fs/promises");
  const { join } = await import("node:path");
  const directory = await mkdtemp("/tmp/navigation-cli-");
  const outputPath = join(directory, "readback.json");
  const hub = fake();
  const lines = [];
  await runNavigationInvalidationCLI(
    { EVENER_NAVIGATION_OUTPUT_FILE: outputPath, EVENER_NAVIGATION_OBSERVE_MS: "0" },
    hub,
    { log: (line) => lines.push(line) },
  );
  assert.equal((await stat(outputPath)).mode & 0o777, 0o600);
  assert.deepEqual(JSON.parse(await readFile(outputPath, "utf8")), {
    status: "ok",
    representation: "snapshot",
    generationId: "g1",
    revision: 1,
    etag: "e1",
    data: manifestData("g1", 1),
  });
  assert.equal(lines.length, 1);
  await rm(directory, { recursive: true, force: true });
  assert.deepEqual(JSON.parse(lines[0]), {
    outcome: "read",
    events: 0,
    overflow: false,
    reconciled: false,
    generationId: "g1",
    revision: 1,
    outputWritten: true,
  });
});

test("reserves output before connecting and rejects an occupied path", async () => {
  const { mkdtemp, rm, writeFile } = await import("node:fs/promises");
  const { join } = await import("node:path");
  const { runNavigationInvalidationCLI } = await import("./navigation-invalidation-cli.mjs");
  const directory = await mkdtemp("/tmp/navigation-cli-collision-");
  const outputPath = join(directory, "readback.json");
  await writeFile(outputPath, "existing");
  const hub = fake();
  await assert.rejects(
    runNavigationInvalidationCLI({ EVENER_NAVIGATION_OUTPUT_FILE: outputPath, EVENER_NAVIGATION_OBSERVE_MS: "0" }, hub),
    /exist/,
  );
  assert.deepEqual(hub.calls, []);
  await rm(directory, { recursive: true, force: true });
});

test("rejects targets with a mismatched selector", async () => {
  const hub = fake();
  await assert.rejects(
    runNavigationInvalidation(hub, {
      observe: async () =>
        hub.emit({ generationId: "g1", sequence: 1, targets: [{ kind: "manifest", projectKey: "p", revision: 1 }] }),
    }),
    /does not permit projectKey/,
  );
  assert.equal(hub.callbacks.size, 0);
});

test("CLI rejects a missing private output path before connecting", async () => {
  const { runNavigationInvalidationCLI } = await import("./navigation-invalidation-cli.mjs");
  const hub = fake();
  await assert.rejects(
    runNavigationInvalidationCLI({ EVENER_NAVIGATION_OBSERVE_MS: "0" }, hub),
    /EVENER_NAVIGATION_OUTPUT_FILE/,
  );
  assert.deepEqual(hub.calls, []);
});

test("rejects every forbidden all-loaded-projects selector key, including empty values", async () => {
  for (const key of ["section", "sectionId", "catalog", "projectKey"]) {
    const hub = fake();
    await assert.rejects(
      runNavigationInvalidation(hub, {
        observe: async () =>
          hub.emit({ generationId: "g1", sequence: 1, targets: [{ kind: "all_loaded_projects", [key]: "" }] }),
      }),
      /all-loaded-projects target/,
    );
    assert.equal(hub.callbacks.size, 0);
  }
});
