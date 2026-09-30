// @vitest-environment node
import {
  decodeNavigationResponse,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  normalizedGraphFromSnapshot,
} from "@evener/appwire-client/state/navigation";
import { expect, test } from "vitest";
import { activeWatchCount } from "../../shell/rail/railNodes";
import { selectRailModel } from "./selectors";

const key = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
const entityKey = `${navigationViewScope(key)}/entity/${"1".repeat(64)}`;
const snapshot = (fields: Record<string, unknown>) => ({
  metadata: {},
  entities: [{ key: entityKey, kind: "session", value: { ref: "remote:owner", children: [], ...fields } }],
  containers: [
    {
      key: navigationRootContainerKey(key, "sessions"),
      owner: { kind: "resource_root", slot: "sessions" },
      children: [entityKey],
    },
    {
      key: navigationOwnedContainerKey(entityKey, "children"),
      owner: { kind: "entity", entityKey, slot: "children" },
      children: [],
    },
  ],
});
test("v3 compact receiver counts materialize without navigation watch detail", () => {
  const decoded = decodeNavigationResponse(key, undefined, {
    status: "ok",
    representation: "snapshot",
    generationId: "g",
    revision: 1,
    etag: "tag",
    data: snapshot({ watch_count: 40, armed_watch_count: 32, running_job_count: 2, running_job_command: "go test" }),
  });
  expect(decoded.status).toBe("snapshot");
  if (decoded.status !== "snapshot") throw new Error("invalid compact fixture");
  const model = selectRailModel({
    key,
    graph: normalizedGraphFromSnapshot(decoded.snapshot),
    version: decoded.version,
    presence: "present",
  });
  const session = model.sessions.get(entityKey);
  if (!session) throw new Error("missing compact session");
  expect(session).toMatchObject({
    watch_count: 40,
    armed_watch_count: 32,
    running_job_count: 2,
    running_job_command: "go test",
  });
  expect(activeWatchCount(session)).toBe(32);
  expect("watches" in session).toBe(false);
});
test("v3 browser decoder rejects retired navigation watch detail instead of silently accepting it", () => {
  const decoded = decodeNavigationResponse(key, undefined, {
    status: "ok",
    representation: "snapshot",
    generationId: "g",
    revision: 1,
    etag: "tag",
    data: snapshot({ watches: [{ id: "obsolete" }] }),
  });
  expect(decoded.status).toBe("error");
});
