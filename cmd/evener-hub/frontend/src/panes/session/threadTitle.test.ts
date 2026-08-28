import { expect, test } from "vitest";
import type { ThreadModel } from "../../protocol/model";
import type { NavigationSessionSummary } from "../../protocol/types.gen";
import { resolveThreadName } from "./threadTitle";

function summary(title: string): NavigationSessionSummary {
  return {
    ref: "ref_a",
    host_id: "local",
    session_id: "ref_a",
    title,
    project: "test-project",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
  };
}

test("prefers the live thread name when hydrated", () => {
  const threads = new Map<string, ThreadModel>([["ref_a", { name: "Live name" } as ThreadModel]]);
  expect(resolveThreadName(threads, summary("Navigation title"), "ref_a")).toBe("Live name");
});
test("falls back to the navigation location summary", () => {
  expect(resolveThreadName(new Map(), summary("Navigation title"), "ref_a")).toBe("Navigation title");
});
test("returns undefined when neither source has a name", () => {
  expect(resolveThreadName(new Map(), null, "ref_a")).toBeUndefined();
});
