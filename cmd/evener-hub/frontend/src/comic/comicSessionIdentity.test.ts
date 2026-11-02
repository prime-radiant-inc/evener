import { hydrateThread } from "@evener/appwire-client";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { subagentOutcomesDelegatesResponse } from "@evener/appwire-client/testing/subagentWireFixtures";
import { expect, test } from "vitest";
import { comicSessionRef } from "./comicSessionIdentity";

test.each(["local", "remote-source"])("qualifies the serving session under a %s routing alias", (host) => {
  const model = hydrateThread(
    {
      thread: wireThread(`${host}:stable`, {
        id: "wire-thread",
        sessionId: "current-session",
        evener: { ...wireThread().evener, instanceId: "daemon-instance" },
      }),
    },
    `${host}:stable`,
    0,
  );
  expect(comicSessionRef(model)).toBe(`${host}:current-session`);
  expect(comicSessionRef({ ...model, instanceId: "restarted-daemon" })).toBe(`${host}:current-session`);
  const context = {
    ...subagentOutcomesDelegatesResponse().context,
    ref: `${host}:authoritative-owner`,
    sessionId: "current-session",
  };
  expect(comicSessionRef(model, context)).toBe(`${host}:authoritative-owner`);
  expect(comicSessionRef(model, { ...context, sessionId: "different-session" })).toBe(`${host}:current-session`);
  expect(comicSessionRef({ ...model, imageSessionId: "replacement-session" }, context)).toBe(
    `${host}:replacement-session`,
  );
});
