import { expect, test, vi } from "vitest";

// An ordinary HTTP origin outside localhost (a hub reached on a LAN or
// tailnet name) is not a secure context, and `crypto.randomUUID` simply does
// not exist there. The shell must still boot and navigate: an unguarded
// `crypto.randomUUID()` at module load takes the whole SPA down before any
// route renders, and one per navigation takes every history step with it.
test("the shell's routing and attention history boot and navigate without crypto.randomUUID", async () => {
  const secure = globalThis.crypto;
  expect(typeof secure.randomUUID).toBe("function");
  vi.stubGlobal("crypto", {
    getRandomValues: (array: Uint8Array<ArrayBuffer>) => secure.getRandomValues(array),
  });
  vi.resetModules();
  try {
    const routing = await import("./routing");
    const attention = await import("./attentionHistory");

    routing.navigate("/s/local%3Ano-secure-context");
    expect(window.location.pathname).toBe("/s/local%3Ano-secure-context");
    expect(routing.routeHistoryEntryId(window.history.state)).toBeTruthy();

    routing.navigate("/s/local%3Asecond-step");
    expect(window.location.pathname).toBe("/s/local%3Asecond-step");
    expect(routing.routeHistoryEntryId(window.history.state)).toBeTruthy();

    // The attention module mints its generation at load; reaching here at all
    // proves that load survived, and an entry with no token still answers.
    expect(attention.currentAttentionEntryToken()).toBeNull();
  } finally {
    vi.unstubAllGlobals();
    vi.resetModules();
  }
});
