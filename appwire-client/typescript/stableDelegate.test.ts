import { expect, test } from "vitest";
import { delegateWaitText, delegateWorkText } from "./stableDelegate";

// The one rule every surface shares: a working row's newest tool intent
// stands in for the status word, and every other row state keeps the word the
// surface passed in -- so each client keeps its own status vocabulary.
test("a working row's intent stands in for the status word", () => {
  expect(delegateWorkText({ terminal: false, latestIntent: "Reading the board." }, "working")).toBe(
    "Reading the board.",
  );
});

test("a terminal row keeps the status word over a stale intent", () => {
  expect(
    delegateWorkText({ terminal: true, outcome: "completed", latestIntent: "Reading the board." }, "completed"),
  ).toBe("completed");
});

test("a row without a non-empty intent keeps the status word", () => {
  expect(delegateWorkText({ terminal: false }, "working")).toBe("working");
  expect(delegateWorkText({ latestIntent: "" }, "working")).toBe("working");
});

// The hold, not the count, decides whether a row reads as waiting. These assert
// the transition between states rather than the copy: which source supplies the
// text, and what it reverts to when the hold clears.
test("the drain hold, not the job count, supplies the wait text", () => {
  const held = { terminal: false, drainHeldByJobs: true, runningJobCount: 1 };
  expect(delegateWaitText(held)).not.toBe("");
  // Held outranks the intent: the intent stopped moving for the whole hold.
  expect(delegateWorkText({ ...held, latestIntent: "Reading." }, "working")).toBe(delegateWaitText(held));

  // A positive count with no hold is a delegate still working: it keeps both
  // its own intent and, without one, the status word.
  const working = { terminal: false, runningJobCount: 3 };
  expect(delegateWaitText(working)).toBe("");
  expect(delegateWorkText({ ...working, latestIntent: "Reading." }, "working")).toBe("Reading.");
  expect(delegateWorkText(working, "working")).toBe("working");
});

test("clearing the hold puts the intent and the status word back", () => {
  const intent = "Retrying the flaky search.";
  const held = { terminal: false, drainHeldByJobs: true, runningJobCount: 2, latestIntent: intent };
  expect(delegateWorkText(held, "working")).not.toBe(intent);

  // The same row once the drain has let go: the intent resumes, and with no
  // intent the status word does.
  const released = { ...held, drainHeldByJobs: false, runningJobCount: 0 };
  expect(delegateWorkText(released, "working")).toBe(intent);
  expect(delegateWorkText({ ...released, latestIntent: "" }, "working")).toBe("working");

  // A terminal row never claims a wait, whatever the hold flag last said.
  expect(delegateWaitText({ terminal: true, drainHeldByJobs: true, runningJobCount: 2 })).toBe("");
});

// The count phases the wait; it never changes whether there is one.
test("the job count phrases the hold and nothing more", () => {
  const one = delegateWaitText({ terminal: false, drainHeldByJobs: true, runningJobCount: 1 });
  const many = delegateWaitText({ terminal: false, drainHeldByJobs: true, runningJobCount: 4 });
  expect(one).not.toBe("");
  expect(many).not.toBe("");
  expect(one).not.toBe(many);
  // A hold with no countable job states no wait: the count is required to
  // phrase one, and an unphrased hold is not rendered at all.
  expect(delegateWaitText({ terminal: false, drainHeldByJobs: true })).toBe("");
});

// A coordinator parked in its finalization drain on open descendant subagents
// is waiting exactly as one parked on its own jobs is. The same hold gates it,
// so a running delegate with children keeps its intent: only the drain flag
// makes the count a wait.
test("a subagent hold reads as a wait, and its count phrases it", () => {
  const held = { terminal: false, drainHeldBySubagents: true, runningSubagentCount: 2 };
  const wait = delegateWaitText(held);
  expect(wait).not.toBe("");
  expect(wait).toContain("2");
  // Held outranks the frozen pre-drain intent.
  expect(delegateWorkText({ ...held, latestIntent: "Retrying the flaky search." }, "working")).toBe(wait);

  // Singular and plural are different claims about how many are still open.
  const one = delegateWaitText({ terminal: false, drainHeldBySubagents: true, runningSubagentCount: 1 });
  const many = delegateWaitText({ terminal: false, drainHeldBySubagents: true, runningSubagentCount: 4 });
  expect(one).not.toBe("");
  expect(many).not.toBe("");
  expect(one).not.toBe(many);
  expect(one).toContain("1");

  // The count alone is a delegate still working, not a wait.
  const working = { terminal: false, runningSubagentCount: 3 };
  expect(delegateWaitText(working)).toBe("");
  expect(delegateWorkText({ ...working, latestIntent: "Reading." }, "working")).toBe("Reading.");
  // A hold with nothing countable states no wait.
  expect(delegateWaitText({ terminal: false, drainHeldBySubagents: true })).toBe("");
  expect(delegateWaitText({ terminal: true, drainHeldBySubagents: true, runningSubagentCount: 2 })).toBe("");
});

// A delegate can be parked on both at once: its own shells and the descendants
// below it. Neither count may hide the other.
test("a hold on both jobs and subagents reports both counts", () => {
  const both = delegateWaitText({
    terminal: false,
    drainHeldByJobs: true,
    runningJobCount: 2,
    drainHeldBySubagents: true,
    runningSubagentCount: 3,
  });
  expect(both).not.toBe("");
  expect(both).toContain("2");
  expect(both).toContain("3");
  // It is a different claim from either half alone.
  expect(both).not.toBe(delegateWaitText({ terminal: false, drainHeldByJobs: true, runningJobCount: 2 }));
  expect(both).not.toBe(delegateWaitText({ terminal: false, drainHeldBySubagents: true, runningSubagentCount: 3 }));
});

// A caller that renders the waiting subagents expanded below the row passes
// omitSubagents so the row does not repeat them. The frozen pre-drain intent
// must NOT come back in their place: the delegate is still parked, and the
// intent stopped moving when the drain took the run.
test("omitting subagents leaves a subagent-only wait blank instead of the frozen intent", () => {
  const held = {
    terminal: false,
    drainHeldBySubagents: true,
    runningSubagentCount: 2,
    latestIntent: "Retrying the flaky search.",
  };
  expect(delegateWorkText(held, "working", { omitSubagents: true })).toBe("");

  // The jobs the caller is not rendering still show: only the subagent half is
  // omitted.
  const both = delegateWorkText({ ...held, drainHeldByJobs: true, runningJobCount: 1 }, "working", {
    omitSubagents: true,
  });
  expect(both).not.toBe("");
  expect(both).toContain("1");
  // Omitting the subagent half leaves exactly the job-only wait, whatever
  // words that wait is phrased with.
  expect(both).toBe(delegateWaitText({ terminal: false, drainHeldByJobs: true, runningJobCount: 1 }));

  // Nothing to omit when the drain holds no subagent: the intent stands.
  expect(
    delegateWorkText(
      { terminal: false, drainHeldBySubagents: false, runningSubagentCount: 0, latestIntent: "Reading." },
      "working",
      { omitSubagents: true },
    ),
  ).toBe("Reading.");
  // Nor is a terminal row affected by the option.
  expect(
    delegateWorkText(
      { terminal: true, outcome: "completed", drainHeldBySubagents: true, runningSubagentCount: 2 },
      "completed",
      { omitSubagents: true },
    ),
  ).toBe("completed");
});
