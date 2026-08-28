import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  AnyNotification,
  MethodName,
  MethodTypes,
  Thread,
  ThreadCapabilities,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ProfileRedacted } from "../services/nativeProfiles";
import { createConnectionStore } from "../state/connection";
import { createNavigationStore } from "../state/navigation";
import { createPreferencesStore } from "../state/preferences";
import type { FakeProfileService } from "../test/fakeProfileService";
import { createShellServices } from "./fixture-services";
import {
  createProfileScopedServices,
  type ProfileAppwireClient,
} from "./production-services";
import { RootShell } from "./RootShell";

afterEach(() => {
  cleanup();
});

const PROFILES: readonly ProfileRedacted[] = [
  { id: "p1", name: "laptop", origin: "https://hub.example.com" },
  { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
];

const ALL_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function makeThread({
  id,
  ref,
  name,
}: {
  id: string;
  ref: string;
  name: string;
}): Thread {
  return {
    id,
    sessionId: id,
    name,
    preview: name,
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1_000_000,
    updatedAt: 1_000_000,
    status: { type: "ready" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "local",
    turns: [],
    evener: {
      ref,
      capabilities: ALL_CAPABILITIES,
      queue: { revision: 0 },
    },
  };
}

class ProductionClientFake implements ProfileAppwireClient {
  readonly connect = vi.fn(async () => ({}));
  readonly close = vi.fn();
  readonly requests: Array<{ method: string; params: unknown }> = [];
  private readonly notificationHandlers = new Set<
    (notification: AnyNotification) => void
  >();

  constructor(readonly thread: Thread | null) {}

  request<M extends MethodName>(
    method: M,
    params: MethodTypes[M]["params"],
  ): Promise<MethodTypes[M]["result"]> {
    this.requests.push({ method, params });
    if (method === "thread/list") {
      return Promise.resolve({
        data: this.thread === null ? [] : [this.thread],
      }) as Promise<MethodTypes[M]["result"]>;
    }
    if (method === "thread/read" && this.thread !== null) {
      return Promise.resolve({ thread: this.thread }) as Promise<
        MethodTypes[M]["result"]
      >;
    }
    if (method === "thread/turns/list") {
      return Promise.resolve({ data: [] }) as Promise<MethodTypes[M]["result"]>;
    }
    return Promise.reject(new Error(`unexpected request: ${method}`));
  }

  onNotification(handler: (notification: AnyNotification) => void): () => void {
    this.notificationHandlers.add(handler);
    return () => this.notificationHandlers.delete(handler);
  }

  onStateChange(_handler: (state: string) => void): () => void {
    return () => {};
  }

  emit(notification: AnyNotification): void {
    for (const handler of this.notificationHandlers) handler(notification);
  }
}

function renderProductionShell(
  makeClient: (
    profile: ProfileRedacted,
    creation: number,
  ) => ProductionClientFake,
) {
  const base = createShellServices({
    profiles: PROFILES,
    activeProfileId: "p1",
  });
  const clients: ProductionClientFake[] = [];
  const createScoped = vi.fn((profile: ProfileRedacted) => {
    const client = makeClient(profile, clients.length);
    clients.push(client);
    return createProfileScopedServices(profile, () => client);
  });
  const services = {
    ...base,
    createProfileScopedServices: createScoped,
  };
  const stores = {
    connection: createConnectionStore(services.profile),
    navigation: createNavigationStore(),
    preferences: createPreferencesStore(),
  };
  const result = render(<RootShell services={services} stores={stores} />);
  return { ...result, services, stores, clients, createScoped };
}

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

async function openServerSwitcher(): Promise<void> {
  fireEvent.click(screen.getByRole("tab", { name: /settings/i }));
  fireEvent.click(
    await screen.findByRole("button", { name: /active server/i }),
  );
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
  it("New and Settings remain canonical production screens", async () => {
    renderShell({ profiles: PROFILES, activeProfileId: "p1" });
    await waitForTabs();
    fireEvent.click(screen.getByRole("tab", { name: /new/i }));
    expect(screen.getByText("New Session")).toBeInTheDocument();
    expect(screen.getByLabelText("Project path")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: /settings/i }));
    expect(screen.getByRole("tab", { name: /settings/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(
      screen.getByRole("button", { name: /active server/i }),
    ).toBeInTheDocument();
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

describe("RootShell — canonical server management", () => {
  it("opens the server switcher from canonical Settings", async () => {
    renderShell({ profiles: PROFILES, activeProfileId: "p1" });
    await waitForTabs();
    await openServerSwitcher();
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
    await openServerSwitcher();
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
    await openServerSwitcher();
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
    await openServerSwitcher();
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
    await openServerSwitcher();
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
    await vi.waitFor(() =>
      expect(
        document.querySelector('[data-roster-empty="true"]'),
      ).not.toBeNull(),
    );
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

describe("RootShell — live concept production composition", () => {
  it("connects once, builds one graph, and renders Sessions through the Host", async () => {
    const thread = makeThread({ id: "thread-a", ref: "ref-a", name: "Alpha" });
    const harness = renderProductionShell(
      () => new ProductionClientFake(thread),
    );

    await waitForTabs();
    await vi.waitFor(() => expect(harness.clients).toHaveLength(1));
    const client = harness.clients[0];
    if (client === undefined) throw new Error("missing profile client");
    await vi.waitFor(() => expect(client.connect).toHaveBeenCalledTimes(1));
    expect(harness.createScoped).toHaveBeenCalledTimes(1);
    await vi.waitFor(() =>
      expect(client.requests).toContainEqual({
        method: "thread/list",
        params: { limit: 501 },
      }),
    );
    expect(await screen.findByText("Alpha")).toBeInTheDocument();
    expect(
      document.querySelector('[data-concept-root][data-surface="sessions"]'),
    ).not.toBeNull();
    expect(document.querySelectorAll("[data-concept-root]")).toHaveLength(1);
  });

  it("routes an active conversation and shared Work state through one Host", async () => {
    const thread = makeThread({ id: "thread-a", ref: "ref-a", name: "Alpha" });
    const harness = renderProductionShell(
      () => new ProductionClientFake(thread),
    );
    await screen.findByText("Alpha");
    const historyLength = window.history.length;

    fireEvent.click(screen.getByText("Alpha"));
    await vi.waitFor(() =>
      expect(
        document.querySelector(
          '[data-concept-root][data-surface="conversation"]',
        ),
      ).not.toBeNull(),
    );
    expect(harness.stores.navigation.getState().conversationStack).toHaveLength(
      1,
    );

    fireEvent.click(await screen.findByRole("button", { name: /^work$/i }));
    await vi.waitFor(() =>
      expect(
        document.querySelector('[data-concept-root][data-surface="work"]'),
      ).not.toBeNull(),
    );
    expect(document.querySelectorAll("[data-concept-root]")).toHaveLength(1);
    expect(harness.stores.navigation.getState().conversationStack).toHaveLength(
      1,
    );
    expect(window.history.length).toBe(historyLength);
    expect(screen.queryAllByRole("tab")).toHaveLength(0);
  });

  it("mounts one concept portal outside the renderer and restores replacement-trigger focus", async () => {
    const harness = renderProductionShell(
      () =>
        new ProductionClientFake(
          makeThread({ id: "thread-a", ref: "ref-a", name: "Alpha" }),
        ),
    );
    await vi.waitFor(() => expect(harness.clients).toHaveLength(1));
    const trigger = await screen.findByRole("button", {
      name: /switch concept/i,
    });
    trigger.focus();
    fireEvent.click(trigger);

    const dialog = await screen.findByRole("dialog", {
      name: /switch concept/i,
    });
    const conceptRoot = document.querySelector("[data-concept-root]");
    expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
    expect(conceptRoot?.contains(dialog)).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: /constellation/i }));
    await vi.waitFor(() =>
      expect(document.querySelector(".concept-constellation")).not.toBeNull(),
    );
    const replacementTrigger = screen.getByRole("button", {
      name: /switch concept/i,
    });
    await vi.waitFor(() =>
      expect(document.activeElement).toBe(replacementTrigger),
    );
    expect(
      screen.queryByRole("dialog", { name: /switch concept/i }),
    ).toBeNull();
  });
});

describe("RootShell — profile-scope ownership", () => {
  it("atomically replaces all scoped sources before accepting a fresh monotonic A→B→A scope", async () => {
    const harness = renderProductionShell((profile, creation) => {
      const name = `${profile.name} scope ${creation + 1}`;
      return new ProductionClientFake(
        makeThread({
          id: `thread-${creation + 1}`,
          ref: `ref-${creation + 1}`,
          name,
        }),
      );
    });
    expect(await screen.findByText("laptop scope 1")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /laptop scope 1/i }));
    fireEvent.click(await screen.findByRole("button", { name: /^work$/i }));
    await vi.waitFor(() =>
      expect(
        document.querySelector('[data-concept-root][data-surface="work"]'),
      ).not.toBeNull(),
    );
    const firstClient = harness.clients[0];
    if (firstClient === undefined) throw new Error("missing first client");

    await act(async () => {
      await harness.stores.connection.getState().switchTo("p2");
    });
    await vi.waitFor(() => expect(harness.clients).toHaveLength(2));
    expect(firstClient.close).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[data-surface="work"]')).toBeNull();
    expect(screen.queryByText("laptop scope 1")).toBeNull();

    const secondClient = harness.clients[1];
    if (secondClient === undefined) throw new Error("missing second client");
    await act(async () => {
      await harness.stores.connection.getState().switchTo("p1");
    });
    await vi.waitFor(() => expect(harness.clients).toHaveLength(3));
    expect(secondClient.close).toHaveBeenCalledTimes(1);
    harness.stores.navigation.getState().clearConversations();
    expect(await screen.findByText("laptop scope 3")).toBeInTheDocument();
    expect(screen.queryByText("laptop scope 1")).toBeNull();
    expect(harness.createScoped).toHaveBeenCalledTimes(3);
    for (const client of harness.clients) {
      expect(client.connect).toHaveBeenCalledTimes(1);
    }
  });

  it("suppresses late notifications and reads from the disposed old scope", async () => {
    const harness = renderProductionShell(
      (_profile, creation) =>
        new ProductionClientFake(
          creation === 0
            ? makeThread({
                id: "old-thread",
                ref: "old-ref",
                name: "Old scope",
              })
            : makeThread({
                id: "new-thread",
                ref: "new-ref",
                name: "New scope",
              }),
        ),
    );
    expect(await screen.findByText("Old scope")).toBeInTheDocument();
    const oldClient = harness.clients[0];
    if (oldClient === undefined) throw new Error("missing old client");

    await act(async () => {
      await harness.stores.connection.getState().switchTo("p2");
      harness.stores.navigation.getState().clearConversations();
    });
    expect(await screen.findByText("New scope")).toBeInTheDocument();
    const oldRequestCount = oldClient.requests.length;

    act(() => {
      oldClient.emit({
        method: "evener/tree/changed",
        params: { revision: 7 },
      } as AnyNotification);
    });
    await Promise.resolve();
    expect(oldClient.requests).toHaveLength(oldRequestCount);
    expect(screen.queryByText("Old scope")).toBeNull();
    expect(screen.getByText("New scope")).toBeInTheDocument();
  });
});
