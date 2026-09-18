import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { describe, expect, test, vi } from "vitest";
import { createReadyGenerationCallback } from "./readyGenerationCallback";

describe("readyGenerationCallback", () => {
  test("runs while its client is still current", () => {
    const readyGenerationCallback = createReadyGenerationCallback();
    const client = new FakeClient("connecting");
    const begin = vi.fn();
    const callback = readyGenerationCallback(client, () => client, begin);

    callback();

    expect(begin).toHaveBeenCalledTimes(1);
  });

  test("a registration for a client that is no longer current is a no-op", () => {
    const readyGenerationCallback = createReadyGenerationCallback();
    const client = new FakeClient("connecting");
    const other = new FakeClient("connecting");
    const begin = vi.fn();
    const callback = readyGenerationCallback(client, () => other, begin);

    callback();

    expect(begin).not.toHaveBeenCalled();
  });

  test("a later registration for the same client object supersedes an earlier one", () => {
    const readyGenerationCallback = createReadyGenerationCallback();
    const client = new FakeClient("connecting");
    const wired = client;
    const currentClient = () => wired;
    const firstBegin = vi.fn();
    const firstCallback = readyGenerationCallback(client, currentClient, firstBegin);
    const secondBegin = vi.fn();
    const secondCallback = readyGenerationCallback(client, currentClient, secondBegin);

    firstCallback();
    expect(firstBegin).not.toHaveBeenCalled();

    secondCallback();
    expect(secondBegin).toHaveBeenCalledTimes(1);
  });

  test("a stale registration from before a rewire away and back cannot fire once the same client is current again", () => {
    // One store's own guard: a rewire away and back is that store's own
    // history, distinct from two DIFFERENT stores sharing one client (below).
    const readyGenerationCallback = createReadyGenerationCallback();
    const a = new FakeClient("connecting");
    const b = new FakeClient("connecting");
    let wired: FakeClient = a;
    const currentClient = () => wired;
    const firstBegin = vi.fn();
    const firstCallback = readyGenerationCallback(a, currentClient, firstBegin);

    // Rewired to `b`, then rewired back to the SAME `a` instance - a fresh
    // registration is minted for it, superseding the first one.
    wired = b;
    readyGenerationCallback(b, currentClient, vi.fn());
    wired = a;
    const secondBegin = vi.fn();
    const secondCallback = readyGenerationCallback(a, currentClient, secondBegin);

    // The stale FIRST registration's callback firing late must not begin
    // anything: identity alone (`a === currentClient()`) would pass, but this
    // registration was superseded by the second one for the same object.
    firstCallback();
    expect(firstBegin).not.toHaveBeenCalled();

    secondCallback();
    expect(secondBegin).toHaveBeenCalledTimes(1);
  });

  test("two stores' own guards do not contend over one shared client", () => {
    // keybindings.ts and transcriptDisplay.ts both wire the SAME
    // connectionStore client, each through its own
    // createReadyGenerationCallback() instance (module-scoped, created once).
    // A registration in one store's guard must never supersede a
    // registration in the OTHER store's guard for the same client object.
    const keybindingsGuard = createReadyGenerationCallback();
    const transcriptDisplayGuard = createReadyGenerationCallback();
    const client = new FakeClient("connecting");
    const currentClient = () => client;
    const keybindingsBegin = vi.fn();
    const transcriptDisplayBegin = vi.fn();

    const keybindingsCallback = keybindingsGuard(client, currentClient, keybindingsBegin);
    const transcriptDisplayCallback = transcriptDisplayGuard(client, currentClient, transcriptDisplayBegin);

    keybindingsCallback();
    transcriptDisplayCallback();

    expect(keybindingsBegin).toHaveBeenCalledTimes(1);
    expect(transcriptDisplayBegin).toHaveBeenCalledTimes(1);
  });
});
