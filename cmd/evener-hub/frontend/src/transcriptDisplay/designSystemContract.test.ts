import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const frontendRoot = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

type CssRule = { selector: string; body: string };
type Declaration = [property: string, value: string];

function source(path: string): string {
  return readFileSync(join(frontendRoot, path), "utf8");
}

function css(path: string): string {
  return source(path).replace(/\/\*[\s\S]*?\*\//g, "");
}

function cssRules(path: string): CssRule[] {
  const rules: CssRule[] = [];
  const rulePattern = /([^{}]+)\{([^{}]*)\}/g;
  for (const match of css(path).matchAll(rulePattern)) {
    const selector = match[1]?.trim();
    const body = match[2]?.trim();
    if (selector !== undefined && body !== undefined) rules.push({ selector, body });
  }
  return rules;
}

function rulesFor(path: string, selector: string): CssRule[] {
  return cssRules(path).filter((rule) => rule.selector.split(",").some((candidate) => candidate.trim() === selector));
}

function declarations(rule: CssRule): Declaration[] {
  return rule.body
    .split(";")
    .map((declaration) => declaration.trim())
    .filter(Boolean)
    .map((declaration) => {
      const separator = declaration.indexOf(":");
      return [declaration.slice(0, separator).trim(), declaration.slice(separator + 1).trim()];
    });
}

function declarationValues(rule: CssRule, property: string): string[] {
  return declarations(rule)
    .filter(([name]) => name === property)
    .map(([, value]) => value);
}

function expectExactlyOneRule(path: string, selector: string): CssRule {
  const matchingRules = rulesFor(path, selector);
  expect(matchingRules, `${path} ${selector}`).toHaveLength(1);
  return matchingRules[0]!;
}

function expectMappedSelectorsUnique(path: string, allowed: ReadonlyMap<string, Declaration[]>): void {
  for (const selector of allowed.keys()) {
    expect(
      cssRules(path).filter((rule) => rule.selector === selector),
      `${path} ${selector}`,
    ).toHaveLength(1);
  }
}

function expectDeclarations(rule: CssRule, expected: Declaration[]): void {
  expect(declarations(rule)).toEqual(expected);
}

const featureStyles = [
  "src/panes/session/transcript/transcriptDisplay.module.css",
  "src/panes/settings/sections/transcriptDisplayCard.module.css",
  "src/panes/settings/sections/transcript.module.css",
] as const;

const editor = source("src/panes/session/transcript/TranscriptDetailEditor.tsx");
const liveControl = source("src/panes/session/transcript/TranscriptDetailControl.tsx");
const settingsCard = source("src/panes/settings/sections/TranscriptDisplayCard.tsx");
const transcriptSources = [editor, liveControl, settingsCard] as const;

function expectNoFeatureSelectors(pattern: RegExp): void {
  for (const path of featureStyles) {
    for (const rule of cssRules(path)) expect(rule.selector, path).not.toMatch(pattern);
  }
}

function expectNoFeatureDeclarations(pattern: RegExp): void {
  for (const path of featureStyles) {
    for (const rule of cssRules(path)) expect(rule.body, path).not.toMatch(pattern);
  }
}

function expectOnlyAllowedChrome(path: string, allowed: ReadonlyMap<string, Declaration[]>): void {
  const chrome = /^(?:background(?:-[\w-]+)?|border(?:-[\w-]+)?|box-shadow)$/i;
  expectMappedSelectorsUnique(path, allowed);
  for (const rule of cssRules(path)) {
    const declarationsInRule = declarations(rule).filter(([property]) => chrome.test(property));
    if (declarationsInRule.length === 0) continue;
    const expected = allowed.get(rule.selector);
    expect(expected, `${path} unexpected chrome selector ${rule.selector}`).toBeDefined();
    expect(declarationsInRule).toEqual(expected);
  }
}

function expectOnlyAllowedOverflow(path: string, allowed: ReadonlyMap<string, Declaration[]>): void {
  const overflow = /^overflow(?:-[\w-]+)?$/i;
  expectMappedSelectorsUnique(path, allowed);
  for (const rule of cssRules(path)) {
    const declarationsInRule = declarations(rule).filter(([property]) => overflow.test(property));
    if (declarationsInRule.length === 0) continue;
    const expected = allowed.get(rule.selector);
    expect(expected, `${path} unexpected overflow selector ${rule.selector}`).toBeDefined();
    expect(declarationsInRule).toEqual(expected);
  }
}

function expectOnlyAllowedPadding(path: string, allowed: ReadonlyMap<string, Declaration[]>): void {
  expectMappedSelectorsUnique(path, allowed);
  for (const rule of cssRules(path)) {
    const declarationsInRule = declarations(rule).filter(([property]) => /^padding(?:-[\w-]+)?$/i.test(property));
    if (declarationsInRule.length === 0) continue;
    const expected = allowed.get(rule.selector);
    expect(expected, `${path} unexpected padding selector ${rule.selector}`).toBeDefined();
    expect(declarationsInRule).toEqual(expected);
  }
}

function expectOnlyAllowedSizing(path: string, allowed: ReadonlyMap<string, Declaration[]>): void {
  const sizing = /^(?:(?:min|max)-)?(?:width|inline-size)$/i;
  expectMappedSelectorsUnique(path, allowed);
  for (const rule of cssRules(path)) {
    const declarationsInRule = declarations(rule).filter(([property]) => sizing.test(property));
    if (declarationsInRule.length === 0) continue;
    const expected = allowed.get(rule.selector);
    expect(expected, `${path} unexpected sizing selector ${rule.selector}`).toBeDefined();
    expect(declarationsInRule).toEqual(expected);
  }
}

test("keeps transcript display surfaces on shared design-system primitives", () => {
  expectNoFeatureSelectors(/(?:radio|switch)(?:[-_.:#[]|[A-Z]|\b)/i);
  expectNoFeatureSelectors(/(?:^|[\s>])\.(?:select|advancedTrigger|detailTrigger|retry)(?:[-_A-Z]|\b)/);
  expectNoFeatureSelectors(/(?:^|[\s>])(?:button|input|select|textarea)(?:\b|[.#[:])/i);
  expectNoFeatureSelectors(/(?:^|[\s>])\.(?:option|track|summary|details|chevron|popover|card)(?:[-_A-Z]|\b)/);
  expectNoFeatureSelectors(/\[\s*role\s*[~|^$*]?=/i);
  expectNoFeatureDeclarations(/(?:accent|border-inline-start|border-left)/i);
  expectNoFeatureDeclarations(/line-height\s*:\s*1\.(?:45|5)\b/);

  const motionRules = featureStyles.flatMap((path) =>
    cssRules(path).filter((rule) => /(?:^|;)\s*(?:transition|animation)(?:-[\w-]+)?\s*:/i.test(rule.body)),
  );
  expect(motionRules.every((rule) => !rule.selector.includes("*"))).toBe(true);

  for (const text of transcriptSources) {
    expect(text).not.toContain("Current detail");
    expect(text).not.toMatch(/critical[- ]rows?\s+(?:explanation|note|means|show|include|are|details?)/i);
  }
  expect(editor).toMatch(/<SegmentedControl(?:\s|>)/);
  expect(editor).toMatch(/<Disclosure(?:\s|>)/);
  expect(editor).toMatch(/<Switch(?:\s|>)/);
  expect(editor).toMatch(/<FormRow(?:\s|>)/);
  expect(editor).toMatch(/<Select(?:\s|>)/);
  expect(settingsCard).toMatch(/<Button(?:\s|>)/);
  expect(settingsCard).toMatch(/<Card(?:\s|>)/);
  expect(liveControl).toMatch(/<Button(?:\s|>)/);
  expect(liveControl).toMatch(/<Dialog(?:\s|>)/);
  expect(liveControl).toMatch(/<Sheet(?:\s|>)/);
  expect(editor).not.toMatch(/<(?:button|input|select|textarea)\b/);
  expect(settingsCard).not.toMatch(/<(?:button|input|select|textarea)\b/);
  expect(liveControl).not.toMatch(/<(?:button|input|select|textarea)\b/);
  expect(settingsCard).not.toMatch(/(?:style|width)\s*=\s*[{"]|style\s*:/i);
});

test("keeps feature rules from taking shared Card and overlay chrome", () => {
  const cardPath = "src/panes/settings/sections/transcriptDisplayCard.module.css";
  const contentRule = expectExactlyOneRule(cardPath, ".content");
  expectDeclarations(contentRule, [
    ["display", "flex"],
    ["flex-direction", "column"],
    ["gap", "var(--space-4)"],
    ["min-width", "0"],
  ]);

  const detailPath = "src/panes/session/transcript/transcriptDisplay.module.css";
  const detailRule = expectExactlyOneRule(detailPath, ".detailPanel");
  expectDeclarations(detailRule, [
    ["min-width", "0"],
    ["container-type", "inline-size"],
    ["container-name", "transcript-detail-panel"],
  ]);

  expectOnlyAllowedChrome(
    detailPath,
    new Map([
      [".fieldset", [["border", "0"]]],
      [".detailStatus,\n.detailWarning", [["background", "var(--surface-inset)"]]],
    ]),
  );
  expectOnlyAllowedOverflow(detailPath, new Map());
  expectOnlyAllowedPadding(
    detailPath,
    new Map([
      [".fieldset", [["padding", "0"]]],
      [".fieldset legend", [["padding", "0"]]],
      [".detailStatus,\n.detailWarning", [["padding", "var(--space-2) var(--space-3)"]]],
    ]),
  );
  expectOnlyAllowedChrome(
    cardPath,
    new Map([
      [".example", [["background", "var(--surface-canvas)"]]],
      [".inventory", [["background", "var(--surface-inset)"]]],
      [".error", [["background", "var(--surface-inset)"]]],
    ]),
  );
  expectOnlyAllowedOverflow(cardPath, new Map());
  expectOnlyAllowedPadding(
    cardPath,
    new Map([
      [".example", [["padding", "var(--space-3)"]]],
      [".inventory", [["padding", "var(--space-2) var(--space-3)"]]],
      [".error", [["padding", "var(--space-2) var(--space-3)"]]],
    ]),
  );
  const sectionPath = "src/panes/settings/sections/transcript.module.css";
  expectOnlyAllowedChrome(sectionPath, new Map([[".error", [["background", "var(--surface-inset)"]]]]));
  expectOnlyAllowedOverflow(sectionPath, new Map());
  expectOnlyAllowedPadding(sectionPath, new Map([[".error", [["padding", "var(--space-2) var(--space-3)"]]]]));
});

test("keeps width ownership and neutral preview/status surfaces explicit", () => {
  const cardPath = "src/panes/settings/sections/transcriptDisplayCard.module.css";
  const width390Rules = featureStyles.flatMap((path) =>
    cssRules(path).flatMap((rule) =>
      declarations(rule)
        .filter(
          ([property, value]) => /^(?:(?:min|max)-)?(?:width|inline-size)$/.test(property) && value.includes("390px"),
        )
        .map(() => ({ path, rule })),
    ),
  );
  expect(width390Rules).toHaveLength(1);
  expect(width390Rules[0]?.path).toBe(cardPath);
  expect(width390Rules[0]?.rule.selector).toBe(".mobileCanvas");
  expectOnlyAllowedSizing(
    cardPath,
    new Map([
      [".content", [["min-width", "0"]]],
      [".controls", [["min-width", "0"]]],
      [".preview", [["min-width", "0"]]],
      [".example", [["min-width", "0"]]],
      [".mobileCanvas", [["width", "min(390px, 100%)"]]],
    ]),
  );

  const cardStyles = css(cardPath);
  expect(cardStyles).toMatch(
    /\.mobileCanvas\s*\{[^}]*box-sizing\s*:\s*border-box[^}]*width\s*:\s*min\(390px,\s*100%\)[^}]*margin-inline\s*:\s*auto/s,
  );
  const exampleRule = expectExactlyOneRule(cardPath, ".example");
  expectDeclarations(exampleRule, [
    ["min-width", "0"],
    ["background", "var(--surface-canvas)"],
    ["padding", "var(--space-3)"],
  ]);
  const inventoryRule = expectExactlyOneRule(cardPath, ".inventory");
  expect(declarationValues(inventoryRule, "background")).toEqual(["var(--surface-inset)"]);
  const cardErrorRule = expectExactlyOneRule(cardPath, ".error");
  expect(declarationValues(cardErrorRule, "background")).toEqual(["var(--surface-inset)"]);
  const sectionErrorRule = expectExactlyOneRule("src/panes/settings/sections/transcript.module.css", ".error");
  expect(declarationValues(sectionErrorRule, "background")).toEqual(["var(--surface-inset)"]);
  expect(cardStyles).not.toMatch(/\b(?:border|border-radius|box-shadow|overflow(?:-[xy])?)\s*:/);
});
