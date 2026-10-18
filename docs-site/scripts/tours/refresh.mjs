import { spawnSync } from 'node:child_process';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir, homedir, platform } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { renderTranscript } from './transcript.mjs';

const args = process.argv.slice(2);
const surfaces = args[2]?.split(',') ?? [];
if (args[0] !== 'check' || args[1] !== '--surfaces' || args[3] !== '--scenario' || !['receipt', 'daily', 'configuration', 'extensions'].includes(args[4]) || args.length !== 5 || !surfaces.length || surfaces.some(s => !['cli', 'web'].includes(s))) {
 console.error('Use check --surfaces cli,web --scenario receipt|daily|configuration|extensions. No assets were published.');
 process.exitCode = 1;
} else {
 const root = fileURLToPath(new URL('../../../', import.meta.url));
 for (const surface of surfaces) {
 const out = await mkdtemp(join(tmpdir(), 'evener-tutorial-'));
 try {
  const run = spawnSync('go', ['test', './cmd/evener-hub', '-run', '^TestTutorialTours$', '-count=1', '-timeout=20m', '-v'], {
   cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
   env: { ...process.env, PLAYWRIGHT_BROWSERS_PATH: process.env.PLAYWRIGHT_BROWSERS_PATH || join(platform() === 'darwin' ? join(homedir(), 'Library', 'Caches') : process.env.XDG_CACHE_HOME || join(homedir(), '.cache'), 'ms-playwright'), EVENER_TUTORIAL_TOURS: '1', EVENER_TUTORIAL_SURFACES: surface, EVENER_TUTORIAL_SCENARIO: args[4], EVENER_TUTORIAL_OUT: out },
  });
  if (run.error) throw run.error;
  if (run.status !== 0) throw new Error(`Tour failed (${run.status})\n${run.stdout}\n${run.stderr}`);
  const result = JSON.parse(await readFile(join(out, 'result.json'), 'utf8'));
  if (result.scenario !== args[4] || result.surface !== surface || !result.executed || !result.passed || result.checkpoints.join(',') !== 'entry,action,verified') throw new Error('Incomplete tour evidence');
  for (const id of result.checkpoints) {
   if (surface === 'cli') {
   const record = JSON.parse(await readFile(join(out, `${id}.json`), 'utf8'));
   await renderTranscript(record, join(out, `${id}.png`));
   }
   for (const ext of surface === 'cli' ? ['txt', 'png'] : ['png']) {
    const hash = createHash('sha256').update(await readFile(join(out, `${id}.${ext}`))).digest('hex');
    console.log(`${id}.${ext} sha256=${hash}`);
   }
  }
  console.log(`${args[4]}/${surface}: executed=1 passed=1 checkpoints=3 coverage=${result.coverage} (partial coverage, no publication)`);
 } catch (error) {
  console.error(error.message);
  process.exitCode = 1;
 } finally { await rm(out, { recursive: true, force: true }); }
}
}
