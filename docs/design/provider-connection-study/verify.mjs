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
  for (const concept of ['a', 'b', 'c', 'd', 'e', 'f']) {
    await page.setViewportSize({ width: 1440, height: 1050 });
    await open(concept);
    await noOverflow(`${concept} desktop start`);
    await page.screenshot({ path: `${output}/${concept}.png`, fullPage: true });
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
  assert(await page.locator('#next-model').isVisible());
  checks.push('Custom: explicit API format and keyless route');
  await open('a', '&screen=all');
  await page.locator('#provider-search').fill('no-such-provider');
  assert(await page.locator('#no-results').isVisible());
  await page.locator('#provider-search').fill('groq');
  assert(await page.locator('[data-provider="groq"]').isVisible());
  assert.equal(await page.locator('[data-provider="anthropic"]').isVisible(), false);
  checks.push('Catalog: searchable provider names and no-results recovery');
  assert.deepEqual(failures, [], 'Browser console and network must be clean');
  const report = { passed: true, checks, errors: failures, scope: 'Design-prototype browser checks only; no production integration or real auth verified' };
  await writeFile(`${output}/verification.json`, JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify(report, null, 2));
} finally {
  await browser.close();
}
