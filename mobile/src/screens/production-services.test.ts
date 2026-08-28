import { afterEach, describe, expect, it, vi } from "vitest";
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
    expect(scoped.client).toBe(client);
    expect(scoped.rosterService).toBeDefined();
    expect(scoped.rosterStore).toBeDefined();
    expect(scoped.newSessionService).toBeDefined();
    expect(liveConversationService).toBeDefined();
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
