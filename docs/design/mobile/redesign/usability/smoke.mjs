#!/usr/bin/env node
// smoke.mjs — the prototype's regression check. Run it after every change
// to docs/design/mobile/redesign/prototype/ and before handing the prototype
// to usability participants.
//
// It serves the prototype, opens it in an emulated iPhone, renders every
// screen, sheet, menu and scripted event (light and dark), drives a handful
// of real UI flows by tapping, and fails on:
//   - any console error or uncaught page error,
//   - any sideways scrolling of the page or of a main scroll area,
//   - a UI flow whose expected action is missing from window.__proto.log.
// Screenshots of every state land in --out for a quick visual pass.
//
// Usage: node smoke.mjs [--out <dir>] [--only <substring>]
// Output: one PASS/FAIL line per check, then a summary and the screenshot
// directory. Exit status is non-zero on any failure.

import { spawn } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { loadPlaywright, launchChromium, newPhoneContext } from './harness/browser.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const protoDir = path.resolve(here, '../prototype');
const args = process.argv.slice(2);
const outDir = path.resolve(args.includes('--out') ? args[args.indexOf('--out') + 1] : fs.mkdtempSync(path.join(os.tmpdir(), 'evener-smoke-')));
const only = args.includes('--only') ? args[args.indexOf('--only') + 1] : null;
fs.mkdirSync(outDir, { recursive: true });

function freePort() {
  return new Promise((resolve) => { const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)); }); });
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const port = await freePort();
const server = spawn(process.execPath, [path.join(here, 'harness/serve.mjs'), '--dir', protoDir, '--port', String(port)], { stdio: 'ignore' });
let failures = 0, passes = 0, shotN = 0;
const errors = [];

async function main() {
  await sleep(400);
  const { chromium } = loadPlaywright();
  const browser = await launchChromium(chromium, () => {});
  try {
    for (const scheme of ['light', 'dark']) {
      const context = await newPhoneContext(browser, { scheme });
      const page = await context.newPage();
      page.on('console', (m) => { if (m.type() === 'error') errors.push(`[${scheme}] console: ${m.text()}`); });
      page.on('pageerror', (e) => errors.push(`[${scheme}] pageerror: ${e.message}`));
      await page.goto(`http://127.0.0.1:${port}/`, { waitUntil: 'networkidle' });
      await page.waitForFunction(() => window.EV && window.EV.S && document.querySelector('[data-screen="board"]'), null, { timeout: 15000 });
      await runChecks(page, scheme);
      await context.close();
    }
  } finally {
    await browser.close();
  }
}

async function check(name, page, fn) {
  if (only && !name.includes(only)) return;
  const before = errors.length;
  let problem = null;
  try {
    await fn();
    await sleep(450);
    const ov = await page.evaluate(() => {
      const bad = [];
      if (document.documentElement.scrollWidth > innerWidth + 1) bad.push('page scrolls sideways (' + document.documentElement.scrollWidth + 'px)');
      document.querySelectorAll('.scroll, .sheet-b').forEach((el) => {
        const r = el.getBoundingClientRect();
        if (r.width > 0 && el.scrollWidth > el.clientWidth + 1) bad.push((el.className || el.tagName) + ' scrolls sideways by ' + (el.scrollWidth - el.clientWidth) + 'px');
      });
      return bad;
    });
    if (ov.length) problem = ov.join('; ');
  } catch (e) {
    problem = e.message.split('\n')[0];
  }
  shotN++;
  const file = path.join(outDir, String(shotN).padStart(3, '0') + '-' + name.replace(/[^a-z0-9]+/gi, '-') + '.png');
  await page.screenshot({ path: file, scale: 'css' }).catch(() => {});
  if (errors.length > before) problem = (problem ? problem + '; ' : '') + errors.slice(before).join(' | ');
  if (problem) { failures++; console.log(`FAIL ${name}: ${problem}`); } else { passes++; console.log(`PASS ${name}`); }
}

const ev = (page, js) => page.evaluate(js);
const reset = (page, preset) => ev(page, `window.__proto.reset(${JSON.stringify(preset || 'default')})`).then(() => sleep(250));
const logHas = async (page, type, pred) => {
  const log = await ev(page, 'window.__proto.log');
  const hit = log.find((e) => e.type === type && (!pred || pred(e)));
  if (!hit) throw new Error(`expected a '${type}' entry in the action log; got: ${log.map((e) => e.type).join(', ')}`);
};

async function runChecks(page, scheme) {
  const S = (name) => `${scheme}/${name}`;
  await check(S('board'), page, () => reset(page));
  if (scheme === 'dark') {
    await check(S('session-working'), page, async () => { await reset(page); await ev(page, `EV.openSession('s-pr2138')`); });
    await check(S('session-question'), page, async () => { await reset(page); await ev(page, `EV.openSession('s-audit')`); });
    await check(S('reader'), page, () => reset(page, 'reading'));
    return;
  }
  await check(S('board-scrolled'), page, async () => { await reset(page); await ev(page, `document.querySelector('[data-screen="board"] .scroll').scrollTop = 700`); });
  await check(S('board-hosts'), page, async () => { await reset(page); await ev(page, `EV.S.board.organize='host'; EV.S.board.open['host:magic-kingdom']=true; EV.S.board.open['hp:magic-kingdom:evener']=true; EV.S.board.jump='projects'; EV.update()`); });
  await check(S('board-archived-open'), page, async () => { await reset(page); await ev(page, `EV.S.board.collapsed.archived=false; EV.S.board.collapsed.idle=false; EV.S.board.collapsed.testruns=false; EV.S.board.jump='archived'; EV.update()`); });
  await check(S('board-search'), page, async () => { await reset(page); await ev(page, `EV.S.board.searching=true; EV.S.board.query='caching'; EV.update()`); });
  await check(S('board-select'), page, async () => { await reset(page); await ev(page, `EV.S.board.selecting=true; EV.S.board.selected={'s-hier':true}; EV.update()`); });
  await check(S('row-menu'), page, async () => { await reset(page); await ev(page, `EV.rowMenu(EV.sess('s-audit'))`); });
  await check(S('pin-menu'), page, async () => { await reset(page); await ev(page, `EV.pinMenu(EV.sess('s-retry'))`); });
  await check(S('category-menu'), page, async () => { await reset(page); await ev(page, `EV.categoryMenu(EV.S.categories[0])`); });

  for (const id of ['s-audit', 's-mirror', 's-retry', 's-namer', 's-hier', 's-flakes', 's-pr2138', 's-tasklist', 's-gateway', 's-wasm', 's-gocache']) {
    await check(S('session-' + id), page, async () => { await reset(page); await ev(page, `EV.openSession('${id}')`); });
  }
  for (const lvl of ['Chat', 'Tools', 'Full']) {
    await check(S('detail-' + lvl), page, async () => { await reset(page); await ev(page, `EV.S.prefs.detail['s-pr2138']='${lvl}'; EV.openSession('s-pr2138')`); });
  }
  await check(S('composer-typed-working'), page, async () => { await reset(page); await ev(page, `EV.S.drafts['s-tasklist']='Also cover the empty state'; EV.openSession('s-tasklist')`); });
  await check(S('detail-menu'), page, async () => { await reset(page); await ev(page, `EV.openSession('s-pr2138'); EV.detailMenu(EV.sess('s-pr2138'))`); });
  await check(S('host-offline'), page, async () => { await reset(page, 'host-offline'); await ev(page, `EV.openSheet('host',{hostId:'paradise-park'})`); });
  await check(S('subagents'), page, async () => { await reset(page); await ev(page, `EV.openSession('s-pr2138'); EV.push('subagents',{sessionId:'s-pr2138'})`); });
  await check(S('subagent-failed'), page, async () => { await reset(page); await ev(page, `EV.push('subagent',{sessionId:'s-pr2138',subId:'g-settle'})`); });
  await check(S('reader'), page, () => reset(page, 'reading'));
  await check(S('artifact'), page, async () => { await reset(page); await ev(page, `EV.push('artifact',{id:'a-hier',sessionId:'s-hier'})`); await sleep(1500); });

  const sheets = [
    ['launch', `EV.openNew('smoke')`], ['launch-plugins', `EV.openNew('smoke'); EV.openSheet('pickPlugins',{})`], ['launch-model', `EV.openNew('smoke'); EV.openSheet('model',{target:'launch'})`],
    ['launch-host', `EV.openNew('smoke'); EV.openSheet('pickHost',{})`], ['launch-project', `EV.openNew('smoke'); EV.openSheet('pickProject',{})`], ['launch-access', `EV.openNew('smoke'); EV.openSheet('pickAccess',{})`],
    ['launch-branch', `EV.openNew('smoke'); EV.openSheet('pickBranch',{})`], ['launch-more', `EV.openNew('smoke'); EV.openSheet('moreOptions',{})`], ['launch-save', `EV.openNew('smoke'); EV.openSheet('saveRecipe',{})`],
    ['hub', `EV.openSheet('hub',{})`], ['hosts', `EV.openSheet('hub',{}); EV.openSheet('hosts',{})`], ['host', `EV.openSheet('host',{hostId:'paradise-park'})`],
    ['providers', `EV.openSheet('providers',{})`], ['provider', `EV.openSheet('provider',{providerId:'codex-jesse-fsck.com'})`], ['signin', `EV.openSheet('signin',{provider:'codex-jesse-fsck.com'})`],
    ['plugins', `EV.openSheet('plugins',{})`], ['recipes', `EV.openSheet('recipes',{})`], ['display', `EV.openSheet('display',{})`], ['alerts', `EV.openSheet('alerts',{})`], ['hubs', `EV.openSheet('hubs',{})`],
    ['session-info', `EV.openSession('s-pr2138'); EV.openSheet('session',{sessionId:'s-pr2138'})`], ['model-session', `EV.openSession('s-pr2138'); EV.openSheet('model',{target:'session',sessionId:'s-pr2138'})`],
    ['commands', `EV.openSession('s-pr2138'); EV.openSheet('commands',{sessionId:'s-pr2138'})`], ['tasks', `EV.openSheet('tasks',{sessionId:'s-pr2138'})`], ['goal', `EV.openSheet('goal',{sessionId:'s-pr2138'})`],
    ['notes', `EV.openSession('s-pr2138'); EV.openSheet('notes',{sessionId:'s-pr2138'})`], ['notes-links-only', `EV.openSession('s-hier'); EV.openSheet('notes',{sessionId:'s-hier'})`],
    ['notes-empty', `EV.openSession('s-tasklist'); EV.openSheet('notes',{sessionId:'s-tasklist'})`], ['notes-ended', `EV.openSheet('notes',{sessionId:'s-roster'})`],
    ['browser', `EV.openSheet('browser',{url:'https://github.com/prime-radiant-inc/evener/pull/2138',label:'PR #2138'})`], ['find', `EV.openSheet('find',{sessionId:'s-pr2138'})`], ['aside', `EV.openSheet('aside',{sessionId:'s-pr2138'})`],
    ['files', `EV.openSession('s-hier'); EV.openSheet('files',{sessionId:'s-hier'})`], ['queue', `EV.S.queue['s-tasklist']=[{id:'q1',text:'Also cover the empty state'}]; EV.openSheet('queue',{sessionId:'s-tasklist'})`],
    ['stop-subagent', `EV.push('subagent',{sessionId:'s-pr2138',subId:'g-settle'}); EV.openSheet('stopSub',{sessionId:'s-pr2138',subId:'g-settle'})`],
    ['comment', `EV.openSheet('comment',{path:'docs/superpowers/plans/2026-09-25-host-project-hierarchy.md',sessionId:'s-hier',block:3})`],
    ['review', `EV.S.comments['docs/superpowers/plans/2026-09-25-host-project-hierarchy.md']=[{block:3,text:'Say which host is local'}]; EV.openSheet('review',{path:'docs/superpowers/plans/2026-09-25-host-project-hierarchy.md',sessionId:'s-hier'})`],
    ['outline', `EV.openSheet('outline',{path:'docs/superpowers/plans/2026-09-25-host-project-hierarchy.md'})`],
    ['proposal', `EV.openSheet('proposal',{artifactId:'a-hier',sessionId:'s-hier',text:'Go with layout B (host badges inside projects).'})`],
    ['new-category', `EV.openSheet('newCategory',{sessionId:'s-retry'})`], ['rename', `EV.openSheet('rename',{sessionId:'s-retry'})`],
  ];
  for (const [name, js] of sheets) await check(S('sheet-' + name), page, async () => { await reset(page); await ev(page, js); });

  for (const e of ['question', 'failure', 'many', 'host-offline', 'reconnecting', 'approval', 'finish']) {
    await check(S('event-' + e), page, async () => { await reset(page); await ev(page, `window.__proto.trigger('${e}')`); await sleep(1200); });
  }
  await check(S('event-held-while-reading'), page, async () => {
    await reset(page, 'reading'); await ev(page, `window.__proto.trigger('question')`); await sleep(300); await logHas(page, 'banner_held');
    const badge = await page.locator('[data-screen="reader"] .back-btn .badge').textContent().catch(() => null);
    if (badge !== '1') throw new Error('the Reader\'s Back should count the held alert, got ' + JSON.stringify(badge));
  });

  // Real UI flows, by tapping, asserted against the action log.
  const tapRole = async (role, name) => { const loc = page.getByRole(role, { name, exact: false }).first(); await loc.waitFor({ timeout: 4000 }); await loc.tap(); await sleep(250); };
  const tap = async (text) => { const loc = page.getByText(text, { exact: true }).first(); await loc.waitFor({ timeout: 4000 }); await loc.tap(); await sleep(250); };
  // A finger held down long enough for a long-press, then lifted.
  const holdAt = async (x, y) => {
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
    await sleep(700);
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  };
  await check(S('flow-answer-question'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-audit')`); await sleep(400);
    await tapRole('radio', 'Drop them'); await tapRole('button', 'Next question'); await tapRole('checkbox', 'Job tools'); await tapRole('button', 'Send answers');
    await logHas(page, 'answer', (e) => e.answers[0] === 'Drop them');
  });
  await check(S('flow-other-answer-brings-composer'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-audit')`); await sleep(400);
    if (await page.locator('textarea[aria-label="Message"]').count()) throw new Error('the composer shows while the question dock is open');
    await tapRole('button', 'Other answer');
    const focused = await ev(page, 'document.activeElement && document.activeElement.getAttribute("aria-label")');
    if (focused !== 'Message') throw new Error('Other answer… did not put the cursor in the composer (focused: ' + focused + ')');
  });
  await check(S('flow-approve'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-mirror')`); await sleep(400);
    await tapRole('button', 'Allow this file only');
    await logHas(page, 'approval', (e) => e.decision === 'allow');
  });
  await check(S('flow-steer-and-queue'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-tasklist')`); await sleep(400);
    await page.locator('textarea[aria-label="Message"]').tap(); await page.keyboard.type('Also cover the empty state');
    await page.getByRole('button', { name: 'Queue', exact: true }).tap(); await sleep(250);
    await logHas(page, 'send', (e) => e.mode === 'queue');
    await page.locator('textarea[aria-label="Message"]').tap(); await page.keyboard.type('Stop and switch to the Tasks panel');
    await page.getByRole('button', { name: 'Steer', exact: true }).tap(); await sleep(250);
    await logHas(page, 'send', (e) => e.mode === 'steer');
  });
  await check(S('flow-row-tap-and-next'), page, async () => {
    await reset(page); await tap('Host Project Hierarchy UI Mockups');
    await logHas(page, 'row_tap', (e) => e.sessionId === 's-hier');
    await page.getByRole('button', { name: 'Go to the next session that needs you' }).tap(); await sleep(400);
    await logHas(page, 'next', (e) => e.from === 's-hier' && !!e.to);
  });
  await check(S('flow-next-hidden-while-asking'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-audit')`); await sleep(400);
    const n = await page.locator('.next-cap').count();
    if (n) throw new Error('the Next capsule shows while the question dock is open');
  });
  await check(S('flow-hold-next-for-list'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-hier')`); await sleep(400);
    const box = await page.locator('.next-cap').boundingBox();
    await holdAt(box.x + box.width / 2, box.y + box.height / 2);
    await sleep(300);
    await logHas(page, 'needs_list_open', (e) => e.how === 'hold');
    const log = await ev(page, 'window.__proto.log');
    if (log.some((e) => e.type === 'next')) throw new Error('holding Next also went to the next session');
  });
  // A finger dragged in from the left edge, the way the back gesture starts.
  const edgeSwipe = async (y) => {
    const cdp = await page.context().newCDPSession(page);
    const pts = (x) => [{ x, y }];
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: pts(3) });
    for (let x = 20; x <= 300; x += 20) await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: pts(x) });
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
    await sleep(400);
  };
  await check(S('flow-edge-swipe-never-archives'), page, async () => {
    await reset(page);
    await edgeSwipe(430);
    const log = await ev(page, 'window.__proto.log');
    if (log.some((e) => e.type === 'archive')) throw new Error('an edge swipe on the Board archived a row');
  });
  await check(S('flow-edge-back-one-level'), page, async () => {
    await reset(page, 'reading'); await sleep(300);
    const before = await ev(page, 'EV.S.nav.map((n) => n.name).join(">")');
    await edgeSwipe(300);
    const after = await ev(page, 'EV.S.nav.map((n) => n.name).join(">")');
    const want = before.split('>').slice(0, -1).join('>');
    if (after !== want) throw new Error(`one back swipe went from ${before} to ${after}, expected ${want}`);
  });
  await check(S('flow-edge-back-in-stacked-sheet'), page, async () => {
    await reset(page); await ev(page, `EV.openNew('smoke'); EV.openSheet('pickPlugins',{})`); await sleep(400);
    await edgeSwipe(500);
    const kinds = await ev(page, 'EV.S.sheets.map((s) => s.kind).join(",")');
    if (kinds !== 'launch') throw new Error(`a back swipe in the plugin picker left sheets [${kinds}], expected [launch]`);
  });
  await check(S('flow-scoped-approval'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-mirror')`); await sleep(400);
    await tapRole('button', 'Allow all of');
    await logHas(page, 'approval', (e) => e.decision === 'allow_scope');
  });
  await check(S('flow-continue-reading'), page, async () => {
    await reset(page, 'reading'); await sleep(300);
    await ev(page, `EV.popToBoard()`); await sleep(500);
    await tapRole('button', 'Continue reading');
    await logHas(page, 'continue_reading');
  });
  await check(S('flow-comment-on-low-paragraph'), page, async () => {
    await reset(page, 'reading'); await sleep(300);
    const y = await ev(page, `(() => { const sc = document.querySelector('[data-screen="reader"] .scroll'); sc.scrollTop = sc.scrollHeight; const b = [...document.querySelectorAll('[data-screen="reader"] .rblock')].pop().getBoundingClientRect(); return Math.round((b.top + b.bottom) / 2); })()`);
    await sleep(200);
    // Hold like a finger, then lift: the lift must not close or trigger the menu.
    await holdAt(200, y);
    await sleep(100);
    const open = await ev(page, '!!EV.S.menu');
    if (!open) throw new Error('the long-press menu closed as soon as the finger lifted');
    await tapRole('menuitem', 'Comment');
    await logHas(page, 'sheet', (e) => e.kind === 'comment');
  });
  // Taps inside the topmost sheet only: the screens under it stay in the DOM.
  const tapInSheet = async (role, name, exact = false) => { const loc = page.locator('.layer').last().getByRole(role, { name, exact }).first(); await loc.waitFor({ timeout: 4000 }); await loc.tap(); await sleep(250); };
  await check(S('flow-notes-bar-opens-link'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-pr2138')`); await sleep(400);
    await tapRole('button', 'Notes and links');
    await tapInSheet('button', 'PR #2138');
    await logHas(page, 'link_open', (e) => e.url.endsWith('/pull/2138'));
    const kinds = await ev(page, 'EV.S.sheets.map((s) => s.kind).join(",")');
    if (kinds !== 'notes,browser') throw new Error(`expected the browser over the notes sheet, got [${kinds}]`);
  });
  await check(S('flow-file-link-opens-reader'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-pr2138'); EV.openSheet('notes',{sessionId:'s-pr2138'})`); await sleep(400);
    await tapInSheet('button', 'Settle race plan');
    const top = await ev(page, 'EV.S.nav[EV.S.nav.length - 1]');
    if (top.name !== 'reader' || !top.path.endsWith('settle-race.md')) throw new Error('the file link did not open the plan in the Reader: ' + JSON.stringify(top));
  });
  await check(S('flow-note-saves-on-close'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-hier'); EV.openSheet('notes',{sessionId:'s-hier'})`); await sleep(400);
    await page.locator('textarea[aria-label="Your note"]').tap(); await page.keyboard.type('Prefer layout B');
    await tapInSheet('button', 'Done', true); await sleep(300);
    await logHas(page, 'note_set', (e) => e.text === 'Prefer layout B' && e.woke === true);
    const toast = await ev(page, 'EV.S.toast && EV.S.toast.text');
    if (!/Note saved/.test(toast || '')) throw new Error('closing the sheet saved silently; toast was ' + JSON.stringify(toast));
  });
  await check(S('flow-note-blur-waits'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-hier'); EV.openSheet('notes',{sessionId:'s-hier'})`); await sleep(400);
    await page.locator('textarea[aria-label="Your note"]').tap(); await page.keyboard.type('Prefer layout B');
    await ev(page, 'document.activeElement.blur()'); await sleep(300);
    await logHas(page, 'note_leave', (e) => e.how === 'blur');
    if (await ev(page, 'window.__proto.log.some((e) => e.type === "note_set")')) throw new Error('leaving the field inside the sheet saved at once');
    await page.waitForFunction(() => window.__proto.log.some((e) => e.type === 'note_set'), null, { timeout: 12000 });
  });
  await check(S('flow-notes-bar-edits-your-note'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-pr2138')`); await sleep(400);
    await tapRole('button', 'Notes and links'); await sleep(300);
    const at = await ev(page, '(() => { const t = document.activeElement; return t && t.getAttribute("aria-label") === "Your note" ? [t.selectionStart, t.value.length] : null; })()');
    if (!at || at[0] !== at[1]) throw new Error('the notes bar should open your note ready to add to, cursor at the end; got ' + JSON.stringify(at));
  });
  await check(S('flow-next-goes-to-the-alert'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-hier')`); await sleep(400);
    await ev(page, `window.__proto.trigger('question')`); await sleep(400);
    const alerted = await ev(page, 'EV.S.recent[0]');
    await page.getByRole('button', { name: 'Go to the next session that needs you' }).tap(); await sleep(400);
    await logHas(page, 'next', (e) => e.from === 's-hier' && e.to === alerted);
    const stack = await ev(page, 'EV.S.nav.map((n) => n.name + (n.id ? ":" + n.id : "")).join(">")');
    if (stack !== 'board>session:s-hier>session:' + alerted) throw new Error('Next should push, so Back returns to s-hier; stack is ' + stack);
  });
  await check(S('flow-remove-link'), page, async () => {
    await reset(page); await ev(page, `EV.openSession('s-pr2138'); EV.openSheet('notes',{sessionId:'s-pr2138'})`); await sleep(400);
    await ev(page, `EV.linkMenu(EV.sess('s-pr2138'), EV.sess('s-pr2138').urls[1])`); await sleep(300);
    await tapRole('menuitem', 'Remove link');
    await logHas(page, 'link_remove', (e) => e.url.endsWith('/checks'));
  });
  await check(S('flow-board-file-back-to-session'), page, async () => {
    await reset(page);
    await page.locator('[data-session="s-hier"] .att').first().tap(); await sleep(500);
    const stack = await ev(page, 'EV.S.nav.map((n) => n.name).join(">")');
    if (stack !== 'board>session>reader') throw new Error('a plan opened from the Board should sit on its session; the stack is ' + stack);
  });
  await check(S('flow-comment-marker-on-its-item'), page, async () => {
    await reset(page, 'reading'); await sleep(300);
    // Find a list block, and comment on its second item the way the comment sheet records it.
    const where = await ev(page, `(() => { const b = [...document.querySelectorAll('[data-screen="reader"] .rblock')].find((x) => x.querySelectorAll('li').length > 1); return b ? +b.dataset.block : null; })()`);
    if (where == null) throw new Error('no list in the reading preset');
    await ev(page, `EV.S.comments['docs/superpowers/plans/2026-09-25-host-project-hierarchy.md'] = [{ block: ${where}, item: 1, quote: 'x', text: 'y' }]; EV.update()`); await sleep(300);
    const marked = await ev(page, `[...document.querySelectorAll('[data-screen="reader"] .rblock[data-block="${where}"] li')].map((li) => !!li.querySelector('.cmark'))`);
    if (!(marked[1] && !marked[0])) throw new Error('the marker should sit on the second item only; got ' + JSON.stringify(marked));
  });
  await check(S('flow-launch'), page, async () => {
    await reset(page); await page.locator('button[aria-label="New session"]').tap(); await sleep(400);
    await page.keyboard.type('Profile why the roster load is slow');
    await tapRole('button', 'Start session');
    await logHas(page, 'start_session', (e) => e.prompt.includes('roster'));
  });
}

try {
  await main();
} catch (e) {
  failures++;
  console.log('FAIL harness: ' + e.message);
} finally {
  server.kill();
}
console.log(`\n${passes} passed, ${failures} failed · screenshots: ${outDir}`);
process.exit(failures ? 1 : 0);
