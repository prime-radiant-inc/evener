import assert from 'node:assert/strict';
import { test } from 'node:test';
import { chromium } from '@playwright/test';
import * as web from './web.mjs';
import { readFile, rm, stat } from 'node:fs/promises';
import { dirname } from 'node:path';

test('web failure diagnostics retain details privately and expose only phase and path', async t => {
 const error=new Error('private-locator-canary\nCall log: locator click failed');
 assert.equal(typeof web.recordWebFailure,'function');
 await web.recordWebFailure(error,'folder-confirm');
 const file=error.diagnosticsPath;t.after(()=>rm(dirname(file),{recursive:true,force:true}));
 assert.equal(error.tourPhase,`folder-confirm; private diagnostics: ${file}`);
 assert.ok(!error.tourPhase.includes('private-locator-canary'));
 assert.deepEqual(JSON.parse(await readFile(file,'utf8')),{phase:'folder-confirm',message:error.message,stack:error.stack});
 if(process.platform!=='win32') {
  assert.equal((await stat(dirname(file))).mode&0o777,0o700);
  assert.equal((await stat(file)).mode&0o777,0o600);
 }
});

test('workspace labels include only exact temporary path aliases', () => {
 assert.deepEqual(web.workspaceLabels('/private/tmp/owned/work'), ['/private/tmp/owned/work', '/tmp/owned/work']);
 assert.deepEqual(web.workspaceLabels('/tmp/owned/work'), ['/tmp/owned/work', '/private/tmp/owned/work']);
 for (const path of ['/tmp', '/private/tmp', '/tmp-other/work', '/private/tmp-other/work', '/work/tmp/owned', 'tmp/owned', '/Users/example/work']) {
  assert.deepEqual(web.workspaceLabels(path), [path]);
 }
});

test('capture readiness requires every started session in the rail without a never-run marker', async () => {
 assert.equal(typeof web.railCaptureReady, 'function', 'capture must expose its real DOM readiness predicate');
 const browser = await chromium.launch({ headless: true });
 try {
  const page = await browser.newPage();
  const ready = refs => page.evaluate(web.railCaptureReady, refs);
  await page.setContent('<main data-session-ref="local:one"></main>');
  assert.equal(await ready(['local:one']), false, 'a conversation pane is not a rail row');
  await page.setContent('<span data-session-ref="local:one"><button data-testid="rail-row-title"></button><span data-testid="rail-row-not-started"></span></span>');
  assert.equal(await ready(['local:one']), false, 'indexed never-run state cannot certify submitted work');
  await page.locator('[data-testid="rail-row-not-started"]').evaluate(node => node.remove());
  assert.equal(await ready(['local:one']), true);
  assert.equal(await ready(['local:one', 'local:two']), false, 'an earlier missing session must still block capture');
  await page.setContent('<span data-session-ref="local:one"><button data-testid="rail-row-title"></button></span><span data-session-ref="local:two"><button data-testid="rail-row-title"></button></span>');
  assert.equal(await ready(['local:one', 'local:two']), true);
  assert.equal(await ready([]), true, 'pre-launch checkpoints need no session rows');
 } finally {
  await browser.close();
 }
});
