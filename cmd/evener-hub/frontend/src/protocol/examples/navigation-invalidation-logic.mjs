import assert from "node:assert/strict";

const TARGET_KINDS = new Set([
  "manifest",
  "section",
  "pin_catalog",
  "pin_section",
  "catalog",
  "project",
  "all_loaded_projects",
]);
const MAX_DURATION_MS = 10_000;
const MAX_EVENTS = 100;

function record(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function decodeResponse(value) {
  assert.ok(record(value), "Invalid navigation response.");
  assert.equal(value.status, "ok", "Navigation read did not return an ok response.");
  assert.ok(typeof value.generationId === "string" && value.generationId.length > 0, "Invalid navigation generation.");
  assert.ok(Number.isSafeInteger(value.revision) && value.revision >= 0, "Invalid navigation revision.");
  assert.ok(typeof value.etag === "string", "Invalid navigation etag.");
  assert.equal(value.representation, "snapshot", "Navigation response was not a manifest snapshot.");
  assert.ok(record(value.data), "Navigation response did not include manifest data.");
  assert.ok(record(value.data.metadata), "Navigation response did not include snapshot metadata.");
  assert.ok(Array.isArray(value.data.entities), "Navigation response did not include snapshot entities.");
  assert.ok(Array.isArray(value.data.containers), "Navigation response did not include snapshot containers.");
  assert.equal(
    value.data.metadata.generation_id,
    value.generationId,
    "Manifest metadata generation does not match response.",
  );
  assert.equal(value.data.metadata.revision, value.revision, "Manifest metadata revision does not match response.");
  return structuredClone(value);
}

function decodeTarget(value) {
  assert.ok(record(value), "Invalid navigation invalidation target.");
  assert.ok(TARGET_KINDS.has(value.kind), "Invalid navigation invalidation target kind.");
  for (const key of ["section", "sectionId", "catalog", "projectKey"]) {
    if (key in value) assert.equal(typeof value[key], "string", `Invalid navigation ${key}.`);
  }
  if (value.kind === "all_loaded_projects") {
    assert.ok(
      !("revision" in value) &&
        !("section" in value) &&
        !("sectionId" in value) &&
        !("catalog" in value) &&
        !("projectKey" in value),
      "Invalid all-loaded-projects target.",
    );
    return structuredClone(value);
  }
  assert.ok(Number.isSafeInteger(value.revision) && value.revision >= 0, "Invalid navigation target revision.");
  const selectors = {
    manifest: [],
    pin_catalog: [],
    section: ["section"],
    pin_section: ["sectionId"],
    catalog: ["catalog"],
    project: ["projectKey"],
  }[value.kind];
  for (const key of ["section", "sectionId", "catalog", "projectKey"]) {
    if (selectors.includes(key)) assert.ok(value[key], `Navigation ${value.kind} requires ${key}.`);
    else assert.ok(!(key in value), `Navigation ${value.kind} does not permit ${key}.`);
  }
  return structuredClone(value);
}

function decodeEvent(value) {
  assert.ok(record(value), "Invalid navigation invalidation.");
  assert.ok(
    typeof value.generationId === "string" && value.generationId.length > 0,
    "Invalid navigation invalidation generation.",
  );
  assert.ok(Number.isSafeInteger(value.sequence) && value.sequence >= 0, "Invalid navigation invalidation sequence.");
  assert.ok(Array.isArray(value.targets), "Invalid navigation invalidation targets.");
  return { generationId: value.generationId, sequence: value.sequence, targets: value.targets.map(decodeTarget) };
}

const readParams = () => ({ representationVersion: 2, resource: "manifest" });

function boundedWait(durationMs) {
  return durationMs === 0 ? Promise.resolve() : new Promise((resolve) => setTimeout(resolve, durationMs));
}

export async function runNavigationInvalidation(
  hub,
  { observe = null, observeDurationMs = 0, maxEvents = MAX_EVENTS } = {},
) {
  assert.ok(
    Number.isSafeInteger(observeDurationMs) && observeDurationMs >= 0 && observeDurationMs <= MAX_DURATION_MS,
    "Invalid observation duration.",
  );
  assert.ok(Number.isSafeInteger(maxEvents) && maxEvents > 0 && maxEvents <= MAX_EVENTS, "Invalid observation limit.");
  const events = [];
  let eventCount = 0;
  let overflow = false;
  let listenerError;
  let stop;
  const observedGenerations = new Set();
  let latestObservedGeneration;
  const lastSequences = new Map();
  const latestManifestRevisions = new Map();
  let gap = false;
  const listener = (notification) => {
    if (notification?.method !== "evener/navigation/invalidated") return;
    try {
      const event = decodeEvent(notification.params);
      eventCount += 1;
      observedGenerations.add(event.generationId);
      latestObservedGeneration = event.generationId;
      const previous = lastSequences.get(event.generationId);
      if (previous !== undefined && event.sequence > previous + 1) gap = true;
      lastSequences.set(event.generationId, Math.max(previous ?? event.sequence, event.sequence));
      for (const target of event.targets) {
        if (target.kind === "manifest") {
          latestManifestRevisions.set(
            event.generationId,
            Math.max(latestManifestRevisions.get(event.generationId) ?? 0, target.revision),
          );
        }
      }
      if (events.length < maxEvents) events.push(event);
      else overflow = true;
    } catch (error) {
      listenerError ??= error;
    }
  };
  try {
    await hub.connect();
    stop = hub.onNotification(listener);
    const initial = decodeResponse(await hub.request("evener/navigation/read", readParams()));
    if (observe) await observe();
    else await boundedWait(observeDurationMs);
    if (listenerError) throw listenerError;
    const analyze = () => {
      const generationChanged = [...observedGenerations].some((generationId) => generationId !== initial.generationId);
      return { gap, generationChanged, reconciled: generationChanged || gap || eventCount > 0 };
    };
    const beforeReadback = analyze();
    let readback = beforeReadback.reconciled
      ? decodeResponse(await hub.request("evener/navigation/read", readParams()))
      : initial;
    if (listenerError) throw listenerError;
    const isCurrent = (response) => {
      if (latestObservedGeneration && latestObservedGeneration !== response.generationId) return false;
      return (latestManifestRevisions.get(response.generationId) ?? 0) <= response.revision;
    };
    let attempts = 1;
    while (beforeReadback.reconciled && !isCurrent(readback) && attempts < 2) {
      readback = decodeResponse(await hub.request("evener/navigation/read", readParams()));
      attempts += 1;
      if (listenerError) throw listenerError;
    }
    const afterReadback = analyze();
    const current = beforeReadback.reconciled ? isCurrent(readback) : false;
    return {
      outcome: beforeReadback.reconciled && !current ? "uncertain" : "read",
      readback,
      events,
      eventCount,
      overflow,
      ...afterReadback,
      reconciled: current,
    };
  } finally {
    stop?.();
  }
}

export function summarizeNavigationInvalidation(result) {
  return {
    outcome: result.outcome,
    events: result.eventCount,
    overflow: result.overflow,
    reconciled: result.reconciled,
    generationId: result.readback.generationId,
    revision: result.readback.revision,
  };
}
