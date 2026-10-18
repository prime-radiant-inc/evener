import assert from 'node:assert/strict';
import { chromium } from '@playwright/test';
import { join } from 'node:path';

export function workspaceLabels(workspace) {
 return workspace.startsWith('/private/tmp/') ? [workspace, workspace.slice(8)] : workspace.startsWith('/tmp/') ? [workspace, `/private${workspace}`] : [workspace];
}

export async function open(options) {
 const { url, authURL, workspace, viewport, outputDir } = options;
 assert.deepEqual(viewport, { width: 1280, height: 900 });
 let phase = "browser launch";
 let browser;
 try {
 browser = await chromium.launch({ headless: true });
 } catch (error) { error.tourPhase = phase; throw error; }
 try {
  const context = await browser.newContext({ viewport, deviceScaleFactor: 1, serviceWorkers: 'block', reducedMotion: 'reduce' });
  // This guard only denies external traffic. It never supplies a response.
  await context.route('**/*', route => new URL(route.request().url()).origin === new URL(url).origin ? route.continue() : route.abort());
  await context.routeWebSocket('**/*', socket => {
   const target = new URL(socket.url());
   target.protocol = target.protocol === 'wss:' ? 'https:' : 'http:';
   if (target.origin === new URL(url).origin) socket.connectToServer();
   else socket.close();
  });
  const page = await context.newPage();
  page.setDefaultTimeout(30_000);
  phase = "authentication";
  await page.goto(authURL);
  await page.waitForURL(u => !u.pathname.startsWith('/auth/'));
  phase = 'new session';
  await page.getByRole('button', { name: 'New session', exact: true }).click();
  phase = 'directory selection';
  await page.getByRole('button', { name: /^Working directory:/ }).click();
  await page.getByRole('textbox', { name: 'Path', exact: true }).fill(workspace);
  await page.getByRole('textbox', { name: 'Path', exact: true }).press('Enter');
  await page.getByRole('button', { name: 'Use this folder', exact: true }).click();
  phase = 'model selection';
  await page.getByRole('button', { name: /change model$/i }).click();
  await page.getByRole('combobox', { name: 'Model', exact: true }).fill(options.model.split('/')[1]);
  await page.getByRole('listbox', { name: 'Model', exact: true }).getByRole('option').click();
  return {
   async act(action, args = {}) {
    if (action === 'start') {
     const prompt = page.getByRole('textbox', { name: 'Prompt', exact: true });
     await prompt.fill('');
     const lines = args.prompt.split('\n');
     for (const [index, line] of lines.entries()) {
      if (index) await prompt.press('Shift+Enter');
      await page.keyboard.insertText(line);
     }
     await page.getByRole('button', { name: 'Start', exact: true }).click();
     try { await page.waitForURL(u => u.pathname.startsWith('/s/')); }
     catch (error) {
      error.tourPhase = 'session navigation, alerts: ' + (await page.getByRole('alert').allTextContents()).join(' | ').replace(/(?:https?:|wss?:)\/\/\S+|\/[^\s]+/g, '[private path]');
      throw error;
     }
    } else if (action === 'capture') {
     assert.ok(['entry', 'action', 'verified'].includes(args.id));
     assert.ok(!page.url().includes('/auth/'));
     if (args.id === 'entry') await page.locator('[data-testid="spawn-plugin-summary"][data-state="ready"]').waitFor({ state: 'visible' });
     if (args.id === 'action') await page.locator('[data-tool-name="edit_file"]').waitFor({ state: 'visible' });
     if (args.id === 'verified') await page.locator('[data-testid="agent-message-item"][data-live="false"]').waitFor({ state: 'visible' });
     await page.evaluate(() => document.fonts.ready);
     // Opaque screenshot masks redact only exact owned workspace labels.
     // The live DOM and all runtime evidence remain unchanged.
     const aliases = workspaceLabels(workspace);
     const masks = aliases.map(path => page.getByText(path, { exact: true }));
     assert.ok((await Promise.all(masks.map(mask => mask.count()))).some(count => count > 0), 'owned workspace label must be present for redaction');
     await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
     const png = await page.screenshot({ path: join(outputDir, `${args.id}.png`), animations: 'disabled', mask: masks, maskColor: '#000000' });
     const dimensions = await page.evaluate(async data => {
      const image = new Image();
      image.src = `data:image/png;base64,${data}`;
      await image.decode();
      return { width: image.naturalWidth, height: image.naturalHeight };
     }, png.toString('base64'));
     assert.deepEqual(dimensions, { width: 1280, height: 900 });
    } else throw new Error(`Unsupported web action: ${action}`);
    return {};
   },
   async close() { await browser.close(); },
  };
 } catch (error) { error.tourPhase = phase + ': ' + error.message.split('\n')[0].replace(/(?:https?:|wss?:)\/\/\S+|\/[^\s]+/g, '[private path]'); await browser.close(); throw error; }
}
