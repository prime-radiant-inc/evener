import assert from "node:assert/strict";

const methods = {
  favorite: "evener/favorite/set",
  archive: "evener/archive/set",
  projectDelete: "evener/project/delete",
  sessionDelete: "evener/session/delete",
};
const rec = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const text = (v) => typeof v === "string" && v.trim() !== "";
function validate(action, p) {
  assert.ok(Object.hasOwn(methods, action), "Invalid saved-items action.");
  assert.ok(rec(p), "Provide saved-items parameters.");
  const allowed =
    action === "favorite"
      ? ["kind", "id", "favorited", "reviewed"]
      : action === "archive"
        ? ["kind", "id", "workingDir", "archived", "reviewed"]
        : action === "projectDelete"
          ? ["key", "workingDir", "reviewed", "confirmTarget"]
          : ["ref", "reviewed", "confirmTarget"];
  assert.ok(
    Object.keys(p).every((key) => allowed.includes(key)),
    "Unknown saved-items parameter.",
  );
  let wire, identity;
  if (action === "favorite") {
    assert.equal(p.kind, "project", "Favorite target must be a project.");
    assert.ok(text(p.id), "Favorite id is required.");
    assert.equal(typeof p.favorited, "boolean", "favorited must be boolean.");
    wire = { kind: p.kind, id: p.id, favorited: p.favorited };
    identity = { kind: p.kind, id: p.id };
  } else if (action === "archive") {
    assert.ok(["project", "session"].includes(p.kind), "Archive target kind is invalid.");
    assert.ok(text(p.id), "Archive id is required.");
    assert.equal(typeof p.archived, "boolean", "archived must be boolean.");
    if (p.kind === "project") {
      assert.ok(text(p.workingDir), "workingDir is required for project archive.");
      wire = { kind: p.kind, id: p.id, workingDir: p.workingDir, archived: p.archived };
      identity = { kind: p.kind, id: p.id, workingDir: p.workingDir };
    } else {
      assert.ok(p.workingDir === undefined, "Session archive omits workingDir.");
      wire = { kind: p.kind, id: p.id, archived: p.archived };
      identity = { kind: p.kind, id: p.id };
    }
  } else if (action === "projectDelete") {
    assert.ok(text(p.key) && text(p.workingDir), "Project key and workingDir are required.");
    wire = { key: p.key, workingDir: p.workingDir };
    identity = wire;
  } else {
    assert.ok(
      text(p.ref) && p.ref.startsWith("local:") && text(p.ref.slice("local:".length)),
      "Session ref must be a non-empty local ref.",
    );
    wire = { ref: p.ref };
    identity = wire;
  }
  assert.deepEqual(p.reviewed, identity, "Reviewed target identity does not match.");
  if (action.endsWith("Delete"))
    assert.deepEqual(p.confirmTarget, identity, "Deletion confirmation does not match target.");
  return wire;
}
function nav(v) {
  assert.ok(rec(v) && text(v.generation_id) && Array.isArray(v.targets), "Invalid navigation mutation.");
  const kinds = new Set([
    "manifest",
    "section",
    "pin_catalog",
    "pin_section",
    "catalog",
    "project",
    "all_loaded_projects",
  ]);
  for (const target of v.targets) {
    assert.ok(rec(target) && kinds.has(target.kind), "Invalid navigation target.");
    const selectors = { section: "section", pin_section: "sectionId", catalog: "catalog", project: "projectKey" };
    const selector = selectors[target.kind];
    if (selector) {
      assert.ok(text(target[selector]), "Invalid navigation target selector.");
      assert.ok(
        Object.keys(target).every((key) => ["kind", selector, "revision"].includes(key)),
        "Invalid navigation target field.",
      );
      assert.ok(Number.isSafeInteger(target.revision) && target.revision >= 0, "Invalid navigation target revision.");
    } else if (target.kind === "all_loaded_projects") {
      assert.deepEqual(Object.keys(target), ["kind"], "Invalid wildcard navigation target.");
    } else {
      assert.deepEqual(Object.keys(target).sort(), ["kind", "revision"], "Invalid navigation target fields.");
      assert.ok(Number.isSafeInteger(target.revision) && target.revision >= 0, "Invalid navigation target revision.");
    }
  }
}
function decode(action, v) {
  assert.ok(rec(v), "Invalid saved-items response.");
  if (action === "favorite" || action === "archive") {
    assert.equal(v.ok, true, "Saved-items mutation was not acknowledged.");
    nav(v.navigation);
  } else {
    assert.ok(Array.isArray(v.deleted) && Array.isArray(v.skipped), "Invalid deletion response.");
    assert.ok(
      v.deleted.every(text) && v.skipped.every((s) => rec(s) && text(s.id) && text(s.reason)),
      "Invalid deletion entries.",
    );
    nav(v.navigation);
  }
  return structuredClone(v);
}
export async function runSavedItems(
  hub,
  {
    action,
    params,
    ownedHub,
    mutationOptIn = process.env.EVENER_SAVED_ITEMS_MUTATION,
    rpcUrl = process.env.EVENER_RPC_URL,
  } = {},
) {
  assert.equal(mutationOptIn, "1", "Saved-items mutation requires explicit opt-in.");
  assert.ok(text(ownedHub), "Confirm ownership of the target hub.");
  assert.equal(ownedHub, rpcUrl, "Owned hub must match the connected endpoint.");
  const input = validate(action, params);
  await hub.connect();
  try {
    return {
      action,
      outcome: "acknowledged",
      execution: "unverified",
      readback: decode(action, await hub.request(methods[action], input)),
    };
  } catch (error) {
    return { action, outcome: "uncertain", execution: "unverified", error };
  }
}
export function safeSavedItemsSummary(r) {
  return {
    action: r.action,
    outcome: r.outcome,
    execution: r.execution,
    ...(r.readback?.deleted
      ? { deletedCount: r.readback.deleted.length, skippedCount: r.readback.skipped.length }
      : {}),
  };
}
