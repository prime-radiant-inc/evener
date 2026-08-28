import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { type ReactElement, StrictMode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  AnyNotification,
  MethodName,
  MethodTypes,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  Turn,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { LiveConceptRendererProps } from "../live-concepts/contract";
import { liveConceptRegistry } from "../live-concepts/registry";
import type { ProfileRedacted } from "../services/nativeProfiles";
import { createConnectionStore } from "../state/connection";
import * as conversationStateModule from "../state/conversation";
import { createNavigationStore } from "../state/navigation";
import { createPreferencesStore } from "../state/preferences";
import type { FakeProfileService } from "../test/fakeProfileService";
import { createShellServices } from "./fixture-services";
import {
  createProfileScopedServices,
  type ProfileClientTransport,
  type ProfileScopedServices,
} from "./production-services";
import { RootShell } from "./RootShell";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
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

interface Deferred<T> {
  readonly promise: Promise<T>;
  readonly resolve: (value: T) => void;
  readonly reject: (cause: unknown) => void;
}

function createDeferred<T>(): Deferred<T> {
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
  return {
    promise,
    resolve: resolvePromise,
    reject: rejectPromise,
  };
}

interface ProductionClientControls {
  readonly connect?: Deferred<unknown>;
  readonly list?: Deferred<MethodTypes["thread/list"]["result"]>;
  readonly read?: Deferred<MethodTypes["thread/read"]["result"]>;
  readonly turns?: Deferred<MethodTypes["thread/turns/list"]["result"]>;
}

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

function makeTextTurn(id: string, itemId: string, text: string): Turn {
  return {
    id,
    itemsView: "default",
    status: "completed",
    items: [
      {
        id: itemId,
        turnId: id,
        type: "userMessage",
        text,
      } as ThreadItem,
    ],
  };
}

class ProductionClientFake implements ProfileClientTransport {
  readonly connect;
  readonly close;
  readonly closed = createDeferred<void>();
  readonly requests: Array<{ method: string; params: unknown }> = [];
  readonly notificationCallbacks: Array<
    (notification: AnyNotification) => void
  > = [];
  readonly notificationUnsubscribes: Array<ReturnType<typeof vi.fn>> = [];
  readonly stateCallbacks: Array<(state: string) => void> = [];
  readonly stateUnsubscribes: Array<ReturnType<typeof vi.fn>> = [];
  private readonly notificationHandlers = new Set<
    (notification: AnyNotification) => void
  >();

  constructor(
    readonly thread: Thread | null,
    private readonly controls: ProductionClientControls = {},
  ) {
    this.connect = vi.fn(
      () => this.controls.connect?.promise ?? Promise.resolve({}),
    );
    this.close = vi.fn(() => this.closed.resolve());
  }

  request<M extends MethodName>(
    method: M,
    params: MethodTypes[M]["params"],
  ): Promise<MethodTypes[M]["result"]> {
    this.requests.push({ method, params });
    if (method === "thread/list") {
      if (this.controls.list !== undefined) {
        return this.controls.list.promise as Promise<MethodTypes[M]["result"]>;
      }
      return Promise.resolve({
        data: this.thread === null ? [] : [this.thread],
      }) as Promise<MethodTypes[M]["result"]>;
    }
    if (method === "thread/read" && this.thread !== null) {
      if (this.controls.read !== undefined) {
        return this.controls.read.promise as Promise<MethodTypes[M]["result"]>;
      }
      return Promise.resolve({ thread: this.thread }) as Promise<
        MethodTypes[M]["result"]
      >;
    }
    if (method === "thread/turns/list") {
      if (this.controls.turns !== undefined) {
        return this.controls.turns.promise as Promise<MethodTypes[M]["result"]>;
      }
      return Promise.resolve({ data: [] }) as Promise<MethodTypes[M]["result"]>;
    }
    return Promise.reject(new Error(`unexpected request: ${method}`));
  }

  onNotification(handler: (notification: AnyNotification) => void): () => void {
    this.notificationCallbacks.push(handler);
    this.notificationHandlers.add(handler);
    const unsubscribe = vi.fn(() => this.notificationHandlers.delete(handler));
    this.notificationUnsubscribes.push(unsubscribe);
    return unsubscribe;
  }

  onStateChange(handler: (state: string) => void): () => void {
    this.stateCallbacks.push(handler);
    const unsubscribe = vi.fn();
    this.stateUnsubscribes.push(unsubscribe);
    return unsubscribe;
  }

  emit(notification: AnyNotification): void {
    for (const handler of this.notificationHandlers) handler(notification);
  }

  emitCaptured(index: number, notification: AnyNotification): void {
    const handler = this.notificationCallbacks[index];
    if (handler === undefined)
      throw new Error(`missing notification callback ${index}`);
    handler(notification);
  }
}

interface RenderProductionShellOptions {
  readonly strict?: boolean;
  readonly preseedConnection?: boolean;
  readonly conceptStorage?: {
    read(): string | null;
    write(conceptId: string): void;
    remove(): void;
  };
}

function renderProductionShell(
  makeClient: (
    profile: ProfileRedacted,
    creation: number,
  ) => ProductionClientFake,
  options: RenderProductionShellOptions = {},
) {
  const base = createShellServices({
    profiles: PROFILES,
    activeProfileId: "p1",
  });
  const clients: ProductionClientFake[] = [];
  const scopedServices: ProfileScopedServices[] = [];
  const createScoped = vi.fn((profile: ProfileRedacted) => {
    const client = makeClient(profile, clients.length);
    clients.push(client);
    const scoped = createProfileScopedServices(profile, () => client);
    scopedServices.push(scoped);
    return scoped;
  });
  const services = {
    ...base,
    ...(options.conceptStorage === undefined
      ? {}
      : { conceptStorage: options.conceptStorage }),
    createProfileScopedServices: createScoped,
  };
  const stores = {
    connection: createConnectionStore(services.profile),
    navigation: createNavigationStore(),
    preferences: createPreferencesStore(),
  };
  if (options.preseedConnection === true) {
    stores.connection.setState({
      profiles: PROFILES,
      activeProfileId: "p1",
      generation: 0,
      status: "ready",
    });
  }
  const shell = (): ReactElement => (
    <RootShell services={services} stores={stores} />
  );
  const rendered = render(
    options.strict === true ? <StrictMode>{shell()}</StrictMode> : shell(),
  );
  return {
    ...rendered,
    services,
    stores,
    clients,
    scopedServices,
    createScoped,
    rerenderShell: () =>
      rendered.rerender(
        options.strict === true ? <StrictMode>{shell()}</StrictMode> : shell(),
      ),
  };
}

function renderShell(
  opts: {
    profiles?: readonly ProfileRedacted[];
    activeProfileId?: string | null;
    fail?: Partial<{ health: boolean }>;
    conceptStorage?: RenderProductionShellOptions["conceptStorage"];
  } = {},
) {
  const base = createShellServices({
    profiles: opts.profiles ?? [],
    activeProfileId: opts.activeProfileId ?? null,
  });
  const services = {
    ...base,
    ...(opts.conceptStorage === undefined
      ? {}
      : { conceptStorage: opts.conceptStorage }),
  };
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

function VoiceRouteRenderer({
  state,
  dispatch,
}: LiveConceptRendererProps): ReactElement {
  if (state.surface === "sessions") {
    const row = state.roster.groups.flatMap((group) => group.rows)[0];
    return row === undefined ? (
      <p>No controlled session</p>
    ) : (
      <button
        type="button"
        onClick={() => dispatch({ type: "openConversation", key: row.key })}
      >
        Open {row.title}
      </button>
    );
  }
  return (
    <button type="button" onClick={() => dispatch({ type: "openVoice" })}>
      Open canonical Voice
    </button>
  );
}

function LifecycleObservationRenderer({
  state,
  dispatch,
}: LiveConceptRendererProps): ReactElement {
  const rows = state.roster.groups.flatMap((group) => group.rows);
  return (
    <div
      data-testid="lifecycle-observation"
      data-surface={state.surface}
      data-work-open={state.ui.workOpen ? "true" : "false"}
      data-conversation={state.conversation === null ? "absent" : "present"}
      data-activity={state.activity === null ? "absent" : "present"}
      data-error={state.composer.error ?? ""}
    >
      {rows.map((row) => (
        <button
          key={row.key}
          type="button"
          onClick={() => dispatch({ type: "openConversation", key: row.key })}
        >
          Open {row.title}
        </button>
      ))}
      {state.conversation?.items.map((item) => (
        <p key={item.key}>{item.body}</p>
      ))}
      {state.conversation?.olderAvailable === true ? (
        <button type="button" onClick={() => dispatch({ type: "loadOlder" })}>
          Load controlled older
        </button>
      ) : null}
      {state.conversation !== null && state.surface === "conversation" ? (
        <button type="button" onClick={() => dispatch({ type: "openWork" })}>
          Open controlled Work
        </button>
      ) : null}
    </div>
  );
}

function observeLoadingBoundary(): {
  readonly seen: Promise<void>;
  readonly disconnect: () => void;
} {
  let resolveSeen: () => void = () => {
    throw new Error("loading observer was not initialized");
  };
  const seen = new Promise<void>((resolve) => {
    resolveSeen = resolve;
  });
  const observer = new MutationObserver((records) => {
    for (const record of records) {
      for (const node of Array.from(record.addedNodes)) {
        if (node.textContent?.includes("Loading…") === true) {
          resolveSeen();
          observer.disconnect();
          return;
        }
      }
    }
  });
  observer.observe(document.body, { childList: true, subtree: true });
  return { seen, disconnect: () => observer.disconnect() };
}

function captureNextConversationStore(): () => ReturnType<
  typeof conversationStateModule.createConversationStore
> {
  const createRealStore = conversationStateModule.createConversationStore;
  let captured: ReturnType<
    typeof conversationStateModule.createConversationStore
  > | null = null;
  vi.spyOn(
    conversationStateModule,
    "createConversationStore",
  ).mockImplementation(() => {
    captured = createRealStore();
    return captured;
  });
  return () => {
    if (captured === null)
      throw new Error("conversation store was not captured");
    return captured;
  };
}

async function preparePendingOlderTransition() {
  const rendererSpy = vi
    .spyOn(liveConceptRegistry.stillwater, "Renderer")
    .mockImplementation(LifecycleObservationRenderer);
  const threadA: Thread = {
    ...makeThread({ id: "thread-a", ref: "ref-a", name: "Scope A" }),
    turns: [
      makeTextTurn("turn-current-a", "item-current-a", "Current A transcript"),
    ],
  };
  const threadB = makeThread({ id: "thread-b", ref: "ref-b", name: "Scope B" });
  const connectA = createDeferred<unknown>();
  const listA = createDeferred<MethodTypes["thread/list"]["result"]>();
  const readA = createDeferred<MethodTypes["thread/read"]["result"]>();
  const turnsA = createDeferred<MethodTypes["thread/turns/list"]["result"]>();
  const connectB = createDeferred<unknown>();
  const listB = createDeferred<MethodTypes["thread/list"]["result"]>();
  const harness = renderProductionShell(
    (profile) =>
      profile.id === "p1"
        ? new ProductionClientFake(threadA, {
            connect: connectA,
            list: listA,
            read: readA,
            turns: turnsA,
          })
        : new ProductionClientFake(threadB, {
            connect: connectB,
            list: listB,
          }),
    { preseedConnection: true },
  );
  await act(async () => {
    await harness.stores.connection.getState().refresh();
    connectA.resolve({});
    await connectA.promise;
    listA.resolve({ data: [threadA] });
    await listA.promise;
  });
  fireEvent.click(screen.getByRole("button", { name: "Open Scope A" }));
  await act(async () => {
    readA.resolve({ thread: threadA, olderCursor: "older-a" });
    await readA.promise;
  });
  expect(screen.getByText("Current A transcript")).toBeInTheDocument();
  fireEvent.click(
    screen.getByRole("button", { name: "Load controlled older" }),
  );
  const clientA = harness.clients[0];
  const scopedA = harness.scopedServices[0];
  if (clientA === undefined || scopedA === undefined) {
    throw new Error("missing controlled A graph");
  }
  expect(clientA.requests).toContainEqual({
    method: "thread/turns/list",
    params: { ref: "ref-a", cursor: "older-a", limit: 50 },
  });
  fireEvent.click(screen.getByRole("button", { name: "Open controlled Work" }));
  const observation = screen.getByTestId("lifecycle-observation");
  expect(observation).toHaveAttribute("data-surface", "work");
  expect(observation).toHaveAttribute("data-work-open", "true");
  expect(observation).toHaveAttribute("data-activity", "present");

  return {
    harness,
    threadA,
    threadB,
    turnsA,
    connectB,
    listB,
    clientA,
    scopedA,
    rendererSpy,
  };
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

  it("routes the Host Voice callback to the canonical Voice screen", async () => {
    vi.spyOn(liveConceptRegistry.stillwater, "Renderer").mockImplementation(
      VoiceRouteRenderer,
    );
    const thread = makeThread({ id: "thread-a", ref: "ref-a", name: "Alpha" });
    renderProductionShell(() => new ProductionClientFake(thread));

    fireEvent.click(await screen.findByRole("button", { name: "Open Alpha" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Open canonical Voice" }),
    );

    expect(
      await screen.findByRole("button", { name: "End voice mode" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Switch to keyboard" }),
    ).toBeInTheDocument();
    expect(document.querySelector("[data-concept-root]")).toBeNull();
  });

  it("defaults safely when concept storage read throws during initialization", async () => {
    renderShell({
      profiles: PROFILES,
      activeProfileId: "p1",
      conceptStorage: {
        read: () => {
          throw new Error("controlled RootShell read failure");
        },
        write: vi.fn(),
        remove: vi.fn(),
      },
    });

    await waitForTabs();
    expect(document.querySelector(".concept-stillwater")).not.toBeNull();
  });

  it("switches concept, closes, and restores focus when persistence write throws", async () => {
    renderShell({
      profiles: PROFILES,
      activeProfileId: "p1",
      conceptStorage: {
        read: () => "stillwater",
        write: () => {
          throw new Error("controlled RootShell write failure");
        },
        remove: vi.fn(),
      },
    });
    const trigger = await screen.findByRole("button", {
      name: /switch concept/i,
    });
    trigger.focus();
    fireEvent.click(trigger);
    fireEvent.click(
      await screen.findByRole("button", { name: /constellation/i }),
    );

    expect(document.querySelector(".concept-constellation")).not.toBeNull();
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

  it("retains one graph through StrictMode replay and a stable rerender, then disposes once", async () => {
    const connect = createDeferred<unknown>();
    const harness = renderProductionShell(
      () =>
        new ProductionClientFake(
          makeThread({ id: "thread-a", ref: "ref-a", name: "Alpha" }),
          { connect },
        ),
      { strict: true, preseedConnection: true },
    );

    expect(harness.createScoped).toHaveBeenCalledTimes(1);
    expect(harness.clients).toHaveLength(1);
    const client = harness.clients[0];
    if (client === undefined) throw new Error("missing StrictMode client");
    expect(client.connect).toHaveBeenCalledTimes(1);
    expect(client.close).not.toHaveBeenCalled();
    const stateCallback = client.stateCallbacks[0];
    if (stateCallback === undefined)
      throw new Error("missing active state callback");
    act(() => stateCallback("ready"));
    expect(harness.stores.connection.getState().reachability.p1).toBe(
      "reachable",
    );

    harness.rerenderShell();
    expect(harness.createScoped).toHaveBeenCalledTimes(1);
    expect(client.connect).toHaveBeenCalledTimes(1);
    expect(client.close).not.toHaveBeenCalled();

    await act(async () => {
      connect.resolve({});
      await connect.promise;
    });
    expect(await screen.findByText("Alpha")).toBeInTheDocument();

    harness.unmount();
    await client.closed.promise;
    expect(client.close).toHaveBeenCalledTimes(1);
    expect(client.notificationUnsubscribes[0]).toHaveBeenCalledTimes(1);
    expect(client.stateUnsubscribes[0]).toHaveBeenCalledTimes(1);
  });

  it("deactivates callbacks synchronously when unmounted before a pending connect reaction", async () => {
    const connect = createDeferred<unknown>();
    const harness = renderProductionShell(
      () =>
        new ProductionClientFake(
          makeThread({ id: "thread-a", ref: "ref-a", name: "Late Alpha" }),
          { connect },
        ),
      { preseedConnection: true },
    );
    await act(async () => {
      await harness.stores.connection.getState().refresh();
    });
    const client = harness.clients[0];
    if (client === undefined) throw new Error("missing pending client");
    expect(harness.stores.connection.getState().reachability.p1).toBe(
      "reconnecting",
    );

    connect.resolve({});
    harness.unmount();
    const stateCallback = client.stateCallbacks[0];
    if (stateCallback === undefined)
      throw new Error("missing captured state callback");
    stateCallback("ready");
    expect(harness.stores.connection.getState().reachability.p1).toBe(
      "reconnecting",
    );
    expect(client.requests).toHaveLength(0);

    await client.closed.promise;
    expect(client.requests).toHaveLength(0);
    expect(client.notificationUnsubscribes[0]).toHaveBeenCalledTimes(1);
    expect(client.stateUnsubscribes[0]).toHaveBeenCalledTimes(1);
    expect(client.close).toHaveBeenCalledTimes(1);
  });

  it.each(["resolve", "reject"] as const)(
    "invalidates an in-flight roster request before final-unmount %s settlement",
    async (outcome) => {
      const connect = createDeferred<unknown>();
      const list = createDeferred<MethodTypes["thread/list"]["result"]>();
      const thread = makeThread({
        id: "thread-final-list",
        ref: "ref-final-list",
        name: "Late roster result",
      });
      const harness = renderProductionShell(
        () => new ProductionClientFake(thread, { connect, list }),
        { preseedConnection: true },
      );
      await act(async () => {
        await harness.stores.connection.getState().refresh();
      });
      const client = harness.clients[0];
      const scoped = harness.scopedServices[0];
      if (client === undefined || scoped === undefined) {
        throw new Error("missing final roster graph");
      }
      const refresh = vi.spyOn(scoped.rosterStore.getState(), "refresh");
      await act(async () => {
        connect.resolve({});
        await connect.promise;
      });
      const refreshResult = refresh.mock.results[0]?.value;
      if (!(refreshResult instanceof Promise)) {
        throw new Error("missing in-flight roster refresh promise");
      }
      expect(scoped.rosterStore.getState().loading).toBe(true);

      const rawSettlement =
        outcome === "resolve"
          ? list.promise
          : list.promise.catch((cause) => cause);
      harness.unmount();
      await client.closed.promise;
      expect(client.close).toHaveBeenCalledTimes(1);
      if (outcome === "resolve") {
        list.resolve({ data: [thread] });
      } else {
        list.reject(new Error("controlled final roster rejection"));
      }
      const settledRaw = await rawSettlement;
      if (outcome === "reject") {
        expect(settledRaw).toMatchObject({
          message: "controlled final roster rejection",
        });
      }
      await refreshResult;

      expect(scoped.rosterStore.getState()).toMatchObject({
        entries: [],
        loading: false,
        error: null,
      });
      expect(client.requests).toEqual([
        { method: "thread/list", params: { limit: 501 } },
      ]);
      expect(client.notificationUnsubscribes[0]).toHaveBeenCalledTimes(1);
      expect(client.stateUnsubscribes[0]).toHaveBeenCalledTimes(1);
      expect(client.close).toHaveBeenCalledTimes(1);
      expect(harness.container.childElementCount).toBe(0);
    },
  );

  it.each(["resolve", "reject"] as const)(
    "invalidates an in-flight conversation read before final-unmount %s settlement",
    async (outcome) => {
      const getConversationStore = captureNextConversationStore();
      const connect = createDeferred<unknown>();
      const list = createDeferred<MethodTypes["thread/list"]["result"]>();
      const read = createDeferred<MethodTypes["thread/read"]["result"]>();
      const thread = makeThread({
        id: "thread-final-read",
        ref: "ref-final-read",
        name: "Late conversation result",
      });
      const harness = renderProductionShell(
        () => new ProductionClientFake(thread, { connect, list, read }),
        { preseedConnection: true },
      );
      await act(async () => {
        await harness.stores.connection.getState().refresh();
        connect.resolve({});
        await connect.promise;
        list.resolve({ data: [thread] });
        await list.promise;
      });
      const client = harness.clients[0];
      if (client === undefined) throw new Error("missing final read client");
      const conversationStore = getConversationStore();
      const openProjected = vi.spyOn(
        conversationStore.getState(),
        "openProjected",
      );
      fireEvent.click(
        screen.getByRole("button", { name: /Late conversation result/i }),
      );
      const openResult = openProjected.mock.results[0]?.value;
      if (!(openResult instanceof Promise)) {
        throw new Error("missing in-flight conversation open promise");
      }
      expect(conversationStore.getState()).toMatchObject({
        status: "opening",
        ref: "ref-final-read",
        conversation: null,
        error: null,
      });

      const rawSettlement =
        outcome === "resolve"
          ? read.promise
          : read.promise.catch((cause) => cause);
      harness.unmount();
      await client.closed.promise;
      expect(client.close).toHaveBeenCalledTimes(1);
      if (outcome === "resolve") {
        read.resolve({ thread });
      } else {
        read.reject(new Error("controlled final read rejection"));
      }
      const settledRaw = await rawSettlement;
      if (outcome === "reject") {
        expect(settledRaw).toMatchObject({
          message: "controlled final read rejection",
        });
      }
      await openResult;

      expect(conversationStore.getState()).toMatchObject({
        status: "idle",
        ref: null,
        conversation: null,
        error: null,
      });
      expect(client.requests).toEqual([
        { method: "thread/list", params: { limit: 501 } },
        {
          method: "thread/read",
          params: {
            ref: "ref-final-read",
            includeTurns: true,
            replaceSubscription: true,
            subscribe: true,
            turnLimit: 50,
          },
        },
      ]);
      expect(client.notificationUnsubscribes[0]).toHaveBeenCalledTimes(1);
      expect(client.stateUnsubscribes[0]).toHaveBeenCalledTimes(1);
      expect(client.close).toHaveBeenCalledTimes(1);
      expect(harness.container.childElementCount).toBe(0);
    },
  );

  it("shows the fail-closed Loading boundary and ignores a late older-page success after every source resets", async () => {
    const scenario = await preparePendingOlderTransition();
    const resetRoster = vi.spyOn(
      scenario.scopedA.rosterStore.getState(),
      "reset",
    );
    const renderCountBeforeTransition = scenario.rendererSpy.mock.calls.length;
    const loading = observeLoadingBoundary();

    act(() => {
      scenario.harness.stores.connection.setState({
        profiles: PROFILES,
        activeProfileId: "p2",
        generation: 1,
        status: "ready",
      });
    });
    await loading.seen;
    loading.disconnect();

    expect(resetRoster).toHaveBeenCalledTimes(1);
    const observation = screen.getByTestId("lifecycle-observation");
    expect(observation).toHaveAttribute("data-surface", "sessions");
    expect(observation).toHaveAttribute("data-work-open", "false");
    expect(observation).toHaveAttribute("data-conversation", "absent");
    expect(observation).toHaveAttribute("data-activity", "absent");
    expect(observation).toHaveAttribute("data-error", "");
    expect(
      scenario.harness.stores.navigation.getState().conversationStack,
    ).toHaveLength(0);
    expect(screen.queryByText("Current A transcript")).toBeNull();
    expect(screen.queryByText("Scope B")).toBeNull();
    const resetOrder = resetRoster.mock.invocationCallOrder[0];
    const acceptedRenderOrder =
      scenario.rendererSpy.mock.invocationCallOrder[
        renderCountBeforeTransition
      ];
    const acceptedCall =
      scenario.rendererSpy.mock.calls[renderCountBeforeTransition];
    if (resetOrder === undefined || acceptedRenderOrder === undefined) {
      throw new Error("missing reset/render ordering evidence");
    }
    if (acceptedCall === undefined) {
      throw new Error("missing first accepted source render");
    }
    expect(resetOrder).toBeLessThan(acceptedRenderOrder);
    const acceptedState = acceptedCall[0].state;
    expect(acceptedState.surface).toBe("sessions");
    expect(acceptedState.roster.groups).toEqual([]);
    expect(acceptedState.conversation).toBeNull();
    expect(acceptedState.activity).toBeNull();
    expect(acceptedState.ui.workOpen).toBe(false);

    const oldRequestCount = scenario.clientA.requests.length;
    await act(async () => {
      scenario.turnsA.resolve({
        data: [
          makeTextTurn(
            "turn-older-a",
            "item-older-a",
            "Late older A transcript",
          ),
        ],
      });
      await scenario.turnsA.promise;
    });
    expect(scenario.clientA.requests).toHaveLength(oldRequestCount);
    expect(screen.queryByText("Late older A transcript")).toBeNull();
    expect(screen.getByTestId("lifecycle-observation")).toHaveAttribute(
      "data-error",
      "",
    );

    const clientB = scenario.harness.clients[1];
    if (clientB === undefined) throw new Error("missing controlled B graph");
    await act(async () => {
      scenario.connectB.resolve({});
      await scenario.connectB.promise;
      scenario.listB.resolve({ data: [scenario.threadB] });
      await scenario.listB.promise;
    });
    expect(
      screen.getByRole("button", { name: "Open Scope B" }),
    ).toBeInTheDocument();
    expect(clientB.requests).toEqual([
      { method: "thread/list", params: { limit: 501 } },
    ]);
  });

  it("ignores a late older-page rejection after the profile transition", async () => {
    const scenario = await preparePendingOlderTransition();
    act(() => {
      scenario.harness.stores.connection.setState({
        profiles: PROFILES,
        activeProfileId: "p2",
        generation: 1,
        status: "ready",
      });
    });
    const oldRequestCount = scenario.clientA.requests.length;

    await act(async () => {
      scenario.turnsA.reject(
        new Error("controlled stale older-page rejection"),
      );
      try {
        await scenario.turnsA.promise;
      } catch {}
    });

    expect(scenario.clientA.requests).toHaveLength(oldRequestCount);
    const observation = screen.getByTestId("lifecycle-observation");
    expect(observation).toHaveAttribute("data-surface", "sessions");
    expect(observation).toHaveAttribute("data-conversation", "absent");
    expect(observation).toHaveAttribute("data-activity", "absent");
    expect(observation).toHaveAttribute("data-work-open", "false");
    expect(observation).toHaveAttribute("data-error", "");
    expect(screen.queryByText("Current A transcript")).toBeNull();
  });

  it("replaces a pending connect exactly once and ignores its late completion and callback", async () => {
    const connectA = createDeferred<unknown>();
    const connectB = createDeferred<unknown>();
    const listB = createDeferred<MethodTypes["thread/list"]["result"]>();
    const threadA = makeThread({
      id: "thread-a",
      ref: "ref-a",
      name: "Scope A",
    });
    const threadB = makeThread({
      id: "thread-b",
      ref: "ref-b",
      name: "Scope B",
    });
    const harness = renderProductionShell(
      (profile) =>
        profile.id === "p1"
          ? new ProductionClientFake(threadA, { connect: connectA })
          : new ProductionClientFake(threadB, {
              connect: connectB,
              list: listB,
            }),
      { preseedConnection: true },
    );
    const clientA = harness.clients[0];
    if (clientA === undefined) throw new Error("missing profile A client");
    await act(async () => {
      await harness.stores.connection.getState().refresh();
    });

    act(() => {
      harness.stores.connection.setState({
        profiles: PROFILES,
        activeProfileId: "p2",
        generation: 1,
        status: "ready",
      });
    });
    expect(harness.clients).toHaveLength(2);
    const clientB = harness.clients[1];
    if (clientB === undefined) throw new Error("missing profile B client");
    expect(clientA.close).toHaveBeenCalledTimes(1);
    expect(clientA.notificationUnsubscribes[0]).toHaveBeenCalledTimes(1);
    expect(clientA.stateUnsubscribes[0]).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("Scope A")).toBeNull();
    expect(screen.queryByText("Scope B")).toBeNull();

    await act(async () => {
      connectA.resolve({});
      await connectA.promise;
    });
    expect(clientA.requests).toHaveLength(0);
    clientA.emitCaptured(0, {
      method: "evener/tree/changed",
      params: { revision: 11 },
    } as AnyNotification);
    expect(clientA.requests).toHaveLength(0);

    await act(async () => {
      connectB.resolve({});
      await connectB.promise;
    });
    expect(clientB.requests).toEqual([
      { method: "thread/list", params: { limit: 501 } },
    ]);
    expect(screen.queryByText("Scope B")).toBeNull();
    await act(async () => {
      listB.resolve({ data: [threadB] });
      await listB.promise;
    });
    expect(screen.getByText("Scope B")).toBeInTheDocument();
    expect(harness.createScoped).toHaveBeenCalledTimes(2);
  });

  it("rejects a late read across controlled A→B→A scopes and never revives scope 1", async () => {
    const threads = [
      makeThread({ id: "thread-a1", ref: "ref-a1", name: "Scope A1" }),
      makeThread({ id: "thread-b", ref: "ref-b", name: "Scope B" }),
      makeThread({ id: "thread-a3", ref: "ref-a3", name: "Scope A3" }),
    ];
    const controls = threads.map(() => ({
      connect: createDeferred<unknown>(),
      list: createDeferred<MethodTypes["thread/list"]["result"]>(),
      read: createDeferred<MethodTypes["thread/read"]["result"]>(),
    }));
    const harness = renderProductionShell(
      (_profile, creation) => {
        const thread = threads[creation];
        const control = controls[creation];
        if (thread === undefined || control === undefined) {
          throw new Error(`unexpected scope creation ${creation}`);
        }
        return new ProductionClientFake(thread, control);
      },
      { preseedConnection: true },
    );
    const clientA1 = harness.clients[0];
    const controlA1 = controls[0];
    const threadA1 = threads[0];
    if (
      clientA1 === undefined ||
      controlA1 === undefined ||
      threadA1 === undefined
    ) {
      throw new Error("missing scope A1 controls");
    }
    await act(async () => {
      await harness.stores.connection.getState().refresh();
    });
    await act(async () => {
      controlA1.connect.resolve({});
      await controlA1.connect.promise;
      controlA1.list.resolve({ data: [threadA1] });
      await controlA1.list.promise;
    });
    fireEvent.click(screen.getByRole("button", { name: /Scope A1/i }));
    expect(clientA1.requests).toContainEqual({
      method: "thread/read",
      params: {
        ref: "ref-a1",
        includeTurns: true,
        replaceSubscription: true,
        subscribe: true,
        turnLimit: 50,
      },
    });

    act(() => {
      harness.stores.connection.setState({
        profiles: PROFILES,
        activeProfileId: "p2",
        generation: 1,
        status: "ready",
      });
    });
    expect(harness.stores.navigation.getState().conversationStack).toHaveLength(
      0,
    );
    expect(clientA1.close).toHaveBeenCalledTimes(1);
    await act(async () => {
      controlA1.read.resolve({ thread: threadA1 });
      await controlA1.read.promise;
    });
    clientA1.emitCaptured(0, {
      method: "evener/tree/changed",
      params: { revision: 12 },
    } as AnyNotification);
    expect(screen.queryByText("Scope A1")).toBeNull();

    const clientB = harness.clients[1];
    const controlB = controls[1];
    const threadB = threads[1];
    if (
      clientB === undefined ||
      controlB === undefined ||
      threadB === undefined
    ) {
      throw new Error("missing scope B controls");
    }
    await act(async () => {
      controlB.connect.resolve({});
      await controlB.connect.promise;
      controlB.list.resolve({ data: [threadB] });
      await controlB.list.promise;
    });
    expect(screen.getByText("Scope B")).toBeInTheDocument();

    act(() => {
      harness.stores.connection.setState({
        profiles: PROFILES,
        activeProfileId: "p1",
        generation: 2,
        status: "ready",
      });
    });
    expect(clientB.close).toHaveBeenCalledTimes(1);
    const clientA3 = harness.clients[2];
    const controlA3 = controls[2];
    const threadA3 = threads[2];
    if (
      clientA3 === undefined ||
      controlA3 === undefined ||
      threadA3 === undefined
    ) {
      throw new Error("missing scope A3 controls");
    }
    await act(async () => {
      controlA3.connect.resolve({});
      await controlA3.connect.promise;
      controlA3.list.resolve({ data: [threadA3] });
      await controlA3.list.promise;
    });
    expect(screen.getByText("Scope A3")).toBeInTheDocument();
    expect(screen.queryByText("Scope A1")).toBeNull();
    expect(harness.createScoped).toHaveBeenCalledTimes(3);
  });

  it("disposes a pending scope when the active profile is removed and ignores late work", async () => {
    const connect = createDeferred<unknown>();
    const list = createDeferred<MethodTypes["thread/list"]["result"]>();
    const thread = makeThread({
      id: "thread-a",
      ref: "ref-a",
      name: "Removed A",
    });
    const harness = renderProductionShell(
      () => new ProductionClientFake(thread, { connect, list }),
      { preseedConnection: true },
    );
    const client = harness.clients[0];
    if (client === undefined) throw new Error("missing removed profile client");
    await act(async () => {
      await harness.stores.connection.getState().refresh();
    });
    await act(async () => {
      connect.resolve({});
      await connect.promise;
    });
    expect(client.requests).toHaveLength(1);

    act(() => {
      harness.stores.connection.setState({
        profiles: [],
        activeProfileId: null,
        generation: 1,
        status: "ready",
      });
    });
    expect(client.close).toHaveBeenCalledTimes(1);
    expect(client.notificationUnsubscribes[0]).toHaveBeenCalledTimes(1);
    expect(client.stateUnsubscribes[0]).toHaveBeenCalledTimes(1);
    expect(screen.getByText(/connect to a hub/i)).toBeInTheDocument();

    await act(async () => {
      list.resolve({ data: [thread] });
      await list.promise;
    });
    client.emitCaptured(0, {
      method: "evener/tree/changed",
      params: { revision: 13 },
    } as AnyNotification);
    expect(screen.queryByText("Removed A")).toBeNull();
    expect(client.requests).toHaveLength(1);
  });

  it("contains rejected connect and read operations within their exact owner scopes", async () => {
    const rejectedConnect = createDeferred<unknown>();
    const acceptedConnect = createDeferred<unknown>();
    const acceptedList = createDeferred<MethodTypes["thread/list"]["result"]>();
    const rejectedRead = createDeferred<MethodTypes["thread/read"]["result"]>();
    const thread = makeThread({
      id: "thread-a",
      ref: "ref-a",
      name: "Retry A",
    });
    const harness = renderProductionShell(
      (_profile, creation) =>
        creation === 0
          ? new ProductionClientFake(thread, { connect: rejectedConnect })
          : new ProductionClientFake(thread, {
              connect: acceptedConnect,
              list: acceptedList,
              read: rejectedRead,
            }),
      { preseedConnection: true },
    );
    const failedClient = harness.clients[0];
    if (failedClient === undefined) throw new Error("missing failed client");
    await act(async () => {
      await harness.stores.connection.getState().refresh();
    });
    await act(async () => {
      rejectedConnect.reject(new Error("controlled connect rejection"));
      try {
        await rejectedConnect.promise;
      } catch {}
    });
    expect(failedClient.requests).toHaveLength(0);

    act(() => {
      harness.stores.connection.setState({ generation: 1 });
    });
    const retryClient = harness.clients[1];
    if (retryClient === undefined) throw new Error("missing retry client");
    expect(failedClient.close).toHaveBeenCalledTimes(1);
    await act(async () => {
      acceptedConnect.resolve({});
      await acceptedConnect.promise;
      acceptedList.resolve({ data: [thread] });
      await acceptedList.promise;
    });
    fireEvent.click(screen.getByRole("button", { name: /Retry A/i }));
    await act(async () => {
      rejectedRead.reject(new Error("controlled read rejection"));
      try {
        await rejectedRead.promise;
      } catch {}
    });
    expect(
      document.querySelector(
        '[data-concept-root][data-surface="conversation"]',
      ),
    ).not.toBeNull();
    expect(screen.getByText("No conversation")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^work$/i })).toBeNull();
  });
});
