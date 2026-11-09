import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { recordWebFailure } from './web.mjs';
import { collectDiagnostics, sanitizeFailure, summarizeFailure } from './diagnostics.mjs';

const record = {
 phase: 'folder-confirm',
 message: 'locator.click: /home/runner/private-canary http://127.0.0.1:1/auth/token-canary',
 stack: 'Error at /home/runner/private-canary',
 details: {
  rpc: [
   {method: 'evener/path/validate', event: 'sent', call: 1, timeMS: 1000, requested: true, length: 30, endsWithRequested: true, containsRequested: true, path: '/tmp/private-canary'},
   {method: 'evener/path/validate', event: 'response', call: 1, timeMS: 1040, elapsedMS: 40, valid: true, error: false, errorCode: null, requested: true},
  ],
  picker: [{stage: 'before-Enter', timeMS: 1010, prevented: false, generation: 1, present: true, inputMatches: true, pending: false, edited: true, error: false, confirmEnabled: false, inputFocused: true, selectionLength: 0, inputShape: {length: 30, endsWithRequested: true, containsRequested: true, text: 'private-canary'}, value: '/tmp/private-canary'}],
 },
};

test('sanitized failure keeps only allowlisted scalars of the trace and probe', () => {
 const failure = sanitizeFailure(record);
 assert.equal(failure.phase, 'folder-confirm');
 assert.equal(failure.rpc.length, 2);
 assert.equal(failure.picker[0].confirmEnabled, false);
 assert.equal(failure.picker[0].inputShape.length, 30);
 assert.equal(failure.picker[0].inputShape.endsWithRequested, true);
 assert.equal(failure.rpc[0].containsRequested, true);
 assert.equal(JSON.stringify(failure).includes('canary'), false);
 assert.equal(JSON.stringify(summarizeFailure(failure)).includes('canary'), false);
});

test('sanitizing drops a phase or stage that is not a plain label', () => {
 const failure = sanitizeFailure({phase: '/home/private-canary', details: {picker: [{stage: '/tmp/private-canary'}]}});
 assert.equal(failure.phase, 'unknown');
 assert.equal(failure.picker[0].stage, null);
});

test('summary orders rpc and picker events on one timeline', () => {
 const lines = summarizeFailure(sanitizeFailure(record)).split('\n');
 assert.equal(lines.length, 4);
 assert.ok(lines[1].startsWith('rpc +0ms'));
 assert.ok(lines[2].startsWith('picker +10ms'));
 assert.ok(lines[3].startsWith('rpc +40ms'));
});

test('collect writes one sanitized file per recorded failure and ignores other entries', async t => {
 const root = await mkdtemp(join(tmpdir(), 'tour-diagnostics-'));
 t.after(() => rm(root, {recursive: true, force: true}));
 const source = join(root, 'source'), out = join(root, 'out');
 await mkdir(join(source, 'evener-web-failure-a'), {recursive: true});
 await mkdir(join(source, 'unrelated'), {recursive: true});
 await mkdir(join(source, 'evener-web-failure-empty'), {recursive: true});
 await writeFile(join(source, 'evener-web-failure-a', 'failure.json'), JSON.stringify(record));
 const failures = await collectDiagnostics({source, out});
 assert.equal(failures.length, 1);
 assert.deepEqual(await readdir(out), ['web-failure-1.json']);
 assert.equal((await readFile(join(out, 'web-failure-1.json'), 'utf8')).includes('canary'), false);
});

test('collect reads where recordWebFailure writes, whatever TMPDIR says', async t => {
 const root = await mkdtemp(join(tmpdir(), 'tour-diagnostics-'));
 t.after(() => rm(root, {recursive: true, force: true}));
 const error = new Error('private-canary');
 const original = process.env.TMPDIR;
 process.env.TMPDIR = root;
 t.after(() => { if (original === undefined) delete process.env.TMPDIR; else process.env.TMPDIR = original; });
 await recordWebFailure(error, 'folder-confirm', {rpc: [], picker: []});
 t.after(() => rm(dirname(error.diagnosticsPath), {recursive: true, force: true}));
 const failures = await collectDiagnostics({out: join(root, 'out')});
 assert.ok(failures.some(f => f.phase === 'folder-confirm'));
});
