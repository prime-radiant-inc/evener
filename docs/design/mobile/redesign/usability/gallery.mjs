#!/usr/bin/env node
// gallery.mjs — a curated, stably named set of prototype screenshots for
// design critique and first-glance comprehension tests.
//
// It runs smoke.mjs (so the gallery only exists when every check passes),
// then copies the key screens to <out-dir> under names that don't change
// between runs, so critics and participants in later rounds see the same
// screens under the same names.
//
// Usage: node gallery.mjs <out-dir>

import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const out = process.argv[2];
if (!out) { console.error('usage: node gallery.mjs <out-dir>'); process.exit(2); }
fs.mkdirSync(out, { recursive: true });
const shots = fs.mkdtempSync(path.join(os.tmpdir(), 'evener-gallery-'));

const run = spawnSync(process.execPath, [path.join(here, 'smoke.mjs'), '--out', shots], { encoding: 'utf8' });
const tail = run.stdout.trim().split('\n').slice(-1)[0];
if (run.status !== 0) {
  console.error('smoke failed, no gallery written:\n' + run.stdout.split('\n').filter((l) => l.startsWith('FAIL')).join('\n'));
  process.exit(1);
}

// smoke screenshot name (after its NNN- prefix) → gallery name
const PICK = {
  'light-board.png': '01-board.png',
  'light-session-s-pr2138.png': '02-session-working.png',
  'light-session-s-audit.png': '03-session-question.png',
  'light-session-s-mirror.png': '04-session-approval.png',
  'light-composer-typed-working.png': '05-composer-steer-queue.png',
  'light-subagents.png': '06-subagents.png',
  'light-reader.png': '07-reader.png',
  'light-sheet-launch.png': '08-new-session.png',
  'light-host-offline.png': '09-host-offline.png',
  'light-detail-menu.png': '10-detail-menu.png',
  'light-row-menu.png': '11-row-menu.png',
  'light-board-search.png': '12-search.png',
  'light-sheet-launch-plugins.png': '13-plugins.png',
  'light-session-s-hier.png': '14-session-finished.png',
  'light-artifact.png': '15-artifact.png',
  'light-board-hosts.png': '16-board-hosts.png',
  'light-event-question.png': '17-alert.png',
  'light-sheet-hub.png': '18-hub.png',
  'dark-board.png': '19-board-dark.png',
  'dark-session-working.png': '20-session-working-dark.png',
  'dark-session-question.png': '21-session-question-dark.png',
  'dark-reader.png': '22-reader-dark.png',
};
let n = 0;
for (const f of fs.readdirSync(shots)) {
  const key = f.replace(/^\d+-/, '');
  if (PICK[key]) { fs.copyFileSync(path.join(shots, f), path.join(out, PICK[key])); n++; }
}
const missing = Object.values(PICK).filter((g) => !fs.existsSync(path.join(out, g)));
console.log(`${tail}\ngallery: ${n} screens in ${out}${missing.length ? '\nmissing: ' + missing.join(', ') : ''}`);
process.exit(missing.length ? 1 : 0);
