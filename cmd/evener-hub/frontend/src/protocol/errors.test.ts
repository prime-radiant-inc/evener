// @vitest-environment node

import { expect, test } from "vitest";
import {
  ClientNotReadyError,
  ConnectionClosedError,
  errorKind,
  errorText,
  friendlyErrorMessage,
  friendlyLaunchErrorMessage,
  isHubLaunchError,
  sessionActionError,
  sessionActionHeadline,
  WireError,
} from "./errors";

test("errorText prefers an Error's message and stringifies anything else", () => {
  expect(errorText(new Error("switch boom"))).toBe("switch boom");
  expect(errorText(new WireError("turn t1 is active", -32013, { evenerErrorInfo: "conflict" }))).toBe(
    "turn t1 is active",
  );
  expect(errorText("plain string")).toBe("plain string");
  expect(errorText(404)).toBe("404");
});

test("isHubLaunchError matches only a WireError carrying the hubLaunch discriminator", () => {
  expect(
    isHubLaunchError(new WireError("fork/exec evener: no such file", -32014, { evenerErrorInfo: "hubLaunch" })),
  ).toBe(true);
  // The code alone is never the discriminator - a sibling error can share it.
  expect(isHubLaunchError(new WireError("turn t1 is active", -32014, { evenerErrorInfo: "conflict" }))).toBe(false);
  expect(isHubLaunchError(new WireError("no data at all", -32014))).toBe(false);
  expect(isHubLaunchError(new Error("fork/exec evener: no such file"))).toBe(false);
  expect(isHubLaunchError("hubLaunch")).toBe(false);
});

// The hub resumes a cold session behind every session mutation
// (cmd/evener-hub/app_session_resume.go's withSessionResume). When that resume
// is what died, naming the mutation sends the user debugging the wrong step.
test("sessionActionError names the resume, not the action, when the resume is what failed", () => {
  expect(
    sessionActionError(
      "Couldn't change model",
      new WireError("evener launch-check timed out", -32014, { evenerErrorInfo: "hubLaunch" }),
    ),
  ).toBe("Couldn't start this session: evener launch-check timed out");
});

test("sessionActionError names the action for every other failure", () => {
  expect(sessionActionError("Couldn't change model", new Error("switch boom"))).toBe(
    "Couldn't change model: switch boom",
  );
  expect(
    sessionActionError(
      "Couldn't set goal",
      new WireError("turn t1 is active", -32013, { evenerErrorInfo: "conflict" }),
    ),
  ).toBe("Couldn't set goal: turn t1 is active");
});

test("sessionActionError drops the separator when the failure carries no detail", () => {
  expect(sessionActionError("Couldn't compact", new Error(""))).toBe("Couldn't compact");
  expect(sessionActionError("Couldn't compact", new WireError("", -32014, { evenerErrorInfo: "hubLaunch" }))).toBe(
    "Couldn't start this session",
  );
});

// A surface that renders the headline and the detail in separate slots (an
// EmptyState's title/hint) needs the same substitution without the join, and
// must not carry its own copy of the resume's wording.
test("sessionActionHeadline picks the same headline sessionActionError would", () => {
  const launch = new WireError("evener launch-check timed out", -32014, { evenerErrorInfo: "hubLaunch" });
  expect(sessionActionHeadline("Couldn't load tasks", launch)).toBe("Couldn't start this session");
  expect(sessionActionHeadline("Couldn't load tasks", new Error("tasks boom"))).toBe("Couldn't load tasks");
  expect(sessionActionError("Couldn't load tasks", launch)).toBe(
    `${sessionActionHeadline("Couldn't load tasks", launch)}: evener launch-check timed out`,
  );
});

// friendlyErrorMessage is the one conversion every user-facing error display
// must go through: a WireError's message came from the hub and is meant to
// be read, but AppwireClient's own internal rejections (thrown client-side
// when a request is attempted against a socket that isn't open) are
// implementation detail that must never reach the screen - see client.ts's
// request() and close(). Both the real client and testing/fakeClient.ts's
// stand-in phrase these identically ("<Name>Client: cannot call ... while
// state is ..." / "; not connected"), so the match is on that shape, not on
// a specific class.
test("friendlyErrorMessage keeps a WireError's own message untouched", () => {
  expect(friendlyErrorMessage(new WireError("turn t1 is active", -32013, { evenerErrorInfo: "conflict" }))).toBe(
    "turn t1 is active",
  );
});

test("friendlyErrorMessage falls back to a generic sentence for a WireError with no message text", () => {
  expect(friendlyErrorMessage(new WireError("", -32013))).toBe("Something went wrong.");
});

test("friendlyErrorMessage maps ConnectionClosedError to a plain sentence naming the hub, not the class", () => {
  expect(friendlyErrorMessage(new ConnectionClosedError("AppwireClient: closed"))).toBe(
    "Can't reach the hub right now.",
  );
});

test("friendlyErrorMessage maps AppwireClient's cannot-call-while-closed rejection to the same plain sentence", () => {
  expect(friendlyErrorMessage(new Error('AppwireClient: cannot call "model/list" while state is "closed"'))).toBe(
    "Can't reach the hub right now.",
  );
  expect(friendlyErrorMessage(new Error('AppwireClient: cannot call "thread/start" while state is "connecting"'))).toBe(
    "Can't reach the hub right now.",
  );
  expect(friendlyErrorMessage(new Error('AppwireClient: cannot call "thread/start"; not connected'))).toBe(
    "Can't reach the hub right now.",
  );
});

// ClientNotReadyError (stores/threads.ts's requireReadyClient, issue #195's
// RCA) fires only after a caller-side bounded wait for a ready client is
// exhausted - a genuinely, not just momentarily, unreachable hub. It must
// route through the same friendly sentence as every other client-unreachable
// shape, not leak its own "timed out waiting for a ready client after
// 15000ms" text to a person.
test("friendlyErrorMessage maps ClientNotReadyError to the same hub-unreachable sentence", () => {
  expect(
    friendlyErrorMessage(new ClientNotReadyError("threads store: timed out waiting for a ready client after 15000ms")),
  ).toBe("Can't reach the hub right now.");
});

test("friendlyErrorMessage recognizes the same shape from FakeClient, tests' own stand-in", () => {
  expect(friendlyErrorMessage(new Error('FakeClient: cannot call "thread/start" while state is "closed"'))).toBe(
    "Can't reach the hub right now.",
  );
});

test("friendlyErrorMessage recognizes the closed-client shape even as a bare string, not just an Error", () => {
  expect(friendlyErrorMessage('AppwireClient: cannot call "model/list" while state is "closed"')).toBe(
    "Can't reach the hub right now.",
  );
});

test("friendlyErrorMessage never leaks a class name or method name for an unrelated internal error", () => {
  const message = friendlyErrorMessage(new TypeError("Cannot read properties of undefined (reading 'foo')"));
  expect(message).toBe("Something went wrong.");
  expect(message).not.toMatch(/TypeError|properties of undefined/);
});

test("friendlyErrorMessage gives every other unknown rejection the same generic sentence", () => {
  expect(friendlyErrorMessage("plain string failure")).toBe("Something went wrong.");
  expect(friendlyErrorMessage(404)).toBe("Something went wrong.");
  expect(friendlyErrorMessage(undefined)).toBe("Something went wrong.");
});

// errorKind is the classification spawn/model-picker call sites use to tell
// a dead hub connection apart from a hub that answered but couldn't reach
// the target project's agent daemon (T3: the first-run worst moment).
test("errorKind classifies a closed/not-yet-open socket as hub-unreachable", () => {
  expect(errorKind(new ConnectionClosedError("AppwireClient: closed"))).toBe("hub-unreachable");
  expect(errorKind(new Error('AppwireClient: cannot call "model/list" while state is "closed"'))).toBe(
    "hub-unreachable",
  );
});

test("errorKind classifies ClientNotReadyError's ready-wait timeout as hub-unreachable too", () => {
  expect(errorKind(new ClientNotReadyError("threads store: timed out waiting for a ready client after 15000ms"))).toBe(
    "hub-unreachable",
  );
});

test("errorKind classifies the hubLaunch WireError family as daemon-missing", () => {
  expect(errorKind(new WireError("fork/exec evener: no such file", -32014, { evenerErrorInfo: "hubLaunch" }))).toBe(
    "daemon-missing",
  );
  expect(errorKind(new WireError("evener launch-check timed out", -32014, { evenerErrorInfo: "hubLaunch" }))).toBe(
    "daemon-missing",
  );
});

test("errorKind classifies every other WireError as server", () => {
  expect(errorKind(new WireError("turn t1 is active", -32013, { evenerErrorInfo: "conflict" }))).toBe("server");
  expect(errorKind(new WireError("no data at all", -32014))).toBe("server");
});

test("errorKind classifies anything else as unknown", () => {
  expect(errorKind(new Error("switch boom"))).toBe("unknown");
  expect(errorKind("plain string failure")).toBe("unknown");
  expect(errorKind(undefined)).toBe("unknown");
});

// friendlyLaunchErrorMessage is friendlyErrorMessage plus actionable copy for
// the daemon-missing family - everything else passes through unchanged.
test("friendlyLaunchErrorMessage gives the daemon-missing family actionable copy instead of the launch-check's raw text", () => {
  expect(
    friendlyLaunchErrorMessage(
      new WireError("evener launch-check timed out", -32014, { evenerErrorInfo: "hubLaunch" }),
    ),
  ).toBe("No agent daemon responded for this project. Start one by running evener in the repo, then retry.");
});

test("friendlyLaunchErrorMessage passes a hubLaunch config/credentials message through untouched", () => {
  // These launch failures carry their own actionable instructions - masking
  // them with the daemon guidance sends the user to fix the WRONG thing
  // (live repro: a credentialed daemon, an uncredentialed default provider).
  expect(
    friendlyLaunchErrorMessage(
      new WireError(
        "provider credentials missing for openai: set via evener/auth/apiKey/set or set the matching env var",
        -32014,
        { evenerErrorInfo: "hubLaunch" },
      ),
    ),
  ).toBe("provider credentials missing for openai: set via evener/auth/apiKey/set or set the matching env var");
  expect(
    friendlyLaunchErrorMessage(
      new WireError("model is not configured for Evener launch: openai/gpt-5.5", -32014, {
        evenerErrorInfo: "hubLaunch",
      }),
    ),
  ).toBe("model is not configured for Evener launch: openai/gpt-5.5");
  expect(
    friendlyLaunchErrorMessage(
      new WireError("model provider is not reported by the Evener launch harness: openai", -32014, {
        evenerErrorInfo: "hubLaunch",
      }),
    ),
  ).toBe("model provider is not reported by the Evener launch harness: openai");
});

test("friendlyLaunchErrorMessage masks only the no-diagnosis subset with the guidance copy", () => {
  for (const raw of [
    "evener launch-check timed out",
    "evener launch-check canceled",
    "fork/exec evener: no such file or directory",
  ]) {
    expect(friendlyLaunchErrorMessage(new WireError(raw, -32014, { evenerErrorInfo: "hubLaunch" }))).toBe(
      "No agent daemon responded for this project. Start one by running evener in the repo, then retry.",
    );
  }
});

test("friendlyLaunchErrorMessage preserves the daemon's own stderr (the hub propagates it on purpose)", () => {
  // Mirrors cmd/evener-hub/app_rpc_test.go's stderr-propagation fixture: a
  // daemon that SPAWNED and crashed carries its diagnosis in the message,
  // and 'run evener in the repo' would reproduce the same crash silently.
  const stderr =
    'daemon spawn failed: process exited before rendezvous: exit status 1: evener serve: session creation: plugin initialization: resolving plugin dir "/Users/jesse/x": lstat /Users: no such file or directory';
  expect(friendlyLaunchErrorMessage(new WireError(stderr, -32014, { evenerErrorInfo: "hubLaunch" }))).toBe(stderr);
  expect(
    friendlyLaunchErrorMessage(
      new WireError("evener launch-check failed: boom", -32014, { evenerErrorInfo: "hubLaunch" }),
    ),
  ).toBe("evener launch-check failed: boom");
});

test("friendlyLaunchErrorMessage passes wrapped and resume-advice messages through", () => {
  const wrapped =
    "session s1 is still held by live daemon pid 42. Stop it and resume again. Replacement spawn failed: provider credentials missing for openai: set via evener/auth/apiKey/set or set the matching env var";
  expect(friendlyLaunchErrorMessage(new WireError(wrapped, -32014, { evenerErrorInfo: "hubLaunch" }))).toBe(wrapped);
  expect(
    friendlyLaunchErrorMessage(
      new WireError("codex launch not configured: src1", -32014, { evenerErrorInfo: "hubLaunch" }),
    ),
  ).toBe("codex launch not configured: src1");
});

test("friendlyLaunchErrorMessage keeps the hub-unreachable message for a closed connection", () => {
  expect(friendlyLaunchErrorMessage(new ConnectionClosedError("AppwireClient: closed"))).toBe(
    "Can't reach the hub right now.",
  );
});

test("friendlyLaunchErrorMessage keeps every other WireError's own message untouched", () => {
  expect(friendlyLaunchErrorMessage(new WireError("turn t1 is active", -32013, { evenerErrorInfo: "conflict" }))).toBe(
    "turn t1 is active",
  );
});

test("friendlyLaunchErrorMessage gives an unknown rejection the same generic sentence friendlyErrorMessage would", () => {
  expect(friendlyLaunchErrorMessage(new Error("switch boom"))).toBe("Something went wrong.");
});
