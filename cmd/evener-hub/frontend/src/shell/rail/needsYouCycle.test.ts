// @vitest-environment jsdom
import { afterEach, expect, test } from "vitest";
import type { NavigationSessionSummary } from "../../protocol/types.gen";
import { needsYouRefs, nextNeedsYouRef, openNeedsYouSession } from "./needsYouCycle";

afterEach(() => {
  window.history.pushState({}, "", "/");
});

function row(ref: string): NavigationSessionSummary {
  return {
    ref,
    host_id: "local",
    session_id: ref,
    title: ref,
    project: "Proj",
    state: "awaiting",
    kind: "session",
    live: true,
    children: [],
  };
}

test("needsYouRefs preserves bounded server order", () => {
  expect(needsYouRefs([row("local:a"), row("local:b"), row("local:c")])).toEqual(["local:a", "local:b", "local:c"]);
});
test("needsYouRefs is empty before resources load", () => expect(needsYouRefs(null)).toEqual([]));
test("nextNeedsYouRef advances and wraps", () => {
  expect(nextNeedsYouRef(["a", "b", "c"], "a")).toBe("b");
  expect(nextNeedsYouRef(["a", "b", "c"], "c")).toBe("a");
});
test("nextNeedsYouRef starts at first without a matching focus", () => {
  expect(nextNeedsYouRef(["a", "b", "c"], null)).toBe("a");
  expect(nextNeedsYouRef(["a", "b", "c"], "missing")).toBe("a");
});
test("nextNeedsYouRef returns null for an empty list", () => expect(nextNeedsYouRef([], "a")).toBeNull());
test("openNeedsYouSession navigates to encoded session URL", () => {
  openNeedsYouSession("local:ny1");
  expect(window.location.pathname).toBe("/s/local%3Any1");
});
test("openNeedsYouSession is a no-op for the current URL", () => {
  window.history.pushState({}, "", "/s/local%3Any1");
  let popped = false;
  window.addEventListener("popstate", () => (popped = true), { once: true });
  openNeedsYouSession("local:ny1");
  expect(popped).toBe(false);
});
