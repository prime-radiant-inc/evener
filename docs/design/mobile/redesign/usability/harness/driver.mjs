#!/usr/bin/env node
// driver.mjs — long-running per-participant browser driver.
//
// Launches one headless Chromium tab emulating an iPhone, points it at the
// prototype, and then sits there as an HTTP server accepting one JSON
// command at a time from phone.mjs (the CLI a usability-test "participant"
// types into). One driver = one participant = one browser tab = one output
// directory. State (screenshot counter, current task, scheduled events,
// collected console/page errors) lives in memory for the life of the
// process and is flushed to --out on `done` / `finish`.
//
// Usage:
//   node driver.mjs --port <port> --url <url> --out <dir> \
//       [--scheme light|dark] [--tasks <tasks.json>]
//
// Playwright is not a project dependency here (this directory has none by
// design). It's installed globally, so it's loaded from the global root.

import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { PHONE_WIDTH, PHONE_HEIGHT, loadPlaywright, launchChromium, newPhoneContext } from './browser.mjs';

const HELP = `driver.mjs — long-running per-participant browser driver

Usage:
  node driver.mjs --port <port> --url <url> --out <dir> \\
      [--scheme light|dark] [--tasks <tasks.json>]

Options:
  --port <n>       Port to listen on for commands from phone.mjs, 127.0.0.1
                    only (required).
  --url <url>      URL of the prototype to open, normally what serve.mjs
                    prints (required).
  --out <dir>      Directory to write screenshots, actions.jsonl, task logs
                    and summary.json into. Created if missing (required).
  --scheme <name>  'light' (default) or 'dark' — emulated color scheme.
  --tasks <path>   JSON file describing moderator-scripted tasks. Required
                    only if the session uses the 'task' command.
  --help           Show this help.
`;

function parseArgs(argv) {
  const args = { port: null, url: null, out: null, scheme: 'light', tasks: null, help: false };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--port') args.port = Number(argv[++i]);
    else if (a === '--url') args.url = argv[++i];
    else if (a === '--out') args.out = argv[++i];
    else if (a === '--scheme') args.scheme = argv[++i];
    else if (a === '--tasks') args.tasks = argv[++i];
    else if (a === '--help' || a === '-h') args.help = true;
  }
  return args;
}

const args = parseArgs(process.argv.slice(2));
if (args.help || !args.port || Number.isNaN(args.port) || !args.url || !args.out) {
  process.stdout.write(HELP);
  process.exit(args.help ? 0 : 1);
}
if (args.scheme !== 'light' && args.scheme !== 'dark') {
  console.error(`driver.mjs: --scheme must be 'light' or 'dark', got '${args.scheme}'`);
  process.exit(1);
}

fs.mkdirSync(args.out, { recursive: true });

let tasks = null;
if (args.tasks) {
  try {
    tasks = JSON.parse(fs.readFileSync(args.tasks, 'utf8'));
  } catch (err) {
    console.error(`driver.mjs: could not read --tasks '${args.tasks}': ${err.message}`);
    process.exit(1);
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function slugify(s) {
  const slug = String(s)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40);
  return slug || 'x';
}

function findTask(list, k) {
  const byId = list.find((t) => t.id === k);
  if (byId) return byId;
  const n = Number(k);
  if (Number.isInteger(n) && n >= 1 && n <= list.length) return list[n - 1];
  return null;
}


// Helpers injected into the page itself (via addInitScript, so they survive
// navigations) rather than redefined inline in every page.evaluate call.
// window.__harnessHelpers is the harness's own instrumentation; it has
// nothing to do with window.__proto, which belongs to the prototype.
function installHarnessHelpers() {
  function isVisible(el) {
    if (!(el instanceof Element)) return false;
    const style = getComputedStyle(el);
    if (style.display === 'none' || style.visibility === 'hidden' || Number(style.opacity) === 0) return false;
    const rect = el.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  }

  // True when the element (or something inside it) is what a finger at its
  // center would actually touch, i.e. it isn't covered by a sheet, scrim or menu.
  // Returns the point a finger would use to touch the element: somewhere in
  // its visible part (clipped to the screen) that isn't covered by a sheet,
  // scrim, menu or bar. Null when no such point exists.
  function touchPoint(el, rect) {
    const left = Math.max(rect.left, 0), right = Math.min(rect.right, window.innerWidth);
    const top = Math.max(rect.top, 0), bottom = Math.min(rect.bottom, window.innerHeight);
    if (right - left < 2 || bottom - top < 2) return null;
    const xs = [(left + right) / 2, left + (right - left) * 0.25, left + (right - left) * 0.75];
    const ys = [(top + bottom) / 2, top + (bottom - top) * 0.25, top + (bottom - top) * 0.75];
    for (const y of ys) {
      for (const x of xs) {
        const hit = document.elementFromPoint(x, y);
        if (hit && (hit === el || el.contains(hit))) return { x: Math.round(x), y: Math.round(y) };
      }
    }
    return null;
  }
  function reachable(el, rect) {
    return touchPoint(el, rect) != null;
  }

  function inViewport(rect) {
    return rect.right > 0 && rect.bottom > 0 && rect.left < window.innerWidth && rect.top < window.innerHeight;
  }

  function accessibleName(el) {
    const aria = el.getAttribute && el.getAttribute('aria-label');
    if (aria && aria.trim()) return aria.trim();
    const labelledby = el.getAttribute && el.getAttribute('aria-labelledby');
    if (labelledby) {
      const txt = labelledby
        .split(/\s+/)
        .map((id) => {
          const t = document.getElementById(id);
          return t ? t.textContent.trim() : '';
        })
        .filter(Boolean)
        .join(' ');
      if (txt) return txt;
    }
    if (el.tagName === 'IMG') {
      const alt = el.getAttribute('alt');
      if (alt && alt.trim()) return alt.trim();
    }
    if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') {
      if (el.labels && el.labels.length) {
        const txt = Array.from(el.labels)
          .map((l) => l.textContent.trim())
          .filter(Boolean)
          .join(' ');
        if (txt) return txt;
      }
      if (el.placeholder && el.placeholder.trim()) return el.placeholder.trim();
      if ((el.type === 'submit' || el.type === 'button') && el.value) return el.value.trim();
    }
    const title = el.getAttribute && el.getAttribute('title');
    if (title && title.trim()) return title.trim();
    return (el.textContent || '').trim();
  }

  function isClickable(el) {
    if (!(el instanceof Element)) return false;
    const tag = el.tagName;
    if (tag === 'BUTTON' || tag === 'A' || tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true;
    if (el.getAttribute('role') === 'button') return true;
    if (el.hasAttribute('onclick')) return true;
    try {
      if (getComputedStyle(el).cursor === 'pointer') return true;
    } catch {
      /* detached or exotic element; not clickable */
    }
    return false;
  }

  // Climbs from a text match to the nearest clickable ancestor (inclusive).
  // Falls back to the original element when nothing clickable is found, so
  // a plain text hit still resolves to *something* tappable.
  function findTappable(el) {
    let cur = el;
    let hops = 0;
    while (cur && cur !== document.body && hops < 8) {
      if (isClickable(cur)) return cur;
      cur = cur.parentElement;
      hops += 1;
    }
    return el;
  }

  function roleOf(el) {
    const explicit = el.getAttribute && el.getAttribute('role');
    if (explicit) return explicit;
    switch (el.tagName) {
      case 'BUTTON':
        return 'button';
      case 'A':
        return 'link';
      case 'SELECT':
        return 'combobox';
      case 'TEXTAREA':
        return 'textbox';
      case 'INPUT': {
        const t = (el.getAttribute('type') || 'text').toLowerCase();
        if (t === 'checkbox') return 'checkbox';
        if (t === 'radio') return 'radio';
        if (t === 'submit' || t === 'button') return 'button';
        return 'textbox';
      }
      case 'H1':
      case 'H2':
      case 'H3':
      case 'H4':
      case 'H5':
      case 'H6':
        return 'heading';
      default:
        return el.tagName.toLowerCase();
    }
  }

  const TEXT_SELECTOR =
    'button, a, [role], input, textarea, select, label, [onclick], [tabindex], li, h1, h2, h3, h4, h5, h6, p, span, div, td, th, img, svg';

  // Finds elements whose visible text or accessible name matches `text`
  // (exact match wins over substring), dedupes to the tappable target when
  // climb=true, and returns candidates sorted smallest-area first with an
  // onScreen flag so the caller can apply the "only tap what's visible"
  // rule and the "prefer the smallest match" rule.
  function findByText(text, { climb }) {
    const wanted = text.trim().toLowerCase();
    const all = Array.from(document.querySelectorAll(TEXT_SELECTOR));
    const scored = [];
    for (const el of all) {
      if (!isVisible(el)) continue;
      const names = [accessibleName(el)];
      if ((el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') && el.placeholder) names.push(el.placeholder.trim());
      const lowers = names.filter(Boolean).map((n) => n.toLowerCase());
      if (!lowers.length) continue;
      const isExact = lowers.some((l) => l === wanted || l.replace(/[.…]+$/, '') === wanted.replace(/[.…]+$/, ''));
      const isSub = lowers.some((l) => l.includes(wanted.replace(/[.…]+$/, '')));
      if (!isExact && !isSub) continue;
      const target = climb ? findTappable(el) : el;
      const rect = target.getBoundingClientRect();
      const inView = inViewport(rect);
      const point = inView ? touchPoint(target, rect) : null;
      scored.push({ el: target, exact: isExact, rect, point, onScreen: !!point, covered: inView && !point, area: rect.width * rect.height });
    }
    const byEl = new Map();
    for (const c of scored) {
      const prev = byEl.get(c.el);
      if (!prev || (c.exact && !prev.exact)) byEl.set(c.el, c);
    }
    let candidates = Array.from(byEl.values());
    if (candidates.some((c) => c.exact)) candidates = candidates.filter((c) => c.exact);
    candidates.sort((a, b) => a.area - b.area);
    return candidates.map((c) => ({
      tag: c.el.tagName.toLowerCase(),
      role: roleOf(c.el),
      name: accessibleName(c.el).slice(0, 60),
      cx: c.point ? c.point.x : Math.round(c.rect.left + c.rect.width / 2),
      cy: c.point ? c.point.y : Math.round(c.rect.top + c.rect.height / 2),
      area: c.area,
      onScreen: c.onScreen,
      covered: c.covered,
    }));
  }

  function listVisible() {
    const SEL =
      'button, a, [role=button], [role=tab], [role=switch], [role=checkbox], [role=menuitem], [role=option], [tabindex], h1, h2, h3, h4, h5, h6';
    const rows = [];
    for (const el of document.querySelectorAll(SEL)) {
      if (!isVisible(el)) continue;
      const rect = el.getBoundingClientRect();
      if (!inViewport(rect) || !reachable(el, rect)) continue;
      rows.push({
        role: roleOf(el),
        name: accessibleName(el).slice(0, 60),
        x: Math.round(rect.left + rect.width / 2),
        y: Math.round(rect.top + rect.height / 2),
        top: rect.top,
        left: rect.left,
      });
    }
    rows.sort((a, b) => a.top - b.top || a.left - b.left);
    return rows.slice(0, 40).map(({ role, name, x, y }) => ({ role, name, x, y }));
  }

  function activeElementIsEditable() {
    const el = document.activeElement;
    if (!el) return false;
    if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') return true;
    return !!el.isContentEditable;
  }

  window.__harnessHelpers = { findByText, listVisible, activeElementIsEditable };
}

async function main() {
  const { chromium } = loadPlaywright();
  const browser = await launchChromium(chromium, (m) => console.error('driver.mjs: ' + m));
  const context = await newPhoneContext(browser, { scheme: args.scheme });
  await context.addInitScript(installHarnessHelpers);
  const page = await context.newPage();

  const state = {
    url: args.url,
    screenshotCounter: 0,
    currentTask: null,
    taskResults: [],
    tasks,
    consoleErrors: [],
    pageErrors: [],
    startedAt: new Date().toISOString(),
  };

  page.on('console', (msg) => {
    if (msg.type() === 'error') {
      state.consoleErrors.push({ text: msg.text(), location: msg.location(), at: new Date().toISOString() });
    }
  });
  page.on('pageerror', (err) => {
    state.pageErrors.push({ message: err.message, stack: err.stack, at: new Date().toISOString() });
  });

  await page.goto(args.url, { waitUntil: 'load' });

  async function saveScreenshot(label) {
    state.screenshotCounter += 1;
    const n = String(state.screenshotCounter).padStart(3, '0');
    const filePath = path.join(args.out, `${n}-${slugify(label)}.png`);
    await page.screenshot({ path: filePath, scale: 'css' });
    return filePath;
  }

  async function touchDrag(x1, y1, x2, y2, ms) {
    const cdp = await context.newCDPSession(page);
    const steps = 12;
    const stepDelay = ms / steps;
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: x1, y: y1 }] });
    for (let i = 1; i <= steps; i++) {
      const x = x1 + ((x2 - x1) * i) / steps;
      const y = y1 + ((y2 - y1) * i) / steps;
      await sleep(stepDelay);
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y }] });
    }
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  }

  async function locate(text, { climb }) {
    const candidates = await page.evaluate(
      ({ text: t, climb: c }) => window.__harnessHelpers.findByText(t, { climb: c }),
      { text, climb },
    );
    return { all: candidates, onScreen: candidates.filter((c) => c.onScreen) };
  }

  const commands = {
    async shot({ label }) {
      const p = await saveScreenshot(label || 'shot');
      return { ok: true, text: p };
    },

    async tap({ text, nth }) {
      if (!text) return { ok: false, text: 'tap needs a label, e.g. tap "Send"' };
      const { all, onScreen } = await locate(text, { climb: true });
      if (all.length === 0) return { ok: false, text: `no visible element matching '${text}'` };
      if (onScreen.length === 0) {
        if (all.some((c) => c.covered)) return { ok: false, text: `'${text}' is behind something else on screen (a sheet, menu or banner); close that or tap what's in front` };
        return { ok: false, text: `'${text}' is not on screen; scroll first` };
      }
      let target;
      if (nth != null) {
        target = onScreen[nth - 1];
        if (!target) {
          return { ok: false, text: `--nth ${nth} out of range (${onScreen.length} on-screen matches for '${text}')` };
        }
      } else if (onScreen.length === 1) {
        target = onScreen[0];
      } else if (onScreen[0].area === onScreen[1].area) {
        const list = onScreen
          .slice(0, 6)
          .map((c, i) => `  ${i + 1}. ${c.tag}[${c.role}] "${c.name}" @${c.cx},${c.cy}`)
          .join('\n');
        return {
          ok: false,
          text: `'${text}' matches ${onScreen.length} on-screen elements; pick one with --nth:\n${list}`,
        };
      } else {
        target = onScreen[0];
      }
      await page.touchscreen.tap(target.cx, target.cy);
      return { ok: true, text: `tapped ${target.tag} "${target.name}" @${target.cx},${target.cy}` };
    },

    async tapxy({ x, y }) {
      if (x == null || y == null || Number.isNaN(x) || Number.isNaN(y)) {
        return { ok: false, text: 'tapxy needs X and Y, e.g. tapxy 196 420' };
      }
      await page.touchscreen.tap(x, y);
      return { ok: true, text: `tapped @${x},${y}` };
    },

    async longpress({ text, x, y }) {
      let cx = x;
      let cy = y;
      let label = null;
      if (cx == null || cy == null) {
        if (!text) return { ok: false, text: 'longpress needs "<text>" or --xy X Y' };
        const { all, onScreen } = await locate(text, { climb: true });
        if (all.length === 0) return { ok: false, text: `no visible element matching '${text}'` };
        if (onScreen.length === 0) return { ok: false, text: `'${text}' is not on screen; scroll first` };
        const target = onScreen[0];
        cx = target.cx;
        cy = target.cy;
        label = target.name;
      }
      const cdp = await context.newCDPSession(page);
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: cx, y: cy }] });
      await sleep(650);
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
      return { ok: true, text: `long-pressed ${label ? `"${label}" ` : ''}@${cx},${cy}` };
    },

    async swipe({ x1, y1, x2, y2, ms }) {
      if ([x1, y1, x2, y2].some((v) => v == null || Number.isNaN(v))) {
        return { ok: false, text: 'swipe needs X1 Y1 X2 Y2, e.g. swipe 300 400 80 400' };
      }
      await touchDrag(x1, y1, x2, y2, ms || 300);
      return { ok: true, text: `swiped ${x1},${y1} -> ${x2},${y2}` };
    },

    async swiperow({ text, direction }) {
      if (!text || !['left', 'right'].includes(direction)) {
        return { ok: false, text: 'swiperow needs "<text>" left|right' };
      }
      const { all, onScreen } = await locate(text, { climb: false });
      if (all.length === 0) return { ok: false, text: `no visible element matching '${text}'` };
      if (onScreen.length === 0) return { ok: false, text: `'${text}' is not on screen; scroll first` };
      const target = onScreen[0];
      const span = PHONE_WIDTH * 0.65;
      const half = span / 2;
      const center = PHONE_WIDTH / 2;
      const [x1, x2] = direction === 'right' ? [center - half, center + half] : [center + half, center - half];
      await touchDrag(x1, target.cy, x2, target.cy, 300);
      return { ok: true, text: `swiped ${direction} on "${target.name}"` };
    },

    async scroll({ direction, pixels, atX, atY }) {
      if (!['up', 'down'].includes(direction)) {
        return { ok: false, text: 'scroll needs up|down, e.g. scroll down 500' };
      }
      const px = pixels || 500;
      const x = atX != null ? atX : PHONE_WIDTH / 2;
      const y = atY != null ? atY : PHONE_HEIGHT * 0.55;
      const cdp = await context.newCDPSession(page);
      // CDP: positive yDistance scrolls UP (reveals content above), so
      // "down" (reveal content further down) needs a negative yDistance.
      const yDistance = direction === 'down' ? -px : px;
      await cdp.send('Input.synthesizeScrollGesture', { x, y, xDistance: 0, yDistance, gestureSourceType: 'touch' });
      return { ok: true, text: `scrolled ${direction} ${px}px` };
    },

    async type({ text }) {
      if (text == null) return { ok: false, text: 'type needs "<text>" to type' };
      const editable = await page.evaluate(() => window.__harnessHelpers.activeElementIsEditable());
      if (!editable) return { ok: false, text: 'nothing to type into; tap a text field first' };
      await page.keyboard.type(text);
      return { ok: true, text: `typed "${text}"` };
    },

    async key({ name }) {
      if (!name) return { ok: false, text: 'key needs a key name, e.g. key Enter' };
      await page.keyboard.press(name);
      return { ok: true, text: `pressed ${name}` };
    },

    async back() {
      await touchDrag(3, 426, 260, 426, 250);
      return { ok: true, text: 'swiped back' };
    },

    async see() {
      const rows = await page.evaluate(() => window.__harnessHelpers.listVisible());
      if (rows.length === 0) return { ok: true, text: '(nothing interactive on screen)' };
      return { ok: true, text: rows.map((r) => `${r.role} "${r.name}" @${r.x},${r.y}`).join('\n') };
    },

    async task({ k }) {
      if (!state.tasks) return { ok: false, text: 'no tasks file loaded; start driver with --tasks <file>' };
      const found = findTask(state.tasks, String(k));
      if (!found) return { ok: false, text: `no task matches '${k}'` };
      state.currentTask = {
        id: found.id,
        prompt: found.prompt,
        actionCount: 0,
        events: (found.events || []).map((e) => ({ ...e, fired: false })),
      };
      state.taskResults.push({
        id: found.id,
        prompt: found.prompt,
        startedAt: new Date().toISOString(),
        endedAt: null,
        claim: null,
        actionCount: 0,
      });
      if (found.preset) {
        await page.evaluate((p) => {
          if (window.__proto && window.__proto.reset) window.__proto.reset(p);
        }, found.preset);
        await sleep(400);
      }
      return { ok: true, text: found.prompt };
    },

    async done({ note }) {
      if (!state.currentTask) return { ok: false, text: 'no task in progress; start one with: task <k>' };
      const t = state.currentTask;
      const logJson = await page.evaluate(() => JSON.stringify((window.__proto && window.__proto.log) || []));
      const outPath = path.join(args.out, `task-${t.id}-log.json`);
      let pretty = logJson;
      try {
        pretty = JSON.stringify(JSON.parse(logJson), null, 2);
      } catch {
        /* keep the raw string if it somehow isn't valid JSON */
      }
      fs.writeFileSync(outPath, pretty);
      await saveScreenshot(`task-${t.id}-done`);
      const result = state.taskResults.find((r) => r.id === t.id && r.endedAt === null);
      if (result) {
        result.endedAt = new Date().toISOString();
        result.claim = note || '';
        result.actionCount = t.actionCount;
      }
      state.currentTask = null;
      return { ok: true, text: `task ${t.id} done; log saved to ${outPath}` };
    },

    async finish() {
      const summary = {
        url: state.url,
        startedAt: state.startedAt,
        finishedAt: new Date().toISOString(),
        tasks: state.taskResults,
        consoleErrors: state.consoleErrors,
        pageErrors: state.pageErrors,
      };
      const summaryPath = path.join(args.out, 'summary.json');
      fs.writeFileSync(summaryPath, JSON.stringify(summary, null, 2));
      return { ok: true, text: `summary saved to ${summaryPath}`, shutdown: true };
    },

    async health() {
      return { ok: true, text: `ok ${state.url} task=${state.currentTask ? state.currentTask.id : 'none'}` };
    },
  };

  const ACTION_COMMANDS = new Set([
    'tap', 'tapxy', 'longpress', 'swipe', 'swiperow', 'scroll', 'type', 'key', 'back',
  ]);

  function logAction(command, cmdArgs, result) {
    const entry = {
      ts: new Date().toISOString(),
      task: state.currentTask ? state.currentTask.id : null,
      command,
      args: cmdArgs,
      result: result.ok ? 'ok' : 'error',
      text: result.text.split('\n')[0].slice(0, 200),
    };
    fs.appendFileSync(path.join(args.out, 'actions.jsonl'), `${JSON.stringify(entry)}\n`);
  }

  async function handleCommand(command, cmdArgs) {
    const handler = commands[command];
    if (!handler) return { ok: false, text: `unknown command '${command}'` };
    let result;
    try {
      result = await handler(cmdArgs);
    } catch (err) {
      result = { ok: false, text: `internal error: ${err.message}` };
    }
    if (ACTION_COMMANDS.has(command) && result.ok) {
      if (state.currentTask) {
        state.currentTask.actionCount += 1;
        for (const ev of state.currentTask.events) {
          if (!ev.fired && state.currentTask.actionCount >= ev.afterActions) {
            ev.fired = true;
            try {
              await page.evaluate((name) => {
                if (window.__proto && window.__proto.trigger) window.__proto.trigger(name);
              }, ev.name);
            } catch (err) {
              result.text += `\n(warning: scheduled event '${ev.name}' failed: ${err.message})`;
            }
          }
        }
      }
      await sleep(450);
      const shotPath = await saveScreenshot(labelFor(command, cmdArgs));
      result.text += `\n${shotPath}`;
    }
    logAction(command, cmdArgs, result);
    return result;
  }

  function labelFor(command, cmdArgs) {
    switch (command) {
      case 'tap':
        return `tap-${cmdArgs.text}`;
      case 'tapxy':
        return `tapxy-${cmdArgs.x}-${cmdArgs.y}`;
      case 'longpress':
        return cmdArgs.text ? `longpress-${cmdArgs.text}` : `longpress-${cmdArgs.x}-${cmdArgs.y}`;
      case 'swipe':
        return `swipe-${cmdArgs.x1}-${cmdArgs.y1}-${cmdArgs.x2}-${cmdArgs.y2}`;
      case 'swiperow':
        return `swiperow-${cmdArgs.text}-${cmdArgs.direction}`;
      case 'scroll':
        return `scroll-${cmdArgs.direction}`;
      case 'type':
        return 'type';
      case 'key':
        return `key-${cmdArgs.name}`;
      case 'back':
        return 'back';
      default:
        return command;
    }
  }

  const server = http.createServer((req, res) => {
    if (req.method !== 'POST') {
      res.writeHead(405, { 'content-type': 'text/plain' });
      res.end('use POST');
      return;
    }
    let body = '';
    req.on('data', (chunk) => {
      body += chunk;
    });
    req.on('end', async () => {
      let payload;
      try {
        payload = JSON.parse(body || '{}');
      } catch (err) {
        res.writeHead(400, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ ok: false, text: `bad JSON body: ${err.message}` }));
        return;
      }
      const result = await handleCommand(payload.command, payload.args || {});
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ ok: result.ok, text: result.text }));
      if (result.shutdown) {
        setTimeout(async () => {
          try {
            await browser.close();
          } catch {
            /* already gone */
          }
          server.close(() => process.exit(0));
        }, 50);
      }
    });
  });

  server.listen(args.port, '127.0.0.1', () => {
    console.log(`driver.mjs: ready at http://127.0.0.1:${args.port} showing ${args.url}`);
  });

  function shutdown() {
    (async () => {
      try {
        await browser.close();
      } catch {
        /* already gone */
      }
      server.close(() => process.exit(0));
    })();
    setTimeout(() => process.exit(0), 3000).unref();
  }
  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);
}

main().catch((err) => {
  console.error(`driver.mjs: fatal: ${err.stack || err.message}`);
  process.exit(1);
});
