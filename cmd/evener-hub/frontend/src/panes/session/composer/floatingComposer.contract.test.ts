// @vitest-environment node
import { expect, test } from "vitest";
import { blockBody, escapeRegExp, readModuleCss, topRuleBlock } from "../../../styles/cssBlock";
import { DUO_COVER_QUERY } from "./narrowComposer";

// The floating composer (docs/web-ui/design-system.md §12) is pure stylesheet
// geometry - jsdom lays out nothing, so the phone rules are pinned off disk
// the same way display-gates.test.ts pins its gates. What is pinned:
//
//  - PaneScaffold's phone block floats a session pane's footer over the body:
//    the root passes pointer events through its gutters to the scroll behind
//    it (its children take taps again). The phone layoutguard checks the
//    browser's rendered gradient scrim.
//  - The session pane's own stylesheet sizes the scrollable reserve that lets
//    the last turn clear the floating composer: the end inset row measures to
//    the chrome's measured height plus the home-indicator band in mobile
//    sessions, topped up by the floating stack's own clearance, and the
//    dormant attention dock and live FlowOverlay stack ride above the same
//    reserve.
//  - PromptCard's phone block turns the composer card - and only the
//    composer's, keyed on [data-composer] so the spawn form keeps its framed
//    card - into the floating surface: no border (the outline focus ring is
//    the one line it draws), a translucent field color over a 16px backdrop
//    blur, and the card keeps its panel radius in every state.
//  - Browser composer and transcript surfaces use the radius ladder.
const scaffoldCss = readModuleCss(import.meta.url, "../../../widgets/panescaffold/panescaffold.module.css");
const cardCss = readModuleCss(import.meta.url, "../../../widgets/promptcard/promptcard.module.css");
const sessionCss = readModuleCss(import.meta.url, "../session.module.css");
const flowOverlayCss = readModuleCss(import.meta.url, "../transcript/flow/flowoverlay.module.css");
const askDockCss = readModuleCss(import.meta.url, "./askDock/askdock.module.css");
const loadOlderCss = readModuleCss(import.meta.url, "../transcript/flow/loadolderrow.module.css");
const userMessageCss = readModuleCss(import.meta.url, "../transcript/messages/usermessageitem.module.css");

// The session-scoped selector panescaffold's floating rules key on: session
// panes without an edge footer (mobile/tablet/Duo). Narrow desktop Dockview
// popouts (<900px) still render a StatusBar edgeFooter and keep the composer
// in flow above it.
const SESSION_PANE = escapeRegExp('.pane:has(> .body[data-pane-scaffold^="session:"]):not(:has(> .edgeFooter))');

// The block of the first rule in `block` whose selector text matches
// `selectorPattern` (callers pass the pattern; the selector is CSS Modules
// source, not a literal, so no escapeRegExp passthrough here).
function ruleIn(block: string, selectorPattern: RegExp): string {
  const match = selectorPattern.exec(block);
  if (!match) throw new Error(`no rule matching ${selectorPattern.source}`);
  return blockBody(block, block.indexOf("{", match.index));
}

test("a phone session pane floats its footer over the transcript", () => {
  // The pane itself must establish a non-scrolling containing block so an
  // absolutely positioned footer anchors to the pane in narrow/split hosts
  // rather than escaping to the viewport, and focus-into-view on a bottom-flush
  // button cannot scroll .pane and lift the footer off the bottom edge.
  const pane = ruleIn(scaffoldCss, new RegExp(`${SESSION_PANE}\\s*\\{`));
  expect(pane).toMatch(/position:\s*relative/);
  expect(pane).toMatch(/overflow:\s*clip/);
  const footer = ruleIn(scaffoldCss, new RegExp(`${SESSION_PANE} > \\.footer\\s*\\{`));
  expect(footer).toMatch(/position:\s*absolute/);
  // The overlay spans the pane's width; its gutters must not eat scroll
  // gestures aimed at the transcript behind them.
  expect(footer).toMatch(/pointer-events:\s*none/);

  const reachable = ruleIn(scaffoldCss, new RegExp(`${SESSION_PANE} > \\.footer > \\*\\s*\\{`));
  expect(reachable).toMatch(/pointer-events:\s*auto/);

  // The Duo-cover footer override must carry the same edge-footer exemption so
  // a narrow desktop popout in that width/aspect band keeps .footerBeforeEdge.
  const duo = ruleIn(scaffoldCss, new RegExp(`@media ${escapeRegExp(DUO_COVER_QUERY)}\\s*\\{`));
  const duoFooter = ruleIn(duo, new RegExp(`${SESSION_PANE} > \\.footer\\s*\\{`));
  expect(duoFooter).toMatch(/padding-bottom:\s*var\(--dock-clearance\)/);

  const UNGATED_SESSION_PANE = escapeRegExp('.pane:has(> .body[data-pane-scaffold^="session:"])');
  expect(scaffoldCss, "floating rules must exempt panes carrying an edgeFooter").not.toMatch(
    new RegExp(`${UNGATED_SESSION_PANE}\\s*(?:\\{|>\\s*\\.footer\\b)`),
  );
});

test("the transcript carries a scrollable reserve under the floating composer on the phone", () => {
  // Scoped to session panes so cascade columns (data-pane-scaffold="cascade",
  // which keep an in-flow footer) retain --floating-composer-reserve: 0px.
  expect(topRuleBlock(sessionCss, ".contentColumn")).toMatch(/--floating-composer-reserve:\s*0px/);
  const SESSION_BODY = escapeRegExp(':global([data-pane-scaffold^="session:"]):not([data-has-edge-footer="true"])');
  const column = ruleIn(sessionCss, new RegExp(`${SESSION_BODY}\\s+\\.contentColumn\\s*\\{`));
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
  const dock = ruleIn(sessionCss, /\.attentionDock\s*\{/);
  expect(dock).toMatch(/bottom:\s*calc\([\s\S]*--floating-composer-reserve/);

  // The live FlowOverlay stack anchors to bottom: 0 and carries its gutter,
  // composer, and home-indicator clearance in padding-bottom so ResizeObserver
  // on border-box re-measures whenever any inset changes.
  const stack = ruleIn(flowOverlayCss, /\.stack\s*\{/);
  expect(stack).toMatch(/bottom:\s*0/);
  expect(stack).toMatch(/padding-bottom:\s*calc\(var\(--space-3\)\s*\+\s*var\(--floating-composer-reserve,\s*0px\)\)/);
});

test("the composer card becomes the floating surface on the phone, and only the composer's", () => {
  // Scoped to the composer's anchor so the spawn pane's shared card keeps its
  // framed look inside a scrolling form body.
  const card = ruleIn(cardCss, /\[data-composer\][^{]*\.card\s*\{/);
  expect(card).toMatch(/border:\s*none/);
  expect(card).toMatch(/background:\s*transparent/);
  // Never put backdrop-filter on .card itself: a non-none backdrop-filter makes
  // .card a containing block for position: fixed descendants, trapping inline
  // SessionMenu dialogs (Rename, Shut down, Force stop, Delete) inside the card.
  expect(card).not.toMatch(/backdrop-filter/);

  const surface = ruleIn(cardCss, /\[data-composer\][^{]*\.card::before\s*\{/);
  expect(surface).toMatch(/position:\s*absolute/);
  expect(surface).toMatch(/inset:\s*0/);
  expect(surface).toMatch(/border-radius:\s*inherit/);
  expect(surface).toMatch(/pointer-events:\s*none/);
  expect(surface).toMatch(/background:\s*color-mix\(in oklab, var\(--field\)/);
  expect(surface).toMatch(/backdrop-filter:\s*blur\(16px\)/);

  // Direct flex children (.field, .controls) take z-index: 0 so they paint
  // above ::before in tree order without isolation: isolate (which would form
  // a backdrop root and stop ::before from blurring the transcript) and
  // without position: relative (which would trap .attachSlot inside .controls).
  const children = ruleIn(cardCss, /\[data-composer\][^{]*\.card\s*>\s*\*\s*\{/);
  expect(children).toMatch(/z-index:\s*0/);
  expect(children).not.toMatch(/position:\s*relative/);

  // The card keeps the panel radius in every state: the pill is control
  // geometry, never a surface shape, so the folded one-row shape stays
  // concentric with its control-radius verbs instead of colliding with them.
  expect(cardCss, "the folded card must not re-round on the phone").not.toMatch(
    /\[data-composer\][^{]*\.card\[data-folded="true"\]\s*\{[^}]*border-radius/,
  );
});

test("browser session composer and transcript chrome carry ladder radii", () => {
  expect(topRuleBlock(cardCss, ".card")).toContain("border-radius: var(--radius-panel)");
  expect(askDockCss, "askdock.module.css must not shape a tab with --radius-pill").not.toContain("--radius-pill");
  expect(loadOlderCss, "loadolderrow.module.css must not shape a row with --radius-pill").not.toContain(
    "--radius-pill",
  );
  expect(userMessageCss, "usermessageitem.module.css must not shape the railmark with --radius-pill").not.toContain(
    "--radius-pill",
  );

  expect(ruleIn(askDockCss, /\.tab\s*\{/)).toContain("var(--radius-control)");
  expect(ruleIn(loadOlderCss, /\.row\s*\{/)).toContain("var(--radius-pane)");
  expect(ruleIn(userMessageCss, /\.provisional\s+\.railmark\s*\{/)).toContain("var(--radius-control)");
});
