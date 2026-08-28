import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import {
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  stat,
  symlink,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  assertCompleteLiveSmoke,
  launchProductionBundle,
  ProcessRegistry,
  parseCli,
  pollSemanticTree,
  REQUIRED_LIVE_MILESTONES,
  redactEvidence,
  runLiveSmoke,
  runSpawned,
  writeEvidence,
} from "./smoke-live-concepts.mjs";

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

function semanticSource(index) {
  return {
    tool: "idb",
    commandStatus: 0,
    commandDigest: `sha256:${String(index + 1).padStart(64, "a")}`,
    semanticTreeDigest: `sha256:${String(index + 1).padStart(64, "b")}`,
    observedAtMonotonicMs: index + 1,
  };
}

function baseEvidence(milestone) {
  switch (milestone) {
    case "profile-connected":
      return {
        device: { model: "iPhone 17 Pro", os: "iOS 20.0" },
        bundle: {
          id: "com.primeradiant.evener",
          version: "1.4.2",
          hash: `sha256:${"1".repeat(64)}`,
          signed: true,
        },
        hub: {
          expected: "abc123",
          observed: "abc123",
          protocol: "appwire/1",
          originDigest: `sha256:${"2".repeat(64)}`,
        },
        profileStatus: "connected",
      };
    case "stillwater-roster":
      return {
        listLimit: 501,
        retainedCount: 500,
        row501Present: true,
        hasMore: true,
        rosterIds: ["row-a", "row-b"],
        requestSequence: ["thread/list(limit=501)"],
        notificationSequence: ["evener/tree/changed"],
      };
    case "stillwater-conversation":
      return {
        readLimit: 200,
        activeThreadId: "thread-opaque",
        rosterIds: ["row-a", "row-b"],
        requestSequence: ["thread/read(subscribe=true,limit=200)"],
        notificationSequence: ["evener/thread/item-started"],
        transcriptItems: 2,
      };
    case "stillwater-send":
      return {
        draftBefore: "smoke-draft-sentinel",
        draftAfter: "smoke-draft-sentinel",
        receipt: {
          kind: "send",
          status: "accepted",
          idDigest: `sha256:${"3".repeat(64)}`,
        },
        requestSequence: ["thread/send"],
        notificationSequence: ["evener/thread/item-completed"],
      };
    case "constellation-preserved":
      return {
        rosterIds: ["row-a", "row-b"],
        activeThreadId: "thread-opaque",
        draftBefore: "smoke-draft-sentinel",
        draftAfter: "smoke-draft-sentinel",
        switchTrees: {
          beforeDigest: `sha256:${"9".repeat(64)}`,
          afterDigest: `sha256:${"a".repeat(64)}`,
        },
        preserved: ["thread", "draft", "roster"],
      };
    case "constellation-steer-queue":
      return {
        draftBefore: "smoke-draft-sentinel",
        draftAfter: "smoke-draft-sentinel",
        receipts: [
          {
            kind: "steer",
            status: "accepted",
            idDigest: `sha256:${"4".repeat(64)}`,
          },
          {
            kind: "queue",
            status: "accepted",
            idDigest: `sha256:${"5".repeat(64)}`,
          },
        ],
        requestSequence: ["thread/steer", "thread/queue"],
        notificationSequence: ["evener/thread/item-completed"],
      };
    case "field-notes-preserved":
      return {
        rosterIds: ["row-a", "row-b"],
        activeThreadId: "thread-opaque",
        draftBefore: "smoke-draft-sentinel",
        draftAfter: "smoke-draft-sentinel",
        switchTrees: {
          beforeDigest: `sha256:${"9".repeat(64)}`,
          afterDigest: `sha256:${"a".repeat(64)}`,
        },
        preserved: ["thread", "draft", "roster"],
      };
    case "field-notes-interrupt":
      return {
        receipt: {
          kind: "interrupt",
          status: "accepted",
          idDigest: `sha256:${"6".repeat(64)}`,
        },
        requestSequence: ["thread/interrupt"],
        notificationSequence: ["evener/thread/status"],
      };
    case "work-activity-usage":
      return {
        itemLifecycle: { started: true, delta: true, completed: true },
        reasoningSummary: {
          observed: true,
          semanticDigest: `sha256:${"7".repeat(64)}`,
        },
        toolDelta: {
          observed: true,
          semanticDigest: `sha256:${"8".repeat(64)}`,
        },
        tasks: 1,
        jobs: 1,
        delegates: 1,
        usage: { inputTokens: 10, outputTokens: 20, contextTokens: 30 },
      };
    case "background-foreground":
      return {
        backgroundGeneration: 4,
        foregroundGeneration: 5,
        activeThreadId: "thread-opaque",
        rehydrated: true,
        backgroundTreeDigest: `sha256:${"b".repeat(64)}`,
        foregroundTreeDigest: `sha256:${"c".repeat(64)}`,
      };
    case "reconnect":
      return {
        beforeGeneration: 5,
        afterGeneration: 6,
        activeThreadId: "thread-opaque",
        rehydrated: true,
        staleFramesAbsent: true,
        hubObserved: "abc123",
      };
    case "fixture-absence":
      return { fixtureAbsent: true, searchAbsent: true, labAbsent: true };
    default:
      throw new Error(`unknown test milestone ${milestone}`);
  }
}

function validObserved() {
  return REQUIRED_LIVE_MILESTONES.map((milestone, index) => ({
    milestone,
    concept: CONCEPTS[milestone],
    threadIdentity: THREAD_MILESTONES.has(milestone) ? "thread-opaque" : null,
    fixtureAbsent: true,
    source: semanticSource(index),
    action: {
      kind: index === 0 ? "observe" : "semantic",
      label: `action-${index}`,
    },
    positiveMarker: {
      kind: "semantic-tree",
      markerDigest: `sha256:${"c".repeat(64)}`,
    },
    evidence: baseEvidence(milestone),
  }));
}

function rejects(mutator, pattern) {
  const observed = structuredClone(validObserved());
  mutator(observed);
  assert.throws(() => assertCompleteLiveSmoke(observed), pattern);
}

test("exports the exact ordered milestone contract", () => {
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
  assert.doesNotThrow(() => assertCompleteLiveSmoke(validObserved()));
});

test("rejects missing, extra, duplicate, and out-of-order milestones", async (t) => {
  await t.test("missing", () =>
    rejects((rows) => rows.pop(), /missing.*fixture-absence/i),
  );
  await t.test("extra", () =>
    rejects(
      (rows) => rows.push({ ...rows[0], milestone: "extra" }),
      /extra|exactly 12/i,
    ),
  );
  await t.test("duplicate", () =>
    rejects((rows) => {
      rows[2].milestone = rows[1].milestone;
    }, /duplicate/i),
  );
  await t.test("out of order", () =>
    rejects((rows) => {
      [rows[4], rows[5]] = [rows[5], rows[4]];
    }, /out of order/i),
  );
});

test("rejects wrong concepts, threads, and fixture contamination", async (t) => {
  await t.test("wrong concept", () =>
    rejects((rows) => {
      rows[4].concept = "Stillwater";
    }, /wrong concept/i),
  );
  await t.test("empty thread", () =>
    rejects((rows) => {
      rows[7].threadIdentity = "";
    }, /thread identity/i),
  );
  await t.test("wrong thread", () =>
    rejects((rows) => {
      rows[9].threadIdentity = "other-thread";
    }, /wrong thread/i),
  );
  await t.test("fixture truth missing", () =>
    rejects((rows) => {
      delete rows[3].fixtureAbsent;
    }, /fixture.*true/i),
  );
  await t.test("fixture contaminated", () =>
    rejects((rows) => {
      rows[8].fixtureAbsent = false;
    }, /fixture.*true/i),
  );
});

test("rejects self-attested and malformed semantic sources", async (t) => {
  await t.test("passed true", () =>
    rejects((rows) => {
      rows[0].passed = true;
    }, /passed.*not evidence/i),
  );
  await t.test("nested passed true", () =>
    rejects((rows) => {
      rows[0].evidence.device.passed = true;
    }, /passed.*not evidence/i),
  );
  await t.test("non-IDB source", () =>
    rejects((rows) => {
      rows[2].source.tool = "caller";
    }, /source.*idb/i),
  );
  await t.test("nonzero command", () =>
    rejects((rows) => {
      rows[3].source.commandStatus = 1;
    }, /command status/i),
  );
  await t.test("missing tree digest", () =>
    rejects((rows) => {
      rows[4].source.semanticTreeDigest = "";
    }, /semantic tree digest/i),
  );
  await t.test("missing positive marker", () =>
    rejects((rows) => {
      rows[5].positiveMarker = null;
    }, /positive marker/i),
  );
});

test("rejects milestone-specific incomplete or contradictory evidence", async (t) => {
  await t.test("stale Hub", () =>
    rejects((rows) => {
      rows[0].evidence.hub.observed = "old";
    }, /stale Hub/i),
  );
  await t.test("unsigned bundle", () =>
    rejects((rows) => {
      rows[0].evidence.bundle.signed = false;
    }, /signed bundle/i),
  );
  await t.test("wrong list limit", () =>
    rejects((rows) => {
      rows[1].evidence.listLimit = 500;
    }, /limit.*501/i),
  );
  await t.test("501st mismatch", () =>
    rejects((rows) => {
      rows[1].evidence.hasMore = false;
    }, /501st|hasMore/i),
  );
  await t.test("wrong read limit", () =>
    rejects((rows) => {
      rows[2].evidence.readLimit = 0;
    }, /read limit/i),
  );
  await t.test("send receipt", () =>
    rejects((rows) => {
      rows[3].evidence.receipt.kind = "queue";
    }, /send receipt/i),
  );
  await t.test("draft changed", () =>
    rejects((rows) => {
      rows[4].evidence.draftAfter = "changed";
    }, /draft sentinel/i),
  );
  await t.test("missing queue", () =>
    rejects((rows) => {
      rows[5].evidence.receipts.pop();
    }, /steer.*queue/i),
  );
  await t.test("wrong preserved thread", () =>
    rejects((rows) => {
      rows[6].evidence.activeThreadId = "other";
    }, /active thread/i),
  );
  await t.test("interrupt receipt", () =>
    rejects((rows) => {
      rows[7].evidence.receipt.status = "pending";
    }, /interrupt receipt/i),
  );
  await t.test("lifecycle", () =>
    rejects((rows) => {
      rows[8].evidence.itemLifecycle.completed = false;
    }, /item lifecycle/i),
  );
  await t.test("reasoning", () =>
    rejects((rows) => {
      rows[8].evidence.reasoningSummary.observed = false;
    }, /reasoning summary/i),
  );
  await t.test("tool delta", () =>
    rejects((rows) => {
      rows[8].evidence.toolDelta.observed = false;
    }, /tool delta/i),
  );
  await t.test("tasks", () =>
    rejects((rows) => {
      rows[8].evidence.tasks = 0;
    }, /task/i),
  );
  await t.test("jobs", () =>
    rejects((rows) => {
      rows[8].evidence.jobs = 0;
    }, /job/i),
  );
  await t.test("delegates", () =>
    rejects((rows) => {
      rows[8].evidence.delegates = 0;
    }, /delegate/i),
  );
  await t.test("usage", () =>
    rejects((rows) => {
      rows[8].evidence.usage = null;
    }, /usage/i),
  );
  await t.test("foreground generation", () =>
    rejects((rows) => {
      rows[9].evidence.foregroundGeneration = 4;
    }, /foreground generation/i),
  );
  await t.test("background tree", () =>
    rejects((rows) => {
      delete rows[9].evidence.backgroundTreeDigest;
    }, /background semantic tree digest/i),
  );
  await t.test("reconnect generation", () =>
    rejects((rows) => {
      rows[10].evidence.afterGeneration = 5;
    }, /reconnect generation/i),
  );
  await t.test("Search present", () =>
    rejects((rows) => {
      rows[11].evidence.searchAbsent = false;
    }, /Search.*absent/i),
  );
  await t.test("Lab present", () =>
    rejects((rows) => {
      rows[11].evidence.labAbsent = false;
    }, /Lab.*absent/i),
  );
});

test("parseCli accepts only all four required flags", () => {
  assert.deepEqual(
    parseCli([
      "--udid",
      "phone-1",
      "--bundle-id",
      "com.primeradiant.evener",
      "--output-dir",
      "/private/tmp/live-smoke",
      "--hub-version",
      "abc123",
    ]),
    {
      udid: "phone-1",
      bundleId: "com.primeradiant.evener",
      outputDir: "/private/tmp/live-smoke",
      hubVersion: "abc123",
    },
  );
});

test("parseCli rejects missing, duplicate, unknown, relative, and empty flags", async (t) => {
  const valid = [
    "--udid",
    "phone-1",
    "--bundle-id",
    "com.example.app",
    "--output-dir",
    "/tmp/out",
    "--hub-version",
    "abc",
  ];
  await t.test("missing", () =>
    assert.throws(() => parseCli(valid.slice(0, -2)), /missing.*hub-version/i),
  );
  await t.test("duplicate", () =>
    assert.throws(
      () => parseCli([...valid, "--udid", "phone-2"]),
      /duplicate.*udid/i,
    ),
  );
  await t.test("unknown", () =>
    assert.throws(() => parseCli([...valid, "--wat", "x"]), /unknown flag/i),
  );
  await t.test("relative", () =>
    assert.throws(
      () => parseCli(valid.map((v) => (v === "/tmp/out" ? "relative" : v))),
      /absolute/i,
    ),
  );
  await t.test("empty", () =>
    assert.throws(
      () => parseCli(valid.map((v) => (v === "phone-1" ? "" : v))),
      /nonempty/i,
    ),
  );
  await t.test("bare value", () =>
    assert.throws(() => parseCli([...valid, "stray"]), /unexpected argument/i),
  );
});

test("pollSemanticTree polls back-to-back and stops on the positive marker", async () => {
  let monotonic = 0;
  const calls = [];
  const frames = [{ state: "loading" }, { state: "ready", marker: "ok" }];
  const result = await pollSemanticTree({
    readTree: async () => {
      calls.push(monotonic);
      monotonic += 1;
      return frames.shift();
    },
    accept: (tree) => tree.marker === "ok",
    now: () => monotonic,
  });
  assert.equal(result.marker, "ok");
  assert.deepEqual(calls, [0, 1]);
});

test("pollSemanticTree trips at ten monotonic seconds without sleeping", async () => {
  let monotonic = 0;
  let calls = 0;
  await assert.rejects(
    pollSemanticTree({
      readTree: async () => {
        calls += 1;
        monotonic += 2_500;
        return { state: "loading" };
      },
      accept: () => false,
      now: () => monotonic,
    }),
    /10-second semantic tripwire/i,
  );
  assert.equal(calls, 4);
});

test("pollSemanticTree fails closed on malformed and nonzero semantic output", async (t) => {
  await t.test("malformed", async () => {
    await assert.rejects(
      pollSemanticTree({
        readTree: async () => null,
        accept: () => false,
        now: () => 0,
      }),
      /malformed semantic tree/i,
    );
  });
  await t.test("nonzero", async () => {
    await assert.rejects(
      pollSemanticTree({
        readTree: async () => {
          throw new Error("idb exited 1");
        },
        accept: () => false,
        now: () => 0,
      }),
      /idb exited 1/i,
    );
  });
});

test("launch fallback allows devicectl only for install/launch after IDB loss", async () => {
  const calls = [];
  const run = async (program, argv) => {
    calls.push([program, ...argv]);
    if (program === "idb" && argv[0] === "launch") {
      const error = new Error("companion connection lost");
      error.code = "IDB_COMPANION_LOST";
      throw error;
    }
    return { status: 0, stdout: "", stderr: "", version: "test" };
  };
  const result = await launchProductionBundle(
    { udid: "u", bundleId: "b" },
    { run },
  );
  assert.equal(result.tool, "devicectl");
  assert.deepEqual(calls, [
    ["idb", "launch", "--udid", "u", "b"],
    ["xcrun", "devicectl", "device", "process", "launch", "--device", "u", "b"],
  ]);
  assert.equal(calls.flat().includes("tap"), false);
});

test("launch fallback rejects generic IDB failure and never uses devicectl for semantics", async (t) => {
  await t.test("generic launch failure", async () => {
    const calls = [];
    await assert.rejects(
      launchProductionBundle(
        { udid: "u", bundleId: "b" },
        {
          run: async (program, argv) => {
            calls.push([program, ...argv]);
            throw new Error("signed bundle rejected");
          },
        },
      ),
      /signed bundle rejected/i,
    );
    assert.equal(calls.length, 1);
  });
  await t.test("semantic automation unavailable", async () => {
    await assert.rejects(
      pollSemanticTree({
        readTree: async () => {
          throw new Error(
            "IDB semantic automation unavailable; devicectl cannot substitute",
          );
        },
        accept: () => false,
        now: () => 0,
      }),
      /devicectl cannot substitute/i,
    );
  });
});

test("redaction removes raw origin, thread, and token-like values and keeps opaque digests", () => {
  const secret = {
    origin: "https://hub.internal:7443",
    threadRef: "raw-thread-ref",
    authToken: "super-secret-token",
    nested: {
      profileOrigin: "wss://secret",
      access_token: "token-two",
      safe: "kept",
    },
  };
  const redacted = redactEvidence(secret);
  const encoded = JSON.stringify(redacted);
  for (const raw of [
    secret.origin,
    secret.threadRef,
    secret.authToken,
    secret.nested.profileOrigin,
    secret.nested.access_token,
  ]) {
    assert.equal(encoded.includes(raw), false);
  }
  assert.match(encoded, /sha256:[a-f0-9]{64}/);
  assert.equal(redacted.nested.safe, "kept");
});

test("writeEvidence creates 0700 directories and atomic 0600 JSON without secret filenames", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "evener-live-smoke-test-"));
  const output = path.join(root, "evidence");
  const raw = {
    origin: "https://secret",
    threadRef: "thread-secret",
    authToken: "token-secret",
  };
  const summary = {
    schemaVersion: 1,
    status: "passed",
    linkDigest: `sha256:${"f".repeat(64)}`,
  };
  const paths = await writeEvidence(output, summary, raw);
  assert.equal((await stat(output)).mode & 0o777, 0o700);
  assert.equal((await stat(paths.scratchDir)).mode & 0o777, 0o700);
  assert.equal((await stat(paths.summaryPath)).mode & 0o777, 0o600);
  assert.equal((await stat(paths.rawPath)).mode & 0o777, 0o600);
  assert.deepEqual(
    JSON.parse(await readFile(paths.summaryPath, "utf8")),
    summary,
  );
  assert.deepEqual(JSON.parse(await readFile(paths.rawPath, "utf8")), raw);
  assert.equal(paths.summaryPath.includes("secret"), false);
  assert.equal(paths.rawPath.includes("secret"), false);
});

test("writeEvidence refuses symlink output clobber", async () => {
  const root = await mkdtemp(
    path.join(os.tmpdir(), "evener-live-smoke-link-test-"),
  );
  const target = path.join(root, "target");
  const output = path.join(root, "evidence");
  await mkdir(target, { mode: 0o700 });
  await symlink(target, output);
  await assert.rejects(
    writeEvidence(output, { status: "passed" }, { threadRef: "raw" }),
    /symbolic link/i,
  );
  assert.equal((await lstat(output)).isSymbolicLink(), true);
});

class FakeStream extends EventEmitter {
  setEncoding() {}
}

class FakeChild extends EventEmitter {
  constructor() {
    super();
    this.stdout = new FakeStream();
    this.stderr = new FakeStream();
    this.killedSignals = [];
  }
  kill(signal) {
    this.killedSignals.push(signal);
    this.emit("close", null, signal);
    return true;
  }
}

test("runSpawned uses argv arrays and captures structured completion", async () => {
  const child = new FakeChild();
  let spawnArgs;
  const promise = runSpawned(
    "idb",
    ["ui", "describe-all", "--udid", "u; rm -rf nope"],
    {
      spawnImpl(program, argv, options) {
        spawnArgs = { program, argv, options };
        return child;
      },
    },
  );
  child.stdout.emit("data", '{"ok":true}');
  child.emit("close", 0, null);
  assert.deepEqual(await promise, {
    status: 0,
    stdout: '{"ok":true}',
    stderr: "",
  });
  assert.equal(spawnArgs.program, "idb");
  assert.deepEqual(spawnArgs.argv, [
    "ui",
    "describe-all",
    "--udid",
    "u; rm -rf nope",
  ]);
  assert.equal(spawnArgs.options.shell, false);
});

test("runSpawned classifies companion loss without exposing command stderr", async () => {
  const child = new FakeChild();
  const promise = runSpawned("idb", ["launch", "bundle"], {
    spawnImpl() {
      return child;
    },
  });
  child.stderr.emit(
    "data",
    "failed to connect to companion; auth-token-must-not-leak",
  );
  child.emit("close", 1, null);
  await assert.rejects(promise, (error) => {
    assert.equal(error.code, "IDB_COMPANION_LOST");
    assert.equal(error.message.includes("auth-token-must-not-leak"), false);
    return true;
  });
});

test("ProcessRegistry cleans every live child on failure paths", async () => {
  const registry = new ProcessRegistry();
  const first = new FakeChild();
  const second = new FakeChild();
  registry.add(first);
  registry.add(second);
  registry.cleanup();
  assert.deepEqual(first.killedSignals, ["SIGTERM"]);
  assert.deepEqual(second.killedSignals, ["SIGTERM"]);
  assert.equal(registry.size, 0);
});

test("runLiveSmoke completes from fake command and monotonic boundaries only", async () => {
  const root = await mkdtemp(
    path.join(os.tmpdir(), "evener-live-smoke-run-test-"),
  );
  let phase = "profile";
  let concept = "Stillwater";
  let mode = "send";
  let sentinel = "";
  let receipt = "";
  let generation = 1;
  let terminated = false;
  let monotonic = 0;
  const calls = [];

  function tree() {
    const common = {
      label: `${concept} Connected abc123 appwire/1 https://hub.example.invalid ${sentinel}`,
      "data-session-id": "row-a",
      "data-roster-list-limit": "501",
      "data-roster-retained-count": "500",
      "data-roster-row-501-present": "true",
      "data-roster-has-more": "true",
      "data-observed-notification": "evener/thread/item-completed",
    };
    if (phase === "profile") return common;
    if (phase === "background") {
      return { label: "SpringBoard", generation: String(generation) };
    }
    const conversation = {
      ...common,
      "data-thread-key": "thread-opaque",
      "data-thread-read-limit": "200",
      "data-transcript-item-id": "item-1",
      "data-composer-receipt": receipt,
      "data-connection-generation": String(generation),
      "data-stale-frames-absent": "true",
      transcript: "Transcript",
    };
    if (phase !== "work") return conversation;
    return {
      ...conversation,
      label: `${conversation.label} Task summary Usage`,
      "data-work-node-id": ["task-1", "job-1", "delegate-1"],
      "data-work-kind": ["task", "job", "delegate"],
      "data-item-lifecycle": "started,delta,completed",
      "data-reasoning-summary": "reasoned",
      "data-tool-delta": "tool delta",
      "data-usage-input": "10",
      "data-usage-output": "20",
      "data-usage-context": "30",
    };
  }

  const run = async (program, argv) => {
    calls.push([program, ...argv]);
    if (argv.includes("--version")) {
      return { status: 0, stdout: `${program}-test-version\n`, stderr: "" };
    }
    if (program === "idb" && argv[0] === "describe") {
      return {
        status: 0,
        stdout: JSON.stringify({
          modelName: "iPhone 17 Pro",
          osVersion: "iOS 20.0",
        }),
        stderr: "",
      };
    }
    if (program === "idb" && argv[0] === "list-apps") {
      return {
        status: 0,
        stdout: JSON.stringify([
          {
            bundleId: "com.primeradiant.evener",
            version: "1.2.3",
            sha256: "a".repeat(64),
            signed: true,
          },
        ]),
        stderr: "",
      };
    }
    if (program === "idb" && argv[0] === "launch") {
      if (argv.includes("--foreground-if-running")) {
        generation += 1;
        phase = "work";
      } else if (terminated) {
        generation += 1;
        terminated = false;
      }
      return { status: 0, stdout: "", stderr: "" };
    }
    if (program === "idb" && argv[0] === "terminate") {
      terminated = true;
      return { status: 0, stdout: "", stderr: "" };
    }
    if (program === "idb" && argv[0] === "ui" && argv[1] === "button") {
      assert.equal(argv[2], "HOME");
      phase = "background";
      return { status: 0, stdout: "", stderr: "" };
    }
    if (program === "idb" && argv[0] === "ui" && argv[1] === "describe-all") {
      monotonic += 1;
      return { status: 0, stdout: JSON.stringify(tree()), stderr: "" };
    }
    if (program === "idb" && argv[0] === "ui" && argv[1] === "tap") {
      const target = argv[2];
      if (target === "row-a") phase = "conversation";
      if (
        target === "Stillwater" ||
        target === "Constellation" ||
        target === "Field Notes"
      )
        concept = target;
      if (target === "Send" || target === "Steer" || target === "Queue")
        mode = target.toLowerCase();
      if (target === "Submit" || target === "Submit message") {
        receipt = `${mode}:accepted`;
        sentinel = "";
      }
      if (target === "Interrupt") receipt = "interrupt:accepted";
      if (target === "Work") phase = "work";
      return { status: 0, stdout: "", stderr: "" };
    }
    if (program === "idb" && argv[0] === "ui" && argv[1] === "text") {
      sentinel = argv[2];
      return { status: 0, stdout: "", stderr: "" };
    }
    throw new Error(`unexpected fake command ${program} ${argv.join(" ")}`);
  };

  const result = await runLiveSmoke(
    {
      udid: "phone-1",
      bundleId: "com.primeradiant.evener",
      outputDir: path.join(root, "evidence"),
      hubVersion: "abc123",
    },
    { run, now: () => monotonic },
  );

  assert.equal(result.summary.status, "passed");
  assert.equal(
    JSON.parse(await readFile(result.paths.summaryPath, "utf8")).status,
    "passed",
  );
  assert.equal(
    calls.some((call) => call.includes("restart")),
    false,
  );
  assert.equal(
    calls.some((call) => call.includes("x") || call.includes("y")),
    false,
  );
  assert.equal(
    calls.some(
      (call) =>
        call[0] === "idb" &&
        call[1] === "ui" &&
        call[2] === "button" &&
        call[3] === "HOME",
    ),
    true,
  );
  assert.equal(
    calls.filter((call) => call[0] === "idb" && call[1] === "terminate").length,
    1,
  );
  assert.equal(
    calls.filter(
      (call) =>
        call[0] === "idb" && call[1] === "ui" && call[2] === "describe-all",
    ).length >= 12,
    true,
  );
});

test("runLiveSmoke records a stale Hub as blocked and never restarts it", async () => {
  const root = await mkdtemp(
    path.join(os.tmpdir(), "evener-live-smoke-stale-test-"),
  );
  const calls = [];
  const run = async (program, argv) => {
    calls.push([program, ...argv]);
    if (argv.includes("--version")) {
      return { status: 0, stdout: "test-version\n", stderr: "" };
    }
    if (program === "idb" && argv[0] === "describe") {
      return {
        status: 0,
        stdout: JSON.stringify({ model: "iPhone", osVersion: "iOS 20" }),
        stderr: "",
      };
    }
    if (program === "idb" && argv[0] === "list-apps") {
      return {
        status: 0,
        stdout: JSON.stringify([
          {
            bundleId: "com.primeradiant.evener",
            version: "1",
            sha256: "a".repeat(64),
            signed: true,
          },
        ]),
        stderr: "",
      };
    }
    if (program === "idb" && argv[0] === "launch") {
      return { status: 0, stdout: "", stderr: "" };
    }
    if (program === "idb" && argv[0] === "ui") {
      return {
        status: 0,
        stdout: JSON.stringify({
          label: "Connected old-version appwire/1 https://private.invalid",
          "data-hub-version": "old-version",
        }),
        stderr: "",
      };
    }
    throw new Error("unexpected fake command");
  };
  const outputDir = path.join(root, "evidence");

  await assert.rejects(
    runLiveSmoke(
      {
        udid: "phone-1",
        bundleId: "com.primeradiant.evener",
        outputDir,
        hubVersion: "expected-version",
      },
      { run, now: () => 1 },
    ),
    /stale Hub.*blocked prerequisite/i,
  );

  const summary = JSON.parse(
    await readFile(path.join(outputDir, "live-smoke-summary.json"), "utf8"),
  );
  assert.equal(summary.status, "blocked");
  assert.equal(JSON.stringify(summary).includes("private.invalid"), false);
  assert.equal(
    calls.some((call) => call.includes("restart")),
    false,
  );
});
