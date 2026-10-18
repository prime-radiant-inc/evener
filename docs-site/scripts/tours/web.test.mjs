import assert from 'node:assert/strict';
import { test } from 'node:test';
import { chromium } from '@playwright/test';
import * as web from './web.mjs';

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
