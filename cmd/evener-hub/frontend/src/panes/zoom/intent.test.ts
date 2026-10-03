import type { SessionActivityContext } from "@evener/appwire-client";
import { expect, test } from "vitest";
import { activityContext } from "../../stores/sessionActivityTestUtils";
import { deriveCascadePath, drillZoomIntent, parseZoomParams, popZoomIntent, type SessionZoomParams } from "./intent";

const child: SessionZoomParams = {
  ref: "child",
  source: { type: "session", params: { ref: "root" } },
  edges: [{ ownerRef: "root", childRef: "child", delegateId: "d1" }],
};
const deep: SessionZoomParams = {
  ...child,
  ref: "grandchild",
  edges: [...child.edges, { ownerRef: "child", childRef: "grandchild", delegateId: "d2" }],
};

function context(overrides: Partial<SessionActivityContext> = {}): SessionActivityContext {
  return { ...activityContext("grandchild"), sessionId: "grandchild-id", rootRef: "root", ...overrides };
}

test("valid intent retains only requested bindings and exact conversation return params", () => {
  expect(parseZoomParams(deep)).toEqual(deep);
  const transcript = { ...child, source: { type: "transcript", params: { ref: "root", parentRef: "previous" } } };
  expect(parseZoomParams(transcript)).toEqual(transcript);
});

test.each([
  null,
  {},
  { ref: "job:output" },
  { ...child, ref: "" },
  { ...child, ref: "  " },
  { ...child, ref: 42 },
  { ...child, source: null },
  { ...child, source: { type: "sessionZoom", params: { ref: "root" } } },
  { ...child, source: { type: "session", params: { ref: "job:output" } } },
  { ...child, source: { type: "session", params: { ref: false } } },
  { ...child, source: { type: "transcript", params: { ref: "root", parentRef: 42 } } },
])("invalid base intent is rejected locally, case %#", (value) => {
  expect(parseZoomParams(value)).toBeNull();
});

test.each([
  null,
  {},
  [null],
  [{ ownerRef: "root", childRef: "child", delegateId: 1 }],
  [{ ownerRef: "root", childRef: "job:output", delegateId: "d1" }],
  [...child.edges, { ownerRef: "unrelated", childRef: "grandchild", delegateId: "d2" }],
  [...child.edges, { ownerRef: "child", childRef: "root", delegateId: "d2" }],
  [...child.edges, ...child.edges],
  [{ ownerRef: "root", childRef: "other", delegateId: "d1" }],
])("malformed edge segments preserve usable leaf and source, case %#", (edges) => {
  expect(parseZoomParams({ ...child, edges })).toEqual({ ...child, edges: [] });
});

test("unknown ancestry displays only the contiguous clicked segment and does not certify a root", () => {
  const expected = {
    ancestryKnown: false,
    scopes: [
      { requestedRef: "root", title: "root" },
      { requestedRef: "child", title: "child" },
      { requestedRef: "grandchild", title: "grandchild" },
    ],
  };
  expect(deriveCascadePath(deep, null)).toEqual(expected);
  expect(
    deriveCascadePath(
      deep,
      context({ ancestryKnown: false, ancestors: [{ ref: "unproven", sessionId: "x", title: "X" }] }),
    ),
  ).toEqual(expected);
});

test("authoritative ancestry overrides stale clicked presentation without adding unrelated source scopes", () => {
  const authority = context({
    rootRef: "actual-root",
    ancestors: [
      { ref: "actual-root", sessionId: "actual-root-id", title: "Actual root" },
      {
        ref: "actual-parent",
        sessionId: "actual-parent-id",
        delegateId: "actual-parent-delegate",
        title: "Actual parent",
      },
    ],
    delegateId: "actual-leaf-delegate",
  });
  expect(deriveCascadePath(deep, authority)).toEqual({
    ancestryKnown: true,
    scopes: [
      { requestedRef: "actual-root", sessionId: "actual-root-id", title: "Actual root" },
      { requestedRef: "actual-parent", sessionId: "actual-parent-id", title: "Actual parent" },
      { requestedRef: "grandchild", sessionId: "grandchild-id", title: "grandchild" },
    ],
  });
});

test("authoritative path keeps a requested owner alias when the clicked delegation proves its position", () => {
  const aliased: SessionZoomParams = {
    ...child,
    source: { type: "session", params: { ref: "root-alias" } },
    edges: [{ ownerRef: "root-alias", childRef: "child", delegateId: "d1" }],
  };
  const authority = context({
    ref: "canonical-child",
    sessionId: "child-id",
    delegateId: "d1",
    ancestors: [{ ref: "canonical-root", sessionId: "root-id", title: "Root" }],
  });
  expect(deriveCascadePath(aliased, authority)).toEqual({
    ancestryKnown: true,
    scopes: [
      { requestedRef: "root-alias", sessionId: "root-id", title: "Root" },
      { requestedRef: "child", sessionId: "child-id", title: "child" },
    ],
  });
});

test("root authority is distinguishable from an unavailable or mismatched context", () => {
  const root = { ...child, ref: "root", edges: [] };
  expect(deriveCascadePath(root, { ...activityContext("root"), sessionId: "root-id" })).toEqual({
    ancestryKnown: true,
    scopes: [{ requestedRef: "root", sessionId: "root-id", title: "root" }],
  });
  expect(deriveCascadePath(root, null)).toEqual({
    ancestryKnown: false,
    scopes: [{ requestedRef: "root", title: "root" }],
  });
});

test("deeper drill extends the accepted segment and an ancestor sibling replaces the old suffix", () => {
  expect(
    drillZoomIntent(child, deep.edges[1] ?? { ownerRef: "child", childRef: "grandchild", delegateId: "d2" }),
  ).toEqual(deep);
  expect(drillZoomIntent(deep, { ownerRef: "root", childRef: "sibling", delegateId: "d3" })).toEqual({
    ...child,
    ref: "sibling",
    edges: [{ ownerRef: "root", childRef: "sibling", delegateId: "d3" }],
  });
});

test("pop trims the selected branch and keeps the source return binding unchanged", () => {
  const path = deriveCascadePath(deep, null);
  expect(popZoomIntent(deep, "child", path)).toEqual(child);
  expect(popZoomIntent(deep, "root", path)).toEqual({ ...child, ref: "root", edges: [] });
  expect(popZoomIntent(deep, "unrelated", path)).toBe(deep);
});

test("pop can select an authoritative ancestor absent from the clicked segment", () => {
  const path = {
    ancestryKnown: true,
    scopes: [
      { requestedRef: "earlier-root", sessionId: "earlier-id", title: "Earlier" },
      { requestedRef: "child", sessionId: "child-id", title: "Child" },
      { requestedRef: "grandchild", sessionId: "grandchild-id", title: "Grandchild" },
    ],
  };
  expect(popZoomIntent(deep, "earlier-root", path)).toEqual({ ...deep, ref: "earlier-root", edges: [] });
});
