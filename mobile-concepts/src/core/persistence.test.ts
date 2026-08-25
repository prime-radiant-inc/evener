import { describe, expect, it, vi } from "vitest";
import { canonicalFixture } from "./fixtures";
import {
  decodePreferences,
  defaultPreferences,
  encodePreferences,
} from "./persistence";
import { createInitialState, reducePrototype } from "./reducer";
import { projectScenario } from "./scenarios";

const valid = {
  version: 1,
  concept: "constellation",
  appearance: "dark",
  textScale: "accessibility",
  reducedMotion: true,
  scenario: "voice",
} as const;

const silentDiagnostics = { report: () => {} };

describe("preference persistence", () => {
  it("decodes only the exact V1 allowlist", () => {
    const report = vi.fn();
    expect(decodePreferences(JSON.stringify(valid), { report })).toEqual(valid);
    expect(report).not.toHaveBeenCalled();
  });

  it.each([
    ["malformed JSON", '{"raw-secret-marker"', "preference-invalid", "$"],
    ["malformed shape", '["raw-secret-marker"]', "preference-invalid", "$"],
    [
      "future version",
      JSON.stringify({ ...valid, version: 2 }),
      "preference-version",
      "$.version",
    ],
  ])(
    "reports one code/path-only diagnostic for %s",
    (_name, raw, code, path) => {
      const report = vi.fn();
      expect(decodePreferences(raw, { report })).toEqual(defaultPreferences);
      expect(report).toHaveBeenCalledOnce();
      expect(report).toHaveBeenCalledWith({ code, path });
      const diagnostic = report.mock.calls[0]?.[0];
      expect(Object.keys(diagnostic ?? {})).toEqual(["code", "path"]);
      expect(JSON.stringify(report.mock.calls)).not.toContain(
        "raw-secret-marker",
      );
    },
  );

  it.each([
    ["missing", null],
    ["malformed JSON", "{"],
    ["array", "[]"],
    ["future version", JSON.stringify({ ...valid, version: 2 })],
    ["unknown concept", JSON.stringify({ ...valid, concept: "future" })],
    ["unknown appearance", JSON.stringify({ ...valid, appearance: "neon" })],
    ["unknown text scale", JSON.stringify({ ...valid, textScale: "tiny" })],
    ["unknown scenario", JSON.stringify({ ...valid, scenario: "future" })],
    ["wrong boolean", JSON.stringify({ ...valid, reducedMotion: "yes" })],
    ["missing key", JSON.stringify({ ...valid, scenario: undefined })],
    ["future field", JSON.stringify({ ...valid, future: true })],
    [
      "credential-shaped field",
      JSON.stringify({ ...valid, accessToken: "secret" }),
    ],
    ["credential key", JSON.stringify({ ...valid, password: "secret" })],
  ])("returns defaults for %s", (_name, raw) => {
    expect(decodePreferences(raw, silentDiagnostics)).toEqual(
      defaultPreferences,
    );
  });

  it("returns a fresh default value that callers cannot contaminate", () => {
    const first = decodePreferences(
      "bad",
      silentDiagnostics,
    ) as unknown as Record<string, unknown>;
    first.concept = "stillwater";
    expect(decodePreferences("bad", silentDiagnostics)).toEqual(
      defaultPreferences,
    );
  });

  it("encodes only preferences and omits every interaction category", () => {
    let state = createInitialState({
      platform: "ios",
      preferences: valid,
      projection: projectScenario(canonicalFixture, "voice"),
    });
    state = reducePrototype(state, { type: "setDraft", value: "secret draft" });
    state = reducePrototype(state, {
      type: "setGlobalQuery",
      value: "secret query",
    });
    state = reducePrototype(state, {
      type: "toggleTool",
      itemId: "item-tool-inspect",
    });
    state = reducePrototype(state, {
      type: "setNewSessionProject",
      value: "/workspace/aurora",
    });
    state = reducePrototype(state, {
      type: "setSpeakResponses",
      enabled: false,
    });
    state = reducePrototype(state, { type: "advanceVoice" });

    const encoded = encodePreferences(state);
    expect(JSON.parse(encoded)).toEqual(valid);
    for (const omitted of [
      "route",
      "history",
      "query",
      "draft",
      "answers",
      "sessions",
      "expanded",
      "newSession",
      "voicePreferences",
      "project",
      "prompt",
    ]) {
      expect(encoded).not.toContain(omitted);
    }
    expect(encoded).not.toContain("secret");
    expect(encoded).not.toContain("/workspace/aurora");
  });
});
