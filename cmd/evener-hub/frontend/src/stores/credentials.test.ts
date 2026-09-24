import type {
  AuthStatusResponse,
  AuthTestResponse,
  HostForwardedResult,
  HostRequestParams,
  HostRow,
  InstanceEntry,
  InstanceListResponse,
} from "@evener/appwire-client";
import { CONNECTION_REPLACED_ERROR, WireError } from "@evener/appwire-client";
import { deferRequest, FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { threadStartedNotification } from "@evener/appwire-client/testing/notifications";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import {
  credentialsStore,
  fetchHost,
  hostInstancesStore,
  hostPartition,
  isStaleListingRefusal,
  resetCredentialsStoreForTests,
  resetHostInstancesForTests,
  StaleListingRefusal,
  staleListingHeld,
  useCredentialsStore,
  useHostInstances,
} from "./credentials";
import { hostsStore } from "./hosts";
import { setMutationClientIdentityForTests } from "./mutationClientIdentity";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const ONE_INSTANCE: InstanceEntry = {
  name: "work",
  providerId: "openai-codex",
  protocol: "openai-responses",
  auth: "oauth-openai-codex",
  isDefault: true,
  implicit: false,
  authModes: ["oauth"],
  activeSource: "oauth",
  hasStoredFile: false,
  hasStoredOAuth: true,
  envVar: "",
  storedEmail: "me@example.com",
  credentialRequired: true,
};

const LIST_RESPONSE: InstanceListResponse = {
  instances: [ONE_INSTANCE],
  availableProviders: [
    { id: "anthropic", protocol: "anthropic", auth: "bearer", implicit: true },
    { id: "openai-codex", protocol: "openai-responses", auth: "oauth-openai-codex", implicit: true },
  ],
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  resetHostInstancesForTests();
  // The auth mutations carry the page's identity on the wire now, so the
  // assertions that pin their exact params need one that cannot vary.
  setMutationClientIdentityForTests("test-tab");
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cleanup();
});

describe("fetch", () => {
  test("a late read cannot overwrite credentials from a newer refresh", async () => {
    const client = connectFakeClient();
    let resolveOld: (value: InstanceListResponse) => void = () => {};
    const oldResponse = new Promise<InstanceListResponse>((resolve) => {
      resolveOld = resolve;
    });
    client.on("evener/instance/list", () => oldResponse);
    const oldRead = credentialsStore.getState().fetch();
    await Promise.resolve();
    client.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    resolveOld({ instances: [], availableProviders: [] });
    await oldRead;
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });
  test("disconnecting releases an interrupted fetch and ready reloads the list", async () => {
    const fake = connectFakeClient();
    let finishOld!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishOld = resolve;
        }),
    );
    const old = credentialsStore.getState().fetch();
    await Promise.resolve();
    fake.emitStateChange("reconnecting");
    expect(credentialsStore.getState().loading).toBe(false);
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.emitReady();
    await Promise.resolve();
    await Promise.resolve();
    finishOld({ instances: [], availableProviders: [] });
    await old;
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    expect(credentialsStore.getState().loading).toBe(false);
  });

  test("throws if no client is connected", async () => {
    await expect(credentialsStore.getState().fetch()).rejects.toThrow(/no client connected/);
  });

  test("a replaced connection marks the held listing, and its own read clears it", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false);

    // The client is replaced: the listing still held was read on the one that
    // went away, and this one has not answered with its own yet.
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => LIST_RESPONSE);
    connectionStore.getState().connect(replacement);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);

    // This connection's own read is what clears the mark.
    await credentialsStore.getState().fetch();
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false);
  });

  test("a write issued while the held listing is stale is refused until this connection's read lands", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    // A replacement whose own read is held open: the rows still on screen name
    // instances and endpoints of the connection that went away, so a write
    // issued from them is refused rather than submitted to a connection that
    // never read them.
    let finishRestore!: (value: InstanceListResponse) => void;
    const replacement = new FakeClient("ready");
    replacement.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRestore = resolve;
        }),
    );
    replacement.on("evener/instance/setDefault", () => LIST_RESPONSE);
    connectionStore.getState().connect(replacement);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);

    await expect(credentialsStore.getState().setDefault("work")).rejects.toThrow(/replaced/);
    expect(replacement.calls.filter((call) => call.method === "evener/instance/setDefault")).toHaveLength(0);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);

    // The refusal is not sticky: once this connection's own listing lands, the
    // same write goes through.
    finishRestore(LIST_RESPONSE);
    await vi.waitFor(() => expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false));
    await credentialsStore.getState().setDefault("work");
    expect(replacement.calls.filter((call) => call.method === "evener/instance/setDefault")).toHaveLength(1);
  });

  // roborev round 5: the mark means "these rows were read by a connection that
  // is gone". A same-client transition - a transport flap to reconnecting, a
  // failed read's error state - is not that: the rows were read by the client
  // still wired, and the actions that would act on them are the user's to
  // retry once the client is ready again. Marking it stale refused those
  // actions with a message claiming the connection had been replaced.
  test("a same-client connection-state transition does not mark the held listing stale", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/instance/setDefault", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    fake.emitStateChange("reconnecting");
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false);
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);

    // Once the client is ready again the rows are still this client's, so a
    // write from them is issued rather than refused as a replaced
    // connection's - and the reconnect's own read is what the retry lands on.
    fake.emitReady();
    await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "evener/instance/list").length).toBe(2));
    await credentialsStore.getState().setDefault("work");
    expect(fake.calls.filter((call) => call.method === "evener/instance/setDefault")).toHaveLength(1);
  });

  // The refusal's own words are user-facing: a caller that cannot tell it apart
  // from any other failure shows this text, so it must not name the store's
  // internals.
  test("the stale-listing refusal carries the same words every surface shows", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => new Promise<InstanceListResponse>(() => {}));
    connectionStore.getState().connect(replacement);

    const refusal = await credentialsStore
      .getState()
      .setDefault("work")
      .then(
        () => undefined,
        (err: unknown) => err as Error,
      );
    expect(isStaleListingRefusal(refusal)).toBe(true);
    expect(refusal?.message).toBe(CONNECTION_REPLACED_ERROR);
  });

  // The refusal and the surfaces that gate their controls on it have to agree:
  // a stale refusal needs rows on screen that describe the connection that is
  // gone. A connection that holds no listing - a fresh client, a first read
  // that failed - has nothing stale to act on, so both allow the action.
  test("staleListingHeld names the condition the refusal and the gating surfaces share", () => {
    expect(staleListingHeld({ instances: [], availableProviders: [], listingFromPreviousConnection: true })).toBe(
      false,
    );
    expect(staleListingHeld({ instances: [], availableProviders: [], listingFromPreviousConnection: false })).toBe(
      false,
    );
    expect(
      staleListingHeld({ instances: [ONE_INSTANCE], availableProviders: [], listingFromPreviousConnection: false }),
    ).toBe(false);
    expect(
      staleListingHeld({ instances: [ONE_INSTANCE], availableProviders: [], listingFromPreviousConnection: true }),
    ).toBe(true);
    expect(
      staleListingHeld({
        instances: [],
        availableProviders: [{ id: "anthropic", protocol: "anthropic", auth: "bearer", implicit: true }],
        listingFromPreviousConnection: true,
      }),
    ).toBe(true);
  });

  test("a credential test issued while the held listing is stale is refused with a refusal callers can tell apart", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    // A probe asserts the row's endpoint and dials whatever the name resolves
    // to now (app_credentials.go), so a test issued from the previous
    // connection's listing is refused exactly as a write is. The refusal is
    // exported as its own error type because what it asks for is a listing
    // re-read and a retry, and a caller that cannot tell it apart reports a
    // failure no user action resolves - the raw store message, or a test
    // result claiming an endpoint could not be reached.
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => new Promise<InstanceListResponse>(() => {}));
    replacement.on("evener/auth/test", () => ({ provider: "work", status: "success", message: "" }));
    connectionStore.getState().connect(replacement);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);

    const refusal = await credentialsStore
      .getState()
      .testCredentials("work", "fp")
      .then(
        () => undefined,
        (err: unknown) => err,
      );
    expect(refusal).toBeInstanceOf(StaleListingRefusal);
    expect(isStaleListingRefusal(refusal)).toBe(true);
    expect(isStaleListingRefusal(new Error("anything else"))).toBe(false);
    expect(replacement.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(0);
  });

  test("populates instances/availableProviders/diagnostics/writesRefused from evener/instance/list on success", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({
      ...LIST_RESPONSE,
      diagnostics: ['providers.toml: unexpected key "type"'],
      userLayer: "user layer: /home/x/.config/evener/providers.toml",
      writesRefused: true,
    }));
    await credentialsStore.getState().fetch();
    const state = credentialsStore.getState();
    expect(state.instances).toEqual([ONE_INSTANCE]);
    expect(state.availableProviders).toEqual(LIST_RESPONSE.availableProviders);
    expect(state.diagnostics).toEqual(['providers.toml: unexpected key "type"']);
    expect(state.userLayer).toBe("user layer: /home/x/.config/evener/providers.toml");
    expect(state.writesRefused).toBe(true);
    expect(state.loading).toBe(false);
    expect(state.error).toBeNull();
  });

  test("defaults diagnostics/userLayer/writesRefused when the response omits them", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE); // no diagnostics/userLayer/writesRefused keys
    await credentialsStore.getState().fetch();
    const state = credentialsStore.getState();
    expect(state.diagnostics).toEqual([]);
    expect(state.userLayer).toBe("");
    expect(state.writesRefused).toBe(false);
  });

  test("sets loading true for the duration of the request", async () => {
    const fake = connectFakeClient();
    let resolveRequest: (() => void) | undefined;
    fake.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          resolveRequest = () => resolve(LIST_RESPONSE);
        }),
    );
    const promise = credentialsStore.getState().fetch();
    await Promise.resolve();
    expect(credentialsStore.getState().loading).toBe(true);
    resolveRequest?.();
    await promise;
    expect(credentialsStore.getState().loading).toBe(false);
  });

  test("on failure, clears loading and sets error without touching prior instances", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    fake.on("evener/instance/list", () => {
      throw new Error("boom");
    });
    await credentialsStore.getState().fetch();
    const state = credentialsStore.getState();
    expect(state.loading).toBe(false);
    expect(state.error).toBe("boom");
    expect(state.instances).toEqual([ONE_INSTANCE]); // unchanged - not blanked
  });
});

describe("mutations returning the updated instance list", () => {
  test.each(["replacement", "reconnect", "same connection"])(
    "a delayed mutation cannot overwrite a %s refresh",
    async (change) => {
      const old = connectFakeClient();
      let finishMutation!: (value: InstanceListResponse) => void;
      old.on(
        "evener/instance/remove",
        () =>
          new Promise<InstanceListResponse>((resolve) => {
            finishMutation = resolve;
          }),
      );
      const mutation = credentialsStore.getState().remove("work");
      await Promise.resolve();
      const current = change === "replacement" ? connectFakeClient() : old;
      if (change === "reconnect") {
        old.emitStateChange("reconnecting");
        old.emitReady();
      }
      let finishRead!: (value: InstanceListResponse) => void;
      current.on(
        "evener/instance/list",
        () =>
          new Promise<InstanceListResponse>((resolve) => {
            finishRead = resolve;
          }),
      );
      const refresh = credentialsStore.getState().fetch();
      await Promise.resolve();
      finishMutation({ instances: [], availableProviders: [] });
      await mutation;
      expect(credentialsStore.getState().loading).toBe(true);
      finishRead(LIST_RESPONSE);
      await refresh;
      expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    },
  );

  test("concurrent refreshes for different instances each land their own row", async () => {
    const fake = connectFakeClient();
    const OTHER: InstanceEntry = { ...ONE_INSTANCE, name: "personal" };
    const workModels = [{ id: "work-live-model" }];
    const personalModels = [{ id: "personal-live-model" }];
    const gates = new Map<string, (v: InstanceListResponse) => void>();
    fake.on("evener/instance/refreshModels", (params: { name: string }) => {
      return new Promise<InstanceListResponse>((resolve) => {
        gates.set(params.name, resolve);
      });
    });
    fake.on("evener/instance/list", () => ({ instances: [ONE_INSTANCE, OTHER], availableProviders: [] }));
    await credentialsStore.getState().fetch();
    const workRefresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();
    const personalRefresh = credentialsStore.getState().refreshModels("personal");
    await Promise.resolve();
    // Each answer carries ONLY its own instance's row — the way the
    // per-instance merge must treat it. If the store replaced the whole
    // snapshot (or dropped the earlier answer), one row's models would be
    // missing below.
    gates.get("personal")?.({ instances: [{ ...OTHER, models: personalModels }], availableProviders: [] });
    await personalRefresh;
    gates.get("work")?.({ instances: [{ ...ONE_INSTANCE, models: workModels }], availableProviders: [] });
    await workRefresh;
    const byName = new Map(credentialsStore.getState().instances.map((i) => [i.name, i]));
    expect([...byName.keys()].sort()).toEqual(["personal", "work"]);
    expect(byName.get("work")?.models).toEqual(workModels);
    expect(byName.get("personal")?.models).toEqual(personalModels);
  });

  test("a refresh neither sets nor clears the global fetch spinner", async () => {
    const fake = connectFakeClient();
    let finishFetch!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishFetch = resolve;
        }),
    );
    const fetchPromise = credentialsStore.getState().fetch();
    await Promise.resolve();
    expect(credentialsStore.getState().loading).toBe(true);
    // A refresh racing the fetch must leave the spinner alone: it never
    // sets loading, and finishing must not clear the fetch's spinner.
    fake.on("evener/instance/refreshModels", () => ({ instances: [ONE_INSTANCE], availableProviders: [] }));
    await credentialsStore.getState().refreshModels("work");
    expect(credentialsStore.getState().loading).toBe(true);
    finishFetch(LIST_RESPONSE);
    await fetchPromise;
    expect(credentialsStore.getState().loading).toBe(false);
  });

  test("a refresh whose answer omits a concurrently removed instance does not resurrect it", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    // Order: refresh starts, remove completes (its applyMutation bumps
    // the global version past the refresh's basis), then the stale
    // refresh answer omits the removed row. The merge must leave the
    // store empty, not resurrect a phantom stub.
    let finishRefresh!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    const refresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();
    fake.on("evener/instance/remove", () => ({ instances: [], availableProviders: [] }));
    await credentialsStore.getState().remove("work");
    // Stale answer omits the removed row: the store must stay empty,
    // not resurrect a phantom stub.
    finishRefresh({ instances: [], availableProviders: [] });
    await refresh;
    expect(credentialsStore.getState().instances).toEqual([]);
  });

  test("a stale full-list fetch keeps newer metadata and refreshed models", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({
      instances: [{ ...ONE_INSTANCE, activeSource: "none" }],
      availableProviders: [],
    }));
    await credentialsStore.getState().fetch();

    // A slow read starts, then a refresh lands with a live-only row.
    let finishRead!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRead = resolve;
        }),
    );
    const read = credentialsStore.getState().fetch();
    await Promise.resolve();
    fake.on("evener/instance/refreshModels", () => ({
      instances: [{ ...ONE_INSTANCE, activeSource: "none", models: [{ id: "live-new" }] }],
      availableProviders: [],
    }));
    await credentialsStore.getState().refreshModels("work");

    // The read's answer is the newer authority for everything except the
    // models the refresh fetched.
    finishRead({
      instances: [{ ...ONE_INSTANCE, activeSource: "store" }],
      availableProviders: [],
      diagnostics: ["new diagnostics"],
    });
    await read;
    const state = credentialsStore.getState();
    expect(state.instances[0]?.activeSource).toBe("store");
    expect(state.instances[0]?.models?.map((model) => model.id)).toEqual(["live-new"]);
    expect(state.diagnostics).toEqual(["new diagnostics"]);
  });

  test("a stale full-list fetch does not resurrect an instance its answer omits", async () => {
    const fake = connectFakeClient();
    const OTHER: InstanceEntry = { ...ONE_INSTANCE, name: "personal" };
    fake.on("evener/instance/list", () => ({ instances: [ONE_INSTANCE, OTHER], availableProviders: [] }));
    await credentialsStore.getState().fetch();

    // A read is out when a refresh for "personal" lands...
    let finishRead!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRead = resolve;
        }),
    );
    const read = credentialsStore.getState().fetch();
    await Promise.resolve();
    fake.on("evener/instance/refreshModels", () => ({
      instances: [{ ...OTHER, models: [{ id: "personal-live" }] }],
      availableProviders: [],
    }));
    await credentialsStore.getState().refreshModels("personal");
    expect(credentialsStore.getState().instances.map((entry) => entry.name)).toEqual(["work", "personal"]);

    // ...and the read's answer no longer carries it: the server removed it,
    // so the stale answer must not put the row back.
    finishRead({ instances: [ONE_INSTANCE], availableProviders: [] });
    await read;
    expect(credentialsStore.getState().instances.map((entry) => entry.name)).toEqual(["work"]);
  });

  test("a stale full-list fetch merges around landed refresh rows instead of wiping them", async () => {
    const fake = connectFakeClient();
    const workModels = [{ id: "work-live-model" }];
    let finishFetch!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishFetch = resolve;
        }),
    );
    const fetchPromise = credentialsStore.getState().fetch();
    await Promise.resolve();
    // Refresh lands first with the live row; the older fetch answers
    // after with the pre-refresh snapshot. The live row must survive.
    fake.on("evener/instance/refreshModels", () => ({
      instances: [{ ...ONE_INSTANCE, models: workModels }],
      availableProviders: [],
    }));
    await credentialsStore.getState().refreshModels("work");
    finishFetch(LIST_RESPONSE);
    await fetchPromise;
    expect(credentialsStore.getState().instances).toEqual([{ ...ONE_INSTANCE, models: workModels }]);
  });

  test("a refresh that landed before the read began is not newer than the read's answer", async () => {
    const fake = connectFakeClient();
    const OTHER: InstanceEntry = { ...ONE_INSTANCE, name: "personal" };
    fake.on("evener/instance/list", () => ({ instances: [ONE_INSTANCE, OTHER], availableProviders: [] }));
    await credentialsStore.getState().fetch();

    // A refresh for "work" lands BEFORE this read starts: the answer the read
    // is about to receive postdates it, so none of this row is newer than the
    // answer.
    fake.on("evener/instance/refreshModels", () => ({
      instances: [{ ...ONE_INSTANCE, models: [{ id: "refresh-before-read" }] }],
      availableProviders: [],
    }));
    await credentialsStore.getState().refreshModels("work");
    expect(credentialsStore.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["refresh-before-read"]);

    let finishRead!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRead = resolve;
        }),
    );
    const read = credentialsStore.getState().fetch();
    await Promise.resolve();
    // A refresh for a DIFFERENT instance lands while the read is out: that is
    // what makes this answer stale for its own rows, and it is why the earlier
    // "work" refresh must not be read as one of them.
    fake.on("evener/instance/refreshModels", () => ({
      instances: [{ ...OTHER, models: [{ id: "personal-live" }] }],
      availableProviders: [],
    }));
    await credentialsStore.getState().refreshModels("personal");

    // The answer carries the newer inventory for "work".
    finishRead({
      instances: [
        { ...ONE_INSTANCE, models: [{ id: "answer-newer" }] },
        { ...OTHER, models: [{ id: "personal-old" }] },
      ],
      availableProviders: [],
    });
    await read;
    const rows = credentialsStore.getState().instances;
    expect(rows.find((entry) => entry.name === "work")?.models?.map((model) => model.id)).toEqual(["answer-newer"]);
    // The row a refresh landed for during the read keeps that refresh's
    // inventory, which is the merge this read still owes.
    expect(rows.find((entry) => entry.name === "personal")?.models?.map((model) => model.id)).toEqual([
      "personal-live",
    ]);
  });

  test("a background fetch in flight does not discard a landing refresh", async () => {
    const fake = connectFakeClient();
    const workModels = [{ id: "work-live-model" }];
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    // Refresh starts; a notification-driven background refetch starts
    // while it is out. The refresh must still land when it answers.
    let finishRefresh!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    const refresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();
    await credentialsStore.getState().fetch();
    finishRefresh({ instances: [{ ...ONE_INSTANCE, models: workModels }], availableProviders: [] });
    await refresh;
    expect(credentialsStore.getState().instances).toEqual([{ ...ONE_INSTANCE, models: workModels }]);
  });

  test("a newer mutation wins when mutation responses arrive out of order", async () => {
    const fake = connectFakeClient();
    let finishOld!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/remove",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishOld = resolve;
        }),
    );
    const older = credentialsStore.getState().remove("work");
    await Promise.resolve();
    fake.on("evener/instance/create", () => LIST_RESPONSE);
    await credentialsStore.getState().create({ name: "work", base: "openai-codex", baseUrl: "" });
    finishOld({ instances: [], availableProviders: [] });
    await older;
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  test("concurrent toggles for different models both land when the later answer omits the earlier write", async () => {
    const fake = connectFakeClient();
    const withModels = (models: InstanceEntry["models"]): InstanceListResponse => ({
      instances: [{ ...ONE_INSTANCE, models }],
      availableProviders: [],
    });
    fake.on("evener/instance/list", () => withModels([{ id: "m1" }, { id: "m2" }]));
    await credentialsStore.getState().fetch();

    const gates = new Map<string, (value: InstanceListResponse) => void>();
    fake.on(
      "evener/instance/setModelDisabled",
      (params: { model: string }) =>
        new Promise<InstanceListResponse>((resolve) => {
          gates.set(params.model, resolve);
        }),
    );
    const first = credentialsStore.getState().setModelDisabled({ name: "work", model: "m1", disabled: true });
    await Promise.resolve();
    const second = credentialsStore.getState().setModelDisabled({ name: "work", model: "m2", disabled: true });
    await Promise.resolve();

    // The later toggle answers first, and its answer was computed before the
    // earlier write reached the server: it still shows m1 enabled.
    gates.get("m2")?.(withModels([{ id: "m1" }, { id: "m2", disabled: true }]));
    await second;
    // The earlier toggle answers second, carrying both writes — but a newer
    // request already replaced the listing, so its answer is superseded.
    gates.get("m1")?.(
      withModels([
        { id: "m1", disabled: true },
        { id: "m2", disabled: true },
      ]),
    );
    await first;

    // Both switches must read disabled: dropping the superseded answer
    // loses the first toggle until something else refetches.
    expect(credentialsStore.getState().instances[0]?.models).toEqual([
      { id: "m1", disabled: true },
      { id: "m2", disabled: true },
    ]);
  });

  test("a refresh cannot overwrite a toggle that landed while it was in flight", async () => {
    const fake = connectFakeClient();
    const withModels = (models: InstanceEntry["models"]): InstanceListResponse => ({
      instances: [{ ...ONE_INSTANCE, models }],
      availableProviders: [],
    });
    fake.on("evener/instance/list", () => withModels([{ id: "m1" }, { id: "m2" }]));
    await credentialsStore.getState().fetch();

    // The toggle starts first and is still out when the refresh starts, so
    // the refresh's guard has to catch a write that LANDS during its flight —
    // not one that starts after it.
    let finishToggle!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/setModelDisabled",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishToggle = resolve;
        }),
    );
    const toggle = credentialsStore.getState().setModelDisabled({ name: "work", model: "m1", disabled: true });
    await Promise.resolve();
    let finishRefresh!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    const refresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();

    finishToggle(withModels([{ id: "m1", disabled: true }, { id: "m2" }]));
    await toggle;
    // The refresh's snapshot predates the toggle: applying it would flip the
    // switch back.
    finishRefresh(withModels([{ id: "m1" }, { id: "m2" }]));
    await refresh;

    expect(credentialsStore.getState().instances[0]?.models).toEqual([{ id: "m1", disabled: true }, { id: "m2" }]);
  });

  test("a read cannot revert a toggle that landed while it was in flight", async () => {
    const fake = connectFakeClient();
    const withModels = (models: InstanceEntry["models"]): InstanceListResponse => ({
      instances: [{ ...ONE_INSTANCE, models }],
      availableProviders: [],
    });
    fake.on("evener/instance/list", () => withModels([{ id: "m1" }]));
    await credentialsStore.getState().fetch();

    // The toggle starts first...
    let finishToggle!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/setModelDisabled",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishToggle = resolve;
        }),
    );
    const toggle = credentialsStore.getState().setModelDisabled({ name: "work", model: "m1", disabled: true });
    await Promise.resolve();

    // ...and a full-list refetch starts after it, before the write has been
    // applied server-side.
    let finishRead!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRead = resolve;
        }),
    );
    const read = credentialsStore.getState().fetch();
    await Promise.resolve();

    // The toggle lands; the newer read supersedes its answer, so only the
    // row it wrote is reconciled.
    finishToggle(withModels([{ id: "m1", disabled: true }]));
    await toggle;
    expect(credentialsStore.getState().instances[0]?.models).toEqual([{ id: "m1", disabled: true }]);

    // The read answers with the pre-write listing: it must not flip the
    // switch back.
    finishRead(withModels([{ id: "m1" }]));
    await read;
    expect(credentialsStore.getState().instances[0]?.models).toEqual([{ id: "m1", disabled: true }]);
  });

  test("a write on another instance does not discard this instance's refresh", async () => {
    const fake = connectFakeClient();
    const OTHER: InstanceEntry = { ...ONE_INSTANCE, name: "personal" };
    fake.on("evener/instance/list", () => ({ instances: [ONE_INSTANCE, OTHER], availableProviders: [] }));
    await credentialsStore.getState().fetch();

    // A refresh for "work" is out when the user signs a DIFFERENT instance
    // in: that write says nothing about work's live models.
    let finishRefresh!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    const refresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();
    fake.on("evener/auth/apiKey/set", () => ({
      provider: "personal",
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().setApiKey("personal", "sk-personal");

    finishRefresh({ instances: [{ ...ONE_INSTANCE, models: [{ id: "work-live" }] }], availableProviders: [] });
    await refresh;
    const work = credentialsStore.getState().instances.find((entry) => entry.name === "work");
    expect(work?.models?.map((model) => model.id)).toEqual(["work-live"]);
  });

  test("refresh bookkeeping does not survive a client change", async () => {
    const first = connectFakeClient();
    const OTHER: InstanceEntry = { ...ONE_INSTANCE, name: "personal" };
    first.on("evener/instance/list", () => ({
      instances: [{ ...ONE_INSTANCE, activeSource: "store" }, OTHER],
      availableProviders: [],
    }));
    await credentialsStore.getState().fetch();
    // The first client's refresh marks "work" with its own row.
    first.on("evener/instance/refreshModels", () => ({
      instances: [{ ...ONE_INSTANCE, activeSource: "store", models: [{ id: "old-live" }] }],
      availableProviders: [],
    }));
    await credentialsStore.getState().refreshModels("work");
    expect(credentialsStore.getState().instances[0]?.activeSource).toBe("store");

    // A new client takes over — a server that says "work" has no stored
    // credentials. Its read is in flight when a refresh for another
    // instance lands, which moves the generation and forces that read
    // through the merge path.
    const second = new FakeClient("ready");
    let finishRead!: (value: InstanceListResponse) => void;
    second.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRead = resolve;
        }),
    );
    second.on("evener/instance/refreshModels", () => ({
      instances: [{ ...OTHER, models: [{ id: "personal-live" }] }],
      availableProviders: [],
    }));
    connectionStore.getState().connect(second);
    await Promise.resolve();
    await credentialsStore.getState().refreshModels("personal");

    // The old client's marked row must not be preserved into the new
    // client's listing: this answer is the only authority for its fields.
    finishRead({
      instances: [
        { ...ONE_INSTANCE, activeSource: "none" },
        { ...OTHER, activeSource: "none" },
      ],
      availableProviders: [],
    });
    await Promise.resolve();
    await Promise.resolve();
    expect(credentialsStore.getState().instances[0]?.activeSource).toBe("none");
    // The old client's marked inventory is gone with it: this answer's row
    // for "work" carries no live models, and nothing may stage them back.
    expect(credentialsStore.getState().instances[0]?.models).toBeUndefined();
  });

  test("a write landing from a replaced client does not discard the new client's refresh", async () => {
    const first = connectFakeClient();
    first.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    let finishToggle!: (value: InstanceListResponse) => void;
    first.on(
      "evener/instance/setModelDisabled",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishToggle = resolve;
        }),
    );
    const toggle = credentialsStore.getState().setModelDisabled({ name: "work", model: "m1", disabled: true });
    await Promise.resolve();

    // The session reconnects on a new client while that write is still out,
    // and the new client's refresh starts.
    const second = new FakeClient("ready");
    second.on("evener/instance/list", () => LIST_RESPONSE);
    connectionStore.getState().connect(second);
    await Promise.resolve();
    let finishRefresh!: (value: InstanceListResponse) => void;
    second.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    const refresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();

    // The old client's write lands afterwards. It belongs to a client that is
    // gone: it must not count against the new client's refresh.
    finishToggle({ instances: [{ ...ONE_INSTANCE, models: [{ id: "m1", disabled: true }] }], availableProviders: [] });
    await toggle;
    finishRefresh({
      instances: [{ ...ONE_INSTANCE, models: [{ id: "m1" }, { id: "live-new" }] }],
      availableProviders: [],
    });
    await refresh;
    expect(credentialsStore.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["m1", "live-new"]);
  });

  test("a refresh landing after a newer read does not revert its metadata", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({
      instances: [{ ...ONE_INSTANCE, activeSource: "none", models: [{ id: "m1" }] }],
      availableProviders: [],
      diagnostics: ["old diagnostics"],
    }));
    await credentialsStore.getState().fetch();

    // The refresh is out when a newer read lands: the credential source and
    // the diagnostics it reports are newer than the refresh's snapshot.
    let finishRefresh!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    const refresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();
    fake.on("evener/instance/list", () => ({
      instances: [{ ...ONE_INSTANCE, activeSource: "store", models: [{ id: "m1" }] }],
      availableProviders: [],
      diagnostics: ["new diagnostics"],
    }));
    await credentialsStore.getState().fetch();

    // The refresh only ever knows about live models: its older row and
    // globals must not overwrite what the newer read established.
    finishRefresh({
      instances: [{ ...ONE_INSTANCE, activeSource: "none", models: [{ id: "m1" }, { id: "live-new" }] }],
      availableProviders: [],
      diagnostics: ["old diagnostics"],
    });
    await refresh;

    const state = credentialsStore.getState();
    expect(state.instances[0]?.activeSource).toBe("store");
    expect(state.instances[0]?.models?.map((model) => model.id)).toEqual(["m1", "live-new"]);
    expect(state.diagnostics).toEqual(["new diagnostics"]);
  });

  test("a second refresh landing during a write keeps its newer inventory", async () => {
    const fake = connectFakeClient();
    const withModels = (models: InstanceEntry["models"]): InstanceListResponse => ({
      instances: [{ ...ONE_INSTANCE, models }],
      availableProviders: [],
    });
    fake.on("evener/instance/list", () => withModels([{ id: "m1" }]));
    await credentialsStore.getState().fetch();

    // One refresh lands before the write starts, marking the row...
    fake.on("evener/instance/refreshModels", () => withModels([{ id: "m1" }, { id: "first-live" }]));
    await credentialsStore.getState().refreshModels("work");

    // ...the write starts...
    let finishToggle!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/setModelDisabled",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishToggle = resolve;
        }),
    );
    const toggle = credentialsStore.getState().setModelDisabled({ name: "work", model: "m1", disabled: true });
    await Promise.resolve();

    // ...and a SECOND refresh for the same instance lands while it is out,
    // with inventory newer than the write's answer.
    fake.on("evener/instance/refreshModels", () => withModels([{ id: "m1", disabled: true }, { id: "second-live" }]));
    await credentialsStore.getState().refreshModels("work");
    expect(credentialsStore.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["m1", "second-live"]);

    finishToggle(withModels([{ id: "m1", disabled: true }, { id: "first-live" }]));
    await toggle;
    expect(credentialsStore.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["m1", "second-live"]);
  });

  test("reconnecting lets a refresh stage a row the previous client never had", async () => {
    const first = connectFakeClient();
    first.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    // The session reconnects — its own first read is still out — and the new
    // client's refresh starts for an instance this store has never held.
    const second = new FakeClient("ready");
    second.on("evener/instance/list", () => new Promise<InstanceListResponse>(() => {}));
    connectionStore.getState().connect(second);
    await Promise.resolve();
    let finishRefresh!: (value: InstanceListResponse) => void;
    second.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    const refresh = credentialsStore.getState().refreshModels("personal");
    await Promise.resolve();
    finishRefresh({
      instances: [{ ...ONE_INSTANCE, name: "personal", models: [{ id: "personal-live" }] }],
      availableProviders: [],
    });
    await refresh;
    const personal = credentialsStore.getState().instances.find((entry) => entry.name === "personal");
    expect(personal?.models?.map((model) => model.id)).toEqual(["personal-live"]);
  });

  test("a superseded toggle from a replaced client does not touch the new client's listing", async () => {
    const first = connectFakeClient();
    const withModels = (models: InstanceEntry["models"]): InstanceListResponse => ({
      instances: [{ ...ONE_INSTANCE, models }],
      availableProviders: [],
    });
    first.on("evener/instance/list", () => withModels([{ id: "m1" }]));
    await credentialsStore.getState().fetch();

    // The toggle is out when the session reconnects on a new client, whose
    // own listing says the model is enabled.
    let finishToggle!: (value: InstanceListResponse) => void;
    first.on(
      "evener/instance/setModelDisabled",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishToggle = resolve;
        }),
    );
    const toggle = credentialsStore.getState().setModelDisabled({ name: "work", model: "m1", disabled: true });
    await Promise.resolve();
    const second = new FakeClient("ready");
    second.on("evener/instance/list", () => withModels([{ id: "m1" }]));
    connectionStore.getState().connect(second);
    await Promise.resolve();
    await credentialsStore.getState().fetch();

    // The old client's answer lands afterwards: it must not write its row
    // into the new client's listing.
    finishToggle(withModels([{ id: "m1", disabled: true }]));
    await toggle;
    expect(credentialsStore.getState().instances[0]?.models).toEqual([{ id: "m1" }]);
  });

  test("a replaced client's refresh cannot delete the new client's refresh token", async () => {
    const first = connectFakeClient();
    first.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    // A refresh is out on the first client...
    let finishOldRefresh!: (value: InstanceListResponse) => void;
    first.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishOldRefresh = resolve;
        }),
    );
    const oldRefresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();

    // ...when the session reconnects and the new client starts its own
    // refresh for the same instance (its version restarts at 1).
    const second = new FakeClient("ready");
    second.on("evener/instance/list", () => LIST_RESPONSE);
    connectionStore.getState().connect(second);
    await Promise.resolve();
    let finishNewRefresh!: (value: InstanceListResponse) => void;
    second.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishNewRefresh = resolve;
        }),
    );
    const newRefresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();

    // The old request's cleanup must not take the new request's token with
    // it: that would discard the live inventory the new client asked for.
    finishOldRefresh({ instances: [{ ...ONE_INSTANCE, models: [{ id: "old-live" }] }], availableProviders: [] });
    await oldRefresh;
    finishNewRefresh({ instances: [{ ...ONE_INSTANCE, models: [{ id: "new-live" }] }], availableProviders: [] });
    await newRefresh;
    expect(credentialsStore.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["new-live"]);
  });

  test("a refresh cannot revert an edit that landed while it was in flight", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    // The other side of the same guard: a write that STARTS after the
    // refresh does keep superseding it, so the refresh's older snapshot
    // never carries an edited field back to its previous value.
    let finishRefresh!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    const refresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();
    fake.on("evener/instance/edit", () => ({
      instances: [{ ...ONE_INSTANCE, baseUrl: "https://edited" }],
      availableProviders: [],
    }));
    await credentialsStore.getState().edit({ name: "work", baseUrl: "https://edited" });
    finishRefresh({ instances: [{ ...ONE_INSTANCE, models: [{ id: "live" }] }], availableProviders: [] });
    await refresh;

    expect(credentialsStore.getState().instances).toEqual([{ ...ONE_INSTANCE, baseUrl: "https://edited" }]);
  });

  test("a toggle cannot discard live rows a refresh landed while it was in flight", async () => {
    const fake = connectFakeClient();
    const withModels = (models: InstanceEntry["models"]): InstanceListResponse => ({
      instances: [{ ...ONE_INSTANCE, models }],
      availableProviders: [],
    });
    fake.on("evener/instance/list", () => withModels([{ id: "m1" }]));
    await credentialsStore.getState().fetch();

    // The toggle starts first...
    let finishToggle!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/setModelDisabled",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishToggle = resolve;
        }),
    );
    const toggle = credentialsStore.getState().setModelDisabled({ name: "work", model: "m1", disabled: true });
    await Promise.resolve();

    // ...and a refresh lands while it is out, carrying a live-only row the
    // toggle's answer (computed before that fetch) cannot know about.
    fake.on("evener/instance/refreshModels", () => withModels([{ id: "m1" }, { id: "live-new" }]));
    await credentialsStore.getState().refreshModels("work");
    expect(credentialsStore.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["m1", "live-new"]);

    // The toggle's answer arrives with the pre-refresh inventory: it is
    // authoritative for the row it wrote, not for a newer live listing.
    finishToggle(withModels([{ id: "m1", disabled: true }]));
    await toggle;

    const models = credentialsStore.getState().instances[0]?.models ?? [];
    expect(models.map((model) => model.id)).toEqual(["m1", "live-new"]);
    expect(models.find((model) => model.id === "m1")?.disabled).toBe(true);
  });

  test("a failed mutation releases loading from a superseded read", async () => {
    const fake = connectFakeClient();
    let finishRead!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRead = resolve;
        }),
    );
    const read = credentialsStore.getState().fetch();
    await Promise.resolve();
    fake.on("evener/instance/remove", () => {
      throw new Error("write refused");
    });
    await expect(credentialsStore.getState().remove("work")).rejects.toThrow("write refused");
    finishRead(LIST_RESPONSE);
    await read;
    expect(credentialsStore.getState().loading).toBe(false);
  });

  test("an authoritative mutation completes loading and supersedes an older read", async () => {
    const fake = connectFakeClient();
    let resolveRead: ((response: InstanceListResponse) => void) | undefined;
    fake.on(
      "evener/instance/list",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );
    fake.on("evener/instance/create", () => LIST_RESPONSE);
    const read = credentialsStore.getState().fetch();
    expect(credentialsStore.getState().loading).toBe(true);
    await credentialsStore.getState().create({ name: "work", base: "openai-codex", baseUrl: "" });
    expect(credentialsStore.getState().loading).toBe(false);
    expect(credentialsStore.getState().error).toBeNull();
    resolveRead?.({ instances: [], availableProviders: [] });
    await read;
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  test("create() calls evener/instance/create and applies the returned list", async () => {
    const fake = connectFakeClient();
    const created: InstanceListResponse = { instances: [ONE_INSTANCE], availableProviders: [] };
    fake.on("evener/instance/create", (params) => {
      expect(params).toEqual({ name: "work", base: "openai-codex", baseUrl: "", originClientId: "test-tab" });
      return created;
    });
    await credentialsStore.getState().create({ name: "work", base: "openai-codex", baseUrl: "" });
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  test("edit() calls evener/instance/edit and applies the returned list", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", baseUrl: "https://x", originClientId: "test-tab" });
      return LIST_RESPONSE;
    });
    await credentialsStore.getState().edit({ name: "work", baseUrl: "https://x" });
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  // The listing an edit answers with is only the truth if the store kept it.
  // A caller that steers a view on the strength of its own save has to hear
  // that verdict: a response a newer request superseded is a document the
  // store already threw away.
  test("edit() reports whether the store applied its response", async () => {
    const fake = connectFakeClient();
    let finishEdit!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/edit",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishEdit = resolve;
        }),
    );
    const superseded = credentialsStore.getState().edit({ name: "work", baseUrl: "https://x" });
    await Promise.resolve();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    finishEdit({ instances: [], availableProviders: [] });
    expect(await superseded).toBe(false);
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);

    fake.on("evener/instance/edit", () => ({ instances: [], availableProviders: [] }));
    expect(await credentialsStore.getState().edit({ name: "work", baseUrl: "https://x" })).toBe(true);
    expect(credentialsStore.getState().instances).toEqual([]);
  });

  // create() and remove() steer onboarding and removal flows on the strength
  // of their own write the same way edit steers the sheet, so they owe their
  // callers the same verdict: a response the store discarded must not read as
  // authoritative.
  test("create() and remove() report whether the store applied their response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    let finishCreate!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/create",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishCreate = resolve;
        }),
    );
    const created = credentialsStore.getState().create({ name: "work2", base: "openai-codex", baseUrl: "" });
    await Promise.resolve();
    // A listing read issued after the create wins the store race, so the
    // create's own response is superseded before it lands.
    await credentialsStore.getState().fetch();
    finishCreate({
      instances: [ONE_INSTANCE, { ...ONE_INSTANCE, name: "work2", isDefault: false }],
      availableProviders: [],
    });
    expect(await created).toBe(false);
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);

    let finishRemove!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/remove",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRemove = resolve;
        }),
    );
    const removed = credentialsStore.getState().remove("work");
    await Promise.resolve();
    await credentialsStore.getState().fetch();
    finishRemove({ instances: [], availableProviders: [] });
    expect(await removed).toBe(false);
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);

    fake.on("evener/instance/create", () => ({
      instances: [ONE_INSTANCE, { ...ONE_INSTANCE, name: "work2", isDefault: false }],
      availableProviders: [],
    }));
    expect(await credentialsStore.getState().create({ name: "work2", base: "openai-codex", baseUrl: "" })).toBe(true);
    fake.on("evener/instance/remove", () => ({ instances: [], availableProviders: [] }));
    expect(await credentialsStore.getState().remove("work")).toBe(true);
    expect(credentialsStore.getState().instances).toEqual([]);
  });

  test("remove() calls evener/instance/remove and applies the returned list", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/remove", (params) => {
      expect(params).toEqual({ name: "work", originClientId: "test-tab" });
      return { instances: [], availableProviders: [] };
    });
    await credentialsStore.getState().remove("work");
    expect(credentialsStore.getState().instances).toEqual([]);
  });

  test("setDefault() calls evener/instance/setDefault and applies the returned list", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/setDefault", (params) => {
      expect(params).toEqual({ name: "work", originClientId: "test-tab" });
      return LIST_RESPONSE;
    });
    await credentialsStore.getState().setDefault("work");
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  test("a mutation failure rejects and does not touch stored instances", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/create", () => {
      throw new Error("name already exists");
    });
    await expect(
      credentialsStore.getState().create({ name: "work", base: "openai-codex", baseUrl: "" }),
    ).rejects.toThrow("name already exists");
    expect(credentialsStore.getState().instances).toEqual([]);
  });
});

// --- host-partitioned instance lists (component 07b) ------------------------
//
// A remote host's listing is HOST-SCOPED data. fetchHost(remote) must not write
// it into the controller-scoped top-level fields: Settings > Credentials and
// ConnectProviderDialog read those and their mutations act on the controller,
// so a remote listing there would be displayed as the controller's and then
// silently reverted (or destroyed) by any controller-scoped write or refetch.
// The reverse must hold too: the controller's own loads and evener/auth/updated
// refetches must never replace a remote host's listing.

const REMOTE_INSTANCE: InstanceEntry = {
  name: "buildbox-anthropic",
  providerId: "anthropic",
  protocol: "anthropic",
  auth: "bearer",
  isDefault: true,
  implicit: false,
  authModes: ["apiKey"],
  activeSource: "store",
  hasStoredFile: true,
  hasStoredOAuth: false,
  envVar: "",
  storedEmail: "",
  credentialRequired: true,
};

const REMOTE_LIST: InstanceListResponse = { instances: [REMOTE_INSTANCE], availableProviders: [] };

// serveRemoteList answers the proxy call for evener/instance/list for the
// "buildbox" host, asserting the forwarded envelope is exactly the one
// fetchHost promised.
function serveRemoteList(fake: FakeClient, response: InstanceListResponse): void {
  fake.on("evener/host/request", (params) => {
    expect(params).toEqual({ host: "buildbox", method: "evener/instance/list", params: {} });
    return response as unknown as HostForwardedResult;
  });
}

describe("host-partitioned instance lists (component 07b)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  test("a remote fetchHost partitions its listing and leaves the controller's fields alone", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    serveRemoteList(fake, REMOTE_LIST);

    await fetchHost("buildbox");

    const state = credentialsStore.getState();
    expect(state.instances).toEqual([ONE_INSTANCE]); // the controller's own list
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").instances).toEqual([REMOTE_INSTANCE]);
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").loading).toBe(false);
    // The remote read was the only one forwarded; the controller load went direct.
    expect(fake.calls.filter((call) => call.method === "evener/host/request")).toEqual([
      {
        method: "evener/host/request",
        params: { host: "buildbox", method: "evener/instance/list", params: {} },
      },
    ]);
  });

  test("an evener/auth/updated controller refetch cannot replace a remote host's listing", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    serveRemoteList(fake, REMOTE_LIST);
    await fetchHost("buildbox");

    fake.emitNotification({ method: "evener/auth/updated", params: {} });
    await vi.advanceTimersByTimeAsync(250);

    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").instances).toEqual([REMOTE_INSTANCE]);
  });

  // A credential change made ON a remote host reaches this browser wrapped in
  // evener/host/notification, tagged with the host (app_host_admin.go's fan-out
  // re-emits that host's own evener/auth/updated). It must refresh that host's
  // OWN partition -- a remote spawn's provider verdict and model catalog read it
  // -- and never the controller's top-level listing.
  test("a wrapped remote config notification refetches that host's partition, not the controller's", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    serveRemoteList(fake, REMOTE_LIST);
    await fetchHost("buildbox");

    // The credential was removed on the host: its own reload reports an
    // unconfigured instance while the controller's listing is unchanged.
    const remoteAfter: InstanceListResponse = {
      instances: [{ ...REMOTE_INSTANCE, activeSource: "none" }],
      availableProviders: [],
    };
    fake.on("evener/host/request", () => remoteAfter as unknown as HostForwardedResult);
    const controllerList = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", controllerList);

    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/auth/updated", params: {} },
    });
    await vi.advanceTimersByTimeAsync(249);
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").instances).toEqual([REMOTE_INSTANCE]);
    await vi.advanceTimersByTimeAsync(1);
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").instances).toEqual(remoteAfter.instances);
    // The controller's own listing and its refetch path are untouched.
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    expect(controllerList).not.toHaveBeenCalled();
  });

  test("wrapped config notifications refetch each named host independently", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    const forwarded: string[] = [];
    fake.on("evener/host/request", (params) => {
      forwarded.push((params as HostRequestParams).host);
      return REMOTE_LIST as unknown as HostForwardedResult;
    });

    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/auth/updated", params: {} },
    });
    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "otherbox", method: "evener/auth/updated", params: {} },
    });
    await vi.advanceTimersByTimeAsync(250);

    // One shared debounce would have collapsed these into one host's load.
    expect(forwarded).toEqual(["buildbox", "otherbox"]);
  });

  test("a wrapped host config notification this store cannot act on triggers no refetch", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    const requestSpy = vi.fn(() => REMOTE_LIST as unknown as HostForwardedResult);
    fake.on("evener/host/request", requestSpy);
    const controllerList = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", controllerList);

    // The fan-out also wraps launch/plugin/agents-doc updates; the instance
    // listing is this store's only wire-truth data, so they are not actionable.
    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/launch/updated", params: {} },
    });
    await vi.advanceTimersByTimeAsync(500);

    expect(requestSpy).not.toHaveBeenCalled();
    expect(controllerList).not.toHaveBeenCalled();
  });

  test("a controller read does not supersede an in-flight remote host read", async () => {
    const fake = connectFakeClient();
    let finishRemote: (value: InstanceListResponse) => void = () => {};
    fake.on(
      "evener/host/request",
      () =>
        new Promise<HostForwardedResult>((resolve) => {
          finishRemote = (value) => resolve(value as unknown as HostForwardedResult);
        }),
    );
    const remote = fetchHost("buildbox");
    await Promise.resolve();

    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    finishRemote(REMOTE_LIST);
    await remote;

    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").instances).toEqual([REMOTE_INSTANCE]);
  });

  test("a controller-scoped instance mutation leaves a remote host's listing intact", async () => {
    const fake = connectFakeClient();
    serveRemoteList(fake, REMOTE_LIST);
    await fetchHost("buildbox");

    fake.on("evener/instance/remove", () => ({ instances: [], availableProviders: [] }));
    await credentialsStore.getState().remove("buildbox-anthropic");

    expect(credentialsStore.getState().instances).toEqual([]);
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").instances).toEqual([REMOTE_INSTANCE]);
    // The mutation was issued on the plain connection, never forwarded to the
    // remote host whose row the user was looking at.
    expect(fake.calls.filter((call) => call.method === "evener/host/request")).toHaveLength(1);
  });

  test("a failed remote load reports on its own partition and leaves the controller's status alone", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    fake.on("evener/host/request", () => {
      throw new Error("remote hub unavailable");
    });

    await fetchHost("buildbox");

    const state = credentialsStore.getState();
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").error).toBe("remote hub unavailable");
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").loading).toBe(false);
    expect(state.error).toBeNull();
    expect(state.loading).toBe(false);
    expect(state.instances).toEqual([ONE_INSTANCE]);
  });

  test("a reconnect releases a remote host's in-flight status without discarding its listing", async () => {
    const fake = connectFakeClient();
    serveRemoteList(fake, REMOTE_LIST);
    await fetchHost("buildbox");
    let finish!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/host/request",
      () =>
        new Promise<HostForwardedResult>((resolve) => {
          finish = (value) => resolve(value as unknown as HostForwardedResult);
        }),
    );
    const reload = fetchHost("buildbox");
    await Promise.resolve();
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").loading).toBe(true);

    fake.emitStateChange("reconnecting");
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").loading).toBe(false);
    finish({ instances: [{ ...REMOTE_INSTANCE, name: "stale" }], availableProviders: [] });
    await reload;
    // The interrupted load cannot commit over the connection transition, and
    // the listing it was refreshing is still the one the store holds.
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").instances).toEqual([REMOTE_INSTANCE]);
  });

  // The release above and the read it orphans are ordered rather than racing:
  // the transition clears the in-flight read's loading flag, the answer that
  // read was waiting for cannot commit (it belongs to the client that is gone),
  // and the read every consumer issues next - the pane's provider setup re-runs
  // on the connection change, and this is that read - is the one that lands. The
  // ordering that would hurt is the orphaned answer arriving LAST: it must still
  // be refused rather than overwriting the newer listing or leaving the
  // partition claiming a load is in flight.
  test("a released host read cannot outlive the transition that replaced it", async () => {
    const fake = connectFakeClient();
    let finishReleased!: (value: HostForwardedResult) => void;
    fake.on(
      "evener/host/request",
      () =>
        new Promise<HostForwardedResult>((resolve) => {
          finishReleased = resolve;
        }),
    );
    const released = fetchHost("buildbox");
    await Promise.resolve();
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").loading).toBe(true);

    fake.emitStateChange("reconnecting");
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").loading).toBe(false);

    // The connection comes back: the transport refuses a call while it is
    // reconnecting, so the read every consumer issues next happens here.
    fake.emitReady();
    const afterReconnect: InstanceListResponse = {
      instances: [{ ...REMOTE_INSTANCE, name: "after-reconnect" }],
      availableProviders: [],
    };
    fake.on("evener/host/request", () => afterReconnect as unknown as HostForwardedResult);
    await fetchHost("buildbox");
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").instances).toEqual(afterReconnect.instances);

    finishReleased(REMOTE_LIST as unknown as HostForwardedResult);
    await released;

    expect(hostPartition(hostInstancesStore.getState(), "buildbox").instances).toEqual(afterReconnect.instances);
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").loading).toBe(false);
  });

  // The generation above orders reads across a CONNECTION transition, never
  // between two reads of the SAME host: both overlapping fetchHost calls capture
  // one generation, so the older answer can commit over the newer partition.
  // The product reaches that state without anything unusual — a mount-time load
  // of the selected host raced by the 250ms wrapped-notification refetch, or a
  // user's retry — so the per-host order needs its own monotonic sequence, the
  // same guard the package's credential instances store puts on its listing
  // reads (appwire-client's instances.ts requestVersion).
  test("an older in-flight remote load cannot overwrite a newer one for the same host", async () => {
    const fake = connectFakeClient();
    const answers: Array<(value: HostForwardedResult) => void> = [];
    fake.on(
      "evener/host/request",
      () =>
        new Promise<HostForwardedResult>((resolve) => {
          answers.push(resolve);
        }),
    );
    // The most recently parked request is the newest read; answering it out of
    // order is the whole shape under test, so the resolver is taken by the end
    // of the queue rather than by a bare index.
    const answerLatest = (): ((value: HostForwardedResult) => void) => {
      const resolve = answers.pop();
      if (!resolve) throw new Error("no in-flight forwarded request to answer");
      return resolve;
    };

    const stale = fetchHost("buildbox");
    await Promise.resolve();
    const newer = fetchHost("buildbox");
    await Promise.resolve();
    expect(answers).toHaveLength(2);

    // The NEWER request answers first. The older one then lands with the answer
    // it computed before it: a credential change on the host, a retry, or any
    // other read that started earlier and finished later.
    const afterEdit: InstanceListResponse = {
      instances: [{ ...REMOTE_INSTANCE, name: "edited-on-the-host" }],
      availableProviders: [],
    };
    answerLatest()(afterEdit as unknown as HostForwardedResult);
    await newer;
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").instances).toEqual(afterEdit.instances);

    answerLatest()(REMOTE_LIST as unknown as HostForwardedResult);
    await stale;

    // The superseded answer is dropped instead of committing over the newer
    // partition, and the partition is not left claiming a load is still running.
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").instances).toEqual(afterEdit.instances);
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").loading).toBe(false);
  });
});

describe("auth RPCs: thin proxies, no local state mutation", () => {
  test("testCredentials() sends the exact configured instance name and returns the typed safe response", async () => {
    const fake = connectFakeClient();
    const response: AuthTestResponse = {
      provider: "custom / team-east",
      status: "success",
      message: "Credentials verified.",
    };
    fake.on("evener/auth/test", (params) => {
      expect(params).toEqual({ provider: "custom / team-east" });
      return response;
    });

    await expect(credentialsStore.getState().testCredentials("custom / team-east")).resolves.toEqual(response);
  });

  test("a model refresh cannot revive a pre-auth row after an auth mutation landed", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({
      instances: [{ ...ONE_INSTANCE, activeSource: "none" }],
      availableProviders: [],
    }));
    await credentialsStore.getState().fetch();

    // A model refresh is out when the user signs in.
    let finishRefresh!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    const refresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();

    // The auth write lands and the caller refetches the list, which now
    // reports the stored key.
    fake.on("evener/auth/apiKey/set", () => ({
      provider: "work",
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().setApiKey("work", "sk-secret");
    fake.on("evener/instance/list", () => ({
      instances: [{ ...ONE_INSTANCE, activeSource: "store" }],
      availableProviders: [],
    }));
    await credentialsStore.getState().fetch();
    expect(credentialsStore.getState().instances[0]?.activeSource).toBe("store");

    // The refresh's snapshot predates the sign-in: applying it would show
    // the signed-out row again.
    finishRefresh({ instances: [{ ...ONE_INSTANCE, activeSource: "none" }], availableProviders: [] });
    await refresh;
    expect(credentialsStore.getState().instances[0]?.activeSource).toBe("store");
  });

  test("setApiKey() calls evener/auth/apiKey/set and returns its response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/apiKey/set", (params) => {
      expect(params).toEqual({ provider: "work", value: "sk-secret", originClientId: "test-tab" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    const result = await credentialsStore.getState().setApiKey("work", "sk-secret");
    expect(result.activeSource).toBe("store");
    // Never stored on the store itself - never-echo invariant.
    expect(JSON.stringify(credentialsStore.getState())).not.toContain("sk-secret");
  });

  test("clearStoredKey() calls evener/auth/apiKey/clear and returns its response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/apiKey/clear", (params) => {
      expect(params).toEqual({ provider: "work", originClientId: "test-tab" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true };
    });
    const result = await credentialsStore.getState().clearStoredKey("work");
    expect(result.activeSource).toBe("oauth");
    expect(result.hasStoredOAuth).toBe(true);
  });

  test("logout() calls evener/auth/logout", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/logout", (params) => {
      expect(params).toEqual({ provider: "work", originClientId: "test-tab" });
      return {
        removed: true,
        status: { provider: "work", supported: true, signedIn: false, activeSource: "none", hasStoredOAuth: false },
      };
    });
    const result = await credentialsStore.getState().logout("work");
    expect(result.removed).toBe(true);
  });

  // A destructive action carries the endpoint the caller showed the user, so
  // the hub can refuse a name that now resolves elsewhere rather than clearing
  // or removing a replacement's credentials/configuration. A caller shown no
  // endpoint sends nothing, rather than an empty-string assertion.
  test("remove() forwards the expected endpoint fingerprint when given one", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/remove", (params) => {
      expect(params).toEqual({ name: "work", expectedEndpointFingerprint: "fp-work", originClientId: "test-tab" });
      return { instances: [], availableProviders: [] };
    });
    expect(await credentialsStore.getState().remove("work", "fp-work")).toBe(true);
  });

  test("clearStoredKey() forwards the expected endpoint fingerprint when given one", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/apiKey/clear", (params) => {
      expect(params).toEqual({ provider: "work", expectedEndpointFingerprint: "fp-work", originClientId: "test-tab" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true };
    });
    await credentialsStore.getState().clearStoredKey("work", "fp-work");
  });

  test("logout() forwards the expected endpoint fingerprint when given one", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/logout", (params) => {
      expect(params).toEqual({ provider: "work", expectedEndpointFingerprint: "fp-work", originClientId: "test-tab" });
      return {
        removed: true,
        status: { provider: "work", supported: true, signedIn: false, activeSource: "none", hasStoredOAuth: false },
      };
    });
    await credentialsStore.getState().logout("work", "fp-work");
  });

  test("the destructive wrappers omit the fingerprint when none was captured", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/remove", (params) => {
      expect(params).toEqual({ name: "work", originClientId: "test-tab" });
      return { instances: [], availableProviders: [] };
    });
    fake.on("evener/auth/apiKey/clear", (params) => {
      expect(params).toEqual({ provider: "work", originClientId: "test-tab" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true };
    });
    fake.on("evener/auth/logout", (params) => {
      expect(params).toEqual({ provider: "work", originClientId: "test-tab" });
      return {
        removed: true,
        status: { provider: "work", supported: true, signedIn: false, activeSource: "none", hasStoredOAuth: false },
      };
    });
    await credentialsStore.getState().remove("work");
    // An empty string is "shown no endpoint" too, not an empty assertion.
    await credentialsStore.getState().clearStoredKey("work", "");
    await credentialsStore.getState().logout("work", "");
  });

  test("loginStart() calls evener/auth/login/start", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/login/start", (params) => {
      expect(params).toEqual({ provider: "work" });
      return { provider: "work", flowId: "flow-1", url: "https://auth.example.com/start" };
    });
    const result = await credentialsStore.getState().loginStart("work");
    expect(result.url).toBe("https://auth.example.com/start");
  });

  test("loginComplete() calls evener/auth/login/complete", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/login/complete", (params) => {
      expect(params).toEqual({
        provider: "work",
        flowId: "flow-1",
        redirectUrl: "https://redirect",
        originClientId: "test-tab",
      });
      return {
        status: { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true },
      };
    });
    const result = await credentialsStore.getState().loginComplete("work", "flow-1", "https://redirect");
    expect(result.status.signedIn).toBe(true);
  });

  test("deviceStart() calls evener/auth/device/start", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/device/start", (params) => {
      expect(params).toEqual({ provider: "work" });
      return {
        provider: "work",
        flowId: "flow-2",
        userCode: "ABCD-EFGH",
        verificationUrl: "https://verify",
        intervalSeconds: 5,
      };
    });
    const result = await credentialsStore.getState().deviceStart("work");
    expect(result.userCode).toBe("ABCD-EFGH");
  });

  test("devicePoll() calls evener/auth/device/poll", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/device/poll", (params) => {
      expect(params).toEqual({ provider: "work", flowId: "flow-2", originClientId: "test-tab" });
      return { state: "pending" };
    });
    const result = await credentialsStore.getState().devicePoll("work", "flow-2");
    expect(result.state).toBe("pending");
  });

  test("a pending device poll does not discard an in-flight model refresh", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    // A refresh is out when the user's device poll answers "pending": that
    // answer writes nothing, so the listing it fetched is still good.
    let finishRefresh!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/refreshModels",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    const refresh = credentialsStore.getState().refreshModels("work");
    await Promise.resolve();
    fake.on("evener/auth/device/poll", () => ({ state: "pending" }));
    await credentialsStore.getState().devicePoll("work", "flow-2");
    finishRefresh({ instances: [{ ...ONE_INSTANCE, models: [{ id: "live-new" }] }], availableProviders: [] });
    await refresh;
    expect(credentialsStore.getState().instances[0]?.models?.map((model) => model.id)).toEqual(["live-new"]);
  });
});

describe("useCredentialsStore", () => {
  test("selector overload returns a derived value reactively", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    const { result } = renderHook(() => useCredentialsStore((s) => s.instances.length));
    expect(result.current).toBe(0);
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    expect(result.current).toBe(1);
  });

  test("no-selector overload returns the whole state", () => {
    const { result } = renderHook(() => useCredentialsStore());
    expect(result.current.instances).toEqual([]);
    expect(typeof result.current.fetch).toBe("function");
  });
});

describe("notification-triggered refetch", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  test("evener/auth/updated schedules a debounced fetch, 250ms", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch(); // initial load; also wires notification handling
    const updated: InstanceListResponse = {
      instances: [{ ...ONE_INSTANCE, hasStoredOAuth: false }],
      availableProviders: [],
    };
    fake.on("evener/instance/list", () => updated);

    fake.emitNotification({ method: "evener/auth/updated", params: {} });
    await vi.advanceTimersByTimeAsync(249);
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    await vi.advanceTimersByTimeAsync(1);
    expect(credentialsStore.getState().instances).toEqual(updated.instances);
  });

  test("wiring attaches as soon as a client connects, with no prior fetch call required", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.emitNotification({ method: "evener/auth/updated", params: {} });
    await vi.advanceTimersByTimeAsync(250);
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  test.each(["replacement", "reset"])(
    "%s removes old notification subscriptions and scheduled refreshes",
    async (change) => {
      const old = connectFakeClient();
      old.emitNotification({ method: "evener/auth/updated", params: {} });
      const current = change === "replacement" ? connectFakeClient() : old;
      if (change === "reset") {
        resetCredentialsStoreForTests();
        resetHostInstancesForTests();
      }
      const list = vi.fn(() => LIST_RESPONSE);
      current.on("evener/instance/list", list);
      old.emitNotification({ method: "evener/auth/updated", params: {} });
      await vi.advanceTimersByTimeAsync(300);
      expect(list).not.toHaveBeenCalled();
    },
  );

  test("an irrelevant notification does not trigger a refetch", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    fake.emitNotification(threadStartedNotification());
    await vi.advanceTimersByTimeAsync(1000);
    expect(listSpy).not.toHaveBeenCalled();
  });

  test("a burst of evener/auth/updated coalesces into exactly one refetch", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    fake.emitNotification({ method: "evener/auth/updated", params: {} });
    await vi.advanceTimersByTimeAsync(100);
    fake.emitNotification({ method: "evener/auth/updated", params: {} });
    await vi.advanceTimersByTimeAsync(100); // 200ms elapsed total, but the second notification reset the window
    expect(listSpy).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(150); // 250ms since the last notification
    expect(listSpy).toHaveBeenCalledTimes(1);
  });

  test("a successful save refreshes the listing through the store, and the echo does not refresh again", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    await credentialsStore.getState().setApiKey("work", "draft");
    // The store owns the post-mutation refresh: the component that issued the
    // save may be canceled, hidden, or unmounted before the response lands,
    // so a caller-scoped refresh leaves the listing stale. The hub also
    // BroadcastAlls the originator its own success echo (notifyAuthUpdated) -
    // that echo must not schedule a SECOND refresh on top of the store's own.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(1000);
    expect(listSpy).toHaveBeenCalledTimes(1);
  });

  test("a foreign change coalesced into this client's own refresh window keeps the refresh foreign", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch(); // initial load; also wires notification handling
    const before = credentialsStore.getState().selfRefresh;

    // A foreign change opens the debounce window first, and this client's own
    // mutation lands inside it. The single coalesced refresh carried a foreign
    // change, so it must not be marked as the store's own: the flow's
    // invalidation guard has to keep seeing it as foreign, or a genuine
    // foreign change would be silently suppressed.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "anthropic", activeSource: "store" } });
    await credentialsStore.getState().setApiKey("work", "draft");
    const marks: number[] = [];
    const unsubscribe = credentialsStore.subscribe((current) => marks.push(current.selfRefresh));
    await vi.advanceTimersByTimeAsync(250);
    unsubscribe();

    expect(credentialsStore.getState().selfRefresh).toBe(before);
    expect(marks.every((mark) => mark === before)).toBe(true);
  });

  test("a foreign change landing after this client's own request still keeps the refresh foreign", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch(); // initial load; also wires notification handling
    const before = credentialsStore.getState().selfRefresh;

    // The other order of the same rule: this client's own mutation opens the
    // debounce window and a foreign change lands inside it. The single
    // coalesced read observes that change too, so it is not the store's own
    // refresh and the flow's invalidation guard has to keep seeing it as
    // foreign. Marking it self would suppress a genuine foreign change for
    // exactly the read that carried it.
    await credentialsStore.getState().setApiKey("work", "draft");
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "anthropic", activeSource: "store" } });
    const marks: number[] = [];
    const unsubscribe = credentialsStore.subscribe((current) => marks.push(current.selfRefresh));
    await vi.advanceTimersByTimeAsync(250);
    unsubscribe();

    expect(credentialsStore.getState().selfRefresh).toBe(before);
    expect(marks.every((mark) => mark === before)).toBe(true);
  });

  test("a refresh serving only this client's own mutation still carries the self mark", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch();
    const before = credentialsStore.getState().selfRefresh;

    await credentialsStore.getState().setApiKey("work", "draft");
    await vi.advanceTimersByTimeAsync(250);

    expect(credentialsStore.getState().selfRefresh).toBeGreaterThan(before);
  });

  test("another client's auth change still refetches after a local mutation", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    await credentialsStore.getState().setApiKey("work", "draft");
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "anthropic", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);
  });

  test("a same-provider notification with no recent local mutation still refetches", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    // An unrelated client mutating the same provider is indistinguishable from
    // an echo by payload alone - only a recent LOCAL mutation suppresses.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);
  });

  test("a same-provider change in the echo window after a successful save still refetches", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    await credentialsStore.getState().setApiKey("work", "draft");
    // The store's own post-save refresh runs first.
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);

    // The real echo never arrives; another client's same-provider change does,
    // inside the correlation window. The marker must not swallow it: the
    // listing has to be re-read even though the notification matched.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(2);
  });

  test("a slow save's response extends the window for its own late echo", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    let finishSave: ((value: AuthStatusResponse) => void) | undefined;
    fake.on(
      "evener/auth/apiKey/set",
      () =>
        new Promise<AuthStatusResponse>((resolve) => {
          finishSave = resolve;
        }),
    );
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);
    const before = credentialsStore.getState().selfRefresh;

    const pending = credentialsStore.getState().setApiKey("work", "draft");
    // A loaded host (or a slow hub) answers the save later than the fixed
    // window measured from the issue: the response lands at 2001ms. The hub's
    // broadcast follows the response it answers, so the echo arrives after
    // that - still this client's own.
    await vi.advanceTimersByTimeAsync(2001);
    finishSave?.({ provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false });
    await pending;
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);

    // The echo coalesces with the store's own post-save refresh into exactly
    // one read, and that read keeps the self mark: the guided flow must not
    // read its own successful save as "Connection or configuration changed".
    expect(listSpy).toHaveBeenCalledTimes(1);
    expect(credentialsStore.getState().selfRefresh).toBeGreaterThan(before);
  });

  test("the echo correlation window is bounded", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    await credentialsStore.getState().setApiKey("work", "draft");
    await vi.advanceTimersByTimeAsync(2001);
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    // Two refreshes: the store's own post-save refresh (inside the window) and
    // this late echo's - past the window the marker is gone, so a same-provider
    // notification is an unrelated client's change again.
    expect(listSpy).toHaveBeenCalledTimes(2);
  });

  test("a failed save does not suppress a later same-provider notification", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", () => {
      throw new Error("hub refused the key");
    });
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    await expect(credentialsStore.getState().setApiKey("work", "draft")).rejects.toThrow("hub refused the key");
    // A failed mutation broadcasts nothing, so this same-provider notification
    // is an unrelated client's change - it must still refetch.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);
  });

  test("a routine pending device poll does not extend the suppression window", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/device/poll", () => ({ provider: "work", state: "pending" }));
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    await credentialsStore.getState().devicePoll("work", "flow");
    // A pending poll broadcasts nothing; an unrelated same-provider change
    // arriving during the poll loop must still refetch.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);
  });

  test("an authorized device poll refreshes the listing even with no caller left to do it", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/device/poll", () => ({ provider: "work", state: "authorized" }));
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    await credentialsStore.getState().devicePoll("work", "flow");
    // The polling dialog may already be closed by the time authorization
    // lands; the store owns the refresh, so the listing updates anyway.
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);
  });

  test("the echo correlation is consumed, not window-wide", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    await credentialsStore.getState().setApiKey("work", "draft");
    // The own echo, consumed by the correlation.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(50);
    // A second same-provider notification inside the old 2s window is an
    // unrelated client's change: the marker was consumed, so it refetches.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(300);
    expect(listSpy).toHaveBeenCalledTimes(1);
  });

  test("an echo that names this page is consumed as its own", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);
    const before = credentialsStore.getState().selfRefresh;

    await credentialsStore.getState().setApiKey("work", "draft");
    // The hub echoes the id this page's mutation carried, so the broadcast
    // names this page as its originator: it is this client's own echo, and it
    // coalesces with the store's post-save refresh instead of scheduling a
    // second read on top of it.
    fake.emitNotification({
      method: "evener/auth/updated",
      params: { provider: "work", activeSource: "store", originClientId: "test-tab" },
    });
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);
    expect(credentialsStore.getState().selfRefresh).toBeGreaterThan(before);

    // Consumed by identity, not matched window-wide: the marker is gone, so a
    // second same-provider notification is an unrelated client's change again.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(2);
  });

  test("an echo that names another client leaves this page's marker alone", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);
    const before = credentialsStore.getState().selfRefresh;

    await credentialsStore.getState().setApiKey("work", "draft");
    // Another client's change names that client, so it is foreign however close
    // it lands to this page's own mutation: the coalesced read carries a change
    // this store did not make, so it must not take the self mark - marking it
    // self would keep the guided flow's invalidation guard from seeing it.
    fake.emitNotification({
      method: "evener/auth/updated",
      params: { provider: "work", activeSource: "store", originClientId: "tab-b" },
    });
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);
    expect(credentialsStore.getState().selfRefresh).toBe(before);
    // Anchored after the foreign read so this assertion can only be satisfied
    // by a read the follow-up itself schedules.
    const afterForeign = credentialsStore.getState().selfRefresh;

    // The marker survived that foreign notification, so this page's own echo
    // is still attributed to this page when it arrives without an id (an older
    // hub). Under the provider-plus-window rule the notification above retired
    // the marker, and this id-less echo would read as foreign - scheduling the
    // read foreign-marked, so the self mark below would not move.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(credentialsStore.getState().selfRefresh).toBeGreaterThan(afterForeign);
  });

  test("a second same-provider mutation's own echo still carries the self mark", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    fake.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    // Two same-provider mutations: each one broadcasts exactly one own echo.
    // The second marker must not overwrite the first, or the first echo
    // consumes the only marker and the second self echo is misread as an
    // unrelated client's change.
    await credentialsStore.getState().setApiKey("work", "first");
    await credentialsStore.getState().setApiKey("work", "second");
    await vi.advanceTimersByTimeAsync(250); // the two post-save refreshes coalesce

    // First echo: consumed by the first outstanding marker.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);

    const beforeSecondEcho = credentialsStore.getState().selfRefresh;
    // Second echo: still this client's own echo, so its refresh keeps the
    // self mark. With a single overwritten timestamp this notification is
    // treated as foreign and the coalesced read drops the self mark, which
    // spuriously invalidates the guided flow.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(credentialsStore.getState().selfRefresh).toBeGreaterThan(beforeSecondEcho);
  });

  test("a same-provider change during a pending device poll is refetched without waiting for the poll", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    let resolvePoll: (value: { provider: string; state: string }) => void = () => {};
    const pollAnswer = new Promise<{ provider: string; state: string }>((resolve) => {
      resolvePoll = resolve;
    });
    fake.on("evener/auth/device/poll", () => pollAnswer);
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    const pending = credentialsStore.getState().devicePoll("work", "flow");
    // Another client's same-provider change lands while the poll is in flight.
    // The correlation may read it as this poll's echo, but the listing is still
    // re-read: the marker must not permanently swallow a foreign change.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);
    resolvePoll({ provider: "work", state: "pending" });
    await pending;
    // A pending result proves no echo of ours was coming; that makeup read
    // coalesces with the one above.
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);
  });

  test("a same-provider change during an in-flight save is refetched without waiting for the save", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    let rejectSave: (reason: Error) => void = () => {};
    const saveAnswer = new Promise<never>((_resolve, reject) => {
      rejectSave = reject;
    });
    fake.on("evener/auth/apiKey/set", () => saveAnswer);
    await credentialsStore.getState().fetch();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    const pending = credentialsStore.getState().setApiKey("work", "draft");
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);
    rejectSave(new Error("hub refused the key"));
    await expect(pending).rejects.toThrow("hub refused the key");
    // The failure proves no echo of ours was coming; that makeup coalesces with
    // the notification's own read.
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy).toHaveBeenCalledTimes(1);
  });

  test("a marker from a replaced connection cannot suppress a later notification", async () => {
    const first = connectFakeClient();
    first.on("evener/instance/list", () => LIST_RESPONSE);
    first.on("evener/auth/apiKey/set", ({ provider }) => ({
      provider,
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    await credentialsStore.getState().fetch();
    await credentialsStore.getState().setApiKey("work", "draft");
    // The client is replaced before the mutation's echo can arrive: whatever
    // same-provider notification comes on the NEW connection is not that lost
    // echo, so a surviving marker must not swallow it.
    const second = connectFakeClient();
    const listSpy = vi.fn(() => LIST_RESPONSE);
    second.on("evener/instance/list", listSpy);
    // Let the reconnect's own restore fetch settle before counting.
    await vi.advanceTimersByTimeAsync(0);
    const before = listSpy.mock.calls.length;
    second.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy.mock.calls.length).toBe(before + 1);
  });

  test("a stale save failure from a replaced connection cannot retire the new connection's marker", async () => {
    const first = connectFakeClient();
    first.on("evener/instance/list", () => LIST_RESPONSE);
    let rejectFirst: (reason: Error) => void = () => {};
    const firstSave = new Promise<AuthStatusResponse>((_resolve, reject) => {
      rejectFirst = reject;
    });
    first.on("evener/auth/apiKey/set", () => firstSave);
    await credentialsStore.getState().fetch();

    const firstPending = credentialsStore.getState().setApiKey("work", "first");
    // The connection is replaced while the first save is still in flight: the
    // old connection's callback lands after its markers were cleared.
    // The replacement answers its own restore read: a connection whose own
    // listing has not landed yet refuses writes (requireWritableClient).
    const second = new FakeClient("ready");
    second.on("evener/instance/list", () => LIST_RESPONSE);
    connectionStore.getState().connect(second);
    await vi.advanceTimersByTimeAsync(0); // the reconnect's restore fetch settles

    let resolveSecond: (value: AuthStatusResponse) => void = () => {};
    const secondSave = new Promise<AuthStatusResponse>((resolve) => {
      resolveSecond = resolve;
    });
    second.on("evener/auth/apiKey/set", () => secondSave);
    const secondPending = credentialsStore.getState().setApiKey("work", "second");

    // The stale rejection must be ignored whole - it belongs to a connection
    // that is gone - and must not retire the new connection's marker.
    rejectFirst(new Error("hub refused the old key"));
    await expect(firstPending).rejects.toThrow("hub refused the old key");

    resolveSecond({ provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false });
    await secondPending;
    const before = credentialsStore.getState().selfRefresh;

    // The new connection's own echo is still consumed as its own, so the
    // coalesced read keeps the self mark.
    second.emitNotification({ method: "evener/auth/updated", params: { provider: "work", activeSource: "store" } });
    await vi.advanceTimersByTimeAsync(250);

    expect(credentialsStore.getState().selfRefresh).toBeGreaterThan(before);
  });

  test("a stale save success from a replaced connection does not schedule a self-marked read", async () => {
    const first = connectFakeClient();
    first.on("evener/instance/list", () => LIST_RESPONSE);
    let resolveFirst: (value: AuthStatusResponse) => void = () => {};
    const firstSave = new Promise<AuthStatusResponse>((resolve) => {
      resolveFirst = resolve;
    });
    first.on("evener/auth/apiKey/set", () => firstSave);
    await credentialsStore.getState().fetch();

    const firstPending = credentialsStore.getState().setApiKey("work", "first");
    // The connection is replaced before the first save's success lands.
    const second = connectFakeClient();
    second.on("evener/instance/list", () => LIST_RESPONSE);
    await vi.advanceTimersByTimeAsync(0); // the reconnect's restore fetch settles
    const before = credentialsStore.getState().selfRefresh;

    // The old connection's success must not schedule a read on the new one,
    // let alone mark it as this connection's own refresh.
    resolveFirst({ provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false });
    await firstPending;
    await vi.advanceTimersByTimeAsync(250);

    expect(credentialsStore.getState().selfRefresh).toBe(before);
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  test("a background refetch race with no client connected is swallowed, not an unhandled rejection", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    fake.emitNotification({ method: "evener/auth/updated", params: {} });
    // Disconnect before the debounce fires - fetch()'s own requireClient()
    // throws outside its try/catch by design (this store's own doc comment),
    // so the scheduled background call must swallow that rejection itself
    // rather than surfacing an unhandled promise rejection that would fail
    // this test even though nothing here ever awaits it directly.
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    await vi.advanceTimersByTimeAsync(250);
    // Reaching this line (rather than the test failing on an unhandled
    // rejection) is the real assertion; this just also confirms the failed
    // background attempt left the prior successful load untouched.
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  test("a superseded set-default schedules the store's own listing read", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch(); // initial load; also wires notification handling
    const listSpy = vi.fn(() => LIST_RESPONSE);
    fake.on("evener/instance/list", listSpy);

    // Hold the set-default response so a newer listing read can overtake it.
    let finishSetDefault!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/setDefault",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          finishSetDefault = resolve;
        }),
    );
    const pending = credentialsStore.getState().setDefault("work");
    await Promise.resolve();

    // A listing read issued after the set-default wins the store race, so the
    // set-default response lands superseded and applyMutation discards it.
    await credentialsStore.getState().fetch();
    const readsBefore = listSpy.mock.calls.length;

    finishSetDefault(LIST_RESPONSE);
    expect(await pending).toBe(false); // the superseded verdict

    // The discarded response carried the new default flag, and the read that
    // won may have started before the hub applied it. A superseded verdict must
    // therefore schedule the store's own post-mutation read, or the listing
    // keeps the old flag until something else refreshes it.
    await vi.advanceTimersByTimeAsync(250);
    expect(listSpy.mock.calls.length).toBe(readsBefore + 1);
  });

  // A read is stamped with the registry revision it was issued under, so a
  // snapshot that arrives afterwards - including the registry's FIRST answer -
  // supersedes it, and nothing read under an older one is ever current.
  test("a read records the registry revision it was issued under", async () => {
    const fake = connectFakeClient();
    serveRemoteList(fake, REMOTE_LIST);
    hostsStore.getState().resetForTests();

    await fetchHost("buildbox"); // issued while the registry is unread
    const issued = hostPartition(hostInstancesStore.getState(), "buildbox");
    expect(issued.registryRevision).toBe(0);
    expect(hostsStore.getState().revision).toBe(0);

    // The registry answers, naming the host: that is a different answer, so the
    // revision advances and the rows read before it are no longer current.
    hostsStore.setState({ load: { phase: "ready", hosts: [registryRow({ name: "buildbox" })] } });
    expect(hostsStore.getState().revision).toBe(1);
    expect(hostPartition(hostInstancesStore.getState(), "buildbox").registryRevision).not.toBe(
      hostsStore.getState().revision,
    );
    hostsStore.getState().resetForTests();
  });
});

// registryRow builds one registry row for the invalidation tests below - the
// same shape stores/hosts.ts publishes in its ready snapshot.
function registryRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: true, midAttach: false, removed: false, ...overrides };
}

function remoteReads(fake: FakeClient): number {
  return fake.calls.filter((call) => call.method === "evener/host/request").length;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

// --- The registry-revision rule (round 7 consolidation) ----------------------
//
// ONE rule ties every remote listing to the registry: a partition is current only
// while it was read under the registry's CURRENT revision, and useHostInstances is
// the one place a host is (re)read - when a consumer watches a host the registry
// names and there is no partition for the current revision. Every corner below is
// that same rule, entered by a different door; nothing tracks dropped partitions.

// (a) The name is re-registered as a different machine.
test("a re-registered host's rows are withheld and re-read", async () => {
  const fake = connectFakeClient();
  serveRemoteList(fake, REMOTE_LIST);
  hostsStore.setState({ load: { phase: "ready", hosts: [registryRow({ name: "buildbox", address: "a.example" })] } });

  const { result } = renderHook(() => useHostInstances("buildbox"));
  await waitFor(() => expect(result.current.instances).toEqual([REMOTE_INSTANCE]));
  expect(remoteReads(fake)).toBe(1);

  // The name now means a different machine, and the new host's listing is held
  // open so the withholding is observable.
  const replaced = deferred<HostForwardedResult>();
  const replacedList: InstanceListResponse = {
    instances: [{ ...REMOTE_INSTANCE, name: "replaced-anthropic" }],
    availableProviders: [],
  };
  fake.on("evener/host/request", () => replaced.promise);
  await act(async () => {
    hostsStore.setState({
      load: { phase: "ready", hosts: [registryRow({ name: "buildbox", address: "b.example" })] },
    });
  });

  // The previous registration's rows are not shown, and the store re-reads the
  // name under the registry's new snapshot.
  expect(result.current.instances).toEqual([]);
  expect(remoteReads(fake)).toBe(2);

  await act(async () => replaced.resolve(replacedList as unknown as HostForwardedResult));
  await waitFor(() => expect(result.current.instances).toEqual(replacedList.instances));
  hostsStore.getState().resetForTests();
});

// (b) The host is removed and re-added while a consumer is mounted.
test("a host removed and re-added while mounted is read again", async () => {
  const fake = connectFakeClient();
  let configured = true;
  fake.on("evener/host/request", () => {
    if (!configured) throw new WireError("host buildbox is not configured", -32000);
    return REMOTE_LIST as unknown as HostForwardedResult;
  });
  hostsStore.setState({ load: { phase: "ready", hosts: [registryRow({ name: "buildbox" })] } });

  const { result } = renderHook(() => useHostInstances("buildbox"));
  await waitFor(() => expect(result.current.instances).toEqual([REMOTE_INSTANCE]));
  expect(remoteReads(fake)).toBe(1);

  // Removed: its rows are dropped and the hub refuses the name, so nothing is
  // shown - the old registration is not left on screen.
  configured = false;
  await act(async () => {
    hostsStore.setState({ load: { phase: "ready", hosts: [] } });
  });
  await waitFor(() => expect(remoteReads(fake)).toBe(2));
  expect(result.current.instances).toEqual([]);

  // Re-added: the store reads it again, with no remount and no bookkeeping.
  configured = true;
  await act(async () => {
    hostsStore.setState({ load: { phase: "ready", hosts: [registryRow({ name: "buildbox" })] } });
  });
  await waitFor(() => expect(result.current.instances).toEqual([REMOTE_INSTANCE]));
  expect(remoteReads(fake)).toBe(3);
  hostsStore.getState().resetForTests();
});

// (c) The connection's client is replaced while a registry read is in flight.
// The superseded client's answer must not publish over the replacement's view.
test("a registry read in flight across a client swap cannot publish the old hub's hosts", async () => {
  const a = connectFakeClient();
  const releaseA = deferRequest<unknown>(a, "evener/host/list");
  const fromA = hostsStore.getState().fetch();
  await Promise.resolve();

  // Replaced before A answers, with B's own registry read also still out.
  const b = connectFakeClient();
  const releaseB = deferRequest<unknown>(b, "evener/host/list");
  const fromB = hostsStore.getState().fetch();
  await Promise.resolve();

  await act(async () => releaseA({ hosts: [registryRow({ name: "oldhub", address: "a.example" })] }));
  await fromA;
  const mid = hostsStore.getState().load;
  expect(mid.phase === "ready" && mid.hosts.some((row) => row.name === "oldhub")).toBe(false);

  await act(async () => releaseB({ hosts: [registryRow({ name: "newhub", address: "b.example" })] }));
  await fromB;
  const settled = hostsStore.getState().load;
  expect(settled.phase === "ready" && settled.hosts.map((row) => row.name)).toEqual(["newhub"]);
  hostsStore.getState().resetForTests();
});

// The rule's cost ceiling: an unchanged snapshot advances no revision, so the
// poll cadence re-reads nothing.
test("an unchanged registry snapshot never re-reads a host", async () => {
  const fake = connectFakeClient();
  serveRemoteList(fake, REMOTE_LIST);
  const row = registryRow({ name: "buildbox" });
  hostsStore.setState({ load: { phase: "ready", hosts: [row] } });

  const { result } = renderHook(() => useHostInstances("buildbox"));
  await waitFor(() => expect(result.current.instances).toEqual([REMOTE_INSTANCE]));
  expect(remoteReads(fake)).toBe(1);

  // Two quiet polls carrying the same registration as fresh arrays, the way the
  // wire does: the read count must not move.
  fake.on("evener/host/list", () => ({ hosts: [{ ...row }] }));
  await act(async () => {
    await hostsStore.getState().refresh();
  });
  await act(async () => {
    await hostsStore.getState().refresh();
  });

  expect(remoteReads(fake)).toBe(1);
  expect(result.current.instances).toEqual([REMOTE_INSTANCE]);
  hostsStore.getState().resetForTests();
});

// The read waits for a registry read that is already on its way, rather than
// reading under an answer that is about to be replaced (one wasted request per
// deep-link otherwise).
test("a remote host waits for a registry read that is in flight", async () => {
  const fake = connectFakeClient();
  const registry = deferRequest<unknown>(fake, "evener/host/list");
  serveRemoteList(fake, REMOTE_LIST);
  hostsStore.getState().resetForTests();
  const reading = hostsStore.getState().fetch();
  await act(async () => {});

  const { result } = renderHook(() => useHostInstances("buildbox"));
  await act(async () => {});
  expect(remoteReads(fake)).toBe(0);
  expect(result.current.instances).toEqual([]);

  await act(async () => {
    registry({ hosts: [registryRow({ name: "buildbox" })] });
  });
  await reading;
  await waitFor(() => expect(result.current.instances).toEqual([REMOTE_INSTANCE]));
  expect(remoteReads(fake)).toBe(1);
  hostsStore.getState().resetForTests();
});

// A reconnect on the SAME client is not a client replacement, so the registry
// revision does not move - but the connection did, and the host's listing may
// have changed with it. The partition is tied to the connection generation it
// was read under, so a reconnect converges.
test("a same-client reconnect re-reads a remote host", async () => {
  const fake = connectFakeClient();
  serveRemoteList(fake, REMOTE_LIST);
  hostsStore.setState({ load: { phase: "ready", hosts: [registryRow({ name: "buildbox" })] } });

  const { result } = renderHook(() => useHostInstances("buildbox"));
  await waitFor(() => expect(result.current.instances).toEqual([REMOTE_INSTANCE]));
  expect(remoteReads(fake)).toBe(1);

  // The SAME client's transport flaps and recovers: no replacement, no registry
  // change.
  await act(async () => {
    connectionStore.setState({ state: "reconnecting" });
    connectionStore.setState({ state: "ready" });
    // Let the reconnect's own read land inside this act.
    await Promise.resolve();
  });

  await waitFor(() => expect(remoteReads(fake)).toBe(2));
  await waitFor(() => expect(result.current.instances).toEqual([REMOTE_INSTANCE]));
  hostsStore.getState().resetForTests();
});

// The registry's FIRST answer is an answer even when it names no host: a read
// taken while the registry was unread must not survive it (an empty first
// snapshot used to be indistinguishable from "never published").
test("a read taken while the registry was unread does not survive its first empty answer", async () => {
  const fake = connectFakeClient();
  serveRemoteList(fake, REMOTE_LIST);
  hostsStore.getState().resetForTests();

  await fetchHost("buildbox");
  expect(hostPartition(hostInstancesStore.getState(), "buildbox").registryRevision).toBe(0);

  hostsStore.setState({ load: { phase: "ready", hosts: [] } });

  expect(hostsStore.getState().revision).toBe(1);
  expect(hostPartition(hostInstancesStore.getState(), "buildbox").registryRevision).not.toBe(
    hostsStore.getState().revision,
  );
  hostsStore.getState().resetForTests();
});

// An answer that lands after the registry moved on was issued under the old
// snapshot: it is committed to the partition but never shown, and the read for
// the current snapshot is what a consumer sees.
test("an answer issued under an older registry snapshot is never shown", async () => {
  const fake = connectFakeClient();
  hostsStore.setState({ load: { phase: "ready", hosts: [registryRow({ name: "buildbox", address: "a.example" })] } });
  const answers: Array<(value: HostForwardedResult) => void> = [];
  fake.on(
    "evener/host/request",
    () =>
      new Promise<HostForwardedResult>((resolve) => {
        answers.push(resolve);
      }),
  );

  const { result } = renderHook(() => useHostInstances("buildbox"));
  await act(async () => {});
  expect(answers).toHaveLength(1);

  // The registry re-registers the name while that read is out.
  await act(async () => {
    hostsStore.setState({
      load: { phase: "ready", hosts: [registryRow({ name: "buildbox", address: "b.example" })] },
    });
  });
  expect(answers).toHaveLength(2);

  // The superseded answer lands, and is withheld.
  await act(async () => {
    answers[0]?.(REMOTE_LIST as unknown as HostForwardedResult);
  });
  expect(result.current.instances).toEqual([]);

  await act(async () => {
    answers[1]?.(REMOTE_LIST as unknown as HostForwardedResult);
  });
  expect(result.current.instances).toEqual([REMOTE_INSTANCE]);
  hostsStore.getState().resetForTests();
});
