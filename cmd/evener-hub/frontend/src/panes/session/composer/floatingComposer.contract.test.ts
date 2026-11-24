// @vitest-environment node
import { expect, test } from "vitest";
import { blockBody, escapeRegExp, mediaBlock, readModuleCss } from "../../../styles/cssBlock";

// The floating composer (docs/web-ui/design-system.md §12) is pure stylesheet
// geometry - jsdom lays out nothing, so the phone rules are pinned off disk
// the same way display-gates.test.ts pins its gates. What is pinned:
//
//  - PaneScaffold's phone block floats a session pane's footer over the body:
//    no border-top rectangle, a gradient scrim that fades the transcript out
//    behind the floating chrome (a painted surface, not a boxed strip), and a
//    root that passes pointer events through to the scroll behind it in its
//    gutters (its children take taps again).
//  - The session pane's own stylesheet sizes the scrollable reserve that lets
//    the last turn clear the floating composer: the end inset row measures to
//    the chrome's measured height plus the home-indicator band only under the
//    phone's gate, topped up by the floating stack's own clearance, and the
//    dormant attention dock rides above the same reserve.
//  - PromptCard's phone block turns the composer card - and only the
//    composer's, keyed on [data-composer] so the spawn form keeps its framed
//    card - into the floating surface: no border (the outline focus ring is
//    the one line it draws), a translucent field color over a 16px backdrop
//    blur, and the folded one-row shape takes the capsule radius. The
//    expanded card keeps its panel radius: a 999px radius on a stacked card
//    paints the field's first line outside the arc.
const scaffoldCss = readModuleCss(import.meta.url, "../../../widgets/panescaffold/panescaffold.module.css");
const cardCss = readModuleCss(import.meta.url, "../../../widgets/promptcard/promptcard.module.css");
const sessionCss = readModuleCss(import.meta.url, "../session.module.css");

// The session-scoped selector both panescaffold rules key on: session panes
// are the only ones whose footer is a floating composer.
const SESSION_PANE = escapeRegExp('.pane:has(> .body[data-pane-scaffold^="session:"])');

// The block of the first rule in `block` whose selector text matches
// `selectorPattern` (callers pass the pattern; the selector is CSS Modules
// source, not a literal, so no escapeRegExp passthrough here).
function ruleIn(block: string, selectorPattern: RegExp): string {
  const match = selectorPattern.exec(block);
  if (!match) throw new Error(`no rule matching ${selectorPattern.source}`);
  return blockBody(block, block.indexOf("{", match.index));
}

test("a phone session pane floats its footer over the transcript", () => {
  const phone = mediaBlock(scaffoldCss, "max-width: 899px");
  const footer = ruleIn(phone, new RegExp(`${SESSION_PANE} > \\.footer\\s*\\{`));
  expect(footer).toMatch(/position:\s*absolute/);
  expect(footer).toMatch(/border-top:\s*none/);
  // A fade, not a strip: the scrim starts transparent so content visibly
  // slides under the floating chrome.
  expect(footer).toMatch(/background:\s*linear-gradient\(to bottom, transparent,/);
  // The overlay spans the pane's width; its gutters must not eat scroll
  // gestures aimed at the transcript behind them.
  expect(footer).toMatch(/pointer-events:\s*none/);

  const reachable = ruleIn(phone, new RegExp(`${SESSION_PANE} > \\.footer > \\*\\s*\\{`));
  expect(reachable).toMatch(/pointer-events:\s*auto/);
});

test("the transcript carries a scrollable reserve under the floating composer on the phone", () => {
  const phone = mediaBlock(sessionCss, "max-width: 899px");
  const column = ruleIn(phone, /\.contentColumn\s*\{/);
  // The reserve is the chrome's measured height - the footer ConversationBody
  // publishes - plus the home-indicator band, with the resting composer's
  // height as its pre-measure fallback.
  expect(column).toMatch(
    /--floating-composer-reserve:\s*calc\(var\(--floating-chrome-height,\s*150px\)\s*\+\s*var\(--dock-clearance\)\)/,
  );
  // The end inset row (TranscriptBody) rides on the same reserve, topped up
  // by the mounted stack's own measured clearance so the two reserves take
  // the larger instead of adding: the last turn ends at the stack's top edge,
  // never a second composer band higher.
  const inset = ruleIn(sessionCss, /\.transcriptEndInset\s*\{/);
  expect(inset).toMatch(
    /height:\s*max\(0px,\s*calc\(var\(--floating-composer-reserve\)\s*-\s*var\(--overlay-clearance,\s*0px\)\)\)/,
  );
  const dock = ruleIn(phone, /\.attentionDock\s*\{/);
  expect(dock).toMatch(/bottom:\s*calc\([\s\S]*--floating-composer-reserve/);
});

test("the composer card becomes the floating surface on the phone, and only the composer's", () => {
  const phone = mediaBlock(cardCss, "max-width: 899px");
  // Scoped to the composer's anchor so the spawn pane's shared card keeps its
  // framed look inside a scrolling form body.
  const card = ruleIn(phone, /\[data-composer\][^{]*\.card\s*\{/);
  expect(card).toMatch(/border:\s*none/);
  expect(card).toMatch(/background:\s*color-mix\(in oklab, var\(--field\)/);
  expect(card).toMatch(/backdrop-filter:\s*blur\(16px\)/);

  // The card keeps the panel radius in every state (Jesse 2026-11-23: the
  // pill is control geometry, never a surface shape) - the same value the
  // desktop card carries, so the folded one-row shape is concentric with
  // its control-radius verbs instead of colliding with them. No folded
  // radius override exists to pin: the phone block must not re-round the
  // card at all.
  expect(phone, "the folded card must not re-round on the phone").not.toMatch(
    /\[data-composer\][^{]*\.card\[data-folded="true"\]\s*\{[^}]*border-radius/,
  );
  expect(phone).not.toContain("--radius-pill");
});
