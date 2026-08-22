import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SAMPLE_AUTH_URL_HTTPS } from "../test/fakeProfileService";
import { createOnboardingServices } from "./fixture-services";
import { OnboardingScreen } from "./OnboardingScreen";

afterEach(() => {
  cleanup();
});

describe("C-haptic: pairing success commits even if haptic fails", () => {
  it("onConnected fires even when hapticPerform rejects", async () => {
    const services = createOnboardingServices();
    // Override hapticPerform to reject.
    const _origHaptic = services.native.hapticPerform.bind(services.native);
    services.native.hapticPerform = async () => {
      throw new Error("haptic unavailable");
    };
    const onConnected = vi.fn();
    render(<OnboardingScreen services={services} onConnected={onConnected} />);
    const input = screen.getByLabelText(/authorization url|paste/i);
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(
      screen.getByRole("button", { name: /connect|preview|next/i }),
    );
    await screen.findByText(/hub\.example\.com:8443/i);
    fireEvent.change(screen.getByLabelText(/server name/i), {
      target: { value: "my hub" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^add|connect|save/i }));
    // onConnected must fire despite haptic rejection.
    await vi.waitFor(() => expect(onConnected).toHaveBeenCalled(), {
      timeout: 3000,
    });
  });
});

describe("I-preview: stale preview does not replace latest", () => {
  it("a second previewPaste supersedes the first", async () => {
    // Superseded by the concurrent stale-op tests in
    // state/connection-protocol.test.ts which gate resolution to prove a
    // stale op never publishes. Sequential resolves cannot prove staleness.
    // Kept as a smoke check that the latest origin wins.
    const { createConnectionStore } = await import("../state/connection");
    const { FakeProfileService, SAMPLE_AUTH_URL_HTTPS, SAMPLE_AUTH_URL_HTTP } =
      await import("../test/fakeProfileService");
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = createConnectionStore(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    expect(store.getState().preview?.origin).toBe("http://192.168.1.10:8080");
  });
});

describe("I-repair-same-id: fixture re-pair replaces same profile ID", () => {
  it("re-pair does not create a third profile", async () => {
    const { createConnectionStore } = await import("../state/connection");
    const { FakeProfileService, SAMPLE_AUTH_URL_HTTPS } = await import(
      "../test/fakeProfileService"
    );
    const service = new FakeProfileService({
      profiles: [
        { id: "p1", name: "laptop", origin: "https://hub.example.com:8443" },
        { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
      ],
      activeProfileId: "p1",
    });
    const store = createConnectionStore(service);
    await store.getState().refresh();
    const countBefore = store.getState().profiles.length;
    // Preview repair for p1.
    await store.getState().previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTPS,
    });
    const previewId = store.getState().preview?.previewId;
    expect(previewId).toBeDefined();
    // Re-pair should replace credentials for the same profile, not add a new one.
    await store.getState().rePair("p1", previewId ?? "", "laptop", false);
    expect(store.getState().profiles.length).toBe(countBefore);
  });
});
