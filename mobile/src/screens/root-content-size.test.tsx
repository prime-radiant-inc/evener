/**
 * RED test 4: Root integration — content-size consumption.
 *
 * On mount, the production/fixture shell consumes `services.native.getContentSize()`
 * into the preferences store. On foreground it refreshes. On unmount it
 * unsubscribes. Getter failure is best-effort and must not break profile loading.
 * The hardcoded production `contentSize: () => "large"` lie is removed.
 */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
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

describe("7A root: content-size consumption on mount", () => {
  it("shell calls getContentSize on mount and stores result in preferences", async () => {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    // Override getContentSize to return a specific category.
    const getContentSizeSpy = vi
      .spyOn(services.native, "getContentSize")
      .mockResolvedValue("extraExtraLarge");

    const stores = {
      connection: createConnectionStore(services.profile),
      navigation: createNavigationStore(),
      preferences: createPreferencesStore(),
    };

    render(<RootShell services={services} stores={stores} />);

    // Wait for the tabs to appear (profiles loaded).
    await screen.findByRole("tab", { name: /sessions/i });

    // getContentSize was called.
    expect(getContentSizeSpy).toHaveBeenCalled();

    // The preferences store should reflect the retrieved category.
    await vi.waitFor(() => {
      expect(stores.preferences.getState().contentSize).toBe("extraExtraLarge");
    });
  });

  it("getContentSize failure does not break profile loading", async () => {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    vi.spyOn(services.native, "getContentSize").mockRejectedValue(
      new Error("unavailable"),
    );

    const stores = {
      connection: createConnectionStore(services.profile),
      navigation: createNavigationStore(),
      preferences: createPreferencesStore(),
    };

    render(<RootShell services={services} stores={stores} />);

    // Profile loading must succeed despite content-size failure.
    expect(
      await screen.findByRole("tab", { name: /sessions/i }),
    ).toBeInTheDocument();
  });
});

describe("7A root: hardcoded contentSize lie is removed", () => {
  it("production services do not expose a hardcoded contentSize function", async () => {
    const { createProductionServices } = await import("./production-services");
    const services = createProductionServices();
    // The ProductionServices type should not have a contentSize field.
    // If it does, it should delegate to native.getContentSize, not return "large".
    expect(services).not.toHaveProperty("contentSize");
  });
});
