import { expect, test } from "vitest";
import { errorText, isHubLaunchError, sessionActionError, sessionActionHeadline, WireError } from "./errors";

test("errorText prefers an Error's message and stringifies anything else", () => {
  expect(errorText(new Error("switch boom"))).toBe("switch boom");
  expect(errorText(new WireError("turn t1 is active", -32013, { serfErrorInfo: "conflict" }))).toBe(
    "turn t1 is active",
  );
  expect(errorText("plain string")).toBe("plain string");
  expect(errorText(404)).toBe("404");
});

test("isHubLaunchError matches only a WireError carrying the hubLaunch discriminator", () => {
  expect(isHubLaunchError(new WireError("fork/exec serf: no such file", -32014, { serfErrorInfo: "hubLaunch" }))).toBe(
    true,
  );
  // The code alone is never the discriminator - a sibling error can share it.
  expect(isHubLaunchError(new WireError("turn t1 is active", -32014, { serfErrorInfo: "conflict" }))).toBe(false);
  expect(isHubLaunchError(new WireError("no data at all", -32014))).toBe(false);
  expect(isHubLaunchError(new Error("fork/exec serf: no such file"))).toBe(false);
  expect(isHubLaunchError("hubLaunch")).toBe(false);
});

// The hub resumes a cold session behind every session mutation
// (cmd/serf-hub/app_session_resume.go's withSessionResume). When that resume
// is what died, naming the mutation sends the user debugging the wrong step.
test("sessionActionError names the resume, not the action, when the resume is what failed", () => {
  expect(
    sessionActionError(
      "Couldn't change model",
      new WireError("serf launch-check timed out", -32014, { serfErrorInfo: "hubLaunch" }),
    ),
  ).toBe("Couldn't start this session: serf launch-check timed out");
});

test("sessionActionError names the action for every other failure", () => {
  expect(sessionActionError("Couldn't change model", new Error("switch boom"))).toBe(
    "Couldn't change model: switch boom",
  );
  expect(
    sessionActionError("Couldn't set goal", new WireError("turn t1 is active", -32013, { serfErrorInfo: "conflict" })),
  ).toBe("Couldn't set goal: turn t1 is active");
});

test("sessionActionError drops the separator when the failure carries no detail", () => {
  expect(sessionActionError("Couldn't compact", new Error(""))).toBe("Couldn't compact");
  expect(sessionActionError("Couldn't compact", new WireError("", -32014, { serfErrorInfo: "hubLaunch" }))).toBe(
    "Couldn't start this session",
  );
});

// A surface that renders the headline and the detail in separate slots (an
// EmptyState's title/hint) needs the same substitution without the join, and
// must not carry its own copy of the resume's wording.
test("sessionActionHeadline picks the same headline sessionActionError would", () => {
  const launch = new WireError("serf launch-check timed out", -32014, { serfErrorInfo: "hubLaunch" });
  expect(sessionActionHeadline("Couldn't load tasks", launch)).toBe("Couldn't start this session");
  expect(sessionActionHeadline("Couldn't load tasks", new Error("tasks boom"))).toBe("Couldn't load tasks");
  expect(sessionActionError("Couldn't load tasks", launch)).toBe(
    `${sessionActionHeadline("Couldn't load tasks", launch)}: serf launch-check timed out`,
  );
});
