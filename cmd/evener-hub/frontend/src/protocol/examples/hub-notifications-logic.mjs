import assert from "node:assert/strict";
import { observeNotifications } from "./bounded-notifications.mjs";

export const METHODS = [
  "evener/auth/updated",
  "evener/attention/changed",
  "evener/marketplace/updated",
  "evener/plugin/updated",
  "evener/sandbox/escalation/requested",
  "evener/sandbox/escalation/resolved",
  "evener/settings/transcriptDisplay/changed",
  "evener/settings/keybindings/changed",
];
const rec = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const text = (v) => typeof v === "string";
const nonempty = (v) => text(v) && v.trim() !== "";
const integer = (v) => Number.isSafeInteger(v) && v >= 0;
const empty = (v) => rec(v) && Object.keys(v).length === 0;

function decodeAuth(params) {
  assert.ok(rec(params), "Invalid auth notification.");
  if (params.provider !== undefined) assert.ok(nonempty(params.provider), "Invalid auth provider.");
  if (params.activeSource !== undefined) assert.ok(text(params.activeSource), "Invalid auth source.");
  return { provider: params.provider ?? null, activeSource: params.activeSource ?? null };
}
function decodeAttention(params) {
  assert.ok(rec(params) && Array.isArray(params.changed) && rec(params.summary), "Invalid attention notification.");
  for (const item of params.changed) {
    assert.ok(
      rec(item) &&
        nonempty(item.threadId) &&
        text(item.title) &&
        text(item.project) &&
        text(item.level) &&
        text(item.prevLevel),
      "Invalid attention change.",
    );
    if (item.askPending !== undefined) assert.equal(typeof item.askPending, "boolean");
  }
  for (const key of ["needsYou", "error", "working"])
    assert.ok(integer(params.summary[key]), "Invalid attention summary.");
  return {
    changed: params.changed.map((item) => ({
      threadId: item.threadId,
      title: item.title,
      project: item.project,
      level: item.level,
      prevLevel: item.prevLevel,
      ...(item.askPending === undefined ? {} : { askPending: item.askPending }),
    })),
    summary: { ...params.summary },
  };
}
function decodeSandbox(params, ref) {
  if (rec(params) && params.ref !== ref) return null;
  assert.ok(
    rec(params) && nonempty(params.threadId) && params.ref === ref && nonempty(params.escalationId),
    "Invalid sandbox notification identity.",
  );
  return { threadId: params.threadId, ref, escalationId: params.escalationId };
}
function decodeSandboxRequest(params, ref) {
  if (rec(params) && params.ref !== ref) return null;
  const identity = decodeSandbox(params, ref);
  if (identity === null) return null;
  assert.ok(
    text(params.mode) && text(params.tool) && text(params.kind) && text(params.deniedPath),
    "Invalid sandbox request.",
  );
  if (params.command !== undefined) assert.equal(typeof params.command, "string", "Invalid sandbox command.");
  if (params.outputSoFar !== undefined) assert.equal(typeof params.outputSoFar, "string", "Invalid sandbox output.");
  if (params.partiallyRan !== undefined)
    assert.equal(typeof params.partiallyRan, "boolean", "Invalid sandbox execution flag.");
  return {
    ...identity,
    mode: params.mode,
    tool: params.tool,
    kind: params.kind,
    deniedPath: params.deniedPath,
    ...(params.command === undefined ? {} : { command: params.command }),
    ...(params.outputSoFar === undefined ? {} : { outputSoFar: params.outputSoFar }),
    ...(params.partiallyRan === undefined ? {} : { partiallyRan: params.partiallyRan }),
  };
}
function validateAuthProvider(value) {
  assert.ok(
    rec(value) &&
      text(value.provider) &&
      typeof value.supported === "boolean" &&
      typeof value.signedIn === "boolean" &&
      text(value.activeSource) &&
      typeof value.hasStoredOAuth === "boolean",
    "Invalid auth provider snapshot.",
  );
}
function validateMarketplace(value) {
  assert.ok(
    rec(value) &&
      text(value.name) &&
      rec(value.source) &&
      text(value.source.kind) &&
      Number.isFinite(value.lastUpdated),
    "Invalid marketplace snapshot.",
  );
}
function validatePlugin(value) {
  assert.ok(
    rec(value) &&
      text(value.plugin) &&
      text(value.marketplace) &&
      text(value.version) &&
      typeof value.enabled === "boolean" &&
      typeof value.autoUpgrade === "boolean" &&
      typeof value.broken === "boolean" &&
      text(value.installPath) &&
      Number.isFinite(value.installedAt) &&
      Number.isFinite(value.lastUpdated),
    "Invalid plugin snapshot.",
  );
}
function validateKeybindingRules(rules) {
  assert.ok(
    Array.isArray(rules) &&
      rules.every((rule) => rec(rule) && text(rule.action) && (rule.chord === null || text(rule.chord))),
    "Invalid keybindings rules.",
  );
}
function validateTranscriptConfig(config) {
  assert.ok(
    rec(config) &&
      Number.isSafeInteger(config.version) &&
      rec(config.content) &&
      text(config.content.kind) &&
      rec(config.advanced),
    "Invalid transcript display config.",
  );
  for (const key of ["roundTimings", "tokenCounts", "estimatedCost", "systemEvents", "promptEvents"])
    assert.equal(typeof config.advanced[key], "boolean", `Invalid transcript ${key}.`);
  assert.ok(text(config.advanced.hookExits), "Invalid transcript hook exits.");
}
export function decodeNotification(notification, { ref } = {}) {
  if (!rec(notification) || !METHODS.includes(notification.method)) return null;
  const params = notification.params;
  switch (notification.method) {
    case "evener/auth/updated":
      return { method: notification.method, ...decodeAuth(params) };
    case "evener/attention/changed":
      return { method: notification.method, ...decodeAttention(params) };
    case "evener/marketplace/updated":
    case "evener/plugin/updated":
      assert.ok(empty(params), "Expected empty notification parameters.");
      return { method: notification.method };
    case "evener/sandbox/escalation/requested": {
      const event = decodeSandboxRequest(params, ref);
      return event === null ? null : { method: notification.method, ...event };
    }
    case "evener/sandbox/escalation/resolved": {
      const event = decodeSandbox(params, ref);
      return event === null ? null : { method: notification.method, ...event };
    }
    case "evener/settings/transcriptDisplay/changed":
      assert.ok(
        rec(params) && text(params.layout) && integer(params.revision),
        "Invalid transcript display notification.",
      );
      validateTranscriptConfig(params.config);
      return {
        method: notification.method,
        layout: params.layout,
        revision: params.revision,
        config: structuredClone(params.config),
      };
    case "evener/settings/keybindings/changed":
      assert.ok(
        rec(params) && integer(params.version) && integer(params.revision),
        "Invalid keybindings notification.",
      );
      validateKeybindingRules(params.rules);
      return {
        method: notification.method,
        version: params.version,
        revision: params.revision,
        rules: structuredClone(params.rules),
        ...(params.loadError === undefined ? {} : { loadError: params.loadError }),
      };
    default:
      return null;
  }
}

function readNavigation(hub) {
  return hub.request("evener/navigation/read", { representationVersion: 2, resource: "manifest" }).then((value) => {
    assert.equal(value?.status, "ok", "Invalid navigation snapshot.");
    assert.equal(value.representation, "snapshot", "Navigation read was not a snapshot.");
    assert.ok(rec(value.data) && Array.isArray(value.data.entities), "Invalid navigation snapshot data.");
    return structuredClone(value);
  });
}
export async function readSnapshot(hub, { methods = METHODS, ref } = {}) {
  const result = {};
  const has = (method) => methods.includes(method);
  if (has("evener/auth/updated")) {
    const value = await hub.request("evener/auth/list", {});
    assert.ok(Array.isArray(value?.providers), "Invalid auth list snapshot.");
    value.providers.forEach(validateAuthProvider);
    result.auth = structuredClone(value);
  }
  if (has("evener/marketplace/updated")) {
    const value = await hub.request("evener/marketplace/list", {});
    assert.ok(Array.isArray(value?.marketplaces), "Invalid marketplace list snapshot.");
    value.marketplaces.forEach(validateMarketplace);
    result.marketplaces = structuredClone(value);
  }
  if (has("evener/plugin/updated")) {
    const value = await hub.request("evener/plugin/list", {});
    assert.ok(Array.isArray(value?.plugins), "Invalid plugin list snapshot.");
    value.plugins.forEach(validatePlugin);
    result.plugins = structuredClone(value);
  }
  if (has("evener/settings/transcriptDisplay/changed")) {
    const value = await hub.request("evener/settings/transcriptDisplay/get", {});
    assert.ok(rec(value) && rec(value.desktop) && rec(value.mobile), "Invalid transcript display snapshot.");
    for (const layout of ["desktop", "mobile"]) {
      assert.ok(integer(value[layout].revision), "Invalid transcript display revision.");
      validateTranscriptConfig(value[layout].config);
    }
    result.transcriptDisplay = structuredClone(value);
  }
  if (has("evener/settings/keybindings/changed")) {
    const value = await hub.request("evener/settings/keybindings/get", {});
    assert.ok(
      rec(value) && integer(value.version) && integer(value.revision) && Array.isArray(value.rules),
      "Invalid keybindings snapshot.",
    );
    validateKeybindingRules(value.rules);
    result.keybindings = structuredClone(value);
  }
  if (has("evener/attention/changed")) result.attention = await readNavigation(hub);
  if (has("evener/sandbox/escalation/requested") || has("evener/sandbox/escalation/resolved")) {
    assert.ok(nonempty(ref), "EVENER_THREAD_REF is required for sandbox notifications.");
    const value = await hub.request("thread/read", { ref, includeTurns: false, subscribe: true });
    assert.ok(rec(value?.thread) && value.thread.evener?.ref === ref, "Invalid sandbox thread snapshot.");
    assert.ok(
      value.thread.evener.pendingEscalations === undefined || Array.isArray(value.thread.evener.pendingEscalations),
      "Invalid sandbox pending escalations.",
    );
    result.sandbox = structuredClone(value.thread.evener.pendingEscalations ?? []);
  }
  return result;
}
export async function runHubNotifications(
  hub,
  { methods = METHODS, ref, observe = null, observeDurationMs = 1000, maxEvents = 100 } = {},
) {
  assert.ok(
    Array.isArray(methods) && methods.length > 0 && methods.every((method) => METHODS.includes(method)),
    "Provide supported notification methods.",
  );
  if (methods.some((method) => method.startsWith("evener/sandbox/")))
    assert.ok(nonempty(ref), "EVENER_THREAD_REF is required for sandbox notifications.");
  const sandbox = methods.some((method) => method.startsWith("evener/sandbox/"));
  let sandboxReadRequested = false;
  try {
    return await observeNotifications(hub, {
      methods,
      decodeNotification: (notification) => decodeNotification(notification, { ref }),
      readSnapshot: () => {
        if (sandbox) sandboxReadRequested = true;
        return readSnapshot(hub, { methods, ref });
      },
      observe,
      observeDurationMs,
      maxEvents,
    });
  } finally {
    if (sandboxReadRequested) await hub.request("thread/unsubscribe", { ref }).catch(() => {});
  }
}
