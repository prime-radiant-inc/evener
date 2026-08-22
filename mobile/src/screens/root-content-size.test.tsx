/**
 * Root integration — content-size consumption and lifecycle refresh.
 *
 * On mount, the shell consumes `services.native.getContentSize()` into the
 * preferences store. On foreground it refreshes content size too. A
 * monotonically generated guard prevents unmount and an older/slower mount
 * read from overwriting a newer foreground result. Getter failure is
 * best-effort. The hardcoded production `contentSize` lie is removed.
 */
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ContentSizeCategory } from "../native/contract";
import { createConnectionStore } from "../state/connection";
import { createNavigationStore } from "../state/navigation";
import { createPreferencesStore } from "../state/preferences";
import { createShellServices } from "./fixture-services";
import { RootShell } from "./RootShell";

afterEach(() => {
  cleanup();
});

const PROFILES = [
  { id: "p1", name: "laptop", origin: "https://hub.example.com:8443" },
  { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
] as const;

function makeServices(opts?: {
  profiles?: readonly { id: string; name: string; origin: string }[];
  activeProfileId?: string | null;
}): ReturnType<typeof createShellServices> {
  return createShellServices({
    profiles: opts?.profiles ?? PROFILES,
    activeProfileId: opts?.activeProfileId ?? "p1",
  });
}

function makeStores(services: ReturnType<typeof createShellServices>) {
  return {
    connection: createConnectionStore(services.profile),
    navigation: createNavigationStore(),
    preferences: createPreferencesStore(),
  };
}

describe("7A root: content-size consumption on mount", () => {
  it("shell calls getContentSize on mount and stores result in preferences", async () => {
    const services = makeServices();
    const getContentSizeSpy = vi
      .spyOn(services.native, "getContentSize")
      .mockResolvedValue("extraExtraLarge");

    const stores = makeStores(services);

    render(<RootShell services={services} stores={stores} />);

    await screen.findByRole("tab", { name: /sessions/i });
    expect(getContentSizeSpy).toHaveBeenCalled();

    await vi.waitFor(() => {
      expect(stores.preferences.getState().contentSize).toBe("extraExtraLarge");
    });
  });

  it("getContentSize failure does not break profile loading", async () => {
    const services = makeServices();
    vi.spyOn(services.native, "getContentSize").mockRejectedValue(
      new Error("unavailable"),
    );

    const stores = makeStores(services);

    render(<RootShell services={services} stores={stores} />);

    expect(
      await screen.findByRole("tab", { name: /sessions/i }),
    ).toBeInTheDocument();
  });
});

describe("7A root: foreground refresh updates content size", () => {
  it("foreground changes content size value", async () => {
    const services = makeServices();
    let currentCategory: ContentSizeCategory = "large";
    vi.spyOn(services.native, "getContentSize").mockImplementation(async () => {
      return currentCategory;
    });

    // Capture the lifecycle handler so we can trigger foreground.
    let lifecycleHandler: ((state: string) => void) | null = null;
    vi.spyOn(services.native, "onLifecycle").mockImplementation((handler) => {
      lifecycleHandler = handler as (state: string) => void;
      return () => {};
    });

    const stores = makeStores(services);

    render(<RootShell services={services} stores={stores} />);
    await screen.findByRole("tab", { name: /sessions/i });

    await vi.waitFor(() => {
      expect(stores.preferences.getState().contentSize).toBe("large");
    });

    // Change the category and trigger foreground.
    currentCategory = "accessibilityLarge";
    act(() => {
      lifecycleHandler?.("foreground");
    });

    await vi.waitFor(() => {
      expect(stores.preferences.getState().contentSize).toBe(
        "accessibilityLarge",
      );
    });
  });

  it("mount-old resolves after foreground-new and is ignored", async () => {
    const services = makeServices();
    // Mount returns "large" (slow), foreground returns "extraExtraLarge" (fast).
    let resolveMount: ((category: ContentSizeCategory) => void) | undefined;
    const mountPromise = new Promise<ContentSizeCategory>((r) => {
      resolveMount = r;
    });
    const getContentSizeSpy = vi
      .spyOn(services.native, "getContentSize")
      .mockImplementation(async () => {
        return mountPromise;
      });

    let lifecycleHandler: ((state: string) => void) | null = null;
    vi.spyOn(services.native, "onLifecycle").mockImplementation((handler) => {
      lifecycleHandler = handler as (state: string) => void;
      return () => {};
    });

    const stores = makeStores(services);

    render(<RootShell services={services} stores={stores} />);
    await screen.findByRole("tab", { name: /sessions/i });

    // Foreground fires and resolves quickly with extraExtraLarge.
    getContentSizeSpy.mockResolvedValueOnce("extraExtraLarge");
    act(() => {
      lifecycleHandler?.("foreground");
    });

    await vi.waitFor(() => {
      expect(stores.preferences.getState().contentSize).toBe("extraExtraLarge");
    });

    // Now the old mount read resolves with "large" — must be ignored.
    resolveMount?.("large");
    await vi.waitFor(() => {
      // A microtask flush.
    });
    // Give it a tick.
    await Promise.resolve();
    await Promise.resolve();

    // The newer foreground result must win.
    expect(stores.preferences.getState().contentSize).toBe("extraExtraLarge");
  });

  it("unmount pending completion is ignored", async () => {
    const services = makeServices();
    let resolveMount: ((category: ContentSizeCategory) => void) | undefined;
    const mountPromise = new Promise<ContentSizeCategory>((r) => {
      resolveMount = r;
    });
    vi.spyOn(services.native, "getContentSize").mockImplementation(async () => {
      return mountPromise;
    });

    const stores = makeStores(services);

    const { unmount } = render(
      <RootShell services={services} stores={stores} />,
    );
    await screen.findByRole("tab", { name: /sessions/i });

    // Unmount before the mount read resolves.
    unmount();

    // Resolve the mount read — must not throw or update the store.
    resolveMount?.("extraExtraLarge");
    await Promise.resolve();
    await Promise.resolve();

    // Store keeps its default.
    expect(stores.preferences.getState().contentSize).toBe("large");
  });

  it("lifecycle unsubscribe is called on unmount", () => {
    const services = makeServices();
    const unsubscribeSpy = vi.fn();
    vi.spyOn(services.native, "onLifecycle").mockReturnValue(unsubscribeSpy);

    const stores = makeStores(services);
    const { unmount } = render(
      <RootShell services={services} stores={stores} />,
    );

    unmount();
    expect(unsubscribeSpy).toHaveBeenCalled();
  });
});

describe("7A root: hardcoded contentSize lie is removed", () => {
  it("production services do not expose a hardcoded contentSize function", async () => {
    const { createProductionServices } = await import("./production-services");
    const services = createProductionServices();
    expect(services).not.toHaveProperty("contentSize");
  });
});
