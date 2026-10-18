import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, writeFile, readFile, rm, symlink, readdir, stat } from 'node:fs/promises';
import { join, dirname, relative } from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { independentPNG, candidateFixture } from './artifact-test-support.mjs';
import * as artifacts from './artifacts.mjs';
import { generateCandidate } from './refresh.mjs';
import * as refresh from './refresh.mjs';

const hash = b => createHash('sha256').update(b).digest('hex');
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
 assert.equal(failure.message,`Command failed (7); private diagnostics: ${log}`);
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
