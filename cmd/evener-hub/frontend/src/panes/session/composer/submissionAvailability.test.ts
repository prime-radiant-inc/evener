import type { ThreadModel } from "@evener/appwire-client";
import { expect, test } from "vitest";
import { composerAvailability } from "./submissionAvailability";

// Only these model fields enter the existing browser gate.
const idle = { status: { type: "idle" }, capabilities: { send: true, queue: true } } as ThreadModel;

test.each([
  ["idle Send", idle, false, false, {}, { canSend: true, canQueue: false }],
  ["active Queue", { ...idle, status: { type: "active" } }, false, false, {}, { canSend: false, canQueue: true }],
  ["pending send queues", idle, true, false, {}, { canSend: false, canQueue: true }],
  [
    "shutdown send-driven resume",
    { ...idle, status: { type: "notLoaded" } },
    false,
    true,
    { mutationStateAuthoritative: true },
    { canSend: true, canQueue: false },
  ],
  [
    "owner-retained uncertainty refuses",
    { ...idle, status: { type: "active" }, parentRef: "parent" },
    false,
    true,
    { uncertainMessages: true },
    { canSend: false, canQueue: false },
  ],
  ["Stop in flight refuses live Send", idle, false, false, { stopInFlight: true }, { canSend: false, canQueue: false }],
] as const)("%s", (_name, target, pending, obligated, signals, expected) => {
  expect(composerAvailability(target, pending, obligated, signals)).toEqual(expected);
});
