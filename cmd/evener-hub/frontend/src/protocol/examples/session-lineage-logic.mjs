import assert from "node:assert/strict";
import { AcknowledgedReadbackError, mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const record = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const string = (v) => typeof v === "string";
const nonBlank = (v) => string(v) && v.trim() !== "";
function source(value, ref, reviewed) {
  assert.ok(record(value) && record(value.thread) && record(value.thread.evener), "Invalid source thread.");
  const t = value.thread;
  assert.ok(
    nonBlank(t.id) && nonBlank(t.evener.ref) && (ref === undefined || t.evener.ref === ref),
    "Invalid source thread identity.",
  );
  assert.ok(t.evener.instanceId === undefined || nonBlank(t.evener.instanceId), "Invalid source instance.");
  assert.ok(t.name === undefined || string(t.name), "Invalid source name.");
  if (reviewed)
    assert.deepEqual(
      {
        threadId: t.id,
        ...(t.evener.instanceId === undefined ? {} : { instanceId: t.evener.instanceId }),
        ...(Object.hasOwn(t, "name") ? { name: t.name } : {}),
      },
      reviewed,
      "Source thread changed; review current state.",
    );
  return structuredClone(value);
}
function paramsFor(action, p) {
  assert.ok(["review", "transcripts", "preview", "resume", "fork"].includes(action), "Invalid session lineage action.");
  assert.ok(record(p), "Provide session lineage parameters.");
  const allowed =
    action === "fork"
      ? [
          "ref",
          "expectedInstanceId",
          "reviewed",
          "sourceTurnId",
          "editedInput",
          "label",
          "modelProvider",
          "model",
          "deferInput",
          "aside",
        ]
      : action === "resume"
        ? ["ref", "expectedInstanceId", "reviewed"]
        : ["ref", ...(action === "preview" ? ["limit"] : [])];
  assert.ok(
    Object.keys(p).every((k) => allowed.includes(k)),
    "Unknown session lineage parameter.",
  );
  assert.ok(nonBlank(p.ref), "Provide the session reference.");
  if (action === "preview")
    assert.ok(p.limit === undefined || Number.isSafeInteger(p.limit), "Preview limit must be an integer.");
  if (["review", "transcripts", "preview"].includes(action)) return structuredClone(p);
  assert.ok(record(p.reviewed) && nonBlank(p.reviewed.threadId), "Provide reviewed source state.");
  assert.ok(
    Object.keys(p.reviewed).every((k) => ["threadId", "instanceId", "name"].includes(k)),
    "Invalid reviewed fields.",
  );
  assert.ok(p.reviewed.instanceId === undefined || nonBlank(p.reviewed.instanceId), "Invalid reviewed instance.");
  assert.ok(p.reviewed.name === undefined || string(p.reviewed.name), "Invalid reviewed name.");
  if (p.expectedInstanceId !== undefined)
    assert.ok(
      nonBlank(p.expectedInstanceId) && p.expectedInstanceId === p.reviewed.instanceId,
      "Expected instance must match the review.",
    );
  if (action === "fork") {
    for (const key of ["aside", "deferInput"])
      assert.ok(p[key] === undefined || typeof p[key] === "boolean", `${key} must be boolean.`);
    if (p.sourceTurnId !== undefined)
      assert.ok(
        string(p.sourceTurnId) && /^(?:turn_)?[1-9]\d*$/.test(p.sourceTurnId.trim()),
        "Provide a positive transcript entry index.",
      );
    assert.ok(!(nonBlank(p.editedInput) && p.deferInput), "Choose edited input or deferred input.");
    assert.ok(p.editedInput === undefined || string(p.editedInput), "Edited input must be text.");
    if (p.aside) {
      assert.ok(p.ref.startsWith("local:"), "Aside requires a local Evener thread.");
      assert.ok(
        p.sourceTurnId === undefined &&
          p.editedInput === undefined &&
          p.deferInput === undefined &&
          p.label === undefined,
        "Aside cannot include source-turn options.",
      );
    } else if (p.ref.startsWith("local:")) {
      assert.ok(nonBlank(p.sourceTurnId), "Provide the source transcript entry.");
      assert.ok(p.deferInput === true || nonBlank(p.editedInput), "Provide edited input or choose deferred input.");
    }
    for (const key of ["label", "modelProvider", "model"])
      assert.ok(p[key] === undefined || string(p[key]), `${key} must be text.`);
  }
  return structuredClone(p);
}
function decodeRead(action, value, ref) {
  if (action === "transcripts") {
    assert.ok(record(value) && Array.isArray(value.data), "Invalid transcript list.");
    assert.ok(
      value.data.every((x) => record(x) && nonBlank(x.ref) && string(x.title) && string(x.kind)),
      "Invalid transcript target.",
    );
    for (const target of value.data) {
      for (const key of ["threadId", "status", "source"])
        assert.ok(target[key] === undefined || string(target[key]), "Invalid transcript metadata.");
      assert.ok(
        target.turnsUsed === undefined || (Number.isSafeInteger(target.turnsUsed) && target.turnsUsed >= 0),
        "Invalid transcript turn count.",
      );
    }
  } else {
    assert.ok(
      record(value) && value.ref === ref && Array.isArray(value.items) && typeof value.truncated === "boolean",
      "Invalid subagent preview.",
    );
    for (const item of value.items)
      assert.ok(record(item) && nonBlank(item.type) && string(item.id), "Invalid preview item.");
  }
  return structuredClone(value);
}
export async function runSessionLineage(hub, { action = "transcripts", params = {}, ownedHub } = {}) {
  const p = paramsFor(action, params);
  if (action === "review") {
    await hub.connect();
    const readback = source(
      await hub.request("thread/read", { ref: p.ref, includeTurns: false, subscribe: false }),
      p.ref,
    );
    const t = readback.thread;
    return {
      outcome: "read",
      action,
      readback,
      review: {
        threadId: t.id,
        ...(t.evener.instanceId === undefined ? {} : { instanceId: t.evener.instanceId }),
        ...(t.name === undefined ? {} : { name: t.name }),
      },
    };
  }
  if (action === "transcripts" || action === "preview") {
    await hub.connect();
    const method = action === "transcripts" ? "evener/thread/transcripts/list" : "evener/subagentPreview";
    return {
      outcome: "read",
      action,
      readback: decodeRead(
        action,
        await hub.request(
          method,
          action === "preview" ? { ref: p.ref, ...(p.limit === undefined ? {} : { limit: p.limit }) } : { ref: p.ref },
        ),
        p.ref,
      ),
    };
  }
  requireOwnedHub(process.env.EVENER_SESSION_LINEAGE_MUTATION, ownedHub);
  await hub.connect();
  const before = source(
    await hub.request("thread/read", { ref: p.ref, includeTurns: false, subscribe: false }),
    p.ref,
    p.reviewed,
  );
  const turnFork =
    !p.aside && (nonBlank(p.sourceTurnId) || nonBlank(p.editedInput) || nonBlank(p.label) || p.deferInput === true);
  if (action === "fork" && turnFork)
    assert.equal(before.thread.evener.capabilities?.forkFromTurn, true, "Session control is unavailable.");
  if (action === "resume")
    return {
      action,
      ...(await mutateAndReadback(
        hub,
        "thread/resume",
        { ref: p.ref },
        (v) => source(v, p.ref),
        async () =>
          source(await hub.request("thread/read", { ref: p.ref, includeTurns: false, subscribe: false }), p.ref),
      )),
    };
  const body = {
    ref: p.ref,
    ...(p.sourceTurnId === undefined ? {} : { sourceTurnId: p.sourceTurnId }),
    ...Object.fromEntries(
      ["editedInput", "label", "modelProvider", "model", "deferInput", "aside"]
        .filter((k) => p[k] !== undefined)
        .map((k) => [k, p[k]]),
    ),
  };
  let childRef;
  let response;
  let result;
  try {
    result = await mutateAndReadback(
      hub,
      "thread/fork",
      body,
      (v) => {
        const child = source(v);
        assert.notEqual(child.thread.evener.ref, p.ref, "Fork response did not identify a new thread.");
        assert.ok(child.originalInput === undefined || string(child.originalInput), "Invalid original input.");
        response = child;
        childRef = child.thread.evener.ref;
      },
      async () =>
        source(
          await hub.request("thread/read", { ref: childRef ?? p.ref, includeTurns: false, subscribe: false }),
          childRef ?? p.ref,
        ),
    );
  } catch (error) {
    if (!(error instanceof AcknowledgedReadbackError)) throw error;
    // Retain the acknowledged child and original input even if its read fails.
    result = { outcome: "acknowledged", execution: "unverified", readback: undefined };
  }
  return { action, ...result, response, readbackTarget: childRef === undefined ? "parent" : "child" };
}
export function summarizeSessionLineage(result) {
  const v = result.readback;
  return {
    outcome: result.outcome,
    action: result.action,
    readbackAvailable: v !== undefined,
    ...(result.readbackTarget === undefined ? {} : { readbackTarget: result.readbackTarget }),
    ...(Array.isArray(v?.data) ? { count: v.data.length } : {}),
    ...(result.action === "preview" ? { count: v.items?.length ?? 0, truncated: v.truncated } : {}),
  };
}
