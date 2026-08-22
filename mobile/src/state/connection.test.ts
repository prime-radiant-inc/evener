import { describe, expect, it } from "vitest";
import type {
  ProfileRedacted,
  ProfileService,
} from "../services/nativeProfiles";
import {
  FakeProfileService,
  parseOrigin,
  SAMPLE_AUTH_URL_HTTP,
  SAMPLE_AUTH_URL_HTTPS,
  SECRET_TOKEN,
} from "../test/fakeProfileService";
import { createConnectionStore } from "./connection";

const PROFILES: readonly ProfileRedacted[] = [
  { id: "p1", name: "laptop", origin: "https://hub.example.com" },
  { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
];

function storeWith(service: ProfileService) {
  return createConnectionStore(service);
}

// ---------------------------------------------------------------------------
// refresh + status
// ---------------------------------------------------------------------------

describe("connection store — refresh", () => {
  it("loads health snapshot into profiles/active/generation", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
      generation: 3,
    });
    const store = storeWith(service);
    await store.getState().refresh();
    const s = store.getState();
    expect(s.profiles).toEqual([
      { id: "p1", name: "laptop", origin: "https://hub.example.com" },
      { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
    ]);
    expect(s.activeProfileId).toBe("p1");
    expect(s.generation).toBe(3);
    expect(s.status).toBe("ready");
  });
});

describe("connection store — status transitions", () => {
  it("transitions idle -> loading -> ready on refresh", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    expect(store.getState().status).toBe("initial");
    const p = store.getState().refresh();
    expect(store.getState().status).toBe("loading");
    await p;
    expect(store.getState().status).toBe("ready");
  });

  it("transitions to error on refresh failure", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    service.failOnce("health");
    const store = storeWith(service);
    await store.getState().refresh();
    expect(store.getState().status).toBe("error");
  });
});

// ---------------------------------------------------------------------------
// confirmPairing (add)
// ---------------------------------------------------------------------------

describe("connection store — confirmPairing (add)", () => {
  it("activates the new profile and refreshes the list", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    const preview = await service.previewPaste({ raw: SAMPLE_AUTH_URL_HTTPS });
    const profile = await store
      .getState()
      .confirmPairing(preview.previewId, "my hub", false);
    expect(profile.name).toBe("my hub");
    const s = store.getState();
    expect(s.activeProfileId).toBe(profile.id);
    expect(s.profiles.length).toBe(1);
    expect(s.generation).toBeGreaterThan(0);
  });

  it("does not store the raw auth URL or token in store state", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    const preview = await service.previewPaste({ raw: SAMPLE_AUTH_URL_HTTPS });
    await store.getState().confirmPairing(preview.previewId, "my hub", false);
    const serialized = JSON.stringify(store.getState());
    expect(serialized).not.toContain(SECRET_TOKEN);
    expect(serialized).not.toContain("token=");
  });
});

// ---------------------------------------------------------------------------
// name uniqueness and duplicate origin
// ---------------------------------------------------------------------------

describe("connection store — name uniqueness and duplicate origin", () => {
  it("rejects a duplicate name (case-insensitive)", async () => {
    const service = new FakeProfileService({
      profiles: [
        { id: "p1", name: "Laptop", origin: "https://hub.example.com" },
      ],
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    const preview = await service.previewPaste({ raw: SAMPLE_AUTH_URL_HTTPS });
    await expect(
      store.getState().confirmPairing(preview.previewId, "laptop", false),
    ).rejects.toThrow(/name|unique/i);
  });

  it("rejects a duplicate origin without explicit consent", async () => {
    const service = new FakeProfileService({
      profiles: [
        { id: "p1", name: "first", origin: "https://hub.example.com:8443" },
      ],
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    // Use the store's previewPaste so the store knows the origin to validate.
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const previewId = store.getState().preview?.previewId;
    expect(previewId).toBeDefined();
    await expect(
      store.getState().confirmPairing(previewId ?? "", "second", false),
    ).rejects.toThrow(/origin|duplicate|confirm/i);
  });

  it("allows a duplicate origin with explicit consent", async () => {
    const service = new FakeProfileService({
      profiles: [
        { id: "p1", name: "first", origin: "https://hub.example.com:8443" },
      ],
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const previewId = store.getState().preview?.previewId;
    expect(previewId).toBeDefined();
    const profile = await store
      .getState()
      .confirmPairing(previewId ?? "", "second", true);
    expect(profile.name).toBe("second");
    expect(store.getState().profiles.length).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// rename
// ---------------------------------------------------------------------------

describe("connection store — rename", () => {
  it("updates the profile name in the list", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    await store.getState().rename("p1", "workstation");
    expect(store.getState().profiles[0]?.name).toBe("workstation");
  });

  it("rejects a duplicate rename target name", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    await expect(store.getState().rename("p1", "server")).rejects.toThrow(
      /name|unique/i,
    );
  });

  it("rename failure leaves the profile unchanged", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    service.failOnce("rename");
    const store = storeWith(service);
    await expect(store.getState().rename("p1", "newname")).rejects.toThrow();
    expect(store.getState().profiles.find((p) => p.id === "p1")?.name).toBe(
      "laptop",
    );
  });
});

// ---------------------------------------------------------------------------
// remove
// ---------------------------------------------------------------------------

describe("connection store — remove", () => {
  it("removes a non-active profile without changing active", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    await store.getState().remove("p2");
    expect(store.getState().profiles.map((p) => p.id)).toEqual(["p1"]);
    expect(store.getState().activeProfileId).toBe("p1");
  });

  it("removing the active profile falls back to the next saved profile", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    await store.getState().remove("p1");
    expect(store.getState().activeProfileId).toBe("p2");
    expect(store.getState().profiles.map((p) => p.id)).toEqual(["p2"]);
  });

  it("removing the last profile returns to onboarding (activeProfileId null)", async () => {
    const service = new FakeProfileService({
      profiles: [{ id: "p1", name: "only", origin: "https://hub.example.com" }],
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    await store.getState().remove("p1");
    expect(store.getState().activeProfileId).toBeNull();
    expect(store.getState().profiles).toEqual([]);
  });

  it("remove failure leaves the profile usable", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    service.failOnce("remove");
    const store = storeWith(service);
    await expect(store.getState().remove("p2")).rejects.toThrow();
    expect(
      store
        .getState()
        .profiles.map((p) => p.id)
        .sort(),
    ).toEqual(["p1", "p2"]);
    expect(store.getState().activeProfileId).toBe("p1");
  });
});

// ---------------------------------------------------------------------------
// switch
// ---------------------------------------------------------------------------

describe("connection store — switch", () => {
  it("switches active profile and increments generation", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    const genBefore = store.getState().generation;
    await store.getState().switchTo("p2");
    expect(store.getState().activeProfileId).toBe("p2");
    expect(store.getState().generation).toBeGreaterThan(genBefore);
  });

  it("switching clears server-scoped placeholder state", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    store.getState().__seedServerScopedState({ thread: "stale-data" });
    await store.getState().switchTo("p2");
    expect(store.getState().__serverScopedState).toBeNull();
  });

  it("select failure leaves active unchanged", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    service.failOnce("select");
    const store = storeWith(service);
    await expect(store.getState().switchTo("p2")).rejects.toThrow();
    expect(store.getState().activeProfileId).toBe("p1");
  });
});

// ---------------------------------------------------------------------------
// re-pair
// ---------------------------------------------------------------------------

describe("connection store — re-pair", () => {
  it("re-pair success replaces credentials and keeps the profile id", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    const preview = await service.previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTPS,
    });
    await store.getState().rePair("p1", preview.previewId, "laptop", false);
    expect(store.getState().profiles.find((p) => p.id === "p1")?.name).toBe(
      "laptop",
    );
  });

  it("re-pair failure leaves the old profile usable and active unchanged", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    service.failOnce("confirmPairing");
    const store = storeWith(service);
    const preview = await service.previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTPS,
    });
    await expect(
      store.getState().rePair("p1", preview.previewId, "laptop", false),
    ).rejects.toThrow();
    expect(store.getState().activeProfileId).toBe("p1");
    expect(store.getState().profiles.find((p) => p.id === "p1")?.name).toBe(
      "laptop",
    );
  });

  it("re-pair failure does not change active profile or name", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    service.failOnce("confirmPairing");
    const store = storeWith(service);
    const preview = await service.previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTPS,
    });
    await expect(
      store.getState().rePair("p1", preview.previewId, "newname", false),
    ).rejects.toThrow();
    expect(store.getState().activeProfileId).toBe("p1");
    expect(store.getState().profiles.find((p) => p.id === "p1")?.name).toBe(
      "laptop",
    );
  });
});

// ---------------------------------------------------------------------------
// preview and redaction
// ---------------------------------------------------------------------------

describe("connection store — preview and redaction", () => {
  it("previewPaste sets preview with origin only, no token", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const preview = store.getState().preview;
    expect(preview).not.toBeNull();
    expect(JSON.stringify(preview)).not.toContain(SECRET_TOKEN);
  });

  it("previewPaste returns origin only; raw url never appears in store snapshot", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const snapshot = JSON.stringify(store.getState());
    expect(snapshot).not.toContain(SECRET_TOKEN);
    expect(snapshot).not.toContain("token=");
    expect(store.getState().preview?.origin).toBe(
      "https://hub.example.com:8443",
    );
  });

  it("preview error sets a redacted message without the token/query", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    service.failOnce("previewPaste");
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const serialized = JSON.stringify(store.getState());
    expect(serialized).not.toContain(SECRET_TOKEN);
    expect(store.getState().previewError).not.toBeNull();
  });

  it("previewPaste error message redacts token and query", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    service.failOnce("previewPaste");
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const snapshot = JSON.stringify(store.getState());
    expect(snapshot).not.toContain(SECRET_TOKEN);
    expect(store.getState().previewError).toBeTruthy();
  });

  it("previewRepair error message redacts token and query", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    service.failOnce("previewRepair");
    const store = storeWith(service);
    await store.getState().previewRepair({
      profileId: "p1",
      raw: SAMPLE_AUTH_URL_HTTPS,
    });
    const snapshot = JSON.stringify(store.getState());
    expect(snapshot).not.toContain(SECRET_TOKEN);
  });

  it("confirmPairing error does not echo token or query", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    service.failOnce("confirmPairing");
    const store = storeWith(service);
    const preview = await service.previewPaste({ raw: SAMPLE_AUTH_URL_HTTPS });
    await expect(
      store.getState().confirmPairing(preview.previewId, "hub", false),
    ).rejects.toThrow();
    const snapshot = JSON.stringify(store.getState());
    expect(snapshot).not.toContain(SECRET_TOKEN);
  });

  it("HTTP origin is flagged as private-network", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTP);
    const preview = store.getState().preview;
    expect(preview?.origin).toBe("http://192.168.1.10:8080");
    expect(preview?.isPrivateNetwork).toBe(true);
  });

  it("HTTPS origin is not flagged private-network", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    expect(store.getState().preview?.isPrivateNetwork).toBe(false);
  });

  it("cancelPreview clears the transient preview", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    const previewId = store.getState().preview?.previewId;
    expect(previewId).toBeDefined();
    await store.getState().cancelPreview();
    expect(store.getState().preview).toBeNull();
  });

  it("previewPaste accepts a PreviewInput and clears prior error", async () => {
    const service = new FakeProfileService({
      profiles: [],
      activeProfileId: null,
    });
    service.failOnce("previewPaste");
    const store = storeWith(service);
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    expect(store.getState().previewError).not.toBeNull();
    await store.getState().previewPaste(SAMPLE_AUTH_URL_HTTPS);
    expect(store.getState().previewError).toBeNull();
  });

  it("parseOrigin never returns the token or query", () => {
    expect(parseOrigin(SAMPLE_AUTH_URL_HTTPS)).toBe(
      "https://hub.example.com:8443",
    );
    expect(parseOrigin(SAMPLE_AUTH_URL_HTTPS)).not.toContain("token");
  });
});

// ---------------------------------------------------------------------------
// reachability
// ---------------------------------------------------------------------------

describe("connection store — reachability", () => {
  it("tracks reachability per profile", async () => {
    const service = new FakeProfileService({
      profiles: PROFILES,
      activeProfileId: "p1",
    });
    const store = storeWith(service);
    store.getState().setReachability("p1", "reachable");
    store.getState().setReachability("p2", "unreachable");
    expect(store.getState().reachability.p1).toBe("reachable");
    expect(store.getState().reachability.p2).toBe("unreachable");
  });
});
