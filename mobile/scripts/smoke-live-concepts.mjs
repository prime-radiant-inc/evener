import { spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import {
  chmod,
  link,
  lstat,
  mkdir,
  open,
  readdir,
  readFile,
  unlink,
} from "node:fs/promises";
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

export const EXPECTED_LIVE_SEQUENCE = Object.freeze({
  classification: "expected-task4-proven",
  roster: Object.freeze({
    requests: Object.freeze(["thread/list(limit=501)"]),
    notifications: Object.freeze([
      "tree-or-attention -> bounded roster refresh",
    ]),
  }),
  conversation: Object.freeze({
    requests: Object.freeze(["thread/read(subscribe=true,turnLimit=50)"]),
    notifications: Object.freeze([
      "item-started -> item-delta -> item-completed",
      "thread-resync -> coalesced bounded read",
    ]),
  }),
  send: Object.freeze({
    requests: Object.freeze(["thread/send"]),
    notifications: Object.freeze([
      "mutation pending -> item lifecycle -> accepted",
    ]),
  }),
  steerQueue: Object.freeze({
    requests: Object.freeze(["thread/steer", "thread/queue"]),
    notifications: Object.freeze([
      "steer pending -> accepted",
      "queue pending -> accepted",
    ]),
  }),
  interrupt: Object.freeze({
    requests: Object.freeze(["thread/interrupt"]),
    notifications: Object.freeze(["interrupt pending -> accepted"]),
  }),
});

const SHA256 = /^sha256:[a-f0-9]{64}$/;
const OPAQUE_ID = /^[A-Za-z0-9._:-]+$/;
const SAFE_VERSION = /^[A-Za-z0-9][A-Za-z0-9._+:-]{0,127}$/;
const SAFE_BUNDLE_ID = /^[A-Za-z0-9][A-Za-z0-9.-]{0,254}$/;
const SAFE_DEVICE_MODEL = /^[A-Za-z0-9][A-Za-z0-9 ._()+-]{0,127}$/;
const THREAD_MILESTONES = new Set(REQUIRED_LIVE_MILESTONES.slice(2, 11));
const TRIPWIRE_MS = 10_000;
const CONTAMINATION =
  /(?:\bfixture(?:[\s_-]+(?:session|scenario|content))?\b|\bscenario(?:[\s_-]+switcher)?\b|\bprototype\b|\bsynthetic[\s_-]+completion\b)/i;
const FORBIDDEN_FINAL = /^(?:Search|Lab[ _-]+Controls?)$/i;

class SmokeError extends Error {
  constructor(code, status = "failed") {
    super(code);
    this.name = "SmokeError";
    this.code = code;
    this.smokeStatus = status;
  }
}

function digestBytes(bytes) {
  return `sha256:${createHash("sha256").update(bytes).digest("hex")}`;
}

function digest(value) {
  return digestBytes(Buffer.from(String(value), "utf8"));
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function requireObject(value, label) {
  if (!isObject(value)) throw new Error(`${label} must be an object`);
  return value;
}

function requireNonempty(value, label) {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${label} must be nonempty`);
  }
  return value;
}

function requireDigest(value, label) {
  if (typeof value !== "string" || !SHA256.test(value)) {
    throw new Error(`${label} must be a sha256 digest`);
  }
}

function requirePositiveInteger(value, label, allowZero = false) {
  if (!Number.isSafeInteger(value) || value < (allowZero ? 0 : 1)) {
    throw new Error(`${label} must be an integer`);
  }
}

function requireIds(value, label) {
  if (
    !Array.isArray(value) ||
    value.length === 0 ||
    value.some(
      (entry) => typeof entry !== "string" || !OPAQUE_ID.test(entry),
    ) ||
    new Set(value).size !== value.length
  ) {
    throw new Error(`${label} must contain unique opaque IDs`);
  }
}

function arraysEqual(left, right) {
  return (
    Array.isArray(left) &&
    Array.isArray(right) &&
    left.length === right.length &&
    left.every((value, index) => value === right[index])
  );
}

function containsPassedTrue(value) {
  if (Array.isArray(value)) return value.some(containsPassedTrue);
  if (!isObject(value)) return false;
  return Object.entries(value).some(
    ([key, entry]) =>
      (key.toLowerCase() === "passed" && entry === true) ||
      containsPassedTrue(entry),
  );
}

export function derivePositiveMarker(observation) {
  return digest(
    JSON.stringify({
      milestone: observation?.milestone ?? null,
      concept: observation?.concept ?? null,
      threadIdentity: observation?.threadIdentity ?? null,
      fixtureAbsent: observation?.fixtureAbsent ?? null,
      source: observation?.source ?? null,
      action: observation?.action ?? null,
      evidence: observation?.evidence ?? null,
    }),
  );
}

function requireExpectedSequence(value, label) {
  const sequence = requireObject(value, `${label} expected sequence`);
  if (sequence.classification !== "expected-task4-proven") {
    throw new Error(`${label} expected sequence classification is invalid`);
  }
  for (const key of ["requests", "notifications"]) {
    if (
      !Array.isArray(sequence[key]) ||
      sequence[key].length === 0 ||
      sequence[key].some(
        (entry) => typeof entry !== "string" || entry.trim() === "",
      )
    ) {
      throw new Error(`${label} expected ${key} must be nonempty`);
    }
  }
}

function requireReceipt(value, kind) {
  const receipt = requireObject(value, `${kind} receipt`);
  if (receipt.kind !== kind || receipt.status !== "accepted") {
    throw new Error(`${kind} receipt must be accepted`);
  }
  requirePositiveInteger(receipt.receipt, `${kind} receipt number`);
  requireDigest(receipt.pendingTreeDigest, `${kind} pending tree`);
  requireDigest(receipt.acceptedTreeDigest, `${kind} accepted tree`);
  if (receipt.pendingTreeDigest === receipt.acceptedTreeDigest) {
    throw new Error(`${kind} pending and accepted trees must differ`);
  }
}

function requirePreservation(evidence, concept, shared, threadIdentity) {
  requireIds(evidence.rosterIds, `${concept} roster IDs`);
  if (!arraysEqual(evidence.rosterIds, shared.rosterIds)) {
    throw new Error(`${concept} roster IDs must match Stillwater`);
  }
  if (evidence.activeThreadId !== threadIdentity) {
    throw new Error(`${concept} active thread must match`);
  }
  requireNonempty(evidence.draftBefore, `${concept} draft before`);
  if (evidence.draftAfter !== evidence.draftBefore) {
    throw new Error(`${concept} draft must be preserved`);
  }
  requireDigest(evidence.beforeTreeDigest, `${concept} before tree`);
  requireDigest(evidence.afterTreeDigest, `${concept} after tree`);
  if (evidence.beforeTreeDigest === evidence.afterTreeDigest) {
    throw new Error(`${concept} trees must differ`);
  }
}

function validateMilestoneEvidence(observation, shared) {
  const evidence = requireObject(
    observation.evidence,
    `${observation.milestone} evidence`,
  );
  switch (observation.milestone) {
    case "profile-connected": {
      const device = requireObject(evidence.device, "device evidence");
      requireNonempty(device.model, "device model");
      requireNonempty(device.os, "device OS");
      const installed = requireObject(evidence.installedApp, "installed app");
      const local = requireObject(evidence.localBundle, "local bundle");
      for (const app of [installed, local]) {
        requireNonempty(app.id, "app ID");
        requireNonempty(app.version, "app version");
      }
      if (installed.id !== local.id || installed.version !== local.version) {
        throw new Error("installed and local app identity must match");
      }
      requirePositiveInteger(installed.pid, "installed app process ID");
      requireDigest(local.hash, "local bundle hash");
      if (local.codesignVerified !== true) {
        throw new Error("local bundle must be codesign verified");
      }
      const hub = requireObject(evidence.hub, "Hub evidence");
      requireNonempty(hub.expectedVersion, "expected Hub version");
      requireNonempty(hub.observedVersion, "observed Hub version");
      if (hub.expectedVersion !== hub.observedVersion) {
        throw new Error("stale Hub is blocked");
      }
      requireNonempty(hub.protocolVersion, "Hub protocol");
      requireDigest(hub.originDigest, "origin digest");
      const connection = requireObject(
        evidence.connection,
        "connection evidence",
      );
      if (connection.status !== "connected") {
        throw new Error("profile must be connected");
      }
      for (const key of [
        "profileGeneration",
        "lifecycleGeneration",
        "handshakeGeneration",
      ]) {
        requirePositiveInteger(connection[key], key, true);
      }
      shared.hubVersion = hub.observedVersion;
      shared.app = installed;
      break;
    }
    case "stillwater-roster":
      requireIds(evidence.rosterIds, "Stillwater roster IDs");
      if (evidence.retainedCount !== evidence.rosterIds.length) {
        throw new Error("retained count must equal observed roster IDs");
      }
      if (typeof evidence.hasMore !== "boolean") {
        throw new Error("hasMore must be observed");
      }
      if (
        evidence.completeness !==
        (evidence.hasMore ? "more-available" : "complete")
      ) {
        throw new Error("roster completeness must match hasMore");
      }
      if (evidence.listLimit !== undefined && evidence.listLimit !== 501) {
        throw new Error("expected roster list limit must be 501");
      }
      requireExpectedSequence(evidence.expectedSequence, "roster");
      shared.rosterIds = evidence.rosterIds;
      break;
    case "stillwater-conversation":
      if (evidence.activeThreadId !== observation.threadIdentity) {
        throw new Error("active thread ID must match");
      }
      requireDigest(evidence.titleDigest, "conversation title digest");
      requireIds(evidence.transcriptIds, "transcript IDs");
      if (evidence.readLimit !== undefined && evidence.readLimit !== 50) {
        throw new Error("expected read limit must be 50");
      }
      requireExpectedSequence(evidence.expectedSequence, "conversation");
      shared.titleDigest = evidence.titleDigest;
      break;
    case "stillwater-send": {
      requireReceipt(evidence.receipt, "send");
      const lifecycle = requireObject(evidence.lifecycle, "item lifecycle");
      requireNonempty(lifecycle.itemId, "lifecycle item ID");
      requireDigest(lifecycle.streamingTreeDigest, "streaming tree");
      requireDigest(lifecycle.completedTreeDigest, "completed tree");
      if (lifecycle.streamingTreeDigest === lifecycle.completedTreeDigest) {
        throw new Error("streaming and completed trees must differ");
      }
      const reasoning = requireObject(evidence.reasoning, "reasoning evidence");
      requireNonempty(reasoning.itemId, "reasoning item ID");
      requireDigest(reasoning.contentDigest, "reasoning content");
      const tool = requireObject(evidence.tool, "tool evidence");
      requireNonempty(tool.itemId, "tool item ID");
      requireDigest(tool.contentDigest, "tool content");
      requireExpectedSequence(evidence.expectedSequence, "send");
      break;
    }
    case "constellation-preserved":
      requirePreservation(
        evidence,
        "Constellation",
        shared,
        observation.threadIdentity,
      );
      break;
    case "constellation-steer-queue":
      if (!Array.isArray(evidence.receipts) || evidence.receipts.length !== 2) {
        throw new Error("steer and queue receipts are required");
      }
      requireReceipt(evidence.receipts[0], "steer");
      requireReceipt(evidence.receipts[1], "queue");
      requireExpectedSequence(evidence.expectedSequence, "steer/queue");
      break;
    case "field-notes-preserved":
      requirePreservation(
        evidence,
        "Field Notes",
        shared,
        observation.threadIdentity,
      );
      break;
    case "field-notes-interrupt":
      requireReceipt(evidence.receipt, "interrupt");
      requireExpectedSequence(evidence.expectedSequence, "interrupt");
      break;
    case "work-activity-usage":
      for (const key of ["tasks", "jobs", "delegates"]) {
        if (!Array.isArray(evidence[key]) || evidence[key].length === 0) {
          throw new Error(`${key} evidence is required`);
        }
        for (const item of evidence[key]) {
          requireNonempty(item.id, `${key} ID`);
          requireDigest(item.digest, `${key} digest`);
        }
      }
      requireDigest(
        requireObject(evidence.usage, "usage evidence").digest,
        "usage digest",
      );
      break;
    case "background-foreground":
      requirePositiveInteger(evidence.processIdBefore, "process ID before");
      requirePositiveInteger(evidence.processIdAfter, "process ID after");
      if (evidence.processIdBefore !== evidence.processIdAfter) {
        throw new Error("background/foreground must preserve the same process");
      }
      requirePositiveInteger(
        evidence.lifecycleGenerationBefore,
        "lifecycle before",
        true,
      );
      requirePositiveInteger(
        evidence.lifecycleGenerationAfter,
        "lifecycle after",
        true,
      );
      if (
        evidence.lifecycleGenerationAfter <= evidence.lifecycleGenerationBefore
      ) {
        throw new Error("lifecycle generation must advance");
      }
      for (const prefix of ["profile", "handshake"]) {
        const before = evidence[`${prefix}GenerationBefore`];
        const after = evidence[`${prefix}GenerationAfter`];
        requirePositiveInteger(before, `${prefix} generation before`, true);
        requirePositiveInteger(after, `${prefix} generation after`, true);
        if (before !== after) {
          throw new Error(
            `${prefix} generation must stay on the same connection`,
          );
        }
      }
      if (evidence.activeThreadId !== observation.threadIdentity) {
        throw new Error("foreground thread must match");
      }
      requireNonempty(evidence.draftBefore, "background draft before");
      if (evidence.draftAfter !== evidence.draftBefore) {
        throw new Error("background draft must be preserved");
      }
      requireDigest(evidence.backgroundTreeDigest, "background tree");
      requireDigest(evidence.foregroundTreeDigest, "foreground tree");
      break;
    case "reconnect":
      requirePositiveInteger(evidence.processIdBefore, "reconnect PID before");
      requirePositiveInteger(evidence.processIdAfter, "reconnect PID after");
      if (evidence.processIdBefore === evidence.processIdAfter) {
        throw new Error("reconnect must use a fresh process");
      }
      if (evidence.reopened !== true) {
        throw new Error("reconnect must explicitly reopen the session");
      }
      if (evidence.activeThreadId !== observation.threadIdentity) {
        throw new Error("reconnect thread must match");
      }
      requireDigest(evidence.titleDigest, "reconnect title digest");
      if (evidence.titleDigest !== shared.titleDigest) {
        throw new Error("reconnect title must identify the same session");
      }
      requireDigest(evidence.transcriptDigest, "reconnect transcript digest");
      requireDigest(evidence.connectionTreeDigest, "reconnect connection tree");
      break;
    case "fixture-absence":
      if (
        evidence.fixtureAbsent !== true ||
        evidence.searchAbsent !== true ||
        evidence.labAbsent !== true
      ) {
        throw new Error("fixture, Search, and Lab must be absent");
      }
      break;
    default:
      throw new Error("unsupported milestone");
  }
}

export function assertCompleteLiveSmoke(observed) {
  if (!Array.isArray(observed))
    throw new Error("observations must be an array");
  const names = observed.map((entry) => entry?.milestone);
  const seen = new Set();
  for (const name of names) {
    if (seen.has(name)) throw new Error(`duplicate milestone ${String(name)}`);
    seen.add(name);
  }
  const missing = REQUIRED_LIVE_MILESTONES.filter((name) => !seen.has(name));
  if (missing.length > 0)
    throw new Error(`missing milestones ${missing.join(",")}`);
  if (
    observed.length !== REQUIRED_LIVE_MILESTONES.length ||
    names.some((name) => !REQUIRED_LIVE_MILESTONES.includes(name))
  ) {
    throw new Error("extra milestones");
  }
  for (let index = 0; index < REQUIRED_LIVE_MILESTONES.length; index += 1) {
    if (names[index] !== REQUIRED_LIVE_MILESTONES[index]) {
      throw new Error("milestones are out of order");
    }
  }

  const shared = { rosterIds: null, hubVersion: null, titleDigest: null };
  let threadIdentity = null;
  let priorTime = -Infinity;
  for (const observation of observed) {
    requireObject(observation, "observation");
    if (containsPassedTrue(observation)) {
      throw new Error("passed:true is not evidence");
    }
    if (observation.concept !== EXPECTED_CONCEPT[observation.milestone]) {
      throw new Error("wrong concept");
    }
    if (observation.fixtureAbsent !== true) {
      throw new Error("fixture contamination detected");
    }
    if (THREAD_MILESTONES.has(observation.milestone)) {
      requireNonempty(observation.threadIdentity, "thread identity");
      if (threadIdentity === null) threadIdentity = observation.threadIdentity;
      if (threadIdentity !== observation.threadIdentity) {
        throw new Error("wrong thread identity");
      }
    }
    const source = requireObject(observation.source, "observation source");
    if (source.tool !== "idb" || source.commandStatus !== 0) {
      throw new Error("observation source must be successful IDB");
    }
    requireDigest(source.commandDigest, "source command digest");
    requireDigest(source.semanticTreeDigest, "source tree digest");
    if (
      typeof source.observedAtMonotonicMs !== "number" ||
      !Number.isFinite(source.observedAtMonotonicMs) ||
      source.observedAtMonotonicMs <= priorTime
    ) {
      throw new Error("observation times must be strictly increasing");
    }
    priorTime = source.observedAtMonotonicMs;
    requireObject(observation.action, "milestone action");
    const marker = requireObject(observation.positiveMarker, "positive marker");
    if (marker.markerDigest !== derivePositiveMarker(observation)) {
      throw new Error("positive marker is not derived from observation");
    }
    validateMilestoneEvidence(observation, shared);
  }
  return true;
}

export function parseCli(argv) {
  const known = new Map([
    ["--udid", "udid"],
    ["--bundle-id", "bundleId"],
    ["--output-dir", "outputDir"],
    ["--hub-version", "hubVersion"],
  ]);
  const result = {};
  if (argv.length % 2 !== 0) throw new SmokeError("invalid-cli");
  for (let index = 0; index < argv.length; index += 2) {
    const flag = argv[index];
    const value = argv[index + 1];
    if (!known.has(flag)) throw new SmokeError("unknown-cli-flag");
    const key = known.get(flag);
    if (Object.hasOwn(result, key)) throw new SmokeError("duplicate-cli-flag");
    if (typeof value !== "string" || value.trim() === "") {
      throw new SmokeError("invalid-cli-value");
    }
    result[key] = value;
  }
  for (const key of known.values()) {
    if (!Object.hasOwn(result, key)) throw new SmokeError("missing-cli-flag");
  }
  if (!path.isAbsolute(result.outputDir)) {
    throw new SmokeError("output-dir-must-be-absolute");
  }
  if (!SAFE_BUNDLE_ID.test(result.bundleId)) {
    throw new SmokeError("invalid-bundle-id");
  }
  if (!SAFE_VERSION.test(result.hubVersion)) {
    throw new SmokeError("invalid-hub-version");
  }
  return result;
}

export function readSmokeEnvironment(env) {
  const appPath = env?.EVENER_SMOKE_APP_PATH;
  const threadRef = env?.EVENER_SMOKE_THREAD_REF;
  if (
    typeof appPath !== "string" ||
    appPath.trim() === "" ||
    !path.isAbsolute(appPath)
  ) {
    throw new SmokeError("app prerequisite unavailable");
  }
  if (typeof threadRef !== "string" || threadRef.trim() === "") {
    throw new SmokeError("thread prerequisite unavailable");
  }
  return { appPath, threadRef };
}

export function parseAxDocument(input) {
  let document;
  try {
    document = typeof input === "string" ? JSON.parse(input) : input;
  } catch {
    throw new SmokeError("malformed complete AX document");
  }
  if (
    !isObject(document) ||
    document.format !== "complete" ||
    typeof document.backend !== "string" ||
    document.backend.trim() === "" ||
    !Array.isArray(document.elements)
  ) {
    throw new SmokeError("malformed complete AX document");
  }
  const nodes = [];
  const visit = (value) => {
    if (Array.isArray(value)) {
      for (const child of value) visit(child);
      return;
    }
    if (!isObject(value)) return;
    if (typeof value.AXLabel === "string" && value.AXLabel.trim() !== "") {
      const rawValue = value.AXValue;
      if (
        rawValue !== undefined &&
        rawValue !== null &&
        !["string", "number", "boolean"].includes(typeof rawValue)
      ) {
        throw new SmokeError("malformed-AX-node");
      }
      const role = value.role ?? value.AXRole ?? null;
      if (role !== null && typeof role !== "string") {
        throw new SmokeError("malformed-AX-node");
      }
      nodes.push({
        label: value.AXLabel,
        value: rawValue == null ? null : String(rawValue),
        role,
      });
    }
    for (const child of Object.values(value)) {
      if (child !== value.AXLabel && child !== value.AXValue) visit(child);
    }
  };
  visit(document.elements);
  if (nodes.length === 0) throw new SmokeError("missing AX node");
  return {
    format: "complete",
    backend: document.backend,
    nodes,
    raw: document,
  };
}

export function decodeDeviceDescription(stdout, expectedUdid) {
  let value;
  try {
    value = JSON.parse(stdout);
  } catch {
    throw new SmokeError("invalid device metadata");
  }
  const model = value?.model ?? value?.device?.model;
  if (
    !isObject(value) ||
    value.udid !== expectedUdid ||
    value.type !== "device" ||
    typeof model !== "string" ||
    !SAFE_DEVICE_MODEL.test(model) ||
    typeof value.os_version !== "string" ||
    !SAFE_VERSION.test(value.os_version)
  ) {
    throw new SmokeError("invalid device metadata");
  }
  return { model, os: value.os_version };
}

export function decodeInstalledApps(stdout) {
  const lines = stdout
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  if (lines.length === 0) throw new SmokeError("invalid app listing");
  return lines.map((line) => {
    let value;
    try {
      value = JSON.parse(line);
    } catch {
      throw new SmokeError("invalid app listing");
    }
    if (
      !isObject(value) ||
      typeof value.bundle_id !== "string" ||
      !SAFE_BUNDLE_ID.test(value.bundle_id) ||
      typeof value.name !== "string" ||
      value.name.trim() === "" ||
      !["Running", "Not running", "Unknown"].includes(value.process_state) ||
      !Number.isSafeInteger(value.pid) ||
      value.pid < 0
    ) {
      throw new SmokeError("invalid app listing");
    }
    return {
      bundleId: value.bundle_id,
      name: value.name,
      processState: value.process_state,
      pid: value.pid,
    };
  });
}

async function assertNoSymlinkComponents(target) {
  const absolute = path.resolve(target);
  const parsed = path.parse(absolute);
  let current = parsed.root;
  for (const component of absolute
    .slice(parsed.root.length)
    .split(path.sep)
    .filter(Boolean)) {
    current = path.join(current, component);
    try {
      const info = await lstat(current);
      if (info.isSymbolicLink()) throw new SmokeError("symbolic link refused");
    } catch (error) {
      if (error?.code === "ENOENT") return;
      throw error;
    }
  }
}

async function deterministicBundleHash(appPath) {
  const entries = [];
  const walk = async (directory, relativeDirectory = "") => {
    const names = await readdir(directory);
    names.sort();
    if (names.length === 0)
      entries.push({ type: "directory", path: relativeDirectory });
    for (const name of names) {
      const absolute = path.join(directory, name);
      const relative = path.posix.join(
        relativeDirectory.split(path.sep).join(path.posix.sep),
        name,
      );
      const info = await lstat(absolute);
      if (info.isSymbolicLink()) throw new SmokeError("symbolic link refused");
      if (info.isDirectory()) {
        entries.push({ type: "directory", path: relative });
        await walk(absolute, relative);
      } else if (info.isFile()) {
        entries.push({
          type: "file",
          path: relative,
          bytes: await readFile(absolute),
        });
      } else {
        throw new SmokeError("unsupported bundle entry");
      }
    }
  };
  await walk(appPath);
  const hash = createHash("sha256");
  for (const entry of entries) {
    const nameBytes = Buffer.from(entry.path, "utf8");
    const nameLength = Buffer.alloc(4);
    nameLength.writeUInt32BE(nameBytes.length);
    hash.update(entry.type === "file" ? Buffer.from([1]) : Buffer.from([0]));
    hash.update(nameLength);
    hash.update(nameBytes);
    if (entry.type === "file") {
      const size = Buffer.alloc(8);
      size.writeBigUInt64BE(BigInt(entry.bytes.length));
      hash.update(size);
      hash.update(entry.bytes);
    }
  }
  return `sha256:${hash.digest("hex")}`;
}

export async function inspectLocalAppBundle(appPath, { run }) {
  await assertNoSymlinkComponents(appPath);
  const appInfo = await lstat(appPath).catch(() => null);
  if (
    appInfo === null ||
    !appInfo.isDirectory() ||
    path.extname(appPath) !== ".app"
  ) {
    throw new SmokeError("invalid local app bundle");
  }
  const plistPath = path.join(appPath, "Info.plist");
  const plistInfo = await lstat(plistPath).catch(() => null);
  if (plistInfo === null || !plistInfo.isFile() || plistInfo.isSymbolicLink()) {
    throw new SmokeError("invalid local app bundle");
  }
  await run("codesign", ["--verify", "--strict", appPath]);
  const idResult = await run("plutil", [
    "-extract",
    "CFBundleIdentifier",
    "raw",
    "-o",
    "-",
    plistPath,
  ]);
  const versionResult = await run("plutil", [
    "-extract",
    "CFBundleShortVersionString",
    "raw",
    "-o",
    "-",
    plistPath,
  ]);
  const id = idResult.stdout.trim();
  const version = versionResult.stdout.trim();
  if (!SAFE_BUNDLE_ID.test(id) || !SAFE_VERSION.test(version)) {
    throw new SmokeError("invalid local app identity");
  }
  const hash = await deterministicBundleHash(appPath);
  const finalAppInfo = await lstat(appPath).catch(() => null);
  if (
    finalAppInfo === null ||
    !finalAppInfo.isDirectory() ||
    finalAppInfo.dev !== appInfo.dev ||
    finalAppInfo.ino !== appInfo.ino
  ) {
    throw new SmokeError("local app bundle changed");
  }
  return { id, version, hash, codesignVerified: true };
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
        child.kill("SIGKILL");
      } catch {
        // The child may already have exited.
      }
    }
    this.#children.clear();
  }
}

const DEFAULT_SCHEDULER = {
  schedule: (callback, milliseconds) => setTimeout(callback, milliseconds),
  cancel: (handle) => clearTimeout(handle),
};

export function runSpawned(program, argv, options = {}) {
  if (
    typeof program !== "string" ||
    !Array.isArray(argv) ||
    argv.some((entry) => typeof entry !== "string")
  ) {
    throw new SmokeError("invalid-command");
  }
  const spawnImpl = options.spawnImpl ?? spawn;
  const registry = options.registry ?? new ProcessRegistry();
  const now =
    options.now ?? (() => Number(process.hrtime.bigint() / 1_000_000n));
  const scheduler = options.scheduler ?? DEFAULT_SCHEDULER;
  const controller = new AbortController();
  const started = now();
  return new Promise((resolve, reject) => {
    let child;
    let settled = false;
    let timedOut = false;
    let stdout = "";
    let stderr = "";
    const finish = (error, value) => {
      if (settled) return;
      settled = true;
      scheduler.cancel(timer);
      if (child) registry.delete(child);
      if (error) reject(error);
      else resolve(value);
    };
    const onTripwire = () => {
      const remaining = TRIPWIRE_MS - (now() - started);
      if (remaining > 0) {
        timer = scheduler.schedule(onTripwire, remaining);
        return;
      }
      timedOut = true;
      controller.abort();
      try {
        child?.kill("SIGTERM");
        child?.kill("SIGKILL");
      } catch {
        finish(new SmokeError("command tripwire reached"));
      }
    };
    let timer = scheduler.schedule(onTripwire, TRIPWIRE_MS);
    try {
      child = spawnImpl(program, argv, {
        shell: false,
        stdio: ["ignore", "pipe", "pipe"],
        windowsHide: true,
        signal: controller.signal,
      });
    } catch {
      finish(new SmokeError("command start failed"));
      return;
    }
    registry.add(child);
    child.stdout?.setEncoding("utf8");
    child.stderr?.setEncoding("utf8");
    child.stdout?.on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr?.on("data", (chunk) => {
      stderr += chunk;
    });
    child.once("error", () => {
      if (!timedOut) finish(new SmokeError("command execution failed"));
    });
    child.once("close", (code, signal) => {
      if (timedOut) {
        finish(new SmokeError("command tripwire reached"));
        return;
      }
      if (code !== 0) {
        const error = new SmokeError("command failed");
        error.status = code;
        error.code =
          program === "idb" &&
          /(?:companion.{0,80}(?:lost|disconnect|connect|unavailable)|(?:lost|disconnect|connect|unavailable).{0,80}companion)/is.test(
            stderr,
          )
            ? "IDB_COMPANION_LOST"
            : "COMMAND_FAILED";
        finish(error);
        return;
      }
      finish(null, { status: 0, stdout, stderr, signal });
    });
  });
}

export async function pollSemanticTree({ readTree, accept, now }) {
  if (
    typeof readTree !== "function" ||
    typeof accept !== "function" ||
    typeof now !== "function"
  ) {
    throw new SmokeError("invalid semantic poll");
  }
  const started = now();
  for (;;) {
    if (now() - started >= TRIPWIRE_MS) {
      throw new SmokeError("semantic tripwire reached");
    }
    const tree = await readTree();
    if (!isObject(tree)) throw new SmokeError("malformed semantic tree");
    if (accept(tree)) return tree;
    if (now() - started >= TRIPWIRE_MS) {
      throw new SmokeError("semantic tripwire reached");
    }
  }
}

async function pathIdentity(target, kind) {
  const info = await lstat(target).catch(() => null);
  if (
    info === null ||
    info.isSymbolicLink() ||
    (kind === "directory" ? !info.isDirectory() : !info.isFile())
  ) {
    throw new SmokeError("evidence path changed");
  }
  return { dev: info.dev, ino: info.ino };
}

function sameIdentity(left, right) {
  return left.dev === right.dev && left.ino === right.ino;
}

export async function createEvidenceRoot(outputDir) {
  if (!path.isAbsolute(outputDir)) {
    throw new SmokeError("evidence output must be absolute");
  }
  await assertNoSymlinkComponents(path.dirname(outputDir));
  try {
    await mkdir(outputDir, { mode: 0o700 });
  } catch (error) {
    if (error?.code === "EEXIST") {
      throw new SmokeError("evidence output already exists");
    }
    throw new SmokeError("evidence root creation failed");
  }
  await chmod(outputDir, 0o700);
  await syncDirectory(path.dirname(outputDir));
  const scratchDir = path.join(outputDir, "sensitive-scratch");
  try {
    await mkdir(scratchDir, { mode: 0o700 });
  } catch {
    throw new SmokeError("evidence scratch creation failed");
  }
  await chmod(scratchDir, 0o700);
  await syncDirectory(outputDir);
  return {
    outputDir,
    scratchDir,
    outputIdentity: await pathIdentity(outputDir, "directory"),
    scratchIdentity: await pathIdentity(scratchDir, "directory"),
    rawPath: path.join(scratchDir, "raw-evidence.json"),
    summaryPath: path.join(outputDir, "live-smoke-summary.json"),
  };
}

async function assertEvidenceRoots(paths) {
  const output = await pathIdentity(paths.outputDir, "directory");
  const scratch = await pathIdentity(paths.scratchDir, "directory");
  if (
    !sameIdentity(output, paths.outputIdentity) ||
    !sameIdentity(scratch, paths.scratchIdentity)
  ) {
    throw new SmokeError("evidence path changed");
  }
}

async function syncDirectory(directory) {
  const handle = await open(directory, "r");
  try {
    await handle.sync();
  } finally {
    await handle.close();
  }
}

async function defaultWriteBytes(handle, bytes) {
  await handle.writeFile(bytes);
  await handle.sync();
}

async function publishNoReplace(
  directory,
  destination,
  bytes,
  writeBytes = defaultWriteBytes,
) {
  const temporary = path.join(
    directory,
    `.tmp-${randomBytes(16).toString("hex")}`,
  );
  let handle;
  try {
    handle = await open(temporary, "wx", 0o600);
    await writeBytes(handle, bytes);
    await handle.close();
    handle = null;
    await chmod(temporary, 0o600);
    try {
      await lstat(destination);
      throw new SmokeError("refusing to overwrite evidence");
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
    await link(temporary, destination);
    await unlink(temporary);
    await syncDirectory(directory);
  } catch (error) {
    if (handle !== null && handle !== undefined)
      await handle.close().catch(() => {});
    await unlink(temporary).catch(() => {});
    if (error instanceof SmokeError) throw error;
    throw new SmokeError("evidence write failed");
  }
}

function safeObservationSummary(observation) {
  return {
    milestone: observation.milestone,
    concept: observation.concept,
    observedAtMonotonicMs: observation.source.observedAtMonotonicMs,
    semanticTreeDigest: observation.source.semanticTreeDigest,
    markerDigest: observation.positiveMarker.markerDigest,
    evidenceDigest: digest(JSON.stringify(observation.evidence)),
  };
}

function safeVersionOrNull(value) {
  return typeof value === "string" && SAFE_VERSION.test(value) ? value : null;
}

function safeBundleOrNull(value) {
  return typeof value === "string" && SAFE_BUNDLE_ID.test(value) ? value : null;
}

function safeDeviceModelOrNull(value) {
  return typeof value === "string" && SAFE_DEVICE_MODEL.test(value)
    ? value
    : null;
}

function safeDigestOrNull(value) {
  return typeof value === "string" && SHA256.test(value) ? value : null;
}

function buildSafeSummary(input, linkDigest) {
  const status = ["passed", "failed", "blocked"].includes(input.status)
    ? input.status
    : "failed";
  return {
    schemaVersion: 2,
    status,
    linkDigest,
    tools: {
      idb: {
        versionDigest: safeDigestOrNull(input.tools?.idb?.versionDigest),
        capabilitiesDigest: safeDigestOrNull(
          input.tools?.idb?.capabilitiesDigest,
        ),
      },
    },
    device: {
      model: safeDeviceModelOrNull(input.device?.model),
      modelDigest: safeDigestOrNull(input.device?.modelDigest),
      os: safeVersionOrNull(input.device?.os),
    },
    app: {
      id: safeBundleOrNull(input.app?.id),
      version: safeVersionOrNull(input.app?.version),
      hash: safeDigestOrNull(input.app?.hash),
    },
    hub: {
      version: safeVersionOrNull(input.hub?.version),
      protocol: safeVersionOrNull(input.hub?.protocol),
      originDigest: safeDigestOrNull(input.hub?.originDigest),
    },
    observations: Array.isArray(input.observations)
      ? input.observations.map(safeObservationSummary)
      : [],
  };
}

export async function publishEvidence(paths, summaryInput, raw, options = {}) {
  await assertEvidenceRoots(paths);
  const rawBytes = Buffer.from(`${JSON.stringify(raw, null, 2)}\n`, "utf8");
  const linkDigest = digestBytes(rawBytes);
  const summary = buildSafeSummary(summaryInput, linkDigest);
  const summaryBytes = Buffer.from(
    `${JSON.stringify(summary, null, 2)}\n`,
    "utf8",
  );
  const writeBytes = options.writeBytes ?? defaultWriteBytes;
  try {
    await publishNoReplace(
      paths.scratchDir,
      paths.rawPath,
      rawBytes,
      writeBytes,
    );
    await assertEvidenceRoots(paths);
    await publishNoReplace(
      paths.outputDir,
      paths.summaryPath,
      summaryBytes,
      writeBytes,
    );
  } catch (error) {
    if (error instanceof SmokeError) throw error;
    throw new SmokeError("evidence write failed");
  }
  return { ...paths, summary, linkDigest };
}

export async function verifyIdbCapabilities({ run }) {
  const checks = [
    {
      argv: ["--version"],
      tokens: [],
      version: true,
    },
    {
      argv: ["ui", "describe-all", "--help"],
      tokens: ["--format", "complete", "--json", "--udid"],
    },
    {
      argv: ["ui", "tap", "--help"],
      tokens: ["target", "--match-key", "AXLabel", "--udid"],
    },
    { argv: ["ui", "text", "--help"], tokens: ["text", "--udid"] },
    { argv: ["ui", "button", "--help"], tokens: ["HOME", "--udid"] },
    {
      argv: ["launch", "--help"],
      tokens: ["--foreground-if-running", "--udid", "bundle_id"],
    },
    { argv: ["terminate", "--help"], tokens: ["--udid", "bundle_id"] },
    {
      argv: ["describe", "--help"],
      tokens: ["--diagnostics", "--json", "--udid"],
    },
    {
      argv: ["list-apps", "--help"],
      tokens: ["--fetch-process-state", "--json", "--udid"],
    },
  ];
  const outputs = [];
  let version = "";
  for (const check of checks) {
    const result = await run("idb", check.argv);
    if (typeof result.stdout !== "string" || result.stdout.trim() === "") {
      throw new SmokeError("IDB capability unavailable");
    }
    if (check.version) version = result.stdout.trim();
    for (const token of check.tokens) {
      if (!result.stdout.includes(token)) {
        throw new SmokeError("IDB capability unavailable");
      }
    }
    outputs.push(digest(result.stdout));
  }
  return {
    versionDigest: digest(version),
    capabilitiesDigest: digest(JSON.stringify(outputs)),
  };
}

function commandSource(snapshot) {
  return {
    tool: "idb",
    commandStatus: 0,
    commandDigest: snapshot.commandDigest,
    semanticTreeDigest: snapshot.treeDigest,
    observedAtMonotonicMs: snapshot.observedAtMonotonicMs,
  };
}

function makeObservation({
  milestone,
  snapshot,
  threadIdentity,
  evidence,
  action,
}) {
  const observation = {
    milestone,
    concept: EXPECTED_CONCEPT[milestone],
    threadIdentity,
    fixtureAbsent: true,
    source: commandSource(snapshot),
    action,
    evidence,
  };
  return {
    ...observation,
    positiveMarker: { markerDigest: derivePositiveMarker(observation) },
  };
}

function findNode(snapshot, predicate, label) {
  const matching = snapshot.nodes.filter(predicate);
  if (matching.length !== 1) throw new SmokeError(label);
  return matching[0];
}

function parseConceptNode(snapshot, expectedConcept, expectedSurface) {
  const node = findNode(
    snapshot,
    (candidate) => candidate.label.startsWith("Evener concept;"),
    "concept AX unavailable",
  );
  const match = node.label.match(
    /^Evener concept; concept (Stillwater|Constellation|Field Notes); surface (sessions|conversation|work)(?:; connection (connecting|connected|offline|error))?(.*)$/,
  );
  if (!match || match[1] !== expectedConcept || match[2] !== expectedSurface) {
    throw new SmokeError("wrong concept AX state");
  }
  const sessionMatch = match[4].match(/; session ([A-Za-z0-9._:-]+)/);
  return {
    node,
    concept: match[1],
    surface: match[2],
    status: match[3] ?? null,
    session: sessionMatch?.[1] ?? null,
  };
}

function parseConnection(snapshot) {
  const separate = snapshot.nodes.find((node) =>
    node.label.startsWith("Evener connection;"),
  );
  if (separate) {
    const match = separate.label.match(
      /^Evener connection; status (connected|connecting|offline|error); server ([^;]+); protocol ([^;]+); profile-generation (\d+); lifecycle-phase (active|inactive|background|foreground); lifecycle-generation (\d+); handshake-generation (\d+); app ([A-Za-z0-9.-]+)@([^;]+); origin (sha256:[a-f0-9]{64})$/,
    );
    if (!match) throw new SmokeError("connection AX unavailable");
    const connection = {
      status: match[1],
      serverVersion: match[2],
      protocolVersion: match[3],
      profileGeneration: Number(match[4]),
      lifecyclePhase: match[5],
      lifecycleGeneration: Number(match[6]),
      handshakeGeneration: Number(match[7]),
      bundleId: match[8],
      appVersion: match[9],
      originDigest: match[10],
    };
    if (
      !SAFE_VERSION.test(connection.serverVersion) ||
      !SAFE_VERSION.test(connection.protocolVersion) ||
      !SAFE_VERSION.test(connection.appVersion)
    ) {
      throw new SmokeError("connection AX unavailable");
    }
    return connection;
  }
  const concept = findNode(
    snapshot,
    (node) => node.label.startsWith("Evener concept;"),
    "connection AX unavailable",
  );
  const match = concept.label.match(
    /; connection (connected|connecting|offline|error); server ([^;]+); protocol ([^;]+); profile generation (\d+); lifecycle (active|inactive|background|foreground) (\d+); handshake (\d+); app ([A-Za-z0-9.-]+) ([^;]+); origin (sha256:[a-f0-9]{64})/,
  );
  if (!match) throw new SmokeError("connection AX unavailable");
  const connection = {
    status: match[1],
    serverVersion: match[2],
    protocolVersion: match[3],
    profileGeneration: Number(match[4]),
    lifecyclePhase: match[5],
    lifecycleGeneration: Number(match[6]),
    handshakeGeneration: Number(match[7]),
    bundleId: match[8],
    appVersion: match[9],
    originDigest: match[10],
  };
  if (
    !SAFE_VERSION.test(connection.serverVersion) ||
    !SAFE_VERSION.test(connection.protocolVersion) ||
    !SAFE_VERSION.test(connection.appVersion)
  ) {
    throw new SmokeError("connection AX unavailable");
  }
  return connection;
}

function parseRoster(snapshot, concept) {
  parseConceptNode(snapshot, concept, "sessions");
  const status = findNode(
    snapshot,
    (node) => node.label.startsWith("Evener roster;"),
    "roster AX unavailable",
  );
  const match = status.label.match(
    /^Evener roster; concept (Stillwater|Constellation|Field Notes); retained (\d+); has-more (true|false)$/,
  );
  if (!match || match[1] !== concept)
    throw new SmokeError("roster AX unavailable");
  const rows = snapshot.nodes
    .map((node) => {
      const row = node.label.match(
        /^Session ([A-Za-z0-9._:-]+); (.+); project (.+); status (attention|running|success|failed|idle|unknown)$/,
      );
      return row
        ? {
            id: row[1],
            title: row[2],
            project: row[3],
            status: row[4],
            label: node.label,
            role: node.role,
          }
        : null;
    })
    .filter(Boolean);
  if (
    rows.length === 0 ||
    rows.length !== Number(match[2]) ||
    rows.some((row) => !/Button/i.test(row.role ?? ""))
  ) {
    throw new SmokeError("roster row AX unavailable");
  }
  return {
    rows,
    rosterIds: rows.map((row) => row.id),
    retainedCount: Number(match[2]),
    hasMore: match[3] === "true",
  };
}

function parseConversation(snapshot, concept, expectedTitle = null) {
  const root = parseConceptNode(snapshot, concept, "conversation");
  if (root.session === null) throw new SmokeError("thread AX unavailable");
  const prefixedTitle = snapshot.nodes.find((node) =>
    node.label.startsWith("Conversation title "),
  );
  const exactTitle =
    expectedTitle === null
      ? null
      : snapshot.nodes.find(
          (node) =>
            node.label === expectedTitle && /Heading/i.test(node.role ?? ""),
        );
  const titleNode = prefixedTitle ?? exactTitle;
  if (!titleNode) throw new SmokeError("conversation title AX unavailable");
  const title = prefixedTitle
    ? prefixedTitle.label.slice("Conversation title ".length)
    : titleNode.label;
  const draft = findNode(
    snapshot,
    (node) => node.label === "Message",
    "draft AX unavailable",
  ).value;
  if (draft === null) throw new SmokeError("draft AX unavailable");
  return {
    threadId: root.session,
    title,
    titleDigest: digest(title),
    draft,
    transcript: parseTranscript(snapshot),
  };
}

function parseMutation(snapshot, kind, status) {
  const node = findNode(
    snapshot,
    (candidate) => candidate.label.startsWith("Evener mutation;"),
    "mutation AX unavailable",
  );
  const match = node.label.match(
    /^Evener mutation; kind (send|steer|queue|interrupt); status (pending|failed|accepted); receipt (none|\d+)$/,
  );
  if (!match || match[1] !== kind || match[2] !== status) {
    throw new SmokeError("mutation AX unavailable");
  }
  if (status === "accepted" && match[3] === "none") {
    throw new SmokeError("accepted receipt AX unavailable");
  }
  return {
    kind,
    status,
    receipt: match[3] === "none" ? null : Number(match[3]),
  };
}

function parseTranscript(snapshot) {
  return snapshot.nodes
    .map((node) => {
      const match = node.label.match(
        /^Evener transcript item; id ([A-Za-z0-9._:-]+); kind (user|assistant|reasoning|tool|question|failure|attachment); status (streaming|completed); label ([^;]*); content ([\s\S]+)$/,
      );
      return match
        ? {
            id: match[1],
            kind: match[2],
            status: match[3],
            label: match[4],
            content: match[5],
          }
        : null;
    })
    .filter(Boolean);
}

function parseWork(snapshot) {
  parseConceptNode(snapshot, "Field Notes", "work");
  const items = snapshot.nodes
    .map((node) => {
      const match = node.label.match(
        /^Evener work item; id ([A-Za-z0-9._:-]+); kind (task|delegate|job|watch); status (attention|running|success|failed|idle|unknown); title ([\s\S]+)$/,
      );
      return match
        ? { id: match[1], kind: match[2], digest: digest(node.label) }
        : null;
    })
    .filter(Boolean);
  const usage = findNode(
    snapshot,
    (node) => node.label.startsWith("Evener usage;"),
    "usage AX unavailable",
  );
  const byKind = (kind) => items.filter((item) => item.kind === kind);
  const result = {
    tasks: byKind("task"),
    jobs: byKind("job"),
    delegates: byKind("delegate"),
    usage: { digest: digest(usage.label) },
  };
  if (
    result.tasks.length === 0 ||
    result.jobs.length === 0 ||
    result.delegates.length === 0
  ) {
    throw new SmokeError("work AX unavailable");
  }
  return result;
}

function assertNoFixture(snapshot, final = false) {
  for (const node of snapshot.nodes) {
    if (
      CONTAMINATION.test(node.label) ||
      CONTAMINATION.test(node.value ?? "")
    ) {
      throw new SmokeError("fixture contamination detected");
    }
    if (final && FORBIDDEN_FINAL.test(node.label)) {
      throw new SmokeError("forbidden final surface detected");
    }
  }
}

function findInstalledApp(apps, bundleId, requireRunning = true) {
  const matches = apps.filter((app) => app.bundleId === bundleId);
  if (matches.length !== 1) throw new SmokeError("installed app unavailable");
  const app = matches[0];
  if (requireRunning && (app.processState !== "Running" || app.pid <= 0)) {
    throw new SmokeError("installed app process unavailable");
  }
  return app;
}

export async function launchProductionBundle(config, run, foreground = false) {
  try {
    await run("idb", [
      "launch",
      ...(foreground ? ["--foreground-if-running"] : []),
      "--udid",
      config.udid,
      config.bundleId,
    ]);
    return "idb";
  } catch (error) {
    if (error?.code !== "IDB_COMPANION_LOST") throw error;
    await run("xcrun", [
      "devicectl",
      "device",
      "process",
      "launch",
      "--device",
      config.udid,
      config.bundleId,
    ]);
    return "devicectl";
  }
}

function exactExpected(kind) {
  const source = EXPECTED_LIVE_SEQUENCE[kind];
  return {
    classification: EXPECTED_LIVE_SEQUENCE.classification,
    requests: [...source.requests],
    notifications: [...source.notifications],
  };
}

export async function runLiveSmoke(config, dependencies = {}) {
  const environment = readSmokeEnvironment(dependencies.env ?? process.env);
  const paths = await createEvidenceRoot(config.outputDir);
  const registry = dependencies.registry ?? new ProcessRegistry();
  const now =
    dependencies.now ?? (() => Number(process.hrtime.bigint() / 1_000_000n));
  const scheduler = dependencies.scheduler ?? DEFAULT_SCHEDULER;
  const baseRun =
    dependencies.run ??
    ((program, argv) =>
      runSpawned(program, argv, { registry, now, scheduler }));
  const observed = [];
  const raw = {
    schemaVersion: 2,
    operational: { threadRef: environment.threadRef },
    commands: [],
    semanticTrees: [],
    observations: observed,
  };
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
      raw.commands.push({ program, argv, status: "failed" });
      throw error;
    }
  };
  const describeArgv = [
    "ui",
    "describe-all",
    "--format",
    "complete",
    "--json",
    "--udid",
    config.udid,
  ];
  const readTree = async () => {
    const result = await run("idb", describeArgv);
    const document = parseAxDocument(result.stdout);
    const encoded = Buffer.from(JSON.stringify(document.raw), "utf8");
    const snapshot = {
      ...document,
      treeDigest: digestBytes(encoded),
      commandDigest: digest(JSON.stringify(["idb", ...describeArgv])),
      observedAtMonotonicMs: now(),
    };
    assertNoFixture(snapshot);
    raw.semanticTrees.push(document.raw);
    return snapshot;
  };
  const waitFor = (accept) => pollSemanticTree({ readTree, accept, now });
  const tap = async (label, snapshot, requirePresent = true) => {
    if (requirePresent) {
      const node = findNode(
        snapshot,
        (candidate) => candidate.label === label,
        "action AX unavailable",
      );
      const expectedRole =
        label === "Message" ? /TextArea|TextField/i : /Button/i;
      if (!expectedRole.test(node.role ?? "")) {
        throw new SmokeError("action AX unavailable");
      }
    }
    await run("idb", [
      "ui",
      "tap",
      label,
      "--match-key",
      "AXLabel",
      "--udid",
      config.udid,
    ]);
  };
  const typeText = async (value) => {
    await run("idb", ["ui", "text", value, "--udid", config.udid]);
  };
  const readApps = async () => {
    const result = await run("idb", [
      "list-apps",
      "--fetch-process-state",
      "--json",
      "--udid",
      config.udid,
    ]);
    return decodeInstalledApps(result.stdout);
  };
  const sentinels = dependencies.sentinels ?? {
    send: `smoke-send-${randomBytes(8).toString("hex")}`,
    steer: `smoke-steer-${randomBytes(8).toString("hex")}`,
    queue: `smoke-queue-${randomBytes(8).toString("hex")}`,
    preserve: `smoke-draft-${randomBytes(8).toString("hex")}`,
  };
  let safeSummaryInput = {
    status: "failed",
    tools: {},
    device: {},
    app: {},
    hub: {},
    observations: [],
  };

  const observeMutation = async ({
    kind,
    concept,
    current,
    text,
    lifecycle = false,
  }) => {
    const snapshot = current;
    if (kind !== "interrupt") {
      await tap("Message", snapshot);
      await typeText(text);
      await tap(`Use ${kind} mode`, snapshot);
      await tap("Submit message", snapshot);
    } else {
      await tap("Interrupt", snapshot);
    }
    const pending = await waitFor((candidate) => {
      try {
        parseMutation(candidate, kind, "pending");
        parseConceptNode(candidate, concept, "conversation");
        return true;
      } catch {
        return false;
      }
    });
    let streaming = null;
    if (lifecycle) {
      streaming = await waitFor((candidate) => {
        const items = parseTranscript(candidate).filter(
          (item) => item.status === "streaming",
        );
        return items.length > 0;
      });
    }
    const accepted = await waitFor((candidate) => {
      try {
        parseMutation(candidate, kind, "accepted");
        return true;
      } catch {
        return false;
      }
    });
    const acceptedValue = parseMutation(accepted, kind, "accepted");
    let completed = accepted;
    if (lifecycle) {
      const streamingIds = new Set(
        parseTranscript(streaming)
          .filter((item) => item.status === "streaming")
          .map((item) => item.id),
      );
      const hasCompletedEvidence = (candidate) => {
        const items = parseTranscript(candidate);
        return (
          items.some(
            (item) => streamingIds.has(item.id) && item.status === "completed",
          ) &&
          items.some(
            (item) =>
              item.kind === "reasoning" &&
              item.status === "completed" &&
              item.content.trim() !== "",
          ) &&
          items.some(
            (item) =>
              item.kind === "tool" &&
              item.status === "completed" &&
              item.content.trim() !== "",
          )
        );
      };
      if (!hasCompletedEvidence(accepted)) {
        completed = await waitFor(hasCompletedEvidence);
      }
    }
    return {
      current: completed,
      receipt: {
        kind,
        status: "accepted",
        receipt: acceptedValue.receipt,
        pendingTreeDigest: pending.treeDigest,
        acceptedTreeDigest: accepted.treeDigest,
      },
      pending,
      streaming,
      accepted,
      completed,
    };
  };

  try {
    const tools = await verifyIdbCapabilities({ run });
    const localBundle = await inspectLocalAppBundle(environment.appPath, {
      run,
    });
    if (localBundle.id !== config.bundleId) {
      throw new SmokeError("local bundle ID mismatch");
    }
    const deviceResult = await run("idb", [
      "describe",
      "--diagnostics",
      "--json",
      "--udid",
      config.udid,
    ]);
    const device = decodeDeviceDescription(deviceResult.stdout, config.udid);
    findInstalledApp(await readApps(), config.bundleId, false);
    await launchProductionBundle(config, run);
    const initialApp = findInstalledApp(await readApps(), config.bundleId);

    const profile = await waitFor((snapshot) => {
      try {
        const connection = parseConnection(snapshot);
        return connection.status === "connected";
      } catch {
        return false;
      }
    });
    const profileConnection = parseConnection(profile);
    if (
      profileConnection.serverVersion !== config.hubVersion ||
      profileConnection.protocolVersion.trim() === ""
    ) {
      throw new SmokeError("stale Hub is blocked", "blocked");
    }
    if (
      profileConnection.bundleId !== config.bundleId ||
      profileConnection.appVersion !== localBundle.version
    ) {
      throw new SmokeError("installed app identity mismatch");
    }
    const installedApp = {
      id: initialApp.bundleId,
      version: profileConnection.appVersion,
      pid: initialApp.pid,
    };
    const profileEvidence = {
      device,
      installedApp,
      localBundle,
      hub: {
        expectedVersion: config.hubVersion,
        observedVersion: profileConnection.serverVersion,
        protocolVersion: profileConnection.protocolVersion,
        originDigest: profileConnection.originDigest,
      },
      connection: {
        status: profileConnection.status,
        profileGeneration: profileConnection.profileGeneration,
        lifecycleGeneration: profileConnection.lifecycleGeneration,
        handshakeGeneration: profileConnection.handshakeGeneration,
      },
    };
    observed.push(
      makeObservation({
        milestone: "profile-connected",
        snapshot: profile,
        threadIdentity: null,
        evidence: profileEvidence,
        action: { kind: "observe", label: "connected production app" },
      }),
    );

    const stillwaterRosterSnapshot = await waitFor((snapshot) => {
      try {
        parseRoster(snapshot, "Stillwater");
        return true;
      } catch {
        return false;
      }
    });
    const stillwaterRoster = parseRoster(
      stillwaterRosterSnapshot,
      "Stillwater",
    );
    observed.push(
      makeObservation({
        milestone: "stillwater-roster",
        snapshot: stillwaterRosterSnapshot,
        threadIdentity: null,
        evidence: {
          rosterIds: stillwaterRoster.rosterIds,
          retainedCount: stillwaterRoster.retainedCount,
          hasMore: stillwaterRoster.hasMore,
          completeness: stillwaterRoster.hasMore
            ? "more-available"
            : "complete",
          listLimit: 501,
          expectedSequence: exactExpected("roster"),
        },
        action: { kind: "observe", label: "Stillwater roster" },
      }),
    );
    const selectedRow = stillwaterRoster.rows[0];
    await tap(selectedRow.label, stillwaterRosterSnapshot);
    let current = await waitFor((snapshot) => {
      try {
        parseConversation(snapshot, "Stillwater", selectedRow.title);
        return true;
      } catch {
        return false;
      }
    });
    const initialConversation = parseConversation(
      current,
      "Stillwater",
      selectedRow.title,
    );
    if (initialConversation.transcript.length === 0) {
      throw new SmokeError("transcript AX unavailable");
    }
    const threadIdentity = initialConversation.threadId;
    observed.push(
      makeObservation({
        milestone: "stillwater-conversation",
        snapshot: current,
        threadIdentity,
        evidence: {
          activeThreadId: threadIdentity,
          titleDigest: initialConversation.titleDigest,
          transcriptIds: initialConversation.transcript.map((item) => item.id),
          readLimit: 50,
          expectedSequence: exactExpected("conversation"),
        },
        action: { kind: "semantic", label: digest(selectedRow.label) },
      }),
    );

    const send = await observeMutation({
      kind: "send",
      concept: "Stillwater",
      current,
      text: sentinels.send,
      lifecycle: true,
    });
    current = send.current;
    const streamingItems = parseTranscript(send.streaming).filter(
      (item) => item.status === "streaming",
    );
    const completedItems = parseTranscript(send.completed);
    const lifecycleItem = streamingItems.find((item) =>
      completedItems.some(
        (completed) =>
          completed.id === item.id && completed.status === "completed",
      ),
    );
    if (!lifecycleItem) throw new SmokeError("item lifecycle AX unavailable");
    const reasoning = completedItems.find(
      (item) => item.kind === "reasoning" && item.content.trim() !== "",
    );
    const tool = completedItems.find(
      (item) => item.kind === "tool" && item.content.trim() !== "",
    );
    if (!reasoning || !tool) {
      throw new SmokeError("reasoning or tool AX unavailable");
    }
    observed.push(
      makeObservation({
        milestone: "stillwater-send",
        snapshot: send.completed,
        threadIdentity,
        evidence: {
          receipt: send.receipt,
          lifecycle: {
            itemId: lifecycleItem.id,
            streamingTreeDigest: send.streaming.treeDigest,
            completedTreeDigest: send.completed.treeDigest,
          },
          reasoning: {
            itemId: reasoning.id,
            contentDigest: digest(reasoning.content),
          },
          tool: { itemId: tool.id, contentDigest: digest(tool.content) },
          expectedSequence: exactExpected("send"),
        },
        action: { kind: "semantic", label: "send" },
      }),
    );

    const switchConcept = async (from, to, milestone) => {
      await tap("Message", current);
      await typeText(sentinels.preserve);
      const before = await waitFor((snapshot) => {
        try {
          return (
            parseConversation(snapshot, from, selectedRow.title).draft ===
            sentinels.preserve
          );
        } catch {
          return false;
        }
      });
      await tap("Switch concept", before);
      await tap(`Switch to ${to}`, before, false);
      const after = await waitFor((snapshot) => {
        try {
          const conversation = parseConversation(
            snapshot,
            to,
            selectedRow.title,
          );
          return (
            conversation.threadId === threadIdentity &&
            conversation.draft === sentinels.preserve
          );
        } catch {
          return false;
        }
      });
      await tap("Back", after);
      const rosterSnapshot = await waitFor((snapshot) => {
        try {
          parseRoster(snapshot, to);
          return true;
        } catch {
          return false;
        }
      });
      const roster = parseRoster(rosterSnapshot, to);
      const row = roster.rows.find(
        (candidate) => candidate.id === selectedRow.id,
      );
      if (!row) throw new SmokeError("destination roster missing session");
      await tap(row.label, rosterSnapshot);
      const reopened = await waitFor((snapshot) => {
        try {
          return (
            parseConversation(snapshot, to, selectedRow.title).threadId ===
            threadIdentity
          );
        } catch {
          return false;
        }
      });
      observed.push(
        makeObservation({
          milestone,
          snapshot: after,
          threadIdentity,
          evidence: {
            rosterIds: roster.rosterIds,
            activeThreadId: threadIdentity,
            draftBefore: sentinels.preserve,
            draftAfter: parseConversation(after, to, selectedRow.title).draft,
            beforeTreeDigest: before.treeDigest,
            afterTreeDigest: after.treeDigest,
            rosterTreeDigest: rosterSnapshot.treeDigest,
          },
          action: { kind: "semantic", label: `switch to ${to}` },
        }),
      );
      current = reopened;
      return { roster, rosterSnapshot, reopened };
    };

    await switchConcept(
      "Stillwater",
      "Constellation",
      "constellation-preserved",
    );
    const steer = await observeMutation({
      kind: "steer",
      concept: "Constellation",
      current,
      text: sentinels.steer,
    });
    current = steer.current;
    const queue = await observeMutation({
      kind: "queue",
      concept: "Constellation",
      current,
      text: sentinels.queue,
    });
    current = queue.current;
    observed.push(
      makeObservation({
        milestone: "constellation-steer-queue",
        snapshot: queue.accepted,
        threadIdentity,
        evidence: {
          receipts: [steer.receipt, queue.receipt],
          expectedSequence: exactExpected("steerQueue"),
        },
        action: { kind: "semantic", label: "steer then queue" },
      }),
    );

    await switchConcept(
      "Constellation",
      "Field Notes",
      "field-notes-preserved",
    );
    const interrupted = await observeMutation({
      kind: "interrupt",
      concept: "Field Notes",
      current,
    });
    current = interrupted.current;
    observed.push(
      makeObservation({
        milestone: "field-notes-interrupt",
        snapshot: interrupted.accepted,
        threadIdentity,
        evidence: {
          receipt: interrupted.receipt,
          expectedSequence: exactExpected("interrupt"),
        },
        action: { kind: "semantic", label: "interrupt" },
      }),
    );

    await tap("Message", current);
    await typeText(sentinels.preserve);
    const beforeWork = await waitFor((snapshot) => {
      try {
        return (
          parseConversation(snapshot, "Field Notes", selectedRow.title)
            .draft === sentinels.preserve
        );
      } catch {
        return false;
      }
    });
    await tap("Work", beforeWork);
    const workSnapshot = await waitFor((snapshot) => {
      try {
        parseWork(snapshot);
        return true;
      } catch {
        return false;
      }
    });
    const workEvidence = parseWork(workSnapshot);
    observed.push(
      makeObservation({
        milestone: "work-activity-usage",
        snapshot: workSnapshot,
        threadIdentity,
        evidence: workEvidence,
        action: { kind: "semantic", label: "Work" },
      }),
    );
    await tap("Close", workSnapshot);
    const beforeBackground = await waitFor((snapshot) => {
      try {
        const conversation = parseConversation(
          snapshot,
          "Field Notes",
          selectedRow.title,
        );
        return (
          conversation.threadId === threadIdentity &&
          conversation.draft === sentinels.preserve
        );
      } catch {
        return false;
      }
    });
    const preBackgroundConnection = parseConnection(beforeBackground);
    const pidBeforeBackground = findInstalledApp(
      await readApps(),
      config.bundleId,
    ).pid;
    await run("idb", ["ui", "button", "HOME", "--udid", config.udid]);
    const background = await waitFor(
      (snapshot) =>
        snapshot.nodes.some((node) => node.label === "SpringBoard") &&
        !snapshot.nodes.some((node) =>
          node.label.startsWith("Evener concept;"),
        ),
    );
    await launchProductionBundle(config, run, true);
    const pidAfterBackground = findInstalledApp(
      await readApps(),
      config.bundleId,
    ).pid;
    const foreground = await waitFor((snapshot) => {
      try {
        const connection = parseConnection(snapshot);
        const conversation = parseConversation(
          snapshot,
          "Field Notes",
          selectedRow.title,
        );
        return (
          connection.lifecyclePhase === "foreground" &&
          connection.lifecycleGeneration >
            preBackgroundConnection.lifecycleGeneration &&
          conversation.threadId === threadIdentity &&
          conversation.draft === sentinels.preserve
        );
      } catch {
        return false;
      }
    });
    const foregroundConnection = parseConnection(foreground);
    observed.push(
      makeObservation({
        milestone: "background-foreground",
        snapshot: foreground,
        threadIdentity,
        evidence: {
          processIdBefore: pidBeforeBackground,
          processIdAfter: pidAfterBackground,
          lifecycleGenerationBefore:
            preBackgroundConnection.lifecycleGeneration,
          lifecycleGenerationAfter: foregroundConnection.lifecycleGeneration,
          profileGenerationBefore: preBackgroundConnection.profileGeneration,
          profileGenerationAfter: foregroundConnection.profileGeneration,
          handshakeGenerationBefore:
            preBackgroundConnection.handshakeGeneration,
          handshakeGenerationAfter: foregroundConnection.handshakeGeneration,
          activeThreadId: threadIdentity,
          draftBefore: sentinels.preserve,
          draftAfter: parseConversation(
            foreground,
            "Field Notes",
            selectedRow.title,
          ).draft,
          backgroundTreeDigest: background.treeDigest,
          foregroundTreeDigest: foreground.treeDigest,
        },
        action: { kind: "lifecycle", label: "HOME then foreground" },
      }),
    );
    current = foreground;

    const beforeReconnectConversation = parseConversation(
      current,
      "Field Notes",
      selectedRow.title,
    );
    const beforeReconnectTranscriptDigest = digest(
      JSON.stringify(beforeReconnectConversation.transcript),
    );
    const pidBeforeReconnect = findInstalledApp(
      await readApps(),
      config.bundleId,
    ).pid;
    await run("idb", ["terminate", "--udid", config.udid, config.bundleId]);
    await launchProductionBundle(config, run);
    const pidAfterReconnect = findInstalledApp(
      await readApps(),
      config.bundleId,
    ).pid;
    const reconnectRosterSnapshot = await waitFor((snapshot) => {
      try {
        const connection = parseConnection(snapshot);
        parseRoster(snapshot, "Field Notes");
        return (
          connection.status === "connected" &&
          connection.serverVersion === config.hubVersion
        );
      } catch {
        return false;
      }
    });
    const reconnectRoster = parseRoster(reconnectRosterSnapshot, "Field Notes");
    const reconnectRow = reconnectRoster.rows.find(
      (candidate) => candidate.id === selectedRow.id,
    );
    if (!reconnectRow) throw new SmokeError("reconnect session missing");
    await tap(reconnectRow.label, reconnectRosterSnapshot);
    const reopened = await waitFor((snapshot) => {
      try {
        const conversation = parseConversation(
          snapshot,
          "Field Notes",
          selectedRow.title,
        );
        return (
          conversation.threadId === threadIdentity &&
          conversation.titleDigest === initialConversation.titleDigest
        );
      } catch {
        return false;
      }
    });
    const reopenedConversation = parseConversation(
      reopened,
      "Field Notes",
      selectedRow.title,
    );
    const reopenedTranscriptDigest = digest(
      JSON.stringify(reopenedConversation.transcript),
    );
    if (reopenedTranscriptDigest !== beforeReconnectTranscriptDigest) {
      throw new SmokeError("reconnect transcript changed");
    }
    observed.push(
      makeObservation({
        milestone: "reconnect",
        snapshot: reopened,
        threadIdentity,
        evidence: {
          processIdBefore: pidBeforeReconnect,
          processIdAfter: pidAfterReconnect,
          reopened: true,
          activeThreadId: threadIdentity,
          titleDigest: reopenedConversation.titleDigest,
          transcriptDigest: reopenedTranscriptDigest,
          connectionTreeDigest: reconnectRosterSnapshot.treeDigest,
        },
        action: { kind: "lifecycle", label: "terminate launch reopen" },
      }),
    );

    const absence = await waitFor((snapshot) => {
      assertNoFixture(snapshot, true);
      return true;
    });
    observed.push(
      makeObservation({
        milestone: "fixture-absence",
        snapshot: absence,
        threadIdentity: null,
        evidence: {
          fixtureAbsent: true,
          searchAbsent: true,
          labAbsent: true,
        },
        action: { kind: "observe", label: "final AX absence" },
      }),
    );

    assertCompleteLiveSmoke(observed);
    safeSummaryInput = {
      status: "passed",
      tools: { idb: tools },
      device: {
        model: device.model,
        modelDigest: digest(device.model),
        os: device.os,
      },
      app: {
        id: localBundle.id,
        version: localBundle.version,
        hash: localBundle.hash,
      },
      hub: {
        version: profileConnection.serverVersion,
        protocol: profileConnection.protocolVersion,
        originDigest: profileConnection.originDigest,
      },
      observations: observed,
    };
    const published = await publishEvidence(paths, safeSummaryInput, raw);
    return { summary: published.summary, paths: published };
  } catch (cause) {
    const status = cause?.smokeStatus === "blocked" ? "blocked" : "failed";
    safeSummaryInput = { ...safeSummaryInput, status, observations: [] };
    try {
      const published = await publishEvidence(paths, safeSummaryInput, raw);
      const error =
        cause instanceof SmokeError
          ? cause
          : new SmokeError("live workflow failed", status);
      error.paths = published;
      throw error;
    } catch (publishError) {
      if (publishError === cause || publishError?.paths) throw publishError;
      throw new SmokeError("evidence write failed", status);
    }
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
      `${JSON.stringify({ status: result.summary.status, summary: "live-smoke-summary.json" })}\n`,
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
  main(process.argv.slice(2)).catch(() => {
    process.stderr.write("live smoke failed\n");
    process.exitCode = 1;
  });
}
