// @vitest-environment node

// Contract test for motion (docs/web-ui/motion.md): the shared recipes in
// styles/motion.module.css, each consumer's wiring to them, and the rules
// every stylesheet under src must keep. jsdom evaluates no cascade and runs no
// animation, so these are declaration-level assertions over the stylesheet
// sources, comments stripped.
//
// PENDING lists the violations that are known, owned by later work, and not
// yet fixed. Each entry must still be violating: once its fix lands, the entry
// fails the test until it is deleted, so the list only ever shrinks.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, test } from "vitest";

const SRC = dirname(dirname(fileURLToPath(import.meta.url)));
const MOTION = "styles/motion.module.css";

function walk(dir: string): string[] {
  const found: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) found.push(...walk(full));
    else if (entry.isFile() && entry.name.endsWith(".css")) found.push(full);
  }
  return found;
}

const stripComments = (css: string) => css.replace(/\/\*[\s\S]*?\*\//g, "");
const STYLESHEETS = new Map(
  walk(SRC)
    .map((path) => [relative(SRC, path).split("\\").join("/"), stripComments(readFileSync(path, "utf8"))] as const)
    .sort(([a], [b]) => a.localeCompare(b)),
);
const read = (rel: string): string => {
  const css = STYLESHEETS.get(rel);
  if (css === undefined) throw new Error(`no stylesheet ${rel}`);
  return css;
};

// The three continuous animations (motion.md, "Continuous motion"), by
// keyframe name. Only these may loop, use a raw duration or easing, stagger
// with raw delays, or declare keyframes outside the recipes module.
const CONTINUOUS = new Set(["statusDotSpin", "loader-pulse", "streamingCaretBlink"]);

// Layout properties are animated only by the spatial surfaces that do it in
// CSS; the composer fold and the zoom columns move through src/motion.
const LAYOUT_PROPERTIES =
  /^(?:height|width|min-height|max-height|min-width|max-width|top|right|bottom|left|inset|margin(?:-\w+)?|padding(?:-\w+)?|grid-template-\w+|flex(?:-basis)?)$/;
const SPATIAL_CSS = new Set(["widgets/sheet/sheet.module.css"]);

type Rule =
  | "transition-all"
  | "raw-duration"
  | "raw-easing"
  | "default-easing"
  | "missing-duration"
  | "layout-transition"
  | "ungated-animation"
  | "ungated-transform"
  | "keyframes-outside-recipes"
  | "continuous-keyframes-duplicated";

interface Pending {
  rule: Rule;
  file: string;
  reason: string;
}

// Known violations that later work owns. Delete an entry when its fix lands:
// the test fails on an entry that no longer violates.
const PENDING: Pending[] = [
  {
    rule: "default-easing",
    file: "panes/session/composer/composer.module.css",
    reason:
      "two transitions (the card's border-color and an opacity reveal) name a duration but no easing, so they run on the raw `ease` default",
  },
  {
    rule: "ungated-transform",
    file: "panes/session/chrome/topnotespanel.module.css",
    reason: "the chevron rotation (the Move recipe) is not gated by reduced motion",
  },
  {
    rule: "continuous-keyframes-duplicated",
    file: "panes/session/transcript/streamingtext.module.css",
    reason:
      "streamingCaretBlink is declared here and again in messages/agentmessageitem.module.css; the caret's keyframes belong in one place",
  },
];

// --- parsing -----------------------------------------------------------------

interface Declaration {
  property: string;
  value: string;
  /** Every @media condition the declaration sits inside. */
  gates: string[];
}

function mediaRanges(css: string): { condition: string; start: number; end: number }[] {
  const ranges: { condition: string; start: number; end: number }[] = [];
  for (const match of css.matchAll(/@media([^{]*)\{/g)) {
    const open = (match.index ?? 0) + match[0].length - 1;
    let depth = 0;
    for (let i = open; i < css.length; i++) {
      if (css[i] === "{") depth++;
      else if (css[i] === "}" && --depth === 0) {
        ranges.push({ condition: (match[1] ?? "").trim(), start: open, end: i });
        break;
      }
    }
  }
  return ranges;
}

function declarations(css: string, property: RegExp): Declaration[] {
  const ranges = mediaRanges(css);
  const found: Declaration[] = [];
  for (const match of css.matchAll(/(?<=[;{\s])([a-z-]+)\s*:\s*([^;{}]+);/g)) {
    const name = match[1] ?? "";
    if (!property.test(name)) continue;
    const at = match.index ?? 0;
    found.push({
      property: name,
      value: (match[2] ?? "").replace(/\s+/g, " ").trim(),
      gates: ranges.filter((range) => at > range.start && at < range.end).map((range) => range.condition),
    });
  }
  return found;
}

/** Splits on `separator` outside parentheses. */
function splitTopLevel(value: string, separator: RegExp): string[] {
  const parts: string[] = [];
  let depth = 0;
  let current = "";
  for (const char of value) {
    if (char === "(") depth++;
    else if (char === ")") depth--;
    if (depth === 0 && separator.test(char)) {
      if (current.trim() !== "") parts.push(current.trim());
      current = "";
    } else current += char;
  }
  if (current.trim() !== "") parts.push(current.trim());
  return parts;
}

const TIME = /^-?[\d.]+m?s$/;
const ZERO_TIME = /^0m?s$/;
const RAW_EASING =
  /^(?:ease|ease-in|ease-out|ease-in-out|linear|step-start|step-end|cubic-bezier\(.*\)|steps\(.*\)|linear\(.*\))$/;
const DURATION_TOKEN = /^var\(--motion-duration-[a-z-]+\)$/;
const EASING_TOKEN = /^var\(--motion-easing-[a-z-]+\)$/;
const ANIMATION_KEYWORDS =
  /^(?:infinite|normal|reverse|alternate|alternate-reverse|none|forwards|backwards|both|running|paused|\d+)$/;

function animationName(item: string): string | undefined {
  return splitTopLevel(item, /\s/).find(
    (token) =>
      !TIME.test(token) && !RAW_EASING.test(token) && !token.startsWith("var(") && !ANIMATION_KEYWORDS.test(token),
  );
}

interface Violation {
  rule: Rule;
  file: string;
  detail: string;
}

function violationsIn(file: string, css: string): Violation[] {
  const found: Violation[] = [];
  const add = (rule: Rule, detail: string) => found.push({ rule, file, detail });
  const motion = declarations(css, /^(?:transition|animation)(?:-[a-z-]+)?$/);
  const animationNames = motion
    .filter((d) => d.property === "animation" || d.property === "animation-name")
    .flatMap((d) => splitTopLevel(d.value, /,/).map((item) => animationName(item)))
    .filter((name): name is string => name !== undefined);
  const onlyContinuous = animationNames.length > 0 && animationNames.every((name) => CONTINUOUS.has(name));
  const gated = (d: Declaration) => d.gates.some((gate) => /prefers-reduced-motion:\s*no-preference/.test(gate));
  const reduceSwitchesOff = (property: "animation" | "transition") =>
    declarations(css, new RegExp(`^${property}$`)).some(
      (d) => d.value === "none" && d.gates.some((gate) => /prefers-reduced-motion:\s*reduce/.test(gate)),
    );

  for (const d of motion) {
    if (d.value === "none") continue;
    const items = splitTopLevel(d.value, /,/);
    if (d.property === "transition" || d.property === "animation") {
      for (const item of items) {
        const tokens = splitTopLevel(item, /\s/);
        const name = d.property === "animation" ? animationName(item) : undefined;
        if (name !== undefined && CONTINUOUS.has(name)) continue;
        const subject = d.property === "transition" ? tokens[0] : name;
        if (d.property === "transition") {
          if (subject === undefined || subject === "all" || subject.startsWith("var(") || TIME.test(subject)) {
            add("transition-all", item);
            continue;
          }
          if (LAYOUT_PROPERTIES.test(subject) && !SPATIAL_CSS.has(file)) add("layout-transition", item);
        }
        // An instant visibility flip timed to a neighbour's fade is not motion.
        const instantFlip = subject === "visibility" && tokens.some((t) => ZERO_TIME.test(t));
        for (const token of tokens) {
          if (TIME.test(token) && !(instantFlip && ZERO_TIME.test(token))) add("raw-duration", item);
          if (RAW_EASING.test(token)) add("raw-easing", item);
        }
        if (instantFlip) continue;
        if (!tokens.some((t) => DURATION_TOKEN.test(t))) add("missing-duration", item);
        if (!tokens.some((t) => EASING_TOKEN.test(t))) add("default-easing", item);
      }
      continue;
    }
    for (const item of items) {
      if (/-property$/.test(d.property) && item === "all") add("transition-all", `${d.property}: ${item}`);
      if (/-duration$/.test(d.property) && !DURATION_TOKEN.test(item)) add("raw-duration", `${d.property}: ${item}`);
      if (/-delay$/.test(d.property) && !DURATION_TOKEN.test(item) && !ZERO_TIME.test(item) && !onlyContinuous) {
        add("raw-duration", `${d.property}: ${item}`);
      }
      if (/-timing-function$/.test(d.property) && !EASING_TOKEN.test(item)) {
        add("raw-easing", `${d.property}: ${item}`);
      }
    }
  }

  // Every animation is gated: declared under no-preference, or switched off
  // under reduce.
  for (const d of motion) {
    if (d.value === "none" || !(d.property === "animation" || d.property === "animation-name")) continue;
    if (!gated(d) && !reduceSwitchesOff("animation")) add("ungated-animation", d.value);
  }
  // A transform transition moves something; its stylesheet gates that move.
  const movesTransform = motion.some(
    (d) =>
      d.property === "transition" &&
      splitTopLevel(d.value, /,/).some((item) => splitTopLevel(item, /\s/)[0] === "transform"),
  );
  if (movesTransform && !/prefers-reduced-motion/.test(css)) add("ungated-transform", "transform transition");

  if (file !== MOTION) {
    for (const match of css.matchAll(/@keyframes\s+([\w-]+)/g)) {
      const name = match[1] ?? "";
      if (!CONTINUOUS.has(name)) add("keyframes-outside-recipes", name);
    }
  }
  return found;
}

function continuousDuplicates(): Violation[] {
  const homes = new Map<string, string[]>();
  for (const [file, css] of STYLESHEETS) {
    for (const match of css.matchAll(/@keyframes\s+([\w-]+)/g)) {
      const name = match[1] ?? "";
      if (CONTINUOUS.has(name)) homes.set(name, [...(homes.get(name) ?? []), file]);
    }
  }
  // The first home (in path order) keeps the keyframes; each later one is
  // the duplicate.
  const found: Violation[] = [];
  for (const [name, files] of homes) {
    for (const file of files.slice(1)) found.push({ rule: "continuous-keyframes-duplicated", file, detail: name });
  }
  return found;
}

const ALL_VIOLATIONS: Violation[] = [
  ...[...STYLESHEETS].flatMap(([file, css]) => violationsIn(file, css)),
  ...continuousDuplicates(),
];

const isPending = (v: Violation) => PENDING.some((p) => p.rule === v.rule && p.file === v.file);

// --- the rules ---------------------------------------------------------------

describe("every stylesheet under src", () => {
  test("holds no motion violation outside the pending list", () => {
    const unexpected = ALL_VIOLATIONS.filter((v) => !isPending(v)).map((v) => `${v.rule} ${v.file}: ${v.detail}`);
    expect(unexpected).toEqual([]);
  });

  test("every pending entry is still a violation (delete it once fixed)", () => {
    const fixed = PENDING.filter((p) => !ALL_VIOLATIONS.some((v) => v.rule === p.rule && v.file === p.file));
    expect(fixed).toEqual([]);
  });
});

describe("the rule checker", () => {
  const rulesFor = (css: string) => violationsIn("probe.module.css", css).map((v) => v.rule);

  test("catches each kind of violation it guards", () => {
    expect(rulesFor(".a { transition: all var(--motion-duration-hover) var(--motion-easing-standard); }")).toEqual([
      "transition-all",
    ]);
    expect(rulesFor(".a { transition: var(--motion-duration-hover); }")).toEqual(["transition-all"]);
    expect(rulesFor(".a { transition: color 150ms var(--motion-easing-standard); }")).toEqual([
      "raw-duration",
      "missing-duration",
    ]);
    expect(rulesFor(".a { transition: color var(--motion-duration-hover) ease-out; }")).toEqual([
      "raw-easing",
      "default-easing",
    ]);
    expect(rulesFor(".a { transition: color var(--motion-duration-hover); }")).toEqual(["default-easing"]);
    expect(rulesFor(".a { transition: height var(--motion-duration-spatial) var(--motion-easing-standard); }")).toEqual(
      ["layout-transition"],
    );
    expect(rulesFor(".a { animation: spin var(--motion-duration-hover) var(--motion-easing-standard); }")).toEqual([
      "ungated-animation",
    ]);
    expect(
      rulesFor(".a { transition: transform var(--motion-duration-hover) var(--motion-easing-standard); }"),
    ).toEqual(["ungated-transform"]);
    expect(rulesFor("@keyframes fadeIn { from { opacity: 0; } }")).toEqual(["keyframes-outside-recipes"]);
    expect(rulesFor(".a { transition-timing-function: linear; }")).toEqual(["raw-easing"]);
  });

  test("accepts the shapes the recipes and the continuous animations use", () => {
    expect(
      rulesFor(
        "@media (prefers-reduced-motion: no-preference) { .a { animation: x var(--motion-duration-overlay) var(--motion-easing-pop) backwards; } }",
      ),
    ).toEqual([]);
    expect(
      rulesFor(
        ".a { transition: opacity var(--motion-duration-hover) var(--motion-easing-standard), visibility 0s var(--motion-duration-hover); }",
      ),
    ).toEqual([]);
    expect(
      rulesFor(
        "@media (prefers-reduced-motion: no-preference) { .a { animation: loader-pulse 1.4s ease-in-out infinite; } .a:nth-child(2) { animation-delay: 90ms; } }",
      ),
    ).toEqual([]);
  });
});

// --- the recipes -------------------------------------------------------------

function ruleBody(css: string, selector: string): string {
  const escaped = selector.replace(/[.[\]>:()~*+="]/g, (c) => `\\${c}`);
  const match = new RegExp(`(?:^|[\\s,{}])${escaped}\\s*\\{([^}]*)\\}`).exec(css);
  if (!match) throw new Error(`no rule ${selector}`);
  return match[1] ?? "";
}

function keyframes(css: string, name: string): string {
  const match = new RegExp(`@keyframes\\s+${name}\\s*\\{([\\s\\S]*?)\\n\\}`).exec(css);
  if (!match) throw new Error(`no keyframes ${name}`);
  return match[1] ?? "";
}

const recipes = read(MOTION);

// Each recipe: its enter and exit animation (name, duration token, easing
// token) and what its keyframes move.
const RECIPES = {
  float: {
    enter: ["--motion-duration-overlay", "--motion-easing-pop"],
    exit: ["--motion-duration-exit", "--motion-easing-exit"],
    moves: /opacity:\s*0;\s*transform:\s*scale\(var\(--motion-float-from, var\(--motion-scale-from\)\)\)/,
  },
  fade: {
    enter: ["--motion-duration-overlay", "--motion-easing-standard"],
    exit: ["--motion-duration-exit", "--motion-easing-exit"],
    moves: /^\s*(?:from|to)\s*\{\s*opacity:\s*0;\s*\}\s*$/,
  },
  scrimSlide: {
    enter: ["--motion-duration-spatial", "--motion-easing-standard"],
    exit: ["--motion-duration-spatial-exit", "--motion-easing-exit"],
    moves: /^\s*(?:from|to)\s*\{\s*opacity:\s*0;\s*\}\s*$/,
  },
  slideLeft: {
    enter: ["--motion-duration-spatial", "--motion-easing-standard"],
    exit: ["--motion-duration-spatial-exit", "--motion-easing-exit"],
    moves: /^\s*(?:from|to)\s*\{\s*transform:\s*translateX\(-100%\);\s*\}\s*$/,
  },
  slideRight: {
    enter: ["--motion-duration-spatial", "--motion-easing-standard"],
    exit: ["--motion-duration-spatial-exit", "--motion-easing-exit"],
    moves: /^\s*(?:from|to)\s*\{\s*transform:\s*translateX\(100%\);\s*\}\s*$/,
  },
  slideBottom: {
    enter: ["--motion-duration-spatial", "--motion-easing-standard"],
    exit: ["--motion-duration-spatial-exit", "--motion-easing-exit"],
    moves: /^\s*(?:from|to)\s*\{\s*transform:\s*translateY\(100%\);\s*\}\s*$/,
  },
} as const;

describe("styles/motion.module.css", () => {
  const gate = mediaRanges(recipes)
    .filter((range) => /prefers-reduced-motion:\s*no-preference/.test(range.condition))
    .map((range) => recipes.slice(range.start, range.end + 1))
    .join("\n");

  for (const [recipe, spec] of Object.entries(RECIPES)) {
    test(`${recipe} enters and leaves on its tokens, inside the reduced-motion gate`, () => {
      const enter = /animation:\s*([\w-]+)\s+var\((--[\w-]+)\)\s+var\((--[\w-]+)\)\s+backwards/.exec(
        ruleBody(gate, `.${recipe}`),
      );
      const exit = /animation:\s*([\w-]+)\s+var\((--[\w-]+)\)\s+var\((--[\w-]+)\)\s+forwards/.exec(
        ruleBody(gate, `.${recipe}[data-motion="closing"]`),
      );
      expect(enter?.slice(2)).toEqual([...spec.enter]);
      expect(exit?.slice(2)).toEqual([...spec.exit]);
      expect(keyframes(recipes, enter?.[1] ?? "")).toMatch(spec.moves);
      expect(keyframes(recipes, exit?.[1] ?? "")).toMatch(spec.moves);
      // The leaving layer lets the pointer through to what it uncovers.
      expect(recipes).toMatch(
        new RegExp(`\\.${recipe}\\[data-motion="closing"\\][^{]*\\{\\s*pointer-events:\\s*none;\\s*\\}`),
      );
    });
  }

  test("the reveal fades a body in on the overlay budget, with no exit", () => {
    const reveal = /animation:\s*([\w-]+)\s+var\(--motion-duration-overlay\)\s+var\(--motion-easing-standard\)/.exec(
      ruleBody(gate, ".bodyReveal"),
    );
    expect(reveal).not.toBeNull();
    expect(keyframes(recipes, reveal?.[1] ?? "")).toMatch(/^\s*from\s*\{\s*opacity:\s*0;\s*\}\s*$/);
    expect(recipes).not.toMatch(/\.bodyReveal\[data-motion/);
  });

  test("every animation in the recipes sits inside the reduced-motion gate", () => {
    const outside = mediaRanges(recipes)
      .filter((range) => /prefers-reduced-motion:\s*no-preference/.test(range.condition))
      .reduceRight((css, range) => css.slice(0, range.start) + css.slice(range.end + 1), recipes);
    expect(outside).not.toMatch(/\banimation\s*:/);
  });
});

// --- the consumers -----------------------------------------------------------

// Every layer the spec names composes its recipe and declares no motion of its
// own: the recipe is the one place its keyframes, tokens and gate live.
const CONSUMERS: [file: string, selector: string, recipe: string][] = [
  ["widgets/dialog/dialog.module.css", ".dialogVariant", "float"],
  ["widgets/dialog/dialog.module.css", ".dialogVariantLarge", "float"],
  ["widgets/dialog/dialog.module.css", ".scrimFloat", "fade"],
  ["widgets/dialog/dialog.module.css", ".scrimEdge", "scrimSlide"],
  ["widgets/sheet/sheet.module.css", ".left", "slideLeft"],
  ["widgets/sheet/sheet.module.css", ".right", "slideRight"],
  ["widgets/sheet/sheet.module.css", ".bottom", "slideBottom"],
  ["widgets/popover/popover.module.css", ".panel", "float"],
  ["widgets/menu/menu.module.css", ".popup", "float"],
  ["widgets/hovercard/hovercard.module.css", ".bubble", "float"],
  ["widgets/tooltip/tooltip.module.css", ".bubble", "fade"],
  ["widgets/toast/toast.module.css", ".toast", "float"],
  ["widgets/mermaid/mermaid.module.css", ".viewerPanel", "float"],
  ["panes/session/transcript/selectionquote.module.css", ".bar", "float"],
  ["shell/holdhints/holdhints.module.css", ".root", "fade"],
  ["widgets/disclosure/disclosure.module.css", ".body", "bodyReveal"],
  ["panes/session/transcript/toolcallitem.module.css", ".body", "bodyReveal"],
];

describe("the layers compose their recipe", () => {
  for (const [file, selector, recipe] of CONSUMERS) {
    test(`${file} ${selector} composes ${recipe}`, () => {
      const body = ruleBody(read(file), selector);
      expect(body).toMatch(new RegExp(`composes:\\s*${recipe} from "(?:\\.\\./)+styles/motion\\.module\\.css";`));
      expect(body).not.toMatch(/\banimation|\btransition/);
    });
  }

  test("a Menu on a phone keeps the fade and drops the scale", () => {
    const menu = read("widgets/menu/menu.module.css");
    const phone = mediaRanges(menu).find((range) => range.condition === "(max-width: 899px)");
    expect(phone).toBeDefined();
    expect(menu.slice(phone?.start, phone?.end)).toMatch(/\.popup\s*\{\s*--motion-float-from:\s*1;/);
  });
});
