import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "./App";

afterEach(() => {
  cleanup();
});

describe("App", () => {
  it("renders the dedicated mobile shell (no Hub widgets)", async () => {
    render(<App fixtureRoute="onboarding" />);
    // With no profiles, the onboarding screen shows.
    expect(
      await screen.findByRole("heading", { name: /evener/i }),
    ).toBeInTheDocument();
    // No Dockview or desktop web widgets.
    expect(screen.queryByText("Dockview")).not.toBeInTheDocument();
  });

  it("fixture=onboarding shows the connect flow", async () => {
    render(<App fixtureRoute="onboarding" />);
    expect(
      await screen.findByRole("button", { name: /scan qr/i }),
    ).toBeInTheDocument();
    expect(
      await screen.findByRole("button", { name: /paste/i }),
    ).toBeInTheDocument();
  });

  it("fixture=sessions shows the three-tab shell", async () => {
    render(<App fixtureRoute="sessions" />);
    expect(
      await screen.findByRole("tab", { name: /sessions/i }),
    ).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /new/i })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /settings/i })).toBeInTheDocument();
  });

  it("fixture=settings opens on the settings tab", async () => {
    render(<App fixtureRoute="settings" />);
    expect(await screen.findByText(/appearance|theme/i)).toBeInTheDocument();
  });
});
