import { describe, expect, it } from "vitest";
import fixture from "../../tauri-plugin-evener-native/ios/Tests/PluginTests/Fixtures/contract-v1.json";
import {
  decodeNativeCommand,
  decodeNativeEvent,
  decodeNativeResponse,
  NATIVE_BRIDGE_VERSION,
} from "./contract";

describe("native bridge V1 contract", () => {
  it("decodes every V1 command, response, and event discriminator", () => {
    expect(NATIVE_BRIDGE_VERSION).toBe(fixture.bridgeVersion);
    expect(fixture.commands.map(decodeNativeCommand)).toEqual(fixture.commands);
    expect(fixture.responses.map(decodeNativeResponse)).toEqual(
      fixture.responses,
    );
    expect(fixture.events.map(decodeNativeEvent)).toEqual(fixture.events);
  });

  it.each([decodeNativeCommand, decodeNativeResponse, decodeNativeEvent])(
    "fails closed on an unknown bridge version",
    (decode) => {
      expect(() =>
        decode({ version: 2, type: "lifecycle.changed", state: "active" }),
      ).toThrow(/version/i);
    },
  );

  it("fails closed on an unknown command discriminator", () => {
    expect(() =>
      decodeNativeCommand({ version: 1, type: "secure.export" }),
    ).toThrow(/type/i);
  });

  it("fails closed on an unknown response discriminator", () => {
    expect(() =>
      decodeNativeResponse({
        version: 1,
        type: "secure.value",
        capability: "secret",
      }),
    ).toThrow(/type/i);
  });

  it("fails closed on an unknown event discriminator", () => {
    expect(() =>
      decodeNativeEvent({
        version: 1,
        type: "lifecycle.future",
        state: "active",
      }),
    ).toThrow(/type/i);
  });

  it("rejects secret-bearing response and error fields", () => {
    expect(() =>
      decodeNativeResponse({
        version: 1,
        type: "secure.state",
        present: true,
        capability: "secret",
      }),
    ).toThrow(/field/i);
    expect(() =>
      decodeNativeResponse({
        version: 1,
        type: "error",
        error: {
          id: "e",
          kind: "internal",
          message: "redacted",
          token: "secret",
        },
      }),
    ).toThrow(/field/i);
  });
});
