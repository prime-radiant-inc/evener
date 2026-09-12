import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { InstanceListResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { confirmListingState } from "./reconcileListing";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const EMPTY: InstanceListResponse = { instances: [], availableProviders: [] };

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

describe("confirmListingState", () => {
  // Every read losing the race or failing leaves whatever state the store
  // already held. That leftover is not a listing the caller's write produced,
  // so matching it would report a mutation the host may never have reflected
  // (the removal path would clear a guided draft on it).
  test("a listing that never applied cannot confirm a match", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => EMPTY);
    await credentialsStore.getState().fetch();
    fake.on("evener/instance/list", () => {
      throw new Error("list denied");
    });

    const confirmed = await confirmListingState((instances) => !instances.some((instance) => instance.name === "work"));

    expect(confirmed).toBe(false);
  });

  test("an applied listing that matches confirms", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => EMPTY);
    await credentialsStore.getState().fetch();

    const confirmed = await confirmListingState((instances) => !instances.some((instance) => instance.name === "work"));

    expect(confirmed).toBe(true);
  });

  // A dropped connection makes fetch() reject before readListing's own error
  // handling (requireClient throws outside its try - see credentials.ts's
  // contract). That rejection must stay inside the retry loop: the helper's
  // contract is a boolean, and a caller steering a flow on it treats a
  // rejection as an unhandled failure rather than the "could not confirm"
  // outcome it actually is.
  test("a read that rejects is one more failed attempt, not an escape", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => EMPTY);
    await credentialsStore.getState().fetch();
    connectionStore.setState({ state: "idle", client: null });

    const confirmed = await confirmListingState(() => true);

    expect(confirmed).toBe(false);
  });
});
