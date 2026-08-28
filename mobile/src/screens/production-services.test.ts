import { afterEach, describe, expect, it, vi } from "vitest";
import type { AnyNotification } from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  ConversationClientLike,
  LiveConversationService,
} from "../services/conversation";
import type { ProfileRedacted } from "../services/nativeProfiles";
import {
  createBrowserConceptStorage,
  createProfileScopedServices,
} from "./production-services";

afterEach(() => {
  vi.unstubAllGlobals();
});

const PROFILE: ProfileRedacted = {
  id: "profile-1",
  name: "Hub",
  origin: "http://192.168.1.10:9180",
};

function createDeferred<T>() {
  let resolvePromise: (value: T) => void = () => {
    throw new Error("deferred resolve was not initialized");
  };
  let rejectPromise: (cause: unknown) => void = () => {
    throw new Error("deferred reject was not initialized");
  };
  const promise = new Promise<T>((resolve, reject) => {
    resolvePromise = resolve;
    rejectPromise = reject;
  });
  return { promise, resolve: resolvePromise, reject: rejectPromise };
}

describe("createProfileScopedServices", () => {
  it("builds every live session service around one profile-scoped AppWire client", () => {
    const client: ConversationClientLike & {
      connect: () => Promise<unknown>;
      close: () => void;
      onStateChange: (handler: (state: string) => void) => () => void;
    } = {
      request: vi.fn(),
      onNotification: vi.fn(() => () => {}),
      connect: vi.fn(() => Promise.resolve({})),
      close: vi.fn(),
      onStateChange: vi.fn(() => () => {}),
    };
    const createClient = vi.fn(() => client);

    const scoped = createProfileScopedServices(PROFILE, createClient);
    const liveConversationService: LiveConversationService =
      scoped.conversationService;

    expect(createClient).toHaveBeenCalledWith(PROFILE);
    expect(scoped.client).not.toBe(client);
    expect(scoped.client.setActive).toBeTypeOf("function");
    expect(scoped.rosterService).toBeDefined();
    expect(scoped.rosterStore).toBeDefined();
    expect(scoped.newSessionService).toBeDefined();
    expect(liveConversationService).toBeDefined();
  });

  it("gates state, notification, and request delivery while the scope lease is inactive", async () => {
    const callbacks: {
      notification?: (notification: AnyNotification) => void;
      state?: (state: string) => void;
    } = {};
    const request = vi.fn(() => Promise.resolve({}));
    const client: ConversationClientLike & {
      connect: () => Promise<unknown>;
      close: () => void;
      onStateChange: (handler: (state: string) => void) => () => void;
    } = {
      request,
      onNotification: vi.fn((handler) => {
        callbacks.notification = handler;
        return () => {};
      }),
      connect: vi.fn(() => Promise.resolve({})),
      close: vi.fn(),
      onStateChange: vi.fn((handler) => {
        callbacks.state = handler;
        return () => {};
      }),
    };
    const scoped = createProfileScopedServices(PROFILE, () => client);
    const onNotification = vi.fn();
    const onState = vi.fn();
    scoped.client.onNotification(onNotification);
    scoped.client.onStateChange(onState);
    const notification = {
      method: "evener/tree/changed",
      params: { revision: 1 },
    } as AnyNotification;
    const notificationCallback = callbacks.notification;
    const stateCallback = callbacks.state;
    if (notificationCallback === undefined || stateCallback === undefined) {
      throw new Error("lease-aware callbacks were not installed");
    }

    notificationCallback(notification);
    stateCallback("ready");
    expect(onNotification).toHaveBeenCalledTimes(1);
    expect(onState).toHaveBeenCalledTimes(1);

    scoped.client.setActive(false);
    notificationCallback(notification);
    stateCallback("closed");
    await expect(
      scoped.client.request("thread/list", { limit: 501 }),
    ).rejects.toThrow("profile scope is inactive");
    expect(onNotification).toHaveBeenCalledTimes(1);
    expect(onState).toHaveBeenCalledTimes(1);
    expect(request).not.toHaveBeenCalled();

    scoped.client.setActive(true);
    notificationCallback(notification);
    stateCallback("ready");
    expect(onNotification).toHaveBeenCalledTimes(2);
    expect(onState).toHaveBeenCalledTimes(2);
  });

  it("holds an inactive in-flight request for replay and suppresses it on final close", async () => {
    const replayRequest = createDeferred<unknown>();
    const finalRequest = createDeferred<unknown>();
    const request = vi
      .fn()
      .mockImplementationOnce(() => replayRequest.promise)
      .mockImplementationOnce(() => finalRequest.promise);
    const close = vi.fn();
    const client: ConversationClientLike & {
      connect: () => Promise<unknown>;
      close: () => void;
      onStateChange: (handler: (state: string) => void) => () => void;
    } = {
      request,
      onNotification: vi.fn(() => () => {}),
      connect: vi.fn(() => Promise.resolve({})),
      close,
      onStateChange: vi.fn(() => () => {}),
    };
    const scoped = createProfileScopedServices(PROFILE, () => client);

    const replayResult = scoped.client.request("thread/list", { limit: 501 });
    scoped.client.setActive(false);
    replayRequest.resolve({ data: [] });
    await replayRequest.promise;
    scoped.client.setActive(true);
    await expect(replayResult).resolves.toEqual({ data: [] });

    const finalResult = scoped.client.request("thread/list", { limit: 501 });
    scoped.client.setActive(false);
    finalRequest.resolve({ data: [] });
    await finalRequest.promise;
    scoped.client.close();
    await expect(finalResult).rejects.toThrow("profile scope is inactive");
    expect(request).toHaveBeenCalledTimes(2);
    expect(close).toHaveBeenCalledTimes(1);
  });
});

describe("createBrowserConceptStorage", () => {
  it("treats throwing localStorage property access as unavailable", () => {
    const windowLike = {};
    Object.defineProperty(windowLike, "localStorage", {
      configurable: true,
      get: () => {
        throw new Error("controlled localStorage property failure");
      },
    });
    vi.stubGlobal("window", windowLike);
    const storage = createBrowserConceptStorage();

    expect(storage.read()).toBeNull();
    expect(() => storage.write("constellation")).not.toThrow();
    expect(() => storage.remove()).not.toThrow();
  });

  it("treats a throwing getItem as an empty preference", () => {
    vi.stubGlobal("window", {
      localStorage: {
        getItem: () => {
          throw new Error("controlled getItem failure");
        },
      },
    });

    expect(createBrowserConceptStorage().read()).toBeNull();
  });

  it("treats a throwing setItem as a best-effort no-op", () => {
    vi.stubGlobal("window", {
      localStorage: {
        setItem: () => {
          throw new Error("controlled setItem failure");
        },
      },
    });

    expect(() =>
      createBrowserConceptStorage().write("field-notes"),
    ).not.toThrow();
  });

  it("treats a throwing removeItem as a best-effort no-op", () => {
    vi.stubGlobal("window", {
      localStorage: {
        removeItem: () => {
          throw new Error("controlled removeItem failure");
        },
      },
    });

    expect(() => createBrowserConceptStorage().remove()).not.toThrow();
  });

  it("reads, writes, and removes the exact production preference key", () => {
    const getItem = vi.fn(() => "constellation");
    const setItem = vi.fn();
    const removeItem = vi.fn();
    vi.stubGlobal("window", {
      localStorage: { getItem, setItem, removeItem },
    });
    const storage = createBrowserConceptStorage();

    expect(storage.read()).toBe("constellation");
    storage.write("field-notes");
    storage.remove();

    expect(getItem).toHaveBeenCalledWith("evener.live-concept");
    expect(setItem).toHaveBeenCalledWith("evener.live-concept", "field-notes");
    expect(removeItem).toHaveBeenCalledWith("evener.live-concept");
  });
});
