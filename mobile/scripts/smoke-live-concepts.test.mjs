import assert from "node:assert/strict";
import { createHash } from "node:crypto";
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
  installStagedBundle,
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
  const source =
    kind === "list"
      ? EXPECTED_LIVE_SEQUENCE.roster
      : kind === "read"
        ? EXPECTED_LIVE_SEQUENCE.conversation
        : EXPECTED_LIVE_SEQUENCE[kind];
  return {
    classification: EXPECTED_LIVE_SEQUENCE.classification,
    requests: [...source.requests],
    notifications: [...source.notifications],
  };
}

function receipt(kind, suffix) {
  return {
    kind,
    status: "accepted",
    receipt: `sha256:${String.fromCharCode(96 + Number(suffix)).repeat(64)}`,
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
        stagedBundle: {
          id: "com.primeradiant.evener",
          version: "0.1.0",
          hash: SHA_A,
          codesignVerified: true,
        },
        installReceipt: {
          bundleId: "com.primeradiant.evener",
          digest: SHA_C,
        },
        hub: {
          expectedVersion: "hub-1",
          observedVersion: "hub-1",
          protocolVersion: "evener-appwire-v3",
          originDigest: SHA_B,
        },
        connection: {
          status: "connected",
        },
      };
    case "stillwater-roster":
      return {
        rosterIds: ["row-a", "row-b"],
        retainedCount: 2,
        hasMore: false,
        completeness: "complete",
        listLimit: 501,
        expectedSequence: expected("list"),
      };
    case "stillwater-conversation":
      return {
        activeThreadId: "thread-opaque",
        titleDigest: SHA_A,
        selectedTitle: "Field Notes current conversation",
        transcriptIds: ["item-a"],
        readLimit: 50,
        expectedSequence: expected("read"),
      };
    case "stillwater-send":
      return {
        receipt: receipt("send", 1),
        baselineTranscriptIds: ["baseline-a"],
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
        selectedTitle: "Field Notes current conversation",
        draftBefore: "draft-sentinel",
        draftAfter: "draft-sentinel",
        beforeTreeDigest: SHA_A,
        afterTreeDigest: SHA_B,
      };
    case "constellation-steer-queue":
      return {
        receipts: [receipt("steer", 2), receipt("queue", 3)],
        expectedSequence: expected("steerQueue"),
      };
    case "field-notes-preserved":
      return {
        rosterIds: ["row-a", "row-b"],
        activeThreadId: "thread-opaque",
        selectedTitle: "Field Notes current conversation",
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
        usage: { digest: SHA_B, tokenCount: 4096, valueCount: 3 },
      };
    case "background-foreground":
      return {
        processIdBefore: 101,
        processIdAfter: 101,
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
        selectedTitle: "Field Notes current conversation",
        transcriptDigest: SHA_B,
        connectionTreeDigest: SHA_C,
      };
    case "fixture-absence":
      return {
        fixtureAbsent: true,
        searchAbsent: true,
        labAbsent: true,
        activeThreadId: "thread-opaque",
        titleDigest: SHA_A,
        selectedTitle: "Field Notes current conversation",
        hubVersion: "hub-1",
        protocolVersion: "evener-appwire-v3",
      };
    default:
      throw new Error(`unknown milestone ${milestone}`);
  }
}

function validObserved() {
  const rows = REQUIRED_LIVE_MILESTONES.map((milestone, index) => ({
    milestone,
    concept: CONCEPTS[milestone],
    threadIdentity:
      THREAD_MILESTONES.has(milestone) || milestone === "fixture-absence"
        ? "thread-opaque"
        : null,
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
  assert.deepEqual(EXPECTED_LIVE_SEQUENCE.send.requests, ["turn/start"]);
  assert.deepEqual(EXPECTED_LIVE_SEQUENCE.steerQueue.requests, [
    "turn/steer",
    "turn/queue",
  ]);
  assert.deepEqual(EXPECTED_LIVE_SEQUENCE.interrupt.requests, [
    "turn/interrupt",
  ]);
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
  await t.test("missing final app identity", () =>
    rejects((rows) => {
      delete rows[11].evidence.activeThreadId;
      rows[11].positiveMarker.markerDigest = derivePositiveMarker(rows[11]);
    }, /final.*identity|active thread/i),
  );
  await t.test("wrong final Hub", () =>
    rejects((rows) => {
      rows[11].evidence.hubVersion = "other-hub";
      rows[11].positiveMarker.markerDigest = derivePositiveMarker(rows[11]);
    }, /final.*Hub|Hub.*match/i),
  );
  await t.test("missing roster list limit", () =>
    rejects((rows) => {
      delete rows[1].evidence.listLimit;
      rows[1].positiveMarker.markerDigest = derivePositiveMarker(rows[1]);
    }, /list limit/i),
  );
  await t.test("missing conversation read limit", () =>
    rejects((rows) => {
      delete rows[2].evidence.readLimit;
      rows[2].positiveMarker.markerDigest = derivePositiveMarker(rows[2]);
    }, /read limit/i),
  );
  await t.test("altered nonempty expected sequence", () =>
    rejects((rows) => {
      rows[3].evidence.expectedSequence.requests = ["thread/not-send"];
      rows[3].positiveMarker.markerDigest = derivePositiveMarker(rows[3]);
    }, /expected.*requests|sequence/i),
  );
  await t.test("duplicate receipt sequence", () =>
    rejects((rows) => {
      rows[5].evidence.receipts[1].receipt =
        rows[5].evidence.receipts[0].receipt;
      rows[5].positiveMarker.markerDigest = derivePositiveMarker(rows[5]);
    }, /status digests.*distinct/i),
  );
  await t.test("unchanged background tree", () =>
    rejects((rows) => {
      rows[9].evidence.foregroundTreeDigest = SHA_A;
      rows[9].positiveMarker.markerDigest = derivePositiveMarker(rows[9]);
    }, /background.*foreground|trees must differ/i),
  );
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
      EVENER_SMOKE_THREAD_TITLE: "Other live session",
    }),
    {
      appPath: "/tmp/Evener.app",
      threadRef: "raw-sensitive-ref",
      threadTitle: "Other live session",
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

function completeAxDocument(elements, overrides = {}) {
  return {
    backend: "axbridge-persistent",
    target: {
      kind: "frontmost",
      pid: null,
      x: null,
      y: null,
      value: null,
      match_key: null,
    },
    screen: { width: 390, height: 844, coordinate_space: "screen" },
    truncated: false,
    modal: null,
    elements,
    profile: null,
    coverage: null,
    interaction: null,
    frames: null,
    automation: null,
    ...overrides,
  };
}

test("parseAxDocument accepts only complete unblocked physical documents", () => {
  const parsed = parseAxDocument(
    completeAxDocument([
      {
        label: "Stillwater sessions",
        type: "Group",
        children: [
          {
            label: "Message",
            value: "draft-sentinel",
            type: "TextArea",
          },
        ],
      },
    ]),
  );
  assert.deepEqual(parsed.nodes, [
    {
      label: "Stillwater sessions",
      value: null,
      role: "Group",
      descendants: ["Message", "draft-sentinel"],
    },
    {
      label: "Message",
      value: "draft-sentinel",
      role: "TextArea",
      descendants: [],
    },
  ]);
  assert.throws(
    () =>
      parseAxDocument({
        ...completeAxDocument([]),
        backend: "ax",
        elements: [{ "data-thread-key": "not-AX" }],
      }),
    /AX node/i,
  );
  assert.throws(
    () =>
      parseAxDocument({
        ...completeAxDocument([axNode("Stillwater sessions")]),
        format: "complete",
      }),
    /complete AX document/i,
  );
  assert.throws(
    () => parseAxDocument([axNode("Stillwater sessions")]),
    /complete AX document/i,
  );
  for (const invalid of [
    completeAxDocument([axNode("Stillwater sessions")], { truncated: true }),
    completeAxDocument([axNode("Stillwater sessions")], { omitted_count: 1 }),
    completeAxDocument([axNode("Stillwater sessions")], {
      modal: { kind: "system", label: "Permission" },
    }),
    (() => {
      const value = completeAxDocument([axNode("Stillwater sessions")]);
      delete value.screen;
      return value;
    })(),
    completeAxDocument([axNode("Stillwater sessions")], {
      target: {
        kind: "application",
        pid: 123,
        x: null,
        y: null,
        value: null,
        match_key: null,
      },
    }),
  ]) {
    assert.throws(
      () => parseAxDocument(invalid),
      /complete|truncat|modal|target/i,
    );
  }
});

test("stages and re-verifies one immutable app artifact inside evidence root", async () => {
  const module = await import("./smoke-live-concepts.mjs");
  assert.equal(typeof module.stageAppBundle, "function");
  const parent = await mkdtemp(path.join(os.tmpdir(), "evener-stage-test-"));
  const source = path.join(parent, "Evener.app");
  await mkdir(source);
  await writeFile(path.join(source, "Info.plist"), "plist");
  await writeFile(path.join(source, "Evener"), "signed-binary");
  const paths = await createEvidenceRoot(path.join(parent, "evidence"));
  const run = async (program, argv) => {
    if (program === "codesign") return ok();
    return ok(
      argv[1] === "CFBundleIdentifier"
        ? "com.primeradiant.evener\n"
        : "0.1.0\n",
    );
  };
  const staged = await module.stageAppBundle(paths, source, { run });
  assert.equal(path.dirname(staged.path), paths.stageDir);
  assert.equal((await stat(paths.stageDir)).mode & 0o777, 0o700);
  assert.equal(staged.bundle.id, "com.primeradiant.evener");
  assert.match(staged.bundle.hash, /^sha256:/);
  await writeFile(path.join(source, "Evener"), "changed-source");
  const reverified = await inspectLocalAppBundle(staged.path, { run });
  assert.equal(reverified.hash, staged.bundle.hash);
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

test("staged install uses exact IDB receipt and companion-only devicectl fallback", async (t) => {
  const staged = {
    path: "/safe/output/staged-app/Evener.app",
    bundle: { id: "com.example.app", version: "1.0.0", hash: SHA_A },
  };
  await t.test("healthy", async () => {
    const calls = [];
    const result = await installStagedBundle(
      { udid: "u", bundleId: "com.example.app" },
      staged,
      async (program, argv) => {
        calls.push([program, ...argv]);
        return ok(
          argv[0] === "install"
            ? JSON.stringify({
                installedAppBundleId: "com.example.app",
                uuid: "install-uuid",
              })
            : "",
        );
      },
    );
    assert.equal(result.tool, "idb");
    assert.deepEqual(calls, [
      ["idb", "install", "--udid", "u", "--json", staged.path],
      ["idb", "launch", "--udid", "u", "com.example.app"],
    ]);
  });
  await t.test("companion loss", async () => {
    const calls = [];
    const result = await installStagedBundle(
      { udid: "u", bundleId: "com.example.app" },
      staged,
      async (program, argv) => {
        calls.push([program, ...argv]);
        if (program === "idb") {
          const error = new Error("companion unavailable");
          error.code = "IDB_COMPANION_LOST";
          throw error;
        }
        return ok("installed");
      },
    );
    assert.equal(result.tool, "devicectl");
    assert.deepEqual(calls, [
      ["idb", "install", "--udid", "u", "--json", staged.path],
      [
        "xcrun",
        "devicectl",
        "device",
        "install",
        "app",
        "--device",
        "u",
        staged.path,
      ],
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
  await t.test("generic failure", async () => {
    const calls = [];
    await assert.rejects(
      installStagedBundle(
        { udid: "u", bundleId: "com.example.app" },
        staged,
        async (program, argv) => {
          calls.push([program, ...argv]);
          throw new Error("generic");
        },
      ),
      /generic/,
    );
    assert.equal(
      calls.some((call) => call[0] === "xcrun"),
      false,
    );
  });
});

test("pollSemanticTree shares one overall monotonic budget", async () => {
  let now = 0;
  let calls = 0;
  const remainingBudgets = [];
  await assert.rejects(
    pollSemanticTree({
      readTree: async (remainingMs) => {
        remainingBudgets.push(remainingMs);
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
  assert.deepEqual(remainingBudgets, [10_000, 6_000, 2_000]);
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
      tools: { idb: { helpDigest: SHA_A, capabilitiesDigest: SHA_B } },
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
        hub: { originDigest: SHA_B },
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

test("evidence root rejects unsafe parent and publication rolls back a partial link", async () => {
  const unsafeParent = await mkdtemp(
    path.join(os.tmpdir(), "evener-evidence-unsafe-"),
  );
  await chmod(unsafeParent, 0o777);
  await assert.rejects(
    createEvidenceRoot(path.join(unsafeParent, "output")),
    /parent.*mode|unsafe.*parent/i,
  );

  const safeParent = await mkdtemp(
    path.join(os.tmpdir(), "evener-evidence-partial-"),
  );
  const paths = await createEvidenceRoot(path.join(safeParent, "output"));
  let writes = 0;
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
        async writeBytes(handle, bytes) {
          writes += 1;
          if (writes === 2) throw new Error("second publication failed");
          await handle.writeFile(bytes);
          await handle.sync();
        },
      },
    ),
    /evidence write failed/i,
  );
  assert.deepEqual(await readdir(paths.scratchDir), []);
  assert.equal(
    (await readdir(paths.outputDir)).includes("live-smoke-summary.json"),
    false,
  );

  const { rename } = await import("node:fs/promises");
  const movedParent = `${safeParent}-moved`;
  await rename(safeParent, movedParent);
  await mkdir(safeParent, { mode: 0o700 });
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
    /path changed/i,
  );
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
    { argv: ["idb", "--help"], result: ok("help") },
  ]);
  await assert.rejects(script.run("idb", ["describe"]), /Expected values/);
});

test("exact command script rejects a missing command", () => {
  const script = new ExactCommandScript([
    { argv: ["idb", "--help"], result: ok("help") },
  ]);
  assert.throws(() => script.assertDone());
});

test("runtime IDB capability preflight uses exact installed syntax and never invokes xcrun", async () => {
  const script = new ExactCommandScript([
    {
      argv: ["idb", "--help"],
      result: ok(
        "Usage: idb [OPTIONS] COMMAND\nOptions:\n  --help\nCommands:\n  install\n",
      ),
    },
    {
      argv: ["idb", "ui", "describe-all", "--help"],
      result: ok(
        "Usage: idb ui describe-all [OPTIONS]\nOptions:\n  --format {default,nested,complete}  complete is a consolidated object\n  --json\n  --udid TEXT",
      ),
    },
    {
      argv: ["idb", "ui", "tap", "--help"],
      result: ok(
        "Usage: idb ui tap [OPTIONS] TARGET\nOptions:\n  --match-key {AXLabel,AXValue}\n  --udid TEXT",
      ),
    },
    {
      argv: ["idb", "ui", "set-value", "--help"],
      result: ok(
        "Usage: idb ui set-value [OPTIONS] TARGET\nOptions:\n  --value TEXT\n  --match-key {AXLabel,AXValue}\n  --udid TEXT",
      ),
    },
    {
      argv: ["idb", "ui", "button", "--help"],
      result: ok(
        "Usage: idb ui button [OPTIONS] BUTTON\nOptions:\n  --udid TEXT",
      ),
    },
    {
      argv: ["idb", "launch", "--help"],
      result: ok(
        "Usage: idb launch [OPTIONS] BUNDLE_ID\nOptions:\n  -f, --foreground-if-running\n  --udid TEXT",
      ),
    },
    {
      argv: ["idb", "terminate", "--help"],
      result: ok(
        "Usage: idb terminate [OPTIONS] BUNDLE_ID\nOptions:\n  --udid TEXT",
      ),
    },
    {
      argv: ["idb", "install", "--help"],
      result: ok(
        "Usage: idb install [OPTIONS] BUNDLE_PATH\nOptions:\n  --udid TEXT\n  --json",
      ),
    },
    {
      argv: ["idb", "describe", "--help"],
      result: ok(
        "Usage: idb describe [OPTIONS]\nOptions:\n  --diagnostics\n  --json\n  --udid TEXT",
      ),
    },
    {
      argv: ["idb", "list-apps", "--help"],
      result: ok(
        "Usage: idb list-apps [OPTIONS]\nOptions:\n  --fetch-process-state\n  --json\n  --udid TEXT",
      ),
    },
  ]);
  const evidence = await verifyIdbCapabilities({
    run: script.run.bind(script),
  });
  script.assertDone();
  assert.equal(Object.hasOwn(evidence, "versionDigest"), false);
  assert.match(evidence.helpDigest, /^sha256:/);
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
        return ok(
          call === 1
            ? "Usage: idb [OPTIONS] COMMAND\nOptions:\n  --help"
            : "Usage: idb ui describe-all [OPTIONS]\nOptions:\n  --json\n  --udid TEXT",
        );
      },
    }),
    /IDB capability unavailable/,
  );
  assert.equal(call, 2);
});

function axNode(label, role = "AXStaticText", value = undefined) {
  return value === undefined
    ? { label, type: role }
    : { label, value, type: role };
}

function axOutput(nodes, overrides = {}) {
  return JSON.stringify(completeAxDocument(nodes, overrides));
}

const HELP_OUTPUTS = new Map([
  [
    "--help",
    "Usage: idb [OPTIONS] COMMAND\nOptions:\n  --help\nCommands:\n  install",
  ],
  [
    "ui describe-all --help",
    "Usage: idb ui describe-all [OPTIONS]\nOptions:\n  --format {default,nested,complete}  complete is a consolidated object\n  --json\n  --udid TEXT",
  ],
  [
    "ui tap --help",
    "Usage: idb ui tap [OPTIONS] TARGET\nOptions:\n  --match-key {AXLabel,AXValue}\n  --udid TEXT",
  ],
  [
    "ui set-value --help",
    "Usage: idb ui set-value [OPTIONS] TARGET\nOptions:\n  --value TEXT\n  --match-key {AXLabel,AXValue}\n  --udid TEXT",
  ],
  [
    "ui button --help",
    "Usage: idb ui button [OPTIONS] BUTTON\nOptions:\n  --udid TEXT",
  ],
  [
    "launch --help",
    "Usage: idb launch [OPTIONS] BUNDLE_ID\nOptions:\n  -f, --foreground-if-running\n  --udid TEXT",
  ],
  [
    "terminate --help",
    "Usage: idb terminate [OPTIONS] BUNDLE_ID\nOptions:\n  --udid TEXT",
  ],
  [
    "install --help",
    "Usage: idb install [OPTIONS] BUNDLE_PATH\nOptions:\n  --udid TEXT\n  --json",
  ],
  [
    "describe --help",
    "Usage: idb describe [OPTIONS]\nOptions:\n  --diagnostics\n  --json\n  --udid TEXT",
  ],
  [
    "list-apps --help",
    "Usage: idb list-apps [OPTIONS]\nOptions:\n  --fetch-process-state\n  --json\n  --udid TEXT",
  ],
]);

class StatefulIdbFake {
  constructor({
    appendDraft = false,
    reuseBaselineForStreaming = false,
    finalFault = null,
  } = {}) {
    this.appendDraft = appendDraft;
    this.reuseBaselineForStreaming = reuseBaselineForStreaming;
    this.finalFault = finalFault;
    this.calls = [];
    this.concept = "Stillwater";
    this.surface = "sessions";
    this.draft = "existing-draft";
    this.mode = "send";
    this.pid = 101;
    this.running = false;
    this.installed = false;
    this.background = false;
    this.terminated = false;
    this.serverSheet = false;
    this.switcher = false;
    this.mutation = null;
    this.receipt = 0;
    this.reasoningOpen = false;
    this.toolOpen = false;
    this.stagedPath = null;
    this.afterReconnect = false;
    this.finalPhase = false;
  }

  async run(program, argv) {
    this.calls.push([program, ...argv]);
    if (program === "codesign") {
      assert.deepEqual(argv.slice(0, 2), ["--verify", "--strict"]);
      return ok();
    }
    if (program === "plutil") {
      assert.equal(argv[0], "-extract");
      return ok(
        argv[1] === "CFBundleIdentifier"
          ? "com.primeradiant.evener\n"
          : "0.1.0\n",
      );
    }
    assert.equal(program, "idb", `healthy fake forbids ${program}`);
    const help = HELP_OUTPUTS.get(argv.join(" "));
    if (help !== undefined) return ok(help);
    if (argv[0] === "describe") {
      assert.deepEqual(argv, [
        "describe",
        "--diagnostics",
        "--json",
        "--udid",
        "phone",
      ]);
      return ok(
        JSON.stringify({
          name: "Jesse iPhone",
          udid: "phone",
          state: "Booted",
          type: "device",
          os_version: "20.0",
          architecture: "arm64",
          device: { model: "iPhone 17 Pro" },
        }),
      );
    }
    if (argv[0] === "install") {
      assert.deepEqual(argv.slice(0, 4), [
        "install",
        "--udid",
        "phone",
        "--json",
      ]);
      assert.match(argv[4], /\/staged-app\/Evener\.app$/);
      this.stagedPath = argv[4];
      this.installed = true;
      return ok(
        JSON.stringify({
          installedAppBundleId: "com.primeradiant.evener",
          uuid: "install-uuid",
        }),
      );
    }
    if (argv[0] === "launch") {
      const foreground = argv[1] === "-f";
      assert.deepEqual(argv, [
        "launch",
        ...(foreground ? ["-f"] : []),
        "--udid",
        "phone",
        "com.primeradiant.evener",
      ]);
      assert.equal(this.installed, true);
      this.running = true;
      if (this.terminated) {
        this.pid = 202;
        this.surface = "sessions";
        this.terminated = false;
        this.afterReconnect = true;
      } else if (foreground) {
        assert.equal(this.background, true);
        this.background = false;
        this.surface = "conversation";
      }
      return ok();
    }
    if (argv[0] === "terminate") {
      assert.deepEqual(argv, [
        "terminate",
        "--udid",
        "phone",
        "com.primeradiant.evener",
      ]);
      this.running = false;
      this.terminated = true;
      return ok();
    }
    if (argv[0] === "list-apps") {
      assert.deepEqual(argv, [
        "list-apps",
        "--fetch-process-state",
        "--json",
        "--udid",
        "phone",
      ]);
      return ok(
        `${JSON.stringify({
          bundle_id: "com.primeradiant.evener",
          name: "Evener",
          install_type: "user",
          architectures: ["arm64"],
          process_state: this.running ? "Running" : "Not running",
          debuggable: true,
          pid: this.running ? this.pid : 0,
        })}\n`,
      );
    }
    if (argv[0] === "ui" && argv[1] === "describe-all") {
      assert.deepEqual(argv, [
        "ui",
        "describe-all",
        "--format",
        "complete",
        "--json",
        "--udid",
        "phone",
      ]);
      const output = axOutput(this.nodes(), this.documentOverrides());
      if (
        this.afterReconnect &&
        this.surface === "conversation" &&
        !this.finalPhase
      ) {
        this.finalPhase = true;
      }
      this.advanceMutation();
      return ok(output);
    }
    if (argv[0] === "ui" && argv[1] === "set-value") {
      assert.deepEqual(argv, [
        "ui",
        "set-value",
        "Message",
        "--value",
        argv[4],
        "--match-key",
        "AXLabel",
        "--udid",
        "phone",
      ]);
      this.draft = this.appendDraft ? `${this.draft}${argv[4]}` : argv[4];
      return ok();
    }
    if (argv[0] === "ui" && argv[1] === "tap") {
      assert.deepEqual(argv.slice(2), [
        argv[2],
        "--match-key",
        "AXLabel",
        "--udid",
        "phone",
      ]);
      this.tap(argv[2]);
      return ok();
    }
    if (argv[0] === "ui" && argv[1] === "button") {
      assert.deepEqual(argv, ["ui", "button", "HOME", "--udid", "phone"]);
      this.background = true;
      return ok();
    }
    throw new Error(`unexpected command idb ${argv.join(" ")}`);
  }

  tap(label) {
    if (/ active server /.test(label)) {
      this.serverSheet = true;
      return;
    }
    if (label === "Done") {
      this.serverSheet = false;
      return;
    }
    if (label.startsWith("Open ")) {
      this.surface = "conversation";
      if (this.afterReconnect) this.finalPhase = false;
      return;
    }
    if (label.startsWith("Use ")) {
      this.mode = label.split(" ")[1];
      return;
    }
    if (label === "Submit message") {
      this.startMutation(this.mode);
      return;
    }
    if (label === "Interrupt") {
      this.startMutation("interrupt");
      return;
    }
    if (label === "Reasoning") {
      this.reasoningOpen = true;
      return;
    }
    if (label === "exec_command") {
      this.toolOpen = true;
      return;
    }
    if (label === "Switch concept") {
      this.switcher = true;
      return;
    }
    if (label.startsWith("Switch to ")) {
      this.concept = label.slice("Switch to ".length);
      this.switcher = false;
      return;
    }
    if (label === "Back" && this.surface === "conversation") {
      this.surface = "sessions";
      return;
    }
    if (label === "Work") {
      this.surface = "work";
      return;
    }
    if (label === "Close") {
      this.surface = "conversation";
    }
  }

  startMutation(kind) {
    this.receipt += 1;
    this.mutation = { kind, phase: 0, receipt: this.receipt };
    this.reasoningOpen = false;
    this.toolOpen = false;
  }

  advanceMutation() {
    if (this.mutation === null) return;
    const maximum = this.mutation.kind === "send" ? 2 : 1;
    if (this.mutation.phase < maximum) this.mutation.phase += 1;
  }

  nodes() {
    if (this.finalPhase && this.finalFault !== null) {
      if (this.finalFault === "springboard") {
        return [axNode("SpringBoard", "AXApplication")];
      }
      if (this.finalFault === "another-app") {
        return [axNode("Other Application", "AXApplication")];
      }
      if (this.finalFault === "wrong-concept") {
        return [
          ...this.connectionNodes(),
          axNode("Stillwater sessions", "AXGroup"),
        ];
      }
    }
    if (this.background) return [axNode("SpringBoard", "AXApplication")];
    if (this.serverSheet) {
      return [
        axNode("Servers", "AXDialog"),
        axNode("https://hub.example.test", "AXStaticText"),
        axNode("Done", "AXButton"),
      ];
    }
    if (this.surface === "sessions") return this.rosterNodes();
    if (this.surface === "work") return this.workNodes();
    return this.conversationNodes();
  }

  connectionNodes() {
    return [
      axNode(
        this.finalPhase && this.finalFault === "wrong-hub"
          ? "Connected to other-hub hub-9; protocol wrong-v9; app version 0.1.0"
          : "Connected to test-hub hub-1; protocol evener-appwire-v3; app version 0.1.0",
        "AXStaticText",
      ),
    ];
  }

  rosterNodes() {
    return [
      ...this.connectionNodes(),
      axNode("laptop active server reachable", "AXButton"),
      axNode(`${this.concept} sessions`, "AXGroup"),
      axNode(`${this.concept} sessions; 2 sessions; complete list`),
      axNode("Open Known live session; status running", "AXButton"),
      axNode("Open Other live session; status idle", "AXButton"),
      axNode("Switch concept", "AXButton"),
    ];
  }

  transcriptNodes() {
    const baseline = [
      axNode("Your message; completed", "AXGroup", "baseline question"),
      axNode(
        this.reuseBaselineForStreaming && this.mutation?.kind === "send"
          ? "Assistant response; streaming"
          : "Assistant response; completed",
        "AXGroup",
        "baseline response",
      ),
    ];
    if (this.mutation?.kind !== "send" || this.reuseBaselineForStreaming) {
      return baseline;
    }
    if (this.mutation.phase === 1) {
      return [
        ...baseline,
        axNode(
          "Assistant response; streaming",
          "AXGroup",
          "partial smoke-send",
        ),
      ];
    }
    if (this.mutation.phase >= 2) {
      return [
        ...baseline,
        axNode(
          "Assistant response; completed",
          "AXGroup",
          "complete response to smoke-send",
        ),
        axNode(
          "Reasoning; completed",
          "AXGroup",
          this.reasoningOpen ? "route summary" : "",
        ),
        axNode(
          "Tool exec_command; completed",
          "AXGroup",
          this.toolOpen ? "tool output" : "",
        ),
        axNode("Reasoning", "AXButton"),
        axNode("exec_command", "AXButton"),
      ];
    }
    return baseline;
  }

  mutationLabel() {
    if (this.mutation === null) return null;
    const title = `${this.mutation.kind[0].toUpperCase()}${this.mutation.kind.slice(1)}`;
    return this.mutation.phase === 0
      ? `${title} pending`
      : `${title} accepted by Hub`;
  }

  conversationNodes() {
    const nodes = [
      ...this.connectionNodes(),
      axNode(`${this.concept} conversation`, "AXGroup"),
      axNode(
        this.finalPhase && this.finalFault === "wrong-thread"
          ? "Session Known live session"
          : "Session Other live session",
        "AXGroup",
      ),
      axNode("Message", "AXTextArea", this.draft),
      axNode("Use send mode", "AXButton"),
      axNode("Use steer mode", "AXButton"),
      axNode("Use queue mode", "AXButton"),
      axNode("Submit message", "AXButton"),
      axNode("Interrupt", "AXButton"),
      axNode("Work", "AXButton"),
      axNode("Back", "AXButton"),
      axNode("Switch concept", "AXButton"),
      ...(this.switcher
        ? [
            axNode("Switch to Stillwater", "AXButton"),
            axNode("Switch to Constellation", "AXButton"),
            axNode("Switch to Field Notes", "AXButton"),
          ]
        : []),
      ...(this.mutationLabel() === null
        ? []
        : [axNode(this.mutationLabel(), "AXStaticText")]),
      ...this.transcriptNodes(),
    ];
    if (this.finalPhase && this.finalFault === "descendant-contamination") {
      nodes.push({
        label: "Transcript container",
        type: "AXGroup",
        children: [{ value: "synthetic_completion" }],
      });
    }
    if (this.finalPhase && this.finalFault === "forbidden-control") {
      nodes.push(axNode("Search", "AXButton"));
    }
    return nodes;
  }

  documentOverrides() {
    return this.finalPhase && this.finalFault === "modal"
      ? { modal: { kind: "system", label: "Permission" } }
      : {};
  }

  workNodes() {
    return [
      ...this.connectionNodes(),
      axNode("Field Notes work", "AXGroup"),
      axNode("Task Fix auth; status running", "AXGroup"),
      axNode("Delegate Investigate; status running", "AXGroup"),
      axNode("Job Run tests; status idle", "AXGroup"),
      {
        label: "Usage summary",
        type: "AXGroup",
        children: [
          axNode("Tokens"),
          axNode("4096"),
          axNode("Cost"),
          axNode("$0.42"),
        ],
      },
      axNode("Close", "AXButton"),
    ];
  }
}

async function runStatefulSmoke(options = {}) {
  const parent = await mkdtemp(
    path.join(os.tmpdir(), "evener-full-smoke-test-"),
  );
  const app = path.join(parent, "Evener.app");
  await mkdir(app);
  await writeFile(path.join(app, "Info.plist"), "plist");
  await writeFile(path.join(app, "Evener"), "binary");
  const fake = new StatefulIdbFake(options);
  let monotonic = 0;
  const now = () => {
    monotonic +=
      (options.fastFailure || options.finalFault !== undefined) &&
      fake.finalPhase
        ? 4_000
        : 1;
    return monotonic;
  };
  const result = await runLiveSmoke(
    {
      udid: "phone",
      bundleId: "com.primeradiant.evener",
      outputDir: path.join(parent, "evidence"),
      hubVersion: "hub-1",
    },
    {
      run: fake.run.bind(fake),
      now,
      env: {
        EVENER_SMOKE_APP_PATH: app,
        EVENER_SMOKE_THREAD_REF: "raw-sensitive-thread-ref",
        EVENER_SMOKE_THREAD_TITLE: "Other live session",
      },
      sentinels: {
        send: "smoke-send",
        steer: "smoke-steer",
        queue: "smoke-queue",
        preserve: "draft-preserve",
      },
    },
  );
  return { result, fake, app };
}

test("physical orchestration uses staged install, stateful set-value, and causal post-send AX", async () => {
  const { result, fake, app } = await runStatefulSmoke();
  assert.equal(result.summary.status, "passed");
  const summaryText = await readFile(result.paths.summaryPath, "utf8");
  const rawEvidence = JSON.parse(await readFile(result.paths.rawPath, "utf8"));
  const origin = "https://hub.example.test";
  const originDigest = `sha256:${createHash("sha256").update(origin).digest("hex")}`;
  assert.equal(result.summary.observations.length, 12);
  assert.equal(rawEvidence.observations.length, 12);
  assert.equal(rawEvidence.operational.threadRef, "raw-sensitive-thread-ref");
  assert.equal(summaryText.includes("raw-sensitive-thread-ref"), false);
  assert.equal(summaryText.includes(app), false);
  assert.equal(summaryText.includes("tool output"), false);
  assert.equal(summaryText.includes("partial"), false);
  assert.equal(result.summary.hub.originDigest, originDigest);
  assert.equal(JSON.stringify(rawEvidence).includes(origin), true);
  assert.equal(summaryText.includes(origin), false);
  assert.equal(
    fake.calls.some((call) => call[0] === "xcrun"),
    false,
  );
  assert.equal(
    fake.calls.some((call) => call[0] === "idb" && call[1] === "--version"),
    false,
  );
  assert.equal(
    fake.calls.some(
      (call) => call[0] === "idb" && call[1] === "ui" && call[2] === "text",
    ),
    false,
  );
  const install = fake.calls.find(
    (call) =>
      call[0] === "idb" && call[1] === "install" && call[2] === "--udid",
  );
  assert.ok(install);
  assert.match(install.at(-1), /\/staged-app\/Evener\.app$/);
  assert.ok(
    fake.calls.filter(
      (call) =>
        call[0] === "idb" && call[1] === "ui" && call[2] === "set-value",
    ).length >= 4,
  );
});

test("stateful fake proves set-value replaces rather than appends", async () => {
  await assert.rejects(
    runStatefulSmoke({ appendDraft: true, fastFailure: true }),
    /semantic tripwire|draft.*append|live workflow failed/i,
  );
});

test("stateful fake rejects a historical completed item transitioning backward", async () => {
  await assert.rejects(
    runStatefulSmoke({ reuseBaselineForStreaming: true, fastFailure: true }),
    /semantic tripwire|item lifecycle|live workflow failed/i,
  );
});

test("final absence remains bound to the reopened production app", async (t) => {
  for (const fault of [
    "springboard",
    "modal",
    "another-app",
    "wrong-concept",
    "wrong-thread",
    "wrong-hub",
    "forbidden-control",
  ]) {
    await t.test(fault, async () => {
      await assert.rejects(
        runStatefulSmoke({ finalFault: fault }),
        /semantic tripwire/i,
      );
    });
  }
  await t.test("transcript words are not fixture landmarks", async () => {
    await assert.doesNotReject(
      runStatefulSmoke({ finalFault: "descendant-contamination" }),
    );
  });
});
