import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { MobileSection } from "./mobile";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("renders a pairing QR, copy action, and private-network warning", async () => {
  const authURL = "http://192.168.1.20:9180/auth?token=mobile-secret";
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
  expect(screen.getByText(/private-network HTTP connection/i)).toBeTruthy();
  expect(fetch).toHaveBeenCalledWith("/api/mobile/pairing", { credentials: "same-origin" });
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
