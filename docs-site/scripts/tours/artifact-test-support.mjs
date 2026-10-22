// Test support only, deliberately independent of the production PNG encoder.
import { mkdtemp, mkdir, writeFile, rm, realpath } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { deflateSync } from 'node:zlib';
import { sourceTreeDigest, gitEnvironment } from './artifacts.mjs';

const hash = b => createHash('sha256').update(b).digest('hex');
// Cached buffers stay private. Return copies so callers cannot mutate the cache.
const pngCache = new Map();

export function chunk(type, data) {
  const body = Buffer.concat([Buffer.from(type), data]);
  let crc = 0xffffffff;
  for (const b of body) {
    crc ^= b;
    for (let i = 0; i < 8; i++) crc = (crc >>> 1) ^ (crc & 1 ? 0xedb88320 : 0);
  }
  const size = Buffer.alloc(4);
  size.writeUInt32BE(data.length);
  const end = Buffer.alloc(4);
  end.writeUInt32BE((crc ^ 0xffffffff) >>> 0);
  return Buffer.concat([size, body, end]);
}

// Independent PNG fixture encoder, producing real RGB scanlines.
export function independentPNG(width, height) {
  const key = `${width}x${height}`;
  if (pngCache.has(key)) return Buffer.from(pngCache.get(key));

  const header = Buffer.alloc(13);
  header.writeUInt32BE(width);
  header.writeUInt32BE(height, 4);
  header[8] = 8;
  header[9] = 2;
  const bytes = Buffer.concat([
    Buffer.from('89504e470d0a1a0a', 'hex'),
    chunk('IHDR', header),
    chunk('IDAT', deflateSync(Buffer.alloc((width * 3 + 1) * height))),
    chunk('IEND', Buffer.alloc(0)),
  ]);
  pngCache.set(key, bytes);
  return Buffer.from(bytes);
}

export async function candidateFixture(t, required, parent=tmpdir()) {
  const base = await realpath(await mkdtemp(join(parent, 'tour-artifacts-test-')));
  t.after(() => rm(base, { recursive: true, force: true }));
  const repo = join(base, 'repo');
  await mkdir(join(repo, 'docs/assets/tutorials'), { recursive: true });
  await mkdir(join(repo, 'docs-site'), { recursive: true });
  await writeFile(join(repo, 'docs/assets/tutorials/approved.png'), independentPNG(1, 1));
  await writeFile(join(repo, 'docs-site/publication.json'), '{}');
  await writeFile(join(repo, 'go.mod'), 'module example\n');
  const git = (...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8', env: gitEnvironment(repo) });
  const template = join(base, 'empty-git-template');
  await mkdir(template);
  git('init', '-q', `--template=${template}`);
  git('add', '.');
  git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'fixture');
  const root = join(base, 'candidate');
  await mkdir(root);
  const rows = [];
  for (const r of required) {
    const file = `${r.scenario}/${r.surface}/${r.checkpoint}.png`;
    await mkdir(join(root, r.scenario, r.surface), { recursive: true });
    const dimensions = r.surface === 'cli' ? { width: 1200, height: 1800 } : { width: 1280, height: 900 };
    const bytes = independentPNG(dimensions.width, dimensions.height);
    await writeFile(join(root, file), bytes);
    const row = {
      ...r,
      representation: r.surface === 'cli' ? 'rendered-command-transcript' : 'web-screenshot',
      file,
      sha256: hash(bytes),
      dimensions,
      assertions: [{ name: 'oracle', passed: true }],
      result: 'passed',
    };
    if (r.surface === 'cli') {
      row.text = { file: file.replace('.png', '.txt'), sha256: hash('recorded output') };
      await writeFile(join(root, row.text.file), 'recorded output');
    }
    rows.push(row);
  }
  const manifest = {
    version: 1,
    sourceRevision: git('rev-parse', 'HEAD').trim(),
    sourceTreeDigest: await sourceTreeDigest(repo),
    toolVersions: { node: process.version, go: 'go1.26', playwright: '1.56.1' },
    coverage: required,
    complete: true,
    deferredSurfaces: ['native'],
    rows,
  };
  const save = () => writeFile(join(root, 'manifest.json'), JSON.stringify(manifest));
  await save();
  return { base, repo, root, manifest, save, git };
}
