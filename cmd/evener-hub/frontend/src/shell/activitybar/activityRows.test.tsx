// activityRows' exported line builders, unit-tested directly (the rows
// themselves are exercised through the tab and sidebar suites).

import type { NavigationSessionSummary } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import { agentRollupLine } from "./activityRows";

function sub(partial: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary {
  return {
    ref: "local:sub",
    host_id: "local",
    session_id: "sub",
    title: "Sub",
    project: "p",
    state: "active",
    kind: "subagent",
    live: true,
    children: [],
    ...partial,
  } as NavigationSessionSummary;
}

describe("agentRollupLine", () => {
  test("counts the wire's omitted subagents in the agents figure", () => {
    // The Agents tab's fold reports the TRUE total (loaded rows plus
    // more_subagents); a rollup counting only loaded rows would understate
    // the same scope one line away.
    expect(agentRollupLine(sub({ children: [sub({ ref: "local:child" })], more_subagents: 2 }))).toContain("3 agents");
  });

  test("a lone loaded child still reads 1 agent", () => {
    expect(agentRollupLine(sub({ children: [sub({ ref: "local:child" })] }))).toContain("1 agent");
  });

  test("fork originals never count as agents", () => {
    expect(
      agentRollupLine(
        sub({
          children: [sub({ ref: "local:fork", kind: "fork" }), sub({ ref: "local:child", kind: "subagent" })],
        }),
      ),
    ).toContain("1 agent");
  });
});
