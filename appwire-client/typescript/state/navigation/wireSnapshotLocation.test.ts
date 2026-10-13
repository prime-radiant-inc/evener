import { expect, test } from "vitest";
import { wireSnapshot } from "../../testing/navigation";
import type { NavigationReadParams, NavigationSessionLocation } from "../../types.gen";
import { decodeNavigationResponse, materializeSnapshot } from "./codec";
import type { ResourceKey } from "./types";

// Issue #3727. The shared builder's location branch emitted `top_level: true`
// for every body, even one that named a different owner in `top_level_ref` and
// said `top_level: false`, so a nested session decoded as its own top-level row
// and a consumer promoted it into the primary pane. These drive the real
// builder and the real codec end to end: the emitted topology must match the
// body it was given.
function decodeLocation(ref: string, body: Record<string, unknown>): NavigationSessionLocation {
  const params: NavigationReadParams = { representationVersion: 3, resource: "location", ref };
  const key: ResourceKey = { kind: "location", ref };
  const decoded = decodeNavigationResponse(key, undefined, wireSnapshot(params, body));
  if (decoded.status !== "snapshot") throw new Error(`expected a snapshot, got ${decoded.status}`);
  return materializeSnapshot(key, decoded) as unknown as NavigationSessionLocation;
}

test("a nested location body decodes with its owner and top_level false", () => {
  const location = decodeLocation("local:child", {
    ref: "local:child",
    top_level_ref: "local:owner",
    top_level: false,
    session: { ref: "local:child", kind: "subagent", children: [] },
  });
  expect(location).toMatchObject({
    ref: "local:child",
    top_level_ref: "local:owner",
    top_level: false,
    session: { ref: "local:child" },
  });
});

test("a top-level location body keeps its own ref as the carrier row", () => {
  const location = decodeLocation("local:root", {
    ref: "local:root",
    top_level_ref: "local:root",
    top_level: true,
    session: { ref: "local:root", kind: "session", children: [] },
  });
  expect(location).toMatchObject({ ref: "local:root", top_level_ref: "local:root", top_level: true });
});

test("a location body that omits top_level still decodes as top-level", () => {
  const location = decodeLocation("local:plain", {
    ref: "local:plain",
    session: { ref: "local:plain", kind: "session", children: [] },
  });
  expect(location).toMatchObject({ ref: "local:plain", top_level_ref: "local:plain", top_level: true });
});
