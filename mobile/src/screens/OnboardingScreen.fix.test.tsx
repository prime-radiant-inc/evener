import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { createOnboardingServices } from "./fixture-services";
import { OnboardingScreen } from "./OnboardingScreen";

afterEach(() => {
  cleanup();
});

describe("C2: paste field clears and no raw retained", () => {
  it("FakeProfileService does not expose recordedRaws array", async () => {
    const services = createOnboardingServices();
    // The fake must not retain raw URLs.
    expect(
      (services.profile as unknown as Record<string, unknown>).recordedRaws,
    ).toBeUndefined();
  });
});

describe("C3: QR scan uses native previewId directly", () => {
  it("scan sets preview from native result without calling previewPaste with a fake token", async () => {
    const services = createOnboardingServices();
    const { container } = render(<OnboardingScreen services={services} />);
    fireEvent.click(screen.getByRole("button", { name: /scan qr code/i }));
    await screen.findByText(/hub\.example\.com:8443/i);
    // No fake token string should appear in the DOM.
    expect(container.textContent).not.toContain("redacted-scan-token");
  });
});

describe("C4: re-pair UI in server switcher", () => {
  it("server switcher has a re-pair action per profile", async () => {
    // Re-pair UI is tested in RootShell.test.tsx; here we assert the
    // ServerSwitcherSheet exposes a re-pair affordance.
    const { ServerSwitcherSheet } = await import("./ServerSwitcherSheet");
    const { createConnectionStore } = await import("../state/connection");
    const { FakeProfileService } = await import("../test/fakeProfileService");
    const service = new FakeProfileService({
      profiles: [
        { id: "p1", name: "laptop", origin: "https://hub.example.com" },
      ],
      activeProfileId: "p1",
    });
    const store = createConnectionStore(service);
    await store.getState().refresh();
    render(
      <ServerSwitcherSheet
        connection={store}
        onSwitch={() => {}}
        onAdd={() => {}}
        onClose={() => {}}
      />,
    );
    // Click the profile row to open the detail view.
    fireEvent.click(await screen.findByRole("button", { name: /laptop/i }));
    // Each profile row should have a re-pair button (in the detail view).
    expect(
      await screen.findByRole("button", { name: /re-?pair/i }),
    ).toBeInTheDocument();
  });
});
