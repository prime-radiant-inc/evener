// Contract test for the notification card's status glyph seat. The card is a
// "run" row (layoutRoles.ts), so its head sits inside .runContent's reserved
// --speaker-gutter padding above the 700px breakpoint; the status glyph is
// the one element that reaches back into that gutter, in the same avatar
// column every other rail icon (ToolRow's .rowIcon, ThinkBlock's bulb, the
// steer diamond) occupies - the column is shared by this seat's consumers
// and the documented variants alike (styles/railseat.module.css). jsdom
// cannot see CSS, so these are declaration-level assertions
// over the stylesheet sources - the same idiom as thinkBlockMotion.test.ts and
// agentMessageSize.contract.test.ts. Comments are stripped first: a stylesheet
// grep that matches its own comment prose asserts nothing.
//
// The seat's geometry lives in styles/railseat.module.css and is composed
// here via `composes:` - that is the consolidation contract. This file pins
// the card's WIRING: .statusIcon composes the shared seat and gutter pull
// and restates no geometry of its own. The shared declarations every rail
// consumer inherits are pinned once, next to the module, in
// railseat.contract.test.ts; the browser-measured geometry is the
// transcript-rail-icon-seats layoutguard case.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, "notificationcard.module.css"), "utf8");
const uncommented = css.replace(/\/\*[\s\S]*?\*\//g, "");

// The .statusIcon rule's own body: with the composes lines removed, nothing
// may remain - every geometry declaration is the shared seat's to make. A
// local restatement would be exactly the hand-synced copy this consolidation
// removed, so it fails the contract instead of drifting silently.
const statusIconRule = uncommented.match(/\.statusIcon\s*\{([^}]*)\}/)?.[1] ?? "";
const statusIconOwn = statusIconRule.replace(/composes:[^;]+;/g, "").trim();

test("the status slot IS the shared rail seat: composed, not copied", () => {
  expect(statusIconRule).toContain('composes: seat from "../../../../styles/railseat.module.css"');
  expect(statusIconRule).toContain('composes: gutterPull from "../../../../styles/railseat.module.css"');
});

test("statusIcon restates no seat geometry of its own", () => {
  expect(statusIconOwn).toBe("");
});

// The failure tone's seat variant: the sibling .statusIcon seat at FULL
// strength - the red cross is the card's one attention signal now that the
// error chip is gone, so the seat's ambient 50% opacity is the one
// declaration it overrides. It composes the sibling .statusIcon rather than
// restating the shared seat pair, so the two seats cannot drift apart; the
// shared rail geometry itself is pinned by the .statusIcon tests above. The
// colour comes from the FailureGlyph widget's own stylesheet (the token
// allowlist's sanctioned --danger home); this card stylesheet never names
// a hue.
const statusIconErrorRule = uncommented.match(/\.statusIconError\s*\{([^}]*)\}/)?.[1] ?? "";
const statusIconErrorOwn = statusIconErrorRule.replace(/composes:[^;]+;/g, "").trim();

test("the failure tone seats its glyph by composing the statusIcon seat", () => {
  expect(statusIconErrorRule).toContain("composes: statusIcon;");
});

test("the failure seat overrides only the ambient opacity, nothing else", () => {
  expect(statusIconErrorOwn).toBe("opacity: 1;");
});
