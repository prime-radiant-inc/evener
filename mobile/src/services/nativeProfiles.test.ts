import { describe, expect, it } from "vitest";
import {
  type ConfirmPairingInput,
  createProfileService,
  type HealthSnapshot,
  isProfileServiceError,
  type PreviewRepairInput,
  type ProfileServiceError,
  type RenameInput,
} from "./nativeProfiles";
import type { TauriBridge } from "./tauri";

// ---------------------------------------------------------------------------
// Fake TauriBridge — records invoke commands/args and returns scripted results
// ---------------------------------------------------------------------------

interface ScriptedInvoke {
  readonly cmd: string;
  readonly result: unknown;
  readonly error?: string;
}

function fakeBridge(scripts: ScriptedInvoke[]): TauriBridge & {
  readonly invocations: { cmd: string; args: Record<string, unknown> }[];
} {
  const invocations: { cmd: string; args: Record<string, unknown> }[] = [];
  const queue = [...scripts];
  const bridge: TauriBridge = {
    async invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
      invocations.push({ cmd, args: args ?? {} });
      const next = queue.shift();
      if (next && next.cmd !== cmd) {
        throw new Error(`unexpected command: wanted ${next.cmd} got ${cmd}`);
      }
      if (next?.error) {
        throw new Error(next.error);
      }
      return next?.result as T;
    },
    createChannel<T>(_onMessage: (response: T) => void) {
      return { id: 0, onmessage: _onMessage } as never;
    },
    listen(): Promise<() => void> {
      return Promise.resolve(() => {});
    },
  };
  return Object.assign(bridge, { invocations });
}

describe("nativeProfiles — list", () => {
  it("decodes a redacted profile list with exactly {id,name,origin}", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_list",
        result: [
          { id: "p1", name: "laptop", origin: "https://hub.example.com" },
        ],
      },
    ]);
    const svc = createProfileService(bridge);
    const profiles = await svc.list();
    expect(profiles).toEqual([
      { id: "p1", name: "laptop", origin: "https://hub.example.com" },
    ]);
  });

  it("rejects a profile summary carrying an extra capability field", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_list",
        result: [
          {
            id: "p1",
            name: "laptop",
            origin: "https://hub.example.com",
            capability: "secret-token",
          },
        ],
      },
    ]);
    const svc = createProfileService(bridge);
    await expect(svc.list()).rejects.toThrow(/capability|field/i);
  });

  it("rejects a profile summary carrying an extra token field", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_list",
        result: [
          {
            id: "p1",
            name: "laptop",
            origin: "https://hub.example.com",
            token: "secret",
          },
        ],
      },
    ]);
    const svc = createProfileService(bridge);
    await expect(svc.list()).rejects.toThrow(/token|field/i);
  });

  it("rejects a profile summary with a non-string origin", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_list",
        result: [{ id: "p1", name: "laptop", origin: 42 }],
      },
    ]);
    const svc = createProfileService(bridge);
    await expect(svc.list()).rejects.toThrow(/origin|string/i);
  });
});

describe("nativeProfiles — preview paste", () => {
  it("invokes profile_preview_paste with camelCase {raw} and decodes {previewId,origin}", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_preview_paste",
        result: { previewId: "pv1", origin: "https://hub.example.com" },
      },
    ]);
    const svc = createProfileService(bridge);
    const preview = await svc.previewPaste({ raw: "evener://pair/abc" });
    expect(preview).toEqual({
      previewId: "pv1",
      origin: "https://hub.example.com",
    });
    expect(bridge.invocations[0]).toEqual({
      cmd: "profile_preview_paste",
      args: { request: { raw: "evener://pair/abc" } },
    });
  });

  it("rejects a preview response with an extra rawQr field", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_preview_paste",
        result: {
          previewId: "pv1",
          origin: "https://hub.example.com",
          rawQr: "secret-qr-text",
        },
      },
    ]);
    const svc = createProfileService(bridge);
    await expect(svc.previewPaste({ raw: "x" })).rejects.toThrow(
      /rawQr|field/i,
    );
  });

  it("surfaces a structured error with no secret when invoke rejects", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_preview_paste",
        result: null,
        error: "invalid pairing text",
      },
    ]);
    const svc = createProfileService(bridge);
    const err = await svc.previewPaste({ raw: "garbage" }).catch((e) => e);
    expect(isProfileServiceError(err)).toBe(true);
    const perr = err as ProfileServiceError;
    expect(perr.code).toBe("preview_failed");
    expect(perr.message).not.toMatch(/garbage/);
  });
});

describe("nativeProfiles — preview repair (re-pair)", () => {
  it("invokes profile_preview_repair with camelCase {profileId,raw}", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_preview_repair",
        result: { previewId: "pv2", origin: "https://hub.example.com" },
      },
    ]);
    const svc = createProfileService(bridge);
    const input: PreviewRepairInput = {
      profileId: "p1",
      raw: "evener://pair/def",
    };
    const preview = await svc.previewRepair(input);
    expect(preview).toEqual({
      previewId: "pv2",
      origin: "https://hub.example.com",
    });
    expect(bridge.invocations[0]).toEqual({
      cmd: "profile_preview_repair",
      args: { request: { profileId: "p1", raw: "evener://pair/def" } },
    });
  });
});

describe("nativeProfiles — confirm pairing (add / re-pair)", () => {
  it("invokes profile_confirm_pairing with camelCase {previewId,name,allowDuplicateOrigin}", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_confirm_pairing",
        result: { id: "p1", name: "laptop", origin: "https://hub.example.com" },
      },
    ]);
    const svc = createProfileService(bridge);
    const input: ConfirmPairingInput = {
      previewId: "pv1",
      name: "laptop",
      allowDuplicateOrigin: true,
    };
    const profile = await svc.confirmPairing(input);
    expect(profile).toEqual({
      id: "p1",
      name: "laptop",
      origin: "https://hub.example.com",
    });
    expect(bridge.invocations[0]).toEqual({
      cmd: "profile_confirm_pairing",
      args: {
        request: {
          previewId: "pv1",
          name: "laptop",
          allowDuplicateOrigin: true,
        },
      },
    });
  });

  it("defaults allowDuplicateOrigin to false when omitted", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_confirm_pairing",
        result: { id: "p1", name: "laptop", origin: "https://hub.example.com" },
      },
    ]);
    const svc = createProfileService(bridge);
    await svc.confirmPairing({ previewId: "pv1", name: "laptop" });
    expect(bridge.invocations[0]?.args).toEqual({
      request: {
        previewId: "pv1",
        name: "laptop",
        allowDuplicateOrigin: false,
      },
    });
  });

  it("rejects a confirm response carrying an extra capability field", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_confirm_pairing",
        result: {
          id: "p1",
          name: "laptop",
          origin: "https://hub.example.com",
          capability: "leak",
        },
      },
    ]);
    const svc = createProfileService(bridge);
    await expect(
      svc.confirmPairing({ previewId: "pv1", name: "laptop" }),
    ).rejects.toThrow(/capability|field/i);
  });
});

describe("nativeProfiles — rename", () => {
  it("invokes profile_rename with camelCase {profileId,newName}", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_rename",
        result: { id: "p1", name: "phone", origin: "https://hub.example.com" },
      },
    ]);
    const svc = createProfileService(bridge);
    const input: RenameInput = { profileId: "p1", newName: "phone" };
    const profile = await svc.rename(input);
    expect(profile).toEqual({
      id: "p1",
      name: "phone",
      origin: "https://hub.example.com",
    });
    expect(bridge.invocations[0]).toEqual({
      cmd: "profile_rename",
      args: { request: { profileId: "p1", newName: "phone" } },
    });
  });
});

describe("nativeProfiles — remove", () => {
  it("invokes profile_remove and decodes SelectResponse {profileId,generation}", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_remove",
        result: { profileId: "p2", generation: 7 },
      },
    ]);
    const svc = createProfileService(bridge);
    const res = await svc.remove({ profileId: "p1" });
    expect(res).toEqual({ profileId: "p2", generation: 7 });
    expect(bridge.invocations[0]).toEqual({
      cmd: "profile_remove",
      args: { request: { profileId: "p1" } },
    });
  });

  it("decodes a null active profile after removing the last", async () => {
    const bridge = fakeBridge([
      { cmd: "profile_remove", result: { profileId: null, generation: 8 } },
    ]);
    const svc = createProfileService(bridge);
    const res = await svc.remove({ profileId: "p1" });
    expect(res).toEqual({ profileId: null, generation: 8 });
  });

  it("rejects a remove response with an extra token field", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_remove",
        result: { profileId: null, generation: 8, token: "secret" },
      },
    ]);
    const svc = createProfileService(bridge);
    await expect(svc.remove({ profileId: "p1" })).rejects.toThrow(
      /token|field/i,
    );
  });
});

describe("nativeProfiles — select", () => {
  it("invokes profile_select with camelCase {profileId} and decodes generation", async () => {
    const bridge = fakeBridge([
      { cmd: "profile_select", result: { profileId: "p1", generation: 3 } },
    ]);
    const svc = createProfileService(bridge);
    const res = await svc.select({ profileId: "p1" });
    expect(res).toEqual({ profileId: "p1", generation: 3 });
    expect(bridge.invocations[0]).toEqual({
      cmd: "profile_select",
      args: { request: { profileId: "p1" } },
    });
  });
});

describe("nativeProfiles — health", () => {
  it("decodes HealthResponse {activeProfileId,profiles,generation}", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_health",
        result: {
          activeProfileId: "p1",
          profiles: [
            { id: "p1", name: "laptop", origin: "https://hub.example.com" },
          ],
          generation: 5,
        },
      },
    ]);
    const svc = createProfileService(bridge);
    const health: HealthSnapshot = await svc.health();
    expect(health).toEqual({
      activeProfileId: "p1",
      profiles: [
        { id: "p1", name: "laptop", origin: "https://hub.example.com" },
      ],
      generation: 5,
    });
  });

  it("rejects a health profile summary with an extra rawQr field", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_health",
        result: {
          activeProfileId: null,
          profiles: [
            {
              id: "p1",
              name: "laptop",
              origin: "https://hub.example.com",
              rawQr: "secret",
            },
          ],
          generation: 5,
        },
      },
    ]);
    const svc = createProfileService(bridge);
    await expect(svc.health()).rejects.toThrow(/rawQr|field/i);
  });

  it("decodes a null activeProfileId when no profile is selected", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_health",
        result: { activeProfileId: null, profiles: [], generation: 0 },
      },
    ]);
    const svc = createProfileService(bridge);
    const health = await svc.health();
    expect(health.activeProfileId).toBeNull();
    expect(health.profiles).toEqual([]);
    expect(health.generation).toBe(0);
  });
});

describe("nativeProfiles — structured errors", () => {
  it("error message never echoes a secret-bearing invoke failure", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_select",
        result: null,
        error: "no capability for profile AAEC-secret-token",
      },
    ]);
    const svc = createProfileService(bridge);
    const err = await svc.select({ profileId: "p1" }).catch((e) => e);
    expect(isProfileServiceError(err)).toBe(true);
    expect(String((err as Error).message)).not.toMatch(/AAEC-secret-token/);
  });

  it("redacts error text from a failed confirm", async () => {
    const bridge = fakeBridge([
      {
        cmd: "profile_confirm_pairing",
        result: null,
        error: "duplicate origin https://hub.example.com with token xyz",
      },
    ]);
    const svc = createProfileService(bridge);
    const err = await svc
      .confirmPairing({ previewId: "pv1", name: "laptop" })
      .catch((e) => e);
    expect(isProfileServiceError(err)).toBe(true);
    expect(String((err as Error).message)).not.toMatch(/xyz/);
  });

  it("ProfileServiceError has a stable code and is throwable", async () => {
    const bridge = fakeBridge([
      { cmd: "profile_rename", result: null, error: "profile not found" },
    ]);
    const svc = createProfileService(bridge);
    const err = await svc
      .rename({ profileId: "p1", newName: "x" })
      .catch((e) => e);
    expect(isProfileServiceError(err)).toBe(true);
    const perr = err as ProfileServiceError;
    expect(perr.code).toBe("command_failed");
    expect(perr.cause).toBeUndefined();
  });
});

describe("nativeProfiles — command name / casing contract", () => {
  it("uses the exact Rust command names for every method", async () => {
    const bridge = fakeBridge([
      { cmd: "profile_list", result: [] },
      { cmd: "profile_preview_paste", result: { previewId: "a", origin: "o" } },
      {
        cmd: "profile_preview_repair",
        result: { previewId: "a", origin: "o" },
      },
      {
        cmd: "profile_confirm_pairing",
        result: { id: "p", name: "n", origin: "o" },
      },
      { cmd: "profile_rename", result: { id: "p", name: "n", origin: "o" } },
      { cmd: "profile_remove", result: { profileId: null, generation: 0 } },
      { cmd: "profile_select", result: { profileId: "p", generation: 0 } },
      {
        cmd: "profile_health",
        result: { activeProfileId: null, profiles: [], generation: 0 },
      },
    ]);
    const svc = createProfileService(bridge);
    await svc.list();
    await svc.previewPaste({ raw: "r" });
    await svc.previewRepair({ profileId: "p", raw: "r" });
    await svc.confirmPairing({ previewId: "a", name: "n" });
    await svc.rename({ profileId: "p", newName: "n" });
    await svc.remove({ profileId: "p" });
    await svc.select({ profileId: "p" });
    await svc.health();
    expect(bridge.invocations.map((i) => i.cmd)).toEqual([
      "profile_list",
      "profile_preview_paste",
      "profile_preview_repair",
      "profile_confirm_pairing",
      "profile_rename",
      "profile_remove",
      "profile_select",
      "profile_health",
    ]);
  });
});
