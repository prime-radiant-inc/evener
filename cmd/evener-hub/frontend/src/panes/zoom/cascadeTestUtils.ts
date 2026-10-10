import type { SessionActivityContext, ThreadReadResponse } from "@evener/appwire-client";
import {
  activityClient,
  activityContext,
  activitySummary,
  activityThread,
} from "../../stores/sessionActivityTestUtils";

export function cascadeClient(context: (ref: string) => SessionActivityContext) {
  const client = activityClient();
  client.on("thread/read", ({ ref, requestGeneration, includeTurns }) => {
    if (!ref) throw new Error("Missing thread ref");
    return cascadeThread(ref, requestGeneration, includeTurns !== false);
  });
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    context: context(ref),
  }));
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: context(ref),
    scope: scope ?? "session",
    delegates: [],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: context(ref),
    scope: scope ?? "session",
    jobs: [],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: context(ref),
    scope: scope ?? "session",
    watches: [],
    page: { complete: true, issues: [] },
  }));
  return client;
}

export function cascadeContext(
  ref: string,
  ancestors: readonly string[],
  title = (value: string) => value,
): SessionActivityContext {
  return {
    ...activityContext(ref),
    sessionId: `session-${ref}`,
    rootRef: ancestors[0] ?? ref,
    ...(ancestors.length ? { parentRef: ancestors[ancestors.length - 1], delegateId: `edge-${ref}` } : {}),
    ancestors: ancestors.map((ancestor, index) => ({
      ref: ancestor,
      sessionId: `session-${ancestor}`,
      title: title(ancestor),
      ...(index ? { delegateId: `edge-${ancestor}` } : {}),
    })),
  };
}

export function cascadeThread(
  ref: string,
  requestGeneration?: number,
  includeTurns = true,
  name = ref,
): ThreadReadResponse {
  const read = activityThread(ref);
  return {
    ...read,
    requestGeneration,
    thread: {
      ...read.thread,
      id: `wire-${ref}`,
      sessionId: `session-${ref}`,
      name,
      turns: includeTurns
        ? [
            {
              id: `turn-${ref}`,
              status: "completed",
              itemsView: "full",
              items: [
                {
                  id: `message-${ref}`,
                  turnId: `turn-${ref}`,
                  type: "userMessage",
                  text: `${ref} real content`,
                  status: "completed",
                },
              ],
            },
          ]
        : [],
    },
  };
}
