import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
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

function renderShell(
  opts: {
    profiles?: readonly { id: string; name: string; origin: string }[];
    activeProfileId?: string | null;
  } = {},
) {
  const services = createShellServices({
    profiles: opts.profiles ?? PROFILES,
    activeProfileId: opts.activeProfileId ?? "p1",
  });
  const stores = {
    connection: createConnectionStore(services.profile),
    navigation: createNavigationStore(),
    preferences: createPreferencesStore(),
  };
  render(<RootShell services={services} stores={stores} />);
  return { services, stores };
}

describe("I: tablist/tab/panel semantics", () => {
  it("bottom bar uses role=tablist on the container", async () => {
    renderShell();
    await screen.findByRole("tab", { name: /sessions/i });
    const tablist = screen.getByRole("tablist");
    expect(tablist).toBeInTheDocument();
    expect(tablist.getAttribute("aria-label")).toBe("Primary");
  });

  it("each tab has aria-controls pointing to its panel", async () => {
    renderShell();
    await screen.findByRole("tab", { name: /sessions/i });
    const sessionsTab = screen.getByRole("tab", { name: /sessions/i });
    const controls = sessionsTab.getAttribute("aria-controls");
    expect(controls).toBeTruthy();
    const panel = document.getElementById(controls!);
    expect(panel).not.toBeNull();
    expect(panel?.getAttribute("role")).toBe("tabpanel");
  });
});

describe("I: one scroller per screen", () => {
  it("root shell does not nest screen-scroll inside screen-scroll", async () => {
    renderShell();
    await screen.findByRole("tab", { name: /sessions/i });
    // Count elements with the screen-scroll class — should be exactly 1.
    const scrollers = document.querySelectorAll(".evener-screen-scroll");
    expect(scrollers.length).toBe(1);
  });
});

describe("I: initial loading prevents onboarding flash", () => {
  it("shows a loading state, not onboarding, while profiles are loading", async () => {
    const services = createShellServices({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const stores = {
      connection: createConnectionStore(services.profile),
      navigation: createNavigationStore(),
      preferences: createPreferencesStore(),
    };
    render(<RootShell services={services} stores={stores} />);
    // Before refresh completes, should show loading, NOT onboarding.
    // The connection status starts as "initial" — show loading, not onboarding.
    expect(
      screen.queryByRole("heading", { name: /evener/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /scan qr/i }),
    ).not.toBeInTheDocument();
  });
});

describe("I: honest reachability — unknown unless real data", () => {
  it("reachability defaults to unknown/loading, not fabricated Connected", async () => {
    renderShell();
    await screen.findByRole("tab", { name: /sessions/i });
    // Without real health data, the status should not claim "Connected".
    expect(screen.queryByText("Connected")).not.toBeInTheDocument();
  });
});

describe("I: noninteractive diagnostics render disabled/static", () => {
  it("Settings diagnostic rows are not enabled no-op buttons", async () => {
    renderShell();
    await screen.findByRole("tab", { name: /sessions/i });
    fireEvent.click(screen.getByRole("tab", { name: /settings/i }));
    // The diagnostics placeholder button should be disabled.
    const diagBtn = await screen.findByRole("button", {
      name: /diagnostics.*task 8/i,
    });
    expect(diagBtn).toBeDisabled();
  });
});

describe("I: full normalized origin shown including scheme", () => {
  it("switcher shows the full origin with scheme, not just host", async () => {
    renderShell();
    await screen.findByRole("tab", { name: /sessions/i });
    fireEvent.click(
      screen.getByRole("button", {
        name: /laptop.*active server|active server/i,
      }),
    );
    // The full origin including https:// should be visible in the sheet.
    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toMatch(/https:\/\/hub\.example\.com:8443/i);
  });
});

describe("I: Add has Cancel/Back", () => {
  it("onboarding add flow has a Cancel/Back button to return", async () => {
    renderShell();
    await screen.findByRole("tab", { name: /sessions/i });
    fireEvent.click(
      screen.getByRole("button", {
        name: /laptop.*active server|active server/i,
      }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: /add.*server|add.*hub/i }),
    );
    // Should have a Cancel or Back button.
    expect(
      await screen.findByRole("button", { name: /cancel|back|‹/i }),
    ).toBeInTheDocument();
  });
});
