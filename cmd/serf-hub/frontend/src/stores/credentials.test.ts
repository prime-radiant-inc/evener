import { cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { InstanceEntry, InstanceListResponse } from "../protocol/types.gen";
import { connectionStore } from "./connection";
import { credentialsStore, resetCredentialsStoreForTests, useCredentialsStore } from "./credentials";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const ONE_INSTANCE: InstanceEntry = {
  name: "work",
  type: "anthropic",
  apiStyle: "",
  baseUrl: "",
  isDefault: true,
  authModes: ["apiKey", "oauth"],
  activeSource: "oauth",
  hasStoredFile: false,
  hasStoredOAuth: true,
  envVar: "",
  storedEmail: "me@example.com",
};

const LIST_RESPONSE: InstanceListResponse = {
  instances: [ONE_INSTANCE],
  availableTypes: ["anthropic", "openai"],
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
  test("throws if no client is connected", async () => {
    await expect(credentialsStore.getState().fetch()).rejects.toThrow(/no client connected/);
  });

  test("populates instances/availableTypes from serf/instance/list on success", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();
    const state = credentialsStore.getState();
    expect(state.instances).toEqual([ONE_INSTANCE]);
    expect(state.availableTypes).toEqual(["anthropic", "openai"]);
    expect(state.loading).toBe(false);
    expect(state.error).toBeNull();
  });

  test("sets loading true for the duration of the request", async () => {
    const fake = connectFakeClient();
    let resolveRequest: (() => void) | undefined;
    fake.on(
      "serf/instance/list",
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
    fake.on("serf/instance/list", () => LIST_RESPONSE);
    await credentialsStore.getState().fetch();

    fake.on("serf/instance/list", () => {
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
  test("create() calls serf/instance/create and applies the returned list", async () => {
    const fake = connectFakeClient();
    const created: InstanceListResponse = { instances: [ONE_INSTANCE], availableTypes: ["anthropic"] };
    fake.on("serf/instance/create", (params) => {
      expect(params).toEqual({ type: "anthropic", name: "work", apiStyle: "", baseUrl: "" });
      return created;
    });
    await credentialsStore.getState().create({ type: "anthropic", name: "work", apiStyle: "", baseUrl: "" });
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  test("edit() calls serf/instance/edit and applies the returned list", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", apiStyle: "responses", baseUrl: "https://x" });
      return LIST_RESPONSE;
    });
    await credentialsStore.getState().edit({ name: "work", apiStyle: "responses", baseUrl: "https://x" });
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  test("remove() calls serf/instance/remove and applies the returned list", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/remove", (params) => {
      expect(params).toEqual({ name: "work" });
      return { instances: [], availableTypes: ["anthropic"] };
    });
    await credentialsStore.getState().remove("work");
    expect(credentialsStore.getState().instances).toEqual([]);
  });

  test("setDefault() calls serf/instance/setDefault and applies the returned list", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/setDefault", (params) => {
      expect(params).toEqual({ name: "work" });
      return LIST_RESPONSE;
    });
    await credentialsStore.getState().setDefault("work");
    expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
  });

  test("a mutation failure rejects and does not touch stored instances", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/create", () => {
      throw new Error("name already exists");
    });
    await expect(
      credentialsStore.getState().create({ type: "anthropic", name: "work", apiStyle: "", baseUrl: "" }),
    ).rejects.toThrow("name already exists");
    expect(credentialsStore.getState().instances).toEqual([]);
  });
});

describe("auth RPCs: thin proxies, no local state mutation", () => {
  test("setApiKey() calls serf/auth/apiKey/set and returns its response", async () => {
    const fake = connectFakeClient();
    fake.on("serf/auth/apiKey/set", (params) => {
      expect(params).toEqual({ provider: "work", value: "sk-secret" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "file", hasStoredOAuth: false };
    });
    const result = await credentialsStore.getState().setApiKey("work", "sk-secret");
    expect(result.activeSource).toBe("file");
    // Never stored on the store itself - never-echo invariant.
    expect(JSON.stringify(credentialsStore.getState())).not.toContain("sk-secret");
  });

  test("logout() calls serf/auth/logout", async () => {
    const fake = connectFakeClient();
    fake.on("serf/auth/logout", (params) => {
      expect(params).toEqual({ provider: "work" });
      return {
        removed: true,
        status: { provider: "work", supported: true, signedIn: false, activeSource: "absent", hasStoredOAuth: false },
      };
    });
    const result = await credentialsStore.getState().logout("work");
    expect(result.removed).toBe(true);
  });

  test("loginStart() calls serf/auth/login/start", async () => {
    const fake = connectFakeClient();
    fake.on("serf/auth/login/start", (params) => {
      expect(params).toEqual({ provider: "work" });
      return { provider: "work", flowId: "flow-1", url: "https://auth.example.com/start" };
    });
    const result = await credentialsStore.getState().loginStart("work");
    expect(result.url).toBe("https://auth.example.com/start");
  });

  test("loginComplete() calls serf/auth/login/complete", async () => {
    const fake = connectFakeClient();
    fake.on("serf/auth/login/complete", (params) => {
      expect(params).toEqual({ provider: "work", flowId: "flow-1", redirectUrl: "https://redirect" });
      return {
        status: { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true },
      };
    });
    const result = await credentialsStore.getState().loginComplete("work", "flow-1", "https://redirect");
    expect(result.status.signedIn).toBe(true);
  });

  test("deviceStart() calls serf/auth/device/start", async () => {
    const fake = connectFakeClient();
    fake.on("serf/auth/device/start", (params) => {
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

  test("devicePoll() calls serf/auth/device/poll", async () => {
    const fake = connectFakeClient();
    fake.on("serf/auth/device/poll", (params) => {
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
    fake.on("serf/instance/list", () => LIST_RESPONSE);
    const { result } = renderHook(() => useCredentialsStore((s) => s.instances.length));
    expect(result.current).toBe(0);
    await credentialsStore.getState().fetch();
    expect(result.current).toBe(1);
  });

  test("no-selector overload returns the whole state", () => {
    const { result } = renderHook(() => useCredentialsStore());
    expect(result.current.instances).toEqual([]);
    expect(typeof result.current.fetch).toBe("function");
  });
});
