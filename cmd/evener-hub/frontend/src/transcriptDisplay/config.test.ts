import { describe, expect, test } from "vitest";
import {
  advancedEnabledCount,
  type ContentSelection,
  configFingerprint,
  configSummary,
  decodeLocalConfig,
  dualWriteLegacyPreferences,
  encodeLocalConfig,
  fromWireConfig,
  type LegacyPreferenceKey,
  legacyConfigFromValues,
  legacyWritesFromConfig,
  makeTranscriptDisplayConfig,
  normalizeConfig,
  normalizeContent,
  presetContent,
  resolveEffectiveConfig,
  shippedDefaults,
  toWireConfig,
  visibleCategoryInventory,
} from "./config";

describe("transcript display config", () => {
  test("expands the five cumulative content presets", () => {
    expect(presetContent("chat")).toEqual({
      toolIntent: false,
      toolCalls: false,
      reasoning: false,
      expandByDefault: false,
    });
    expect(presetContent("intent")).toEqual({
      toolIntent: true,
      toolCalls: false,
      reasoning: false,
      expandByDefault: false,
    });
    expect(presetContent("tools")).toEqual({
      toolIntent: true,
      toolCalls: true,
      reasoning: false,
      expandByDefault: false,
    });
    expect(presetContent("activity")).toEqual({
      toolIntent: true,
      toolCalls: true,
      reasoning: true,
      expandByDefault: false,
    });
    expect(presetContent("full")).toEqual({
      toolIntent: true,
      toolCalls: true,
      reasoning: true,
      expandByDefault: true,
    });
  });

  test("normalizes exact Custom vectors to named presets and retains other vectors", () => {
    const customIntent: ContentSelection = {
      kind: "custom",
      ...presetContent("intent"),
    };
    expect(normalizeContent(customIntent)).toEqual({ kind: "preset", level: "intent" });
    expect(
      normalizeContent({
        kind: "custom",
        toolIntent: false,
        toolCalls: true,
        reasoning: false,
        expandByDefault: true,
      }),
    ).toEqual({
      kind: "custom",
      toolIntent: false,
      toolCalls: true,
      reasoning: false,
      expandByDefault: true,
    });
  });

  test("regular normalization preserves independent Advanced settings", () => {
    const config = makeTranscriptDisplayConfig(
      { kind: "preset", level: "chat" },
      {
        roundTimings: true,
        tokenCounts: true,
        estimatedCost: true,
        systemEvents: true,
        promptEvents: true,
        hookExits: "all",
      },
    );
    expect(normalizeConfig({ ...config, content: { kind: "preset", level: "tools" } })).toEqual({
      version: 1,
      content: { kind: "preset", level: "tools" },
      advanced: config.advanced,
    });
  });

  test("encodes local data with a stable property order", () => {
    const config = makeTranscriptDisplayConfig(
      { kind: "custom", toolIntent: true, toolCalls: false, reasoning: true, expandByDefault: false },
      { roundTimings: true, estimatedCost: true, hookExits: "successful" },
    );
    expect(encodeLocalConfig(config)).toBe(
      '{"version":1,"content":{"kind":"custom","custom":{"toolIntent":true,"toolCalls":false,"reasoning":true,"expandByDefault":false}},"advanced":{"roundTimings":true,"tokenCounts":false,"estimatedCost":true,"systemEvents":false,"promptEvents":false,"hookExits":"successful"}}',
    );
    expect(configFingerprint(config)).toBe(encodeLocalConfig(config));
  });

  test.each([
    null,
    "",
    "not-json",
    "{}",
    '{"version":2,"content":{"kind":"preset","level":"chat"},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}',
    '{"version":1,"content":{"kind":"preset","level":"unknown"},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}',
    '{"version":1,"content":{"kind":"custom","custom":{"toolIntent":true}},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}',
    '{"version":1,"content":{"kind":"preset","level":"chat","custom":{"toolIntent":false,"toolCalls":false,"reasoning":false,"expandByDefault":false}},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}',
  ])("treats malformed or unsupported local data as absent (%j)", (raw) => {
    expect(decodeLocalConfig(raw)).toBeUndefined();
  });

  test("round-trips normalized wire config and rejects optional-field omissions", () => {
    const config = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }, { systemEvents: true });
    const wire = toWireConfig(config);
    expect(fromWireConfig(wire)).toEqual(config);
    expect(fromWireConfig({ ...wire, content: { kind: "custom" } })).toBeUndefined();
    expect(fromWireConfig({ ...wire, advanced: { ...wire.advanced, hookExits: undefined } })).toBeUndefined();
  });

  test("resolves local then hub then shipped precedence", () => {
    const local = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
    const hub = { revision: 3, config: makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }) };
    const shipped = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
    expect(resolveEffectiveConfig(local, hub, shipped)).toEqual(local);
    expect(resolveEffectiveConfig(undefined, hub, shipped)).toEqual(hub.config);
    expect(resolveEffectiveConfig(undefined, undefined, shipped)).toEqual(shipped);
  });

  test("ships Desktop Tools and Mobile Intent with Advanced disabled", () => {
    expect(shippedDefaults.desktop).toEqual({
      revision: 0,
      config: makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }),
    });
    expect(shippedDefaults.mobile).toEqual({
      revision: 0,
      config: makeTranscriptDisplayConfig({ kind: "preset", level: "intent" }),
    });
    expect(advancedEnabledCount(shippedDefaults.desktop.config)).toBe(0);
  });

  test("summarizes Advanced counts and visible/hidden inventory", () => {
    const config = makeTranscriptDisplayConfig(
      { kind: "preset", level: "tools" },
      {
        roundTimings: true,
        tokenCounts: true,
        hookExits: "successful",
      },
    );
    expect(advancedEnabledCount(config)).toBe(3);
    expect(configSummary(config)).toBe("Tools · 3 advanced");
    expect(visibleCategoryInventory(config)).toEqual({
      visible: [
        "userMessages",
        "agentMessages",
        "criticalRows",
        "toolIntent",
        "toolCalls",
        "roundTimings",
        "tokenCounts",
        "hookExits",
      ],
      hidden: ["reasoning", "expandedDetails", "estimatedCost", "systemEvents", "promptEvents"],
    });
  });

  test("maps legacy values with exact fallbacks and hook precedence", () => {
    expect(legacyConfigFromValues({ transcriptHookExitsNormal: "1" })?.advanced).toEqual({
      roundTimings: true,
      tokenCounts: false,
      estimatedCost: false,
      systemEvents: true,
      promptEvents: true,
      hookExits: "successful",
    });
    expect(
      legacyConfigFromValues({ transcriptHookExitsNormal: "1", transcriptHookExitsAll: "1" })?.advanced.hookExits,
    ).toBe("all");
    expect(legacyConfigFromValues({ transcriptRoundTimings: "0", showCost: "1" })?.advanced).toEqual({
      roundTimings: false,
      tokenCounts: false,
      estimatedCost: true,
      systemEvents: true,
      promptEvents: true,
      hookExits: "none",
    });
    expect(legacyConfigFromValues({})).toBeUndefined();
  });

  test("dual-writes every legacy boolean with exact 1/0 semantics", () => {
    const writes: Partial<Record<LegacyPreferenceKey, boolean>> = {};
    dualWriteLegacyPreferences(
      makeTranscriptDisplayConfig(
        { kind: "preset", level: "activity" },
        {
          roundTimings: true,
          tokenCounts: false,
          estimatedCost: true,
          promptEvents: false,
          hookExits: "all",
        },
      ),
      (key, value) => {
        writes[key] = value;
      },
    );
    expect(writes).toEqual({
      transcriptRoundTimings: true,
      transcriptTokenCounts: false,
      transcriptHookExitsAll: true,
      transcriptHookExitsNormal: false,
      transcriptPromptLoaded: false,
      showCost: true,
    });
    expect(legacyWritesFromConfig(makeTranscriptDisplayConfig())).toEqual({
      transcriptRoundTimings: false,
      transcriptTokenCounts: false,
      transcriptHookExitsAll: false,
      transcriptHookExitsNormal: false,
      transcriptPromptLoaded: false,
      showCost: false,
    });
  });
});
