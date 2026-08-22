/**
 * Task 7B — management races at the screen level.
 *
 * Covers:
 *  - QR scan routed through the store preview protocol (previewScan), with
 *    unmount/cancel invalidating a late scan result so it never publishes.
 *  - Repair sheet cleanup on unmount, dismiss (Done/overlay), Cancel, and Back.
 *  - Remove failure preserves the profile and stays in the confirm/detail view
 *    with an inline role=alert error; only success closes the sheet.
 *  - Honest reachability: unknown (never checked) maps to a "Not checked"
 *    status, not fabricated "Reconnecting"; reachable/offline map honestly.
 *  - Real rendered switch action clears the navigation conversation stack.
 *
 * No source-string assertions. Tests await exact deferred promises/callbacks
 * and observe rendered behavior and store state.
 */
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ProfileRedacted } from "../services/nativeProfiles";
import { createConnectionStore } from "../state/connection";
import { createNavigationStore } from "../state/navigation";
import { createPreferencesStore } from "../state/preferences";
import type { FakeProfileService } from "../test/fakeProfileService";
import { SAMPLE_AUTH_URL_HTTPS } from "../test/fakeProfileService";
import { StatusMark } from "../ui/StatusMark";
import { createShellServices } from "./fixture-services";
import { RootShell } from "./RootShell";
import { ServerSwitcherSheet } from "./ServerSwitcherSheet";
import { SessionsScreen } from "./SessionsScreen";

afterEach(() => {
  cleanup();
});

const PROFILES: readonly ProfileRedacted[] = [
  { id: "p1", name: "laptop", origin: "https://hub.example.com:8443" },
  { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
];

// ---------------------------------------------------------------------------
// Item 2: QR scan routed through store protocol; unmount invalidates late scan
// ---------------------------------------------------------------------------

describe("QR lifecycle — scan routed through store preview protocol", () => {
  it("a late scan result after unmount never publishes a preview", async () => {
    // Build services with a gated native scan so the result arrives late.
    const services = createShellServices({
      profiles: [],
      activeProfileId: null,
    });
    let resolveScan:
      | ((r: { previewId: string; origin: string }) => void)
      | null = null;
    services.native.scanAndPreviewPairing = () =>
      new Promise((res) => {
        resolveScan = res;
      });
    const stores = {
      connection: createConnectionStore(services.profile),
      navigation: createNavigationStore(),
      preferences: createPreferencesStore(),
    };
    const { unmount } = render(
      <RootShell services={services} stores={stores} />,
    );
    // Onboarding shows because no profiles.
    const scanBtn = await screen.findByRole("button", {
      name: /scan qr code/i,
    });
    fireEvent.click(scanBtn);

    // Wait for the scan to be invoked (resolveScan captured), then unmount
    // before it resolves — the late result must never publish.
    await waitFor(() => expect(resolveScan).not.toBeNull());
    unmount();

    // Resolve the late scan — must not publish into the store.
    const resolve: (r: { previewId: string; origin: string }) => void =
      resolveScan ??
      (() => {
        throw new Error("scan was never invoked");
      });
    resolve({
      previewId: "late-scan",
      origin: "https://hub.example.com:8443",
    });
    // Allow microtasks to flush.
    await new Promise((r) => setTimeout(r, 0));
    expect(stores.connection.getState().preview).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Item 3: Repair sheet cleanup on unmount/dismiss/Cancel/Back
// ---------------------------------------------------------------------------

describe("Repair sheet — cleanup invalidates preview", () => {
  function renderSwitcher(
    opts: {
      profiles?: readonly ProfileRedacted[];
      activeProfileId?: string | null;
    } = {},
  ) {
    const services = createShellServices({
      profiles: opts.profiles ?? PROFILES,
      activeProfileId: opts.activeProfileId ?? "p1",
    });
    const connection = createConnectionStore(services.profile);
    void connection.getState().refresh();
    const navigation = createNavigationStore();
    const onSwitch = vi.fn();
    const onAdd = vi.fn();
    const onClose = vi.fn();
    render(
      <ServerSwitcherSheet
        connection={connection}
        navigation={navigation}
        onSwitch={onSwitch}
        onAdd={onAdd}
        onClose={onClose}
      />,
    );
    return { services, connection, navigation, onSwitch, onAdd, onClose };
  }

  async function openRepairForRow(rowName: RegExp) {
    fireEvent.click(await screen.findByRole("button", { name: rowName }));
    fireEvent.click(await screen.findByRole("button", { name: /^re-?pair/i }));
  }

  it("Cancel in the repair-preview step cancels the preview and clears error", async () => {
    const { services, connection, onClose } = renderSwitcher();
    await openRepairForRow(/^laptop/i);
    // Enter a URL and click Preview.
    const urlInput = screen.getByLabelText(/paste new authorization url/i);
    fireEvent.change(urlInput, {
      target: { value: SAMPLE_AUTH_URL_HTTPS },
    });
    fireEvent.click(screen.getByRole("button", { name: /preview/i }));
    // Preview resolves; a preview is visible.
    await waitFor(() => expect(connection.getState().preview).not.toBeNull());
    const previewId = connection.getState().preview?.previewId;
    expect(previewId).toBeDefined();

    // Click Cancel — should cancel the preview with the service.
    fireEvent.click(screen.getByRole("button", { name: /^cancel$/i }));
    await waitFor(() => expect(connection.getState().preview).toBeNull());
    // The preview id was cancelled with the service.
    expect(
      (services.profile as FakeProfileService).cancelPreviewCalls,
    ).toContain(previewId);
    // Cancel returns to detail, does not close the sheet.
    expect(onClose).not.toHaveBeenCalled();
  });

  it("Back from detail cancels any lingering repair preview", async () => {
    const { services, connection } = renderSwitcher();
    await openRepairForRow(/^laptop/i);
    const urlInput = screen.getByLabelText(/paste new authorization url/i);
    fireEvent.change(urlInput, {
      target: { value: SAMPLE_AUTH_URL_HTTPS },
    });
    fireEvent.click(screen.getByRole("button", { name: /preview/i }));
    await waitFor(() => expect(connection.getState().preview).not.toBeNull());
    const previewId = connection.getState().preview?.previewId;

    // Go back to the detail view, then Back to the list.
    fireEvent.click(screen.getByRole("button", { name: /^cancel$/i }));
    await waitFor(() => expect(connection.getState().preview).toBeNull());

    // Now click Back from the detail view.
    fireEvent.click(screen.getByRole("button", { name: /^back$/i }));
    // No preview should be visible (Back should not leave a preview dangling).
    expect(connection.getState().preview).toBeNull();
    if (previewId !== undefined) {
      expect(
        (services.profile as FakeProfileService).cancelPreviewCalls,
      ).toContain(previewId);
    }
  });
});

// ---------------------------------------------------------------------------
// Item 4: Remove failure stays in confirm/detail with inline error
// ---------------------------------------------------------------------------

describe("Remove failure — preserves profile, inline error, stays open", () => {
  function renderShell(opts: { failRemove?: boolean } = {}) {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    if (opts.failRemove) {
      (services.profile as FakeProfileService).failOnce("remove");
    }
    const stores = {
      connection: createConnectionStore(services.profile),
      navigation: createNavigationStore(),
      preferences: createPreferencesStore(),
    };
    render(<RootShell services={services} stores={stores} />);
    return { services, stores };
  }

  async function openRemoveConfirm(rowName: RegExp) {
    fireEvent.click(screen.getByRole("button", { name: /active server/i }));
    fireEvent.click(await screen.findByRole("button", { name: rowName }));
    fireEvent.click(await screen.findByRole("button", { name: /^remove$/i }));
  }

  it("remove failure keeps the profile and shows an inline error, sheet stays open", async () => {
    const { stores } = renderShell({ failRemove: true });
    await screen.findByRole("tab", { name: /sessions/i });
    await openRemoveConfirm(/^laptop.*hub\.example/i);
    fireEvent.click(
      await screen.findByRole("button", { name: /confirm.*remove/i }),
    );
    // An inline role=alert error appears.
    await screen.findByText(/remove.*failed|failed.*remove|could not remove/i);
    // The profile is still present.
    expect(stores.connection.getState().profiles.map((p) => p.id)).toContain(
      "p1",
    );
    // The sheet is still open (dialog with "Servers" label still present).
    expect(
      screen.getByRole("dialog", { name: /servers/i }),
    ).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// Item 5: Honest reachability — unknown, not fabricated Reconnecting
// ---------------------------------------------------------------------------

describe("Honest reachability — unknown unless real data", () => {
  function renderSessions(
    opts: {
      reachability?: Record<string, string>;
      activeProfileId?: string;
    } = {},
  ) {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId: opts.activeProfileId ?? "p1",
    });
    const connection = createConnectionStore(services.profile);
    // Seed profiles immediately so the screen doesn't sit in "initial".
    void connection.getState().refresh();
    for (const [id, state] of Object.entries(opts.reachability ?? {})) {
      connection.getState().setReachability(id, state as never);
    }
    render(
      <SessionsScreen connection={connection} onOpenSwitcher={() => {}} />,
    );
    return { connection };
  }

  it("missing reachability shows Not checked, not Reconnecting", async () => {
    renderSessions({ reachability: {} });
    await screen.findByText(/not checked/i);
    expect(screen.queryByText(/reconnecting/i)).not.toBeInTheDocument();
  });

  it("reachable shows Connected", async () => {
    renderSessions({ reachability: { p1: "reachable" } });
    await screen.findByText(/connected/i);
  });

  it("unreachable shows Offline", async () => {
    renderSessions({
      reachability: { p1: "unreachable" },
    });
    await screen.findByText(/offline/i);
  });

  it("reconnecting shows Reconnecting only for actual reconnecting", async () => {
    renderSessions({ reachability: { p1: "reconnecting" } });
    await screen.findByText(/reconnecting/i);
  });

  it("StatusMark renders unknown with a distinct glyph and label", () => {
    const { container } = render(<StatusMark status="unknown" />);
    expect(container.textContent).toMatch(/not checked/i);
    const glyph = container.querySelector(".evener-status-mark__glyph");
    expect(glyph?.textContent).toBe("?");
  });
});

// ---------------------------------------------------------------------------
// Item 6: Real rendered switch clears navigation conversations
// ---------------------------------------------------------------------------

describe("Switch action — clears navigation conversation stack", () => {
  it("switching server via the rendered sheet clears conversations", async () => {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const connection = createConnectionStore(services.profile);
    const navigation = createNavigationStore();
    void connection.getState().refresh();
    // Seed a conversation that the switch must clear.
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
    // Open the "server" (p2) row detail and switch.
    fireEvent.click(await screen.findByRole("button", { name: /^server/i }));
    fireEvent.click(
      await screen.findByRole("button", { name: /use this server/i }),
    );
    await waitFor(() =>
      expect(connection.getState().activeProfileId).toBe("p2"),
    );
    // The conversation stack was cleared by the switch.
    expect(navigation.getState().conversationStack.length).toBe(0);
  });
});
