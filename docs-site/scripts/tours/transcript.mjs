import { readFile, writeFile } from 'node:fs/promises';
import { chromium } from '@playwright/test';

function literal(value) { return String(value).replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); }

function replacePathTokens(text, path, replacement, descendants = false) {
 const boundary = descendants ? `[\\s/"']` : `[\\s"']`;
 return text.replace(new RegExp(`(^|[\\s"'])${literal(path)}(?=$|${boundary})`, 'g'), (_, prefix) => prefix + replacement);
}

export function normalizeTranscript(record) {
 const records = Array.isArray(record) ? record : [record];
 return 'Rendered command transcript\nStreams are recorded separately, not interleaved.\n\n' + records.map((r) => {
  const argv = r.argv.map((arg) => arg === r.owned?.executable ? 'evener' : arg);
  const output = (text) => r.owned?.executable ? replacePathTokens(text, r.owned.executable, 'evener') : text;
  let text = `$ ${argv.map((arg) => /^[A-Za-z0-9_./=:-]+$/.test(arg) ? arg : `'${arg.replaceAll("'", "'\"'\"'")}'`).join(' ')}\ncwd: ${r.cwdLabel}\nstdout:\n${output(r.stdout)}\nstderr:\n${output(r.stderr)}\nexit: ${r.exitCode}\n`;
  for (const path of r.owned?.paths ?? []) text = replacePathTokens(text, path, '<workspace>', true);
  for (const port of r.owned?.ports ?? []) text = text.replace(new RegExp(`port=${literal(port)}(?![0-9])`, 'g'), 'port=<port>');
  for (const id of r.owned?.ids ?? []) text = text.replace(new RegExp(`id=${literal(id)}(?![\\w-])`, 'g'), 'id=<id>');
  return text.replace(/https?:\/\/[^\s<>]+/g, '[URL REDACTED]')
   .replace(/Bearer\s+\S+/gi, 'Bearer [REDACTED]')
   .replace(/\bsk-[\w-]+/g, '[TOKEN REDACTED]')
   .replace(/((?:api[_-]?key|token|secret)\s*[=:]\s*)[^\s]+/gi, '$1[REDACTED]');
 }).join('\n');
}
export function transcriptHTML(record) {
 const escaped = normalizeTranscript(record).replace(/[&<>"']/g, (c) => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'}[c]));
 return `<!doctype html><html lang="en"><meta charset="utf-8"><style>body{margin:0;background:#101827;color:#edf2f7}pre{margin:32px;font:16px/1.5 Tour,TourGreek,monospace;white-space:pre-wrap;overflow-wrap:anywhere}*{animation:none!important;transition:none!important}</style><pre>${escaped}</pre></html>`;
}
export async function renderTranscript(record, destination) {
 if (!destination.endsWith('.png')) throw new Error('PNG destination required');
 const font = await readFile(new URL('../../node_modules/vitepress/dist/client/theme-default/fonts/inter-roman-latin.woff2', import.meta.url));
 const greek = await readFile(new URL('../../node_modules/vitepress/dist/client/theme-default/fonts/inter-roman-greek.woff2', import.meta.url));
 const browser = await chromium.launch({ headless: true });
 try {
  const page = await browser.newPage({ viewport: { width: 1200, height: 1800 }, deviceScaleFactor: 1, locale: 'en-US', timezoneId: 'UTC', reducedMotion: 'reduce' });
  await page.route('**/*', (route) => route.abort());
  await page.setContent(transcriptHTML(record));
  await page.addStyleTag({ content: `@font-face{font-family:Tour;src:url(data:font/woff2;base64,${font.toString('base64')})}@font-face{font-family:TourGreek;src:url(data:font/woff2;base64,${greek.toString('base64')})}` });
  await page.evaluate(() => document.fonts.ready);
  if (await page.evaluate(() => document.documentElement.scrollHeight > 1800)) throw new Error('Transcript exceeds fixed capture height');
  await page.screenshot({ path: destination, animations: 'disabled' });
  await writeFile(destination.replace(/\.png$/, '.txt'), normalizeTranscript(record));
 } finally { await browser.close(); }
}
