import { expect, test } from "vitest";
import { delegateWorkText } from "./stableDelegate";

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
