import { describe, expect, it } from "vitest";
import fixtureRaw from "../../src-tauri/fixtures/command-envelopes-v1.json?raw";
import { createAppwireSocketFactory } from "./appwireSocket";
import { createHttpService } from "./nativeHttp";
import { createProfileService } from "./nativeProfiles";
import type { TauriBridge } from "./tauri";

type Call = { cmd: string; args: Record<string, unknown> };

const fixture = JSON.parse(fixtureRaw) as {
  commands: Record<string, { args?: Record<string, unknown> }>;
  dtoFixtures: {
    httpResponseMetadata: Record<string, unknown>;
    appwireCurrentText: Record<string, unknown>;
    appwireStaleText: Record<string, unknown>;
  };
};

function recordingBridge(): TauriBridge & {
  calls: Call[];
  emit(value: unknown): void;
} {
  const calls: Call[] = [];
  let receiver: ((value: unknown) => void) | undefined;
  const bridge: TauriBridge = {
    async invoke<T>(
      cmd: string,
      args?: Record<string, unknown> | ArrayBuffer | Uint8Array,
    ): Promise<T> {
      const objectArgs =
        args !== undefined &&
        !(args instanceof ArrayBuffer) &&
        !(args instanceof Uint8Array)
          ? args
          : {};
      calls.push({ cmd, args: objectArgs });
      if (cmd === "profile_preview_paste") {
        return { previewId: "pv", origin: "https://hub.example" } as T;
      }
      if (cmd === "hub_http_prepare") return undefined as T;
      if (cmd === "hub_http_request") {
        const channel = objectArgs.onResponse as
          | { onmessage(value: unknown): void }
          | undefined;
        channel?.onmessage(fixture.dtoFixtures.httpResponseMetadata);
        return new Uint8Array([0xff, 0xd8, 0, 0x80, 0xff, 0xd9]) as T;
      }
      if (cmd === "appwire_open") {
        return {
          connectionId: "<profileId>:7",
          profileId: "<profileId>",
          generation: 7,
        } as T;
      }
      return undefined as T;
    },
    createChannel<T>(onMessage: (response: T) => void) {
      receiver = onMessage as (value: unknown) => void;
      return { id: 1, onmessage: onMessage };
    },
  };
  return Object.assign(bridge, {
    calls,
    emit(value: unknown) {
      receiver?.(value);
    },
  });
}

describe("Rust-authored Tauri command envelopes", () => {
  it("nests profile request structs under request", async () => {
    const bridge = recordingBridge();
    await createProfileService(bridge).previewPaste({ raw: "<redacted>" });
    expect(bridge.calls[0]).toEqual({
      cmd: "profile_preview_paste",
      args: fixture.commands.profile_preview_paste?.args,
    });
  });

  it("nests AppWire request and keeps Channel as a separate argument", async () => {
    const bridge = recordingBridge();
    const socket = createAppwireSocketFactory(bridge, "<profileId>")("ignored");
    await new Promise<void>((resolve) => {
      socket.onopen = resolve;
    });
    expect(bridge.calls[0]?.cmd).toBe("appwire_open");
    expect(bridge.calls[0]?.args).toEqual({
      request: { profileId: "<profileId>" },
      onEvent: expect.objectContaining({ id: 1 }),
    });
    socket.close();
  });

  it("rejects the Rust-fixture stale generation and accepts its current one", async () => {
    const bridge = recordingBridge();
    const socket = createAppwireSocketFactory(bridge, "<profileId>")("ignored");
    await new Promise<void>((resolve) => {
      socket.onopen = resolve;
    });
    const messages: unknown[] = [];
    socket.onmessage = ({ data }) => messages.push(data);
    bridge.emit(fixture.dtoFixtures.appwireStaleText);
    bridge.emit(fixture.dtoFixtures.appwireCurrentText);
    expect(messages).toEqual(["current-frame"]);
    socket.close();
  });

  it("uses the HTTP prepare envelope from the Rust fixture", async () => {
    const bridge = recordingBridge();
    const http = createHttpService(bridge, {
      newRequestId: () => "<requestId>",
    });
    await http.request({
      activeProfileId: "<profileId>",
      method: "GET",
      path: "/api/example",
    });
    expect(bridge.calls[0]?.cmd).toBe("hub_http_prepare");
    expect(bridge.calls[0]?.args).toEqual(
      fixture.commands.hub_http_prepare?.args,
    );
  });
});
