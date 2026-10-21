import test from 'node:test';
import assert from 'node:assert/strict';
import { cp, mkdir, readFile, readdir, readlink, writeFile, rm, symlink } from 'node:fs/promises';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync, spawnSync } from 'node:child_process';
import { candidateFixture, independentPNG, chunk } from './artifact-test-support.mjs';
import { fileURLToPath } from 'node:url';
import { reviewCandidate } from './review.mjs';
import { gitEnvironment } from './artifacts.mjs';

const required = ['receipt','daily','configuration','extensions'].flatMap(scenario => ['cli','web'].flatMap(surface => ['entry','action','verified'].map(checkpoint => ({scenario,surface,checkpoint}))));
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
async function snapshot(root) {
 const result = {};
 for (const entry of await readdir(root,{withFileTypes:true})) {
  const path=join(root,entry.name);
  result[entry.name] = entry.isSymbolicLink() ? {link:await readlink(path)} : entry.isDirectory() ? await snapshot(path) : hash(await readFile(path));
 }
 return result;
}
async function fixture(t) {
 const f = await candidateFixture(t,required);
 const published = join(f.base,'published');
 await cp(f.root,published,{recursive:true});
 return {...f,published,out:join(f.base,'review')};
}
async function review(f) {
 return reviewCandidate({candidate:f.root,published:f.published,sourceRoot:f.repo,out:f.out});
}
async function refusesCLI(f,code,extra=[]) {
 const before=await snapshot(f.base);
 const script=fileURLToPath(new URL('./review.mjs',import.meta.url));
 const result=spawnSync(process.execPath,[script,'--candidate',f.root,'--published',f.published,'--source-root',f.repo,'--out',f.out,...extra],{cwd:f.base,encoding:'utf8'});
 assert.equal(result.error,undefined);
 assert.equal(result.status,1);
 assert.equal(result.stdout,'');
 assert.deepEqual(await snapshot(f.base),before);
 for(const privateValue of [f.base,'credential-canary','/Users/private/path','not-a-repository-canary'])assert.equal(result.stderr.includes(privateValue),false);
 assert.deepEqual(JSON.parse(result.stderr),{status:'failed',code});
}
test('CLI writes only review outputs and rejects duplicate options without leaking inputs',async t=>{
 const f=await fixture(t);const before=await snapshot(f.base);
 const script=fileURLToPath(new URL('./review.mjs',import.meta.url));
 const args=[script,'--candidate',f.root,'--published',f.published,'--source-root',f.repo,'--out',f.out];
 const result=execFileSync(process.execPath,args,{encoding:'utf8'});
 assert.deepEqual(JSON.parse(result),{status:'unchanged',sourceChanged:false,changedFiles:[]});
 const after=await snapshot(f.base);delete after.review;assert.deepEqual(after,before);
 await refusesCLI(f,'invalid-arguments',['--out','credential-canary']);
});
test('review rejects private published data rather than placing it in a deletion patch',async t=>{
 const f=await fixture(t);const path=join(f.published,'manifest.json');
 const manifest=JSON.parse(await readFile(path));manifest.note='credential-canary';await writeFile(path,JSON.stringify(manifest));
 await refusesCLI(f,'private-data');await assert.rejects(readFile(join(f.out,'refresh.patch')));
});
test('revision-only and unrelated documentation commits need no replacement',async t=>{
 const f=await fixture(t);
 await writeFile(join(f.repo,'docs/guide.md'),'unrelated input');
 f.git('add','docs/guide.md');
 f.git('-c','user.name=Test','-c','user.email=test@example.invalid','commit','-qm','docs only');
 f.manifest.sourceRevision=f.git('rev-parse','HEAD').trim();await f.save();
 const before=await snapshot(f.base);
 assert.deepEqual(await review(f),{status:'unchanged',sourceChanged:false,changedFiles:[]});
 assert.equal((await readFile(join(f.out,'refresh.patch'))).length,0);
 const after=await snapshot(f.base);delete after.review;assert.deepEqual(after,before);
 assert.deepEqual(JSON.parse(await readFile(join(f.out,'summary.json'))),{status:'unchanged',sourceChanged:false,changedFiles:[]});
 assert.deepEqual((await readdir(f.out)).sort(),['refresh.patch','review.md','summary.json']);
});
for(const hostile of [false,true]) for(const change of ['source','text','image','evidence']) test(`review and exact binary patch roundtrip: ${change}${hostile?' with hostile Git environment':''}`,async t=>{
 const f=await fixture(t);const row=f.manifest.rows[0];
 if(change==='source') {
  await writeFile(join(f.repo,'go.mod'),'module changed\n');
  const bytes=await readFile(join(f.repo,'go.mod'));
  f.manifest.sourceTreeDigest=createHash('sha256').update(JSON.stringify(['go.mod',bytes.length])).update(bytes).digest('hex');
 } else if(change==='text') {
  const bytes=Buffer.from('independent input\n');await writeFile(join(f.root,row.text.file),bytes);row.text.sha256=hash(bytes);
 } else if(change==='image') {
  // Independently encoded valid PNG, different deflate bytes from the old image.
  const {deflateSync,inflateSync}=await import('node:zlib');
  const bytes=independentPNG(1200,1800);const old=bytes.readUInt32BE(33);
  const raw=inflateSync(bytes.subarray(41,41+old));raw[1]=127;
  const changed=Buffer.concat([bytes.subarray(0,33),chunk('IDAT',deflateSync(raw)),bytes.subarray(45+old)]);
  await writeFile(join(f.root,row.file),changed);row.sha256=hash(changed);
 } else row.assertions.push({name:'second-independent-oracle',passed:true});
 await f.save();
 // Valid JSON whitespace must roundtrip even when caller Git rejects it.
 if(hostile)await writeFile(join(f.root,'manifest.json'),Buffer.concat([await readFile(join(f.root,'manifest.json')),Buffer.from(' \n')]));
 const before=await snapshot(f.base);const summary=await review(f);
 const expected=['docs/assets/tutorials/manifest.json'];
 if(change==='text')expected.push('docs/assets/tutorials/'+row.text.file);
 if(change==='image')expected.push('docs/assets/tutorials/'+row.file);
 assert.deepEqual(summary,{status:'review-required',sourceChanged:change==='source',changedFiles:expected.sort()});
 const after=await snapshot(f.base);delete after.review;assert.deepEqual(after,before);
 const disposable=join(f.base,'disposable');await mkdir(disposable);
 await cp(f.published,join(disposable,'docs/assets/tutorials'),{recursive:true});
 const ambient=process.env;let caller,callerBefore;
 try {
  if(hostile) {
   caller=join(f.base,'caller');await mkdir(caller);
   const template=join(f.base,'caller-template');await mkdir(template);
   execFileSync('git',['init','-q',`--template=${template}`],{cwd:caller,env:gitEnvironment(caller)});
   await cp(f.published,join(caller,'docs/assets/tutorials'),{recursive:true});
   const config=join(caller,'ambient.config');await writeFile(config,'[apply]\n\twhitespace = error\n');
   callerBefore=await snapshot(caller);
   // Process-wide only during these synchronous Git calls, restored on failure.
   // Every hostile target is owned, even when the isolation below regresses.
   process.env={...ambient,GIT_DIR:join(caller,'.git'),GIT_WORK_TREE:caller,
    GIT_CONFIG_GLOBAL:config,GIT_CONFIG_SYSTEM:config,GIT_CONFIG_COUNT:'1',
    GIT_CONFIG_KEY_0:'core.worktree',GIT_CONFIG_VALUE_0:caller};
   assert.equal(execFileSync('git',['rev-parse','--show-toplevel'],{cwd:disposable,encoding:'utf8'}).trim(),caller);
  }
  // Real Git applies the binary patch to an owned copy, never the inputs.
  execFileSync('git',['apply','--check',join(f.out,'refresh.patch')],{cwd:disposable,env:gitEnvironment(disposable),stdio:'pipe'});
  execFileSync('git',['apply',join(f.out,'refresh.patch')],{cwd:disposable,env:gitEnvironment(disposable),stdio:'pipe'});
 } finally {
  process.env=ambient;
  if(hostile)assert.deepEqual(await snapshot(caller),callerBefore);
 }
 assert.deepEqual(await snapshot(join(disposable,'docs/assets/tutorials')),await snapshot(f.root));
});
for(const mode of ['existing','source-child','candidate-child','published-child','ancestor','relative','symlink-output','symlink-candidate','symlink-published','symlink-source']) test(`review refuses unsafe paths: ${mode}`,async t=>{
 const f=await fixture(t);
 if(mode==='existing'){await mkdir(f.out);await writeFile(join(f.out,'keep'),'sentinel');}
 if(mode==='source-child')f.out=join(f.repo,'review');
 if(mode==='candidate-child')f.out=join(f.root,'review');
 if(mode==='published-child')f.out=join(f.published,'review');
 if(mode==='ancestor')f.out=f.base;
 if(mode==='relative')f.out='relative-review';
 if(mode.startsWith('symlink-')) {
  const key=({'symlink-output':'out','symlink-candidate':'root','symlink-published':'published','symlink-source':'repo'})[mode];
  const link=join(f.base,'alias');await symlink(key==='out'?f.base:f[key],link,'dir');f[key]=key==='out'?join(link,'review'):link;
 }
 await refusesCLI(f,mode==='existing'?'output-unavailable':'unsafe-path');
 if(mode==='existing')assert.equal(await readFile(join(f.out,'keep'),'utf8'),'sentinel');
});
for(const mode of ['revision','source','failed','incomplete','malformed','private-text','private-manifest','extra','traversal','image-symlink']) test(`review rejects invalid candidate before output: ${mode}`,async t=>{
 const f=await fixture(t);const row=f.manifest.rows[0];
 if(mode==='revision')f.git('-c','user.name=Test','-c','user.email=test@example.invalid','commit','--allow-empty','-qm','later');
 if(mode==='source')await writeFile(join(f.repo,'go.mod'),'module drift\n');
 if(mode==='failed')row.assertions[0].passed=false;
 if(mode==='incomplete')f.manifest.complete=false;
 if(mode==='malformed'){await writeFile(join(f.root,row.file),'invalid');row.sha256=hash('invalid');}
 if(mode==='private-text'){await writeFile(join(f.root,row.text.file),'credential-canary');row.text.sha256=hash('credential-canary');}
 if(mode==='private-manifest')f.manifest.note='/Users/private/path';
 if(mode==='extra')await writeFile(join(f.root,'raw.log'),'private');
 if(mode==='traversal')row.file='../escape.png';
 if(mode==='image-symlink'){await rm(join(f.root,row.file));await symlink(join(f.published,row.file),join(f.root,row.file));}
 await f.save();
 const code=['revision','source'].includes(mode)?'stale-source':mode.startsWith('private-')?'private-data':'invalid-candidate';
 await refusesCLI(f,code);await assert.rejects(readFile(join(f.out,'refresh.patch')));
});
test('review distinguishes invalid published evidence',async t=>{
 const f=await fixture(t);
 await writeFile(join(f.published,'manifest.json'),'{invalid');
 await refusesCLI(f,'invalid-published');
});
test('review contains Git diagnostics from an invalid source root',async t=>{
 const f=await fixture(t);
 f.repo=join(f.base,'not-a-repository-canary');await mkdir(f.repo);
 await refusesCLI(f,'invalid-candidate');
});
