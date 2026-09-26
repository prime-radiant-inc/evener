// @vitest-environment node

import { describe, expect, test } from "vitest";
import {
  accessibleConfigSummary,
  advancedEnabledCount,
  configFingerprint,
  configSummary,
  contentSummary,
  decodeLocalConfig,
  dualWriteLegacyPreferences,
  encodeLocalConfig,
  fromWireConfig,
  fromWireDefault,
  fromWireDefaults,
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
  toWireDefault,
  toWireDefaults,
  visibleCategoryInventory,
} from "./transcriptDisplayConfig";

// #1431 retired these three type aliases along with the value aliases below:
// none had a consumer in any app tree, and none had coverage here. A type
// alias is erased from the program, so its absence is a compile-time property
// rather than a runtime one: the frontend's `tsc --noEmit` (whose program is
// this file and so the whole package) proves each reference below errors. If
// an alias returns, its directive is reported as unused instead. The union
// keeps the three referenced so biome's noUnusedVariables is satisfied.
// @ts-expect-error -- deleted zero-consumer alias (#1431)
type RetiredTranscriptDisplayConfig = import("./transcriptDisplayConfig").TranscriptDisplayConfig;
// @ts-expect-error -- deleted zero-consumer alias (#1431)
type RetiredTranscriptHookExitDetail = import("./transcriptDisplayConfig").TranscriptHookExitDetail;
// @ts-expect-error -- deleted zero-consumer alias (#1431)
type RetiredTranscriptLevel = import("./transcriptDisplayConfig").TranscriptLevel;
type RetiredTranscriptAliases =
  | RetiredTranscriptDisplayConfig
  | RetiredTranscriptHookExitDetail
  | RetiredTranscriptLevel;
void (undefined as RetiredTranscriptAliases | undefined);

describe("transcript display config", () => {
  const LEVELS = ["chat", "intent", "tools", "activity", "full"] as const;

  test("Chat enables intent proxies without expanding them (catches toolIntent=false)", () => {
    expect(presetContent("chat")).toMatchObject({ toolIntent: true, toolCalls: false, expandByDefault: false });
  });

  test("Intent keeps generic detail expansion disabled", () => {
    expect(presetContent("intent")).toMatchObject({ toolIntent: true, toolCalls: false, expandByDefault: false });
  });

  test("expands the five cumulative content presets", () => {
    expect(presetContent("chat")).toEqual({
      toolIntent: true,
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
      reasoning: false,
      expandByDefault: true,
    });
    expect(presetContent("full")).toEqual({
      toolIntent: true,
      toolCalls: true,
      reasoning: true,
      expandByDefault: true,
    });
  });

  test.each(LEVELS)("preserves explicit Custom identity when its vector equals %s", (level) => {
    const vector = presetContent(level);
    const custom = makeTranscriptDisplayConfig({ kind: "custom", ...vector });
    const preset = makeTranscriptDisplayConfig({ kind: "preset", level });

    expect(custom.content).toEqual({ kind: "custom", ...vector });
    expect(toWireConfig(custom).content).toEqual({ kind: "custom", custom: vector });
    expect(fromWireConfig(toWireConfig(custom))).toEqual(custom);
    expect(decodeLocalConfig(encodeLocalConfig(custom))).toEqual(custom);
    expect(configFingerprint(custom)).not.toBe(configFingerprint(preset));
    expect(contentSummary(custom.content)).toBe("Custom");
  });

  test("normalizes named presets and retains non-preset Custom vectors", () => {
    expect(normalizeContent({ kind: "preset", level: "intent" })).toEqual({ kind: "preset", level: "intent" });
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
    '{"version":1,"content":{"kind":"custom","custom":{"toolIntent":true,"toolCalls":true,"reasoning":false,"expandByDefault":false,"extra":false}},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}',
    '{"version":1,"content":{"kind":"custom","custom":{"toolIntent":"true","toolCalls":true,"reasoning":false,"expandByDefault":false}},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}',
    '{"version":1,"content":{"kind":"preset","level":"chat","custom":{"toolIntent":false,"toolCalls":false,"reasoning":false,"expandByDefault":false}},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}',
  ])("treats malformed or unsupported local data as absent (%j)", (raw) => {
    expect(decodeLocalConfig(raw)).toBeUndefined();
  });

  test("round-trips normalized wire config and rejects optional-field omissions", () => {
    const config = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }, { systemEvents: true });
    const wire = toWireConfig(config);
    expect(fromWireConfig(wire)).toEqual(config);
    expect(fromWireConfig({ ...wire, content: { kind: "custom" } })).toBeUndefined();
    expect(
      fromWireConfig({
        ...wire,
        content: {
          kind: "custom",
          custom: { ...presetContent("tools"), extra: false },
        },
      }),
    ).toBeUndefined();
    expect(
      fromWireConfig({
        ...wire,
        content: {
          kind: "custom",
          custom: { ...presetContent("tools"), toolCalls: "true" },
        },
      }),
    ).toBeUndefined();
    expect(fromWireConfig({ ...wire, advanced: { ...wire.advanced, hookExits: undefined } })).toBeUndefined();
  });

  test("fromWireDefault accepts future top-level fields", () => {
    const wire = toWireDefault(shippedDefaults.desktop);
    expect(fromWireDefault({ ...wire, futureField: "ignored" })).toEqual(shippedDefaults.desktop);
  });

  test("fromWireDefaults accepts future top-level fields", () => {
    const wire = toWireDefaults(shippedDefaults);
    expect(fromWireDefaults({ ...wire, futureField: "ignored" })).toEqual(shippedDefaults);
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
    expect(accessibleConfigSummary(makeTranscriptDisplayConfig({ kind: "preset", level: "full" }))).toBe("Full detail");
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
      hidden: ["reasoning", "expandedDetails", "informationalNotices", "estimatedCost", "systemEvents", "promptEvents"],
    });
  });

  test("inventory gates informational notices on expandByDefault (the high verbosity levels)", () => {
    for (const level of ["activity", "full"] as const) {
      expect(visibleCategoryInventory(makeTranscriptDisplayConfig({ kind: "preset", level })).visible).toContain(
        "informationalNotices",
      );
    }
    for (const level of ["chat", "intent", "tools"] as const) {
      expect(visibleCategoryInventory(makeTranscriptDisplayConfig({ kind: "preset", level })).hidden).toContain(
        "informationalNotices",
      );
    }
    expect(
      visibleCategoryInventory(
        makeTranscriptDisplayConfig({
          kind: "custom",
          toolIntent: true,
          toolCalls: true,
          reasoning: false,
          expandByDefault: true,
        }),
      ).visible,
    ).toContain("informationalNotices");
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
