/**
 * Task 7B fix round 1 — preview operation protocol: exact cancellation.
 *
 * Every preview operation (paste/repair/scan) atomically claims the generation
 * and cancels any visible prior preview synchronously before any await. After
 * the service/callback returns a result, if the generation is stale, the
 * returned previewId is best-effort cancelled with the service exactly once
 * before returning — the winning result alone publishes. Stale errors leave
 * the winner. No `inFlightPreviewId` dead variable; no conditional branches
 * in cancel assertions.
 */

import { waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ProfileService } from "../services/nativeProfiles";
import {
  FakeProfileService,
  SAMPLE_AUTH_URL_HTTP,
  SAMPLE_AUTH_URL_HTTPS,
} from "../test/fakeProfileService";
import {
  type ConnectionPreview,
  createConnectionStore,
  type ScanPreviewResult,
} from "./connection";

function storeWith(service: ProfileService) {
  return createConnectionStore(service);
}

function exactPreviewId(preview: ConnectionPreview | null): string {
  expect(preview).not.toBeNull();
  return (preview as ConnectionPreview).previewId;
}

const HTTPS_ORIGIN = "https://hub.example.com:8443";
const HTTP_ORIGIN = "http://192.168.1.10:8080";

// ---------------------------------------------------------------------------
// CRITICAL 1a: new op cancels visible prior — exact ID assertions
// ---------------------------------------------------------------------------

describe("preview protocol — new op cancels visible prior (exact IDs)", () => {
  it("out-of-order paste: second paste cancels first visible ID, publishes second", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const firstId = exactPreviewId(store.getState().preview);
    expect(firstId).toMatch(/^pv-/);

    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    // Exact ID cancelled — no conditional.
    expect(service.cancelPreviewCalls).toContain(firstId);
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
  });

  it("out-of-order repair: repair cancels visible paste ID, publishes repair", async () => {
    const service = new FakeProfileService({
      profiles: [{ id: "p1", name: "laptop", origin: HTTPS_ORIGIN }],
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const firstId = exactPreviewId(store.getState().preview);
    expect(firstId).toMatch(/^pv-/);

    await store.getState().previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTP,
    });
    expect(service.cancelPreviewCalls).toContain(firstId);
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
  });

  it("scan replaces paste: scan cancels visible paste ID and publishes", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const firstId = exactPreviewId(store.getState().preview);
    expect(firstId).toMatch(/^pv-/);

    const scan: ScanPreviewResult = {
      previewId: "scan-1",
      origin: HTTP_ORIGIN,
    };
    await store.getState().previewScan(async () => scan);
    expect(service.cancelPreviewCalls).toContain(firstId);
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
    expect(store.getState().preview?.previewId).toBe("scan-1");
  });
});

// ---------------------------------------------------------------------------
// CRITICAL 1b: stale result best-effort cancelled exactly once
// ---------------------------------------------------------------------------

describe("preview protocol — stale result cancelled with service", () => {
  it("superseded paste: stale result ID cancelled exactly once, winner stays", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    service.gatePreview("previewPaste");
    const firstP = store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    await waitFor(() => expect(service.isPreviewGatePending()).toBe(true));

    // Second paste wins immediately.
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);

    // Stale first paste resolves with a known ID — must be cancelled.
    service.resolvePreviewGate({
      previewId: "stale-1",
      origin: HTTPS_ORIGIN,
    });
    await firstP;
    expect(service.cancelPreviewCalls).toContain("stale-1");
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
    // The stale ID was cancelled exactly once.
    const staleCancels = service.cancelPreviewCalls.filter(
      (id) => id === "stale-1",
    );
    expect(staleCancels.length).toBe(1);
  });

  it("superseded repair: stale result ID cancelled exactly once, winner stays", async () => {
    const service = new FakeProfileService({
      profiles: [{ id: "p1", name: "laptop", origin: HTTPS_ORIGIN }],
      activeProfileId: "p1",
    });
    const store = storeWith(service);

    service.gatePreview("previewRepair");
    const firstP = store.getState().previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTPS,
    });
    await waitFor(() => expect(service.isPreviewGatePending()).toBe(true));

    await store.getState().previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTP,
    });
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);

    service.resolvePreviewGate({
      previewId: "stale-repair",
      origin: HTTPS_ORIGIN,
    });
    await firstP;
    expect(service.cancelPreviewCalls).toContain("stale-repair");
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
    const staleCancels = service.cancelPreviewCalls.filter(
      (id) => id === "stale-repair",
    );
    expect(staleCancels.length).toBe(1);
  });

  it("superseded scan: stale scan result ID cancelled exactly once, winner stays", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    let resolveScan!: (r: ScanPreviewResult) => void;
    const scanPromise = new Promise<ScanPreviewResult>((res) => {
      resolveScan = res;
    });
    const firstP = store.getState().previewScan(async () => scanPromise);

    await store.getState().previewScan(async () => ({
      previewId: "scan-2",
      origin: HTTP_ORIGIN,
    }));
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);

    resolveScan({ previewId: "scan-1", origin: HTTPS_ORIGIN });
    await firstP;
    expect(service.cancelPreviewCalls).toContain("scan-1");
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
    expect(store.getState().preview?.previewId).toBe("scan-2");
    const staleCancels = service.cancelPreviewCalls.filter(
      (id) => id === "scan-1",
    );
    expect(staleCancels.length).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// CRITICAL 1c: cancel-before-result — stale result cancelled exactly once
// ---------------------------------------------------------------------------

describe("preview protocol — cancel-before-result (exact IDs)", () => {
  it("cancel before paste result: late result ID cancelled exactly once, nothing publishes", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    service.gatePreview("previewPaste");
    const firstP = store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    await waitFor(() => expect(service.isPreviewGatePending()).toBe(true));

    // Cancel while the paste is still in flight.
    await store.getState().cancelPreview();
    expect(store.getState().preview).toBeNull();

    // The stale paste resolves with a known ID — must be cancelled.
    service.resolvePreviewGate({
      previewId: "late-1",
      origin: HTTPS_ORIGIN,
    });
    await firstP;
    expect(store.getState().preview).toBeNull();
    expect(service.cancelPreviewCalls).toContain("late-1");
    const cancels = service.cancelPreviewCalls.filter((id) => id === "late-1");
    expect(cancels.length).toBe(1);
  });

  it("cancel before repair result: late result ID cancelled exactly once, nothing publishes", async () => {
    const service = new FakeProfileService({
      profiles: [{ id: "p1", name: "laptop", origin: HTTPS_ORIGIN }],
      activeProfileId: "p1",
    });
    const store = storeWith(service);

    service.gatePreview("previewRepair");
    const firstP = store.getState().previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTPS,
    });
    await waitFor(() => expect(service.isPreviewGatePending()).toBe(true));

    await store.getState().cancelPreview();
    expect(store.getState().preview).toBeNull();

    service.resolvePreviewGate({
      previewId: "late-repair",
      origin: HTTPS_ORIGIN,
    });
    await firstP;
    expect(store.getState().preview).toBeNull();
    expect(service.cancelPreviewCalls).toContain("late-repair");
    const cancels = service.cancelPreviewCalls.filter(
      (id) => id === "late-repair",
    );
    expect(cancels.length).toBe(1);
  });

  it("cancel before scan result: late scan result ID cancelled exactly once, nothing publishes", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    let resolveScan!: (r: ScanPreviewResult) => void;
    const scanPromise = new Promise<ScanPreviewResult>((res) => {
      resolveScan = res;
    });
    const firstP = store.getState().previewScan(async () => scanPromise);

    await store.getState().cancelPreview();
    expect(store.getState().preview).toBeNull();

    resolveScan({ previewId: "late-scan", origin: HTTPS_ORIGIN });
    await firstP;
    expect(store.getState().preview).toBeNull();
    expect(service.cancelPreviewCalls).toContain("late-scan");
    const cancels = service.cancelPreviewCalls.filter(
      (id) => id === "late-scan",
    );
    expect(cancels.length).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// CRITICAL 1d: visible replacement cancels visible ID once, not twice
// ---------------------------------------------------------------------------

describe("preview protocol — visible replacement and duplicate-cancel race", () => {
  it("visible replacement: second paste cancels visible first ID exactly once", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const firstId = exactPreviewId(store.getState().preview);
    expect(firstId).toMatch(/^pv-/);

    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    const cancels = service.cancelPreviewCalls.filter((id) => id === firstId);
    expect(cancels.length).toBe(1);
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
  });

  it("duplicate-cancel race: cancelPreview then replacement cancels visible ID once not twice", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const firstId = exactPreviewId(store.getState().preview);
    expect(firstId).toMatch(/^pv-/);

    // Cancel clears the visible preview.
    await store.getState().cancelPreview();
    expect(store.getState().preview).toBeNull();

    // A new paste starts — the prior ID should not be cancelled again
    // (it was already cancelled and the visible state is null).
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    const cancels = service.cancelPreviewCalls.filter((id) => id === firstId);
    expect(cancels.length).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// CRITICAL 1e: stale errors leave winner
// ---------------------------------------------------------------------------

describe("preview protocol — stale errors leave winner", () => {
  it("a stale paste error does not clear a newer visible preview", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    service.gatePreview("previewPaste");
    const firstP = store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    await waitFor(() => expect(service.isPreviewGatePending()).toBe(true));

    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);

    service.rejectPreviewGate(new Error("stale failure"));
    await firstP.catch(() => {});
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
    expect(store.getState().previewError).toBeNull();
  });

  it("a stale scan error does not clear a newer visible preview", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    let rejectScan!: (e: unknown) => void;
    const scanPromise = new Promise<ScanPreviewResult>((_res, rej) => {
      rejectScan = rej;
    });
    const firstP = store.getState().previewScan(async () => scanPromise);

    await store.getState().previewScan(async () => ({
      previewId: "scan-win",
      origin: HTTP_ORIGIN,
    }));
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);

    rejectScan(new Error("stale scan failure"));
    await firstP.catch(() => {});
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
    expect(store.getState().previewError).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// IMPORTANT 1: cancelPreview clears UI synchronously even if service blocks
// ---------------------------------------------------------------------------

describe("preview protocol — cancelPreview clears UI synchronously", () => {
  it("cancelPreview clears preview before the service cancel promise resolves", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    expect(store.getState().preview).not.toBeNull();

    // cancelPreview clears the visible state synchronously.
    const cancelP = store.getState().cancelPreview();
    // The preview is null before the service cancel resolves.
    expect(store.getState().preview).toBeNull();
    await cancelP;
  });

  it("superseding during blocked visible cancellation never invokes obsolete paste", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const visibleId = exactPreviewId(store.getState().preview);
    expect(visibleId).toMatch(/^pv-/);

    service.gateCancelPreview();
    const obsolete = store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    expect(store.getState().preview).toBeNull();
    await waitFor(() =>
      expect(service.isCancelPreviewGatePending()).toBe(true),
    );

    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    expect(service.previewPasteCallCount).toBe(2);
    service.resolveCancelPreviewGate();
    await obsolete;

    expect(service.previewPasteCallCount).toBe(2);
    expect(service.cancelPreviewCalls).toEqual([visibleId]);
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
  });

  it("cancel generation during blocked visible cancellation never invokes obsolete paste", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const visibleId = exactPreviewId(store.getState().preview);
    expect(visibleId).toMatch(/^pv-/);

    service.gateCancelPreview();
    const obsolete = store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    expect(store.getState().preview).toBeNull();
    await waitFor(() =>
      expect(service.isCancelPreviewGatePending()).toBe(true),
    );
    await store.getState().cancelPreview();
    service.resolveCancelPreviewGate();
    await obsolete;

    expect(service.previewPasteCallCount).toBe(1);
    expect(service.cancelPreviewCalls).toEqual([visibleId]);
    expect(store.getState().preview).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// IMPORTANT 3: confirmPairing/rePair clear preview without cancel on consumed ID
// ---------------------------------------------------------------------------

describe("preview protocol — confirm consumes preview without cancelling", () => {
  it("confirmPairing clears preview + error without calling cancel on consumed ID", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const previewId = exactPreviewId(store.getState().preview);
    expect(previewId).toMatch(/^pv-/);

    await store.getState().confirmPairing(previewId, "my hub", false);
    await store.getState().cancelPreview();

    // Preview is cleared.
    expect(store.getState().preview).toBeNull();
    expect(store.getState().previewError).toBeNull();
    // The consumed preview ID was NOT cancelled with the service.
    expect(service.cancelPreviewCalls).not.toContain(previewId);
  });

  it("rePair clears preview + error without calling cancel on consumed ID", async () => {
    const service = new FakeProfileService({
      profiles: [{ id: "p1", name: "laptop", origin: HTTPS_ORIGIN }],
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    await store.getState().previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTPS,
    });
    const previewId = exactPreviewId(store.getState().preview);
    expect(previewId).toMatch(/^pv-/);

    await store.getState().rePair("p1", previewId, "laptop", false);
    await store.getState().cancelPreview();

    expect(store.getState().preview).toBeNull();
    expect(store.getState().previewError).toBeNull();
    expect(service.cancelPreviewCalls).not.toContain(previewId);
  });

  it("an intentional no-result scan publishes and cancels nothing", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    await store.getState().previewScan(async () => null);

    expect(store.getState().preview).toBeNull();
    expect(store.getState().previewError).toBeNull();
    expect(service.cancelPreviewCalls).toEqual([]);
  });

  it("confirmPairing failure preserves preview for retry", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    service.failOnce("confirmPairing");
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const previewId = exactPreviewId(store.getState().preview);
    expect(previewId).toMatch(/^pv-/);

    await store
      .getState()
      .confirmPairing(previewId, "my hub", false)
      .catch(() => {});

    // Preview preserved for retry.
    expect(store.getState().preview?.previewId).toBe(previewId);
  });
});
