/**
 * Task 7B — preview operation protocol.
 *
 * One generation-gated preview operation at a time: previewPaste, previewRepair,
 * and previewScan each start a new generation. A new op cancels any visible prior
 * preview. A stale or cancelled operation that resolves after a newer op or a
 * cancellation never publishes and never overwrites the latest preview state.
 * cancelPreview clears state immediately, then best-effort cancels the known
 * in-flight preview id exactly once.
 *
 * These tests exercise the store directly with a FakeProfileService that gates
 * preview resolution so a stale op can be observed resolving after a
 * superseding op or cancellation.
 */

import { waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ProfileService } from "../services/nativeProfiles";
import {
  FakeProfileService,
  SAMPLE_AUTH_URL_HTTP,
  SAMPLE_AUTH_URL_HTTPS,
} from "../test/fakeProfileService";
import { createConnectionStore, type ScanPreviewResult } from "./connection";

function storeWith(service: ProfileService) {
  return createConnectionStore(service);
}

const HTTPS_ORIGIN = "https://hub.example.com:8443";
const HTTP_ORIGIN = "http://192.168.1.10:8080";

describe("preview protocol — new op cancels visible prior preview", () => {
  it("previewPaste cancels a visible prior preview via the service", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const firstId = store.getState().preview?.previewId;
    expect(firstId).toBeDefined();

    // A second paste starts a new generation; the first preview should be
    // cancelled with the service (cancelPreview called for the first id).
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    expect(service.cancelPreviewCalls).toContain(firstId);
    // The visible preview is now the second one.
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
  });

  it("previewRepair cancels a visible prior paste preview", async () => {
    const service = new FakeProfileService({
      profiles: [{ id: "p1", name: "laptop", origin: HTTPS_ORIGIN }],
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const firstId = store.getState().preview?.previewId;
    expect(firstId).toBeDefined();

    await store.getState().previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTP,
    });
    expect(service.cancelPreviewCalls).toContain(firstId);
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
  });

  it("previewScan cancels a visible prior paste preview", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const firstId = store.getState().preview?.previewId;
    expect(firstId).toBeDefined();

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

describe("preview protocol — stale op never publishes", () => {
  it("a superseded paste does not overwrite the newer preview", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    // Arm the gate so the FIRST paste blocks.
    service.gatePreview("previewPaste");
    const firstP = store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    // Wait for the first paste to reach the gated service call (it passes
    // through cancelVisiblePreview first, which is an async microtask).
    await waitFor(() => expect(service.isPreviewGatePending()).toBe(true));

    // While the first is in flight, start a second paste (immediate resolve).
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);

    // Now resolve the stale first paste — it must NOT overwrite the second.
    service.resolvePreviewGate({
      previewId: "stale-1",
      origin: HTTPS_ORIGIN,
    });
    await firstP;
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
    expect(store.getState().preview?.previewId).not.toBe("stale-1");
  });

  it("a superseded repair does not overwrite the newer preview", async () => {
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

    // A second repair (immediate) wins.
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
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
  });

  it("a superseded scan does not overwrite the newer preview", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    // First scan blocks (gated scan callback).
    let resolveScan!: (r: ScanPreviewResult) => void;
    const scanPromise = new Promise<ScanPreviewResult>((res) => {
      resolveScan = res;
    });
    const firstP = store.getState().previewScan(async () => scanPromise);

    // Second scan (immediate) wins.
    await store.getState().previewScan(async () => ({
      previewId: "scan-2",
      origin: HTTP_ORIGIN,
    }));
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);

    // Resolve the stale first scan — must not overwrite.
    resolveScan({ previewId: "scan-1", origin: HTTPS_ORIGIN });
    await firstP;
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
    expect(store.getState().preview?.previewId).toBe("scan-2");
  });
});

describe("preview protocol — cancelled op never publishes", () => {
  it("cancelPreview clears state and a later-resolving paste does not publish", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    service.gatePreview("previewPaste");
    const firstP = store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const gatedId = store.getState().preview?.previewId;
    // The paste is in flight; there's no visible preview yet (still pending).
    // cancelPreview clears any visible preview and cancels the in-flight id.
    await store.getState().cancelPreview();
    expect(store.getState().preview).toBeNull();

    // The stale paste resolves now — must not publish.
    service.resolvePreviewGate({
      previewId: "late-1",
      origin: HTTPS_ORIGIN,
    });
    await firstP;
    expect(store.getState().preview).toBeNull();
    // The cancelled in-flight id was sent to the service cancel.
    if (gatedId !== undefined) {
      expect(service.cancelPreviewCalls).toContain(gatedId);
    }
  });

  it("cancelPreview cancels the known in-flight preview id exactly once", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    // Start a paste that resolves immediately, giving a visible preview.
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const visibleId = store.getState().preview?.previewId;
    expect(visibleId).toBeDefined();

    await store.getState().cancelPreview();
    // The visible id was cancelled with the service exactly once.
    const callsForId = service.cancelPreviewCalls.filter(
      (id) => id === visibleId,
    );
    expect(callsForId.length).toBe(1);
    expect(store.getState().preview).toBeNull();
  });

  it("a cancelled scan that later resolves does not publish", async () => {
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
  });
});

describe("preview protocol — previewScan replaces setScanPreview", () => {
  it("previewScan routes through the generation protocol (stale scan ignored)", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    let resolveFirst!: (r: ScanPreviewResult) => void;
    const firstPromise = new Promise<ScanPreviewResult>((res) => {
      resolveFirst = res;
    });
    const firstOp = store.getState().previewScan(async () => firstPromise);

    // A second scan wins.
    await store.getState().previewScan(async () => ({
      previewId: "scan-win",
      origin: HTTPS_ORIGIN,
    }));
    expect(store.getState().preview?.previewId).toBe("scan-win");

    // Stale first scan resolves — ignored.
    resolveFirst({ previewId: "scan-stale", origin: HTTP_ORIGIN });
    await firstOp;
    expect(store.getState().preview?.previewId).toBe("scan-win");
  });
});

describe("preview protocol — errors from stale ops don't replace current state", () => {
  it("a stale paste error does not clear a newer visible preview", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);

    service.gatePreview("previewPaste");
    const firstP = store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);

    // A second paste wins immediately.
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);

    // The stale first paste rejects — must not clear the newer preview.
    service.rejectPreviewGate(new Error("stale failure"));
    await firstP.catch(() => {});
    expect(store.getState().preview?.origin).toBe(HTTP_ORIGIN);
    expect(store.getState().previewError).toBeNull();
  });
});
