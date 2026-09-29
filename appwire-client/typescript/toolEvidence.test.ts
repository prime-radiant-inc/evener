// @vitest-environment node

import { expect, test } from "vitest";
import { toolWireStep } from "./testing/toolWireFixtures";
import { prettyJSON, shellOutput, skillContext, webFetchResult } from "./toolEvidence";

// Every case reads what the daemon actually sends (agent/testdata/toolwire).

test("splits a command's output from the exit footer the shell tool ends it with", () => {
  expect(shellOutput(toolWireStep("call_shell").output ?? "")).toEqual({ text: "package agent", exitCode: 0 });
  expect(shellOutput(toolWireStep("call_shell_failed").output ?? "")).toEqual({ text: "", exitCode: 1 });
  expect(shellOutput("no footer here\n")).toEqual({ text: "no footer here" });
  // The buffered environment's trailer is a footer too.
  expect(shellOutput("built\nexit_code=2 duration_ms=40 timed_out=false")).toEqual({ text: "built", exitCode: 2 });
});

test("reads a fetched page's answer, where it came from and its size", () => {
  expect(webFetchResult(toolWireStep("call_web_fetch").output ?? "")).toEqual({
    text: "The release notes list three fixes to the tree settle pass.",
    url: "https://example.com/release-notes",
    bytes: 48213,
  });
  // When both models refused, the raw content stands in for the answer.
  expect(
    webFetchResult(JSON.stringify({ fallback: "raw", content: "# Notes", url: "https://x.test", size_bytes: 7 })),
  ).toEqual({ text: "# Notes", url: "https://x.test", bytes: 7 });
  expect(webFetchResult("not json")).toBeUndefined();
});

test("reads the skill an activation loaded", () => {
  expect(skillContext(toolWireStep("call_use_skill").output ?? "")).toEqual({
    name: "systematic-debugging",
    description: "Find the root cause first",
    instructions: "# Systematic debugging\n\nFind the root cause first.\n",
  });
  expect(skillContext("# Just markdown")).toBeUndefined();
});

test("pretty-prints JSON, and nothing else", () => {
  expect(prettyJSON(toolWireStep("call_mcp").argumentsJSON ?? "")).toBe(
    '{\n  "body": "Seen in go test -race.",\n  "title": "Tree settle races the drain"\n}',
  );
  expect(prettyJSON('{"issues":[]}')).toBe('{\n  "issues": []\n}');
  expect(prettyJSON("compacted")).toBeUndefined();
  expect(prettyJSON("42")).toBeUndefined();
});
