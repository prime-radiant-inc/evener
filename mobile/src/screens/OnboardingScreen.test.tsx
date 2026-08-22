import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { FakeNativeBridge } from "../native/fake";
import {
  SAMPLE_AUTH_URL_HTTP,
  SAMPLE_AUTH_URL_HTTPS,
  SECRET_TOKEN,
} from "../test/fakeProfileService";
import { createOnboardingServices } from "./fixture-services";
import { OnboardingScreen } from "./OnboardingScreen";

afterEach(() => {
  cleanup();
});

// A minimal in-test service bundle for OnboardingScreen.
function _makeServices(_opts?: {
  scanPreview?: FakeNativeBridge["setScanPreview"] extends never
    ? never
    : { previewId: string; origin: string } | null;
}) {
  const profiles = createOnboardingServices();
  return profiles;
}

describe("OnboardingScreen — identity and actions", () => {
  it("renders a full-screen Evener identity and clear copy", () => {
    const services = createOnboardingServices();
    render(<OnboardingScreen services={services} />);
    expect(screen.getByRole("heading", { name: /evener/i })).toBeVisible();
    // Copy explains private-network HTTP warning.
    expect(
      screen.getByText(/local network|trusted.*network/i),
    ).toBeInTheDocument();
  });

  it("Scan QR is the primary action and Paste URL is secondary", () => {
    const services = createOnboardingServices();
    render(<OnboardingScreen services={services} />);
    const scan = screen.getByRole("button", { name: /scan qr code/i });
    const paste = screen.getByRole("button", { name: /connect/i });
    // Primary scan has a higher visual prominence class than paste (secondary).
    expect(scan.className).toMatch(/primary/i);
    expect(paste.className).toMatch(/secondary|tertiary/i);
    expect(scan).toBeInTheDocument();
    expect(paste).toBeInTheDocument();
  });
});

describe("OnboardingScreen — paste flow redaction", () => {
  it("clears the paste input immediately after invoking preview", async () => {
    const services = createOnboardingServices();
    render(<OnboardingScreen services={services} />);
    const input = screen.getByLabelText(
      /authorization url/i,
    ) as HTMLInputElement;
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    const pasteButton = screen.getByRole("button", {
      name: /connect|preview|next/i,
    });
    fireEvent.click(pasteButton);
    // Input cleared synchronously after the click dispatch.
    expect(input.value).toBe("");
  });

  it("never shows the token or query in the DOM after paste", async () => {
    const services = createOnboardingServices();
    const { container } = render(<OnboardingScreen services={services} />);
    const input = screen.getByLabelText(/authorization url/i);
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(
      screen.getByRole("button", { name: /connect|preview|next/i }),
    );
    expect(container.textContent).not.toContain(SECRET_TOKEN);
    expect(container.textContent).not.toContain("token=");
  });

  it("shows the confirmed origin (scheme/host/port) before save, never the token", async () => {
    const services = createOnboardingServices();
    render(<OnboardingScreen services={services} />);
    const input = screen.getByLabelText(/authorization url/i);
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(
      screen.getByRole("button", { name: /connect|preview|next/i }),
    );
    // After preview, the redacted origin appears for confirmation.
    const originText = await screen.findByText(/hub\.example\.com:8443/i);
    expect(originText).toBeInTheDocument();
    expect(
      screen.queryByText(new RegExp(SECRET_TOKEN)),
    ).not.toBeInTheDocument();
  });
});

describe("OnboardingScreen — name and origin confirmation", () => {
  async function pasteAndPreview(
    services: ReturnType<typeof createOnboardingServices>,
  ) {
    render(<OnboardingScreen services={services} />);
    const input = screen.getByLabelText(/authorization url/i);
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(
      screen.getByRole("button", { name: /connect|preview|next/i }),
    );
    await screen.findByText(/hub\.example\.com:8443/i);
  }

  it("requires a unique server name before enabling save", async () => {
    const services = createOnboardingServices({
      existing: [
        { id: "p1", name: "laptop", origin: "https://other.example.com:9000" },
      ],
    });
    await pasteAndPreview(services);
    const nameInput = screen.getByLabelText(/server name/i) as HTMLInputElement;
    // Empty name => save disabled.
    const save = screen.getByRole("button", { name: /^connect|add|save/i });
    expect(
      save.hasAttribute("disabled") ||
        save.getAttribute("aria-disabled") === "true",
    ).toBe(true);
    fireEvent.change(nameInput, { target: { value: "my hub" } });
    expect(save.hasAttribute("disabled")).toBe(false);
  });

  it("blocks duplicate name and shows a message", async () => {
    const services = createOnboardingServices({
      existing: [
        { id: "p1", name: "laptop", origin: "https://other.example.com:9000" },
      ],
    });
    await pasteAndPreview(services);
    const nameInput = screen.getByLabelText(/server name/i);
    fireEvent.change(nameInput, { target: { value: "laptop" } });
    expect(await screen.findByText(/must be unique/i)).toBeInTheDocument();
  });

  it("requires explicit consent for a duplicate origin", async () => {
    const services = createOnboardingServices({
      existing: [
        { id: "p1", name: "first", origin: "https://hub.example.com:8443" },
      ],
    });
    await pasteAndPreview(services);
    const nameInput = screen.getByLabelText(/server name/i);
    fireEvent.change(nameInput, { target: { value: "second" } });
    // Duplicate origin requires a consent control.
    expect(
      await screen.findByText(
        /already have.*server|duplicate origin|second.*credential/i,
      ),
    ).toBeInTheDocument();
    const consent = screen.getByRole("checkbox", {
      name: /confirm|second.*credential|duplicate/i,
    });
    expect(consent).not.toBeChecked();
    const save = screen.getByRole("button", { name: /^connect|add|save/i });
    // Save blocked until consent checked.
    expect(save.hasAttribute("disabled")).toBe(true);
    fireEvent.click(consent);
    expect(save.hasAttribute("disabled")).toBe(false);
  });
});

describe("OnboardingScreen — success navigation", () => {
  it("on successful add, haptic fires and navigates to Sessions", async () => {
    const services = createOnboardingServices();
    const onConnected = vi.fn();
    render(<OnboardingScreen services={services} onConnected={onConnected} />);
    const input = screen.getByLabelText(/authorization url/i);
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(
      screen.getByRole("button", { name: /connect|preview|next/i }),
    );
    await screen.findByText(/hub\.example\.com:8443/i);
    const nameInput = screen.getByLabelText(/server name/i);
    fireEvent.change(nameInput, { target: { value: "my hub" } });
    fireEvent.click(screen.getByRole("button", { name: /^connect|add|save/i }));
    // haptic success was invoked
    await vi.waitFor(() =>
      expect(services.hapticCalls).toContain("notificationSuccess"),
    );
    // onConnected fires (navigation to Sessions)
    await vi.waitFor(() => expect(onConnected).toHaveBeenCalled());
  });
});

describe("OnboardingScreen — error redaction", () => {
  it("preview error never echoes token or query text", async () => {
    const services = createOnboardingServices({ failPreview: true });
    const { container } = render(<OnboardingScreen services={services} />);
    const input = screen.getByLabelText(/authorization url/i);
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(
      screen.getByRole("button", { name: /connect|preview|next/i }),
    );
    await screen.findByText(/failed|error|unable/i);
    expect(container.textContent).not.toContain(SECRET_TOKEN);
  });

  it("confirm error never echoes token or query text", async () => {
    const services = createOnboardingServices({ failConfirm: true });
    const { container } = render(<OnboardingScreen services={services} />);
    const input = screen.getByLabelText(/authorization url/i);
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTPS } });
    fireEvent.click(
      screen.getByRole("button", { name: /connect|preview|next/i }),
    );
    await screen.findByText(/hub\.example\.com:8443/i);
    fireEvent.change(screen.getByLabelText(/server name/i), {
      target: { value: "my hub" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^connect|add|save/i }));
    await screen.findByText(/failed|error|unable/i);
    expect(container.textContent).not.toContain(SECRET_TOKEN);
  });
});

describe("OnboardingScreen — private-network warning", () => {
  it("shows a private-network warning for an HTTP origin", async () => {
    const services = createOnboardingServices();
    render(<OnboardingScreen services={services} />);
    const input = screen.getByLabelText(/authorization url/i);
    fireEvent.change(input, { target: { value: SAMPLE_AUTH_URL_HTTP } });
    fireEvent.click(
      screen.getByRole("button", { name: /connect|preview|next/i }),
    );
    // Wait for the confirm view to appear with the origin.
    await screen.findByText(/192\.168\.1\.10:8080/i);
    expect(
      await screen.findByText(
        /private network|trusted.*network|http.*not.*secure/i,
      ),
    ).toBeInTheDocument();
  });
});
