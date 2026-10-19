import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, writeFile, readFile, rm, symlink, readdir, stat, mkdtemp, realpath, cp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, dirname, relative } from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { independentPNG, candidateFixture } from './artifact-test-support.mjs';
import * as artifacts from './artifacts.mjs';
import { generateCandidate } from './refresh.mjs';
import * as refresh from './refresh.mjs';

const hash = b => createHash('sha256').update(b).digest('hex');
for (const mode of ['signing', 'hooks', 'template', 'selection', 'read-chain']) {
 test(`Git isolation: ${mode}`, async t => {
  const f = await candidateFixture(t, required);
  const decoy = await candidateFixture(t, []);
  await writeFile(join(decoy.repo, 'go.sum'), 'decoy source\n');
  decoy.git('add', '.');
  decoy.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'decoy');
  const source = await readFile(join(f.repo,'go.mod'));
  const expectedDigest = createHash('sha256').update(JSON.stringify(['go.mod',source.length])).update(source).digest('hex');
  assert.equal(f.manifest.sourceTreeDigest, expectedDigest);
  assert.notEqual(decoy.git('rev-parse','HEAD').trim(), f.manifest.sourceRevision);
  async function snapshot(path) {
   const entries = await readdir(path, { withFileTypes: true });
   const result = {};
   for (const entry of entries.sort((a,b)=>a.name.localeCompare(b.name))) {
    result[entry.name] = entry.isDirectory() ? await snapshot(join(path,entry.name)) : hash(await readFile(join(path,entry.name)));
   }
   return result;
  }
  const before = await snapshot(decoy.repo);
  const hooks = join(f.base, 'hooks');
  const template = join(f.base, 'template');
  await mkdir(hooks);
  await mkdir(join(template, 'hooks'), { recursive: true });
  for (const dir of [hooks, join(template, 'hooks')]) {
   await writeFile(join(dir,'pre-commit'), '#!/bin/sh\nexit 71\n', { mode: 0o755 });
  }
  const config = join(f.base, 'host.gitconfig');
  await writeFile(config, mode === 'signing'
   ? `[commit]\n gpgsign = true\n[user]\n signingkey = missing-test-key\n[gpg]\n program = ${join(f.base,'missing-gpg')}\n`
   : `[core]\n hooksPath = ${hooks}\n`);
  // Build the child baseline independently of the production environment helper.
  const env = { PATH: process.env.PATH, HOME: f.base, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' };
  if (mode === 'signing' || mode === 'hooks') env.GIT_CONFIG_GLOBAL = config;
  if (mode === 'template') env.GIT_TEMPLATE_DIR = template;
  if (mode === 'selection' || mode === 'read-chain') Object.assign(env, {
   GIT_DIR: join(decoy.repo,'.git'), GIT_COMMON_DIR: join(decoy.repo,'.git'),
   GIT_WORK_TREE: decoy.repo, GIT_INDEX_FILE: join(decoy.repo,'.git/index'),
   GIT_CONFIG: config, GIT_CONFIG_SYSTEM: config, GIT_CONFIG_GLOBAL: config,
   GIT_CONFIG_COUNT: '1', GIT_CONFIG_KEY_0: 'core.hooksPath', GIT_CONFIG_VALUE_0: hooks,
   GIT_CONFIG_PARAMETERS: "'commit.gpgsign=true'", GIT_TEMPLATE_DIR: template,
  });
  const script = `
   import assert from 'node:assert/strict';
   import { candidateFixture } from ${JSON.stringify(new URL('./artifact-test-support.mjs',import.meta.url).href)};
   import * as artifacts from ${JSON.stringify(new URL('./artifacts.mjs',import.meta.url).href)};
   import { writeFile } from 'node:fs/promises';
   import { join } from 'node:path';
   const cleanup=[];
   try {
    ${mode === 'read-chain' ? `
     assert.equal(artifacts.revision(${JSON.stringify(f.repo)}), ${JSON.stringify(f.manifest.sourceRevision)});
     assert.equal(await artifacts.sourceTreeDigest(${JSON.stringify(f.repo)}), ${JSON.stringify(expectedDigest)});
     await artifacts.validateCandidate(${JSON.stringify(f.root)}, artifacts.fullMatrix, {sourceRoot:${JSON.stringify(f.repo)}});
    ` : `
     const f=await candidateFixture({after:fn=>cleanup.push(fn)}, artifacts.fullMatrix, ${JSON.stringify(f.base)});
     await artifacts.validateCandidate(f.root, artifacts.fullMatrix, {sourceRoot:f.repo});
     f.git('-c','user.name=Test','-c','user.email=test@example.invalid','commit','--allow-empty','-qm','historical provenance');
     assert.equal(f.git('rev-list','--count','HEAD').trim(),'2');
     assert.notEqual(artifacts.revision(f.repo),f.manifest.sourceRevision);
     await assert.rejects(artifacts.validateCandidate(f.root, artifacts.fullMatrix, {sourceRoot:f.repo}));
     await artifacts.validateCandidate(f.root, artifacts.fullMatrix, {sourceRoot:f.repo,historicalProvenance:true});
     await writeFile(join(f.repo,'go.mod'),'module changed\\n');
     assert.notEqual(await artifacts.sourceTreeDigest(f.repo),f.manifest.sourceTreeDigest);
     // Repository-local hooks remain active, unlike ambient hooks.
     f.git('config','core.hooksPath',${JSON.stringify(hooks)});
     assert.throws(()=>f.git('-c','user.name=Test','-c','user.email=test@example.invalid','commit','--allow-empty','-qm','must fail'));
    `}
   } finally { for(const fn of cleanup.reverse()) await fn(); }
  `;
  try {
   execFileSync(process.execPath, ['--input-type=module','-e',script], {env, stdio:'pipe'});
  } finally {
   assert.deepEqual(await snapshot(decoy.repo), before, 'decoy repository must remain byte-identical');
  }
 });
}
test('internally-created check and fixture roots canonicalize a symlinked temp parent',async t=>{
 const base=await realpath(await mkdtemp(join(tmpdir(),'tour-symlink-test-')));
 t.after(()=>rm(base,{recursive:true,force:true}));
 const parent=join(base,'alias');await symlink(base,parent,'dir');
 await t.test('check root',async()=>{
  const owned=await refresh.createCheckRoot(parent);
  assert.equal(owned,await realpath(owned));
  const f=await candidateFixture(t,required);
  const result=await generateCandidate({out:join(owned,'candidate'),sourceRoot:f.repo,required,collect:out=>cp(f.root,out,{recursive:true})});
  assert.equal(result.rows.length,required.length);
 });
 await t.test('fixture root',async()=>{
  const f=await candidateFixture(t,required,parent);
  assert.equal(f.base,await realpath(f.base));
  await artifacts.validateCandidate(f.root,required,{sourceRoot:f.repo});
 });
 const f=await candidateFixture(t,required);
 let collected=false;
 await assert.rejects(generateCandidate({out:join(parent,'explicit'),sourceRoot:f.repo,required,collect:()=>{collected=true;throw Error('collection sentinel');}}));
 assert.equal(collected,false);
});
test('failure summary emits only allowlisted structure, never diagnostic text',()=>{
 const classify=artifacts.classifyTourFailure;
 assert.equal(typeof classify,'function');
 assert.deepEqual(classify('    tutorial_extensions_test.go:456: web driver open failed: Web driver action failed: open (folder-confirm; private diagnostics: /tmp/secret/failure.json)'),{step:'folder-confirm',code:'web-driver-failed',location:'tutorial_extensions_test.go:456'});
 assert.deepEqual(classify('    tutorial_daily_test.go:302: context deadline exceeded'),{step:'unknown',code:'deadline-exceeded',location:'tutorial_daily_test.go:302'});
 assert.deepEqual(classify('    tutorial_extensions_test.go:460: Web driver action failed: capture (TimeoutError)'),{step:'capture',code:'timeout',location:'tutorial_extensions_test.go:460'});
 for(const raw of [
  'Bearer secret-canary /Users/private/path https://example/auth/token',
  '    /tmp/tutorial_extensions_test.go:456: private payload',
  '    tutorial_secret_test.go:456: private payload',
  '    tutorial_extensions_test.go:456secret: private payload',
  'Web driver action failed: secret-canary (secret-canary)',
  'Web driver action failed: open-secret (folder-confirm-secret)',
 ]) assert.deepEqual(classify(raw),{step:'unknown',code:'unknown',location:'unknown'});
 const injected='    tutorial_extensions_test.go:456: Web driver action failed: open (folder-confirm; private diagnostics: /tmp/secret-canary) Bearer secret-canary\nRAW SECRET';
 assert.deepEqual(classify(injected),{step:'folder-confirm',code:'web-driver-failed',location:'tutorial_extensions_test.go:456'});
});
test('failed process exposes structural summary but keeps injected diagnostics private',async t=>{
 const raw='    tutorial_extensions_test.go:456: Web driver action failed: open (folder-confirm; private diagnostics: /tmp/private-secret-canary) Bearer secret-canary\nRAW SECRET';
 let failure;
 try {await refresh.run(process.execPath,['-e',`process.stderr.write(${JSON.stringify(raw)});process.exitCode=1`],{});}catch(error){failure=error;}
 assert.ok(failure?.diagnosticsPath);
 t.after(()=>rm(dirname(failure.diagnosticsPath),{recursive:true,force:true}));
 assert.equal(failure.message,`Command failed (1); step=folder-confirm code=web-driver-failed location=tutorial_extensions_test.go:456; private diagnostics: ${failure.diagnosticsPath}`);
 assert.equal(JSON.parse(await readFile(failure.diagnosticsPath,'utf8')).stderr,raw);
});
test('failed real process retains exact output privately outside candidate and approved files',async t=>{
 const f=await fixture(t);const out=join(f.base,'failed-process');
 const approved=await readFile(join(f.repo,'docs/assets/tutorials/approved.png'));
 const publication=await readFile(join(f.repo,'docs-site/publication.json'));
 assert.equal(typeof refresh.run,'function');
 let failure;
 try {
  await generateCandidate({out,sourceRoot:f.repo,required,collect:()=>refresh.run(process.execPath,['-e',"process.stdout.write('private stdout\\n');process.stderr.write('private stderr\\n');process.exitCode=7"],{cwd:f.repo})});
 } catch(error) { failure=error; }
 assert.ok(failure?.diagnosticsPath,'failure must report a private diagnostics path');
 const log=failure.diagnosticsPath;t.after(()=>rm(dirname(log),{recursive:true,force:true}));
 assert.ok(relative(out,log).startsWith('..'));assert.ok(relative(f.repo,log).startsWith('..'));
 assert.equal(failure.message,`Command failed (7); step=unknown code=unknown location=unknown; private diagnostics: ${log}`);
 assert.deepEqual(JSON.parse(await readFile(log,'utf8')),{exitCode:7,signal:null,stdout:'private stdout\n',stderr:'private stderr\n'});
 assert.deepEqual(await readdir(out),[]);
 assert.deepEqual(await readFile(join(f.repo,'docs/assets/tutorials/approved.png')),approved);
 assert.deepEqual(await readFile(join(f.repo,'docs-site/publication.json')),publication);
 if(process.platform!=='win32') {
  assert.equal((await stat(dirname(log))).mode&0o777,0o700);
  assert.equal((await stat(log)).mode&0o777,0o600);
 }
 assert.equal(await refresh.run(process.execPath,['-e',"process.stdout.write('success');process.stderr.write('not a failure')"],{cwd:f.repo}),'success');
});
function png(width, height) {
 return independentPNG(width, height);
}
const required = ['receipt','daily','configuration','extensions'].flatMap(scenario => ['cli','web'].flatMap(surface => ['entry','action','verified'].map(checkpoint => ({scenario,surface,checkpoint}))));
async function fixture(t) {
 return candidateFixture(t, required);
}
test('validates all 24 rows and rejects a native requirement',async t=>{const f=await fixture(t);assert.equal((await artifacts.validateCandidate(f.root,required,{sourceRoot:f.repo})).rows.length,24);await assert.rejects(artifacts.validateCandidate(f.root,[{scenario:'receipt',surface:'native',checkpoint:'entry'}],{sourceRoot:f.repo}));});
const mutations={
 'missing row':async f=>f.manifest.rows.pop(),
 'missing image':async f=>rm(join(f.root,f.manifest.rows[0].file)),
 'missing text':async f=>rm(join(f.root,f.manifest.rows[0].text.file)),
 'failed assertion':async f=>f.manifest.rows[0].assertions[0].passed=false,
 'empty assertions':async f=>f.manifest.rows[0].assertions=[],
 'failed scenario':async f=>f.manifest.rows[0].result='failed',
 'zero bytes':async f=>writeFile(join(f.root,f.manifest.rows[0].file),''),
 'invalid PNG with matching hash':async f=>{const b=Buffer.from('not PNG');await writeFile(join(f.root,f.manifest.rows[0].file),b);f.manifest.rows[0].sha256=hash(b);},
 'truncated PNG with matching hash':async f=>{const b=(await readFile(join(f.root,f.manifest.rows[0].file))).subarray(0,40);await writeFile(join(f.root,f.manifest.rows[0].file),b);f.manifest.rows[0].sha256=hash(b);},
 'wrong dimensions':async f=>f.manifest.rows[0].dimensions.width=1,
 'wrong hash':async f=>f.manifest.rows[0].sha256='0'.repeat(64),
 'wrong text hash':async f=>f.manifest.rows[0].text.sha256='0'.repeat(64),
 'duplicate IDs':async f=>f.manifest.rows.push(f.manifest.rows[0]),
 'stale dirty source':async f=>writeFile(join(f.repo,'go.mod'),'module changed\n'),
 'extra asset':async f=>writeFile(join(f.root,'extra.txt'),'extra'),
 'traversal':async f=>f.manifest.rows[0].file='../escape.png',
 'symlink':async f=>{const p=join(f.root,f.manifest.rows[0].file);await rm(p);await symlink(join(f.repo,'docs/assets/tutorials/approved.png'),p);},
 'manifest canary':async f=>f.manifest.note='credential-canary-123',
 'transcript canary':async f=>{const s='credential-canary-123';await writeFile(join(f.root,f.manifest.rows[0].text.file),s);f.manifest.rows[0].text.sha256=hash(s);},
 'private path':async f=>f.manifest.note='/Users/private/home',
 'false completeness':async f=>f.manifest.complete=false,
};
for(const [name,mutate] of Object.entries(mutations)) test(`rejects ${name}`,async t=>{const f=await fixture(t);await mutate(f);await f.save();await assert.rejects(artifacts.validateCandidate(f.root,required,{sourceRoot:f.repo}));});
test('source digest includes dirty and untracked active source but excludes deferred native',async t=>{const f=await fixture(t);const before=await artifacts.sourceTreeDigest(f.repo);await mkdir(join(f.repo,'agent'));await writeFile(join(f.repo,'agent/new.go'),'package agent');assert.notEqual(await artifacts.sourceTreeDigest(f.repo),before);const active=await artifacts.sourceTreeDigest(f.repo);await mkdir(join(f.repo,'mobile-native'));await writeFile(join(f.repo,'mobile-native/a.ts'),'native');assert.equal(await artifacts.sourceTreeDigest(f.repo),active);});
test('source digest covers runtime dependencies outside agent',async t=>{const f=await fixture(t);const before=await artifacts.sourceTreeDigest(f.repo);await mkdir(join(f.repo,'llm'));await writeFile(join(f.repo,'llm/provider.go'),'package llm');assert.notEqual(await artifacts.sourceTreeDigest(f.repo),before);});
test('cancelled generation cannot mark a collected candidate complete',async t=>{
 const f=await fixture(t);const controller=new AbortController();const out=join(f.base,'cancelled');
 const publicationFile=join(f.repo,'docs-site/publication.json');const before=await readFile(publicationFile);
 const image=join(f.repo,'docs/assets/tutorials/approved.png');const imageBefore=await readFile(image);
 await assert.rejects(generateCandidate({out,sourceRoot:f.repo,required,signal:controller.signal,collect:async destination=>{const {cp}=await import('node:fs/promises');await cp(f.root,destination,{recursive:true});controller.abort();}}));
 assert.equal(JSON.parse(await readFile(join(out,'manifest.json'),'utf8')).complete,false);
 assert.deepEqual(await readFile(publicationFile),before);
 assert.deepEqual(await readFile(image),imageBefore);
 assert.equal(hash(await readFile(image)),hash(imageBefore));
});
test('failed generation never rewrites a symlinked manifest target',async t=>{
 const f=await fixture(t);const publicationFile=join(f.repo,'docs-site/publication.json');const before=await readFile(publicationFile);
 await assert.rejects(generateCandidate({out:join(f.base,'linked-manifest'),sourceRoot:f.repo,required,collect:async destination=>{await symlink(publicationFile,join(destination,'manifest.json'));}}));
 assert.deepEqual(await readFile(publicationFile),before);
});
test('subset coverage stays incomplete and cannot satisfy the full requirement',async t=>{
 const f=await fixture(t);const subset=required.filter(r=>r.scenario==='receipt'&&r.surface==='cli');
 for(const row of f.manifest.rows.slice(3)){await rm(join(f.root,row.file));if(row.text)await rm(join(f.root,row.text.file));}
 f.manifest.rows=f.manifest.rows.slice(0,3);f.manifest.coverage=subset;f.manifest.complete=false;await f.save();
 assert.equal((await artifacts.validateCandidate(f.root,subset,{sourceRoot:f.repo})).complete,false);
 await assert.rejects(artifacts.validateCandidate(f.root,required,{sourceRoot:f.repo}));
});
test('refuses unsafe output and preserves approved bytes through success, failure and interruption',async t=>{
 const f=await fixture(t);const publicationFile=join(f.repo,'docs-site/publication.json');const before=await readFile(publicationFile);const image=join(f.repo,'docs/assets/tutorials/approved.png');const imageHash=hash(await readFile(image));
 await symlink(f.base,join(f.base,'link'));
 for(const out of [f.root,join(f.repo,'..candidate'),join(f.repo,'docs/assets/tutorials/new'),join(f.repo,'docs-site/.vitepress/dist/new'),join(f.base,'link/new')]) {
  let called=false;
  await assert.rejects(generateCandidate({out,sourceRoot:f.repo,required,collect:async()=>{called=true;throw Error('must not execute');}}));
  assert.equal(called,false);
 }
 for(const outcome of ['success','failure','interrupted']) {
  const out=join(f.base,outcome);
  const collect=async destination=>{if(outcome!=='success')throw Error(outcome);const {cp}=await import('node:fs/promises');await cp(f.root,destination,{recursive:true});};
  if(outcome==='success')await generateCandidate({out,sourceRoot:f.repo,required,collect});else await assert.rejects(generateCandidate({out,sourceRoot:f.repo,required,collect}));
  assert.deepEqual(await readFile(publicationFile),before);assert.equal(hash(await readFile(image)),imageHash);
 }
});
