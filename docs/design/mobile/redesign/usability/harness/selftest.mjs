#!/usr/bin/env node
// selftest.mjs — proves the harness works without the real prototype.
//
// Writes a tiny fixture fragment (a stand-in for the prototype's
// index.html), starts serve.mjs and driver.mjs against it as real child
// processes, then drives it through phone.mjs exactly as a participant
// would: tap, swiperow, longpress, scroll, type, a scripted task with a
// preset and a scheduled event, done, finish. It then checks the files the
// driver produced (task log, screenshots) for what the fixture's own
// instrumentation recorded.
//
// Usage: node selftest.mjs

import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const SERVE = path.join(HERE, 'serve.mjs');
const DRIVER = path.join(HERE, 'driver.mjs');
const PHONE = path.join(HERE, 'phone.mjs');

let passCount = 0;
let failCount = 0;
function check(name, cond, detail) {
  if (cond) {
    passCount += 1;
    console.log(`PASS: ${name}`);
  } else {
    failCount += 1;
    console.log(`FAIL: ${name}${detail ? ` — ${detail}` : ''}`);
  }
}
function info(line) {
  console.log(`INFO: ${line}`);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function getFreePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
    srv.on('error', reject);
  });
}

async function waitForHttp(url, { timeoutMs = 20000, method = 'GET', body } = {}) {
  const start = Date.now();
  let lastErr = null;
  while (Date.now() - start < timeoutMs) {
    try {
      const opts = method === 'POST' ? { method, headers: { 'content-type': 'application/json' }, body } : {};
      const resp = await fetch(url, opts);
      if (resp.status < 500) return resp;
    } catch (err) {
      lastErr = err;
    }
    await sleep(200);
  }
  throw new Error(`timed out waiting for ${url}: ${lastErr ? lastErr.message : 'no response'}`);
}

function tailOutput(child) {
  const out = (child.__stdout || []).join('');
  const err = (child.__stderr || []).join('');
  return `--- stdout ---\n${out}\n--- stderr ---\n${err}`;
}

function spawnLogged(cmd, cmdArgs) {
  const child = spawn(cmd, cmdArgs, { stdio: ['ignore', 'pipe', 'pipe'] });
  child.__stdout = [];
  child.__stderr = [];
  child.stdout.on('data', (d) => child.__stdout.push(d.toString()));
  child.stderr.on('data', (d) => child.__stderr.push(d.toString()));
  return child;
}

// Runs one phone.mjs command and returns { status, stdout }. Never throws —
// a non-zero exit is a normal, checkable outcome for some steps.
function phone(port, cmdArgs) {
  const result = spawnSync('node', [PHONE, '--port', String(port), ...cmdArgs], { encoding: 'utf8' });
  return { status: result.status, stdout: (result.stdout || '').trim(), stderr: (result.stderr || '').trim() };
}

// The fixture fragment: no <!doctype>/<html>/<body>, matching how the real
// prototype is authored. Exercises every gesture the harness supports and
// records what happened into window.__proto.log, the same convention the
// real prototype is expected to use.
const FIXTURE_HTML = `<div id="app">
  <input id="text-input" type="text" placeholder="Type here" aria-label="Message input"
    style="display:block; width:300px; height:32px; margin:8px;" />
  <button id="send-btn" style="display:block; margin:8px;">Send</button>
  <div id="swipe-row" style="display:block; margin:8px; padding:16px; background:#eee; touch-action:none;">Swipe Row</div>
  <div id="longpress-target" style="display:block; margin:8px; padding:16px; background:#ddd; touch-action:none;">Long Press Me</div>
  <ul id="list" style="display:block; height:400px; margin:8px; overflow-y:auto; touch-action:pan-y; border:1px solid #ccc; padding:0; list-style:none;"></ul>
</div>
<script>
  window.__proto = {
    log: [],
    reset(p) { this.log.push({ type: 'reset', p }); },
    trigger(n) { this.log.push({ type: 'event', n }); },
  };

  const list = document.getElementById('list');
  for (let i = 1; i <= 40; i++) {
    const li = document.createElement('li');
    li.textContent = 'Row ' + i;
    li.style.padding = '10px';
    li.style.borderBottom = '1px solid #eee';
    list.appendChild(li);
  }
  list.addEventListener('scroll', () => {
    window.__proto.log.push({ type: 'scroll', top: list.scrollTop });
  });

  document.getElementById('send-btn').addEventListener('click', () => {
    window.__proto.log.push({ type: 'click', id: 'send-btn' });
  });

  document.getElementById('text-input').addEventListener('input', (e) => {
    window.__proto.log.push({ type: 'input', value: e.target.value });
  });

  (function () {
    const el = document.getElementById('swipe-row');
    let startX = null;
    el.addEventListener('touchstart', (e) => { startX = e.touches[0].clientX; });
    el.addEventListener('touchend', (e) => {
      if (startX == null) return;
      const t = e.changedTouches && e.changedTouches[0];
      const dx = (t ? t.clientX : startX) - startX;
      if (dx > 60) window.__proto.log.push({ type: 'swipe-right' });
      else if (dx < -60) window.__proto.log.push({ type: 'swipe-left' });
      startX = null;
    });
  })();

  (function () {
    const el = document.getElementById('longpress-target');
    let timer = null;
    el.addEventListener('touchstart', () => {
      timer = setTimeout(() => { window.__proto.log.push({ type: 'longpress' }); }, 500);
    });
    const cancel = () => { if (timer) { clearTimeout(timer); timer = null; } };
    el.addEventListener('touchend', cancel);
    el.addEventListener('touchcancel', cancel);
  })();
</script>
`;

async function main() {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'harness-selftest-'));
  const protoDir = path.join(tmpDir, 'proto');
  const outDir = path.join(tmpDir, 'out');
  fs.mkdirSync(protoDir, { recursive: true });
  fs.mkdirSync(outDir, { recursive: true });
  fs.writeFileSync(path.join(protoDir, 'index.html'), FIXTURE_HTML);

  const tasksPath = path.join(tmpDir, 'tasks.json');
  fs.writeFileSync(
    tasksPath,
    JSON.stringify(
      [
        {
          id: 'T1',
          prompt: 'Selftest task: send a message and browse the list.',
          preset: 'selftest-preset',
          events: [{ name: 'selftest-event', afterActions: 1 }],
        },
      ],
      null,
      2,
    ),
  );

  let serveProc = null;
  let driverProc = null;

  try {
    const servePort = await getFreePort();
    const driverPort = await getFreePort();

    serveProc = spawnLogged('node', [SERVE, '--dir', protoDir, '--port', String(servePort)]);
    const serveUrl = `http://127.0.0.1:${servePort}/`;
    try {
      await waitForHttp(serveUrl);
    } catch (err) {
      throw new Error(`serve.mjs never came up: ${err.message}\n${tailOutput(serveProc)}`);
    }
    check('serve.mjs starts and answers requests', true);

    driverProc = spawnLogged('node', [
      DRIVER, '--port', String(driverPort), '--url', serveUrl, '--out', outDir, '--tasks', tasksPath,
    ]);
    const driverBase = `http://127.0.0.1:${driverPort}/`;
    try {
      await waitForHttp(driverBase, { method: 'POST', body: JSON.stringify({ command: 'health', args: {} }) });
    } catch (err) {
      throw new Error(`driver.mjs never came up: ${err.message}\n${tailOutput(driverProc)}`);
    }
    check('driver.mjs launches Chromium and answers health', true);

    // Start the task first: the scheduled event (afterActions:1) can only
    // fire once a task is in progress, so the first action command below is
    // what triggers it.
    const taskRes = phone(driverPort, ['task', 'T1']);
    check('task T1 starts and prints only the prompt', taskRes.status === 0 && taskRes.stdout.includes('Selftest task'), taskRes.stdout);

    const tapSend = phone(driverPort, ['tap', 'Send']);
    check('tap "Send" succeeds and prints a screenshot path', tapSend.status === 0 && /\.png$/m.test(tapSend.stdout), tapSend.stdout);

    const swipeLeft = phone(driverPort, ['swiperow', 'Swipe Row', 'left']);
    check('swiperow left succeeds', swipeLeft.status === 0, swipeLeft.stdout);

    const swipeRight = phone(driverPort, ['swiperow', 'Swipe Row', 'right']);
    check('swiperow right succeeds', swipeRight.status === 0, swipeRight.stdout);

    const longpress = phone(driverPort, ['longpress', 'Long Press Me']);
    check('longpress succeeds', longpress.status === 0, longpress.stdout);

    const scrollDown = phone(driverPort, ['scroll', 'down', '300', '--at', '196', '450']);
    check('scroll down succeeds', scrollDown.status === 0, scrollDown.stdout);

    const tapInput = phone(driverPort, ['tap', 'Message input']);
    check('tap on the text input succeeds', tapInput.status === 0, tapInput.stdout);

    const typeRes = phone(driverPort, ['type', 'hello']);
    check('type into the focused input succeeds', typeRes.status === 0, typeRes.stdout);

    const keyRes = phone(driverPort, ['key', 'Escape']);
    check('key Escape succeeds', keyRes.status === 0, keyRes.stdout);

    const doneRes = phone(driverPort, ['done', 'sent a message and looked at the list']);
    check('done ends the task and does not print the log', doneRes.status === 0 && !doneRes.stdout.includes('"log"'), doneRes.stdout);

    const seeRes = phone(driverPort, ['see']);
    check('see lists on-screen elements', seeRes.status === 0 && seeRes.stdout.length > 0, seeRes.stdout);

    const finishRes = phone(driverPort, ['finish']);
    check('finish writes a summary and reports success', finishRes.status === 0, finishRes.stdout);

    // The driver must exit cleanly on its own after `finish`.
    const exited = await new Promise((resolve) => {
      if (driverProc.exitCode !== null) return resolve(true);
      const timer = setTimeout(() => resolve(false), 5000);
      driverProc.once('exit', () => {
        clearTimeout(timer);
        resolve(true);
      });
    });
    check('driver.mjs exits on its own after finish', exited);

    // --- Inspect what got written to --out ---

    const taskLogPath = path.join(outDir, 'task-T1-log.json');
    check('task-T1-log.json was written', fs.existsSync(taskLogPath));
    let log = [];
    if (fs.existsSync(taskLogPath)) {
      log = JSON.parse(fs.readFileSync(taskLogPath, 'utf8'));
    }
    const hasEntry = (pred) => log.some(pred);
    check('log records the preset reset', hasEntry((e) => e.type === 'reset' && e.p === 'selftest-preset'), JSON.stringify(log));
    check('log records the scheduled event firing once', log.filter((e) => e.type === 'event' && e.n === 'selftest-event').length === 1, JSON.stringify(log));
    check('log records the Send button click', hasEntry((e) => e.type === 'click' && e.id === 'send-btn'), JSON.stringify(log));
    check('log records swipe-left', hasEntry((e) => e.type === 'swipe-left'), JSON.stringify(log));
    check('log records swipe-right', hasEntry((e) => e.type === 'swipe-right'), JSON.stringify(log));
    check('log records the long press', hasEntry((e) => e.type === 'longpress'), JSON.stringify(log));
    check('log records typed input', hasEntry((e) => e.type === 'input' && e.value === 'hello'), JSON.stringify(log));

    const sawNestedScroll = hasEntry((e) => e.type === 'scroll');
    info(`nested overflow-list received a native scroll event from synthesizeScrollGesture: ${sawNestedScroll ? 'yes' : 'no'}`);

    const summaryPath = path.join(outDir, 'summary.json');
    check('summary.json was written', fs.existsSync(summaryPath));
    if (fs.existsSync(summaryPath)) {
      const summary = JSON.parse(fs.readFileSync(summaryPath, 'utf8'));
      check('summary.json records the T1 task with a claim', summary.tasks?.[0]?.id === 'T1' && !!summary.tasks[0].claim);
    }

    const actionsPath = path.join(outDir, 'actions.jsonl');
    check('actions.jsonl was written', fs.existsSync(actionsPath));
    if (fs.existsSync(actionsPath)) {
      const lines = fs.readFileSync(actionsPath, 'utf8').trim().split('\n');
      check('actions.jsonl has one line per command', lines.length >= 10, String(lines.length));
    }

    const pngFiles = fs.readdirSync(outDir).filter((f) => f.endsWith('.png'));
    check('screenshots were saved', pngFiles.length > 0, `found ${pngFiles.length}`);
    let allCorrectSize = pngFiles.length > 0;
    for (const f of pngFiles) {
      const buf = fs.readFileSync(path.join(outDir, f));
      const width = buf.readUInt32BE(16);
      const height = buf.readUInt32BE(20);
      if (width !== 393 || height !== 852) {
        allCorrectSize = false;
        info(`${f} is ${width}x${height}, expected 393x852`);
      }
    }
    check('all screenshots are 393x852 PNGs', allCorrectSize);
  } finally {
    for (const child of [serveProc, driverProc]) {
      if (child && child.exitCode === null) {
        child.kill('SIGTERM');
      }
    }
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }

  console.log(`\n${passCount} passed, ${failCount} failed`);
  process.exit(failCount > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error(`selftest.mjs: fatal: ${err.stack || err.message}`);
  process.exit(1);
});
