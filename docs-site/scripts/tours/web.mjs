import assert from 'node:assert/strict';
import { chromium } from '@playwright/test';
import { join } from 'node:path';
import { mkdtemp, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';

export async function recordWebFailure(error, phase) {
 // The Go driver owns TMPDIR and removes it before the caller can inspect logs.
 const directory=await mkdtemp(join(process.platform==='win32'?tmpdir():'/tmp','evener-web-failure-'));
 const file=join(directory,'failure.json');
 await writeFile(file,JSON.stringify({phase,message:error.message,stack:error.stack}),{mode:0o600});
 error.diagnosticsPath=file;
 error.tourPhase=`${phase}; private diagnostics: ${file}`;
}

export function workspaceLabels(workspace) {
 return workspace.startsWith('/private/tmp/') ? [workspace, workspace.slice(8)] : workspace.startsWith('/tmp/') ? [workspace, `/private${workspace}`] : [workspace];
}

// Runs in the browser. Conversation panes also carry data-session-ref, so
// only a row with the rail's title can certify navigation readiness.
export function railCaptureReady(refs) {
 const rows = [...document.querySelectorAll('[data-session-ref]')].filter(row => row.querySelector('[data-testid="rail-row-title"]'));
 return refs.every(ref => {
  const matching = rows.filter(row => row.getAttribute('data-session-ref') === ref);
  return matching.length > 0 && matching.every(row => !row.querySelector('[data-testid="rail-row-not-started"]'));
 });
}

export async function open(options) {
 const { url, authURL, workspace, viewport, outputDir } = options;
 assert.deepEqual(viewport, { width: 1280, height: 900 });
 const startedRefs = new Set();
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
  async function newSession(selectedWorkspace) {
  try {
  if (new URL(page.url()).pathname.startsWith('/settings/')) await page.goto(url);
  phase = 'new session';
  await page.getByRole('button', { name: /^(?:\+ )?New session$/ }).click();
  phase = 'directory-open';
  await page.getByRole('button', { name: /^Working directory:/ }).click();
  phase = 'directory-path';
  await page.getByRole('textbox', { name: 'Path', exact: true }).fill(selectedWorkspace);
  await page.getByRole('textbox', { name: 'Path', exact: true }).press('Enter');
  phase = 'folder-confirm';
  await page.getByRole('button', { name: 'Use this folder', exact: true }).click();
  phase = 'model selection';
  await page.getByRole('button', { name: /change model$/i }).click();
  await page.getByRole('combobox', { name: 'Model', exact: true }).fill(options.model.split('/')[1]);
  await page.getByRole('listbox', { name: 'Model', exact: true }).getByRole('option').first().click();
  } catch (error) { await recordWebFailure(error,phase); throw error; }
  }
  await newSession(workspace);
  return {
   async act(action, args = {}) {
    if (action === 'configure') {
     assert.equal(args.scope, 'project');
     assert.equal(args.field, 'max_rounds');
     assert.ok([workspace, ...(options.workspaces ?? [])].includes(args.workspace ?? workspace));
     await page.goto(`${url}/settings/project?cwd=${encodeURIComponent(args.workspace ?? workspace)}`);
     await page.getByRole('spinbutton', { name: 'Max rounds', exact: true }).fill(String(args.value));
     await page.getByRole('button', { name: 'Save launch defaults', exact: true }).click();
     await page.locator('[data-testid="launch-config-status"][data-state="saved"]').waitFor();
    } else if (action === 'start') {
     if (args.workspace) await newSession(args.workspace);
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
     startedRefs.add(decodeURIComponent(new URL(page.url()).pathname.slice(3)));
    } else if (action === 'send' || action === 'steer') {
     const message = page.getByRole('textbox', { name: 'Message', exact: true });
     await message.fill('');
     await message.pressSequentially(args.prompt);
     await page.getByTestId(action === 'steer' ? 'composer-steer' : 'composer-submit').click();
    } else if (action === 'stop') {
     await page.getByTestId('composer-stop').click();
    } else if (action === 'capture') {
     assert.ok(['entry', 'action', 'verified'].includes(args.id));
     assert.ok(!page.url().includes('/auth/'));
     if (options.scenario === 'configuration' && args.id === 'entry') await page.getByTestId('composer-stop').waitFor({ state: 'visible' });
     if (options.scenario === 'configuration' && args.id === 'action') await page.getByRole('spinbutton', { name: 'Max rounds', exact: true }).scrollIntoViewIfNeeded();
     if (options.scenario === 'configuration' && args.id === 'verified') {
      await page.locator('[data-tool-name="apply_patch"]').waitFor({ state: 'visible' });
      await page.locator('[data-testid="agent-message-item"][data-live="false"]').waitFor({ state: 'visible' });
     }
     if (options.scenario === 'daily') {
      if (args.id === 'entry') await page.getByTestId('composer-stop').waitFor({ state: 'visible' });
      if (args.id === 'action') await page.locator('[data-tool-name="shell"], [data-tool-name="exec_command"]').first().waitFor({ state: 'visible' });
     } else if (options.scenario === 'extensions') {
      if (args.id === 'entry') await page.locator('[data-testid="spawn-plugin-summary"][data-state="ready"]').waitFor({ state: 'visible' });
      if (args.id === 'action') {
       const echo = page.locator('[data-tool-name="example__echo"]');
       await echo.waitFor({ state: 'visible' });
       const disclosure = echo.getByTestId('tool-row-trigger');
       if (await disclosure.getAttribute('aria-expanded') !== 'true') await disclosure.click();
       await echo.getByTestId('tool-call-body').waitFor({ state: 'visible' });
      }
     } else if (options.scenario !== 'configuration') {
      if (args.id === 'entry') await page.locator('[data-testid="spawn-plugin-summary"][data-state="ready"]').waitFor({ state: 'visible' });
      if (args.id === 'action') await page.locator('[data-tool-name="edit_file"]').waitFor({ state: 'visible' });
     }
     if (args.id === 'verified' && options.scenario !== 'configuration') {
      const completed = page.locator('[data-testid="agent-message-item"][data-live="false"]');
      await (['daily', 'extensions'].includes(options.scenario) ? completed.nth(1) : completed).waitFor({ state: 'visible' });
     }
     if (new URL(page.url()).pathname.startsWith('/s/')) {
      await page.waitForFunction(railCaptureReady, [...startedRefs], { timeout: 10_000 });
     }
     await page.evaluate(() => document.fonts.ready);
     // Opaque screenshot masks redact only exact owned workspace labels.
     // The live DOM and all runtime evidence remain unchanged.
     const aliases = [workspace, ...(options.workspaces ?? [])].flatMap(workspaceLabels);
     const masks = aliases.map(path => page.getByText(path, { exact: true }));
     assert.ok((await Promise.all(masks.map(mask => mask.count()))).some(count => count > 0), 'owned workspace label must be present for redaction');
     await page.mouse.move(0, 0);
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
 } catch (error) { if(!error.tourPhase)await recordWebFailure(error,phase); await browser.close(); throw error; }
}
