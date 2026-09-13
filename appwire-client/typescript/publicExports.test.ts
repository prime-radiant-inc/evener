// @vitest-environment node
import { describe, expect, it } from "vitest";
import { type ActivityNodeLike, activityNodeID } from "./index";

// activityNodeID's parameter type is part of the package's public surface: an
// installed consumer must be able to name it. Import it from the package root
// in a type position so a missing re-export fails compilation here.
describe("protocol package root public exports", () => {
  it("re-exports the type that activityNodeID's parameter uses", () => {
    const nodes: ActivityNodeLike[] = [
      { kind: "session", sessionId: "s1" },
      { kind: "delegate", delegateId: "d1" },
      { kind: "shell", jobId: "j1" },
    ];
    expect(nodes.map(activityNodeID)).toEqual(["session:s1", "delegate:d1", "job:j1"]);
  });
});
