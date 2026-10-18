import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { normalizeTranscript, transcriptHTML, renderTranscript } from './transcript.mjs';

test('normalization preserves supplied data and only replaces declared owned fields', () => {
 const record = { argv: ['python3', '<script>'], cwdLabel: '/owned/work', stdout: 'λ /owned/work /other/work port=1234 id=abc', stderr: 'failed', exitCode: 1, owned: { paths: ['/owned/work'], ports: [1234], ids: ['abc'] } };
 const text = normalizeTranscript(record);
 assert.ok(text.includes('λ <workspace> /other/work port=<port> id=<id>'));
 assert.ok(text.includes('exit: 1'));
 const html = transcriptHTML(record);
 assert.ok(!html.includes('<script>'));
 assert.ok(html.includes('&lt;script&gt;'));
 assert.equal(normalizeTranscript(record), text);
});

test('owned identifiers do not rewrite unrelated values with the same prefix', () => {
 const record = { argv: ['echo'], cwdLabel: 'workspace', stdout: '/owned/work-other port=12345 id=abcdef', stderr: '', exitCode: 0, owned: { paths: ['/owned/work'], ports: [1234], ids: ['abc'] } };
 assert.ok(normalizeTranscript(record).includes(record.stdout));
});

test('owned path normalization preserves unrelated prefix and suffix tokens', () => {
 const output = '/backup/owned/work /backup/owned/work/file /owned/work-other /owned/work.other';
 const record = { argv: ['echo'], cwdLabel: 'workspace', stdout: output, stderr: output, exitCode: 1, owned: { paths: ['/owned/work'] } };
 const text = normalizeTranscript(record);
 assert.ok(text.includes(`stdout:\n${output}\nstderr:\n${output}\nexit: 1`));
});

test('executable normalization requires an exact argv element or output path token', () => {
 const output = '/backup/owned/bin/evener /owned/bin/evener-helper /owned/bin/evener/file /owned/bin/evener.other';
 const record = { argv: ['/owned/bin/evener-helper', '/backup/owned/bin/evener', 'echo /owned/bin/evener'], cwdLabel: 'workspace', stdout: output, stderr: output, exitCode: 1, owned: { executable: '/owned/bin/evener' } };
 const text = normalizeTranscript(record);
 assert.ok(text.includes("$ /owned/bin/evener-helper /backup/owned/bin/evener 'echo /owned/bin/evener'\n"));
 assert.ok(text.includes(`stdout:\n${output}\nstderr:\n${output}\nexit: 1`));
});

test('normalization replaces exact owned paths and their descendants', () => {
 const record = { argv: ['/owned/bin/evener', '/owned/work/file'], cwdLabel: '/owned/work', stdout: '/owned/work /owned/work/file "/owned/work/nested/file"', stderr: '/owned/bin/evener "/owned/bin/evener"', exitCode: 0, owned: { paths: ['/owned/work'], executable: '/owned/bin/evener' } };
 const text = normalizeTranscript(record);
 assert.ok(text.includes('$ evener <workspace>/file\ncwd: <workspace>\n'));
 assert.ok(text.includes('stdout:\n<workspace> <workspace>/file "<workspace>/nested/file"\nstderr:\nevener "evener"\nexit: 0'));
});

test('tokens and URLs are redacted before HTML rendering', () => {
 const r = { argv: ['echo'], cwdLabel: 'workspace', stdout: 'https://host/auth/private?token=secret Bearer abc123 sk-12345678901234567890', stderr: '', exitCode: 0 };
 const html = transcriptHTML(r);
 for (const secret of ['https://host', 'private', 'abc123', 'sk-123']) assert.ok(!html.includes(secret));
});

test('independent renders produce identical PNG bytes and selectable text', async () => {
 const dir = await mkdtemp(join(tmpdir(), 'tutorial-render-'));
 try {
  const record = { argv: ['python3', 'check.py'], cwdLabel: 'receipt-workspace', stdout: 'λ actual output\n', stderr: 'failure\n', exitCode: 1 };
  await renderTranscript(record, join(dir, 'first.png'));
  await renderTranscript(record, join(dir, 'second.png'));
  const firstPNG = await readFile(join(dir, 'first.png'));
  const secondPNG = await readFile(join(dir, 'second.png'));
  assert.deepEqual(firstPNG, secondPNG);
  assert.equal(await readFile(join(dir, 'first.txt'), 'utf8'), normalizeTranscript(record));
 } finally { await rm(dir, { recursive: true, force: true }); }
});
