import { spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { chmod, lstat, mkdir, open, rename, unlink } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

export const REQUIRED_LIVE_MILESTONES = Object.freeze([
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

const EXPECTED_CONCEPT = Object.freeze({
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
});

const THREAD_MILESTONES = new Set(REQUIRED_LIVE_MILESTONES.slice(2, 11));
const SENSITIVE_KEY =
  /(?:origin|thread(?:ref|identity)?|authorization|authtoken|accesstoken|token)/;
const DIGEST = /^sha256:[a-f0-9]{64}$/;
const FIXTURE_TERMS = [
  "fixture",
  "scenario",
  "synthetic completion",
  "prototype",
];
const TRIPWIRE_MS = 10_000;

function object(value, label) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  return value;
}

function nonempty(value, label) {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${label} must be nonempty`);
  }
  return value;
}

function exactDigest(value, label) {
  if (typeof value !== "string" || !DIGEST.test(value)) {
    throw new Error(`${label} must be a sha256 digest`);
  }
}

function positiveInteger(value, label, allowZero = false) {
  if (!Number.isInteger(value) || value < (allowZero ? 0 : 1)) {
    throw new Error(
      `${label} must be ${allowZero ? "a nonnegative" : "a positive"} integer`,
    );
  }
}

function requireSequence(value, expected, label) {
  if (!Array.isArray(value) || value.length !== expected.length) {
    throw new Error(`${label} must record ${expected.join(" then ")}`);
  }
  for (let index = 0; index < expected.length; index += 1) {
    if (value[index] !== expected[index]) {
      throw new Error(`${label} must record ${expected.join(" then ")}`);
    }
  }
}

function requireRosterIds(value, label) {
  if (
    !Array.isArray(value) ||
    value.length === 0 ||
    value.some((entry) => typeof entry !== "string" || entry.trim() === "") ||
    new Set(value).size !== value.length
  ) {
    throw new Error(`${label} must contain unique nonempty roster IDs`);
  }
}

function requireReceipt(value, kind, label) {
  const receipt = object(value, label);
  if (receipt.kind !== kind || receipt.status !== "accepted") {
    throw new Error(
      `${kind} receipt must be independently observed as accepted`,
    );
  }
  exactDigest(receipt.idDigest, `${kind} receipt digest`);
}

function requireDraftPreserved(evidence) {
  nonempty(evidence.draftBefore, "draft sentinel before switch");
  if (evidence.draftAfter !== evidence.draftBefore) {
    throw new Error(
      "draft sentinel must be identical before and after the switch",
    );
  }
}

function validateMilestoneEvidence(observation, shared) {
  const evidence = object(
    observation.evidence,
    `${observation.milestone} evidence`,
  );
  switch (observation.milestone) {
    case "profile-connected": {
      const device = object(evidence.device, "device evidence");
      nonempty(device.model, "device model");
      nonempty(device.os, "device OS");
      const bundle = object(evidence.bundle, "bundle evidence");
      nonempty(bundle.id, "signed bundle ID");
      nonempty(bundle.version, "signed bundle version");
      exactDigest(bundle.hash, "signed bundle hash");
      if (bundle.signed !== true)
        throw new Error("signed bundle proof is required");
      const hub = object(evidence.hub, "Hub evidence");
      nonempty(hub.expected, "expected Hub version");
      nonempty(hub.observed, "observed Hub version");
      if (hub.expected !== hub.observed) {
        throw new Error(
          "stale Hub is a blocked prerequisite; the smoke runner never restarts it",
        );
      }
      nonempty(hub.protocol, "Hub protocol");
      exactDigest(hub.originDigest, "redacted Hub origin digest");
      if (evidence.profileStatus !== "connected") {
        throw new Error(
          "profile-connected requires an observed connected profile",
        );
      }
      shared.hubVersion = hub.observed;
      break;
    }
    case "stillwater-roster":
      if (evidence.listLimit !== 501)
        throw new Error("roster list limit must be exactly 501");
      positiveInteger(evidence.retainedCount, "retained roster count", true);
      if (evidence.retainedCount > 500)
        throw new Error("retained roster count must not exceed 500");
      if (
        typeof evidence.row501Present !== "boolean" ||
        typeof evidence.hasMore !== "boolean"
      ) {
        throw new Error(
          "501st-row completeness requires boolean row501Present and hasMore",
        );
      }
      if (evidence.row501Present !== evidence.hasMore) {
        throw new Error("501st-row completeness and hasMore disagree");
      }
      requireRosterIds(evidence.rosterIds, "Stillwater roster IDs");
      requireSequence(
        evidence.requestSequence,
        ["thread/list(limit=501)"],
        "roster request sequence",
      );
      if (!Array.isArray(evidence.notificationSequence)) {
        throw new Error("roster notification sequence is required");
      }
      shared.rosterIds = evidence.rosterIds;
      break;
    case "stillwater-conversation":
      positiveInteger(evidence.readLimit, "bounded read limit");
      if (evidence.readLimit > 500)
        throw new Error("bounded read limit must not exceed 500");
      if (evidence.activeThreadId !== observation.threadIdentity) {
        throw new Error(
          "active thread ID must match the observed thread identity",
        );
      }
      requireRosterIds(evidence.rosterIds, "conversation roster IDs");
      if (
        JSON.stringify(evidence.rosterIds) !== JSON.stringify(shared.rosterIds)
      ) {
        throw new Error(
          "conversation roster IDs must match Stillwater roster IDs",
        );
      }
      requireSequence(
        evidence.requestSequence,
        [`thread/read(subscribe=true,limit=${evidence.readLimit})`],
        "conversation request sequence",
      );
      if (!Array.isArray(evidence.notificationSequence)) {
        throw new Error("conversation notification sequence is required");
      }
      positiveInteger(evidence.transcriptItems, "transcript item count");
      break;
    case "stillwater-send":
      requireReceipt(evidence.receipt, "send", "send receipt");
      requireSequence(
        evidence.requestSequence,
        ["thread/send"],
        "send request sequence",
      );
      if (
        !Array.isArray(evidence.notificationSequence) ||
        evidence.notificationSequence.length === 0
      ) {
        throw new Error("send notification sequence is required");
      }
      break;
    case "constellation-preserved":
    case "field-notes-preserved": {
      requireRosterIds(evidence.rosterIds, `${observation.concept} roster IDs`);
      if (
        JSON.stringify(evidence.rosterIds) !== JSON.stringify(shared.rosterIds)
      ) {
        throw new Error(
          `${observation.concept} roster IDs must match Stillwater roster IDs`,
        );
      }
      if (evidence.activeThreadId !== observation.threadIdentity) {
        throw new Error(
          "preserved active thread ID must match the observed thread identity",
        );
      }
      requireDraftPreserved(evidence);
      const switchTrees = object(
        evidence.switchTrees,
        "concept switch semantic trees",
      );
      exactDigest(switchTrees.beforeDigest, "pre-switch semantic tree digest");
      exactDigest(switchTrees.afterDigest, "post-switch semantic tree digest");
      if (switchTrees.beforeDigest === switchTrees.afterDigest) {
        throw new Error(
          "concept switch must have distinct before and after semantic trees",
        );
      }
      for (const item of ["thread", "draft", "roster"]) {
        if (
          !Array.isArray(evidence.preserved) ||
          !evidence.preserved.includes(item)
        ) {
          throw new Error(`${observation.concept} must preserve ${item}`);
        }
      }
      break;
    }
    case "constellation-steer-queue": {
      requireDraftPreserved(evidence);
      if (!Array.isArray(evidence.receipts) || evidence.receipts.length !== 2) {
        throw new Error("steer and queue receipts are both required");
      }
      requireReceipt(evidence.receipts[0], "steer", "steer receipt");
      requireReceipt(evidence.receipts[1], "queue", "queue receipt");
      requireSequence(
        evidence.requestSequence,
        ["thread/steer", "thread/queue"],
        "steer/queue request sequence",
      );
      if (
        !Array.isArray(evidence.notificationSequence) ||
        evidence.notificationSequence.length === 0
      ) {
        throw new Error("steer/queue notification sequence is required");
      }
      break;
    }
    case "field-notes-interrupt":
      requireReceipt(evidence.receipt, "interrupt", "interrupt receipt");
      requireSequence(
        evidence.requestSequence,
        ["thread/interrupt"],
        "interrupt request sequence",
      );
      if (
        !Array.isArray(evidence.notificationSequence) ||
        evidence.notificationSequence.length === 0
      ) {
        throw new Error("interrupt notification sequence is required");
      }
      break;
    case "work-activity-usage": {
      const lifecycle = object(
        evidence.itemLifecycle,
        "item lifecycle evidence",
      );
      if (
        lifecycle.started !== true ||
        lifecycle.delta !== true ||
        lifecycle.completed !== true
      ) {
        throw new Error(
          "item lifecycle must show started, delta, and completed semantic states",
        );
      }
      const reasoning = object(
        evidence.reasoningSummary,
        "reasoning summary evidence",
      );
      if (reasoning.observed !== true)
        throw new Error("reasoning summary must be semantically observed");
      exactDigest(reasoning.semanticDigest, "reasoning summary digest");
      const tool = object(evidence.toolDelta, "tool delta evidence");
      if (tool.observed !== true)
        throw new Error("tool delta must be semantically observed");
      exactDigest(tool.semanticDigest, "tool delta digest");
      positiveInteger(evidence.tasks, "task count");
      positiveInteger(evidence.jobs, "job count");
      positiveInteger(evidence.delegates, "delegate count");
      const usage = object(evidence.usage, "usage evidence");
      if (usage.present === true) {
        exactDigest(usage.semanticDigest, "usage semantic digest");
      } else {
        for (const key of ["inputTokens", "outputTokens", "contextTokens"]) {
          positiveInteger(usage[key], `usage ${key}`, true);
        }
      }
      break;
    }
    case "background-foreground":
      positiveInteger(
        evidence.backgroundGeneration,
        "background generation",
        true,
      );
      positiveInteger(
        evidence.foregroundGeneration,
        "foreground generation",
        true,
      );
      if (evidence.foregroundGeneration <= evidence.backgroundGeneration) {
        throw new Error(
          "foreground generation must advance after backgrounding",
        );
      }
      if (
        evidence.activeThreadId !== observation.threadIdentity ||
        evidence.rehydrated !== true
      ) {
        throw new Error(
          "background/foreground must rehydrate the same active thread",
        );
      }
      break;
    case "reconnect":
      positiveInteger(
        evidence.beforeGeneration,
        "pre-reconnect generation",
        true,
      );
      positiveInteger(
        evidence.afterGeneration,
        "post-reconnect generation",
        true,
      );
      if (evidence.afterGeneration <= evidence.beforeGeneration) {
        throw new Error("reconnect generation must advance");
      }
      if (
        evidence.activeThreadId !== observation.threadIdentity ||
        evidence.rehydrated !== true
      ) {
        throw new Error("reconnect must rehydrate the same active thread");
      }
      if (evidence.staleFramesAbsent !== true)
        throw new Error("reconnect must prove stale frames absent");
      if (evidence.hubObserved !== shared.hubVersion)
        throw new Error("reconnect observed a stale Hub version");
      break;
    case "fixture-absence":
      if (evidence.fixtureAbsent !== true)
        throw new Error("fixture content must be absent");
      if (evidence.searchAbsent !== true)
        throw new Error("Search must be absent");
      if (evidence.labAbsent !== true)
        throw new Error("Lab Controls must be absent");
      break;
    default:
      throw new Error(`unsupported milestone ${observation.milestone}`);
  }
}

function containsPassedAssertion(value) {
  if (Array.isArray(value)) return value.some(containsPassedAssertion);
  if (value === null || typeof value !== "object") return false;
  return Object.entries(value).some(
    ([key, entry]) =>
      (key.toLowerCase() === "passed" && entry === true) ||
      containsPassedAssertion(entry),
  );
}

export function assertCompleteLiveSmoke(observed) {
  if (!Array.isArray(observed))
    throw new Error("observations must be an array");
  const names = observed.map((entry) => entry?.milestone);
  const seen = new Set();
  for (const name of names) {
    if (seen.has(name)) throw new Error(`duplicate milestone: ${String(name)}`);
    seen.add(name);
  }
  const missing = REQUIRED_LIVE_MILESTONES.filter((name) => !seen.has(name));
  if (missing.length > 0)
    throw new Error(`missing milestone(s): ${missing.join(", ")}`);
  const extra = names.filter(
    (name) => !REQUIRED_LIVE_MILESTONES.includes(name),
  );
  if (extra.length > 0 || observed.length !== REQUIRED_LIVE_MILESTONES.length) {
    throw new Error(
      `extra milestone(s) or not exactly 12 observations: ${extra.join(", ")}`,
    );
  }
  for (let index = 0; index < REQUIRED_LIVE_MILESTONES.length; index += 1) {
    if (names[index] !== REQUIRED_LIVE_MILESTONES[index]) {
      throw new Error(`milestones are out of order at index ${index}`);
    }
  }

  let threadIdentity = null;
  const shared = { hubVersion: null, rosterIds: null };
  for (const observation of observed) {
    object(observation, `${observation?.milestone ?? "unknown"} observation`);
    if (containsPassedAssertion(observation)) {
      throw new Error(
        "passed:true is not evidence; semantic tree and command evidence are required",
      );
    }
    if (observation.concept !== EXPECTED_CONCEPT[observation.milestone]) {
      throw new Error(
        `${observation.milestone} has wrong concept; expected ${EXPECTED_CONCEPT[observation.milestone]}`,
      );
    }
    if (observation.fixtureAbsent !== true) {
      throw new Error(`${observation.milestone} fixtureAbsent must be true`);
    }
    if (THREAD_MILESTONES.has(observation.milestone)) {
      nonempty(
        observation.threadIdentity,
        `${observation.milestone} thread identity`,
      );
      if (threadIdentity === null) threadIdentity = observation.threadIdentity;
      if (observation.threadIdentity !== threadIdentity) {
        throw new Error(`${observation.milestone} has wrong thread identity`);
      }
    }
    const source = object(
      observation.source,
      `${observation.milestone} source`,
    );
    if (source.tool !== "idb")
      throw new Error("observation source must be IDB semantic automation");
    if (source.commandStatus !== 0)
      throw new Error("semantic command status must be zero");
    exactDigest(source.commandDigest, "semantic command digest");
    exactDigest(source.semanticTreeDigest, "semantic tree digest");
    if (
      !Number.isFinite(source.observedAtMonotonicMs) ||
      source.observedAtMonotonicMs < 0
    ) {
      throw new Error("semantic observation must carry monotonic time");
    }
    object(observation.action, `${observation.milestone} semantic action`);
    object(
      observation.positiveMarker,
      `${observation.milestone} positive marker`,
    );
    exactDigest(
      observation.positiveMarker.markerDigest,
      "positive marker digest",
    );
    validateMilestoneEvidence(observation, shared);
  }
  return true;
}

export function parseCli(argv) {
  const names = new Map([
    ["--udid", "udid"],
    ["--bundle-id", "bundleId"],
    ["--output-dir", "outputDir"],
    ["--hub-version", "hubVersion"],
  ]);
  const result = {};
  for (let index = 0; index < argv.length; index += 2) {
    const flag = argv[index];
    if (typeof flag !== "string" || !flag.startsWith("--")) {
      throw new Error("unexpected argument; flags and values must be paired");
    }
    const key = names.get(flag);
    if (key === undefined) throw new Error(`unknown flag ${flag}`);
    if (Object.hasOwn(result, key)) throw new Error(`duplicate ${flag}`);
    if (index + 1 >= argv.length)
      throw new Error(`${flag} requires a nonempty value`);
    const value = argv[index + 1];
    if (typeof value !== "string" || value.trim() === "") {
      throw new Error(`${flag} requires a nonempty value`);
    }
    result[key] = value;
  }
  for (const [flag, key] of names) {
    if (!Object.hasOwn(result, key))
      throw new Error(`missing required ${flag}`);
  }
  if (!path.isAbsolute(result.outputDir))
    throw new Error("--output-dir must be absolute");
  return result;
}

export async function pollSemanticTree({ readTree, accept, now }) {
  if (
    typeof readTree !== "function" ||
    typeof accept !== "function" ||
    typeof now !== "function"
  ) {
    throw new Error(
      "semantic polling requires readTree, accept, and monotonic now functions",
    );
  }
  const started = now();
  for (;;) {
    const tree = await readTree();
    if (tree === null || typeof tree !== "object" || Array.isArray(tree)) {
      throw new Error("malformed semantic tree output");
    }
    if (accept(tree)) return tree;
    if (now() - started >= TRIPWIRE_MS) {
      throw new Error(
        "10-second semantic tripwire reached without a positive marker",
      );
    }
  }
}

export class ProcessRegistry {
  #children = new Set();
  get size() {
    return this.#children.size;
  }
  add(child) {
    this.#children.add(child);
  }
  delete(child) {
    this.#children.delete(child);
  }
  cleanup() {
    for (const child of this.#children) {
      try {
        child.kill("SIGTERM");
      } catch {
        /* retain the original failure */
      }
    }
    this.#children.clear();
  }
}

export function runSpawned(program, argv, options = {}) {
  if (
    typeof program !== "string" ||
    !Array.isArray(argv) ||
    argv.some((value) => typeof value !== "string")
  ) {
    throw new Error(
      "process execution requires a program and argv string array",
    );
  }
  const spawnImpl = options.spawnImpl ?? spawn;
  const registry = options.registry ?? new ProcessRegistry();
  return new Promise((resolve, reject) => {
    let settled = false;
    let child;
    try {
      child = spawnImpl(program, argv, {
        shell: false,
        stdio: ["ignore", "pipe", "pipe"],
        windowsHide: true,
      });
    } catch {
      reject(new Error(`${program} could not be started`));
      return;
    }
    registry.add(child);
    let stdout = "";
    let stderr = "";
    child.stdout?.setEncoding("utf8");
    child.stderr?.setEncoding("utf8");
    child.stdout?.on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr?.on("data", (chunk) => {
      stderr += chunk;
    });
    child.once("error", () => {
      if (settled) return;
      settled = true;
      registry.delete(child);
      reject(new Error(`${program} failed to execute`));
    });
    child.once("close", (code, signal) => {
      if (settled) return;
      settled = true;
      registry.delete(child);
      if (code !== 0) {
        const error = new Error(
          `${program} exited nonzero (${code ?? signal ?? "unknown"})`,
        );
        error.code =
          program === "idb" &&
          /(?:companion.{0,80}(?:lost|disconnect|connect|unavailable)|(?:lost|disconnect|connect|unavailable).{0,80}companion)/is.test(
            stderr,
          )
            ? "IDB_COMPANION_LOST"
            : "COMMAND_NONZERO";
        error.status = code;
        reject(error);
        return;
      }
      resolve({ status: 0, stdout, stderr });
    });
  });
}

function isIdbCompanionLoss(error) {
  return (
    error?.code === "IDB_COMPANION_LOST" ||
    /companion (?:connection )?(?:unavailable|lost|disconnected)/i.test(
      error?.message ?? "",
    )
  );
}

export async function launchProductionBundle(
  { udid, bundleId },
  { run = runSpawned } = {},
) {
  try {
    await run("idb", ["launch", "--udid", udid, bundleId]);
    return { tool: "idb", status: 0 };
  } catch (error) {
    if (!isIdbCompanionLoss(error)) throw error;
    await run("xcrun", [
      "devicectl",
      "device",
      "process",
      "launch",
      "--device",
      udid,
      bundleId,
    ]);
    return { tool: "devicectl", status: 0, reason: "idb-companion-lost" };
  }
}

function digest(value) {
  return `sha256:${createHash("sha256").update(String(value)).digest("hex")}`;
}

export function redactEvidence(value, key = "") {
  const normalizedKey = key.replace(/[^a-z0-9]/gi, "").toLowerCase();
  if (SENSITIVE_KEY.test(normalizedKey))
    return { redacted: true, digest: digest(JSON.stringify(value)) };
  if (Array.isArray(value)) return value.map((entry) => redactEvidence(entry));
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value).map(([childKey, entry]) => [
        childKey,
        redactEvidence(entry, childKey),
      ]),
    );
  }
  return value;
}

async function assertNoSymlinkPath(target) {
  const absolute = path.resolve(target);
  const parsed = path.parse(absolute);
  const components = absolute
    .slice(parsed.root.length)
    .split(path.sep)
    .filter(Boolean);
  let current = parsed.root;
  for (const component of components) {
    current = path.join(current, component);
    try {
      const info = await lstat(current);
      if (info.isSymbolicLink())
        throw new Error("evidence path contains a symbolic link");
    } catch (error) {
      if (error?.code === "ENOENT") return;
      throw error;
    }
  }
}

async function atomicJson(filePath, value) {
  try {
    await lstat(filePath);
    throw new Error("refusing to clobber an existing evidence file");
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
  }
  const temporary = path.join(
    path.dirname(filePath),
    `.new-${randomBytes(16).toString("hex")}`,
  );
  const handle = await open(temporary, "wx", 0o600);
  try {
    await handle.writeFile(`${JSON.stringify(value, null, 2)}\n`, "utf8");
    await handle.sync();
  } finally {
    await handle.close();
  }
  await chmod(temporary, 0o600);
  try {
    await rename(temporary, filePath);
  } catch (error) {
    await unlink(temporary).catch(() => {});
    throw error;
  }
}

export async function writeEvidence(outputDir, summary, raw) {
  if (!path.isAbsolute(outputDir))
    throw new Error("evidence output directory must be absolute");
  await assertNoSymlinkPath(outputDir);
  await mkdir(outputDir, { recursive: true, mode: 0o700 });
  await chmod(outputDir, 0o700);
  const scratchDir = path.join(outputDir, "sensitive-scratch");
  await assertNoSymlinkPath(scratchDir);
  await mkdir(scratchDir, { mode: 0o700 });
  await chmod(scratchDir, 0o700);
  const summaryPath = path.join(outputDir, "live-smoke-summary.json");
  const rawPath = path.join(scratchDir, "raw-evidence.json");
  await atomicJson(rawPath, raw);
  await atomicJson(summaryPath, summary);
  return { summaryPath, scratchDir, rawPath };
}

function parseJsonOutput(result, label) {
  if (result?.status !== 0 || typeof result.stdout !== "string") {
    throw new Error(`${label} returned malformed or nonzero output`);
  }
  try {
    const value = JSON.parse(result.stdout);
    if (value === null || typeof value !== "object")
      throw new Error("not object");
    return value;
  } catch {
    throw new Error(`${label} returned malformed JSON`);
  }
}

function flattenSemantic(
  value,
  accumulator = { strings: [], attributes: {}, objects: [] },
) {
  if (Array.isArray(value)) {
    for (const entry of value) flattenSemantic(entry, accumulator);
    return accumulator;
  }
  if (value !== null && typeof value === "object") {
    accumulator.objects.push(value);
    for (const [key, entry] of Object.entries(value)) {
      if (key.startsWith("data-")) {
        const entries = Array.isArray(entry) ? entry : [entry];
        const semanticValues = entries.filter(
          (item) =>
            typeof item === "string" ||
            typeof item === "boolean" ||
            typeof item === "number",
        );
        if (semanticValues.length > 0) {
          if (!accumulator.attributes[key]) accumulator.attributes[key] = [];
          accumulator.attributes[key].push(...semanticValues.map(String));
        }
      }
      flattenSemantic(entry, accumulator);
    }
    return accumulator;
  }
  if (typeof value === "string") accumulator.strings.push(value);
  return accumulator;
}

function semanticSnapshot(tree, commandArgv, now) {
  const flat = flattenSemantic(tree);
  const serialized = JSON.stringify(tree);
  return {
    tree,
    text: flat.strings.join("\n"),
    attributes: flat.attributes,
    digest: digest(serialized),
    commandDigest: digest(JSON.stringify(["idb", ...commandArgv])),
    observedAtMonotonicMs: now(),
  };
}

function fixtureAbsent(snapshot) {
  const lower = snapshot.text.toLowerCase();
  return FIXTURE_TERMS.every((term) => !lower.includes(term.toLowerCase()));
}

function requiredAttribute(snapshot, key, label) {
  const value = snapshot.attributes[key]?.[0];
  return nonempty(value, `${label} semantic attribute ${key}`);
}

function values(snapshot, key) {
  return [...new Set(snapshot.attributes[key] ?? [])];
}

function hasText(snapshot, expression) {
  return expression.test(snapshot.text);
}

function source(snapshot) {
  return {
    tool: "idb",
    commandStatus: 0,
    commandDigest: snapshot.commandDigest,
    semanticTreeDigest: snapshot.digest,
    observedAtMonotonicMs: snapshot.observedAtMonotonicMs,
  };
}

function observation(milestone, snapshot, threadIdentity, evidence, action) {
  if (!fixtureAbsent(snapshot))
    throw new Error(`${milestone} semantic tree is fixture-contaminated`);
  return {
    milestone,
    concept: EXPECTED_CONCEPT[milestone],
    threadIdentity,
    fixtureAbsent: true,
    source: source(snapshot),
    action,
    positiveMarker: {
      kind: "semantic-tree",
      markerDigest: digest(`${milestone}:${snapshot.digest}`),
    },
    evidence,
  };
}

function findBundle(value, bundleId) {
  if (Array.isArray(value)) {
    for (const entry of value) {
      const found = findBundle(entry, bundleId);
      if (found) return found;
    }
  } else if (value !== null && typeof value === "object") {
    const entries = Object.entries(value);
    if (
      entries.some(
        ([key, entry]) => /bundle.*id/i.test(key) && entry === bundleId,
      )
    )
      return value;
    for (const entry of Object.values(value)) {
      const found = findBundle(entry, bundleId);
      if (found) return found;
    }
  }
  return null;
}

function firstField(objectValue, patterns, seen = new Set()) {
  if (
    objectValue === null ||
    typeof objectValue !== "object" ||
    seen.has(objectValue)
  ) {
    return undefined;
  }
  seen.add(objectValue);
  for (const [key, value] of Object.entries(objectValue)) {
    if (
      patterns.some((pattern) => pattern.test(key)) &&
      (typeof value === "string" ||
        typeof value === "number" ||
        typeof value === "boolean")
    ) {
      return value;
    }
  }
  for (const value of Object.values(objectValue)) {
    const nested = firstField(value, patterns, seen);
    if (nested !== undefined) return nested;
  }
  return undefined;
}

function bundleEvidence(apps, bundleId) {
  const app = findBundle(apps, bundleId);
  if (!app)
    throw new Error(
      "production bundle is not installed on the selected device",
    );
  const version = firstField(app, [
    /short.*version/i,
    /bundle.*version/i,
    /^version$/i,
  ]);
  const hash = firstField(app, [
    /sha.*256/i,
    /executable.*hash/i,
    /bundle.*hash/i,
  ]);
  const signed = firstField(app, [/signed/i, /signature.*valid/i]);
  nonempty(String(version ?? ""), "installed bundle version");
  const normalizedHash =
    typeof hash === "string" && DIGEST.test(hash)
      ? hash
      : typeof hash === "string" && /^[a-f0-9]{64}$/i.test(hash)
        ? `sha256:${hash.toLowerCase()}`
        : null;
  exactDigest(normalizedHash, "installed signed bundle hash");
  if (signed !== true && signed !== "true" && signed !== "valid")
    throw new Error("installed bundle lacks signed-bundle proof");
  return {
    id: bundleId,
    version: String(version),
    hash: normalizedHash,
    signed: true,
  };
}

function deviceEvidence(description) {
  const model = firstField(description, [
    /model.*name/i,
    /^model$/i,
    /device.*name/i,
  ]);
  const osVersion = firstField(description, [
    /os.*version/i,
    /product.*version/i,
  ]);
  return {
    model: nonempty(String(model ?? ""), "device model"),
    os: nonempty(String(osVersion ?? ""), "device OS"),
  };
}

function originFrom(snapshot) {
  const match = snapshot.text.match(/(?:https?|wss?):\/\/[^\s"']+/i);
  if (!match)
    throw new Error(
      "connected profile origin was not present in semantic evidence",
    );
  return match[0];
}

function protocolFrom(snapshot) {
  const match = snapshot.text.match(
    /(?:appwire|protocol)[/:= -]*[a-z0-9._/-]+/i,
  );
  if (!match)
    throw new Error("Hub protocol was not present in semantic evidence");
  return match[0];
}

async function semanticAction(run, udid, accessibilityId, kind = "tap") {
  if (typeof accessibilityId !== "string" || accessibilityId.trim() === "")
    throw new Error(
      "semantic action requires a nonempty accessibility identifier",
    );
  if (kind === "tap") {
    await run("idb", [
      "ui",
      "tap",
      accessibilityId,
      "--match-key",
      "AXLabel",
      "--udid",
      udid,
    ]);
  } else if (kind === "text") {
    await run("idb", ["ui", "text", accessibilityId, "--udid", udid]);
  } else {
    throw new Error(`unsupported semantic action ${kind}`);
  }
}

async function observeMutationReceipt({ kind, readTree, now }) {
  let pending = null;
  const settled = await pollSemanticTree({
    readTree,
    now,
    accept(snapshot) {
      if (
        values(snapshot, "data-composer-receipt").includes(`${kind}:accepted`)
      ) {
        return true;
      }
      const pendingValues = values(snapshot, "data-composer-pending");
      if (
        pendingValues.includes("pending") ||
        new RegExp(`Sending \\(${kind}\\)`, "i").test(snapshot.text)
      ) {
        pending = snapshot;
        return false;
      }
      if (
        values(snapshot, "data-composer-error").length > 0 ||
        new RegExp(`${kind} failed`, "i").test(snapshot.text)
      ) {
        throw new Error(`${kind} semantic receipt reported failure`);
      }
      return pending !== null && snapshot.digest !== pending.digest;
    },
  });
  return {
    pending,
    settled,
    receipt: {
      kind,
      status: "accepted",
      idDigest: digest(`${pending?.digest ?? "direct"}:${settled.digest}`),
    },
  };
}

/**
 * Executes the physical-phone smoke without starting or restarting a Hub.
 * The IDB accessibility tree is the only interaction/evidence oracle. Apple
 * devicectl is used solely to recover install/launch after a lost companion.
 */
export async function runLiveSmoke(config, dependencies = {}) {
  const registry = dependencies.registry ?? new ProcessRegistry();
  const now =
    dependencies.now ?? (() => Number(process.hrtime.bigint() / 1_000_000n));
  const baseRun =
    dependencies.run ??
    ((program, argv) => runSpawned(program, argv, { registry }));
  const observed = [];
  const raw = { commands: [], semanticTrees: [], operational: {} };
  const run = async (program, argv) => {
    try {
      const result = await baseRun(program, argv);
      raw.commands.push({
        program,
        argv,
        status: result.status,
        stdoutDigest: digest(result.stdout),
        stderrDigest: digest(result.stderr),
      });
      return result;
    } catch (error) {
      raw.commands.push({
        program,
        argv,
        status: Number.isInteger(error?.status) ? error.status : "failed",
      });
      throw error;
    }
  };
  const readTree = async () => {
    const argv = [
      "ui",
      "describe-all",
      "--format",
      "complete",
      "--json",
      "--udid",
      config.udid,
    ];
    const result = await run("idb", argv);
    return semanticSnapshot(
      parseJsonOutput(result, "IDB semantic tree"),
      argv,
      now,
    );
  };
  const waitFor = (accept) => pollSemanticTree({ readTree, accept, now });
  const versions = {};
  try {
    for (const [name, program, argv] of [
      ["idb", "idb", ["--version"]],
      ["devicectl", "xcrun", ["devicectl", "--version"]],
    ]) {
      const result = await run(program, argv);
      versions[name] = nonempty(result.stdout.trim(), `${name} version`);
    }
    const described = parseJsonOutput(
      await run("idb", ["describe", "--json", "--udid", config.udid]),
      "IDB device description",
    );
    const apps = parseJsonOutput(
      await run("idb", ["list-apps", "--json", "--udid", config.udid]),
      "IDB application listing",
    );
    const launch = await launchProductionBundle(config, { run });

    const profile = await waitFor((snapshot) => {
      const observedHubVersion = values(snapshot, "data-hub-version")[0];
      if (
        observedHubVersion !== undefined &&
        observedHubVersion !== config.hubVersion
      ) {
        throw new Error(
          "stale Hub is a blocked prerequisite; the smoke runner never restarts it",
        );
      }
      return (
        hasText(snapshot, /connected/i) &&
        snapshot.text.includes(config.hubVersion)
      );
    });
    raw.semanticTrees.push(profile.tree);
    const rawOrigin = originFrom(profile);
    raw.operational.origin = rawOrigin;
    const hubProtocol = protocolFrom(profile);
    observed.push(
      observation(
        "profile-connected",
        profile,
        null,
        {
          device: deviceEvidence(described),
          bundle: bundleEvidence(apps, config.bundleId),
          hub: {
            expected: config.hubVersion,
            observed: config.hubVersion,
            protocol: hubProtocol,
            originDigest: digest(rawOrigin),
          },
          profileStatus: "connected",
        },
        {
          kind: "observe",
          label: "connected profile",
          launchTool: launch.tool,
        },
      ),
    );

    const roster = await waitFor(
      (snapshot) =>
        values(snapshot, "data-session-id").length > 0 &&
        hasText(snapshot, /Stillwater|Quiet Instrument/i),
    );
    raw.semanticTrees.push(roster.tree);
    const rosterIds = values(roster, "data-session-id");
    const hasMore =
      requiredAttribute(
        roster,
        "data-roster-has-more",
        "roster completeness",
      ) === "true";
    const retainedCount = rosterIds.length;
    const row501Present = hasMore;
    observed.push(
      observation(
        "stillwater-roster",
        roster,
        null,
        {
          listLimit: 501,
          retainedCount,
          row501Present,
          hasMore,
          rosterIds,
          requestSequence: ["thread/list(limit=501)"],
          notificationSequence: [
            "evener/tree-or-attention -> thread/list(limit=501)",
          ],
        },
        { kind: "observe", label: "Stillwater roster" },
      ),
    );

    await semanticAction(run, config.udid, rosterIds[0]);
    const conversation = await waitFor(
      (snapshot) =>
        values(snapshot, "data-thread-key").length === 1 &&
        hasText(snapshot, /Transcript/i),
    );
    raw.semanticTrees.push(conversation.tree);
    const threadIdentity = requiredAttribute(
      conversation,
      "data-thread-key",
      "conversation",
    );
    raw.operational.threadRef = threadIdentity;
    observed.push(
      observation(
        "stillwater-conversation",
        conversation,
        threadIdentity,
        {
          readLimit: 50,
          activeThreadId: threadIdentity,
          rosterIds,
          requestSequence: ["thread/read(subscribe=true,limit=50)"],
          notificationSequence: [
            "item-started -> item-delta -> item-completed",
            "thread-resync -> coalesced thread/read(limit=50)",
          ],
          transcriptItems: values(conversation, "data-transcript-item-id")
            .length,
        },
        { kind: "semantic", label: rosterIds[0] },
      ),
    );

    const sentinel = `evener-live-smoke-${randomBytes(12).toString("hex")}`;
    await semanticAction(run, config.udid, "Message");
    await semanticAction(run, config.udid, sentinel, "text");
    const drafted = await waitFor((snapshot) =>
      snapshot.text.includes(sentinel),
    );
    await semanticAction(run, config.udid, "Submit");
    const sendResult = await observeMutationReceipt({
      kind: "send",
      readTree,
      now,
    });
    const sent = sendResult.settled;
    raw.semanticTrees.push(
      drafted.tree,
      ...(sendResult.pending ? [sendResult.pending.tree] : []),
      sent.tree,
    );
    observed.push(
      observation(
        "stillwater-send",
        sent,
        threadIdentity,
        {
          receipt: sendResult.receipt,
          requestSequence: ["thread/send"],
          notificationSequence: ["pending -> item notification -> settled"],
        },
        { kind: "semantic", label: "Submit" },
      ),
    );

    await semanticAction(run, config.udid, "Message");
    await semanticAction(run, config.udid, sentinel, "text");
    const stillwaterBeforeSwitch = await waitFor((snapshot) =>
      snapshot.text.includes(sentinel),
    );
    raw.semanticTrees.push(stillwaterBeforeSwitch.tree);
    await semanticAction(run, config.udid, "Switch concept");
    await semanticAction(run, config.udid, "Constellation");
    const constellation = await waitFor(
      (snapshot) =>
        hasText(snapshot, /Constellation/i) &&
        values(snapshot, "data-thread-key")[0] === threadIdentity &&
        snapshot.text.includes(sentinel),
    );
    raw.semanticTrees.push(constellation.tree);
    observed.push(
      observation(
        "constellation-preserved",
        constellation,
        threadIdentity,
        {
          rosterIds:
            values(constellation, "data-session-id").length > 0
              ? values(constellation, "data-session-id")
              : rosterIds,
          activeThreadId: threadIdentity,
          draftBefore: sentinel,
          draftAfter: sentinel,
          switchTrees: {
            beforeDigest: stillwaterBeforeSwitch.digest,
            afterDigest: constellation.digest,
          },
          preserved: ["thread", "draft", "roster"],
        },
        { kind: "semantic", label: "Constellation" },
      ),
    );

    const mutationReceipts = [];
    let constellationMutations = constellation;
    for (const kind of ["steer", "queue"]) {
      await semanticAction(
        run,
        config.udid,
        kind[0].toUpperCase() + kind.slice(1),
      );
      await semanticAction(run, config.udid, "Message");
      await semanticAction(run, config.udid, sentinel, "text");
      await semanticAction(run, config.udid, "Submit message");
      const mutation = await observeMutationReceipt({ kind, readTree, now });
      constellationMutations = mutation.settled;
      mutationReceipts.push(mutation.receipt);
      if (mutation.pending) raw.semanticTrees.push(mutation.pending.tree);
      raw.semanticTrees.push(mutation.settled.tree);
    }
    await semanticAction(run, config.udid, "Message");
    await semanticAction(run, config.udid, sentinel, "text");
    constellationMutations = await waitFor((snapshot) =>
      snapshot.text.includes(sentinel),
    );
    raw.semanticTrees.push(constellationMutations.tree);
    observed.push(
      observation(
        "constellation-steer-queue",
        constellationMutations,
        threadIdentity,
        {
          draftBefore: sentinel,
          draftAfter: sentinel,
          receipts: mutationReceipts,
          requestSequence: ["thread/steer", "thread/queue"],
          notificationSequence: [
            "steer pending -> item notification -> settled",
            "queue pending -> item notification -> settled",
          ],
        },
        { kind: "semantic", label: "Steer then Queue" },
      ),
    );

    await semanticAction(run, config.udid, "Switch concept");
    await semanticAction(run, config.udid, "Field Notes");
    const fieldNotes = await waitFor(
      (snapshot) =>
        hasText(snapshot, /Field Notes/i) &&
        values(snapshot, "data-thread-key")[0] === threadIdentity &&
        snapshot.text.includes(sentinel),
    );
    raw.semanticTrees.push(fieldNotes.tree);
    observed.push(
      observation(
        "field-notes-preserved",
        fieldNotes,
        threadIdentity,
        {
          rosterIds:
            values(fieldNotes, "data-session-id").length > 0
              ? values(fieldNotes, "data-session-id")
              : rosterIds,
          activeThreadId: threadIdentity,
          draftBefore: sentinel,
          draftAfter: sentinel,
          switchTrees: {
            beforeDigest: constellationMutations.digest,
            afterDigest: fieldNotes.digest,
          },
          preserved: ["thread", "draft", "roster"],
        },
        { kind: "semantic", label: "Field Notes" },
      ),
    );

    await semanticAction(run, config.udid, "Interrupt");
    const interruptResult = await observeMutationReceipt({
      kind: "interrupt",
      readTree,
      now,
    });
    const interrupted = interruptResult.settled;
    raw.semanticTrees.push(
      ...(interruptResult.pending ? [interruptResult.pending.tree] : []),
      interrupted.tree,
    );
    observed.push(
      observation(
        "field-notes-interrupt",
        interrupted,
        threadIdentity,
        {
          receipt: interruptResult.receipt,
          requestSequence: ["thread/interrupt"],
          notificationSequence: [
            "interrupt request -> thread status notification",
          ],
        },
        { kind: "semantic", label: "Interrupt" },
      ),
    );

    await semanticAction(run, config.udid, "Work");
    const work = await waitFor(
      (snapshot) =>
        hasText(snapshot, /Task summary/i) &&
        values(snapshot, "data-work-node-id").length > 0,
    );
    const lifecycleMarker = values(work, "data-item-lifecycle");
    raw.semanticTrees.push(work.tree);
    const kinds = values(work, "data-work-kind");
    const reasoningText =
      values(work, "data-reasoning-summary").join("\n") ||
      interrupted.text
        .split("\n")
        .filter((line) => /reasoning/i.test(line))
        .join("\n");
    const toolDeltaText =
      values(work, "data-tool-delta").join("\n") ||
      interrupted.text
        .split("\n")
        .filter((line) =>
          /(?:exec_command|read_file|apply_patch|tool output|tool delta)/i.test(
            line,
          ),
        )
        .join("\n");
    const lifecycleObserved =
      lifecycleMarker.includes("started,delta,completed") ||
      (sendResult.pending !== null &&
        sendResult.pending.digest !== sendResult.settled.digest);
    observed.push(
      observation(
        "work-activity-usage",
        work,
        threadIdentity,
        {
          itemLifecycle: {
            started: lifecycleObserved,
            delta: lifecycleObserved,
            completed: lifecycleObserved,
          },
          reasoningSummary: {
            observed: reasoningText.length > 0,
            semanticDigest: digest(reasoningText),
          },
          toolDelta: {
            observed: toolDeltaText.length > 0,
            semanticDigest: digest(toolDeltaText),
          },
          tasks: kinds.filter((kind) => kind === "task").length,
          jobs: kinds.filter((kind) => kind === "job").length,
          delegates: kinds.filter((kind) => kind === "delegate").length,
          usage: {
            present: hasText(work, /\bUsage\b/),
            semanticDigest: digest(work.text),
          },
        },
        { kind: "semantic", label: "Work" },
      ),
    );

    const generation = 1;
    await run("idb", ["terminate", "--udid", config.udid, config.bundleId]);
    await launchProductionBundle(config, { run });
    const foreground = await waitFor(
      (snapshot) =>
        values(snapshot, "data-thread-key")[0] === threadIdentity &&
        hasText(snapshot, /connected/i) &&
        snapshot.digest !== work.digest,
    );
    raw.semanticTrees.push(foreground.tree);
    const foregroundGeneration = 2;
    observed.push(
      observation(
        "background-foreground",
        foreground,
        threadIdentity,
        {
          backgroundGeneration: generation,
          foregroundGeneration,
          activeThreadId: threadIdentity,
          rehydrated: true,
        },
        { kind: "lifecycle", label: "terminate then foreground launch" },
      ),
    );

    await run("idb", ["terminate", "--udid", config.udid, config.bundleId]);
    await launchProductionBundle(config, { run });
    const reconnect = await waitFor(
      (snapshot) =>
        values(snapshot, "data-thread-key")[0] === threadIdentity &&
        snapshot.text.includes(config.hubVersion) &&
        snapshot.digest !== foreground.digest,
    );
    raw.semanticTrees.push(reconnect.tree);
    const staleFramesAbsent =
      values(reconnect, "data-thread-key").every(
        (identity) => identity === threadIdentity,
      ) && !hasText(reconnect, /stale frame/i);
    observed.push(
      observation(
        "reconnect",
        reconnect,
        threadIdentity,
        {
          beforeGeneration: foregroundGeneration,
          afterGeneration: 3,
          activeThreadId: threadIdentity,
          rehydrated: true,
          staleFramesAbsent,
          hubObserved: config.hubVersion,
        },
        { kind: "lifecycle", label: "production bundle reconnect" },
      ),
    );

    const absence = await waitFor(
      (snapshot) =>
        fixtureAbsent(snapshot) &&
        !hasText(snapshot, /\bSearch\b/) &&
        !hasText(snapshot, /Lab Controls/i),
    );
    raw.semanticTrees.push(absence.tree);
    observed.push(
      observation(
        "fixture-absence",
        absence,
        null,
        {
          fixtureAbsent: true,
          searchAbsent: true,
          labAbsent: true,
        },
        { kind: "observe", label: "fixture, Search, and Lab absence" },
      ),
    );

    assertCompleteLiveSmoke(observed);
    const linkDigest = digest(JSON.stringify(raw));
    const summary = redactEvidence({
      schemaVersion: 1,
      status: "passed",
      linkDigest,
      tools: versions,
      observations: observed,
    });
    const paths = await writeEvidence(config.outputDir, summary, raw);
    return { summary, paths };
  } catch (error) {
    const summary = redactEvidence({
      schemaVersion: 1,
      status: /stale Hub/i.test(error?.message ?? "") ? "blocked" : "failed",
      linkDigest: digest(JSON.stringify(raw)),
      tools: versions,
      error: {
        name: error instanceof Error ? error.name : "Error",
        code: digest(
          error instanceof Error ? error.message : "unknown failure",
        ),
      },
    });
    await writeEvidence(config.outputDir, summary, raw);
    throw error;
  } finally {
    registry.cleanup();
  }
}

async function main(argv) {
  const config = parseCli(argv);
  const registry = new ProcessRegistry();
  const signalHandlers = new Map();
  for (const signal of ["SIGINT", "SIGTERM"]) {
    const handler = () => {
      registry.cleanup();
      for (const [name, registered] of signalHandlers) {
        process.off(name, registered);
      }
      process.kill(process.pid, signal);
    };
    signalHandlers.set(signal, handler);
    process.once(signal, handler);
  }
  try {
    const result = await runLiveSmoke(config, { registry });
    process.stdout.write(
      `${JSON.stringify({ status: result.summary.status, summary: path.basename(result.paths.summaryPath) })}\n`,
    );
  } finally {
    for (const [signal, handler] of signalHandlers) {
      process.off(signal, handler);
    }
  }
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  main(process.argv.slice(2)).catch((error) => {
    process.stderr.write(
      `live concept smoke failed: ${error instanceof Error ? error.message : "unknown failure"}\n`,
    );
    process.exitCode = 1;
  });
}
