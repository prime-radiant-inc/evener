// @vitest-environment node

import { expect, test } from "vitest";
import { toolWireStep } from "./testing/toolWireFixtures";
import { prettyJSON, shellOutput, skillContext, webFetchResult } from "./toolEvidence";

// Every case reads what the daemon actually sends (agent/testdata/toolwire).

test("splits a command's output from the exit footer the shell tool ends it with", () => {
  expect(shellOutput(toolWireStep("call_shell").output ?? "")).toEqual({ text: "package agent", exitCode: 0 });
  expect(shellOutput(toolWireStep("call_shell_failed").output ?? "")).toEqual({ text: "", exitCode: 1 });
  expect(shellOutput("no footer here\n")).toEqual({ text: "no footer here" });
});

test("reads a windowed output's multi-part footer", () => {
  const windowed = shellOutput(toolWireStep("call_shell_windowed").output ?? "");
  expect(windowed.exitCode).toBe(0);
  expect(windowed.text.endsWith("3000")).toBe(true);
  expect(windowed.text).not.toContain("output windowed");
});

test("reads a command whose foreground wait timed out, still running in the background", () => {
  const promoted = shellOutput(toolWireStep("call_shell_timeout").output ?? "");
  expect(promoted).toEqual({ text: "started", timedOut: true, stillRunning: true });
});

test("leaves out what the registry appends after the footer", () => {
  const nudged = "ok\n[exit 0]\n\nYou have now made this same call and received the identical result 3 times in a row.";
  expect(shellOutput(nudged)).toEqual({ text: "ok", exitCode: 0 });
});

test("reads a footer after CRLF line endings, and mixed stdout and stderr", () => {
  expect(shellOutput("out\r\nerr\r\n[exit 3]\r\n")).toEqual({ text: "out\nerr", exitCode: 3 });
  expect(shellOutput("out\nwarning: deprecated [x]\nerr\n[exit 2 · timed out]")).toEqual({
    text: "out\nwarning: deprecated [x]\nerr",
    exitCode: 2,
    timedOut: true,
  });
});

test("strips only the final [ERROR: …] block, never one the command printed", () => {
  expect(
    shellOutput(
      "x\n[ERROR: mine]\nmiddle\n[ERROR: Command timed out after 300ms. Partial output is shown above.]\nexit_code=-1 duration_ms=1 timed_out=true\n",
    ),
  ).toEqual({ text: "x\n[ERROR: mine]\nmiddle", exitCode: -1, timedOut: true });
});

test("reads a footer's windowed output and a runtime-limit stop as what they are", () => {
  expect(shellOutput(toolWireStep("call_shell_windowed").output ?? "").windowed).toBe(true);
  expect(
    shellOutput(
      "[stopped by evener's runtime limit (max_runtime_ms) — not a command failure · no output before the limit — it may still have been working, e.g. compiling]",
    ),
  ).toEqual({ text: "", timedOut: true });
  expect(shellOutput("[no output before the limit — it may still have been working]")).toEqual({
    text: "",
    timedOut: true,
  });
});

test("keeps an [ERROR: …] line the command printed at its end", () => {
  expect(shellOutput("app out\n[ERROR: real application error]\nexit_code=1 duration_ms=5 timed_out=false\n")).toEqual({
    text: "app out\n[ERROR: real application error]",
    exitCode: 1,
  });
  // The environment's own cancel block is its own.
  expect(
    shellOutput(
      "part\n[ERROR: Command was canceled before completion. Partial output is shown above.]\nexit_code=-1 duration_ms=5 timed_out=false\n",
    ),
  ).toEqual({ text: "part", exitCode: -1 });
});

test("leaves out an appended intervention that holds blank lines of its own", () => {
  const nudged = "ok\n[exit 0]\n\nYou keep making this call.\n\nChange your approach.";
  expect(shellOutput(nudged)).toEqual({ text: "ok", exitCode: 0 });
});

test("reads a directly backgrounded command as still running, not timed out", () => {
  expect(shellOutput("started\n[running in background as job_x]")).toEqual({ text: "started", stillRunning: true });
});

test("reads the buffered environment's trailer and its timeout error", () => {
  expect(shellOutput("built\nexit_code=2 duration_ms=40 timed_out=false\n")).toEqual({ text: "built", exitCode: 2 });
  expect(
    shellOutput(
      "partial\n[ERROR: Command timed out after 300ms. Partial output is shown above.\nYou can retry with a longer timeout by setting the max_runtime_ms parameter.]\nexit_code=-1 duration_ms=301 timed_out=true\n",
    ),
  ).toEqual({ text: "partial", exitCode: -1, timedOut: true });
  // A command's own "exit_code=" line mid-output is output, not a trailer.
  expect(shellOutput("exit_code=5 duration_ms=1 timed_out=false\nmore\n")).toEqual({
    text: "exit_code=5 duration_ms=1 timed_out=false\nmore",
  });
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
  // JSON that isn't web_fetch's result.
  expect(webFetchResult('{"foo":1}')).toBeUndefined();
});

test("reads the skill an activation loaded", () => {
  expect(skillContext(toolWireStep("call_use_skill").output ?? "")).toEqual({
    name: "systematic-debugging",
    description: "Find the root cause first",
    instructions: "# Systematic debugging\n\nFind the root cause first.\n",
  });
  expect(skillContext("# Just markdown")).toBeUndefined();
  // An intervention the registry appended after a blank line is not the skill.
  const nudged = `${toolWireStep("call_use_skill").output ?? ""}\n\nYou have now made this same call and received the identical result 2 times in a row.`;
  expect(skillContext(nudged)?.name).toBe("systematic-debugging");
});

test("pretty-prints JSON, and nothing else", () => {
  expect(prettyJSON(toolWireStep("call_mcp").argumentsJSON ?? "")).toBe(
    '{\n  "body": "Seen in go test -race.",\n  "title": "Tree settle races the drain"\n}',
  );
  expect(prettyJSON('{"issues":[]}')).toBe('{\n  "issues": []\n}');
  expect(prettyJSON("compacted")).toBeUndefined();
  expect(prettyJSON("42")).toBeUndefined();
});
