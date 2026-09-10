import assert from "node:assert/strict";

const METHODS = {
  paths: ["evener/paths/complete", ["prefix", "limit", "includeFiles"]],
  projects: ["evener/projects/recent", ["limit"]],
  validatePath: ["evener/path/validate", ["path", "kind"]],
  gitHead: ["evener/git/head", ["cwd"]],
  search: ["evener/search", ["query"]],
  harnesses: ["evener/harnesses/list", []],
  settings: ["evener/settings/overview", []],
};
const isRecord = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const nonempty = (v) => typeof v === "string" && v.trim() !== "";
const optionalString = (v) => v === undefined || typeof v === "string";
function inputFor(action, params) {
  assert.ok(Object.hasOwn(METHODS, action), "Invalid discovery action.");
  assert.ok(isRecord(params), "Invalid discovery parameters.");
  const allowed = METHODS[action][1];
  assert.ok(
    Object.keys(params).every((key) => allowed.includes(key)),
    "Unknown discovery parameter.",
  );
  if (action === "gitHead") assert.ok(nonempty(params.cwd), "Required path parameter.");
  if (action === "paths" || action === "validatePath")
    assert.ok(optionalString(params[action === "paths" ? "prefix" : "path"]), "Invalid path parameter.");
  if (action === "projects" && params.limit !== undefined)
    assert.ok(Number.isSafeInteger(params.limit), "Invalid project limit.");
  if (action === "paths") {
    if (params.limit !== undefined) assert.ok(Number.isSafeInteger(params.limit), "Invalid path limit.");
    assert.ok(params.includeFiles === undefined || typeof params.includeFiles === "boolean", "Invalid includeFiles.");
  }
  if (action === "validatePath") assert.ok(optionalString(params.kind), "Invalid path kind.");
  if (action === "search") assert.ok(optionalString(params.query), "Invalid search query.");
  return structuredClone(params);
}
function stringArray(value, name) {
  assert.ok(Array.isArray(value) && value.every((v) => typeof v === "string"), `Invalid ${name}.`);
}
function decode(action, value) {
  assert.ok(isRecord(value), "Invalid discovery response.");
  if (["paths", "projects", "harnesses"].includes(action)) {
    assert.ok(Array.isArray(value.data), "Invalid discovery data.");
    if (action === "harnesses")
      for (const row of value.data)
        assert.ok(
          isRecord(row) &&
            nonempty(row.id) &&
            nonempty(row.label) &&
            ["kind", "emptyTaskUnsupportedReason", "emptyTaskUnsupportedNextAction"].every((key) =>
              optionalString(row[key]),
            ),
          "Invalid harness descriptor.",
        );
    else stringArray(value.data, "discovery data");
  } else if (action === "validatePath") {
    assert.ok(typeof value.path === "string" && typeof value.valid === "boolean", "Invalid path validation response.");
    assert.ok(optionalString(value.error), "Invalid path validation error.");
  } else if (action === "gitHead") assert.ok(typeof value.head === "string", "Invalid git head response.");
  else if (action === "search") {
    for (const key of ["live", "past"]) {
      assert.ok(Array.isArray(value[key]), `Invalid search ${key} results.`);
      for (const row of value[key]) {
        assert.ok(
          isRecord(row) && ["id", "title", "project", "state", "age", "ref"].every((k) => typeof row[k] === "string"),
          "Invalid search result.",
        );
      }
    }
  } else if (action === "settings") {
    const optionalStrings = (object, keys) => {
      if (object === undefined) return;
      assert.ok(isRecord(object), "Invalid settings section.");
      for (const key of keys) assert.ok(optionalString(object[key]), "Invalid settings field.");
    };
    optionalStrings(value.hub, ["version", "commit", "listenAddr", "runDir", "spawnTimeout", "bearerTokenAge"]);
    optionalStrings(value.storage, ["stateDir"]);
    if (value.hub?.pastIndex !== undefined) {
      assert.ok(isRecord(value.hub.pastIndex), "Invalid settings past index.");
      optionalStrings(value.hub.pastIndex, ["path", "size"]);
      for (const key of ["perPage", "count"])
        assert.ok(
          value.hub.pastIndex[key] === undefined || Number.isSafeInteger(value.hub.pastIndex[key]),
          "Invalid settings count.",
        );
    }
    assert.ok(
      value.agents === undefined ||
        (Array.isArray(value.agents) &&
          value.agents.every((a) => isRecord(a) && nonempty(a.name) && optionalString(a.editPath))),
      "Invalid settings agents.",
    );
    if (value.mcpDiscovered !== undefined) {
      assert.ok(isRecord(value.mcpDiscovered), "Invalid MCP settings.");
      assert.ok(optionalString(value.mcpDiscovered.error), "Invalid MCP settings error.");
      if (value.mcpDiscovered.servers !== undefined)
        assert.ok(
          Array.isArray(value.mcpDiscovered.servers) &&
            value.mcpDiscovered.servers.every((server) => {
              if (!isRecord(server) || !nonempty(server.name)) return false;
              return ["transport", "status", "error"].every((key) => optionalString(server[key]));
            }),
          "Invalid MCP settings servers.",
        );
    }
  }
  return structuredClone(value);
}

/** Run exactly one explicitly selected, read-only hub method. */
export async function runDiscovery(hub, { action, params = {} } = {}) {
  const [method] = METHODS[action] ?? [];
  const captured = inputFor(action, params);
  await hub.connect();
  const response = await hub.request(method, captured);
  return { outcome: "read", action, readback: decode(action, response) };
}

export function summarizeDiscovery(result) {
  const value = result.readback;
  let count;
  if (Array.isArray(value?.data)) count = value.data.length;
  else if (Array.isArray(value?.live) && Array.isArray(value?.past)) count = value.live.length + value.past.length;
  else if (result.action === "settings") count = value ? Object.keys(value).length : 0;
  return { outcome: result.outcome, action: result.action, ...(count === undefined ? {} : { count }) };
}

