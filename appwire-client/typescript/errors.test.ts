// @vitest-environment node

import { describe, expect, test } from "vitest";
import appwireErrorsGo from "../../appwire/errors.go?raw";
import appwireTypesGo from "../../appwire/types.go?raw";
import {
  ClientNotReadyError,
  ConnectionClosedError,
  ErrorEndpointConflict,
  ErrorInstanceRemoveApplied,
  ErrorInstanceRenamePersisted,
  ErrorInvalidHostField,
  ErrorMarketplaceRemoveApplied,
  ErrorTranscriptHistoryFailed,
  ErrorUpgradeRequired,
  errorKind,
  errorText,
  friendlyErrorMessage,
  friendlyLaunchErrorMessage,
  hostFieldError,
  isHubLaunchError,
  isInstanceRemoveApplied,
  isTranscriptHistoryFailedError,
  isUpgradeRequiredError,
  sessionActionError,
  sessionActionHeadline,
  WireError,
  wireRejectionPayload,
} from "./errors";

function decodeUpper(value: unknown): string | undefined {
  return typeof value === "string" ? value.toUpperCase() : undefined;
}

test("wireRejectionPayload decodes the named key when the discriminator matches", () => {
  const error = new WireError("boom", -32603, { evenerErrorInfo: "somePostApply", applied: "ok" });
  expect(wireRejectionPayload(error, "somePostApply", "applied", decodeUpper)).toBe("OK");
});

test("wireRejectionPayload returns undefined for a different evenerErrorInfo - the code alone is never the discriminator", () => {
  const error = new WireError("boom", -32603, { evenerErrorInfo: "conflict", applied: "ok" });
  expect(wireRejectionPayload(error, "somePostApply", "applied", decodeUpper)).toBeUndefined();
});

test("wireRejectionPayload returns undefined for a non-WireError, or when decode rejects the payload", () => {
  expect(wireRejectionPayload(new Error("boom"), "somePostApply", "applied", decodeUpper)).toBeUndefined();
  const malformed = new WireError("boom", -32603, { evenerErrorInfo: "somePostApply", applied: 42 });
  expect(wireRejectionPayload(malformed, "somePostApply", "applied", decodeUpper)).toBeUndefined();
});

// goErrorInfo reads one ErrorInfo constant's value out of appwire/errors.go, the
// file the hub stamps every evenerErrorInfo from. Reading the source (the way
// mobile/src/services/conversation.test.ts binds its decoder literals to
// appwire/types.go) is what makes the binding real: renaming the constant or
// changing its value in Go fails here, rather than leaving every client matching
// a discriminator the hub no longer sends.
function goErrorInfo(name: string): string {
  const match = appwireErrorsGo.match(new RegExp(`${name}\\s+ErrorInfo\\s*=\\s*"([^"]+)"`));
  if (!match) throw new Error(`appwire/errors.go has no ${name} ErrorInfo constant`);
  return match[1]!;
}

test("hostFieldError reads the blamed input off the hub's own discriminant", () => {
  // The binding: the Go constant's value is what this module matches on.
  expect(ErrorInvalidHostField).toBe(goErrorInfo("ErrorInvalidHostField"));
  const blamed = new WireError('host "m4": missing ssh destination', -32602, {
    evenerErrorInfo: ErrorInvalidHostField,
    field: "address",
  });
  expect(hostFieldError(blamed)).toBe("address");
  // A plain validation refusal carries no field, a form-level refusal carries an
  // empty one, and a non-WireError carries nothing at all.
  expect(hostFieldError(new WireError("boom", -32602, { evenerErrorInfo: "invalidParams" }))).toBeUndefined();
  expect(hostFieldError(new WireError("boom", -32602, { evenerErrorInfo: ErrorInvalidHostField }))).toBeUndefined();
  expect(hostFieldError(new Error("boom"))).toBeUndefined();
});

test("the host entry's wire spellings are the ones refusals and inputs share", () => {
  // The dialog's inputs and a refusal's field are the same strings because both
  // are HostEntry's json names; this pins that vocabulary against the Go tags so
  // a rename on one side cannot leave the other looking for a missing input.
  // Each name is matched as a whole json tag — the name followed by a closing
  // quote or an omitempty comma — not as a prefix, so "addr" cannot be satisfied
  // by the "address" tag. "name" is the field hostEntryField blames for an
  // invalid or reserved host name, so it belongs in the pinned set too.
  for (const name of ["name", "address", "user", "keyPath", "evenerPath", "configPath", "addr", "roots"]) {
    expect(appwireTypesGo).toMatch(new RegExp(`json:"${name}("|,)`));
  }
});

// The persisted rename discriminator (a rename that stood in the config but
// could not finish) is the one place a client decides whether to report a
// standing write or a failed one. A hub-side rename of it must break this test,
// not silently disable recognition in the web pane, the sheet, and native.
describe("the persisted rename discriminator is bound to appwire/errors.go", () => {
  test("the exported value is the hub's own constant", () => {
    expect(ErrorInstanceRenamePersisted).toBe(goErrorInfo("ErrorInstanceRenamePersisted"));
  });
});

// The applied-removal discriminator is the removal-side sibling: a removal that
// stood but could not put back what it deleted must be reconciled by every
// client, not read as a retryable failure. A hub-side rename of it has to break
// this binding.
describe("the applied-removal discriminator is bound to appwire/errors.go", () => {
  test("the exported value is the hub's own constant", () => {
    expect(ErrorInstanceRemoveApplied).toBe(goErrorInfo("ErrorInstanceRemoveApplied"));
  });

  test("isInstanceRemoveApplied reads only that discriminator", () => {
    expect(
      isInstanceRemoveApplied(new WireError("left behind", -32603, { evenerErrorInfo: ErrorInstanceRemoveApplied })),
    ).toBe(true);
    expect(
      isInstanceRemoveApplied(new WireError("left behind", -32603, { evenerErrorInfo: ErrorInstanceRenamePersisted })),
    ).toBe(false);
    expect(isInstanceRemoveApplied(new Error("left behind"))).toBe(false);
  });
});

// The marketplace remove-applied discriminator is the marketplace-side
// sibling of the applied-removal family: a marketplace removal that stood -
// unregister and clone cleanup both done - whose response could not re-read
// the updated list. Every consumer must reconcile rather than retry, and a
// hub-side rename of it has to break this binding rather than silently read
// the standing removal as a failed one.
describe("the marketplace remove-applied discriminator is bound to appwire/errors.go", () => {
  test("the exported value is the hub's own constant", () => {
    expect(ErrorMarketplaceRemoveApplied).toBe(goErrorInfo("ErrorMarketplaceRemoveApplied"));
  });
});

// The endpoint-conflict discriminant is what separates a moved destination from
// a genuine conflict (a name collision) that shares CodeConflict. A hub-side
// rename of it must break this binding rather than silently read collisions as
// endpoint conflicts.
describe("the endpoint-conflict discriminator is bound to appwire/errors.go", () => {
  test("the exported value is the hub's own constant", () => {
    expect(ErrorEndpointConflict).toBe(goErrorInfo("ErrorEndpointConflict"));
  });
});

// A read of a thread whose history failed keeps the client's held history and
// shows one diagnostic, and a daemon refusing an old client's protocol names the
// fix. Both are recognized only by their discriminant, which must stay the hub's.
describe("the history-failed and upgrade-required discriminators are bound to appwire/errors.go", () => {
  test("the exported values are the hub's own constants", () => {
    expect(ErrorTranscriptHistoryFailed).toBe(goErrorInfo("ErrorTranscriptHistoryFailed"));
    expect(ErrorUpgradeRequired).toBe(goErrorInfo("ErrorUpgradeRequired"));
  });

  test("each predicate reads only its own discriminator", () => {
    const historyFailed = new WireError("thread history failed at entry 7", -32603, {
      evenerErrorInfo: ErrorTranscriptHistoryFailed,
      bootGeneration: "4",
      epoch: 2,
    });
    const upgradeRequired = new WireError("upgrade required", -32600, { evenerErrorInfo: ErrorUpgradeRequired });
    expect(isTranscriptHistoryFailedError(historyFailed)).toBe(true);
    expect(isTranscriptHistoryFailedError(upgradeRequired)).toBe(false);
    expect(isUpgradeRequiredError(upgradeRequired)).toBe(true);
    expect(isUpgradeRequiredError(historyFailed)).toBe(false);
    expect(isTranscriptHistoryFailedError(new Error("thread history failed at entry 7"))).toBe(false);
  });
});

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
    friendlyLaunchErrorMessage(new WireError("external launch failed: src1", -32014, { evenerErrorInfo: "hubLaunch" })),
  ).toBe("external launch failed: src1");
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
