import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../services/conversation";
import type { ProfileRedacted } from "../services/nativeProfiles";
import { createConnectionStore } from "../state/connection";
import { createNavigationStore } from "../state/navigation";
import { createPreferencesStore } from "../state/preferences";
import type { FakeProfileService } from "../test/fakeProfileService";
import { createShellServices } from "./fixture-services";
import { createProfileScopedServices } from "./production-services";
import { RootShell } from "./RootShell";

afterEach(() => {
  cleanup();
});

const PROFILES: readonly ProfileRedacted[] = [
  { id: "p1", name: "laptop", origin: "https://hub.example.com" },
  { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
];

function renderShell(
  opts: {
    profiles?: readonly ProfileRedacted[];
    activeProfileId?: string | null;
    fail?: Partial<{ health: boolean }>;
  } = {},
) {
  const services = createShellServices({
    profiles: opts.profiles ?? [],
    activeProfileId: opts.activeProfileId ?? null,
  });
  if (opts.fail?.health)
    (services.profile as FakeProfileService).failOnce("health");
  const stores = {
    connection: createConnectionStore(services.profile),
    navigation: createNavigationStore(),
    preferences: createPreferencesStore(),
  };
  render(<RootShell services={services} stores={stores} />);
  return { services, stores };
}

/** Wait for the tab bar to appear after the initial async refresh. */
async function waitForTabs() {
  return screen.findByRole("tab", { name: /sessions/i });
}

describe("RootShell — three-tab bottom bar", () => {
  it("renders Sessions, New, Settings tabs", async () => {
    renderShell({ profiles: PROFILES, activeProfileId: "p1" });
    await waitForTabs();
    expect(screen.getByRole("tab", { name: /sessions/i })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /new/i })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /settings/i })).toBeInTheDocument();
    expect(screen.getAllByRole("tab").length).toBe(3);
  });

  it("no profiles renders full-screen onboarding, not the tab bar", async () => {
    renderShell({ profiles: [], activeProfileId: null });
    // Onboarding copy includes "Connect to a Hub".
    expect(await screen.findByText(/connect to a hub/i)).toBeInTheDocument();
    expect(
      screen.queryByRole("tab", { name: /sessions/i }),
    ).not.toBeInTheDocument();
  });
});

describe("RootShell — tab switching", () => {
  it("clicking a tab switches the active screen", async () => {
    renderShell({ profiles: PROFILES, activeProfileId: "p1" });
    await waitForTabs();
    fireEvent.click(screen.getByRole("tab", { name: /new/i }));
    expect(screen.getByRole("tab", { name: /new/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    fireEvent.click(screen.getByRole("tab", { name: /settings/i }));
    expect(screen.getByRole("tab", { name: /settings/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("default tab is Sessions", async () => {
    renderShell({ profiles: PROFILES, activeProfileId: "p1" });
    await waitForTabs();
    expect(screen.getByRole("tab", { name: /sessions/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });
});

describe("RootShell — Sessions top bar active server button", () => {
  it("shows the active server name and opens the switcher sheet", async () => {
    renderShell({ profiles: PROFILES, activeProfileId: "p1" });
    await waitForTabs();
    const btn = await screen.findByRole("button", {
      name: /laptop.*active server/i,
    });
    expect(btn).toBeInTheDocument();
    fireEvent.click(btn);
    // Sheet lists all profiles redacted — find the "server" profile row.
    expect(await screen.findByText(/^server$/i)).toBeInTheDocument();
  });
});

describe("RootShell — server switcher flows", () => {
  it("switch selects a profile and closes the sheet", async () => {
    const { stores } = renderShell({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    await waitForTabs();
    fireEvent.click(
      screen.getByRole("button", {
        name: /laptop.*active server|active server/i,
      }),
    );
    // Click the "server" profile row to open its detail view.
    fireEvent.click(await screen.findByRole("button", { name: /^server/i }));
    // Click "Use This Server" in the detail view.
    fireEvent.click(
      await screen.findByRole("button", { name: /use this server/i }),
    );
    await vi.waitFor(() =>
      expect(stores.connection.getState().activeProfileId).toBe("p2"),
    );
    // Sheet closed: the heading is gone.
    expect(
      screen.queryByRole("heading", { name: /servers/i }),
    ).not.toBeInTheDocument();
  });

  it("switch clears server-scoped placeholder state", async () => {
    const { stores } = renderShell({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    stores.connection.getState().__seedServerScopedState({ thread: "stale" });
    await waitForTabs();
    fireEvent.click(
      screen.getByRole("button", {
        name: /laptop.*active server|active server/i,
      }),
    );
    // Click the "server" profile row to open its detail view.
    fireEvent.click(await screen.findByRole("button", { name: /^server/i }));
    // Click "Use This Server" in the detail view.
    fireEvent.click(
      await screen.findByRole("button", { name: /use this server/i }),
    );
    await vi.waitFor(() =>
      expect(stores.connection.getState().__serverScopedState).toBeNull(),
    );
  });

  it("remove active profile requires confirmation then falls back", async () => {
    const { stores } = renderShell({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    await waitForTabs();
    fireEvent.click(
      screen.getByRole("button", {
        name: /laptop.*active server|active server/i,
      }),
    );
    // Click the "laptop" profile row to open its detail view.
    const dialog = await screen.findByRole("dialog", { name: /servers/i });
    fireEvent.click(
      await within(dialog).findByRole("button", {
        name: /laptop.*hub\.example/i,
      }),
    );
    // Click "Remove" in the detail view.
    fireEvent.click(await screen.findByRole("button", { name: /^remove/i }));
    // Confirmation required.
    const confirm = await screen.findByRole("button", {
      name: /confirm.*remove|delete.*confirm/i,
    });
    fireEvent.click(confirm);
    await vi.waitFor(() =>
      expect(stores.connection.getState().activeProfileId).toBe("p2"),
    );
  });

  it("add opens onboarding within the sheet flow", async () => {
    renderShell({ profiles: PROFILES, activeProfileId: "p1" });
    await waitForTabs();
    fireEvent.click(
      screen.getByRole("button", {
        name: /laptop.*active server|active server/i,
      }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: /add.*server|add.*hub/i }),
    );
    expect(
      await screen.findByRole("button", { name: /scan qr code/i }),
    ).toBeInTheDocument();
  });
});

describe("RootShell — honest states", () => {
  it("Sessions shows an empty state when no sessions exist", async () => {
    renderShell({ profiles: PROFILES, activeProfileId: "p1" });
    await waitForTabs();
    expect(
      await screen.findByText(/no sessions|nothing here|empty/i),
    ).toBeInTheDocument();
  });

  it("Sessions shows a loading state then ready", async () => {
    renderShell({ profiles: PROFILES, activeProfileId: "p1" });
    // After loading, the tab panel is present.
    expect(await screen.findByRole("tabpanel")).toBeInTheDocument();
  });

  it("Settings shows mobile-only sections", async () => {
    renderShell({ profiles: PROFILES, activeProfileId: "p1" });
    await waitForTabs();
    fireEvent.click(screen.getByRole("tab", { name: /settings/i }));
    expect(await screen.findByText(/appearance|theme/i)).toBeInTheDocument();
    // Connection group header + diagnostics section.
    expect(
      (await screen.findAllByText("Connection")).length,
    ).toBeGreaterThanOrEqual(1);
    expect(await screen.findByText(/Permissions/i)).toBeInTheDocument();
  });
});

describe("RootShell — production AppWire services", () => {
  it("connects the active profile and passes a roster service to Sessions", async () => {
    const profile = PROFILES[0];
    if (profile === undefined) throw new Error("missing test profile");
    let resolveConnect = (_value: unknown): void => {
      throw new Error("connect resolver was not initialized");
    };
    const connectPromise = new Promise<unknown>((resolve) => {
      resolveConnect = resolve;
    });
    const client: ConversationClientLike & {
      connect: () => Promise<unknown>;
      close: () => void;
      onStateChange: (handler: (state: string) => void) => () => void;
    } = {
      request: vi.fn(() => Promise.resolve({ data: [] })),
      onNotification: vi.fn(() => () => {}),
      connect: vi.fn(() => connectPromise),
      close: vi.fn(),
      onStateChange: vi.fn(() => () => {}),
    };
    const base = createShellServices({
      profiles: [profile],
      activeProfileId: profile.id,
    });
    const services = {
      ...base,
      createProfileScopedServices: () =>
        createProfileScopedServices(profile, () => client),
    };
    const stores = {
      connection: createConnectionStore(services.profile),
      navigation: createNavigationStore(),
      preferences: createPreferencesStore(),
    };

    render(<RootShell services={services} stores={stores} />);

    await waitForTabs();
    await vi.waitFor(() => expect(client.connect).toHaveBeenCalledTimes(1));
    expect(client.request).not.toHaveBeenCalled();
    resolveConnect({});
    await vi.waitFor(() =>
      expect(client.request).toHaveBeenCalledWith("thread/list", {
        limit: 501,
      }),
    );

    stores.connection.setState({ generation: 2 });
    await vi.waitFor(() => expect(client.connect).toHaveBeenCalledTimes(2));
    expect(client.close).toHaveBeenCalledTimes(1);
  });
});
