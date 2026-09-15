import { parseConditionText } from "@evener/appwire-client";
import { expect, test } from "vitest";
import { watchTriggerPhrases } from "./watchConditionPhrase";

// The watch list row and the watch card both render these phrases; this table
// is what keeps the card saying what the list says. The card drifted to
// "events *" where the list said "any event" once already, which is the bug
// one composer prevents.
//
// Conditions are built as parsed shapes rather than condition strings: the
// grammar has its own coverage in the AppWire package, and the vocabulary is
// what is under test here.

test("a bare timer is one humanized phrase", () => {
  expect(watchTriggerPhrases({ events: [], afterSeconds: 300 })).toEqual({ timer: "in 5m", bits: [] });
  expect(watchTriggerPhrases({ events: [], repeatSeconds: 120 })).toEqual({ timer: "every 2m", bits: [] });
});

test("a pattern leads the phrases, quoted and unlabelled", () => {
  expect(watchTriggerPhrases({ events: [], outputMatch: "ready" })).toEqual({ bits: ["“ready”"] });
});

test("events render their labels, with the throttle once", () => {
  expect(watchTriggerPhrases({ events: ["error", "ok"], every: 3 })).toEqual({ bits: ["error, ok (every 3)"] });
  // The wildcard reads as words on every surface, never as a bare "*".
  expect(watchTriggerPhrases({ events: ["*"] })).toEqual({ bits: ["any event"] });
});

test("the throttle rides the filter phrase when no events render", () => {
  expect(watchTriggerPhrases({ events: [], every: 3, filterToolName: "bash", filterStatus: "error" })).toEqual({
    bits: ["failed tool calls on bash (every 3)"],
  });
});

test("each filter shape names itself in words, never its raw keys", () => {
  expect(watchTriggerPhrases({ events: [], filterStatus: "ok" }).bits).toEqual(["successful tool calls"]);
  expect(watchTriggerPhrases({ events: [], filterToolName: "bash" }).bits).toEqual(["calls on bash"]);
  expect(watchTriggerPhrases({ events: [], filterStatus: "error" }).bits).toEqual(["failed tool calls"]);
});

test("the heartbeat files last, as its own phrase", () => {
  expect(watchTriggerPhrases({ events: [], outputMatch: "ready", progressIntervalMS: 120_000 })).toEqual({
    bits: ["“ready”", "every 2m"],
  });
});

test("a condition with no trigger clauses yields no phrases", () => {
  expect(watchTriggerPhrases(parseConditionText("note: Follow up after the release"))).toEqual({ bits: [] });
});
