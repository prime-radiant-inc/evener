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

  it("rejects contentSize.value with a category outside the ContentSizeCategory union", () => {
    expect(() =>
      decodeNativeResponse({
        version: 1,
        type: "contentSize.value",
        category: "not-a-category",
      }),
    ).toThrow();
  });

  it("isContentSizeCategory is exported and validates the union", async () => {
    const { isContentSizeCategory } = await import("./contract");
    expect(isContentSizeCategory("large")).toBe(true);
    expect(isContentSizeCategory("accessibilityExtraExtraExtraLarge")).toBe(
      true,
    );
    expect(isContentSizeCategory("not-a-category")).toBe(false);
    expect(isContentSizeCategory(42)).toBe(false);
  });

  it("rejects error with a kind outside the NativeErrorKind union", () => {
    expect(() =>
      decodeNativeResponse({
        version: 1,
        type: "error",
        error: {
          id: "e",
          kind: "totally_made_up",
          message: "bad kind",
        },
      }),
    ).toThrow();
  });

  it("decodes every voice command variant with required fields", () => {
    expect(
      decodeNativeCommand({ version: 1, type: "voice.permissions" }),
    ).toEqual({ version: 1, type: "voice.permissions" });
    expect(
      decodeNativeCommand({ version: 1, type: "voice.start", locale: "en-US" }),
    ).toEqual({ version: 1, type: "voice.start", locale: "en-US" });
    expect(
      decodeNativeCommand({
        version: 1,
        type: "voice.stop",
        voiceSessionId: "s1",
      }),
    ).toEqual({ version: 1, type: "voice.stop", voiceSessionId: "s1" });
    expect(
      decodeNativeCommand({
        version: 1,
        type: "voice.speak",
        voiceSessionId: "s1",
        chunkId: "c1",
        text: "hello",
      }),
    ).toEqual({
      version: 1,
      type: "voice.speak",
      voiceSessionId: "s1",
      chunkId: "c1",
      text: "hello",
    });
    expect(
      decodeNativeCommand({
        version: 1,
        type: "voice.stopSpeaking",
        voiceSessionId: "s1",
      }),
    ).toEqual({
      version: 1,
      type: "voice.stopSpeaking",
      voiceSessionId: "s1",
    });
    expect(
      decodeNativeCommand({
        version: 1,
        type: "voice.setRate",
        voiceSessionId: "s1",
        rate: 0.5,
      }),
    ).toEqual({
      version: 1,
      type: "voice.setRate",
      voiceSessionId: "s1",
      rate: 0.5,
    });
  });

  it("rejects voice command missing required fields", () => {
    expect(() =>
      decodeNativeCommand({ version: 1, type: "voice.start" }),
    ).toThrow(/locale/i);
    expect(() =>
      decodeNativeCommand({ version: 1, type: "voice.stop" }),
    ).toThrow(/voiceSessionId/i);
    expect(() =>
      decodeNativeCommand({
        version: 1,
        type: "voice.speak",
        voiceSessionId: "s1",
      }),
    ).toThrow(/chunkId/i);
    expect(() =>
      decodeNativeCommand({
        version: 1,
        type: "voice.setRate",
        voiceSessionId: "s1",
      }),
    ).toThrow(/rate/i);
  });

  it("rejects voice command with extra fields", () => {
    expect(() =>
      decodeNativeCommand({
        version: 1,
        type: "voice.permissions",
        extra: true,
      } as unknown as Record<string, unknown>),
    ).toThrow(/field/i);
  });

  it("decodes every voice response variant", () => {
    expect(
      decodeNativeResponse({
        version: 1,
        type: "voice.permissions",
        granted: true,
      }),
    ).toEqual({ version: 1, type: "voice.permissions", granted: true });
    expect(
      decodeNativeResponse({
        version: 1,
        type: "voice.ready",
        voiceSessionId: "s1",
      }),
    ).toEqual({ version: 1, type: "voice.ready", voiceSessionId: "s1" });
    expect(decodeNativeResponse({ version: 1, type: "voice.stopped" })).toEqual(
      { version: 1, type: "voice.stopped" },
    );
    expect(
      decodeNativeResponse({ version: 1, type: "voice.queued", chunkId: "c1" }),
    ).toEqual({ version: 1, type: "voice.queued", chunkId: "c1" });
    expect(
      decodeNativeResponse({ version: 1, type: "voice.speakingStopped" }),
    ).toEqual({ version: 1, type: "voice.speakingStopped" });
    expect(
      decodeNativeResponse({ version: 1, type: "voice.rateSet", rate: 0.5 }),
    ).toEqual({ version: 1, type: "voice.rateSet", rate: 0.5 });
  });

  it("decodes every voice event variant with session/seq/timestamp", () => {
    const base = { version: 1, voiceSessionId: "s1", seq: 1, timestamp: 1000 };
    expect(
      decodeNativeEvent({ ...base, type: "voice.level", level: 0.5 }),
    ).toEqual({ ...base, type: "voice.level", level: 0.5 });
    expect(
      decodeNativeEvent({
        ...base,
        type: "voice.partial",
        text: "hi",
        locale: "en-US",
      }),
    ).toEqual({ ...base, type: "voice.partial", text: "hi", locale: "en-US" });
    expect(
      decodeNativeEvent({
        ...base,
        type: "voice.final",
        text: "hello",
        locale: "en-US",
      }),
    ).toEqual({ ...base, type: "voice.final", text: "hello", locale: "en-US" });
    expect(
      decodeNativeEvent({
        ...base,
        type: "voice.speechStarted",
        chunkId: "c1",
      }),
    ).toEqual({ ...base, type: "voice.speechStarted", chunkId: "c1" });
    expect(
      decodeNativeEvent({
        ...base,
        type: "voice.speechFinished",
        chunkId: "c1",
      }),
    ).toEqual({ ...base, type: "voice.speechFinished", chunkId: "c1" });
    expect(
      decodeNativeEvent({ ...base, type: "voice.bargeIn", partial: "stop" }),
    ).toEqual({ ...base, type: "voice.bargeIn", partial: "stop" });
    expect(decodeNativeEvent({ ...base, type: "voice.interrupted" })).toEqual({
      ...base,
      type: "voice.interrupted",
    });
    expect(
      decodeNativeEvent({
        ...base,
        type: "voice.error",
        error: { id: "e", kind: "internal", message: "boom" },
      }),
    ).toEqual({
      ...base,
      type: "voice.error",
      error: { id: "e", kind: "internal", message: "boom" },
    });
  });

  it("rejects voice event with missing seq/timestamp", () => {
    expect(() =>
      decodeNativeEvent({
        version: 1,
        type: "voice.level",
        voiceSessionId: "s1",
        timestamp: 1000,
        level: 0.5,
      } as unknown as Record<string, unknown>),
    ).toThrow(/seq/i);
  });

  it("rejects voice event with extra fields", () => {
    expect(() =>
      decodeNativeEvent({
        version: 1,
        type: "voice.interrupted",
        voiceSessionId: "s1",
        seq: 1,
        timestamp: 1000,
        text: "leak",
      } as unknown as Record<string, unknown>),
    ).toThrow(/field/i);
  });
});
