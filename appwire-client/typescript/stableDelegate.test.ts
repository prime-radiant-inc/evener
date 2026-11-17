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

// A delegate whose drain is holding its run open on its own shell says so,
// and that wait outranks the intent that stopped moving for the whole hold.
test("a row waiting on its own running job names that wait", () => {
  expect(delegateWorkText({ terminal: false, runningJobCount: 1 }, "working")).toBe("Waiting on 1 running job");
  expect(delegateWorkText({ terminal: false, runningJobCount: 3, latestIntent: "Reading." }, "working")).toBe(
    "Waiting on 3 running jobs",
  );
});

// The other direction: a row with no count says nothing new, so the status
// word and the intent keep their places and an omission never claims idleness.
test("a row with no running job keeps the status word or its intent", () => {
  expect(delegateWorkText({ terminal: false, runningJobCount: 0 }, "working")).toBe("working");
  expect(delegateWorkText({ terminal: false, runningJobCount: 0, latestIntent: "Reading." }, "working")).toBe(
    "Reading.",
  );
  expect(delegateWaitText({ terminal: false })).toBe("");
  expect(delegateWaitText({ terminal: false, runningJobCount: 2 })).toBe("Waiting on 2 running jobs");
  expect(delegateWaitText({ terminal: true, runningJobCount: 2 })).toBe("");
});
