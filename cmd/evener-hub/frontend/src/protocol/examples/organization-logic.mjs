import assert from "node:assert/strict";

const MAX_PAGE = 40;
const actions = {
  assign: { method: "evener/session-pin/assign" },
  unpin: { method: "evener/session-pin/unpin" },
  rename: { method: "evener/pin-section/rename" },
  delete: { method: "evener/pin-section/delete" },
};
const text = (value, label) => {
  assert.equal(typeof value, "string", `Provide ${label}.`);
  const result = value.trim();
  assert.ok(result, `Provide ${label}.`);
  return result;
};
function snapshot(response, resource, expectedRef) {
  assert.equal(response?.status, "ok", "Navigation read was not successful.");
  assert.equal(response.representation, "snapshot", "Navigation read was not a v2 snapshot.");
  const data = response.data;
  assert.ok(
    data && typeof data === "object" && Array.isArray(data.entities) && Array.isArray(data.containers),
    "Invalid v2 navigation snapshot.",
  );
  const metadata = data.metadata;
  assert.ok(metadata && typeof metadata === "object", "Navigation snapshot omitted metadata.");
  assert.equal(metadata.generation_id, response.generationId, "Navigation metadata generation mismatch.");
  assert.equal(metadata.revision, response.revision, "Navigation metadata revision mismatch.");
  const slot = resource === "pin_catalog" ? "pin_sections" : resource === "location" ? "session" : "manifest";
  const roots = data.containers.filter(
    (container) => container.owner?.kind === "resource_root" && container.owner.slot === slot,
  );
  assert.equal(roots.length, 1, "Navigation snapshot must contain its resource root.");
  const root = roots[0];
  assert.ok(Array.isArray(root.children), "Invalid navigation root.");
  const byKey = new Map(data.entities.map((entity) => [entity.key, entity]));
  assert.equal(byKey.size, data.entities.length, "Duplicate navigation entity.");
  assert.equal(new Set(root.children).size, root.children.length, "Duplicate navigation root child.");
  const entities = root.children.map((key) => {
    const entity = byKey.get(key);
    assert.ok(entity, "Navigation root referenced a missing entity.");
    return entity;
  });

  if (resource === "location") {
    assert.equal(metadata.ref, expectedRef, "Location read returned the wrong session.");
    assert.ok(
      entities.length === 1 && entities[0].kind === "session" && entities[0].value?.ref === expectedRef,
      "Location root omitted the selected session.",
    );
  }
  return { metadata, entities };
}
function version(response) {
  assert.equal(typeof response?.generationId, "string", "Navigation response omitted generation.");
  assert.ok(response.generationId, "Navigation response omitted generation.");
  assert.equal(typeof response.revision, "number", "Navigation response omitted revision.");
  assert.ok(Number.isSafeInteger(response.revision) && response.revision >= 0, "Invalid navigation revision.");
  assert.equal(typeof response.etag, "string", "Navigation response omitted etag.");
  return { generationId: response.generationId, revision: response.revision };
}
export async function readNavigation(hub, sessionRef) {
  const manifest = await hub.request("evener/navigation/read", { representationVersion: 2, resource: "manifest" });
  const base = version(manifest);
  snapshot(manifest, "manifest");
  const sections = [];
  const sectionIds = new Set();
  let catalogVersion;
  for (let offset = 0; ; ) {
    const page = await hub.request("evener/navigation/read", {
      representationVersion: 2,
      resource: "pin_catalog",
      offset,
      limit: MAX_PAGE,
    });
    const pageVersion = version(page);
    assert.equal(
      pageVersion.generationId,
      base.generationId,
      "Navigation generation changed while reading the pin catalog.",
    );
    if (!catalogVersion) catalogVersion = pageVersion;
    else assert.equal(pageVersion.revision, catalogVersion.revision, "Pin catalog revision changed while paging.");
    const pageData = snapshot(page, "pin_catalog");
    const pageSections = pageData.entities.map((entity) => {
      const value = entity.value;
      assert.ok(
        entity.kind === "pin_section" &&
          value &&
          typeof value.id === "string" &&
          value.id &&
          typeof value.name === "string" &&
          value.name &&
          Number.isSafeInteger(value.count) &&
          value.count >= 0,
        "Invalid pinned section.",
      );
      assert.ok(!sectionIds.has(value.id), "Pin catalog repeated a section while paging.");
      sectionIds.add(value.id);
      return value;
    });
    assert.ok(pageSections.length <= MAX_PAGE, "Pin catalog exceeded its requested page limit.");
    sections.push(...pageSections);
    const remaining = pageData.metadata.remaining;
    if (remaining === 0) break;
    assert.ok(
      Number.isSafeInteger(remaining) && remaining > 0 && pageSections.length > 0,
      "Pin catalog page did not advance.",
    );
    offset += pageSections.length;
  }
  let location;
  if (sessionRef) {
    const response = await hub.request("evener/navigation/read", {
      representationVersion: 2,
      resource: "location",
      ref: sessionRef,
    });
    const locationVersion = version(response);
    assert.equal(
      locationVersion.generationId,
      base.generationId,
      "Navigation generation changed while reading session membership.",
    );
    const data = snapshot(response, "location", sessionRef);
    location = {
      version: locationVersion,
      response,
      pinSectionId: data.metadata.pin_section_id,
      values: data.entities.map((entity) => entity.value),
    };
  }
  return {
    manifest,
    versions: { manifest: base, catalog: catalogVersion, location: location?.version },
    generationId: base.generationId,
    revision: base.revision,
    sections,
    location,
  };
}
function normalizeTarget(action, target) {
  if (!target || typeof target !== "object") throw new Error("Provide an organization target.");
  const sessionRef = action === "assign" || action === "unpin" ? text(target.sessionRef, "sessionRef") : undefined;
  if (action === "assign") {
    const hasId = Object.hasOwn(target, "sectionId");
    const hasName = Object.hasOwn(target, "sectionName");
    const sectionId = hasId ? text(target.sectionId, "sectionId") : undefined;
    const sectionName = hasName ? text(target.sectionName, "sectionName") : undefined;
    assert.equal(Boolean(sectionId) + Boolean(sectionName), 1, "Provide exactly one of sectionId or sectionName.");
    if (sectionName) assert.ok([...sectionName].length <= 80, "sectionName must be at most 80 characters.");
    return { sessionRef, ...(sectionId ? { sectionId } : { sectionName }) };
  }
  if (action === "unpin") return { sessionRef };
  const sectionId = text(target.sectionId, "sectionId");
  if (action === "rename") {
    const name = text(target.name, "name");
    assert.ok([...name].length <= 80, "name must be at most 80 characters.");
    return { sectionId, name };
  }
  return { sectionId };
}
export async function runOrganization(hub, { action, target, ownedHub }) {
  assert.equal(process.env.EVENER_ORGANIZATION_MUTATION, "1", "Organization mutation requires explicit opt-in.");
  const owner = text(ownedHub, "owned hub URL");
  const connected = text(process.env.EVENER_RPC_URL, "EVENER_RPC_URL");
  assert.equal(owner, connected, "Owned hub must match the connected endpoint.");
  assert.ok(Object.hasOwn(actions, action), "Unknown organization action.");
  const spec = actions[action];
  const params = normalizeTarget(action, target);
  const before = await readNavigation(hub, params.sessionRef);
  let response;
  try {
    response = await hub.request(spec.method, params);
  } catch {
    try {
      return { outcome: "unknownMutation", action, before, readback: await readNavigation(hub, params.sessionRef) };
    } catch {
      return { outcome: "unknownMutationReadFailed", action, before };
    }
  }
  if (response?.ok === false) return { outcome: "rejected", action, changed: false, before };
  try {
    assert.equal(response?.ok, true, "Mutation acknowledgement omitted ok.");
    assert.equal(typeof response.changed, "boolean", "Mutation response omitted changed.");
    assert.ok(
      response.navigation &&
        typeof response.navigation.generation_id === "string" &&
        response.navigation.generation_id &&
        Array.isArray(response.navigation.targets),
      "Mutation response omitted navigation receipt.",
    );
    const after = await readNavigation(hub, params.sessionRef);
    assert.equal(
      after.generationId,
      response.navigation.generation_id,
      "Readback generation differs from mutation receipt.",
    );
    for (const target of response.navigation.targets) {
      assert.ok(target && typeof target.kind === "string", "Invalid navigation receipt target.");
      assert.ok(
        target.revision === undefined || (Number.isSafeInteger(target.revision) && target.revision >= 0),
        "Invalid navigation receipt revision.",
      );
    }
    // Revisions belong to semantic resources. A section or project revision
    // cannot be used as a floor for the catalog, manifest, or session location.
    for (const [resource, kind] of [
      ["manifest", "manifest"],
      ["catalog", "pin_catalog"],
    ]) {
      const floor = Math.max(
        before.versions[resource].revision,
        ...response.navigation.targets.filter((target) => target.kind === kind).map((target) => target.revision ?? 0),
      );
      assert.ok(after.versions[resource].revision >= floor, "Readback is older than the resource receipt.");
    }
    if (after.versions.location)
      assert.ok(
        after.versions.location.revision >= before.versions.location.revision,
        "Session location revision regressed.",
      );

    return { outcome: "applied", action, changed: response.changed, receipt: response.navigation, before, after };
  } catch {
    return { outcome: "ackReadbackFailed", action, changed: response?.changed, receipt: response?.navigation, before };
  }
}
