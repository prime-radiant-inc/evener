#!/usr/bin/env node
/** Verify and capture the six standalone mockups, never Evener or provider APIs. */
import { createRequire } from 'node:module';
import { mkdir, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import assert from 'node:assert/strict';

const args = process.argv.slice(2);
if (args.includes('--help')) {
  console.log('Usage: node verify.mjs --url http://127.0.0.1:PORT --output DIR --playwright /path/to/playwright/package.json\nUses installed Google Chrome or CHROME_PATH. Checks the design prototypes and saves desktop/mobile screenshots. Makes no provider requests.');
  process.exit(0);
}
function option(name) {
  const index = args.indexOf(name);
  if (index < 0 || !args[index + 1]) throw new Error(`Missing ${name}; use --help`);
  return args[index + 1];
}
const base = new URL(option('--url'));
assert(['localhost', '127.0.0.1'].includes(base.hostname), 'Serve this design study on loopback only');
const output = resolve(option('--output'));
const require = createRequire(resolve(option('--playwright')));
const { chromium } = require('playwright');
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/usr/bin/google-chrome', headless: true });
const failures = [];
const checks = [];
const page = await browser.newPage({ viewport: { width: 1440, height: 1050 }, reducedMotion: 'reduce' });
page.on('pageerror', error => failures.push(error.message));
page.on('console', message => { if (message.type() === 'error') failures.push(message.text()); });
await page.route('**/*', route => {
  if (new URL(route.request().url()).origin !== base.origin) {
    failures.push(`Unexpected external request: ${route.request().url()}`);
    return route.abort();
  }
  return route.continue();
});
async function open(concept, extra = '') {
  await page.goto(new URL(`concept.html?concept=${concept}${extra}`, base).href);
  await page.locator('.stage').waitFor();
}
async function chooseAnthropic(concept) {
  if (concept === 'd') await page.locator('[data-action="model-anthropic"]').click();
  else {
    if (concept === 'e') await page.locator('[data-action="picker"]').first().click();
    await page.locator('[data-provider="anthropic"]').first().click();
  }
}
async function noOverflow(label) {
  const dimensions = await page.evaluate(() => ({ width: innerWidth, scroll: document.documentElement.scrollWidth }));
  assert(dimensions.scroll <= dimensions.width, `${label}: horizontal overflow ${JSON.stringify(dimensions)}`);
}
try {
  await page.goto(new URL('index.html', base).href);
  assert.equal(await page.locator('.gallery-card').count(), 6, 'Gallery retains all six options');
  assert(await page.locator('.gallery-card img').evaluateAll(images => images.every(image => image.complete && image.naturalWidth > 0)), 'All six gallery previews load');
  await page.setViewportSize({ width: 390, height: 844 });
  await noOverflow('Gallery mobile');
  checks.push('Gallery: six options, loaded preview images, mobile width');
  for (const concept of ['a', 'b', 'c', 'd', 'e', 'f']) {
    await page.setViewportSize({ width: 1440, height: 1050 });
    await open(concept);
    await noOverflow(`${concept} desktop start`);
    await page.locator('.stage > *').screenshot({ path: `${output}/${concept}.png` });
    await chooseAnthropic(concept);
    assert.equal(await page.locator('input[required]:visible').count(), 1, `${concept}: one required key input`);
    assert.equal(await page.locator('#advanced').getAttribute('open'), null, `${concept}: advanced initially closed`);
    await page.locator('#advanced summary').click();
    assert(await page.locator('[name="credential-header"]').isVisible(), `${concept}: expert controls remain available`);
    await page.locator('#advanced summary').click();
    await page.screenshot({ path: `${output}/${concept}-key.png`, fullPage: true });
    await page.locator('#connect-form button[type="submit"]').click();
    assert(await page.locator('#api-key').isVisible(), `${concept}: blank key cannot proceed`);
    await page.locator('[data-action="fill-demo"]').click();
    await page.locator('#connect-form button[type="submit"]').click();
    assert(await page.locator('#next-model').isVisible(), `${concept}: verified result offers model choice`);
    await page.screenshot({ path: `${output}/${concept}-success.png`, fullPage: true });
    await page.locator('[data-action="done"]').click();
    assert(await page.locator('[data-heading]').isVisible(), `${concept}: completed flow returns to context`);
    await page.setViewportSize({ width: 390, height: 844 });
    await open(concept);
    await noOverflow(`${concept} mobile start`);
    await chooseAnthropic(concept);
    await noOverflow(`${concept} mobile key`);
    await page.screenshot({ path: `${output}/${concept}-mobile.png`, fullPage: true });
    checks.push(`${concept.toUpperCase()}: desktop, mobile, requiredness, advanced, submit, return`);
  }
  await page.setViewportSize({ width: 1440, height: 1050 });
  for (const outcome of ['auth', 'endpoint', 'unsupported']) {
    await open('a', '&screen=credential&provider=anthropic');
    await page.locator('#demo-outcome').selectOption(outcome);
    await page.locator('[data-action="fill-demo"]').click();
    await page.locator('#connect-form button[type="submit"]').click();
    assert(await page.locator(outcome === 'unsupported' ? '[role="status"]' : '[role="alert"]').isVisible());
    assert.equal(await page.locator('#next-model').count(), 0, `${outcome}: must not show verified state`);
    await page.screenshot({ path: `${output}/${outcome}.png`, fullPage: true });
    checks.push(`Outcome ${outcome}: distinct feedback and no verified model state`);
  }
  await open('a', '&screen=credential&provider=openai');
  assert.equal(await page.locator('#api-key').count(), 0, 'ChatGPT requires no key');
  await page.locator('[data-action="key-method"]').click();
  assert(await page.locator('#api-key').isVisible(), 'OpenAI API path has key input');
  await page.locator('[data-action="signin-method"]').click();
  await page.locator('#connect-form button[type="submit"]').click();
  assert(await page.locator('.code').isVisible(), 'OAuth device step visible');
  checks.push('OpenAI: distinct API key and ChatGPT device-code routes');
  await open('a', '&screen=credential&provider=vertex');
  assert.equal(await page.locator('input[required]:visible').count(), 2, 'Cloud project/location stay visible');
  await page.locator('#cloud-auth').selectOption('json');
  assert(await page.locator('#credential-json').isVisible());
  checks.push('Vertex: project/location and conditional credential JSON');
  await open('a', '&screen=credential&provider=custom');
  assert(await page.locator('#custom-format').isVisible());
  await page.locator('#custom-auth').selectOption('none');
  assert.equal(await page.locator('#api-key').isVisible(), false);
  await page.locator('[data-action="fill-demo"]').click();
  await page.locator('#connect-form button[type="submit"]').click();
  assert(await page.locator('[data-action="confirm-destination"]').isVisible());
  await page.locator('[data-action="confirm-destination"]').click();
  assert(await page.locator('#next-model').isVisible());
  checks.push('Custom: explicit API format and keyless route');
  await open('a', '&screen=all');
  await page.locator('#provider-search').fill('no-such-provider');
  assert(await page.locator('#no-results').isVisible());
  await page.locator('#provider-search').fill('groq');
  assert(await page.locator('[data-provider="groq"]').isVisible());
  assert.equal(await page.locator('[data-provider="anthropic"]').isVisible(), false);
  checks.push('Catalog: searchable provider names and no-results recovery');
  async function panelCheck(label, check) {
    try { await check(); checks.push(label); }
    catch (error) { failures.push(`${label}: ${error.message}`); }
  }
  await panelCheck('Invalid advanced URL opens disclosure and receives focus', async () => {
    await open('a', '&screen=credential&provider=anthropic');
    await page.locator('[data-action="fill-demo"]').click();
    await page.locator('#advanced summary').click();
    await page.locator('[name="advanced-url"]').fill('not-a-url');
    await page.locator('#advanced summary').click();
    await page.locator('#connect-form button[type="submit"]').click();
    assert(await page.locator('#advanced').evaluate(el => el.open));
    assert(await page.locator('[name="advanced-url"]').evaluate(el => el === document.activeElement));
  });
  await panelCheck('Inline outcomes retain visible focused headings', async () => {
    for (const outcome of ['success', 'auth', 'endpoint', 'unsupported']) {
      await open('b', '&screen=credential&provider=anthropic');
      await page.locator('#demo-outcome').selectOption(outcome);
      await page.locator('[data-action="fill-demo"]').click();
      await page.locator('#connect-form button[type="submit"]').click();
      assert(await page.locator('[data-heading]').isVisible(), `${outcome}: heading visible`);
      assert(await page.locator('[data-heading]').evaluate(el => el === document.activeElement));
    }
  });
  await panelCheck('Error recovery and method switching retain volatile drafts', async () => {
    await open('a', '&screen=credential&provider=anthropic');
    await page.locator('[data-action="fill-demo"]').click();
    await page.locator('#advanced summary').click();
    await page.locator('[name="connection-name"]').fill('Panel custom name');
    await page.locator('[name="advanced-url"]').fill('https://team.example.com/v1');
    await page.locator('#demo-outcome').selectOption('auth');
    await page.locator('#connect-form button[type="submit"]').click();
    assert(await page.locator('[data-action="confirm-destination"]').isVisible());
    await page.locator('[data-action="confirm-destination"]').click();
    assert(await page.locator('[role="alert"]').isVisible());
    await page.locator('[data-action="credential"]').click();
    assert.equal(await page.locator('#api-key').inputValue(), 'demo-key-not-a-secret');
    assert.equal(await page.locator('[name="connection-name"]').inputValue(), 'Panel custom name');
    assert.equal(await page.locator('[name="advanced-url"]').inputValue(), 'https://team.example.com/v1');
    await open('a', '&screen=credential&provider=openai');
    await page.locator('[data-action="key-method"]').click();
    await page.locator('#api-key').fill('volatile-demo');
    await page.locator('[data-action="signin-method"]').click();
    await page.locator('[data-action="key-method"]').click();
    assert.equal(await page.locator('#api-key').inputValue(), 'volatile-demo');
    assert(await page.evaluate(() => localStorage.length === 0 && sessionStorage.length === 0));
  });
  await panelCheck('Directory preserves mobile search and return focus', async () => {
    await page.setViewportSize({ width: 390, height: 844 });
    await open('f');
    await page.locator('#provider-search').fill('groq');
    await page.locator('[data-provider="groq"]').click();
    await page.getByRole('button', { name: '← Change provider', exact: true }).click();
    assert.equal(await page.locator('#provider-search').inputValue(), 'groq');
    const position = await page.locator('[data-provider="groq"]').evaluate(el => ({ focused: el === document.activeElement, top: el.getBoundingClientRect().top, bottom: el.getBoundingClientRect().bottom, height: innerHeight }));
    assert(position.focused && position.top >= 0 && position.bottom <= position.height, JSON.stringify(position));
  });
  await panelCheck('Form boundaries meet 3:1 non-text contrast', async () => {
    await open('a', '&screen=credential&provider=anthropic');
    const colors = await page.locator('#api-key').evaluate(el => ({ border: getComputedStyle(el).borderTopColor, fill: getComputedStyle(el).backgroundColor, panel: getComputedStyle(el.closest('.panel')).backgroundColor }));
    const luminance = color => color.match(/\d+/g).slice(0, 3).map(Number).map(value => { const s = value / 255; return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4; }).reduce((sum, value, index) => sum + value * [0.2126, 0.7152, 0.0722][index], 0);
    for (const adjacent of ['fill', 'panel']) {
      const a = luminance(colors.border), b = luminance(colors[adjacent]);
      assert((Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05) >= 3, `${adjacent}: ${JSON.stringify(colors)}`);
    }
  });
  await panelCheck('Access-first Change provider returns to provider choices', async () => {
    await open('e');
    await chooseAnthropic('e');
    await page.getByRole('button', { name: '← Change provider', exact: true }).click();
    assert(await page.locator('[data-provider="anthropic"]').isVisible());
  });
  await panelCheck('API-key entry preserves OpenAI method intent', async () => {
    await open('e');
    await page.locator('[data-action="picker"]').first().click();
    await page.locator('[data-provider="openai"]').click();
    assert(await page.locator('#api-key').isVisible());
  });
  await panelCheck('Cancel exits setup and discards volatile credentials', async () => {
    for (const concept of ['a', 'b', 'c', 'd', 'e', 'f']) {
      await open(concept, '&screen=credential&provider=anthropic');
      await page.locator('[data-action="fill-demo"]').click();
      await page.getByRole('button', { name: 'Cancel', exact: true }).click();
      assert.equal(await page.locator('[data-provider]:visible').count(), 0);
      assert.equal(await page.locator('#connect-form').count(), 0);
      await page.locator('[data-action="home"]').last().click();
      await chooseAnthropic(concept);
      assert.equal(await page.locator('#api-key').inputValue(), '');
    }
  });
  await panelCheck('Save and configuration failures have explicit recovery states', async () => {
    for (const screen of ['missing', 'configuration', 'save-failure', 'partial-save']) {
      await open('a', `&screen=${screen}&provider=anthropic`);
      assert(await page.locator('[role="alert"]').isVisible(), screen);
      assert.equal(await page.locator('#next-model').count(), 0);
      assert(await page.locator('[data-action="credential"]').isVisible());
      await page.screenshot({ path: `${output}/${screen}.png`, fullPage: true });
    }
  });
  await panelCheck('Vertex JSON is concealed by default with explicit reveal', async () => {
    await open('a', '&screen=credential&provider=vertex');
    await page.locator('#cloud-auth').selectOption('json');
    assert.equal(await page.locator('#credential-json').getAttribute('type'), 'password');
    await page.locator('[data-action="show-json"]').click();
    assert.equal(await page.locator('#credential-json').getAttribute('type'), 'text');
  });
  await panelCheck('Local keys and advanced editor remain reachable', async () => {
    await open('a', '&screen=credential&provider=ollama');
    await page.locator('#advanced summary').click();
    assert.equal(await page.locator('#api-key').count(), 1);
    assert.equal(await page.locator('#api-key').getAttribute('required'), null);
    await page.locator('[data-action="editor"]').click();
    assert(await page.locator('[data-action="credential"]').isVisible());
  });
  await panelCheck('Endpoint overrides require destination and credential review', async () => {
    await open('a', '&screen=credential&provider=anthropic');
    await page.locator('[data-action="fill-demo"]').click();
    await page.locator('#advanced summary').click();
    await page.locator('[name="advanced-url"]').fill('https://team.example.com/v1');
    await page.locator('#connect-form button[type="submit"]').click();
    assert.equal(await page.locator('#next-model').count(), 0);
    assert(await page.locator('[data-action="confirm-destination"]').isVisible());
    await page.screenshot({ path: `${output}/review-destination.png`, fullPage: true });
  });
  await panelCheck('Host-access switching preserves the separate pasted-key draft', async () => {
    await open('a', '&screen=credential&provider=anthropic');
    await page.locator('#api-key').fill('volatile-key-draft');
    assert.equal(await page.locator('[data-action="source-host"]').isVisible(), false);
    await page.locator('#advanced summary').click();
    await page.locator('[data-action="source-host"]').click();
    assert.equal(await page.locator('input[required]').count(), 0);
    await page.locator('[data-action="source-key"]').click();
    assert.equal(await page.locator('#api-key').inputValue(), 'volatile-key-draft');
    await open('a', '&screen=credential&provider=azure');
    assert.equal(await page.locator('input[required]').count(), 2);
    assert.equal(await page.locator('[data-action="source-host"]').count(), 0, 'Azure uses its explicit resource/key route or full editor');
  });
  await panelCheck('Remote Ollama review uses the entered server destination', async () => {
    await open('a', '&screen=credential&provider=ollama');
    await page.locator('#endpoint').fill('https://local-models.example.com/v1');
    await page.locator('#connect-form button[type="submit"]').click();
    assert.equal(await page.locator('.destination').count(), 1);
    assert.equal(await page.locator('.destination').textContent(), 'https://local-models.example.com/v1');
    assert.doesNotMatch(await page.locator('.callout').textContent(), /New credential supplied/);
    await page.locator('[data-action="credential"]').click();
    await page.locator('#advanced summary').click();
    await page.locator('#api-key').fill('demo-remote-key');
    await page.locator('#connect-form button[type="submit"]').click();
    assert.match(await page.locator('.callout').textContent(), /New credential supplied/);
  });
  for (const concept of ['a', 'b', 'c', 'd', 'e', 'f']) {
    await panelCheck(`${concept.toUpperCase()}: zero-provider first-run start`, async () => {
      await open(concept);
      assert(await page.locator('[data-empty-state]').isVisible());
      assert.equal(await page.locator('#next-model').count(), 0);
      assert.equal(await page.locator('.provider.selected').count(), 0);
    });
    await panelCheck(`${concept.toUpperCase()}: first connection leads to first session`, async () => {
      await open(concept);
      await chooseAnthropic(concept);
      await page.locator('[data-action="fill-demo"]').click();
      await page.locator('#connect-form button[type="submit"]').click();
      await page.locator('[data-action="done"]').click();
      assert(await page.locator('#first-session-prompt').isVisible());
      assert.equal(await page.locator('[data-session-connection]').getAttribute('data-session-connection'), 'anthropic');
      await page.locator('#first-session-prompt').fill('Help me understand this project');
      assert.equal(await page.locator('#first-session-prompt').inputValue(), 'Help me understand this project');
      await page.screenshot({ path: `${output}/${concept}-first-session.png`, fullPage: true });
    });
  }
  await page.setViewportSize({ width: 1440, height: 1050 });
  await page.goto(new URL('index.html', base).href);
  await noOverflow('Final gallery desktop');
  await page.screenshot({ path: `${output}/gallery.png`, fullPage: true });
  assert.deepEqual(failures, [], 'Browser console and network must be clean');
  const report = { passed: true, checks, errors: failures, scope: 'Design-prototype browser checks only; no production integration or real auth verified' };
  await writeFile(`${output}/verification.json`, JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify(report, null, 2));
} finally {
  await browser.close();
}
