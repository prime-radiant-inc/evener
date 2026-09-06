import { describe, expect, it } from "vitest";
import type {
  ThreadListParams,
  ThreadListResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { createRosterService } from "../../mobile/src/services/roster";
import { RosterSearch } from "./rosterSearch";

function boundary() {
  const requests: {
    params: ThreadListParams;
    resolve: (result: ThreadListResponse) => void;
    reject: (cause: Error) => void;
  }[] = [];
  const client: ConversationClientLike = {
    request: (_method, params) =>
      new Promise((resolve, reject) =>
        requests.push({ params: params as ThreadListParams, resolve, reject }),
      ),
    onNotification: () => () => {},
  };
  return {
    requests,
    search: new RosterSearch(createRosterService(client, 50)),
  };
}
describe("roster search", () => {
  it("ignores an older query completing after a newer one", async () => {
    const { requests, search } = boundary();
    const first = search.load("old");
    const second = search.load("new");
    expect(requests.map((r) => r.params.searchTerm)).toEqual(["old", "new"]);
    requests[1].resolve({ data: [] });
    await second;
    requests[0].reject(new Error("old failed"));
    await first;
    expect(search.getSnapshot()).toMatchObject({
      query: "new",
      loading: false,
      error: null,
    });
  });
  it("invalidates in-flight work when leaving a hub", async () => {
    const { requests, search } = boundary();
    const pending = search.load("query");
    search.cancel();
    requests[0].reject(new Error("connection closed"));
    await pending;
    expect(search.getSnapshot()).toMatchObject({ loading: false, error: null });
  });
  it("retries the same query explicitly after failure", async () => {
    const { requests, search } = boundary();
    const pending = search.load(" query ");
    requests[0].reject(new Error("offline"));
    await pending;
    expect(search.getSnapshot().error).toBeTruthy();
    const retry = search.load();
    expect(requests[1].params.searchTerm).toBe("query");
    requests[1].resolve({ data: [] });
    await retry;
    expect(search.getSnapshot().error).toBeNull();
  });
});
