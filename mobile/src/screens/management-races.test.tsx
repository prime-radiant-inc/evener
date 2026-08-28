/**
 * Task 7B fix round 1 — management races at the screen level.
 *
 * Covers:
 *  - CRITICAL 2: click order/unmount reversed by refresh — Onboarding
 *    establishes generation synchronously at user intent before refresh.
 *  - IMPORTANT 2: ServerSwitcherSheet useEffect cleanup on unmount/Done/overlay.
 *  - IMPORTANT 3: confirmed preview ID not cancelled by later cleanup.
 *  - IMPORTANT 4: explicit fixture reachability seeds + unknown glyph styling.
 *  - IMPORTANT 6: strengthened remove test (first failure + retry success).
 *  - IMPORTANT 7: real rendered switch clears conversations only after success.
 *
 * No source-string assertions. No setTimeout(0). No conditional branches in
 * cancel assertions. Tests await exact deferred promises/callbacks.
 */
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { createFixture, type FixtureRoute } from "../fixture";
import type { ProfileRedacted } from "../services/nativeProfiles";
import {
  type ConnectionPreview,
  createConnectionStore,
  type Reachability,
} from "../state/connection";
import { createNavigationStore } from "../state/navigation";
import type { FakeProfileService } from "../test/fakeProfileService";
import {
  SAMPLE_AUTH_URL_HTTPS,
  SECRET_TOKEN,
} from "../test/fakeProfileService";
import { StatusMark } from "../ui/StatusMark";
import {
  createOnboardingServices,
  createShellServices,
} from "./fixture-services";
import { OnboardingScreen } from "./OnboardingScreen";
import { ServerSwitcherSheet } from "./ServerSwitcherSheet";
import { SessionsScreen } from "./SessionsScreen";

afterEach(() => {
  cleanup();
});

const PROFILES: readonly ProfileRedacted[] = [
  { id: "p1", name: "laptop", origin: "https://hub.example.com:8443" },
  { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
];

const QUERY_TEXT = new URL(SAMPLE_AUTH_URL_HTTPS).search;

function deferred<T>(): {
  readonly promise: Promise<T>;
  readonly resolve: (value: T) => void;
} {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function expectRedacted(...values: readonly string[]): void {
  const combined = values.join("\n");
  expect(combined).not.toContain(SECRET_TOKEN);
  expect(combined).not.toContain("token=");
  expect(combined).not.toContain(SAMPLE_AUTH_URL_HTTPS);
  expect(combined).not.toContain(QUERY_TEXT);
}

function exactPreviewId(preview: ConnectionPreview | null): string {
  expect(preview).not.toBeNull();
  return (preview as ConnectionPreview).previewId;
}

function fakeOf(
  services: ReturnType<typeof createShellServices>,
): FakeProfileService {
  return services.profile as FakeProfileService;
}

/** Capture and return the exact next previewScan operation promise. */
function trackPreviewScan(
  connection: ReturnType<typeof createConnectionStore>,
): () => Promise<void> {
  const originalPreviewScan = connection.getState().previewScan;
  let operation: Promise<void> | null = null;
  connection.setState({
    previewScan: (scan) => {
      const pending = originalPreviewScan(scan);
      operation = pending;
      return pending;
    },
  });
  return () => {
    expect(operation).not.toBeNull();
    return operation as Promise<void>;
  };
}

// ---------------------------------------------------------------------------
// CRITICAL 2: click order/unmount reversed by refresh
// ---------------------------------------------------------------------------

describe("Onboarding — generation established before refresh", () => {
  it("scan then paste while refresh is gated never starts the native scan", async () => {
    const services = createOnboardingServices();
    const fake = services.profile as FakeProfileService;
    const connection = createConnectionStore(fake);
    const scanOperation = trackPreviewScan(connection);
    fake.gateHealth();

    const nativeScan = vi.fn().mockResolvedValue({
      previewId: "must-not-start",
      origin: "http://192.168.1.10:8080",
    });
    services.native.scanAndPreviewPairing = nativeScan;

    const onConnected = vi.fn();
    render(
      <OnboardingScreen
        services={services}
        connection={connection}
        onConnected={onConnected}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /scan qr code/i }));
    const scanSettled = scanOperation();
    await waitFor(() => expect(fake.isHealthGatePending()).toBe(true));

    const input = screen.getByLabelText(/authorization url/i);
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(screen.getByRole("button", { name: /connect/i }));

    fake.resolveHealthGate();
    await scanSettled;
    await waitFor(() =>
      expect(screen.getByText(/hub\.example\.com:8443/i)).toBeInTheDocument(),
    );
    expect(nativeScan).not.toHaveBeenCalled();
    expect(fake.cancelPreviewCalls).toEqual([]);
    expectRedacted(document.documentElement.outerHTML);
  });

  it("scan then direct Cancel while refresh is gated never starts native", async () => {
    const services = createOnboardingServices();
    const fake = services.profile as FakeProfileService;
    const connection = createConnectionStore(fake);
    const scanOperation = trackPreviewScan(connection);
    fake.gateHealth();
    const nativeScan = vi.fn().mockResolvedValue({
      previewId: "must-not-start",
      origin: "https://hub.example.com:8443",
    });
    services.native.scanAndPreviewPairing = nativeScan;
    const onCancel = vi.fn(() => fake.resolveHealthGate());
    render(
      <OnboardingScreen
        services={services}
        connection={connection}
        onCancel={onCancel}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /scan qr code/i }));
    const scanSettled = scanOperation();
    await waitFor(() => expect(fake.isHealthGatePending()).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: /^cancel$/i }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    await scanSettled;

    expect(nativeScan).not.toHaveBeenCalled();
    expect(fake.cancelPreviewCalls).toEqual([]);
    expectRedacted(
      document.documentElement.outerHTML,
      JSON.stringify(fake.cancelPreviewCalls),
    );
  });

  it("unmount while refresh pending never calls native preview service", async () => {
    const services = createOnboardingServices();
    const fake = services.profile as FakeProfileService;
    const connection = createConnectionStore(fake);
    const scanOperation = trackPreviewScan(connection);
    fake.gateHealth();

    const nativeScan = vi.fn().mockResolvedValue({
      previewId: "scan-1",
      origin: "https://hub.example.com:8443",
    });
    services.native.scanAndPreviewPairing = nativeScan;

    const { unmount } = render(
      <OnboardingScreen services={services} connection={connection} />,
    );
    fireEvent.click(screen.getByRole("button", { name: /scan qr code/i }));
    const scanSettled = scanOperation();
    await waitFor(() => expect(fake.isHealthGatePending()).toBe(true));

    unmount();
    fake.resolveHealthGate();
    await scanSettled;
    expect(nativeScan).not.toHaveBeenCalled();
    expect(fake.cancelPreviewCalls).toEqual([]);
  });

  it("unmount after native scan starts cancels its eventual real ID once", async () => {
    const services = createOnboardingServices();
    const fake = services.profile as FakeProfileService;
    const scan = deferred<{ previewId: string; origin: string }>();
    const nativeScan = vi.fn(() => scan.promise);
    services.native.scanAndPreviewPairing = nativeScan;
    const { unmount } = render(<OnboardingScreen services={services} />);

    fireEvent.click(screen.getByRole("button", { name: /scan qr code/i }));
    await waitFor(() => expect(nativeScan).toHaveBeenCalledTimes(1));
    unmount();
    scan.resolve({
      previewId: "native-started",
      origin: "https://hub.example.com:8443",
    });

    await waitFor(() =>
      expect(fake.cancelPreviewCalls).toEqual(["native-started"]),
    );
    expectRedacted(JSON.stringify(fake.cancelPreviewCalls));
  });

  it("paste raw is cleared and its late result is cancelled", async () => {
    const services = createOnboardingServices();
    const fake = services.profile as FakeProfileService;
    // Gate previewPaste so the paste result arrives late.
    fake.gatePreview("previewPaste");

    const onCancel = vi.fn();
    render(<OnboardingScreen services={services} onCancel={onCancel} />);
    const input = screen.getByLabelText(/authorization url/i);
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(screen.getByRole("button", { name: /connect/i }));
    // Wait for previewPaste to block.
    await waitFor(() => expect(fake.isPreviewGatePending()).toBe(true));

    // Click Cancel — should invalidate the pending preview.
    fireEvent.click(screen.getByRole("button", { name: /^cancel$/i }));

    // Now resolve the paste — the stale result must be cancelled, not published.
    fake.resolvePreviewGate({
      previewId: "stale-paste",
      origin: "https://hub.example.com:8443",
    });
    await waitFor(() => expect(fake.isPreviewGatePending()).toBe(false));
    // The stale paste ID was best-effort cancelled with the service.
    expect(fake.cancelPreviewCalls).toContain("stale-paste");
    // The paste input was cleared synchronously (raw not retained in DOM).
    expect(
      (screen.getByLabelText(/authorization url/i) as HTMLInputElement).value,
    ).toBe("");
    expectRedacted(
      document.documentElement.outerHTML,
      JSON.stringify(fake.cancelPreviewCalls),
    );
  });
});

// ---------------------------------------------------------------------------
// IMPORTANT 2: ServerSwitcherSheet cleanup on unmount
// ---------------------------------------------------------------------------

describe("Repair sheet — unmount cleanup invalidates preview", () => {
  function renderRepairSheet() {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const connection = createConnectionStore(services.profile);
    const onClose = vi.fn();
    const view = render(
      <ServerSwitcherSheet
        connection={connection}
        navigation={createNavigationStore()}
        onSwitch={vi.fn()}
        onAdd={vi.fn()}
        onClose={onClose}
      />,
    );
    void connection.getState().refresh();
    return { services, connection, onClose, ...view };
  }

  async function openLaptopRepair(): Promise<void> {
    fireEvent.click(await screen.findByRole("button", { name: /^laptop/i }));
    fireEvent.click(await screen.findByRole("button", { name: /^re-?pair/i }));
  }

  async function startGatedRepair(fake: FakeProfileService): Promise<void> {
    fake.gatePreview("previewRepair");
    const urlInput = screen.getByLabelText(/paste new authorization url/i);
    fireEvent.change(urlInput, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(screen.getByRole("button", { name: /preview/i }));
    await waitFor(() => expect(fake.isPreviewGatePending()).toBe(true));
  }

  it("in-flight repair Cancel invalidates before a preview is visible", async () => {
    const { services, connection } = renderRepairSheet();
    const fake = fakeOf(services);
    await openLaptopRepair();
    await startGatedRepair(fake);

    fireEvent.click(screen.getByRole("button", { name: /^cancel$/i }));
    expect(connection.getState().preview).toBeNull();
    fake.resolvePreviewGate({
      previewId: "repair-after-cancel",
      origin: "https://hub.example.com:8443",
    });

    await waitFor(() =>
      expect(fake.cancelPreviewCalls).toEqual(["repair-after-cancel"]),
    );
    expect(connection.getState().preview).toBeNull();
    expectRedacted(
      document.documentElement.outerHTML,
      JSON.stringify(fake.cancelPreviewCalls),
    );
  });

  it("detail Back defensively invalidates an in-flight repair", async () => {
    const { services, connection } = renderRepairSheet();
    const fake = fakeOf(services);
    fireEvent.click(await screen.findByRole("button", { name: /^laptop/i }));
    fake.gatePreview("previewRepair");
    const repair = connection.getState().previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTPS,
    });
    await waitFor(() => expect(fake.isPreviewGatePending()).toBe(true));

    fireEvent.click(screen.getByRole("button", { name: /^back$/i }));
    fake.resolvePreviewGate({
      previewId: "repair-after-back",
      origin: "https://hub.example.com:8443",
    });
    await repair;

    expect(fake.cancelPreviewCalls).toEqual(["repair-after-back"]);
    expect(connection.getState().preview).toBeNull();
  });

  it("full sheet unmount invalidates an in-flight repair", async () => {
    const { services, connection, unmount } = renderRepairSheet();
    const fake = fakeOf(services);
    await openLaptopRepair();
    await startGatedRepair(fake);

    unmount();
    fake.resolvePreviewGate({
      previewId: "repair-after-unmount",
      origin: "https://hub.example.com:8443",
    });
    await waitFor(() =>
      expect(fake.cancelPreviewCalls).toEqual(["repair-after-unmount"]),
    );
    expect(connection.getState().preview).toBeNull();
  });

  it("unmount while repair preview visible cancels the preview", async () => {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const connection = createConnectionStore(services.profile);
    void connection.getState().refresh();
    render(
      <ServerSwitcherSheet
        connection={connection}
        navigation={createNavigationStore()}
        onSwitch={() => {}}
        onAdd={() => {}}
        onClose={() => {}}
      />,
    );
    // Open repair for laptop and start a preview.
    fireEvent.click(await screen.findByRole("button", { name: /^laptop/i }));
    fireEvent.click(await screen.findByRole("button", { name: /^re-?pair/i }));
    const urlInput = screen.getByLabelText(/paste new authorization url/i);
    fireEvent.change(urlInput, {
      target: { value: SAMPLE_AUTH_URL_HTTPS },
    });
    fireEvent.click(screen.getByRole("button", { name: /preview/i }));
    await waitFor(() => expect(connection.getState().preview).not.toBeNull());
    const previewId = exactPreviewId(connection.getState().preview);
    expect(previewId).toMatch(/^pv-/);

    // Unmount — the cleanup effect should cancel the preview.
    cleanup();
    await waitFor(() => expect(connection.getState().preview).toBeNull());
    expect(fakeOf(services).cancelPreviewCalls).toEqual([previewId]);
  });

  it("visible repair Cancel cancels its exact preview ID once", async () => {
    const { services, connection } = renderRepairSheet();
    const fake = fakeOf(services);
    await openLaptopRepair();
    const urlInput = screen.getByLabelText(/paste new authorization url/i);
    fireEvent.change(urlInput, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(screen.getByRole("button", { name: /preview/i }));
    await waitFor(() => expect(connection.getState().preview).not.toBeNull());
    const previewId = exactPreviewId(connection.getState().preview);
    expect(previewId).toMatch(/^pv-/);

    fireEvent.click(screen.getByRole("button", { name: /^cancel$/i }));
    await waitFor(() => expect(connection.getState().preview).toBeNull());

    expect(fake.cancelPreviewCalls).toEqual([previewId]);
  });

  it("confirmed re-pair ID is consumed and never cancelled by cleanup", async () => {
    const { services, connection, unmount } = renderRepairSheet();
    const fake = fakeOf(services);
    await openLaptopRepair();
    const urlInput = screen.getByLabelText(/paste new authorization url/i);
    fireEvent.change(urlInput, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(screen.getByRole("button", { name: /preview/i }));
    await waitFor(() => expect(connection.getState().preview).not.toBeNull());
    const consumedId = exactPreviewId(connection.getState().preview);
    expect(consumedId).toMatch(/^pv-/);

    fireEvent.click(screen.getByRole("button", { name: /confirm re-pair/i }));
    await waitFor(() => expect(connection.getState().preview).toBeNull());
    unmount();
    await connection.getState().cancelPreview();

    expect(fake.cancelPreviewCalls).not.toContain(consumedId);
  });
});

// ---------------------------------------------------------------------------
// IMPORTANT 3: confirmed ID not cancelled by later cleanup
// ---------------------------------------------------------------------------

describe("Onboarding — confirmed preview not cancelled on unmount", () => {
  it("unmount after successful pairing does not cancel the consumed preview ID", async () => {
    const services = createOnboardingServices();
    const fake = services.profile as FakeProfileService;
    const onConnected = vi.fn();
    const { unmount } = render(
      <OnboardingScreen services={services} onConnected={onConnected} />,
    );
    const input = screen.getByLabelText(/authorization url/i);
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(screen.getByRole("button", { name: /connect/i }));
    await screen.findByText(/hub\.example\.com:8443/i);
    const consumedId = fake.previewIdsIssued[0] as string;
    expect(consumedId).toMatch(/^pv-/);
    fireEvent.change(screen.getByLabelText(/server name/i), {
      target: { value: "my hub" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^connect/i }));
    await waitFor(() => expect(onConnected).toHaveBeenCalled());

    unmount();
    expect(fake.cancelPreviewCalls).not.toContain(consumedId);
    expectRedacted(JSON.stringify(fake.cancelPreviewCalls));
  });
});

// ---------------------------------------------------------------------------
// IMPORTANT 4: fixture reachability seeds + unknown glyph styling
// ---------------------------------------------------------------------------

describe("Honest reachability — explicit fixture seeds", () => {
  function renderSessions(
    reachability: Record<string, string>,
    activeProfileId = "p1",
  ) {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId,
    });
    const connection = createConnectionStore(services.profile);
    void connection.getState().refresh();
    for (const [id, state] of Object.entries(reachability)) {
      connection.getState().setReachability(id, state as never);
    }
    render(
      <SessionsScreen connection={connection} onOpenSwitcher={() => {}} />,
    );
    return { connection };
  }

  it("reachable shows Connected", async () => {
    renderSessions({ p1: "reachable" });
    await screen.findByText(/connected/i);
  });

  it("unreachable shows Offline", async () => {
    renderSessions({ p1: "unreachable" });
    await screen.findByText(/offline/i);
  });

  it("reconnecting shows Reconnecting only for explicit reconnecting", async () => {
    renderSessions({ p1: "reconnecting" });
    await screen.findByText(/reconnecting/i);
  });

  it("unknown shows Not checked, not Reconnecting", async () => {
    renderSessions({ p1: "unknown" });
    await screen.findByText(/not checked/i);
    expect(screen.queryByText(/reconnecting/i)).not.toBeInTheDocument();
  });

  it("missing reachability maps to Not checked, not Reconnecting", async () => {
    renderSessions({});
    await screen.findByText(/not checked/i);
    expect(screen.queryByText(/reconnecting/i)).not.toBeInTheDocument();
  });

  it("every saved fixture profile has an explicit allowed reachability", () => {
    const routes: readonly FixtureRoute[] = [
      "onboarding",
      "servers",
      "sessions",
      "new",
      "settings",
    ];
    const allowed: readonly Reachability[] = [
      "reachable",
      "reconnecting",
      "unreachable",
      "unknown",
    ];
    const observed = new Set<Reachability>();

    for (const route of routes) {
      const fixture = createFixture(route);
      expect(Object.keys(fixture.reachabilitySeed).sort()).toEqual(
        fixture.seedProfiles.map((profile) => profile.id).sort(),
      );
      for (const state of Object.values(fixture.reachabilitySeed)) {
        expect(allowed).toContain(state);
        observed.add(state);
      }
    }

    expect(observed).toEqual(new Set(allowed));
  });

  it("fixture ShellHost applies explicit reachability to saved profiles", async () => {
    render(<App fixtureRoute="sessions" />);
    await screen.findByText(/not checked/i);
    fireEvent.click(
      await screen.findByRole("button", {
        name: /laptop.*active server.*unknown/i,
      }),
    );
    await screen.findByRole("dialog", { name: /servers/i });
    expect(screen.getByText(/reconnecting/i)).toBeInTheDocument();
  });

  it("StatusMark unknown has data-status=unknown and ? glyph", () => {
    const { container } = render(<StatusMark status="unknown" />);
    const mark = container.querySelector(
      '.evener-status-mark[data-status="unknown"]',
    );
    expect(mark).not.toBeNull();
    expect(mark?.querySelector(".evener-status-mark__glyph")?.textContent).toBe(
      "?",
    );
    expect(
      mark?.querySelector(".evener-status-mark__label")?.textContent,
    ).toMatch(/not checked/i);
  });
});

// ---------------------------------------------------------------------------
// IMPORTANT 6: strengthened remove test
// ---------------------------------------------------------------------------

describe("Remove failure then retry — preserves state, closes exactly once", () => {
  it("failure retains exact state and confirm UI; retry closes exactly once", async () => {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const onClose = vi.fn();
    const onSwitch = vi.fn();
    const connection = createConnectionStore(services.profile);
    const navigation = createNavigationStore();
    void connection.getState().refresh();
    render(
      <ServerSwitcherSheet
        connection={connection}
        navigation={navigation}
        onSwitch={onSwitch}
        onAdd={() => {}}
        onClose={onClose}
      />,
    );
    (services.profile as FakeProfileService).failOnce("remove");
    fireEvent.click(await screen.findByRole("button", { name: /^laptop/i }));
    const profileBefore = connection
      .getState()
      .profiles.find((p) => p.id === "p1");
    expect(profileBefore).toEqual(PROFILES[0]);
    const activeBefore = connection.getState().activeProfileId;
    fireEvent.click(await screen.findByRole("button", { name: /^remove$/i }));
    fireEvent.click(
      await screen.findByRole("button", { name: /confirm.*remove/i }),
    );
    const failure = await screen.findByText(/remove.*failed/i);

    expect(connection.getState().profiles.find((p) => p.id === "p1")).toEqual(
      profileBefore,
    );
    expect(connection.getState().activeProfileId).toBe(activeBefore);
    expect(
      screen.getByRole("button", { name: /confirm.*remove/i }),
    ).toBeEnabled();
    expect(
      screen.getByRole("button", { name: /^cancel$/i }),
    ).toBeInTheDocument();
    expect(onClose).toHaveBeenCalledTimes(0);
    expect(onSwitch).toHaveBeenCalledTimes(0);
    expectRedacted(document.documentElement.outerHTML, failure.outerHTML);

    fireEvent.click(screen.getByRole("button", { name: /confirm.*remove/i }));
    await waitFor(() =>
      expect(connection.getState().activeProfileId).toBe("p2"),
    );
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onSwitch).toHaveBeenCalledTimes(0);
    expect(connection.getState().profiles.map((p) => p.id)).not.toContain("p1");
  });
});

// ---------------------------------------------------------------------------
// IMPORTANT 7: real rendered switch clears conversations only after success
// ---------------------------------------------------------------------------

describe("Switch action — clears conversations only after successful selection", () => {
  it("successful switch clears the conversation stack", async () => {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const connection = createConnectionStore(services.profile);
    const navigation = createNavigationStore();
    void connection.getState().refresh();
    navigation
      .getState()
      .pushConversation({ sessionId: "s1", title: "Thread" });
    expect(navigation.getState().conversationStack.length).toBe(1);

    render(
      <ServerSwitcherSheet
        connection={connection}
        navigation={navigation}
        onSwitch={vi.fn()}
        onAdd={() => {}}
        onClose={() => {}}
      />,
    );
    fireEvent.click(await screen.findByRole("button", { name: /^server/i }));
    fireEvent.click(
      await screen.findByRole("button", { name: /use this server/i }),
    );
    await waitFor(() =>
      expect(connection.getState().activeProfileId).toBe("p2"),
    );
    expect(navigation.getState().conversationStack.length).toBe(0);
  });

  it("failed switch does not clear the conversation stack", async () => {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    (services.profile as FakeProfileService).failOnce("select");
    const connection = createConnectionStore(services.profile);
    const navigation = createNavigationStore();
    void connection.getState().refresh();
    navigation
      .getState()
      .pushConversation({ sessionId: "s1", title: "Thread" });
    expect(navigation.getState().conversationStack.length).toBe(1);

    render(
      <ServerSwitcherSheet
        connection={connection}
        navigation={navigation}
        onSwitch={vi.fn()}
        onAdd={() => {}}
        onClose={() => {}}
      />,
    );
    fireEvent.click(await screen.findByRole("button", { name: /^server/i }));
    fireEvent.click(
      await screen.findByRole("button", { name: /use this server/i }),
    );
    // Switch fails — conversations NOT cleared.
    await waitFor(() =>
      expect(screen.getByText(/switch.*failed/i)).toBeInTheDocument(),
    );
    expect(navigation.getState().conversationStack.length).toBe(1);
    expect(connection.getState().activeProfileId).toBe("p1");
  });
});
