// @vitest-environment node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, test } from "vitest";
import {
  lifecycleMachine,
  lifecycleState,
  lifecycleStateClass,
  lifecycleStateIsFailed,
  lifecycleStateIsResting,
  lifecycleStateIsTerminal,
} from "./lifecycle";

interface LifecycleClassCase {
  machine: string;
  state: string;
  class: string;
  terminal: boolean;
  resting: boolean;
}

const SHARED_CASES: LifecycleClassCase[] = JSON.parse(
  readFileSync(fileURLToPath(new URL("../../testdata/lifecycle/state_classes.json", import.meta.url).href), "utf8"),
);

describe("generated lifecycle catalog", () => {
  test("carries the declared machines", () => {
    for (const kind of [
      "session",
      "turn",
      "threadStatus",
      "job",
      "delegate",
      "delegateRun",
      "delegateOutcome",
      "watch",
      "task",
      "goal",
      "hostOperation",
      "mutationProjection",
    ]) {
      expect(lifecycleMachine(kind), kind).toBeDefined();
    }
  });

  test("classes the job statuses the client must distinguish", () => {
    expect(lifecycleStateClass("job", "running")).toBe("active");
    expect(lifecycleStateClass("job", "completed")).toBe("settled");
    expect(lifecycleStateClass("job", "command_exited_nonzero")).toBe("failed");
    expect(lifecycleStateClass("job", "command_killed")).toBe("failed");
    expect(lifecycleStateClass("job", "exhausted")).toBe("failed");
    expect(lifecycleStateClass("job", "cancelled")).toBe("neutral");
    expect(lifecycleStateClass("job", "stopped")).toBe("neutral");
  });

  test("terminality follows the declaration, not the class", () => {
    expect(lifecycleStateIsTerminal("job", "completed")).toBe(true);
    expect(lifecycleStateIsTerminal("job", "running")).toBe(false);
    expect(lifecycleStateIsTerminal("task", "done")).toBe(false);
    expect(lifecycleStateIsTerminal("delegate", "closed")).toBe(true);
    expect(lifecycleStateIsTerminal("job", "not-a-state")).toBe(false);
  });

  test("resting includes a failed rest and excludes working states", () => {
    expect(lifecycleStateIsResting("threadStatus", "idle")).toBe(true);
    expect(lifecycleStateIsResting("threadStatus", "systemError")).toBe(true);
    expect(lifecycleStateIsResting("threadStatus", "active")).toBe(false);
    expect(lifecycleStateIsResting("threadStatus", "awaiting")).toBe(false);
  });

  test("failure classification matches every machine", () => {
    expect(lifecycleStateIsFailed("delegateOutcome", "failed")).toBe(true);
    expect(lifecycleStateIsFailed("delegateOutcome", "exhausted")).toBe(true);
    expect(lifecycleStateIsFailed("delegateOutcome", "completed")).toBe(false);
    expect(lifecycleStateIsFailed("hostOperation", "failed")).toBe(true);
    expect(lifecycleStateIsFailed("goal", "blocked")).toBe(true);
  });

  test("unknown machines and states have no facts", () => {
    expect(lifecycleMachine("bogus")).toBeUndefined();
    expect(lifecycleState("bogus", "idle")).toBeUndefined();
    expect(lifecycleState("job", "bogus")).toBeUndefined();
  });

  test("matches the shared cross-language classification contract", () => {
    expect(SHARED_CASES.length).toBeGreaterThan(0);
    for (const tc of SHARED_CASES) {
      const label = `${tc.machine}/${tc.state}`;
      expect(lifecycleStateClass(tc.machine, tc.state), label).toBe(tc.class);
      expect(lifecycleStateIsTerminal(tc.machine, tc.state), label).toBe(tc.terminal);
      expect(lifecycleStateIsResting(tc.machine, tc.state), label).toBe(tc.resting);
    }
  });
});
