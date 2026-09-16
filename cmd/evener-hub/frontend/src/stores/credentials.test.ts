import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import { threadStartedNotification } from "../protocol/testing/notifications";
import type {
  AuthTestResponse,
  HostForwardedResult,
  HostRequestParams,
  InstanceEntry,
  InstanceListResponse,
} from "../protocol/types.gen";
import { connectionStore } from "./connection";
import { credentialsStore, hostPartition, resetCredentialsStoreForTests, useCredentialsStore } from "./credentials";

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
      expect(params).toEqual({ name: "work", base: "openai-codex", baseUrl: "" });
      return created;
    });
    await credentialsStore.getState().create({ name: "work", base: "openai-codex", baseUrl: "" });
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  test("edit() calls evener/instance/edit and applies the returned list", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", baseUrl: "https://x" });
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

  test("remove() calls evener/instance/remove and applies the returned list", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/remove", (params) => {
      expect(params).toEqual({ name: "work" });
      return { instances: [], availableProviders: [] };
    });
    await credentialsStore.getState().remove("work");
    expect(credentialsStore.getState().instances).toEqual([]);
  });

  test("setDefault() calls evener/instance/setDefault and applies the returned list", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/setDefault", (params) => {
      expect(params).toEqual({ name: "work" });
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

    await credentialsStore.getState().fetchHost("buildbox");

    const state = credentialsStore.getState();
    expect(state.instances).toEqual([ONE_INSTANCE]); // the controller's own list
    expect(hostPartition(state, "buildbox").instances).toEqual([REMOTE_INSTANCE]);
    expect(hostPartition(state, "buildbox").loading).toBe(false);
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
    await credentialsStore.getState().fetchHost("buildbox");

    fake.emitNotification({ method: "evener/auth/updated", params: {} });
    await vi.advanceTimersByTimeAsync(250);

    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    expect(hostPartition(credentialsStore.getState(), "buildbox").instances).toEqual([REMOTE_INSTANCE]);
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
    await credentialsStore.getState().fetchHost("buildbox");

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
    expect(hostPartition(credentialsStore.getState(), "buildbox").instances).toEqual([REMOTE_INSTANCE]);
    await vi.advanceTimersByTimeAsync(1);
    expect(hostPartition(credentialsStore.getState(), "buildbox").instances).toEqual(remoteAfter.instances);
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
    const remote = credentialsStore.getState().fetchHost("buildbox");
    await Promise.resolve();

    fake.on("evener/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    finishRemote(REMOTE_LIST);
    await remote;

    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    expect(hostPartition(credentialsStore.getState(), "buildbox").instances).toEqual([REMOTE_INSTANCE]);
  });

  test("a controller-scoped instance mutation leaves a remote host's listing intact", async () => {
    const fake = connectFakeClient();
    serveRemoteList(fake, REMOTE_LIST);
    await credentialsStore.getState().fetchHost("buildbox");

    fake.on("evener/instance/remove", () => ({ instances: [], availableProviders: [] }));
    await credentialsStore.getState().remove("buildbox-anthropic");

    expect(credentialsStore.getState().instances).toEqual([]);
    expect(hostPartition(credentialsStore.getState(), "buildbox").instances).toEqual([REMOTE_INSTANCE]);
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

    await credentialsStore.getState().fetchHost("buildbox");

    const state = credentialsStore.getState();
    expect(hostPartition(state, "buildbox").error).toBe("remote hub unavailable");
    expect(hostPartition(state, "buildbox").loading).toBe(false);
    expect(state.error).toBeNull();
    expect(state.loading).toBe(false);
    expect(state.instances).toEqual([ONE_INSTANCE]);
  });

  test("a reconnect releases a remote host's in-flight status without discarding its listing", async () => {
    const fake = connectFakeClient();
    serveRemoteList(fake, REMOTE_LIST);
    await credentialsStore.getState().fetchHost("buildbox");
    let finish!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/host/request",
      () =>
        new Promise<HostForwardedResult>((resolve) => {
          finish = (value) => resolve(value as unknown as HostForwardedResult);
        }),
    );
    const reload = credentialsStore.getState().fetchHost("buildbox");
    await Promise.resolve();
    expect(hostPartition(credentialsStore.getState(), "buildbox").loading).toBe(true);

    fake.emitStateChange("reconnecting");
    expect(hostPartition(credentialsStore.getState(), "buildbox").loading).toBe(false);
    finish({ instances: [{ ...REMOTE_INSTANCE, name: "stale" }], availableProviders: [] });
    await reload;
    // The interrupted load cannot commit over the connection transition, and
    // the listing it was refreshing is still the one the store holds.
    expect(hostPartition(credentialsStore.getState(), "buildbox").instances).toEqual([REMOTE_INSTANCE]);
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

  test("setApiKey() calls evener/auth/apiKey/set and returns its response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/apiKey/set", (params) => {
      expect(params).toEqual({ provider: "work", value: "sk-secret" });
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
      expect(params).toEqual({ provider: "work" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true };
    });
    const result = await credentialsStore.getState().clearStoredKey("work");
    expect(result.activeSource).toBe("oauth");
    expect(result.hasStoredOAuth).toBe(true);
  });

  test("logout() calls evener/auth/logout", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/logout", (params) => {
      expect(params).toEqual({ provider: "work" });
      return {
        removed: true,
        status: { provider: "work", supported: true, signedIn: false, activeSource: "none", hasStoredOAuth: false },
      };
    });
    const result = await credentialsStore.getState().logout("work");
    expect(result.removed).toBe(true);
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
      expect(params).toEqual({ provider: "work", flowId: "flow-1", redirectUrl: "https://redirect" });
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
      expect(params).toEqual({ provider: "work", flowId: "flow-2" });
      return { state: "pending" };
    });
    const result = await credentialsStore.getState().devicePoll("work", "flow-2");
    expect(result.state).toBe("pending");
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
      if (change === "reset") resetCredentialsStoreForTests();
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
});
