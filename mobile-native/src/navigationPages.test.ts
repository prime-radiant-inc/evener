import { describe, expect, it } from "vitest";
import type {
  AnyNotification,
  NavigationInvalidatedPayload,
  NavigationReadParams,
  NavigationReadResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { NavigationPages } from "./navigationPages";

function boundary() {
  const requests: {
    params: NavigationReadParams;
    resolve: (value: NavigationReadResponse) => void;
  }[] = [];
  let notify: (event: AnyNotification) => void = () => {};
  const client: ConversationClientLike = {
    request: (_method, params) =>
      new Promise((resolve) =>
        requests.push({ params: params as NavigationReadParams, resolve }),
      ),
    onNotification: (listener) => {
      notify = listener;
      return () => {
        notify = () => {};
      };
    },
  };
  return {
    requests,
    invalidate: (payload: NavigationInvalidatedPayload) =>
      notify({ method: "evener/navigation/invalidated", params: payload }),
    pages: new NavigationPages<{ key: string }>(
      client,
      { resource: "catalog", catalog: "projects" },
      "projects",
      (row) => row.key,
      2,
    ),
  };
}
function response(
  keys: string[],
  remaining = 0,
  revision = 1,
): NavigationReadResponse {
  return {
    status: "ok",
    generationId: "hub-generation",
    revision,
    etag: "etag",
    data: { projects: keys.map((key) => ({ key })), remaining },
  };
}
describe("navigation pages", () => {
  it("advances by raw rows while deduplicating navigable identities", async () => {
    const { requests, pages } = boundary();
    const first = pages.refresh();
    requests[0].resolve(response(["a", "a"], 2));
    await first;
    const next = pages.more();
    void pages.more();
    expect(requests).toHaveLength(2);
    expect(requests[1].params.offset).toBe(2);
    requests[1].resolve(response(["b", "c"]));
    await next;
    expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual([
      "a",
      "b",
      "c",
    ]);
    expect(pages.getSnapshot().remaining).toBe(0);
  });
  it("refuses to combine revisions and requires refresh", async () => {
    const { requests, pages } = boundary();
    const first = pages.refresh();
    requests[0].resolve(response(["a"], 1));
    await first;
    const next = pages.more();
    requests[1].resolve(response(["b"], 0, 2));
    await next;
    expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["a"]);
    expect(pages.getSnapshot().stale).toBe(true);
    await pages.more();
    expect(requests).toHaveLength(2);
    const refresh = pages.refresh();
    requests[2].resolve(response(["b"], 0, 2));
    await refresh;
    expect(pages.getSnapshot().rows.map((row) => row.key)).toEqual(["b"]);
    expect(pages.getSnapshot().stale).toBe(false);
  });
  it("ignores an in-flight completion after leaving the binding", async () => {
    const { requests, pages } = boundary();
    const first = pages.refresh();
    pages.cancel();
    requests[0].resolve(response(["old"]));
    await first;
    expect(pages.getSnapshot().rows).toEqual([]);
    expect(pages.getSnapshot().loading).toBe(false);
  });
  it("reports malformed progress instead of looping empty pages", async () => {
    const { requests, pages } = boundary();
    const first = pages.refresh();
    requests[0].resolve(response([], 3));
    await first;
    expect(pages.getSnapshot().error).toBeTruthy();
    expect(pages.getSnapshot().remaining).toBe(0);
  });
});

it("marks relevant updates stale without replacing visible rows", async () => {
  const { requests, pages, invalidate } = boundary();
  const stop = pages.watch();
  const first = pages.refresh();
  requests[0].resolve(response(["a"], 1));
  await first;
  invalidate({
    generationId: "hub-generation",
    sequence: 1,
    targets: [{ kind: "catalog", catalog: "archived_projects", revision: 2 }],
  });
  expect(pages.getSnapshot().stale).toBe(false);
  invalidate({
    generationId: "hub-generation",
    sequence: 2,
    targets: [{ kind: "catalog", catalog: "projects", revision: 2 }],
  });
  expect(pages.getSnapshot().stale).toBe(true);
  expect(pages.getSnapshot().rows).toEqual([{ key: "a" }]);
  await pages.more();
  expect(requests).toHaveLength(1);
  const refresh = pages.refresh();
  requests[1].resolve(response(["b"], 0, 2));
  await refresh;
  expect(pages.getSnapshot().stale).toBe(false);
  stop();
  invalidate({ generationId: "other", sequence: 1, targets: [] });
  expect(pages.getSnapshot().stale).toBe(false);
});
it("does not let an old in-flight read clear a newer invalidation", async () => {
  const { requests, pages, invalidate } = boundary();
  pages.watch();
  const first = pages.refresh();
  invalidate({
    generationId: "hub-generation",
    sequence: 1,
    targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
  });
  requests[0].resolve(response(["old"], 0, 2));
  await first;
  expect(pages.getSnapshot().stale).toBe(true);
  expect(pages.getSnapshot().rows).toEqual([]);
  const refresh = pages.refresh();
  requests[1].resolve(response(["fresh"], 0, 3));
  await refresh;
  expect(pages.getSnapshot().stale).toBe(false);
});
it("accepts a read that already incorporates the notified revision", async () => {
  const { requests, pages, invalidate } = boundary();
  pages.watch();
  const first = pages.refresh();
  invalidate({
    generationId: "hub-generation",
    sequence: 1,
    targets: [{ kind: "catalog", catalog: "projects", revision: 3 }],
  });
  requests[0].resolve(response(["fresh"], 0, 3));
  await first;
  expect(pages.getSnapshot().rows).toEqual([{ key: "fresh" }]);
  expect(pages.getSnapshot().stale).toBe(false);
});
it("requires refresh after a sequence gap even with unrelated targets", async () => {
  const { requests, pages, invalidate } = boundary();
  pages.watch();
  const first = pages.refresh();
  requests[0].resolve(response(["a"]));
  await first;
  invalidate({ generationId: "hub-generation", sequence: 1, targets: [] });
  invalidate({ generationId: "hub-generation", sequence: 3, targets: [] });
  expect(pages.getSnapshot().stale).toBe(true);
});

it("establishes a restarted hub generation from an explicit refresh", async () => {
  const { requests, pages, invalidate } = boundary();
  pages.watch();
  const first = pages.refresh();
  requests[0].resolve(response(["a"], 0, 10));
  await first;
  invalidate({
    generationId: "hub-generation",
    sequence: 1,
    targets: [{ kind: "catalog", catalog: "projects", revision: 10 }],
  });
  const refresh = pages.refresh();
  requests[1].resolve({
    ...response(["new"], 0, 1),
    generationId: "restarted",
  });
  await refresh;
  expect(pages.getSnapshot().rows).toEqual([{ key: "new" }]);
  expect(pages.getSnapshot().stale).toBe(false);
});
it("rejects a response from before a generation change during the request", async () => {
  const { requests, pages, invalidate } = boundary();
  pages.watch();
  const first = pages.refresh();
  invalidate({ generationId: "restarted", sequence: 1, targets: [] });
  requests[0].resolve(response(["old"]));
  await first;
  expect(pages.getSnapshot().rows).toEqual([]);
  expect(pages.getSnapshot().stale).toBe(true);
});

it("retains truncation disclosure across appended pages and clears it on refresh", async () => {
  const { requests, pages } = boundary();
  const first = pages.refresh();
  const partial = response(["a"], 1);
  partial.data = { ...(partial.data as object), truncated: true };
  requests[0].resolve(partial);
  await first;
  expect(pages.getSnapshot().truncated).toBe(true);
  const next = pages.more();
  requests[1].resolve(response(["b"]));
  await next;
  expect(pages.getSnapshot().truncated).toBe(true);
  const refresh = pages.refresh();
  requests[2].resolve(response(["c"]));
  await refresh;
  expect(pages.getSnapshot().truncated).toBe(false);
});
