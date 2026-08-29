import { describe, expect, it, vi } from "vitest";
import type { NativeTransport } from "./client";
import { createNativeBridge } from "./client";

function nativeTransport(openExternalUrl = vi.fn(async () => {})) {
  const transport = {
    send: vi.fn(),
    subscribe: vi.fn(() => () => {}),
    openExternalUrl,
  } as unknown as NativeTransport;
  return { transport, openExternalUrl };
}

describe("NativeBridge external URL policy", () => {
  it.each([
    [
      "HTTP://EXAMPLE.com:80/a/../docs?q=1#part",
      "http://example.com/docs?q=1#part",
    ],
    ["https://EXAMPLE.com:443/docs", "https://example.com/docs"],
  ])("canonicalizes and forwards allowed URL %s", async (raw, canonical) => {
    const { transport, openExternalUrl } = nativeTransport();
    await createNativeBridge(transport).openExternalUrl(raw);
    expect(openExternalUrl).toHaveBeenCalledWith(canonical);
  });

  it.each([
    "not a url",
    "/relative",
    "mailto:user@example.com",
    "tel:+15551234567",
    "custom://host/path",
    "file:///private/path",
    "javascript:alert(1)",
    "data:text/plain,secret",
  ])("rejects disallowed URL %s before calling the plugin", async (raw) => {
    const { transport, openExternalUrl } = nativeTransport();
    await expect(
      createNativeBridge(transport).openExternalUrl(raw),
    ).rejects.toThrow(/^External link unavailable$/);
    expect(openExternalUrl).not.toHaveBeenCalled();
  });

  it("replaces plugin errors without URL or cause disclosure", async () => {
    const secretUrl = "https://example.com/private?token=secret-value";
    const pluginFailure = new Error(`OS rejected ${secretUrl}`);
    const { transport } = nativeTransport(
      vi.fn(async () => {
        throw pluginFailure;
      }),
    );
    let failure: unknown;
    try {
      await createNativeBridge(transport).openExternalUrl(secretUrl);
    } catch (error) {
      failure = error;
    }
    expect(failure).toBeInstanceOf(Error);
    expect((failure as Error).message).toBe("External link unavailable");
    expect((failure as Error).message).not.toContain(secretUrl);
    expect((failure as Error).message).not.toContain("secret-value");
    expect((failure as Error).cause).toBeUndefined();
  });
});
