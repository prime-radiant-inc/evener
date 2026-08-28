import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import {
  chmod,
  lstat,
  mkdir,
  mkdtemp,
  readdir,
  readFile,
  stat,
  symlink,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  assertCompleteLiveSmoke,
  createEvidenceRoot,
  decodeDeviceDescription,
  decodeInstalledApps,
  derivePositiveMarker,
  EXPECTED_LIVE_SEQUENCE,
  inspectLocalAppBundle,
  launchProductionBundle,
  ProcessRegistry,
  parseAxDocument,
  parseCli,
  pollSemanticTree,
  publishEvidence,
  REQUIRED_LIVE_MILESTONES,
  readSmokeEnvironment,
  runLiveSmoke,
  runSpawned,
  verifyIdbCapabilities,
} from "./smoke-live-concepts.mjs";

const SHA_A = `sha256:${"a".repeat(64)}`;
const SHA_B = `sha256:${"b".repeat(64)}`;
const SHA_C = `sha256:${"c".repeat(64)}`;
const CONCEPTS = {
  "profile-connected": "Stillwater",
  "stillwater-roster": "Stillwater",
  "stillwater-conversation": "Stillwater",
  "stillwater-send": "Stillwater",
  "constellation-preserved": "Constellation",
  "constellation-steer-queue": "Constellation",
  "field-notes-preserved": "Field Notes",
  "field-notes-interrupt": "Field Notes",
  "work-activity-usage": "Field Notes",
  "background-foreground": "Field Notes",
  reconnect: "Field Notes",
  "fixture-absence": "Field Notes",
};
const THREAD_MILESTONES = new Set(REQUIRED_LIVE_MILESTONES.slice(2, 11));

function expected(kind) {
  return {
    classification: "expected-task4-proven",
    requests: [`thread/${kind}`],
    notifications: [`${kind} notification`],
  };
}

function receipt(kind, suffix) {
  return {
    kind,
    status: "accepted",
    receipt: Number(suffix),
    pendingTreeDigest: SHA_A,
    acceptedTreeDigest: suffix === 1 ? SHA_B : SHA_C,
  };
}

function evidenceFor(milestone) {
  switch (milestone) {
    case "profile-connected":
      return {
        device: { model: "iPhone 17 Pro", os: "20.0" },
        installedApp: {
          id: "com.primeradiant.evener",
          version: "0.1.0",
          pid: 101,
        },
        localBundle: {
          id: "com.primeradiant.evener",
          version: "0.1.0",
          hash: SHA_A,
          codesignVerified: true,
        },
        hub: {
          expectedVersion: "hub-1",
          observedVersion: "hub-1",
          protocolVersion: "evener-appwire-v3",
          originDigest: SHA_B,
        },
        connection: {
          status: "connected",
          profileGeneration: 7,
          lifecycleGeneration: 1,
          handshakeGeneration: 2,
        },
      };
    case "stillwater-roster":
      return {
        rosterIds: ["row-a", "row-b"],
        retainedCount: 2,
        hasMore: false,
        completeness: "complete",
        expectedSequence: expected("list"),
      };
    case "stillwater-conversation":
      return {
        activeThreadId: "thread-opaque",
        titleDigest: SHA_A,
        transcriptIds: ["item-a"],
        expectedSequence: expected("read"),
      };
    case "stillwater-send":
      return {
        receipt: receipt("send", 1),
        lifecycle: {
          itemId: "assistant-a",
          streamingTreeDigest: SHA_A,
          completedTreeDigest: SHA_B,
        },
        reasoning: { itemId: "reason-a", contentDigest: SHA_A },
        tool: { itemId: "tool-a", contentDigest: SHA_B },
        expectedSequence: expected("send"),
      };
    case "constellation-preserved":
      return {
        rosterIds: ["row-a", "row-b"],
        activeThreadId: "thread-opaque",
        draftBefore: "draft-sentinel",
        draftAfter: "draft-sentinel",
        beforeTreeDigest: SHA_A,
        afterTreeDigest: SHA_B,
      };
    case "constellation-steer-queue":
      return {
        receipts: [receipt("steer", 2), receipt("queue", 3)],
        expectedSequence: {
          classification: "expected-task4-proven",
          requests: ["thread/steer", "thread/queue"],
          notifications: ["steer notification", "queue notification"],
        },
      };
    case "field-notes-preserved":
      return {
        rosterIds: ["row-a", "row-b"],
        activeThreadId: "thread-opaque",
        draftBefore: "draft-sentinel",
        draftAfter: "draft-sentinel",
        beforeTreeDigest: SHA_A,
        afterTreeDigest: SHA_B,
      };
    case "field-notes-interrupt":
      return {
        receipt: receipt("interrupt", 4),
        expectedSequence: expected("interrupt"),
      };
    case "work-activity-usage":
      return {
        tasks: [{ id: "task-a", digest: SHA_A }],
        jobs: [{ id: "job-a", digest: SHA_A }],
        delegates: [{ id: "delegate-a", digest: SHA_A }],
        usage: { digest: SHA_B },
      };
    case "background-foreground":
      return {
        processIdBefore: 101,
        processIdAfter: 101,
        lifecycleGenerationBefore: 1,
        lifecycleGenerationAfter: 2,
        profileGenerationBefore: 7,
        profileGenerationAfter: 7,
        handshakeGenerationBefore: 2,
        handshakeGenerationAfter: 2,
        activeThreadId: "thread-opaque",
        draftBefore: "draft-sentinel",
        draftAfter: "draft-sentinel",
        backgroundTreeDigest: SHA_A,
        foregroundTreeDigest: SHA_B,
      };
    case "reconnect":
      return {
        processIdBefore: 101,
        processIdAfter: 202,
        reopened: true,
        activeThreadId: "thread-opaque",
        titleDigest: SHA_A,
        transcriptDigest: SHA_B,
        connectionTreeDigest: SHA_C,
      };
    case "fixture-absence":
      return { fixtureAbsent: true, searchAbsent: true, labAbsent: true };
    default:
      throw new Error(`unknown milestone ${milestone}`);
  }
}

function validObserved() {
  const rows = REQUIRED_LIVE_MILESTONES.map((milestone, index) => ({
    milestone,
    concept: CONCEPTS[milestone],
    threadIdentity: THREAD_MILESTONES.has(milestone) ? "thread-opaque" : null,
    fixtureAbsent: true,
    source: {
      tool: "idb",
      commandStatus: 0,
      commandDigest: SHA_A,
      semanticTreeDigest: index % 2 === 0 ? SHA_B : SHA_C,
      observedAtMonotonicMs: index + 1,
    },
    action: { kind: "semantic", label: `action-${index}` },
    evidence: evidenceFor(milestone),
  }));
  return rows.map((row) => ({
    ...row,
    positiveMarker: { markerDigest: derivePositiveMarker(row) },
  }));
}

function rejects(mutator, pattern) {
  const observed = structuredClone(validObserved());
  mutator(observed);
  assert.throws(() => assertCompleteLiveSmoke(observed), pattern);
}

test("exports the exact milestone and explicitly expected wire sequence contracts", () => {
  assert.deepEqual(REQUIRED_LIVE_MILESTONES, [
    "profile-connected",
    "stillwater-roster",
    "stillwater-conversation",
    "stillwater-send",
    "constellation-preserved",
    "constellation-steer-queue",
    "field-notes-preserved",
    "field-notes-interrupt",
    "work-activity-usage",
    "background-foreground",
    "reconnect",
    "fixture-absence",
  ]);
  assert.equal(EXPECTED_LIVE_SEQUENCE.classification, "expected-task4-proven");
  assert.doesNotThrow(() => assertCompleteLiveSmoke(validObserved()));
});

test("validator rejects order, concept, identity, time, fixture, and arbitrary marker failures", async (t) => {
  await t.test("missing", () => rejects((rows) => rows.pop(), /missing/i));
  await t.test("extra", () =>
    rejects((rows) => rows.push(structuredClone(rows[0])), /duplicate|extra/i),
  );
  await t.test("out of order", () =>
    rejects((rows) => {
      [rows[2], rows[3]] = [rows[3], rows[2]];
    }, /out of order/i),
  );
  await t.test("wrong concept", () =>
    rejects((rows) => {
      rows[4].concept = "Stillwater";
    }, /wrong concept/i),
  );
  await t.test("wrong thread", () =>
    rejects((rows) => {
      rows[9].threadIdentity = "other";
    }, /wrong thread/i),
  );
  await t.test("non-increasing time", () =>
    rejects((rows) => {
      rows[5].source.observedAtMonotonicMs =
        rows[4].source.observedAtMonotonicMs;
      rows[5].positiveMarker.markerDigest = derivePositiveMarker(rows[5]);
    }, /strictly increasing/i),
  );
  await t.test("fixture space variant", () =>
    rejects((rows) => {
      rows[3].fixtureAbsent = false;
      rows[3].contamination = "Fixture Session";
    }, /fixture/i),
  );
  await t.test("fixture hyphen variant", () =>
    rejects((rows) => {
      rows[3].fixtureAbsent = false;
      rows[3].contamination = "fixture-session";
    }, /fixture/i),
  );
  await t.test("arbitrary marker", () =>
    rejects((rows) => {
      rows[7].positiveMarker.markerDigest = SHA_A;
    }, /positive marker/i),
  );
  await t.test("nested passed true", () =>
    rejects((rows) => {
      rows[0].evidence.device.passed = true;
    }, /passed:true/i),
  );
});

test("validator rejects synthesized physical outcomes and empty expected sequences", async (t) => {
  await t.test("roster count", () =>
    rejects((rows) => {
      rows[1].evidence.retainedCount = 500;
      rows[1].positiveMarker.markerDigest = derivePositiveMarker(rows[1]);
    }, /retained count/i),
  );
  await t.test("empty destination roster", () =>
    rejects((rows) => {
      rows[4].evidence.rosterIds = [];
      rows[4].positiveMarker.markerDigest = derivePositiveMarker(rows[4]);
    }, /roster IDs/i),
  );
  await t.test("draft", () =>
    rejects((rows) => {
      rows[6].evidence.draftAfter = "changed";
      rows[6].positiveMarker.markerDigest = derivePositiveMarker(rows[6]);
    }, /draft/i),
  );
  await t.test("pending equals accepted", () =>
    rejects((rows) => {
      rows[3].evidence.receipt.acceptedTreeDigest = SHA_A;
      rows[3].positiveMarker.markerDigest = derivePositiveMarker(rows[3]);
    }, /pending.*accepted/i),
  );
  await t.test("missing streaming", () =>
    rejects((rows) => {
      rows[3].evidence.lifecycle.streamingTreeDigest = "";
      rows[3].positiveMarker.markerDigest = derivePositiveMarker(rows[3]);
    }, /streaming/i),
  );
  await t.test("missing reasoning", () =>
    rejects((rows) => {
      rows[3].evidence.reasoning.contentDigest = "";
      rows[3].positiveMarker.markerDigest = derivePositiveMarker(rows[3]);
    }, /reasoning/i),
  );
  await t.test("missing tool", () =>
    rejects((rows) => {
      rows[3].evidence.tool.contentDigest = "";
      rows[3].positiveMarker.markerDigest = derivePositiveMarker(rows[3]);
    }, /tool/i),
  );
  await t.test("same process reconnect", () =>
    rejects((rows) => {
      rows[10].evidence.processIdAfter = 101;
      rows[10].positiveMarker.markerDigest = derivePositiveMarker(rows[10]);
    }, /fresh process/i),
  );
  await t.test("different process foreground", () =>
    rejects((rows) => {
      rows[9].evidence.processIdAfter = 202;
      rows[9].positiveMarker.markerDigest = derivePositiveMarker(rows[9]);
    }, /same process/i),
  );
  await t.test("empty expected notifications", () =>
    rejects((rows) => {
      rows[5].evidence.expectedSequence.notifications = [];
      rows[5].positiveMarker.markerDigest = derivePositiveMarker(rows[5]);
    }, /expected.*notifications/i),
  );
});

test("removing every milestone action or observation source fails closed", async (t) => {
  for (let index = 0; index < REQUIRED_LIVE_MILESTONES.length; index += 1) {
    const milestone = REQUIRED_LIVE_MILESTONES[index];
    await t.test(`${milestone} action`, () =>
      rejects((rows) => {
        delete rows[index].action;
        rows[index].positiveMarker.markerDigest = derivePositiveMarker(
          rows[index],
        );
      }, /action/i),
    );
    await t.test(`${milestone} observation`, () =>
      rejects((rows) => {
        delete rows[index].source;
      }, /source|observation/i),
    );
  }
});

test("parseCli keeps exactly four flags and environment carries sensitive prerequisites", () => {
  assert.deepEqual(
    parseCli([
      "--udid",
      "phone",
      "--bundle-id",
      "com.primeradiant.evener",
      "--output-dir",
      "/tmp/new-evidence",
      "--hub-version",
      "hub-1",
    ]),
    {
      udid: "phone",
      bundleId: "com.primeradiant.evener",
      outputDir: "/tmp/new-evidence",
      hubVersion: "hub-1",
    },
  );
  assert.deepEqual(
    readSmokeEnvironment({
      EVENER_SMOKE_APP_PATH: "/tmp/Evener.app",
      EVENER_SMOKE_THREAD_REF: "raw-sensitive-ref",
    }),
    {
      appPath: "/tmp/Evener.app",
      threadRef: "raw-sensitive-ref",
    },
  );
  assert.throws(
    () => readSmokeEnvironment({ EVENER_SMOKE_THREAD_REF: "ref" }),
    /app prerequisite unavailable/i,
  );
  assert.throws(
    () => readSmokeEnvironment({ EVENER_SMOKE_APP_PATH: "/tmp/Evener.app" }),
    /thread prerequisite unavailable/i,
  );
  assert.throws(
    () =>
      parseCli([
        "--udid",
        "u",
        "--bundle-id",
        "b",
        "--output-dir",
        "relative",
        "--hub-version",
        "h",
        "--extra",
        "x",
      ]),
    /absolute|unknown/i,
  );
});

test("parseCli rejects a missing required flag with a fixed classification", () => {
  assert.throws(
    () => parseCli(["--udid", "u"]),
    /missing-cli-flag|invalid-cli/,
  );
});

test("parseCli rejects duplicate flags", () => {
  assert.throws(
    () =>
      parseCli([
        "--udid",
        "u",
        "--udid",
        "other",
        "--bundle-id",
        "b",
        "--output-dir",
        "/tmp/o",
        "--hub-version",
        "h",
      ]),
    /duplicate-cli-flag/,
  );
});

test("parseCli rejects unknown flags", () => {
  assert.throws(
    () =>
      parseCli([
        "--udid",
        "u",
        "--bundle-id",
        "b",
        "--output-dir",
        "/tmp/o",
        "--unknown",
        "h",
      ]),
    /unknown-cli-flag/,
  );
});

test("parseAxDocument recursively accepts plausible complete AX nodes and rejects DOM attributes", () => {
  const parsed = parseAxDocument({
    format: "complete",
    backend: "axbridge-persistent",
    elements: [
      {
        AXLabel:
          "Evener concept; concept Stillwater; surface sessions; session none",
        role: "AXGroup",
        children: [
          {
            AXLabel: "Message",
            AXValue: "draft-sentinel",
            role: "AXTextArea",
          },
        ],
      },
    ],
  });
  assert.deepEqual(parsed.nodes, [
    {
      label:
        "Evener concept; concept Stillwater; surface sessions; session none",
      value: null,
      role: "AXGroup",
    },
    { label: "Message", value: "draft-sentinel", role: "AXTextArea" },
  ]);
  assert.throws(
    () =>
      parseAxDocument({
        format: "complete",
        backend: "ax",
        elements: [{ "data-thread-key": "not-AX" }],
      }),
    /AX node/i,
  );
  assert.throws(
    () => parseAxDocument({ format: "default", elements: [] }),
    /complete AX document/i,
  );
});

test("strict IDB decoders use real device and app-listing fields", () => {
  assert.deepEqual(
    decodeDeviceDescription(
      JSON.stringify({
        name: "Jesse iPhone",
        udid: "phone",
        state: "Booted",
        type: "device",
        os_version: "20.0",
        architecture: "arm64",
        device: { model: "iPhone 17 Pro" },
      }),
      "phone",
    ),
    { model: "iPhone 17 Pro", os: "20.0" },
  );
  const apps = decodeInstalledApps(
    `${JSON.stringify({
      bundle_id: "com.primeradiant.evener",
      name: "Evener",
      install_type: "user",
      architectures: ["arm64"],
      process_state: "Running",
      debuggable: true,
      pid: 123,
    })}\n`,
  );
  assert.deepEqual(apps[0], {
    bundleId: "com.primeradiant.evener",
    name: "Evener",
    processState: "Running",
    pid: 123,
  });
  assert.throws(
    () => decodeDeviceDescription('{"name":"phone"}', "phone"),
    /device metadata/i,
  );
  assert.throws(
    () => decodeInstalledApps('{"bundle_id":"b","pid":"123"}\n'),
    /app listing/i,
  );
});

test("inspectLocalAppBundle codesign-verifies, reads Info.plist, and hashes recursively", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "evener-signed-app-test-"));
  const app = path.join(root, "Evener.app");
  await mkdir(path.join(app, "Frameworks"), { recursive: true });
  await writeFile(path.join(app, "Info.plist"), "plist-bytes", { mode: 0o600 });
  await writeFile(path.join(app, "Evener"), "executable", { mode: 0o700 });
  await writeFile(path.join(app, "Frameworks", "A"), "framework", {
    mode: 0o600,
  });
  const calls = [];
  const run = async (program, argv) => {
    calls.push([program, ...argv]);
    if (program === "codesign") return { status: 0, stdout: "", stderr: "" };
    const key = argv[1];
    return {
      status: 0,
      stdout:
        key === "CFBundleIdentifier" ? "com.primeradiant.evener\n" : "0.1.0\n",
      stderr: "",
    };
  };
  const first = await inspectLocalAppBundle(app, { run });
  const second = await inspectLocalAppBundle(app, { run });
  assert.deepEqual(first, second);
  assert.match(first.hash, /^sha256:[a-f0-9]{64}$/);
  assert.deepEqual(calls.slice(0, 3), [
    ["codesign", "--verify", "--strict", app],
    [
      "plutil",
      "-extract",
      "CFBundleIdentifier",
      "raw",
      "-o",
      "-",
      path.join(app, "Info.plist"),
    ],
    [
      "plutil",
      "-extract",
      "CFBundleShortVersionString",
      "raw",
      "-o",
      "-",
      path.join(app, "Info.plist"),
    ],
  ]);
  await symlink(path.join(app, "Evener"), path.join(app, "linked"));
  await assert.rejects(inspectLocalAppBundle(app, { run }), /symbolic link/i);
});

class FakeStream extends EventEmitter {
  setEncoding() {}
}

class FakeChild extends EventEmitter {
  constructor({ closeOnKill = true } = {}) {
    super();
    this.stdout = new FakeStream();
    this.stderr = new FakeStream();
    this.kills = [];
    this.closeOnKill = closeOnKill;
  }
  kill(signal) {
    this.kills.push(signal);
    if (this.closeOnKill) this.emit("close", null, signal);
    return true;
  }
}

function manualScheduler() {
  const callbacks = [];
  return {
    schedule(callback, _ms) {
      callbacks.push(callback);
      return callback;
    },
    cancel(handle) {
      const index = callbacks.indexOf(handle);
      if (index >= 0) callbacks.splice(index, 1);
    },
    fire() {
      const callback = callbacks.shift();
      if (!callback) throw new Error("missing scheduled callback");
      callback();
    },
  };
}

test("runSpawned aborts, kills, and reaps a never-resolving command at ten monotonic seconds", async () => {
  const child = new FakeChild();
  const scheduler = manualScheduler();
  let now = 0;
  const promise = runSpawned("idb", ["describe"], {
    spawnImpl: () => child,
    now: () => now,
    scheduler,
  });
  now = 10_000;
  scheduler.fire();
  await assert.rejects(promise, /command tripwire/i);
  assert.deepEqual(child.kills, ["SIGTERM", "SIGKILL"]);
});

test("healthy launch uses only exact IDB argv", async () => {
  const calls = [];
  const tool = await launchProductionBundle(
    { udid: "u", bundleId: "com.example.app" },
    async (program, argv) => {
      calls.push([program, ...argv]);
      return ok();
    },
  );
  assert.equal(tool, "idb");
  assert.deepEqual(calls, [
    ["idb", "launch", "--udid", "u", "com.example.app"],
  ]);
});

test("companion-loss launch alone falls back to exact devicectl argv", async () => {
  const calls = [];
  const tool = await launchProductionBundle(
    { udid: "u", bundleId: "com.example.app" },
    async (program, argv) => {
      calls.push([program, ...argv]);
      if (program === "idb") {
        const error = new Error("fixed");
        error.code = "IDB_COMPANION_LOST";
        throw error;
      }
      return ok();
    },
  );
  assert.equal(tool, "devicectl");
  assert.deepEqual(calls, [
    ["idb", "launch", "--udid", "u", "com.example.app"],
    [
      "xcrun",
      "devicectl",
      "device",
      "process",
      "launch",
      "--device",
      "u",
      "com.example.app",
    ],
  ]);
});

test("generic IDB launch failure never invokes devicectl", async () => {
  const calls = [];
  await assert.rejects(
    launchProductionBundle(
      { udid: "u", bundleId: "com.example.app" },
      async (program, argv) => {
        calls.push([program, ...argv]);
        throw new Error("generic");
      },
    ),
    /generic/,
  );
  assert.deepEqual(calls, [
    ["idb", "launch", "--udid", "u", "com.example.app"],
  ]);
});

test("pollSemanticTree shares one overall monotonic budget", async () => {
  let now = 0;
  let calls = 0;
  await assert.rejects(
    pollSemanticTree({
      readTree: async () => {
        calls += 1;
        now += 4_000;
        return { nodes: [] };
      },
      accept: () => false,
      now: () => now,
    }),
    /semantic tripwire/i,
  );
  assert.equal(calls, 3);
});

test("evidence publication creates exclusive roots, hashes exact raw bytes, and refuses overwrite", async () => {
  const parent = await mkdtemp(
    path.join(os.tmpdir(), "evener-evidence-parent-"),
  );
  const output = path.join(parent, "new-output");
  const paths = await createEvidenceRoot(output);
  assert.equal((await stat(output)).mode & 0o777, 0o700);
  assert.equal((await stat(paths.scratchDir)).mode & 0o777, 0o700);
  const raw = { secret: "hostile-token", nested: { threadRef: "raw-ref" } };
  const result = await publishEvidence(
    paths,
    {
      status: "passed",
      tools: { idb: { versionDigest: SHA_A, capabilitiesDigest: SHA_B } },
      device: { model: "iPhone 17 Pro", modelDigest: SHA_A, os: "20.0" },
      app: { id: "com.primeradiant.evener", version: "0.1.0", hash: SHA_A },
      hub: { version: "hub-1", protocol: "v3", originDigest: SHA_B },
      observations: [],
      hostileKey: "hostile-summary-value",
      nested: { authToken: "summary-token" },
    },
    raw,
  );
  const rawBytes = await readFile(result.rawPath);
  const summaryBytes = await readFile(result.summaryPath, "utf8");
  const summary = JSON.parse(summaryBytes);
  const { createHash } = await import("node:crypto");
  assert.equal(
    summary.linkDigest,
    `sha256:${createHash("sha256").update(rawBytes).digest("hex")}`,
  );
  assert.equal(summary.device.model, "iPhone 17 Pro");
  assert.equal(summaryBytes.includes("hostile-token"), false);
  assert.equal(summaryBytes.includes("raw-ref"), false);
  assert.equal(summaryBytes.includes("hostile-summary-value"), false);
  assert.equal(summaryBytes.includes("summary-token"), false);
  assert.equal((await stat(result.rawPath)).mode & 0o777, 0o600);
  assert.equal((await stat(result.summaryPath)).mode & 0o777, 0o600);
  await assert.rejects(
    publishEvidence(
      paths,
      {
        status: "passed",
        tools: {},
        device: {},
        app: {},
        hub: {},
        observations: [],
      },
      raw,
    ),
    /already exists|refusing to overwrite/i,
  );
  await assert.rejects(createEvidenceRoot(output), /already exists/i);
});

test("evidence publication rejects path swaps and cleans temporary files on write failure", async () => {
  const parent = await mkdtemp(path.join(os.tmpdir(), "evener-evidence-swap-"));
  const output = path.join(parent, "output");
  const paths = await createEvidenceRoot(output);
  await chmod(paths.scratchDir, 0o700);
  const moved = path.join(output, "moved-scratch");
  await import("node:fs/promises").then(({ rename }) =>
    rename(paths.scratchDir, moved),
  );
  await symlink(moved, paths.scratchDir);
  await assert.rejects(
    publishEvidence(
      paths,
      {
        status: "failed",
        tools: {},
        device: {},
        app: {},
        hub: {},
        observations: [],
      },
      { raw: true },
    ),
    /symbolic link|path changed/i,
  );
  assert.equal((await lstat(paths.scratchDir)).isSymbolicLink(), true);
});

test("evidence publication cleans exclusive temp files after an injected write failure", async () => {
  const parent = await mkdtemp(
    path.join(os.tmpdir(), "evener-evidence-write-"),
  );
  const paths = await createEvidenceRoot(path.join(parent, "output"));
  await assert.rejects(
    publishEvidence(
      paths,
      {
        status: "failed",
        tools: {},
        device: {},
        app: {},
        hub: {},
        observations: [],
      },
      { raw: true },
      {
        async writeBytes() {
          throw new Error("injected write failure with hostile-token");
        },
      },
    ),
    (error) => {
      assert.match(error.message, /evidence write failed/i);
      assert.equal(error.message.includes("hostile-token"), false);
      return true;
    },
  );
  assert.deepEqual(await readdir(paths.scratchDir), []);
  assert.equal((await readdir(paths.outputDir)).includes(".tmp"), false);
});

test("ProcessRegistry cleans every child", () => {
  const registry = new ProcessRegistry();
  const first = new FakeChild({ closeOnKill: false });
  const second = new FakeChild({ closeOnKill: false });
  registry.add(first);
  registry.add(second);
  registry.cleanup();
  assert.deepEqual(first.kills, ["SIGTERM", "SIGKILL"]);
  assert.deepEqual(second.kills, ["SIGTERM", "SIGKILL"]);
  assert.equal(registry.size, 0);
});

class ExactCommandScript {
  constructor(entries) {
    this.entries = [...entries];
    this.calls = [];
  }

  async run(program, argv) {
    this.calls.push([program, ...argv]);
    const next = this.entries.shift();
    assert.ok(next, `unexpected command ${program} ${argv.join(" ")}`);
    assert.deepEqual([program, ...argv], next.argv);
    if (typeof next.result === "function") return next.result();
    return next.result;
  }

  assertDone() {
    assert.deepEqual(this.entries, []);
  }
}

const ok = (stdout = "") => ({ status: 0, stdout, stderr: "" });

test("exact command script rejects reordered argv", async () => {
  const script = new ExactCommandScript([
    { argv: ["idb", "--version"], result: ok("v") },
  ]);
  await assert.rejects(script.run("idb", ["describe"]), /Expected values/);
});

test("exact command script rejects a missing command", () => {
  const script = new ExactCommandScript([
    { argv: ["idb", "--version"], result: ok("v") },
  ]);
  assert.throws(() => script.assertDone());
});

test("runtime IDB capability preflight uses exact installed syntax and never invokes xcrun", async () => {
  const script = new ExactCommandScript([
    { argv: ["idb", "--version"], result: ok("idb 1.5.0\n") },
    {
      argv: ["idb", "ui", "describe-all", "--help"],
      result: ok("--format {default,nested,complete} --json --udid"),
    },
    {
      argv: ["idb", "ui", "tap", "--help"],
      result: ok("target --match-key {AXLabel,AXValue} --udid"),
    },
    {
      argv: ["idb", "ui", "text", "--help"],
      result: ok("text --udid"),
    },
    {
      argv: ["idb", "ui", "button", "--help"],
      result: ok("{HOME,LOCK} --udid"),
    },
    {
      argv: ["idb", "launch", "--help"],
      result: ok("--foreground-if-running --udid bundle_id"),
    },
    {
      argv: ["idb", "terminate", "--help"],
      result: ok("--udid bundle_id"),
    },
    {
      argv: ["idb", "describe", "--help"],
      result: ok("--diagnostics --json --udid"),
    },
    {
      argv: ["idb", "list-apps", "--help"],
      result: ok("--fetch-process-state --json --udid"),
    },
  ]);
  const evidence = await verifyIdbCapabilities({
    run: script.run.bind(script),
  });
  script.assertDone();
  assert.match(evidence.versionDigest, /^sha256:/);
  assert.match(evidence.capabilitiesDigest, /^sha256:/);
  assert.equal(
    script.calls.some((call) => call[0] === "xcrun"),
    false,
  );
});

test("runtime IDB capability preflight fails when complete AX support is absent", async () => {
  let call = 0;
  await assert.rejects(
    verifyIdbCapabilities({
      run: async () => {
        call += 1;
        return ok(call === 2 ? "--json --udid" : "all required tokens");
      },
    }),
    /IDB capability unavailable/,
  );
  assert.equal(call, 2);
});

function axNode(label, role = "AXStaticText", value = undefined) {
  return value === undefined
    ? { AXLabel: label, role }
    : { AXLabel: label, AXValue: value, role };
}

function axOutput(nodes) {
  return JSON.stringify({
    format: "complete",
    backend: "axbridge-persistent",
    elements: nodes,
  });
}

function connectionLabel({
  lifecycle = 1,
  phase = "active",
  handshake = 1,
} = {}) {
  return `Evener connection; status connected; server hub-1; protocol evener-appwire-v3; profile-generation 7; lifecycle-phase ${phase}; lifecycle-generation ${lifecycle}; handshake-generation ${handshake}; app com.primeradiant.evener@0.1.0; origin ${SHA_B}`;
}

function conceptLabel(concept, surface, session = "none") {
  return `Evener concept; concept ${concept}; surface ${surface}; session ${session}`;
}

const ROW_LABEL =
  "Session row-a; Known live session; project evener/mobile; status running";

function rosterNodes(concept, connection = connectionLabel()) {
  return [
    axNode(connection, "AXGroup"),
    axNode(conceptLabel(concept, "sessions"), "AXGroup"),
    axNode(
      `Evener roster; concept ${concept}; retained 2; has-more false`,
      "AXStaticText",
    ),
    axNode(ROW_LABEL, "AXButton"),
    axNode(
      "Session row-b; Other live session; project evener/core; status idle",
      "AXButton",
    ),
    axNode("Switch concept", "AXButton"),
  ];
}

function conversationNodes({
  concept,
  draft = "",
  mutation = null,
  transcript = [],
  connection = connectionLabel(),
}) {
  return [
    axNode(connection, "AXGroup"),
    axNode(conceptLabel(concept, "conversation", "thread-opaque"), "AXGroup"),
    axNode("Conversation title Known live session", "AXHeading"),
    axNode("Message", "AXTextArea", draft),
    axNode("Use send mode", "AXButton"),
    axNode("Use steer mode", "AXButton"),
    axNode("Use queue mode", "AXButton"),
    axNode("Submit message", "AXButton"),
    axNode("Interrupt", "AXButton"),
    axNode("Work", "AXButton"),
    axNode("Back", "AXButton"),
    axNode("Switch concept", "AXButton"),
    ...(mutation === null ? [] : [axNode(mutation, "AXStaticText")]),
    ...transcript.map((label) => axNode(label, "AXGroup")),
  ];
}

function appLine(pid) {
  return `${JSON.stringify({
    bundle_id: "com.primeradiant.evener",
    name: "Evener",
    install_type: "user",
    architectures: ["arm64"],
    process_state: "Running",
    debuggable: true,
    pid,
  })}\n`;
}

function describeEntry(nodes) {
  return {
    argv: [
      "idb",
      "ui",
      "describe-all",
      "--format",
      "complete",
      "--json",
      "--udid",
      "phone",
    ],
    result: ok(axOutput(nodes)),
  };
}

function tapEntry(label) {
  return {
    argv: [
      "idb",
      "ui",
      "tap",
      label,
      "--match-key",
      "AXLabel",
      "--udid",
      "phone",
    ],
    result: ok(),
  };
}

function textEntry(value) {
  return {
    argv: ["idb", "ui", "text", value, "--udid", "phone"],
    result: ok(),
  };
}

function mutationEntries(kind, acceptedReceipt, finalTranscript = []) {
  const mode = `Use ${kind} mode`;
  return [
    tapEntry("Message"),
    textEntry(`smoke-${kind}`),
    ...(kind === "interrupt"
      ? []
      : [tapEntry(mode), tapEntry("Submit message")]),
    ...(kind === "interrupt" ? [tapEntry("Interrupt")] : []),
    describeEntry(
      conversationNodes({
        concept: kind === "send" ? "Stillwater" : "Constellation",
        mutation: `Evener mutation; kind ${kind}; status pending; receipt none`,
      }),
    ),
    describeEntry(
      conversationNodes({
        concept: kind === "send" ? "Stillwater" : "Constellation",
        mutation: `Evener mutation; kind ${kind}; status accepted; receipt ${acceptedReceipt}`,
        transcript: finalTranscript,
      }),
    ),
  ];
}

test("physical orchestration consumes an exact AX/argv script with distinct lifecycle and reconnect phases", async () => {
  const parent = await mkdtemp(
    path.join(os.tmpdir(), "evener-full-smoke-test-"),
  );
  const app = path.join(parent, "Evener.app");
  await mkdir(app);
  await writeFile(path.join(app, "Info.plist"), "plist");
  await writeFile(path.join(app, "Evener"), "binary");
  const outputDir = path.join(parent, "evidence");
  const device = JSON.stringify({
    name: "Jesse iPhone",
    udid: "phone",
    state: "Booted",
    type: "device",
    os_version: "20.0",
    architecture: "arm64",
    device: { model: "iPhone 17 Pro" },
  });
  const completedTranscript = [
    "Evener transcript item; id assistant-a; kind assistant; status completed; label Assistant; content complete response",
    "Evener transcript item; id reason-a; kind reasoning; status completed; label Reasoning; content route summary",
    "Evener transcript item; id tool-a; kind tool; status completed; label exec_command; content tool output",
  ];
  const script = new ExactCommandScript([
    { argv: ["idb", "--version"], result: ok("idb 1.5.0\n") },
    {
      argv: ["idb", "ui", "describe-all", "--help"],
      result: ok("--format complete --json --udid"),
    },
    {
      argv: ["idb", "ui", "tap", "--help"],
      result: ok("target --match-key AXLabel --udid"),
    },
    { argv: ["idb", "ui", "text", "--help"], result: ok("text --udid") },
    { argv: ["idb", "ui", "button", "--help"], result: ok("HOME --udid") },
    {
      argv: ["idb", "launch", "--help"],
      result: ok("--foreground-if-running --udid bundle_id"),
    },
    { argv: ["idb", "terminate", "--help"], result: ok("--udid bundle_id") },
    {
      argv: ["idb", "describe", "--help"],
      result: ok("--diagnostics --json --udid"),
    },
    {
      argv: ["idb", "list-apps", "--help"],
      result: ok("--fetch-process-state --json --udid"),
    },
    { argv: ["codesign", "--verify", "--strict", app], result: ok() },
    {
      argv: [
        "plutil",
        "-extract",
        "CFBundleIdentifier",
        "raw",
        "-o",
        "-",
        path.join(app, "Info.plist"),
      ],
      result: ok("com.primeradiant.evener\n"),
    },
    {
      argv: [
        "plutil",
        "-extract",
        "CFBundleShortVersionString",
        "raw",
        "-o",
        "-",
        path.join(app, "Info.plist"),
      ],
      result: ok("0.1.0\n"),
    },
    {
      argv: ["idb", "describe", "--diagnostics", "--json", "--udid", "phone"],
      result: ok(device),
    },
    {
      argv: [
        "idb",
        "list-apps",
        "--fetch-process-state",
        "--json",
        "--udid",
        "phone",
      ],
      result: ok(appLine(101)),
    },
    {
      argv: ["idb", "launch", "--udid", "phone", "com.primeradiant.evener"],
      result: ok(),
    },
    {
      argv: [
        "idb",
        "list-apps",
        "--fetch-process-state",
        "--json",
        "--udid",
        "phone",
      ],
      result: ok(appLine(101)),
    },
    describeEntry(rosterNodes("Stillwater")),
    describeEntry(rosterNodes("Stillwater")),
    tapEntry(ROW_LABEL),
    describeEntry(
      conversationNodes({
        concept: "Stillwater",
        transcript: completedTranscript,
      }),
    ),
    ...mutationEntries("send", 1, [
      "Evener transcript item; id assistant-a; kind assistant; status streaming; label Assistant; content partial",
    ]).slice(0, -1),
    describeEntry(
      conversationNodes({
        concept: "Stillwater",
        mutation: "Evener mutation; kind send; status pending; receipt none",
        transcript: [
          "Evener transcript item; id assistant-a; kind assistant; status streaming; label Assistant; content partial",
        ],
      }),
    ),
    describeEntry(
      conversationNodes({
        concept: "Stillwater",
        mutation: "Evener mutation; kind send; status accepted; receipt 1",
        transcript: completedTranscript,
      }),
    ),
    tapEntry("Message"),
    textEntry("draft-preserve"),
    describeEntry(
      conversationNodes({
        concept: "Stillwater",
        draft: "draft-preserve",
        transcript: completedTranscript,
      }),
    ),
    tapEntry("Switch concept"),
    tapEntry("Switch to Constellation"),
    describeEntry(
      conversationNodes({
        concept: "Constellation",
        draft: "draft-preserve",
        transcript: completedTranscript,
      }),
    ),
    tapEntry("Back"),
    describeEntry(rosterNodes("Constellation")),
    tapEntry(ROW_LABEL),
    describeEntry(
      conversationNodes({
        concept: "Constellation",
        transcript: completedTranscript,
      }),
    ),
    ...mutationEntries("steer", 2),
    ...mutationEntries("queue", 3),
    tapEntry("Message"),
    textEntry("draft-preserve"),
    describeEntry(
      conversationNodes({
        concept: "Constellation",
        draft: "draft-preserve",
        transcript: completedTranscript,
      }),
    ),
    tapEntry("Switch concept"),
    tapEntry("Switch to Field Notes"),
    describeEntry(
      conversationNodes({
        concept: "Field Notes",
        draft: "draft-preserve",
        transcript: completedTranscript,
      }),
    ),
    tapEntry("Back"),
    describeEntry(rosterNodes("Field Notes")),
    tapEntry(ROW_LABEL),
    describeEntry(
      conversationNodes({
        concept: "Field Notes",
        transcript: completedTranscript,
      }),
    ),
    tapEntry("Interrupt"),
    describeEntry(
      conversationNodes({
        concept: "Field Notes",
        mutation:
          "Evener mutation; kind interrupt; status pending; receipt none",
        transcript: completedTranscript,
      }),
    ),
    describeEntry(
      conversationNodes({
        concept: "Field Notes",
        mutation: "Evener mutation; kind interrupt; status accepted; receipt 4",
        transcript: completedTranscript,
      }),
    ),
    tapEntry("Message"),
    textEntry("draft-preserve"),
    describeEntry(
      conversationNodes({
        concept: "Field Notes",
        draft: "draft-preserve",
        transcript: completedTranscript,
      }),
    ),
    tapEntry("Work"),
    describeEntry([
      axNode(connectionLabel(), "AXGroup"),
      axNode(conceptLabel("Field Notes", "work", "thread-opaque"), "AXGroup"),
      axNode(
        "Evener work item; id task-a; kind task; status running; title Task",
        "AXGroup",
      ),
      axNode(
        "Evener work item; id delegate-a; kind delegate; status running; title Delegate",
        "AXGroup",
      ),
      axNode(
        "Evener work item; id job-a; kind job; status success; title Job",
        "AXGroup",
      ),
      axNode(
        "Evener usage; tokens 100; cost none; duration 1s; context 20%",
        "AXGroup",
      ),
      axNode("Close", "AXButton"),
    ]),
    tapEntry("Close"),
    describeEntry(
      conversationNodes({
        concept: "Field Notes",
        draft: "draft-preserve",
        transcript: completedTranscript,
      }),
    ),
    {
      argv: [
        "idb",
        "list-apps",
        "--fetch-process-state",
        "--json",
        "--udid",
        "phone",
      ],
      result: ok(appLine(101)),
    },
    { argv: ["idb", "ui", "button", "HOME", "--udid", "phone"], result: ok() },
    describeEntry([axNode("SpringBoard", "AXApplication")]),
    {
      argv: [
        "idb",
        "launch",
        "--foreground-if-running",
        "--udid",
        "phone",
        "com.primeradiant.evener",
      ],
      result: ok(),
    },
    {
      argv: [
        "idb",
        "list-apps",
        "--fetch-process-state",
        "--json",
        "--udid",
        "phone",
      ],
      result: ok(appLine(101)),
    },
    describeEntry(
      conversationNodes({
        concept: "Field Notes",
        draft: "draft-preserve",
        transcript: completedTranscript,
        connection: connectionLabel({ lifecycle: 2, phase: "foreground" }),
      }),
    ),
    {
      argv: [
        "idb",
        "list-apps",
        "--fetch-process-state",
        "--json",
        "--udid",
        "phone",
      ],
      result: ok(appLine(101)),
    },
    {
      argv: ["idb", "terminate", "--udid", "phone", "com.primeradiant.evener"],
      result: ok(),
    },
    {
      argv: ["idb", "launch", "--udid", "phone", "com.primeradiant.evener"],
      result: ok(),
    },
    {
      argv: [
        "idb",
        "list-apps",
        "--fetch-process-state",
        "--json",
        "--udid",
        "phone",
      ],
      result: ok(appLine(202)),
    },
    describeEntry(
      rosterNodes("Field Notes", connectionLabel({ handshake: 1 })),
    ),
    tapEntry(ROW_LABEL),
    describeEntry(
      conversationNodes({
        concept: "Field Notes",
        transcript: completedTranscript,
        connection: connectionLabel({ handshake: 1 }),
      }),
    ),
    describeEntry(
      conversationNodes({
        concept: "Field Notes",
        transcript: completedTranscript,
        connection: connectionLabel({ handshake: 1 }),
      }),
    ),
  ]);

  let monotonic = 0;
  const result = await runLiveSmoke(
    {
      udid: "phone",
      bundleId: "com.primeradiant.evener",
      outputDir,
      hubVersion: "hub-1",
    },
    {
      run: script.run.bind(script),
      now: () => ++monotonic,
      env: {
        EVENER_SMOKE_APP_PATH: app,
        EVENER_SMOKE_THREAD_REF: "raw-sensitive-thread-ref",
      },
      sentinels: {
        send: "smoke-send",
        steer: "smoke-steer",
        queue: "smoke-queue",
        preserve: "draft-preserve",
      },
    },
  );
  script.assertDone();
  assert.equal(result.summary.status, "passed");
  const summaryText = await readFile(result.paths.summaryPath, "utf8");
  const summary = JSON.parse(summaryText);
  const rawEvidence = JSON.parse(await readFile(result.paths.rawPath, "utf8"));
  assert.equal(summary.observations.length, 12);
  assert.equal(rawEvidence.observations.length, 12);
  assert.equal(rawEvidence.operational.threadRef, "raw-sensitive-thread-ref");
  assert.equal(summaryText.includes("raw-sensitive-thread-ref"), false);
  assert.equal(summaryText.includes(app), false);
  assert.equal(summaryText.includes("tool output"), false);
  assert.equal(summaryText.includes("partial"), false);
  assert.equal(
    script.calls.some((call) => call[0] === "xcrun"),
    false,
  );
});
