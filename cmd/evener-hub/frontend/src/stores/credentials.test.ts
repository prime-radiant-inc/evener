import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import { threadStartedNotification } from "../protocol/testing/notifications";
import type { AuthTestResponse, InstanceEntry, InstanceListResponse } from "../protocol/types.gen";
import { connectionStore } from "./connection";
import { credentialsStore, resetCredentialsStoreForTests, useCredentialsStore } from "./credentials";

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
