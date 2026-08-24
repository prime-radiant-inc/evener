import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { MobileSection } from "./mobile";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("renders HTTP observation and reusable-capability warnings for a mixed-case scheme", async () => {
  const authURL = "HTTP://192.168.1.20:9180/auth?token=mobile-secret";
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ auth_url: authURL }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );

  render(<MobileSection />);

  expect(await screen.findByRole("img", { name: "Mobile app pairing QR code" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Copy pairing link" })).toBeTruthy();
  expect(screen.getByText(/anyone who can observe this network can observe the pairing capability/i)).toBeTruthy();
  expect(screen.getByText(/this link remains valid and can be reused/i)).toBeTruthy();
  expect(fetch).toHaveBeenCalledWith("/api/mobile/pairing", { credentials: "same-origin" });
});

test("renders the reusable-capability warning for HTTPS without the HTTP observation warning", async () => {
  const authURL = "https://hub.example.test/auth?token=mobile-secret";
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ auth_url: authURL }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );

  render(<MobileSection />);

  expect(await screen.findByRole("img", { name: "Mobile app pairing QR code" })).toBeTruthy();
  expect(screen.getByText(/this link remains valid and can be reused/i)).toBeTruthy();
  expect(screen.queryByText(/anyone who can observe this network/i)).toBeNull();
});

test("shows configuration guidance instead of a QR when the Hub has no reachable origin", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "mobile pairing requires a reachable non-loopback Hub origin" }), {
        status: 409,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );

  render(<MobileSection />);

  expect(await screen.findByText("Mobile pairing needs a reachable Hub origin")).toBeTruthy();
  expect(screen.getByText(/configure mobile_base_url/i)).toBeTruthy();
  expect(screen.queryByRole("img", { name: "Mobile app pairing QR code" })).toBeNull();
});
