import { describe, expect, it } from "vitest";
import type {
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
  const client: ConversationClientLike = {
    request: (_method, params) =>
      new Promise((resolve) =>
        requests.push({ params: params as NavigationReadParams, resolve }),
      ),
    onNotification: () => () => {},
  };
  return {
    requests,
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
