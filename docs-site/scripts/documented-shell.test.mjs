import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, closeSync, existsSync, mkdirSync, mkdtempSync, openSync, readFileSync, readlinkSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import test, { after } from 'node:test';

const doc = readFileSync(new URL('../../docs/daily-work.md', import.meta.url), 'utf8');
const blocks = [...doc.matchAll(/```sh\n([\s\S]*?)\n```/g)].map((match) => match[1]);
assert.ok(blocks.length >= 2);
const [block, continuation] = blocks;
const fixtureParent = realpathSync(mkdtempSync(path.join(process.env.EVENER_SCRATCH_DIR || process.env.TMPDIR || '/tmp', 'documented-shell-fixtures-')));
after(() => rmSync(fixtureParent, { recursive: true, force: true }));

function runDailyBlock(t, { collision, failGit = false, resume = false, parent = fixtureParent } = {}) {
  const root = realpathSync(mkdtempSync(path.join(parent, 'daily-work-')));
  const caller = path.join(root, 'caller');
  mkdirSync(caller);
  t.after(() => {
    if (collision === 'unwritable') chmodSync(caller, 0o755);
    rmSync(root, { recursive: true, force: true });
  });
  const workspace = path.join(caller, 'daily-workspace');
  const target = path.join(root, 'target');
  if (collision === 'directory') {
    mkdirSync(workspace);
    writeFileSync(path.join(workspace, 'keep'), Buffer.from([0, 13, 255]));
  }
  if (collision === 'file') writeFileSync(workspace, Buffer.from([0, 13, 255]));
  if (collision === 'symlink') {
    mkdirSync(target);
    writeFileSync(path.join(target, 'keep'), Buffer.from([0, 13, 255]));
    symlinkSync(target, workspace);
  }
  if (collision === 'dangling') symlinkSync(path.join(root, 'absent'), workspace);
  if (resume) {
    mkdirSync(workspace);
    writeFileSync(path.join(workspace, 'initial.json'), '{"nonce":"first","receipts":["first"]}');
    writeFileSync(path.join(workspace, 'continued.json'), '{"nonce":"second","receipts":["second"]}');
  }
  const credentials = collision === 'unwritable' && process.getuid() === 0 ? { uid: 65534, gid: 65534 } : {};
  // Only the nonsecret probe must be reachable after dropping uid. Keep the
  // workspace, HOME and logs under private scratch, whose ancestors may be 0700.
  const bin = credentials.uid !== undefined ? realpathSync(mkdtempSync('/tmp/evener-docs-probe-')) : path.join(root, 'bin');
  if (credentials.uid !== undefined) {
    t.after(() => rmSync(bin, { recursive: true, force: true }));
    chmodSync(bin, 0o755);
  } else {
    mkdirSync(bin);
  }
  const calls = path.join(root, 'calls');
  const evener = path.join(bin, 'evener');
  writeFileSync(evener, '#!/bin/sh\nprintf "%s\\t%s\\n" "$PWD" "$1" >&4\n');
  chmodSync(evener, 0o755);
  if (failGit) {
    const git = path.join(bin, 'git');
    writeFileSync(git, '#!/bin/sh\nprintf "%s\\t%s\\n" "$PWD" "$1" >> "$GIT_CALLS"\nexit 41\n');
    chmodSync(git, 0o755);
  }
  const env = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('GIT_')));
  mkdirSync(path.join(root, 'home'));
  Object.assign(env, {
    HOME: path.join(root, 'home'),
    XDG_CONFIG_HOME: path.join(root, 'home'),
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_CEILING_DIRECTORIES: root,
    PATH: `${bin}:${env.PATH}`,
    GIT_CALLS: path.join(root, 'git-calls'),
    CALLER_AFTER: path.join(root, 'caller-after'),
  });
  if (collision === 'unwritable') chmodSync(caller, 0o555);
  const run = resume ? continuation : block;
  // Capture the block's status before recording the caller's CWD; do not put the block
  // in an if condition, which would disable errexit in its subshell.
  // Inherited FDs stay writable without exposing private log paths to the dropped uid.
  const callerAfter = openSync(env.CALLER_AFTER, 'w', 0o600);
  const callsFD = openSync(calls, 'w', 0o600);
  const stdio = ['ignore', 'pipe', 'pipe', callerAfter, callsFD];
  let probeCalls = '';
  let result;
  try {
    if (collision === 'unwritable') {
      const check = spawnSync('/bin/sh', ['-c', 'id -u && evener --fixture-probe'], { cwd: caller, env, encoding: 'utf8', stdio, ...credentials });
      assert.ifError(check.error);
      assert.equal(check.status, 0, check.stderr);
      assert.equal(check.stdout.trim(), String(credentials.uid ?? process.getuid()));
      probeCalls = readFileSync(calls, 'utf8');
      assert.equal(probeCalls, `${caller}\t--fixture-probe\n`);
    }
    result = spawnSync('/bin/sh', ['-c', `${run}\nblock_status=$?\nprintf '%s' "$PWD" >&3\nexit "$block_status"`], {
      cwd: caller, env, encoding: 'utf8', stdio, ...credentials,
    });
  } finally {
    closeSync(callerAfter);
    closeSync(callsFD);
  }
  assert.ifError(result.error);
  const recordedCalls = readFileSync(calls, 'utf8');
  if (collision === 'unwritable') assert.equal(recordedCalls, probeCalls);
  const blockCalls = recordedCalls.slice(probeCalls.length).trimEnd();
  const agentCalls = blockCalls ? blockCalls.split('\n').map((line) => {
    const [cwd, firstArg] = line.split('\t');
    return { cwd, firstArg };
  }) : [];
  const gitRoots = [];
  const realGitEnv = { ...env, PATH: process.env.PATH };
  for (const directory of [caller, workspace]) {
    if (!existsSync(directory)) continue;
    const top = spawnSync('git', ['-C', directory, 'rev-parse', '--show-toplevel'], { env: realGitEnv, encoding: 'utf8' });
    assert.ifError(top.error);
    if (top.status !== 0) continue;
    const gitDir = spawnSync('git', ['-C', directory, 'rev-parse', '--absolute-git-dir'], { env: realGitEnv, encoding: 'utf8' });
    assert.equal(gitDir.status, 0, gitDir.stderr);
    gitRoots.push({ top: top.stdout.trim(), gitDir: gitDir.stdout.trim() });
  }
  let preserved = null;
  if (collision === 'directory') preserved = readFileSync(path.join(workspace, 'keep'));
  if (collision === 'file') preserved = readFileSync(workspace);
  if (collision === 'symlink') preserved = { link: readlinkSync(workspace), bytes: readFileSync(path.join(target, 'keep')) };
  if (collision === 'dangling') preserved = readlinkSync(workspace);
  return {
    status: result.status, stderr: result.stderr, stdout: result.stdout,
    callerCwd: readFileSync(env.CALLER_AFTER, 'utf8'), agentCwds: agentCalls.map(({ cwd }) => cwd),
    agentCalls, gitRoots, preserved, workspace, caller, root,
    gitCalls: existsSync(env.GIT_CALLS) ? readFileSync(env.GIT_CALLS, 'utf8') : '',
  };
}

test('fresh workspace runs only inside its new directory', (t) => {
  const r = runDailyBlock(t);
  assert.equal(r.status, 0, r.stderr);
  assert.deepEqual(r.agentCalls, [{ cwd: r.workspace, firstArg: '--model' }, { cwd: r.workspace, firstArg: '--list-sessions' }]);
  assert.deepEqual(r.gitRoots, [{ top: r.workspace, gitDir: path.join(r.workspace, '.git') }]);
  assert.equal(r.callerCwd, r.caller);
});

for (const resume of [false, true]) {
  test(`documented block uses physical paths from a symlinked fixture parent (resume=${resume})`, (t) => {
    const outer = mkdtempSync(path.join(fixtureParent, 'aliased-parent-'));
    t.after(() => rmSync(outer, { recursive: true, force: true }));
    const physical = path.join(outer, 'physical');
    const alias = path.join(outer, 'alias');
    mkdirSync(physical);
    symlinkSync(physical, alias, 'dir');
    const r = runDailyBlock(t, { parent: alias, resume });
    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.callerCwd, r.caller);
    assert.deepEqual(r.agentCalls, (resume ? ['--resume'] : ['--model', '--list-sessions']).map(firstArg => ({ cwd: r.workspace, firstArg })));
    assert.deepEqual(r.gitRoots, resume ? [] : [{ top: r.workspace, gitDir: path.join(r.workspace, '.git') }]);
    if (resume) assert.deepEqual(JSON.parse(r.stdout), {
      'continued.json': { nonce: 'second', receipts: ['second'] },
      'initial.json': { nonce: 'first', receipts: ['first'] },
    });
  });
}

for (const collision of ['directory', 'file', 'symlink', 'dangling']) {
  test(`colliding destinations preserve contents and do not launch (${collision})`, (t) => {
    const r = runDailyBlock(t, { collision });
    assert.deepEqual(r.agentCwds, []);
    assert.notEqual(r.status, 0);
    assert.deepEqual(r.gitRoots, []);
    assert.equal(r.callerCwd, r.caller);
    if (collision === 'directory' || collision === 'file') assert.deepEqual(r.preserved, Buffer.from([0, 13, 255]));
    if (collision === 'symlink') assert.deepEqual(r.preserved, { link: path.join(r.root, 'target'), bytes: Buffer.from([0, 13, 255]) });
    if (collision === 'dangling') {
      assert.equal(r.preserved, path.join(r.root, 'absent'));
      assert.equal(existsSync(r.preserved), false);
    }
  });
}

test('unwritable parent does not launch', (t) => {
  const r = runDailyBlock(t, { collision: 'unwritable' });
  assert.notEqual(r.status, 0, r.stderr);
  assert.deepEqual(r.agentCwds, []);
  assert.deepEqual(r.gitRoots, []);
  assert.equal(existsSync(r.workspace), false);
  assert.equal(r.callerCwd, r.caller);
});

test('Git failure prevents launch', (t) => {
  const r = runDailyBlock(t, { failGit: true });
  assert.equal(r.status, 41, r.stderr);
  assert.equal(r.gitCalls, `${r.workspace}\tinit\n`);
  assert.deepEqual(r.agentCwds, []);
  assert.deepEqual(r.gitRoots, []);
  assert.equal(r.callerCwd, r.caller);
});

test('continuation runs and verifies files inside the daily workspace', (t) => {
  const r = runDailyBlock(t, { resume: true });
  assert.equal(r.status, 0, r.stderr);
  assert.deepEqual(r.agentCalls, [{ cwd: r.workspace, firstArg: '--resume' }]);
  assert.deepEqual(JSON.parse(r.stdout), {
    'continued.json': { nonce: 'second', receipts: ['second'] },
    'initial.json': { nonce: 'first', receipts: ['first'] },
  });
  assert.equal(r.callerCwd, r.caller);
});
